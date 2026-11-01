// Package effects is snip's live X11/session layer — the small surface where
// the Go tool touches the world outside its own files. Same discipline as the
// sibling stt-voice effects package:
//
// 🔴 EVERY EFFECT IS AN INTERFACE, so the paste/picker tests run with a fake
// that records calls. No unit test reaches a real X server; the LIVE
// implementations below are exec'd by bare name and are exercised for real
// only by scripts/tests/snip_e2e.sh (Xvfb) and the operator's manual verify.
//
// 🔴 THE BINARIES ARE RESOLVED VIA PATH, AND THE PACKAGING PINS THEM. The
// call chain starts in an i3 `exec` with the session PATH, which
// ~/.nix-profile blanks for ~30s during every home-manager switch — so
// default.nix wraps the binary with makeBinPath over exactly the tools these
// functions exec (xclip, xdotool, i3-msg, dunstify, alacritty). Add an
// effect, grow that list in the same commit.
package effects

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// SnipWindowClass is the WM_CLASS the picker window carries — set by
// snip-launch's `alacritty --class snip,snip`. It is ONE value spelled in
// THREE places: this constant (the wait-for-gone probe below), snip-launch
// in nix/pkgs/tools/snip/default.nix, and nix/i3/config.nix's for_window
// rule — scripts/tests/test_i3_snip.py pins all three together.
const SnipWindowClass = "snip"

// snipGoneTimeout bounds the picker-must-die wait: the picker quits the
// moment it spawns the paste helper, so a snip-class window still alive after
// this long means the TUI is wedged — pasting then would land INSIDE the
// picker (the measured focus-steal failure mode), so the paste aborts instead.
const snipGoneTimeout = 3 * time.Second

// snipGonePoll is the wait's poll interval.
const snipGonePoll = 25 * time.Millisecond

// PasteKeyFor maps an origin WM_CLASS to the paste keystroke. Default
// `ctrl+v`; terminals need `ctrl+shift+v` — alacritty's default binding
// forwards plain Ctrl+V to the child as readline quoted-insert, which is the
// measured reason this entry exists. The match is case-insensitive and EXACT —
// WM_CLASS conventions capitalise ("Alacritty", "XTerm").
//
// 🔴 THE MAP IS EMBEDDED, NOT CONFIGURABLE — a small honest table over a
// measured seed, grown per-app only when a real desktop shows a class the
// default key does not reach. An end-to-end test verifies the mapping.
func PasteKeyFor(class string) string {
	if strings.EqualFold(class, "alacritty") {
		return "ctrl+shift+v"
	}
	return "ctrl+v"
}

// Effects is the interface the paste helper acts through.
type Effects interface {
	// ClipboardGet reads the CLIPBOARD selection; ok=false when no owner
	// exists (an empty clipboard is a normal state, not an error).
	ClipboardGet() (data []byte, ok bool)
	// ClipboardSet puts bytes on the CLIPBOARD selection (xclip -i, stdin —
	// no shell, no length limit).
	ClipboardSet(data []byte) error
	// WindowExists reports whether an X window id still exists.
	WindowExists(id int) bool
	// WaitSnipGone blocks until NO window of class snip exists, or the
	// bounded timeout expires (error).
	WaitSnipGone() error
	// Focus asks i3 to focus the container; the i3-msg JSON REPLY is the
	// authority (i3-msg answers success:false in JSON with exit 0 — check
	// the reply, not the exit code).
	Focus(containerID int) error
	// PinFocus pins the X-server-level input focus on the window AND waits
	// the measured beat before returning (the stt-voice lesson, live
	// 2026-09-30: a client needs a beat after a focus transfer or the first
	// keystrokes of the burst are dropped entirely — 'CHAINED' arrived as
	// 'e CHAINED'). i3's focus sets the WM/frame focus; the X input focus
	// pin is what xdotool key actually targets against.
	PinFocus(containerID int) error
	// FocusedWorkspace returns the number of the currently focused i3
	// workspace, ok=false when i3 cannot answer.
	FocusedWorkspace() (int, bool)
	// SendKey sends a keystroke to the focused window (xdotool key
	// --clearmodifiers …).
	SendKey(keys string) error
	// Toast surfaces a failure in words. Best effort; never blocks the flow.
	Toast(msg string)
}

