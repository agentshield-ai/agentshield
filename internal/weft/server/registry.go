package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/agentshield-ai/agentshield/internal/weft/store"
)

// POST /api/v1/registry/link links an object version into a registry
// collection of a target project (native_more.go); POST
// /api/v1/registry/query reads collections, versions and aliases back.

func (s *Server) registryQuery(ctx context.Context, _ *http.Request, req *store.RegistryQuery) (any, error) {
	cs, err := s.store.QueryRegistry(ctx, *req)
	if err != nil {
		return nil, err
	}
	return map[string]any{"collections": cs}, nil
}

// authorizeProject re-runs the auth middleware for a write naming project
// pid (the same key, method and path), so project-scoped, read-only and
// role-limited principals are checked against pid as well. It returns 0 when
// allowed, otherwise the status and body the middleware wrote.
func (s *Server) authorizeProject(r *http.Request, pid string) (int, []byte) {
	if !s.authRequired() {
		return 0, nil
	}
	body, _ := json.Marshal(map[string]string{"project_id": pid})
	r2 := r.Clone(r.Context())
	r2.Body = io.NopCloser(bytes.NewReader(body))
	r2.ContentLength = int64(len(body))
	r2.Header.Set("Content-Type", "application/json")
	r2.Header.Del("Content-Encoding")
	r2.URL.RawQuery = ""
	allowed := false
	rec := &captureWriter{header: http.Header{}, code: http.StatusOK}
	s.auth(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { allowed = true })).ServeHTTP(rec, r2)
	if allowed {
		return 0, nil
	}
	if rec.code < 400 {
		rec.code = http.StatusForbidden
	}
	msg := rec.buf.Bytes()
	if len(bytes.TrimSpace(msg)) == 0 || !strings.HasPrefix(strings.TrimSpace(string(msg)), "{") {
		msg, _ = json.Marshal(map[string]string{"reason": "no access to project " + pid})
	}
	return rec.code, msg
}

type captureWriter struct {
	header http.Header
	code   int
	buf    bytes.Buffer
}

func (c *captureWriter) Header() http.Header         { return c.header }
func (c *captureWriter) Write(b []byte) (int, error) { return c.buf.Write(b) }
func (c *captureWriter) WriteHeader(code int)        { c.code = code }
