package store

import (
	"context"
	"database/sql"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/agentshield-ai/agentshield/internal/weft/refs"
)

// Custom chart metrics (/calls/metrics): any aggregate of a call field over
// time, optionally split by a group key. Values are aggregated in Go because
// SQLite has no percentile functions; at most metricsRowLimit calls are read.

const (
	metricsRowLimit   = 200000
	maxMetricsPerReq  = 20
	defaultMaxGroups  = 10
	maxMetricsGroups  = 50
	metricsOtherGroup = "__other__"
)

// MetricAggs lists the supported aggregations.
var MetricAggs = []string{"count", "sum", "avg", "min", "max", "p50", "p90", "p95", "p99", "error_rate"}

// MetricSpec is one requested series.
type MetricSpec struct {
	Name  string `json:"name"`
	Field string `json:"field"`
	Agg   string `json:"agg"`
}

// MetricsQuery is the body of /calls/metrics.
type MetricsQuery struct {
	ProjectID     string       `json:"project_id"`
	Filter        *CallsFilter `json:"filter"`
	Query         any          `json:"query"`
	Search        string       `json:"search"`
	Metrics       []MetricSpec `json:"metrics"`
	GroupBy       string       `json:"group_by"`
	BucketSeconds int64        `json:"bucket_seconds"`
	Start         string       `json:"start"`
	End           string       `json:"end"`
	MaxGroups     int          `json:"max_groups"`
}

// MetricsGroup is the series of one group value.
type MetricsGroup struct {
	// Group is the group value; nil when the call has none (or no group_by).
	Group *string `json:"group"`
	// Other marks the group that merges everything past max_groups.
	Other  bool                    `json:"other,omitempty"`
	Count  int64                   `json:"count"`
	Series map[string][]*float64   `json:"series"`
	Totals map[string]*float64     `json:"totals"`
	acc    map[string][]*metricAcc // per metric, per bucket
	total  map[string]*metricAcc
}

// MetricsResult is the response of /calls/metrics.
type MetricsResult struct {
	BucketSeconds int64               `json:"bucket_seconds"`
	Start         *string             `json:"start"`
	End           *string             `json:"end"`
	Buckets       []string            `json:"buckets"`
	Metrics       []MetricSpec        `json:"metrics"`
	GroupBy       string              `json:"group_by"`
	Groups        []*MetricsGroup     `json:"groups"`
	Totals        map[string]*float64 `json:"totals"`
	Count         int64               `json:"count"`
	Truncated     bool                `json:"truncated"`
}

type metricAcc struct {
	n, errs int64
	vals    []float64
	sum     float64
	min     float64
	max     float64
}

func (a *metricAcc) add(v float64) {
	if len(a.vals) == 0 || v < a.min {
		a.min = v
	}
	if len(a.vals) == 0 || v > a.max {
		a.max = v
	}
	a.vals = append(a.vals, v)
	a.sum += v
}

func (a *metricAcc) result(agg string) *float64 {
	f := func(v float64) *float64 { return &v }
	switch agg {
	case "count":
		return f(float64(a.n))
	case "error_rate":
		if a.n == 0 {
			return nil
		}
		return f(float64(a.errs) / float64(a.n))
	case "sum":
		return f(a.sum)
	}
	if len(a.vals) == 0 {
		return nil
	}
	switch agg {
	case "avg":
		return f(a.sum / float64(len(a.vals)))
	case "min":
		return f(a.min)
	case "max":
		return f(a.max)
	}
	sort.Float64s(a.vals)
	p := map[string]float64{"p50": 0.5, "p90": 0.9, "p95": 0.95, "p99": 0.99}[agg]
	return percentile(a.vals, p)
}

// usage-derived fields computed in Go from the call's usage and the price book.
var usageFields = map[string]bool{
	"total_tokens": true, "prompt_tokens": true, "completion_tokens": true, "cost": true, "cost_usd": true,
	"summary.total_tokens": true, "summary.weave.total_tokens": true,
}

