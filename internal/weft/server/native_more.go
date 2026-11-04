package server

import (
	"context"
	"errors"
	"net/http"
	"sort"

	"github.com/agentshield-ai/agentshield/internal/weft/evals"
	"github.com/agentshield-ai/agentshield/internal/weft/store"
)

// Native paths of Weft features that had only another product's route
// shape: project statistics, the registry, custom runtimes, bulk exports
// and dataset lineage; plus GET /api/v1/server.

// serverFeatures lists what this server supports, for clients to check.
func (s *Server) serverFeatures() []string {
	f := []string{"spans", "objects", "tables", "feedback", "files", "refs", "prices", "completions", "embeddings", "images",
		"evaluations", "sessions", "agents", "teams", "handoffs", "detections", "scores", "monitors", "automations",
		"annotation_queues", "registry", "runtimes", "exports", "dataset_sources", "otlp"}
	if s.store.FileBackend() != nil {
		f = append(f, "files_backend")
	}
	if s.compat != nil {
		f = append(f, "compat_adapter")
	}
	sort.Strings(f)
	return f
}

// v1Server describes the server: version, features and limits. It needs
// no API key, like /health.
func (s *Server) v1Server(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{
		"name": "weft", "version": Version, "api_version": "v1",
		"auth_required": s.authRequired(), "otel_endpoint": "/otel/v1/traces",
		"features": s.serverFeatures(),
		"digest":   map[string]any{"algorithm": "sha256", "encoding": "hex", "canonical_json": "sorted keys, no whitespace, UTF-8"},
		"limits": map[string]any{
			"max_request_bytes":     maxJSONBody,
			"max_file_bytes":        64 << 20,
			"max_span_batch":        maxSpanBatch,
			"max_feedback_batch":    1000,
			"max_refs_per_read":     1000,
			"max_table_rows":        1_000_000,
			"default_page_size":     defaultListLimit,
			"max_page_size":         maxListLimit,
			"max_compare_runs":      50,
			"max_scored_spans":      100,
			"max_scorers_per_score": 10,
		},
	})
}

func (s *Server) v1ProjectStats(w http.ResponseWriter, r *http.Request) {
	pid, err := project(r.URL.Query().Get("project_id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	st, err := s.store.GetProjectStats(r.Context(), pid)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"span_count": st.CallCount, "trace_count": st.TraceCount, "object_count": st.ObjectCount,
		"table_count": st.TableCount, "file_count": st.FileCount, "feedback_count": st.FeedbackCount,
		"storage_size_bytes": st.StorageSizeBytes})
}

