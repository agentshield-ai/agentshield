package compat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/agentshield-ai/agentshield/internal/weft/compat/legacy"
	"github.com/agentshield-ai/agentshield/internal/weft/evals"
	"github.com/agentshield-ai/agentshield/internal/weft/llm"
	"github.com/agentshield-ai/agentshield/internal/weft/otlp"
	"github.com/agentshield-ai/agentshield/internal/weft/refs"
	"github.com/agentshield-ai/agentshield/internal/weft/store"
	"github.com/agentshield-ai/agentshield/internal/weft/weftlog"
	"github.com/go-chi/chi/v5"
)

// The route shapes Weft served before its own API (docs/weft/DESIGN.md
// §10): /call/*, /calls/*, /obj/*, /objs/*, /table/*, /refs/*, /file/*,
// /files/*, /feedback/*, /cost/*, /threads/*, /trace/*, /completions/*,
// /evaluate_model, /evaluation_status, /evaluations/*, /leaderboard/*,
// /link_to_registry, /registry/*, /dataset_sources/*, /embeddings/create,
// /image/create, /project/stats, /tags, /aliases, /v2/{entity}/{project}/…,
// /v1/entities/…:export, /inference/*, /geolocate, /version and
// /server_info. They are the weave client's shapes, so the adapter serves
// them, translating to Weft's API or calling the same internals.
//
// These routes:
//
//   - compute new digests in the older form (legacy.Digester), which the
//     weave client computes itself;
//   - write refs of functions and spans with their older kind names ("op",
//     "call") in responses (legacy.RefKindsOut); Weft reads both.
//
// They are mounted with --weave-compat, and also while the server's
// LegacyRoutes switch is on (a migration aid for Weft's own SDKs and UI
// until they use the native API).

// Host is what the adapter needs from the server.
type Host interface {
	Store() *store.Store
	Engine() *evals.Engine
	LLM() *llm.Client
	Logger() *slog.Logger
	Version() string
	AuthRequired() bool
	// PrepareStart fills a call start's defaults (id, start time, user,
	// trace id), like POST /api/v1/spans/start.
	PrepareStart(ctx context.Context, r *http.Request, st *store.CallStart) error
	// CallsEnded runs the post-ingest work (monitors, automations) for calls
	// that ended.
	CallsEnded([]store.EndedCall)
	// User returns explicit when set, else the authenticated user (or nil).
	User(r *http.Request, explicit *string) *string
	// AuthorizeProject checks the caller may write to project pid (0 when
	// allowed, otherwise the status and body to answer with).
	AuthorizeProject(r *http.Request, pid string) (int, []byte)
	// AuthorizeWrite checks the caller may write to project pid whatever
	// the route (nil when allowed): /graphql is a read route, but its
	// upsertModel creates the project.
	AuthorizeWrite(r *http.Request, pid string) error
	// ExportCalls streams calls as CSV, JSONL or JSON with the stored
	// field names (POST /api/v1/spans/export uses Weft's).
	ExportCalls(w http.ResponseWriter, r *http.Request, q store.CallsQuery, format string, flatten *bool)
	// Native serves Weft's own API (the /api/v1 routes), for requests that
	// already passed authentication.
	Native() http.Handler
}

// Routes serves the older route shapes.
type Routes struct {
	host    Host
	store   *store.Store
	engine  *evals.Engine
	llm     *llm.Client
	log     *slog.Logger
	adapter *Adapter // nil unless --weave-compat
}

// NewRoutes builds the older routes for a server; a is the adapter, or nil
// when it is off (the routes are then served for the LegacyRoutes switch).
func NewRoutes(h Host, a *Adapter) *Routes {
	return &Routes{host: h, store: h.Store(), engine: h.Engine(), llm: h.LLM(), log: h.Logger(), adapter: a}
}

// MountPublic registers the older routes that need no API key.
func (s *Routes) MountPublic(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(legacyResponses)
		r.Get("/server_info", s.serverInfo)
		r.Get("/version", func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, map[string]any{"version": s.host.Version(), "server": "weft", "trace_server_version": s.host.Version()})
		})
		r.Get("/geolocate", s.geolocate)
	})
}

// serverInfo is GET /server_info (Weft's own is GET /api/v1/server).
func (s *Routes) serverInfo(w http.ResponseWriter, r *http.Request) {
	info := map[string]any{"version": s.host.Version(), "auth_required": s.host.AuthRequired(), "otel_endpoint": "/otel/v1/traces",
		"trace_server_version": s.host.Version()}
	if s.adapter != nil {
		s.adapter.ServerInfo(info)
	}
	writeJSON(w, info)
}

