package ui

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// 🔴 NO STYLE MAY SPELL A HEX LITERAL. Every colour has to come through the
// ledger, or the ledger is documentation of the palette rather than the palette
// itself — and a value with a second copy is a value that will drift from its
// guard.
func TestNoStyleSpellsAHexLiteralOutsideTheLedger(t *testing.T) {
	raw, err := os.ReadFile("theme.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	ledgerStart := strings.Index(text, "var PaletteHex = map[string]string{")
	if ledgerStart < 0 {
		t.Fatal("cannot find the PaletteHex block — this guard is not reading what it thinks")
	}
	ledgerEnd := strings.Index(text[ledgerStart:], "\n}\n")
	if ledgerEnd < 0 {
		t.Fatal("cannot find the end of the PaletteHex block")
	}
	outside := text[:ledgerStart] + text[ledgerStart+ledgerEnd:]

	hex := regexp.MustCompile(`"#[0-9a-fA-F]{6}"`)
	if found := hex.FindAllString(outside, -1); len(found) > 0 {
		t.Errorf("hex literals outside the ledger: %v", found)
	}
	// POSITIVE CONTROL: the pattern MUST match inside the ledger, or "no
	// matches outside" is a claim about a regex that matches nothing.
	if n := len(hex.FindAllString(text[ledgerStart:ledgerStart+ledgerEnd], -1)); n == 0 {
		t.Fatal("the hex pattern matched nothing even inside the ledger — " +
			"this guard cannot see a violation")
	} else {
		t.Logf("hex pattern matched %d literals inside the ledger, 0 outside", n)
	}
}

// 🔴 EVERY ICON IS A REAL GLYPH, NOT A BLANK. An empty or whitespace icon would
// collapse the icon column and read as a rendering bug; a fallback that equals a
// real category's icon would make a misclassified row indistinguishable.
func TestEveryIconIsANonEmptyGlyph(t *testing.T) {
	if len(CategoryIcons) == 0 {
		t.Fatal("the icon ledger is EMPTY — this guard would pass wired to nothing")
	}
	seen := map[string]string{DefaultIcon: "<default>"}
	for cat, ic := range CategoryIcons {
		if strings.TrimSpace(ic) == "" {
			t.Errorf("category %q has an empty icon", cat)
		}
		if prev, dup := seen[ic]; dup {
			t.Errorf("category %q shares icon %q with %s — a misclassified row "+
				"would look identical", cat, ic, prev)
		}
		seen[ic] = cat
	}
}

// ⚠ REMOVED FROM THIS REPO, DELIBERATELY (2026-11-01, the canonicalisation):
// TestThePaletteMatchesTheAlacrittyConfigBothWays and
// TestIconsCoverEveryCategoryTheNixListEmits compared PaletteHex and
// CategoryIcons against files that live in the CONSUMER's repo (the
// Alacritty config and the generated nix snippet list in devrc). They walked
// up looking for `flake.nix` — which is now THIS repo's own flake — found a
// root, demanded the consumer's files, and failed the public CI. The
// couplings they guarded are the desktop packaging's concern: the consumer
// that spawns the picker inside its own Alacritty owns the palette agreement,
// and the owner of the catalog source owns the icon-coverage agreement. The
// in-module guards above stay.
