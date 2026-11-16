package telemetry

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

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

// 🔴 A CLIP ROW CARRIES NO QUERY — AND THE GUARD READS THE EMITTED LINE, NOT
// THE PAYLOAD STRUCT. In the shared clipboard the operator is typing
// substrings of CLIP BODIES (the filter matches on the body and nothing else),
// so the query is a fragment of the captured text itself — which the design
// treats as possibly credential-bearing — and the spool ships to a store with
// its own retention that no part of this feature's design scoped. For the
// CATALOG the query is a snippet's own label words, which the audit's term
// evidence is made of, so it must still be there.
//
// This asserts the relationship at the SINK every row passes through, so a
// third emitter arrives redacted without knowing the rule exists.
func TestAClipRowCarriesNoQueryAndACatalogRowStillDoes(t *testing.T) {
	// A realistic clip filter: a substring of a captured secret.
	p := Payload{
		Method: "search", Query: "hunter2", SearchTerm: "hunter2",
		Outcome: "cancel", LatencyOpenMS: 12,
	}

	// Read it off the WIRE LINE, DECODED, the way the collector will: the
	// payload is base64'd inside the line, so a raw substring check is not a
	// read of what the row carries.
	clipLine := decodeLine(t, BuildLine(FireFieldsSource(p, SourceClip), fixedTime, "h"))
	if strings.Contains(clipLine, "hunter2") {
		t.Errorf("the clip row's decoded line carries the typed filter:\n%s", clipLine)
	}
	var clip map[string]any
	if err := json.Unmarshal([]byte(payloadOf(t, FireFieldsSource(p, SourceClip))), &clip); err != nil {
		t.Fatal(err)
	}
	if clip["query"] != "" || clip["search_term"] != "" {
		t.Errorf("clip payload query/search_term = %v/%v, want both empty",
			clip["query"], clip["search_term"])
	}
	// `method` SURVIVES: "search" vs "direct" says only WHETHER something was
	// typed, which is the signal the audit reads, and carries none of the text.
	if clip["method"] != "search" {
		t.Errorf("method = %v, want \"search\" — the derived flag is not the text", clip["method"])
	}

	// POSITIVE CONTROL: the identical payload under the CATALOG source keeps
	// its query, so the two empties above are the redaction and not a field
	// that never gets set.
	var cat map[string]any
	if err := json.Unmarshal([]byte(payloadOf(t, FireFieldsSource(p, SourceSnip))), &cat); err != nil {
		t.Fatal(err)
	}
	if cat["query"] != "hunter2" || cat["search_term"] != "hunter2" {
		t.Fatalf("the catalog row lost its query (%v/%v) — this control proves "+
			"nothing and the audit's term evidence is gone",
			cat["query"], cat["search_term"])
	}
	catLine := decodeLine(t, BuildLine(FireFieldsSource(p, SourceSnip), fixedTime, "h"))
	if !strings.Contains(catLine, "hunter2") {
		t.Fatalf("the catalog LINE does not carry the query either — the decoded "+
			"line read above cannot see a query at all, so its zero is the "+
			"INSTRUMENT and not the redaction:\n%s", catLine)
	}

	// The caller's own struct is untouched: the redaction is the sink's, so a
	// caller that reuses the payload for something else is not surprised.
	if p.Query != "hunter2" {
		t.Errorf("FireFieldsSource mutated the caller's payload: %q", p.Query)
	}
}

// decodeLine renders a v1 line with every `b64:` field decoded, so a substring
// read over it is a read of what the row actually carries. A raw-line grep is
// NOT: base64 of a substring is not a substring of base64, so "the text is not
// in the line" is unprovable without decoding it.
func decodeLine(t *testing.T, line string) string {
	t.Helper()
	parts := strings.Split(line, "\t")
	for i, p := range parts {
		if !strings.HasPrefix(p, "b64:") {
			continue
		}
		kv := strings.SplitN(strings.TrimPrefix(p, "b64:"), "=", 2)
		if len(kv) != 2 {
			t.Fatalf("field %q is not key=value", p)
		}
		raw, err := base64.StdEncoding.DecodeString(kv[1])
		if err != nil {
			t.Fatalf("field %q does not decode: %v", kv[0], err)
		}
		parts[i] = kv[0] + "=" + string(raw)
	}
	return strings.Join(parts, "\t")
}

// payloadOf is the row's `payload` field value.
func payloadOf(t *testing.T, fields []Field) string {
	t.Helper()
	for _, f := range fields {
		if f.Key == "payload" {
			return f.Value
		}
	}
	t.Fatal("no payload field")
	return ""
}
