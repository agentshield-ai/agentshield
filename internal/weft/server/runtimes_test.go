package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// doJSON sends any method with a JSON body and decodes a JSON object response.
func (e *testEnv) api2Do(method, path string, body any) (int, map[string]any) {
	e.t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, rdr)
	req.Header.Set("Content-Type", "application/json")
	if e.key != "" {
		req.Header.Set("Authorization", "Bearer "+e.key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	if m == nil {
		m = map[string]any{"__raw": string(raw)}
	}
	return resp.StatusCode, m
}

// modelServer is an httptest model endpoint serving both a predict API
// (POST /predict) and an OpenAI-compatible /v1/chat/completions.
type modelServer struct {
	mu       sync.Mutex
	predicts []map[string]any
	chats    []map[string]any
	auth     []string
}

func newModelServer(t *testing.T) (*modelServer, *httptest.Server) {
	m := &modelServer{}
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		m.mu.Lock()
		m.auth = append(m.auth, r.Header.Get("Authorization")+"|"+r.Header.Get("X-Team"))
		m.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/predict":
			m.mu.Lock()
			m.predicts = append(m.predicts, body)
			m.mu.Unlock()
			in, _ := body["inputs"].(map[string]any)
			q, _ := in["question"].(string)
			if q == "boom" {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = io.WriteString(w, `{"error":"model crashed"}`)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"output": strings.ToUpper(q), "model": "remote-upper",
				"usage": map[string]any{"prompt_tokens": 3, "completion_tokens": 1, "total_tokens": 4}})
		case "/v1/chat/completions":
			m.mu.Lock()
			m.chats = append(m.chats, body)
			m.mu.Unlock()
			msgs, _ := body["messages"].([]any)
			last, _ := msgs[len(msgs)-1].(map[string]any)
			content, _ := last["content"].(string)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "c1", "object": "chat.completion", "model": body["model"],
				"choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": strings.ToLower(content)}, "finish_reason": "stop"}},
				"usage":   map[string]any{"prompt_tokens": 5, "completion_tokens": 2, "total_tokens": 7}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(hs.Close)
	return m, hs
}

