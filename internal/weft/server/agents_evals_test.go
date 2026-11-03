package server

import (
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"
)

// seedAgentEvals records three SDK conversations, an OTLP agent trace and two
// evaluation runs that link to them in each of the supported ways.
func seedAgentEvals(t *testing.T, e *testEnv) time.Time {
	t.Helper()
	now := time.Now().UTC().Add(-time.Minute)
	ts := func(ms int) string { return now.Add(time.Duration(ms) * time.Millisecond).Format(time.RFC3339Nano) }
	call := func(id, parent, trace, thread, op string, ms int, attrs, inputs map[string]any, out any) []any {
		s := map[string]any{"project_id": pid, "id": id, "trace_id": trace, "op_name": op, "started_at": ts(ms), "attributes": attrs, "inputs": inputs}
		if thread != "" {
			s["thread_id"] = thread
		}
		if parent != "" {
			s["parent_id"] = parent
		} else if thread != "" {
			s["turn_id"] = id
		}
		return []any{
			map[string]any{"mode": "start", "req": map[string]any{"start": s}},
			map[string]any{"mode": "end", "req": map[string]any{"end": map[string]any{"project_id": pid, "id": id, "ended_at": ts(ms + 50), "output": out, "summary": map[string]any{}}}},
		}
	}
	agent := func(name string, extra map[string]any) map[string]any {
		a := map[string]any{"weft": map[string]any{"span_kind": "agent"}, "gen_ai": map[string]any{"agent": map[string]any{"name": name}}}
		for k, v := range extra {
			a[k] = v
		}
		return a
	}
	llm := map[string]any{"weft": map[string]any{"span_kind": "llm"}}
	evalRef := "weft:///" + pid + "/object/support-eval:abc"
	modelRef := "weft:///" + pid + "/object/support-model:def"
	var batch []any
	add := func(xs []any) { batch = append(batch, xs...) }
	// Conversations recorded before the evaluation (linked afterwards).
	add(call("a-turn", "", "tr-a", "conv-a", "support_turn", 0, agent("support_bot", nil), map[string]any{"question": "reset password"}, "use the link"))
	add(call("a-llm", "a-turn", "tr-a", "conv-a", "chat", 5, llm, map[string]any{}, "use the link"))
	add(call("d-turn", "", "tr-d", "conv-d", "support_turn", 100, agent("support_bot", nil), map[string]any{"question": "refund"}, "ok"))
	// A conversation recorded inside a prediction: weft.eval.* on the turn only.
	add(call("b-turn", "", "tr-b", "conv-b", "billing_turn", 200, agent("billing_bot", map[string]any{"weft": map[string]any{"eval": map[string]any{
		"run_id": "run-1", "predict_and_score_call_id": "pas-2", "kind": "agent"}}}), map[string]any{"question": "invoice"}, "sent"))
	add(call("b-llm", "b-turn", "tr-b", "conv-b", "chat", 205, llm, map[string]any{}, "sent"))
	// Unrelated.
	add(call("c-turn", "", "tr-c", "conv-c", "support_turn", 300, agent("support_bot", nil), map[string]any{"question": "hello"}, "hi"))
	// Run 1: pas-1 links an existing turn; pas-2 recorded conv-b; pas-3 links a conversation.
	add(call("run-1", "", "tr-run1", "", "Evaluation.evaluate", 400, nil, map[string]any{"self": evalRef, "model": modelRef},
		map[string]any{"resolved": map[string]any{"true_fraction": 0.67}}))
	add(call("pas-1", "run-1", "tr-run1", "", "Evaluation.predict_and_score", 410, map[string]any{"weft": map[string]any{
		"linked_calls": []any{map[string]any{"call_id": "a-turn", "trace_id": "tr-a"}}, "genai_span_ref": []any{map[string]any{"trace_id": "tr-a", "span_id": "a-turn"}},
		"eval": map[string]any{"kind": "agent", "row_digest": "row-a"}}},
		map[string]any{"example": map[string]any{"question": "reset password"}}, map[string]any{"output": "use the link", "scores": map[string]any{"resolved": true}, "model_latency": 0.05}))
	add(call("pas-2", "run-1", "tr-run1", "", "Evaluation.predict_and_score", 420, nil, map[string]any{"example": map[string]any{"question": "invoice"}},
		map[string]any{"output": "sent", "scores": map[string]any{"resolved": true}}))
	add(call("pas-3", "run-1", "tr-run1", "", "Evaluation.predict_and_score", 430, map[string]any{"weft": map[string]any{"linked_conversation_ids": []any{"conv-d"}}},
		map[string]any{"example": map[string]any{"question": "refund"}}, map[string]any{"output": "ok", "scores": map[string]any{"resolved": false}}))
	// Run 2 links the same turn again.
	add(call("run-2", "", "tr-run2", "", "Evaluation.evaluate", 500, nil, map[string]any{"self": evalRef, "model": modelRef}, map[string]any{"resolved": map[string]any{"true_fraction": 1.0}}))
	add(call("pas-9", "run-2", "tr-run2", "", "Evaluation.predict_and_score", 510, map[string]any{"weft": map[string]any{"linked_calls": []any{"weft:///" + pid + "/call/a-turn"}}},
		map[string]any{"example": map[string]any{"question": "reset password"}}, map[string]any{"output": "use the link", "scores": map[string]any{"resolved": true}}))
	e.post("/call/upsert_batch", map[string]any{"batch": batch})

	// An OTel agent service tags only its root span; the child is linked as a descendant.
	tr, root, child := hexID("evtr", 32), hexID("evroot", 16), hexID("evchild", 16)
	body := otlpBody(now.Add(600*time.Millisecond), []testSpan{
		{tr, child, root, "chat gpt-4o", 5, 20, map[string]any{"gen_ai.operation.name": "chat", "gen_ai.request.model": "gpt-4o"}, ""},
		{tr, root, "", "invoke_agent otel_bot", 0, 40, map[string]any{"gen_ai.operation.name": "invoke_agent", "gen_ai.agent.name": "otel_bot",
			"gen_ai.conversation.id": "conv-otel", "weft.eval.run_id": "run-1", "weft.eval.predict_and_score_call_id": "pas-2"}, ""},
	})
	if code, b := e.raw("/agents/otel/v1/traces", body, map[string]string{"project_id": pid}); code != 200 {
		t.Fatalf("otlp: %d %s", code, b)
	}
	return now
}

