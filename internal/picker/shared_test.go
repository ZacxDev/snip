package picker

import (
	"errors"
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

// 🔴 THE TWO CHORDS ARE DISTINCT KEYS AND NEITHER IS A KEY THE BROWSE TABLE
// ALREADY OWNS. A chord that collided would silently take over an existing
// binding — the one failure a per-key test cannot see, because each would
// still pass on its own. This is the enumerated ledger: it fails when the
// browse set GROWS into these keys or when the two shared chords converge.
func TestTheSharedChordsCollideWithNothing(t *testing.T) {
	browseKeys := map[string]bool{
		"esc": true, "ctrl+c": true, "enter": true, "return": true,
		"up": true, "ctrl+p": true, "down": true, "ctrl+n": true,
		"ctrl+e": true, "backspace": true,
	}
	if SharedKey == DeleteKey {
		t.Fatalf("the mode chord and the delete chord are the same key (%q)", SharedKey)
	}
	if browseKeys[DeleteKey] {
		t.Errorf("%s is already a browse-table key", DeleteKey)
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
	for _, msg := range runCmd(cmd) {
		m = upd(m, msg)
	}
	if fx.fetches != 2 {
		t.Fatalf("fetches = %d after two entries, want 2 — every toggle back re-reads the store", fx.fetches)
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
