// Side-effect helpers of the picker: the telemetry emission seam and the
// origin-workspace lookup. The pure model (picker.go) never touches the
// world; every world-touching line lives here or behind the Launcher.
package picker

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/ZacxDev/snip/internal/telemetry"
)

// stderrW is the picker's error channel; a test overrides it to capture.
var stderrW = os.Stderr

// emitFireFn is the row emitter as a package var so tests can stub it.
// Default: the shared telemetry emitter (spool contract in internal/telemetry).
// The SOURCE is a parameter, not a constant: a shared-clipboard row must not
// land in source=snip, where the audit's analysis would read it as demand for
// a catalog snippet (telemetry.SourceClip says why).
var emitFireFn = func(p telemetry.Payload, source string) string {
	return telemetry.EmitFireSource(p, source, "")
}

func emitFire(p telemetry.Payload, source string) string { return emitFireFn(p, source) }

// nowMSFn is the real clock the clip rows' ages render against, and it lives
// here because this is the world-touching file: the pure model (picker.go,
// shared.go) must not call time.Now itself.
//
// 🔴 IT IS NOT THE TEST SEAM — that is Model.NowMS, which Model.nowMS prefers
// whenever it is non-zero, and which is what the clock-sensitive test sets. No
// test assigns this var; the comment here used to claim one did.
var nowMSFn = func() int64 { return time.Now().UnixMilli() }

// editorCommand resolves the editor to run on the staged body and returns the
// command for the picker to hand the terminal to.
//
// 🔴 IT IS RESOLVED HERE, IN THE WORLD-TOUCHING FILE, AND BY ONE RULE:
// $SNIP_EDITOR, then $VISUAL, then $EDITOR, then `nvim`. Snip's own variable
// wins so the e2e and the tests can pin one without touching the operator's
// shell; VISUAL precedes EDITOR because that is the convention the operator's
// environment already follows (both are `nvim` there).
//
// The value may carry ARGUMENTS ("nvim -f"), so it is split on whitespace — an
// editor that cannot be spelled with space-separated flags is not supported,
// and REFUSING it is better than putting the whole string in argv[0] and
// failing with a confusing exec error.
func editorCommand(path string) *exec.Cmd {
	for _, key := range []string{"SNIP_EDITOR", "VISUAL", "EDITOR"} {
		val := strings.TrimSpace(os.Getenv(key))
		if val == "" {
			continue
		}
		fields := strings.Fields(val)
		return exec.Command(fields[0], append(fields[1:], path)...)
	}
	return exec.Command("nvim", path)
}

// focusedWorkspace reads the currently focused i3 workspace number for the
// telemetry row. Best-effort: i3 unavailable or unparseable → 0. This is the
// ONE i3 read the picker makes itself; the paste helper does its own (after
// its own focus, where the focused workspace is definitionally the origin's).
func focusedWorkspace() int {
	out, err := exec.Command("i3-msg", "-t", "get_workspaces").Output()
	if err != nil {
		return 0
	}
	var ws []struct {
		Num     int  `json:"num"`
		Focused bool `json:"focused"`
	}
	if json.Unmarshal(out, &ws) != nil {
		return 0
	}
	for _, w := range ws {
		if w.Focused {
			return w.Num
		}
	}
	return 0
}
