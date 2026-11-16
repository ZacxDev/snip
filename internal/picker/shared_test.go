package picker

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/ZacxDev/snip/internal/shared"
	"github.com/ZacxDev/snip/internal/telemetry"
)

// 🔴 MOSTLY INVARIANT GUARDS, NOT REGRESSION COVERAGE. The shared mode is new
// in the same commit, so there is no pre-change code these could be red
// against; they pin the properties the design settled on. Each was watched to
// go red against a deliberately broken copy of the thing it guards, with that
// guard's own message — the matrix is in the PR description, not restated per
// test.
//
// The one test here that IS red on code without the feature is
// TestTheSecondKeypressDoesNotGrowTheQuery's sibling property: a chord that
// bubbletea reports with Mod == 0 would type itself into the filter.

var clipRows = []shared.Clip{
	{ID: 9, Body: "github.com password hunter2", Device: "phone", CreatedAt: 1_700_000_000_000, BumpCount: 0},
	{ID: 8, Body: "Ärger über Äpfel", Device: "phone", CreatedAt: 1_699_999_000_000, BumpCount: 1},
	{ID: 7, Body: "a multi\nline body", Device: "laptop", CreatedAt: 1_699_000_000_000, BumpCount: 0},
}

// key builds the chord message for a key string bubbletea would report.
func ctrl(r rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: r, Mod: tea.ModCtrl}
}

// enterShared presses the second keypress and runs the fetch cmd, so the model
// comes back with whatever the fake was loaded with.
func enterShared(t *testing.T, fx *fakeLauncher) Model {
	t.Helper()
	m, cmd := updCmd(newModel(fx), ctrl('g'))
	if m.mode != modeShared {
		t.Fatalf("%s did not switch the mode", SharedKey)
	}
	for _, msg := range runCmd(cmd) {
		m = upd(m, msg)
	}
	return m
}

// 🔴 THE FETCH IS LAZY. Building the model must make NO fetch: the picker's
// open latency is an acceptance-criteria number measured from process entry to
// the first render, and an eager fetch would put a mesh round trip inside
// every open — including the vast majority that never ask for a clip.
//
// This pins the STATE (the fake's call count), not a spelling, so it cannot be
// satisfied by renaming anything.
func TestNewMakesNoFetch(t *testing.T) {
	fx := &fakeLauncher{clips: clipRows}
	m := newModel(fx)
	_ = m.Lines() // the first render, which runPicker warms synchronously
	if fx.fetches != 0 {
		t.Fatalf("New + the first render made %d fetches, want 0 — the fetch must "+
			"be fired by the keypress, never by New", fx.fetches)
	}
	// POSITIVE CONTROL: the fake can count, so the zero above is a real zero.
	if m2 := enterShared(t, fx); fx.fetches != 1 {
		t.Fatalf("the keypress made %d fetches, want 1 (mode=%v)", fx.fetches, m2.mode)
	}
}

// The second keypress switches mode, clears the query, and fires exactly one
// fetch intent.
func TestTheSecondKeypressOpensTheSharedMode(t *testing.T) {
	fx := &fakeLauncher{clips: clipRows}
	m := typeStr(newModel(fx), "ssh")
	next, intents := m.Step(ctrl('g'))
	if next.mode != modeShared {
		t.Fatalf("mode = %v, want shared", next.mode)
	}
	if next.query != "" {
		t.Errorf("query = %q, want it cleared — a catalog filter has no meaning over clips", next.query)
	}
	if len(intents) != 1 {
		t.Fatalf("intents = %+v, want exactly one FetchShared", intents)
	}
	if _, ok := intents[0].(FetchShared); !ok {
		t.Fatalf("intent = %T, want FetchShared", intents[0])
	}
}

// 🔴 THE SECOND KEYPRESS MUST NOT GROW THE QUERY. Every plain printable key is
// taken by the filter, so an unmodified key would type itself in instead of
// opening the shared view. This is the property that forced a ctrl chord.
func TestTheSecondKeypressDoesNotGrowTheQuery(t *testing.T) {
	fx := &fakeLauncher{clips: clipRows}
	m, _ := newModel(fx).Step(ctrl('g'))
	if m.query != "" {
		t.Fatalf("query = %q after %s — the chord typed itself into the filter", m.query, SharedKey)
	}
	// POSITIVE CONTROL: an UNMODIFIED 'g' does grow the query, so the check
	// above is about the modifier and not about Step ignoring text.
	plain, _ := newModel(fx).Step(tea.KeyPressMsg{Code: 'g', Text: "g"})
	if plain.query != "g" {
		t.Fatalf("an unmodified key did not grow the query (%q) — this control proves nothing", plain.query)
	}
}

// The fetched rows land in the model in the SERVER'S ORDER, and the header
// counts the clip population rather than the catalog's.
func TestTheFetchedRowsLandInServerOrder(t *testing.T) {
	m := enterShared(t, &fakeLauncher{clips: clipRows})
	if len(m.filteredClips) != 3 {
		t.Fatalf("got %d rows, want 3", len(m.filteredClips))
	}
	if m.filteredClips[0].ID != 9 || m.filteredClips[2].ID != 7 {
		t.Errorf("order = %d..%d, want 9..7 exactly as served",
			m.filteredClips[0].ID, m.filteredClips[2].ID)
	}
	head := stripANSI(m.Lines()[0])
	if !strings.Contains(head, "shared") || !strings.Contains(head, "3/3") {
		t.Errorf("header = %q, want the shared mode word and the CLIP counts", head)
	}
}

// 🔴 A FAILED FETCH IS NOT AN EMPTY LIST, AND THE PICKER STAYS OPEN. The
// reason is rendered in words, the catalog still works, and the list must not
// fall back on browse mode's "(no match)" line — which is the empty-result
// conflation this whole feature is written against.
func TestAFailedFetchSaysWhyAndIsNotAnEmptyList(t *testing.T) {
	fx := &fakeLauncher{fetchErr: errors.New("clip store unreachable: connection refused")}
	m := enterShared(t, fx)
	if m.quit || m.Canceled {
		t.Fatal("a failed fetch must leave the picker open")
	}
	joined := strings.Join(bareLines(m), "\n")
	if !strings.Contains(joined, "connection refused") {
		t.Errorf("the reason is not on screen: %q", joined)
	}
	if strings.Contains(joined, "no match") {
		t.Errorf("a failed fetch rendered browse mode's empty-filter line — the "+
			"operator cannot tell a dead mesh from an empty clipboard: %q", joined)
	}
	if len(fx.toasts) == 0 {
		t.Error("a failed fetch owes a toast as well as the notice")
	}
}

// 🔴 THREE RENDERS, NOT TWO: fetching / reachable-but-empty / unreachable.
// Each must be distinguishable from the other two by its own words.
func TestThreeRendersAreDistinguishable(t *testing.T) {
	// 1. fetching — the keypress has switched mode, the cmd has not run yet.
	fetching, _ := updCmd(newModel(&fakeLauncher{clips: clipRows}), ctrl('g'))
	fetchingTxt := strings.Join(bareLines(fetching), "\n")
	if !strings.Contains(fetchingTxt, "fetching") {
		t.Errorf("the in-flight render does not say so: %q", fetchingTxt)
	}

	// 2. reachable, but the store holds nothing.
	emptyM := enterShared(t, &fakeLauncher{clips: nil})
	emptyTxt := strings.Join(bareLines(emptyM), "\n")
	if !strings.Contains(emptyTxt, "empty") {
		t.Errorf("the reachable-but-empty render does not say so: %q", emptyTxt)
	}
	if strings.Contains(emptyTxt, "fetching") {
		t.Errorf("a settled empty store still says fetching: %q", emptyTxt)
	}

	// 3. unreachable.
	deadM := enterShared(t, &fakeLauncher{fetchErr: errors.New("i/o timeout")})
	deadTxt := strings.Join(bareLines(deadM), "\n")
	if !strings.Contains(deadTxt, "i/o timeout") {
		t.Errorf("the unreachable render does not carry the reason: %q", deadTxt)
	}
	if strings.Contains(deadTxt, "empty") {
		t.Errorf("an unreachable store rendered as an empty one: %q", deadTxt)
	}
}

// Typing filters the CLIP population through RankShared — a substring test
// over the body, which is the only searchable text a clip has.
func TestTypingFiltersTheClips(t *testing.T) {
	m := typeStr(enterShared(t, &fakeLauncher{clips: clipRows}), "pass")
	if len(m.filteredClips) != 1 || m.filteredClips[0].ID != 9 {
		t.Fatalf("filtered = %+v, want only clip 9", m.filteredClips)
	}
	head := stripANSI(m.Lines()[0])
	if !strings.Contains(head, "shared filter") || !strings.Contains(head, "1/3") {
		t.Errorf("header = %q, want the shared filter mode and 1/3", head)
	}
}

