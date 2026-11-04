package compat_test

import (
	"bytes"
	"encoding/json"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/agentshield-ai/agentshield/internal/weft/compat"
	"github.com/agentshield-ai/agentshield/internal/weft/compat/legacy"
	"github.com/agentshield-ai/agentshield/internal/weft/refs"
	"github.com/agentshield-ai/agentshield/internal/weft/server"
	"github.com/agentshield-ai/agentshield/internal/weft/store"
	"github.com/go-chi/chi/v5"
)

const pid = "acme/evals"

type env struct {
	t   *testing.T
	srv *httptest.Server
	key string
}

// newEnv builds a server with the adapter on or off; with it off, the older
// route shapes are off too.
func newEnv(t *testing.T, compatOn bool, keys ...string) *env {
	t.Helper()
	e, _ := newEnvServer(t, compatOn, keys...)
	return e
}

func newEnvServer(t *testing.T, compatOn bool, keys ...string) (*env, *server.Server) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "weft.db"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := server.Config{APIKeys: keys, Compat: compatOn, UI: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ui"))
	})}
	s := server.New(cfg, st)
	hs := httptest.NewServer(s.Handler())
	t.Cleanup(func() {
		hs.Close()
		s.Close()
		_ = st.Close()
	})
	return &env{t: t, srv: hs}, s
}

