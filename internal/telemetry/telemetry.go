// Package telemetry is snip's port of the keylog spool emitter
// (scripts/collector/keylog/spool_emit.py): it appends v1 spool lines to the
// SAME spool directory the running activity-collector ships, so snip's
// self-reported fires ride the existing pipeline with no collector change.
//
// The v1 line contract (spool_emit.py:9-17):
//
//		v1<TAB>ts=<...><TAB>source=<s><TAB>kind=<k><TAB>b64:text=<b64>...
//
//	  - Free-text fields go through `b64:<key>=` base64 (no-wrap) so arbitrary
//	    bytes (quotes, newlines, tabs, unicode) survive intact.
//	  - Plain scalar keys (ts, source, kind, host, duration_ms, exit_code) are
//	    written verbatim.
//	  - ts (ClickHouse DateTime64(3) local format) and host are auto-filled when
//	    the caller omits them — mirroring the bash `emit`.
//	  - Append is a single newline-terminated write, opened O_APPEND, so
//	    concurrent writers do not interleave for sub-PIPE_BUF lines.
//
// 🔴 TELEMETRY MUST NEVER BREAK PICKING. Every write path here is
// best-effort: a spool failure degrades to a stderr note, never an error out
// of the picker or the paste helper.
package telemetry

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// CurrentName is the spool's active file (spool_emit.CURRENT_NAME).
const CurrentName = "current.log"

// plainKeys are written verbatim; every other key is base64'd under a `b64:`
// prefix (spool_emit._PLAIN_KEYS).
var plainKeys = map[string]bool{
	"ts": true, "host": true, "source": true, "kind": true,
	"duration_ms": true, "exit_code": true,
}

// Field is one ordered key=value pair. Order is SIGNIFICANT: the Python
// emitter iterates a dict (insertion-ordered); the Go side takes an explicit
// slice so the line shape stays deterministic and byte-compatible.
type Field struct {
	Key   string
	Value string
}

// SpoolDir resolves the spool directory the same way emit/collector do:
// $ACTIVITY_SPOOL_DIR, else ($XDG_STATE_HOME || ~/.local/state)/activity/spool.
func SpoolDir() string {
	if env := os.Getenv("ACTIVITY_SPOOL_DIR"); env != "" {
		return env
	}
	state := os.Getenv("XDG_STATE_HOME")
	if state == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			// Unresolvable home: report the canonical default; the write
			// will fail and degrade to the stderr note, which is the
			// contract (telemetry never breaks picking).
			return filepath.Join("~/.local/state", "activity", "spool")
		}
		state = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(state, "activity", "spool")
}

// TimestampNow renders the UTC instant in ClickHouse DateTime64(3) local
// format: "2006-01-02 15:04:05.mmm" (spool_emit._ts_now).
func TimestampNow(t time.Time) string {
	return t.UTC().Format("2006-01-02 15:04:05.") + fmt.Sprintf("%03d", t.Nanosecond()/1e6)
}

// BuildLine builds one v1 spool line from ordered fields. Plain-scalar keys
// are emitted verbatim; every other key is base64-encoded under a `b64:`
// prefix. ts/host are auto-filled when absent, appended AFTER the caller's
// fields exactly as spool_emit does.
func BuildLine(fields []Field, now time.Time, host string) string {
	parts := make([]string, 0, len(fields)+3)
	parts = append(parts, "v1")
	haveTS, haveHost := false, false
	for _, f := range fields {
		switch f.Key {
		case "ts":
			haveTS = true
		case "host":
			haveHost = true
		}
		if plainKeys[f.Key] {
			parts = append(parts, f.Key+"="+f.Value)
			continue
		}
		enc := base64.StdEncoding.EncodeToString([]byte(f.Value))
		parts = append(parts, "b64:"+f.Key+"="+enc)
	}
	if !haveTS {
		parts = append(parts, "ts="+TimestampNow(now))
	}
	if !haveHost {
		if host == "" {
			host = os.Getenv("HOST")
		}
		if host == "" {
			if hn, err := os.Hostname(); err == nil {
				host = hn
			}
		}
		parts = append(parts, "host="+host)
	}
	line := ""
	for i, p := range parts {
		if i > 0 {
			line += "\t"
		}
		line += p
	}
	return line
}