// RankShared's semantics, pinned with literal expectations.
//
// 🔴 IT IS NOT THE SERVER'S `?q=`. A substring test over the body is what runs
// locally; the endpoint's trigram index matches across word boundaries and
// folds case by query LENGTH. These cases pin the LOCAL rule only.
func TestRankShared(t *testing.T) {
	ids := func(cs []shared.Clip) []int64 {
		var out []int64
		for _, c := range cs {
			out = append(out, c.ID)
		}
		return out
	}
	cases := []struct {
		query string
		want  []int64
	}{
		{"", []int64{9, 8, 7}},      // browse: verbatim, no reordering
		{"   ", []int64{9, 8, 7}},   // whitespace is browse too
		{"pass", []int64{9}},        // substring, mid-word
		{"PASS", []int64{9}},        // case-folded
		{"github pass", []int64{9}}, // AND across tokens, any order
		{"pass github", []int64{9}}, //   ...order does not matter
		{"pass zzz", nil},           // one token missing ⇒ no row
		{"line", []int64{7}},        // a body with a newline in it still matches
		{"Ärger", []int64{8}},       // non-ASCII, same case
		{"ärger", []int64{8}},       // non-ASCII, folded
		{"zzz", nil},                // nothing matches ⇒ empty, not everything
	}
	for _, c := range cases {
		got := ids(RankShared(clipRows, c.query))
		if len(got) != len(c.want) {
			t.Errorf("RankShared(%q) = %v, want %v", c.query, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("RankShared(%q) = %v, want %v", c.query, got, c.want)
				break
			}
		}
	}
}

// 🔴 NO REORDERING IS APPLIED TO THE CLIP LIST — not even the usage order the
// sibling catalog browse uses. Chronological newest-first IS the requirement,
// and a clipboard re-sorted by anything else has lost the only property it is
// for. Pinned by feeding a deliberately NON-chronological server answer: the
// model must hand it back exactly as given rather than "fixing" it.
func TestTheClipListIsNeverReordered(t *testing.T) {
	scrambled := []shared.Clip{
		{ID: 1, Body: "oldest stamp, served first", CreatedAt: 1},
		{ID: 2, Body: "newest stamp, served second", CreatedAt: 9_999_999_999_999},
	}
	m := enterShared(t, &fakeLauncher{clips: scrambled})
	if m.filteredClips[0].ID != 1 || m.filteredClips[1].ID != 2 {
		t.Fatalf("the model re-sorted the server's answer: %+v", m.filteredClips)
	}
}

