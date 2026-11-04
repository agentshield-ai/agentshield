package server

import (
	"net/url"
	"testing"
	"time"
)

// Spans sent with the attribute keys earlier SDK versions emitted
// (weft.op_name, weft.thread_id, weft.parent_call.*,
// weft.eval.predict_and_score_call_id, weft.linked_calls with call_id
// items; store/attrkeys.go) show up exactly like spans sent with the
// current keys: their session, their link to the calling span, their
// evaluation run and the spans a prediction links to. Queries by either
// name match both.
func TestDeprecatedAttributeKeys(t *testing.T) {
	e := newEnv(t)

	// Evaluation links: run membership through the old prediction key and
	// linked_calls items, as in TestAgentEvalsFilterAndList.
	seedAgentEvalsKeys(t, e, true)
	for _, c := range []struct {
		filter map[string]any
		want   float64
	}{
		{map[string]any{"evaluation_run_id": "run-1", "limit": 100}, 7},
		{map[string]any{"prediction_span_id": "pas-2"}, 4},
		{map[string]any{"evaluation_run_id": "run-2"}, 2},
	} {
		c.filter["project_id"] = pid
		if res := e.post("/agents/spans/query", c.filter); res["total_count"].(float64) != c.want {
			t.Fatalf("%v: %v spans %v, want %v", c.filter, res["total_count"], spanIDsOf(res), c.want)
		}
	}

	// OTLP: an older SDK's OTel exporter names the session, the span and
	// the calling span with the old keys. The sub-agent trace's root
	// failed: being linked to a caller in another trace, it is a failed step.
	base := time.Now().Add(-time.Minute)
	tr := hexID("oldkeys", 32)
	body := otlpBody(base, []testSpan{
		{trace: tr, id: hexID("ok1", 16), name: "invoke_agent helper", durMS: 30, err: "boom", attrs: map[string]any{
			"gen_ai.operation.name": "invoke_agent", "gen_ai.agent.name": "helper", "weft.is_turn": true,
			"weft.thread_id": "old-session", "weft.op_name": "helper.run",
			"weft.parent_call.id": "caller-1", "weft.parent_call.trace_id": "caller-trace"}},
		{trace: tr, id: hexID("ok2", 16), parent: hexID("ok1", 16), name: "chat m", startMS: 5, durMS: 10, attrs: map[string]any{
			"gen_ai.operation.name": "chat", "gen_ai.request.model": "m", "weft.thread_id": "old-session"}},
	})
	if code, out := e.raw("/otel/v1/traces", body, map[string]string{"project_id": pid}); code != 200 {
		t.Fatalf("otlp -> %d %s", code, out)
	}
	root := e.post("/api/v1/spans/query", map[string]any{"project_id": pid, "include_run_status": true,
		"filter": map[string]any{"ids": []any{hexID("ok1", 16)}}})["items"].([]any)
	if len(root) != 1 {
		t.Fatalf("root = %v", root)
	}
	r := root[0].(map[string]any)
	if r["name"] != "helper.run" || r["session_id"] != "old-session" ||
		r["summary"].(map[string]any)["weft"].(map[string]any)["run_status"] != "failed_step" {
		t.Fatalf("root span = %v", r)
	}
	P := "project_id=" + url.QueryEscape(pid)
	if s := e.v1Get("/api/v1/sessions/old-session?" + P); s["session_id"] != "old-session" || v1Num(s["span_count"]) != 2 {
		t.Fatalf("session = %v", s)
	}
	for _, f := range []string{"linked_parent_span_id", "weft.parent_span.id", "parent_call_id"} {
		res := e.post("/agents/spans/query", map[string]any{"project_id": pid, "query": map[string]any{"$expr": map[string]any{
			"$eq": []any{map[string]any{"$getField": f}, map[string]any{"$literal": "caller-1"}}}}})
		s := res["spans"].([]any)
		if len(s) != 1 || s[0].(map[string]any)["linked_parent_trace_id"] != "caller-trace" ||
			s[0].(map[string]any)["conversation_id"] != "old-session" {
			t.Fatalf("by %s: %v", f, s)
		}
	}

	// SDK spans (nested attributes): one with the old link key, one with
	// the new; a span query by either attribute path finds both.
	now := time.Now().UTC().Format(time.RFC3339Nano)
	e.startSpan(map[string]any{"id": "old-sdk", "trace_id": "old-sdk-tr", "name": "chat", "started_at": now, "inputs": map[string]any{},
		"attributes": map[string]any{"weft": map[string]any{"parent_call": map[string]any{"id": "caller-2", "trace_id": "ct-2"}}}})
	e.startSpan(map[string]any{"id": "new-sdk", "trace_id": "new-sdk-tr", "name": "chat", "started_at": now, "inputs": map[string]any{},
		"attributes": map[string]any{"weft": map[string]any{"parent_span": map[string]any{"id": "caller-2", "trace_id": "ct-2"}}}})
	for _, path := range []string{"attributes.weft.parent_span.id", "attributes.weft.parent_call.id"} {
		items := e.post("/api/v1/spans/query", map[string]any{"project_id": pid, "query": map[string]any{"$expr": map[string]any{
			"$eq": []any{map[string]any{"$getField": path}, map[string]any{"$literal": "caller-2"}}}}})["items"].([]any)
		if len(items) != 2 {
			t.Fatalf("by %s: %v", path, items)
		}
	}
	// Flat OTel keys by the new name.
	items := e.post("/api/v1/spans/query", map[string]any{"project_id": pid, "query": map[string]any{"$expr": map[string]any{
		"$eq": []any{map[string]any{"$getField": "attributes.otel.attributes.[weft.session_id]"}, map[string]any{"$literal": "old-session"}}}}})["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("by flat weft.session_id: %v", items)
	}
}
