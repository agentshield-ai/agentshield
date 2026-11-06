package server

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/agentshield-ai/agentshield/internal/weft/store"
)

// syncBuffer collects log output from concurrent handlers.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// logLines decodes JSON log lines.
func (s *syncBuffer) lines(t *testing.T) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(s.String()), "\n") {
		if l == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("log line is not JSON: %q", l)
		}
		out = append(out, m)
	}
	return out
}

// find returns the lines with message msg whose attributes include want.
func (s *syncBuffer) find(t *testing.T, msg string, want map[string]any) []map[string]any {
	t.Helper()
	var out []map[string]any
next:
	for _, l := range s.lines(t) {
		if l["msg"] != msg {
			continue
		}
		for k, v := range want {
			if got, ok := l[k]; !ok || jsonText(got) != jsonText(v) {
				continue next
			}
		}
		out = append(out, l)
	}
	return out
}

func jsonText(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// newLogEnv is newEnv with every log line, at debug, captured as JSON.
func newLogEnv(t *testing.T, keys ...string) (*testEnv, *syncBuffer, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "weft.db"))
	if err != nil {
		t.Fatal(err)
	}
	buf := &syncBuffer{}
	log := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	s := New(Config{APIKeys: keys, Logger: log}, st)
	hs := httptest.NewServer(s.Handler())
	t.Cleanup(func() {
		hs.Close()
		s.Close()
		_ = st.Close()
	})
	e := &testEnv{t: t, srv: hs}
	if len(keys) > 0 {
		e.key = keys[0]
		if _, k, ok := strings.Cut(e.key, "="); ok {
			e.key = k
		}
	}
	return e, buf, st
}

func TestRequestLogLevelsAndFields(t *testing.T) {
	e, buf, _ := newLogEnv(t, "alice=sekret-static-key-123")

	// A write: info, with the route, project, user and sizes.
	e.post("/api/v1/objects/save", map[string]any{"project_id": "acme/app", "name": "cfg", "value": map[string]any{"a": 1}})
	// A read: debug.
	if code, _ := e.raw("/api/v1/server", nil, nil); code != 200 {
		t.Fatalf("server -> %d", code)
	}
	// A refusal: info with the reason.
	if code, _ := e.raw("/api/v1/objects/save", map[string]any{"project_id": "not a project"}, nil); code != 400 {
		t.Fatalf("bad save -> %d", code)
	}
	// Unauthenticated: 401 at info.
	req, _ := http.NewRequest(http.MethodGet, e.srv.URL+"/api/v1/spans/stats?project_id=acme/app", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	// Health: debug.
	if code, _ := e.raw("/health", nil, nil); code != 200 {
		t.Fatal("health")
	}

	writes := buf.find(t, "request", map[string]any{"method": "POST", "route": "/api/v1/objects/save", "status": 200})
	if len(writes) != 1 {
		t.Fatalf("write lines = %d\n%s", len(writes), buf)
	}
	w := writes[0]
	if w["level"] != "INFO" || w["project"] != "acme/app" || w["user"] != "alice" || w["auth"] != "static_key" {
		t.Fatalf("write line = %v", w)
	}
	for _, k := range []string{"duration_ms", "bytes_in", "bytes_out", "remote", "req_id", "path"} {
		if _, ok := w[k]; !ok {
			t.Fatalf("write line has no %s: %v", k, w)
		}
	}
	if n, _ := w["bytes_in"].(float64); n <= 0 {
		t.Fatalf("bytes_in = %v", w["bytes_in"])
	}
	if l := buf.find(t, "request", map[string]any{"route": "/api/v1/server", "status": 200}); len(l) != 1 || l[0]["level"] != "DEBUG" {
		t.Fatalf("read lines = %v", l)
	}
	bad := buf.find(t, "request", map[string]any{"route": "/api/v1/objects/save", "status": 400})
	if len(bad) != 1 || bad[0]["level"] != "INFO" || bad[0]["reason"] == nil {
		t.Fatalf("refusal lines = %v", bad)
	}
	if l := buf.find(t, "request", map[string]any{"status": 401}); len(l) != 1 || l[0]["level"] != "INFO" {
		t.Fatalf("401 lines = %v", l)
	}
	if l := buf.find(t, "request", map[string]any{"route": "/health"}); len(l) != 1 || l[0]["level"] != "DEBUG" {
		t.Fatalf("health lines = %v", l)
	}
	if strings.Contains(buf.String(), "sekret-static-key-123") {
		t.Fatal("the API key was logged")
	}
}

func TestRequestLogServerErrorCarriesTheError(t *testing.T) {
	e, buf, st := newLogEnv(t)
	e.post("/api/v1/objects/save", map[string]any{"project_id": "acme/app", "name": "cfg", "value": 1})
	_ = st.Close() // every query now fails
	if code, _ := e.raw("/api/v1/objects/save", map[string]any{"project_id": "acme/app", "name": "cfg", "value": 2}, nil); code != 500 {
		t.Fatalf("save on a closed store -> %d", code)
	}
	l := buf.find(t, "request", map[string]any{"status": 500})
	if len(l) != 1 || l[0]["level"] != "ERROR" || l[0]["err"] == nil || l[0]["project"] != "acme/app" ||
		l[0]["route"] != "/api/v1/objects/save" {
		t.Fatalf("500 lines = %v\n%s", l, buf)
	}
}

func TestRequestLogRecoversPanics(t *testing.T) {
	buf := &syncBuffer{}
	s := &Server{log: slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))}
	h := s.requestLog(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/objects/save", strings.NewReader("{}")))
	if rec.Code != 500 {
		t.Fatalf("status = %d", rec.Code)
	}
	if len(buf.find(t, "panic serving request", nil)) != 1 || len(buf.find(t, "request", map[string]any{"status": 500, "level": "ERROR"})) != 1 {
		t.Fatalf("log = %s", buf)
	}
}
