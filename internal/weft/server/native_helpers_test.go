package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"

	"github.com/agentshield-ai/agentshield/internal/weft/store"
	"github.com/google/uuid"
)

// Test helpers for Weft's own API (native*.go). Test servers run with the
// older route shapes off (nativeOnly), so every test that is not about the
// compatibility adapter uses the native routes; adapter tests use
// newLegacyEnv.

var (
	legacyOff = false
	legacyOn  = true
)

// nativeOnly turns the older route shapes off.
func nativeOnly(c Config) Config {
	c.LegacyRoutes = &legacyOff
	return c
}

// newLegacyEnv is newEnv with the older route shapes served (the adapter's
// routes, without the rest of the adapter), for tests of those routes.
func newLegacyEnv(t *testing.T, keys ...string) *testEnv {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "weft.db"))
	if err != nil {
		t.Fatal(err)
	}
	s := New(Config{APIKeys: keys, LegacyRoutes: &legacyOn}, st)
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

// send makes a request with any method and decodes a JSON object answer.
func (e *testEnv) send(method, path string, body any) (int, map[string]any, []byte) {
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
	out, _ := io.ReadAll(resp.Body)
	var m map[string]any
	_ = json.Unmarshal(out, &m)
	return resp.StatusCode, m, out
}

// getJSON GETs path and fails the test unless it answers 200.
func (e *testEnv) getJSON(path string) map[string]any {
	e.t.Helper()
	code, m, out := e.send(http.MethodGet, path, nil)
	if code != 200 {
		e.t.Fatalf("GET %s -> %d: %s", path, code, out)
	}
	return m
}

// startSpan starts a span (POST /api/v1/spans/start) in project pid unless
// the fields name another; it returns the span id.
func (e *testEnv) startSpan(fields map[string]any) string {
	e.t.Helper()
	if _, ok := fields["project_id"]; !ok {
		fields["project_id"] = pid
	}
	return e.post("/api/v1/spans/start", fields)["id"].(string)
}

// endSpan ends a span (POST /api/v1/spans/end).
func (e *testEnv) endSpan(fields map[string]any) {
	e.t.Helper()
	if _, ok := fields["project_id"]; !ok {
		fields["project_id"] = pid
	}
	e.post("/api/v1/spans/end", fields)
}

// spanBatch posts /api/v1/spans/batch items ({start}, {end} or {span}) for
// project pid.
func (e *testEnv) spanBatch(items ...map[string]any) []any {
	e.t.Helper()
	list := make([]any, len(items))
	for i, it := range items {
		list[i] = it
	}
	return e.post("/api/v1/spans/batch", map[string]any{"project_id": pid, "items": list})["items"].([]any)
}

// completeSpan is a {span: ...} batch item.
func completeSpan(fields map[string]any) map[string]any { return map[string]any{"span": fields} }

// startItem and endItem are {start: ...} and {end: ...} batch items.
func startItem(fields map[string]any) map[string]any { return map[string]any{"start": fields} }
func endItem(fields map[string]any) map[string]any   { return map[string]any{"end": fields} }

// span reads a span (GET /api/v1/spans/{id}) with the include_* flags set.
func (e *testEnv) span(id string, include ...string) map[string]any {
	e.t.Helper()
	q := url.Values{"project_id": {pid}}
	for _, f := range include {
		q.Set("include_"+f, "true")
	}
	return e.getJSON("/api/v1/spans/" + url.PathEscape(id) + "?" + q.Encode())
}

// spans queries spans (POST /api/v1/spans/query) and returns the items.
func (e *testEnv) spans(body map[string]any) []any {
	e.t.Helper()
	if _, ok := body["project_id"]; !ok {
		body["project_id"] = pid
	}
	return e.post("/api/v1/spans/query", body)["items"].([]any)
}

// save saves an object version and returns its ref.
func (e *testEnv) save(name string, value any) string {
	e.t.Helper()
	return e.post("/api/v1/objects/save", map[string]any{"project_id": pid, "name": name, "value": value})["ref"].(string)
}

// feedback creates feedback and returns its id.
func (e *testEnv) feedback(fields map[string]any) string {
	e.t.Helper()
	if _, ok := fields["project_id"]; !ok {
		fields["project_id"] = pid
	}
	return e.post("/api/v1/feedback/create", fields)["id"].(string)
}

// spanRef is the ref of a span of project pid.
func spanRef(id string) string { return "weft:///" + pid + "/span/" + id }

func newID() string { return uuid.Must(uuid.NewV7()).String() }
