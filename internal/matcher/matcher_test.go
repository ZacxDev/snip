package matcher

import (
	"reflect"
	"testing"
)

// The fixtures below mirror the documented traps in the detector's own test
// suite (scripts/collector/keylog/tests/test_espanso_detect.py) and the
// comment history of nix/espanso-matches.nix. Every one of them is a measured
// incident, not a synthetic case.
//
// 🔴 IDENTITY IS THE LABEL (2026-10-31). The old trigger shorthand was removed
// outright, so `Snippet` has no Trigger and `Attribute` returns the matched
// LABEL. A query naming the old shorthand (":acq", "acq") no longer resolves —
// the operator picks by description.
const (
	acqLabel  = "ask clarifying questions"
	dacqLabel = "Process feedback: dispatch subagent + elicit scope"
)

// acq/dacq is the REAL :acq/:dacq split (2026-08-28): both snippets spell
// "ask" in their declared search_terms, so declared-precedence narrows
// nothing and _AMBIGUOUS_TERM_OWNER is what decides.
func acqDacq() []Snippet {
	return []Snippet{
		{Label: dacqLabel,
			SearchTerms: []string{"ask", "feedback", "dispatch", "process", "elicit", "scope", "include"}},
		{Label: acqLabel,
			SearchTerms: []string{"ask", "clarifying", "questions"}},
	}
}

func TestTokenMatchesLabelAndTerms(t *testing.T) {
	s := Snippet{Label: "End-of-session ritual", SearchTerms: []string{"handoff"}}
	cases := []struct {
		token string
		want  bool
	}{
		{"eos", false},    // the old trigger shorthand is GONE — no arm matches it
		{":eos", false},   // …and a colon-spelled shorthand resolves nothing
		{"hand", true},    // token ⊂ declared search term
		{"ritual", true},  // token ⊂ label word
		{"session", true}, // label word ("end-of-session" splits on the hyphen)
		{"end", true},     // the other half of the hyphenated label word
		{"zzz", false},
		{"handoffs", false}, // not a substring of any source
	}
	for _, c := range cases {
		if got := TokenMatches(c.token, s, true); got != c.want {
			t.Errorf("TokenMatches(%q) = %v, want %v", c.token, got, c.want)
		}
	}
}

// 🔴 'ask' ⊂ 'task' — THE LABEL-THEFT TRAP. `_token_matches` is a SUBSTRING
// test, so a token matches any label WORD that contains it: a label spelling
// "task" claims the token "ask". The :cgt lesson (nix/espanso-matches.nix,
// 2026-08-19): the obvious term for :cgt was "task", and 'ask' ⊂ 'task' would
// have hijacked all 58 of :acq's 'ask' fires. v1 REPLICATES the substring
// semantics — the blind spot carries over deliberately (scope doc §9) — so
// this test PINS the theft, and the declared-precedence test below pins the
// rescue.
func TestSubstringSemanticsStealThroughTask(t *testing.T) {
	cgt := Snippet{Label: "create a clawgate task to pick up the issues"}
	if !TokenMatches("ask", cgt, true) {
		t.Fatal("'ask' must match a label spelling 'task' — substring semantics are the ported contract")
	}
	// The rescue is the declared-interface precedence: when the ask snippet
	// (which DECLARES "ask") is in the candidate set, _attribute points at it,
	// never at the thief.
	snippets := append(acqDacq(), cgt)
	if got := Attribute("ask", snippets); got != acqLabel {
		t.Fatalf("Attribute(ask) = %q, want the ask label (declared interface must outbid the 'task' label prose)", got)
	}
	// And where NOTHING declares the token, the thief is the only match —
	// that is the theft, honestly recorded.
	if got := Attribute("ask", []Snippet{cgt}); got != cgt.Label {
		t.Fatalf("Attribute(ask) with only a task-labelled snippet = %q, want its label", got)
	}
}