// 🔴 Rank IS UNTOUCHED BY ANY OF THIS. The repo's audit tooling models the
// picker with exactly that matcher, so a clip must never reach it and the
// catalog population must never change shape. Entering and leaving the shared
// mode leaves the browse list byte-identical.
func TestTheCatalogListIsUnaffected(t *testing.T) {
	fx := &fakeLauncher{clips: clipRows}
	before := labels(newModel(fx))
	m := enterShared(t, fx)
	m = upd(m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.mode != modeBrowse {
		t.Fatalf("esc did not return to browse (mode=%v)", m.mode)
	}
	after := labels(m)
	if strings.Join(before, "|") != strings.Join(after, "|") {
		t.Fatalf("the catalog list changed across a shared-mode round trip:\n%v\n%v", before, after)
	}
	// And the clips are still held: coming back must not re-fetch.
	if fx.fetches != 1 {
		t.Errorf("fetches = %d after one round trip, want 1", fx.fetches)
	}
}

// 🔴 THE MODES ARE DISJOINT. ctrl+e means "edit this catalog row and land it
// through the landing script", which has no meaning for a row from another
// device — in shared mode it must be SWALLOWED, not fall through to the
// catalog arm where it would act on a row the cursor is not even pointing at.
func TestTheSharedKeyTableIsDisjoint(t *testing.T) {
	fx := &fakeLauncher{clips: clipRows}
	m := enterShared(t, fx)
	m, intents := m.Step(ctrl('e'))
	if len(intents) != 0 {
		t.Fatalf("ctrl+e in shared mode produced %+v, want nothing", intents)
	}
	if m.editLabel != "" || m.editBody != "" {
		t.Errorf("ctrl+e staged an edit from shared mode: label=%q", m.editLabel)
	}
	if fx.stagedBody != "" {
		t.Errorf("ctrl+e reached StageEdit from shared mode: %q", fx.stagedBody)
	}
	// POSITIVE CONTROL: ctrl+e DOES stage an edit in browse mode, so the
	// silence above is about the mode and not about ctrl+e being dead.
	_, browseIntents := newModel(fx).Step(ctrl('e'))
	if len(browseIntents) != 1 {
		t.Fatalf("ctrl+e in browse mode produced %+v — this control proves nothing", browseIntents)
	}
}

// 🔴 THE BODY TRAVELS WITH THE PASTE. Enter hands the clip's body to the
// spawn, so the detached helper never re-looks-up a row that a delete from
// another device may have removed in the meantime.
func TestEnterPastesTheClipBody(t *testing.T) {
	fx := &fakeLauncher{clips: clipRows}
	m := typeStr(enterShared(t, fx), "pass")
	m, cmd := updCmd(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.quit {
		t.Fatal("quit must wait for the spawn OUTCOME, not fire with the keypress")
	}
	msgs := runCmd(cmd)
	for _, msg := range msgs {
		m = upd(m, msg)
	}
	if len(fx.clipIDs) != 1 || fx.clipIDs[0] != 9 {
		t.Fatalf("spawned clip ids = %v, want [9]", fx.clipIDs)
	}
	if fx.clipBodies[0] != "github.com password hunter2" {
		t.Errorf("spawned body = %q, want the clip's body verbatim", fx.clipBodies[0])
	}
	if fx.clipQuery[0] != "pass" {
		t.Errorf("spawned query = %q, want the typed query handed over", fx.clipQuery[0])
	}
	if len(fx.spawned) != 0 {
		t.Errorf("the CATALOG spawn was used for a clip: %v", fx.spawned)
	}
	if m.ClipPasted != 9 {
		t.Errorf("ClipPasted = %d, want 9", m.ClipPasted)
	}
	if !m.quit {
		t.Error("a successful spawn must close the TUI")
	}
}

// A spawn failure in shared mode behaves like it does in browse mode: the
// window stays open with the reason, because nothing was launched and this is
// the only surface that can say why.
func TestAClipSpawnFailureStaysOpen(t *testing.T) {
	fx := &fakeLauncher{clips: clipRows, spawnErr: errors.New("no such file")}
	rows := capturePickerRows(t)
	m := enterShared(t, fx)
	m, cmd := updCmd(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	for _, msg := range runCmd(cmd) {
		m = upd(m, msg)
	}
	if !m.SpawnFailed {
		t.Fatal("the spawn failure was not recorded")
	}
	if m.quit {
		t.Fatal("a spawn failure must keep the window open")
	}
	if !strings.Contains(strings.Join(bareLines(m), "\n"), "no such file") {
		t.Error("the spawn failure's reason is not on screen")
	}
	// 🔴 THE ABORT ROW IS A CLIP ROW. It must not land in source=snip, where
	// the audit's analysis would read it as demand for a catalog snippet.
	if len(rows.sources) != 1 || rows.sources[0] != telemetry.SourceClip {
		t.Fatalf("abort row sources = %v, want exactly [%s]", rows.sources, telemetry.SourceClip)
	}
}

// 🔴 A CANCEL TYPED AGAINST THE CLIP LIST IS EVIDENCE ABOUT CLIPS. Landing it
// in source=snip breaks the prune analysis: a clip's query credited as term
// evidence can rescue a genuinely DEAD snippet from the only prune verdict
// there is.
func TestACancelInSharedModeIsAClipRow(t *testing.T) {
	rows := capturePickerRows(t)
	m := enterShared(t, &fakeLauncher{clips: clipRows})
	m = typeStr(m, "pass")
	m, _ = m.Step(ctrl('c'))
	if !m.Canceled {
		t.Fatal("ctrl+c did not cancel")
	}
	if code := finish(m); code != 0 {
		t.Errorf("exit code = %d, want 0 — a cancel is a normal outcome", code)
	}
	if len(rows.sources) != 1 || rows.sources[0] != telemetry.SourceClip {
		t.Fatalf("cancel row sources = %v, want exactly [%s]", rows.sources, telemetry.SourceClip)
	}
	// POSITIVE CONTROL: the SAME path in browse mode emits a catalog row, so
	// the assertion above is about the mode and not about a hardcoded source.
	rows2 := capturePickerRows(t)
	b, _ := newModel(&fakeLauncher{}).Step(ctrl('c'))
	_ = finish(b)
	if len(rows2.sources) != 1 || rows2.sources[0] != telemetry.SourceSnip {
		t.Fatalf("browse cancel sources = %v, want exactly [%s]", rows2.sources, telemetry.SourceSnip)
	}
}

// A clip row renders its age and device attribution, and a multi-line body is
// flattened onto one line — the window maths counts rows, so a body with a
// newline in it must not become two.
func TestAClipRowShowsAgeAndDeviceOnOneLine(t *testing.T) {
	m := enterShared(t, &fakeLauncher{clips: clipRows})
	m.NowMS = 1_700_000_000_000 + 3*3_600_000 // three hours after clip 9
	m.Width = 120
	lines := bareLines(m)
	if !strings.Contains(lines[1], "3h · phone") {
		t.Errorf("row 0 = %q, want the sibling history row's \"3h · phone\" vocabulary", lines[1])
	}
	for i, l := range lines {
		if strings.Contains(l, "\n") {
			t.Errorf("line %d carries a newline: %q", i, l)
		}
	}
	// The multi-line body's text is on ONE line, with the break made visible.
	multi := lines[3]
	if !strings.Contains(multi, "a multi") || !strings.Contains(multi, "line body") {
		t.Errorf("the multi-line body did not flatten onto one row: %q", multi)
	}
}

// The frame still fits the terminal in shared mode: a long clip body truncates
// rather than wrapping.
func TestTheSharedFrameFitsTheWidth(t *testing.T) {
	long := strings.Repeat("y", 500)
	m := enterShared(t, &fakeLauncher{clips: []shared.Clip{
		{ID: 1, Body: long, Device: "phone", CreatedAt: 1_700_000_000_000},
	}})
	m.Width = 40
	for i, l := range m.Lines() {
		if w := lipgloss.Width(l); w > m.Width {
			t.Errorf("line %d is %d cells wide, over the 40-cell width: %q", i, w, stripANSI(l))
		}
	}
}

// The shared footer is its own key table and must not advertise ctrl+e, which
// does nothing to a clip.
func TestTheSharedFooterDoesNotAdvertiseEdit(t *testing.T) {
	m := enterShared(t, &fakeLauncher{clips: clipRows})
	m.Width = 120
	foot := stripANSI(m.Lines()[len(m.Lines())-1])
	if strings.Contains(foot, "ctrl+e") {
		t.Errorf("the shared footer offers ctrl+e, which has no meaning for a clip: %q", foot)
	}
	for _, want := range []string{"paste", "back", "filter"} {
		if !strings.Contains(foot, want) {
			t.Errorf("the shared footer is missing %q: %q", want, foot)
		}
	}
	// The BROWSE footer advertises the second keypress, so the mode is
	// discoverable at all.
	browse := stripANSI(newModel(&fakeLauncher{}).footer())
	if !strings.Contains(browse, SharedKey) {
		t.Errorf("the browse footer does not name %s: %q", SharedKey, browse)
	}
}

// Enter is inert on an empty shared list, like it is on an empty filter: there
// is nothing to choose.
func TestEnterIsInertOnAnEmptySharedList(t *testing.T) {
	fx := &fakeLauncher{clips: nil}
	m := enterShared(t, fx)
	m, intents := m.Step(tea.KeyPressMsg{Code: tea.KeyEnter})
	if len(intents) != 0 || m.ClipPasted != 0 {
		t.Fatalf("Enter on an empty clip list produced %+v / ClipPasted=%d", intents, m.ClipPasted)
	}
}

// --- the delete affordance ----------------------------------------------------
//
// 🔴 THESE ARE INVARIANT GUARDS ON A NEW AFFORDANCE, NOT REGRESSION COVERAGE,
// with one exception noted on it. Each was watched to go red against a
// deliberately broken copy of the thing it guards, with that guard's own
// message; the matrix is in the PR description.

// drive runs one message through Update and then every message its cmd
// produces, recursively — so a confirmed delete AND the re-list it triggers
// both run, which is the behaviour under test.
func drive(m Model, msg tea.Msg) Model {
	next, cmd := updCmd(m, msg)
	for _, out := range runCmd(cmd) {
		next = drive(next, out)
	}
	return next
}

// 🔴 THE DELETE CHORD ONLY ASKS. It must call NOTHING: this removes the
// operator's data from every device and the store has no trash, so the
// destructive call waits behind a y/N that names the row.
//
// The three controls in here are what make the silence meaningful: the chord
// does not type itself into the filter, a PLAIN key of the same letter does
// grow the filter instead of prompting (so the guard is about the modifier),
// and the prompt is raised for the row UNDER THE CURSOR rather than the first.
func TestTheDeleteChordOnlyAsks(t *testing.T) {
	fx := &fakeLauncher{clips: clipRows}
	m := enterShared(t, fx)
	m, intents := m.Step(ctrl('x'))
	if len(intents) != 0 {
		t.Fatalf("%s produced %+v — it must ask, not act", DeleteKey, intents)
	}
	if len(fx.clipDeletes) != 0 {
		t.Fatalf("%s reached the store: %v", DeleteKey, fx.clipDeletes)
	}
	if m.confirmClipID != 9 {
		t.Fatalf("confirmClipID = %d, want 9 — the row under the cursor", m.confirmClipID)
	}
	if m.query != "" {
		t.Errorf("query = %q — the chord typed itself into the filter", m.query)
	}
	screen := strings.Join(bareLines(m), "\n")
	if !strings.Contains(screen, "9") || !strings.Contains(screen, "[y/N]") {
		t.Errorf("the prompt does not name the clip and ask: %q", screen)
	}

	// CONTROL 1: the cursor decides which row is asked about.
	second := enterShared(t, &fakeLauncher{clips: clipRows})
	second = upd(second, tea.KeyPressMsg{Code: tea.KeyDown})
	second, _ = second.Step(ctrl('x'))
	if second.confirmClipID != 8 {
		t.Errorf("confirmClipID = %d after one ↓, want 8", second.confirmClipID)
	}

	// CONTROL 2: an UNMODIFIED 'x' filters and raises NO prompt, so the guard
	// above is about the modifier and not about Step ignoring the key.
	plain := typeStr(enterShared(t, &fakeLauncher{clips: clipRows}), "x")
	if plain.query != "x" {
		t.Errorf("a plain x did not reach the filter (%q) — this control proves nothing", plain.query)
	}
	if plain.confirmClipID != 0 {
		t.Errorf("a plain x raised the delete prompt (id=%d)", plain.confirmClipID)
	}
}

// The chord is inert on an empty list, like Enter: there is no row to ask about.
func TestTheDeleteChordIsInertOnAnEmptySharedList(t *testing.T) {
	fx := &fakeLauncher{clips: nil}
	m := enterShared(t, fx)
	m, intents := m.Step(ctrl('x'))
	if len(intents) != 0 || m.confirmClipID != 0 {
		t.Fatalf("%s on an empty clip list produced %+v / confirmClipID=%d",
			DeleteKey, intents, m.confirmClipID)
	}
}

// 🔴 THE CHORD BELONGS TO THE SHARED MODE ONLY — the key tables are disjoint,
// and this one is destructive, so in browse mode it must do nothing at all
// (not delete a clip, not land a catalog deletion, not grow the query).
func TestTheDeleteChordDoesNothingInBrowseMode(t *testing.T) {
	fx := &fakeLauncher{clips: clipRows}
	m, intents := newModel(fx).Step(ctrl('x'))
	if len(intents) != 0 {
		t.Fatalf("%s in browse mode produced %+v, want nothing", DeleteKey, intents)
	}
	if m.confirmClipID != 0 || m.confirmDelete != "" {
		t.Errorf("it raised a prompt in browse mode: clip=%d label=%q", m.confirmClipID, m.confirmDelete)
	}
	if m.query != "" {
		t.Errorf("query = %q", m.query)
	}
	if len(fx.clipDeletes) != 0 || len(fx.deleted) != 0 {
		t.Fatalf("it reached a delete path from browse mode: clips=%v labels=%v",
			fx.clipDeletes, fx.deleted)
	}
	// POSITIVE CONTROL: the SAME chord in shared mode DOES raise the prompt,
	// so the silence above is about the mode and not about a dead key.
	asked, _ := enterShared(t, fx).Step(ctrl('x'))
	if asked.confirmClipID == 0 {
		t.Fatal("the chord raised no prompt in shared mode either — this control proves nothing")
	}
}

// 🔴 THE CONFIRMED DELETE CALLS THE STORE WITH THE CLIP'S OWN ID, AND THE LIST
// IS THEN RE-READ FROM THE STORE RATHER THAN EDITED LOCALLY. The fake answers
// the second fetch with the store's new contents, so a model that spliced the
// row out of its own slice instead would still be holding the OLD list — and
// would miss the row the store gained in the meantime.
func TestAConfirmedDeleteRemovesTheClipAndReReadsTheStore(t *testing.T) {
	fx := &fakeLauncher{
		clips: clipRows,
		clipsAfterDelete: []shared.Clip{
			{ID: 8, Body: "Ärger über Äpfel", Device: "phone", CreatedAt: 1_699_999_000_000},
			{ID: 7, Body: "a multi\nline body", Device: "laptop", CreatedAt: 1_699_000_000_000},
			{ID: 11, Body: "captured while the prompt was up", Device: "phone", CreatedAt: 1_700_000_500_000},
		},
	}
	m := enterShared(t, fx)
	m, _ = m.Step(ctrl('x'))
	m = drive(m, tea.KeyPressMsg{Code: 'y', Text: "y"})

	if len(fx.clipDeletes) != 1 || fx.clipDeletes[0] != 9 {
		t.Fatalf("DeleteClip calls = %v, want exactly [9]", fx.clipDeletes)
	}
	if fx.fetches != 2 {
		t.Fatalf("fetches = %d, want 2 — the list must be re-read after the delete lands", fx.fetches)
	}
	ids := clipIDsOf(m.filteredClips)
	if len(ids) != 3 || ids[0] != 8 || ids[1] != 7 || ids[2] != 11 {
		t.Fatalf("list = %v, want the store's new answer [8 7 11] — a local splice would be [8 7]", ids)
	}
	if m.confirmClipID != 0 {
		t.Errorf("the prompt is still up (id=%d) after the delete landed", m.confirmClipID)
	}
	if m.quit || m.Canceled {
		t.Error("a delete must leave the picker open — the operator may delete another row")
	}
	if m.Deleted != "" {
		t.Errorf("Deleted = %q — that field is the CATALOG label; a clip deletion owes no catalog state", m.Deleted)
	}
}

// `n` (and esc) declines: nothing is called and the row stays.
func TestDecliningTheDeleteCallsNothing(t *testing.T) {
	for _, key := range []tea.KeyPressMsg{
		{Code: 'n', Text: "n"},
		{Code: tea.KeyEscape},
	} {
		fx := &fakeLauncher{clips: clipRows}
		m := enterShared(t, fx)
		m, _ = m.Step(ctrl('x'))
		m = drive(m, key)
		if len(fx.clipDeletes) != 0 {
			t.Fatalf("%v reached the store: %v", key, fx.clipDeletes)
		}
		if m.confirmClipID != 0 {
			t.Errorf("%v left the prompt up (id=%d)", key, m.confirmClipID)
		}
		if ids := clipIDsOf(m.filteredClips); len(ids) != 3 {
			t.Errorf("%v changed the list: %v", key, ids)
		}
		if m.mode != modeShared {
			t.Errorf("%v left the shared mode — it backs out of the DELETION, not the mode", key)
		}
		if fx.fetches != 1 {
			t.Errorf("%v re-fetched (%d) — nothing changed in the store", key, fx.fetches)
		}
	}
}

// 🔴 THE CLIP PROMPT IS MODAL. A keystroke meant for it must never become a
// filter character or, worse, the Enter that pastes something. (The CATALOG
// prompt's own modality is TestTheDeletePromptIsModal in picker_test.go —
// these are two prompts, deliberately.)
func TestTheClipDeletePromptIsModal(t *testing.T) {
	fx := &fakeLauncher{clips: clipRows}
	m := enterShared(t, fx)
	m, _ = m.Step(ctrl('x'))

	typed, intents := m.Step(tea.KeyPressMsg{Code: 'z', Text: "z"})
	if typed.query != "" {
		t.Errorf("a keystroke under the prompt grew the filter: %q", typed.query)
	}
	if len(intents) != 0 {
		t.Errorf("it produced %+v", intents)
	}
	if typed.confirmClipID != 9 {
		t.Errorf("the prompt was dismissed by an unrelated key (id=%d)", typed.confirmClipID)
	}

	entered, intents := m.Step(tea.KeyPressMsg{Code: tea.KeyEnter})
	if len(intents) != 0 || entered.ClipPasted != 0 {
		t.Fatalf("Enter under the prompt pasted: %+v / ClipPasted=%d", intents, entered.ClipPasted)
	}
	if len(fx.clipIDs) != 0 {
		t.Fatalf("Enter under the prompt reached the paste spawn: %v", fx.clipIDs)
	}

	// 🔴 AND `y` UNDER THIS PROMPT DOES NOT ANSWER THE CATALOG'S. The two
	// populations do not share a confirm: answering here must never land a
	// deletion through the landing script (a git commit against a label).
	answered := drive(m, tea.KeyPressMsg{Code: 'y', Text: "y"})
	if len(fx.deleted) != 0 {
		t.Fatalf("y over a clip landed a CATALOG deletion: %v", fx.deleted)
	}
	if answered.Deleted != "" {
		t.Errorf("Deleted = %q", answered.Deleted)
	}
}

// 🔴 A FAILED DELETE MUST NOT DROP THE ROW, AND IT SAYS WHY. A row that
// vanished from the list after a refused delete would show the operator a
// deletion that did not happen. The reason is surfaced the way an unreachable
// store's is: the notice line plus a toast.
func TestAFailedDeleteKeepsTheRowAndSaysWhy(t *testing.T) {
	fx := &fakeLauncher{
		clips:        clipRows,
		clipDeleteEr: errors.New("clip store refused (503): the store is read-only"),
	}
	m := enterShared(t, fx)
	m, _ = m.Step(ctrl('x'))
	m = drive(m, tea.KeyPressMsg{Code: 'y', Text: "y"})

	if len(fx.clipDeletes) != 1 {
		t.Fatalf("the delete was not attempted: %v", fx.clipDeletes)
	}
	ids := clipIDsOf(m.filteredClips)
	if len(ids) != 3 || ids[0] != 9 {
		t.Fatalf("the row was dropped on a REFUSED delete: %v", ids)
	}
	if fx.fetches != 1 {
		t.Errorf("fetches = %d — a failed delete changed nothing, so there is nothing to re-read", fx.fetches)
	}
	screen := strings.Join(bareLines(m), "\n")
	if !strings.Contains(screen, "read-only") {
		t.Errorf("the reason is not on screen: %q", screen)
	}
	if !strings.Contains(screen, "delete failed") {
		t.Errorf("the screen does not say the DELETE failed: %q", screen)
	}
	if len(fx.toasts) == 0 {
		t.Error("a failed delete owes a toast as well as the notice")
	}
	if m.quit {
		t.Error("a failed delete must leave the picker open")
	}
	if m.confirmClipID != 0 {
		t.Errorf("the prompt is still up (id=%d) after the refusal was reported", m.confirmClipID)
	}
}

// The shared footer advertises the delete chord, so the affordance is
// discoverable at all — and the BROWSE footer does not, because the chord has
// no meaning there.
func TestTheSharedFooterAdvertisesDelete(t *testing.T) {
	m := enterShared(t, &fakeLauncher{clips: clipRows})
	m.Width = 120
	foot := stripANSI(m.Lines()[len(m.Lines())-1])
	if !strings.Contains(foot, DeleteKey) || !strings.Contains(foot, "delete") {
		t.Errorf("the shared footer does not offer %s delete: %q", DeleteKey, foot)
	}
	browse := stripANSI(newModel(&fakeLauncher{}).footer())
	if strings.Contains(browse, DeleteKey) {
		t.Errorf("the browse footer offers %s, which only means something over a clip: %q", DeleteKey, browse)
	}
}

// keyTableOf reads a key table OUT OF THE SOURCE: every `case` label of every
// `switch msg.String()` inside the named function, with the two key constants
// resolved to their values.
//
// 🔴 IT PARSES picker.go RATHER THAN RESTATING IT, AND THAT IS THE WHOLE POINT.
// This guard used to compare against a hand-written SECOND COPY of the browse
// table, which is a copy of a list, not a read of the code: adding
// `case "ctrl+x":` to the real browse switch left the entire suite GREEN
// (round-1 finding 3 — mutation-proven). An empty case body makes the mutation
// behaviourally invisible, so no amount of key-pressing can see it; only
// reading the table can. The source IS the ledger now, and it fails when the
// browse set grows into the shared chords.
//
// ⚠ ITS BLIND SPOT IS SHAPE, NOT CONTENT: it reads `case` labels of a
// `switch msg.String()` and nothing else, so the same collision spelled
// `if msg.String() == DeleteKey { … }` ahead of the browse switch is invisible
// to it and leaves the whole suite green (mutation-proven). Read it as "the
// browse SWITCH does not name these keys", not as "no browse code handles
// them"; making it shape-general was considered and is not what this guard is.
func keyTableOf(t *testing.T, file, funcName string) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v — this guard reads the real key table and cannot "+
			"fall back on a copy of it", file, err)
	}
	var fn *ast.FuncDecl
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == funcName {
			fn = fd
		}
	}
	if fn == nil {
		t.Fatalf("%s holds no func %s — the key table moved and this guard is "+
			"reading nothing", file, funcName)
	}
	keys := map[string]bool{}
	ast.Inspect(fn, func(n ast.Node) bool {
		sw, ok := n.(*ast.SwitchStmt)
		if !ok {
			return true
		}
		// Only the switches on the KEY STRING; a switch on anything else is
		// not a key table.
		call, ok := sw.Tag.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "String" {
			return true
		}
		for _, stmt := range sw.Body.List {
			cc, ok := stmt.(*ast.CaseClause)
			if !ok {
				continue
			}
			for _, expr := range cc.List {
				switch e := expr.(type) {
				case *ast.BasicLit:
					if e.Kind == token.STRING {
						if v, err := strconv.Unquote(e.Value); err == nil {
							keys[v] = true
						}
					}
				case *ast.Ident:
					// The key CONSTANTS, resolved to the values Step compares.
					switch e.Name {
					case "SharedKey":
						keys[SharedKey] = true
					case "DeleteKey":
						keys[DeleteKey] = true
					default:
						t.Fatalf("%s: case %s is an identifier this guard cannot "+
							"resolve — resolve it here rather than letting the "+
							"ledger silently miss a key", funcName, e.Name)
					}
				}
			}
		}
		return true
	})
	return keys
}

