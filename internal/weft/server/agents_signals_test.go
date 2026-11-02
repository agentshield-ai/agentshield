package server

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func spanIDsWhere(t *testing.T, e *testEnv, body map[string]any) map[string]bool {
	t.Helper()
	body["project_id"] = pid
	body["limit"] = 1000
	res := e.post("/agents/spans/query", body)
	out := map[string]bool{}
	for _, s := range res["spans"].([]any) {
		out[s.(map[string]any)["trace_id"].(string)] = true
	}
	return out
}

// turnSpan returns the invoke_agent span of conv-1 started first or last.
func convTurns(t *testing.T, e *testEnv) []map[string]any {
	t.Helper()
	res := e.post("/agents/spans/query", map[string]any{"project_id": pid, "sort_by": []any{map[string]any{"field": "started_at", "direction": "asc"}},
		"query": map[string]any{"$expr": map[string]any{"$and": []any{
			map[string]any{"$eq": []any{map[string]any{"$getField": "conversation_id"}, map[string]any{"$literal": "conv-1"}}},
			map[string]any{"$eq": []any{map[string]any{"$getField": "operation_name"}, map[string]any{"$literal": "invoke_agent"}}},
		}}}})
	var out []map[string]any
	for _, s := range res["spans"].([]any) {
		out = append(out, s.(map[string]any))
	}
	if len(out) != 2 {
		t.Fatalf("conv-1 turns = %v", res)
	}
	return out
}

func TestAgentSignalsRunAndQuery(t *testing.T) {
	e := newEnv(t)
	seedAgentData(t, e, time.Now().Add(-time.Hour))

	code, body := e.raw("/agents/signals/presets", nil, nil)
	var pr map[string]any
	if code != 200 || json.Unmarshal(body, &pr) != nil {
		t.Fatalf("presets: %d %s", code, body)
	}
	presets := pr["presets"].([]any)
	ids := map[string]string{}
	for _, p := range presets {
		m := p.(map[string]any)
		ids[m["id"].(string)] = m["kind"].(string)
	}
	for id, kind := range map[string]string{"user_frustration": "tag", "jailbreak": "tag", "nsfw": "tag", "low_quality_response": "tag",
		"user_satisfaction": "rating", "user_good_intent": "rating", "safe_for_work": "rating", "response_quality": "rating",
		"sentiment": "rating", "task_success": "rating"} {
		if ids[id] != kind {
			t.Fatalf("preset %s = %q (%v)", id, ids[id], presets)
		}
	}

	run := map[string]any{"project_id": pid, "model": "mock/json", "signals": []any{
		map[string]any{"preset": "user_frustration"}, map[string]any{"preset": "task_success"},
		map[string]any{"id": "polite", "name": "Politeness", "kind": "rating", "description": "How polite the agent is."}}}
	res := e.post("/agents/signals/run", run)
	if res["turns"].(float64) != 4 || res["scored"].(float64) != 12 || res["failed"].(float64) != 0 {
		t.Fatalf("run = %v", res)
	}
	for _, r := range res["results"].([]any) {
		m := r.(map[string]any)
		if m["kind"] == "rating" {
			if sc := m["score"].(float64); sc < 0 || sc > 1 {
				t.Fatalf("score not normalised: %v", m)
			}
		} else if _, ok := m["matched"].(bool); !ok {
			t.Fatalf("tag result = %v", m)
		}
	}
	// The judge's own LLM calls are internal and stay out of the agent views.
	if got := e.post("/agents/spans/query", map[string]any{"project_id": pid, "query": map[string]any{"$expr": map[string]any{"$eq": []any{
		map[string]any{"$getField": "request_model"}, map[string]any{"$literal": "mock/json"}}}}}); got["total_count"].(float64) != 0 {
		t.Fatalf("judge calls indexed as agent spans: %v", got["total_count"])
	}
	// Already-scored turns are skipped unless rescore is set.
	res = e.post("/agents/signals/run", run)
	if res["scored"].(float64) != 0 || res["skipped"].(float64) != 12 {
		t.Fatalf("second run = %v", res)
	}
	// Bad specs are rejected.
	for _, bad := range []map[string]any{
		{"project_id": pid, "model": "mock/json", "signals": []any{map[string]any{"preset": "nope"}}},
		{"project_id": pid, "model": "mock/json", "signals": []any{map[string]any{"id": "Bad Id!", "kind": "tag", "description": "x"}}},
		{"project_id": pid, "model": "mock/json", "signals": []any{map[string]any{"id": "x", "kind": "score", "description": "x"}}},
		{"project_id": pid, "signals": []any{map[string]any{"preset": "nsfw"}}},
	} {
		if code, out := e.raw("/agents/signals/run", bad, nil); code != 400 {
			t.Fatalf("bad run accepted: %d %s", code, out)
		}
	}

	q := e.post("/agents/signals/query", map[string]any{"project_id": pid, "kind": "rating", "limit": 100})
	if q["total_count"].(float64) != 8 {
		t.Fatalf("rating results = %v", q["total_count"])
	}
	row := q["results"].([]any)[0].(map[string]any)
	if row["reason"] != "mock judgement" || row["span_id"] == "" || row["turn_started_at"] == nil || row["scorer_call_id"] == nil {
		t.Fatalf("rating row = %v", row)
	}
	sums := map[string]map[string]any{}
	for _, s := range q["summary"].([]any) {
		m := s.(map[string]any)
		sums[m["signal"].(string)] = m
	}
	if sums["task_success"]["count"].(float64) != 4 || sums["polite"]["avg"] == nil {
		t.Fatalf("summary = %v", q["summary"])
	}
	q = e.post("/agents/signals/query", map[string]any{"project_id": pid, "agent_name": "travel-agent", "signal": "task_success"})
	if q["total_count"].(float64) != 2 {
		t.Fatalf("agent results = %v", q)
	}
	if row := q["results"].([]any)[0].(map[string]any); row["agent_name"] != "travel-agent" || row["conversation_id"] != "conv-1" || row["last_message"] == nil {
		t.Fatalf("agent row = %v", row)
	}
}

