package server

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Following a Claude Code session from the logs: each batch at info with
// its counts, the sessions and rebuilt turns at debug, how each verdict
// attached, and the switch to a higher-ranked source.
func TestIngestLogFollowsAClaudeCodeSession(t *testing.T) {
	e, buf, _ := newLogEnv(t)
	sendClaudeCode(t, e, pid)

	batches := buf.find(t, "otlp logs ingested", map[string]any{"route": "/otel/v1/logs", "project": pid})
	if len(batches) == 0 {
		t.Fatalf("no ingest lines:\n%s", buf)
	}
	newEvents, ignored, rebuilt := 0.0, 0.0, 0.0
	for _, b := range batches {
		if b["level"] != "INFO" || !strings.HasPrefix(b["harness"].(string), "claude-code=") {
			t.Fatalf("batch line = %v", b)
		}
		newEvents += b["events_new"].(float64)
		ignored += b["ignored"].(float64)
		rebuilt += b["turns_rebuilt"].(float64)
	}
	if newEvents == 0 || ignored == 0 || rebuilt == 0 {
		t.Fatalf("events_new=%v ignored=%v turns_rebuilt=%v", newEvents, ignored, rebuilt)
	}
	if !strings.Contains(buf.String(), `"ignored_by":"noise_event=`) {
		t.Fatalf("no ignored breakdown:\n%s", buf)
	}
	// The batch's request line moves to debug: the ingest line says it all.
	for _, l := range buf.find(t, "request", map[string]any{"route": "/otel/v1/logs", "status": 200}) {
		if l["level"] != "DEBUG" {
			t.Fatalf("ingest request line at %v", l["level"])
		}
	}
	if len(buf.find(t, "otlp logs sessions", nil)) == 0 || !strings.Contains(buf.String(), ccBashSession) {
		t.Fatalf("no session list at debug:\n%s", buf)
	}
	if len(buf.find(t, "turn rebuilt", map[string]any{"session_id": ccBashSession, "source": "claude-code", "level": "DEBUG"})) == 0 {
		t.Fatalf("no turn rebuilt line:\n%s", buf)
	}
	// Every line of a request carries its request id.
	for _, l := range buf.find(t, "turn rebuilt", nil) {
		if l["req_id"] == nil {
			t.Fatalf("turn line without req_id: %v", l)
		}
	}

	// Verdicts: by tool call id, by time, and one that cannot attach.
	readStart := ""
	for _, s := range e.spans(map[string]any{"filter": map[string]any{"session_ids": []any{ccReadSession}}}) {
		m := s.(map[string]any)
		if a, _ := m["attributes"].(map[string]any); a["gen_ai.tool.call.id"] == ccReadToolUse {
			readStart, _ = m["started_at"].(string)
		}
	}
	if readStart == "" {
		t.Fatal("no Read span")
	}
	at, _ := time.Parse(time.RFC3339Nano, readStart)
	dets := []any{
		map[string]any{"event_id": "evt-bash", "session_id": ccBashSession, "tool_call_id": ccBashToolUse, "tool_name": "Bash", "action": "block",
			"mode": "enforce", "severity": "high", "timestamp": "2026-11-06T08:05:11.220Z", "rules": []any{map[string]any{"id": "r1", "level": "high"}}},
		map[string]any{"event_id": "evt-read", "session_id": ccReadSession, "tool_name": "Read", "action": "log", "mode": "audit",
			"severity": "low", "timestamp": at.Add(time.Second).Format(time.RFC3339Nano)},
		map[string]any{"event_id": "evt-write", "session_id": ccReadSession, "tool_name": "Write", "action": "log", "mode": "audit",
			"severity": "low", "timestamp": at.Format(time.RFC3339Nano)},
		map[string]any{"event_id": "evt-bad", "action": "deny"},
	}
	if code, out := e.raw("/api/v1/shield/ingest", map[string]any{"project_id": pid, "detections": dets}, nil); code != 200 {
		t.Fatalf("shield ingest -> %d %s", code, out)
	}
	l := buf.find(t, "shield verdicts ingested", map[string]any{"project": pid, "received": 4, "accepted": 3, "created": 3, "rejected": 1})
	if len(l) != 1 || l[0]["level"] != "INFO" || l[0]["attached"] != "nearest_time=1,tool_call_id=1,unattached=1" {
		t.Fatalf("verdict batch line = %v\n%s", l, buf)
	}
	if len(buf.find(t, "verdict attached", map[string]any{"link": "tool_call_id", "session_id": ccBashSession, "trigger": "verdict"})) != 1 {
		t.Fatalf("no tool_call_id attach line:\n%s", buf)
	}
	if len(buf.find(t, "verdict attached", map[string]any{"link": "nearest_time", "tool": "Read", "span_tool": "Read"})) != 1 {
		t.Fatalf("no nearest_time attach line:\n%s", buf)
	}
	if nl := buf.find(t, "verdict not attached", map[string]any{"tool": "Write", "session_id": ccReadSession}); len(nl) != 1 || nl[0]["reason"] == "" {
		t.Fatalf("no unattached line:\n%s", buf)
	}
	if len(buf.find(t, "shield record rejected", map[string]any{"kind": "detection", "index": 3})) != 1 {
		t.Fatalf("no rejected record line:\n%s", buf)
	}

	// The plugin sends the Bash session: its spans replace the log events'.
	if code, out := e.raw("/api/v1/spans/batch", map[string]any{"project_id": pid, "items": pluginItems(ccBashSession)}, nil); code != 200 {
		t.Fatalf("plugin batch -> %d %s", code, out)
	}
	rep := buf.find(t, "session source replaced", map[string]any{"session_id": ccBashSession, "from": "claude-code-otel-logs",
		"to": "weft-claude-code", "verdicts_relinked": 1})
	if len(rep) != 1 || rep[0]["level"] != "INFO" || rep[0]["spans_hidden"].(float64) == 0 {
		t.Fatalf("replacement line = %v\n%s", rep, buf)
	}
	// Re-sent log events are duplicates; the session's events stay dropped.
	sendClaudeCode(t, e, pid)
	dup := 0.0
	for _, b := range buf.find(t, "otlp logs ingested", nil) {
		dup += b["events_duplicate"].(float64)
	}
	if dup == 0 {
		t.Fatalf("no duplicates counted:\n%s", buf)
	}
}

