// Package picker is the snip picker TUI: a bubbletea program spawned inside
// an alacritty (class snip,snip — set by snip-launch, matched by i3's
// for_window rule) listing the whole catalog, filtering as the operator types
// through the PORTED matcher (internal/matcher — the same code the audit's
// replay gate models), and handing the selection to the DETACHED paste helper.
//
// 🔴 THE MODEL IS PURE; EFFECTS ARE INTENTS — the stt-voice discipline. Step()
// returns intents; Update turns an intent into a tea.Cmd that calls the
// Launcher. Every test here runs against the pure model with a fake Launcher;
// no test reaches X11.
//
// 🔴 SELECT CLOSES THE TUI FIRST. The select intent spawns the detached
// helper (`snip paste --trigger T`) and the TUI quits as soon as the spawn
// SUCCEEDS; the helper waits for this window to be gone, then focuses the
// origin and sends the paste key. Pasting while the TUI is still open is
// measured broken (stt-voice, 2026-09-30: i3's focus-follows-mouse hands
// focus back to the floating TUI and the content lands in the TUI itself).
// A spawn FAILURE stays open with the failure in words — nothing was
// launched, so this window is still the only surface that can say why, and
// the abort row it owes is emitted by the picker itself (the helper, which
// would have emitted it, never ran).
package picker

import (
	"fmt"
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/ZacxDev/snip/internal/artifact"
	"github.com/ZacxDev/snip/internal/matcher"
	"github.com/ZacxDev/snip/internal/paste"
	"github.com/ZacxDev/snip/internal/shared"
	"github.com/ZacxDev/snip/internal/telemetry"
	"github.com/ZacxDev/snip/internal/ui"
)

// --- intents ----------------------------------------------------------------

// Select says "the operator chose this label" — Update turns it into the
// detached paste-helper spawn.
type Select struct{ Label string }

// Edit says "the operator wants to edit this row's BODY" — Update stages the
// body in a scratch file and hands the terminal to the editor.
//
// 🔴 THE BODY IS CARRIED IN THE INTENT, not looked up later: the model already
// holds the full artifact row, and re-reading the artifact after the editor
// returns would race a config change made in between.
type Edit struct {
	Label string
	Body  string
}

// ConfirmDelete says "yes, remove this row" — Update turns it into the landing
// call. It exists as an INTENT rather than being acted on inside Step because
// the landing is a world call, and every world call in this model goes through
// Update so a test can drive it with a fake.
type ConfirmDelete struct{ Label string }

// FetchShared says "read the shared clipboard store" — Update turns it into
// the mesh fetch.
//
// 🔴 IT IS ONLY EVER RETURNED FROM THE SECOND KEYPRESS, NEVER FROM New(). The
// open-latency number is measured from process entry to the first render and
// is an acceptance criterion queried from real telemetry rows; an eager fetch
// in New would add a mesh round trip to EVERY open, including the vast
// majority that never ask for a clip, and would block the first render behind
// a network call.
type FetchShared struct{}

// SelectClip says "the operator chose this clip" — Update turns it into the
// detached ephemeral paste.
//
// 🔴 THE BODY IS CARRIED IN THE INTENT. The picker already holds every clip it
// displays, and re-fetching the row after the keypress would race a DELETE
// made from another device in between — the paste would then deliver nothing,
// or the wrong thing, while the operator believes he pasted what he saw.
type SelectClip struct {
	ID   int64
	Body string
}

// DeleteClip says "the operator confirmed removing this clip from the store" —
// Update turns it into the DELETE call.
//
// 🔴 IT IS ONLY EVER RETURNED FROM THE CONFIRMED ANSWER, never from the
// keypress that asks. The store has no trash and the service has no undo, so
// the destructive call is one y/N away from the key, exactly as the catalog's
// ConfirmDelete is.
//
// 🔴 AND IT IS THE ONLY THING THAT REMOVES A ROW. Nothing in the model drops a
// clip locally: the list is re-read from the store after the delete lands, so a
// REFUSED delete structurally cannot show the operator a deletion that did not
// happen.
type DeleteClip struct{ ID int64 }

// Intent is the intent sum type the pure Step returns (unexported marker
// method, stt-voice shape).
type Intent interface{ intentName() }

func (Select) intentName()        {}
func (Edit) intentName()          {}
func (ConfirmDelete) intentName() {}
func (FetchShared) intentName()   {}
func (SelectClip) intentName()    {}
func (DeleteClip) intentName()    {}

// spawned reports the helper-spawn outcome back into the loop.
type spawned struct{ Err error }

// editStaged reports the scratch-file write back into the loop; Path is what
// the editor is then pointed at.
type editStaged struct {
	Path string
	Err  error
}

// editorDone reports the editor's exit. Err is the editor's own failure (a
// non-zero exit, a missing binary) — NOT "the operator changed nothing", which
// is decided by comparing the file against what was staged.
type editorDone struct{ Err error }

// editRead is the outcome of the whole landing sequence: the file was read
// back, the row was rewritten, validated, committed and pushed.
//
//	Unchanged — the body came back byte-identical; nothing was touched
//	Empty     — the body came back EMPTY: the operator's delete gesture, which
//	            is CONFIRMED before anything lands (it is destructive and it
//	            goes to main)
//	Err       — the landing was REFUSED; the TUI stays open with the reason
//	Ready     — the landing succeeded; the caller now pastes the new body
type editRead struct {
	Unchanged bool
	Empty     bool
	Ready     bool
	Err       error
}

// deleteDone reports the deletion landing back into the loop.
type deleteDone struct{ Err error }

// clipDeleted reports the store's answer to a DELETE back into the loop.
//
// 🔴 A 404 IS NOT CARRIED HERE AS AN ERROR. The client maps it to success —
// the row is gone either way, which is what was asked for — so this message's
// Err means the row may still be there, and the list is NOT touched on one.
type clipDeleted struct{ Err error }

// --- ranking (ONE implementation, shared with the search CLI) ----------------

