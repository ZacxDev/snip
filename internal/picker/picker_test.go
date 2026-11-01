package picker

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/ZacxDev/snip/internal/artifact"
	"github.com/ZacxDev/snip/internal/telemetry"
)

// fakeLauncher records the spawn + toasts; the model's world-touching calls.
type fakeLauncher struct {
	spawned   []string // labels
	queries   []string // the SNIP_QUERY handed to each spawn
	openMS    []int64
	bodyFiles []string // the SNIP paste --body-file handed to each spawn ("" normally)
	spawnErr  error
	toasts    []string

	// the edit path's three calls
	stagedBody string // what StageEdit was handed
	staged     string // the scratch path it returned
	stageErr   error
	readBody   string // what ReadEdit returns (the post-editor contents)
	readErr    error
	applied    []string // labels handed to ApplyEdit
	appliedF   []string // the body files handed to ApplyEdit
	applyErr   error
	deleted    []string // labels handed to ApplyDelete
	deleteErr  error
}

func (f *fakeLauncher) SpawnPaste(label, query string, openLatencyMS int64, bodyFile string) error {
	f.spawned = append(f.spawned, label)
	f.queries = append(f.queries, query)
	f.openMS = append(f.openMS, openLatencyMS)
	f.bodyFiles = append(f.bodyFiles, bodyFile)
	return f.spawnErr
}

func (f *fakeLauncher) StageEdit(label, body string) (string, error) {
	f.stagedBody = body
	if f.stageErr != nil {
		return "", f.stageErr
	}
	if f.staged == "" {
		f.staged = "/tmp/snip-edit-fake.txt"
	}
	return f.staged, nil
}

func (f *fakeLauncher) ReadEdit(path string) (string, error) { return f.readBody, f.readErr }

func (f *fakeLauncher) ApplyEdit(label, bodyFile string) error {
	f.applied = append(f.applied, label)
	f.appliedF = append(f.appliedF, bodyFile)
	return f.applyErr
}

func (f *fakeLauncher) ApplyDelete(label string) error {
	f.deleted = append(f.deleted, label)
	return f.deleteErr
}

func (f *fakeLauncher) Toast(msg string) { f.toasts = append(f.toasts, msg) }

var catalog = []artifact.Snippet{
	{Label: "SSH rig via mesh vpn", SearchTerms: []string{"vpn", "mesh", "remote"}},
	{Label: "SSH desktop (LAN)", SearchTerms: []string{"ssh", "desktop", "wb", "lan", "local"}},
	{Label: "End-of-session ritual", SearchTerms: []string{"end", "session", "handoff"}},
	{Label: "ask clarifying questions", SearchTerms: []string{"ask", "clarifying", "questions"}},
	{Label: "Process feedback: dispatch subagent + elicit scope", SearchTerms: []string{"ask", "feedback", "dispatch"}},
}

func newModel(fx *fakeLauncher) Model { return New(catalog, fx) }

// 🔴 CASE-INSENSITIVE FILTER. The matcher's contract is a PRE-LOWERCASED term
// (lowercased and stripped at entry), but the
// picker hands Rank the RAW typed query, and shift/caps-lock produce uppercase
// text ("SSH DES"). espanso's own search UI is case-insensitive, so a query
// that resolves in lowercase must not go dead because a modifier key was held.
// Watched RED on pre-fix code (Rank passed the raw query straight through).
func TestRankIsCaseInsensitive(t *testing.T) {
	lower := Rank(catalog, "ssh des")
	if len(lower) == 0 || lower[0].Label != "SSH desktop (LAN)" {
		t.Fatalf("lowercase control: want the desktop row, got %+v", lower)
	}
	upper := Rank(catalog, "SSH DES")
	if len(upper) == 0 || upper[0].Label != "SSH desktop (LAN)" {
		t.Fatalf("uppercase query must resolve identically; got %+v", upper)
	}
}

