package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ZacxDev/snip/internal/artifact"
	"github.com/ZacxDev/snip/internal/recent"
)

// The version is the value the release tooling stamps back via -X — the two
// must never diverge, so the declaration's SHAPE is pinned here (exactly one
// `var buildVersion = "…"` line).
func TestVersionDeclarationShape(t *testing.T) {
	raw, err := os.ReadFile("version.go")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(raw), "\n")
	matches := 0
	var value string
	for _, l := range lines {
		// Mirror the nix pattern: var buildVersion = "..." exactly once.
		if rest, ok := strings.CutPrefix(l, `var buildVersion = "`); ok {
			if end, ok := strings.CutSuffix(rest, `"`); ok && !strings.Contains(end, `"`) {
				matches++
				value = end
			}
		}
	}
	if matches != 1 {
		t.Fatalf("version.go carries %d buildVersion declarations, want exactly 1", matches)
	}
	if value == "" {
		t.Fatal("buildVersion is empty - the binary would report a blank version")
	}
}

// usage goes to stderr, not stdout (a scripted caller must not read help as
// output).
func TestUsageOnStderr(t *testing.T) {
	r, w, _ := os.Pipe()
	old := os.Stderr
	os.Stderr = w
	usage()
	w.Close()
	os.Stderr = old
	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	out := string(buf[:n])
	for _, want := range []string{"snip picker", "snip paste", "--label", "--body-file", "snip search", "ctrl+e"} {
		if !strings.Contains(out, want) {
			t.Errorf("usage missing %q", want)
		}
	}
}

// 🔴 `--body-file` IS THE EDIT PATH'S ONLY WAY TO PASTE FRESH TEXT. The artifact
// is written by home-manager, so right after a ctrl+e edit it still holds the
// OLD body for that label; the picker hands over the operator's text instead.
// An override that silently missed would paste the stale body, so the not-found
// case must be reported, not swallowed.
func TestOverrideBody(t *testing.T) {
	cat := []artifact.Snippet{
		{Label: "alpha", Body: "OLD-A"},
		{Label: "beta", Body: "OLD-B"},
	}
	got, ok := overrideBody(cat, "alpha", []byte("NEW-A"))
	if !ok {
		t.Fatal("alpha must be found")
	}
	if got[0].Body != "NEW-A" {
		t.Fatalf("alpha body = %q, want NEW-A", got[0].Body)
	}
	// The other rows are untouched, and so is the caller's slice.
	if got[1].Body != "OLD-B" || cat[0].Body != "OLD-A" {
		t.Fatalf("override leaked: got=%v original=%v", got, cat)
	}
	if _, ok := overrideBody(cat, "nope", []byte("X")); ok {
		t.Fatal("an unknown label must report not-found (it would paste the stale body)")
	}
}

// The list/search/get read paths honour SNIP_CONFIG (the test override) and
// the artifact loader's loud failure names the file.
func TestListReadsTheArtifact(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "snippets.json")
	content := `[{"label": "alpha", "body": "A", "search_terms": ["x"], "category": "prompt"}]`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SNIP_CONFIG", path)
	if rc := runList([]string{}); rc != 0 {
		t.Fatalf("list exit = %d", rc)
	}
	if rc := runSearch([]string{"x"}); rc != 0 {
		t.Fatalf("search exit = %d", rc)
	}
	if rc := runGet([]string{"alpha"}); rc != 0 {
		t.Fatalf("get exit = %d", rc)
	}
	if rc := runGet([]string{"no-such-label"}); rc != 1 {
		t.Fatalf("get(missing) exit = %d, want 1", rc)
	}
	if rc := runSearch([]string{}); rc != 2 {
		t.Fatalf("search(no args) exit = %d, want 2 (usage)", rc)
	}
}

func labelsOf(ss []artifact.Snippet) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = s.Label
	}
	return out
}

// 🔴 THE BROWSE ORDER COMES FROM THE RECENCY FILE, AND ITS ABSENCE IS NOT AN
// ERROR. Both halves are here on purpose: with nothing recorded the order is the
// ARTIFACT order (so a missing or unreadable file cannot silently empty or
// reshuffle the picker), and one recorded label moves that row to the front
// while the rest keep artifact order. Either half alone would be satisfied by a
// function that ignored the file, or by one that reordered everything.
func TestOrderedForBrowseUsesTheRecencyFile(t *testing.T) {
	rec := filepath.Join(t.TempDir(), "recent.json")
	t.Setenv("SNIP_RECENT", rec)

	cat := []artifact.Snippet{{Label: "first"}, {Label: "second"}, {Label: "third"}}
	if got := labelsOf(orderedForBrowse(cat)); got[0] != "first" {
		t.Fatalf("with nothing recorded the order must be artifact order, got %v", got)
	}
	if err := os.WriteFile(rec, []byte(`{"third": 9}`), 0o644); err != nil {
		t.Fatal(err)
	}
	got := labelsOf(orderedForBrowse(cat))
	if len(got) != 3 || got[0] != "third" || got[1] != "first" || got[2] != "second" {
		t.Fatalf("got %v, want [third first second]", got)
	}
	// An unreadable file is the same as no file, not a failure.
	t.Setenv("SNIP_RECENT", filepath.Join(t.TempDir(), "absent.json"))
	if got := labelsOf(orderedForBrowse(cat)); got[0] != "first" {
		t.Fatalf("an absent file must degrade to artifact order, got %v", got)
	}
}

// The recency file is STATE under XDG, not a cache under the config dir, and it
// is NOT the artifact: SNIP_CONFIG must not steer it.
func TestTheRecencyPathIsIndependentOfTheArtifact(t *testing.T) {
	t.Setenv("SNIP_CONFIG", "/tmp/some/artifact.json")
	t.Setenv("SNIP_RECENT", "")
	t.Setenv("XDG_STATE_HOME", "/tmp/xdg-state")
	got, err := recent.Path()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join("/tmp/xdg-state", "snip", "recent.json"); got != want {
		t.Fatalf("Path = %q, want %q", got, want)
	}
}

// 🔴 A USE IS REMEMBERED WITH THE SAME CLOCK AND KEY THE ORDER IS READ BY. This
// is the round trip ACROSS the two packages: a units mismatch (seconds vs
// millis) or a key mismatch would show up here rather than as a silently
// unchanged browse order on the desktop.
func TestATouchThenOrderRoundTrips(t *testing.T) {
	rec := filepath.Join(t.TempDir(), "recent.json")
	cat := []artifact.Snippet{{Label: "alpha"}, {Label: "beta"}}
	if err := recent.Touch(rec, "beta", time.Now()); err != nil {
		t.Fatal(err)
	}
	ordered := recent.Order(cat, recent.Load(rec))
	if len(ordered) != 2 || ordered[0].Label != "beta" {
		t.Fatalf("got %v, want beta first", labelsOf(ordered))
	}
}
