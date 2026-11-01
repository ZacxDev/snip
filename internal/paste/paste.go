// Package paste is the detached paste helper (`snip paste --label L`): the
// deterministic, boring part of snip. It is spawned by the
// picker with setsid and runs OUTLIVE the TUI on purpose — the picker quits
// the moment the spawn succeeds, and the helper's first job is to WAIT until
// the snip-class window is gone before anything is focused or typed.
//
// The sequence, in order (each step's why is inline):
//
//	abort check → resolve {{clip}} → save clipboard → set clipboard
//	→ wait picker-gone → focus origin → send paste key
//	→ wait restore_delay → restore clipboard → emit fire row
//
// 🔴 IF THE ORIGIN WINDOW IS GONE the helper ABORTS — no paste, a dunst toast,
// an outcome=abort row. Pasting into "whatever i3 focuses next" is the
// wrong-window failure mode; aborting is the honest one.
package paste

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ZacxDev/snip/internal/artifact"
	"github.com/ZacxDev/snip/internal/effects"
	"github.com/ZacxDev/snip/internal/telemetry"
)

// ClipVar is the ONE variable the artifact may carry (no shell vars, no date
// vars, no forms — {{clip}} only). It resolves at expand time
// from the X clipboard, which is what espanso's
// `vars = [{name="clip"; type="clipboard";}]` does today.
const ClipVar = "{{clip}}"

// DefaultRestoreDelayMS is how long the helper waits between the paste key
// and the clipboard restore. 🔴 RESIDUAL RACE, same one espanso lives with: an
// app that reads the clipboard later than the delay gets the pre-paste
// content. espanso has the identical race behind its paste_timeout; the delay
// is configurable and documented rather than pretended away.
const DefaultRestoreDelayMS = 300

// RestoreDelayMS resolves the delay: $SNIP_RESTORE_DELAY_MS (milliseconds)
// or the default. Unparseable values fall back to the default (an env typo
// must not turn the restore off).
func RestoreDelayMS() time.Duration {
	if v := os.Getenv("SNIP_RESTORE_DELAY_MS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return time.Duration(n) * time.Millisecond
		}
	}
	return DefaultRestoreDelayMS * time.Millisecond
}

// Request is everything the helper needs from its environment and the picker.
type Request struct {
	Label         string // the selected snippet label (the row's identity)
	OriginWindow  int    // SNIP_ORIGIN_WINDOW, 0 when nothing was captured
	OriginClass   string // SNIP_ORIGIN_CLASS
	Query         string // SNIP_QUERY — the exact typed string (may be "")
	OpenLatencyMS int64  // SNIP_OPEN_LATENCY_MS — the picker's open-phase number
}

// RequestFromEnv builds the request from the process environment: the origin
// pair comes from snip-launch, the query/open-latency pair from the picker.
func RequestFromEnv(label string) Request {
	return Request{
		Label:         label,
		OriginWindow:  atoi(os.Getenv("SNIP_ORIGIN_WINDOW")),
		OriginClass:   os.Getenv("SNIP_ORIGIN_CLASS"),
		Query:         os.Getenv("SNIP_QUERY"),
		OpenLatencyMS: int64(atoi(os.Getenv("SNIP_OPEN_LATENCY_MS"))),
	}
}

