// Package server exposes the Weft trace server HTTP API and the web UI.
package server

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/agentshield-ai/agentshield/internal/weft/evals"
	"github.com/agentshield-ai/agentshield/internal/weft/llm"
	"github.com/agentshield-ai/agentshield/internal/weft/otlp"
	"github.com/agentshield-ai/agentshield/internal/weft/refs"
	"github.com/agentshield-ai/agentshield/internal/weft/store"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
)

// Version is reported by /server_info.
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
}

// Server is the HTTP API.
type Server struct {
	cfg    Config
	store  *store.Store
	llm    *llm.Client
	engine *evals.Engine
	log    *slog.Logger
}

// New builds a server around a store.
func New(cfg Config, st *store.Store) *Server {
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	s := &Server{cfg: cfg, store: st, log: log}
	s.llm = llm.New(func(ctx context.Context, name string) (string, bool) {
		v, ok, err := st.GetSecret(ctx, name)
		return v, ok && err == nil
	}, s.resolveProvider)
	s.engine = evals.New(st, s.llm, log)
	return s
}

// Close stops background work.
func (s *Server) Close() { s.engine.Close() }

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
	r.Use(s.cors)
	r.Get("/health", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, map[string]any{"status": "ok"}) })
	r.Get("/server_info", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"version": Version, "auth_required": len(s.cfg.APIKeys) > 0, "otel_endpoint": "/otel/v1/traces"})
	})

	r.Group(func(r chi.Router) {
		r.Use(s.auth)
		r.Get("/auth/check", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, map[string]any{"ok": true}) })
		r.Get("/projects", s.listProjects)
		r.Post("/project/stats", handle(s.projectStats))

		r.Post("/call/start", handle(s.callStart))
		r.Post("/call/end", handle(s.callEnd))
		r.Post("/call/upsert_batch", handle(s.callUpsertBatch))
		r.Post("/call/read", handle(s.callRead))
		r.Post("/call/update", handle(s.callUpdate))
		r.Post("/calls/query", handle(s.callsQuery))
		r.Post("/calls/stream_query", s.callsStreamQuery)
		r.Post("/calls/query_stats", handle(s.callsQueryStats))
		r.Post("/calls/delete", handle(s.callsDelete))
		r.Post("/calls/stats", handle(s.callsStats))
		r.Post("/calls/op_counts", handle(s.opCounts))
		r.Post("/threads/query", handle(s.threadsQuery))

		r.Post("/obj/create", handle(s.objCreate))
		r.Post("/obj/read", handle(s.objRead))
		r.Post("/objs/query", handle(s.objsQuery))
		r.Post("/obj/delete", handle(s.objDelete))
		r.Post("/obj/alias/set", handle(s.aliasSet))
		r.Post("/obj/alias/remove", handle(s.aliasRemove))

		r.Post("/table/create", handle(s.tableCreate))
		r.Post("/table/update", handle(s.tableUpdate))
		r.Post("/table/query", handle(s.tableQuery))
		r.Post("/table/query_stats", handle(s.tableQueryStats))
		r.Post("/refs/read_batch", handle(s.refsReadBatch))
		r.Post("/file/create", s.fileCreate)
		r.Post("/file/content", s.fileContent)
		r.Get("/file/content", s.fileContent)

		r.Post("/feedback/create", handle(s.feedbackCreate))
		r.Post("/feedback/query", handle(s.feedbackQuery))
		r.Post("/feedback/purge", handle(s.feedbackPurge))
		r.Post("/feedback/replace", handle(s.feedbackReplace))

		r.Post("/cost/create", handle(s.costCreate))
		r.Post("/cost/query", handle(s.costQuery))
		r.Post("/cost/purge", handle(s.costPurge))

		r.Get("/llm/models", s.llmModels)
		r.Post("/completions/create", handle(s.completionsCreate))
		r.Post("/completions/create_stream", s.completionsStream)
		r.Post("/secrets/set", handle(s.secretsSet))
		r.Get("/secrets/list", s.secretsList)
		r.Post("/secrets/delete", handle(s.secretsDelete))

		r.Post("/evaluate_model", handle(s.evaluateModel))
		r.Post("/evaluation_status", handle(s.evaluationStatus))
		r.Post("/evaluations/compare", handle(s.evaluationsCompare))
		r.Post("/leaderboard/compute", handle(s.leaderboardCompute))

		r.Post("/otel/v1/traces", s.otelTraces)
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

