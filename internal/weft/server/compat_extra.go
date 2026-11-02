package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/agentshield-ai/agentshield/internal/weft/refs"
	"github.com/agentshield-ai/agentshield/internal/weft/store"
	"github.com/go-chi/chi/v5"
)

// Smaller Weave trace-server endpoints, following the vendored server SDK
// schemas: feedback analytics, table and file stats, usage roll-ups, thread
// streaming, evaluation aliases and rescoring, and dataset lineage.

func init() {
	routeExtensions = append(routeExtensions, func(s *Server, r chi.Router) {
		r.Post("/feedback/stats", handle(s.feedbackStats))
		r.Post("/feedback/aggregate", handle(s.feedbackAggregate))
		r.Post("/feedback/payload_schema", handle(s.feedbackPayloadSchema))

		r.Post("/table/create_from_digests", handle(s.tableCreateFromDigests))
		r.Post("/table/query_stats_batch", handle(s.tableQueryStatsBatch))
		r.Post("/files/query_stats", handle(s.filesQueryStats))

		r.Post("/calls/usage", handle(s.callsUsage))
		r.Post("/trace/usage", handle(s.traceUsage))
		r.Post("/threads/stream_query", s.threadsStreamQuery)

		r.Post("/evaluations/evaluate_model", handle(s.evaluateModel))
		r.Post("/evaluations/status", handle(s.evaluationsStatus))
		r.Post("/evaluations/rescore", handle(s.evaluationsRescore))

		r.Post("/dataset_sources/link", handle(s.datasetSourcesLink))
		r.Post("/dataset_sources/query", handle(s.datasetSourcesQuery))
		r.Post("/dataset_sources/source_datasets_query", handle(s.sourceDatasetsQuery))
		r.Post("/dataset_sources/link_delete", handle(s.datasetSourcesLinkDelete))
	})
}

// ---------------------------------------------------------------- feedback analytics

type feedbackMetric struct {
	JSONPath     string    `json:"json_path"`
	Aggregations []string  `json:"aggregations"`
	Percentiles  []float64 `json:"percentiles"`
	ValueType    string    `json:"value_type"`
}

type feedbackStatsReq struct {
	ProjectID    string           `json:"project_id"`
	Start        string           `json:"start"`
	End          string           `json:"end"`
	FeedbackType *string          `json:"feedback_type"`
	Granularity  *int64           `json:"granularity"`
	Metrics      []feedbackMetric `json:"metrics"`
	Timezone     string           `json:"timezone"`
	TriggerRef   *string          `json:"trigger_ref"`
}

var granularities = []int64{60, 300, 900, 1800, 3600, 3 * 3600, 6 * 3600, 12 * 3600, 86400, 7 * 86400, 30 * 86400}

func autoGranularity(span time.Duration) int64 {
	for _, g := range granularities {
		if span/time.Duration(g*int64(time.Second)) <= 100 {
			return g
		}
	}
	return granularities[len(granularities)-1]
}

func parseRange(start, end string) (time.Time, time.Time, error) {
	if start == "" {
		return time.Time{}, time.Time{}, store.Invalidf("start is required")
	}
	s, err := store.ParseTime(start)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	e := time.Now()
	if end != "" {
		us, err := store.ParseTime(end)
		if err != nil {
			return time.Time{}, time.Time{}, err
		}
		e = time.UnixMicro(us)
	}
	st := time.UnixMicro(s)
	if !e.After(st) {
		return time.Time{}, time.Time{}, store.Invalidf("end must be after start")
	}
	return st.UTC(), e.UTC(), nil
}

func metricSlug(path string) string {
	return strings.Trim(regexp.MustCompile(`[^A-Za-z0-9]+`).ReplaceAllString(path, "_"), "_")
}

// payloadPath walks a dot path into a payload ("output.score").
func payloadPath(v any, path string) (any, bool) {
	if path == "" {
		return v, true
	}
	cur := v
	for _, p := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = m[p]; !ok {
			return nil, false
		}
	}
	return cur, true
}