// Launcher is the picker's effect surface (internal/picker): the DETACHED
// paste-helper spawn plus the toast. 🔴 THE PICKER MUST DIE BEFORE ANYTHING
// IS PASTED: the TUI quits as soon as SpawnPaste returns; the helper (own
// session, setsid) waits for the snip-class window to be gone, then focuses
// and pastes. Typing while the TUI is still open is measured broken
// (stt-voice, 2026-09-30).
type Launcher interface {
	// SpawnPaste launches `self paste --label L` detached and returns
	// without waiting for it, injecting the picker's handoff values into the
	// child environment:
	//
	//	SNIP_QUERY             the exact typed string (may be "")
	//	SNIP_OPEN_LATENCY_MS   the picker's open-phase latency
	//
	// SNIP_ORIGIN_WINDOW / SNIP_ORIGIN_CLASS are already in the environment —
	// snip-launch captured them before this alacritty existed, and every
	// process down this chain inherits them.
	SpawnPaste(label, query string, openLatencyMS int64) error
	// Toast surfaces a failure in words. Best effort.
	Toast(msg string)
}

// Live is the real implementation.
type Live struct{}

// ClipboardGet reads the CLIPBOARD selection. xclip exits non-zero when no
// process owns the selection — a normal empty-clipboard state, reported as
// ok=false rather than an error.
func (Live) ClipboardGet() ([]byte, bool) {
	out, err := exec.Command("xclip", "-selection", "clipboard", "-o").Output()
	if err != nil {
		return nil, false
	}
	return out, true
}

