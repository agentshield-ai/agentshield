package server

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/agentshield-ai/agentshield/internal/weft/otlp"
	"github.com/agentshield-ai/agentshield/internal/weft/store"
	"github.com/google/uuid"
)

// Span is one traced function or model call (docs/weft/DESIGN.md §10.2).
type Span struct {
	ID          string         `json:"id"`
	ProjectID   string         `json:"project_id"`
	TraceID     string         `json:"trace_id"`
	ParentID    *string        `json:"parent_id"`
	Name        string         `json:"name"`
	DisplayName *string        `json:"display_name"`
	StartedAt   string         `json:"started_at"`
	EndedAt     *string        `json:"ended_at"`
	Inputs      map[string]any `json:"inputs"`
	Output      any            `json:"output"`
	Error       *string        `json:"error"`
	Attributes  map[string]any `json:"attributes"`
	Summary     map[string]any `json:"summary"`
	SessionID   *string        `json:"session_id"`
	TurnID      *string        `json:"turn_id"`
	UserID      *string        `json:"user_id"`
	RunID       *string        `json:"run_id"`
	RunStep     *int64         `json:"run_step"`
	RunStepEnd  *int64         `json:"run_step_end"`
	DeletedAt   *string        `json:"deleted_at"`
	ExpireAt    *string        `json:"expire_at"`
	// Present when asked for (include_* flags).
	Feedback              []Feedback `json:"feedback,omitempty"`
	StorageSizeBytes      *int64     `json:"storage_size_bytes,omitempty"`
	TotalStorageSizeBytes *int64     `json:"total_storage_size_bytes,omitempty"`
	Username              *string    `json:"username,omitempty"`
}

func spanFrom(c *store.Call, withSize bool) *Span {
	sp := &Span{ID: c.ID, ProjectID: c.ProjectID, TraceID: c.TraceID, ParentID: c.ParentID, Name: c.OpName,
		DisplayName: c.DisplayName, StartedAt: c.StartedAt, EndedAt: c.EndedAt, Inputs: c.Inputs, Output: c.Output,
		Error: c.Exception, Attributes: c.Attributes, Summary: c.Summary, SessionID: c.ThreadID, TurnID: c.TurnID,
		UserID: c.UserID, RunID: c.RunID, RunStep: c.RunStep, RunStepEnd: c.RunStepEnd, DeletedAt: c.DeletedAt,
		ExpireAt: c.ExpireAt, TotalStorageSizeBytes: c.TotalStorageSizeBytes, Username: c.Username}
	if withSize {
		sp.StorageSizeBytes = c.StorageSizeBytes
	}
	if c.Feedback != nil {
		sp.Feedback = make([]Feedback, len(c.Feedback))
		for i := range c.Feedback {
			sp.Feedback[i] = feedbackFrom(c.Feedback[i])
		}
	}
	return sp
}

// spanStart is the body of /api/v1/spans/start (and a start in a batch).
type spanStart struct {
	ProjectID   string         `json:"project_id"`
	ID          string         `json:"id"`
	TraceID     string         `json:"trace_id"`
	ParentID    *string        `json:"parent_id"`
	Name        string         `json:"name"`
	DisplayName *string        `json:"display_name"`
	StartedAt   string         `json:"started_at"`
	Attributes  map[string]any `json:"attributes"`
	Inputs      map[string]any `json:"inputs"`
	SessionID   *string        `json:"session_id"`
	TurnID      *string        `json:"turn_id"`
	UserID      *string        `json:"user_id"`
	RunID       *string        `json:"run_id"`
	RunStep     *int64         `json:"run_step"`
}

// check reports a span start the store would reject for a missing name
// (in Weft's words; the store says op_name).
func (st *spanStart) check() error {
	if strings.TrimSpace(st.Name) == "" {
		return store.Invalidf("name is required")
	}
	return nil
}

func (st *spanStart) store(defProject string) store.CallStart {
	pid := st.ProjectID
	if pid == "" {
		pid = defProject
	}
	return store.CallStart{ProjectID: pid, ID: st.ID, OpName: st.Name, DisplayName: st.DisplayName, TraceID: st.TraceID,
		ParentID: st.ParentID, ThreadID: st.SessionID, TurnID: st.TurnID, StartedAt: st.StartedAt, Attributes: st.Attributes,
		Inputs: st.Inputs, UserID: st.UserID, RunID: st.RunID, RunStep: st.RunStep}
}