// 🔴 THE TWO CHORDS ARE DISTINCT KEYS AND NEITHER IS A KEY THE BROWSE TABLE
// ALREADY OWNS. A chord that collided would silently take over an existing
// binding — the one failure a per-key test cannot see, because each would
// still pass on its own. This is the ledger, READ OUT OF THE SOURCE: it fails
// when the browse set GROWS into these keys or when the two shared chords
// converge.
func TestTheSharedChordsCollideWithNothing(t *testing.T) {
	browseKeys := keyTableOf(t, "picker.go", "Step")

	// 🔴 VALIDATE THE INSTRUMENT BEFORE READING ITS VERDICT. A parse that found
	// the wrong switch, or no switch, would report an empty set — and an empty
	// set satisfies every assertion below while measuring nothing. These keys
	// are in the browse table today; if they are not in the parsed set, the
	// read is broken and the verdict is worthless.
	for _, k := range []string{"esc", "ctrl+c", "enter", "up", "down", "backspace", "ctrl+e", SharedKey} {
		if !browseKeys[k] {
			t.Fatalf("the parsed browse table is missing %q (%d keys: %v) — this "+
				"guard is reading the wrong code and proves nothing", k, len(browseKeys), browseKeys)
		}
	}
	// And the shared table is read the same way, so the delete chord is known
	// to live SOMEWHERE — a DeleteKey that no table names would pass the
	// collision check vacuously.
	sharedKeys := keyTableOf(t, "shared.go", "stepShared")
	if !sharedKeys[DeleteKey] {
		t.Fatalf("the shared table does not name %s (%v) — the chord is dead and "+
			"this guard is vacuous", DeleteKey, sharedKeys)
	}

	if SharedKey == DeleteKey {
		t.Fatalf("the mode chord and the delete chord are the same key (%q)", SharedKey)
	}
	if browseKeys[DeleteKey] {
		t.Errorf("%s is a key the REAL browse table now names (picker.go's Step) — "+
			"the destructive shared chord collides with a catalog binding", DeleteKey)
	}
	// 🔴 AND IT IS NOT A READLINE EDITING CHORD. This list is a text-entry
	// box; the chords an operator's hands produce there without meaning to are
	// exactly these, and one of them deleting a clip is the accident this
	// constant is chosen to prevent.
	for _, k := range []string{"ctrl+w", "ctrl+u", "ctrl+k", "ctrl+d", "ctrl+h", "ctrl+a"} {
		if DeleteKey == k {
			t.Errorf("%s is a readline editing chord — it can arrive from typing", DeleteKey)
		}
	}
	// It must be a MODIFIED chord, not a printable key.
	if !strings.HasPrefix(DeleteKey, "ctrl+") {
		t.Errorf("%s is not a ctrl chord — a plain key types itself into the filter", DeleteKey)
	}
}

