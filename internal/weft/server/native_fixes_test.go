package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agentshield-ai/agentshield/internal/weft/store"
)

// refs/read returns stored values with the publisher's key order and
// number text, through every kind of path (CLAUDE.md, API.md "Tables").
func TestRefsReadKeepsKeyOrder(t *testing.T) {
	e := newEnv(t)
	code, out := e.raw("/api/v1/tables/create", []byte(`{"project_id":"`+pid+`","rows":[{"zeta":1.0,"alpha":{"y":2,"x":3}}]}`), nil)
	if code != 200 {
		t.Fatal(string(out))
	}
	var tc map[string]any
	_ = json.Unmarshal(out, &tc)
	tref := tc["ref"].(string)
	row := tc["row_digests"].([]any)[0].(string)
	code, out = e.raw("/api/v1/objects/save", []byte(`{"project_id":"`+pid+`","name":"o","value":{"zeta":1.50,"alpha":{"y":[{"q":1,"p":2}],"x":3},"rows":"`+tref+`"}}`), nil)
	if code != 200 {
		t.Fatal(string(out))
	}
	obj := "weft:///" + pid + "/object/o:latest"
	for _, tc := range []struct{ ref, want string }{
		{obj, `{"zeta":1.50,"alpha":{"y":[{"q":1,"p":2}],"x":3},"rows":"` + tref + `"}`},
		{obj + "/attr/alpha", `{"y":[{"q":1,"p":2}],"x":3}`},
		{obj + "/key/alpha/key/y/index/0", `{"q":1,"p":2}`},
		{obj + "/attr/rows", `[{"zeta":1.0,"alpha":{"y":2,"x":3}}]`},
		{obj + "/attr/rows/index/0", `{"zeta":1.0,"alpha":{"y":2,"x":3}}`},
		{tref + "/id/" + row, `{"zeta":1.0,"alpha":{"y":2,"x":3}}`},
		{tref, `[{"zeta":1.0,"alpha":{"y":2,"x":3}}]`},
	} {
		_, out := e.raw("/api/v1/refs/read", map[string]any{"refs": []any{tc.ref}}, nil)
		if want := `{"items":[` + tc.want + `]}`; strings.TrimSpace(string(out)) != want {
			t.Errorf("refs/read %s\n got %s\nwant %s", tc.ref, out, want)
		}
	}
}

// A span reached through a ref has the Span schema on Weft's own API and
// the stored row on the adapter's route.
func TestSpanRefsUseSpanSchema(t *testing.T) {
	e := newLegacyEnv(t)
	e.startSpan(map[string]any{"id": "s1", "name": "f", "session_id": "conv", "inputs": map[string]any{"q": 1}})
	e.endSpan(map[string]any{"id": "s1", "error": "boom"})
	e.startSpan(map[string]any{"id": "s2", "name": "g", "inputs": map[string]any{"parent": spanRef("s1")}})
	native := e.post("/api/v1/refs/read", map[string]any{"refs": []any{spanRef("s1"), spanRef("s1") + "/key/error"}})["items"].([]any)
	sp := native[0].(map[string]any)
	if sp["name"] != "f" || sp["error"] != "boom" || sp["session_id"] != "conv" || sp["op_name"] != nil || sp["exception"] != nil ||
		sp["thread_id"] != nil || native[1] != "boom" {
		t.Fatalf("native span ref = %v", native)
	}
	q := e.post("/api/v1/spans/query", map[string]any{"project_id": pid, "filter": map[string]any{"ids": []any{"s2"}},
		"expand_columns": []any{"inputs.parent"}})
	exp := q["items"].([]any)[0].(map[string]any)["inputs"].(map[string]any)["parent"].(map[string]any)
	if exp["name"] != "f" || exp["error"] != "boom" || exp["op_name"] != nil || exp["_ref"] != spanRef("s1") {
		t.Fatalf("expanded span ref = %v", exp)
	}
	old := e.post("/refs/read_batch", map[string]any{"refs": []any{spanRef("s1")}})["vals"].([]any)[0].(map[string]any)
	if old["op_name"] != "f" || old["exception"] != "boom" || old["thread_id"] != "conv" || old["name"] != nil {
		t.Fatalf("adapter span ref = %v", old)
	}
}

// A read-only key may not run automation checks (they fire webhooks and
// record runs), and every POST route classified as a read must be one.
func TestReadOnlyKeyCannotCheckAutomations(t *testing.T) {
	e := newEnv(t, "root")
	ro, _ := createKey(e, "root", map[string]any{"name": "ro", "read_only": true})
	code, out := e.admDo("POST", "/automations/check", ro, map[string]any{"project_id": pid})
	if code != 403 || !strings.Contains(string(out), "read-only") {
		t.Fatalf("/automations/check with a read-only key -> %d %s", code, out)
	}
	for _, path := range []string{"/automations/check"} {
		r := httptest.NewRequest(http.MethodPost, path, nil)
		if isReadRequest(r) {
			t.Errorf("%s is classified as a read", path)
		}
	}
}

