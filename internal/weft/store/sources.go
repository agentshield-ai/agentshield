package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// Session sources: the producers that can record the same Claude Code
// session, richest first. The weft-claude-code plugin reads the transcript
// (reasoning, tool results); Claude Code's own OpenTelemetry export sends
// traces (beta) and log events. A user may turn on more than one, so each
// session is shown from one source only: the highest-ranked source that has
// sent spans for it. Spans of a lower source are dropped at ingest once a
// higher one is present, and are soft-deleted when a higher one arrives
// (docs/weft/README.md, "Claude Code without a plugin").
//
// A span's source is its weft.source attribute; spans of any other source
// (or none) are never affected.
const (
	SourceClaudeCodePlugin = "weft-claude-code"
	SourceClaudeCodeTraces = "claude-code-otel-traces"
	SourceClaudeCodeLogs   = "claude-code-otel-logs"
)

var sessionSourceRank = map[string]int{SourceClaudeCodePlugin: 3, SourceClaudeCodeTraces: 2, SourceClaudeCodeLogs: 1}

// SessionSourceRank is the precedence of a span source (0: not ranked).
func SessionSourceRank(source string) int { return sessionSourceRank[source] }

// sourcesRanked returns the ranked sources above (higher) or below rank.
func sourcesRanked(rank int, higher bool) []string {
	var out []string
	for s, r := range sessionSourceRank {
		if (higher && r > rank) || (!higher && r < rank) {
			out = append(out, s)
		}
	}
	return out
}

type sourceBatchKey struct{}

// sourceBatch remembers, within one write, the decision taken for each
// (project, session, source), so a batch checks a session once.
type sourceBatch struct{ drop map[[3]string]bool }

func withSourceBatch(ctx context.Context) context.Context {
	if _, ok := ctx.Value(sourceBatchKey{}).(*sourceBatch); ok {
		return ctx
	}
	return context.WithValue(ctx, sourceBatchKey{}, &sourceBatch{drop: map[[3]string]bool{}})
}

// spanSource is a call's weft.source attribute (nested or flat).
func spanSource(attributes map[string]any) string {
	return newSpanAttrs(attributes).str("weft.source")
}

// applySourcePrecedence decides whether a call start of a ranked source is
// stored: it is dropped when its session already has spans of a higher
// source; otherwise spans of lower sources in the session are soft-deleted
// (and detections linked to them are linked again).
func (s *Store) applySourcePrecedence(ctx context.Context, tx *sql.Tx, st *CallStart) (drop bool, err error) {
	if st.ThreadID == nil || *st.ThreadID == "" {
		return false, nil
	}
	src := spanSource(st.Attributes)
	rank := sessionSourceRank[src]
	if rank == 0 {
		return false, nil
	}
	key := [3]string{st.ProjectID, *st.ThreadID, src}
	b, _ := ctx.Value(sourceBatchKey{}).(*sourceBatch)
	if b != nil {
		if d, ok := b.drop[key]; ok {
			return s.markSourceDrop(ctx, tx, st, d)
		}
	}
	drop, err = s.sessionHasSource(ctx, tx, st.ProjectID, *st.ThreadID, sourcesRanked(rank, true))
	if err != nil {
		return false, err
	}
	if !drop {
		if err := s.retireLowerSources(ctx, tx, st.ProjectID, *st.ThreadID, sourcesRanked(rank, false)); err != nil {
			return false, err
		}
	}
	if b != nil {
		b.drop[key] = drop
	}
	return s.markSourceDrop(ctx, tx, st, drop)
}

// markSourceDrop records a dropped start so that its end is dropped too.
func (s *Store) markSourceDrop(ctx context.Context, tx *sql.Tx, st *CallStart, drop bool) (bool, error) {
	if !drop {
		return false, nil
	}
	_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO ingest_dropped(project_id, id, dropped_at) VALUES(?,?,?)`, st.ProjectID, st.ID, nowMicros())
	return true, err
}

// droppedEnd reports an end whose start was dropped (no stored call, and a
// drop marker): it is not stored either.
func (s *Store) droppedEnd(ctx context.Context, tx *sql.Tx, en *CallEnd) (bool, error) {
	var one int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM calls WHERE project_id = ? AND id = ?`, en.ProjectID, en.ID).Scan(&one)
	if err == nil {
		return false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM ingest_dropped WHERE project_id = ? AND id = ?`, en.ProjectID, en.ID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func sourceList(sources []string) (string, []any) {
	args := make([]any, len(sources))
	for i, s := range sources {
		args[i] = s
	}
	return strings.TrimSuffix(strings.Repeat("?,", len(sources)), ","), args
}

func (s *Store) sessionHasSource(ctx context.Context, tx *sql.Tx, pid, session string, sources []string) (bool, error) {
	if len(sources) == 0 {
		return false, nil
	}
	ph, srcArgs := sourceList(sources)
	var one int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM calls WHERE project_id = ? AND thread_id = ? AND deleted_at IS NULL
		AND json_extract(attributes, '$.weft.source') IN (`+ph+`) LIMIT 1`, append([]any{pid, session}, srcArgs...)...).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// retireLowerSources soft-deletes a session's spans of the given sources and
// re-links the detections that pointed at them.
func (s *Store) retireLowerSources(ctx context.Context, tx *sql.Tx, pid, session string, sources []string) error {
	if len(sources) == 0 {
		return nil
	}
	ph, srcArgs := sourceList(sources)
	rows, err := tx.QueryContext(ctx, `SELECT id FROM calls WHERE project_id = ? AND thread_id = ? AND deleted_at IS NULL
		AND json_extract(attributes, '$.weft.source') IN (`+ph+`)`, append([]any{pid, session}, srcArgs...)...)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(ids) == 0 {
		return err
	}
	now := nowMicros()
	var dets []string
	for start := 0; start < len(ids); start += 400 {
		chunk := ids[start:min(start+400, len(ids))]
		c, a := inList("id", chunk)
		if _, err := tx.ExecContext(ctx, `UPDATE calls SET deleted_at = ? WHERE project_id = ? AND `+c, append([]any{now, pid}, a...)...); err != nil {
			return err
		}
		c, a = inList("span_id", chunk)
		got, err := detQueryIDs(ctx, tx, `SELECT id FROM detections WHERE project_id = ? AND link <> 'replay' AND `+c, append([]any{pid}, a...)...)
		if err != nil {
			return err
		}
		dets = append(dets, got...)
	}
	for _, d := range dets {
		if _, err := tx.ExecContext(ctx, `UPDATE detections SET span_id = NULL, link = '' WHERE project_id = ? AND id = ?`, pid, d); err != nil {
			return err
		}
		if err := linkDetectionTx(ctx, tx, pid, d); err != nil {
			return err
		}
	}
	return nil
}