// 🔴 RE-ENTERING THE SHARED MODE RE-FETCHES. The esc arm's comment used to say
// coming back "does not re-fetch"; the clips do stay in the model, but the
// ctrl+g arm sets fetching and returns FetchShared unconditionally, so every
// toggle back is a fresh read. This pins the behaviour the corrected comment
// describes — and it matters for delete: a row removed from the phone is gone
// from the desktop's list on the next toggle, not stale.
func TestComingBackToTheSharedModeReFetches(t *testing.T) {
	fx := &fakeLauncher{clips: clipRows}
	m := enterShared(t, fx)
	m = upd(m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.mode != modeBrowse {
		t.Fatalf("esc did not return to browse (mode=%v)", m.mode)
	}
	if len(m.clips) != 3 {
		t.Errorf("the clips were cleared on the way out (%d) — the model keeps them", len(m.clips))
	}
	m, cmd := updCmd(m, ctrl('g'))
	if !m.fetching {
		t.Error("re-entering did not mark a fetch in flight")
	}
	// 🔴 AND THE PREVIOUS VISIT'S ROWS ARE WHAT IS ON SCREEN UNTIL IT LANDS —
	// the thing the esc arm's comment used to deny. This test pinned the kept
	// clips without noticing they were RENDERED and ACTIONABLE, which is the
	// sentence that hid the blocker (round-1 finding 7). What matters is not
	// that they are absent (they are not) but that they cannot be acted on:
	// the frame shows them, says it is still fetching, and the two keys that
	// act on a row are inert.
	m.Width = 120
	frame := strings.Join(bareLines(m), "\n")
	if !strings.Contains(frame, "github.com password hunter2") {
		t.Errorf("the previous visit's rows are NOT on screen — if that ever "+
			"becomes true, the esc arm's comment and listIsStale both need "+
			"rewriting, not this assertion deleting:\n%s", frame)
	}
	if !strings.Contains(frame, "fetching") {
		t.Errorf("the frame does not say the list is being re-read:\n%s", frame)
	}
	if _, intents := m.Step(tea.KeyPressMsg{Code: tea.KeyEnter}); len(intents) != 0 {
		t.Errorf("enter acted on the previous visit's rows: %+v", intents)
	}
	if armed, intents := m.Step(ctrl('x')); len(intents) != 0 || armed.confirmClipID != 0 {
		t.Errorf("ctrl+x armed a delete against the previous visit's rows: %+v / id=%d",
			intents, armed.confirmClipID)
	}
	for _, msg := range runCmd(cmd) {
		m = upd(m, msg)
	}
	if fx.fetches != 2 {
		t.Fatalf("fetches = %d after two entries, want 2 — every toggle back re-reads the store", fx.fetches)
	}
}

// --- the blocker: an armed prompt that was not on the screen -------------------

// wantClipPrompt is the prompt line for clip 9 of clipRows, as a LITERAL. It is
// written out here rather than derived from confirmPrompt: a guard that asks the
// implementation what to expect asserts only that the code equals itself.
//
// 🔴 IT PINS THE WHOLE NORMALISED STRING, NOT A WORD. Greping the frame for
// "delete clip" is walkable by rewording the prompt — the hazard would still be
// there in a different spelling, and the guard would still be green. The price
// is that a cosmetic reword fails this test; that is the price of a
// machine-readable claim about what is on the screen.
const wantClipPrompt = `[y/N] delete clip 9 from the shared store — "github.com password hunter2" · no undo`

// wantClipPromptGone is the same prompt when the armed clip is no longer in the
// list — the store was re-read underneath the prompt and answered without it.
// The ask and the id stay; only the preview gives way, and its absence is named
// rather than rendered as an empty pair of quotes.
const wantClipPromptGone = `[y/N] delete clip 9 from the shared store — (that row is no longer listed) · no undo`

// armedPrompt is the settled shared mode with the delete prompt up on clip 9,
// on a terminal wide enough that the prompt is not truncated.
func armedPrompt(t *testing.T, fx *fakeLauncher) Model {
	t.Helper()
	m := enterShared(t, fx)
	m.Width = 200
	m, _ = m.Step(ctrl('x'))
	if m.confirmClipID != 9 {
		t.Fatalf("fixture: confirmClipID = %d, want 9", m.confirmClipID)
	}
	if !strings.Contains(strings.Join(bareLines(m), "\n"), wantClipPrompt) {
		t.Fatalf("fixture: the prompt is not on screen to begin with:\n%s",
			strings.Join(bareLines(m), "\n"))
	}
	return m
}

// 🔴 THE ROUND-1 BLOCKER, AS A RELATIONSHIP RATHER THAN A WORD:
//
//	m.confirmClipID != 0  ⇔  the rendered frame carries THAT clip's prompt
//
// The armed half (confirmClipID, which accepts `y`) and the visible half (a
// copy of the prompt text parked in m.notice) used to be two facts in two
// fields with two lifetimes, and three handlers cleared the second without
// looking at the first. A fetch landing between the ctrl+x and the keypress
// wiped the question off the screen while leaving the answer live, and `y` —
// the first character of an ordinary filter query, under a footer reading "type
// to filter" — deleted a clip from every device with no undo.
//
// This asserts the BICONDITIONAL over every state mutation that used to break
// it, so it fails whether the prompt goes missing while armed OR is rendered
// while disarmed. It cannot be satisfied by rewording anything, and it cannot
// be satisfied by re-adding `if m.confirmClipID == 0` at one of the three
// notice sites, because it enumerates all three plus a direct write.
func TestAnArmedClipPromptIsOnTheScreenExactlyWhenItIsArmed(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(Model) Model
		// want is the EXACT line the frame must carry, or "" when the mutation
		// legitimately disarms the prompt (then the frame must carry neither
		// spelling). Literal, not derived: a guard that asks confirmPrompt what
		// to expect asserts only that the code equals itself.
		want string
	}{
		{"a fetch lands with rows (the blocker's own route)", func(m Model) Model {
			return upd(m, sharedFetched{Clips: clipRows})
		}, wantClipPrompt},
		{"a fetch lands empty — the row is gone from under the prompt", func(m Model) Model {
			return upd(m, sharedFetched{Clips: nil})
		}, wantClipPromptGone},
		{"a fetch fails", func(m Model) Model {
			return upd(m, sharedFetched{Err: errors.New("i/o timeout")})
		}, wantClipPrompt},
		{"a delete is refused", func(m Model) Model {
			return upd(m, clipDeleted{Err: errors.New("503 read-only")})
		}, ""},
		{"a delete lands", func(m Model) Model {
			return upd(m, clipDeleted{})
		}, ""},
		{"a handler writes the notice directly", func(m Model) Model {
			m.notice = "something else entirely"
			return m
		}, wantClipPrompt},
		{"the window is resized", func(m Model) Model {
			return upd(m, tea.WindowSizeMsg{Width: 200, Height: 24})
		}, wantClipPrompt},
		{"nothing happens at all", func(m Model) Model { return m }, wantClipPrompt},
	}
	armedSeen, disarmedSeen := 0, 0
	for _, c := range cases {
		m := c.mutate(armedPrompt(t, &fakeLauncher{clips: clipRows}))
		screen := strings.Join(bareLines(m), "\n")
		onScreen := strings.Contains(screen, wantClipPrompt) ||
			strings.Contains(screen, wantClipPromptGone)
		armed := m.confirmClipID != 0
		if armed {
			armedSeen++
		} else {
			disarmedSeen++
		}
		if armed != onScreen {
			t.Errorf("%s: confirmClipID=%d (armed=%v) but the prompt on screen = %v.\n"+
				"An armed prompt that is not rendered is the round-1 blocker: `y` is "+
				"live with nothing asking for it.\nframe:\n%s",
				c.name, m.confirmClipID, armed, onScreen, screen)
		}
		if c.want != "" && !strings.Contains(screen, c.want) {
			t.Errorf("%s: the frame does not carry the expected prompt line.\nwant: %s\nframe:\n%s",
				c.name, c.want, screen)
		}
		if c.want == "" && onScreen {
			t.Errorf("%s: a disarmed prompt is still on the screen:\n%s", c.name, screen)
		}
		// 🔴 AND ARMED MEANS ARMED: the biconditional must not be satisfied by
		// disarming everything, so where it IS armed, `y` still answers.
		if armed {
			_, intents := m.Step(tea.KeyPressMsg{Code: 'y', Text: "y"})
			if len(intents) != 1 {
				t.Errorf("%s: the prompt is rendered and armed, but y produced %+v",
					c.name, intents)
			}
		}
	}
	// The loop is only meaningful if it exercised BOTH directions.
	if armedSeen == 0 || disarmedSeen == 0 {
		t.Fatalf("the cases covered armed=%d disarmed=%d — the biconditional was "+
			"only ever tested in one direction, which it satisfies trivially",
			armedSeen, disarmedSeen)
	}
}