// Mount registers the older routes that need authentication.
func (s *Routes) Mount(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(legacyResponses)
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
		r.Post("/calls/stats", s.callsStats)
		r.Post("/calls/metrics", s.forwardFiltered("/api/v1/spans/metrics"))
		r.Post("/calls/op_counts", handle(s.opCounts))
		r.Post("/calls/score", handle(s.callsScore))
		r.Post("/calls/export", s.callsExport)
		r.Post("/calls/usage", handle(s.callsUsage))
		r.Post("/trace/usage", handle(s.traceUsage))
		r.Post("/threads/query", handle(s.threadsQuery))
		r.Post("/threads/stream_query", s.threadsStreamQuery)

		r.Post("/obj/create", handle(s.objCreate))
		r.Post("/obj/read", handle(s.objRead))
		r.Post("/objs/query", handle(s.objsQuery))
		r.Post("/obj/delete", handle(s.objDelete))
		r.Post("/obj/alias/set", handle(s.aliasSet))
		r.Post("/obj/alias/remove", handle(s.aliasRemove))
		r.Get("/tags", s.tagsList)
		r.Get("/aliases", s.aliasesList)
		r.Put("/objs/{object_id}/aliases", handle(s.objAliasesSet))
		r.Post("/objs/{object_id}/aliases", handle(s.objAliasesSet))
		r.Post("/objs/{object_id}/aliases/remove", handle(s.objAliasesRemove))
		r.Put("/objs/{object_id}/versions/{digest}/tags", handle(s.objTagsAdd))
		r.Post("/objs/{object_id}/versions/{digest}/tags", handle(s.objTagsAdd))
		r.Post("/objs/{object_id}/versions/{digest}/tags/remove", handle(s.objTagsRemove))

		r.Post("/table/create", handle(s.tableCreate))
		r.Post("/table/update", handle(s.tableUpdate))
		r.Post("/table/query", handle(s.tableQuery))
		r.Post("/table/query_stats", handle(s.tableQueryStats))
		r.Post("/table/create_from_digests", handle(s.tableCreateFromDigests))
		r.Post("/table/query_stats_batch", handle(s.tableQueryStatsBatch))
		r.Post("/refs/read_batch", handle(s.refsReadBatch))
		r.Post("/file/create", s.fileCreate)
		r.Post("/file/content", s.fileContent)
		r.Get("/file/content", s.fileContent)
		r.Post("/files/resolve_url", s.forward("/api/v1/files/resolve", fileContentURLs))
		r.Post("/files/query_stats", handle(s.filesQueryStats))

		r.Post("/feedback/create", handle(s.feedbackCreate))
		r.Post("/feedback/batch/create", handle(s.feedbackBatchCreate))
		r.Post("/feedback/query", handle(s.feedbackQuery))
		r.Post("/feedback/purge", handle(s.feedbackPurge))
		r.Post("/feedback/replace", handle(s.feedbackReplace))
		r.Post("/feedback/stats", handle(s.feedbackStats))
		r.Post("/feedback/aggregate", handle(s.feedbackAggregate))
		r.Post("/feedback/payload_schema", handle(s.feedbackPayloadSchema))

		r.Post("/cost/create", handle(s.costCreate))
		r.Post("/cost/query", handle(s.costQuery))
		r.Post("/cost/purge", handle(s.costPurge))

		r.Post("/completions/create", handle(s.completionsCreate))
		r.Post("/completions/create_stream", s.completionsStream)
		r.Post("/embeddings/create", s.forward("/api/v1/embeddings", nil))
		r.Post("/image/create", s.imageCreate)

		r.Post("/evaluate_model", handle(s.evaluateModel))
		r.Post("/evaluations/evaluate_model", handle(s.evaluateModel))
		r.Post("/evaluation_status", handle(s.evaluationStatus))
		r.Post("/evaluations/status", handle(s.evaluationsStatus))
		r.Post("/evaluations/rescore", handle(s.evaluationsRescore))
		r.Post("/evaluations/compare", handle(s.evaluationsCompare))
		r.Post("/leaderboard/compute", handle(s.leaderboardCompute))

		r.Post("/dataset_sources/link", handle(s.datasetSourcesLink))
		r.Post("/dataset_sources/query", handle(s.datasetSourcesQuery))
		r.Post("/dataset_sources/source_datasets_query", handle(s.sourceDatasetsQuery))
		r.Post("/dataset_sources/link_delete", handle(s.datasetSourcesLinkDelete))

		r.Post("/link_to_registry", s.linkToRegistry)
		r.Post("/registry/query", s.forward("/api/v1/registry/query", nil))

		r.Get("/inference/catalog/models", s.inferenceCatalog)
		r.Get("/inference/modelsdev/models", s.inferenceModelsDev)
		r.Get("/inference/router/openrouter/models", s.inferenceOpenRouter)
		r.Get("/inference/analysis/artificialanalysis/models", s.inferenceOpenRouter)
		r.Get("/inference/nvidia/v2/hardware", s.inferenceNvidiaHardware)

		const p = "/v2/{entity}/{project}"
		r.Put(p+"/runtimes/{runtime_name}", s.runtimeApply)
		r.Get(p+"/runtimes", s.runtimeList)
		s.mountV2(r)

		r.Post("/v1/entities/{entity}/projects/{project}:export", s.bulkExport("", http.MethodPost))
		r.Get("/v1/entities/{entity}/projects/{project}/exports/{job_id}", s.bulkExport("job_id", http.MethodGet))
		r.Get("/v1/entities/{entity}/projects/{project}/exports/{job_id}/artifacts/{target}", s.bulkExport("target", http.MethodGet))
	})
}

