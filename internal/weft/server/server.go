// Package server exposes the Weft trace server HTTP API and the web UI.
package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/agentshield-ai/agentshield/internal/weft/compat"
	"github.com/agentshield-ai/agentshield/internal/weft/evals"
	"github.com/agentshield-ai/agentshield/internal/weft/llm"
	"github.com/agentshield-ai/agentshield/internal/weft/otlp"
	"github.com/agentshield-ai/agentshield/internal/weft/refs"
	"github.com/agentshield-ai/agentshield/internal/weft/store"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// routeExtensions let feature modules register authenticated routes from
// their own files: func init() { routeExtensions = append(routeExtensions, ...) }.
var routeExtensions []func(*Server, chi.Router)

// publicRouteExtensions register routes that need no authentication (SSO).
var publicRouteExtensions []func(*Server, chi.Router)

// startExtensions start background work for feature modules when a server is
// built; the returned stop functions run on Close.
var startExtensions []func(*Server) (stop func())

// Store exposes the store to feature modules.
func (s *Server) Store() *store.Store { return s.store }

// Version is reported by GET /api/v1/server.
const Version = "0.1.0"

// Config configures the server.
type Config struct {
	// APIKeys, when non-empty, are required on every API request.
	APIKeys []string
	// CORSOrigins lists browser origins allowed to call the API ("*" for any).
	CORSOrigins []string
	// UI serves the web app; nil disables it.
	UI     http.Handler
	Logger *slog.Logger
	// IAM configures users, sessions, SSO and the audit log (iam.go).
	IAM IAMConfig
	// Compat turns on the compatibility adapter for clients of another
	// tracing product (package compat, docs/weft/COMPATIBILITY.md).
	Compat bool
}

// Server is the HTTP API.
type Server struct {
	cfg    Config
	keys   []apiKey
	store  *store.Store
	llm    *llm.Client
	engine *evals.Engine
	log    *slog.Logger
	stops  []func()
	// compat is the compatibility adapter; nil when it is off.
	compat *compat.Adapter
	// legacy serves the older route shapes, which exist only with the
	// adapter; nil otherwise.
	legacy *compat.Routes
	// native serves the /api/v1 routes without middleware, for the older
	// routes, which forward to them after authentication.
	native http.Handler
}

// New builds a server around a store.
func New(cfg Config, st *store.Store) *Server {
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	s := &Server{cfg: cfg, store: st, log: log, keys: parseKeys(cfg.APIKeys)}
	s.llm = llm.New(func(ctx context.Context, name string) (string, bool) {
		v, ok, err := st.GetSecret(ctx, name)
		return v, ok && err == nil
	}, s.resolveProvider)
	s.engine = evals.New(st, s.llm, log)
	if cfg.Compat {
		s.compat = compat.New(compatHost{s})
	}
	if cfg.Compat {
		s.legacy = compat.NewRoutes(compatHost{s}, s.compat)
	}
	nr := chi.NewRouter()
	s.mountNative(nr)
	s.native = nr
	for _, start := range startExtensions {
		s.stops = append(s.stops, start(s))
	}
	return s
}

// Close stops background work.
func (s *Server) Close() {
	for _, stop := range s.stops {
		stop()
	}
	s.engine.Close()
}

// Engine exposes the evaluation engine (tests).
func (s *Server) Engine() *evals.Engine { return s.engine }

func (s *Server) resolveProvider(ctx context.Context, projectID, name string) (*llm.CustomProvider, error) {
	if projectID == "" {
		return nil, nil
	}
	o, err := s.store.ReadObject(ctx, projectID, name, "latest")
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if o.BaseObjectClass == nil || *o.BaseObjectClass != "Provider" {
		return nil, nil
	}
	v, _ := o.Val.(map[string]any)
	cp := &llm.CustomProvider{Name: name, ExtraHeaders: map[string]string{}}
	cp.BaseURL, _ = v["base_url"].(string)
	cp.APIKeyName, _ = v["api_key_name"].(string)
	if h, ok := v["extra_headers"].(map[string]any); ok {
		for k, x := range h {
			cp.ExtraHeaders[k] = fmt.Sprint(x)
		}
	}
	if cp.BaseURL == "" {
		return nil, fmt.Errorf("provider %q has no base_url", name)
	}
	return cp, nil
}

