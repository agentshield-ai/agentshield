package store

import (
	"context"
	"database/sql"
	"math"
	"sort"
	"strings"

	"github.com/agentshield-ai/agentshield/internal/weft/refs"
)

// Project summarises a project.
type Project struct {
	ProjectID    string  `json:"project_id"`
	Entity       string  `json:"entity"`
	Project      string  `json:"project"`
	CallCount    int64   `json:"call_count"`
	TraceCount   int64   `json:"trace_count"`
	ObjectCount  int64   `json:"object_count"`
	LastActivity *string `json:"last_activity"`
}

// EnsureProject creates a project if it does not exist.
func (s *Store) EnsureProject(ctx context.Context, projectID string) error {
	pid, err := refs.NormalizeProject(projectID)
	if err != nil {
		return &InvalidError{Msg: err.Error()}
	}
	return s.withTx(ctx, func(tx *sql.Tx) error { return s.touchProject(ctx, tx, pid) })
}

// ListProjects lists all projects with counts.
func (s *Store) ListProjects(ctx context.Context) ([]Project, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT p.project_id, p.last_activity,
		(SELECT COUNT(*) FROM calls c WHERE c.project_id = p.project_id AND c.deleted_at IS NULL AND c.has_start = 1),
		(SELECT COUNT(*) FROM calls c WHERE c.project_id = p.project_id AND c.deleted_at IS NULL AND c.has_start = 1 AND c.parent_id IS NULL),
		(SELECT COUNT(DISTINCT object_id) FROM objects o WHERE o.project_id = p.project_id AND o.deleted_at IS NULL)
		FROM projects p ORDER BY p.last_activity DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Project{}
	for rows.Next() {
		var p Project
		var last int64
		if err := rows.Scan(&p.ProjectID, &last, &p.CallCount, &p.TraceCount, &p.ObjectCount); err != nil {
			return nil, err
		}
		p.Entity, p.Project = refs.SplitProject(p.ProjectID)
		l := FormatMicros(last)
		p.LastActivity = &l
		out = append(out, p)
	}
	return out, rows.Err()
}

// ProjectStats are storage and count statistics for a project.
type ProjectStats struct {
	CallCount        int64 `json:"call_count"`
	TraceCount       int64 `json:"trace_count"`
	ObjectCount      int64 `json:"object_count"`
	TableCount       int64 `json:"table_count"`
	FileCount        int64 `json:"file_count"`
	FeedbackCount    int64 `json:"feedback_count"`
	StorageSizeBytes int64 `json:"storage_size_bytes"`
}

