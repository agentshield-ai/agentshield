package store

import (
	"context"
	"database/sql"
	"sort"
	"strconv"
	"time"

	"github.com/agentshield-ai/agentshield/internal/weft/refs"
)

// Metric buckets of /calls/stats: time-bucketed usage metrics grouped by
// model (usage_buckets) and span metrics (span_buckets). Column names are
// "<agg>_<metric>" (sum_input_tokens, avg_latency_ms, count_true_error_count)
// and "p<pct>_<metric>" (p95_latency_ms), plus "timestamp", "count" and, for
// usage buckets, "model". sum/count/avg default to 0 in empty buckets;
// min/max/percentiles are null.

// BucketMetricSpec is a UsageMetricSpec or CallMetricSpec.
type BucketMetricSpec struct {
	Metric       string    `json:"metric"`
	Aggregations []string  `json:"aggregations"`
	Percentiles  []float64 `json:"percentiles"`
}

// MetricStatsReq is a metric-bucket /api/v1/spans/stats request.
type MetricStatsReq struct {
	ProjectID    string
	Start        string
	End          string
	Granularity  *int64
	UsageMetrics []BucketMetricSpec
	SpanMetrics  []BucketMetricSpec
	Filter       *CallsFilter
	Timezone     string
}

// MetricStatsRes is a metric-bucket /api/v1/spans/stats response.
type MetricStatsRes struct {
	Start        string           `json:"start"`
	End          string           `json:"end"`
	Granularity  int64            `json:"granularity"`
	Timezone     string           `json:"timezone"`
	UsageBuckets []map[string]any `json:"usage_buckets"`
	SpanBuckets  []map[string]any `json:"span_buckets"`
}

var (
	usageMetricNames = map[string]bool{"input_tokens": true, "output_tokens": true, "total_tokens": true,
		"cache_read_input_tokens": true, "cache_creation_input_tokens": true, "input_cost": true, "output_cost": true, "total_cost": true}
	spanMetricNames = map[string]bool{"latency_ms": true, "span_count": true, "error_count": true}
	metricAggs      = map[string]bool{"sum": true, "avg": true, "min": true, "max": true, "count": true, "count_true": true, "count_false": true}
)

const metricMaxBuckets = 10000

func metricAutoGranularity(d time.Duration) int64 {
	switch {
	case d <= 2*time.Hour:
		return 300
	case d <= 12*time.Hour:
		return 3600
	case d <= 3*24*time.Hour:
		return 6 * 3600
	case d <= 14*24*time.Hour:
		return 12 * 3600
	}
	return 86400
}

func normalizeMetricSpecs(specs []BucketMetricSpec, valid map[string]bool, kind string) ([]BucketMetricSpec, error) {
	out := make([]BucketMetricSpec, 0, len(specs))
	for _, sp := range specs {
		if !valid[sp.Metric] {
			return nil, Invalidf("unknown %s metric %q", kind, sp.Metric)
		}
		for _, a := range sp.Aggregations {
			if !metricAggs[a] {
				return nil, Invalidf("unknown aggregation %q", a)
			}
		}
		for _, p := range sp.Percentiles {
			if p < 0 || p > 100 {
				return nil, Invalidf("percentile must be between 0 and 100, got %v", p)
			}
		}
		if len(sp.Aggregations) == 0 && len(sp.Percentiles) == 0 {
			sp.Aggregations = []string{"sum"}
		}
		out = append(out, sp)
	}
	return out, nil
}

// bucketAcc collects the non-null values of one metric in one bucket.
type bucketAcc struct{ vals []float64 }

func (a *bucketAcc) add(v *float64) {
	if v != nil {
		a.vals = append(a.vals, *v)
	}
}