type metricAcc struct {
	nums             []float64
	count, trues, fs int
}

func (a *metricAcc) add(m feedbackMetric, v any) {
	switch m.ValueType {
	case "boolean":
		if b, ok := v.(bool); ok {
			a.count++
			if b {
				a.trues++
			} else {
				a.fs++
			}
		}
	case "categorical":
		if v != nil {
			a.count++
		}
	default:
		if _, isBool := v.(bool); isBool {
			return
		}
		if f, ok := store.ToFloat(v); ok && !math.IsNaN(f) {
			a.nums = append(a.nums, f)
			a.count++
		}
	}
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 1 {
		return sorted[0]
	}
	rank := p / 100 * float64(len(sorted)-1)
	lo := int(math.Floor(rank))
	hi := int(math.Ceil(rank))
	return sorted[lo] + (sorted[hi]-sorted[lo])*(rank-float64(lo))
}

func (a *metricAcc) results(m feedbackMetric) map[string]*float64 {
	out := map[string]*float64{}
	f := func(v float64) *float64 { return &v }
	aggs := m.Aggregations
	if len(aggs) == 0 {
		switch m.ValueType {
		case "boolean":
			aggs = []string{"count_true", "count_false"}
		case "categorical":
			aggs = []string{"count"}
		default:
			aggs = []string{"avg", "min", "max"}
		}
	}
	sorted := append([]float64(nil), a.nums...)
	sort.Float64s(sorted)
	for _, agg := range aggs {
		var v *float64
		switch agg {
		case "count":
			v = f(float64(a.count))
		case "count_true":
			v = f(float64(a.trues))
		case "count_false":
			v = f(float64(a.fs))
		case "sum", "avg", "min", "max":
			if len(sorted) > 0 {
				var sum float64
				for _, x := range sorted {
					sum += x
				}
				switch agg {
				case "sum":
					v = f(sum)
				case "avg":
					v = f(sum / float64(len(sorted)))
				case "min":
					v = f(sorted[0])
				case "max":
					v = f(sorted[len(sorted)-1])
				}
			}
		}
		out[agg] = v
	}
	if m.ValueType == "" || m.ValueType == "numeric" {
		for _, p := range m.Percentiles {
			key := "p" + strings.TrimSuffix(strings.TrimRight(fmt.Sprintf("%.2f", p), "0"), ".")
			if len(sorted) > 0 && p >= 0 && p <= 100 {
				out[key] = f(percentile(sorted, p))
			} else {
				out[key] = nil
			}
		}
	}
	return out
}