func (s *Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && s.originAllowed(origin) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Content-Encoding, X-Weft-Api-Key, X-Weft-User, X-Weft-Project, project_id")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
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
	if k := r.Header.Get("Wandb-Api-Key"); k != "" {
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
	if r.Method == http.MethodGet && r.URL.Path == "/file/content" {
		return r.URL.Query().Get("api_key")
	}
	return ""
}

func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(s.cfg.APIKeys) == 0 {
			next.ServeHTTP(w, r)
			return
		}
		k := presentedKey(r)
		ok := false
		for _, want := range s.cfg.APIKeys {
			if k != "" && subtle.ConstantTimeCompare([]byte(k), []byte(want)) == 1 {
				ok = true
			}
		}
		if !ok {
			w.Header().Set("WWW-Authenticate", `Bearer realm="weft"`)
			writeError(w, http.StatusUnauthorized, "missing or invalid API key")
			return
		}
		next.ServeHTTP(w, r)
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
	if u := r.Header.Get("X-Weft-User"); u != "" {
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

func (s *Server) projectStats(ctx context.Context, _ *http.Request, req *projectReq) (any, error) {
	return s.store.GetProjectStats(ctx, req.ProjectID)
}

// ---------------------------------------------------------------- calls

func (s *Server) prepareStart(ctx context.Context, r *http.Request, st *store.CallStart) error {
	if st.ID == "" {
		st.ID = uuid.Must(uuid.NewV7()).String()
	}
	if st.StartedAt == "" {
		st.StartedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	st.WBUserID = userFrom(r, st.WBUserID)
	if st.TraceID == "" && st.ParentID != nil && *st.ParentID != "" {
		if pid, err := project(st.ProjectID); err == nil {
			if p, err := s.store.GetCall(ctx, pid, *st.ParentID, false, false, false); err == nil {
				st.TraceID = p.TraceID
			}
		}
	}
	if st.TraceID == "" && (st.ParentID == nil || *st.ParentID == "") {
		st.TraceID = st.ID
	}
	if st.TraceID == "" {
		return store.Invalidf("trace_id is required when the parent call is unknown")
	}
	return nil
}

type callStartReq struct {
	Start store.CallStart `json:"start"`
}

func (s *Server) callStart(ctx context.Context, r *http.Request, req *callStartReq) (any, error) {
	if err := s.prepareStart(ctx, r, &req.Start); err != nil {
		return nil, err
	}
	if err := s.store.StartCall(ctx, req.Start); err != nil {
		return nil, err
	}
	return map[string]any{"id": req.Start.ID, "trace_id": req.Start.TraceID}, nil
}

type callEndReq struct {
	End store.CallEnd `json:"end"`
}

func (s *Server) callEnd(ctx context.Context, _ *http.Request, req *callEndReq) (any, error) {
	if req.End.EndedAt == "" {
		req.End.EndedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if err := s.store.EndCall(ctx, req.End); err != nil {
		return nil, err
	}
	pid, _ := project(req.End.ProjectID)
	s.engine.Notify([]store.EndedCall{{ProjectID: pid, ID: req.End.ID}})
	return empty, nil
}

type batchReq struct {
	Batch []struct {
		Mode string `json:"mode"`
		Req  struct {
			Start *store.CallStart `json:"start"`
			End   *store.CallEnd   `json:"end"`
		} `json:"req"`
	} `json:"batch"`
}

func (s *Server) callUpsertBatch(ctx context.Context, r *http.Request, req *batchReq) (any, error) {
	if len(req.Batch) > 100000 {
		return nil, store.Invalidf("batch too large")
	}
	items := make([]store.BatchItem, len(req.Batch))
	res := make([]any, len(req.Batch))
	for i, b := range req.Batch {
		items[i] = store.BatchItem{Mode: b.Mode, Start: b.Req.Start, End: b.Req.End}
		switch {
		case b.Mode == "start" && b.Req.Start != nil:
			if err := s.prepareStart(ctx, r, b.Req.Start); err != nil {
				return nil, err
			}
			res[i] = map[string]any{"id": b.Req.Start.ID, "trace_id": b.Req.Start.TraceID}
		case b.Mode == "end" && b.Req.End != nil:
			if b.Req.End.EndedAt == "" {
				b.Req.End.EndedAt = time.Now().UTC().Format(time.RFC3339Nano)
			}
			res[i] = empty
		}
	}
	ended, err := s.store.UpsertBatch(ctx, items)
	if err != nil {
		return nil, err
	}
	s.engine.Notify(ended)
	return map[string]any{"res": res}, nil
}

type callReadReq struct {
	ProjectID          string `json:"project_id"`
	ID                 string `json:"id"`
	IncludeCosts       bool   `json:"include_costs"`
	IncludeFeedback    bool   `json:"include_feedback"`
	IncludeStorageSize bool   `json:"include_storage_size"`
}

func (s *Server) callRead(ctx context.Context, _ *http.Request, req *callReadReq) (any, error) {
	pid, err := project(req.ProjectID)
	if err != nil {
		return nil, err
	}
	c, err := s.store.GetCall(ctx, pid, req.ID, req.IncludeCosts, req.IncludeFeedback, req.IncludeStorageSize)
	if errors.Is(err, store.ErrNotFound) {
		return map[string]any{"call": nil}, nil
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"call": c}, nil
}

type callUpdateReq struct {
	ProjectID   string  `json:"project_id"`
	CallID      string  `json:"call_id"`
	DisplayName *string `json:"display_name"`
}

func (s *Server) callUpdate(ctx context.Context, _ *http.Request, req *callUpdateReq) (any, error) {
	pid, err := project(req.ProjectID)
	if err != nil {
		return nil, err
	}
	if req.DisplayName == nil {
		return nil, store.Invalidf("display_name is required")
	}
	return empty, s.store.UpdateCallDisplayName(ctx, pid, req.CallID, *req.DisplayName)
}

func (s *Server) callsQuery(ctx context.Context, _ *http.Request, req *store.CallsQuery) (any, error) {
	if req.Limit == nil {
		l := 1000
		req.Limit = &l
	}
	calls, err := s.store.ListCalls(ctx, *req)
	if err != nil {
		return nil, err
	}
	return map[string]any{"calls": calls}, nil
}

func (s *Server) callsStreamQuery(w http.ResponseWriter, r *http.Request) {
	var req store.CallsQuery
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	// Run the query fully before writing so errors produce a proper status.
	calls, err := s.store.ListCalls(r.Context(), req)
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/jsonl")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	flusher, _ := w.(http.Flusher)
	for i, c := range calls {
		if err := enc.Encode(c); err != nil {
			return
		}
		if flusher != nil && i%200 == 199 {
			flusher.Flush()
		}
	}
}

type queryStatsReq struct {
	ProjectID string             `json:"project_id"`
	Filter    *store.CallsFilter `json:"filter"`
	Query     any                `json:"query"`
	Search    string             `json:"search"`
}

func (s *Server) callsQueryStats(ctx context.Context, _ *http.Request, req *queryStatsReq) (any, error) {
	n, size, err := s.store.CountCalls(ctx, req.ProjectID, req.Filter, req.Query, req.Search)
	if err != nil {
		return nil, err
	}
	return map[string]any{"count": n, "total_storage_size_bytes": size}, nil
}

type callsDeleteReq struct {
	ProjectID string   `json:"project_id"`
	CallIDs   []string `json:"call_ids"`
}

func (s *Server) callsDelete(ctx context.Context, _ *http.Request, req *callsDeleteReq) (any, error) {
	pid, err := project(req.ProjectID)
	if err != nil {
		return nil, err
	}
	return empty, s.store.DeleteCalls(ctx, pid, req.CallIDs)
}

type callsStatsReq struct {
	ProjectID     string             `json:"project_id"`
	Filter        *store.CallsFilter `json:"filter"`
	Query         any                `json:"query"`
	Start         string             `json:"start"`
	End           string             `json:"end"`
	BucketSeconds int64              `json:"bucket_seconds"`
}

func (s *Server) callsStats(ctx context.Context, _ *http.Request, req *callsStatsReq) (any, error) {
	return s.store.ComputeCallsStats(ctx, req.ProjectID, req.Filter, req.Query, req.Start, req.End, req.BucketSeconds)
}

type opCountsReq struct {
	ProjectID      string `json:"project_id"`
	TraceRootsOnly bool   `json:"trace_roots_only"`
}

func (s *Server) opCounts(ctx context.Context, _ *http.Request, req *opCountsReq) (any, error) {
	ops, err := s.store.OpCounts(ctx, req.ProjectID, req.TraceRootsOnly)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ops": ops}, nil
}

func (s *Server) threadsQuery(ctx context.Context, _ *http.Request, req *store.ThreadsQuery) (any, error) {
	ts, err := s.store.QueryThreads(ctx, *req)
	if err != nil {
		return nil, err
	}
	return map[string]any{"threads": ts}, nil
}

// ---------------------------------------------------------------- objects

type objCreateReq struct {
	Obj struct {
		ProjectID          string          `json:"project_id"`
		ObjectID           string          `json:"object_id"`
		Val                json.RawMessage `json:"val"`
		BuiltinObjectClass string          `json:"builtin_object_class"`
		WBUserID           *string         `json:"wb_user_id"`
	} `json:"obj"`
}

func (s *Server) objCreate(ctx context.Context, r *http.Request, req *objCreateReq) (any, error) {
	o := req.Obj
	pid, err := project(o.ProjectID)
	if err != nil {
		return nil, err
	}
	if len(o.Val) == 0 || string(o.Val) == "null" {
		return nil, store.Invalidf("val is required")
	}
	d, v, err := s.store.CreateObjectRaw(ctx, pid, o.ObjectID, nil, o.Val, o.BuiltinObjectClass, userFrom(r, o.WBUserID))
	if err != nil {
		return nil, err
	}
	s.engine.InvalidateMonitors(pid)
	return map[string]any{"digest": d, "object_id": o.ObjectID, "version_index": v}, nil
}

type objReadReq struct {
	ProjectID string `json:"project_id"`
	ObjectID  string `json:"object_id"`
	Digest    string `json:"digest"`
}

func (s *Server) objRead(ctx context.Context, _ *http.Request, req *objReadReq) (any, error) {
	o, err := s.store.ReadObject(ctx, req.ProjectID, req.ObjectID, req.Digest)
	if err != nil {
		return nil, err
	}
	return map[string]any{"obj": o}, nil
}

func (s *Server) objsQuery(ctx context.Context, _ *http.Request, req *store.ObjsQuery) (any, error) {
	objs, err := s.store.QueryObjects(ctx, *req)
	if err != nil {
		return nil, err
	}
	return map[string]any{"objs": objs}, nil
}

type objDeleteReq struct {
	ProjectID string   `json:"project_id"`
	ObjectID  string   `json:"object_id"`
	Digests   []string `json:"digests"`
}

func (s *Server) objDelete(ctx context.Context, _ *http.Request, req *objDeleteReq) (any, error) {
	n, err := s.store.DeleteObject(ctx, req.ProjectID, req.ObjectID, req.Digests)
	if err != nil {
		return nil, err
	}
	pid, _ := project(req.ProjectID)
	s.engine.InvalidateMonitors(pid)
	return map[string]any{"num_deleted": n}, nil
}

type aliasReq struct {
	ProjectID string `json:"project_id"`
	ObjectID  string `json:"object_id"`
	Digest    string `json:"digest"`
	Alias     string `json:"alias"`
}

func (s *Server) aliasSet(ctx context.Context, _ *http.Request, req *aliasReq) (any, error) {
	return empty, s.store.SetAlias(ctx, req.ProjectID, req.ObjectID, req.Digest, req.Alias)
}

func (s *Server) aliasRemove(ctx context.Context, _ *http.Request, req *aliasReq) (any, error) {
	return empty, s.store.RemoveAlias(ctx, req.ProjectID, req.ObjectID, req.Alias)
}

// ---------------------------------------------------------------- tables, refs, files

type tableCreateReq struct {
	Table struct {
		ProjectID string            `json:"project_id"`
		Rows      []json.RawMessage `json:"rows"`
	} `json:"table"`
}

func (s *Server) tableCreate(ctx context.Context, _ *http.Request, req *tableCreateReq) (any, error) {
	d, rows, err := s.store.CreateTableRaw(ctx, req.Table.ProjectID, req.Table.Rows)
	if err != nil {
		return nil, err
	}
	return map[string]any{"digest": d, "row_digests": rows}, nil
}

type tableUpdateReq struct {
	ProjectID  string              `json:"project_id"`
	BaseDigest string              `json:"base_digest"`
	Updates    []store.TableUpdate `json:"updates"`
}

func (s *Server) tableUpdate(ctx context.Context, _ *http.Request, req *tableUpdateReq) (any, error) {
	d, rows, err := s.store.UpdateTable(ctx, req.ProjectID, req.BaseDigest, req.Updates)
	if err != nil {
		return nil, err
	}
	return map[string]any{"digest": d, "updated_row_digests": rows}, nil
}

func (s *Server) tableQuery(ctx context.Context, _ *http.Request, req *store.TableQuery) (any, error) {
	rows, err := s.store.QueryTable(ctx, *req)
	if err != nil {
		return nil, err
	}
	return map[string]any{"rows": rows}, nil
}

type tableStatsReq struct {
	ProjectID string `json:"project_id"`
	Digest    string `json:"digest"`
}

func (s *Server) tableQueryStats(ctx context.Context, _ *http.Request, req *tableStatsReq) (any, error) {
	n, err := s.store.TableCount(ctx, req.ProjectID, req.Digest)
	if err != nil {
		return nil, err
	}
	return map[string]any{"count": n}, nil
}

type refsReq struct {
	Refs []string `json:"refs"`
}

func (s *Server) refsReadBatch(ctx context.Context, _ *http.Request, req *refsReq) (any, error) {
	if len(req.Refs) > 1000 {
		return nil, store.Invalidf("too many refs (max 1000)")
	}
	vals, err := s.store.ReadRefs(ctx, req.Refs)
	if err != nil {
		return nil, err
	}
	return map[string]any{"vals": vals}, nil
}

func (s *Server) fileCreate(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 65<<20)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "expected multipart form with project_id and file")
		return
	}
	f, _, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "missing file")
		return
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil {
		writeError(w, http.StatusBadRequest, "reading file")
		return
	}
	d, err := s.store.CreateFile(r.Context(), r.FormValue("project_id"), b)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"digest": d})
}

