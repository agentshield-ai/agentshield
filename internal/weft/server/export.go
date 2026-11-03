package server

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/agentshield-ai/agentshield/internal/weft/store"
	"github.com/go-chi/chi/v5"
)

// /calls/export streams every call matching a CallsQuery as CSV, JSONL or a
// JSON array. Rows are read page by page so memory stays bounded however many
// calls match. CSV (and `flatten: true`) turns nested values into dotted
// columns (inputs.question, output.answer, attributes.env, ...).

func init() {
	routeExtensions = append(routeExtensions, func(s *Server, r chi.Router) {
		r.Post("/calls/export", s.callsExport)
	})
}

const (
	exportPageSize   = 500
	maxExportColumns = 1000
	maxFlattenDepth  = 8
)

type callsExportReq struct {
	store.CallsQuery
	Format  string `json:"format"`
	Flatten *bool  `json:"flatten"`
}

// exportBaseColumns come first, in this order, in CSV and flattened output.
var exportBaseColumns = []string{
	"id", "trace_id", "parent_id", "op_name", "display_name", "status", "started_at", "ended_at", "latency_ms",
	"prompt_tokens", "completion_tokens", "total_tokens", "cost_usd", "exception", "thread_id", "turn_id", "wb_user_id", "wb_run_id",
}

func (s *Server) callsExport(w http.ResponseWriter, r *http.Request) {
	var req callsExportReq
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	format := strings.ToLower(strings.TrimSpace(req.Format))
	if format == "" {
		format = "jsonl"
	}
	if format != "csv" && format != "jsonl" && format != "json" {
		writeError(w, http.StatusBadRequest, "format must be csv, jsonl or json")
		return
	}
	flatten := format == "csv"
	if req.Flatten != nil {
		flatten = *req.Flatten
	}
	columns := make([]string, 0, len(req.Columns))
	for _, c := range req.Columns {
		if c = strings.TrimSpace(c); c != "" {
			columns = append(columns, c)
		}
	}
	if len(columns) > maxExportColumns {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("at most %d columns", maxExportColumns))
		return
	}
	q := req.CallsQuery
	q.IncludeCosts = true
	q.Columns = nil
	// Calls that start while the export runs would shift the pages; pin the end.
	q.Query = andQuery(q.Query, map[string]any{"$lte": []any{
		map[string]any{"$getField": "started_at"}, map[string]any{"$literal": time.Now().UTC().Format(time.RFC3339Nano)},
	}})
	remaining := -1
	if req.Limit != nil && *req.Limit >= 0 {
		remaining = *req.Limit
	}
	baseOffset := q.Offset

	// pages calls fn for each page; the first page is read before anything is
	// written so a bad query still gets a proper error status.
	pages := func(fn func([]*store.Call) error) error {
		return forEachCallPage(r.Context(), s.store, q, baseOffset, remaining, fn)
	}

	// Validate the query up front.
	one := 1
	probe := q
	probe.Limit = &one
	if _, err := s.store.ListCalls(r.Context(), probe); err != nil {
		writeErr(w, err)
		return
	}

	// CSV with flattened columns needs the full column set before the header:
	// a first pass collects keys only.
	if format == "csv" && len(columns) == 0 {
		if flatten {
			seen := map[string]bool{}
			for _, c := range exportBaseColumns {
				seen[c] = true
			}
			var extra []string
			err := pages(func(page []*store.Call) error {
				for _, c := range page {
					for k := range flattenCall(c, req.IncludeFeedback) {
						if !seen[k] && len(extra) < maxExportColumns-len(exportBaseColumns) {
							seen[k] = true
							extra = append(extra, k)
						}
					}
				}
				return nil
			})
			if err != nil {
				writeErr(w, err)
				return
			}
			sortExportColumns(extra)
			columns = append(append(columns, exportBaseColumns...), extra...)
		} else {
			columns = append(append(columns, exportBaseColumns...), "inputs", "output", "attributes")
			if req.IncludeFeedback {
				columns = append(columns, "feedback")
			}
		}
	}

	ext := map[string]string{"csv": "csv", "jsonl": "jsonl", "json": "json"}[format]
	ctype := map[string]string{"csv": "text/csv; charset=utf-8", "jsonl": "application/jsonl", "json": "application/json"}[format]
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, exportFilename(q.ProjectID, ext)))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	flusher, _ := w.(http.Flusher)
	flush := func() {
		if flusher != nil {
			flusher.Flush()
		}
	}

	var err error
	switch format {
	case "csv":
		cw := csv.NewWriter(w)
		_ = cw.Write(columns)
		err = pages(func(page []*store.Call) error {
			rec := make([]string, len(columns))
			for _, c := range page {
				row := exportRow(c, req.IncludeFeedback)
				var flat map[string]any
				if flatten {
					flat = flattenCall(c, req.IncludeFeedback)
				}
				for i, col := range columns {
					v, ok := flat[col]
					if !ok {
						v = lookupDotted(row, col)
					}
					rec[i] = csvValue(v)
				}
				if err := cw.Write(rec); err != nil {
					return err
				}
			}
			cw.Flush()
			flush()
			return cw.Error()
		})
	default:
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(false)
		first := true
		if format == "json" {
			_, _ = io.WriteString(w, "[\n")
		}
		err = pages(func(page []*store.Call) error {
			for _, c := range page {
				var v any = c
				switch {
				case len(columns) > 0:
					row := exportRow(c, req.IncludeFeedback)
					var flat map[string]any
					if flatten {
						flat = flattenCall(c, req.IncludeFeedback)
					}
					obj := make(map[string]any, len(columns))
					for _, col := range columns {
						x, ok := flat[col]
						if !ok {
							x = lookupDotted(row, col)
						}
						obj[col] = x
					}
					v = obj
				case flatten:
					v = flattenCall(c, req.IncludeFeedback)
				}
				if format == "json" && !first {
					if _, err := io.WriteString(w, ",\n"); err != nil {
						return err
					}
				}
				first = false
				if err := enc.Encode(v); err != nil {
					return err
				}
			}
			flush()
			return nil
		})
		if format == "json" {
			_, _ = io.WriteString(w, "]\n")
		}
	}
	if err != nil {
		// Headers are gone; all we can do is log and stop.
		s.log.Warn("calls export stopped", "err", err)
	}
}