func TestCustomRuntimeApply(t *testing.T) {
	e := newEnv(t)
	_, hs := newModelServer(t)
	code, res := e.api2Do(http.MethodPut, "/v2/acme/evals/runtimes/my-vllm", map[string]any{
		"base_url": hs.URL + "/v1/", "api_key_secret": "VLLM_KEY", "headers": map[string]any{"X-Team": "ml"},
		"runtime_ids": []any{map[string]any{"id": "llama-3-8b"}, map[string]any{"id": "qwen/qwen2-7b", "max_tokens": 8192}},
	})
	if code != 200 {
		t.Fatalf("apply -> %d %v", code, res)
	}
	if res["name"] != "my-vllm" || res["base_url"] != hs.URL+"/v1" || res["api_key_secret"] != "VLLM_KEY" ||
		res["headers"].(map[string]any)["X-Team"] != "ml" {
		t.Fatalf("apply response = %v", res)
	}
	ids := res["runtime_ids"].([]any)
	if len(ids) != 2 {
		t.Fatalf("runtime_ids = %v", ids)
	}
	first, second := ids[0].(map[string]any), ids[1].(map[string]any)
	if first["max_tokens"].(float64) != 4096 || first["playground_id"] != "my-vllm/llama-3-8b" || second["max_tokens"].(float64) != 8192 {
		t.Fatalf("runtime ids = %v", ids)
	}
	// The runtime IDs are selectable models.
	code, models := e.api2Do(http.MethodGet, "/llm/models?project_id="+pid, nil)
	if code != 200 || !strings.Contains(toJSON(models), "my-vllm/qwen/qwen2-7b") {
		t.Fatalf("llm models = %v", models)
	}
	// /completions/create routes "<runtime>/<id>" to the endpoint with the secret and headers.
	e.post("/secrets/set", map[string]any{"name": "VLLM_KEY", "value": "sk-vllm"})
	resp := e.post("/completions/create", map[string]any{"project_id": pid, "inputs": map[string]any{
		"model": "my-vllm/llama-3-8b", "messages": []any{map[string]any{"role": "user", "content": "HELLO"}}}})
	if !strings.Contains(toJSON(resp), `"content":"hello"`) {
		t.Fatalf("completion via runtime = %v", resp)
	}

	// Re-applying replaces the list: the omitted ID is removed.
	code, res = e.api2Do(http.MethodPut, "/v2/acme/evals/runtimes/my-vllm", map[string]any{
		"base_url": hs.URL + "/v1", "runtime_ids": []any{map[string]any{"id": "llama-3-8b"}}})
	if code != 200 || res["api_key_secret"] != nil || len(res["runtime_ids"].([]any)) != 1 {
		t.Fatalf("re-apply -> %d %v", code, res)
	}
	_, models = e.api2Do(http.MethodGet, "/llm/models?project_id="+pid, nil)
	if strings.Contains(toJSON(models), "qwen2-7b") {
		t.Fatalf("removed runtime id still listed: %v", models)
	}
	_, list := e.api2Do(http.MethodGet, "/v2/acme/evals/runtimes", nil)
	if rs := list["runtimes"].([]any); len(rs) != 1 || rs[0].(map[string]any)["kind"] != "openai" {
		t.Fatalf("runtime list = %v", list)
	}

	for _, bad := range []struct {
		name string
		body map[string]any
	}{
		{"x", map[string]any{"base_url": "ftp://x", "runtime_ids": []any{}}},
		{"x", map[string]any{"base_url": hs.URL}},
		{"x", map[string]any{"base_url": hs.URL, "runtime_ids": []any{map[string]any{"id": "has space"}}}},
		{"x", map[string]any{"base_url": hs.URL, "runtime_ids": []any{map[string]any{"id": "a", "max_tokens": 0}}}},
		{"openai", map[string]any{"base_url": hs.URL, "runtime_ids": []any{}}},
		{"bad%20name", map[string]any{"base_url": hs.URL, "runtime_ids": []any{}}},
		{"x", map[string]any{"base_url": hs.URL, "runtime_ids": []any{}, "kind": "grpc"}},
	} {
		if code, res := e.api2Do(http.MethodPut, "/v2/acme/evals/runtimes/"+bad.name, bad.body); code != 400 {
			t.Fatalf("bad runtime %s %v -> %d %v", bad.name, bad.body, code, res)
		}
	}
}

func toJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// evalFixture publishes a 3-row dataset and an exact-match evaluation.
func evalFixture(e *testEnv) string {
	tbl := e.post("/table/create", map[string]any{"table": map[string]any{"project_id": pid, "rows": []any{
		map[string]any{"question": "paris", "expected": "PARIS"},
		map[string]any{"question": "rome", "expected": "MADRID"},
		map[string]any{"question": "oslo", "expected": "OSLO"},
	}}})
	ds := publish(e, "caps", map[string]any{"_type": "Dataset", "_class_name": "Dataset", "_bases": []any{"Object", "BaseModel"},
		"name": "caps", "rows": "weft:///" + pid + "/table/" + tbl["digest"].(string)})
	exact := publish(e, "exact", map[string]any{"_class_name": "ExactMatchScorer", "_bases": []any{"Scorer", "Object", "BaseModel"}, "target_column": "expected"})
	return publish(e, "caps-eval", map[string]any{"_type": "Evaluation", "_class_name": "Evaluation", "_bases": []any{"Object", "BaseModel"},
		"dataset": ds, "scorers": []any{exact}})
}