func (a *bucketAcc) write(out map[string]any, sp BucketMetricSpec) {
	n := len(a.vals)
	var sum, mn, mx float64
	var trues, falses int
	for i, v := range a.vals {
		sum += v
		if i == 0 || v < mn {
			mn = v
		}
		if i == 0 || v > mx {
			mx = v
		}
		switch v {
		case 1:
			trues++
		case 0:
			falses++
		}
	}
	num := func(v float64) any {
		if v == float64(int64(v)) && v < 1e15 && v > -1e15 {
			return int64(v)
		}
		return v
	}
	for _, agg := range sp.Aggregations {
		key := agg + "_" + sp.Metric
		switch agg {
		case "sum":
			out[key] = num(sum)
		case "count":
			out[key] = n
		case "count_true":
			out[key] = trues
		case "count_false":
			out[key] = falses
		case "avg":
			if n == 0 {
				out[key] = 0
			} else {
				out[key] = sum / float64(n)
			}
		case "min", "max":
			if n == 0 {
				out[key] = nil
			} else if agg == "min" {
				out[key] = num(mn)
			} else {
				out[key] = num(mx)
			}
		}
	}
	var sorted []float64
	if len(sp.Percentiles) > 0 {
		sorted = append([]float64(nil), a.vals...)
		sort.Float64s(sorted)
	}
	for _, p := range sp.Percentiles {
		key := "p" + strconv.FormatFloat(p, 'f', -1, 64) + "_" + sp.Metric
		if n == 0 {
			out[key] = nil
			continue
		}
		rank := p / 100 * float64(n-1)
		lo := int(rank)
		hi := min(lo+1, n-1)
		out[key] = sorted[lo] + (sorted[hi]-sorted[lo])*(rank-float64(lo))
	}
}

