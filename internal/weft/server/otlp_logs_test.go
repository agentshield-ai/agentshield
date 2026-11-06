package server

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// Real Claude Code 2.1.318 exports (otlp/testdata/claude_code): a Read
// run as OTLP JSON, and a Bash + sub-agent run as protobuf.
const (
	ccReadSession = "74deee12-92fa-4127-9157-1af0dced000d"
	ccBashSession = "0c36c29e-0bb8-4e24-b31e-a4d8110ad3a8"
	ccBashToolUse = "toolu_0172cnpPm5jcXnzXfwJ2vEjT"
	ccReadToolUse = "toolu_01DXUQ8ESEs5J12ZyQWjYQv6"
)

func ccFixtureFiles(t *testing.T, dir, glob string) [][]byte {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join("..", "otlp", "testdata", "claude_code", dir, glob))
	sort.Strings(files)
	if len(files) == 0 {
		t.Fatalf("no fixtures %s/%s", dir, glob)
	}
	var out [][]byte
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, b)
	}
	return out
}

// otlpPost sends an OTLP request with Claude Code's headers.
func (e *testEnv) otlpPost(path string, body []byte, ctype string, headers map[string]string) (int, []byte, string) {
	e.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, e.srv.URL+path, bytes.NewReader(body))
	req.Header.Set("Content-Type", ctype)
	if e.key != "" {
		req.Header.Set("Authorization", "Bearer "+e.key)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out, resp.Header.Get("Content-Type")
}

// sendClaudeCode posts both real runs: the Read run as JSON, the Bash run
// as protobuf (gzipped, as the exporter may send it).
func sendClaudeCode(t *testing.T, e *testEnv, project string) {
	t.Helper()
	for _, b := range ccFixtureFiles(t, "read_json", "*_logs.json") {
		if code, out, ct := e.otlpPost("/otel/v1/logs", b, "application/json", map[string]string{"project_id": project}); code != 200 ||
			ct != "application/json" || strings.TrimSpace(string(out)) != "{}" {
			t.Fatalf("logs (json) -> %d %s %s", code, ct, out)
		}
	}
	for _, b := range ccFixtureFiles(t, "bash_subagent_proto", "*_logs.pb") {
		var gz bytes.Buffer
		w := gzip.NewWriter(&gz)
		_, _ = w.Write(b)
		_ = w.Close()
		if code, out, ct := e.otlpPost("/otel/v1/logs", gz.Bytes(), "application/x-protobuf",
			map[string]string{"project_id": project, "Content-Encoding": "gzip"}); code != 200 || ct != "application/x-protobuf" || len(out) != 0 {
			t.Fatalf("logs (protobuf) -> %d %s %q", code, ct, out)
		}
	}
}

func (e *testEnv) session(project, id string) map[string]any {
	e.t.Helper()
	return e.getJSON("/api/v1/sessions/" + id + "?project_id=" + project)
}

func TestOTLPLogsClaudeCodeSessions(t *testing.T) {
	e := newEnv(t)
	sendClaudeCode(t, e, pid)
	for _, tc := range []struct {
		session            string
		tools, llms        float64
		input, output      float64
		cost               float64
		tool               string
		spans, agentsCount int
	}{
		{ccReadSession, 1, 2, 47335 + 48346, 114, 0.176356 + 0.0176426, "Read", 4, 1},
		{ccBashSession, 3, 5, 221115, 468, 0.313334, "Bash", 10, 2},
	} {
		s := e.session(pid, tc.session)
		if fnum(s["turn_count"]) != 1 || fnum(s["llm_call_count"]) != tc.llms || fnum(s["tool_call_count"]) != tc.tools ||
			fnum(s["input_tokens"]) != tc.input || fnum(s["output_tokens"]) != tc.output || s["status"] != "ok" {
			t.Fatalf("session %s = %v", tc.session, s)
		}
		if math.Abs(fnum(s["cost_usd"])-tc.cost) > 1e-9 {
			t.Fatalf("session %s cost = %v, want the reported %v", tc.session, s["cost_usd"], tc.cost)
		}
		if agents, _ := s["agents"].([]any); len(agents) != tc.agentsCount || agents[0] != "claude-code" && agents[len(agents)-1] != "claude-code" {
			t.Fatalf("session %s agents = %v", tc.session, s["agents"])
		}
		if spans := e.spans(map[string]any{"filter": map[string]any{"session_ids": []any{tc.session}}}); len(spans) != tc.spans {
			t.Fatalf("session %s spans = %d, want %d", tc.session, len(spans), tc.spans)
		}
	}
	// Sending everything again changes nothing.
	sendClaudeCode(t, e, pid)
	if s := e.session(pid, ccBashSession); fnum(s["input_tokens"]) != 221115 || fnum(s["llm_call_count"]) != 5 {
		t.Fatalf("after a re-send: %v", s)
	}
	// The sub-agent is an agent of the session, delegated to from claude-code.
	if s := e.session(pid, ccBashSession); fnum(s["handoff_count"]) < 1 {
		t.Fatalf("no hand-off to the sub-agent: %v", s)
	}
}