func (s *Server) feedbackStats(ctx context.Context, _ *http.Request, req *feedbackStatsReq) (any, error) {
	start, end, err := parseRange(req.Start, req.End)
	if err != nil {
		return nil, err
	}
	loc := time.UTC
	if req.Timezone != "" {
		if loc, err = time.LoadLocation(req.Timezone); err != nil {
			return nil, store.Invalidf("unknown timezone %q", req.Timezone)
		}
	}
	gran := autoGranularity(end.Sub(start))
	if req.Granularity != nil && *req.Granularity > 0 {
		gran = *req.Granularity
	}
	if end.Sub(start)/time.Duration(gran*int64(time.Second)) > 10000 {
		return nil, store.Invalidf("granularity too small for the time range")
	}
	if len(req.Metrics) > 50 {
		return nil, store.Invalidf("too many metrics (max 50)")
	}
	f := store.FeedbackFilter{StartUS: start.UnixMicro(), EndUS: end.UnixMicro(), FeedbackType: req.FeedbackType}
	if req.TriggerRef != nil && *req.TriggerRef != "" {
		f.TriggerRefs = []string{*req.TriggerRef}
	}
	rows, err := s.store.FeedbackForAnalytics(ctx, req.ProjectID, f)
	if err != nil {
		return nil, err
	}
	// Bucket boundaries aligned to the timezone (day and week buckets start at local midnight).
	_, offset := start.In(loc).Zone()
	g := gran * 1e6
	align := func(us int64) int64 {
		local := us + int64(offset)*1e6
		return local - ((local%g)+g)%g - int64(offset)*1e6
	}
	type bucket struct {
		count int
		accs  []*metricAcc
	}
	newAccs := func() []*metricAcc {
		a := make([]*metricAcc, len(req.Metrics))
		for i := range a {
			a[i] = &metricAcc{}
		}
		return a
	}
	buckets := map[int64]*bucket{}
	window := newAccs()
	for _, row := range rows {
		k := align(row.CreatedUS)
		b := buckets[k]
		if b == nil {
			b = &bucket{accs: newAccs()}
			buckets[k] = b
		}
		b.count++
		for i, m := range req.Metrics {
			if v, ok := payloadPath(row.Payload, m.JSONPath); ok {
				b.accs[i].add(m, v)
				window[i].add(m, v)
			}
		}
	}
	out := []map[string]any{}
	for k := align(start.UnixMicro()); k < end.UnixMicro(); k += g {
		entry := map[string]any{"timestamp": time.UnixMicro(k).In(loc).Format(time.RFC3339), "count": 0}
		b := buckets[k]
		for i, m := range req.Metrics {
			acc := &metricAcc{}
			if b != nil {
				acc = b.accs[i]
			}
			for agg, v := range acc.results(m) {
				if v == nil {
					entry[agg+"__"+metricSlug(m.JSONPath)] = nil
				} else {
					entry[agg+"__"+metricSlug(m.JSONPath)] = *v
				}
			}
		}
		if b != nil {
			entry["count"] = b.count
		}
		out = append(out, entry)
	}
	ws := map[string]map[string]*float64{}
	for i, m := range req.Metrics {
		ws[metricSlug(m.JSONPath)] = window[i].results(m)
	}
	tz := req.Timezone
	if tz == "" {
		tz = "UTC"
	}
	return map[string]any{"start": start.Format(time.RFC3339Nano), "end": end.Format(time.RFC3339Nano), "granularity": gran,
		"timezone": tz, "buckets": out, "window_stats": ws}, nil
}

type feedbackAggregateReq struct {
	ProjectID         string   `json:"project_id"`
	AfterMS           *int64   `json:"after_ms"`
	BeforeMS          *int64   `json:"before_ms"`
	FeedbackTypes     []string `json:"feedback_types"`
	GroupBy           []string `json:"group_by"`
	MonitorIDs        []string `json:"monitor_ids"`
	ScorerIDs         []string `json:"scorer_ids"`
	RatingMin         *float64 `json:"rating_min"`
	RatingMax         *float64 `json:"rating_max"`
	Tags              []string `json:"tags"`
	TimeBucketSeconds *int64   `json:"time_bucket_seconds"`
	SpanAgentNames    []string `json:"span_agent_names"`
	SpanTypes         []string `json:"span_types"`
}

// scorerID is the scorer object id behind a runnable feedback row.
func scorerID(row store.FeedbackRow) string {
	if row.RunnableRef != nil {
		if r, err := refs.Parse(*row.RunnableRef); err == nil {
			return r.Name
		}
	}
	return strings.TrimPrefix(row.FeedbackType, "wandb.runnable.")
}

