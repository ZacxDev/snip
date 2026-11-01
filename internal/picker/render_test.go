package picker

import (
	"regexp"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/ZacxDev/snip/internal/artifact"
	"github.com/ZacxDev/snip/internal/ui"
)

// themedCatalog carries a category on every row, the way the real artifact does
// (the picker's own `catalog` fixture deliberately does not).
var themedCatalog = []artifact.Snippet{
	{Label: "project abs path", Body: "/home/alice/work/dev", Category: "path"},
	{Label: "SSH desktop (LAN)", Body: "ssh alice@10.0.0.15", Category: "ssh"},
	{Label: "prod kubeconfig", Body: "~/ops/prod-kubeconfig", Category: "kubeconfig"},
	{Label: "End-of-session ritual", Body: "reflect on work done and key learnings", Category: "prompt"},
	{Label: "Paste from clipboard", Body: "{{clip}}", Category: "clipboard"},
}

// sgr matches every ANSI escape sequence lipgloss can emit.
var sgr = regexp.MustCompile(`\x1b\[[0-9;:]*[a-zA-Z]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)`)

func stripANSI(s string) string { return sgr.ReplaceAllString(s, "") }

// 🔴 VALIDATE THE INSTRUMENT BEFORE READING ITS VERDICT. If stripANSI stripped
// nothing, every assertion below would find its text anyway — because the text
// was there all along — and the suite would pass while measuring NOTHING.
func TestTheColourStripperActuallyStrips(t *testing.T) {
	styled := ui.StyCursor.Render("proj")
	if !strings.Contains(styled, "\x1b[") {
		t.Fatal("a styled render carries NO escape bytes — this test cannot tell " +
			"colour-removed from coloured, so every guard below is vacuous")
	}
	if bare := stripANSI(styled); bare != "proj" {
		t.Errorf("stripANSI(%q) = %q, want %q", styled, bare, "proj")
	}
	// Negative control: plain text comes back unchanged, so the stripper is not
	// a function that deletes text generally.
	if got := stripANSI("plain text"); got != "plain text" {
		t.Errorf("stripANSI mangled plain text: %q", got)
	}
}

func themedModel() Model { return New(themedCatalog, &fakeLauncher{}) }

// bareLines renders the frame with every escape removed — the screen the
// operator gets on a dumb terminal, with NO_COLOR, or through a pipe.
func bareLines(m Model) []string {
	out := m.Lines()
	for i := range out {
		out[i] = stripANSI(out[i])
	}
	return out
}

// 🔴 MEANING IS NEVER COLOUR-ONLY. Every mark the operator needs — the `▍`
// cursor pointer, each row's icon and label, and the footer keys — must
// survive with all colour stripped. This goes red the day the selection is
// encoded as a background colour alone.
func TestMeaningSurvivesColourRemoval(t *testing.T) {
	m := themedModel()
	lines := bareLines(m)

	for i, r := range m.filtered {
		line := lines[i+1] // line 0 is the header
		if !strings.Contains(line, ui.Icon(r.Category)) {
			t.Errorf("row %d (%s) lost its category icon with colour removed: %q",
				i, r.Label, line)
		}
		if !strings.Contains(line, r.Label) {
			t.Errorf("row %d lost its label with colour removed: %q", i, line)
		}
	}

	footer := lines[len(lines)-1]
	for _, key := range []string{"↑/↓", "⏎", "esc"} {
		if !strings.Contains(footer, key) {
			t.Errorf("the footer lost the %q key with colour removed: %q", key, footer)
		}
	}
	if !strings.Contains(lines[0], "snip") {
		t.Errorf("the header lost the tool name with colour removed: %q", lines[0])
	}
}

// 🔴 THE CURSOR IS A GLYPH FIRST. Exactly one row carries the `▍` pointer, and
// it is the cursor's — so the selection is readable even with colour stripped.
func TestCursorRowIsTheOnlyOneWithThePointer(t *testing.T) {
	m := themedModel()
	m = upd(m, tea.KeyPressMsg{Code: tea.KeyDown}) // move to row 1
	lines := bareLines(m)

	got := -1
	for i, line := range lines {
		if strings.Contains(line, "▍") {
			if got != -1 {
				t.Fatalf("the pointer appears on more than one line (%d and %d)", got, i)
			}
			got = i
		}
	}
	if got != m.cursor+1 {
		t.Fatalf("the pointer is on line %d, want the cursor row (line %d, cursor %d)",
			got, m.cursor+1, m.cursor)
	}
	if m.cursor != 1 {
		t.Fatalf("fixture: cursor = %d, want 1 after one down", m.cursor)
	}
}

