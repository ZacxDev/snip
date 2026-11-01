// Package matcher is snip's port of the keylog detector's search matcher
// (scripts/collector/keylog/espanso_detect.py:336-458), reproduced SEMANTICS
// FOR SEMANTICS — substring tests, the declared-interface precedence step and
// the _AMBIGUOUS_TERM_OWNER table all carry over byte for byte in behaviour.
// The names-trigger tie-break does NOT: the trigger was removed outright
// (2026-10-31), so nothing names a snippet by shorthand any more.
//
// 🔴 WHY PORT INSTEAD OF IMPROVE. The repo's audit tooling (snip-usage.py
// --replay/--gate) models the picker with exactly this matcher; the replay gate
// only means something while the matcher it models is the matcher the picker
// runs (scope doc §"The matcher and the gate"). A better fuzzy matcher is a v2
// with a deliberate gate re-baseline, not a silent improvement here. The known
// blind spots carry over too, measured-and-accepted upstream:
//
//   - `token in word` is a SUBSTRING test, so 'ask' matches a label spelling
//     'task' (the documented way a neighbour silently steals a token);
//   - AMBIGUOUS_TERM_OWNER is an EXACT-STRING lookup, so a typed PREFIX of an
//     owned term ("clar" for "clarify") is not owned and stays unattributed.
package matcher

import (
	"regexp"
	"strings"
)

// wordRe is `_WORD_RE` (espanso_detect.py:36): `[a-z0-9]+`.
// 🔴 HYPHENS ARE NOT WORD CHARS — this is measured history, not taste:
// snip-usage.py once carried its own tokenizer that kept hyphens
// (`WORD = re.compile(r"[a-z][a-z'\-]+")`, commit a072a4c5) and it invented
// "self-miss" findings for every hyphenated label word until it was replaced
// by an import of the detector's definition. A test here fails on purpose if
// a hyphen-keeping tokenizer ever replaces this class.
var wordRe = regexp.MustCompile(`[a-z0-9]+`)

// LabelWords is the label tokenizer: every maximal [a-z0-9]+ run of the
// lowercased label (Python lowercases BEFORE findall — the order matters for
// non-ASCII labels). Hyphens (and every other non-alphanumeric char) SEPARATE
// words; they are never part of one.
func LabelWords(label string) []string {
	return wordRe.FindAllString(strings.ToLower(label), -1)
}

// Snippet is the matcher's view of one artifact row: the label prose (which
// is ALSO the row's identity, since the trigger shorthand was dropped) and the
// DECLARED search terms. It mirrors the Python side's
// `ts.meta[label] = {label, search_terms, replace}` shape minus `replace`,
// which the matcher never reads.
type Snippet struct {
	Label       string
	SearchTerms []string
}

// TokenMatches is `_token_matches` (espanso_detect.py:440-458): one whitespace
// token vs one snippet, by the original (pre-2026-08-05) rules — a SUBSTRING
// test of the token against, in order:
//
//  1. (when useLabel) every [a-z0-9]+ word of the label,
//  2. every declared search_term.
//
// 🔴 THE OLD TRIGGER ARM IS GONE (2026-10-31). The matcher used to test the
// token against the trigger STRING first; the trigger was removed outright and
// identity moved to the label, so a query naming the old shorthand (":acq",
// "acq") no longer resolves — the operator picks by description now, not by
// typing a shorthand. The label is matched as its WORDS (not as one blob), so
// a separator-containing token ("self-miss") still matches no word.
//
// The first hit wins; there is no scoring. useLabel=false restricts the test
// to the snippet's DECLARED interface (search_terms only, the trigger being
// gone) — the declared/label distinction _attribute's precedence step is built
// on.
//
// CONTRACT: token is non-empty, exactly as in Python — every caller gets its
// tokens from whitespace splitting (Python `term.split()`, Go `strings.Fields`),
// which never yields an empty token; TermMatches guards the empty-term case
// upstream. An empty token is vacuously `in` every string in Python; no
// reachable caller produces one, so the port does not invent a guard the
// original never had.
func TokenMatches(token string, s Snippet, useLabel bool) bool {
	if useLabel {
		for _, w := range LabelWords(s.Label) {
			if strings.Contains(w, token) {
				return true
			}
		}
	}
	for _, st := range s.SearchTerms {
		if st == "" {
			continue // Python skips non-str entries; an empty term matches nothing
		}
		if strings.Contains(strings.ToLower(st), token) {
			return true
		}
	}
	return false
}

