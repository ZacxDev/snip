package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ZacxDev/snip/internal/artifact"
)

// 🔴 --ephemeral WITHOUT --body-file IS A USAGE ERROR, NOT A PASTE OF NOTHING.
// The body is the only thing an ephemeral paste has; with no file there is
// nothing to paste, and silently pasting an empty string would clear the
// operator's clipboard and type nothing into his window.
func TestEphemeralRequiresABodyFile(t *testing.T) {
	if code := runPaste([]string{"--ephemeral", "--label", "4221"}); code != 2 {
		t.Fatalf("exit code = %d, want 2 (usage)", code)
	}
	// POSITIVE CONTROL: the same invocation WITH a body file gets past the
	// usage check — it then fails for want of an origin window (exit 1), a
	// different code, which proves the 2 above was the usage refusal and not a
	// blanket rejection of --ephemeral.
	f := filepath.Join(t.TempDir(), "body.txt")
	if err := os.WriteFile(f, []byte("from another device"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SNIP_ORIGIN_WINDOW", "")
	if code := runPaste([]string{"--ephemeral", "--label", "4221", "--body-file", f}); code != 1 {
		t.Fatalf("control exit code = %d, want 1 (no origin window) — the control proves nothing", code)
	}
}

// 🔴 THE EPHEMERAL PATH NEVER READS THE CATALOG. A clip is not a catalog row;
// loading the artifact would only create the chance of a collision between a
// clip id and a snippet label, and would make a clip paste fail on a machine
// whose snippets.json is broken for reasons that have nothing to do with the
// clip.
//
// Pinned by pointing SNIP_CONFIG at a file that makes loadOrDie EXIT THE
// PROCESS: if the ephemeral path read the catalog this test would not return
// at all.
func TestEphemeralDoesNotReadTheCatalog(t *testing.T) {
	broken := filepath.Join(t.TempDir(), "snippets.json")
	if err := os.WriteFile(broken, []byte("{ not an array"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SNIP_CONFIG", broken)
	t.Setenv("SNIP_ORIGIN_WINDOW", "")

	// POSITIVE CONTROL that the artifact really is unloadable: the loader
	// refuses it. (Called directly, because routing it through the CATALOG
	// path would os.Exit this test binary.)
	if _, err := artifact.Load(broken); err == nil {
		t.Fatal("the fixture parses after all — this guard would pass vacuously")
	}

	f := filepath.Join(t.TempDir(), "body.txt")
	if err := os.WriteFile(f, []byte("from another device"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Returning at all means the catalog was not loaded; exit 1 is the abort
	// for a missing origin window, which is as far as a test without an X
	// server can go.
	if code := runPaste([]string{"--ephemeral", "--label", "4221", "--body-file", f}); code != 1 {
		t.Fatalf("exit code = %d, want 1 (origin-window abort)", code)
	}
	// The scratch file is CONSUMED: an ephemeral paste, successful or aborted,
	// leaves nothing behind in /tmp.
	if _, err := os.Stat(f); !os.IsNotExist(err) {
		t.Errorf("the body file survived the paste (%v) — it must be removed", err)
	}
}

// 🔴 overrideBody's UNKNOWN-LABEL GUARD IS NOT LOOSENED. A clip is by
// definition absent from the catalog, which is exactly why --ephemeral is its
// own spelling: loosening this to append-when-missing would delete the guard
// that stops the EDIT path pasting the OLD body while the operator believes he
// pasted the new one.
func TestOverrideBodyStillRefusesAnUnknownLabel(t *testing.T) {
	cat := []artifact.Snippet{{Label: "known", Body: "old"}}
	if _, ok := overrideBody(cat, "a clip id, not a label", []byte("new")); ok {
		t.Fatal("overrideBody accepted a label that is not in the catalog")
	}
	// POSITIVE CONTROL: a known label IS overridden, so the refusal above is
	// about the label and not about overrideBody never working.
	out, ok := overrideBody(cat, "known", []byte("new"))
	if !ok || out[0].Body != "new" {
		t.Fatalf("overrideBody(known) = %+v, %v — this control proves nothing", out, ok)
	}
}

// The usage text names the new surfaces, so `snip --help` is a complete
// description of what the binary can do.
func TestUsageNamesTheSharedClipboard(t *testing.T) {
	r, w, _ := os.Pipe()
	old := os.Stderr
	os.Stderr = w
	usage()
	w.Close()
	os.Stderr = old
	buf := make([]byte, 8192)
	n, _ := r.Read(buf)
	out := string(buf[:n])
	for _, want := range []string{"ctrl+g", "--ephemeral", "SNIP_SHARED_ENV", "SNIP_SHARED_TOKEN"} {
		if !strings.Contains(out, want) {
			t.Errorf("usage does not name %q", want)
		}
	}
}