func TestOTLPLogsAuthAndProject(t *testing.T) {
	e := newEnv(t, "root")
	logs := ccFixtureFiles(t, "read_json", "*_logs.json")
	e.key = ""
	if code, _, _ := e.otlpPost("/otel/v1/logs", logs[2], "application/json", map[string]string{"project_id": pid}); code != 401 {
		t.Fatalf("no key -> %d", code)
	}
	e.key = "root"
	scoped, _ := createKey(e, "root", map[string]any{"name": "cc", "projects": []any{pid}})
	ro, _ := createKey(e, "root", map[string]any{"name": "ro", "read_only": true})
	for _, c := range []struct {
		name, key, header string
		want              int
	}{
		{"scoped key without the project header", scoped, "", 403},
		{"scoped key, another project", scoped, "acme/other", 403},
		{"read-only key", ro, pid, 403},
		{"scoped key with the header", scoped, pid, 200},
	} {
		e.key = c.key
		h := map[string]string{}
		if c.header != "" {
			h["project_id"] = c.header
		}
		for _, path := range []string{"/otel/v1/logs", "/otel/v1/metrics"} {
			if code, out, _ := e.otlpPost(path, logs[2], "application/json", h); code != c.want {
				t.Fatalf("%s %s -> %d %s, want %d", c.name, path, code, out, c.want)
			}
		}
	}
	// X-Weft-Project works like project_id; without a header the resource
	// attribute weft.project picks the project, else default/default.
	e.key = "root"
	for _, b := range logs {
		e.otlpPost("/otel/v1/logs", b, "application/json", map[string]string{"X-Weft-Project": "acme/hdr"})
		e.otlpPost("/otel/v1/logs", bytes.Replace(b, []byte(`"key": "service.name"`), []byte(`"key": "weft.project", "value": {"stringValue": "acme/res"}}, {"key": "service.name"`), 1),
			"application/json", nil)
		e.otlpPost("/otel/v1/logs", b, "application/json", nil)
	}
	for _, p := range []string{"acme/hdr", "acme/res", "default/default"} {
		if s := e.session(p, ccReadSession); fnum(s["turn_count"]) != 1 {
			t.Fatalf("project %s: %v", p, s)
		}
	}
}

func TestOTLPMetricsAccepted(t *testing.T) {
	e := newEnv(t)
	for _, b := range ccFixtureFiles(t, "read_json", "*_metrics.json") {
		if code, out, ct := e.otlpPost("/otel/v1/metrics", b, "application/json", map[string]string{"project_id": pid}); code != 200 ||
			ct != "application/json" || strings.TrimSpace(string(out)) != "{}" {
			t.Fatalf("metrics -> %d %s %s", code, ct, out)
		}
	}
	if code, _, ct := e.otlpPost("/otel/v1/metrics", []byte{}, "application/x-protobuf", nil); code != 200 || ct != "application/x-protobuf" {
		t.Fatalf("protobuf metrics -> %d %s", code, ct)
	}
	if code, _ := e.raw("/otel/v1/logs", nil, nil); code != 405 {
		t.Fatalf("GET /otel/v1/logs -> %d", code)
	}
	if code, _, _ := e.otlpPost("/otel/v1/logs", []byte("not otlp"), "application/x-protobuf", nil); code != 400 {
		t.Fatalf("garbage logs -> %d", code)
	}
	if spans := e.spans(map[string]any{}); len(spans) != 0 {
		t.Fatalf("metrics stored spans: %d", len(spans))
	}
}