// Multi-word queries are how the search UI is actually driven — a historical
// bug tested the WHOLE term as a substring of a single word, so "ssh des"
// (any term containing a space) could never match anything.
func TestMultiwordQueries(t *testing.T) {
	sshwl := Snippet{Label: "SSH desktop (LAN)",
		SearchTerms: []string{"ssh", "desktop", "wb", "lan", "local"}}
	sshwn := Snippet{Label: "SSH rig via mesh vpn",
		SearchTerms: []string{"vpn", "mesh", "remote"}}
	all := []Snippet{sshwn, sshwl}

	if !TermMatches("ssh des", sshwl) {
		t.Error(`"ssh des" must match the desktop row (every token, across label+terms)`)
	}
	if TermMatches("ssh des", sshwn) {
		t.Error(`"ssh des" must NOT match the rig row ("work" reaches nothing on it)`)
	}
	if got := Attribute("ssh des", all); got != sshwl.Label {
		t.Errorf(`Attribute("ssh des") = %q, want the desktop label`, got)
	}
	// All tokens are required: one token matching is not enough.
	if TermMatches("ssh nothinghere", sshwl) {
		t.Error("a term with a non-matching token must not match")
	}
	// Extra whitespace is tokenized the same way (Python str.split()).
	if !TermMatches("  ssh   des ", sshwl) {
		t.Error("whitespace-padded multi-word term must still match")
	}
	// "spine prod" is the other real query shape: two tokens, one of which
	// reaches nothing on the row.
	csc := Snippet{Label: "spine-controller path", SearchTerms: []string{"controller", "spine controller", "spine"}}
	if got := Attribute("spine prod", []Snippet{csc}); got != "" {
		// "prod" reaches nothing on the spine row — two tokens, one must fail.
		t.Errorf(`Attribute("spine prod") on a prod-less catalog = %q, want ""`, got)
	}
}

// 🔴 HYPHENATED LABEL WORDS. `_WORD_RE` is [a-z0-9]+ — hyphens are
// separators, never word chars. This is the red-demonstration anchor: a
// tokenizer that keeps hyphens is a known-real mutant (a python porter's own
// copy once kept them) that invented "self-miss" findings.
// TestWordTokenizerDropsHyphens below shows the suite red under that mutant.
func TestHyphenatedLabelWords(t *testing.T) {
	s := Snippet{Label: "the self-miss check"}
	words := LabelWords(s.Label)
	if !reflect.DeepEqual(words, []string{"the", "self", "miss", "check"}) {
		t.Fatalf("LabelWords = %v, want hyphen-separated [the self miss check]", words)
	}
	// A token typed WITH its hyphen must NOT match — no [a-z0-9]+ word
	// contains a hyphen, so no single word can contain the hyphenated token.
	if TokenMatches("self-miss", s, true) {
		t.Error("a hyphenated token must not match a hyphenated label word (hyphens are separators)")
	}
	// Either half matches, because either half is a word.
	if !TokenMatches("self", s, true) || !TokenMatches("miss", s, true) {
		t.Error("the halves of a hyphenated label word must each match")
	}
	// Digits are word chars; the map entry class is exactly [a-z0-9].
	if got := LabelWords("submodel dc 03"); !reflect.DeepEqual(got, []string{"submodel", "dc", "03"}) {
		t.Errorf("LabelWords = %v, want digits kept as words", got)
	}
}