// GetProjectStats computes project statistics.
func (s *Store) GetProjectStats(ctx context.Context, projectID string) (*ProjectStats, error) {
	pid, err := refs.NormalizeProject(projectID)
	if err != nil {
		return nil, &InvalidError{Msg: err.Error()}
	}
	var st ProjectStats
	var callBytes, objBytes, fileBytes, rowBytes int64
	err = s.r.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM calls WHERE project_id = ?1 AND deleted_at IS NULL AND has_start = 1),
		(SELECT COUNT(*) FROM calls WHERE project_id = ?1 AND deleted_at IS NULL AND has_start = 1 AND parent_id IS NULL),
		(SELECT COUNT(DISTINCT object_id) FROM objects WHERE project_id = ?1 AND deleted_at IS NULL),
		(SELECT COUNT(*) FROM tables WHERE project_id = ?1),
		(SELECT COUNT(*) FROM files WHERE project_id = ?1),
		(SELECT COUNT(*) FROM feedback WHERE project_id = ?1),
		(SELECT COALESCE(SUM(storage_size), 0) FROM calls WHERE project_id = ?1),
		(SELECT COALESCE(SUM(size_bytes), 0) FROM objects WHERE project_id = ?1),
		(SELECT COALESCE(SUM(size_bytes), 0) FROM files WHERE project_id = ?1),
		(SELECT COALESCE(SUM(length(val)), 0) FROM table_rows WHERE project_id = ?1)`, pid).Scan(
		&st.CallCount, &st.TraceCount, &st.ObjectCount, &st.TableCount, &st.FileCount, &st.FeedbackCount,
		&callBytes, &objBytes, &fileBytes, &rowBytes)
	if err != nil {
		return nil, err
	}
	st.StorageSizeBytes = callBytes + objBytes + fileBytes + rowBytes
	return &st, nil
}

// OpCount summarises calls per op.
type OpCount struct {
	OpName     string `json:"op_name"`
	OpBaseName string `json:"op_base_name"`
	Count      int64  `json:"count"`
	ErrorCount int64  `json:"error_count"`
	LastCalled string `json:"last_called"`
}

// OpCounts groups calls by op.
func (s *Store) OpCounts(ctx context.Context, projectID string, rootsOnly bool) ([]OpCount, error) {
	pid, err := refs.NormalizeProject(projectID)
	if err != nil {
		return nil, &InvalidError{Msg: err.Error()}
	}
	q := `SELECT op_name, op_base_name, COUNT(*), SUM(exception IS NOT NULL), MAX(started_at) FROM calls
		WHERE project_id = ? AND deleted_at IS NULL AND has_start = 1`
	if rootsOnly {
		q += " AND parent_id IS NULL"
	}
	rows, err := s.r.QueryContext(ctx, q+" GROUP BY op_name ORDER BY COUNT(*) DESC LIMIT 1000", pid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []OpCount{}
	for rows.Next() {
		var o OpCount
		var last int64
		if err := rows.Scan(&o.OpName, &o.OpBaseName, &o.Count, &o.ErrorCount, &last); err != nil {
			return nil, err
		}
		o.LastCalled = FormatMicros(last)
		out = append(out, o)
	}
	return out, rows.Err()
}

// StatsBucket is one time bucket of call statistics.
type StatsBucket struct {
	Start            string   `json:"start"`
	Count            int64    `json:"count"`
	ErrorCount       int64    `json:"error_count"`
	LatencyAvg       *float64 `json:"latency_ms_avg"`
	LatencyP50       *float64 `json:"latency_ms_p50"`
	LatencyP95       *float64 `json:"latency_ms_p95"`
	LatencyP99       *float64 `json:"latency_ms_p99"`
	PromptTokens     float64  `json:"prompt_tokens"`
	CompletionTokens float64  `json:"completion_tokens"`
	TotalTokens      float64  `json:"total_tokens"`
	CostUSD          float64  `json:"cost_usd"`

	latencies []float64
}

// StatsTotals summarises all buckets.
type StatsTotals struct {
	Count      int64    `json:"count"`
	ErrorCount int64    `json:"error_count"`
	TotalToken float64  `json:"total_tokens"`
	CostUSD    float64  `json:"cost_usd"`
	LatencyP50 *float64 `json:"latency_ms_p50"`
	LatencyP95 *float64 `json:"latency_ms_p95"`
}

// CallsStats is the response of /calls/stats.
type CallsStats struct {
	BucketSeconds int64          `json:"bucket_seconds"`
	Buckets       []*StatsBucket `json:"buckets"`
	Totals        StatsTotals    `json:"totals"`
}

func percentile(sorted []float64, p float64) *float64 {
	if len(sorted) == 0 {
		return nil
	}
	idx := int(math.Ceil(p*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	v := sorted[idx]
	return &v
}

// niceBuckets are the allowed automatic bucket sizes in seconds.
var niceBuckets = []int64{1, 5, 10, 30, 60, 300, 600, 1800, 3600, 3 * 3600, 6 * 3600, 12 * 3600, 86400, 7 * 86400, 30 * 86400}

// ComputeCallsStats buckets matching calls over time.
func (s *Store) ComputeCallsStats(ctx context.Context, projectID string, f *CallsFilter, query any, search, startStr, endStr string, bucketSec int64) (*CallsStats, error) {
	pid, err := refs.NormalizeProject(projectID)
	if err != nil {
		return nil, &InvalidError{Msg: err.Error()}
	}
	where, args, err := whereCalls(pid, f, query, search, false)
	if err != nil {
		return nil, err
	}
	var startUS, endUS int64
	if startStr != "" {
		if startUS, err = ParseTime(startStr); err != nil {
			return nil, err
		}
		where += " AND c.started_at >= ?"
		args = append(args, startUS)
	}
	if endStr != "" {
		if endUS, err = ParseTime(endStr); err != nil {
			return nil, err
		}
		where += " AND c.started_at < ?"
		args = append(args, endUS)
	}
	// Roots carry trace totals; other calls only their own usage.
	rootsOnly := f != nil && f.TraceRootsOnly
	rows, err := s.r.QueryContext(ctx, `SELECT c.started_at, c.ended_at, c.exception IS NOT NULL, c.summary, c.rollup, c.cumulative FROM calls c WHERE `+
		where+` ORDER BY c.started_at LIMIT 200000`, args...)
	if err != nil {
		return nil, err
	}
	type pt struct {
		start   int64
		latency *float64
		failed  bool
		usage   Contribution
	}
	var pts []pt
	for rows.Next() {
		var st int64
		var en sql.NullInt64
		var failed bool
		var summary, rollup string
		var cum bool
		if err := rows.Scan(&st, &en, &failed, &summary, &rollup, &cum); err != nil {
			rows.Close()
			return nil, err
		}
		p := pt{start: st, failed: failed}
		if en.Valid {
			l := float64(en.Int64-st) / 1000
			p.latency = &l
		}
		ru := ParseContribution(rollup)
		p.usage = TotalContribution(decodeMap(summary), en.Valid, failed, cum, ru)
		if !rootsOnly {
			p.usage.Sub(ru) // own usage only, so nested calls aren't counted twice
		}
		p.usage.StatusCounts = nil
		pts = append(pts, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := &CallsStats{Buckets: []*StatsBucket{}}
	if len(pts) == 0 {
		out.BucketSeconds = max(bucketSec, 3600)
		return out, nil
	}
	if startUS == 0 {
		startUS = pts[0].start
	}
	if endUS == 0 {
		endUS = pts[len(pts)-1].start + 1
	}
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
	out.BucketSeconds = bucketSec
	bucketUS := bucketSec * 1_000_000
	first := (startUS / bucketUS) * bucketUS
	n := int((endUS-first)/bucketUS) + 1
	if n > 5000 {
		return nil, Invalidf("too many buckets; increase bucket_seconds")
	}
	prices, err := s.LoadPriceBook(ctx, pid)
	if err != nil {
		return nil, err
	}
	buckets := make([]*StatsBucket, n)
	for i := range buckets {
		buckets[i] = &StatsBucket{Start: FormatMicros(first + int64(i)*bucketUS)}
	}
	var allLat []float64
	for _, p := range pts {
		i := int((p.start - first) / bucketUS)
		if i < 0 || i >= n {
			continue
		}
		b := buckets[i]
		b.Count++
		out.Totals.Count++
		if p.failed {
			b.ErrorCount++
			out.Totals.ErrorCount++
		}
		if p.latency != nil {
			b.latencies = append(b.latencies, *p.latency)
			allLat = append(allLat, *p.latency)
		}
		for _, m := range p.usage.Usage {
			pr, co, tot := UsageTokens(m)
			b.PromptTokens += pr
			b.CompletionTokens += co
			b.TotalTokens += tot
			out.Totals.TotalToken += tot
		}
		c := prices.TotalCost(p.usage, p.start)
		b.CostUSD += c
		out.Totals.CostUSD += c
	}
	for _, b := range buckets {
		if len(b.latencies) > 0 {
			sort.Float64s(b.latencies)
			var sum float64
			for _, l := range b.latencies {
				sum += l
			}
			avg := sum / float64(len(b.latencies))
			b.LatencyAvg = &avg
			b.LatencyP50 = percentile(b.latencies, 0.5)
			b.LatencyP95 = percentile(b.latencies, 0.95)
			b.LatencyP99 = percentile(b.latencies, 0.99)
		}
	}
	sort.Float64s(allLat)
	out.Totals.LatencyP50 = percentile(allLat, 0.5)
	out.Totals.LatencyP95 = percentile(allLat, 0.95)
	out.Buckets = buckets
	return out, nil
}

// Thread summarises one conversation thread.
type Thread struct {
	ThreadID          string   `json:"thread_id"`
	TurnCount         int64    `json:"turn_count"`
	CallCount         int64    `json:"call_count"`
	StartTime         string   `json:"start_time"`
	LastUpdated       string   `json:"last_updated"`
	FirstTurnID       *string  `json:"first_turn_id"`
	LastTurnID        *string  `json:"last_turn_id"`
	P50TurnDurationMS *float64 `json:"p50_turn_duration_ms"`
	P99TurnDurationMS *float64 `json:"p99_turn_duration_ms"`
	TotalTokens       float64  `json:"total_tokens"`
	CostUSD           float64  `json:"cost_usd"`

	startUS, lastUS int64
	durations       []float64
}

// ThreadsQuery is the body of /threads/query.
type ThreadsQuery struct {
	ProjectID string   `json:"project_id"`
	Limit     int      `json:"limit"`
	Offset    int      `json:"offset"`
	SortBy    []SortBy `json:"sort_by"`
	Filter    *struct {
		AfterDatetime  string   `json:"after_datetime"`
		BeforeDatetime string   `json:"before_datetime"`
		ThreadIDs      []string `json:"thread_ids"`
	} `json:"filter"`
}

// QueryThreads groups calls with a thread_id into threads.
func (s *Store) QueryThreads(ctx context.Context, q ThreadsQuery) ([]*Thread, error) {
	pid, err := refs.NormalizeProject(q.ProjectID)
	if err != nil {
		return nil, &InvalidError{Msg: err.Error()}
	}
	conds := []string{"c.project_id = ?", "c.thread_id IS NOT NULL", "c.deleted_at IS NULL", "c.has_start = 1"}
	args := []any{pid}
	if q.Filter != nil {
		if q.Filter.AfterDatetime != "" {
			v, err := ParseTime(q.Filter.AfterDatetime)
			if err != nil {
				return nil, err
			}
			conds = append(conds, "c.started_at >= ?")
			args = append(args, v)
		}
		if q.Filter.BeforeDatetime != "" {
			v, err := ParseTime(q.Filter.BeforeDatetime)
			if err != nil {
				return nil, err
			}
			conds = append(conds, "c.started_at < ?")
			args = append(args, v)
		}
		if len(q.Filter.ThreadIDs) > 0 {
			c, a := inList("c.thread_id", q.Filter.ThreadIDs)
			conds = append(conds, c)
			args = append(args, a...)
		}
	}
	rows, err := s.r.QueryContext(ctx, `SELECT c.thread_id, c.id, c.started_at, c.ended_at,
		COALESCE(c.turn_id = c.id OR (c.turn_id IS NULL AND (c.parent_id IS NULL OR NOT EXISTS (
			SELECT 1 FROM calls p WHERE p.project_id = c.project_id AND p.id = c.parent_id AND p.thread_id = c.thread_id))), 0) AS is_turn,
		c.summary, c.rollup, c.cumulative, c.exception IS NOT NULL FROM calls c WHERE `+strings.Join(conds, " AND ")+` ORDER BY c.started_at LIMIT 500000`, args...)
	if err != nil {
		return nil, err
	}
	prices, err := s.LoadPriceBook(ctx, pid)
	if err != nil {
		rows.Close()
		return nil, err
	}
	byID := map[string]*Thread{}
	var order []*Thread
	for rows.Next() {
		var tid, id, summary, rollup string
		var st int64
		var en sql.NullInt64
		var isTurn, cum, failed bool
		if err := rows.Scan(&tid, &id, &st, &en, &isTurn, &summary, &rollup, &cum, &failed); err != nil {
			rows.Close()
			return nil, err
		}
		t := byID[tid]
		if t == nil {
			t = &Thread{ThreadID: tid, startUS: st, lastUS: st}
			byID[tid] = t
			order = append(order, t)
		}
		t.CallCount++
		last := st
		if en.Valid {
			last = en.Int64
		}
		t.lastUS = max(t.lastUS, last)
		if isTurn {
			t.TurnCount++
			idCopy := id
			if t.FirstTurnID == nil {
				t.FirstTurnID = &idCopy
			}
			t.LastTurnID = &idCopy
			if en.Valid {
				t.durations = append(t.durations, float64(en.Int64-st)/1000)
			}
			u := TotalContribution(decodeMap(summary), en.Valid, failed, cum, ParseContribution(rollup))
			t.TotalTokens += u.Tokens()
			t.CostUSD += prices.TotalCost(u, st)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, t := range order {
		t.StartTime = FormatMicros(t.startUS)
		t.LastUpdated = FormatMicros(t.lastUS)
		sort.Float64s(t.durations)
		t.P50TurnDurationMS = percentile(t.durations, 0.5)
		t.P99TurnDurationMS = percentile(t.durations, 0.99)
	}
	field, dir := "last_updated", "desc"
	if len(q.SortBy) > 0 {
		field, dir = q.SortBy[0].Field, strings.ToLower(q.SortBy[0].Direction)
	}
	less := func(a, b *Thread) bool {
		switch field {
		case "start_time":
			return a.startUS < b.startUS
		case "turn_count":
			return a.TurnCount < b.TurnCount
		case "call_count":
			return a.CallCount < b.CallCount
		case "thread_id":
			return a.ThreadID < b.ThreadID
		case "total_tokens":
			return a.TotalTokens < b.TotalTokens
		default:
			return a.lastUS < b.lastUS
		}
	}
	sort.SliceStable(order, func(i, j int) bool {
		if dir == "asc" {
			return less(order[i], order[j])
		}
		return less(order[j], order[i])
	})
	if q.Offset > len(order) {
		return []*Thread{}, nil
	}
	order = order[q.Offset:]
	if q.Limit > 0 && q.Limit < len(order) {
		order = order[:q.Limit]
	}
	return order, nil
}
