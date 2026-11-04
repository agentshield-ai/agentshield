package compat_test

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// With the adapter on, Weft's own routes keep their contract for every
// client: only the adapter's routes are translated for the weave client.
func TestNativeRoutesNotTranslated(t *testing.T) {
	e := newEnv(t, true)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	e.post("/api/v1/spans/start", map[string]any{"project_id": pid, "id": "c1", "name": "op", "started_at": now}, nil)
	e.post("/api/v1/feedback/create", map[string]any{"project_id": pid, "ref": "weft:///" + pid + "/span/c1",
		"feedback_type": "weft.note", "payload": map[string]any{"note": "x"}}, nil)
	for _, tc := range []struct {
		path string
		body map[string]any
		want string
	}{
		{"/api/v1/feedback/query", map[string]any{"project_id": pid}, `"ref":"weft:///` + pid + `/span/c1"`},
		{"/api/v1/spans/query", map[string]any{"project_id": pid, "include_feedback": true}, `"ref":"weft:///` + pid + `/span/c1"`},
		{"/api/v1/refs/read", map[string]any{"refs": []any{"weft:///" + pid + "/span/c1"}}, `"name":"op"`},
	} {
		for _, h := range []map[string]string{weaveUA, {"X-Weft-Ref-Scheme": "weave"}, {"X-Stainless-Lang": "python"}} {
			code, out, _ := e.raw(http.MethodPost, tc.path, tc.body, h)
			s := string(out)
			if code != 200 || strings.Contains(s, "weave") || strings.Contains(s, "wandb.") || !strings.Contains(s, tc.want) {
				t.Errorf("%s with %v -> %d %s", tc.path, h, code, s)
			}
		}
	}
	// The adapter's own route still translates for its client.
	_, out, _ := e.raw(http.MethodPost, "/feedback/query", map[string]any{"project_id": pid}, weaveUA)
	if !strings.Contains(string(out), "weave:///") {
		t.Fatalf("adapter route not translated: %s", out)
	}
}

// With the adapter on, a GET of one of its POST routes is a JSON 405, not
// the UI.
func TestAdapterRouteWrongMethod(t *testing.T) {
	e := newEnv(t, true)
	for _, p := range []string{"/obj/read", "/calls/stream_query", "/api/v1/refs/read"} {
		code, out, h := e.raw(http.MethodGet, p, nil, nil)
		if code != http.StatusMethodNotAllowed || string(out) == "ui" || !strings.Contains(h.Get("Content-Type"), "json") {
			t.Errorf("GET %s -> %d %s", p, code, out)
		}
	}
}
