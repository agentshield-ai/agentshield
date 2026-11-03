package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/agentshield-ai/agentshield/internal/weft/refs"
)

// Store helpers for smaller trace API endpoints (tables from digests,
// batched table stats, file stats, feedback analytics).

// CreateTableFromDigests builds a table from existing row digests.
func (s *Store) CreateTableFromDigests(ctx context.Context, projectID string, rowDigests []string) (string, error) {
	pid, err := refs.NormalizeProject(projectID)
	if err != nil {
		return "", &InvalidError{Msg: err.Error()}
	}
	if len(rowDigests) > 1000000 {
		return "", Invalidf("too many rows")
	}
	uniq := map[string]bool{}
	for _, d := range rowDigests {
		uniq[d] = true
	}
	list := make([]string, 0, len(uniq))
	for d := range uniq {
		list = append(list, d)
	}
	found := 0
	for start := 0; start < len(list); start += 500 {
		end := min(start+500, len(list))
		c, a := inList("digest", list[start:end])
		var n int
		if err := s.r.QueryRowContext(ctx, `SELECT COUNT(*) FROM table_rows WHERE project_id = ? AND `+c, append([]any{pid}, a...)...).Scan(&n); err != nil {
			return "", err
		}
		found += n
	}
	if found != len(list) {
		return "", Invalidf("%d of the row digests are unknown in this project", len(list)-found)
	}
	var digest string
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		if err := s.touchProject(ctx, tx, pid); err != nil {
			return err
		}
		digest, err = s.insertTable(ctx, tx, pid, rowDigests)
		return err
	})
	return digest, err
}

// TableStat is one entry of /table/query_stats_batch.
type TableStat struct {
	Digest           string `json:"digest"`
	Count            int    `json:"count"`
	StorageSizeBytes *int64 `json:"storage_size_bytes,omitempty"`
}

// TableStatsBatch counts rows (and optionally bytes) of several tables.
// Unknown digests are omitted.
func (s *Store) TableStatsBatch(ctx context.Context, projectID string, digests []string, withSize bool) ([]TableStat, error) {
	pid, err := refs.NormalizeProject(projectID)
	if err != nil {
		return nil, &InvalidError{Msg: err.Error()}
	}
	if len(digests) > 1000 {
		return nil, Invalidf("too many digests (max 1000)")
	}
	out := []TableStat{}
	for _, d := range digests {
		var n int
		err := s.r.QueryRowContext(ctx, `SELECT json_array_length(row_digests) FROM tables WHERE project_id = ? AND digest = ?`, pid, d).Scan(&n)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		st := TableStat{Digest: d, Count: n}
		if withSize {
			var size int64
			if err := s.r.QueryRowContext(ctx, `SELECT COALESCE(SUM(length(r.val)), 0) FROM tables t, json_each(t.row_digests) j
				JOIN table_rows r ON r.project_id = t.project_id AND r.digest = j.value WHERE t.project_id = ? AND t.digest = ?`, pid, d).Scan(&size); err != nil {
				return nil, err
			}
			st.StorageSizeBytes = &size
		}
		out = append(out, st)
	}
	return out, nil
}

// FilesTotalSize sums the stored file bytes of a project.
func (s *Store) FilesTotalSize(ctx context.Context, projectID string) (int64, error) {
	pid, err := refs.NormalizeProject(projectID)
	if err != nil {
		return 0, &InvalidError{Msg: err.Error()}
	}
	var n int64
	err = s.r.QueryRowContext(ctx, `SELECT COALESCE(SUM(size_bytes), 0) FROM files WHERE project_id = ?`, pid).Scan(&n)
	return n, err
}

// FeedbackRow is the subset of feedback used for analytics.
type FeedbackRow struct {
	CreatedUS    int64
	FeedbackType string
	Payload      any
	RunnableRef  *string
	TriggerRef   *string
}

// FeedbackFilter selects feedback for analytics.
type FeedbackFilter struct {
	StartUS, EndUS int64
	// FeedbackType matches exactly; TypePrefixes match by prefix.
	FeedbackType *string
	TypePrefixes []string
	// TriggerRefs and RunnableRefs match exactly, or by prefix when they end with "*"
	// (a ref ending ":*" matches every version).
	TriggerRefs  []string
	RunnableRefs []string
	Limit        int
}

func prefixOrExact(col string, vals []string) (string, []any) {
	var parts []string
	var args []any
	for _, v := range vals {
		if strings.HasSuffix(v, "*") {
			parts = append(parts, col+" LIKE ? ESCAPE '\\'")
			args = append(args, likeEscape(strings.TrimSuffix(v, "*"))+"%")
		} else {
			parts = append(parts, col+" = ?")
			args = append(args, v)
		}
	}
	return "(" + strings.Join(parts, " OR ") + ")", args
}

func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// FeedbackForAnalytics loads feedback in [StartUS, EndUS) oldest first.
func (s *Store) FeedbackForAnalytics(ctx context.Context, projectID string, f FeedbackFilter) ([]FeedbackRow, error) {
	pid, err := refs.NormalizeProject(projectID)
	if err != nil {
		return nil, &InvalidError{Msg: err.Error()}
	}
	conds := []string{"project_id = ?", "created_at >= ?", "created_at < ?"}
	args := []any{pid, f.StartUS, f.EndUS}
	if f.FeedbackType != nil && *f.FeedbackType != "" {
		conds, args = append(conds, "feedback_type = ?"), append(args, *f.FeedbackType)
	}
	if len(f.TypePrefixes) > 0 {
		var parts []string
		for _, p := range f.TypePrefixes {
			parts = append(parts, "feedback_type LIKE ? ESCAPE '\\'")
			args = append(args, likeEscape(p)+"%")
		}
		conds = append(conds, "("+strings.Join(parts, " OR ")+")")
	}
	if len(f.TriggerRefs) > 0 {
		c, a := prefixOrExact("trigger_ref", f.TriggerRefs)
		conds, args = append(conds, c), append(args, a...)
	}
	if len(f.RunnableRefs) > 0 {
		c, a := prefixOrExact("runnable_ref", f.RunnableRefs)
		conds, args = append(conds, c), append(args, a...)
	}
	limit := f.Limit
	if limit <= 0 || limit > 200000 {
		limit = 200000
	}
	rows, err := s.r.QueryContext(ctx, `SELECT created_at, feedback_type, payload, runnable_ref, trigger_ref FROM feedback WHERE `+
		strings.Join(conds, " AND ")+` ORDER BY created_at LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FeedbackRow
	for rows.Next() {
		var r FeedbackRow
		var payload string
		var rr, tr sql.NullString
		if err := rows.Scan(&r.CreatedUS, &r.FeedbackType, &payload, &rr, &tr); err != nil {
			return nil, err
		}
		r.Payload = decodeJSON(payload)
		r.RunnableRef, r.TriggerRef = strPtr(rr), strPtr(tr)
		out = append(out, r)
	}
	return out, rows.Err()
}
