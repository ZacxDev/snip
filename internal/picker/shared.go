// The picker's SHARED-CLIPBOARD mode: the second keypress's disjoint list.
//
// 🔴 IT IS STRUCTURALLY A MODE, NOT A MERGED LIST AND NOT A TYPED PREFIX.
// Rank() matches on label words plus declared search_terms; a clip has neither,
// and its searchable text is the BODY, which the matcher deliberately never
// reads. Two populations therefore need two match functions. A filter prefix
// (`>foo`) was the other option and is worse: it would route through the same
// refilter()/Rank path with the prefix character sitting in m.query — one rule
// in two places, which is the shape that regenerates the same bug at every
// site. The modal confirm-delete prompt in picker.go is the precedent inside
// this file's own package.
//
// 🔴 Rank IS UNTOUCHED. The repo's audit tooling (snip-usage.py
// --replay/--gate) models the picker with exactly that matcher, so changing it
// silently re-baselines a gate; RankShared is a sibling, not an improvement to
// it.
package picker

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/ZacxDev/snip/internal/shared"
	"github.com/ZacxDev/snip/internal/ui"
)

// mode is which population the list shows.
type mode int

const (
	modeBrowse mode = iota // the nix catalog (the default, and what every existing test drives)
	modeShared             // the cross-device clipboard
)

// SharedKey is the second keypress that opens the shared view.
//
// 🔴 IT MUST BE A MODIFIED CHORD. Every plain printable key is taken by the
// query (Step's trailing `msg.Text != "" && msg.Mod == 0` arm grows the filter
// with it), so an unmodified key would type itself into the query instead.
// ctrl+g is free: the picker's other chords are ctrl+c/p/n/e. ⚠ `tab` was NOT
// used, deliberately — bubbletea's Text for Tab was not measured here, and if
// it is "\t" the key would grow the query, which is the exact failure this
// constant exists to avoid.
const SharedKey = "ctrl+g"

// sharedFetched reports the mesh fetch back into the loop.
//
// 🔴 Err AND Clips ARE INDEPENDENT. A failed fetch is NOT an empty list: the
// store being unreadable and the store being empty are different states, and
// conflating them is the failure posture this whole feature is written
// against — so the error is carried and rendered with its reason, never
// flattened into "(no match)".
type sharedFetched struct {
	Clips []shared.Clip
	Err   error
}

// RankShared filters the clip population for a query: every whitespace-
// separated token must appear as a SUBSTRING of the body, case-folded. An
// empty query is BROWSE and returns the rows verbatim.
//
// 🔴 CHRONOLOGICAL NEWEST-FIRST, WITH NO REORDERING APPLIED — not on browse,
// not on a match. The sibling Rank's caller hands it a most-recently-USED
// order and the browse list is the operator's own history; a clipboard is the
// opposite, and re-sorting it would destroy the one ordering it is for. Three
// lines from here, the catalog path does apply a usage order: that difference
// is deliberate.
//
// 🔴 THIS IS THE CLIENT'S OWN LOCAL MATCHER OVER CLIPS ALREADY FETCHED. It is
// NOT the server's `?q=` semantics and the two do not necessarily agree — the
// server's FTS5 trigram index matches substrings across word boundaries and
// folds case differently depending on the query's LENGTH. The picker holds
// every clip it displays (the body travels with the paste), so local filtering
// is what actually runs. If this ever starts delegating to the endpoint, that
// is the moment to measure the two against each other.
func RankShared(clips []shared.Clip, query string) []shared.Clip {
	norm := strings.ToLower(strings.TrimSpace(query))
	if norm == "" {
		return clips
	}
	tokens := strings.Fields(norm)
	var out []shared.Clip
	for _, c := range clips {
		if clipMatches(strings.ToLower(c.Body), tokens) {
			out = append(out, c)
		}
	}
	return out
}

// clipMatches is the per-row test: AND across tokens, substring per token.
// Both arguments are already lowercased by the caller — normalise once, before
// any matching, the same discipline Rank follows (and for the same measured
// reason: shift/caps-lock produce uppercase text and a query must not go dead
// because a modifier was held).
func clipMatches(body string, tokens []string) bool {
	for _, t := range tokens {
		if !strings.Contains(body, t) {
			return false
		}
	}
	return true
}