// Emit appends one event to <spool_dir>/current.log and returns the written
// line. Best-effort + bounded: telemetry must never crash its caller, so a
// failure degrades to a stderr note (the line is still returned for tests).
// O_APPEND single write == atomic for sub-PIPE_BUF lines.
func Emit(fields []Field, spoolDir string) string {
	line := BuildLine(fields, time.Now(), "")
	if spoolDir == "" {
		spoolDir = SpoolDir()
	}
	if err := os.MkdirAll(spoolDir, 0o755); err != nil {
		note(err)
		return line
	}
	f, err := os.OpenFile(filepath.Join(spoolDir, CurrentName), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		note(err)
		return line
	}
	defer f.Close()
	if _, err := f.WriteString(line + "\n"); err != nil {
		note(err)
	}
	return line
}

func note(err error) {
	fmt.Fprintf(os.Stderr, "snip: telemetry write failed (picking continues): %v\n", err)
}

// --- the snip fire row -------------------------------------------------------

// MatcherVersion is stamped on every row (payload.matcher).
const MatcherVersion = "token-v1"

// Payload is the JSON `payload` blob of a snip fire row — the telemetry
// vocabulary. Field order here is the JSON key order
// the row carries.
type Payload struct {
	// method: "search" when a non-empty query was typed, "direct" when the
	// row was picked without typing. Deliberately re-uses the direct/search
	// split's MEANING (query-driven vs not) so every existing query that
	// splits on it keeps working.
	Method string `json:"method"`
	// inferred is always false: snip's rows are self-reported, not inferred;
	// the flag stays so consumers can distinguish them from legacy inferred
	// rows in the union.
	Inferred bool `json:"inferred"`
	// Query is the exact typed string (may be ""); SearchTerm is the legacy
	// key name kept as an alias so --terms/--replay code reads one or the
	// other without branching.
	Query      string `json:"query"`
	SearchTerm string `json:"search_term"`
	// outcome: select | cancel | abort (origin window gone).
	Outcome string `json:"outcome"`
	Label   string `json:"label"`
	// Workspace and App describe the ORIGIN window (captured by the launcher
	// wrapper), the detector's origin-context discipline applied at the
	// source instead of reconstructed. Workspace is the focused i3 workspace
	// NUMBER (0 = unknown — i3 numbers from 1, so 0 is never a real one).
	App       string `json:"app"`
	Workspace int    `json:"workspace,omitempty"`
	// LatencyOpenMS / LatencyPasteMS: the §3 acceptance criteria are queried
	// from real rows, not hand-timed. Paste is omitted on cancel rows (no
	// paste was attempted).
	LatencyOpenMS  int64 `json:"latency_open_ms,omitempty"`
	LatencyPasteMS int64 `json:"latency_paste_ms,omitempty"`
	// Tool / Matcher: belt-and-braces provenance; cheap now, priceless if a
	// v2 matcher ever lands.
	Tool    string `json:"tool"`
	Matcher string `json:"matcher"`
}

// LabelText returns the row's `text` value: the selected label, or "" for a
// cancelled open. 🔴 THE ROW'S `text` CARRIES THE LABEL: the legacy
// convention is that `text` names the row's identity, and the identity is the
// label, so text and the payload's
// `label` are the same string (a cancelled open fired nothing, hence "").
func (p Payload) LabelText() string {
	if p.Outcome == "cancel" {
		return ""
	}
	return p.Label
}

// FireFields builds the ordered v1 fields for one snip fire row. The top-level
// shape is the legacy row shape (source/kind/text/app/project/session/payload —
// the top-level `app` is what downstream analytics key on), while the payload
// JSON carries the full field set, including `app` again so payload-side
// consumers see the origin class without reaching for the top-level column.
func FireFields(p Payload) []Field {
	p.Tool = "snip"
	p.Matcher = MatcherVersion
	p.Inferred = false
	blob, err := json.Marshal(p)
	if err != nil {
		// Payload marshal of plain strings/ints cannot fail; degrade to an
		// empty object rather than dropping the row's other fields.
		blob = []byte("{}")
	}
	return []Field{
		{Key: "source", Value: "snip"},
		{Key: "kind", Value: "fire"},
		{Key: "text", Value: p.LabelText()},
		{Key: "app", Value: p.App},
		{Key: "project", Value: ""},
		{Key: "session", Value: ""},
		{Key: "payload", Value: string(blob)},
	}
}

// EmitFire builds and appends one snip fire row. Best-effort, like Emit.
func EmitFire(p Payload, spoolDir string) string {
	return Emit(FireFields(p), spoolDir)
}