// feedbackScores flattens a payload's output into ratings (numbers) and tags
// (true booleans and string labels), keyed by path.
func feedbackScores(payload any) (map[string]float64, []string) {
	ratings := map[string]float64{}
	var tags []string
	v, ok := payloadPath(payload, "output")
	if !ok {
		v = payload
	}
	var walk func(prefix string, x any)
	walk = func(prefix string, x any) {
		key := prefix
		if key == "" {
			key = "_rating_"
		}
		switch t := x.(type) {
		case map[string]any:
			for k, y := range t {
				p := k
				if prefix != "" {
					p = prefix + "." + k
				}
				walk(p, y)
			}
		case bool:
			if t {
				if prefix == "" {
					tags = append(tags, "true")
				} else {
					tags = append(tags, prefix)
				}
			}
		case string:
			if prefix == "" || len(t) <= 64 {
				tags = append(tags, t)
			}
		default:
			if f, ok := store.ToFloat(x); ok {
				ratings[key] = f
			}
		}
	}
	walk("", v)
	return ratings, tags
}

func (s *Server) feedbackAggregate(ctx context.Context, _ *http.Request, req *feedbackAggregateReq) (any, error) {
	if req.AfterMS == nil || req.BeforeMS == nil || *req.BeforeMS <= *req.AfterMS {
		return nil, store.Invalidf("after_ms and before_ms are required and before_ms must be later")
	}
	for _, g := range req.GroupBy {
		switch g {
		case "scorer_id", "span_agent_name", "span_agent_version", "span_status_code":
		default:
			return nil, store.Invalidf("cannot group by %q", g)
		}
	}
	f := store.FeedbackFilter{StartUS: *req.AfterMS * 1000, EndUS: *req.BeforeMS * 1000, TypePrefixes: req.FeedbackTypes, TriggerRefs: req.MonitorIDs}
	rows, err := s.store.FeedbackForAnalytics(ctx, req.ProjectID, f)
	if err != nil {
		return nil, err
	}
	matchID := func(id string, patterns []string) bool {
		if len(patterns) == 0 {
			return true
		}
		for _, p := range patterns {
			if p == id || (strings.HasSuffix(p, "*") && strings.HasPrefix(id, strings.TrimSuffix(p, "*"))) {
				return true
			}
		}
		return false
	}
	type agg struct {
		group                 map[string]string
		bucket                *int64
		total, scored         int
		ratingCounts, tagCnts map[string]int
		ratingSums            map[string]float64
	}
	groups := map[string]*agg{}
	var order []string
	for _, row := range rows {
		sid := scorerID(row)
		if !matchID(sid, req.ScorerIDs) {
			continue
		}
		ratings, tags := feedbackScores(row.Payload)
		if req.RatingMin != nil || req.RatingMax != nil {
			r, ok := ratings["_rating_"]
			if !ok || (req.RatingMin != nil && r < *req.RatingMin) || (req.RatingMax != nil && r > *req.RatingMax) {
				continue
			}
		}
		if len(req.Tags) > 0 {
			hit := false
			for _, t := range tags {
				for _, want := range req.Tags {
					hit = hit || t == want
				}
			}
			if !hit {
				continue
			}
		}
		group := map[string]string{}
		for _, g := range req.GroupBy {
			group[g] = ""
			if g == "scorer_id" {
				group[g] = sid
			}
		}
		var bucket *int64
		if req.TimeBucketSeconds != nil && *req.TimeBucketSeconds > 0 {
			ms := row.CreatedUS / 1000
			w := *req.TimeBucketSeconds * 1000
			b := ms - ms%w
			bucket = &b
		}
		gk, _ := json.Marshal(group)
		key := string(gk)
		if bucket != nil {
			key += fmt.Sprint(*bucket)
		}
		a := groups[key]
		if a == nil {
			a = &agg{group: group, bucket: bucket, ratingCounts: map[string]int{}, tagCnts: map[string]int{}, ratingSums: map[string]float64{}}
			groups[key] = a
			order = append(order, key)
		}
		a.total++
		if len(ratings) > 0 || len(tags) > 0 {
			a.scored++
		}
		for k, v := range ratings {
			a.ratingCounts[k]++
			a.ratingSums[k] += v
		}
		for _, t := range tags {
			a.tagCnts[t]++
		}
	}
	buckets := make([]map[string]any, 0, len(order))
	for _, k := range order {
		a := groups[k]
		buckets = append(buckets, map[string]any{"group": a.group, "rating_counts": a.ratingCounts, "rating_sums": a.ratingSums,
			"scored_count": a.scored, "tag_counts": a.tagCnts, "time_bucket_start_ms": a.bucket, "total_count": a.total})
	}
	sort.SliceStable(buckets, func(i, j int) bool {
		bi, _ := buckets[i]["time_bucket_start_ms"].(*int64)
		bj, _ := buckets[j]["time_bucket_start_ms"].(*int64)
		return bi != nil && bj != nil && *bi < *bj
	})
	return map[string]any{"after_ms": *req.AfterMS, "before_ms": *req.BeforeMS, "buckets": buckets, "time_bucket_seconds": req.TimeBucketSeconds}, nil
}