func typeStr(m Model, s string) Model {
	for _, r := range s {
		m = upd(m, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	return m
}

// upd drives one message through Update, asserting the model round-trip.
func upd(m Model, msg tea.Msg) Model {
	next, _ := m.Update(msg)
	return next.(Model)
}

// updCmd is upd, keeping the returned cmd.
func updCmd(m Model, msg tea.Msg) (Model, tea.Cmd) {
	next, cmd := m.Update(msg)
	return next.(Model), cmd
}

// runCmd executes a cmd and unwraps tea.Batch — Update batches the quit cmd
// with the spawn cmd, so the direct return is a BatchMsg carrying BOTH. It
// returns every non-nil message; tests pick the one they mean.
func runCmd(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range batch {
			if c == nil {
				continue
			}
			if m := c(); m != nil {
				out = append(out, m)
			}
		}
		return out
	}
	return []tea.Msg{msg}
}

// spawnedFrom picks the spawned message out of runCmd's results.
func spawnedFrom(msgs []tea.Msg) spawned {
	for _, m := range msgs {
		if sp, ok := m.(spawned); ok {
			return sp
		}
	}
	return spawned{Err: errors.New("no spawned message in " + fmt.Sprint(msgs))}
}

// The picker OPENS SHOWING ALL ROWS — browse mode, artifact order.
func TestOpensShowingAllRows(t *testing.T) {
	m := newModel(&fakeLauncher{})
	if len(m.filtered) != len(catalog) {
		t.Fatalf("open rows = %d, want all %d", len(m.filtered), len(catalog))
	}
	for i, s := range catalog {
		if m.filtered[i].Label != s.Label {
			t.Fatalf("browse order changed at %d: %q != %q", i, m.filtered[i].Label, s.Label)
		}
	}
}

// Typing filters through the PORTED matcher — every token of the query must
// match the row (the multi-word rule), and the rank is declared search_terms
// first, then label prose.
func TestTypingFiltersThroughThePortedMatcher(t *testing.T) {
	m := typeStr(newModel(&fakeLauncher{}), "ssh des")
	got := labels(m)
	if len(got) != 1 || got[0] != "SSH desktop (LAN)" {
		t.Fatalf(`filtered("ssh des") = %v, want the desktop row`, got)
	}
	// Ambiguity means TWO rows in the picker (the feature — nothing is
	// dropped, no owner table applied).
	m = typeStr(newModel(&fakeLauncher{}), "ask")
	got = labels(m)
	if len(got) != 2 || got[0] != "ask clarifying questions" ||
		got[1] != "Process feedback: dispatch subagent + elicit scope" {
		t.Fatalf(`filtered("ask") = %v, want both rows, the ask row first (declared, then…)`, got)
	}
	// Neither row NAMES anything here (the shorthand is gone); both DECLARE
	// "ask" → same group → artifact order.
	if got[0] != "ask clarifying questions" ||
		got[1] != "Process feedback: dispatch subagent + elicit scope" {
		t.Fatalf("declared ties must keep artifact order: %v", got)
	}
}

// The rank: a query that reaches a row through its DECLARED search_terms
// outranks one that only spells it in label prose, and the old trigger
// shorthand resolves nothing any more.
func TestRankOrdering(t *testing.T) {
	// "ssh" is DECLARED by the desktop row (a search_term) and only spelled
	// in the rig row's LABEL → the desktop row ranks first.
	got := labels(typeStr(newModel(&fakeLauncher{}), "ssh"))
	if len(got) != 2 || got[0] != "SSH desktop (LAN)" || got[1] != "SSH rig via mesh vpn" {
		t.Fatalf(`rank("ssh") = %v, want [desktop rig] (declared before prose)`, got)
	}
	// A LABEL-only match still lists.
	ranked := Rank(catalog, "ritual") // only the eos row's LABEL spells "ritual"
	if len(ranked) != 1 || ranked[0].Label != "End-of-session ritual" {
		t.Fatalf(`rank("ritual") = %v, want the eos row via its label`, ranked)
	}
	// 🔴 THE OLD SHORTHAND RESOLVES NOTHING (2026-10-31): ":sshwl" and "eos"
	// named rows when identity was the trigger; with the trigger gone they
	// reach no label word and no search_term.
	if got := labels(typeStr(newModel(&fakeLauncher{}), ":sshwl")); len(got) != 0 {
		t.Fatalf(`rank(":sshwl") = %v, want nothing (the shorthand is gone)`, got)
	}
	if got := labels(typeStr(newModel(&fakeLauncher{}), "eos")); len(got) != 0 {
		t.Fatalf(`rank("eos") = %v, want nothing (the shorthand is gone)`, got)
	}
}

func labels(m Model) []string {
	out := make([]string, len(m.filtered))
	for i, s := range m.filtered {
		out[i] = s.Label
	}
	return out
}

// Arrows move the cursor; Enter selects the row under it.
func TestArrowsAndEnterSelect(t *testing.T) {
	fx := &fakeLauncher{}
	m := newModel(fx)
	m = upd(m, tea.KeyPressMsg{Code: tea.KeyDown})
	m = upd(m, tea.KeyPressMsg{Code: tea.KeyDown})
	if m.cursor != 2 {
		t.Fatalf("cursor = %d, want 2 after two downs", m.cursor)
	}
	m = upd(m, tea.KeyPressMsg{Code: tea.KeyUp})
	if m.cursor != 1 {
		t.Fatalf("cursor = %d, want 1 after up", m.cursor)
	}
	m, cmd := updCmd(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.Selected != "SSH desktop (LAN)" {
		t.Fatalf("Selected = %q, want the row under the cursor", m.Selected)
	}
	if cmd == nil {
		t.Fatal("enter must produce the spawn cmd")
	}
	// The cmd SPAWNS (via the fake) and reports success → the model quits.
	sp := spawnedFrom(runCmd(cmd))
	if sp.Err != nil {
		t.Fatalf("spawn cmd produced %v, want a successful spawned", sp)
	}
	if len(fx.spawned) != 1 || fx.spawned[0] != "SSH desktop (LAN)" {
		t.Fatalf("spawned = %v", fx.spawned)
	}
}

// The spawn carries the picker's handoff: the exact query and the open
// latency (SNIP_QUERY / SNIP_OPEN_LATENCY_MS at the helper).
func TestSpawnCarriesHandoff(t *testing.T) {
	fx := &fakeLauncher{}
	m := typeStr(newModel(fx), "ssh des")
	m.OpenLatencyMS = 123
	_, cmd := updCmd(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	cmd()
	if len(fx.queries) != 1 || fx.queries[0] != "ssh des" {
		t.Fatalf("spawn query = %v, want the exact typed string", fx.queries)
	}
	if len(fx.openMS) != 1 || fx.openMS[0] != 123 {
		t.Fatalf("spawn openMS = %v, want 123", fx.openMS)
	}
}

// Esc cancels: no spawn, Canceled set, quit.
func TestEscCancels(t *testing.T) {
	fx := &fakeLauncher{}
	m := newModel(fx)
	m = upd(m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if !m.Canceled || !m.quit || m.Selected != "" {
		t.Fatalf("esc state: canceled=%v quit=%v selected=%q", m.Canceled, m.quit, m.Selected)
	}
	if len(fx.spawned) != 0 {
		t.Fatal("esc must not spawn anything")
	}
}

// Enter on an EMPTY filtered list is inert (no crash, no empty selection).
func TestEnterOnEmptyListInert(t *testing.T) {
	fx := &fakeLauncher{}
	m := typeStr(newModel(fx), "zzznotfound")
	if len(m.filtered) != 0 {
		t.Fatalf("expected empty filter, got %v", labels(m))
	}
	m2, cmd := updCmd(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m2.Selected != "" || cmd != nil {
		t.Fatalf("enter on empty list must be inert: selected=%q cmd=%v", m2.Selected, cmd)
	}
}

// Backspace shortens the query and re-filters; backspacing to empty returns
// to browse mode.
func TestBackspace(t *testing.T) {
	m := typeStr(newModel(&fakeLauncher{}), "ssh")
	if len(m.filtered) != 2 {
		t.Fatalf(`"ssh" should match both ssh rows, got %v`, labels(m))
	}
	m = upd(m, tea.KeyPressMsg{Code: tea.KeyBackspace})
	if m.query != "ss" {
		t.Fatalf("query = %q, want ss", m.query)
	}
	m = upd(m, tea.KeyPressMsg{Code: tea.KeyBackspace})
	m = upd(m, tea.KeyPressMsg{Code: tea.KeyBackspace})
	if m.query != "" || len(m.filtered) != len(catalog) {
		t.Fatalf("backspace-to-empty must return to browse: query=%q rows=%d", m.query, len(m.filtered))
	}
}

// A MODIFIED key (ctrl+w etc.) must not grow the query.
func TestModifiedKeysDoNotGrowQuery(t *testing.T) {
	m := typeStr(newModel(&fakeLauncher{}), "ss")
	m = upd(m, tea.KeyPressMsg{Code: 'w', Text: "w", Mod: tea.ModCtrl})
	if m.query != "ss" {
		t.Fatalf("ctrl+w grew the query to %q", m.query)
	}
}

// Row emission: a cancel emits EXACTLY ONE outcome=cancel row with text=""
// semantics (Label=""), the typed query, and the derived method.
func TestCancelRowEmitted(t *testing.T) {
	captured := capturePickerRows(t)
	fx := &fakeLauncher{}
	m := typeStr(newModel(fx), "ssh des")
	m = upd(m, tea.KeyPressMsg{Code: tea.KeyEscape})
	m.OpenLatencyMS = 87
	rc := finish(m)
	if rc != 0 {
		t.Fatalf("cancel exit = %d, want 0 (a cancel is a normal outcome)", rc)
	}
	if len(captured.rows) != 1 {
		t.Fatalf("rows = %d, want exactly one cancel row", len(captured.rows))
	}
	p := captured.rows[0]
	if p.Outcome != "cancel" || p.Query != "ssh des" || p.Method != "search" ||
		p.Label != "" || p.LatencyOpenMS != 87 {
		t.Fatalf("cancel row: %+v", p)
	}
}

// A clean cancel after TYPING NOTHING is method=direct.
func TestDirectCancelRow(t *testing.T) {
	captured := capturePickerRows(t)
	m := newModel(&fakeLauncher{})
	m = upd(m, tea.KeyPressMsg{Code: tea.KeyEscape})
	finish(m)
	if len(captured.rows) != 1 || captured.rows[0].Method != "direct" {
		t.Fatalf("rows: %+v", captured.rows)
	}
}

// 🔴 SPAWN FAILURE: the abort row is emitted BY THE PICKER (once), the TUI
// stays open with the failure in words, a toast fires, and the later exit
// emits NO second row — one row per open, always.
func TestSpawnFailureStaysOpenAndEmitsAbortOnce(t *testing.T) {
	captured := capturePickerRows(t)
	fx := &fakeLauncher{spawnErr: errors.New("spawn paste helper: fork failed")}
	m := typeStr(newModel(fx), "ritual")
	m, cmd := updCmd(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.Selected != "End-of-session ritual" {
		t.Fatalf("Selected = %q", m.Selected)
	}
	sp := spawnedFrom(runCmd(cmd))
	if sp.Err == nil {
		t.Fatal("expected the fake's spawn error")
	}
	// Feed the spawned message back through Update (what the program loop
	// does): the model must stay OPEN, marked failed.
	m2, cmd2 := updCmd(m, sp)
	_ = cmd2 // a failed spawn produces no quit cmd — that IS the assertion
	if m2.quit {
		t.Fatal("a spawn failure must keep the TUI open with the failure in words")
	}
	if !m2.SpawnFailed || m2.notice == "" {
		t.Fatalf("spawn failure state: failed=%v notice=%q", m2.SpawnFailed, m2.notice)
	}
	if len(fx.toasts) != 1 {
		t.Fatalf("toasts: %v", fx.toasts)
	}
	if len(captured.rows) != 1 || captured.rows[0].Outcome != "abort" {
		t.Fatalf("rows after failure: %+v", captured.rows)
	}
	// The operator then presses Esc: quiet exit, NO second row.
	m3 := upd(m2, tea.KeyPressMsg{Code: tea.KeyEscape})
	rc := finish(m3)
	if rc != 1 {
		t.Fatalf("spawn-failure exit = %d, want 1", rc)
	}
	if len(captured.rows) != 1 {
		t.Fatalf("rows = %d, want STILL exactly one (no double row)", len(captured.rows))
	}
}

// Run with no effect surface refuses in words (the mis-wired caller case) —
// this one Run path exits BEFORE the TTY is touched, so the test can call it.
func TestRunWithoutSurfaceRefuses(t *testing.T) {
	m := New(catalog, nil)
	m.fx = nil
	if rc := Run(m, 0); rc != 1 {
		t.Fatalf("exit = %d, want 1", rc)
	}
}

// --- the edit keybind (ctrl+e) ------------------------------------------------

// A dedicated catalog: the edit path carries the BODY, and the shared catalog
// above has none.
var editCatalog = []artifact.Snippet{
	{Label: "Merge rule", Body: "merge after a round with no high sev findings", Category: "prompt"},
	{Label: "project abs path", Body: "${workspace}/dev", Category: "path"},
}

func stagedFrom(msgs []tea.Msg) editStaged {
	for _, m := range msgs {
		if st, ok := m.(editStaged); ok {
			return st
		}
	}
	return editStaged{Err: errors.New("no editStaged message in " + fmt.Sprint(msgs))}
}

func editReadFrom(t *testing.T, msgs []tea.Msg) editRead {
	t.Helper()
	for _, m := range msgs {
		if er, ok := m.(editRead); ok {
			return er
		}
	}
	t.Fatalf("no editRead message in %v", msgs)
	return editRead{}
}

// ctrl+e stages the row's CURRENT body under the cursor. The staged text is
// taken from the loaded artifact, not re-read after the editor runs — a config
// change in between must not silently become the new baseline.
func TestCtrlEStagesTheSelectedBody(t *testing.T) {
	fx := &fakeLauncher{}
	m := New(editCatalog, fx)
	m, cmd := updCmd(m, tea.KeyPressMsg{Code: 'e', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal("ctrl+e must produce the staging cmd")
	}
	if m.editLabel != "Merge rule" {
		t.Fatalf("editLabel = %q, want the row under the cursor", m.editLabel)
	}
	st := stagedFrom(runCmd(cmd))
	if st.Err != nil {
		t.Fatalf("staging: %v", st.Err)
	}
	if fx.stagedBody != "merge after a round with no high sev findings" {
		t.Fatalf("staged body = %q, want the artifact's body", fx.stagedBody)
	}
	// Feeding the message back records the scratch path and hands the terminal
	// to the editor.
	m2, ec := updCmd(m, st)
	if m2.editPath != fx.staged {
		t.Fatalf("editPath = %q, want %q", m2.editPath, fx.staged)
	}
	if ec == nil {
		t.Fatal("editStaged must return the editor cmd (tea.ExecProcess)")
	}
}

// ctrl+e on an empty filtered list is inert, like Enter.
func TestCtrlEOnEmptyListIsInert(t *testing.T) {
	fx := &fakeLauncher{}
	m := typeStr(New(editCatalog, fx), "zzznotfound")
	if len(m.filtered) != 0 {
		t.Fatalf("expected an empty filter, got %v", labels(m))
	}
	m2, cmd := updCmd(m, tea.KeyPressMsg{Code: 'e', Mod: tea.ModCtrl})
	if cmd != nil || m2.editLabel != "" {
		t.Fatalf("ctrl+e on an empty list must be inert: label=%q cmd=%v", m2.editLabel, cmd)
	}
}

// A staging failure keeps the TUI open with the failure in words and never
// reaches the editor.
func TestEditStagingFailureStaysOpen(t *testing.T) {
	fx := &fakeLauncher{stageErr: errors.New("scratch file: no space left")}
	m := New(editCatalog, fx)
	m, cmd := updCmd(m, tea.KeyPressMsg{Code: 'e', Mod: tea.ModCtrl})
	m2, ec := updCmd(m, stagedFrom(runCmd(cmd)))
	if ec != nil {
		t.Fatal("a failed staging must not open an editor")
	}
	if !strings.Contains(m2.notice, "no space left") || len(fx.toasts) == 0 {
		t.Fatalf("staging failure: notice=%q toasts=%v", m2.notice, fx.toasts)
	}
}

// The body coming back byte-identical (no edit, or the editor exited without
// saving) commits NOTHING and spawns nothing.
func TestEditUnchangedCommitsNothing(t *testing.T) {
	captured := capturePickerRows(t)
	fx := &fakeLauncher{readBody: "merge after a round with no high sev findings"}
	m := New(editCatalog, fx)
	m, cmd := updCmd(m, tea.KeyPressMsg{Code: 'e', Mod: tea.ModCtrl})
	m = upd(m, stagedFrom(runCmd(cmd)))
	m, landCmd := updCmd(m, editorDone{})
	er := editReadFrom(t, runCmd(landCmd))
	if !er.Unchanged || er.Err != nil || er.Ready {
		t.Fatalf("editRead = %+v, want Unchanged", er)
	}
	m2, spawnCmd := updCmd(m, er)
	if spawnCmd != nil {
		t.Fatal("an unchanged body must not spawn a paste")
	}
	if m2.quit {
		t.Fatal("an unchanged body must leave the TUI open")
	}
	if len(fx.applied) != 0 {
		t.Fatalf("ApplyEdit called for an unchanged body: %v", fx.applied)
	}
	if len(captured.rows) != 0 {
		t.Fatalf("no telemetry row for a no-op edit, got %+v", captured.rows)
	}
}

// 🔴 A REFUSED LANDING KEEPS THE TUI OPEN WITH THE REASON. The operator is
// standing in this window; it is the only surface that can say why the edit did
// not land, and staying open is what lets them fix it and press ctrl+e again.
func TestEditRefusalStaysOpenWithTheReason(t *testing.T) {
	fx := &fakeLauncher{
		readBody: "a new body",
		applyErr: errors.New("snip-edit: the index already holds staged changes"),
	}
	m := New(editCatalog, fx)
	m, cmd := updCmd(m, tea.KeyPressMsg{Code: 'e', Mod: tea.ModCtrl})
	m = upd(m, stagedFrom(runCmd(cmd)))
	m, landCmd := updCmd(m, editorDone{})
	er := editReadFrom(t, runCmd(landCmd))
	if er.Err == nil {
		t.Fatal("a refused landing must report the reason")
	}
	m2, spawnCmd := updCmd(m, er)
	if m2.quit || spawnCmd != nil {
		t.Fatal("a refusal must stay open and paste nothing")
	}
	if !strings.Contains(m2.notice, "staged changes") {
		t.Fatalf("notice = %q, want the script's own refusal line", m2.notice)
	}
	if len(fx.toasts) == 0 {
		t.Fatal("a refusal must also toast")
	}
}

// 🔴 THE SUCCESS PATH: land, then paste the NEW body from the scratch file, then
// close the TUI — and emit NO picker row, because the helper owns the fire row.
func TestEditSuccessPastesTheNewBodyFromTheScratchFile(t *testing.T) {
	captured := capturePickerRows(t)
	fx := &fakeLauncher{readBody: "a NEW body"}
	m := New(editCatalog, fx)
	m.OpenLatencyMS = 42
	m, cmd := updCmd(m, tea.KeyPressMsg{Code: 'e', Mod: tea.ModCtrl})
	m = upd(m, stagedFrom(runCmd(cmd)))
	m, landCmd := updCmd(m, editorDone{})
	er := editReadFrom(t, runCmd(landCmd))
	if !er.Ready || er.Err != nil {
		t.Fatalf("editRead = %+v, want Ready", er)
	}
	if len(fx.applied) != 1 || fx.applied[0] != "Merge rule" {
		t.Fatalf("applied = %v", fx.applied)
	}
	if len(fx.appliedF) != 1 || fx.appliedF[0] != fx.staged {
		t.Fatalf("ApplyEdit body file = %v, want %q", fx.appliedF, fx.staged)
	}

	m, spawnCmd := updCmd(m, er)
	sp := spawnedFrom(runCmd(spawnCmd))
	if sp.Err != nil {
		t.Fatalf("spawn: %v", sp.Err)
	}
	// 🔴 THE BODY FILE IS THE POINT: the deployed artifact does not carry the
	// edit yet, so a spawn without it would paste the OLD body.
	if len(fx.bodyFiles) != 1 || fx.bodyFiles[0] != fx.staged {
		t.Fatalf("bodyFiles = %v, want the scratch file %q", fx.bodyFiles, fx.staged)
	}
	if len(fx.spawned) != 1 || fx.spawned[0] != "Merge rule" {
		t.Fatalf("spawned = %v", fx.spawned)
	}
	if len(fx.queries) != 1 || fx.openMS[0] != 42 {
		t.Fatalf("the handoff must ride along: queries=%v openMS=%v", fx.queries, fx.openMS)
	}

	m, _ = updCmd(m, sp)
	if !m.quit {
		t.Fatal("a landed edit must close the TUI so the paste lands in the origin")
	}
	if rc := finish(m); rc != 0 {
		t.Fatalf("finish = %d, want 0", rc)
	}
	if len(captured.rows) != 0 {
		t.Fatalf("the helper owns the fire row; the picker emitted %+v", captured.rows)
	}
}

// The footer advertises the key — on a wide terminal, where it fits.
func TestFooterAdvertisesTheEditKey(t *testing.T) {
	m := New(editCatalog, &fakeLauncher{})
	m.Width, m.Height = 200, 24
	if !strings.Contains(m.footer(), "ctrl+e") {
		t.Fatalf("footer = %q, want it to name ctrl+e", m.footer())
	}
}

// The editor is resolved by ONE rule, and its value may carry flags.
func TestEditorCommandResolution(t *testing.T) {
	// SNIP_EDITOR wins over the conventional pair.
	t.Setenv("SNIP_EDITOR", "nvim -f")
	t.Setenv("VISUAL", "code")
	t.Setenv("EDITOR", "vi")
	if got := editorCommand("/tmp/x").Args; got[0] != "nvim" || got[1] != "-f" || got[2] != "/tmp/x" {
		t.Fatalf("argv = %v, want [nvim -f /tmp/x]", got)
	}
	// VISUAL wins over EDITOR when snip's own var is unset.
	t.Setenv("SNIP_EDITOR", "")
	t.Setenv("VISUAL", "code --wait")
	if got := editorCommand("/tmp/x").Args; got[0] != "code" || got[1] != "--wait" || got[2] != "/tmp/x" {
		t.Fatalf("argv = %v, want [code --wait /tmp/x]", got)
	}
	// …then EDITOR, then the default.
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "vi")
	if got := editorCommand("/tmp/x").Args; got[0] != "vi" {
		t.Fatalf("argv = %v, want vi", got)
	}
	t.Setenv("EDITOR", "")
	if got := editorCommand("/tmp/x").Args; got[0] != "nvim" {
		t.Fatalf("argv = %v, want the nvim default", got)
	}
}

// --- the browse order the caller supplies -------------------------------------

// 🔴 THE TWO ORDERS ARE DIFFERENT, AND THE DIFFERENCE IS THE REQUIREMENT. The
// unfiltered list is exactly what the caller handed in — cmd/snip hands the
// RECENCY order — and the first typed character switches to the matcher's
// precedence. Clearing the query returns to the browse order rather than
// sticking on the matcher's.
func TestTypingSwitchesFromBrowseOrderToMatcherOrder(t *testing.T) {
	// Browse order: the row the query names is LAST, and the leading row has no
	// search terms that could match it.
	browse := []artifact.Snippet{
		{Label: "recently used"},
		{Label: "desktop path", SearchTerms: []string{"zzz"}},
	}
	m := New(browse, &fakeLauncher{})
	if got := labels(m); len(got) != 2 || got[0] != "recently used" {
		t.Fatalf("browse must be the order handed in, got %v", got)
	}
	m = typeStr(m, "zzz")
	if got := labels(m); len(got) != 1 || got[0] != "desktop path" {
		t.Fatalf("a query must switch to the matcher's order, got %v", got)
	}
	for i := 0; i < 3; i++ {
		m = upd(m, tea.KeyPressMsg{Code: tea.KeyBackspace})
	}
	if m.query != "" {
		t.Fatalf("query = %q, want empty", m.query)
	}
	if got := labels(m); got[0] != "recently used" {
		t.Fatalf("clearing the query must return to the browse order, got %v", got)
	}
}

// --- the emptied body: the DELETE gesture --------------------------------------

// 🔴 THE REGRESSION PIN FOR 2026-11-01. The operator cleared a snippet's text to
// delete it; the body came back empty, was landed as `replace = ""`, and became
// a LIVE ROW THAT PASTES NOTHING while still recording a fire. Green before the
// fix, red after it is the wrong way round — this asserts the landed state did
// NOT happen, and that the confirm is up instead.
func TestAnEmptiedBodyAsksToDeleteInsteadOfLanding(t *testing.T) {
	fx := &fakeLauncher{readBody: ""}
	m := New(editCatalog, fx)
	m, cmd := updCmd(m, tea.KeyPressMsg{Code: 'e', Mod: tea.ModCtrl})
	m = upd(m, stagedFrom(runCmd(cmd)))
	m, landCmd := updCmd(m, editorDone{})
	er := editReadFrom(t, runCmd(landCmd))
	if !er.Empty || er.Ready || er.Err != nil {
		t.Fatalf("editRead = %+v, want Empty", er)
	}

	m2, spawnCmd := updCmd(m, er)
	if spawnCmd != nil {
		t.Fatal("an emptied body must not paste anything")
	}
	if m2.quit {
		t.Fatal("an emptied body must not close the TUI — it asks first")
	}
	if len(fx.applied) != 0 {
		t.Fatalf("an emptied body must NOT be landed as a body update: %v", fx.applied)
	}
	if m2.confirmDelete != "Merge rule" {
		t.Fatalf("confirmDelete = %q, want the row under the cursor", m2.confirmDelete)
	}
	if !strings.Contains(m2.notice, "delete") || !strings.Contains(m2.notice, "[y/N]") {
		t.Fatalf("the prompt must say what it is asking: %q", m2.notice)
	}
}

// A body that is only whitespace is the same gesture: an editor that saves a
// lone newline for an emptied buffer must not become a snippet that pastes a
// newline.
func TestAWhitespaceOnlyBodyIsAlsoTheDeleteGesture(t *testing.T) {
	for _, body := range []string{"\n", "   ", "\t\n"} {
		fx := &fakeLauncher{readBody: body}
		m := New(editCatalog, fx)
		m, cmd := updCmd(m, tea.KeyPressMsg{Code: 'e', Mod: tea.ModCtrl})
		m = upd(m, stagedFrom(runCmd(cmd)))
		_, landCmd := updCmd(m, editorDone{})
		if er := editReadFrom(t, runCmd(landCmd)); !er.Empty {
			t.Fatalf("body %q: editRead = %+v, want Empty", body, er)
		}
	}
}

// The prompt is MODAL: its own answers work, and every other keystroke is
// swallowed rather than leaking into the query (or being the Enter that pastes).
func TestTheDeletePromptIsModal(t *testing.T) {
	fx := &fakeLauncher{readBody: ""}
	m := New(editCatalog, fx)
	m, cmd := updCmd(m, tea.KeyPressMsg{Code: 'e', Mod: tea.ModCtrl})
	m = upd(m, stagedFrom(runCmd(cmd)))
	m, landCmd := updCmd(m, editorDone{})
	m = upd(m, editReadFrom(t, runCmd(landCmd)))
	if m.confirmDelete == "" {
		t.Fatal("expected the prompt to be up")
	}

	// Enter must not paste, and letters must not become a query.
	for _, k := range []tea.Msg{
		tea.KeyPressMsg{Code: tea.KeyEnter},
		tea.KeyPressMsg{Code: 'x', Text: "x"},
		tea.KeyPressMsg{Code: tea.KeyDown},
	} {
		m2, c := updCmd(m, k)
		if c != nil {
			t.Fatalf("%v produced a cmd while the prompt was up", k)
		}
		if m2.query != "" {
			t.Fatalf("%v leaked into the query: %q", k, m2.query)
		}
		if m2.confirmDelete == "" {
			t.Fatalf("%v dismissed the prompt", k)
		}
	}
}

func TestDeleteConfirmNoCancelsTheDeletion(t *testing.T) {
	for _, dismiss := range []tea.KeyPressMsg{
		{Code: 'n', Text: "n"},
		{Code: tea.KeyEscape},
	} {
		fx := &fakeLauncher{readBody: ""}
		m := New(editCatalog, fx)
		m, cmd := updCmd(m, tea.KeyPressMsg{Code: 'e', Mod: tea.ModCtrl})
		m = upd(m, stagedFrom(runCmd(cmd)))
		m, landCmd := updCmd(m, editorDone{})
		m = upd(m, editReadFrom(t, runCmd(landCmd)))

		m2, c := updCmd(m, dismiss)
		if c != nil {
			t.Fatalf("%v must not produce a cmd", dismiss)
		}
		if m2.confirmDelete != "" || m2.quit {
			t.Fatalf("%v: confirmDelete=%q quit=%v", dismiss, m2.confirmDelete, m2.quit)
		}
		if len(fx.deleted) != 0 {
			t.Fatalf("%v still deleted %v", dismiss, fx.deleted)
		}
		if !strings.Contains(m2.notice, "cancelled") {
			t.Fatalf("%v: notice = %q", dismiss, m2.notice)
		}
	}
}

// 🔴 THE WHOLE POINT: yes deletes the ROW, pastes NOTHING, and owes NO row.
func TestDeleteConfirmYesLandsAndPastesNothing(t *testing.T) {
	captured := capturePickerRows(t)
	fx := &fakeLauncher{readBody: ""}
	m := New(editCatalog, fx)
	m, cmd := updCmd(m, tea.KeyPressMsg{Code: 'e', Mod: tea.ModCtrl})
	m = upd(m, stagedFrom(runCmd(cmd)))
	m, landCmd := updCmd(m, editorDone{})
	m = upd(m, editReadFrom(t, runCmd(landCmd)))

	// y → the ConfirmDelete intent → the landing call.
	m, delCmd := updCmd(m, tea.KeyPressMsg{Code: 'y', Text: "y"})
	if delCmd == nil {
		t.Fatal("y must produce the delete cmd")
	}
	// 🔴 RUN IT: the landing call happens INSIDE the cmd, so asserting on the
	// fake before running it would assert on nothing.
	var dd deleteDone
	for _, mm := range runCmd(delCmd) {
		if v, ok := mm.(deleteDone); ok {
			dd = v
		}
	}
	if len(fx.deleted) != 1 || fx.deleted[0] != "Merge rule" {
		t.Fatalf("deleted = %v, want the row under the cursor", fx.deleted)
	}
	m, _ = updCmd(m, dd)
	if !m.quit {
		t.Fatal("a landed deletion must close the TUI")
	}
	if len(fx.spawned) != 0 {
		t.Fatalf("a deletion pastes nothing, but spawned %v", fx.spawned)
	}
	if m.Deleted != "Merge rule" {
		t.Fatalf("Deleted = %q", m.Deleted)
	}
	if rc := finish(m); rc != 0 {
		t.Fatalf("finish = %d, want 0", rc)
	}
	if len(captured.rows) != 0 {
		t.Fatalf("a deletion is neither a use nor a cancel, but emitted %+v", captured.rows)
	}
	if len(fx.toasts) == 0 {
		t.Fatal("the deletion should toast — it is the only thing that survives the window")
	}
}

// A refused deletion keeps the TUI open with the reason and commits nothing.
func TestDeleteFailureStaysOpenWithTheReason(t *testing.T) {
	fx := &fakeLauncher{readBody: "", deleteErr: errors.New("snip-edit: the row is not gone from the evaluated artifact")}
	m := New(editCatalog, fx)
	m, cmd := updCmd(m, tea.KeyPressMsg{Code: 'e', Mod: tea.ModCtrl})
	m = upd(m, stagedFrom(runCmd(cmd)))
	m, landCmd := updCmd(m, editorDone{})
	m = upd(m, editReadFrom(t, runCmd(landCmd)))
	m, _ = updCmd(m, tea.KeyPressMsg{Code: 'y', Text: "y"})
	m2, _ := updCmd(m, deleteDone{Err: fx.deleteErr})
	if m2.quit || m2.Deleted != "" {
		t.Fatalf("a refused deletion must stay open: quit=%v deleted=%q", m2.quit, m2.Deleted)
	}
	if !strings.Contains(m2.notice, "not gone") {
		t.Fatalf("notice = %q, want the script's own refusal", m2.notice)
	}
	if m2.confirmDelete != "" {
		t.Fatal("the prompt must be cleared after an attempt")
	}
}

// --- row-emitter capture -----------------------------------------------------

type capturedRows struct{ rows []telemetry.Payload }

func capturePickerRows(t *testing.T) *capturedRows {
	c := &capturedRows{}
	orig := emitFireFn
	emitFireFn = func(p telemetry.Payload) string {
		c.rows = append(c.rows, p)
		return ""
	}
	t.Cleanup(func() { emitFireFn = orig })
	return c
}