// metricFieldSQL resolves a metric field to a SQL expression. Usage-derived
// fields return "" (computed in Go).
func metricFieldSQL(field string) (string, []any, error) {
	switch field {
	case "latency_ms", "latency":
		return latencySQL, nil, nil
	}
	if usageFields[field] {
		return "", nil, nil
	}
	expr, args, isTime, err := callField(field)
	if err != nil {
		return "", nil, err
	}
	if isTime {
		return "", nil, Invalidf("field %q is a timestamp, not a number", field)
	}
	return expr, args, nil
}

// groupSQL resolves a group_by value to a SQL expression ("" for model,
// which is derived from usage).
func groupSQL(groupBy string) (string, []any, error) {
	switch groupBy {
	case "":
		return "NULL", nil, nil
	case "op_name", "op":
		return "c.op_base_name", nil, nil
	case "status":
		return statusSQL, nil, nil
	case "thread", "thread_id":
		return "c.thread_id", nil, nil
	case "user", "wb_user_id":
		return "c.wb_user_id", nil, nil
	case "model":
		return "", nil, nil
	}
	expr, args, isTime, err := callField(groupBy)
	if err != nil {
		return "", nil, err
	}
	if isTime {
		return "", nil, Invalidf("cannot group by timestamp %q", groupBy)
	}
	return "CAST(" + expr + " AS TEXT)", args, nil
}

func metricNumber(v any) (float64, bool) {
	switch t := v.(type) {
	case int64:
		return float64(t), true
	case float64:
		if math.IsNaN(t) || math.IsInf(t, 0) {
			return 0, false
		}
		return t, true
	case []byte:
		return metricNumber(string(t))
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return 0, false
		}
		return f, true
	case bool:
		if t {
			return 1, true
		}
		return 0, true
	}
	return 0, false
}