// Handler returns the root HTTP handler.
func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(s.requestLog)
	if s.compat != nil {
		r.Use(s.compat.Middleware)
	}
	r.Use(s.cors)
	r.Get("/health", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, map[string]any{"status": "ok"}) })
	r.Get("/api/v1/server", s.v1Server)
	if s.compat != nil {
		s.compat.MountPublic(r)
	}
	if s.legacy != nil {
		s.legacy.MountPublic(r)
	}

	for _, ext := range publicRouteExtensions {
		ext(s, r)
	}
	r.Group(func(r chi.Router) {
		r.Use(s.auditLog) // audit.go; records the outcome of auth too
		r.Use(s.auth)
		r.Get("/auth/check", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, map[string]any{"ok": true}) })
		r.Get("/auth/whoami", func(w http.ResponseWriter, r *http.Request) {
			u := "anonymous"
			if p := userFrom(r, nil); p != nil {
				u = *p
			}
			writeJSON(w, map[string]any{"user_id": u, "auth_required": s.authRequired()})
		})
		r.Get("/projects", s.listProjects)
		s.mountNative(r)
		r.Get("/llm/models", s.llmModels)
		r.Post("/secrets/set", handle(s.secretsSet))
		r.Get("/secrets/list", s.secretsList)
		r.Post("/secrets/delete", handle(s.secretsDelete))

		r.Post("/otel/v1/traces", s.otelTraces)
		for _, ext := range routeExtensions {
			ext(s, r)
		}
		if s.compat != nil {
			s.compat.MountAuthed(r, s.legacy)
		}
		if s.legacy != nil {
			s.legacy.Mount(r)
		}
	})

	r.NotFound(func(w http.ResponseWriter, req *http.Request) {
		if s.cfg.UI != nil && (req.Method == http.MethodGet || req.Method == http.MethodHead) {
			s.cfg.UI.ServeHTTP(w, req)
			return
		}
		writeError(w, http.StatusNotFound, "not found")
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, req *http.Request) {
		if s.cfg.UI != nil && req.Method == http.MethodGet {
			s.cfg.UI.ServeHTTP(w, req)
			return
		}
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	})
	return r
}

// ---------------------------------------------------------------- middleware

// requestLog records each request at debug level (method, path, status, duration).
func (s *Server) requestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.log.Enabled(r.Context(), slog.LevelDebug) {
			next.ServeHTTP(w, r)
			return
		}
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		start := time.Now()
		next.ServeHTTP(ww, r)
		status := ww.Status()
		if status == 0 { // the handler wrote nothing: net/http sends 200
			status = http.StatusOK
		}
		s.log.Debug("request", "method", r.Method, "path", r.URL.Path, "status", status, "ms", time.Since(start).Milliseconds())
	})
}

func (s *Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && s.originAllowed(origin) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Content-Encoding, X-Weft-Api-Key, X-Weft-User, X-Weft-Project, project_id")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) originAllowed(origin string) bool {
	for _, o := range s.cfg.CORSOrigins {
		if o == "*" || strings.EqualFold(o, origin) {
			return true
		}
	}
	return false
}

func presentedKey(r *http.Request) string {
	if k := r.Header.Get("X-Weft-Api-Key"); k != "" {
		return k
	}
	auth := r.Header.Get("Authorization")
	switch {
	case strings.HasPrefix(auth, "Bearer "):
		return strings.TrimSpace(auth[7:])
	case strings.HasPrefix(auth, "Basic "):
		raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(auth[6:]))
		if err != nil {
			return ""
		}
		_, pass, _ := strings.Cut(string(raw), ":")
		return pass
	}
	// Browsers cannot set headers on <img src>; allow a query key for file reads only.
	if r.Method == http.MethodGet && (r.URL.Path == "/file/content" || strings.HasPrefix(r.URL.Path, "/api/v1/files/")) {
		return r.URL.Query().Get("api_key")
	}
	return ""
}

// apiKey is a configured key, optionally bound to a user name ("alice=key").
type apiKey struct {
	user string
	key  []byte
}

var keyUserRe = regexp.MustCompile(`^[A-Za-z0-9_.@-]{1,64}$`)

func parseKeys(raw []string) []apiKey {
	out := make([]apiKey, 0, len(raw))
	for _, k := range raw {
		if name, key, ok := strings.Cut(k, "="); ok && key != "" && keyUserRe.MatchString(name) {
			out = append(out, apiKey{user: name, key: []byte(key)})
			continue
		}
		out = append(out, apiKey{key: []byte(k)})
	}
	return out
}

type ctxKey struct{}

// auth authenticates (static keys, managed keys, session cookies) and
// enforces roles; see authenticate in iam.go.
func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r = s.authenticate(w, r); r != nil {
			next.ServeHTTP(w, r)
		}
	})
}

// ---------------------------------------------------------------- plumbing

const maxJSONBody = 64 << 20