type feedbackPayloadSchemaReq struct {
	ProjectID    string  `json:"project_id"`
	Start        string  `json:"start"`
	End          string  `json:"end"`
	FeedbackType *string `json:"feedback_type"`
	SampleLimit  int     `json:"sample_limit"`
	TriggerRef   *string `json:"trigger_ref"`
}

func (s *Server) feedbackPayloadSchema(ctx context.Context, _ *http.Request, req *feedbackPayloadSchemaReq) (any, error) {
	start, end, err := parseRange(req.Start, req.End)
	if err != nil {
		return nil, err
	}
	limit := req.SampleLimit
	if limit <= 0 {
		limit = 2000
	}
	limit = min(limit, 5000)
	f := store.FeedbackFilter{StartUS: start.UnixMicro(), EndUS: end.UnixMicro(), FeedbackType: req.FeedbackType}
	if req.TriggerRef != nil && *req.TriggerRef != "" {
		f.TriggerRefs = []string{*req.TriggerRef}
	}
	rows, err := s.store.FeedbackForAnalytics(ctx, req.ProjectID, f)
	if err != nil {
		return nil, err
	}
	// Sample one payload per distinct trigger ref (or feedback type), newest first.
	sampled := map[string]bool{}
	types := map[string]string{}
	var order []string
	var walk func(prefix string, v any)
	walk = func(prefix string, v any) {
		var t string
		switch x := v.(type) {
		case map[string]any:
			for k, y := range x {
				p := k
				if prefix != "" {
					p = prefix + "." + k
				}
				walk(p, y)
			}
			return
		case bool:
			t = "boolean"
		case string:
			t = "categorical"
		case nil, []any:
			return
		default:
			if _, ok := store.ToFloat(x); !ok {
				return
			}
			t = "numeric"
		}
		if prefix == "" {
			return
		}
		if _, ok := types[prefix]; !ok {
			order = append(order, prefix)
			types[prefix] = t
		}
	}
	for i := len(rows) - 1; i >= 0 && len(sampled) < limit; i-- {
		key := rows[i].FeedbackType
		if rows[i].TriggerRef != nil {
			key = *rows[i].TriggerRef
		}
		if sampled[key] {
			continue
		}
		sampled[key] = true
		walk("", rows[i].Payload)
	}
	sort.Strings(order)
	paths := make([]map[string]any, 0, len(order))
	for _, p := range order {
		paths = append(paths, map[string]any{"json_path": p, "value_type": types[p]})
	}
	return map[string]any{"paths": paths}, nil
}

// ---------------------------------------------------------------- tables and files

type tableFromDigestsReq struct {
	ProjectID      string   `json:"project_id"`
	RowDigests     []string `json:"row_digests"`
	ExpectedDigest *string  `json:"expected_digest"`
}