// Rank orders the catalog for a query: declared-interface matches (the
// search_terms) first, then label-prose matches; within each group the supplied
// order stands. An empty/whitespace query is BROWSE: every row, in the order the
// CALLER supplied — which cmd/snip sets to the recency order (internal/recent:
// most recently used first, never-used rows after in artifact order). So "type
// to filter" switches to the matcher's ordering while the unfiltered list is
// the operator's own history.
//
// This is the picker-side reading of the SAME precedence _attribute uses for
// attribution — with one deliberate difference: ambiguity is NOT resolved to
// one row here. Two matches means two rows (the feature; the espanso search
// UI behaves the same), so nothing is dropped and no owner table is applied.
//
// 🔴 THE NAMED-OUTRIGHT GROUP IS GONE (2026-10-31): a query used to rank a row
// whose TRIGGER it named first, but the trigger was removed and identity moved
// to the label, so the operator picks by description and nothing names a
// shorthand any more.
//
// cmd/snip's `search` prints exactly this order, so what the CLI shows is
// what the picker lists.
func Rank(snippets []artifact.Snippet, query string) []artifact.Snippet {
	if strings.TrimSpace(query) == "" {
		return snippets
	}
	var declared, labeled []artifact.Snippet
	// 🔴 NORMALIZE ONCE, BEFORE ANY MATCHING — the matcher's contract is a
	// pre-lowercased term (lowercased and stripped at entry), and the picker is
	// the one caller that
	// holds a RAW typed query: shift/caps-lock produce uppercase text, and
	// TokenMatches lowercases the HAYSTACKS but never the token. Watched RED
	// (TestRankIsCaseInsensitive: "SSH DES" resolved to zero rows) before this
	// normalization existed. Both tests — TermMatches and TermMatchesDeclared —
	// see the same normalized string, so listing and attribution can never
	// disagree about case.
	norm := strings.ToLower(strings.TrimSpace(query))
	for _, s := range snippets {
		sn := matcher.Snippet{Label: s.Label, SearchTerms: s.SearchTerms}
		if !matcher.TermMatches(norm, sn) {
			continue
		}
		if matcher.TermMatchesDeclared(norm, sn) {
			declared = append(declared, s)
		} else {
			labeled = append(labeled, s)
		}
	}
	out := make([]artifact.Snippet, 0, len(declared)+len(labeled))
	out = append(out, declared...)
	out = append(out, labeled...)
	return out
}

// --- model ------------------------------------------------------------------

// Model is the pure picker state. It carries the FULL artifact rows: the
// matcher's declared-interface test needs search_terms, so a label-only copy
// is not enough to filter with.
type Model struct {
	all      []artifact.Snippet // the whole catalog, in the order it was supplied (browse order)
	query    string
	cursor   int
	filtered []artifact.Snippet

	// fx is the effect surface (spawn + toast). Tests inject a fake.
	fx Launcher

	// results, set by Step/Update:
	Selected    string // label of the chosen row, "" until chosen
	Canceled    bool   // Esc / ctrl+c
	SpawnFailed bool   // the helper never launched (abort row already emitted)
	quit        bool
	notice      string
	rowEmitted  bool // an abort row was already emitted for this open

	// OpenLatencyMS is the picker's own open-phase number, measured by the
	// caller and handed to the helper with the spawn (only the helper can
	// assemble the complete fire row).
	OpenLatencyMS int64

	// editLabel/editPath/editBody carry the in-flight edit (ctrl+e) between
	// the editor returning and the landing sequence finishing: the label to
	// rewrite, the scratch file the editor wrote, and what that file held
	// BEFORE the editor ran (so "nothing changed" is a comparison, not a guess).
	editLabel string
	editPath  string
	editBody  string

	// confirmDelete is the label awaiting a y/N answer, set when a row's body
	// came back EMPTY. 🔴 THE EMPTY BODY IS THE ONLY SPELLING THIS UI HAS for
	// "delete this", and the gesture is one select-all-and-Delete away from the
	// one that replaces a body — so it is asked about rather than acted on.
	confirmDelete string

	// mode is which population the list is showing: modeBrowse (the nix
	// catalog) or modeShared (the cross-device clipboard). 🔴 THE MODES ARE
	// DISJOINT, WHICH IS A SAFETY PROPERTY AND NOT ONLY A UI ONE: there is no
	// "fall through to browse" arm anywhere in the shared key table, and the
	// two populations are never merged into one list — a clip has no label and
	// no declared search_terms, so Rank cannot see it, and the catalog has no
	// body the matcher reads, so RankShared cannot see that. Two populations,
	// two match functions, one mode field deciding which is on screen.
	mode mode

	// clips/filteredClips/clipCursor are the shared population, fetched lazily
	// on the second keypress. fetching is in-flight; fetched is "a fetch has
	// completed at least once", which is what makes the three renders
	// distinguishable (fetching / reachable-but-empty / unreachable).
	clips         []shared.Clip
	filteredClips []shared.Clip
	fetching      bool
	fetched       bool

	// confirmClipID is the clip awaiting a y/N answer, set by the shared
	// mode's delete chord. 0 is "no prompt up" — the same sentinel ClipPasted
	// uses, and the store's ids are a positive autoincrement so 0 is never a
	// row.
	//
	// 🔴 IT IS NOT confirmDelete. That field is a catalog LABEL and its y arm
	// lands a catalog deletion through the landing script; sharing it would
	// mean a prompt raised over a clip answering `y` into a git commit. Two
	// populations, two prompts — the same reason the key tables are disjoint.
	confirmClipID int64

	// deletingClipID is the clip whose DELETE is in flight: set when `y` is
	// answered (which takes the prompt down), cleared by clipDeleted.
	//
	// 🔴 IT IS WHY THE DECLINE ARM CANNOT LIE. With one field for both states,
	// pressing `n` after `y` printed "the clip is still in the store" while the
	// DELETE was on the wire and about to land. It is also half of listIsStale:
	// a row whose removal is in flight must not be pasteable or deletable
	// again.
	deletingClipID int64

	// fetchFailed is "the last fetch returned a reason, not rows".
	//
	// 🔴 IT CANNOT BE DERIVED FROM len(clips) == 0, AND ASSUMING IT COULD IS
	// EXACTLY THE EMPTY-RESULT CONFLATION. A failed fetch and an empty store
	// both leave the clip list empty, and the first draft of this render said
	// "the shared clipboard is empty" over an i/o timeout — caught by
	// TestThreeRendersAreDistinguishable, which is why that test exists.
	fetchFailed bool

	// NowMS is the clock the clip rows' ages are rendered against, injected so
	// the render is pure and a test pins an exact string. 0 means "read the
	// real clock at render time".
	NowMS int64

	// ClipPasted is the id of the clip this run pasted, 0 when none was. The
	// ephemeral paste carries the body, so there is nothing to look up
	// afterwards and this is only the record of what happened.
	ClipPasted int64

	// Deleted is the label this run DELETED. A deletion owes no telemetry row
	// (it is neither a use nor a cancel) and nothing is pasted afterwards.
	Deleted string

	// Width/Height arrive via tea.WindowSizeMsg; rendering clamps to them.
	Width, Height int

	// labelW is the widest label in the catalog, so the label column is
	// aligned across rows. Computed once at New.
	labelW int
}