// 🔴 THE AUDITOR'S EXACT ROUTE, DRIVEN THROUGH Update WITH REAL KEY MESSAGES:
// ctrl+g → rows land → esc → ctrl+g (the PREVIOUS visit's rows are on screen
// and a re-fetch is in flight) → ↓ → ctrl+x → the re-fetch lands → `y`.
//
// Before the fix: ctrl+x raised the prompt against the stale list, the landing
// fetch wiped the prompt text, and `y` emitted DeleteClip for the clip the
// cursor had been on — no prompt in sight, no undo. After: ctrl+x is inert
// while the list is stale (listIsStale), so nothing is armed, and `y` is an
// ordinary filter character.
func TestTheAuditorsRouteCannotDeleteAClipByTypingY(t *testing.T) {
	fx := &fakeLauncher{clips: clipRows}
	m := enterShared(t, fx) // ctrl+g, fetch run
	m.Width = 200
	m = upd(m, tea.KeyPressMsg{Code: tea.KeyEscape})

	// Re-enter and hold the fetch: the cmd is NOT run, so the previous visit's
	// rows are what is on screen while the re-read is in flight.
	m, pending := updCmd(m, ctrl('g'))
	if !m.fetching || len(m.filteredClips) != 3 {
		t.Fatalf("fixture: fetching=%v rows=%d — the route needs stale rows on "+
			"screen with a fetch in flight", m.fetching, len(m.filteredClips))
	}
	m = upd(m, tea.KeyPressMsg{Code: tea.KeyDown})
	m, intents := m.Step(ctrl('x'))
	if len(intents) != 0 {
		t.Fatalf("ctrl+x on a stale list produced %+v", intents)
	}
	if m.confirmClipID != 0 {
		t.Fatalf("ctrl+x armed a delete against a list that is not the store's "+
			"current answer (id=%d)", m.confirmClipID)
	}

	// The re-fetch lands.
	for _, msg := range runCmd(pending) {
		m = upd(m, msg)
	}

	// …and the operator types `y` as the first character of a filter query.
	m, intents = m.Step(tea.KeyPressMsg{Code: 'y', Text: "y"})
	for _, it := range intents {
		if dc, ok := it.(DeleteClip); ok {
			t.Fatalf("typing y produced DeleteClip{%d} — the blocker is back", dc.ID)
		}
	}
	if len(fx.clipDeletes) != 0 {
		t.Fatalf("the store was called: %v", fx.clipDeletes)
	}
	if m.query != "y" {
		t.Errorf("query = %q, want \"y\" — with no prompt up, y is a filter character", m.query)
	}

	// THE OTHER HALF: a prompt armed against a SETTLED list survives a fetch
	// landing late (two fetches in flight across an esc is how that happens),
	// and the answer is still being asked for on screen.
	m2 := armedPrompt(t, &fakeLauncher{clips: clipRows})
	m2 = upd(m2, sharedFetched{Clips: clipRows})
	screen := strings.Join(bareLines(m2), "\n")
	if !strings.Contains(screen, wantClipPrompt) {
		t.Fatalf("a late fetch wiped the armed prompt off the screen:\n%s", screen)
	}
	m2, intents = m2.Step(tea.KeyPressMsg{Code: 'y', Text: "y"})
	if len(intents) != 1 {
		t.Fatalf("y under the surviving prompt produced %+v, want one DeleteClip", intents)
	}
	if _, ok := intents[0].(DeleteClip); !ok {
		t.Fatalf("intent = %T, want DeleteClip", intents[0])
	}
	if m2.query != "" {
		t.Errorf("query = %q — y answered the prompt AND typed itself in", m2.query)
	}
}

