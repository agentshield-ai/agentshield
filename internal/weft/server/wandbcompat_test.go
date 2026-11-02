package server

import (
	"strings"
	"testing"
)

func TestGraphQLShim(t *testing.T) {
	e := newEnv(t, "bob=key1")
	e.key = "key1"
	code, out := e.raw("/graphql", map[string]any{"query": "query DefaultEntity { viewer { username defaultEntity { name } } }"}, nil)
	if code != 200 || !strings.Contains(string(out), `"defaultEntity":{"name":"bob"}`) {
		t.Fatalf("viewer: %d %s", code, out)
	}
	code, out = e.raw("/graphql", map[string]any{
		"query":     "mutation UpsertModel($name: String!, $entity: String!) { upsertModel(input: {name: $name, entityName: $entity}) { model { name } } }",
		"variables": map[string]any{"name": "proj", "entity": "bob"},
	}, nil)
	if code != 200 || !strings.Contains(string(out), `"name":"proj"`) {
		t.Fatalf("upsertModel: %d %s", code, out)
	}
	_, plist := e.raw("/projects", nil, nil)
	if !strings.Contains(string(plist), `"project_id":"bob/proj"`) {
		t.Fatalf("project not created: %s", plist)
	}
	_, out = e.raw("/graphql", map[string]any{"query": "query { somethingElse }"}, nil)
	if !strings.Contains(string(out), `"errors"`) {
		t.Fatalf("unsupported operation should return a GraphQL error: %s", out)
	}
}

func TestCallsCompleteCumulativeSummaries(t *testing.T) {
	e := newEnv(t)
	usage := func(p int) map[string]any {
		return map[string]any{"usage": map[string]any{"gpt-4o": map[string]any{"requests": 1, "prompt_tokens": p, "completion_tokens": 1}},
			"status_counts": map[string]any{"success": 1, "error": 0}}
	}
	// The weave client sends children first; parents carry cumulative summaries.
	child := map[string]any{"project_id": pid, "id": "k", "trace_id": "t", "parent_id": "r", "op_name": "weave:///" + pid + "/op/llm:abc",
		"started_at": "2026-01-01T00:00:00.1Z", "ended_at": "2026-01-01T00:00:00.5Z", "inputs": map[string]any{}, "attributes": map[string]any{},
		"output": "x", "summary": usage(10)}
	rootSummary := map[string]any{"usage": map[string]any{"gpt-4o": map[string]any{"requests": 1, "prompt_tokens": 10, "completion_tokens": 1}},
		"status_counts": map[string]any{"success": 2, "error": 0}}
	root := map[string]any{"project_id": pid, "id": "r", "trace_id": "t", "op_name": "weave:///" + pid + "/op/agent:def",
		"started_at": "2026-01-01T00:00:00Z", "ended_at": "2026-01-01T00:00:01Z", "inputs": map[string]any{"q": "weave:///" + pid + "/object/ds:d1"},
		"attributes": map[string]any{}, "output": "y", "summary": rootSummary}
	ent, proj, _ := strings.Cut(pid, "/")
	code, out := e.raw("/v2/"+ent+"/"+proj+"/calls/complete", map[string]any{"batch": []any{child, root}},
		map[string]string{"User-Agent": "WeaveTrace/Python 0.54.0"})
	if code != 200 {
		t.Fatalf("calls/complete: %d %s", code, out)
	}
	// Read back as the weave client: refs come back with the weave scheme and usage is not doubled.
	_, body := e.raw("/call/read", map[string]any{"project_id": pid, "id": "r"}, map[string]string{"X-Stainless-Lang": "python"})
	s := string(body)
	if strings.Contains(s, "weft:///") || !strings.Contains(s, `"q":"weave:///`+pid+`/object/ds:d1"`) {
		t.Fatalf("weave client should see weave:/// refs: %s", s)
	}
	if !strings.Contains(s, `"prompt_tokens":10`) {
		t.Fatalf("cumulative usage double counted or lost: %s", s)
	}
	// Weft clients see weft:/// refs.
	r := e.post("/call/read", map[string]any{"project_id": pid, "id": "r"})["call"].(map[string]any)
	if r["inputs"].(map[string]any)["q"] != "weft:///"+pid+"/object/ds:d1" {
		t.Fatalf("weft client ref = %v", r["inputs"])
	}
	if sc := r["summary"].(map[string]any)["status_counts"].(map[string]any); sc["success"].(float64) != 1 {
		t.Fatalf("descendant status counts = %v", sc)
	}
	_, info := e.raw("/service/projects_info", map[string]any{"project_ids": []any{pid}}, nil)
	if !strings.Contains(string(info), `"internal_project_id":"`+pid+`"`) {
		t.Fatalf("projects_info = %s", info)
	}
	_, si := e.raw("/server_info", nil, nil)
	if !strings.Contains(string(si), "min_required_weave_python_version") {
		t.Fatalf("server_info lacks weave fields: %s", si)
	}
}