func (s *Server) fileContent(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProjectID string `json:"project_id"`
		Digest    string `json:"digest"`
	}
	if r.Method == http.MethodGet {
		req.ProjectID, req.Digest = r.URL.Query().Get("project_id"), r.URL.Query().Get("digest")
	} else if err := decodeBody(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	b, ctype, err := s.store.FileContent(r.Context(), req.ProjectID, req.Digest)
	if err != nil {
		writeErr(w, err)
		return
	}
	if r.Method == http.MethodPost {
		ctype = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	_, _ = w.Write(b)
}

// ---------------------------------------------------------------- feedback

func (s *Server) feedbackCreate(ctx context.Context, r *http.Request, req *store.FeedbackCreate) (any, error) {
	req.WBUserID = userFrom(r, req.WBUserID)
	f, err := s.store.CreateFeedback(ctx, *req)
	if err != nil {
		return nil, err
	}
	return map[string]any{"id": f.ID, "created_at": f.CreatedAt, "wb_user_id": f.WBUserID, "payload": f.Payload}, nil
}

type feedbackQueryReq struct {
	ProjectID string         `json:"project_id"`
	Query     any            `json:"query"`
	SortBy    []store.SortBy `json:"sort_by"`
	Limit     int            `json:"limit"`
	Offset    int            `json:"offset"`
}

func (s *Server) feedbackQuery(ctx context.Context, _ *http.Request, req *feedbackQueryReq) (any, error) {
	fs, err := s.store.QueryFeedback(ctx, req.ProjectID, req.Query, req.SortBy, req.Limit, req.Offset)
	if err != nil {
		return nil, err
	}
	return map[string]any{"result": fs}, nil
}

type feedbackPurgeReq struct {
	ProjectID string `json:"project_id"`
	Query     any    `json:"query"`
}

func (s *Server) feedbackPurge(ctx context.Context, _ *http.Request, req *feedbackPurgeReq) (any, error) {
	return empty, s.store.PurgeFeedback(ctx, req.ProjectID, req.Query)
}

type feedbackReplaceReq struct {
	store.FeedbackCreate
	FeedbackID string `json:"feedback_id"`
}

func (s *Server) feedbackReplace(ctx context.Context, r *http.Request, req *feedbackReplaceReq) (any, error) {
	req.WBUserID = userFrom(r, req.WBUserID)
	f, err := s.store.ReplaceFeedback(ctx, req.FeedbackID, req.FeedbackCreate)
	if err != nil {
		return nil, err
	}
	return map[string]any{"id": f.ID, "created_at": f.CreatedAt, "wb_user_id": f.WBUserID, "payload": f.Payload}, nil
}

// ---------------------------------------------------------------- costs

type costCreateReq struct {
	ProjectID string                     `json:"project_id"`
	Costs     map[string]store.CostInput `json:"costs"`
}

func (s *Server) costCreate(ctx context.Context, _ *http.Request, req *costCreateReq) (any, error) {
	pid, err := project(req.ProjectID)
	if err != nil {
		return nil, err
	}
	ids, err := s.store.CreateCosts(ctx, pid, req.Costs)
	if err != nil {
		return nil, err
	}
	if ids == nil {
		ids = [][2]string{}
	}
	return map[string]any{"ids": ids}, nil
}

type costQueryReq struct {
	ProjectID       string `json:"project_id"`
	Query           any    `json:"query"`
	Limit           int    `json:"limit"`
	Offset          int    `json:"offset"`
	IncludeDefaults bool   `json:"include_defaults"`
}

func (s *Server) costQuery(ctx context.Context, _ *http.Request, req *costQueryReq) (any, error) {
	pid, err := project(req.ProjectID)
	if err != nil {
		return nil, err
	}
	ps, err := s.store.QueryCosts(ctx, pid, req.Query, req.Limit, req.Offset)
	if err != nil {
		return nil, err
	}
	if req.IncludeDefaults {
		ps = append(ps, store.DefaultPriceList()...)
	}
	return map[string]any{"results": ps}, nil
}

type costPurgeReq struct {
	ProjectID string   `json:"project_id"`
	IDs       []string `json:"ids"`
	Query     any      `json:"query"`
}

func (s *Server) costPurge(ctx context.Context, _ *http.Request, req *costPurgeReq) (any, error) {
	pid, err := project(req.ProjectID)
	if err != nil {
		return nil, err
	}
	return empty, s.store.PurgeCosts(ctx, pid, req.IDs, req.Query)
}

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
		out["weave_call_id"] = callID
	}
	return out, nil
}

