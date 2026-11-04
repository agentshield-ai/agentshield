package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func scorerVal(cls string, fields map[string]any) map[string]any {
	v := map[string]any{"_type": cls, "_class_name": cls, "_bases": []any{"Scorer", "Object", "BaseModel"}, "name": fields["name"]}
	for k, x := range fields {
		v[k] = x
	}
	return v
}

func scoreResult(t *testing.T, e *testEnv, callID, scorerRef string) map[string]any {
	t.Helper()
	res := e.post("/api/v1/spans/score", map[string]any{"project_id": pid, "ids": []any{callID}, "scorer_refs": []any{scorerRef}})["items"].([]any)
	return res[0].(map[string]any)
}

func scoreOut(t *testing.T, e *testEnv, callID, scorerRef string) any {
	t.Helper()
	r := scoreResult(t, e, callID, scorerRef)
	if r["error"] != nil {
		t.Fatalf("%s on %s: %v", scorerRef, callID, r["error"])
	}
	return r["output"]
}

func scoreOne(t *testing.T, e *testEnv, callID, scorerRef string) map[string]any {
	t.Helper()
	out, _ := scoreOut(t, e, callID, scorerRef).(map[string]any)
	return out
}

func endCall(e *testEnv, id string, inputs map[string]any, output any, exception string) {
	e.startSpan(map[string]any{"id": id, "trace_id": id, "name": "bot", "started_at": "2025-01-01T00:00:00Z", "inputs": inputs})
	end := map[string]any{"id": id, "ended_at": "2025-01-01T00:00:12Z", "output": output, "summary": map[string]any{}}
	if exception != "" {
		end["error"] = exception
	}
	e.endSpan(end)
}

func seedScorerCalls(e *testEnv) {
	endCall(e, "xml", map[string]any{"question": "q", "target": "the cat sat on the mat"}, "<a><b>1</b></a>", "")
	endCall(e, "json", map[string]any{"question": "q"}, `{"name":"x","age":3}`, "")
	endCall(e, "pii", map[string]any{"question": "q"}, "mail me at jo@example.com or call 555-123-4567", "")
	endCall(e, "boom", map[string]any{"question": "q"}, nil, "ValueError: bad")
	endCall(e, "cat", map[string]any{"question": "q", "target": "the cat sat on the mat", "n": "4"}, "the cat sat on a mat", "")
	endCall(e, "rude", map[string]any{"question": "q", "n": 1}, "I hate you, idiot", "")
}

func TestExtraScorers(t *testing.T) {
	e := newEnv(t)
	seedScorerCalls(e)

	xml := publish(e, "xml", scorerVal("ValidXMLScorer", map[string]any{"name": "xml"}))
	if out := scoreOut(t, e, "xml", xml); out != true {
		t.Fatalf("xml: %v", out)
	}
	if out := scoreOut(t, e, "json", xml); out != false {
		t.Fatalf("json as xml: %v", out)
	}

	schema := publish(e, "schema", scorerVal("JSONSchemaScorer", map[string]any{"name": "schema", "schema": map[string]any{
		"type": "object", "required": []any{"name", "age"}, "properties": map[string]any{"name": map[string]any{"type": "string"}, "age": map[string]any{"type": "integer", "minimum": 0}}}}))
	if out := scoreOut(t, e, "json", schema); out != true {
		t.Fatalf("schema: %v", out)
	}
	if out := scoreOut(t, e, "xml", schema); out != false {
		t.Fatalf("schema on xml: %v", out)
	}

	// Named rules (signals) return {flagged, value, reason}.
	rule := func(name, r string, extra map[string]any) string {
		f := map[string]any{"name": name, "rule": r}
		for k, v := range extra {
			f[k] = v
		}
		return publish(e, name, scorerVal("RuleScorer", f))
	}
	cases := []struct {
		scorer, call string
		flagged      bool
	}{
		{rule("exc", "exception", nil), "boom", true},
		{rule("exc", "exception", nil), "xml", false},
		{rule("slow", "latency_gt", map[string]any{"threshold": 10}), "xml", true},
		{rule("slow5", "latency_gt", map[string]any{"threshold": 30}), "xml", false},
		{rule("empty", "empty_output", nil), "boom", true},
		{rule("empty", "empty_output", nil), "xml", false},
		{rule("badjson", "invalid_json", nil), "xml", true},
		{rule("badjson", "invalid_json", nil), "json", false},
		{rule("pii", "pii_detected", nil), "pii", true},
		{rule("pii", "pii_detected", nil), "json", false},
	}
	for _, c := range cases {
		out := scoreOne(t, e, c.call, c.scorer)
		if out["flagged"] != c.flagged {
			t.Fatalf("%s on %s: %v", c.scorer, c.call, out)
		}
	}

	mod := publish(e, "mod", scorerVal("OpenAIModerationScorer", map[string]any{"name": "mod", "model": "mock/moderation"}))
	if out := scoreOne(t, e, "rude", mod); out["flagged"] != true || out["categories"].(map[string]any)["hate"] != true {
		t.Fatalf("moderation: %v", out)
	}
	if out := scoreOne(t, e, "cat", mod); out["flagged"] != false {
		t.Fatalf("moderation clean: %v", out)
	}

	// Judge presets without a stored prompt fall back to the SDK prompts.
	for cls, keys := range map[string][]string{"HallucinationFreeScorer": {"hallucination_free"}, "SummarizationScorer": {"summarization_score", "is_faithful"},
		"RelevanceScorer": {"is_relevant", "relevance_score"}} {
		ref := publish(e, cls, scorerVal(cls, map[string]any{"name": cls, "model": "mock/json"}))
		out := scoreOne(t, e, "cat", ref)
		for _, k := range keys {
			if _, ok := out[k]; !ok {
				t.Fatalf("%s: missing %s in %v", cls, k, out)
			}
		}
	}
}