func TestAgentFeedbackBatchAndSignalFilters(t *testing.T) {
	e := newEnv(t)
	seedAgentData(t, e, time.Now().Add(-time.Hour))
	turns := convTurns(t, e)
	t1, t2 := turns[0], turns[1]
	ref := func(id string) string { return "weave:///" + pid + "/call/" + id }

	res := e.post("/agents/feedback/batch/create", map[string]any{"batch": []any{
		map[string]any{"project_id": pid, "weave_ref": ref(t1["span_id"].(string)), "feedback_type": "wandb.agent_monitor",
			"payload":        map[string]any{"signal": "manual"},
			"scorer_ratings": map[string]any{"response_quality": 0.9, "user_satisfaction": 0.1},
			"scorer_rating_confidences": map[string]any{"response_quality": 1.7}, "scorer_rating_reasons": map[string]any{"response_quality": "Answered\n\n<b>fully</b>"},
			"scorer_tags": []any{"User_Frustration", "user_frustration ", "Weather"}, "scorer_tag_confidences": map[string]any{"USER_FRUSTRATION": 0.8},
			"scorer_tag_reasons": map[string]any{"user_frustration": "Asked twice"}},
		map[string]any{"project_id": pid, "weave_ref": ref(t2["span_id"].(string)), "feedback_type": "wandb.agent_monitor", "payload": map[string]any{},
			"scorer_ratings": map[string]any{"response_quality": 0.2}},
	}})
	stored := res["res"].([]any)[0].(map[string]any)["payload"].(map[string]any)
	if tags := stored["scorer_tags"].([]any); len(tags) != 2 || tags[0] != "user_frustration" || tags[1] != "weather" {
		t.Fatalf("tags not normalised: %v", stored)
	}
	if stored["scorer_rating_confidences"].(map[string]any)["response_quality"].(float64) != 1 ||
		stored["scorer_rating_reasons"].(map[string]any)["response_quality"] != "Answered bfully/b" ||
		stored["scorer_tag_confidences"].(map[string]any)["user_frustration"].(float64) != 0.8 {
		t.Fatalf("scores not normalised: %v", stored)
	}
	if stored["span_conversation_id"] != "conv-1" || stored["span_agent_name"] != "travel-agent" || stored["span_trace_id"] != t1["trace_id"] ||
		stored["span_status_code"] != "OK" {
		t.Fatalf("span fields = %v", stored)
	}
	// A tag over 36 characters rejects the item.
	if code, _ := e.raw("/agents/feedback/batch/create", map[string]any{"batch": []any{map[string]any{"project_id": pid,
		"weave_ref": ref(t1["span_id"].(string)), "feedback_type": "wandb.agent_monitor", "payload": map[string]any{},
		"scorer_tags": []any{strings.Repeat("x", 37)}}}}, nil); code != 400 {
		t.Fatalf("long tag accepted: %d", code)
	}

	// Chat views lift the scorer fields back out of the payload.
	chat := e.post("/agents/conversations/chat", map[string]any{"project_id": pid, "conversation_id": "conv-1", "include_feedback": true})
	fb := chat["turns"].([]any)[0].(map[string]any)["feedback"].([]any)[0].(map[string]any)
	if fb["scorer_ratings"].(map[string]any)["response_quality"].(float64) != 0.9 || fb["scorer_rating_confidences"].(map[string]any)["response_quality"].(float64) != 1 ||
		fb["scorer_tag_reasons"].(map[string]any)["user_frustration"] != "Asked twice" || len(fb["scorer_tags"].([]any)) != 2 {
		t.Fatalf("chat feedback = %v", fb)
	}
	cs := e.post("/agents/conversations/spans", map[string]any{"project_id": pid, "conversation_ids": []any{"conv-1"}})
	sf := cs["conversations"].([]any)[0].(map[string]any)["spans_feedback"].([]any)
	if len(sf) != 2 || sf[0].(map[string]any)["ratings"].([]any)[0].(map[string]any)["confidence"] == nil {
		t.Fatalf("spans_feedback = %v", sf)
	}

	// signal_filters keep spans of matching turns.
	tr1, tr2 := t1["trace_id"].(string), t2["trace_id"].(string)
	got := spanIDsWhere(t, e, map[string]any{"signal_filters": map[string]any{"ratings": []any{map[string]any{"op": "gte", "scorer_key": "response_quality", "value": 0.5}}}})
	if len(got) != 1 || !got[tr1] {
		t.Fatalf("rating filter traces = %v", got)
	}
	got = spanIDsWhere(t, e, map[string]any{"signal_filters": map[string]any{"ratings": []any{map[string]any{"op": "lt", "scorer_key": "response_quality", "value": 0.5}}}})
	if len(got) != 1 || !got[tr2] {
		t.Fatalf("lt filter traces = %v", got)
	}
	got = spanIDsWhere(t, e, map[string]any{"signal_filters": map[string]any{"tags": []any{"USER_FRUSTRATION", "nsfw"}}})
	if len(got) != 1 || !got[tr1] {
		t.Fatalf("tag filter traces = %v", got)
	}
	if got = spanIDsWhere(t, e, map[string]any{"signal_filters": map[string]any{"tags": []any{"nsfw"}}}); len(got) != 0 {
		t.Fatalf("unmatched tag filter = %v", got)
	}
	if code, _ := e.raw("/agents/spans/query", map[string]any{"project_id": pid, "signal_filters": map[string]any{"ratings": []any{map[string]any{"op": "ne", "scorer_key": "x", "value": 1}}}}, nil); code != 400 {
		t.Fatal("bad signal op accepted")
	}

	// insight_filters are conversation-scoped: the frustration tag and the low
	// satisfaction rating on turn 1 mark all of conv-1 as frustrated.
	got = spanIDsWhere(t, e, map[string]any{"insight_filters": []any{map[string]any{"field": "intent_sentiment", "values": []any{"frustrated"}}}})
	if len(got) != 2 || !got[tr1] || !got[tr2] {
		t.Fatalf("insight filter traces = %v", got)
	}
	got = spanIDsWhere(t, e, map[string]any{"insight_filters": []any{map[string]any{"field": "intent_sentiment", "values": []any{"frustrated"}, "exclude": true}}})
	if len(got) != 2 || got[tr1] || got[tr2] {
		t.Fatalf("excluded insight traces = %v", got)
	}
	if code, _ := e.raw("/agents/spans/query", map[string]any{"project_id": pid, "insight_filters": []any{map[string]any{"field": "intent_sentiment", "values": []any{"angry"}}}}, nil); code != 400 {
		t.Fatal("bad sentiment accepted")
	}
	if code, _ := e.raw("/agents/spans/query", map[string]any{"project_id": pid, "insight_filters": []any{map[string]any{"field": "mood", "values": []any{"x"}}}}, nil); code != 400 {
		t.Fatal("bad insight field accepted")
	}
	// Explicit insights in a payload.
	e.post("/agents/feedback/batch/create", map[string]any{"batch": []any{map[string]any{"project_id": pid, "weave_ref": ref(t2["span_id"].(string)),
		"feedback_type": "wandb.agent_monitor", "payload": map[string]any{"insights": map[string]any{"failure_severity": "major", "intent_category": []any{"weather"}}}}}})
	got = spanIDsWhere(t, e, map[string]any{"insight_filters": []any{map[string]any{"field": "failure_severity", "values": []any{"major", "minor"}},
		map[string]any{"field": "intent_category", "values": []any{"weather"}}}})
	if len(got) != 2 {
		t.Fatalf("payload insight traces = %v", got)
	}

	// Stats honour the same filters.
	st := e.post("/agents/spans/stats", map[string]any{"project_id": pid, "start": time.Now().Add(-2 * time.Hour).Format(time.RFC3339),
		"signal_filters": map[string]any{"tags": []any{"user_frustration"}}})
	var n float64
	for _, r := range st["rows"].([]any) {
		n += r.(map[string]any)["count"].(float64)
	}
	if n != 4 {
		t.Fatalf("filtered stats count = %v (%v)", n, st["rows"])
	}
}