// 🔴 THE STALE LIST IS NOT ACTIONABLE. The rows from the previous visit are on
// screen while the re-fetch is in flight (the esc arm's comment says so, and
// says this is why that is safe); the two keys that ACT on a row must be inert
// until the list is the store's current answer. Navigation, typing and esc stay
// live — none of them acts on a row.
func TestTheStaleListIsNotActionableWhileAFetchIsInFlight(t *testing.T) {
	fx := &fakeLauncher{clips: clipRows}
	m := enterShared(t, fx)
	m = upd(m, tea.KeyPressMsg{Code: tea.KeyEscape})
	m, pending := updCmd(m, ctrl('g'))
	if !m.fetching {
		t.Fatal("fixture: the re-entry fetch is not in flight")
	}

	m, intents := m.Step(tea.KeyPressMsg{Code: tea.KeyEnter})
	if len(intents) != 0 || m.ClipPasted != 0 {
		t.Fatalf("enter pasted from a stale list: %+v / ClipPasted=%d", intents, m.ClipPasted)
	}
	m, intents = m.Step(ctrl('x'))
	if len(intents) != 0 || m.confirmClipID != 0 {
		t.Fatalf("ctrl+x armed against a stale list: %+v / id=%d", intents, m.confirmClipID)
	}
	// Navigation and typing are NOT gated: the cursor and the filter act on no row.
	moved := upd(m, tea.KeyPressMsg{Code: tea.KeyDown})
	if moved.cursor != 1 {
		t.Errorf("cursor = %d — moving the cursor is not an action on a row and "+
			"must stay live while fetching", moved.cursor)
	}
	if typed := typeStr(m, "pass"); typed.query != "pass" {
		t.Errorf("query = %q — typing must stay live while fetching", typed.query)
	}

	// POSITIVE CONTROL: once the answer lands, BOTH keys work — so the silence
	// above is about the in-flight fetch and not about two dead keys.
	for _, msg := range runCmd(pending) {
		m = upd(m, msg)
	}
	settled, intents := m.Step(tea.KeyPressMsg{Code: tea.KeyEnter})
	if len(intents) != 1 || settled.ClipPasted == 0 {
		t.Fatalf("enter is dead on a SETTLED list (%+v) — this control proves nothing", intents)
	}
	if _, intents := m.Step(ctrl('x')); len(intents) != 0 {
		t.Fatalf("ctrl+x produced %+v on a settled list, want the prompt only", intents)
	}
	if asked, _ := m.Step(ctrl('x')); asked.confirmClipID == 0 {
		t.Fatal("ctrl+x raises no prompt on a SETTLED list — this control proves nothing")
	}

	// And a DELETE in flight is stale too: the row being removed must not be
	// pasteable or deletable again while the call is on the wire.
	inflight, _ := m.Step(ctrl('x'))
	inflight, _ = inflight.Step(tea.KeyPressMsg{Code: 'y', Text: "y"})
	if inflight.deletingClipID != 9 {
		t.Fatalf("deletingClipID = %d after y, want 9", inflight.deletingClipID)
	}
	if again, intents := inflight.Step(ctrl('x')); len(intents) != 0 || again.confirmClipID != 0 {
		t.Fatalf("ctrl+x armed a second delete while one was in flight: %+v / id=%d",
			intents, again.confirmClipID)
	}

	// 🔴 AND A FETCH THAT FAILED IS STALE TOO — the arm listIsStale's own
	// docstring promised ("the rows on screen are not an answer the store has
	// given yet") and did not have. The re-fetch over the previous visit's rows
	// comes back `i/o timeout`: the fetch is no longer in flight, so the
	// `fetching` arm above has let go, while the rows are still the previous
	// visit's and the store has said nothing about them. Measured on the
	// pre-change predicate: fetching=false fetchFailed=true stale=false rows=3,
	// all three rendered, ctrl+x arming clip 9 and enter emitting SelectClip{9}.
	// The same end state is reachable a second way — a SUCCESSFUL delete whose
	// post-delete re-list then fails leaves the just-deleted clip on screen —
	// and the single disjunct closes both.
	fx2 := &fakeLauncher{clips: clipRows}
	f := enterShared(t, fx2)
	f = upd(f, tea.KeyPressMsg{Code: tea.KeyEscape})
	f, _ = updCmd(f, ctrl('g'))
	f = upd(f, sharedFetched{Err: errors.New("i/o timeout")})
	if f.fetching || !f.fetchFailed || len(f.filteredClips) != 3 {
		t.Fatalf("fixture: fetching=%v fetchFailed=%v rows=%d — want a FAILED "+
			"re-fetch over the previous visit's three rows",
			f.fetching, f.fetchFailed, len(f.filteredClips))
	}
	if pasted, intents := f.Step(tea.KeyPressMsg{Code: tea.KeyEnter}); len(intents) != 0 || pasted.ClipPasted != 0 {
		t.Errorf("enter pasted from rows a FAILED fetch left on screen: %+v / "+
			"ClipPasted=%d — the store has not answered for them", intents, pasted.ClipPasted)
	}
	if armed, intents := f.Step(ctrl('x')); len(intents) != 0 || armed.confirmClipID != 0 {
		t.Errorf("ctrl+x armed a delete against rows a FAILED fetch left on screen: "+
			"%+v / id=%d", intents, armed.confirmClipID)
	}
}

// 🔴 THE DECLINE ARM CANNOT PRINT A FALSE REASSURANCE. Pressing `n` after `y`
// used to say "deletion cancelled — the clip is still in the store" while the
// DELETE was on the wire; it then landed, and the operator had been told in
// words that his data was safe (round-1 finding 9). `y` takes the prompt DOWN
// and moves the id to the in-flight field, so that arm is unreachable for a
// delete that has already been called.
func TestDecliningAfterTheAnswerCannotClaimTheClipIsStillThere(t *testing.T) {
	fx := &fakeLauncher{clips: clipRows}
	m := armedPrompt(t, fx)
	m, delCmd := updCmd(m, tea.KeyPressMsg{Code: 'y', Text: "y"})
	if m.confirmClipID != 0 || m.deletingClipID != 9 {
		t.Fatalf("after y: confirmClipID=%d deletingClipID=%d — the answered prompt "+
			"must come down and the in-flight id must be recorded",
			m.confirmClipID, m.deletingClipID)
	}
	// `n`, while the call is on the wire.
	m2, _ := m.Step(tea.KeyPressMsg{Code: 'n', Text: "n"})
	screen := strings.Join(bareLines(m2), "\n")
	if strings.Contains(screen, "still in the store") || strings.Contains(screen, "cancelled") {
		t.Errorf("the screen claims the deletion was cancelled while the DELETE is "+
			"in flight:\n%s", screen)
	}
	// The delete does land, and the frame never said otherwise.
	for _, msg := range runCmd(delCmd) {
		m = upd(m, msg)
	}
	if len(fx.clipDeletes) != 1 || fx.clipDeletes[0] != 9 {
		t.Fatalf("DeleteClip calls = %v, want [9]", fx.clipDeletes)
	}
	if m.deletingClipID != 0 {
		t.Errorf("deletingClipID = %d after the answer landed, want 0", m.deletingClipID)
	}
	// POSITIVE CONTROL: `n` BEFORE any answer does cancel, and says so — so the
	// assertion above is about the in-flight state, not about a dead arm.
	declined, _ := armedPrompt(t, &fakeLauncher{clips: clipRows}).
		Step(tea.KeyPressMsg{Code: 'n', Text: "n"})
	if !strings.Contains(strings.Join(bareLines(declined), "\n"), "still in the store") {
		t.Fatal("n on an UNANSWERED prompt does not say the clip is safe — this " +
			"control proves nothing")
	}
}