// pluginItems is the weft-claude-code plugin's turn for the Bash session:
// the turn, the Bash call and one LLM call (lib/claude.mjs shapes).
func pluginItems(session string) []any {
	at := time.Date(2026, 11, 6, 8, 5, 8, 0, time.UTC)
	ts := func(d time.Duration) string { return at.Add(d).Format(time.RFC3339Nano) }
	attrs := func(kind string, extra map[string]any) map[string]any {
		m := map[string]any{"weft": map[string]any{"span_kind": kind, "source": "weft-claude-code", "plugin_version": "0.1.0"}, "gen_ai.conversation.id": session}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	span := func(id, parent, name, display string, a map[string]any, summary map[string]any) any {
		s := map[string]any{"project_id": pid, "id": id, "name": name, "display_name": display, "trace_id": "plugin-turn",
			"started_at": ts(0), "ended_at": ts(10 * time.Second), "attributes": a, "inputs": map[string]any{}, "summary": summary,
			"session_id": session, "turn_id": "plugin-turn"}
		if parent != "" {
			s["parent_id"] = parent
		}
		return map[string]any{"span": s}
	}
	return []any{
		span("plugin-turn", "", "claude_code.turn", "invoke_agent claude-code", attrs("agent", map[string]any{"gen_ai.operation.name": "invoke_agent", "gen_ai.agent.name": "claude-code"}), nil),
		span("plugin-bash", "plugin-turn", "claude_code.tool.Bash", "Bash: echo hi", attrs("tool", map[string]any{"gen_ai.operation.name": "execute_tool",
			"gen_ai.tool.name": "Bash", "gen_ai.tool.call.id": ccBashToolUse}), nil),
		span("plugin-llm", "plugin-turn", "claude_code.llm", "chat", attrs("llm", map[string]any{"gen_ai.operation.name": "chat", "gen_ai.request.model": "claude-opus-5-5"}),
			map[string]any{"usage": map[string]any{"claude-opus-5-5": map[string]any{"requests": 1, "prompt_tokens": 1000, "completion_tokens": 7}}}),
	}
}

func spanIDs(spans []any) map[string]bool {
	out := map[string]bool{}
	for _, s := range spans {
		out[s.(map[string]any)["id"].(string)] = true
	}
	return out
}

func TestOTLPLogsDedupAndDetections(t *testing.T) {
	e := newEnv(t)
	sendClaudeCode(t, e, pid)

	// An AgentShield verdict on the Bash call attaches by tool-use id.
	if code, out := e.raw("/api/v1/shield/ingest", map[string]any{"project_id": pid, "detections": []any{map[string]any{
		"event_id": "evt-bash", "session_id": ccBashSession, "tool_call_id": ccBashToolUse, "tool_name": "Bash", "action": "block", "mode": "enforce",
		"severity": "high", "timestamp": "2026-11-06T08:05:11.220Z", "rules": []any{map[string]any{"id": "r1", "title": "Echo", "level": "high"}}}}}, nil); code != 200 {
		t.Fatalf("shield ingest -> %d %s", code, out)
	}
	detSpan := func() (string, string) {
		t.Helper()
		d := e.getJSON("/api/v1/detections?project_id=" + pid + "&session_id=" + ccBashSession)
		items := d["items"].([]any)
		if len(items) != 1 {
			t.Fatalf("detections = %v", d)
		}
		it := items[0].(map[string]any)
		sid, _ := it["span_id"].(string)
		return sid, it["link"].(string)
	}
	bashSpan := ""
	for _, s := range e.spans(map[string]any{"filter": map[string]any{"session_ids": []any{ccBashSession}}}) {
		m := s.(map[string]any)
		if a, _ := m["attributes"].(map[string]any); a["gen_ai.tool.call.id"] == ccBashToolUse {
			bashSpan = m["id"].(string)
		}
	}
	if sid, link := detSpan(); bashSpan == "" || sid != bashSpan || link != "tool_call_id" {
		t.Fatalf("verdict on %q (%s), want the log events' Bash span %q", sid, link, bashSpan)
	}

	// The plugin sends the same session: its spans replace the log events'.
	if code, out := e.raw("/api/v1/spans/batch", map[string]any{"project_id": pid, "items": pluginItems(ccBashSession)}, map[string]string{"X-Weft-Source": "weft-claude-code"}); code != 200 {
		t.Fatalf("plugin batch -> %d %s", code, out)
	}
	only := func(session string, want ...string) {
		t.Helper()
		got := spanIDs(e.spans(map[string]any{"filter": map[string]any{"session_ids": []any{session}}}))
		if len(got) != len(want) {
			t.Fatalf("session %s spans = %v, want %v", session, got, want)
		}
		for _, w := range want {
			if !got[w] {
				t.Fatalf("session %s spans = %v, want %v", session, got, want)
			}
		}
	}
	only(ccBashSession, "plugin-turn", "plugin-bash", "plugin-llm")
	if s := e.session(pid, ccBashSession); fnum(s["input_tokens"]) != 1000 || fnum(s["llm_call_count"]) != 1 || fnum(s["turn_count"]) != 1 {
		t.Fatalf("session counted from both sources: %v", s)
	}
	if sid, link := detSpan(); sid != "plugin-bash" || link != "tool_call_id" {
		t.Fatalf("verdict on %q (%s) after the plugin, want plugin-bash", sid, link)
	}
	// Log events arriving later are not shown either.
	sendClaudeCode(t, e, pid)
	only(ccBashSession, "plugin-turn", "plugin-bash", "plugin-llm")
	// The other session (logs only) is untouched.
	if s := e.session(pid, ccReadSession); fnum(s["llm_call_count"]) != 2 {
		t.Fatalf("read session: %v", s)
	}

	// Traces (beta) for the Read session replace its log events too.
	body := ccTracesReadBody(ccReadSession)
	if code, out := e.raw("/otel/v1/traces", body, map[string]string{"project_id": pid}); code != 200 {
		t.Fatalf("traces -> %d %s", code, out)
	}
	only(ccReadSession, "00000000000000a1", "00000000000000a2", "00000000000000a3")
	sendClaudeCode(t, e, pid)
	only(ccReadSession, "00000000000000a1", "00000000000000a2", "00000000000000a3")
	if s := e.session(pid, ccReadSession); fnum(s["llm_call_count"]) != 1 || fnum(s["tool_call_count"]) != 1 || fnum(s["turn_count"]) != 1 {
		t.Fatalf("read session after traces: %v", s)
	}
}

// ccTracesReadBody is the Read run as Claude Code's traces beta would
// export it (documented span and attribute names).
func ccTracesReadBody(session string) []byte {
	kv := func(k string, v any) map[string]any {
		switch x := v.(type) {
		case int:
			return map[string]any{"key": k, "value": map[string]any{"intValue": x}}
		case bool:
			return map[string]any{"key": k, "value": map[string]any{"boolValue": x}}
		}
		return map[string]any{"key": k, "value": map[string]any{"stringValue": v}}
	}
	span := func(id, parent, name string, start, end int64, attrs ...map[string]any) map[string]any {
		a := []any{kv("session.id", session), kv("span.type", name)}
		for _, x := range attrs {
			a = append(a, x)
		}
		s := map[string]any{"traceId": "226cdcf22b4ded3e712958c7de4761fa", "spanId": id, "name": name,
			"startTimeUnixNano": start * 1e6, "endTimeUnixNano": end * 1e6, "attributes": a}
		if parent != "" {
			s["parentSpanId"] = parent
		}
		return s
	}
	t0 := int64(1793952238348)
	b, _ := json.Marshal(map[string]any{"resourceSpans": []any{map[string]any{
		"resource": map[string]any{"attributes": []any{kv("service.name", "claude-code")}},
		"scopeSpans": []any{map[string]any{"spans": []any{
			span("00000000000000a2", "00000000000000a1", "claude_code.llm_request", t0+100, t0+3000, kv("model", "claude-opus-5-5"),
				kv("input_tokens", 2), kv("output_tokens", 110), kv("cache_read_tokens", 26220), kv("cache_creation_tokens", 21113), kv("success", true)),
			span("00000000000000a3", "00000000000000a1", "claude_code.tool", t0+3000, t0+3100, kv("tool_name", "Read"),
				kv("tool_use_id", ccReadToolUse), kv("gen_ai.tool.call.id", ccReadToolUse)),
			span("00000000000000a1", "", "claude_code.interaction", t0, t0+4900, kv("user_prompt", "<REDACTED>"), kv("interaction.sequence", 1)),
		}}}}}})
	return b
}

func fnum(v any) float64 { f, _ := v.(float64); return f }