// ClipboardSet feeds xclip on stdin: no argument parsing, no shell, no length
// limit (the stt-voice effects.Copy precedent).
//
// 🔴 STDOUT/STDERR GO TO FILES, NOT PIPES — measured (2026-10-29, the e2e's
// first hang): xclip -i forks a selection-owner CHILD that outlives the
// parent and INHERITS whatever stdout/stderr it was given. With
// CombinedOutput those are pipes, and exec's Wait blocks reading them until
// the grandchild exits — which is never (it lives as the clipboard owner).
// A real file FD has no reader to block on, so the parent's exit is what
// Wait sees, and the error text is still captured.
func (Live) ClipboardSet(data []byte) error {
	errF, err := os.CreateTemp("", "snip-xclip-err-*")
	if err != nil {
		return fmt.Errorf("xclip: scratch file: %w", err)
	}
	defer os.Remove(errF.Name())
	defer errF.Close()
	cmd := exec.Command("xclip", "-selection", "clipboard")
	cmd.Stdin = bytes.NewReader(data)
	cmd.Stdout = errF // grandchild-safe: see the comment above
	cmd.Stderr = errF
	if err := cmd.Run(); err != nil {
		out, _ := os.ReadFile(errF.Name())
		return fmt.Errorf("xclip: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// WindowExists probes an X window id. `xdotool getwindowname` exits non-zero
// for a dead id — the cheap honest check.
func (Live) WindowExists(id int) bool {
	return exec.Command("xdotool", "getwindowname", strconv.Itoa(id)).Run() == nil
}

// WaitSnipGone polls `xdotool search --onlyvisible --class ^snip$` until it
// finds nothing (exit 1 = no window) or the bounded timeout expires. The
// anchor `^snip$` is load-bearing: `--class` is a REGEX match, and an
// unanchored "snip" would also match e.g. a "snip-e2e" window — the exact
// window the e2e harness pastes into.
func (Live) WaitSnipGone() error {
	deadline := time.Now().Add(snipGoneTimeout)
	for {
		err := exec.Command("xdotool", "search", "--onlyvisible", "--class", "^"+SnipWindowClass+"$").Run()
		if err != nil {
			return nil // no (visible) snip window: the picker is gone
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("a %q window is still alive after %s — refusing to paste into it", SnipWindowClass, snipGoneTimeout)
		}
		time.Sleep(snipGonePoll)
	}
}

// i3Reply is the shape i3-msg answers a command with: a JSON array of
// {success, error} objects. i3-msg exits 0 even on success:false, so the
// reply — not the exit status — is the authority.
type i3Reply struct {
	Success bool   `json:"success"`
	Error   string `json:"error"`
}

// focusReplyError is the reply half of Focus, split out so tests can pin it
// against the exact JSON shapes i3 answers with — no i3 process needed.
func focusReplyError(containerID int, out []byte) error {
	var replies []i3Reply
	if err := json.Unmarshal(bytes.TrimSpace(out), &replies); err != nil {
		return fmt.Errorf("i3-msg focus: unparseable reply %q: %v", strings.TrimSpace(string(out)), err)
	}
	if len(replies) == 0 || !replies[0].Success {
		msg := "window is gone"
		if len(replies) > 0 && replies[0].Error != "" {
			msg = replies[0].Error
		}
		return fmt.Errorf("i3-msg focus: window %d: %s", containerID, msg)
	}
	return nil
}

// Focus focuses the captured container (the effects.Focus precedent, including
// its lesson: parse the reply; a bracketed id that no longer exists is the
// common case).
func (Live) Focus(containerID int) error {
	out, err := exec.Command("i3-msg", fmt.Sprintf("[id=%d]", containerID), "focus").CombinedOutput()
	if err != nil {
		return fmt.Errorf("i3-msg focus: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return focusReplyError(containerID, out)
}

// focusBeat is the measured pause after an X-level focus transfer before
// any keystroke is sent (stt-voice's live 2026-09-30 measurement; see
// PinFocus).
const focusBeat = 100 * time.Millisecond

// PinFocus pins the X input focus on the window (xdotool windowfocus) and
// waits the beat. A dead window fails here with a BadWindow-style error —
// the same abort-on-gone refusal the stt-voice chain relies on.
func (Live) PinFocus(containerID int) error {
	out, err := exec.Command("xdotool", "windowfocus", strconv.Itoa(containerID)).CombinedOutput()
	if err != nil {
		return fmt.Errorf("xdotool windowfocus %d: %v: %s", containerID, err, strings.TrimSpace(string(out)))
	}
	time.Sleep(focusBeat)
	return nil
}

// FocusedWorkspace reads i3's workspace list and returns the focused one's
// number. Best-effort context for the telemetry row; ok=false whenever i3
// cannot answer.
func (Live) FocusedWorkspace() (int, bool) {
	out, err := exec.Command("i3-msg", "-t", "get_workspaces").Output()
	if err != nil {
		return 0, false
	}
	var ws []struct {
		Num     int  `json:"num"`
		Focused bool `json:"focused"`
	}
	if json.Unmarshal(out, &ws) != nil {
		return 0, false
	}
	for _, w := range ws {
		if w.Focused {
			return w.Num, true
		}
	}
	return 0, false
}

// SendKey sends one keystroke to the FOCUSED window. --clearmodifiers so a
// held modifier (the key that opened the picker) cannot corrupt the paste
// chord.
func (Live) SendKey(keys string) error {
	out, err := exec.Command("xdotool", "key", "--clearmodifiers", keys).CombinedOutput()
	if err != nil {
		return fmt.Errorf("xdotool key %s: %v: %s", keys, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Toast surfaces a failure in words (dunstify — the dunst install is in the
// home closure). Best effort; never blocks the flow.
func (Live) Toast(msg string) {
	_ = exec.Command("dunstify", "snip", msg).Run()
}

// --- the edit path (the picker's ctrl+e) --------------------------------------
//
// 🔴 THREE WORLD CALLS, ONE SEAM. The picker stages the body in a scratch file,
// hands the terminal to the editor, reads it back, and then asks THIS to land
// it. The landing is an external script (`$SNIP_REPO/scripts/snip-edit.sh`)
// rather than Go, because landing a catalog change may involve rewriting a
// generated source file, re-validating it and driving VCS — and because a
// script is the thing a user can run by hand when the TUI is not the right
// surface.

// snipRepoDir is the checkout the landing happens in: $SNIP_REPO, and nothing
// else. snip cannot guess which checkout holds the landing script — a default
// here would be the AUTHOR's path, not the user's — so with nothing set, the
// edit/delete landing fails with the one-line remedy from runLanding.
func snipRepoDir() string {
	return os.Getenv("SNIP_REPO")
}

// StageEdit writes the current body to a scratch file — the file the editor is
// pointed at.
//
// The label is folded into the FILE NAME (sanitised) so a scratch file left
// behind by a crash names its own row in /tmp rather than being an anonymous
// `snip-edit-1234.txt`.
func (Live) StageEdit(label, body string) (string, error) {
	f, err := os.CreateTemp("", "snip-edit-"+scratchSlug(label)+"-*.txt")
	if err != nil {
		return "", fmt.Errorf("scratch file: %w", err)
	}
	if _, err := f.WriteString(body); err != nil {
		name := f.Name()
		f.Close()
		os.Remove(name)
		return "", fmt.Errorf("write the scratch file: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", fmt.Errorf("close the scratch file: %w", err)
	}
	return f.Name(), nil
}

// scratchSlug reduces a label to something safe for a file name: letters,
// digits, dot, dash and underscore survive; everything else becomes `-`.
// Bounded to 40 characters so a long label cannot push the name past NAME_MAX
// once CreateTemp's random suffix is appended.
func scratchSlug(label string) string {
	var b strings.Builder
	for _, r := range label {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
		if b.Len() >= 40 {
			break
		}
	}
	return b.String()
}

// ReadEdit reads the scratch file back after the editor exits.
func (Live) ReadEdit(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read the edited body: %w", err)
	}
	return string(b), nil
}

// ApplyEdit lands the edited body through the landing script: it rewrites the
// catalog's source, validates the result, and commits — whatever the landing
// script's contract is. An error means the landing was REFUSED and nothing was
// changed.
//
// 🔴 THE SCRIPT'S STDERR IS THE MESSAGE. Every refusal it makes says WHY in one
// line ("already has uncommitted changes", "the edit evaluates to ..."), and
// that line is what the operator needs in the TUI — so it is passed through
// verbatim rather than replaced by "exit status 3".
func (Live) ApplyEdit(label, bodyFile string) error {
	return runLanding(label, bodyFile)
}

// ApplyDelete lands the removal of a snippet's row, through the landing
// script's EXPLICIT delete mode.
//
// 🔴 THE MODE IS A FLAG, NOT AN EMPTY FILE. The picker could have passed an
// empty body file and let the script infer "delete" — but that inference is
// exactly what made an emptied body land as a live no-op row, and an inference
// that has already been wrong once does not become right by moving it one layer
// down.
func (Live) ApplyDelete(label string) error {
	return runLanding("--delete", label)
}

// runLanding runs scripts/snip-edit.sh with `args` and passes its stderr through
// verbatim: the script's refusals are one-line explanations ("already has
// uncommitted changes", "the row is not gone from the evaluated artifact"), and
// that line is what the operator needs in the TUI — not "exit status 3".
func runLanding(extra ...string) error {
	repo := snipRepoDir()
	if repo == "" {
		return fmt.Errorf("cannot resolve the repo checkout (set SNIP_REPO)")
	}
	script := filepath.Join(repo, "scripts", "snip-edit.sh")
	if _, err := os.Stat(script); err != nil {
		return fmt.Errorf("no %s (set SNIP_REPO if the checkout lives elsewhere)", script)
	}
	out, err := exec.Command("bash", append([]string{script}, extra...)...).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("%s", msg)
	}
	return nil
}

// SpawnPaste implements picker.Launcher: `self paste --label L` detached
// (own session), with the picker's handoff values (query, open latency)
// injected into the child environment. Returns as soon as the child is
// running; the TUI quits the moment this returns.
//
// A non-empty bodyFile adds `--body-file` — the edited body, which the
// deployed artifact does not carry yet. The helper consumes (and removes) it.
func (Live) SpawnPaste(label, query string, openLatencyMS int64, bodyFile string) error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve self: %w", err)
	}
	args := []string{"paste", "--label", label}
	if bodyFile != "" {
		args = append(args, "--body-file", bodyFile)
	}
	cmd := exec.Command(self, args...)
	cmd.Env = append(os.Environ(),
		"SNIP_QUERY="+query,
		"SNIP_OPEN_LATENCY_MS="+strconv.FormatInt(openLatencyMS, 10),
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("spawn paste helper: %w", err)
	}
	go func() { _ = cmd.Wait() }() // reap the detached child; its failures toast
	return nil
}