// ---------------------------------------------------------------- responses

// legacyResponses makes the older routes compute digests in the older form
// and write refs with the older kind names.
func legacyResponses(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(refs.WithDigester(r.Context(), legacy.Digester))
		next.ServeHTTP(&refKindsWriter{ResponseWriter: w}, r)
	})
}

// refKindsWriter rewrites ref kinds in JSON written to it. Handlers write
// each JSON document (or NDJSON line) in one Write, so a ref is never split.
type refKindsWriter struct {
	http.ResponseWriter
	wroteHeader bool
	json        bool
}

// Unwrap returns the wrapped writer (the request log finds its record there).
func (w *refKindsWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *refKindsWriter) WriteHeader(code int) {
	if !w.wroteHeader {
		w.wroteHeader = true
		ct := w.Header().Get("Content-Type")
		w.json = strings.Contains(ct, "json")
		if w.json {
			w.Header().Del("Content-Length")
		}
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *refKindsWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if w.json {
		if _, err := w.ResponseWriter.Write(legacy.RefKindsOut(b)); err != nil {
			return 0, err
		}
		return len(b), nil
	}
	return w.ResponseWriter.Write(b)
}

func (w *refKindsWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// ---------------------------------------------------------------- forwarding

// forward serves the request with Weft's own handler at path (same method
// and body, or body when set), rewriting a JSON response with out when set.
func (s *Routes) forward(path string, out func(map[string]any) any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.serveNative(w, r, r.Method, path, nil, nil, out)
	}
}

// serveNative runs the native handler for method and path with body (nil:
// the request's own) and query (nil: the request's own).
func (s *Routes) serveNative(w http.ResponseWriter, r *http.Request, method, path string, body []byte, query url.Values,
	out func(map[string]any) any) {
	r2 := r.Clone(context.WithValue(r.Context(), chi.RouteCtxKey, (*chi.Context)(nil)))
	r2.Method = method
	r2.URL.Path, r2.URL.RawPath = path, ""
	if query != nil {
		r2.URL.RawQuery = query.Encode()
	}
	if body != nil {
		r2.Body = io.NopCloser(bytes.NewReader(body))
		r2.ContentLength = int64(len(body))
		r2.Header.Del("Content-Length")
		r2.Header.Del("Content-Encoding")
		r2.Header.Set("Content-Type", "application/json")
	}
	if out == nil {
		s.host.Native().ServeHTTP(w, r2)
		return
	}
	rec := &bufferedWriter{ResponseWriter: w, status: http.StatusOK}
	s.host.Native().ServeHTTP(rec, r2)
	resp := rec.buf
	if rec.status < 300 && strings.Contains(w.Header().Get("Content-Type"), "json") {
		var m map[string]any
		dec := json.NewDecoder(bytes.NewReader(resp))
		dec.UseNumber()
		if dec.Decode(&m) == nil {
			var buf bytes.Buffer
			enc := json.NewEncoder(&buf)
			enc.SetEscapeHTML(false)
			if enc.Encode(out(m)) == nil {
				resp = buf.Bytes()
			}
		}
	}
	w.Header().Del("Content-Length")
	w.WriteHeader(rec.status)
	_, _ = w.Write(resp)
}

// fileContentURLs answers a resolve with this server's file URLs in the
// older form (/file/content?project_id=&digest=).
func fileContentURLs(m map[string]any) any {
	urls, _ := m["download_urls"].([]any)
	for i, x := range urls {
		u, err := url.Parse(fmt.Sprint(x))
		if err != nil {
			continue
		}
		if d, ok := strings.CutPrefix(u.Path, "/api/v1/files/"); ok && d != "" && !strings.Contains(d, "/") {
			q := u.Query()
			u.Path, u.RawPath = "/file/content", ""
			u.RawQuery = url.Values{"project_id": {q.Get("project_id")}, "digest": {d}}.Encode()
			urls[i] = u.String()
		}
	}
	return m
}

// nativeFilter translates a calls filter to the span filter.
func nativeFilter(f map[string]any) map[string]any {
	if f == nil {
		return nil
	}
	names := map[string]string{"op_names": "names", "call_ids": "ids", "thread_ids": "session_ids"}
	out := make(map[string]any, len(f))
	for k, v := range f {
		if n, ok := names[k]; ok {
			k = n
		}
		out[k] = v
	}
	return out
}

// forwardFiltered forwards a body whose "filter" is a calls filter.
func (s *Routes) forwardFiltered(path string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := decodeBody(r, &body); err != nil {
			writeErr(w, err)
			return
		}
		if f, ok := body["filter"].(map[string]any); ok {
			body["filter"] = nativeFilter(f)
		}
		b, _ := json.Marshal(body)
		s.serveNative(w, r, http.MethodPost, path, b, nil, nil)
	}
}

