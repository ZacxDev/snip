// Package ui is snip's shared terminal theme: the Gruvbox Dark palette the
// picker paints with, the per-category icons, and the styles built from both.
//
// 🔴 THE PALETTE IS A LEDGER, PINNED TWO-WAY AGAINST THE ALACRITTY CONFIG by
// theme_test.go — the same mechanism, for the same reason, as the sibling TUIs
// (mention-review / oc-sent-tui internal/ui/theme.go): the picker is spawned
// INSIDE alacritty (snip-launch execs `alacritty --class snip,snip -e snip
// picker`), so a palette that drifts makes it look like a different
// application from the terminal it opened out of.
//
// 🔴 COLOUR IS DECORATION ON TOP OF A MARK THAT ALREADY SAYS IT. The cursor row
// is marked by the `▍` pointer GLYPH; every row carries its icon and label as
// text; the footer help spells its keys. picker_test.go strips every escape and
// asserts all of that still reads, so colour never carries meaning alone (the
// operator's font makes some colour-only encodings indistinguishable —
// mention-review's words_test.go records the same constraint).
package ui

import (
	"image/color"

	"charm.land/lipgloss/v2"
)

// PaletteHex is the ledger. Keys are `<group>.<name>` exactly as the Alacritty
// `colors` block nests them. 🔴 NOTHING BELOW SPELLS A HEX LITERAL: every colour
// is looked up here by key, so this map is not documentation of the palette —
// it IS the palette, and the test that pins it pins what is actually rendered.
var PaletteHex = map[string]string{
	"primary.background": "#282828",
	"primary.foreground": "#ebdbb2",

	"normal.black":   "#282828",
	"normal.red":     "#cc241d",
	"normal.green":   "#98971a",
	"normal.yellow":  "#d79921",
	"normal.blue":    "#458588",
	"normal.magenta": "#b16286",
	"normal.cyan":    "#689d6a",
	"normal.white":   "#a89984",

	"bright.black":   "#928374",
	"bright.red":     "#fb4934",
	"bright.green":   "#b8bb26",
	"bright.yellow":  "#fabd2f",
	"bright.blue":    "#83a598",
	"bright.magenta": "#d3869b",
	"bright.cyan":    "#8ec07c",
	"bright.white":   "#ebdbb2",
}

// c looks a colour up BY LEDGER KEY.
func c(key string) color.Color { return lipgloss.Color(PaletteHex[key]) }

// Styles.
var (
	// StyTitle is the header line: the tool name.
	StyTitle = lipgloss.NewStyle().Foreground(c("bright.yellow")).Bold(true)
	// StyDim is chrome and secondary text: the header's counts, the footer, the
	// unselected labels, and the preview's body.
	StyDim = lipgloss.NewStyle().Foreground(c("bright.black"))
	// StyText is the cursor row's label — the brightest body text on screen.
	StyText = lipgloss.NewStyle().Foreground(c("primary.foreground"))
	// StyPointer marks the cursor row. It is a GLYPH first and a colour second,
	// which is what keeps the selection legible with every escape stripped.
	StyPointer = lipgloss.NewStyle().Foreground(c("bright.yellow")).Bold(true)
	// StyCursor is the cursor row's label: dark-on-accent, a filled chip, so
	// the selection reads as a block of colour as well as by the pointer.
	StyCursor = lipgloss.NewStyle().
			Foreground(c("primary.background")).
			Background(c("bright.yellow")).
			Bold(true)
	// StyKey is a key name in the footer help.
	StyKey = lipgloss.NewStyle().Foreground(c("bright.blue")).Bold(true)
	// StyNotice is a failure line (the picker staying open on a spawn failure).
	StyNotice = lipgloss.NewStyle().Foreground(c("bright.red")).Bold(true)
)

// CategoryIcons maps an artifact `category` to the row's leading icon.
//
// 🔴 EVERY CATEGORY THE NIX LIST CAN EMIT MUST HAVE AN ENTRY. The consumer
// that generates its catalog from a nix list pins the two sets equal in BOTH
// directions against its own list (devrc's packaging is the worked example);
// in-module, the ledger is guarded by TestEveryIconIsANonEmptyGlyph. A new
// category with no entry renders DefaultIcon — the cosmetic defect the
// consumer's own test exists to catch.
//
// ⚠ THESE ARE EMOJI ON PURPOSE, AND IT IS A MEASURED CHOICE. Alacritty carries
// no `font` setting, so it uses fontconfig's monospace default (DejaVu Sans
// Mono), and nix/graphical.nix pins Noto Color Emoji as that chain's fallback
// (`defaultFonts.monospace`). `fc-match -s 'monospace:charset=1F4C1'` returns
// NotoColorEmoji first, so these render as colour glyphs rather than tofu. Every
// entry is Emoji_Presentation=Yes (no U+FE0F needed) and two cells wide, so the
// icon column aligns.
var CategoryIcons = map[string]string{
	"path":       "📁",
	"ssh":        "🌐",
	"kubeconfig": "🔑",
	"prompt":     "💬",
	"clipboard":  "📋",
}

// DefaultIcon is used for a row with no category, or one this theme does not
// know. It is deliberately a plain monochrome bullet: a row that failed to
// classify must not be dressed up as a known category.
const DefaultIcon = "•"

// Icon returns the row icon for a category, or DefaultIcon.
func Icon(category string) string {
	if ic, ok := CategoryIcons[category]; ok {
		return ic
	}
	return DefaultIcon
}

// CategoryColors maps a category to its label colour. Values come through the
// ledger (c), never a literal.
var CategoryColors = map[string]color.Color{
	"path":       c("bright.blue"),
	"ssh":        c("bright.green"),
	"kubeconfig": c("bright.yellow"),
	"prompt":     c("bright.magenta"),
	"clipboard":  c("bright.cyan"),
}

// CategoryColor returns the label colour for a category, or the brightest
// white for one this theme does not know.
func CategoryColor(category string) color.Color {
	if col, ok := CategoryColors[category]; ok {
		return col
	}
	return c("bright.white")
}

// LabelStyle is the unselected label's style: the category colour, bold.
func LabelStyle(category string) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(CategoryColor(category)).Bold(true)
}