func (s *Server) tableCreateFromDigests(ctx context.Context, _ *http.Request, req *tableFromDigestsReq) (any, error) {
	if req.RowDigests == nil {
		req.RowDigests = []string{}
	}
	if req.ExpectedDigest != nil && *req.ExpectedDigest != "" && refs.TableDigest(req.RowDigests) != *req.ExpectedDigest {
		return nil, store.Invalidf("expected_digest %q does not match the table digest %q", *req.ExpectedDigest, refs.TableDigest(req.RowDigests))
	}
	d, err := s.store.CreateTableFromDigests(ctx, req.ProjectID, req.RowDigests)
	if err != nil {
		return nil, err
	}
	return map[string]any{"digest": d}, nil
}

type tableStatsBatchReq struct {
	ProjectID          string   `json:"project_id"`
	Digests            []string `json:"digests"`
	IncludeStorageSize *bool    `json:"include_storage_size"`
}

func (s *Server) tableQueryStatsBatch(ctx context.Context, _ *http.Request, req *tableStatsBatchReq) (any, error) {
	ts, err := s.store.TableStatsBatch(ctx, req.ProjectID, req.Digests, req.IncludeStorageSize != nil && *req.IncludeStorageSize)
	if err != nil {
		return nil, err
	}
	return map[string]any{"tables": ts}, nil
}

func (s *Server) filesQueryStats(ctx context.Context, _ *http.Request, req *projectReq) (any, error) {
	n, err := s.store.FilesTotalSize(ctx, req.ProjectID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"total_size_bytes": n}, nil
}

// ---------------------------------------------------------------- usage

// usageItems renders rolled-up usage per model in Weave's LLMUsageSchema shape.
func usageItems(u store.Contribution, pb *store.PriceBook, atUS int64) map[string]any {
	out := map[string]any{}
	var costs map[string]any
	if pb != nil {
		costs = pb.Costs(u, atUS)
	}
	for model, m := range u.Usage {
		prompt, completion, total := store.UsageTokens(m)
		item := map[string]any{"requests": int64(m["requests"]), "prompt_tokens": int64(prompt), "completion_tokens": int64(completion),
			"total_tokens": int64(total), "cache_creation_input_tokens": int64(m["cache_creation_input_tokens"]),
			"cache_read_input_tokens": int64(m["cache_read_input_tokens"])}
		if pb != nil {
			item["prompt_tokens_total_cost"], item["completion_tokens_total_cost"] = nil, nil
			item["cache_creation_input_tokens_total_cost"], item["cache_read_input_tokens_total_cost"] = nil, nil
			if c, ok := costs[model].(map[string]any); ok {
				item["prompt_tokens_total_cost"] = c["prompt_tokens_total_cost"]
				item["completion_tokens_total_cost"] = c["completion_tokens_total_cost"]
			}
		}
		out[model] = item
	}
	return out
}

type callsUsageReq struct {
	ProjectID    string   `json:"project_id"`
	CallIDs      []string `json:"call_ids"`
	IncludeCosts bool     `json:"include_costs"`
	Limit        int      `json:"limit"`
}

