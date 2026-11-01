package artifact

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "snippets.json")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

const valid = `[
 {"label": "End-of-session ritual", "body": "reflect on work done",
  "search_terms": ["handoff", "session"], "category": "prompt"},
 {"label": "typo fix: dashboard", "body": "dashboard", "search_terms": [], "category": "typo"},
 {"label": "Paste from clipboard", "body": "{{clip}}",
  "search_terms": [], "category": "clipboard"}
]`

func TestLoadValid(t *testing.T) {
	got, err := Load(write(t, valid))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	s := got[0]
	if s.Label != "End-of-session ritual" || s.Body != "reflect on work done" ||
		s.Category != "prompt" || len(s.SearchTerms) != 2 {
		t.Fatalf("row 0 mismatched: %+v", s)
	}
	// A missing search_terms array decodes as empty, not an error.
	loose, err := Load(write(t, `[{"label": "a", "body": "x"}]`))
	if err != nil || len(loose) != 1 || len(loose[0].SearchTerms) != 0 {
		t.Fatalf("missing optional fields must decode as empty: %v %+v", err, loose)
	}
}

// 🔴 FAIL LOUD AND NAME THE FILE — a broken artifact must say WHICH file is
// broken, because every consumer of the tool is otherwise unusable with
// nothing pointing at the cause.
func TestValidationFailsLoudNamingTheFile(t *testing.T) {
	cases := []struct {
		name    string
		content string
		wantIn  string
	}{
		{"empty label", `[{"label": "", "body": "x"}]`, "empty label"},
		{"duplicate labels", `[{"label": "a", "body": "x"}, {"label": "a", "body": "y"}]`, `duplicate label "a"`},
		{"not an array", `{"label": "a"}`, "not a valid snippets.json array"},
		{"truncated json", `[{"label": "a"`, "not a valid snippets.json array"},
	}
	for _, c := range cases {
		path := write(t, c.content)
		_, err := Load(path)
		if err == nil {
			t.Errorf("%s: Load succeeded, want failure", c.name)
			continue
		}
		if !strings.Contains(err.Error(), c.wantIn) {
			t.Errorf("%s: error %q does not name the cause %q", c.name, err, c.wantIn)
		}
		// The FILE name must be in the message — that is the whole point.
		if !strings.Contains(err.Error(), path) {
			t.Errorf("%s: error %q does not name the file %q", c.name, err, path)
		}
	}
}

// A MISSING file is its own loud failure (the operator switched to a config
// that no longer writes the artifact, or runs snip on a machine that never
// had it — both must say so).
func TestMissingFileNamesTheFile(t *testing.T) {
	_, err := Load("/nonexistent/snip/snippets.json")
	if err == nil || !strings.Contains(err.Error(), "/nonexistent/snip/snippets.json") {
		t.Fatalf("missing-file error = %v, want one naming the path", err)
	}
}

func TestFindIsExact(t *testing.T) {
	snippets, err := Load(write(t, valid))
	if err != nil {
		t.Fatal(err)
	}
	if s, ok := Find(snippets, "Paste from clipboard"); !ok || s.Body != "{{clip}}" {
		t.Fatalf("Find(Paste from clipboard) = %+v %v", s, ok)
	}
	// Label matching is EXACT — no substring, no case-folding.
	if _, ok := Find(snippets, "Paste from clipboar"); ok {
		t.Fatal("Find must be exact; a substring is not the label")
	}
	if _, ok := Find(snippets, "paste from clipboard"); ok {
		t.Fatal("Find must be exact; the label is case-sensitive")
	}
}

func TestPathResolution(t *testing.T) {
	t.Setenv("SNIP_CONFIG", "/override/path.json")
	if p, _ := Path(); p != "/override/path.json" {
		t.Fatalf("SNIP_CONFIG override ignored: %s", p)
	}
	os.Unsetenv("SNIP_CONFIG")
	t.Setenv("XDG_CONFIG_HOME", "/xdg/cfg")
	if p, _ := Path(); p != "/xdg/cfg/snip/snippets.json" {
		t.Fatalf("XDG_CONFIG_HOME resolution wrong: %s", p)
	}
}