// ComputeCallsMetrics aggregates arbitrary call metrics over time and groups.
func (s *Store) ComputeCallsMetrics(ctx context.Context, q MetricsQuery) (*MetricsResult, error) {
	pid, err := refs.NormalizeProject(q.ProjectID)
	if err != nil {
		return nil, &InvalidError{Msg: err.Error()}
	}
	if len(q.Metrics) == 0 {
		q.Metrics = []MetricSpec{{Name: "count", Agg: "count"}}
	}
	if len(q.Metrics) > maxMetricsPerReq {
		return nil, Invalidf("at most %d metrics per request", maxMetricsPerReq)
	}
	validAgg := map[string]bool{}
	for _, a := range MetricAggs {
		validAgg[a] = true
	}
	seen := map[string]bool{}
	type fieldCol struct {
		usage string // usage-derived field name, or ""
		col   int    // index into the selected value columns, or -1
	}
	cols := make([]fieldCol, len(q.Metrics))
	var selExprs []string
	var selArgs []any
	colOf := map[string]int{}
	for i := range q.Metrics {
		m := &q.Metrics[i]
		m.Agg = strings.ToLower(strings.TrimSpace(m.Agg))
		m.Field = strings.TrimSpace(m.Field)
		if m.Agg == "" {
			m.Agg = "count"
		}
		if !validAgg[m.Agg] {
			return nil, Invalidf("unknown agg %q (want one of %s)", m.Agg, strings.Join(MetricAggs, ", "))
		}
		if m.Field == "" && m.Agg != "count" && m.Agg != "error_rate" {
			return nil, Invalidf("metric %d: agg %q needs a field", i, m.Agg)
		}
		if m.Name == "" {
			if m.Field == "" {
				m.Name = m.Agg
			} else {
				m.Name = m.Agg + "(" + m.Field + ")"
			}
		}
		if seen[m.Name] {
			return nil, Invalidf("duplicate metric name %q", m.Name)
		}
		seen[m.Name] = true
		cols[i] = fieldCol{col: -1}
		if m.Field == "" || m.Agg == "error_rate" {
			continue
		}
		expr, args, err := metricFieldSQL(m.Field)
		if err != nil {
			return nil, err
		}
		if expr == "" {
			cols[i].usage = m.Field
			continue
		}
		if j, ok := colOf[m.Field]; ok {
			cols[i].col = j
			continue
		}
		colOf[m.Field] = len(selExprs)
		cols[i].col = len(selExprs)
		selExprs = append(selExprs, expr)
		selArgs = append(selArgs, args...)
	}
	groupBy := strings.TrimSpace(q.GroupBy)
	gexpr, gargs, err := groupSQL(groupBy)
	if err != nil {
		return nil, err
	}
	byModel := groupBy == "model"
	if byModel {
		gexpr = "NULL"
	}
	maxGroups := q.MaxGroups
	if maxGroups <= 0 {
		maxGroups = defaultMaxGroups
	}
	maxGroups = min(maxGroups, maxMetricsGroups)

	where, wargs, err := whereCalls(pid, q.Filter, q.Query, q.Search, false)
	if err != nil {
		return nil, err
	}
	var startUS, endUS int64
	if q.Start != "" {
		if startUS, err = ParseTime(q.Start); err != nil {
			return nil, err
		}
		where += " AND c.started_at >= ?"
		wargs = append(wargs, startUS)
	}
	if q.End != "" {
		if endUS, err = ParseTime(q.End); err != nil {
			return nil, err
		}
		where += " AND c.started_at < ?"
		wargs = append(wargs, endUS)
	}
	if startUS != 0 && endUS != 0 && endUS <= startUS {
		return nil, Invalidf("end must be after start")
	}
	rootsOnly := q.Filter != nil && q.Filter.TraceRootsOnly

	sel := "SELECT c.started_at, c.ended_at, c.exception IS NOT NULL, c.summary, c.rollup, c.cumulative, " + gexpr
	for _, e := range selExprs {
		sel += ", " + e
	}
	args := append(append(append([]any{}, gargs...), selArgs...), wargs...)
	rows, err := s.r.QueryContext(ctx, sel+" FROM calls c WHERE "+where+" ORDER BY c.started_at LIMIT ?", append(args, metricsRowLimit+1)...)
	if err != nil {
		return nil, err
	}
	type point struct {
		start  int64
		failed bool
		group  *string
		vals   []any
		usage  Contribution
	}
	var pts []point
	truncated := false
	for rows.Next() {
		if len(pts) == metricsRowLimit {
			truncated = true
			break
		}
		var st int64
		var en sql.NullInt64
		var failed, cum bool
		var summary, rollup string
		var group sql.NullString
		vals := make([]any, len(selExprs))
		dest := []any{&st, &en, &failed, &summary, &rollup, &cum, &group}
		for i := range vals {
			dest = append(dest, &vals[i])
		}
		if err := rows.Scan(dest...); err != nil {
			rows.Close()
			return nil, err
		}
		p := point{start: st, failed: failed, vals: vals}
		if group.Valid {
			g := group.String
			p.group = &g
		}
		ru := ParseContribution(rollup)
		p.usage = TotalContribution(decodeMap(summary), en.Valid, failed, cum, ru)
		if !rootsOnly {
			p.usage.Sub(ru) // own usage only, so nested calls aren't counted twice
		}
		p.usage.StatusCounts = nil
		if byModel {
			best, bestTok := "", -1.0
			for model, u := range p.usage.Usage {
				_, _, tot := UsageTokens(u)
				if tot > bestTok || (tot == bestTok && model < best) {
					best, bestTok = model, tot
				}
			}
			if best != "" {
				p.group = &best
			}
		}
		pts = append(pts, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := &MetricsResult{Metrics: q.Metrics, GroupBy: groupBy, Buckets: []string{}, Groups: []*MetricsGroup{}, Totals: map[string]*float64{}, Truncated: truncated}
	if len(pts) == 0 && (startUS == 0 || endUS == 0) {
		out.BucketSeconds = max(q.BucketSeconds, 3600)
		for _, m := range q.Metrics {
			out.Totals[m.Name] = (&metricAcc{}).result(m.Agg)
		}
		return out, nil
	}
	if startUS == 0 {
		startUS = pts[0].start
	}
	if endUS == 0 {
		endUS = pts[len(pts)-1].start + 1
	}
	bucketSec := q.BucketSeconds
	if bucketSec <= 0 {
		span := float64(endUS-startUS) / 1e6
		bucketSec = niceBuckets[len(niceBuckets)-1]
		for _, b := range niceBuckets {
			if span/float64(b) <= 60 {
				bucketSec = b
				break
			}
		}
	}
	bucketUS := bucketSec * 1_000_000
	first := (startUS / bucketUS) * bucketUS
	n := int((endUS-1-first)/bucketUS) + 1
	if n > 5000 {
		return nil, Invalidf("too many buckets; increase bucket_seconds")
	}
	out.BucketSeconds = bucketSec
	sv, ev := FormatMicros(startUS), FormatMicros(endUS)
	out.Start, out.End = &sv, &ev
	for i := 0; i < n; i++ {
		out.Buckets = append(out.Buckets, FormatMicros(first+int64(i)*bucketUS))
	}
	prices, err := s.LoadPriceBook(ctx, pid)
	if err != nil {
		return nil, err
	}

	// Rank groups by call count and keep the top maxGroups.
	key := func(g *string) string {
		if g == nil {
			return "\x00null"
		}
		return *g
	}
	counts := map[string]int64{}
	groupVal := map[string]*string{}
	for _, p := range pts {
		k := key(p.group)
		counts[k]++
		groupVal[k] = p.group
	}
	order := make([]string, 0, len(counts))
	for k := range counts {
		order = append(order, k)
	}
	sort.Slice(order, func(i, j int) bool {
		if counts[order[i]] != counts[order[j]] {
			return counts[order[i]] > counts[order[j]]
		}
		return order[i] < order[j]
	})
	newGroup := func(g *string) *MetricsGroup {
		mg := &MetricsGroup{Group: g, Series: map[string][]*float64{}, Totals: map[string]*float64{}, acc: map[string][]*metricAcc{}, total: map[string]*metricAcc{}}
		for _, m := range q.Metrics {
			accs := make([]*metricAcc, n)
			for i := range accs {
				accs[i] = &metricAcc{}
			}
			mg.acc[m.Name] = accs
			mg.total[m.Name] = &metricAcc{}
		}
		return mg
	}
	groups := map[string]*MetricsGroup{}
	for i, k := range order {
		if i >= maxGroups {
			break
		}
		g := newGroup(groupVal[k])
		groups[k] = g
		out.Groups = append(out.Groups, g)
	}
	var other *MetricsGroup
	if len(order) > maxGroups {
		ov := metricsOtherGroup
		other = newGroup(&ov)
		other.Other = true
		out.Groups = append(out.Groups, other)
	}
	overall := newGroup(nil)

	for _, p := range pts {
		bi := int((p.start - first) / bucketUS)
		if bi < 0 || bi >= n {
			continue
		}
		g := groups[key(p.group)]
		if g == nil {
			g = other
		}
		g.Count++
		out.Count++
		for mi, m := range q.Metrics {
			v, has := 0.0, false
			switch {
			case m.Field == "" || m.Agg == "error_rate":
			case cols[mi].usage != "":
				v, has = usageValue(cols[mi].usage, p.usage, prices, p.start)
			case cols[mi].col >= 0:
				v, has = metricNumber(p.vals[cols[mi].col])
			}
			for _, acc := range []*metricAcc{g.acc[m.Name][bi], g.total[m.Name], overall.total[m.Name]} {
				if m.Field == "" || m.Agg == "error_rate" {
					acc.n++
					if p.failed {
						acc.errs++
					}
					continue
				}
				if has {
					acc.n++
					acc.add(v)
				}
			}
		}
	}
	for _, g := range out.Groups {
		for _, m := range q.Metrics {
			series := make([]*float64, n)
			for i, acc := range g.acc[m.Name] {
				series[i] = acc.result(m.Agg)
			}
			g.Series[m.Name] = series
			g.Totals[m.Name] = g.total[m.Name].result(m.Agg)
		}
	}
	for _, m := range q.Metrics {
		out.Totals[m.Name] = overall.total[m.Name].result(m.Agg)
	}
	return out, nil
}

func usageValue(field string, u Contribution, prices *PriceBook, atUS int64) (float64, bool) {
	if field == "cost" || field == "cost_usd" {
		if len(u.Usage) == 0 {
			return 0, false
		}
		return prices.TotalCost(u, atUS), true
	}
	if len(u.Usage) == 0 {
		return 0, false
	}
	var pr, co, tot float64
	for _, m := range u.Usage {
		a, b, c := UsageTokens(m)
		pr, co, tot = pr+a, co+b, tot+c
	}
	switch field {
	case "prompt_tokens":
		return pr, true
	case "completion_tokens":
		return co, true
	}
	return tot, true
}