func spanIDsOf(res map[string]any) []string {
	var out []string
	for _, s := range res["spans"].([]any) {
		m := s.(map[string]any)
		out = append(out, fmt.Sprint(m["display_name"]))
	}
	sort.Strings(out)
	return out
}

func TestAgentEvalsFilterAndList(t *testing.T) {
	e := newEnv(t)
	seedAgentEvals(t, e)

	// Spans of run 1: the linked turn and its child, the conversation recorded
	// in the prediction, the linked conversation, and the OTel trace.
	res := e.post("/agents/spans/query", map[string]any{"project_id": pid, "evaluation_run_id": "run-1", "limit": 100})
	if res["total_count"].(float64) != 7 {
		t.Fatalf("run-1 spans = %v %v", res["total_count"], spanIDsOf(res))
	}
	got := strings.Join(spanIDsOf(res), ",")
	for _, bad := range []string{"c-turn", "Evaluation"} {
		if strings.Contains(got, bad) {
			t.Fatalf("run-1 spans include %s: %s", bad, got)
		}
	}
	// One prediction.
	res = e.post("/agents/spans/query", map[string]any{"project_id": pid, "predict_and_score_call_id": "pas-2"})
	if res["total_count"].(float64) != 4 { // b-turn, b-llm, OTel root and child
		t.Fatalf("pas-2 spans = %v", spanIDsOf(res))
	}
	res = e.post("/agents/spans/query", map[string]any{"project_id": pid, "evaluation_run_id": "run-2"})
	if res["total_count"].(float64) != 2 {
		t.Fatalf("run-2 spans = %v", spanIDsOf(res))
	}
	// Conversations of a run, as groups.
	res = e.post("/agents/spans/query", map[string]any{"project_id": pid, "evaluation_run_id": "run-1", "group_by": []any{map[string]any{"key": "conversation_id"}}})
	if res["total_count"].(float64) != 4 {
		t.Fatalf("run-1 conversation groups = %v", res)
	}
	// Stats honour the filter.
	stats := e.post("/agents/spans/stats", map[string]any{"project_id": pid, "start": time.Now().Add(-time.Hour).Format(time.RFC3339), "evaluation_run_id": "run-2"})
	n := 0.0
	for _, r := range stats["rows"].([]any) {
		n += r.(map[string]any)["count"].(float64)
	}
	if n != 2 {
		t.Fatalf("run-2 stats count = %v", n)
	}
	// Unknown run: nothing.
	if res = e.post("/agents/spans/query", map[string]any{"project_id": pid, "evaluation_run_id": "nope"}); res["total_count"].(float64) != 0 {
		t.Fatalf("unknown run spans = %v", res)
	}

	// Runs with agent activity.
	evs := e.post("/agents/evals/query", map[string]any{"project_id": pid, "include_costs": true})
	if evs["total_count"].(float64) != 2 {
		t.Fatalf("evals = %v", evs)
	}
	runs := evs["evaluations"].([]any)
	r1, r2 := runs[1].(map[string]any), runs[0].(map[string]any) // newest first
	if r2["call_id"] != "run-2" || r1["call_id"] != "run-1" {
		t.Fatalf("eval order = %v, %v", r2["call_id"], r1["call_id"])
	}
	if r1["evaluation_name"] != "support-eval" || r1["model_name"] != "support-model" || r1["status"] != "success" ||
		r1["prediction_count"].(float64) != 3 || r1["conversation_count"].(float64) != 4 || r1["span_count"].(float64) != 7 {
		t.Fatalf("run-1 = %v", r1)
	}
	if fmt.Sprint(r1["agent_names"]) != "[billing_bot otel_bot support_bot]" {
		t.Fatalf("run-1 agents = %v", r1["agent_names"])
	}
	if r1["summary"].(map[string]any)["resolved"] == nil {
		t.Fatalf("run-1 summary = %v", r1["summary"])
	}
	// Per agent.
	evs = e.post("/agents/evals/query", map[string]any{"project_id": pid, "agent_name": "billing_bot"})
	if evs["total_count"].(float64) != 1 || evs["evaluations"].([]any)[0].(map[string]any)["span_count"].(float64) != 2 {
		t.Fatalf("billing_bot evals = %v", evs)
	}
	// Per conversation, with the predictions it belongs to.
	evs = e.post("/agents/evals/query", map[string]any{"project_id": pid, "conversation_id": "conv-a"})
	if evs["total_count"].(float64) != 2 {
		t.Fatalf("conv-a evals = %v", evs)
	}
	for _, x := range evs["evaluations"].([]any) {
		r := x.(map[string]any)
		preds := r["predictions"].([]any)
		if len(preds) != 1 {
			t.Fatalf("conv-a predictions of %v = %v", r["call_id"], preds)
		}
		p := preds[0].(map[string]any)
		want := map[string]string{"run-1": "pas-1", "run-2": "pas-9"}[r["call_id"].(string)]
		if p["predict_and_score_call_id"] != want || fmt.Sprint(p["conversation_ids"]) != "[conv-a]" || p["scores"].(map[string]any)["resolved"] != true {
			t.Fatalf("conv-a prediction = %v", p)
		}
		if r["call_id"] == "run-1" && (p["row_digest"] != "row-a" || p["kind"] != "agent") {
			t.Fatalf("pas-1 eval metadata = %v", p)
		}
	}
	// Restricted to a set of runs.
	evs = e.post("/agents/evals/query", map[string]any{"project_id": pid, "evaluation_run_ids": []any{"run-2", "unknown"}})
	if evs["total_count"].(float64) != 1 {
		t.Fatalf("run filter = %v", evs)
	}

	// Conversations of a run with their predictions.
	cs := e.post("/agents/evals/conversations", map[string]any{"project_id": pid, "evaluation_run_id": "run-1", "include_costs": true})
	if cs["total_count"].(float64) != 4 || cs["span_count"].(float64) != 7 || cs["other_span_count"].(float64) != 0 {
		t.Fatalf("run-1 conversations = %v", cs)
	}
	byConv := map[string]map[string]any{}
	for _, x := range cs["conversations"].([]any) {
		m := x.(map[string]any)
		byConv[m["conversation_id"].(string)] = m
	}
	if p := byConv["conv-d"]["predictions"].([]any); len(p) != 1 || p[0].(map[string]any)["predict_and_score_call_id"] != "pas-3" {
		t.Fatalf("conv-d = %v", byConv["conv-d"])
	}
	if byConv["conv-b"]["span_count"].(float64) != 2 || byConv["conv-b"]["first_message"] == nil {
		t.Fatalf("conv-b = %v", byConv["conv-b"])
	}
	if code, _ := e.raw("/agents/evals/conversations", map[string]any{"project_id": pid}, nil); code != 400 {
		t.Fatalf("missing run: %d", code)
	}

	// Deleting a prediction drops its links.
	e.post("/calls/delete", map[string]any{"project_id": pid, "call_ids": []any{"pas-9"}})
	if res = e.post("/agents/spans/query", map[string]any{"project_id": pid, "evaluation_run_id": "run-2"}); res["total_count"].(float64) != 0 {
		t.Fatalf("run-2 spans after delete = %v", spanIDsOf(res))
	}
}