// Declared-interface precedence: a snippet that NAMES the term in its
// declared interface (search_terms, the trigger being gone) outbids one that
// only happens to SPELL it in a sentence. Measured motivation (2026-09-02,
// a720d30d): ':acq' grew the label "…recommend improvements…", which made
// ':rna”s declared 'recom'/'recommend' ambiguous and unattributable. A
// cosmetic reword must not shadow another snippet's declared route.
func TestDeclaredInterfacePrecedence(t *testing.T) {
	rna := Snippet{Label: "Recommend next actions",
		SearchTerms: []string{"recommend", "recom", "next", "actions", "rank", "leverage"}}
	acq := Snippet{Label: "ask clarifying questions and recommend improvements and anything useful to include",
		SearchTerms: []string{"ask", "clarifying", "questions"}}
	all := []Snippet{rna, acq}

	// "recommend" matches BOTH via label (acq's label spells it) — but only
	// the rna row DECLARES it. Precedence must resolve to rna.
	if got := Attribute("recommend", all); got != rna.Label {
		t.Fatalf("Attribute(recommend) = %q, want the rna label (declared interface outbids label prose)", got)
	}
	if got := Attribute("recom", all); got != rna.Label {
		t.Fatalf("Attribute(recom) = %q, want the rna label", got)
	}
	// The prefix chain the 2026-09-02 re-measurement recorded: every prefix
	// of "recommend" resolves to rna once precedence is in place.
	for _, q := range []string{"rec", "reco", "recomm", "recomme", "recommen"} {
		if got := Attribute(q, all); got != rna.Label {
			t.Errorf("Attribute(%q) = %q, want the rna label", q, got)
		}
	}
	// And the picker-side reading: both rows are still LISTED (ambiguity is
	// the feature there) — precedence is an ATTRIBUTION device.
	if !TermMatches("recommend", acq) {
		t.Error("the label-matched row must still be listed by TermMatches")
	}
}

// 🔴 THE OWNER TABLE: 'ask' is the highest-traffic term in the whole config
// (58 fires) and BOTH the ask and dispatch snippets declare it —
// declared-precedence narrows nothing, so _AMBIGUOUS_TERM_OWNER is what
// decides. `owner in matched` keeps the declaration honest: a stale entry goes
// inert, never wrong.
func TestAmbiguousTermOwnerTable(t *testing.T) {
	all := acqDacq()
	if got := Attribute("ask", all); got != acqLabel {
		t.Fatalf("Attribute(ask) = %q, want the ask label (the owner table decides the genuine collision)", got)
	}
	if got := Attribute("clarify", all); got != acqLabel {
		// "clarify" ⊂ the ask snippet's declared "clarifying" (substring) —
		// and the owner table also names it. Either route lands on the ask row.
		t.Fatalf("Attribute(clarify) = %q, want the ask label", got)
	}
	// A term that is ambiguous and NOT in the table stays unattributed —
	// silence is not a licence to guess. Fabricate a shared LABEL word:
	// "alpha" is a label word of BOTH rows and declared on NEITHER, so
	// precedence cannot narrow it and no owner names it → honest silence.
	if got := Attribute("alpha", []Snippet{
		{Label: "alpha beta", SearchTerms: []string{"shared"}},
		{Label: "alpha gamma", SearchTerms: []string{"shared"}},
	}); got != "" {
		t.Fatalf("Attribute(alpha) over two label-only matches = %q, want \"\" (no owner, no guess)", got)
	}
	// `owner in matched`: a table entry for a term the owner does not
	// genuinely match goes inert rather than inventing a resolution.
	if got := Attribute("unmatched-term", all); got != "" {
		t.Fatalf("Attribute(unmatched-term) = %q, want \"\"", got)
	}
}

// 🔴 THE NAMES-TRIGGER TIE-BREAK IS GONE (2026-10-31). It used to let a term
// that IS a candidate's trigger (with or without ':') take it, which is how
// bare "acq" resolved over the :acq/:dacq collision. The trigger was removed
// outright, so a query naming the old shorthand resolves by description ONLY —
// and these shorthands name nothing now.
func TestOldShorthandNoLongerResolves(t *testing.T) {
	all := acqDacq()
	// ":acq" is the old shorthand: no label word or search_term contains it
	// (`:` is not a word char), so it reaches nothing.
	if got := Attribute(":acq", all); got != "" {
		t.Fatalf("Attribute(:acq) = %q, want \"\" (the shorthand no longer names anything)", got)
	}
	// The bare "acq" is a substring of neither label — it resolves to nothing.
	if got := Attribute("acq", all); got != "" {
		t.Fatalf("Attribute(acq) = %q, want \"\" (no trigger to name outright)", got)
	}
	// The description still works: "clarifying" is a declared term of the ask
	// snippet and reaches only it.
	if got := Attribute("clarifying", all); got != acqLabel {
		t.Fatalf("Attribute(clarifying) = %q, want the ask label", got)
	}
}

