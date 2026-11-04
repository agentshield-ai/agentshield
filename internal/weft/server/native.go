package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/agentshield-ai/agentshield/internal/weft/store"
	"github.com/go-chi/chi/v5"
)

// Weft's own trace and object API (docs/weft/DESIGN.md §10, docs/weft/API.md
// "Spans" to "Server"): spans, objects, tables, feedback, files, refs,
// prices, model calls, evaluations and the server description, under
// /api/v1 with Weft's names. Lists return {items, total_count}; streams
// return NDJSON, one item per line. The routes of another product's client
// are served only by the compatibility adapter (package compat).

// mountNative registers the native routes on r (inside the authenticated
// group, or on a bare router for the adapter, which forwards to them).
func (s *Server) mountNative(r chi.Router) {
	r.Post("/api/v1/spans/start", handle(s.v1SpanStart))
	r.Post("/api/v1/spans/end", handle(s.v1SpanEnd))
	r.Post("/api/v1/spans/batch", handle(s.v1SpanBatch))
	r.Post("/api/v1/spans/query", handle(s.v1SpansQuery))
	r.Post("/api/v1/spans/stream", s.v1SpansStream)
	r.Get("/api/v1/spans/{id}", s.v1SpanGet)
	r.Post("/api/v1/spans/update", handle(s.v1SpanUpdate))
	r.Post("/api/v1/spans/delete", handle(s.v1SpansDelete))
	r.Post("/api/v1/spans/stats", handle(s.v1SpansStats))
	r.Post("/api/v1/spans/export", s.v1SpansExport)
	r.Post("/api/v1/spans/metrics", handle(s.v1SpansMetrics))
	r.Post("/api/v1/spans/names", handle(s.v1SpanNames))
	r.Post("/api/v1/spans/score", handle(s.v1SpansScore))
	r.Post("/api/v1/spans/sessions", handle(s.v1SpanSessions))

	r.Post("/api/v1/objects/save", handle(s.v1ObjectSave))
	r.Post("/api/v1/objects/query", handle(s.v1ObjectsQuery))
	r.Post("/api/v1/objects/delete", handle(s.v1ObjectsDelete))
	r.Post("/api/v1/objects/tags", handle(s.v1ObjectTagsList))
	r.Post("/api/v1/objects/aliases", handle(s.v1ObjectAliasesList))
	r.Get("/api/v1/objects/{name}", s.v1ObjectGet)
	r.Get("/api/v1/objects/{name}/versions/{version}", s.v1ObjectGet)
	r.Post("/api/v1/objects/{name}/aliases", handle(s.v1ObjectAliasesSet))
	r.Post("/api/v1/objects/{name}/aliases/remove", handle(s.v1ObjectAliasesRemove))
	r.Post("/api/v1/objects/{name}/versions/{version}/tags", handle(s.v1ObjectTagsAdd))
	r.Post("/api/v1/objects/{name}/versions/{version}/tags/remove", handle(s.v1ObjectTagsRemove))

	r.Post("/api/v1/tables/create", handle(s.v1TableCreate))
	r.Post("/api/v1/tables/update", handle(s.v1TableUpdate))
	r.Post("/api/v1/tables/query", handle(s.v1TableQuery))
	r.Post("/api/v1/tables/stream", s.v1TableStream)
	r.Post("/api/v1/tables/stats", handle(s.v1TableStats))

	r.Post("/api/v1/feedback/create", handle(s.v1FeedbackCreate))
	r.Post("/api/v1/feedback/batch", handle(s.v1FeedbackBatch))
	r.Post("/api/v1/feedback/query", handle(s.v1FeedbackQuery))
	r.Post("/api/v1/feedback/delete", handle(s.v1FeedbackDelete))
	r.Post("/api/v1/feedback/replace", handle(s.v1FeedbackReplace))

	r.Post("/api/v1/files/upload", s.v1FileUpload)
	r.Get("/api/v1/files/{digest}", s.v1FileGet)
	r.Post("/api/v1/files/resolve", handle(s.filesResolveURL))

	r.Post("/api/v1/refs/read", handle(s.v1RefsRead))

	r.Post("/api/v1/prices/create", handle(s.v1PricesCreate))
	r.Post("/api/v1/prices/query", handle(s.v1PricesQuery))
	r.Post("/api/v1/prices/delete", handle(s.v1PricesDelete))

	r.Post("/api/v1/completions", handle(s.v1Completions))
	r.Post("/api/v1/completions/stream", s.v1CompletionsStream)
	r.Post("/api/v1/embeddings", handle(s.v1Embeddings))
	r.Post("/api/v1/images", handle(s.v1Images))

	r.Post("/api/v1/evaluations/run", handle(s.v1EvaluationRun))
	r.Get("/api/v1/evaluations/{id}/status", s.v1EvaluationStatus)
	r.Post("/api/v1/evaluations/compare", handle(s.v1EvaluationsCompare))
	r.Post("/api/v1/leaderboards/compute", handle(s.v1LeaderboardCompute))

	r.Get("/api/v1/projects/stats", s.v1ProjectStats)
	r.Post("/api/v1/registry/link", s.v1RegistryLink)
	r.Post("/api/v1/registry/query", handle(s.registryQuery))
	r.Get("/api/v1/runtimes", s.v1Runtimes)
	r.Put("/api/v1/runtimes/{runtime_name}", s.v1RuntimeApply)
	r.Post("/api/v1/exports", withPathProject(s.bulkExportStart))
	r.Get("/api/v1/exports/{job_id}", withPathProject(s.bulkExportStatus))
	r.Get("/api/v1/exports/{job_id}/artifacts/{target}", withPathProject(s.bulkExportDownload))
	r.Post("/api/v1/dataset_sources/link", handle(s.v1DatasetSourcesLink))
	r.Post("/api/v1/dataset_sources/query", handle(s.v1DatasetSourcesQuery))
	r.Post("/api/v1/dataset_sources/datasets", handle(s.v1DatasetSourcesDatasets))
	r.Post("/api/v1/dataset_sources/delete", handle(s.v1DatasetSourcesDelete))
}