// v1RegistryLink links an object version into a registry collection:
// {ref, project_id (the registry project), collection, aliases?}.
func (s *Server) v1RegistryLink(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Ref        string   `json:"ref"`
		ProjectID  string   `json:"project_id"`
		Collection string   `json:"collection"`
		Aliases    []string `json:"aliases"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if req.Ref == "" || req.ProjectID == "" || req.Collection == "" {
		writeErr(w, store.Invalidf("ref, project_id and collection are required"))
		return
	}
	pid, err := project(req.ProjectID)
	if err != nil {
		writeErr(w, err)
		return
	}
	// The middleware checked both the ref's project and project_id.
	v, err := s.store.LinkToRegistry(r.Context(), req.Ref, pid, req.Collection, req.Aliases, userFrom(r, nil))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"version": v})
}

func (s *Server) v1Runtimes(w http.ResponseWriter, r *http.Request) {
	pid, err := project(r.URL.Query().Get("project_id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	objs, err := s.store.QueryObjects(r.Context(), store.ObjsQuery{ProjectID: pid,
		Filter: &store.ObjsFilter{BaseObjectClasses: []string{"Provider"}, LatestOnly: true}})
	if err != nil {
		writeErr(w, err)
		return
	}
	out := []map[string]any{}
	for _, o := range objs {
		if rt, ok := evals.RuntimeFromObject(o); ok {
			out = append(out, runtimeRes(rt))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i]["name"].(string) < out[j]["name"].(string) })
	writeJSON(w, newList(out, int64(len(out))))
}

func (s *Server) v1RuntimeApply(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ProjectID string `json:"project_id"`
		runtimeApplyBody
	}
	if err := decodeBody(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	pid, err := project(body.ProjectID)
	if err != nil {
		writeErr(w, err)
		return
	}
	rt, err := s.applyRuntime(r.Context(), pid, pathParam(r, "runtime_name"), &body.runtimeApplyBody, userFrom(r, nil))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, runtimeRes(rt))
}

// ---------------------------------------------------------------- dataset lineage

type datasetLinkReq struct {
	ProjectID            string                         `json:"project_id"`
	Dataset              string                         `json:"dataset"`
	Version              string                         `json:"version"`
	Links                []store.DatasetSourceLinkInput `json:"links"`
	IncludeCreatedStatus bool                           `json:"include_created_status"`
	UserID               *string                        `json:"user_id"`
}

func (s *Server) v1DatasetSourcesLink(ctx context.Context, r *http.Request, req *datasetLinkReq) (any, error) {
	version := req.Version
	if version == "" {
		version = "latest"
	}
	o, err := s.store.ReadObject(ctx, req.ProjectID, req.Dataset, version)
	if errors.Is(err, store.ErrNotFound) {
		return nil, store.Invalidf("dataset %s:%s not found", req.Dataset, version)
	}
	if err != nil {
		return nil, err
	}
	entries, err := s.store.LinkDatasetSources(ctx, req.ProjectID, o.ObjectID, o.Digest, req.Links, userFrom(r, req.UserID), req.IncludeCreatedStatus)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, len(entries))
	for i, e := range entries {
		out[i] = map[string]any{"id": e.LinkID}
		if e.Created != nil {
			out[i]["created"] = *e.Created
		}
	}
	return map[string]any{"items": out, "dataset_digest": o.Digest}, nil
}

// DatasetSourceLink is one dataset row's link to the span or session it
// came from.
type DatasetSourceLink struct {
	ID                string         `json:"id"`
	Dataset           string         `json:"dataset"`
	DatasetDigest     string         `json:"dataset_digest"`
	RowDigest         string         `json:"row_digest"`
	SourceKind        string         `json:"source_kind"`
	SourceID          string         `json:"source_id"`
	SourceTraceID     string         `json:"source_trace_id"`
	SourceDisplayName string         `json:"source_display_name"`
	SourceStartedAt   string         `json:"source_started_at"`
	LinkMetadata      map[string]any `json:"link_metadata"`
	AddedBy           *string        `json:"added_by"`
	CreatedAt         string         `json:"created_at"`
	UpdatedAt         string         `json:"updated_at"`
	DeletedAt         *string        `json:"deleted_at"`
}

type datasetSourcesQueryReq struct {
	ProjectID      string   `json:"project_id"`
	Dataset        string   `json:"dataset"`
	RowDigests     []string `json:"row_digests"`
	SourceKinds    []string `json:"source_kinds"`
	IncludeDeleted bool     `json:"include_deleted"`
	Limit          *int     `json:"limit"`
	Offset         int      `json:"offset"`
}

func (s *Server) v1DatasetSourcesQuery(ctx context.Context, _ *http.Request, req *datasetSourcesQueryReq) (any, error) {
	limit, err := listLimit(req.Limit)
	if err != nil {
		return nil, err
	}
	all10k := maxListLimit
	q := store.DatasetSourcesQuery{ProjectID: req.ProjectID, DatasetObjectID: req.Dataset, RowDigests: req.RowDigests,
		SourceKinds: req.SourceKinds, IncludeDeleted: req.IncludeDeleted, Limit: &all10k}
	all, err := s.store.QueryDatasetSources(ctx, q)
	if err != nil {
		return nil, err
	}
	total := int64(len(all))
	if req.Offset > len(all) {
		all = nil
	} else {
		all = all[req.Offset:]
	}
	if len(all) > limit {
		all = all[:limit]
	}
	out := make([]DatasetSourceLink, len(all))
	for i, l := range all {
		out[i] = DatasetSourceLink{ID: l.ID, Dataset: l.DatasetObjectID, DatasetDigest: l.DatasetDigest, RowDigest: l.RowDigest,
			SourceKind: l.SourceKind, SourceID: l.SourceID, SourceTraceID: l.SourceTraceID, SourceDisplayName: l.SourceDisplayName,
			SourceStartedAt: l.SourceStartedAt, LinkMetadata: l.LinkMetadata, AddedBy: l.AddedBy, CreatedAt: l.CreatedAt,
			UpdatedAt: l.UpdatedAt, DeletedAt: l.DeletedAt}
	}
	return newList(out, total), nil
}

func (s *Server) v1DatasetSourcesDatasets(ctx context.Context, _ *http.Request, req *sourceDatasetsReq) (any, error) {
	ms, err := s.store.SourceDatasets(ctx, req.ProjectID, req.Sources, req.IncludeDeleted)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, len(ms))
	for i, m := range ms {
		out[i] = map[string]any{"dataset": m.DatasetObjectID, "first_seen_at": m.FirstSeenAt, "row_digests": m.RowDigests,
			"row_digests_total_count": m.RowDigestsTotalCount, "row_digests_truncated": m.RowDigestsTruncated,
			"source_id": m.SourceID, "source_kind": m.SourceKind, "source_trace_id": m.SourceTraceID}
	}
	return newList(out, int64(len(out))), nil
}

type sourceDatasetsReq struct {
	ProjectID      string                `json:"project_id"`
	Sources        []store.DatasetSource `json:"sources"`
	IncludeDeleted bool                  `json:"include_deleted"`
}

type datasetSourcesDeleteReq struct {
	ProjectID string   `json:"project_id"`
	IDs       []string `json:"ids"`
}

func (s *Server) v1DatasetSourcesDelete(ctx context.Context, _ *http.Request, req *datasetSourcesDeleteReq) (any, error) {
	if len(req.IDs) == 0 || len(req.IDs) > 10000 {
		return nil, store.Invalidf("ids must list 1 to 10000 links")
	}
	res, err := s.store.DeleteDatasetSourceLinks(ctx, req.ProjectID, req.IDs)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(req.IDs))
	for _, id := range req.IDs {
		out = append(out, map[string]any{"id": id, "deleted": res[id]})
	}
	return map[string]any{"items": out}, nil
}
