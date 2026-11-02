package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/agentshield-ai/agentshield/internal/weft/refs"
)

// Call is the API representation of a call.
type Call struct {
	ID               string         `json:"id"`
	ProjectID        string         `json:"project_id"`
	OpName           string         `json:"op_name"`
	DisplayName      *string        `json:"display_name"`
	TraceID          string         `json:"trace_id"`
	ParentID         *string        `json:"parent_id"`
	ThreadID         *string        `json:"thread_id"`
	TurnID           *string        `json:"turn_id"`
	StartedAt        string         `json:"started_at"`
	EndedAt          *string        `json:"ended_at"`
	Exception        *string        `json:"exception"`
	Attributes       map[string]any `json:"attributes"`
	Inputs           map[string]any `json:"inputs"`
	Output           any            `json:"output"`
	Summary          map[string]any `json:"summary"`
	WBUserID         *string        `json:"wb_user_id"`
	WBRunID          *string        `json:"wb_run_id"`
	DeletedAt        *string        `json:"deleted_at"`
	Feedback         []Feedback     `json:"feedback,omitempty"`
	StorageSizeBytes *int64         `json:"storage_size_bytes,omitempty"`

	// internal
	opBaseName string
	startedUS  int64
	endedUS    *int64
	rollup     Contribution
	cumulative bool
	total      *Contribution // set by finishSummary
}

// OpBaseName is the op name without ref decoration.
func (c *Call) OpBaseName() string { return c.opBaseName }

// StartedMicros returns the start time in unix microseconds.
func (c *Call) StartedMicros() int64 { return c.startedUS }

// EndedMicros returns the end time in unix microseconds, if ended.
func (c *Call) EndedMicros() *int64 { return c.endedUS }

// Status returns the computed status.
func (c *Call) Status() string {
	switch {
	case c.Exception != nil:
		return "error"
	case c.EndedAt == nil:
		return "running"
	case c.rollup.StatusCounts["error"] > 0:
		return "descendant_error"
	}
	return "success"
}

// LatencyMS returns the duration in milliseconds, or nil while running.
func (c *Call) LatencyMS() *float64 {
	if c.endedUS == nil {
		return nil
	}
	v := float64(*c.endedUS-c.startedUS) / 1000.0
	return &v
}

// TotalUsage returns own + descendant usage.
func (c *Call) TotalUsage() Contribution {
	if c.total != nil { // finishSummary already merged the roll-up into Summary
		return c.total.Clone()
	}
	t := TotalContribution(c.Summary, c.endedUS != nil, c.Exception != nil, c.cumulative, c.rollup)
	t.StatusCounts = nil
	return t
}

// CallsFilter is the structured filter of a calls query.
type CallsFilter struct {
	OpNames        []string `json:"op_names"`
	InputRefs      []string `json:"input_refs"`
	OutputRefs     []string `json:"output_refs"`
	ParentIDs      []string `json:"parent_ids"`
	TraceIDs       []string `json:"trace_ids"`
	CallIDs        []string `json:"call_ids"`
	TraceRootsOnly bool     `json:"trace_roots_only"`
	WBUserIDs      []string `json:"wb_user_ids"`
	WBRunIDs       []string `json:"wb_run_ids"`
	ThreadIDs      []string `json:"thread_ids"`
	TurnIDs        []string `json:"turn_ids"`
}

// SortBy is one sort key.
type SortBy struct {
	Field     string `json:"field"`
	Direction string `json:"direction"`
}

// CallsQuery is the body of /calls/query and /calls/stream_query.
type CallsQuery struct {
	ProjectID          string       `json:"project_id"`
	Filter             *CallsFilter `json:"filter"`
	Query              any          `json:"query"`
	Search             string       `json:"search"`
	SortBy             []SortBy     `json:"sort_by"`
	Limit              *int         `json:"limit"`
	Offset             int          `json:"offset"`
	Columns            []string     `json:"columns"`
	ExpandColumns      []string     `json:"expand_columns"`
	IncludeCosts       bool         `json:"include_costs"`
	IncludeFeedback    bool         `json:"include_feedback"`
	IncludeStorageSize bool         `json:"include_storage_size"`
	IncludeDeleted     bool         `json:"include_deleted"`
}

const statusSQL = `(CASE WHEN c.exception IS NOT NULL THEN 'error' WHEN c.ended_at IS NULL THEN 'running' WHEN COALESCE(json_extract(c.rollup, '$.status_counts.error'), 0) > 0 THEN 'descendant_error' ELSE 'success' END)`
const latencySQL = `(CASE WHEN c.ended_at IS NULL THEN NULL ELSE (c.ended_at - c.started_at) / 1000.0 END)`