// forEachCallPage reads the calls of q page by page (exportPageSize at a
// time, starting at offset, at most limit calls when limit >= 0) and calls fn
// with each non-empty page. Also used by bulk export jobs (bulkexport.go).
func forEachCallPage(ctx context.Context, st *store.Store, q store.CallsQuery, offset, limit int, fn func([]*store.Call) error) error {
	done := 0
	for limit < 0 || done < limit {
		n := exportPageSize
		if limit >= 0 {
			n = min(n, limit-done)
		}
		pq := q
		pq.Limit = &n
		pq.Offset = offset + done
		page, err := st.ListCalls(ctx, pq)
		if err != nil {
			return err
		}
		if len(page) > 0 {
			if err := fn(page); err != nil {
				return err
			}
		}
		done += len(page)
		if len(page) < n {
			return nil
		}
	}
	return nil
}

// andQuery adds an operation to a CallsQuery query ({"$expr": op} or a bare op).
func andQuery(q any, op map[string]any) any {
	var cur any
	if m, ok := q.(map[string]any); ok && len(m) > 0 {
		if e, ok := m["$expr"]; ok {
			if em, ok := e.(map[string]any); ok && len(em) > 0 {
				cur = em
			}
		} else {
			cur = m
		}
	}
	if cur == nil {
		return map[string]any{"$expr": op}
	}
	return map[string]any{"$expr": map[string]any{"$and": []any{cur, op}}}
}

func exportFilename(projectID, ext string) string {
	slug := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		}
		return '-'
	}, projectID)
	return fmt.Sprintf("%s-calls-%s.%s", slug, time.Now().UTC().Format("20060102-150405"), ext)
}

// exportRow is the structured row: call fields plus computed status, latency,
// tokens and cost.
func exportRow(c *store.Call, withFeedback bool) map[string]any {
	u := c.TotalUsage()
	var pr, co, tot float64
	for _, m := range u.Usage {
		a, b, t := store.UsageTokens(m)
		pr, co, tot = pr+a, co+b, tot+t
	}
	var cost any
	if w, ok := c.Summary["weft"].(map[string]any); ok {
		if costs, ok := w["costs"].(map[string]any); ok && len(costs) > 0 {
			sum := 0.0
			for _, x := range costs {
				if cm, ok := x.(map[string]any); ok {
					a, _ := store.ToFloat(cm["prompt_tokens_total_cost"])
					b, _ := store.ToFloat(cm["completion_tokens_total_cost"])
					sum += a + b
				}
			}
			cost = sum
		}
	}
	row := map[string]any{
		"id": c.ID, "project_id": c.ProjectID, "trace_id": c.TraceID, "parent_id": c.ParentID, "op_name": c.OpName,
		"display_name": c.DisplayName, "status": c.Status(), "started_at": c.StartedAt, "ended_at": c.EndedAt,
		"latency_ms": c.LatencyMS(), "exception": c.Exception, "thread_id": c.ThreadID, "turn_id": c.TurnID,
		"wb_user_id": c.WBUserID, "wb_run_id": c.WBRunID, "cost_usd": cost,
		"inputs": c.Inputs, "output": c.Output, "attributes": c.Attributes, "summary": c.Summary,
	}
	if len(u.Usage) > 0 {
		row["prompt_tokens"], row["completion_tokens"], row["total_tokens"] = pr, co, tot
	}
	if withFeedback {
		row["feedback"] = c.Feedback
	}
	return row
}

