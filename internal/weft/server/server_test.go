package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agentshield-ai/agentshield/internal/weft/store"
)

type testEnv struct {
	t   *testing.T
	srv *httptest.Server
	key string
}

func newEnv(t *testing.T, keys ...string) *testEnv {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "weft.db"))
	if err != nil {
		t.Fatal(err)
	}
	s := New(Config{APIKeys: keys}, st)
	hs := httptest.NewServer(s.Handler())
	t.Cleanup(func() {
		hs.Close()
		s.Close()
		_ = st.Close()
	})
	e := &testEnv{t: t, srv: hs}
	if len(keys) > 0 {
		e.key = keys[0]
	}
	return e
}

func (e *testEnv) raw(path string, body any, headers map[string]string) (int, []byte) {
	e.t.Helper()
	var rdr io.Reader
	switch b := body.(type) {
	case []byte:
		rdr = bytes.NewReader(b)
	case nil:
	default:
		bb, _ := json.Marshal(b)
		rdr = bytes.NewReader(bb)
	}
	method := http.MethodPost
	if rdr == nil {
		method = http.MethodGet
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, rdr)
	req.Header.Set("Content-Type", "application/json")
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
	return resp.StatusCode, out
}

func (e *testEnv) post(path string, body any) map[string]any {
	e.t.Helper()
	code, out := e.raw(path, body, nil)
	if code != 200 {
		e.t.Fatalf("%s -> %d: %s", path, code, out)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		e.t.Fatalf("%s: bad JSON %s", path, out)
	}
	return m
}

const pid = "acme/evals"

func TestAuthRequired(t *testing.T) {
	e := newEnv(t, "s3cret")
	e.key = ""
	if code, _ := e.raw("/projects", nil, nil); code != 401 {
		t.Fatalf("expected 401, got %d", code)
	}
	if code, _ := e.raw("/health", nil, nil); code != 200 {
		t.Fatalf("health should be public, got %d", code)
	}
	if code, _ := e.raw("/projects", nil, map[string]string{"Authorization": "Basic YXBpOnMzY3JldA=="}); code != 200 {
		t.Fatalf("basic auth failed: %d", code)
	}
	if code, _ := e.raw("/projects", nil, map[string]string{"X-Weft-Api-Key": "wrong"}); code != 401 {
		t.Fatalf("wrong key accepted: %d", code)
	}
}

func TestOTLPJSONOverHTTP(t *testing.T) {
	e := newEnv(t)
	body := []byte(`{"resourceSpans":[{"resource":{},"scopeSpans":[{"spans":[
	 {"traceId":"5b8efff798038103d269b633813fc60c","spanId":"aaaaaaaaaaaaaaaa","name":"agent","startTimeUnixNano":"1700000000000000000","endTimeUnixNano":"1700000002000000000"},
	 {"traceId":"5b8efff798038103d269b633813fc60c","spanId":"bbbbbbbbbbbbbbbb","parentSpanId":"aaaaaaaaaaaaaaaa","name":"chat",
	  "startTimeUnixNano":"1700000000100000000","endTimeUnixNano":"1700000001000000000",
	  "attributes":[{"key":"gen_ai.request.model","value":{"stringValue":"gpt-4o"}},{"key":"gen_ai.usage.input_tokens","value":{"intValue":"100"}},
	                {"key":"gen_ai.usage.output_tokens","value":{"intValue":"20"}}]}]}]}]}`)
	code, out := e.raw("/otel/v1/traces", body, map[string]string{"project_id": pid})
	if code != 200 || string(bytes.TrimSpace(out)) != "{}" {
		t.Fatalf("otel ingest: %d %s", code, out)
	}
	res := e.post("/calls/query", map[string]any{"project_id": pid, "filter": map[string]any{"trace_roots_only": true}, "include_costs": true})
	calls := res["calls"].([]any)
	if len(calls) != 1 {
		t.Fatalf("expected one root, got %d", len(calls))
	}
	sum := calls[0].(map[string]any)["summary"].(map[string]any)
	if sum["usage"].(map[string]any)["gpt-4o"].(map[string]any)["prompt_tokens"].(float64) != 100 {
		t.Fatalf("root usage not rolled up: %v", sum["usage"])
	}
	if sum["weave"].(map[string]any)["costs"] == nil {
		t.Fatal("expected costs")
	}
}