// predictCalls returns the <op>.predict calls under an evaluation.
func predictCalls(t *testing.T, e *testEnv, evalID, op string) []map[string]any {
	t.Helper()
	calls := e.post("/calls/query", map[string]any{"project_id": pid, "filter": map[string]any{"trace_ids": []any{evalID}}})["calls"].([]any)
	var out []map[string]any
	for _, c := range calls {
		cm := c.(map[string]any)
		if strings.Contains(cm["op_name"].(string), "/"+op+":") {
			out = append(out, cm)
		}
	}
	return out
}

func TestEvaluateModelWithPredictURL(t *testing.T) {
	e := newEnv(t)
	ms, hs := newModelServer(t)
	ev := evalFixture(e)
	e.post("/secrets/set", map[string]any{"name": "MODEL_TOKEN", "value": "tok-123"})
	model := publish(e, "upper-model", map[string]any{"_class_name": "UpperModel", "_bases": []any{"Model", "Object", "BaseModel"},
		"predict_url": hs.URL + "/predict", "predict_api_key_secret": "MODEL_TOKEN", "predict_headers": map[string]any{"X-Team": "ml"}})
	id := e.post("/evaluate_model", map[string]any{"project_id": pid, "evaluation_ref": ev, "model_ref": model})["call_id"].(string)
	waitEval(t, e, id)
	root := e.post("/call/read", map[string]any{"project_id": pid, "id": id})["call"].(map[string]any)
	if frac := root["output"].(map[string]any)["exact"].(map[string]any)["true_fraction"].(float64); frac < 0.66 || frac > 0.67 {
		t.Fatalf("exact fraction = %v (%v)", frac, root["output"])
	}
	preds := predictCalls(t, e, id, "UpperModel.predict")
	if len(preds) != 3 {
		t.Fatalf("predict calls = %d", len(preds))
	}
	for _, p := range preds {
		in := p["inputs"].(map[string]any)
		if in["self"] != model || in["question"] == nil || p["output"] != strings.ToUpper(in["question"].(string)) {
			t.Fatalf("predict call = %v", p)
		}
		if p["summary"].(map[string]any)["usage"].(map[string]any)["remote-upper"] == nil {
			t.Fatalf("predict usage not recorded: %v", p["summary"])
		}
	}
	ms.mu.Lock()
	defer ms.mu.Unlock()
	if len(ms.predicts) != 3 || ms.predicts[0]["model_ref"] != model || ms.auth[0] != "Bearer tok-123|ml" {
		t.Fatalf("model server saw %v / %v", ms.predicts, ms.auth)
	}
}

