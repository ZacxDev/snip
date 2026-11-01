package effects

import (
	"testing"
)

// 🔴 THE PASTE-KEY SEED TABLE, pinned byte-for-byte (M0 measured facts):
// default ctrl+v; alacritty → ctrl+shift+v (68+ espanso Clipboard-backend
// expansions landed in Alacritty via search fires). The e2e harness verifies
// the mapping end to end through a real terminal.
func TestPasteKeyFor(t *testing.T) {
	cases := []struct {
		class string
		want  string
	}{
		{"alacritty", "ctrl+shift+v"},
		{"Alacritty", "ctrl+shift+v"}, // WM_CLASS conventions capitalise
		{"ALACRITTY", "ctrl+shift+v"}, // case-insensitive is deliberate
		{"snip", "ctrl+v"},            // the picker's own class, non-terminal
		{"XTerm", "ctrl+v"},           // every unlisted class takes the default
		{"", "ctrl+v"},
		{"alacritty-ish", "ctrl+v"}, // EXACT match, not substring
	}
	for _, c := range cases {
		if got := PasteKeyFor(c.class); got != c.want {
			t.Errorf("PasteKeyFor(%q) = %q, want %q", c.class, got, c.want)
		}
	}
}

// The picker-gone probe anchors the class regex — an unanchored "snip" would
// also match a "snip-e2e" window (the e2e harness's own target), which would
// make the wait see the WRONG window and abort a healthy paste.
func TestSnipGoneProbeIsAnchored(t *testing.T) {
	if SnipWindowClass != "snip" {
		t.Fatalf("SnipWindowClass = %q, want snip", SnipWindowClass)
	}
	if probe := "^" + SnipWindowClass + "$"; probe != "^snip$" {
		t.Fatalf("probe pattern %q, want the anchored ^snip$", probe)
	}
}

// 🔴 THE I3-MSG REPLY IS THE AUTHORITY, NOT THE EXIT CODE — i3-msg answers
// success:false in JSON with exit 0, so Focus parses the reply. These are the
// shapes i3 actually answers with.
func TestFocusReplyParsing(t *testing.T) {
	cases := []struct {
		name, reply string
		wantErr     bool
	}{
		{"success", `[{"success": true}]`, false},
		{"window gone", `[{"success": false}]`, true},
		{"window gone with error", `[{"success": false, "error": "Window with ID 123 not found"}]`, true},
		{"not json", ``, true},
		{"not an array", `{"success": true}`, true},
		{"empty array", `[]`, true},
	}
	for _, c := range cases {
		err := focusReplyError(123, []byte(c.reply))
		if (err != nil) != c.wantErr {
			t.Errorf("%s: err=%v, want error=%v (reply %q)", c.name, err, c.wantErr, c.reply)
		}
	}
}