func publish(e *testEnv, id string, val any) string {
	r := e.post("/obj/create", map[string]any{"obj": map[string]any{"project_id": pid, "object_id": id, "val": val}})
	return "weft:///" + pid + "/object/" + id + ":" + r["digest"].(string)
}

func waitEval(t *testing.T, e *testEnv, callID string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		st := e.post("/evaluation_status", map[string]any{"project_id": pid, "call_id": callID})["status"].(map[string]any)
		if st["code"] == "complete" {
			return
		}
		if st["code"] == "failed" {
			t.Fatalf("evaluation failed: %v", st)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("evaluation timed out")
}

func TestServerSideEvaluationCompareLeaderboard(t *testing.T) {
	e := newEnv(t)
	tbl := e.post("/table/create", map[string]any{"table": map[string]any{"project_id": pid, "rows": []any{
		map[string]any{"question": "paris", "expected": "paris"},
		map[string]any{"question": "rome", "expected": "madrid"},
		map[string]any{"question": "oslo", "expected": "oslo"},
	}}})
	ds := publish(e, "capitals", map[string]any{"_type": "Dataset", "_class_name": "Dataset", "_bases": []any{"Object", "BaseModel"},
		"name": "capitals", "rows": "weft:///" + pid + "/table/" + tbl["digest"].(string)})
	exact := publish(e, "exact", map[string]any{"_class_name": "ExactMatchScorer", "_bases": []any{"Scorer", "Object", "BaseModel"}, "target_column": "expected"})
	judge := publish(e, "judge", map[string]any{"_class_name": "LLMAsAJudgeScorer", "_bases": []any{"Scorer", "Object", "BaseModel"},
		"model": "mock/json", "scoring_prompt": "Q: {question} A: {output}"})
	ev := publish(e, "capital-eval", map[string]any{"_type": "Evaluation", "_class_name": "Evaluation", "_bases": []any{"Object", "BaseModel"},
		"dataset": ds, "scorers": []any{exact, judge}})
	echo := publish(e, "echo-model", map[string]any{"_class_name": "LLMStructuredCompletionModel", "_bases": []any{"Model", "Object", "BaseModel"},
		"llm_model_id": "mock/echo", "default_params": map[string]any{"messages_template": []any{map[string]any{"role": "user", "content": "{question}"}}}})
	shout := publish(e, "shout-model", map[string]any{"_class_name": "LLMStructuredCompletionModel", "_bases": []any{"Model", "Object", "BaseModel"},
		"llm_model_id": "mock/echo", "default_params": map[string]any{"messages_template": []any{map[string]any{"role": "user", "content": "{question}!"}}}})

	id1 := e.post("/evaluate_model", map[string]any{"project_id": pid, "evaluation_ref": ev, "model_ref": echo})["call_id"].(string)
	id2 := e.post("/evaluate_model", map[string]any{"project_id": pid, "evaluation_ref": ev, "model_ref": shout})["call_id"].(string)
	waitEval(t, e, id1)
	waitEval(t, e, id2)

	root := e.post("/call/read", map[string]any{"project_id": pid, "id": id1})["call"].(map[string]any)
	out := root["output"].(map[string]any)
	frac := out["exact"].(map[string]any)["true_fraction"].(float64)
	if frac < 0.66 || frac > 0.67 {
		t.Fatalf("echo exact fraction = %v, want 2/3 (summary %v)", frac, out)
	}
	if out["judge"].(map[string]any)["score"].(map[string]any)["mean"] == nil {
		t.Fatalf("judge summary missing: %v", out["judge"])
	}
	if root["summary"].(map[string]any)["usage"] == nil {
		t.Fatal("evaluation root should roll up LLM usage")
	}

	cmp := e.post("/evaluations/compare", map[string]any{"project_id": pid, "evaluation_call_ids": []any{id1, id2}})
	rows := cmp["rows"].([]any)
	if len(rows) != 3 {
		t.Fatalf("compare rows = %d", len(rows))
	}
	r0 := rows[0].(map[string]any)
	if r0["inputs"].(map[string]any)["question"] == nil || len(r0["results"].(map[string]any)) != 2 {
		t.Fatalf("compare row = %v", r0)
	}
	var found bool
	for _, m := range cmp["metrics"].([]any) {
		mm := m.(map[string]any)
		if mm["key"] == "exact.true_fraction" {
			found = true
			if mm["values"].(map[string]any)[id2].(float64) != 0 {
				t.Fatalf("shout model should score 0: %v", mm)
			}
		}
	}
	if !found {
		t.Fatalf("metric missing: %v", cmp["metrics"])
	}

	publish(e, "lb", map[string]any{"_class_name": "Leaderboard", "_bases": []any{"Object", "BaseModel"}, "name": "lb",
		"columns": []any{map[string]any{"evaluation_object_ref": ev, "scorer_name": "exact", "summary_metric_path": "true_fraction"}}})
	lb := e.post("/leaderboard/compute", map[string]any{"project_id": pid, "object_id": "lb"})
	if len(lb["rows"].([]any)) != 2 {
		t.Fatalf("leaderboard rows = %v", lb["rows"])
	}
}

func TestMonitorScoresEndedCalls(t *testing.T) {
	e := newEnv(t)
	judge := publish(e, "contains-ok", map[string]any{"_class_name": "RegexScorer", "_bases": []any{"Scorer", "Object", "BaseModel"}, "pattern": "ok"})
	publish(e, "mon", map[string]any{"_class_name": "Monitor", "_bases": []any{"Object", "BaseModel"}, "name": "mon", "active": true,
		"sampling_rate": 1, "op_names": []any{"answer"}, "scorers": []any{judge}})
	e.post("/call/upsert_batch", map[string]any{"batch": []any{
		map[string]any{"mode": "start", "req": map[string]any{"start": map[string]any{"project_id": pid, "id": "m1", "op_name": "answer",
			"started_at": "2026-01-01T00:00:00Z", "inputs": map[string]any{"q": "x"}}}},
		map[string]any{"mode": "end", "req": map[string]any{"end": map[string]any{"project_id": pid, "id": "m1",
			"ended_at": "2026-01-01T00:00:01Z", "output": "all ok"}}},
	}})
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		fb := e.post("/feedback/query", map[string]any{"project_id": pid,
			"query": map[string]any{"$expr": map[string]any{"$eq": []any{map[string]any{"$getField": "feedback_type"}, map[string]any{"$literal": "wandb.runnable.contains-ok"}}}}})
		if res := fb["result"].([]any); len(res) == 1 {
			f := res[0].(map[string]any)
			if f["payload"].(map[string]any)["output"] != true || !strings.Contains(f["trigger_ref"].(string), "/object/mon:") {
				t.Fatalf("unexpected feedback %v", f)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("monitor did not score the call")
}

func TestBadRequests(t *testing.T) {
	e := newEnv(t)
	cases := []struct {
		path string
		body any
	}{
		{"/calls/query", map[string]any{"project_id": ""}},
		{"/calls/query", map[string]any{"project_id": pid, "query": map[string]any{"$expr": map[string]any{"$nope": []any{}}}}},
		{"/calls/query", map[string]any{"project_id": pid, "sort_by": []any{map[string]any{"field": "bogus", "direction": "asc"}}}},
		{"/obj/create", map[string]any{"obj": map[string]any{"project_id": pid, "object_id": "bad name!", "val": 1}}},
		{"/call/start", map[string]any{"start": map[string]any{"project_id": pid, "op_name": ""}}},
		{"/evaluate_model", map[string]any{"project_id": pid, "evaluation_ref": "nope", "model_ref": "nope"}},
		{"/call/start", []byte("{not json")},
	}
	for _, c := range cases {
		if code, out := e.raw(c.path, c.body, nil); code != 400 {
			t.Errorf("%s %v -> %d %s, want 400", c.path, c.body, code, out)
		}
	}
	if code, _ := e.raw("/obj/read", map[string]any{"project_id": pid, "object_id": "missing", "digest": "latest"}, nil); code != 404 {
		t.Errorf("missing object -> %d, want 404", code)
	}
}