// New builds the model from the loaded artifact. Every row is shown at
// start (empty query = browse mode), in THE ORDER IT IS HANDED — cmd/snip
// hands the recency order, so the model itself stays order-agnostic and a test
// can pin either. fx is required — a model without an effect surface cannot
// complete a selection.
func New(snippets []artifact.Snippet, fx Launcher) Model {
	m := Model{all: snippets, fx: fx, Width: 80, Height: 24, labelW: maxLabelWidth(snippets)}
	m.refilter()
	return m
}

// maxLabelWidth is the widest label in the catalog (at least 1), so the
// label column can be padded to a common width.
func maxLabelWidth(snippets []artifact.Snippet) int {
	w := 1
	for _, s := range snippets {
		if n := lipgloss.Width(s.Label); n > w {
			w = n
		}
	}
	return w
}

// refilter recomputes the visible list for the CURRENT mode. The two
// populations never meet: in shared mode Rank is not called at all, and in
// browse mode RankShared is not.
func (m *Model) refilter() {
	if m.mode == modeShared {
		m.filteredClips = RankShared(m.clips, m.query)
		m.clampCursor()
		return
	}
	m.filtered = Rank(m.all, m.query)
	m.clampCursor()
}

// rowCount is how many rows the list currently holds — the mode's own
// population. One definition, so the cursor clamp, the scroll window and the
// header counts cannot disagree about which list is on screen.
func (m *Model) rowCount() int {
	if m.mode == modeShared {
		return len(m.filteredClips)
	}
	return len(m.filtered)
}