func (s *Server) completionsStream(w http.ResponseWriter, r *http.Request) {
	var req completionsReq
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	pid, err := project(req.ProjectID)
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/jsonl")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher, _ := w.(http.Flusher)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	started := time.Now()
	final, streamErr := s.llm.Stream(r.Context(), pid, req.Inputs, func(ch map[string]any) error {
		if err := enc.Encode(ch); err != nil {
			return err
		}
		if flusher != nil {
			flusher.Flush()
		}
		return nil
	})
	if streamErr != nil {
		_ = enc.Encode(map[string]any{"error": streamErr.Error()})
	}
	meta := map[string]any{}
	if req.TrackLLMCall == nil || *req.TrackLLMCall {
		id, err := s.engine.RecordStreamedCompletion(context.WithoutCancel(r.Context()), pid, req.Inputs, started, final, streamErr)
		if err == nil {
			meta["weave_call_id"] = id
		}
	}
	_ = enc.Encode(map[string]any{"_meta": meta})
	if flusher != nil {
		flusher.Flush()
	}
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

type evaluateReq struct {
	ProjectID     string  `json:"project_id"`
	EvaluationRef string  `json:"evaluation_ref"`
	ModelRef      string  `json:"model_ref"`
	WBUserID      *string `json:"wb_user_id"`
}

func (s *Server) evaluateModel(ctx context.Context, r *http.Request, req *evaluateReq) (any, error) {
	id, err := s.engine.EvaluateModel(ctx, req.ProjectID, req.EvaluationRef, req.ModelRef, userFrom(r, req.WBUserID))
	if err != nil {
		return nil, err
	}
	return map[string]any{"call_id": id}, nil
}

type evalStatusReq struct {
	ProjectID string `json:"project_id"`
	CallID    string `json:"call_id"`
}

func (s *Server) evaluationStatus(ctx context.Context, _ *http.Request, req *evalStatusReq) (any, error) {
	pid, err := project(req.ProjectID)
	if err != nil {
		return nil, err
	}
	st, err := s.engine.EvaluationStatus(ctx, pid, req.CallID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"status": st}, nil
}

type compareReq struct {
	ProjectID         string   `json:"project_id"`
	EvaluationCallIDs []string `json:"evaluation_call_ids"`
}

func (s *Server) evaluationsCompare(ctx context.Context, _ *http.Request, req *compareReq) (any, error) {
	return s.engine.Compare(ctx, req.ProjectID, req.EvaluationCallIDs)
}

type leaderboardReq struct {
	ProjectID string `json:"project_id"`
	ObjectID  string `json:"object_id"`
	Digest    string `json:"digest"`
}

func (s *Server) leaderboardCompute(ctx context.Context, _ *http.Request, req *leaderboardReq) (any, error) {
	return s.engine.ComputeLeaderboard(ctx, req.ProjectID, req.ObjectID, req.Digest)
}

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