// nativeReadPaths are native POST routes that only read (role checks and
// read-only keys; iam.go, admin.go isReadRequest).
var nativeReadPaths = map[string]bool{
	"/api/v1/spans/query": true, "/api/v1/spans/stream": true, "/api/v1/spans/stats": true, "/api/v1/spans/export": true,
	"/api/v1/spans/metrics": true, "/api/v1/spans/names": true, "/api/v1/spans/sessions": true,
	"/api/v1/objects/query": true, "/api/v1/objects/tags": true, "/api/v1/objects/aliases": true,
	"/api/v1/tables/query": true, "/api/v1/tables/stream": true, "/api/v1/tables/stats": true,
	"/api/v1/feedback/query": true, "/api/v1/files/resolve": true, "/api/v1/refs/read": true, "/api/v1/prices/query": true,
	"/api/v1/evaluations/compare": true, "/api/v1/leaderboards/compute": true, "/api/v1/registry/query": true,
	"/api/v1/dataset_sources/query": true, "/api/v1/dataset_sources/datasets": true,
}

// nativeRefs is ctx with spans reached through refs (refs/read, a ref's
// path, expand_columns) in the Span schema.
func nativeRefs(ctx context.Context) context.Context {
	return store.WithSpanEncoder(ctx, func(c *store.Call) any { return spanFrom(c, false) })
}

// page is the shape of every native list response.
type page[T any] struct {
	Items      []T   `json:"items"`
	TotalCount int64 `json:"total_count"`
}

func newList[T any](items []T, total int64) page[T] {
	if items == nil {
		items = []T{}
	}
	return page[T]{Items: items, TotalCount: total}
}

// ndjson writes items as newline-delimited JSON, flushing as it goes.
// newNDJSON writes the 200 status at once, so an empty stream is a 200 with
// no lines (rather than a response whose status was never written); set
// any other headers before calling it.
type ndjson struct {
	enc     *json.Encoder
	flusher http.Flusher
	n       int
}

func newNDJSON(w http.ResponseWriter) *ndjson {
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	f, _ := w.(http.Flusher)
	return &ndjson{enc: enc, flusher: f}
}

func (n *ndjson) write(v any) error {
	if err := n.enc.Encode(v); err != nil {
		return err
	}
	n.n++
	if n.flusher != nil && n.n%200 == 0 {
		n.flusher.Flush()
	}
	return nil
}

func (n *ndjson) flush() {
	if n.flusher != nil {
		n.flusher.Flush()
	}
}

// queryBool reads a boolean query parameter ("true", "1").
func queryBool(r *http.Request, name string) bool {
	v, err := strconv.ParseBool(r.URL.Query().Get(name))
	return err == nil && v
}

// strictJSON decodes into v, rejecting unknown fields, so a misspelt (or
// another product's) filter field is an error instead of a filter that
// silently matches everything.
func strictJSON(b []byte, v any) error {
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.UseNumber()
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return store.Invalidf("%v", err)
	}
	return nil
}

// withPathProject serves a handler written for /…/entities/{entity}/
// projects/{project}/… paths from a native route that names the project in
// its project_id (query parameter, or the JSON body of a POST).
func withPathProject(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pid := r.URL.Query().Get("project_id")
		if pid == "" && r.Method == http.MethodPost {
			body, err := readBodyRestore(r)
			if err != nil {
				writeErr(w, err)
				return
			}
			var probe struct {
				ProjectID string `json:"project_id"`
			}
			_ = json.Unmarshal(body, &probe)
			pid = probe.ProjectID
			// The wrapped handler rejects unknown fields; drop project_id.
			var m map[string]json.RawMessage
			if json.Unmarshal(body, &m) == nil {
				delete(m, "project_id")
				body, _ = json.Marshal(m)
			}
			setBody(r, body)
		}
		p, err := project(pid)
		if err != nil {
			writeErr(w, err)
			return
		}
		ent, proj, _ := strings.Cut(p, "/")
		rctx := chi.RouteContext(r.Context())
		if rctx != nil {
			rctx.URLParams.Add("entity", ent)
			rctx.URLParams.Add("project", proj)
		}
		h(w, r)
	}
}

func pathParam(r *http.Request, name string) string {
	v := chi.URLParam(r, name)
	if u, err := url.PathUnescape(v); err == nil {
		return u
	}
	return v
}

// nativeFileURL is the URL of a stored file on this server.
func nativeFileURL(base, pid, digest string) string {
	return base + "/api/v1/files/" + url.PathEscape(digest) + "?" + url.Values{"project_id": {pid}}.Encode()
}