func (m *Model) clampCursor() {
	if m.cursor >= m.rowCount() {
		m.cursor = m.rowCount() - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

// Init implements tea.Model.
func (m Model) Init() tea.Cmd { return nil }

// Update implements tea.Model. Size messages adjust the render bounds; keys
// route through the pure Step; the select intent becomes the spawn Cmd.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch v := msg.(type) {
	case tea.KeyPressMsg:
		next, intents := m.Step(v)
		var cmds []tea.Cmd
		if next.quit {
			cmds = append(cmds, tea.Quit)
		}
		for _, it := range intents {
			if sel, ok := it.(Select); ok {
				sel := sel
				n := next
				cmds = append(cmds, func() tea.Msg {
					return spawned{Err: n.fx.SpawnPaste(sel.Label, n.query, n.OpenLatencyMS, "")}
				})
			}
			if ed, ok := it.(Edit); ok {
				ed := ed
				n := next
				cmds = append(cmds, func() tea.Msg {
					path, err := n.fx.StageEdit(ed.Label, ed.Body)
					return editStaged{Path: path, Err: err}
				})
			}
			if _, ok := it.(FetchShared); ok {
				cmds = append(cmds, fetchSharedCmd(next))
			}
			if dc, ok := it.(DeleteClip); ok {
				dc := dc
				n := next
				cmds = append(cmds, func() tea.Msg {
					return clipDeleted{Err: n.fx.DeleteClip(dc.ID)}
				})
			}
			if sc, ok := it.(SelectClip); ok {
				sc := sc
				n := next
				cmds = append(cmds, func() tea.Msg {
					return spawned{Err: n.fx.SpawnPasteClip(sc.ID, sc.Body, n.query, n.OpenLatencyMS)}
				})
			}
			if cd, ok := it.(ConfirmDelete); ok {
				cd := cd
				n := next
				cmds = append(cmds, func() tea.Msg {
					return deleteDone{Err: n.fx.ApplyDelete(cd.Label)}
				})
			}
		}
		return next, tea.Batch(cmds...)
	case sharedFetched:
		// 🔴 A FAILED FETCH IS NOT AN EMPTY LIST. The fetch is marked done
		// either way (so the render stops saying "fetching…"), but on failure
		// the clip list is left as it was and the REASON is put in the notice
		// — the picker stays open, the catalog still works, and the operator
		// is told why the shared view is unavailable instead of reading an
		// empty list as "you have no clips".
		//
		// 🔴 AND IT TOUCHES NOTHING THE BROWSE VIEW OWNS. The fetch is in
		// flight across an `esc`, so this message can arrive while the CATALOG
		// list is on screen: the cursor it used to reset and the notice it used
		// to write both belong to whatever mode is showing, and resetting a
		// catalog cursor to 0 from here moved the operator's selection out from
		// under a pending Enter (round-1 finding 5). The clip rows are still
		// taken — they are this mode's data, not the browse view's — and
		// everything POSITIONAL or VISIBLE is left alone unless the shared view
		// is the one on screen.
		m.fetching = false
		m.fetched = true
		if v.Err != nil {
			m.fetchFailed = true
			m.toast("shared clipboard: " + v.Err.Error())
			if m.mode == modeShared {
				m.notice = "shared clipboard: " + v.Err.Error()
			}
			return m, nil
		}
		m.fetchFailed = false
		// The clip the cursor is on BEFORE the answer replaces the rows — read
		// here because the next line is what makes it unreadable.
		prevClipID := int64(0)
		if m.mode == modeShared && m.cursor >= 0 && m.cursor < len(m.filteredClips) {
			prevClipID = m.filteredClips[m.cursor].ID
		}
		m.clips = v.Clips
		if m.mode != modeShared {
			// The rows are held for the next ctrl+g, which refilters on the way
			// in; nothing on screen is the shared mode's, so nothing is moved.
			return m, nil
		}
		m.notice = ""
		// 🔴 CLAMPED, NOT RESET. The reset this replaces was round-1 finding 5's
		// fix applied on one axis only — it was made conditional on the shared
		// mode rather than removed — and navigation is deliberately live while
		// the list is stale, so the two combined INSIDE the mode: re-enter with
		// the fetch in flight, move down twice, and the landing answer put the
		// cursor back on row 0 under a pending Enter. A clip body is arbitrary
		// captured text, so that pastes whatever row 0 happens to be — a
		// credential, into whatever window has focus. The cursor stays on the
		// SAME CLIP ID when the store's answer still carries it; when it does
		// not (another device's delete, or this picker's own post-delete
		// re-list) refilter's clamp lands it in range.
		m.refilter()
		if prevClipID != 0 {
			for i, c := range m.filteredClips {
				if c.ID == prevClipID {
					m.cursor = i
					break
				}
			}
		}
		return m, nil
	case clipDeleted:
		// The answered prompt is already down (stepShared moved the id into
		// deletingClipID when `y` was pressed); this is the in-flight marker
		// being cleared, success or failure.
		m.confirmClipID, m.deletingClipID = 0, 0
		if v.Err != nil {
			// 🔴 THE LIST IS LEFT EXACTLY AS IT WAS. A refused delete that
			// dropped the row would show a deletion that did not happen — and
			// this is not derived from a count: nothing anywhere removes a clip
			// locally, so the only way a row leaves the list is a re-read of
			// the store below. The reason goes where fetchFailed's does.
			m.toast("clip delete failed: " + v.Err.Error())
			m.notice = "clip delete failed: " + v.Err.Error()
			return m, nil
		}
		// 🔴 RE-LIST RATHER THAN REMOVE LOCALLY. A local removal would be one
		// render faster and would be the picker's OWN opinion of what the store
		// now holds — which can disagree with it (another device's capture, a
		// bump, a delete from the phone). The re-fetch is the call the ctrl+g
		// arm already makes, so there is one definition of "what is in the
		// store" and the delete cannot drift from it. At one person's clipboard
		// scale, over the mesh, it costs a 2s-bounded round trip the operator
		// already pays on every open of this mode.
		m.fetching = true
		m.notice = ""
		return m, fetchSharedCmd(m)
	case deleteDone:
		if v.Err != nil {
			m.confirmDelete = ""
			m.toast("delete failed: " + v.Err.Error())
			m.notice = "delete failed: " + v.Err.Error()
			return m, nil
		}
		// 🔴 A DELETION PASTES NOTHING AND OWES NO ROW. There is no body left to
		// paste, so the helper is never spawned; and the operator got exactly
		// what he asked for, so this is not a cancel either. The toast is what
		// survives the window closing.
		m.Deleted, m.confirmDelete = m.confirmDelete, ""
		m.toast("snip: deleted " + m.Deleted)
		m.quit = true
		return m, tea.Quit
	case editStaged:
		// The scratch file exists: hand the terminal to the editor. ExecProcess
		// RESTORES the TUI when it exits, so the operator comes back to the
		// list with the edit already on disk.
		if v.Err != nil {
			m.toast("edit: " + v.Err.Error())
			m.notice = "edit: " + v.Err.Error()
			return m, nil
		}
		m.editPath = v.Path
		return m, tea.ExecProcess(editorCommand(v.Path), func(err error) tea.Msg {
			return editorDone{Err: err}
		})
	case editorDone:
		if v.Err != nil {
			m.toast("editor failed: " + v.Err.Error())
			m.notice = "editor failed: " + v.Err.Error()
			return m, nil
		}
		label, path, staged := m.editLabel, m.editPath, m.editBody
		n := m
		return m, func() tea.Msg {
			body, err := n.fx.ReadEdit(path)
			if err != nil {
				return editRead{Err: err}
			}
			if body == staged {
				// Nothing typed, or the editor exited without saving: touching
				// git for this would be a commit that changes nothing.
				return editRead{Unchanged: true}
			}
			// 🔴 AN EMPTY BODY IS THE DELETE GESTURE, NOT AN EMPTY BODY. The
			// editor only ever holds the body, so "delete this entry" has
			// nowhere else to come from — and landing it as `replace = ""`
			// produced a live row that pastes nothing while still recording a
			// fire (measured on the operator's first real use, 2026-11-01).
			// TrimSpace, not len==0: an editor that saves a lone newline for an
			// emptied buffer is the same gesture.
			if strings.TrimSpace(body) == "" {
				return editRead{Empty: true}
			}
			// 🔴 THE LANDING IS REFUSABLE. A refusal leaves the repo untouched
			// and the reason is shown in the TUI — the operator is standing
			// right here and this window is the only place that can say why.
			if err := n.fx.ApplyEdit(label, path); err != nil {
				return editRead{Err: err}
			}
			return editRead{Ready: true}
		}
	case editRead:
		switch {
		case v.Unchanged:
			m.notice = "no change — nothing committed"
			return m, nil
		case v.Empty:
			// 🔴 CONFIRMED, because it is destructive AND it lands on main.
			// Arming the field is ALL this does: the prompt itself is rendered
			// from it (confirmPrompt), so no handler can clear the question
			// while leaving the answer live.
			m.confirmDelete = m.editLabel
			return m, nil
		case v.Err != nil:
			m.toast("edit failed: " + v.Err.Error())
			m.notice = "edit failed: " + v.Err.Error()
			return m, nil
		}
		// Landed. Paste the NEW body — from the scratch file, because the
		// deployed artifact still holds the old one until the landing's
		// detached redeploy finishes. This closes the TUI exactly like a
		// select does: the helper waits for this window to be gone before it
		// types.
		label, path, q, lat := m.editLabel, m.editPath, m.query, m.OpenLatencyMS
		m.Selected = label
		n := m
		return m, func() tea.Msg {
			return spawned{Err: n.fx.SpawnPaste(label, q, lat, path)}
		}
	case spawned:
		// 🔴 THE TUI CLOSES AS SOON AS THE HELPER IS SPAWNED. A spawn FAILURE
		// is the one case where staying open is right: nothing was launched,
		// so this window is still the only surface that can say why. The
		// abort row the helper would have emitted is emitted HERE (once),
		// and the later quit path is marked so it emits no second row.
		if v.Err != nil {
			m.SpawnFailed = true
			if !m.rowEmitted {
				emitFire(row(m, "abort"), sourceFor(m))
				m.rowEmitted = true
			}
			m.toast("paste helper failed to launch: " + v.Err.Error())
			m.notice = "paste helper failed: " + v.Err.Error()
			return m, nil
		}
		m.quit = true
		return m, tea.Quit
	case tea.WindowSizeMsg:
		m.Width, m.Height = max(v.Width, 1), max(v.Height, 1)
	}
	return m, nil
}