func TestEvaluateModelBoundToRuntime(t *testing.T) {
	e := newEnv(t)
	ms, hs := newModelServer(t)
	ev := evalFixture(e)
	// An OpenAI-compatible runtime and a "predict" runtime.
	if code, res := e.api2Do(http.MethodPut, "/v2/acme/evals/runtimes/chat-rt", map[string]any{
		"base_url": hs.URL + "/v1", "runtime_ids": []any{map[string]any{"id": "lower-1"}}}); code != 200 {
		t.Fatalf("apply chat runtime -> %d %v", code, res)
	}
	if code, res := e.api2Do(http.MethodPut, "/v2/acme/evals/runtimes/predict-rt", map[string]any{
		"base_url": hs.URL + "/predict", "kind": "predict", "headers": map[string]any{"X-Team": "infra"},
		"runtime_ids": []any{map[string]any{"id": "upper-v2"}}}); code != 200 {
		t.Fatalf("apply predict runtime -> %d %v", code, res)
	}
	chatModel := publish(e, "chat-model", map[string]any{"_class_name": "CapitalModel", "_bases": []any{"Model", "Object", "BaseModel"},
		"runtime": "chat-rt", "system_prompt": "Answer in one word.", "prompt_template": "Capital: {question}"})
	predModel := publish(e, "pred-model", map[string]any{"_class_name": "UpperModel", "_bases": []any{"Model", "Object", "BaseModel"},
		"runtime": "predict-rt"})

	id1 := e.post("/evaluate_model", map[string]any{"project_id": pid, "evaluation_ref": ev, "model_ref": chatModel})["call_id"].(string)
	id2 := e.post("/evaluate_model", map[string]any{"project_id": pid, "evaluation_ref": ev, "model_ref": predModel})["call_id"].(string)
	waitEval(t, e, id1)
	waitEval(t, e, id2)

	chatPreds := predictCalls(t, e, id1, "CapitalModel.predict")
	if len(chatPreds) != 3 || chatPreds[0]["output"] != "capital: "+chatPreds[0]["inputs"].(map[string]any)["question"].(string) {
		t.Fatalf("chat runtime predict calls = %v", chatPreds)
	}
	if n := len(predictCalls(t, e, id2, "UpperModel.predict")); n != 3 {
		t.Fatalf("predict runtime calls = %d", n)
	}
	ms.mu.Lock()
	if len(ms.chats) != 3 || ms.chats[0]["model"] != "lower-1" {
		t.Fatalf("chat requests = %v", ms.chats)
	}
	msgs := ms.chats[0]["messages"].([]any)
	if len(msgs) != 2 || msgs[0].(map[string]any)["role"] != "system" {
		t.Fatalf("chat messages = %v", msgs)
	}
	if len(ms.predicts) != 3 || ms.predicts[0]["runtime_id"] != "upper-v2" {
		t.Fatalf("predict requests = %v", ms.predicts)
	}
	ms.mu.Unlock()
	root := e.post("/call/read", map[string]any{"project_id": pid, "id": id2})["call"].(map[string]any)
	if frac := root["output"].(map[string]any)["exact"].(map[string]any)["true_fraction"].(float64); frac < 0.66 || frac > 0.67 {
		t.Fatalf("predict runtime exact fraction = %v", frac)
	}
}

func TestEvaluateModelRejectsUnrunnableModels(t *testing.T) {
	e := newEnv(t)
	ev := evalFixture(e)
	for _, val := range []map[string]any{
		{"_class_name": "PlainModel", "_bases": []any{"Model", "Object", "BaseModel"}},
		{"_class_name": "PlainModel", "_bases": []any{"Model", "Object", "BaseModel"}, "runtime": "nope"},
		{"_class_name": "PlainModel", "_bases": []any{"Model", "Object", "BaseModel"}, "predict_url": "file:///etc/passwd"},
	} {
		m := publish(e, "plain", val)
		if code, out := e.raw("/evaluate_model", map[string]any{"project_id": pid, "evaluation_ref": ev, "model_ref": m}, nil); code != 400 {
			t.Fatalf("%v -> %d %s", val, code, out)
		}
	}
}

func TestEvaluateModelRecordsPredictErrors(t *testing.T) {
	e := newEnv(t)
	_, hs := newModelServer(t)
	tbl := e.post("/table/create", map[string]any{"table": map[string]any{"project_id": pid, "rows": []any{
		map[string]any{"question": "boom", "expected": "x"}}}})
	ds := publish(e, "boom-ds", map[string]any{"_type": "Dataset", "_class_name": "Dataset", "rows": "weft:///" + pid + "/table/" + tbl["digest"].(string)})
	ev := publish(e, "boom-eval", map[string]any{"_type": "Evaluation", "_class_name": "Evaluation", "dataset": ds, "scorers": []any{}})
	model := publish(e, "boom-model", map[string]any{"_class_name": "UpperModel", "predict_url": hs.URL + "/predict"})
	id := e.post("/evaluate_model", map[string]any{"project_id": pid, "evaluation_ref": ev, "model_ref": model})["call_id"].(string)
	waitEval(t, e, id)
	preds := predictCalls(t, e, id, "UpperModel.predict")
	if len(preds) != 1 || preds[0]["exception"] == nil || !strings.Contains(preds[0]["exception"].(string), "500") {
		t.Fatalf("failed predict call = %v", preds)
	}
}