// callsUsage reports each call's usage including all descendants. Weft keeps
// usage rolled up on every call, so this reads the calls directly; a call is
// unfinished while it or a descendant is still running.
func (s *Server) callsUsage(ctx context.Context, _ *http.Request, req *callsUsageReq) (any, error) {
	pid, err := project(req.ProjectID)
	if err != nil {
		return nil, err
	}
	if len(req.CallIDs) > 1000 {
		return nil, store.Invalidf("too many call_ids (max 1000)")
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 100000
	}
	var pb *store.PriceBook
	if req.IncludeCosts {
		if pb, err = s.store.LoadPriceBook(ctx, pid); err != nil {
			return nil, err
		}
	}
	usage := map[string]any{}
	unfinished := []string{}
	if len(req.CallIDs) == 0 {
		return map[string]any{"call_usage": usage, "unfinished_call_ids": unfinished}, nil
	}
	roots, err := s.store.ListCalls(ctx, store.CallsQuery{ProjectID: pid, Filter: &store.CallsFilter{CallIDs: req.CallIDs}})
	if err != nil {
		return nil, err
	}
	for _, c := range roots {
		usage[c.ID] = usageItems(c.TotalUsage(), pb, c.StartedMicros())
		// Find running calls in the subtree.
		trace, err := s.store.ListCalls(ctx, store.CallsQuery{ProjectID: pid, Filter: &store.CallsFilter{TraceIDs: []string{c.TraceID}},
			Columns: []string{"id", "parent_id", "ended_at"}, Limit: &limit})
		if err != nil {
			return nil, err
		}
		children := map[string][]*store.Call{}
		for _, t := range trace {
			if t.ParentID != nil {
				children[*t.ParentID] = append(children[*t.ParentID], t)
			}
		}
		running := c.EndedAt == nil
		stack := []string{c.ID}
		for len(stack) > 0 && !running {
			id := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			for _, ch := range children[id] {
				if ch.EndedAt == nil {
					running = true
				}
				stack = append(stack, ch.ID)
			}
		}
		if running {
			unfinished = append(unfinished, c.ID)
		}
	}
	return map[string]any{"call_usage": usage, "unfinished_call_ids": unfinished}, nil
}

type traceUsageReq struct {
	ProjectID    string             `json:"project_id"`
	Filter       *store.CallsFilter `json:"filter"`
	Query        any                `json:"query"`
	IncludeCosts bool               `json:"include_costs"`
	Limit        int                `json:"limit"`
}

func (s *Server) traceUsage(ctx context.Context, _ *http.Request, req *traceUsageReq) (any, error) {
	pid, err := project(req.ProjectID)
	if err != nil {
		return nil, err
	}
	limit := req.Limit
	if limit <= 0 || limit > 100000 {
		limit = 100000
	}
	calls, err := s.store.ListCalls(ctx, store.CallsQuery{ProjectID: pid, Filter: req.Filter, Query: req.Query, Limit: &limit})
	if err != nil {
		return nil, err
	}
	var pb *store.PriceBook
	if req.IncludeCosts {
		if pb, err = s.store.LoadPriceBook(ctx, pid); err != nil {
			return nil, err
		}
	}
	usage := map[string]any{}
	unfinished := []string{}
	for _, c := range calls {
		usage[c.ID] = usageItems(c.TotalUsage(), pb, c.StartedMicros())
		if c.EndedAt == nil {
			unfinished = append(unfinished, c.ID)
		}
	}
	return map[string]any{"call_usage": usage, "unfinished_call_ids": unfinished}, nil
}

func (s *Server) threadsStreamQuery(w http.ResponseWriter, r *http.Request) {
	var req store.ThreadsQuery
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	ts, err := s.store.QueryThreads(r.Context(), req)
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/jsonl")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	for _, t := range ts {
		if err := enc.Encode(t); err != nil {
			return
		}
	}
}

// ---------------------------------------------------------------- evaluations

// evaluationsStatus is /evaluation_status in Weave's tagged-union shape.
func (s *Server) evaluationsStatus(ctx context.Context, _ *http.Request, req *evalStatusReq) (any, error) {
	pid, err := project(req.ProjectID)
	if err != nil {
		return nil, err
	}
	st, err := s.engine.EvaluationStatus(ctx, pid, req.CallID)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	switch st.Code {
	case "running":
		out = map[string]any{"code": "running", "completed_rows": st.CompletedRows, "total_rows": st.TotalRows}
	case "failed":
		out = map[string]any{"code": "failed", "error": st.Error}
	case "complete":
		output := map[string]any{}
		if c, err := s.store.GetCall(ctx, pid, req.CallID, false, false, false); err == nil {
			if m, ok := c.Output.(map[string]any); ok {
				output = m
			}
		}
		out = map[string]any{"code": "complete", "output": output}
	default:
		out = map[string]any{"code": "not_found"}
	}
	return map[string]any{"status": out}, nil
}