// fetchSharedCmd is the mesh read, as a cmd. ONE definition, used by both the
// second keypress and the re-list after a delete lands — so "what the store
// holds" cannot come to mean two different things at two call sites.
func fetchSharedCmd(m Model) tea.Cmd {
	return func() tea.Msg {
		clips, err := m.fx.FetchShared()
		return sharedFetched{Clips: clips, Err: err}
	}
}

// View implements tea.Model.
func (m Model) View() tea.View {
	if m.quit {
		return tea.NewView("")
	}
	return tea.NewView(strings.Join(m.Lines(), "\n"))
}

// Lines is the rendered frame, one string per terminal line — the pure half the
// tests pin (they never need a TTY).
//
// 🔴 COLOUR IS DECORATION ON TOP OF MARKS THAT ALREADY SAY IT. With every
// escape stripped the header, the `▍` cursor pointer, every row's icon and
// label, and the footer help all still read — picker_test.go asserts
// exactly that. The palette and icons live in internal/ui (the sibling-TUI
// theme package), not here.
func (m Model) Lines() []string {
	out := []string{m.header()}
	if line := m.noticeLine(); line != "" {
		out = append(out, ui.StyNotice.Render(" "+truncate(line, m.Width-1)))
	}
	start, end := m.window()
	if m.mode == modeShared {
		for i := start; i < end; i++ {
			out = append(out, m.clipRowLine(i))
		}
		// 🔴 THE SHARED MODE HAS ITS OWN EMPTY LINE — three states, not one.
		// See sharedBodyLine: a failed fetch must never land on browse mode's
		// "(no match)".
		if line := m.sharedBodyLine(); line != "" {
			out = append(out, line)
		}
		out = append(out, m.sharedFooter())
		return out
	}
	for i := start; i < end; i++ {
		out = append(out, m.rowLine(i))
	}
	if len(m.filtered) == 0 {
		out = append(out, ui.StyDim.Render("  (no match — esc to cancel)"))
	}
	out = append(out, m.footer())
	return out
}

// header is the title line: the tool name, the mode, and how much of the
// catalog is showing. The key help lives in the footer, not here.
// A THIRD mode word is a one-line extension of an asserted convention — the
// header already names its mode and the counts, and a test pins that. In
// shared mode the counts are the CLIP population's, not the catalog's: the two
// lists are never mixed, and showing "2/27" over a list of clips would be a
// lie about what is on screen.
func (m Model) header() string {
	modeWord := "browse"
	if m.mode == modeShared {
		modeWord = "shared"
	}
	if strings.TrimSpace(m.query) != "" {
		modeWord = fmt.Sprintf("filter %q", m.query)
		if m.mode == modeShared {
			modeWord = fmt.Sprintf("shared filter %q", m.query)
		}
	}
	const title = " snip "
	total := len(m.all)
	if m.mode == modeShared {
		total = len(m.clips)
	}
	rest := fmt.Sprintf("%s · %d/%d", modeWord, m.rowCount(), total)
	return ui.StyTitle.Render(title) +
		ui.StyDim.Render(truncate(rest, m.Width-lipgloss.Width(title)))
}

// footer is the key help. 🔴 PARTS ARE DROPPED TO FIT, NEVER WRAPPED: a footer
// that wraps steals a row from the list and makes the window maths wrong, so on
// a narrow terminal the trailing hint goes rather than the layout.
func (m Model) footer() string {
	d := ui.StyDim
	sep := d.Render("  ·  ")
	parts := []string{
		ui.StyKey.Render("↑/↓") + d.Render(" move"),
		ui.StyKey.Render("⏎") + d.Render(" paste"),
		ui.StyKey.Render("ctrl+e") + d.Render(" edit"),
		ui.StyKey.Render(SharedKey) + d.Render(" shared"),
		ui.StyKey.Render("esc") + d.Render(" cancel"),
		d.Render("type to filter"),
	}
	return joinToWidth(parts, sep, m.Width)
}

// joinToWidth joins the footer parts with sep, DROPPING trailing parts that do
// not fit rather than wrapping — one implementation, so the browse and shared
// footers cannot disagree about the layout the window maths assumes.
func joinToWidth(parts []string, sep string, width int) string {
	var b strings.Builder
	b.WriteString(" ")
	used := 1
	for i, p := range parts {
		add := lipgloss.Width(p)
		if i > 0 {
			add += lipgloss.Width(sep)
		}
		if used+add > width {
			break
		}
		if i > 0 {
			b.WriteString(sep)
		}
		b.WriteString(p)
		used += add
	}
	return b.String()
}

// rowLine renders one catalog row: the cursor pointer, the category icon, and
// the label in its category colour. The cursor row gets the `▍` pointer AND a
// filled accent chip carrying the label, so the selection reads as a block of
// colour as well as by the pointer. The label is truncated to the terminal
// width so a long label never wraps onto a second line.
//
// 🔴 THE ROW IS THE LABEL NOW (2026-10-31). The trigger used to be the row's
// anchor with the label as trailing prose; the trigger was removed and its
// identity moved to the label, so there is one text column and it is the label.
func (m Model) rowLine(i int) string {
	r := m.filtered[i]
	selected := i == m.cursor

	pointer := "  "
	if selected {
		pointer = ui.StyPointer.Render("▍") + " "
	}
	icon := ui.Icon(r.Category)

	// The label column is padded to the widest label so columns line up, but
	// never past the terminal edge — a single very long label must truncate
	// rather than blow the frame width.
	prefixW := 2 + lipgloss.Width(icon) + 1
	colW := m.labelW
	if maxW := m.Width - prefixW; colW > maxW {
		colW = maxW
	}
	if colW < 1 {
		colW = 1
	}

	labelStyle := ui.LabelStyle(r.Category).Width(colW)
	if selected {
		labelStyle = ui.StyCursor.Width(colW)
	}

	label := truncate(r.Label, colW)
	return pointer + icon + " " + labelStyle.Render(label)
}