// TermMatches is the porter's `_term_matches`: TRUE when EVERY
// whitespace-separated token of term matches the snippet. Multi-word queries
// are how the search UI is actually driven ("ssh des", "spine prod") — a
// historical bug tested the WHOLE term as a substring of a single word, so
// any term containing a space could never match anything. A single-token term
// takes exactly the old path.
//
// The picker's filter runs through this one function: the same tokens that
// LIST a row are the tokens that NAME it.
func TermMatches(term string, s Snippet) bool {
	tokens := strings.Fields(term)
	if len(tokens) == 0 {
		return false
	}
	for _, tok := range tokens {
		if !TokenMatches(tok, s, true) {
			return false
		}
	}
	return true
}

// TermMatchesDeclared is `_term_matches_declared`: TermMatches, but WITHOUT
// consulting the label — every token must reach the snippet through the
// DECLARED interface (a search_terms entry; the trigger is gone). Used only by
// Attribute's precedence step; the picker keeps reading the label, so a
// label-matched row is still LISTED, it just stops out-bidding a declared one.
func TermMatchesDeclared(term string, s Snippet) bool {
	tokens := strings.Fields(term)
	if len(tokens) == 0 {
		return false
	}
	for _, tok := range tokens {
		if !TokenMatches(tok, s, false) {
			return false
		}
	}
	return true
}

// AMBIGUOUS_TERM_OWNER is `_AMBIGUOUS_TERM_OWNER` (espanso_detect.py:137-140):
// term -> the snippet that OWNS it for attribution, consulted ONLY on the
// already-ambiguous branch and ONLY over the snippets the term genuinely
// reaches (owner must be in the matched set — the lookup can never invent a
// resolution). A term that is ambiguous and NOT listed here resolves to "";
// silence is not a licence to guess.
//
// The owner is a LABEL now (identity moved off the trigger): 'ask' is the
// highest-traffic term in the config and both the ask-clarifying snippet and
// the dispatch snippet declare it, so the ask-clarifying row owns it.
var AMBIGUOUS_TERM_OWNER = map[string]string{
	"ask":     "ask clarifying questions",
	"clarify": "ask clarifying questions",
}

// Attribute is `_attribute` (espanso_detect.py:336-371): fuzzy-attribute a
// search term to exactly ONE snippet, else "" (Python's None). It returns the
// matched row's LABEL — the identity. The steps, in the ported order:
//
//  1. empty/whitespace term → ""
//  2. every snippet that TermMatches matches → if exactly one, done
//  3. DECLARED-INTERFACE PRECEDENCE: narrow to snippets where every token
//     reaches through search_terms alone; a snippet that DECLARES the term
//     outbids one that merely SPELLS it in label prose. One left → done.
//  4. AMBIGUOUS_TERM_OWNER, over the (possibly narrowed) matched set.
//  5. still ambiguous → "".
//
// 🔴 THE NAMES-TRIGGER TIE-BREAK IS GONE (2026-10-31). It used to let a term
// that IS a candidate's trigger (with or without ':') take it; with the trigger
// removed, a query naming the old shorthand no longer resolves — the operator
// picks by description, so there is nothing to name outright.
//
// Steps 3-4 can only ever SHRINK or re-point WITHIN the matched set, so none of
// them can invent a resolution or re-point a term that already resolved
// uniquely (that branch returned at step 2).
func Attribute(term string, snippets []Snippet) string {
	t := strings.ToLower(strings.TrimSpace(term))
	if t == "" {
		return ""
	}
	matched := make([]Snippet, 0, len(snippets))
	for _, s := range snippets {
		if TermMatches(t, s) {
			matched = append(matched, s)
		}
	}
	if len(matched) == 1 {
		return matched[0].Label
	}
	if len(matched) == 0 {
		return ""
	}
	// Declared-interface precedence (step 3). This can only shrink the
	// candidate set; when NO candidate matches declaredly the set is left
	// alone — with nothing but labels to go on there is no basis to prefer
	// one, and narrowing to the empty set would lose the answer the owner
	// table can still give.
	declared := make([]Snippet, 0, len(matched))
	for _, s := range matched {
		if TermMatchesDeclared(t, s) {
			declared = append(declared, s)
		}
	}
	if len(declared) > 0 {
		matched = declared
		if len(matched) == 1 {
			return matched[0].Label
		}
	}
	// Owner table (step 4), exact-string lookup — a typed PREFIX of an owned
	// term is NOT owned (measured blind spot, carried over, not fixed here).
	if owner, ok := AMBIGUOUS_TERM_OWNER[t]; ok {
		for _, s := range matched {
			if s.Label == owner {
				return owner
			}
		}
	}
	return ""
}
