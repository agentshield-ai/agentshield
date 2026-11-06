package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// The logs never carry keys, provider secrets, prompt text, tool arguments
// or outputs, or request bodies: representative requests run with every
// line captured at debug, and none of their sensitive strings may appear.
func TestLogsCarryNoSecretsOrContent(t *testing.T) {
	const (
		apiKey      = "wk-STATIC-KEY-CANARY-1f2e3d"
		providerKey = "sk-PROVIDER-KEY-CANARY-9a8b7c"
		prompt      = "PROMPT-CANARY please summarise my diary"
		toolArg     = "/home/me/TOOLARG-CANARY.txt"
		toolOut     = "TOOLOUT-CANARY contents of the file"
		cookie      = "COOKIE-CANARY-55aa"
		bodyCanary  = "BODY-CANARY-77"
	)
	e, buf, _ := newLogEnv(t, "admin="+apiKey)

	// Claude Code's real export (it holds a prompt and a tool input) ...
	sendClaudeCode(t, e, pid)
	// ... and records carrying our canaries in the prompt, the tool input
	// and output, and an attribute value.
	kv := func(k, v string) map[string]any {
		return map[string]any{"key": k, "value": map[string]any{"stringValue": v}}
	}
	rec := func(name string, attrs ...map[string]any) map[string]any {
		a := []any{kv("event.name", name), kv("session.id", "sess-canary"), kv("prompt.id", "prompt-canary"),
			kv("event.timestamp", "2026-11-06T08:00:00Z")}
		for _, x := range attrs {
			a = append(a, x)
		}
		return map[string]any{"timeUnixNano": "1793952000000000000", "attributes": a}
	}
	logs := map[string]any{"resourceLogs": []any{map[string]any{
		"resource": map[string]any{"attributes": []any{kv("service.name", "claude-code"), kv("user.email", "CANARY-EMAIL@example.com")}},
		"scopeLogs": []any{map[string]any{"scope": map[string]any{"name": "com.anthropic.claude_code.events"}, "logRecords": []any{
			rec("user_prompt", kv("prompt", prompt)),
			rec("tool_result", kv("tool_name", "Read"), kv("tool_use_id", "toolu_canary"), kv("success", "true"),
				kv("tool_input", `{"file_path":"`+toolArg+`"}`), kv("tool_parameters", `{"output":"`+toolOut+`"}`)),
			rec("mcp_server_connection", kv("server", "CANARY-MCP-ATTR")),
		}}},
	}}}
	b, _ := json.Marshal(logs)
	if code, out, _ := e.otlpPost("/otel/v1/logs", b, "application/json", map[string]string{"project_id": pid, "Cookie": "weft_session=" + cookie}); code != 200 {
		t.Fatalf("logs -> %d %s", code, out)
	}
	// A provider secret saved through the API, and a verdict.
	e.post("/secrets/set", map[string]any{"name": "OPENAI_API_KEY", "value": providerKey})
	e.post("/api/v1/shield/ingest", map[string]any{"project_id": pid, "detections": []any{map[string]any{"event_id": "evt-c",
		"session_id": "sess-canary", "tool_call_id": "toolu_canary", "tool_name": "Read", "action": "block", "severity": "high"}}})
	// A refused request echoing a body value, and a wrong key.
	if code, _, _ := e.otlpPost("/otel/v1/traces", []byte(`{"resourceSpans":"`+bodyCanary+`"}`), "application/json", nil); code != 400 {
		t.Fatalf("bad traces -> %d", code)
	}
	if code, _ := e.raw("/api/v1/objects/save", map[string]any{"project_id": pid, "name": "o", "value": bodyCanary},
		map[string]string{"Authorization": "Bearer wrong-" + apiKey}); code != http.StatusUnauthorized {
		t.Fatalf("wrong key -> %d", code)
	}
	if code, _ := e.raw("/api/v1/objects/save", map[string]any{"project_id": "a/b/" + bodyCanary, "name": "o", "value": 1}, nil); code != 400 {
		t.Fatalf("bad project -> %d", code)
	}

	// The refusal is logged with its reason, the echoed value elided.
	if l := buf.find(t, "request", map[string]any{"route": "/api/v1/objects/save", "status": 400}); len(l) != 1 ||
		!strings.Contains(l[0]["reason"].(string), "project_id") {
		t.Fatalf("refusal line = %v", l)
	}
	out := buf.String()
	if !strings.Contains(out, "otlp logs ingested") || !strings.Contains(out, "sess-canary") || !strings.Contains(out, "shield verdicts ingested") {
		t.Fatalf("expected lines missing:\n%s", out)
	}
	for _, s := range []string{apiKey, providerKey, "PROMPT-CANARY", "TOOLARG-CANARY", "TOOLOUT-CANARY", cookie, bodyCanary,
		"CANARY-EMAIL", "CANARY-MCP-ATTR", "Read note.txt", "/home/dev/project/note.txt", "Bearer"} {
		if strings.Contains(out, s) {
			t.Errorf("the log holds %q", s)
		}
	}
	if t.Failed() {
		t.Log(out)
	}
}