// stepShared is the shared mode's whole key table. 🔴 IT IS DISJOINT: every
// key it does not name is swallowed, and there is no arm that falls through to
// the browse table.
func (m Model) stepShared(msg tea.KeyPressMsg) (Model, []Intent) {
	switch msg.String() {
	case "esc":
		// BACK TO BROWSE, not out of the picker. The operator asked for one
		// thing (the shared list) and esc undoes that one thing; a second esc
		// closes the TUI as usual. The clips stay in the model, so coming back
		// does not re-fetch — the store is append-only-ish and a second mesh
		// round trip per toggle would be paid for nothing.
		m.mode = modeBrowse
		m.query = ""
		m.cursor = 0
		m.notice = ""
		m.refilter()
		return m, nil
	case "ctrl+c":
		m.Canceled, m.quit = true, true
		return m, nil
	case "enter", "return":
		if len(m.filteredClips) == 0 {
			return m, nil // nothing to choose; Enter is inert on an empty list
		}
		c := m.filteredClips[m.cursor]
		// 🔴 quit is NOT set here — only the spawned OUTCOME closes the TUI,
		// exactly as the catalog select does: a spawn failure must keep this
		// window open with the reason, and a quit that fires first blanks it.
		m.ClipPasted = c.ID
		return m, []Intent{SelectClip{ID: c.ID, Body: c.Body}}
	case "up", "ctrl+p":
		if m.cursor > 0 {
			m.cursor--
		}
		return m, nil
	case "down", "ctrl+n":
		if m.cursor < len(m.filteredClips)-1 {
			m.cursor++
		}
		return m, nil
	case "backspace":
		if r := []rune(m.query); len(r) > 0 {
			m.query = string(r[:len(r)-1])
			m.cursor = 0
			m.refilter()
		}
		return m, nil
	}
	// Printable text extends the query, which filters the CLIP population
	// through RankShared. A MODIFIED key is not text and must not grow it.
	if msg.Text != "" && msg.Mod == 0 {
		m.query += msg.Text
		m.cursor = 0
		m.refilter()
	}
	return m, nil
}

// clipRowLine renders one clip row: the cursor pointer, the age and device
// attribution, then the body's first line.
//
// The age/device prefix is the sibling history row's vocabulary ("3h · pixel-7")
// rather than a second one invented here. The body is rendered as ONE line with
// its newlines made visible as `⏎`: a clip is arbitrary captured text and a
// multi-line body would otherwise blow the list's row accounting, which the
// window maths depends on.
func (m Model) clipRowLine(i int) string {
	c := m.filteredClips[i]
	selected := i == m.cursor

	pointer := "  "
	if selected {
		pointer = ui.StyPointer.Render("▍") + " "
	}
	meta := shared.Age(c.CreatedAt, m.nowMS()) + " · " + c.Device
	prefix := pointer + ui.StyDim.Render(meta) + " "

	colW := m.Width - lipgloss.Width(pointer) - lipgloss.Width(meta) - 1
	if colW < 1 {
		colW = 1
	}
	style := ui.StyDim
	if selected {
		style = ui.StyCursor
	}
	return prefix + style.Render(truncate(oneLine(c.Body), colW))
}

// oneLine flattens a body for display. The bytes that get PASTED are never
// touched by this — the paste carries the body verbatim through a scratch file;
// this is the list rendering only.
func oneLine(body string) string {
	s := strings.ReplaceAll(body, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\n", " ⏎ ")
	return strings.ReplaceAll(s, "\t", " ")
}

// nowMS is the clock the ages render against: the injected NowMS when a caller
// set one, else the real clock.
func (m Model) nowMS() int64 {
	if m.NowMS != 0 {
		return m.NowMS
	}
	return nowMSFn()
}

// sharedFooter is the shared mode's own key help — a DISJOINT table, like the
// key handling: it must not advertise ctrl+e, which does nothing to a clip
// (there is no catalog row to land and no landing script that knows about the
// mesh store).
func (m Model) sharedFooter() string {
	d := ui.StyDim
	sep := d.Render("  ·  ")
	parts := []string{
		ui.StyKey.Render("↑/↓") + d.Render(" move"),
		ui.StyKey.Render("⏎") + d.Render(" paste"),
		ui.StyKey.Render("esc") + d.Render(" back"),
		d.Render("type to filter"),
	}
	return joinToWidth(parts, sep, m.Width)
}

// sharedBodyLine is the shared mode's THIRD render — the one the catalog list
// does not need.
//
// 🔴 THREE RENDERS, NOT TWO. An empty `filtered` in browse mode renders
// "(no match — esc to cancel)", and a FAILED FETCH landing on that line is the
// empty-result conflation: the operator cannot tell "the mesh is down" from
// "you have no clips". So: fetching… / reachable-but-empty / unreachable with
// the reason (the reason lands in m.notice, rendered above the list). Returns
// "" when there are rows to show, which is when none of the three applies.
func (m Model) sharedBodyLine() string {
	switch {
	case m.fetching:
		return ui.StyDim.Render("  fetching the shared clipboard…")
	case !m.fetched:
		// Only reachable if a render happens between the keypress and the
		// fetch command running; say what is pending rather than "no match".
		return ui.StyDim.Render("  fetching the shared clipboard…")
	case m.fetchFailed:
		// 🔴 CHECKED BEFORE THE EMPTY CASE, AND NOT DERIVED FROM IT. The
		// reason itself is in the notice above the list; this line says the
		// list is unreadable rather than letting it read as "you have no
		// clips".
		return ui.StyDim.Render("  (the shared clipboard could not be read — esc to go back)")
	case len(m.clips) == 0:
		return ui.StyDim.Render("  (the shared clipboard is empty — esc to go back)")
	case len(m.filteredClips) == 0:
		return ui.StyDim.Render("  (no clip matches — esc to go back)")
	}
	return ""
}