type rescoreReq struct {
	ProjectID             string   `json:"project_id"`
	ScorerRefs            []string `json:"scorer_refs"`
	SourceEvaluationRunID string   `json:"source_evaluation_run_id"`
	WBUserID              *string  `json:"wb_user_id"`
}

func (s *Server) evaluationsRescore(ctx context.Context, r *http.Request, req *rescoreReq) (any, error) {
	id, err := s.engine.Rescore(ctx, req.ProjectID, req.SourceEvaluationRunID, req.ScorerRefs, userFrom(r, req.WBUserID))
	if err != nil {
		return nil, err
	}
	return map[string]any{"call_id": id, "evaluation_run_id": id}, nil
}

// ---------------------------------------------------------------- dataset lineage

type datasetSourcesLinkReq struct {
	ProjectID            string                         `json:"project_id"`
	DatasetObjectID      string                         `json:"dataset_object_id"`
	DatasetDigest        string                         `json:"dataset_digest"`
	Links                []store.DatasetSourceLinkInput `json:"links"`
	IncludeCreatedStatus bool                           `json:"include_created_status"`
	WBUserID             *string                        `json:"wb_user_id"`
}

func (s *Server) datasetSourcesLink(ctx context.Context, r *http.Request, req *datasetSourcesLinkReq) (any, error) {
	// Resolve aliases and "latest" to a concrete version.
	o, err := s.store.ReadObject(ctx, req.ProjectID, req.DatasetObjectID, req.DatasetDigest)
	if errors.Is(err, store.ErrNotFound) {
		return nil, store.Invalidf("dataset %s:%s not found", req.DatasetObjectID, req.DatasetDigest)
	}
	if err != nil {
		return nil, err
	}
	entries, err := s.store.LinkDatasetSources(ctx, req.ProjectID, o.ObjectID, o.Digest, req.Links, userFrom(r, req.WBUserID), req.IncludeCreatedStatus)
	if err != nil {
		return nil, err
	}
	return map[string]any{"entries": entries}, nil
}

func (s *Server) datasetSourcesQuery(ctx context.Context, _ *http.Request, req *store.DatasetSourcesQuery) (any, error) {
	links, err := s.store.QueryDatasetSources(ctx, *req)
	if err != nil {
		return nil, err
	}
	return map[string]any{"links": links}, nil
}

type sourceDatasetsReq struct {
	ProjectID      string                `json:"project_id"`
	Sources        []store.DatasetSource `json:"sources"`
	IncludeDeleted bool                  `json:"include_deleted"`
}

func (s *Server) sourceDatasetsQuery(ctx context.Context, _ *http.Request, req *sourceDatasetsReq) (any, error) {
	ms, err := s.store.SourceDatasets(ctx, req.ProjectID, req.Sources, req.IncludeDeleted)
	if err != nil {
		return nil, err
	}
	return map[string]any{"memberships": ms}, nil
}

type linkDeleteReq struct {
	ProjectID string   `json:"project_id"`
	LinkIDs   []string `json:"link_ids"`
	IDs       []string `json:"ids"` // accepted as an alias of link_ids
}

func (s *Server) datasetSourcesLinkDelete(ctx context.Context, _ *http.Request, req *linkDeleteReq) (any, error) {
	req.LinkIDs = append(req.LinkIDs, req.IDs...)
	if len(req.LinkIDs) > 10000 {
		return nil, store.Invalidf("too many link_ids (max 10000)")
	}
	res, err := s.store.DeleteDatasetSourceLinks(ctx, req.ProjectID, req.LinkIDs)
	if err != nil {
		return nil, err
	}
	entries := make([]map[string]any, 0, len(req.LinkIDs))
	for _, id := range req.LinkIDs {
		entries = append(entries, map[string]any{"link_id": id, "deleted": res[id]})
	}
	return map[string]any{"entries": entries}, nil
}
