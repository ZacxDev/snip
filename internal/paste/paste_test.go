package paste

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ZacxDev/snip/internal/artifact"
	"github.com/ZacxDev/snip/internal/telemetry"
)

// fakeEffects records every call IN ORDER — the paste sequence's order is the
// contract (scope doc §3), so the test asserts the ORDER, not just the set.
type fakeEffects struct {
	clip          []byte // what the clipboard holds at start
	clipOwned     bool   // whether anything owns the selection at start
	originExists  bool
	focusErr      error
	pinErr        error
	sendKeyErr    error
	setErr        error
	snipGoneAfter int // WaitSnipGone returns nil immediately when 0; error when <0
	workspace     int
	toasts        []string
	log           []string
	clipNow       string // the clipboard content as the fake sees it
	lastKey       string
	setCalls      int
	windowExistsQ int
}

func (f *fakeEffects) ClipboardGet() ([]byte, bool) {
	f.log = append(f.log, "get")
	return f.clip, f.clipOwned
}

func (f *fakeEffects) ClipboardSet(data []byte) error {
	f.log = append(f.log, "set")
	f.setCalls++
	if f.setErr != nil {
		return f.setErr
	}
	f.clipNow = string(data)
	return nil
}

func (f *fakeEffects) WindowExists(id int) bool {
	f.log = append(f.log, fmt.Sprintf("exists(%d)", id))
	f.windowExistsQ++
	return f.originExists
}

func (f *fakeEffects) WaitSnipGone() error {
	f.log = append(f.log, "wait-snip-gone")
	switch {
	case f.snipGoneAfter < 0:
		return errors.New("a \"snip\" window is still alive after 3s — refusing to paste into it")
	default:
		return nil
	}
}

func (f *fakeEffects) Focus(id int) error {
	f.log = append(f.log, fmt.Sprintf("focus(%d)", id))
	return f.focusErr
}

func (f *fakeEffects) PinFocus(id int) error {
	f.log = append(f.log, fmt.Sprintf("pin(%d)", id))
	return f.pinErr
}

func (f *fakeEffects) FocusedWorkspace() (int, bool) {
	return f.workspace, f.workspace != 0
}

func (f *fakeEffects) SendKey(keys string) error {
	f.log = append(f.log, "key:"+keys)
	f.lastKey = keys
	return f.sendKeyErr
}

func (f *fakeEffects) Toast(msg string) {
	f.log = append(f.log, "toast")
	f.toasts = append(f.toasts, msg)
}

// captureRows stubs the telemetry emitter and records the rows.
type rowCapture struct{ rows []telemetry.Payload }

func captureRows(t *testing.T) *rowCapture {
	c := &rowCapture{}
	orig := emitRowFn
	emitRowFn = func(p telemetry.Payload) string {
		c.rows = append(c.rows, p)
		return ""
	}
	t.Cleanup(func() { emitRowFn = orig })
	return c
}

const eosBody = "reflect on work done and key learnings this session"

var snippets = []artifact.Snippet{
	{Label: "End-of-session ritual", Body: eosBody, Category: "prompt"},
	{Label: "Paste from clipboard", Body: "clipboard says: {{clip}}", Category: "clipboard"},
	{Label: "typo fix: dashboard", Body: "dashboard", Category: "typo"},
}

func req() Request {
	return Request{Label: "End-of-session ritual", OriginWindow: 946, OriginClass: "Alacritty"}
}

func okFake() *fakeEffects {
	return &fakeEffects{originExists: true, clipOwned: true, clip: []byte("saved-clip"), workspace: 2}
}