func TestJudgeNormalisation(t *testing.T) {
	for _, c := range []struct {
		in   any
		want float64
	}{{0.4, 0.4}, {5.0, 1}, {3.0, 0.5}, {8.0, 0.8}, {75.0, 0.75}, {true, 1}, {"no", 0}, {-2.0, 0}} {
		if got, ok := judgeNumber(c.in); !ok || got != c.want {
			t.Fatalf("judgeNumber(%v) = %v, %v", c.in, got, ok)
		}
	}
	m, err := parseJudge("Sure!\n```json\n{\"matched\": \"yes\", \"confidence\": 90, \"reason\": \"x\"}\n```")
	if err != nil {
		t.Fatal(err)
	}
	sc, res, err := signalScores(SignalSpec{ID: "nsfw", Kind: "tag"}, m)
	if err != nil || !*res.Matched || sc.ScorerTags[0] != "nsfw" || sc.ScorerTagConfidences["nsfw"] != 0.9 {
		t.Fatalf("tag scores = %v %v %v", sc, res, err)
	}
	if _, _, err := signalScores(SignalSpec{ID: "q", Kind: "rating"}, map[string]any{"reason": "no score"}); err == nil {
		t.Fatal("missing score accepted")
	}
	if _, err := parseJudge("no json here"); err == nil {
		t.Fatal("non-JSON accepted")
	}
}