// Empty-query behavior: Attribute("") is ""; TermMatches("") is false (no
// tokens — Python `"".split()`); the picker's empty query is BROWSE, never an
// attribution. Also the whitespace-only term.
func TestEmptyQueryBehavior(t *testing.T) {
	all := acqDacq()
	for _, q := range []string{"", "   ", "\t\n"} {
		if got := Attribute(q, all); got != "" {
			t.Errorf("Attribute(%q) = %q, want \"\"", q, got)
		}
		if TermMatches(q, all[1]) {
			t.Errorf("TermMatches(%q) must be false — no tokens", q)
		}
		if TermMatchesDeclared(q, all[1]) {
			t.Errorf("TermMatchesDeclared(%q) must be false — no tokens", q)
		}
	}
}

// The prefix blind spot is CARRIED OVER, not fixed (scope doc §9): a typed
// PREFIX of an owned term is not owned — the exact-string lookup misses it.
// "clar" reaches the ask snippet ("clarifying" substring) and — in the
// historical config — the dispatch snippet; with today's dispatch snippet not
// spelling it, "clar" resolves via declared substring to the ask row uniquely.
// The honest carry-over case needs BOTH declaring: assert the resolution stays
// "" there.
func TestOwnerPrefixBlindSpotCarriedOver(t *testing.T) {
	all := acqDacq()
	// "clar" reaches the ask row only today ("clarify" typed 1 fire ever; the
	// blind spot needs a genuine both-declare collision to show).
	if got := Attribute("clar", all); got != acqLabel {
		t.Fatalf("Attribute(clar) = %q, want the ask label (only it declares clarifying)", got)
	}
	// Fabricate the genuine collision: both declare "clarifying".
	dacqToo := acqDacq()
	dacqToo[0].SearchTerms = append(dacqToo[0].SearchTerms, "clarifying")
	if got := Attribute("clar", dacqToo); got != "" {
		t.Fatalf("Attribute(clar) with both declaring = %q, want \"\" (the measured prefix blind spot, carried over)", got)
	}
}

// 🔴 THE RED-DEMONSTRATION ANCHOR. LabelWords must tokenize on [a-z0-9]+ —
// hyphens SEPARATE words. This is the test that goes red under the known-real
// mutant (a tokenizer that keeps hyphens, the snip-usage.py copy at commit
// a072a4c5 that invented "self-miss" findings).
func TestWordTokenizerDropsHyphens(t *testing.T) {
	got := LabelWords("limit restored — resume agent")
	// The em dash is not a word char either; the label's own shape from the
	// real config tokenizes to four words.
	if !reflect.DeepEqual(got, []string{"limit", "restored", "resume", "agent"}) {
		t.Fatalf("LabelWords = %v, want [limit restored resume agent]", got)
	}
	got = LabelWords("SSH desktop (LAN)")
	if !reflect.DeepEqual(got, []string{"ssh", "desktop", "lan"}) {
		t.Fatalf("LabelWords = %v, want [ssh desbench lan]", got)
	}
	// 🔴 THE FIXTURE THAT CAN SEE THE HYPHEN MUTANT: a hyphenated label must
	// split — a tokenizer that keeps hyphens yields "self-miss" as ONE word
	// and this assertion goes red (the a072a4c5 false-findings shape).
	got = LabelWords("the self-miss check")
	if !reflect.DeepEqual(got, []string{"the", "self", "miss", "check"}) {
		t.Fatalf("LabelWords = %v, want hyphen-separated [the self miss check]", got)
	}
}