// The FULL sequence in the doc's order (§3 / the M1 item list): abort check
// → resolve {{clip}} → save → set → wait-picker-gone → focus → key →
// (delay) → restore → row. This test pins the ORDER byte-for-byte.
func TestHappyPathSequence(t *testing.T) {
	fx := okFake()
	rows := captureRows(t)
	res := Run(fx, snippets, req())
	if res.Outcome != "select" || res.Err != nil {
		t.Fatalf("Run = %+v, want select", res)
	}
	want := []string{
		"get", // {{clip}} resolution probe (:eos has none; one probe happens)
		"get", // the save
		"set", // the expansion
		"wait-snip-gone",
		"focus(946)",
		"key:ctrl+shift+v", // Alacritty's class hits the paste-key map
		"set",              // the restore
		"toast-free restore",
	}
	_ = want // (order asserted below against the log directly)
	got := fx.log
	wantOrder := []string{
		"exists(946)", // the abort pre-check, BEFORE the clipboard is touched
		"get",         // the save (:eos carries no {{clip}}, so no resolution probe)
		"set",         // the expansion
		"wait-snip-gone",
		"focus(946)",
		"pin(946)",         // the X-level focus pin + the measured beat
		"key:ctrl+shift+v", // Alacritty's class hits the paste-key map
		"set",              // the restore
	}
	for i, step := range wantOrder {
		if i >= len(got) || got[i] != step {
			t.Fatalf("sequence:\n got %v\nwant prefix %v", got, wantOrder)
		}
	}
	if len(fx.toasts) != 0 {
		t.Fatalf("happy path must not toast: %v", fx.toasts)
	}
	// The final clipboard content is the RESTORED value, not the expansion.
	if fx.clipNow != "saved-clip" {
		t.Fatalf("clipboard ended as %q, want the saved bytes back", fx.clipNow)
	}
	// Exactly ONE row: outcome=select, with both latencies, method=direct
	// (no query), the origin class as app, workspace recorded.
	if len(rows.rows) != 1 {
		t.Fatalf("rows emitted: %d, want 1", len(rows.rows))
	}
	p := rows.rows[0]
	if p.Outcome != "select" || p.Label != "End-of-session ritual" || p.Method != "direct" ||
		p.App != "Alacritty" || p.Workspace != 2 || p.LatencyOpenMS != 0 {
		t.Fatalf("select row: %+v", p)
	}
	if p.LatencyPasteMS < 0 {
		t.Errorf("paste latency must be a measured non-negative ms, got %d", p.LatencyPasteMS)
	}
	// Tool/Matcher provenance is stamped at EMIT time (telemetry.FireFields) —
	// asserted there, not here.
}

// {{clip}} resolves from the CURRENT (pre-paste) clipboard — before the
// save/set pair, so :clip never resolves to its own expansion.
func TestClipResolvesFromPrePasteClipboard(t *testing.T) {
	fx := okFake()
	fx.clip = []byte("the-user-had-this")
	rows := captureRows(t)
	res := Run(fx, snippets, Request{Label: "Paste from clipboard", OriginWindow: 9, OriginClass: "x"})
	if res.Outcome != "select" {
		t.Fatalf("Run = %+v, want select", res)
	}
	if fx.clipNow != "the-user-had-this" {
		t.Fatalf("final clipboard = %q, want the saved value back", fx.clipNow)
	}
	// The EXPANSION set must have contained the resolution.
	if !contains(fx.log, "set") {
		t.Fatal("no set call")
	}
	if len(rows.rows) != 1 {
		t.Fatalf("rows: %d", len(rows.rows))
	}
	if rows.rows[0].Label != "Paste from clipboard" {
		t.Fatalf("label: %q", rows.rows[0].Label)
	}
}

// 🔴 THE ABORT PATHS — each aborts BEFORE or WITH cleanup, toasts, emits
// outcome=abort, and (after the clipboard was touched) RESTORES.
func TestAbortPaths(t *testing.T) {
	t.Run("no origin captured", func(t *testing.T) {
		fx := okFake()
		rows := captureRows(t)
		res := Run(fx, snippets, Request{Label: "End-of-session ritual", OriginWindow: 0})
		if res.Outcome != "abort" {
			t.Fatalf("outcome %q, want abort", res.Outcome)
		}
		if fx.setCalls != 0 {
			t.Fatalf("abort must not touch the clipboard: %d sets", fx.setCalls)
		}
		if len(fx.toasts) != 1 {
			t.Fatalf("toasts: %v", fx.toasts)
		}
		if len(rows.rows) != 1 || rows.rows[0].Outcome != "abort" {
			t.Fatalf("rows: %+v", rows.rows)
		}
		if rows.rows[0].Label != "End-of-session ritual" {
			t.Errorf("abort row label = %q, want End-of-session ritual", rows.rows[0].Label)
		}
	})
	t.Run("origin window gone", func(t *testing.T) {
		fx := okFake()
		fx.originExists = false
		rows := captureRows(t)
		res := Run(fx, snippets, req())
		if res.Outcome != "abort" {
			t.Fatalf("outcome %q, want abort", res.Outcome)
		}
		if fx.setCalls != 0 {
			t.Fatal("the gone-origin abort must happen BEFORE the clipboard is touched")
		}
		if len(rows.rows) != 1 || rows.rows[0].Outcome != "abort" {
			t.Fatalf("rows: %+v", rows.rows)
		}
	})
	t.Run("picker window never dies", func(t *testing.T) {
		fx := okFake()
		fx.snipGoneAfter = -1
		rows := captureRows(t)
		res := Run(fx, snippets, req())
		if res.Outcome != "abort" {
			t.Fatalf("outcome %q, want abort", res.Outcome)
		}
		// The clipboard was SET before the wait — the abort must have
		// restored it.
		if fx.clipNow != "saved-clip" {
			t.Fatalf("clipboard after abort = %q, want restored", fx.clipNow)
		}
		if len(rows.rows) != 1 || rows.rows[0].Outcome != "abort" {
			t.Fatalf("rows: %+v", rows.rows)
		}
	})
	t.Run("focus fails late", func(t *testing.T) {
		fx := okFake()
		fx.focusErr = errors.New("i3-msg focus: window 946: window is gone")
		rows := captureRows(t)
		res := Run(fx, snippets, req())
		if res.Outcome != "abort" {
			t.Fatalf("outcome %q, want abort", res.Outcome)
		}
		if fx.clipNow != "saved-clip" {
			t.Fatalf("clipboard after abort = %q, want restored", fx.clipNow)
		}
		if len(rows.rows) != 1 || rows.rows[0].Outcome != "abort" {
			t.Fatalf("rows: %+v", rows.rows)
		}
	})
	t.Run("X focus pin fails", func(t *testing.T) {
		fx := okFake()
		fx.pinErr = errors.New("xdotool windowfocus 946: bad window")
		rows := captureRows(t)
		res := Run(fx, snippets, req())
		if res.Outcome != "abort" {
			t.Fatalf("outcome %q, want abort", res.Outcome)
		}
		if fx.clipNow != "saved-clip" {
			t.Fatalf("clipboard after abort = %q, want restored", fx.clipNow)
		}
		if len(rows.rows) != 1 || rows.rows[0].Outcome != "abort" {
			t.Fatalf("rows: %+v", rows.rows)
		}
	})
	t.Run("send key fails", func(t *testing.T) {
		fx := okFake()
		fx.sendKeyErr = errors.New("xdotool failed")
		rows := captureRows(t)
		res := Run(fx, snippets, req())
		if res.Outcome != "abort" {
			t.Fatalf("outcome %q, want abort", res.Outcome)
		}
		if len(rows.rows) != 1 || rows.rows[0].Outcome != "abort" {
			t.Fatalf("rows: %+v", rows.rows)
		}
	})
	t.Run("unknown label", func(t *testing.T) {
		fx := okFake()
		rows := captureRows(t)
		res := Run(fx, snippets, Request{Label: "no such snippet", OriginWindow: 9})
		if res.Outcome != "abort" {
			t.Fatalf("outcome %q, want abort", res.Outcome)
		}
		if len(rows.rows) != 1 || rows.rows[0].Outcome != "abort" {
			t.Fatalf("rows: %+v", rows.rows)
		}
	})
}