func atoi(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

// MethodFor derives payload.method from the query: "search" when a non-empty
// query was typed, "direct" when the row was picked without typing. ONE
// definition — the picker's cancel row and the helper's select/abort rows
// both route through it, so the two emitters cannot drift.
func MethodFor(query string) string {
	if strings.TrimSpace(query) != "" {
		return "search"
	}
	return "direct"
}

// Result is what Run reports to the caller (exit-code mapping in main.go).
type Result struct {
	Outcome string // "select" | "abort"
	Err     error  // non-nil on abort-with-cause
}

// emitRowFn is the row emitter as a package var so tests can capture the rows
// Run emits. Default: the shared telemetry emitter.
var emitRowFn = func(p telemetry.Payload) string { return telemetry.EmitFire(p, "") }

// Run executes the paste sequence. It emits EXACTLY ONE telemetry row per
// invocation — outcome=select on success, outcome=abort for every no-paste
// path — and NEVER returns an error that outranks the row: telemetry and
// toasts are best-effort, the paste decision is deterministic.
func Run(eff effects.Effects, snippets []artifact.Snippet, req Request) Result {
	start := time.Now()
	row := func(outcome string, pasteMS int64) {
		label := ""
		if s, ok := artifact.Find(snippets, req.Label); ok {
			label = s.Label
		}
		workspace := 0
		if outcome == "select" {
			if w, ok := eff.FocusedWorkspace(); ok {
				workspace = w
			}
		}
		emitRowFn(telemetry.Payload{
			Method:         MethodFor(req.Query),
			Query:          req.Query,
			SearchTerm:     req.Query,
			Outcome:        outcome,
			Label:          label,
			App:            req.OriginClass,
			Workspace:      workspace,
			LatencyOpenMS:  req.OpenLatencyMS,
			LatencyPasteMS: pasteMS,
		})
	}
	abort := func(why string) Result {
		eff.Toast("snip: " + why)
		row("abort", 0)
		return Result{Outcome: "abort", Err: fmt.Errorf("%s", why)}
	}

	// Resolve the snippet FIRST: a label the artifact no longer knows is a
	// caller bug (the picker only offers loaded labels) — say so, no paste.
	snippet, ok := artifact.Find(snippets, req.Label)
	if !ok {
		return abort(fmt.Sprintf("label %q is not in the artifact; nothing pasted", req.Label))
	}

	// 🔴 ABORT CHECK — before the clipboard is touched, so an abort leaves the
	// clipboard byte-identical. Nothing answerable was captured (OriginWindow
	// 0: snip-launch found no focused window) and a window that has already
	// died are both aborts.
	if req.OriginWindow <= 0 {
		return abort("no origin window was captured; nothing pasted")
	}
	if !eff.WindowExists(req.OriginWindow) {
		return abort(fmt.Sprintf("origin window %d is gone; nothing pasted", req.OriginWindow))
	}

	// Resolve {{clip}} from the CURRENT clipboard — BEFORE the save/set pair
	// below, so a {{clip}} snippet never resolves to its own expansion.
	body := snippet.Body
	if strings.Contains(body, ClipVar) {
		clip, _ := eff.ClipboardGet()
		body = strings.ReplaceAll(body, ClipVar, string(clip))
	}

	// Save the clipboard bytes (held in memory), then set the expansion.
	saved, savedOK := eff.ClipboardGet()
	if err := eff.ClipboardSet([]byte(body)); err != nil {
		restore(eff, saved, savedOK)
		return abort(fmt.Sprintf("clipboard set failed: %v", err))
	}

	// 🔴 PICKER-MUST-DIE. The picker exits as soon as it spawns this helper;
	// only a wedged TUI keeps a snip-class window alive. Pasting while one is
	// visible is the measured focus-steal failure mode, so the paste aborts.
	if err := eff.WaitSnipGone(); err != nil {
		restore(eff, saved, savedOK)
		return abort(err.Error())
	}

	// Focus the origin. The i3-msg JSON reply is the authority (exit 0 even
	// on failure); a focus failure this late is the race where the origin
	// died between the pre-check and now.
	if err := eff.Focus(req.OriginWindow); err != nil {
		restore(eff, saved, savedOK)
		return abort(fmt.Sprintf("focus origin failed: %v", err))
	}
	// 🔴 THE X-LEVEL FOCUS PIN + THE BEAT. i3's focus is the WM/frame focus;
	// the keystroke lands on the X input focus, and a client needs a beat
	// after a focus transfer or the first keystroke is dropped (stt-voice,
	// measured 2026-09-30). Skipped in the first e2e run and EVERY paste
	// silently delivered nothing — this step is not decoration.
	if err := eff.PinFocus(req.OriginWindow); err != nil {
		restore(eff, saved, savedOK)
		return abort(fmt.Sprintf("focus origin failed (X pin): %v", err))
	}

	// Paste key per origin class, then the text is in the target: that is the
	// end of the paste-phase latency.
	if err := eff.SendKey(effects.PasteKeyFor(req.OriginClass)); err != nil {
		restore(eff, saved, savedOK)
		return abort(fmt.Sprintf("paste key failed: %v", err))
	}
	pasteMS := time.Since(start).Milliseconds()

	// Wait, then restore the saved clipboard bytes. A clipboard that had no
	// owner at save time is left ownerless (restoring an empty owner would
	// fabricate one).
	time.Sleep(RestoreDelayMS())
	restore(eff, saved, savedOK)

	row("select", pasteMS)
	return Result{Outcome: "select"}
}

// restore puts the saved bytes back. Best-effort: a failed restore must not
// crash the helper (the row is already decided); it toasts so the operator
// knows his clipboard changed.
func restore(eff effects.Effects, saved []byte, ok bool) {
	if !ok {
		return
	}
	if err := eff.ClipboardSet(saved); err != nil {
		eff.Toast("snip: clipboard restore failed: " + err.Error())
	}
}
