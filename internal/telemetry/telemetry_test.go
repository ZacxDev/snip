package telemetry

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var fixedTime = time.Date(2026, 10, 29, 14, 30, 5, 250_000_000, time.UTC)

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

// The v1 line contract, byte for byte (spool_emit.py): plain scalar keys
// verbatim, every other key base64'd under a b64: prefix, ts/host appended
// when the caller omits them.
func TestBuildLineContract(t *testing.T) {
	line := BuildLine([]Field{
		{Key: "source", Value: "snip"},
		{Key: "kind", Value: "fire"},
		{Key: "text", Value: ":eos"},
		{Key: "payload", Value: `{"method":"search"}`},
	}, fixedTime, "host-a")
	want := strings.Join([]string{
		"v1",
		"source=snip",
		"kind=fire",
		"b64:text=" + b64(":eos"),
		"b64:payload=" + b64(`{"method":"search"}`),
		"ts=2026-10-29 14:30:05.250",
		"host=host-a",
	}, "\t")
	if line != want {
		t.Fatalf("BuildLine:\n got %q\nwant %q", line, want)
	}
}

// Explicit ts/host are passed through, not duplicated.
func TestBuildLineExplicitTsHost(t *testing.T) {
	line := BuildLine([]Field{
		{Key: "host", Value: "laptop"}, {Key: "ts", Value: "2026-01-01 00:00:00.000"}, {Key: "kind", Value: "fire"},
	}, fixedTime, "host-a")
	want := "v1\thost=laptop\tts=2026-01-01 00:00:00.000\tkind=fire"
	if line != want {
		t.Fatalf("BuildLine:\n got %q\nwant %q", line, want)
	}
}

// HOST env fallback when the caller passes no host (spool_emit's chain:
// env HOST, else os.Hostname).
func TestBuildLineHostFallback(t *testing.T) {
	t.Setenv("HOST", "from-env")
	line := BuildLine([]Field{{Key: "kind", Value: "fire"}}, fixedTime, "")
	if !strings.HasSuffix(line, "\thost=from-env") {
		t.Fatalf("HOST env fallback missing: %q", line)
	}
	os.Unsetenv("HOST")
	line = BuildLine([]Field{{Key: "kind", Value: "fire"}}, fixedTime, "")
	if !strings.Contains(line, "\thost=") || strings.HasSuffix(line, "\thost=") {
		t.Fatalf("hostname fallback missing: %q", line)
	}
}

// 🔴 THE FIRE ROW SHAPE — the legacy top-level row shape, every field
// enumerated. text is the LABEL ("" on cancel); the
// payload carries method/query/search_term/outcome/inferred/label/workspace/
// app/latencies/tool/matcher.
func TestFireRowShape(t *testing.T) {
	fields := FireFields(Payload{
		Method: "search", Query: "ssh des", SearchTerm: "ssh des",
		Outcome: "select", Label: "SSH desktop (LAN)", App: "Alacritty",
		Workspace: 2, LatencyOpenMS: 120, LatencyPasteMS: 85,
	})
	// Top-level shape mirrors the legacy row exactly.
	type kv struct {
		k, v string
	}
	var got []kv
	for _, f := range fields {
		got = append(got, kv{f.Key, f.Value})
	}
	wantKeys := []string{"source", "kind", "text", "app", "project", "session", "payload"}
	if len(got) != len(wantKeys) {
		t.Fatalf("field count %d, want %d (%v)", len(got), len(wantKeys), got)
	}
	for i, k := range wantKeys {
		if got[i].k != k {
			t.Fatalf("field %d = %q, want %q", i, got[i].k, k)
		}
	}
	if got[0].v != "snip" || got[1].v != "fire" {
		t.Fatalf("source/kind = %q/%q, want snip/fire", got[0].v, got[1].v)
	}
	if got[2].v != "SSH desktop (LAN)" {
		t.Fatalf("text = %q, want the label", got[2].v)
	}
	if got[3].v != "Alacritty" {
		t.Fatalf("top-level app = %q, want the origin class (the ClickHouse column)", got[3].v)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(got[6].v), &payload); err != nil {
		t.Fatalf("payload not JSON: %v", err)
	}
	for k, want := range map[string]any{
		"method": "search", "inferred": false, "query": "ssh des", "search_term": "ssh des",
		"outcome": "select", "label": "SSH desktop (LAN)", "app": "Alacritty",
		"workspace": float64(2), "latency_open_ms": float64(120), "latency_paste_ms": float64(85),
		"tool": "snip", "matcher": "token-v1",
	} {
		if payload[k] != want {
			t.Errorf("payload[%q] = %v, want %v", k, payload[k], want)
		}
	}
	// The identity must NOT ride inside the payload as a separate key — the
	// row's `text` (and the payload's `label`) carry the label.
	if _, ok := payload["trigger"]; ok {
		t.Error("payload must not carry a trigger — the label is the identity now")
	}
}

// A cancel row: text is "" (nothing fired), no paste latency, open latency
// present (the open happened).
func TestCancelRowTextEmpty(t *testing.T) {
	fields := FireFields(Payload{Method: "direct", Query: "", Outcome: "cancel", Label: "End-of-session ritual", LatencyOpenMS: 42})
	if fields[2].Value != "" {
		t.Fatalf("cancel row text = %q, want \"\"", fields[2].Value)
	}
	var payload map[string]any
	json.Unmarshal([]byte(fields[6].Value), &payload)
	if payload["outcome"] != "cancel" {
		t.Fatalf("outcome = %v, want cancel", payload["outcome"])
	}
	if _, ok := payload["latency_paste_ms"]; ok {
		t.Error("cancel rows must not carry a paste latency")
	}
	if payload["latency_open_ms"] != float64(42) {
		t.Error("cancel rows must carry the open latency (the open happened)")
	}
}

// Emit writes to <dir>/current.log, newline-terminated, and degrades to a
// stderr note on failure — telemetry must never break its caller.
func TestEmitWritesAndDegrades(t *testing.T) {
	dir := t.TempDir()
	line := Emit([]Field{{Key: "kind", Value: "fire"}}, dir)
	raw, err := os.ReadFile(filepath.Join(dir, "current.log"))
	if err != nil {
		t.Fatalf("spool file not written: %v", err)
	}
	if string(raw) != line+"\n" {
		t.Fatalf("spool content %q, want %q", raw, line+"\n")
	}
	// Unwritable dir: Emit still returns the line, no panic.
	if got := Emit([]Field{{Key: "kind", Value: "fire"}}, "/proc/nonexistent-snip-spool"); got == "" {
		t.Fatal("Emit must return the line even when the write fails")
	}
}

// The spool dir resolution chain: $ACTIVITY_SPOOL_DIR, else
// ($XDG_STATE_HOME || ~/.local/state)/activity/spool.
func TestSpoolDirResolution(t *testing.T) {
	t.Setenv("ACTIVITY_SPOOL_DIR", "/spool/override")
	if d := SpoolDir(); d != "/spool/override" {
		t.Fatalf("ACTIVITY_SPOOL_DIR ignored: %s", d)
	}
	os.Unsetenv("ACTIVITY_SPOOL_DIR")
	t.Setenv("XDG_STATE_HOME", "/state")
	if d := SpoolDir(); d != "/state/activity/spool" {
		t.Fatalf("XDG_STATE_HOME resolution wrong: %s", d)
	}
}