func TestSpansQueryCountsDeleted(t *testing.T) {
	e := newEnv(t)
	for _, id := range []string{"a", "b", "c"} {
		e.startSpan(map[string]any{"id": id, "name": "f"})
	}
	e.post("/api/v1/spans/delete", map[string]any{"project_id": pid, "ids": []any{"b"}})
	for _, tc := range []struct {
		deleted bool
		limit   any
		want    float64
		items   int
	}{
		{false, nil, 2, 2}, {true, nil, 3, 3}, {true, 0, 3, 0}, {true, 1, 3, 1},
	} {
		body := map[string]any{"project_id": pid, "include_deleted": tc.deleted}
		if tc.limit != nil {
			body["limit"] = tc.limit
		}
		res := e.post("/api/v1/spans/query", body)
		if res["total_count"] != tc.want || len(res["items"].([]any)) != tc.items {
			t.Errorf("include_deleted=%v limit=%v: total_count=%v items=%d, want %v/%d", tc.deleted, tc.limit,
				res["total_count"], len(res["items"].([]any)), tc.want, tc.items)
		}
	}
}

// With the adapter off, unknown API paths and the adapter's routes answer
// a JSON 404 instead of the UI; UI paths still get the UI.
func TestUnknownAPIPathsAre404(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "weft.db"))
	if err != nil {
		t.Fatal(err)
	}
	ui := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ui") })
	s := New(Config{UI: ui}, st)
	hs := httptest.NewServer(s.Handler())
	t.Cleanup(func() { hs.Close(); s.Close(); _ = st.Close() })
	do := func(method, path, accept string) (int, string, string) {
		req, _ := http.NewRequest(method, hs.URL+path, nil)
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, resp.Header.Get("Content-Type"), string(b)
	}
	for _, tc := range []struct {
		method, path, accept string
		code                 int
		ui                   bool
	}{
		{"GET", "/server_info", "", 404, false},
		{"GET", "/obj/read", "", 404, false},
		{"GET", "/calls/stream_query", "*/*", 404, false},
		{"GET", "/v2/e/p/call/start", "", 404, false},
		{"GET", "/api/v1/nonexistent", "", 404, false},
		{"GET", "/api/v1/nonexistent", "text/html", 404, false},
		{"GET", "/api/v1/refs/read", "", 405, false}, // a POST route
		{"GET", "/otel/v1/traces", "", 405, false},
		{"HEAD", "/api/v1/x", "", 404, false},
		{"GET", "/", "", 200, true},
		{"GET", "/acme/evals/traces", "", 200, true},
		{"GET", "/help/API", "", 200, true},
		{"GET", "/acme/evals/weave/calls/x", "", 200, true},                // the adapter's link redirect is a UI path when off
		{"GET", "/obj/read", "text/html,application/xhtml+xml", 200, true}, // entity "obj", project "read"
	} {
		code, ct, body := do(tc.method, tc.path, tc.accept)
		if code != tc.code || (body == "ui") != tc.ui || (!tc.ui && tc.method != "HEAD" && !strings.Contains(ct, "json")) {
			t.Errorf("%s %s (Accept %q) -> %d %s %q; want %d ui=%v", tc.method, tc.path, tc.accept, code, ct, body, tc.code, tc.ui)
		}
	}
}

// session_id comes from an explicit field, else the weft.session_id
// attribute (or its deprecated name), nested or flat, on every ingest path.
func TestSessionIDFromAttributes(t *testing.T) {
	e := newEnv(t)
	for _, tc := range []struct {
		name  string
		start map[string]any
		want  any
	}{
		{"flat", map[string]any{"attributes": map[string]any{"weft.session_id": "s-flat"}}, "s-flat"},
		{"nested", map[string]any{"attributes": map[string]any{"weft": map[string]any{"session_id": "s-nested"}}}, "s-nested"},
		{"deprecated flat", map[string]any{"attributes": map[string]any{"weft.thread_id": "s-old"}}, "s-old"},
		{"deprecated nested", map[string]any{"attributes": map[string]any{"weft": map[string]any{"thread_id": "s-old2"}}}, "s-old2"},
		{"new key wins", map[string]any{"attributes": map[string]any{"weft": map[string]any{"thread_id": "old", "session_id": "new"}}}, "new"},
		{"explicit wins", map[string]any{"session_id": "explicit", "attributes": map[string]any{"weft.session_id": "attr"}}, "explicit"},
		{"none", map[string]any{"attributes": map[string]any{"x": 1}}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, via := range []string{"start", "batch start", "batch span"} {
				id := newID()
				f := map[string]any{"project_id": pid, "id": id, "name": "f"}
				for k, v := range tc.start {
					f[k] = v
				}
				switch via {
				case "start":
					e.post("/api/v1/spans/start", f)
				case "batch start":
					e.spanBatch(startItem(f))
				default:
					f["ended_at"] = "2026-01-01T00:00:01Z"
					f["started_at"] = "2026-01-01T00:00:00Z"
					e.spanBatch(completeSpan(f))
				}
				if got := e.span(id)["session_id"]; got != tc.want {
					t.Errorf("%s: session_id = %v, want %v", via, got, tc.want)
				}
			}
		})
	}
}

// spans/update on an unknown span is a 404, like GET /spans/{id}.
func TestSpanUpdateUnknownIs404(t *testing.T) {
	e := newEnv(t)
	e.startSpan(map[string]any{"id": "known", "name": "f"})
	for _, tc := range []struct {
		id   string
		code int
	}{{"known", 200}, {"nope", 404}} {
		code, _, out := e.send("POST", "/api/v1/spans/update", map[string]any{"project_id": pid, "id": tc.id, "display_name": "D"})
		if code != tc.code {
			t.Errorf("update %s -> %d %s, want %d", tc.id, code, out, tc.code)
		}
	}
	// Another project's span is unknown in this one.
	if code, _, out := e.send("POST", "/api/v1/spans/update", map[string]any{"project_id": "other/p", "id": "known", "display_name": "D"}); code != 404 {
		t.Errorf("update in another project -> %d %s", code, out)
	}
}