var callColumns = map[string]string{
	"id": "c.id", "op_name": "c.op_name", "op_base_name": "c.op_base_name", "display_name": "c.display_name",
	"trace_id": "c.trace_id", "parent_id": "c.parent_id", "thread_id": "c.thread_id", "turn_id": "c.turn_id",
	"exception": "c.exception", "wb_user_id": "c.wb_user_id", "wb_run_id": "c.wb_run_id", "project_id": "c.project_id",
}

// callField resolves a field path on the calls table (alias c).
func callField(path string) (string, []any, bool, error) {
	if col, ok := callColumns[path]; ok {
		return col, nil, false, nil
	}
	switch path {
	case "started_at", "ended_at", "deleted_at":
		return "c." + path, nil, true, nil
	case "summary.weave.status":
		return statusSQL, nil, false, nil
	case "summary.weave.latency_ms":
		return latencySQL, nil, false, nil
	case "summary.weave.trace_name":
		return "c.op_base_name", nil, false, nil
	case "summary.weave.display_name":
		return "COALESCE(c.display_name, c.op_base_name)", nil, false, nil
	case "summary.total_tokens", "summary.weave.total_tokens":
		return "c.total_tokens", nil, false, nil
	case "storage_size_bytes":
		return "c.storage_size", nil, false, nil
	case "inputs", "attributes":
		return "c." + path, nil, false, nil
	case "output":
		return "json_extract(c.output, '$')", nil, false, nil
	}
	segs := splitField(path)
	if len(segs) < 2 {
		return "", nil, false, unknownField(path)
	}
	switch segs[0] {
	case "inputs", "output", "attributes":
		s, a := jsonField("c."+segs[0], segs[1:])
		return s, a, false, nil
	case "summary":
		if segs[1] == "status_counts" {
			s, a := jsonField("c.rollup", segs[1:])
			return s, a, false, nil
		}
		s, a := jsonField("c.summary", segs[1:])
		return s, a, false, nil
	case "feedback":
		return feedbackField(segs[1:])
	}
	return "", nil, false, unknownField(path)
}

// feedbackField resolves feedback.<type>.<payload path> to the payload value of
// the most recent matching feedback on the call.
func feedbackField(segs []string) (string, []any, bool, error) {
	if len(segs) == 0 {
		return "", nil, false, unknownField("feedback")
	}
	var ftype string
	var rest []string
	switch {
	case strings.Contains(segs[0], "."):
		ftype, rest = segs[0], segs[1:]
	case segs[0] == "wandb" && len(segs) >= 3:
		ftype, rest = strings.Join(segs[:3], "."), segs[3:]
	default:
		ftype, rest = segs[0], segs[1:]
	}
	if len(rest) > 0 && rest[0] == "payload" {
		rest = rest[1:]
	}
	return `(SELECT json_extract(f.payload, ?) FROM feedback f WHERE f.project_id = c.project_id
		AND f.weave_ref = ('weft:///' || c.project_id || '/call/' || c.id) AND f.feedback_type = ?
		ORDER BY f.created_at DESC LIMIT 1)`, []any{jsonPath(rest), ftype}, false, nil
}

func inList(col string, vals []string) (string, []any) {
	ph := strings.TrimSuffix(strings.Repeat("?,", len(vals)), ",")
	args := make([]any, len(vals))
	for i, v := range vals {
		args[i] = v
	}
	return col + " IN (" + ph + ")", args
}

