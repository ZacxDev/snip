package telemetry

import "testing"

// 🔴 THE ROW'S `source` IS WHAT THE AUDIT'S ANALYSIS KEYS ON. Its fire
// predicate counts `source='snip' AND kind='fire' AND outcome='select'` as
// demand for a CATALOG snippet; a shared-clipboard fire is not that, so clips
// carry their own source and the analysis needs no change.
//
// The expected values here are LITERAL, not read back out of the constants:
// the point of pinning a wire contract is to notice a rename that a derived
// expectation would silently follow.
func TestTheTwoSourcesAreTheWireValues(t *testing.T) {
	if SourceSnip != "snip" {
		t.Errorf("SourceSnip = %q, want \"snip\" — the existing analysis greps for it", SourceSnip)
	}
	if SourceClip != "snipclip" {
		t.Errorf("SourceClip = %q, want \"snipclip\"", SourceClip)
	}
	if SourceSnip == SourceClip {
		t.Fatal("the two sources are the same string — the whole separation is gone")
	}
}

// FireFieldsSource puts the named source on the row, and FireFields keeps the
// catalog default. `kind` stays "fire" in both: the discriminator is the
// SOURCE, so an existing kind-based query keeps working.
func TestFireFieldsCarriesTheSource(t *testing.T) {
	get := func(fields []Field, key string) string {
		for _, f := range fields {
			if f.Key == key {
				return f.Value
			}
		}
		return "<absent>"
	}
	p := Payload{Outcome: "select", Label: "4221"}

	if got := get(FireFields(p), "source"); got != "snip" {
		t.Errorf("FireFields source = %q, want \"snip\"", got)
	}
	if got := get(FireFieldsSource(p, SourceClip), "source"); got != "snipclip" {
		t.Errorf("FireFieldsSource(clip) source = %q, want \"snipclip\"", got)
	}
	if got := get(FireFieldsSource(p, SourceClip), "kind"); got != "fire" {
		t.Errorf("kind = %q, want \"fire\" — the source is the discriminator, not the kind", got)
	}
	// An empty source falls back to the catalog one rather than emitting a row
	// with no source at all: an unattributed row is worse than a
	// mis-attributed one, because the analysis cannot even see it to exclude
	// it.
	if got := get(FireFieldsSource(p, ""), "source"); got != "snip" {
		t.Errorf("empty source = %q, want the \"snip\" fallback", got)
	}
}