func TestIngestLogRejectedBatch(t *testing.T) {
	e, buf, _ := newLogEnv(t)
	if code, _, _ := e.otlpPost("/otel/v1/logs", []byte(`{"resourceLogs":"BODY-CANARY-31337"}`), "application/json", nil); code != 400 {
		t.Fatalf("bad batch -> %d", code)
	}
	l := buf.find(t, "otlp batch rejected", map[string]any{"route": "/otel/v1/logs", "stage": "decode", "content_type": "application/json"})
	if len(l) != 1 || l[0]["level"] != "WARN" || l[0]["bytes"].(float64) == 0 || l[0]["reason"] == "" {
		t.Fatalf("rejected line = %v\n%s", l, buf)
	}
	if strings.Contains(buf.String(), "BODY-CANARY") {
		t.Fatalf("the body reached the log:\n%s", buf)
	}
}

func TestIngestLogTraces(t *testing.T) {
	e, buf, _ := newLogEnv(t)
	kv := func(k, v string) map[string]any {
		return map[string]any{"key": k, "value": map[string]any{"stringValue": v}}
	}
	agent := map[string]any{
		"resource": map[string]any{"attributes": []any{kv("service.name", "my-agent")}},
		"scopeSpans": []any{map[string]any{"spans": []any{map[string]any{
			"traceId": "0102030405060708090a0b0c0d0e0f10", "spanId": "0102030405060708", "name": "chat gpt",
			"startTimeUnixNano": "1700000000000000000", "endTimeUnixNano": "1700000001000000000",
			"attributes": []any{kv("gen_ai.conversation.id", "conv-1")},
		}}}},
	}
	codex := map[string]any{
		"resource":   map[string]any{"attributes": []any{kv("service.name", "codex_exec")}},
		"scopeSpans": []any{map[string]any{"spans": []any{map[string]any{"traceId": "0102030405060708090a0b0c0d0e0f11", "spanId": "0102030405060709", "name": "x"}}}},
	}
	body := map[string]any{"resourceSpans": []any{agent, codex}}
	b, _ := json.Marshal(body)
	if code, out, _ := e.otlpPost("/otel/v1/traces", b, "application/json", map[string]string{"project_id": pid}); code != 200 {
		t.Fatalf("traces -> %d %s", code, out)
	}
	l := buf.find(t, "otlp traces ingested", map[string]any{"project": pid, "spans_received": 2, "spans_stored": 1, "ignored": 1,
		"ignored_by": "codex_internal_spans=1", "harness": "codex=1,otel=1", "sessions": 1})
	if len(l) != 1 || l[0]["level"] != "INFO" {
		t.Fatalf("traces line = %v\n%s", l, buf)
	}
	if s := buf.find(t, "otlp traces sessions", map[string]any{"session_ids": "conv-1"}); len(s) != 1 || s[0]["level"] != "DEBUG" {
		t.Fatalf("sessions line = %v\n%s", s, buf)
	}
}