// whereCalls builds the WHERE clause shared by queries, stats and counts.
func whereCalls(projectID string, f *CallsFilter, query any, search string, includeDeleted bool) (string, []any, error) {
	conds := []string{"c.project_id = ?", "c.has_start = 1"}
	args := []any{projectID}
	if !includeDeleted {
		conds = append(conds, "c.deleted_at IS NULL")
	}
	if f != nil {
		if len(f.OpNames) > 0 {
			var ors []string
			for _, n := range f.OpNames {
				n = refs.Normalize(n)
				if refs.IsRef(n) {
					r, err := refs.Parse(n)
					if err != nil {
						return "", nil, Invalidf("bad op ref %q", n)
					}
					if r.Digest == "*" || r.Digest == "latest" || r.Digest == "" {
						ors = append(ors, "c.op_base_name = ?")
						args = append(args, r.Name)
					} else {
						ors = append(ors, "c.op_name = ?")
						args = append(args, r.Base().String())
					}
				} else {
					ors = append(ors, "(c.op_base_name = ? OR c.op_name = ?)")
					args = append(args, n, n)
				}
			}
			conds = append(conds, "("+strings.Join(ors, " OR ")+")")
		}
		for _, x := range []struct {
			col  string
			vals []string
		}{
			{"c.parent_id", f.ParentIDs}, {"c.trace_id", f.TraceIDs}, {"c.id", f.CallIDs},
			{"c.wb_user_id", f.WBUserIDs}, {"c.wb_run_id", f.WBRunIDs}, {"c.thread_id", f.ThreadIDs}, {"c.turn_id", f.TurnIDs},
		} {
			if len(x.vals) > 0 {
				s, a := inList(x.col, x.vals)
				conds = append(conds, s)
				args = append(args, a...)
			}
		}
		for _, x := range []struct {
			col  string
			vals []string
		}{{"c.input_refs", f.InputRefs}, {"c.output_refs", f.OutputRefs}} {
			if len(x.vals) > 0 {
				norm := make([]string, len(x.vals))
				for i, v := range x.vals {
					norm[i] = refs.Normalize(v)
				}
				s, a := inList("value", norm)
				conds = append(conds, "EXISTS (SELECT 1 FROM json_each("+x.col+") WHERE "+s+")")
				args = append(args, a...)
			}
		}
		if f.TraceRootsOnly {
			conds = append(conds, "c.parent_id IS NULL")
		}
	}
	if query != nil {
		s, a, err := compileQuery(query, callField)
		if err != nil {
			return "", nil, err
		}
		if s != "" {
			conds = append(conds, "("+s+")")
			args = append(args, a...)
		}
	}
	if search = strings.TrimSpace(search); search != "" {
		conds = append(conds, `(instr(lower(c.op_name), lower(?)) > 0 OR instr(lower(COALESCE(c.display_name, '')), lower(?)) > 0
			OR instr(lower(c.inputs), lower(?)) > 0 OR instr(lower(COALESCE(c.output, '')), lower(?)) > 0
			OR instr(lower(COALESCE(c.exception, '')), lower(?)) > 0 OR c.id = ? OR c.trace_id = ?)`)
		for i := 0; i < 7; i++ {
			args = append(args, search)
		}
	}
	return strings.Join(conds, " AND "), args, nil
}

func orderCalls(sorts []SortBy) (string, []any, error) {
	if len(sorts) == 0 {
		return "c.started_at DESC, c.id DESC", nil, nil
	}
	var parts []string
	var args []any
	for _, s := range sorts {
		dir, err := sortDirection(s.Direction)
		if err != nil {
			return "", nil, &InvalidError{Msg: err.Error()}
		}
		expr, a, _, err := callField(s.Field)
		if err != nil {
			return "", nil, err
		}
		parts = append(parts, expr+" "+dir)
		args = append(args, a...)
	}
	parts = append(parts, "c.id ASC")
	return strings.Join(parts, ", "), args, nil
}

const callSelect = `SELECT c.project_id, c.id, c.op_name, c.op_base_name, c.display_name, c.trace_id, c.parent_id, c.thread_id, c.turn_id,
	c.started_at, c.ended_at, c.exception, c.attributes, c.inputs, c.output, c.summary, c.rollup, c.wb_user_id, c.wb_run_id,
	c.deleted_at, c.storage_size, c.cumulative FROM calls c`

func scanCall(rows interface{ Scan(...any) error }) (*Call, error) {
	var c Call
	var display, parent, thread, turn, exc, output, user, run sql.NullString
	var started, ended, deleted sql.NullInt64
	var attrs, inputs, summary, rollup string
	var size int64
	if err := rows.Scan(&c.ProjectID, &c.ID, &c.OpName, &c.opBaseName, &display, &c.TraceID, &parent, &thread, &turn,
		&started, &ended, &exc, &attrs, &inputs, &output, &summary, &rollup, &user, &run, &deleted, &size, &c.cumulative); err != nil {
		return nil, err
	}
	c.DisplayName, c.ParentID, c.ThreadID, c.TurnID = strPtr(display), strPtr(parent), strPtr(thread), strPtr(turn)
	c.Exception, c.WBUserID, c.WBRunID = strPtr(exc), strPtr(user), strPtr(run)
	c.startedUS = started.Int64
	if !started.Valid && ended.Valid {
		c.startedUS = ended.Int64
	}
	c.StartedAt = FormatMicros(c.startedUS)
	if ended.Valid {
		e := ended.Int64
		c.endedUS = &e
	}
	c.EndedAt = fmtNullMicros(ended)
	c.DeletedAt = fmtNullMicros(deleted)
	c.Attributes = decodeMap(attrs)
	c.Inputs = decodeMap(inputs)
	if output.Valid {
		c.Output = decodeJSON(output.String)
	}
	c.Summary = decodeMap(summary)
	c.rollup = ParseContribution(rollup)
	c.StorageSizeBytes = &size
	return &c, nil
}

