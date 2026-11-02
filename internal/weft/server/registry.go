package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/agentshield-ai/agentshield/internal/weft/store"
	"github.com/go-chi/chi/v5"
)

// POST /link_to_registry links an object version into a registry collection
// (W&B Registry "portfolio") of a target project; POST /registry/query (a
// Weft addition) reads collections, versions and aliases back.

func init() {
	routeExtensions = append(routeExtensions, func(s *Server, r chi.Router) {
		r.Post("/link_to_registry", s.linkToRegistry)
		r.Post("/registry/query", handle(s.registryQuery))
	})
}

type linkToRegistryReq struct {
	Ref    string `json:"ref"`
	Target struct {
		PortfolioName string `json:"portfolio_name"`
		EntityName    string `json:"entity_name"`
		ProjectName   string `json:"project_name"`
	} `json:"target"`
	Aliases []string `json:"aliases"`
}

func (s *Server) linkToRegistry(w http.ResponseWriter, r *http.Request) {
	var req linkToRegistryReq
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	t := req.Target
	if req.Ref == "" || t.PortfolioName == "" || t.EntityName == "" || t.ProjectName == "" {
		writeErr(w, store.Invalidf("ref and target.portfolio_name, target.entity_name and target.project_name are required"))
		return
	}
	pid, err := project(t.EntityName + "/" + t.ProjectName)
	if err != nil {
		writeErr(w, err)
		return
	}
	// The middleware checked the ref's project; check the registry project too.
	if code, msg := s.authorizeProject(r, pid); code != 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = w.Write(msg)
		return
	}
	v, err := s.store.LinkToRegistry(r.Context(), req.Ref, pid, t.PortfolioName, req.Aliases, userFrom(r, nil))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"version_index": v})
}

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