func (e *env) raw(method, path string, body any, headers map[string]string) (int, []byte, http.Header) {
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
	req, _ := http.NewRequest(method, e.srv.URL+path, rdr)
	req.Header.Set("Content-Type", "application/json")
	if e.key != "" {
		req.Header.Set("Authorization", "Bearer "+e.key)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	cl := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := cl.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out, resp.Header
}

func (e *env) post(path string, body any, headers map[string]string) map[string]any {
	e.t.Helper()
	code, out, _ := e.raw(http.MethodPost, path, body, headers)
	if code != 200 {
		e.t.Fatalf("%s -> %d: %s", path, code, out)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		e.t.Fatalf("%s: %v: %s", path, err, out)
	}
	return m
}

var weaveUA = map[string]string{"User-Agent": "WeaveTrace/Python 0.54.0"}

// routeSet lists a handler's routes as "METHOD pattern".
func routeSet(t *testing.T, h http.Handler) map[string]bool {
	t.Helper()
	rs, ok := h.(chi.Routes)
	if !ok {
		t.Fatalf("handler %T is not a chi router", h)
	}
	out := map[string]bool{}
	if err := chi.Walk(rs, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		out[method+" "+route] = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

var paramRe = regexp.MustCompile(`\{[^}]+\}`)

// With the adapter off (and the older routes off), every route that only
// the adapter serves returns 404, and legacy names get no special treatment.
func TestOffByDefault(t *testing.T) {
	e, off := newEnvServer(t, false)
	_, on := newEnvServer(t, true)
	offRoutes, onRoutes := routeSet(t, off.Handler()), routeSet(t, on.Handler())
	var adapterOnly []string
	for r := range onRoutes {
		if !offRoutes[r] {
			adapterOnly = append(adapterOnly, r)
		}
	}
	sort.Strings(adapterOnly)
	// The older route shapes are all among them.
	for _, want := range []string{"POST /call/start", "POST /call/upsert_batch", "POST /calls/stream_query", "POST /obj/create",
		"POST /objs/query", "POST /table/create", "POST /refs/read_batch", "POST /file/create", "GET /file/content",
		"POST /files/resolve_url", "POST /feedback/create", "POST /cost/query", "POST /threads/query", "POST /trace/usage",
		"POST /completions/create", "POST /evaluate_model", "POST /evaluation_status", "POST /evaluations/compare",
		"POST /leaderboard/compute", "GET /server_info", "GET /version", "GET /geolocate", "GET /inference/catalog/models",
		"GET /tags", "GET /aliases", "POST /link_to_registry", "POST /registry/query", "POST /dataset_sources/query",
		"POST /embeddings/create", "POST /image/create", "POST /project/stats", "POST /calls/metrics", "POST /calls/export",
		"PUT /v2/{entity}/{project}/runtimes/{runtime_name}", "POST /v2/{entity}/{project}/datasets",
		"POST /v1/entities/{entity}/projects/{project}:export", "POST /graphql", "POST /service/projects_info",
		"POST /v2/{entity}/{project}/calls/complete"} {
		if !onRoutes[want] || offRoutes[want] {
			t.Errorf("%s: on=%v off=%v; want only with the adapter", want, onRoutes[want], offRoutes[want])
		}
	}
	if len(adapterOnly) < 90 {
		t.Fatalf("only %d adapter routes: %v", len(adapterOnly), adapterOnly)
	}
	for _, r := range adapterOnly {
		method, pattern, _ := strings.Cut(r, " ")
		path := paramRe.ReplaceAllString(strings.TrimSuffix(pattern, "/*"), "x")
		if strings.HasSuffix(pattern, "/*") {
			path += "/y"
		}
		code, out, _ := e.raw(method, path, map[string]any{"project_id": pid}, nil)
		// GET falls through to the UI for paths the server does not know;
		// the UI's own pages show "not found" for them.
		if method == http.MethodGet && code == 200 && string(out) == "ui" {
			continue
		}
		if code != http.StatusNotFound && code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s with the adapter off -> %d %s, want 404", method, path, code, out)
		}
	}
	// None of Weft's own routes is an adapter route.
	for r := range offRoutes {
		if strings.Contains(r, "/api/v1/") && !onRoutes[r] {
			t.Errorf("native route %s missing with the adapter on", r)
		}
	}
	_, si, _ := e.raw(http.MethodGet, "/api/v1/server", nil, nil)
	if strings.Contains(string(si), "weave") || strings.Contains(string(si), "compat") {
		t.Fatalf("server description names the adapter while it is off: %s", si)
	}
	// /e/p/weave/... is an ordinary UI path (the UI shows its not-found page).
	if code, out, _ := e.raw(http.MethodGet, "/acme/evals/weave/calls/x", nil, nil); code != 200 || string(out) != "ui" {
		t.Fatalf("weave link with the adapter off -> %d %s", code, out)
	}
	// Legacy refs are not refs, and the legacy field name is unknown.
	for _, body := range []map[string]any{
		{"project_id": pid, "weave_ref": "weave:///" + pid + "/call/c1", "feedback_type": "weft.note", "payload": map[string]any{"note": "x"}},
		{"project_id": pid, "ref": "weave:///" + pid + "/call/c1", "feedback_type": "weft.note", "payload": map[string]any{"note": "x"}},
	} {
		if code, out, _ := e.raw(http.MethodPost, "/api/v1/feedback/create", body, nil); code != 400 {
			t.Fatalf("legacy feedback with the adapter off -> %d %s", code, out)
		}
	}
	// Responses keep Weft's names even for the weave client's user agent.
	now := time.Now().UTC().Format(time.RFC3339Nano)
	e.post("/api/v1/spans/start", map[string]any{"project_id": pid, "id": "c1", "name": "op", "started_at": now}, nil)
	e.post("/api/v1/feedback/create", map[string]any{"project_id": pid, "ref": "weft:///" + pid + "/span/c1", "feedback_type": "weft.note",
		"payload": map[string]any{"note": "x"}}, nil)
	_, body, _ := e.raw(http.MethodPost, "/api/v1/spans/query", map[string]any{"project_id": pid, "include_feedback": true}, weaveUA)
	if strings.Contains(string(body), "weave:///") || !strings.Contains(string(body), `"weft":{`) ||
		!strings.Contains(string(body), `"weft:///`+pid+`/span/c1"`) || strings.Contains(string(body), "wandb.") {
		t.Fatalf("adapter-off response rewritten: %s", body)
	}
}

func start(t *testing.T, e *env, id string, attrs map[string]any) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	e.post("/call/start", map[string]any{"start": map[string]any{"project_id": pid, "id": id, "trace_id": "t-" + id, "op_name": "op",
		"started_at": now, "inputs": map[string]any{}, "attributes": attrs}}, nil)
	e.post("/call/end", map[string]any{"end": map[string]any{"project_id": pid, "id": id, "ended_at": now, "output": 1, "summary": map[string]any{}}}, nil)
}

func TestGraphQLShim(t *testing.T) {
	e := newEnv(t, true, "bob=key1")
	e.key = "key1"
	code, out, _ := e.raw(http.MethodPost, "/graphql", map[string]any{"query": "query DefaultEntity { viewer { username defaultEntity { name } } }"}, nil)
	if code != 200 || !strings.Contains(string(out), `"defaultEntity":{"name":"bob"}`) {
		t.Fatalf("viewer: %d %s", code, out)
	}
	code, out, _ = e.raw(http.MethodPost, "/graphql", map[string]any{
		"query":     "mutation UpsertModel($name: String!, $entity: String!) { upsertModel(input: {name: $name, entityName: $entity}) { model { name } } }",
		"variables": map[string]any{"name": "proj", "entity": "bob"},
	}, nil)
	if code != 200 || !strings.Contains(string(out), `"name":"proj"`) {
		t.Fatalf("upsertModel: %d %s", code, out)
	}
	_, plist, _ := e.raw(http.MethodGet, "/projects", nil, nil)
	if !strings.Contains(string(plist), `"project_id":"bob/proj"`) {
		t.Fatalf("project not created: %s", plist)
	}
	_, out, _ = e.raw(http.MethodPost, "/graphql", map[string]any{"query": "query { somethingElse }"}, nil)
	if !strings.Contains(string(out), `"errors"`) {
		t.Fatalf("unsupported operation should return a GraphQL error: %s", out)
	}
	// The client's API key header.
	e.key = ""
	if code, out, _ := e.raw(http.MethodGet, "/projects", nil, map[string]string{"Wandb-Api-Key": "key1"}); code != 200 {
		t.Fatalf("Wandb-Api-Key -> %d %s", code, out)
	}
}

func TestCallsCompleteCumulativeSummaries(t *testing.T) {
	e := newEnv(t, true)
	usage := func(p int) map[string]any {
		return map[string]any{"usage": map[string]any{"gpt-4o": map[string]any{"requests": 1, "prompt_tokens": p, "completion_tokens": 1}},
			"status_counts": map[string]any{"success": 1, "error": 0}}
	}
	// The client sends children first; parents carry cumulative summaries.
	child := map[string]any{"project_id": pid, "id": "k", "trace_id": "t", "parent_id": "r", "op_name": "weave:///" + pid + "/op/llm:abc",
		"started_at": "2026-01-01T00:00:00.1Z", "ended_at": "2026-01-01T00:00:00.5Z", "inputs": map[string]any{}, "attributes": map[string]any{},
		"output": "x", "summary": usage(10), "wb_run_id": "run-1", "wb_run_step": 11, "wb_run_step_end": 12}
	rootSummary := map[string]any{"usage": map[string]any{"gpt-4o": map[string]any{"requests": 1, "prompt_tokens": 10, "completion_tokens": 1}},
		"status_counts": map[string]any{"success": 2, "error": 0}}
	root := map[string]any{"project_id": pid, "id": "r", "trace_id": "t", "op_name": "weave:///" + pid + "/op/agent:def",
		"started_at": "2026-01-01T00:00:00Z", "ended_at": "2026-01-01T00:00:01Z", "inputs": map[string]any{"q": "weave:///" + pid + "/object/ds:d1"},
		"attributes": map[string]any{}, "output": "y", "summary": rootSummary}
	code, out, _ := e.raw(http.MethodPost, "/v2/acme/evals/calls/complete", map[string]any{"batch": []any{child, root}}, weaveUA)
	if code != 200 {
		t.Fatalf("calls/complete: %d %s", code, out)
	}
	// Read back as the weave client: weave:/// refs, usage not doubled.
	_, body, _ := e.raw(http.MethodPost, "/call/read", map[string]any{"project_id": pid, "id": "r"}, map[string]string{"X-Stainless-Lang": "python"})
	s := string(body)
	if strings.Contains(s, "weft:///") || !strings.Contains(s, `"q":"weave:///`+pid+`/object/ds:d1"`) {
		t.Fatalf("the weave client should see weave:/// refs: %s", s)
	}
	if !strings.Contains(s, `"prompt_tokens":10`) {
		t.Fatalf("cumulative usage double counted or lost: %s", s)
	}
	// Weft clients see weft:/// refs.
	r := e.post("/call/read", map[string]any{"project_id": pid, "id": "r"}, nil)["call"].(map[string]any)
	if r["inputs"].(map[string]any)["q"] != "weft:///"+pid+"/object/ds:d1" {
		t.Fatalf("weft client ref = %v", r["inputs"])
	}
	if sc := r["summary"].(map[string]any)["status_counts"].(map[string]any); sc["success"].(float64) != 1 {
		t.Fatalf("descendant status counts = %v", sc)
	}
	k := e.post("/call/read", map[string]any{"project_id": pid, "id": "k"}, nil)["call"].(map[string]any)
	if k["run_step"] != float64(11) || k["run_step_end"] != float64(12) || k["run_id"] != "run-1" || k["wb_run_id"] != nil {
		t.Fatalf("calls/complete steps = %v %v", k["run_step"], k["run_step_end"])
	}
	// The weave client sees its own field names, outside user data.
	_, body, _ = e.raw(http.MethodPost, "/call/read", map[string]any{"project_id": pid, "id": "k"}, weaveUA)
	var wk map[string]any
	_ = json.Unmarshal(body, &wk)
	wc := wk["call"].(map[string]any)
	if wc["wb_run_step"] != float64(11) || wc["wb_run_step_end"] != float64(12) || wc["wb_run_id"] != "run-1" || wc["run_id"] != nil {
		t.Fatalf("weave client call fields = %s", body)
	}
	// Its filters and query fields use the old names too.
	_, body, _ = e.raw(http.MethodPost, "/calls/stream_query", map[string]any{"project_id": pid,
		"filter":  map[string]any{"wb_run_ids": []any{"run-1"}},
		"query":   map[string]any{"$expr": map[string]any{"$eq": []any{map[string]any{"$getField": "wb_run_id"}, map[string]any{"$literal": "run-1"}}}},
		"sort_by": []any{map[string]any{"field": "wb_user_id", "direction": "asc"}}}, weaveUA)
	if lines := strings.Split(strings.TrimSpace(string(body)), "\n"); len(lines) != 1 || !strings.Contains(lines[0], `"wb_run_step":11`) {
		t.Fatalf("weave client calls query = %s", body)
	}
	_, info, _ := e.raw(http.MethodPost, "/service/projects_info", map[string]any{"project_ids": []any{pid}}, nil)
	if !strings.Contains(string(info), `"internal_project_id":"`+pid+`"`) {
		t.Fatalf("projects_info = %s", info)
	}
	_, si, _ := e.raw(http.MethodGet, "/server_info", nil, nil)
	if !strings.Contains(string(si), "min_required_weave_python_version") {
		t.Fatalf("server_info lacks the client's fields: %s", si)
	}
}

// Feedback in the client's names is stored in Weft's, and read back in the
// client's by the client only.
func TestFeedbackNames(t *testing.T) {
	e := newEnv(t, true)
	start(t, e, "c1", nil)
	ref := "weave:///" + pid + "/call/c1"
	e.post("/feedback/create", map[string]any{"project_id": pid, "weave_ref": ref, "feedback_type": "wandb.reaction.1",
		"payload": map[string]any{"emoji": "👍"}}, weaveUA)
	e.post("/feedback/create", map[string]any{"project_id": pid, "weave_ref": ref, "feedback_type": "wandb.runnable.judge",
		"payload": map[string]any{"output": map[string]any{"ok": true}}}, weaveUA)

	weft := e.post("/feedback/query", map[string]any{"project_id": pid}, nil)["result"].([]any)
	types := map[string]bool{}
	for _, f := range weft {
		m := f.(map[string]any)
		types[m["feedback_type"].(string)] = true
		if m["ref"] != "weft:///"+pid+"/call/c1" {
			t.Fatalf("stored ref = %v", m)
		}
		if _, ok := m["weave_ref"]; ok {
			t.Fatalf("Weft clients get the legacy field: %v", m)
		}
	}
	if !types["weft.reaction"] || !types["weft.scorer.judge"] {
		t.Fatalf("stored types = %v", types)
	}
	// The weave client queries by its own field name and gets its names back.
	code, out, _ := e.raw(http.MethodPost, "/feedback/query", map[string]any{"project_id": pid,
		"query": map[string]any{"$expr": map[string]any{"$eq": []any{map[string]any{"$getField": "weave_ref"}, map[string]any{"$literal": ref}}}}}, weaveUA)
	s := string(out)
	if code != 200 || !strings.Contains(s, `"weave_ref":"`+ref+`"`) || !strings.Contains(s, `"feedback_type":"wandb.reaction.1"`) ||
		!strings.Contains(s, `"feedback_type":"wandb.runnable.judge"`) || strings.Contains(s, "weft") {
		t.Fatalf("weave client feedback = %d %s", code, s)
	}
	// Call queries by the legacy feedback field path still work.
	q := e.post("/calls/query", map[string]any{"project_id": pid, "query": map[string]any{"$expr": map[string]any{"$eq": []any{
		map[string]any{"$getField": "feedback.wandb.reaction.1.emoji"}, map[string]any{"$literal": "👍"}}}}}, nil)
	if len(q["calls"].([]any)) != 1 {
		t.Fatalf("legacy feedback field query = %v", q)
	}
}

// Legacy attribute keys are renamed on ingest, over JSON and OTLP.
func TestAttributeAliases(t *testing.T) {
	e := newEnv(t, true)
	start(t, e, "sdk-llm", map[string]any{"weave": map[string]any{"kind": "llm", "client_version": "0.54.0",
		"parent_call": map[string]any{"trace_id": "op-trace-2", "id": "op-call-2"}}})
	c := e.post("/call/read", map[string]any{"project_id": pid, "id": "sdk-llm"}, nil)["call"].(map[string]any)
	a := c["attributes"].(map[string]any)
	w, _ := a["weft"].(map[string]any)
	if w["span_kind"] != "llm" || w["parent_span"].(map[string]any)["id"] != "op-call-2" || a["weave"].(map[string]any)["client_version"] != "0.54.0" {
		t.Fatalf("attributes = %v", a)
	}
	// The agents API takes the legacy field name in queries, and answers the
	// weave client with its span field names.
	q := map[string]any{"project_id": pid,
		"query": map[string]any{"$expr": map[string]any{"$eq": []any{map[string]any{"$getField": "weave.parent_call.id"}, map[string]any{"$literal": "op-call-2"}}}}}
	res := e.post("/agents/spans/query", q, nil)
	if s := res["spans"].([]any); len(s) != 1 || s[0].(map[string]any)["linked_parent_trace_id"] != "op-trace-2" {
		t.Fatalf("spans = %v", res)
	}
	code, raw, _ := e.raw(http.MethodPost, "/agents/spans/query", q, weaveUA)
	if code != 200 || !strings.Contains(string(raw), `"parent_call_trace_id":"op-trace-2"`) || strings.Contains(string(raw), "linked_parent") {
		t.Fatalf("weave client spans = %d %s", code, raw)
	}

	// OTLP: a wandb.project resource and weave.* span attributes.
	body := []byte(`{"resourceSpans":[{"resource":{"attributes":[{"key":"wandb.entity","value":{"stringValue":"acme"}},{"key":"wandb.project","value":{"stringValue":"evals"}}]},
	 "scopeSpans":[{"scope":{"name":"x"},"spans":[{"traceId":"5b8efff798038103d269b633813fc60c","spanId":"eee19b7ec3c1b174",
	  "name":"turn","kind":1,"startTimeUnixNano":"1700000000000000000","endTimeUnixNano":"1700000000500000000",
	  "attributes":[{"key":"wandb.thread_id","value":{"stringValue":"th-1"}},{"key":"wandb.is_turn","value":{"boolValue":true}},
	                {"key":"weave.eval.run_id","value":{"stringValue":"run-1"}},
	                {"key":"weave.eval.predict_and_score_call_id","value":{"stringValue":"pas-1"}}]}]}]}]}`)
	code, out, _ := e.raw(http.MethodPost, "/otel/v1/traces", body, nil)
	if code != 200 {
		t.Fatalf("otlp -> %d %s", code, out)
	}
	c = e.post("/call/read", map[string]any{"project_id": pid, "id": "eee19b7ec3c1b174"}, nil)["call"].(map[string]any)
	if c["thread_id"] != "th-1" || c["turn_id"] != "eee19b7ec3c1b174" {
		t.Fatalf("otlp call = %v", c)
	}
	oa := c["attributes"].(map[string]any)["otel"].(map[string]any)["attributes"].(map[string]any)
	if oa["weft.eval.run_id"] != "run-1" || oa["weave.eval.run_id"] != nil || oa["weft.session_id"] != "th-1" ||
		oa["weft.eval.prediction_span_id"] != "pas-1" || oa["weft.eval.predict_and_score_call_id"] != nil {
		t.Fatalf("otel attributes = %v", oa)
	}
}

// verl with its weave tracing backend records rollouts through the official
// client: create_call(op=<qualname>, inputs, attributes={step, ...}) and
// finish_call(output). The calls are stored as sent and filterable by those
// attributes.
func TestVerlRollouts(t *testing.T) {
	e := newEnv(t, true)
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	var batch []any
	for i, step := range []int{3, 3, 4} {
		id := []string{"r1", "r2", "r3"}[i]
		attrs := map[string]any{"step": step, "sample_index": i, "rollout_n": 0, "validate": false, "experiment_name": "gsm8k-grpo",
			"weave": map[string]any{"client_version": "0.54.0", "source": "python-sdk"}}
		batch = append(batch,
			map[string]any{"mode": "start", "req": map[string]any{"start": map[string]any{"project_id": pid, "id": id, "trace_id": id,
				"op_name": "ToolAgentLoop.run", "started_at": ts, "attributes": attrs,
				"inputs": map[string]any{"sampling_params": map[string]any{"temperature": 1.0}, "raw_prompt": []any{map[string]any{"role": "user", "content": "2+2?"}}}}}},
			map[string]any{"mode": "end", "req": map[string]any{"end": map[string]any{"project_id": pid, "id": id, "ended_at": ts,
				"output":  map[string]any{"prompt_ids": []any{1, 2}, "response_ids": []any{3}, "num_turns": 2, "prompt_text": "2+2?", "response_text": "4"},
				"summary": map[string]any{}}}})
	}
	e.post("/call/upsert_batch", map[string]any{"batch": batch}, weaveUA)
	res := e.post("/calls/query", map[string]any{"project_id": pid, "query": map[string]any{"$expr": map[string]any{"$and": []any{
		map[string]any{"$eq": []any{map[string]any{"$getField": "attributes.step"}, map[string]any{"$literal": 3}}},
		map[string]any{"$eq": []any{map[string]any{"$getField": "attributes.experiment_name"}, map[string]any{"$literal": "gsm8k-grpo"}}},
	}}}}, nil)
	calls := res["calls"].([]any)
	if len(calls) != 2 {
		t.Fatalf("step 3 calls = %d", len(calls))
	}
	c := calls[0].(map[string]any)
	if c["op_name"] != "ToolAgentLoop.run" || c["output"].(map[string]any)["response_text"] != "4" {
		t.Fatalf("call = %v", c)
	}
}

// Ops serialised by the client are classified as ops.
func TestClientOps(t *testing.T) {
	e := newEnv(t, true)
	e.post("/obj/create", map[string]any{"obj": map[string]any{"project_id": pid, "object_id": "add",
		"val": map[string]any{"_type": "CustomWeaveType", "weave_type": map[string]any{"type": "Op"}, "files": map[string]any{"obj.py": "d1"}}}}, weaveUA)
	o := e.post("/obj/read", map[string]any{"project_id": pid, "object_id": "add", "digest": "latest"}, nil)["obj"].(map[string]any)
	if o["kind"] != "op" {
		t.Fatalf("kind = %v", o["kind"])
	}
}

func TestLinkRedirects(t *testing.T) {
	e := newEnv(t, true)
	for path, want := range map[string]string{
		"/acme/evals/weave":                             "/acme/evals/traces",
		"/acme/evals/weave/calls/0193?x=1":              "/acme/evals/calls/0193?x=1",
		"/acme/evals/weave/agents/conversations/abc-12": "/acme/evals/conversations/abc-12",
		"/acme/evals/weave/boards/x":                    "/acme/evals/traces",
		"/acme/evals/r/call/0193":                       "/acme/evals/calls/0193",
	} {
		code, _, h := e.raw(http.MethodGet, path, nil, nil)
		if code != http.StatusFound || h.Get("Location") != want {
			t.Fatalf("%s -> %d %q, want %q", path, code, h.Get("Location"), want)
		}
	}
	if got := compat.LinkTarget("e", "p", "agents/"); got != "/e/p/agents" {
		t.Fatalf("agents/ -> %q", got)
	}
}

func TestEnvAliases(t *testing.T) {
	fs := flag.NewFlagSet("weft", flag.ContinueOnError)
	insecure := fs.Bool("remote-scorer-allow-insecure-http", false, "")
	on := fs.Bool(compat.FlagName, false, "")
	if err := fs.Parse([]string{"--" + compat.FlagName}); err != nil {
		t.Fatal(err)
	}
	if !compat.Enabled(fs, *on) {
		t.Fatal("flag not honoured")
	}
	env := map[string]string{"WF_SCORING_WORKER_REMOTE_SCORER_ALLOW_INSECURE_HTTP": "true"}
	got := compat.ApplyEnvAliases(fs, func(k string) string { return env[k] })
	if !*insecure || len(got) != 1 {
		t.Fatalf("alias not applied: %v %v", *insecure, got)
	}
	// Weft's own variable wins.
	*insecure = false
	env["WEFT_REMOTE_SCORER_ALLOW_INSECURE_HTTP"] = "false"
	if got := compat.ApplyEnvAliases(fs, func(k string) string { return env[k] }); len(got) != 0 || *insecure {
		t.Fatalf("alias overrode WEFT_*: %v", got)
	}
	t.Setenv(compat.EnvName, "true")
	fs2 := flag.NewFlagSet("weft", flag.ContinueOnError)
	off := fs2.Bool(compat.FlagName, false, "")
	_ = fs2.Parse(nil)
	if !compat.Enabled(fs2, *off) {
		t.Fatal("env not honoured")
	}
}

// The adapter computes digests in the client's form (the client computes
// them itself and uses them in refs before the server answers), and names
// function and span refs with the client's kinds, in its ref scheme.
func TestAdapterDigestsAndRefKinds(t *testing.T) {
	e := newEnv(t, true)
	val := map[string]any{"_type": "Op", "name": "f", "code": "def f(): pass"}
	raw, _ := json.Marshal(val)
	dv, _ := refs.Decode(raw)
	want, _ := legacy.Digester.Value(dv)
	got := e.post("/obj/create", map[string]any{"obj": map[string]any{"project_id": pid, "object_id": "f", "val": val}}, weaveUA)
	if got["digest"] != want {
		t.Fatalf("adapter digest = %v, want the client's %s", got["digest"], want)
	}
	tbl := e.post("/table/create", map[string]any{"table": map[string]any{"project_id": pid, "rows": []any{map[string]any{"a": 1}}}}, weaveUA)
	rowRaw, _ := refs.Decode([]byte(`{"a": 1}`))
	row, _ := legacy.Digester.Value(rowRaw)
	if tbl["row_digests"].([]any)[0] != row || tbl["digest"] != legacy.TableDigest([]string{row}) {
		t.Fatalf("adapter table = %v", tbl)
	}
	// Weft's own API writes Weft's digests, and reads the client's.
	native := e.post("/api/v1/objects/save", map[string]any{"project_id": pid, "name": "f", "value": val}, nil)
	if wd, _, _ := refs.Digest(dv); native["digest"] != wd || native["ref"] != "weft:///"+pid+"/function/f:"+wd {
		t.Fatalf("native save = %v", native)
	}
	vals := e.post("/api/v1/refs/read", map[string]any{"refs": []any{"weft:///" + pid + "/op/f:" + want}}, nil)["items"].([]any)
	if vals[0] == nil {
		t.Fatalf("client digest does not resolve: %v", vals)
	}
	// A span whose name is a function ref reaches the client as an op ref.
	now := time.Now().UTC().Format(time.RFC3339Nano)
	e.post("/api/v1/spans/start", map[string]any{"project_id": pid, "id": "k1", "name": native["ref"], "started_at": now}, nil)
	e.post("/api/v1/feedback/create", map[string]any{"project_id": pid, "ref": "weft:///" + pid + "/span/k1", "feedback_type": "weft.note",
		"payload": map[string]any{"note": "n"}}, nil)
	_, body, _ := e.raw(http.MethodPost, "/call/read", map[string]any{"project_id": pid, "id": "k1", "include_feedback": true}, weaveUA)
	for _, s := range []string{`"op_name":"weave:///` + pid + `/op/f:`, `"weave_ref":"weave:///` + pid + `/call/k1"`} {
		if !strings.Contains(string(body), s) {
			t.Fatalf("client response lacks %s: %s", s, body)
		}
	}
	if strings.Contains(string(body), "/function/") || strings.Contains(string(body), "/span/") {
		t.Fatalf("client response has Weft's ref kinds: %s", body)
	}
}

// Weft names spans its own way in fields its client also uses (metric
// buckets, dataset lineage kinds, the scorer span of feedback); the adapter
// keeps the client's names on its routes.
func TestAdapterKeepsClientSpanNames(t *testing.T) {
	e := newEnv(t, true)
	e.post("/call/start", map[string]any{"start": map[string]any{"project_id": pid, "id": "c1", "trace_id": "t1", "op_name": "f",
		"started_at": "2026-01-01T10:00:00Z", "inputs": map[string]any{}, "attributes": map[string]any{}}}, weaveUA)
	e.post("/call/end", map[string]any{"end": map[string]any{"project_id": pid, "id": "c1", "ended_at": "2026-01-01T10:00:01Z",
		"output": 1, "summary": map[string]any{}}}, weaveUA)

	st := e.post("/calls/stats", map[string]any{"project_id": pid, "start": "2026-01-01T10:00:00Z", "end": "2026-01-01T11:00:00Z",
		"granularity": 3600, "call_metrics": []any{map[string]any{"metric": "call_count"}}}, weaveUA)
	cb, ok := st["call_buckets"].([]any)
	if !ok || len(cb) != 1 || cb[0].(map[string]any)["sum_call_count"].(float64) != 1 || st["span_buckets"] != nil {
		t.Fatalf("calls/stats = %v", st)
	}
	if code, out, _ := e.raw(http.MethodPost, "/api/v1/spans/stats", map[string]any{"project_id": pid, "start": "2026-01-01T10:00:00Z",
		"span_metrics": []any{map[string]any{"metric": "span_count"}}}, nil); code != 200 || !strings.Contains(string(out), `"sum_span_count":1`) {
		t.Fatalf("spans/stats -> %d %s", code, out)
	}

	tbl := e.post("/table/create", map[string]any{"table": map[string]any{"project_id": pid, "rows": []any{map[string]any{"q": 1}}}}, weaveUA)
	obj := e.post("/obj/create", map[string]any{"obj": map[string]any{"project_id": pid, "object_id": "ds",
		"val": map[string]any{"_type": "Dataset", "_class_name": "Dataset", "rows": "weave:///" + pid + "/table/" + tbl["digest"].(string)}}}, weaveUA)
	row := tbl["row_digests"].([]any)[0]
	e.post("/dataset_sources/link", map[string]any{"project_id": pid, "dataset_object_id": "ds", "dataset_digest": obj["digest"],
		"links": []any{map[string]any{"row_digest": row, "sources": []any{map[string]any{"source_id": "c1", "source_kind": "call", "source_trace_id": "t1"}}}}}, weaveUA)
	q := e.post("/dataset_sources/query", map[string]any{"project_id": pid, "dataset_object_id": "ds", "source_kinds": []any{"call"}}, weaveUA)
	if l := q["links"].([]any); len(l) != 1 || l[0].(map[string]any)["source_kind"] != "call" || l[0].(map[string]any)["source_display_name"] == "" {
		t.Fatalf("dataset_sources/query = %v", q)
	}
	nq := e.post("/api/v1/dataset_sources/query", map[string]any{"project_id": pid, "dataset": "ds"}, nil)
	if l := nq["items"].([]any); len(l) != 1 || l[0].(map[string]any)["source_kind"] != "span" {
		t.Fatalf("native query = %v", nq)
	}
	m := e.post("/dataset_sources/source_datasets_query", map[string]any{"project_id": pid,
		"sources": []any{map[string]any{"source_id": "c1", "source_kind": "call"}}}, weaveUA)
	if ms := m["memberships"].([]any); len(ms) != 1 || ms[0].(map[string]any)["source_kind"] != "call" {
		t.Fatalf("source_datasets_query = %v", m)
	}

	e.post("/feedback/batch/create", map[string]any{"batch": []any{map[string]any{"project_id": pid, "weave_ref": "weave:///" + pid + "/call/c1",
		"feedback_type": "wandb.runnable.judge", "payload": map[string]any{"output": 1}, "call_ref": "weave:///" + pid + "/call/judge-1"}}}, weaveUA)
	fq := e.post("/api/v1/feedback/query", map[string]any{"project_id": pid}, nil)
	if items := fq["items"].([]any); len(items) != 1 || items[0].(map[string]any)["span_ref"] != "weft:///"+pid+"/span/judge-1" {
		t.Fatalf("feedback = %v", fq)
	}
}