// finishSummary merges roll-ups into the summary and adds computed fields.
func (c *Call) finishSummary() {
	sum := c.Summary
	if sum == nil {
		sum = map[string]any{}
	}
	usage := c.TotalUsage()
	c.total = &usage
	if len(usage.Usage) > 0 {
		sum["usage"] = usage.UsageMap()
	} else {
		delete(sum, "usage")
	}
	sc := map[string]any{"success": int64(0), "error": int64(0)}
	for k, v := range c.rollup.StatusCounts {
		sc[k] = numberValue(v)
	}
	sum["status_counts"] = sc
	weave, _ := sum["weave"].(map[string]any)
	if weave == nil {
		weave = map[string]any{}
	}
	weave["status"] = c.Status()
	if l := c.LatencyMS(); l != nil {
		weave["latency_ms"] = numberValue(*l)
	} else {
		weave["latency_ms"] = nil
	}
	weave["trace_name"] = c.opBaseName
	weave["display_name"] = c.DisplayName
	sum["weave"] = weave
	c.Summary = sum
}

// QueryCalls runs a calls query and calls fn for each result in order.
func (s *Store) QueryCalls(ctx context.Context, q CallsQuery, fn func(*Call) error) error {
	pid, err := refs.NormalizeProject(q.ProjectID)
	if err != nil {
		return &InvalidError{Msg: err.Error()}
	}
	q.ProjectID = pid
	where, args, err := whereCalls(pid, q.Filter, q.Query, q.Search, q.IncludeDeleted)
	if err != nil {
		return err
	}
	order, oargs, err := orderCalls(q.SortBy)
	if err != nil {
		return err
	}
	limit := 10000
	if q.Limit != nil {
		limit = *q.Limit
	}
	if limit <= 0 || limit > 100000 {
		limit = 100000
	}
	if q.Offset < 0 {
		return Invalidf("offset must be >= 0")
	}
	args = append(args, oargs...)
	args = append(args, limit, q.Offset)
	rows, err := s.r.QueryContext(ctx, callSelect+" WHERE "+where+" ORDER BY "+order+" LIMIT ? OFFSET ?", args...)
	if err != nil {
		return err
	}
	// Buffer one page so that enrichment queries don't hold the cursor.
	var page []*Call
	flush := func() error {
		if len(page) == 0 {
			return nil
		}
		if err := s.enrichCalls(ctx, pid, page, q); err != nil {
			return err
		}
		for _, c := range page {
			if err := fn(c); err != nil {
				return err
			}
		}
		page = page[:0]
		return nil
	}
	var all [][]*Call
	for rows.Next() {
		c, err := scanCall(rows)
		if err != nil {
			rows.Close()
			return err
		}
		page = append(page, c)
		if len(page) == 500 {
			all = append(all, page)
			page = nil
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(page) > 0 {
		all = append(all, page)
	}
	for _, p := range all {
		page = p
		if err := flush(); err != nil {
			return err
		}
	}
	return nil
}

// ListCalls is QueryCalls collected into a slice.
func (s *Store) ListCalls(ctx context.Context, q CallsQuery) ([]*Call, error) {
	out := []*Call{}
	err := s.QueryCalls(ctx, q, func(c *Call) error {
		out = append(out, c)
		return nil
	})
	return out, err
}

// GetCall reads one call.
func (s *Store) GetCall(ctx context.Context, projectID, id string, includeCosts, includeFeedback, includeSize bool) (*Call, error) {
	one := 1
	calls, err := s.ListCalls(ctx, CallsQuery{
		ProjectID: projectID, Filter: &CallsFilter{CallIDs: []string{id}}, Limit: &one, IncludeDeleted: true,
		IncludeCosts: includeCosts, IncludeFeedback: includeFeedback, IncludeStorageSize: includeSize,
	})
	if err != nil {
		return nil, err
	}
	if len(calls) == 0 {
		return nil, ErrNotFound
	}
	return calls[0], nil
}

// CountCalls returns the number of matching calls and their storage size.
func (s *Store) CountCalls(ctx context.Context, projectID string, f *CallsFilter, query any, search string) (int64, int64, error) {
	pid, err := refs.NormalizeProject(projectID)
	if err != nil {
		return 0, 0, &InvalidError{Msg: err.Error()}
	}
	where, args, err := whereCalls(pid, f, query, search, false)
	if err != nil {
		return 0, 0, err
	}
	var n, size int64
	err = s.r.QueryRowContext(ctx, "SELECT COUNT(*), COALESCE(SUM(c.storage_size), 0) FROM calls c WHERE "+where, args...).Scan(&n, &size)
	return n, size, err
}

func (s *Store) enrichCalls(ctx context.Context, projectID string, calls []*Call, q CallsQuery) error {
	var prices *PriceBook
	if q.IncludeCosts {
		var err error
		if prices, err = s.LoadPriceBook(ctx, projectID); err != nil {
			return err
		}
	}
	for _, c := range calls {
		c.finishSummary()
		if prices != nil {
			if costs := prices.Costs(c.TotalUsage(), c.startedUS); len(costs) > 0 {
				c.Summary["weave"].(map[string]any)["costs"] = costs
			}
		}
		if !q.IncludeStorageSize {
			c.StorageSizeBytes = nil
		}
	}
	if q.IncludeFeedback {
		byRef := map[string]*Call{}
		refList := make([]string, 0, len(calls))
		for _, c := range calls {
			r := refs.CallRef(projectID, c.ID)
			byRef[r] = c
			refList = append(refList, r)
			c.Feedback = []Feedback{}
		}
		fbs, err := s.feedbackForRefs(ctx, projectID, refList)
		if err != nil {
			return err
		}
		for _, f := range fbs {
			if c := byRef[f.WeaveRef]; c != nil {
				c.Feedback = append(c.Feedback, f)
			}
		}
	}
	if len(q.ExpandColumns) > 0 {
		if err := s.expandCalls(ctx, calls, q.ExpandColumns); err != nil {
			return err
		}
	}
	return nil
}

func getPath(root map[string]any, segs []string) (any, bool) {
	var cur any = root
	for _, s := range segs {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = m[s]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

func setPath(root map[string]any, segs []string, v any) {
	cur := root
	for i, s := range segs {
		if i == len(segs)-1 {
			cur[s] = v
			return
		}
		next, ok := cur[s].(map[string]any)
		if !ok {
			return
		}
		cur = next
	}
}

// expandCalls replaces refs at the given paths with their values. Expanded
// dicts gain a "_ref" key holding the original ref.
func (s *Store) expandCalls(ctx context.Context, calls []*Call, paths []string) error {
	type site struct {
		c    *Call
		segs []string
		ref  string
	}
	var sites []site
	uniq := map[string]bool{}
	var refList []string
	for _, c := range calls {
		root := map[string]any{"inputs": c.Inputs, "output": c.Output, "attributes": c.Attributes}
		for _, p := range paths {
			segs := splitField(p)
			v, ok := getPath(root, segs)
			if !ok {
				continue
			}
			str, ok := v.(string)
			if !ok || !refs.IsRef(str) {
				continue
			}
			sites = append(sites, site{c, segs, str})
			if !uniq[str] {
				uniq[str] = true
				refList = append(refList, str)
			}
		}
	}
	if len(refList) == 0 {
		return nil
	}
	vals, err := s.ReadRefs(ctx, refList)
	if err != nil {
		return err
	}
	resolved := map[string]any{}
	for i, r := range refList {
		resolved[r] = vals[i]
	}
	for _, st := range sites {
		v := resolved[st.ref]
		if v == nil {
			continue
		}
		if m, ok := v.(map[string]any); ok {
			cp := make(map[string]any, len(m)+1)
			for k, x := range m {
				cp[k] = x
			}
			cp["_ref"] = st.ref
			v = cp
		}
		switch st.segs[0] {
		case "inputs":
			setPath(st.c.Inputs, st.segs[1:], v)
		case "attributes":
			setPath(st.c.Attributes, st.segs[1:], v)
		case "output":
			if len(st.segs) == 1 {
				st.c.Output = v
			} else if m, ok := st.c.Output.(map[string]any); ok {
				setPath(m, st.segs[1:], v)
			}
		}
	}
	return nil
}

// CallExists reports whether a call exists (not deleted).
func (s *Store) CallExists(ctx context.Context, projectID, id string) (bool, error) {
	var one int
	err := s.r.QueryRowContext(ctx, `SELECT 1 FROM calls WHERE project_id = ? AND id = ? AND deleted_at IS NULL`, projectID, id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// CallMatches reports whether call id matches a query expression.
func (s *Store) CallMatches(ctx context.Context, projectID, id string, query any) (bool, error) {
	where, args, err := whereCalls(projectID, &CallsFilter{CallIDs: []string{id}}, query, "", false)
	if err != nil {
		return false, err
	}
	var n int
	err = s.r.QueryRowContext(ctx, "SELECT COUNT(*) FROM calls c WHERE "+where, args...).Scan(&n)
	return n > 0, err
}
