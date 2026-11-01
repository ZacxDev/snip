package recent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ZacxDev/snip/internal/artifact"
)

func cat() []artifact.Snippet {
	return []artifact.Snippet{
		{Label: "never-a"},
		{Label: "used-old"},
		{Label: "never-b"},
		{Label: "used-new"},
		{Label: "used-mid"},
	}
}

func labels(ss []artifact.Snippet) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = s.Label
	}
	return out
}

func eq(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

// 🔴 THE PAIR THAT MAKES THE ORDERING TEST A MEASUREMENT. Order() on an empty
// map must be the IDENTITY (artifact order), and adding one timestamp must MOVE
// that row to the front. Without both, a comparator that returned a constant
// would pass either half alone.
func TestOrderIsArtifactOrderWithNothingKnownAndMovesOnOneUse(t *testing.T) {
	in := cat()
	eq(t, labels(Order(in, map[string]int64{})), labels(in))

	got := labels(Order(in, map[string]int64{"used-old": 100}))
	if got[0] != "used-old" {
		t.Fatalf("a used row must lead: %v", got)
	}
	// …and everything else keeps artifact order.
	eq(t, got, []string{"used-old", "never-a", "never-b", "used-new", "used-mid"})
}

func TestOrderMostRecentFirstThenArtifactOrder(t *testing.T) {
	in := cat()
	times := map[string]int64{"used-old": 100, "used-mid": 200, "used-new": 300}
	eq(t, labels(Order(in, times)), []string{"used-new", "used-mid", "used-old", "never-a", "never-b"})
}

func TestOrderTiesFallBackToArtifactOrder(t *testing.T) {
	in := cat()
	times := map[string]int64{"used-new": 500, "used-mid": 500, "used-old": 500}
	eq(t, labels(Order(in, times)), []string{"used-old", "used-new", "used-mid", "never-a", "never-b"})
}

// A timestamp for a label the catalog no longer carries (retired row, reworded
// label) must not create a row or shift anything.
func TestOrderIgnoresTimesForAbsentLabels(t *testing.T) {
	in := cat()
	times := map[string]int64{"retired-since": 9_999_999_999, "used-mid": 5}
	eq(t, labels(Order(in, times)), []string{"used-mid", "never-a", "used-old", "never-b", "used-new"})
}

// 🔴 The caller's slice is what `snip list` prints, so Order must not reorder it
// in place.
func TestOrderDoesNotMutateTheInput(t *testing.T) {
	in := cat()
	before := labels(in)
	_ = Order(in, map[string]int64{"used-new": 300})
	eq(t, labels(in), before)
}

func TestLoadMissingFileIsEmpty(t *testing.T) {
	if m := Load(filepath.Join(t.TempDir(), "nope.json")); len(m) != 0 {
		t.Fatalf("want an empty map, got %v", m)
	}
}

func TestLoadMalformedIsEmpty(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"not-json.json":  `{"a": `,
		"not-a-map.json": `["a", "b"]`,
		"empty.json":     ``,
	} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if m := Load(p); len(m) != 0 {
			t.Fatalf("%s: want an empty map, got %v", name, m)
		}
	}
}

// A zero or negative timestamp would sort a real row somewhere arbitrary, so
// neither is allowed in.
func TestLoadDropsNonPositiveValuesAndEmptyLabels(t *testing.T) {
	p := filepath.Join(t.TempDir(), "recent.json")
	body := `{"good": 5, "zero": 0, "negative": -1, "": 7}`
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	m := Load(p)
	if len(m) != 1 || m["good"] != 5 {
		t.Fatalf("got %v, want only {good:5}", m)
	}
}

func TestTouchCreatesTheDirectoryAndRecordsTheUse(t *testing.T) {
	p := filepath.Join(t.TempDir(), "nested", "state", "snip", "recent.json")
	at := time.UnixMilli(1_700_000_000_000)
	if err := Touch(p, "alpha", at); err != nil {
		t.Fatalf("Touch: %v", err)
	}
	m := Load(p)
	if m["alpha"] != at.UnixMilli() {
		t.Fatalf("got %v, want alpha at %d", m, at.UnixMilli())
	}
	// Nothing partial left behind (the write is temp-then-rename).
	entries, err := os.ReadDir(filepath.Dir(p))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "recent.json" {
		t.Fatalf("leftover files beside the state file: %v", entries)
	}
}

func TestTouchEmptyLabelIsANoOp(t *testing.T) {
	p := filepath.Join(t.TempDir(), "recent.json")
	if err := Touch(p, "", time.Now()); err != nil {
		t.Fatalf("Touch: %v", err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("an empty label must not create the file (stat err = %v)", err)
	}
}

// The file is bounded, and the bound drops the OLDEST — a cap that dropped the
// newest would keep exactly the rows nobody uses.
func TestTouchPrunesToTheNewestEntries(t *testing.T) {
	p := filepath.Join(t.TempDir(), "recent.json")
	base := time.UnixMilli(1_700_000_000_000)
	for i := 0; i < maxEntries+5; i++ {
		if err := Touch(p, "label-"+string(rune('a'+i%26))+"-"+itoa(i), base.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatalf("Touch %d: %v", i, err)
		}
	}
	m := Load(p)
	if len(m) != maxEntries {
		t.Fatalf("file holds %d entries, want the cap %d", len(m), maxEntries)
	}
	// The first five (oldest) are gone; the last is present.
	for i := 0; i < 5; i++ {
		if _, ok := m["label-"+string(rune('a'+i%26))+"-"+itoa(i)]; ok {
			t.Fatalf("entry %d survived the prune; the cap dropped the wrong end", i)
		}
	}
	if _, ok := m["label-"+string(rune('a'+(maxEntries+4)%26))+"-"+itoa(maxEntries+4)]; !ok {
		t.Fatal("the newest entry was pruned")
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

func TestPathHonoursSNIP_RECENT(t *testing.T) {
	t.Setenv("SNIP_RECENT", "/tmp/explicit-recent.json")
	got, err := Path()
	if err != nil {
		t.Fatal(err)
	}
	if got != "/tmp/explicit-recent.json" {
		t.Fatalf("Path = %q, want the SNIP_RECENT override", got)
	}
}

func TestPathFallsBackUnderXDGStateHome(t *testing.T) {
	t.Setenv("SNIP_RECENT", "")
	t.Setenv("XDG_STATE_HOME", "/tmp/xdg-state")
	got, err := Path()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join("/tmp/xdg-state", "snip", "recent.json"); got != want {
		t.Fatalf("Path = %q, want %q", got, want)
	}
}

// The on-disk shape is a contract with the previous versions of this file, and
// a JSON array would parse as a map error and silently empty the ranking.
func TestTheOnDiskShapeIsAFlatMap(t *testing.T) {
	p := filepath.Join(t.TempDir(), "recent.json")
	if err := Touch(p, "alpha", time.UnixMilli(1234)); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]int64
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("not a flat map: %v", err)
	}
	if m["alpha"] != 1234 {
		t.Fatalf("got %v, want alpha=1234", m)
	}
}
