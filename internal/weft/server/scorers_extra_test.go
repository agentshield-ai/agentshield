package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func scorerVal(cls string, fields map[string]any) map[string]any {
	v := map[string]any{"_type": cls, "_class_name": cls, "_bases": []any{"Scorer", "Object", "BaseModel"}, "name": fields["name"]}
	for k, x := range fields {
		v[k] = x
	}
	return v
}

func scoreOne(t *testing.T, e *testEnv, callID, scorerRef string) map[string]any {
	t.Helper()
	res := e.post("/calls/score", map[string]any{"project_id": pid, "call_ids": []any{callID}, "scorer_refs": []any{scorerRef}})["results"].([]any)
	r := res[0].(map[string]any)
	if r["error"] != nil {
		t.Fatalf("%s: %v", scorerRef, r["error"])
	}
	out, _ := r["output"].(map[string]any)
	return out
}

func endCall(e *testEnv, id string, inputs map[string]any, output any, exception string) {
	e.post("/call/start", map[string]any{"start": map[string]any{"project_id": pid, "id": id, "trace_id": id, "op_name": "bot",
		"started_at": "2025-01-01T00:00:00Z", "inputs": inputs}})
	end := map[string]any{"project_id": pid, "id": id, "ended_at": "2025-01-01T00:00:12Z", "output": output, "summary": map[string]any{}}
	if exception != "" {
		end["exception"] = exception
	}
	e.post("/call/end", map[string]any{"end": end})
}

func TestExtraScorers(t *testing.T) {
	e := newEnv(t)
	endCall(e, "xml", map[string]any{"question": "q", "target": "the cat sat on the mat"}, "<a><b>1</b></a>", "")
	endCall(e, "json", map[string]any{"question": "q"}, `{"name":"x","age":3}`, "")
	endCall(e, "pii", map[string]any{"question": "q"}, "mail me at jo@example.com or call 555-123-4567", "")
	endCall(e, "boom", map[string]any{"question": "q"}, nil, "ValueError: bad")
	endCall(e, "cat", map[string]any{"question": "q", "target": "the cat sat on the mat", "context": "cats sit"}, "the cat sat on a mat", "")
	endCall(e, "rude", map[string]any{"question": "q"}, "I hate you, idiot", "")

	xml := publish(e, "xml", scorerVal("ValidXMLScorer", map[string]any{"name": "xml"}))
	if out := scoreOne(t, e, "xml", xml); out["xml_valid"] != true {
		t.Fatalf("xml: %v", out)
	}
	if out := scoreOne(t, e, "json", xml); out["xml_valid"] != false {
		t.Fatalf("json as xml: %v", out)
	}

	schema := publish(e, "schema", scorerVal("JSONSchemaScorer", map[string]any{"name": "schema", "schema": map[string]any{
		"type": "object", "required": []any{"name", "age"}, "properties": map[string]any{"name": map[string]any{"type": "string"}, "age": map[string]any{"type": "integer", "minimum": 0}}}}))
	if out := scoreOne(t, e, "json", schema); out["valid"] != true {
		t.Fatalf("schema: %v", out)
	}
	if out := scoreOne(t, e, "xml", schema); out["valid"] != false {
		t.Fatalf("schema on xml: %v", out)
	}

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

	emb := publish(e, "emb", scorerVal("EmbeddingSimilarityScorer", map[string]any{"name": "emb", "model_id": "mock/embed", "threshold": 0.5}))
	if out := scoreOne(t, e, "cat", emb); out["is_similar"] != true || out["similarity_score"].(float64) < 0.5 {
		t.Fatalf("embedding: %v", out)
	}

	mod := publish(e, "mod", scorerVal("OpenAIModerationScorer", map[string]any{"name": "mod", "model_id": "mock/moderation"}))
	if out := scoreOne(t, e, "rude", mod); out["passed"] != false {
		t.Fatalf("moderation: %v", out)
	}
	if out := scoreOne(t, e, "cat", mod); out["passed"] != true {
		t.Fatalf("moderation clean: %v", out)
	}

	for _, cls := range []string{"HallucinationFreeScorer", "SummarizationScorer", "RelevanceScorer"} {
		ref := publish(e, cls, scorerVal(cls, map[string]any{"name": cls, "model_id": "mock/json"}))
		out := scoreOne(t, e, "cat", ref)
		key := map[string]string{"HallucinationFreeScorer": "has_hallucination", "SummarizationScorer": "summarization_eval_score", "RelevanceScorer": "relevant"}[cls]
		if _, ok := out[key]; !ok {
			t.Fatalf("%s: missing %s in %v", cls, key, out)
		}
	}

	var got map[string]any
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(401)
			return
		}
		_, _ = w.Write([]byte(`{"score": 0.9, "label": "good"}`))
	}))
	defer remote.Close()
	e.post("/secrets/set", map[string]any{"name": "REMOTE_TOKEN", "value": "tok"})
	rs := publish(e, "remote", scorerVal("RemoteScorer", map[string]any{"name": "remote", "url": remote.URL, "headers_secret": "REMOTE_TOKEN", "timeout": 5}))
	if out := scoreOne(t, e, "cat", rs); out["score"].(float64) != 0.9 {
		t.Fatalf("remote: %v", out)
	}
	if got["output"] != "the cat sat on a mat" || got["inputs"].(map[string]any)["question"] != "q" || got["call"].(map[string]any)["id"] != "cat" {
		t.Fatalf("remote request: %v", got)
	}
}