// confirmPrompt is THE DELETE PROMPT, DERIVED FROM THE ARMED STATE AND NOTHING
// ELSE.
//
// 🔴 THE ARMED HALF AND THE VISIBLE HALF ARE ONE FACT NOW. They used to be two:
// `confirmClipID` (which answers `y`) and a copy of the prompt text parked in
// `m.notice` (which is what the operator sees). Three handlers wrote
// `m.notice = ""` without looking at the confirm fields, so a fetch landing
// between the ctrl+x and the keypress wiped the prompt off the screen while
// leaving it ARMED — and `y`, the first character of an ordinary filter query,
// then deleted a clip with no prompt in sight and "type to filter" in the
// footer (round-1 blocker). Adding `if m.confirmClipID == 0` to each
// `m.notice = ""` site would be the same predicate open-coded at N sites,
// regenerating the bug at site N+1; instead the prompt is COMPUTED at render
// time from the field that arms it, so there is no stored copy for anything to
// clear. The invariant a guard can pin: confirmClipID != 0 ⇒ the rendered frame
// carries this clip's prompt.
//
// The clip prompt wins over the catalog one because the two are never armed
// together (disjoint modes, disjoint key tables), and an arbitrary order is
// better than a frame that shows neither.
func (m Model) confirmPrompt() string {
	if m.confirmClipID != 0 {
		// The prompt NAMES THE ROW — its id and a one-line preview of its body
		// — so the answer cannot be about a different clip than the one under
		// the cursor, the property the catalog prompt's label spelling has.
		//
		// 🔴 THE `[y/N]` LEADS, unlike the catalog prompt's trailing one. This
		// line is TRUNCATED to the terminal width, and with the ask at the end
		// an 80-column terminal cut it off — measured, as the first run of this
		// feature's own test. A destructive prompt whose answer shape is the
		// part that disappears is worse than one with a short preview, so the
		// preview is what gives way.
		// 🔴 THE PREVIEW IS LOOKED UP, AND ITS ABSENCE IS SAID OUT LOUD. The
		// prompt is rendered from the id, and the clip list is re-read from the
		// store underneath it, so the row CAN be gone by the time this renders
		// (another device deleted it, or the store answered without it). A
		// quoted empty string there would read as a clip whose body is empty;
		// the operator is being asked to destroy something and must not be
		// shown a blank where the thing is named.
		for _, c := range m.clips {
			if c.ID == m.confirmClipID {
				return fmt.Sprintf("[y/N] delete clip %d from the shared store — %q · no undo",
					m.confirmClipID, truncate(oneLine(c.Body), 32))
			}
		}
		return fmt.Sprintf("[y/N] delete clip %d from the shared store — (that row is no longer listed) · no undo",
			m.confirmClipID)
	}
	if m.confirmDelete != "" {
		// 🔴 CONFIRMED, because it is destructive AND it lands on main. The
		// prompt names the label so the answer cannot be about a different row
		// than the one under the cursor.
		return fmt.Sprintf("delete %q — its body is empty. [y/N]", m.confirmDelete)
	}
	return ""
}

// noticeLine is the one line above the list: the DELETE PROMPT when one is
// armed, else whatever the last handler had to say. The prompt outranks the
// notice deliberately — a prompt that can be covered is a prompt that can be
// answered blind, which is the whole of the round-1 blocker.
func (m Model) noticeLine() string {
	if p := m.confirmPrompt(); p != "" {
		return p
	}
	return m.notice
}

// chromeLines is how many non-row lines the frame carries: the header and the
// footer, plus the notice/prompt line when present, plus the shared mode's own
// body line when IT is present too. ⚠ There is deliberately NO preview
// line: the operator picks by DESCRIPTION and does not read the expansion
// before pasting, so rendering `trigger → body` under the list was noise he
// asked to have removed (2026-10-31). Do not re-add it without asking.
//
// 🔴 IT COUNTS EVERY NON-ROW LINE Lines() CAN EMIT ALONGSIDE ROWS, AND
// sharedBodyLine IS ONE OF THEM. While `fetching` that line is appended IN
// ADDITION to the rows (the re-entry and post-delete re-list states, where the
// previous answer is still on screen), so leaving it out overflowed the
// terminal by exactly one line — measured at Height=10: 10 lines settled, 11
// re-listing (round-1 finding 8). Keep this in step with Lines().
//
// ⚠ ONE LINE IS DELIBERATELY UNCOUNTED, so this is not "every line that is not
// a row": browse mode's `(no match — esc to cancel)`. It renders only when
// `len(m.filtered) == 0`, i.e. when there are no rows for it to displace, so
// the window it is subtracted from is one nobody is scrolling. A line that can
// appear WITH rows — as sharedBodyLine does — must be counted here.
func (m *Model) chromeLines() int {
	n := 2 // header + footer
	if m.noticeLine() != "" {
		n++
	}
	if m.mode == modeShared && m.sharedBodyLine() != "" {
		n++
	}
	return n
}

// window clamps the visible slice to the terminal height, scrolling around
// the cursor. The chrome is subtracted first so a full window still fits.
func (m *Model) window() (int, int) {
	h := m.Height - m.chromeLines()
	if h < 1 {
		h = 1
	}
	start := 0
	if m.cursor >= h {
		start = m.cursor - h + 1
	}
	end := start + h
	if end > m.rowCount() {
		end = m.rowCount()
	}
	return start, end
}