// 🔴 A CLIP PASTE EMITS NO CANCEL ROW. finish() branches on m.Selected, which
// the clip path never sets (a clip has no label to put there), so every
// successful clip paste used to fall through to the cancel arm and emit
// outcome=cancel ON TOP of the helper's select row: one paste, two rows, and a
// clip store in 100% use reading as 50% cancels (round-1 finding 2).
func TestAClipPasteEmitsNoPickerRow(t *testing.T) {
	// The auditor's own probe, as a literal.
	rows := capturePickerRows(t)
	if code := finish(Model{mode: modeShared, ClipPasted: 7, quit: true}); code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
	if len(rows.rows) != 0 {
		t.Fatalf("a clip paste emitted %+v — the helper owns the select row", rows.rows)
	}

	// And through the real route: enter → the spawn succeeds → the TUI quits.
	rows2 := capturePickerRows(t)
	fx := &fakeLauncher{clips: clipRows}
	m := typeStr(enterShared(t, fx), "pass")
	m, cmd := updCmd(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	for _, msg := range runCmd(cmd) {
		m = upd(m, msg)
	}
	if !m.quit || m.ClipPasted != 9 {
		t.Fatalf("fixture: quit=%v ClipPasted=%d", m.quit, m.ClipPasted)
	}
	if code := finish(m); code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
	if len(rows2.rows) != 0 {
		t.Fatalf("the clip-paste route emitted %+v, want nothing", rows2.rows)
	}

	// POSITIVE CONTROL: the SAME finish does emit a cancel row when the open
	// resolved to nothing, so the two zeros above are not a dead emitter.
	rows3 := capturePickerRows(t)
	c, _ := enterShared(t, fx).Step(ctrl('c'))
	_ = finish(c)
	if len(rows3.rows) != 1 || rows3.rows[0].Outcome != "cancel" {
		t.Fatalf("a cancel emitted %+v — this control proves nothing", rows3.rows)
	}
}

// 🔴 A LATE sharedFetched MUST NOT MOVE THE BROWSE VIEW. The fetch is in flight
// across an `esc`, so its answer can arrive while the CATALOG list is on
// screen: it used to set m.cursor = 0 unconditionally, so the row under a
// pending Enter changed out from under the operator and he pasted row 0
// (round-1 finding 5) — and its failure arm wrote its notice over the catalog
// list. Same root shape as the blocker: a handler mutating state it does not
// own.
func TestALateFetchDoesNotMoveTheBrowseView(t *testing.T) {
	fx := &fakeLauncher{clips: clipRows}
	m := enterShared(t, fx)
	m = upd(m, tea.KeyPressMsg{Code: tea.KeyEscape})
	for i := 0; i < 3; i++ {
		m = upd(m, tea.KeyPressMsg{Code: tea.KeyDown})
	}
	if m.mode != modeBrowse || m.cursor != 3 {
		t.Fatalf("fixture: mode=%v cursor=%d", m.mode, m.cursor)
	}
	want := m.filtered[m.cursor].Label

	// A late SUCCESS: the rows are taken, the cursor is not moved.
	landed := upd(m, sharedFetched{Clips: clipRows})
	if landed.cursor != 3 {
		t.Errorf("a late fetch moved the browse cursor to %d — the row under a "+
			"pending Enter changed", landed.cursor)
	}
	if got := landed.filtered[landed.cursor].Label; got != want {
		t.Errorf("the row under the cursor is now %q, want %q", got, want)
	}
	if len(landed.clips) != 3 {
		t.Errorf("the clips were dropped (%d) — they are this mode's data and are "+
			"kept for the next ctrl+g", len(landed.clips))
	}
	sel, _ := landed.Step(tea.KeyPressMsg{Code: tea.KeyEnter})
	if sel.Selected != want {
		t.Errorf("Enter pasted %q, want %q", sel.Selected, want)
	}

	// A late FAILURE: the reason does not land on the catalog list. The toast
	// still fires — it is the channel that survives the mode.
	failed := upd(m, sharedFetched{Err: errors.New("i/o timeout")})
	screen := strings.Join(bareLines(failed), "\n")
	if strings.Contains(screen, "i/o timeout") {
		t.Errorf("a late fetch failure wrote its reason over the catalog list:\n%s", screen)
	}
	if len(fx.toasts) == 0 {
		t.Error("the failure owes a toast — that is where it goes instead")
	}
	if failed.cursor != 3 {
		t.Errorf("the failure arm moved the browse cursor to %d", failed.cursor)
	}

	// POSITIVE CONTROL: the same message IN shared mode does render the reason,
	// so the silences above are about the mode. It does NOT reset the cursor in
	// either mode any more — the shared arm keeps it on the same clip id
	// (TestALandingFetchKeepsTheSharedCursorOnItsClip).
	shared := upd(enterShared(t, fx), sharedFetched{Err: errors.New("i/o timeout")})
	if !strings.Contains(strings.Join(bareLines(shared), "\n"), "i/o timeout") {
		t.Fatal("the reason is not rendered in shared mode either — this control " +
			"proves nothing")
	}
}

// 🔴 AND IT MUST NOT MOVE THE SHARED CURSOR EITHER, WHICH IS THE SAME FINDING
// ONE MODE OVER. The round-1 fix made `m.cursor = 0` conditional on
// `mode == modeShared` rather than removing it, and navigation is deliberately
// live while the list is stale — so re-entering the mode, moving down twice and
// letting the re-fetch land put the cursor back on row 0 under a pending Enter:
// measured as a paste of clip 9 where the operator was looking at clip 7. A
// clip body is arbitrary captured text (the fixture's row 0 is a password), so
// the wrong row is a credential in whatever window has focus.
//
// The cursor is therefore CLAMPED, NOT RESET: it stays on the same clip ID when
// that id is still in the store's answer, and otherwise lands in range.
func TestALandingFetchKeepsTheSharedCursorOnItsClip(t *testing.T) {
	fx := &fakeLauncher{clips: clipRows}
	m := enterShared(t, fx)
	m = upd(m, tea.KeyPressMsg{Code: tea.KeyEscape})
	m, pending := updCmd(m, ctrl('g'))
	m = upd(m, tea.KeyPressMsg{Code: tea.KeyDown})
	m = upd(m, tea.KeyPressMsg{Code: tea.KeyDown})
	if m.cursor != 2 || !m.fetching || m.filteredClips[m.cursor].ID != 7 {
		t.Fatalf("fixture: cursor=%d fetching=%v id=%d — want the cursor on clip 7 "+
			"with the re-fetch in flight", m.cursor, m.fetching, m.filteredClips[m.cursor].ID)
	}
	held := m // the same in-flight model, for the two arms below

	// 🔴 THE ANSWER ARRIVES WITH A NEW CLIP AT THE HEAD, which is the
	// newest-first store's ordinary case: another device captured something
	// while this visit was away. Every surviving row's INDEX therefore shifts,
	// so holding the index would point one row off and only holding the ID
	// tracks the operator's row. The ids and the indices are pairwise distinct
	// here on purpose — 11/9/8/7 at 0/1/2/3 — so neither a kept index nor a
	// hardcoded 0 or 2 can pass this.
	grown := append([]shared.Clip{{
		ID: 11, Body: "fresh from the phone", Device: "phone", CreatedAt: 1_700_000_100_000,
	}}, clipRows...)
	m = upd(m, sharedFetched{Clips: grown})
	if got := m.filteredClips[m.cursor].ID; got != 7 {
		t.Errorf("the cursor is on clip %d after the fetch landed, want 7 — the row "+
			"under a pending Enter changed out from under the operator", got)
	}
	if m.cursor != 3 {
		t.Errorf("cursor = %d, want 3 — clip 7 moved down one row when the new clip "+
			"landed at the head", m.cursor)
	}
	sel, intents := m.Step(tea.KeyPressMsg{Code: tea.KeyEnter})
	if len(intents) != 1 {
		t.Fatalf("enter produced %+v, want one SelectClip", intents)
	}
	if sc, ok := intents[0].(SelectClip); !ok || sc.ID != 7 {
		t.Errorf("enter emitted %+v, want SelectClip{ID: 7} — the clip the cursor "+
			"was on before the fetch landed", intents[0])
	}
	if sel.ClipPasted != 7 {
		t.Errorf("ClipPasted = %d, want 7", sel.ClipPasted)
	}

	// And an answer that carries the same rows in the same order leaves the
	// cursor exactly where it was — driven through the real cmd the keypress
	// returned, rather than a hand-built message.
	same := held
	for _, msg := range runCmd(pending) {
		same = upd(same, msg)
	}
	if same.cursor != 2 || same.filteredClips[same.cursor].ID != 7 {
		t.Errorf("an unchanged answer moved the cursor to %d (clip %d), want 2 (clip 7)",
			same.cursor, same.filteredClips[same.cursor].ID)
	}

	// 🔴 AND WHEN THE ID IS GONE FROM THE ANSWER, THE CURSOR CLAMPS INTO RANGE
	// rather than being left past the end of a shorter list: another device's
	// delete, or this picker's own post-delete re-list.
	gone := upd(held, sharedFetched{Clips: clipRows[:1]})
	if gone.cursor != 0 {
		t.Errorf("cursor = %d over a one-row answer, want 0", gone.cursor)
	}
	gone.Width = 120
	_ = gone.Lines() // the clamp is what keeps this render in range at all
}

// 🔴 THE FRAME FITS THE TERMINAL HEIGHT IN EVERY SHARED STATE, THE RE-LIST
// INCLUDED. chromeLines counted the header, footer and notice but not
// sharedBodyLine, which Lines appends IN ADDITION to the rows while fetching —
// the re-entry and post-delete states. Measured at Height=10: 10 lines settled,
// 11 re-listing (round-1 finding 8).
func TestTheSharedFrameFitsTheHeight(t *testing.T) {
	many := make([]shared.Clip, 12)
	for i := range many {
		many[i] = shared.Clip{
			ID:        int64(i + 1),
			Body:      fmt.Sprintf("clip body %d", i+1),
			Device:    "phone",
			CreatedAt: 1_700_000_000_000,
		}
	}
	fx := &fakeLauncher{clips: many, clipsAfterDelete: many[1:]}

	check := func(name string, m Model) {
		if n := len(m.Lines()); n > m.Height {
			t.Errorf("%s: the frame is %d lines on a %d-line terminal — it overflows "+
				"by %d:\n%s", name, n, m.Height, n-m.Height,
				strings.Join(bareLines(m), "\n"))
		}
	}
	settled := enterShared(t, fx)
	settled.Height, settled.Width = 10, 120
	check("settled", settled)

	// Re-entry: the previous rows are on screen AND the fetching line is up.
	back := upd(settled, tea.KeyPressMsg{Code: tea.KeyEscape})
	reentry, _ := updCmd(back, ctrl('g'))
	reentry.Height, reentry.Width = 10, 120
	if !reentry.fetching || len(reentry.filteredClips) == 0 {
		t.Fatalf("fixture: fetching=%v rows=%d", reentry.fetching, len(reentry.filteredClips))
	}
	check("re-entering with stale rows", reentry)

	// The post-delete re-list: same shape, reached the other way.
	armed, _ := settled.Step(ctrl('x'))
	relist, _ := updCmd(armed, tea.KeyPressMsg{Code: 'y', Text: "y"})
	relist = upd(relist, clipDeleted{})
	relist.Height, relist.Width = 10, 120
	if !relist.fetching {
		t.Fatalf("fixture: the post-delete re-list is not in flight")
	}
	check("post-delete re-list", relist)

	// With the notice line up as well — every chrome line at once.
	noticed := relist
	noticed.notice = "a notice line is up too"
	check("re-list with a notice", noticed)

	// POSITIVE CONTROL ON THE MEASUREMENT: at an absurd height the chrome alone
	// cannot fit and the frame DOES exceed it. So `len(Lines()) > Height` is a
	// comparison that can come out true, and the three passes above are real
	// zeros rather than an assertion that never fires.
	tiny := settled
	tiny.Height = 2 // a terminal too short for the header and footer alone
	if n := len(tiny.Lines()); n <= tiny.Height {
		t.Fatalf("a 3-line terminal rendered %d lines without overflowing — the "+
			"overflow check above cannot see one and proves nothing", n)
	}
	// And the FULL frame really is at the budget, not comfortably under it: a
	// settled 10-line terminal shows 10 lines, so one uncounted chrome line is
	// an overflow rather than slack absorbing it.
	if n := len(settled.Lines()); n != settled.Height {
		t.Errorf("the settled frame is %d lines on a %d-line terminal — this "+
			"fixture has slack, so it could absorb an uncounted chrome line",
			n, settled.Height)
	}
}

// clipIDsOf is the ids of a clip slice, for comparing a list against a literal.
func clipIDsOf(cs []shared.Clip) []int64 {
	var out []int64
	for _, c := range cs {
		out = append(out, c.ID)
	}
	return out
}