// TestSDKSerialisedScorers runs scorer objects exactly as the Python SDK
// serialises them (testdata/sdk_scorers.json, produced by object_val).
func TestSDKSerialisedScorers(t *testing.T) {
	e := newEnv(t)
	seedScorerCalls(e)

	var got map[string]any
	var gotAuth string
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = nil
		_ = json.Unmarshal(b, &got)
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"score": 0.9, "label": "good"}`))
	}))
	defer remote.Close()
	e.post("/secrets/set", map[string]any{"name": "REMOTE_TOKEN", "value": "tok"})

	raw, err := os.ReadFile("testdata/sdk_scorers.json")
	if err != nil {
		t.Fatal(err)
	}
	raw = []byte(strings.ReplaceAll(string(raw), "http://REMOTE_URL", remote.URL))
	var vals map[string]map[string]any
	if err := json.Unmarshal(raw, &vals); err != nil {
		t.Fatal(err)
	}
	ref := map[string]string{}
	for name, v := range vals {
		ref[name] = publish(e, name, v)
	}

	for name, key := range map[string]string{"hall": "hallucination_free", "summ": "summarization_score", "rel": "is_relevant"} {
		out := scoreOne(t, e, "cat", ref[name])
		if _, ok := out[key]; !ok {
			t.Fatalf("%s: missing %s in %v", name, key, out)
		}
	}
	if out := scoreOut(t, e, "xml", ref["xml"]); out != true {
		t.Fatalf("xml: %v", out)
	}
	if out := scoreOut(t, e, "json", ref["schema"]); out != true {
		t.Fatalf("schema: %v", out)
	}
	if out := scoreOut(t, e, "xml", ref["schema"]); out != false {
		t.Fatalf("schema on xml: %v", out)
	}
	if sim, ok := scoreOut(t, e, "cat", ref["emb"]).(float64); !ok || sim < 0.5 || sim > 1.0001 {
		t.Fatalf("embedding similarity: %v", sim)
	}
	if out := scoreOut(t, e, "cat", ref["rule"]); out != true {
		t.Fatalf("contains rule on cat: %v", out)
	}
	if out := scoreOut(t, e, "rude", ref["rule"]); out != false {
		t.Fatalf("contains rule on rude: %v", out)
	}
	if out := scoreOut(t, e, "cat", ref["rulet"]); out != true { // inputs.n "4" -> 4.0 >= 3
		t.Fatalf("threshold rule on cat: %v", out)
	}
	if out := scoreOut(t, e, "rude", ref["rulet"]); out != false { // 1 < 3
		t.Fatalf("threshold rule on rude: %v", out)
	}
	if out := scoreOne(t, e, "cat", ref["remote"]); out["score"].(float64) != 0.9 {
		t.Fatalf("remote: %v", out)
	}
	if gotAuth != "Bearer tok" || got["output"] != "the cat sat on a mat" || got["inputs"].(map[string]any)["question"] != "q" || len(got) != 2 {
		t.Fatalf("remote request: auth=%q body=%v", gotAuth, got)
	}
	// Without an OpenAI key the moderation scorer is accepted but fails to call the API.
	if os.Getenv("OPENAI_API_KEY") != "" {
		return
	}
	if r := scoreResult(t, e, "cat", ref["mod"]); r["error"] == nil || strings.Contains(r["error"].(string), "cannot run") {
		t.Fatalf("moderation without key: %v", r)
	}
}

func TestEmbeddingsCreate(t *testing.T) {
	e := newEnv(t)
	res := e.post("/api/v1/embeddings", map[string]any{"project_id": pid, "inputs": map[string]any{"model": "mock/embed", "input": []any{"the cat", "the cat"}}})
	data := res["response"].(map[string]any)["data"].([]any)
	if len(data) != 2 || len(data[0].(map[string]any)["embedding"].([]any)) == 0 {
		t.Fatalf("embeddings: %v", res)
	}
	res = e.post("/api/v1/embeddings", map[string]any{"project_id": pid, "inputs": map[string]any{"model": "mock/embed", "input": "one"}})
	if len(res["response"].(map[string]any)["data"].([]any)) != 1 {
		t.Fatalf("single input: %v", res)
	}
	if code, _ := e.raw("/api/v1/embeddings", map[string]any{"project_id": pid, "inputs": map[string]any{"model": "anthropic/claude-x", "input": "x"}}, nil); code != 400 {
		t.Fatalf("anthropic embeddings should fail with 400, got %d", code)
	}
}
