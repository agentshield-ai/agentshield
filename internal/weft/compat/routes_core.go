package compat

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/agentshield-ai/agentshield/internal/weft/store"
)

// The older call, object, table, ref, file, feedback, cost, model-call and
// evaluation routes (Weft's own are under /api/v1, server/native_*.go).

// cumulativeSummaries reports a request whose call summaries already include
// their descendants (X-Weft-Summary: cumulative).
func cumulativeSummaries(r *http.Request) bool {
	return r.Header.Get("X-Weft-Summary") == "cumulative" || r.Header.Get("X-Weft-Summary") == "Cumulative"
}

func (s *Routes) projectStats(ctx context.Context, _ *http.Request, req *projectReq) (any, error) {
	return s.store.GetProjectStats(ctx, req.ProjectID)
}

type projectReq struct {
	ProjectID string `json:"project_id"`
}

// ---------------------------------------------------------------- calls

type callStartReq struct {
	Start store.CallStart `json:"start"`
}

func (s *Routes) callStart(ctx context.Context, r *http.Request, req *callStartReq) (any, error) {
	if err := s.host.PrepareStart(ctx, r, &req.Start); err != nil {
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

func (s *Routes) callEnd(ctx context.Context, r *http.Request, req *callEndReq) (any, error) {
	req.End.Cumulative = cumulativeSummaries(r)
	if req.End.EndedAt == "" {
		req.End.EndedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if err := s.store.EndCall(ctx, req.End); err != nil {
		return nil, err
	}
	pid, _ := project(req.End.ProjectID)
	s.host.CallsEnded([]store.EndedCall{{ProjectID: pid, ID: req.End.ID}})
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

func (s *Routes) callUpsertBatch(ctx context.Context, r *http.Request, req *batchReq) (any, error) {
	if len(req.Batch) > 100000 {
		return nil, store.Invalidf("batch too large")
	}
	items := make([]store.BatchItem, len(req.Batch))
	res := make([]any, len(req.Batch))
	for i, b := range req.Batch {
		items[i] = store.BatchItem{Mode: b.Mode, Start: b.Req.Start, End: b.Req.End}
		switch {
		case b.Mode == "start" && b.Req.Start != nil:
			if err := s.host.PrepareStart(ctx, r, b.Req.Start); err != nil {
				return nil, err
			}
			res[i] = map[string]any{"id": b.Req.Start.ID, "trace_id": b.Req.Start.TraceID}
		case b.Mode == "end" && b.Req.End != nil:
			b.Req.End.Cumulative = cumulativeSummaries(r)
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
	s.host.CallsEnded(ended)
	return map[string]any{"res": res}, nil
}

type callReadReq struct {
	ProjectID               string `json:"project_id"`
	ID                      string `json:"id"`
	IncludeCosts            bool   `json:"include_costs"`
	IncludeFeedback         bool   `json:"include_feedback"`
	IncludeStorageSize      bool   `json:"include_storage_size"`
	IncludeTotalStorageSize bool   `json:"include_total_storage_size"`
	IncludeUsernames        bool   `json:"include_usernames"`
}

func (s *Routes) callRead(ctx context.Context, _ *http.Request, req *callReadReq) (any, error) {
	pid, err := project(req.ProjectID)
	if err != nil {
		return nil, err
	}
	c, err := s.store.GetCallWith(ctx, store.CallsQuery{ProjectID: pid, IncludeCosts: req.IncludeCosts, IncludeFeedback: req.IncludeFeedback,
		IncludeStorageSize: req.IncludeStorageSize, IncludeTotalStorageSize: req.IncludeTotalStorageSize, IncludeUsernames: req.IncludeUsernames}, req.ID)
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

func (s *Routes) callUpdate(ctx context.Context, _ *http.Request, req *callUpdateReq) (any, error) {
	pid, err := project(req.ProjectID)
	if err != nil {
		return nil, err
	}
	if req.DisplayName == nil {
		return nil, store.Invalidf("display_name is required")
	}
	return empty, s.store.UpdateCallDisplayName(ctx, pid, req.CallID, *req.DisplayName)
}

func (s *Routes) callsQuery(ctx context.Context, _ *http.Request, req *store.CallsQuery) (any, error) {
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

func (s *Routes) callsStreamQuery(w http.ResponseWriter, r *http.Request) {
	var req store.CallsQuery
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	calls, err := s.store.ListCalls(r.Context(), req)
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/jsonl")
	w.WriteHeader(http.StatusOK) // an empty stream is a 200 too
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
	Limit     *int64             `json:"limit"`
}

func (s *Routes) callsQueryStats(ctx context.Context, _ *http.Request, req *queryStatsReq) (any, error) {
	n, size, err := s.store.CountCalls(ctx, req.ProjectID, req.Filter, req.Query, req.Search)
	if err != nil {
		return nil, err
	}
	hasMore := false
	if req.Limit != nil && *req.Limit >= 0 && n > *req.Limit {
		n, hasMore = *req.Limit, true
	}
	return map[string]any{"count": n, "has_more": hasMore, "total_storage_size_bytes": size}, nil
}

type callsDeleteReq struct {
	ProjectID string   `json:"project_id"`
	CallIDs   []string `json:"call_ids"`
}

func (s *Routes) callsDelete(ctx context.Context, _ *http.Request, req *callsDeleteReq) (any, error) {
	pid, err := project(req.ProjectID)
	if err != nil {
		return nil, err
	}
	return empty, s.store.DeleteCalls(ctx, pid, req.CallIDs)
}

type callsScoreReq struct {
	ProjectID  string   `json:"project_id"`
	CallIDs    []string `json:"call_ids"`
	ScorerRefs []string `json:"scorer_refs"`
	UserID     *string  `json:"user_id"`
}

func (s *Routes) callsScore(ctx context.Context, r *http.Request, req *callsScoreReq) (any, error) {
	pid, err := project(req.ProjectID)
	if err != nil {
		return nil, err
	}
	res, err := s.engine.ScoreCalls(ctx, pid, req.CallIDs, req.ScorerRefs, s.host.User(r, req.UserID))
	if err != nil {
		return nil, err
	}
	return map[string]any{"results": res}, nil
}

type opCountsReq struct {
	ProjectID      string `json:"project_id"`
	TraceRootsOnly bool   `json:"trace_roots_only"`
}

func (s *Routes) opCounts(ctx context.Context, _ *http.Request, req *opCountsReq) (any, error) {
	ops, err := s.store.OpCounts(ctx, req.ProjectID, req.TraceRootsOnly)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ops": ops}, nil
}

// callsExport streams calls with their stored field names.
func (s *Routes) callsExport(w http.ResponseWriter, r *http.Request) {
	var req struct {
		store.CallsQuery
		Format  string `json:"format"`
		Flatten *bool  `json:"flatten"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	s.host.ExportCalls(w, r, req.CallsQuery, req.Format, req.Flatten)
}

func (s *Routes) threadsQuery(ctx context.Context, _ *http.Request, req *store.ThreadsQuery) (any, error) {
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
		UserID             *string         `json:"user_id"`
	} `json:"obj"`
}

func (s *Routes) objCreate(ctx context.Context, r *http.Request, req *objCreateReq) (any, error) {
	o := req.Obj
	pid, err := project(o.ProjectID)
	if err != nil {
		return nil, err
	}
	if len(o.Val) == 0 || string(o.Val) == "null" {
		return nil, store.Invalidf("val is required")
	}
	d, v, err := s.store.CreateObjectRaw(ctx, pid, o.ObjectID, nil, o.Val, o.BuiltinObjectClass, s.host.User(r, o.UserID))
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

func (s *Routes) objRead(ctx context.Context, _ *http.Request, req *objReadReq) (any, error) {
	o, err := s.store.ReadObject(ctx, req.ProjectID, req.ObjectID, req.Digest)
	if err != nil {
		return nil, err
	}
	return map[string]any{"obj": o}, nil
}

func (s *Routes) objsQuery(ctx context.Context, _ *http.Request, req *store.ObjsQuery) (any, error) {
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

func (s *Routes) objDelete(ctx context.Context, _ *http.Request, req *objDeleteReq) (any, error) {
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

func (s *Routes) aliasSet(ctx context.Context, _ *http.Request, req *aliasReq) (any, error) {
	return empty, s.store.SetAlias(ctx, req.ProjectID, req.ObjectID, req.Digest, req.Alias)
}

func (s *Routes) aliasRemove(ctx context.Context, _ *http.Request, req *aliasReq) (any, error) {
	return empty, s.store.RemoveAlias(ctx, req.ProjectID, req.ObjectID, req.Alias)
}

func (s *Routes) tagsList(w http.ResponseWriter, r *http.Request) {
	tags, err := s.store.ListTags(r.Context(), r.URL.Query().Get("project_id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"tags": tags})
}

func (s *Routes) aliasesList(w http.ResponseWriter, r *http.Request) {
	al, err := s.store.ListAliases(r.Context(), r.URL.Query().Get("project_id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"aliases": al})
}

type objAliasesReq struct {
	ProjectID string   `json:"project_id"`
	Digest    string   `json:"digest"`
	Aliases   []string `json:"aliases"`
}

func (s *Routes) objAliasesSet(ctx context.Context, r *http.Request, req *objAliasesReq) (any, error) {
	if req.Digest == "" {
		return nil, store.Invalidf("digest is required")
	}
	if len(req.Aliases) > 100 {
		return nil, store.Invalidf("too many aliases (max 100)")
	}
	oid := pathParam(r, "object_id")
	for _, a := range req.Aliases {
		if err := s.store.SetAlias(ctx, req.ProjectID, oid, req.Digest, a); err != nil {
			return nil, err
		}
	}
	return empty, nil
}

func (s *Routes) objAliasesRemove(ctx context.Context, r *http.Request, req *objAliasesReq) (any, error) {
	oid := pathParam(r, "object_id")
	for _, a := range req.Aliases {
		if err := s.store.RemoveAlias(ctx, req.ProjectID, oid, a); err != nil {
			return nil, err
		}
	}
	return empty, nil
}

type objTagsReq struct {
	ProjectID string   `json:"project_id"`
	Tags      []string `json:"tags"`
}

func (s *Routes) objTagsAdd(ctx context.Context, r *http.Request, req *objTagsReq) (any, error) {
	return empty, s.store.AddTags(ctx, req.ProjectID, pathParam(r, "object_id"), pathParam(r, "digest"), req.Tags)
}

func (s *Routes) objTagsRemove(ctx context.Context, r *http.Request, req *objTagsReq) (any, error) {
	return empty, s.store.RemoveTags(ctx, req.ProjectID, pathParam(r, "object_id"), pathParam(r, "digest"), req.Tags)
}

// ---------------------------------------------------------------- tables, refs, files

type tableCreateReq struct {
	Table struct {
		ProjectID string            `json:"project_id"`
		Rows      []json.RawMessage `json:"rows"`
	} `json:"table"`
}

func (s *Routes) tableCreate(ctx context.Context, _ *http.Request, req *tableCreateReq) (any, error) {
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

func (s *Routes) tableUpdate(ctx context.Context, _ *http.Request, req *tableUpdateReq) (any, error) {
	d, rows, err := s.store.UpdateTable(ctx, req.ProjectID, req.BaseDigest, req.Updates)
	if err != nil {
		return nil, err
	}
	return map[string]any{"digest": d, "updated_row_digests": rows}, nil
}

func (s *Routes) tableQuery(ctx context.Context, _ *http.Request, req *store.TableQuery) (any, error) {
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

func (s *Routes) tableQueryStats(ctx context.Context, _ *http.Request, req *tableStatsReq) (any, error) {
	n, err := s.store.TableCount(ctx, req.ProjectID, req.Digest)
	if err != nil {
		return nil, err
	}
	return map[string]any{"count": n}, nil
}

type refsReq struct {
	Refs []string `json:"refs"`
}

func (s *Routes) refsReadBatch(ctx context.Context, _ *http.Request, req *refsReq) (any, error) {
	if len(req.Refs) > 1000 {
		return nil, store.Invalidf("too many refs (max 1000)")
	}
	vals, err := s.store.ReadRefsRaw(ctx, req.Refs) // spans as stored call rows
	if err != nil {
		return nil, err
	}
	return map[string]any{"vals": vals}, nil
}

func (s *Routes) fileCreate(w http.ResponseWriter, r *http.Request) {
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

func (s *Routes) fileContent(w http.ResponseWriter, r *http.Request) {
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
	if r.Method == http.MethodPost {
		q := url.Values{"project_id": {req.ProjectID}}
		w = contentTypeWriter{w, "application/octet-stream"}
		s.serveNative(w, r, http.MethodGet, "/api/v1/files/"+url.PathEscape(req.Digest), []byte{}, q, nil)
		return
	}
	s.serveNative(w, r, http.MethodGet, "/api/v1/files/"+url.PathEscape(req.Digest), nil, url.Values{"project_id": {req.ProjectID}}, nil)
}

// contentTypeWriter forces the content type of a successful response.
type contentTypeWriter struct {
	http.ResponseWriter
	ctype string
}

func (w contentTypeWriter) WriteHeader(code int) {
	if code < 300 {
		w.Header().Set("Content-Type", w.ctype)
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w contentTypeWriter) Write(b []byte) (int, error) {
	if w.Header().Get("Content-Type") != "application/json" {
		w.Header().Set("Content-Type", w.ctype)
	}
	return w.ResponseWriter.Write(b)
}

// ---------------------------------------------------------------- feedback

func (s *Routes) feedbackCreate(ctx context.Context, r *http.Request, req *store.FeedbackCreate) (any, error) {
	req.UserID = s.host.User(r, req.UserID)
	f, err := s.store.CreateFeedback(ctx, *req)
	if err != nil {
		return nil, err
	}
	return map[string]any{"id": f.ID, "created_at": f.CreatedAt, "user_id": f.UserID, "payload": f.Payload}, nil
}

type feedbackBatchReq struct {
	Batch []feedbackBatchItem `json:"batch"`
}

// feedbackBatchItem is a batch item as the client sends it: the scorer's
// own span is call_ref (Weft's span_ref).
type feedbackBatchItem struct {
	store.AgentFeedbackItem
	CallRef *string `json:"call_ref"`
}

func (s *Routes) feedbackBatchCreate(ctx context.Context, r *http.Request, req *feedbackBatchReq) (any, error) {
	if len(req.Batch) > 1000 {
		return nil, store.Invalidf("batch too large (max 1000)")
	}
	res := make([]any, 0, len(req.Batch))
	for _, it := range req.Batch {
		b := it.AgentFeedbackItem
		if it.CallRef != nil {
			b.SpanRef = it.CallRef
		}
		b.UserID = s.host.User(r, b.UserID)
		f, err := s.store.CreateAgentFeedback(ctx, b)
		if err != nil {
			return nil, err
		}
		res = append(res, map[string]any{"id": f.ID, "created_at": f.CreatedAt, "user_id": f.UserID, "payload": f.Payload})
	}
	return map[string]any{"res": res}, nil
}

type feedbackQueryReq struct {
	ProjectID string         `json:"project_id"`
	Query     any            `json:"query"`
	SortBy    []store.SortBy `json:"sort_by"`
	Limit     int            `json:"limit"`
	Offset    int            `json:"offset"`
}

func (s *Routes) feedbackQuery(ctx context.Context, _ *http.Request, req *feedbackQueryReq) (any, error) {
	fs, err := s.store.QueryFeedback(ctx, req.ProjectID, req.Query, req.SortBy, req.Limit, req.Offset)
	if err != nil {
		return nil, err
	}
	total, err := s.store.CountFeedback(ctx, req.ProjectID, req.Query)
	if err != nil {
		return nil, err
	}
	return map[string]any{"result": fs, "total_count": total}, nil
}

type feedbackPurgeReq struct {
	ProjectID string `json:"project_id"`
	Query     any    `json:"query"`
}

func (s *Routes) feedbackPurge(ctx context.Context, _ *http.Request, req *feedbackPurgeReq) (any, error) {
	return empty, s.store.PurgeFeedback(ctx, req.ProjectID, req.Query)
}

type feedbackReplaceReq struct {
	store.FeedbackCreate
	FeedbackID string `json:"feedback_id"`
}

func (s *Routes) feedbackReplace(ctx context.Context, r *http.Request, req *feedbackReplaceReq) (any, error) {
	req.UserID = s.host.User(r, req.UserID)
	f, err := s.store.ReplaceFeedback(ctx, req.FeedbackID, req.FeedbackCreate)
	if err != nil {
		return nil, err
	}
	return map[string]any{"id": f.ID, "created_at": f.CreatedAt, "user_id": f.UserID, "payload": f.Payload}, nil
}

// ---------------------------------------------------------------- costs

type costCreateReq struct {
	ProjectID string                     `json:"project_id"`
	Costs     map[string]store.CostInput `json:"costs"`
}

func (s *Routes) costCreate(ctx context.Context, _ *http.Request, req *costCreateReq) (any, error) {
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

func (s *Routes) costQuery(ctx context.Context, _ *http.Request, req *costQueryReq) (any, error) {
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

func (s *Routes) costPurge(ctx context.Context, _ *http.Request, req *costPurgeReq) (any, error) {
	pid, err := project(req.ProjectID)
	if err != nil {
		return nil, err
	}
	return empty, s.store.PurgeCosts(ctx, pid, req.IDs, req.Query)
}

// ---------------------------------------------------------------- model calls

type completionsReq struct {
	ProjectID    string         `json:"project_id"`
	Inputs       map[string]any `json:"inputs"`
	TrackLLMCall *bool          `json:"track_llm_call"`
}

func (s *Routes) completionsCreate(ctx context.Context, _ *http.Request, req *completionsReq) (any, error) {
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

func (s *Routes) completionsStream(w http.ResponseWriter, r *http.Request) {
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
	w.WriteHeader(http.StatusOK) // an empty stream is a 200 too
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
			meta["call_id"] = id
		}
	}
	_ = enc.Encode(map[string]any{"_meta": meta})
	if flusher != nil {
		flusher.Flush()
	}
}

// imageCreate is /image/create: POST /api/v1/images with track_llm_call as
// track, and span_id answered as call_id.
func (s *Routes) imageCreate(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := decodeBody(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	if t, ok := body["track_llm_call"]; ok {
		body["track"] = t
		delete(body, "track_llm_call")
	}
	b, _ := json.Marshal(body)
	s.serveNative(w, r, http.MethodPost, "/api/v1/images", b, nil, func(m map[string]any) any {
		return map[string]any{"response": m["response"], "call_id": m["span_id"]}
	})
}

// ---------------------------------------------------------------- evaluations

type evaluateReq struct {
	ProjectID     string  `json:"project_id"`
	EvaluationRef string  `json:"evaluation_ref"`
	ModelRef      string  `json:"model_ref"`
	UserID        *string `json:"user_id"`
}

func (s *Routes) evaluateModel(ctx context.Context, r *http.Request, req *evaluateReq) (any, error) {
	id, err := s.engine.EvaluateModel(ctx, req.ProjectID, req.EvaluationRef, req.ModelRef, s.host.User(r, req.UserID))
	if err != nil {
		return nil, err
	}
	return map[string]any{"call_id": id}, nil
}

type evalStatusReq struct {
	ProjectID string `json:"project_id"`
	CallID    string `json:"call_id"`
}

func (s *Routes) evaluationStatus(ctx context.Context, _ *http.Request, req *evalStatusReq) (any, error) {
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

func (s *Routes) evaluationsCompare(ctx context.Context, _ *http.Request, req *compareReq) (any, error) {
	return s.engine.Compare(ctx, req.ProjectID, req.EvaluationCallIDs)
}

type leaderboardReq struct {
	ProjectID string `json:"project_id"`
	ObjectID  string `json:"object_id"`
	Digest    string `json:"digest"`
}

func (s *Routes) leaderboardCompute(ctx context.Context, _ *http.Request, req *leaderboardReq) (any, error) {
	return s.engine.ComputeLeaderboard(ctx, req.ProjectID, req.ObjectID, req.Digest)
}

// ---------------------------------------------------------------- registry, runtimes, exports

type linkToRegistryReq struct {
	Ref    string `json:"ref"`
	Target struct {
		PortfolioName string `json:"portfolio_name"`
		EntityName    string `json:"entity_name"`
		ProjectName   string `json:"project_name"`
	} `json:"target"`
	Aliases []string `json:"aliases"`
}

// linkToRegistry is POST /api/v1/registry/link with the target as
// portfolio, entity and project names.
func (s *Routes) linkToRegistry(w http.ResponseWriter, r *http.Request) {
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
	if code, msg := s.host.AuthorizeProject(r, pid); code != 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = w.Write(msg)
		return
	}
	b, _ := json.Marshal(map[string]any{"ref": req.Ref, "project_id": pid, "collection": t.PortfolioName, "aliases": req.Aliases})
	s.serveNative(w, r, http.MethodPost, "/api/v1/registry/link", b, nil, func(m map[string]any) any {
		return map[string]any{"version_index": m["version"]}
	})
}

func (s *Routes) runtimeApply(w http.ResponseWriter, r *http.Request) {
	body, err := readBody(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil || m == nil {
		m = map[string]any{}
	}
	m["project_id"] = pathParam(r, "entity") + "/" + pathParam(r, "project")
	b, _ := json.Marshal(m)
	s.serveNative(w, r, http.MethodPut, "/api/v1/runtimes/"+url.PathEscape(pathParam(r, "runtime_name")), b, nil, nil)
}

func (s *Routes) runtimeList(w http.ResponseWriter, r *http.Request) {
	q := url.Values{"project_id": {pathParam(r, "entity") + "/" + pathParam(r, "project")}}
	s.serveNative(w, r, http.MethodGet, "/api/v1/runtimes", nil, q, func(m map[string]any) any {
		return map[string]any{"runtimes": m["items"]}
	})
}

// bulkExport serves /v1/entities/{entity}/projects/{project}… from
// /api/v1/exports….
func (s *Routes) bulkExport(param, method string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pid := pathParam(r, "entity") + "/" + pathParam(r, "project")
		q := url.Values{"project_id": {pid}}
		switch param {
		case "":
			body, err := readBody(r)
			if err != nil {
				writeErr(w, err)
				return
			}
			s.serveNative(w, r, method, "/api/v1/exports", body, q, nil)
		case "job_id":
			s.serveNative(w, r, method, "/api/v1/exports/"+url.PathEscape(pathParam(r, "job_id")), nil, q, nil)
		default:
			s.serveNative(w, r, method, "/api/v1/exports/"+url.PathEscape(pathParam(r, "job_id"))+"/artifacts/"+
				url.PathEscape(pathParam(r, "target")), nil, q, nil)
		}
	}
}