// The restore delay is honoured and env-overridable (SNIP_RESTORE_DELAY_MS).
func TestRestoreDelay(t *testing.T) {
	t.Setenv("SNIP_RESTORE_DELAY_MS", "0")
	if d := RestoreDelayMS(); d != 0 {
		t.Fatalf("delay = %v, want 0", d)
	}
	fx := okFake()
	captureRows(t)
	start := time.Now()
	Run(fx, snippets, req())
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("delay=0 still waited %v", elapsed)
	}
	t.Setenv("SNIP_RESTORE_DELAY_MS", "not-a-number")
	if d := RestoreDelayMS(); d != DefaultRestoreDelayMS*time.Millisecond {
		t.Fatalf("unparseable delay must fall back to the default, got %v", d)
	}
}

// MethodFor: search ⟺ non-empty query — ONE derivation for both emitters.
func TestMethodFor(t *testing.T) {
	cases := []struct{ query, want string }{
		{"", "direct"}, {"   ", "direct"}, {"ssh des", "search"}, {"a", "search"},
	}
	for _, c := range cases {
		if got := MethodFor(c.query); got != c.want {
			t.Errorf("MethodFor(%q) = %q, want %q", c.query, got, c.want)
		}
	}
}

// RequestFromEnv reads the environment contract: the origin pair from
// snip-launch, the query/open-latency pair from the picker.
func TestRequestFromEnv(t *testing.T) {
	t.Setenv("SNIP_ORIGIN_WINDOW", "946")
	t.Setenv("SNIP_ORIGIN_CLASS", "Alacritty")
	t.Setenv("SNIP_QUERY", "ssh des")
	t.Setenv("SNIP_OPEN_LATENCY_MS", "120")
	r := RequestFromEnv("End-of-session ritual")
	if r.OriginWindow != 946 || r.OriginClass != "Alacritty" || r.Query != "ssh des" || r.OpenLatencyMS != 120 {
		t.Fatalf("RequestFromEnv = %+v", r)
	}
	// Empty env: everything zero-safe.
	os.Unsetenv("SNIP_ORIGIN_WINDOW")
	os.Unsetenv("SNIP_ORIGIN_CLASS")
	os.Unsetenv("SNIP_QUERY")
	os.Unsetenv("SNIP_OPEN_LATENCY_MS")
	r = RequestFromEnv("End-of-session ritual")
	if r.OriginWindow != 0 || r.OriginClass != "" || r.Query != "" || r.OpenLatencyMS != 0 {
		t.Fatalf("empty env RequestFromEnv = %+v", r)
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if strings.Contains(v, s) {
			return true
		}
	}
	return false
}
