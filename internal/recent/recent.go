// Package recent remembers WHICH snippets the operator actually uses, so the
// picker can open on the rows he reaches for instead of on the order the nix
// list happens to be written in.
//
// 🔴 IT ANSWERS "WHAT DID I USE LAST", NOT "WHAT DID I TRIGGER". The trigger
// shorthand was dropped outright on 2026-10-31 and identity moved to the label,
// so a fire is a PASTE and the label is the only key there is. A reworded label
// therefore loses its recency and falls back to artifact order — the same
// accepted cost the artifact's fire history pays, so the two agree rather than
// disagreeing quietly.
//
// 🔴 BEST-EFFORT IN BOTH DIRECTIONS, AND IT IS NOT LAZINESS.
//   - A missing, unreadable or malformed file means "nothing known yet" and the
//     browse order degrades to artifact order. A picker that REFUSED to open
//     because its own convenience cache was unreadable would be trading the
//     tool for its bookkeeping.
//   - A failed Touch must not fail the paste. The paste has already landed by
//     the time this is called; turning a successful paste into an error for it
//     would be the tail wagging the dog.
//
// The file is a flat {"label": unix-millis} map, written atomically and bounded
// to maxEntries, so it stays small enough to read on the hotkey path.
package recent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/ZacxDev/snip/internal/artifact"
)

// maxEntries bounds the file. A snippet catalog is ~30 rows and the operator
// retires rows rather than accumulating them, so this is generous by an order
// of magnitude — it exists so a renamed-label churn cannot grow the file
// without bound, not to trim a real working set.
const maxEntries = 200

// Path returns the recency file: $SNIP_RECENT (tests, and the e2e's hermetic
// run), else $XDG_STATE_HOME/snip/recent.json, else
// ~/.local/state/snip/recent.json.
//
// STATE, not CACHE, and the distinction is deliberate: this is worth keeping
// across a reboot, and it is not reproducible from anything else.
func Path() (string, error) {
	if p := os.Getenv("SNIP_RECENT"); p != "" {
		return p, nil
	}
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		return filepath.Join(xdg, "snip", "recent.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("snip: resolve home for the recency file: %w", err)
	}
	return filepath.Join(home, ".local", "state", "snip", "recent.json"), nil
}

// Load reads the label→last-used map. Every failure mode returns an EMPTY map
// rather than an error: this is a ranking hint, and "I know nothing" is a
// coherent answer that the caller already handles (artifact order).
//
// Values that are not positive are dropped: a zero timestamp would sort as
// "used at the epoch" and put a never-fired row at the BOTTOM of a descending
// order, which is the same place it belongs but for the wrong reason — and a
// NEGATIVE one would sort above every real use.
func Load(path string) map[string]int64 {
	raw, err := os.ReadFile(path)
	if err != nil {
		return map[string]int64{}
	}
	var m map[string]int64
	if err := json.Unmarshal(raw, &m); err != nil {
		return map[string]int64{}
	}
	out := make(map[string]int64, len(m))
	for label, at := range m {
		if label != "" && at > 0 {
			out[label] = at
		}
	}
	return out
}

// Touch records that `label` was used at `at`, and prunes the file to the
// newest maxEntries labels.
func Touch(path, label string, at time.Time) error {
	if label == "" {
		return nil
	}
	m := Load(path)
	m[label] = at.UnixMilli()
	prune(m, maxEntries)

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	blob, err := json.Marshal(m)
	if err != nil {
		return err
	}
	// 🔴 WRITE, THEN RENAME. A half-written recency file is a file that fails to
	// parse, which Load reads as "nothing known yet" — so the failure mode is a
	// silent loss of the whole ranking, not a crash, which is exactly the kind
	// of damage that would go unnoticed.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".recent-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(blob); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Chmod(name, 0o644); err != nil {
		os.Remove(name)
		return err
	}
	return os.Rename(name, path)
}

// prune keeps the newest n entries.
func prune(m map[string]int64, n int) {
	if len(m) <= n {
		return
	}
	type entry struct {
		label string
		at    int64
	}
	all := make([]entry, 0, len(m))
	for label, at := range m {
		all = append(all, entry{label, at})
	}
	// Newest first; the label breaks ties so the pruning is deterministic
	// rather than dependent on map iteration order.
	sort.Slice(all, func(i, j int) bool {
		if all[i].at != all[j].at {
			return all[i].at > all[j].at
		}
		return all[i].label < all[j].label
	})
	for _, e := range all[n:] {
		delete(m, e.label)
	}
}

// Order returns the catalog in BROWSE order: the most recently used label
// first, then the next, and the never-used rows last IN ARTIFACT ORDER.
//
// A copy is returned; the caller's slice is never reordered in place, so a
// caller that also needs artifact order (snip list) keeps it.
func Order(snippets []artifact.Snippet, times map[string]int64) []artifact.Snippet {
	out := make([]artifact.Snippet, len(snippets))
	copy(out, snippets)

	// Artifact position, for the tie-break and for the never-used tail. A
	// duplicate label keeps its FIRST position — the artifact loader refuses
	// duplicates in the real catalog, so this only has to be deterministic.
	pos := make(map[string]int, len(snippets))
	for i, s := range snippets {
		if _, seen := pos[s.Label]; !seen {
			pos[s.Label] = i
		}
	}

	sort.SliceStable(out, func(i, j int) bool {
		ti, tj := times[out[i].Label], times[out[j].Label]
		switch {
		case ti > 0 && tj > 0:
			if ti != tj {
				return ti > tj
			}
		case ti > 0:
			return true
		case tj > 0:
			return false
		}
		return pos[out[i].Label] < pos[out[j].Label]
	})
	return out
}