func decodeBody(r *http.Request, v any) error {
	body, err := otlp.ReadBody(http.MaxBytesReader(nil, r.Body, maxJSONBody), r.Header.Get("Content-Encoding"))
	if err != nil {
		return store.Invalidf("reading body: %v", err)
	}
	if len(bytes.TrimSpace(body)) == 0 {
		body = []byte("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(v); err != nil {
		return store.Invalidf("invalid JSON body: %v", err)
	}
	return nil
}

func handle[T any](fn func(context.Context, *http.Request, *T) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		req := new(T)
		if err := decodeBody(r, req); err != nil {
			writeErr(w, err)
			return
		}
		out, err := fn(r.Context(), r, req)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, out)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"reason": msg})
}

func writeErr(w http.ResponseWriter, err error) {
	var inv *store.InvalidError
	switch {
	case errors.As(err, &inv):
		writeError(w, http.StatusBadRequest, inv.Msg)
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	case errors.Is(err, context.Canceled):
		writeError(w, 499, "request cancelled")
	default:
		slog.Error("weft request failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}

func project(p string) (string, error) {
	pid, err := refs.NormalizeProject(p)
	if err != nil {
		return "", &store.InvalidError{Msg: err.Error()}
	}
	return pid, nil
}

func userFrom(r *http.Request, explicit *string) *string {
	if explicit != nil && *explicit != "" {
		return explicit
	}
	if u, ok := r.Context().Value(ctxKey{}).(string); ok && u != "" {
		return &u
	}
	if u := r.Header.Get("X-Weft-User"); u != "" && len(u) <= 128 {
		return &u
	}
	return nil
}

var empty = map[string]any{}

// ---------------------------------------------------------------- projects

func (s *Server) listProjects(w http.ResponseWriter, r *http.Request) {
	ps, err := s.store.ListProjects(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"projects": ps})
}

type projectReq struct {
	ProjectID string `json:"project_id"`
}

// ---------------------------------------------------------------- calls

type callsStatsReq struct {
	ProjectID     string             `json:"project_id"`
	Search        string             `json:"search"`
	Filter        *store.CallsFilter `json:"filter"`
	Query         any                `json:"query"`
	Start         string             `json:"start"`
	End           string             `json:"end"`
	BucketSeconds int64              `json:"bucket_seconds"`
	// Metric-bucket fields; when any is present the response has
	// usage_buckets/span_buckets, see apishape.go.
	metricStatsFields
}

func (s *Server) callsStats(ctx context.Context, _ *http.Request, req *callsStatsReq) (any, error) {
	if req.isMetricStats() {
		return s.metricCallsStats(ctx, req)
	}
	return s.store.ComputeCallsStats(ctx, req.ProjectID, req.Filter, req.Query, req.Search, req.Start, req.End, req.BucketSeconds)
}

// ---------------------------------------------------------------- objects

// ---------------------------------------------------------------- tables, refs, files

// ---------------------------------------------------------------- feedback

// ---------------------------------------------------------------- costs

// ---------------------------------------------------------------- llm

func (s *Server) llmModels(w http.ResponseWriter, r *http.Request) {
	models := s.llm.Models(r.Context())
	if pid, err := project(r.URL.Query().Get("project_id")); err == nil {
		objs, err := s.store.QueryObjects(r.Context(), store.ObjsQuery{ProjectID: pid,
			Filter: &store.ObjsFilter{BaseObjectClasses: []string{"ProviderModel"}, LatestOnly: true}})
		if err == nil {
			for _, o := range objs {
				v, _ := o.Val.(map[string]any)
				provRef, _ := v["provider"].(string)
				name, _ := v["name"].(string)
				if name == "" {
					name = o.ObjectID
				}
				prov := provRef
				if pr, err := refs.Parse(provRef); err == nil {
					prov = pr.Name
				}
				mt := 4096
				if f, ok := store.ToFloat(v["max_tokens"]); ok {
					mt = int(f)
				}
				id := prov + "/" + name
				models = append(models, llm.ModelInfo{ID: id, Provider: prov, Label: id, Available: true, MaxTokens: mt})
			}
		}
	}
	writeJSON(w, map[string]any{"models": models})
}

type completionsReq struct {
	ProjectID    string         `json:"project_id"`
	Inputs       map[string]any `json:"inputs"`
	TrackLLMCall *bool          `json:"track_llm_call"`
}

func (s *Server) completionsCreate(ctx context.Context, _ *http.Request, req *completionsReq) (any, error) {
	pid, err := project(req.ProjectID)
	if err != nil {
		return nil, err
	}
	if req.Inputs == nil {
		return nil, store.Invalidf("inputs are required")
	}
	track := req.TrackLLMCall == nil || *req.TrackLLMCall
	var resp map[string]any
	var callID string
	if track {
		resp, callID, err = s.engine.TrackedCompletion(ctx, pid, nil, req.Inputs, false)
	} else {
		resp, err = s.llm.Complete(ctx, pid, req.Inputs)
	}
	out := map[string]any{}
	if err != nil {
		out["response"] = map[string]any{"error": err.Error()}
	} else {
		out["response"] = resp
	}
	if callID != "" {
		out["call_id"] = callID
	}
	return out, nil
}

type secretReq struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

func (s *Server) secretsSet(ctx context.Context, _ *http.Request, req *secretReq) (any, error) {
	return empty, s.store.SetSecret(ctx, req.Name, req.Value)
}

func (s *Server) secretsDelete(ctx context.Context, _ *http.Request, req *secretReq) (any, error) {
	return empty, s.store.DeleteSecret(ctx, req.Name)
}

// envSecretNames are environment variables reported by /secrets/list.
var envSecretNames = []string{"OPENAI_API_KEY", "OPENAI_BASE_URL", "ANTHROPIC_API_KEY", "GEMINI_API_KEY", "GROQ_API_KEY",
	"MISTRAL_API_KEY", "DEEPSEEK_API_KEY", "XAI_API_KEY", "OPENROUTER_API_KEY", "TOGETHER_API_KEY", "OLLAMA_BASE_URL"}

func (s *Server) secretsList(w http.ResponseWriter, r *http.Request) {
	names, err := s.store.ListSecrets(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	type entry struct {
		Name   string `json:"name"`
		Source string `json:"source"`
	}
	seen := map[string]bool{}
	out := []entry{}
	for _, n := range names {
		seen[n] = true
		out = append(out, entry{n, "db"})
	}
	for _, n := range envSecretNames {
		if !seen[n] && os.Getenv(n) != "" {
			out = append(out, entry{n, "env"})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	writeJSON(w, map[string]any{"secrets": out})
}

// ---------------------------------------------------------------- evaluations

// ---------------------------------------------------------------- OTLP

func (s *Server) otelTraces(w http.ResponseWriter, r *http.Request) {
	isJSON := otlp.IsJSON(r.Header.Get("Content-Type"))
	body, err := otlp.ReadBody(r.Body, r.Header.Get("Content-Encoding"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	req, err := otlp.Decode(body, isJSON)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	hp := r.Header.Get("project_id")
	if hp == "" {
		hp = r.Header.Get("X-Weft-Project")
	}
	if s.compat != nil {
		compat.RewriteOTLP(req)
	}
	calls, err := otlp.ToCalls(req, hp)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ended, err := s.store.UpsertComplete(r.Context(), calls)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.engine.Notify(ended)
	resp, ctype := otlp.EncodeResponse(isJSON, 0, "")
	w.Header().Set("Content-Type", ctype)
	_, _ = w.Write(resp)
}

// cumulativeSummaries reports a request whose call summaries already include
// their descendants (X-Weft-Summary: cumulative), so they are stored as sent
// instead of being rolled up again.
func cumulativeSummaries(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("X-Weft-Summary"), "cumulative")
}

// compatHost gives the compatibility adapter what it needs from the server.
type compatHost struct{ s *Server }

func (h compatHost) Store() *store.Store                { return h.s.store }
func (h compatHost) Engine() *evals.Engine              { return h.s.engine }
func (h compatHost) LLM() *llm.Client                   { return h.s.llm }
func (h compatHost) Logger() *slog.Logger               { return h.s.log }
func (h compatHost) Version() string                    { return Version }
func (h compatHost) AuthRequired() bool                 { return h.s.authRequired() }
func (h compatHost) CallsEnded(ended []store.EndedCall) { h.s.engine.Notify(ended) }
func (h compatHost) Native() http.Handler               { return h.s.native }
func (h compatHost) User(r *http.Request, explicit *string) *string {
	return userFrom(r, explicit)
}
func (h compatHost) PrepareStart(ctx context.Context, r *http.Request, st *store.CallStart) error {
	return h.s.PrepareStart(ctx, r, st)
}
func (h compatHost) AuthorizeProject(r *http.Request, pid string) (int, []byte) {
	return h.s.authorizeProject(r, pid)
}
func (h compatHost) ExportCalls(w http.ResponseWriter, r *http.Request, q store.CallsQuery, format string, flatten *bool) {
	h.s.exportCalls(w, r, callsExportReq{CallsQuery: q, Format: format, Flatten: flatten}, false)
}
