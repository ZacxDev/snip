// Package artifact loads the snip catalog: ~/.config/snip/snippets.json, or
// wherever $SNIP_CONFIG points. The catalog is a plain JSON array, typically
// generated from a declarative source (nix/home-manager) at deploy time. It is
// read AT PROCESS START, EVERY invocation — no daemon, no watching: a config
// change is picked up by the next fire.
package artifact

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Snippet is one row of the catalog — the exact JSON shape:
// {label, body, search_terms, category}. The LABEL is the row's identity (a
// reword therefore splits that snippet's use history — accepted on purpose).
// Body is the expansion text; the only variable it may carry is {{clip}}
// (resolved at expand time from the X clipboard — no other var exists by
// design).
type Snippet struct {
	Label       string   `json:"label"`
	Body        string   `json:"body"`
	SearchTerms []string `json:"search_terms"`
	Category    string   `json:"category"`
}

// Path returns the artifact location: $SNIP_CONFIG overrides (tests), else
// $XDG_CONFIG_HOME/snip/snippets.json, else ~/.config/snip/snippets.json —
// the same XDG resolution home-manager's xdg.configFile writes to.
func Path() (string, error) {
	if p := os.Getenv("SNIP_CONFIG"); p != "" {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("snip: resolve home for the artifact path: %w", err)
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "snip", "snippets.json"), nil
	}
	return filepath.Join(home, ".config", "snip", "snippets.json"), nil
}

// Load reads + validates the artifact. Validation is LOUD and names the file:
// a broken artifact must say which file is broken, because the tool is
// otherwise unusable (the picker would list nothing and every paste would
// fail) — silent-empty is the failure mode that looks like "the tool is dead".
//
//   - the file must parse as a JSON array;
//   - every label must be non-empty;
//   - labels must be UNIQUE (a duplicate is an editing accident in the nix
//     list, and the label is now the identity the matcher/telemetry key on).
func Load(path string) ([]Snippet, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("snip: %s: %w", path, err)
	}
	var out []Snippet
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("snip: %s: not a valid snippets.json array: %v", path, err)
	}
	seen := make(map[string]struct{}, len(out))
	for i, s := range out {
		if s.Label == "" {
			return nil, fmt.Errorf("snip: %s: entry %d has an empty label", path, i)
		}
		if _, dup := seen[s.Label]; dup {
			return nil, fmt.Errorf("snip: %s: duplicate label %q", path, s.Label)
		}
		seen[s.Label] = struct{}{}
	}
	return out, nil
}

// Find returns the snippet with exactly this label, and whether it was found.
// Label matching is EXACT (byte equality) — the label is the row's identity,
// passed verbatim from the picker/CLI, never guessed.
func Find(snippets []Snippet, label string) (Snippet, bool) {
	for _, s := range snippets {
		if s.Label == label {
			return s, true
		}
	}
	return Snippet{}, false
}