// truncate cuts s to at most w display cells, appending an ellipsis when it had
// to cut. It measures with lipgloss.Width so a wide glyph counts for the cells
// it occupies, not for one rune.
func truncate(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= w {
		return s
	}
	const ell = "…"
	room := w - lipgloss.Width(ell)
	if room < 0 {
		room = 0
	}
	var b strings.Builder
	used := 0
	for _, r := range s {
		rw := lipgloss.Width(string(r))
		if used+rw > room {
			break
		}
		b.WriteRune(r)
		used += rw
	}
	return b.String() + ell
}

// Step is the pure key handler: it returns the next model and any intent.
func (m Model) Step(msg tea.KeyPressMsg) (Model, []Intent) {
	// 🔴 THE DELETE PROMPT IS MODAL. While it is up, only its own answers are
	// read — everything else is SWALLOWED rather than falling through to the
	// query, because a keystroke meant for the prompt must never silently become
	// a filter character (or, worse, be the Enter that pastes something).
	if m.confirmDelete != "" {
		switch msg.String() {
		case "y", "Y":
			return m, []Intent{ConfirmDelete{Label: m.confirmDelete}}
		case "n", "N", "esc":
			// Backs out of the DELETION, not out of the picker: the operator is
			// mid-edit and asking for one thing to be undone. A second esc (or
			// ctrl+c) closes the TUI as usual.
			m.confirmDelete = ""
			m.notice = "deletion cancelled — nothing committed"
			return m, nil
		case "ctrl+c":
			m.Canceled, m.quit = true, true
			return m, nil
		}
		return m, nil
	}
	// 🔴 THE SHARED MODE IS A DISJOINT KEY TABLE, NOT A SET OF EXTRA ARMS.
	// Nothing falls through to the browse table: a key that means something to
	// the catalog list (ctrl+e's edit-and-land, which has no meaning for a row
	// that came from another device) is SWALLOWED here rather than reaching
	// code that would act on a catalog row the cursor is not even pointing at.
	if m.mode == modeShared {
		return m.stepShared(msg)
	}
	switch msg.String() {
	case "esc", "ctrl+c":
		m.Canceled, m.quit = true, true
		return m, nil
	case SharedKey:
		// THE SECOND KEYPRESS. The mode switches now and the fetch is fired as
		// an intent — so the list is on screen saying "fetching…" while the
		// mesh round trip happens, instead of the keypress blocking the loop.
		//
		// The query is CLEARED on the way in: a filter typed against the
		// catalog has no meaning over clips, and carrying it across would open
		// the shared view already filtered by something the operator typed for
		// a different population.
		m.mode = modeShared
		m.query = ""
		m.cursor = 0
		m.notice = ""
		m.fetching = true
		m.refilter()
		return m, []Intent{FetchShared{}}
	case "enter", "return":
		if len(m.filtered) == 0 {
			return m, nil // nothing to choose; Enter is inert on an empty list
		}
		sel := m.filtered[m.cursor]
		// 🔴 quit is NOT set here — only the spawned OUTCOME closes the TUI.
		// The spawn cmd and the quit would otherwise race: a spawn failure
		// must keep the window open with the failure in words, and a quit
		// that fires first blanks it (the stt-voice Quitting lesson). The
		// spawned-success branch is the only place Enter quits.
		m.Selected = sel.Label
		return m, []Intent{Select{Label: sel.Label}}
	case "up", "ctrl+p":
		if m.cursor > 0 {
			m.cursor--
		}
		return m, nil
	case "down", "ctrl+n":
		if m.cursor < len(m.filtered)-1 {
			m.cursor++
		}
		return m, nil
	case "ctrl+e":
		// EDIT THE SELECTED ROW'S BODY. Inert on an empty list, like Enter:
		// there is nothing to edit and an editor opened on nothing would then
		// have to be refused a second time.
		//
		// The terminal is handed to the editor and given back (Update's
		// editStaged case), so this stays the same window the operator opened.
		if len(m.filtered) == 0 {
			return m, nil
		}
		sel := m.filtered[m.cursor]
		m.editLabel = sel.Label
		m.editBody = sel.Body
		m.editPath = ""
		return m, []Intent{Edit{Label: sel.Label, Body: sel.Body}}
	case "backspace":
		if r := []rune(m.query); len(r) > 0 {
			m.query = string(r[:len(r)-1])
			m.cursor = 0
			m.refilter()
		}
		return m, nil
	}
	// Printable text (runes, space included) extends the query. A MODIFIED
	// key (ctrl/alt + letter) is not text and must not grow the query.
	if msg.Text != "" && msg.Mod == 0 {
		m.query += msg.Text
		m.cursor = 0
		m.refilter()
	}
	return m, nil
}

// toast is the model's best-effort notify, through the injected surface.
func (m *Model) toast(msg string) {
	if m.fx != nil {
		m.fx.Toast(msg)
	}
}

// --- the program shell -------------------------------------------------------

