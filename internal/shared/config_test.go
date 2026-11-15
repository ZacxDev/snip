package shared

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 🔴 THESE ARE INVARIANT GUARDS, NOT REGRESSION COVERAGE. This package is new
// in the same commit, so there is no pre-change code for them to be red
// against — they pin the contract the picker's failure rendering depends on
// (a named reason, never a silent empty), and each was watched to fail against
// a deliberately broken copy of the function it guards. They are not evidence
// that any of this behaviour ever regressed.
//
// 🔴 NO TEST HERE OPENS THE OPERATOR'S LIVE CONFIG. Every case points
// $SNIP_SHARED_ENV at a file this test wrote, and no fixture carries a real
// endpoint or a real token — the values are configuration, and this repo is
// public.

// writeEnv writes a config file and points EnvPath at it.
func writeEnv(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "shared.env")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SNIP_SHARED_ENV", path)
	return path
}

// The file's two keys are read, comments and blanks ignored, `export` and
// quotes tolerated.
func TestLoadReadsTheTwoKeys(t *testing.T) {
	writeEnv(t, "# the shared clipboard\n\n"+
		"export SNIP_SHARED_URL=\"http://127.0.0.1:1/\"\n"+
		"SNIP_SHARED_TOKEN='abcdefghijkl'\n"+
		"SOMETHING_ELSE=ignored\n")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.URL != "http://127.0.0.1:1/" {
		t.Errorf("URL = %q", cfg.URL)
	}
	if cfg.Token != "abcdefghijkl" {
		t.Errorf("token = %q", cfg.Token)
	}
}

// 🔴 A MISSING FILE IS A NAMED FAILURE THAT CARRIES THE REMEDY — not a zero
// value the picker would then render as an empty clipboard. The message must
// name the path AND both keys, because that is the whole of what the operator
// has to do next.
func TestAMissingFileNamesThePathAndBothKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent", "shared.env")
	t.Setenv("SNIP_SHARED_ENV", path)
	_, err := Load()
	if err == nil {
		t.Fatal("a missing config file must be an error, not a zero config")
	}
	for _, want := range []string{path, KeyURL, KeyToken, "0600"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the message %q does not name %q", err, want)
		}
	}
}

// Validate's three refusals, each with the file named. The expected strings
// are LITERAL, not derived from the implementation: a test that asks the code
// what it does cannot notice it doing the wrong thing.
func TestValidateRefuses(t *testing.T) {
	const path = "/some/where/shared.env"
	cases := []struct {
		name string
		cfg  Config
		want string
	}{
		{"empty url", Config{Token: "abcdefghijkl"}, "SNIP_SHARED_URL is empty"},
		{"not http", Config{URL: "ftp://x", Token: "abcdefghijkl"}, "SNIP_SHARED_URL must be an http(s) URL"},
		{"short token", Config{URL: "http://x", Token: "short"}, "SNIP_SHARED_TOKEN is shorter than 8 characters"},
		{"empty token", Config{URL: "http://x"}, "SNIP_SHARED_TOKEN is shorter than 8 characters"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.cfg.Validate(path)
			if err == nil {
				t.Fatalf("%+v must be refused", c.cfg)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("message = %q, want it to contain %q", err, c.want)
			}
			if !strings.Contains(err.Error(), path) {
				t.Errorf("message = %q, want it to name the file %q", err, path)
			}
		})
	}
	// POSITIVE CONTROL: a config that satisfies all three passes, so the
	// refusals above are about the values and not about Validate always
	// failing.
	if err := (Config{URL: "https://example.invalid", Token: "abcdefghijkl"}).Validate(path); err != nil {
		t.Fatalf("a valid config was refused: %v", err)
	}
}

// 🔴 THERE IS NO COMPILED-IN DEFAULT ENDPOINT OR TOKEN, AND THIS GUARD IS THE
// REASON THE REPO CAN BE PUBLIC. With no config file and no override, Load
// must FAIL — never fall back to a built-in address.
//
// It pins the STATE (Load errors, and the returned config is zero), not a
// spelling, so it cannot be walked past by renaming a constant.
func TestThereIsNoBuiltInEndpoint(t *testing.T) {
	// Point the resolver at a directory that holds no config: XDG_CONFIG_HOME
	// is honoured ahead of $HOME, so this covers the default path too.
	t.Setenv("SNIP_SHARED_ENV", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg, err := Load()
	if err == nil {
		t.Fatalf("Load succeeded with no config file — it returned %+v, which means "+
			"an endpoint or token is compiled in", cfg)
	}
	if cfg.URL != "" || cfg.Token != "" {
		t.Fatalf("a failed Load handed back %+v, want the zero config", cfg)
	}
}

// EnvPath's precedence: the override wins, then XDG, then ~/.config.
func TestEnvPathPrecedence(t *testing.T) {
	t.Setenv("SNIP_SHARED_ENV", "/override/shared.env")
	t.Setenv("XDG_CONFIG_HOME", "/xdg")
	if got, _ := EnvPath(); got != "/override/shared.env" {
		t.Errorf("override ignored: %q", got)
	}
	t.Setenv("SNIP_SHARED_ENV", "")
	if got, _ := EnvPath(); got != "/xdg/snip/shared.env" {
		t.Errorf("XDG ignored: %q", got)
	}
	t.Setenv("XDG_CONFIG_HOME", "")
	got, err := EnvPath()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(got, "/.config/snip/shared.env") {
		t.Errorf("home default = %q, want it to end in /.config/snip/shared.env", got)
	}
}