// spanEnd is the body of /api/v1/spans/end (and an end in a batch).
type spanEnd struct {
	ProjectID  string         `json:"project_id"`
	ID         string         `json:"id"`
	EndedAt    string         `json:"ended_at"`
	Output     any            `json:"output"`
	Error      *string        `json:"error"`
	Summary    map[string]any `json:"summary"`
	RunStepEnd *int64         `json:"run_step_end"`
}

func (en *spanEnd) store(r *http.Request, defProject string) store.CallEnd {
	pid := en.ProjectID
	if pid == "" {
		pid = defProject
	}
	e := store.CallEnd{ProjectID: pid, ID: en.ID, EndedAt: en.EndedAt, Exception: en.Error, Output: en.Output,
		Summary: en.Summary, RunStepEnd: en.RunStepEnd, Cumulative: cumulativeSummaries(r)}
	if e.EndedAt == "" {
		e.EndedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	return e
}

// spanComplete is a finished span sent in one piece (in a batch).
type spanComplete struct {
	spanStart
	EndedAt    string         `json:"ended_at"`
	Output     any            `json:"output"`
	Error      *string        `json:"error"`
	Summary    map[string]any `json:"summary"`
	RunStepEnd *int64         `json:"run_step_end"`
}

// PrepareStart fills a span start's defaults: an id (UUIDv7), the start
// time, the user, and the trace id (the parent's, or its own id for a root).
func (s *Server) PrepareStart(ctx context.Context, r *http.Request, st *store.CallStart) error {
	if st.ID == "" {
		st.ID = uuid.Must(uuid.NewV7()).String()
	}
	if st.StartedAt == "" {
		st.StartedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	st.UserID = userFrom(r, st.UserID)
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
		return store.Invalidf("trace_id is required when the parent span is unknown")
	}
	return nil
}

func (s *Server) v1SpanStart(ctx context.Context, r *http.Request, req *spanStart) (any, error) {
	if err := req.check(); err != nil {
		return nil, err
	}
	st := req.store("")
	if err := s.PrepareStart(ctx, r, &st); err != nil {
		return nil, err
	}
	if err := s.store.StartCall(ctx, st); err != nil {
		return nil, err
	}
	return map[string]any{"id": st.ID, "trace_id": st.TraceID}, nil
}

func (s *Server) v1SpanEnd(ctx context.Context, r *http.Request, req *spanEnd) (any, error) {
	en := req.store(r, "")
	if err := s.store.EndCall(ctx, en); err != nil {
		return nil, err
	}
	pid, _ := project(en.ProjectID)
	s.engine.Notify([]store.EndedCall{{ProjectID: pid, ID: en.ID}})
	return empty, nil
}

type spanBatchReq struct {
	ProjectID string `json:"project_id"`
	Items     []struct {
		Start *spanStart    `json:"start"`
		End   *spanEnd      `json:"end"`
		Span  *spanComplete `json:"span"`
	} `json:"items"`
}

const maxSpanBatch = 100000

// v1SpanBatch applies starts, ends and complete spans in order, in one
// transaction.
func (s *Server) v1SpanBatch(ctx context.Context, r *http.Request, req *spanBatchReq) (any, error) {
	if len(req.Items) > maxSpanBatch {
		return nil, store.Invalidf("batch too large (max %d items)", maxSpanBatch)
	}
	var items []store.BatchItem
	res := make([]map[string]any, len(req.Items))
	for i, it := range req.Items {
		n := 0
		for _, set := range []bool{it.Start != nil, it.End != nil, it.Span != nil} {
			if set {
				n++
			}
		}
		if n != 1 {
			return nil, store.Invalidf("items[%d]: expected exactly one of start, end or span", i)
		}
		switch {
		case it.Start != nil:
			if err := it.Start.check(); err != nil {
				return nil, store.Invalidf("items[%d]: %v", i, err)
			}
			st := it.Start.store(req.ProjectID)
			if err := s.PrepareStart(ctx, r, &st); err != nil {
				return nil, err
			}
			items = append(items, store.BatchItem{Mode: "start", Start: &st})
			res[i] = map[string]any{"id": st.ID, "trace_id": st.TraceID}
		case it.End != nil:
			en := it.End.store(r, req.ProjectID)
			items = append(items, store.BatchItem{Mode: "end", End: &en})
			res[i] = map[string]any{"id": en.ID}
		default:
			if err := it.Span.check(); err != nil {
				return nil, store.Invalidf("items[%d]: %v", i, err)
			}
			st := it.Span.spanStart.store(req.ProjectID)
			if err := s.PrepareStart(ctx, r, &st); err != nil {
				return nil, err
			}
			e := spanEnd{ProjectID: st.ProjectID, ID: st.ID, EndedAt: it.Span.EndedAt, Output: it.Span.Output,
				Error: it.Span.Error, Summary: it.Span.Summary, RunStepEnd: it.Span.RunStepEnd}
			en := e.store(r, req.ProjectID)
			items = append(items, store.BatchItem{Mode: "start", Start: &st}, store.BatchItem{Mode: "end", End: &en})
			res[i] = map[string]any{"id": st.ID, "trace_id": st.TraceID}
		}
	}
	ended, err := s.store.UpsertBatch(ctx, items)
	if err != nil {
		return nil, err
	}
	s.engine.Notify(ended)
	return map[string]any{"items": res}, nil
}

// spansFilter is the native filter of span queries.
type spansFilter struct {
	Names          []string `json:"names"`
	IDs            []string `json:"ids"`
	TraceIDs       []string `json:"trace_ids"`
	ParentIDs      []string `json:"parent_ids"`
	SessionIDs     []string `json:"session_ids"`
	TurnIDs        []string `json:"turn_ids"`
	UserIDs        []string `json:"user_ids"`
	RunIDs         []string `json:"run_ids"`
	InputRefs      []string `json:"input_refs"`
	OutputRefs     []string `json:"output_refs"`
	TraceRootsOnly bool     `json:"trace_roots_only"`
}

// UnmarshalJSON rejects unknown filter fields.
func (f *spansFilter) UnmarshalJSON(b []byte) error {
	type plain spansFilter
	var p plain
	if err := strictJSON(b, &p); err != nil {
		return err
	}
	*f = spansFilter(p)
	return nil
}

func (f *spansFilter) store() *store.CallsFilter {
	if f == nil {
		return nil
	}
	return &store.CallsFilter{OpNames: f.Names, CallIDs: f.IDs, TraceIDs: f.TraceIDs, ParentIDs: f.ParentIDs,
		ThreadIDs: f.SessionIDs, TurnIDs: f.TurnIDs, UserIDs: f.UserIDs, RunIDs: f.RunIDs, InputRefs: f.InputRefs,
		OutputRefs: f.OutputRefs, TraceRootsOnly: f.TraceRootsOnly}
}

// spansQuery is the body of /api/v1/spans/query, /stream and /export.
type spansQuery struct {
	ProjectID     string         `json:"project_id"`
	Filter        *spansFilter   `json:"filter"`
	Query         any            `json:"query"`
	Search        string         `json:"search"`
	SortBy        []store.SortBy `json:"sort_by"`
	Limit         *int           `json:"limit"`
	Offset        int            `json:"offset"`
	Columns       []string       `json:"columns"`
	ExpandColumns []string       `json:"expand_columns"`

	IncludeCosts            bool `json:"include_costs"`
	IncludeFeedback         bool `json:"include_feedback"`
	IncludeStorageSize      bool `json:"include_storage_size"`
	IncludeTotalStorageSize bool `json:"include_total_storage_size"`
	IncludeUsernames        bool `json:"include_usernames"`
	IncludeDeleted          bool `json:"include_deleted"`
	IncludeRunStatus        bool `json:"include_run_status"`
}

func (q *spansQuery) store() store.CallsQuery {
	return store.CallsQuery{ProjectID: q.ProjectID, Filter: q.Filter.store(), Query: q.Query, Search: q.Search,
		SortBy: q.SortBy, Limit: q.Limit, Offset: q.Offset, Columns: q.Columns, ExpandColumns: q.ExpandColumns,
		IncludeCosts: q.IncludeCosts, IncludeFeedback: q.IncludeFeedback, IncludeStorageSize: q.IncludeStorageSize,
		IncludeDeleted: q.IncludeDeleted, IncludeTotalStorageSize: q.IncludeTotalStorageSize,
		IncludeUsernames: q.IncludeUsernames, IncludeRunStatus: q.IncludeRunStatus}
}

const (
	defaultListLimit = 100
	maxListLimit     = 10000
)

// listLimit applies the default and maximum page size of native lists.
func listLimit(l *int) (int, error) {
	if l == nil {
		return defaultListLimit, nil
	}
	if *l < 0 || *l > maxListLimit {
		return 0, store.Invalidf("limit must be between 0 and %d", maxListLimit)
	}
	return *l, nil
}

func (s *Server) v1SpansQuery(ctx context.Context, _ *http.Request, req *spansQuery) (any, error) {
	limit, err := listLimit(req.Limit)
	if err != nil {
		return nil, err
	}
	q := req.store()
	pid, err := project(q.ProjectID)
	if err != nil {
		return nil, err
	}
	q.ProjectID = pid
	total, _, err := s.store.CountCallsDeleted(ctx, pid, q.Filter, q.Query, q.Search, q.IncludeDeleted)
	if err != nil {
		return nil, err
	}
	spans := []*Span{}
	if limit > 0 {
		q.Limit = &limit
		calls, err := s.store.ListCalls(nativeRefs(ctx), q)
		if err != nil {
			return nil, err
		}
		for _, c := range calls {
			spans = append(spans, spanFrom(c, req.IncludeStorageSize))
		}
	}
	return newList(spans, total), nil
}

// v1SpansStream streams every matching span (no default limit).
func (s *Server) v1SpansStream(w http.ResponseWriter, r *http.Request) {
	var req spansQuery
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if req.Limit != nil && *req.Limit < 0 {
		writeErr(w, store.Invalidf("limit must not be negative"))
		return
	}
	// Run the query fully before writing so errors produce a proper status.
	calls, err := s.store.ListCalls(nativeRefs(r.Context()), req.store())
	if err != nil {
		writeErr(w, err)
		return
	}
	out := newNDJSON(w)
	for _, c := range calls {
		if out.write(spanFrom(c, req.IncludeStorageSize)) != nil {
			return
		}
	}
	out.flush()
}

func (s *Server) v1SpanGet(w http.ResponseWriter, r *http.Request) {
	pid, err := project(r.URL.Query().Get("project_id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	withSize := queryBool(r, "include_storage_size")
	c, err := s.store.GetCallWith(r.Context(), store.CallsQuery{ProjectID: pid, IncludeCosts: queryBool(r, "include_costs"),
		IncludeFeedback: queryBool(r, "include_feedback"), IncludeStorageSize: withSize,
		IncludeTotalStorageSize: queryBool(r, "include_total_storage_size"), IncludeUsernames: queryBool(r, "include_usernames")},
		pathParam(r, "id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, spanFrom(c, withSize))
}

type spanUpdateReq struct {
	ProjectID   string  `json:"project_id"`
	ID          string  `json:"id"`
	DisplayName *string `json:"display_name"`
}

func (s *Server) v1SpanUpdate(ctx context.Context, _ *http.Request, req *spanUpdateReq) (any, error) {
	pid, err := project(req.ProjectID)
	if err != nil {
		return nil, err
	}
	if req.DisplayName == nil {
		return nil, store.Invalidf("display_name is required")
	}
	return empty, s.store.UpdateCallDisplayName(ctx, pid, req.ID, *req.DisplayName)
}

type spansDeleteReq struct {
	ProjectID string   `json:"project_id"`
	IDs       []string `json:"ids"`
}

func (s *Server) v1SpansDelete(ctx context.Context, _ *http.Request, req *spansDeleteReq) (any, error) {
	pid, err := project(req.ProjectID)
	if err != nil {
		return nil, err
	}
	if len(req.IDs) == 0 {
		return nil, store.Invalidf("ids is required")
	}
	return empty, s.store.DeleteCalls(ctx, pid, req.IDs)
}

// spansStatsReq is the body of /api/v1/spans/stats: a time series, or
// metric buckets when any metric-bucket field is set (apishape.go).
type spansStatsReq struct {
	ProjectID     string       `json:"project_id"`
	Search        string       `json:"search"`
	Filter        *spansFilter `json:"filter"`
	Query         any          `json:"query"`
	Start         string       `json:"start"`
	End           string       `json:"end"`
	BucketSeconds int64        `json:"bucket_seconds"`
	metricStatsFields
}

func (s *Server) v1SpansStats(ctx context.Context, r *http.Request, req *spansStatsReq) (any, error) {
	cs := &callsStatsReq{ProjectID: req.ProjectID, Search: req.Search, Filter: req.Filter.store(), Query: req.Query,
		Start: req.Start, End: req.End, BucketSeconds: req.BucketSeconds, metricStatsFields: req.metricStatsFields}
	return s.callsStats(ctx, r, cs)
}

type spansMetricsReq struct {
	ProjectID     string             `json:"project_id"`
	Filter        *spansFilter       `json:"filter"`
	Query         any                `json:"query"`
	Search        string             `json:"search"`
	Metrics       []store.MetricSpec `json:"metrics"`
	GroupBy       string             `json:"group_by"`
	BucketSeconds int64              `json:"bucket_seconds"`
	Start         string             `json:"start"`
	End           string             `json:"end"`
	MaxGroups     int                `json:"max_groups"`
}

func (s *Server) v1SpansMetrics(ctx context.Context, _ *http.Request, req *spansMetricsReq) (any, error) {
	return s.store.ComputeCallsMetrics(ctx, store.MetricsQuery{ProjectID: req.ProjectID, Filter: req.Filter.store(), Query: req.Query,
		Search: req.Search, Metrics: req.Metrics, GroupBy: req.GroupBy, BucketSeconds: req.BucketSeconds, Start: req.Start,
		End: req.End, MaxGroups: req.MaxGroups})
}

type spanNamesReq struct {
	ProjectID      string `json:"project_id"`
	TraceRootsOnly bool   `json:"trace_roots_only"`
}

// SpanName counts the spans of one name.
type SpanName struct {
	Name          string `json:"name"`
	BaseName      string `json:"base_name"`
	Count         int64  `json:"count"`
	ErrorCount    int64  `json:"error_count"`
	LastStartedAt string `json:"last_started_at"`
}

func (s *Server) v1SpanNames(ctx context.Context, _ *http.Request, req *spanNamesReq) (any, error) {
	ops, err := s.store.OpCounts(ctx, req.ProjectID, req.TraceRootsOnly)
	if err != nil {
		return nil, err
	}
	out := make([]SpanName, len(ops))
	for i, o := range ops {
		out[i] = SpanName{Name: o.OpName, BaseName: o.OpBaseName, Count: o.Count, ErrorCount: o.ErrorCount, LastStartedAt: o.LastCalled}
	}
	return newList(out, int64(len(out))), nil
}

type spansScoreReq struct {
	ProjectID  string   `json:"project_id"`
	IDs        []string `json:"ids"`
	ScorerRefs []string `json:"scorer_refs"`
	UserID     *string  `json:"user_id"`
}

// SpanScore is one scorer's result on one span.
type SpanScore struct {
	SpanID       string `json:"span_id"`
	Scorer       string `json:"scorer"`
	Output       any    `json:"output"`
	ScorerSpanID string `json:"scorer_span_id,omitempty"`
	FeedbackID   string `json:"feedback_id,omitempty"`
	Error        string `json:"error,omitempty"`
}

func (s *Server) v1SpansScore(ctx context.Context, r *http.Request, req *spansScoreReq) (any, error) {
	pid, err := project(req.ProjectID)
	if err != nil {
		return nil, err
	}
	res, err := s.engine.ScoreCalls(ctx, pid, req.IDs, req.ScorerRefs, userFrom(r, req.UserID))
	if err != nil {
		return nil, err
	}
	out := make([]SpanScore, len(res))
	for i, x := range res {
		out[i] = SpanScore{SpanID: x.CallID, Scorer: x.Scorer, Output: x.Output, ScorerSpanID: x.ScorerCallID,
			FeedbackID: x.FeedbackID, Error: x.Error}
	}
	return map[string]any{"items": out}, nil
}

// spanSessionsReq lists the session ids found on spans with their turn
// statistics (the raw view under GET /api/v1/sessions).
type spanSessionsReq struct {
	ProjectID string         `json:"project_id"`
	Limit     *int           `json:"limit"`
	Offset    int            `json:"offset"`
	SortBy    []store.SortBy `json:"sort_by"`
	Filter    *struct {
		StartedAfter  string   `json:"started_after"`
		StartedBefore string   `json:"started_before"`
		SessionIDs    []string `json:"session_ids"`
	} `json:"filter"`
}

// SpanSession is the turn statistics of one session id.
type SpanSession struct {
	SessionID         string   `json:"session_id"`
	TurnCount         int64    `json:"turn_count"`
	SpanCount         int64    `json:"span_count"`
	StartedAt         string   `json:"started_at"`
	LastUpdated       string   `json:"last_updated"`
	FirstTurnID       *string  `json:"first_turn_id"`
	LastTurnID        *string  `json:"last_turn_id"`
	P50TurnDurationMS *float64 `json:"p50_turn_duration_ms"`
	P99TurnDurationMS *float64 `json:"p99_turn_duration_ms"`
	TotalTokens       float64  `json:"total_tokens"`
	CostUSD           float64  `json:"cost_usd"`
}

func (s *Server) v1SpanSessions(ctx context.Context, _ *http.Request, req *spanSessionsReq) (any, error) {
	limit, err := listLimit(req.Limit)
	if err != nil {
		return nil, err
	}
	q := store.ThreadsQuery{ProjectID: req.ProjectID, Offset: req.Offset}
	for _, sb := range req.SortBy {
		if sb.Field == "started_at" {
			sb.Field = "start_time"
		}
		q.SortBy = append(q.SortBy, sb)
	}
	if f := req.Filter; f != nil {
		q.Filter = &struct {
			AfterDatetime  string   `json:"after_datetime"`
			BeforeDatetime string   `json:"before_datetime"`
			ThreadIDs      []string `json:"thread_ids"`
		}{f.StartedAfter, f.StartedBefore, f.SessionIDs}
	}
	all := q
	all.Offset = 0
	everything, err := s.store.QueryThreads(ctx, all)
	if err != nil {
		return nil, err
	}
	out := []SpanSession{}
	for i := req.Offset; i < len(everything) && len(out) < limit; i++ {
		t := everything[i]
		out = append(out, SpanSession{SessionID: t.ThreadID, TurnCount: t.TurnCount, SpanCount: t.CallCount, StartedAt: t.StartTime,
			LastUpdated: t.LastUpdated, FirstTurnID: t.FirstTurnID, LastTurnID: t.LastTurnID, P50TurnDurationMS: t.P50TurnDurationMS,
			P99TurnDurationMS: t.P99TurnDurationMS, TotalTokens: t.TotalTokens, CostUSD: t.CostUSD})
	}
	return newList(out, int64(len(everything))), nil
}

// v1SpansExport streams matching spans as CSV, JSONL or a JSON array
// (export.go), with Weft's field names.
func (s *Server) v1SpansExport(w http.ResponseWriter, r *http.Request) {
	var req struct {
		spansQuery
		Format  string `json:"format"`
		Flatten *bool  `json:"flatten"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	s.exportCalls(w, r.WithContext(nativeRefs(r.Context())), callsExportReq{CallsQuery: req.spansQuery.store(), Format: req.Format, Flatten: req.Flatten}, true)
}

// ---------------------------------------------------------------- request plumbing

// readBodyRestore reads a (possibly compressed) JSON body and puts it back
// decompressed, so the handler can read it again.
func readBodyRestore(r *http.Request) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	body, err := otlp.ReadBody(http.MaxBytesReader(nil, r.Body, maxJSONBody), r.Header.Get("Content-Encoding"))
	if err != nil {
		return nil, store.Invalidf("reading body: %v", err)
	}
	r.Header.Del("Content-Encoding")
	setBody(r, body)
	return body, nil
}

func setBody(r *http.Request, body []byte) {
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	r.Header.Del("Content-Length")
}
