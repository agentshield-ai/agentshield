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
	calls := e.spans(map[string]any{"filter": map[string]any{"trace_roots_only": true}, "include_costs": true})
	if len(calls) != 1 {
		t.Fatalf("expected one root, got %d", len(calls))
	}
	sum := calls[0].(map[string]any)["summary"].(map[string]any)
	if sum["usage"].(map[string]any)["gpt-4o"].(map[string]any)["prompt_tokens"].(float64) != 100 {
		t.Fatalf("root usage not rolled up: %v", sum["usage"])
	}
	if sum["weft"].(map[string]any)["costs"] == nil {
		t.Fatal("expected costs")
	}
}

func publish(e *testEnv, id string, val any) string {
	return e.save(id, val)
}

func waitEval(t *testing.T, e *testEnv, callID string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		st := e.getJSON("/api/v1/evaluations/" + callID + "/status?project_id=" + pid)
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
	tbl := e.post("/api/v1/tables/create", map[string]any{"project_id": pid, "rows": []any{
		map[string]any{"question": "paris", "expected": "paris"},
		map[string]any{"question": "rome", "expected": "madrid"},
		map[string]any{"question": "oslo", "expected": "oslo"},
	}})
	ds := publish(e, "capitals", map[string]any{"_type": "Dataset", "_class_name": "Dataset", "_bases": []any{"Object", "BaseModel"},
		"name": "capitals", "rows": tbl["ref"]})
	exact := publish(e, "exact", map[string]any{"_class_name": "ExactMatchScorer", "_bases": []any{"Scorer", "Object", "BaseModel"}, "target_column": "expected"})
	judge := publish(e, "judge", map[string]any{"_class_name": "LLMAsAJudgeScorer", "_bases": []any{"Scorer", "Object", "BaseModel"},
		"model": "mock/json", "scoring_prompt": "Q: {question} A: {output}"})
	ev := publish(e, "capital-eval", map[string]any{"_type": "Evaluation", "_class_name": "Evaluation", "_bases": []any{"Object", "BaseModel"},
		"dataset": ds, "scorers": []any{exact, judge}})
	echo := publish(e, "echo-model", map[string]any{"_class_name": "LLMStructuredCompletionModel", "_bases": []any{"Model", "Object", "BaseModel"},
		"llm_model_id": "mock/echo", "default_params": map[string]any{"messages_template": []any{map[string]any{"role": "user", "content": "{question}"}}}})
	shout := publish(e, "shout-model", map[string]any{"_class_name": "LLMStructuredCompletionModel", "_bases": []any{"Model", "Object", "BaseModel"},
		"llm_model_id": "mock/echo", "default_params": map[string]any{"messages_template": []any{map[string]any{"role": "user", "content": "{question}!"}}}})

	id1 := e.post("/api/v1/evaluations/run", map[string]any{"project_id": pid, "evaluation_ref": ev, "model_ref": echo})["id"].(string)
	id2 := e.post("/api/v1/evaluations/run", map[string]any{"project_id": pid, "evaluation_ref": ev, "model_ref": shout})["id"].(string)
	waitEval(t, e, id1)
	waitEval(t, e, id2)

	root := e.span(id1)
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

	cmp := e.post("/api/v1/evaluations/compare", map[string]any{"project_id": pid, "ids": []any{id1, id2}})
	rows := cmp["rows"].([]any)
	if len(rows) != 3 {
		t.Fatalf("compare rows = %d", len(rows))
	}
	r0 := rows[0].(map[string]any)
	if r0["inputs"].(map[string]any)["question"] == nil || len(r0["results"].(map[string]any)) != 2 {
		t.Fatalf("compare row = %v", r0)
	}
	if res := r0["results"].(map[string]any)[id1].([]any)[0].(map[string]any); res["span_id"] == nil || res["call_id"] != nil {
		t.Fatalf("compare results name the span: %v", res)
	}
	if ev0 := cmp["evaluations"].([]any)[0].(map[string]any); ev0["id"] != id1 {
		t.Fatalf("compare evaluations: %v", ev0)
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
	lb := e.post("/api/v1/leaderboards/compute", map[string]any{"project_id": pid, "name": "lb"})
	if len(lb["rows"].([]any)) != 2 {
		t.Fatalf("leaderboard rows = %v", lb["rows"])
	}
	cell := lb["rows"].([]any)[0].(map[string]any)["values"].(map[string]any)["0"].(map[string]any)
	if cell["evaluation_run_id"] == nil || lb["leaderboard"].(map[string]any)["name"] != "lb" {
		t.Fatalf("leaderboard = %v", lb)
	}
}

func TestMonitorScoresEndedCalls(t *testing.T) {
	e := newEnv(t)
	judge := publish(e, "contains-ok", map[string]any{"_class_name": "RegexScorer", "_bases": []any{"Scorer", "Object", "BaseModel"}, "pattern": "ok"})
	publish(e, "mon", map[string]any{"_class_name": "Monitor", "_bases": []any{"Object", "BaseModel"}, "name": "mon", "active": true,
		"sampling_rate": 1, "names": []any{"answer"}, "scorers": []any{judge}})
	e.spanBatch(
		startItem(map[string]any{"id": "m1", "name": "answer", "started_at": "2026-01-01T00:00:00Z", "inputs": map[string]any{"q": "x"}}),
		endItem(map[string]any{"id": "m1", "ended_at": "2026-01-01T00:00:01Z", "output": "all ok"}),
	)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		fb := e.post("/api/v1/feedback/query", map[string]any{"project_id": pid,
			"query": map[string]any{"$expr": map[string]any{"$eq": []any{map[string]any{"$getField": "feedback_type"}, map[string]any{"$literal": "weft.scorer.contains-ok"}}}}})
		if res := fb["items"].([]any); len(res) == 1 {
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
		{"/api/v1/spans/query", map[string]any{"project_id": ""}},
		{"/api/v1/spans/query", map[string]any{"project_id": pid, "query": map[string]any{"$expr": map[string]any{"$nope": []any{}}}}},
		{"/api/v1/spans/query", map[string]any{"project_id": pid, "sort_by": []any{map[string]any{"field": "bogus", "direction": "asc"}}}},
		{"/api/v1/spans/query", map[string]any{"project_id": pid, "filter": map[string]any{"op_names": []any{"x"}}}}, // not a span filter field
		{"/api/v1/spans/query", map[string]any{"project_id": pid, "limit": 100000}},
		{"/api/v1/objects/save", map[string]any{"project_id": pid, "name": "bad name!", "value": 1}},
		{"/api/v1/objects/save", map[string]any{"project_id": pid, "name": "x"}},
		{"/api/v1/objects/query", map[string]any{"project_id": pid, "filter": map[string]any{"kinds": []any{"nope"}}}},
		{"/api/v1/objects/query", map[string]any{"project_id": pid, "filter": map[string]any{"is_op": true}}},
		{"/api/v1/spans/start", map[string]any{"project_id": pid, "name": ""}},
		{"/api/v1/spans/batch", map[string]any{"project_id": pid, "items": []any{map[string]any{}}}},
		{"/api/v1/spans/delete", map[string]any{"project_id": pid}},
		{"/api/v1/evaluations/run", map[string]any{"project_id": pid, "evaluation_ref": "nope", "model_ref": "nope"}},
		{"/api/v1/spans/start", []byte("{not json")},
		{"/api/v1/feedback/delete", map[string]any{"project_id": pid}},
		{"/api/v1/tables/create", map[string]any{"project_id": pid, "rows": []any{}, "row_digests": []any{}}},
		{"/api/v1/tables/stats", map[string]any{"project_id": pid}},
	}
	for _, c := range cases {
		if code, out := e.raw(c.path, c.body, nil); code != 400 {
			t.Errorf("%s %v -> %d %s, want 400", c.path, c.body, code, out)
		}
	}
	for _, path := range []string{"/api/v1/objects/missing?project_id=" + pid, "/api/v1/spans/missing?project_id=" + pid,
		"/api/v1/objects/missing/versions/v3?project_id=" + pid, "/api/v1/files/abc?project_id=" + pid} {
		if code, _ := e.raw(path, nil, nil); code != 404 {
			t.Errorf("GET %s -> %d, want 404", path, code)
		}
	}
}

func TestKeyOrderPreserved(t *testing.T) {
	e := newEnv(t)
	code, out := e.raw("/api/v1/tables/create", []byte(`{"project_id":"`+pid+`","rows":[{"question":"q","expected":"a","b":1}]}`), nil)
	if code != 200 {
		t.Fatal(string(out))
	}
	var tc map[string]any
	_ = json.Unmarshal(out, &tc)
	_, rows := e.raw("/api/v1/tables/query", map[string]any{"project_id": pid, "digest": tc["digest"]}, nil)
	if !strings.Contains(string(rows), `"value":{"question":"q","expected":"a","b":1}`) {
		t.Fatalf("row order lost: %s", rows)
	}
	code, out = e.raw("/api/v1/objects/save", []byte(`{"project_id":"`+pid+`","name":"o","value":{"zeta":1,"alpha":{"y":2,"x":3}}}`), nil)
	if code != 200 {
		t.Fatal(string(out))
	}
	_, obj := e.raw("/api/v1/objects/o?project_id="+pid, nil, nil)
	if !strings.Contains(string(obj), `"value":{"zeta":1,"alpha":{"y":2,"x":3}}`) {
		t.Fatalf("object order lost: %s", obj)
	}
	// Same content in another order dedupes to the same digest.
	_, out2 := e.raw("/api/v1/objects/save", []byte(`{"project_id":"`+pid+`","name":"o","value":{"alpha":{"x":3,"y":2},"zeta":1}}`), nil)
	if !strings.Contains(string(out2), `"version":0`) {
		t.Fatalf("reordered content should dedupe: %s", out2)
	}
}

func TestNamedKeysAndWhoami(t *testing.T) {
	e := newEnv(t, "alice=k1", "plainkey")
	e.key = "k1"
	_, out := e.raw("/auth/whoami", nil, nil)
	if !strings.Contains(string(out), `"user_id":"alice"`) {
		t.Fatalf("whoami = %s", out)
	}
	fb := e.post("/api/v1/feedback/create", map[string]any{"project_id": pid, "ref": spanRef("x"),
		"feedback_type": "weft.note", "payload": map[string]any{"note": "hi"}})
	if fb["user_id"] != "alice" {
		t.Fatalf("feedback user = %v", fb["user_id"])
	}
	e.key = "plainkey"
	_, out = e.raw("/auth/whoami", nil, nil)
	if !strings.Contains(string(out), `"user_id":"anonymous"`) {
		t.Fatalf("whoami plain = %s", out)
	}
	e.key = "alice"
	if code, _ := e.raw("/auth/whoami", nil, nil); code != 401 {
		t.Fatal("user name must not work as a key")
	}
	e.key = "k1"
	st := e.post("/api/v1/spans/stats", map[string]any{"project_id": pid, "search": "nothing-matches"})
	if st["totals"].(map[string]any)["count"].(float64) != 0 {
		t.Fatalf("stats search ignored: %v", st)
	}
}

func TestScoreCallsOnDemand(t *testing.T) {
	e := newEnv(t)
	sc := publish(e, "has-paris", map[string]any{"_class_name": "ContainsScorer", "_bases": []any{"Scorer", "Object", "BaseModel"},
		"target_column": "expected"})
	e.spanBatch(completeSpan(map[string]any{"id": "s1", "name": "qa", "started_at": "2026-01-01T00:00:00Z",
		"inputs": map[string]any{"expected": "Paris"}, "ended_at": "2026-01-01T00:00:01Z", "output": "It is Paris."}))
	res := e.post("/api/v1/spans/score", map[string]any{"project_id": pid, "ids": []any{"s1"}, "scorer_refs": []any{sc}})
	r0 := res["items"].([]any)[0].(map[string]any)
	if r0["output"] != true || r0["feedback_id"] == "" || r0["span_id"] != "s1" || r0["scorer_span_id"] == nil {
		t.Fatalf("score result = %v", r0)
	}
	c := e.span("s1", "feedback")
	fb := c["feedback"].([]any)
	if len(fb) != 1 || fb[0].(map[string]any)["feedback_type"] != "weft.scorer.has-paris" || fb[0].(map[string]any)["span_ref"] == nil {
		t.Fatalf("feedback = %v", fb)
	}
	if fb[0].(map[string]any)["ref"] != spanRef("s1") {
		t.Fatalf("feedback ref = %v", fb[0].(map[string]any)["ref"])
	}
	if code, _ := e.raw("/api/v1/spans/score", map[string]any{"project_id": pid, "ids": []any{"s1"}, "scorer_refs": []any{"weft:///" + pid + "/object/nope:x"}}, nil); code != 400 {
		t.Fatalf("unknown scorer should be 400, got %d", code)
	}
}