// flattenCall returns base columns plus dotted inputs/output/attributes leaves.
func flattenCall(c *store.Call, withFeedback bool) map[string]any {
	row := exportRow(c, withFeedback)
	out := make(map[string]any, 32)
	for _, k := range exportBaseColumns {
		out[k] = deref(row[k])
	}
	flattenInto(out, "inputs", c.Inputs, 0)
	flattenInto(out, "output", c.Output, 0)
	flattenInto(out, "attributes", c.Attributes, 0)
	if withFeedback {
		out["feedback"] = jsonString(c.Feedback)
	}
	return out
}

func flattenInto(out map[string]any, prefix string, v any, depth int) {
	switch t := v.(type) {
	case map[string]any:
		if len(t) == 0 {
			if depth > 0 {
				out[prefix] = "{}"
			}
			return
		}
		if depth >= maxFlattenDepth {
			out[prefix] = jsonString(t)
			return
		}
		for k, x := range t {
			flattenInto(out, prefix+"."+k, x, depth+1)
		}
	case []any:
		out[prefix] = jsonString(t)
	default:
		out[prefix] = t
	}
}

// sortExportColumns orders inputs.*, output*, attributes.*, then anything else.
func sortExportColumns(cols []string) {
	rank := func(c string) int {
		switch {
		case strings.HasPrefix(c, "inputs."):
			return 0
		case c == "output" || strings.HasPrefix(c, "output."):
			return 1
		case strings.HasPrefix(c, "attributes."):
			return 2
		}
		return 3
	}
	sort.SliceStable(cols, func(i, j int) bool {
		if ri, rj := rank(cols[i]), rank(cols[j]); ri != rj {
			return ri < rj
		}
		return cols[i] < cols[j]
	})
}

// lookupDotted walks a dotted path, also matching keys that contain dots.
func lookupDotted(root map[string]any, path string) any {
	if v, ok := root[path]; ok {
		return deref(v)
	}
	parts := strings.Split(path, ".")
	var cur any = root
	for i := 0; i < len(parts); i++ {
		m, ok := cur.(map[string]any)
		if !ok {
			if arr, ok := cur.([]any); ok {
				if n, err := strconv.Atoi(parts[i]); err == nil && n >= 0 && n < len(arr) {
					cur = arr[n]
					continue
				}
			}
			return nil
		}
		if x, ok := m[parts[i]]; ok {
			cur = x
			continue
		}
		// Keys with dots: try the longest joined key first.
		found := false
		for j := len(parts); j > i+1; j-- {
			if x, ok := m[strings.Join(parts[i:j], ".")]; ok {
				cur, i, found = x, j-1, true
				break
			}
		}
		if !found {
			return nil
		}
	}
	return deref(cur)
}

func deref(v any) any {
	switch t := v.(type) {
	case *string:
		if t == nil {
			return nil
		}
		return *t
	case *float64:
		if t == nil {
			return nil
		}
		return *t
	}
	return v
}

func jsonString(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// csvValue renders a cell. Strings that a spreadsheet would run as a formula
// get a leading apostrophe (OWASP CSV injection guidance).
func csvValue(v any) string {
	switch t := deref(v).(type) {
	case nil:
		return ""
	case string:
		if t != "" && strings.ContainsRune("=+-@\t\r", rune(t[0])) {
			if _, err := strconv.ParseFloat(t, 64); err != nil {
				return "'" + t
			}
		}
		return t
	case bool:
		return strconv.FormatBool(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case int64:
		return strconv.FormatInt(t, 10)
	case int:
		return strconv.Itoa(t)
	case json.Number:
		return t.String()
	default:
		return jsonString(t)
	}
}