// ComputeMetricStats computes the metric buckets of /api/v1/spans/stats.
func (s *Store) ComputeMetricStats(ctx context.Context, q MetricStatsReq) (*MetricStatsRes, error) {
	pid, err := refs.NormalizeProject(q.ProjectID)
	if err != nil {
		return nil, &InvalidError{Msg: err.Error()}
	}
	usageSpecs, err := normalizeMetricSpecs(q.UsageMetrics, usageMetricNames, "usage")
	if err != nil {
		return nil, err
	}
	callSpecs, err := normalizeMetricSpecs(q.SpanMetrics, spanMetricNames, "span")
	if err != nil {
		return nil, err
	}
	if q.Start == "" {
		return nil, Invalidf("start is required")
	}
	startUS, err := ParseTime(q.Start)
	if err != nil {
		return nil, err
	}
	endUS := time.Now().UnixMicro()
	if q.End != "" {
		if endUS, err = ParseTime(q.End); err != nil {
			return nil, err
		}
	}
	if endUS <= startUS {
		return nil, Invalidf("end must be after start")
	}
	tz := q.Timezone
	if tz == "" {
		tz = "UTC"
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return nil, Invalidf("unknown timezone %q", tz)
	}
	span := time.Duration(endUS-startUS) * time.Microsecond
	gran := metricAutoGranularity(span)
	if q.Granularity != nil {
		if *q.Granularity <= 0 {
			return nil, Invalidf("granularity must be positive")
		}
		gran = *q.Granularity
	}
	if secs := span.Seconds(); secs/float64(gran) > metricMaxBuckets {
		gran = max(gran, int64(secs/metricMaxBuckets)+1)
	}
	res := &MetricStatsRes{Start: FormatMicros(startUS), End: FormatMicros(endUS), Granularity: gran, Timezone: tz,
		UsageBuckets: []map[string]any{}, SpanBuckets: []map[string]any{}}
	if len(usageSpecs) == 0 && len(callSpecs) == 0 {
		return res, nil
	}

	// Buckets align to the interval in the requested timezone.
	_, offset := time.UnixMicro(startUS).In(loc).Zone()
	g := gran * 1_000_000
	align := func(us int64) int64 {
		local := us + int64(offset)*1_000_000
		return local - ((local%g)+g)%g - int64(offset)*1_000_000
	}
	first := align(startUS)
	var buckets []int64
	for b := first; b < endUS; b += g {
		buckets = append(buckets, b)
	}
	bucketIdx := func(us int64) int { return int((us - first) / g) }

	where, args, err := whereCalls(pid, q.Filter, nil, "", false)
	if err != nil {
		return nil, err
	}
	where += " AND c.started_at >= ? AND c.started_at < ?"
	args = append(args, startUS, endUS)
	rows, err := s.r.QueryContext(ctx, `SELECT c.started_at, c.ended_at, c.exception IS NOT NULL, c.summary, c.rollup, c.cumulative
		FROM calls c WHERE `+where+` ORDER BY c.started_at LIMIT ?`, append(args, metricsRowLimit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	prices, err := s.LoadPriceBook(ctx, pid)
	if err != nil {
		return nil, err
	}
	rootsOnly := q.Filter != nil && q.Filter.TraceRootsOnly

	type usageKey struct {
		bucket int
		model  string
	}
	usageAcc := map[usageKey][]*bucketAcc{}
	usageCount := map[usageKey]int{}
	models := map[string]bool{}
	callAcc := make([][]*bucketAcc, len(buckets))
	callCount := make([]int, len(buckets))
	for i := range callAcc {
		callAcc[i] = make([]*bucketAcc, len(callSpecs))
		for j := range callAcc[i] {
			callAcc[i][j] = &bucketAcc{}
		}
	}
	f := func(v float64) *float64 { return &v }
	for rows.Next() {
		var st int64
		var en sql.NullInt64
		var failed, cum bool
		var summary, rollup string
		if err := rows.Scan(&st, &en, &failed, &summary, &rollup, &cum); err != nil {
			return nil, err
		}
		bi := bucketIdx(st)
		if bi < 0 || bi >= len(buckets) {
			continue
		}
		if len(callSpecs) > 0 {
			callCount[bi]++
			for j, sp := range callSpecs {
				var v *float64
				switch sp.Metric {
				case "latency_ms":
					if en.Valid {
						v = f(float64(en.Int64-st) / 1000)
					}
				case "span_count":
					v = f(1)
				case "error_count":
					v = f(0)
					if failed {
						v = f(1)
					}
				}
				callAcc[bi][j].add(v)
			}
		}
		if len(usageSpecs) == 0 {
			continue
		}
		ru := ParseContribution(rollup)
		usage := TotalContribution(decodeMap(summary), en.Valid, failed, cum, ru)
		if !rootsOnly {
			usage.Sub(ru) // own usage only, so nested calls aren't counted twice
		}
		for model, m := range usage.Usage {
			prompt, completion, total := UsageTokens(m)
			if prompt == 0 && completion == 0 && total == 0 {
				continue
			}
			k := usageKey{bi, model}
			accs := usageAcc[k]
			if accs == nil {
				accs = make([]*bucketAcc, len(usageSpecs))
				for j := range accs {
					accs[j] = &bucketAcc{}
				}
				usageAcc[k] = accs
			}
			models[model] = true
			usageCount[k]++
			cost, _, priced := prices.ModelCost(model, m, st)
			for j, sp := range usageSpecs {
				var v *float64
				switch sp.Metric {
				case "input_tokens":
					v = f(prompt)
				case "output_tokens":
					v = f(completion)
				case "total_tokens":
					v = f(total)
				case "cache_read_input_tokens", "cache_creation_input_tokens":
					v = f(m[sp.Metric])
				case "input_cost": // all prompt tokens, cache reads and writes at their own rates
					if priced {
						v = f(cost.Input())
					}
				case "output_cost":
					if priced {
						v = f(cost.Completion)
					}
				case "total_cost":
					if priced {
						v = f(cost.Total())
					}
				}
				accs[j].add(v)
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	stamp := func(b int64) string { return time.UnixMicro(b).In(loc).Format(time.RFC3339) }
	if len(callSpecs) > 0 {
		for i, b := range buckets {
			row := map[string]any{"timestamp": stamp(b), "count": callCount[i]}
			for j, sp := range callSpecs {
				callAcc[i][j].write(row, sp)
			}
			res.SpanBuckets = append(res.SpanBuckets, row)
		}
	}
	if len(usageSpecs) > 0 {
		names := make([]string, 0, len(models))
		for m := range models {
			names = append(names, m)
		}
		sort.Strings(names)
		for i, b := range buckets {
			for _, model := range names {
				k := usageKey{i, model}
				row := map[string]any{"timestamp": stamp(b), "model": model, "count": usageCount[k]}
				accs := usageAcc[k]
				for j, sp := range usageSpecs {
					acc := &bucketAcc{}
					if accs != nil {
						acc = accs[j]
					}
					acc.write(row, sp)
				}
				res.UsageBuckets = append(res.UsageBuckets, row)
			}
		}
	}
	return res, nil
}

// CountFeedback counts feedback matching a /feedback/query query.
func (s *Store) CountFeedback(ctx context.Context, projectID string, query any) (int64, error) {
	pid, err := refs.NormalizeProject(projectID)
	if err != nil {
		return 0, &InvalidError{Msg: err.Error()}
	}
	where := "f.project_id = ?"
	args := []any{pid}
	if query != nil {
		qs, qa, err := compileQuery(query, feedbackFieldResolver)
		if err != nil {
			return 0, err
		}
		if qs != "" {
			where += " AND (" + qs + ")"
			args = append(args, qa...)
		}
	}
	var n int64
	err = s.r.QueryRowContext(ctx, "SELECT COUNT(*) FROM feedback f WHERE "+where, args...).Scan(&n)
	return n, err
}