// callsStats forwards /calls/stats to /api/v1/spans/stats. The metric-bucket
// form names span metrics the client's way: its call_metrics (metric
// call_count) are Weft's span_metrics (span_count), and the answer's
// span_buckets (columns <agg>_span_count) are its call_buckets.
func (s *Routes) callsStats(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := decodeBody(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	if f, ok := body["filter"].(map[string]any); ok {
		body["filter"] = nativeFilter(f)
	}
	if m, ok := body["call_metrics"]; ok {
		delete(body, "call_metrics")
		if l, ok := m.([]any); ok {
			for _, x := range l {
				if sp, ok := x.(map[string]any); ok && sp["metric"] == "call_count" {
					sp["metric"] = "span_count"
				}
			}
		}
		body["span_metrics"] = m
	}
	b, _ := json.Marshal(body)
	s.serveNative(w, r, http.MethodPost, "/api/v1/spans/stats", b, nil, func(m map[string]any) any {
		sb, ok := m["span_buckets"]
		if !ok {
			return m
		}
		delete(m, "span_buckets")
		if l, ok := sb.([]any); ok {
			for _, x := range l {
				row, ok := x.(map[string]any)
				if !ok {
					continue
				}
				for k, v := range row {
					if agg, ok := strings.CutSuffix(k, "_span_count"); ok {
						delete(row, k)
						row[agg+"_call_count"] = v
					}
				}
			}
		}
		m["call_buckets"] = sb
		return m
	})
}

// ---------------------------------------------------------------- plumbing

var empty = map[string]any{}

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

func writeErr(w http.ResponseWriter, err error) {
	var inv *store.InvalidError
	var fe *store.ForbiddenError
	switch {
	case otlp.BodyStatus(err) != http.StatusBadRequest:
		writeError(w, otlp.BodyStatus(err), err.Error())
	case errors.As(err, &inv):
		writeError(w, http.StatusBadRequest, inv.Msg)
	case errors.As(err, &fe):
		writeError(w, http.StatusForbidden, fe.Msg)
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	case errors.Is(err, context.Canceled):
		writeError(w, 499, "request cancelled")
	default:
		if !weftlog.RecordError(w, err) { // the request log reports it
			slog.Error("weft request failed", "err", weftlog.Err(err))
		}
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

func pathParam(r *http.Request, name string) string {
	v := chi.URLParam(r, name)
	if u, err := url.PathUnescape(v); err == nil {
		return u
	}
	return v
}

// readBody reads a (possibly compressed) request body.
// It goes through the server-wide body budget (otlp.ReadBodyBudget).
func readBody(r *http.Request) ([]byte, error) {
	if r.Body == nil || r.Body == http.NoBody {
		return nil, nil
	}
	body, res, err := otlp.ReadBodyBudget(r.Context(), http.MaxBytesReader(nil, r.Body, maxJSONBody+1), r.Header.Get("Content-Encoding"), maxJSONBody)
	if err != nil {
		if otlp.BodyStatus(err) != http.StatusBadRequest {
			return nil, err // 413 or 503 (writeErr)
		}
		return nil, store.Invalidf("reading body: %v", err)
	}
	holdBody(r, res)
	return body, nil
}