// Launcher is the effect surface the picker needs: the DETACHED paste-helper
// spawn (with the picker's handoff values), the edit path's three world calls,
// and a toast. Split from effects.Effects so the TUI keeps its own minimal
// seam; effects.Live implements both interfaces.
type Launcher interface {
	// SpawnPaste launches `self paste --label L` detached and returns
	// without waiting for it, injecting SNIP_QUERY and SNIP_OPEN_LATENCY_MS
	// into the child environment.
	//
	// 🔴 bodyFile IS EMPTY ON THE NORMAL PATH. After a ctrl+e edit the row's
	// new body is NOT in the deployed artifact yet — the artifact is
	// regenerated by the landing's redeploy, which is still running — so the
	// helper is handed the operator's edited text instead, and
	// `paste --body-file` overrides the artifact's body for that one label.
	// Without this the paste would deliver the OLD body and the whole feature
	// would be a lie.
	SpawnPaste(label, query string, openLatencyMS int64, bodyFile string) error

	// StageEdit writes body to a scratch file and returns its path — the file
	// the editor is pointed at.
	StageEdit(label, body string) (string, error)

	// ReadEdit returns the scratch file's contents after the editor exits.
	ReadEdit(path string) (string, error)

	// ApplyEdit lands the edited body through the landing script
	// ($SNIP_REPO/scripts/snip-edit.sh): it rewrites the catalog's source,
	// validates the result, and commits it — the landing script's contract.
	// An error means the landing was REFUSED and nothing was committed.
	ApplyEdit(label, bodyFile string) error

	// FetchShared reads the cross-device clipboard store over the mesh.
	//
	// 🔴 AN ERROR IS NOT AN EMPTY LIST. The implementation must map every
	// transport, status and parse failure to an error with a reason in words;
	// returning (nil, nil) for an unreachable store would render as "you have
	// no clips", which is the one failure this feature is written against.
	//
	// 🔴 IT IS CALLED FROM THE SECOND KEYPRESS, NEVER FROM New(). See the
	// FetchShared intent: an eager fetch would put a mesh round trip inside
	// every picker open's measured latency.
	FetchShared() ([]shared.Clip, error)

	// SpawnPasteClip launches the EPHEMERAL paste for one clip: the body
	// travels with the spawn (through a scratch file), so the detached helper
	// never re-looks-up a row that a delete from another device may have
	// removed in the meantime.
	SpawnPasteClip(id int64, body, query string, openLatencyMS int64) error

	// ApplyDelete lands the REMOVAL of a row, through the same landing script's
	// explicit delete mode. Same refusals, same pipeline — and, like
	// ApplyEdit, an error means nothing was committed.
	ApplyDelete(label string) error

	// DeleteClip removes one clip from the shared store.
	//
	// 🔴 A 404 MUST BE SUCCESS. The row is gone either way, which is what the
	// operator asked for: the clip may have been deleted from the phone, or
	// by a second press against a list fetched before that happened. An
	// implementation that reports it as a failure makes the picker say a
	// deletion was refused when the store agrees with the operator.
	//
	// 🔴 EVERY OTHER FAILURE IS AN ERROR WITH ITS REASON IN WORDS. The model
	// removes nothing locally, so an error leaves the row on screen — which
	// is correct, because it may still be in the store.
	DeleteClip(id int64) error

	// Toast surfaces a failure in words. Best effort.
	Toast(msg string)
}

// Run wires the model to a real terminal.
//
// The returned exit code follows the tool's contract: 0 on select AND on
// cancel (a cancel is a normal outcome, not a failure — telemetry records it
// as outcome=cancel), 1 on a failed helper spawn (the abort row + toast
// already said why).
func Run(m Model, openLatencyMS int64) int {
	m.OpenLatencyMS = openLatencyMS
	if m.fx == nil {
		fmt.Fprintf(stderrW, "snip: picker has no effect surface\n")
		return 1
	}
	program := tea.NewProgram(m)
	final, err := program.Run()
	if err != nil {
		// A TUI that cannot run (no TTY) must not silently vanish: the
		// operator pressed a hotkey and got nothing. Say it on stderr.
		fmt.Fprintf(stderrW, "snip: picker failed to start: %v\n", err)
		return 1
	}
	return finish(final.(Model))
}

// finish maps the settled model to the exit code and the one row the picker
// itself owes (cancel; abort was already emitted at the spawn failure).
// Split from Run so tests drive it without a TTY.
func finish(m Model) int {
	switch {
	case m.SpawnFailed:
		return 1
	case m.Deleted != "":
		// 🔴 A DELETION OWES NO ROW. It is neither a USE (nothing was pasted)
		// nor a CANCEL (the operator got what he asked for), so it must not be
		// counted as either — the audit reads these rows as demand, and a
		// deletion is the opposite of demand.
		return 0
	case m.Selected != "":
		// The helper was spawned (it owns the select row) — nothing to emit.
		return 0
	case m.ClipPasted != 0:
		// 🔴 A CLIP PASTE IS A PASTE, AND ITS HELPER OWNS THE ROW. This case
		// used to be absent, so every successful clip paste fell through to the
		// default arm and emitted outcome=cancel ON TOP of the helper's
		// select row — one paste, two rows, and a clip store in 100% use read
		// as 50% cancels (round-1 finding 2). The clip path sets ClipPasted
		// rather than Selected (the clip has no label to put there: the two
		// populations do not share an identity field), which is exactly why
		// the Selected test above cannot see it.
		return 0
	default:
		// Cancelled (Esc/ctrl+c) or closed with nothing chosen: one cancel
		// row, exit 0 — an open that resolved to nothing is demand evidence,
		// not an error. The abort path above already emitted its row.
		if !m.rowEmitted {
			emitFire(row(m, "cancel"), sourceFor(m))
		}
		return 0
	}
}

// sourceFor is the telemetry SOURCE the picker's own rows carry: a row emitted
// while the shared view is open is a clipboard row, not a catalog one.
//
// 🔴 IT IS NOT COSMETIC. snip-usage.py's prune verdicts read catalog rows as
// demand for catalog snippets, and a clip query credited as term evidence can
// rescue a genuinely DEAD snippet from the only prune verdict there is. A
// cancel typed against the clip list is evidence about clips.
func sourceFor(m Model) string {
	if m.mode == modeShared {
		return telemetry.SourceClip
	}
	return telemetry.SourceSnip
}

// row builds the payload both picker-side rows (cancel / spawn-abort) share:
// the picker knows the query, the derived method, and its own open latency;
// there is no paste latency because nothing was pasted. The origin context
// comes from the environment snip-launch captured (the model has no window
// knowledge of its own); the label is the chosen row's label when one was
// chosen, else "".
//
// ⚠ THE QUERY IS PASSED VERBATIM HERE AND REDACTED AT THE SINK. A row emitted
// under telemetry.SourceClip has its query/search_term dropped by
// telemetry.FireFieldsSource — the shared mode's filter is a substring of a
// clip BODY. Do not re-implement that test here: one rule, one place, and this
// is not the place (telemetry.FireFieldsSource says why).
func row(m Model, outcome string) telemetry.Payload {
	return telemetry.Payload{
		Method:        paste.MethodFor(m.query),
		Query:         m.query,
		SearchTerm:    m.query,
		Outcome:       outcome,
		Label:         m.Selected, // "" on cancel — the identity is the label now
		App:           os.Getenv("SNIP_ORIGIN_CLASS"),
		Workspace:     focusedWorkspace(),
		LatencyOpenMS: m.OpenLatencyMS,
	}
}