// 🔴 THE LABEL COLUMN ALIGNS. Every row's label begins at the same display
// column, so the labels form a readable column rather than a ragged edge — the
// property the fixed-width label pad exists for. Measured with lipgloss.Width
// so a wide glyph counts for the cells it occupies.
func TestTheLabelColumnAlignsAcrossRows(t *testing.T) {
	m := themedModel()
	lines := bareLines(m)
	want := -1
	for i, r := range m.filtered {
		line := lines[i+1]
		idx := strings.Index(line, r.Label)
		if idx < 0 {
			t.Fatalf("row %d has no label in %q", i, line)
		}
		col := lipgloss.Width(line[:idx])
		if want == -1 {
			want = col
			continue
		}
		if col != want {
			t.Errorf("row %d (%s): label starts at column %d, want %d — the "+
				"column is ragged", i, r.Label, col, want)
		}
	}
}

// The header names the mode and the counts; typing switches it to filter mode.
func TestHeaderShowsModeAndCounts(t *testing.T) {
	m := themedModel()
	if got := stripANSI(m.Lines()[0]); !strings.Contains(got, "browse") ||
		!strings.Contains(got, "5/5") {
		t.Fatalf("browse header = %q, want mode and 5/5", got)
	}
	m = typeStr(m, "proj")
	got := stripANSI(m.Lines()[0])
	if !strings.Contains(got, "filter") || !strings.Contains(got, "1/5") {
		t.Fatalf("filter header = %q, want filter mode and 1/5", got)
	}
}

// 🔴 THERE IS NO PREVIEW LINE, DELIBERATELY (removed 2026-10-31). The operator
// picks by DESCRIPTION and does not read the expansion before pasting, so the
// `trigger → body` line under the list was noise he asked to have removed. This
// pins its absence so a later edit (or a merge) cannot quietly re-add it.
func TestThereIsNoPreviewLine(t *testing.T) {
	// POSITIVE CONTROL FIRST: the fixture really does carry the body this guard
	// looks for, so a hit below is about the RENDER, never a missing fixture.
	body := ""
	for _, s := range themedCatalog {
		if s.Label == "project abs path" {
			body = s.Body
		}
	}
	if body == "" {
		t.Fatal("the fixture lost its abs-path body — this guard would pass vacuously")
	}
	for _, l := range bareLines(themedModel()) {
		if strings.Contains(l, "→") {
			t.Fatalf("a line renders an expansion arrow — the preview is back: %q", l)
		}
		if strings.Contains(l, body) {
			t.Fatalf("a line renders a snippet BODY — the preview is back: %q", l)
		}
	}
}

// The frame still fits the terminal: a long LABEL truncates rather than wrapping.
func TestTheFrameFitsTheWidth(t *testing.T) {
	long := strings.Repeat("x", 500)
	m := New([]artifact.Snippet{
		{Label: long, Body: long, Category: "prompt"},
	}, &fakeLauncher{})
	m.Width = 40
	for i, l := range m.Lines() {
		if w := lipgloss.Width(l); w > m.Width {
			t.Errorf("line %d is %d cells wide, over the 40-cell width: %q", i, w, stripANSI(l))
		}
	}
}

// An empty filter still renders, with the notice and the footer.
func TestEmptyFilterStillRenders(t *testing.T) {
	m := typeStr(themedModel(), "zzznotfound")
	lines := bareLines(m)
	if len(m.filtered) != 0 {
		t.Fatalf("expected an empty filter, got %v", labels(m))
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "no match") {
		t.Errorf("the empty filter does not say so: %q", joined)
	}
	if !strings.Contains(joined, "esc") {
		t.Errorf("the footer is missing on an empty filter: %q", joined)
	}
}
