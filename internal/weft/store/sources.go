package store

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"
)

// Session sources: the producers that can record the same agent session,
// richest first, per harness. For Claude Code the weft-claude-code plugin
// reads the transcript (reasoning, tool results); Claude Code's own
// OpenTelemetry export sends traces (beta) and log events. For Codex the
// weft-codex plugin reads the rollout file; Codex's own export sends log
// events, and traces of its internals (ranked last, should they ever be
// mapped onto a session). Gemini CLI has no Weft plugin: its log events
// rank above its traces (separate llm_call / tool_call spans). A user may
// turn on more than one, so
// each session is shown from one source only: the highest-ranked source of
// its harness that has sent spans for it. Spans of a lower source are
// dropped at ingest once a higher one is present, and are soft-deleted when
// a higher one arrives (docs/weft/README.md, "Claude Code without a plugin").
//
// A span's source is its weft.source attribute; spans of any other source
// (or none) are never affected, nor are those of another harness.
const (
	SourceClaudeCodePlugin = "weft-claude-code"
	SourceClaudeCodeTraces = "claude-code-otel-traces"
	SourceClaudeCodeLogs   = "claude-code-otel-logs"
	SourceCodexPlugin      = "weft-codex"
	SourceCodexTraces      = "codex-otel-traces"
	SourceCodexLogs        = "codex-otel-logs"
	SourceGeminiLogs       = "gemini-cli-otel-logs"
	SourceGeminiTraces     = "gemini-cli-otel-traces"
)

type rankedSource struct {
	harness string
	rank    int
}

var sessionSourceRank = map[string]rankedSource{
	SourceClaudeCodePlugin: {"claude-code", 3}, SourceClaudeCodeTraces: {"claude-code", 2}, SourceClaudeCodeLogs: {"claude-code", 1},
	SourceCodexPlugin: {"codex", 3}, SourceCodexLogs: {"codex", 2}, SourceCodexTraces: {"codex", 1},
	SourceGeminiLogs: {"gemini-cli", 2}, SourceGeminiTraces: {"gemini-cli", 1},
}

// SessionSourceRank is the precedence of a span source within its
// harness (0: not ranked).
func SessionSourceRank(source string) int { return sessionSourceRank[source].rank }

// sourcesRanked returns the sources of the same harness ranked above
// (higher) or below source.
func sourcesRanked(source string, higher bool) []string {
	me := sessionSourceRank[source]
	var out []string
	for s, r := range sessionSourceRank {
		if r.harness != me.harness {
			continue
		}
		if (higher && r.rank > me.rank) || (!higher && r.rank < me.rank) {
			out = append(out, s)
		}
	}
	sort.Strings(out)
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

// spanSource is a call's weft.source attribute (nested or flat). Gemini
// CLI's own trace spans (llm_call, tool_call, agent_call, ...; always
// exported alongside its log events) carry none: they are recognised by
// their agent name, so the session built from the log events replaces them.
func spanSource(attributes map[string]any) string {
	a := newSpanAttrs(attributes)
	if s := a.str("weft.source"); s != "" {
		return s
	}
	if a.str("gen_ai.agent.name") == geminiCLIAgent && geminiTraceOps[a.str("gen_ai.operation.name")] {
		return SourceGeminiTraces
	}
	return ""
}

const geminiCLIAgent = "gemini-cli"

// geminiTraceOps are Gemini CLI's span operations (GeminiCliOperation).
var geminiTraceOps = map[string]bool{"llm_call": true, "tool_call": true, "schedule_tool_calls": true, "agent_call": true,
	"user_prompt": true, "system_prompt": true}

// sourceCondition is the SQL condition matching calls of the given sources
// (with their arguments).
func sourceCondition(sources []string) (string, []any) {
	ph, args := sourceList(sources)
	cond := `json_extract(attributes, '$.weft.source') IN (` + ph + `)`
	for _, s := range sources {
		if s == SourceGeminiTraces {
			names := make([]string, 0, len(geminiTraceOps))
			for op := range geminiTraceOps {
				names = append(names, op)
			}
			sort.Strings(names)
			ops, opArgs := sourceList(names)
			cond = `(` + cond + ` OR (json_extract(attributes, '$.weft.source') IS NULL
				AND json_extract(attributes, '$.otel.attributes."gen_ai.agent.name"') = ?
				AND json_extract(attributes, '$.otel.attributes."gen_ai.operation.name"') IN (` + ops + `)))`
			args = append(append(args, geminiCLIAgent), opArgs...)
		}
	}
	return cond, args
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
	if sessionSourceRank[src].rank == 0 {
		return false, nil
	}
	key := [3]string{st.ProjectID, *st.ThreadID, src}
	b, _ := ctx.Value(sourceBatchKey{}).(*sourceBatch)
	if b != nil {
		if d, ok := b.drop[key]; ok {
			return s.markSourceDrop(ctx, tx, st, d)
		}
	}
	drop, err = s.sessionHasSource(ctx, tx, st.ProjectID, *st.ThreadID, sourcesRanked(src, true))
	if err != nil {
		return false, err
	}
	if !drop {
		if err := s.retireLowerSources(ctx, tx, st.ProjectID, *st.ThreadID, sourcesRanked(src, false)); err != nil {
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
	cond, srcArgs := sourceCondition(sources)
	var one int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM calls WHERE project_id = ? AND thread_id = ? AND deleted_at IS NULL
		AND `+cond+` LIMIT 1`, append([]any{pid, session}, srcArgs...)...).Scan(&one)
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
	cond, srcArgs := sourceCondition(sources)
	rows, err := tx.QueryContext(ctx, `SELECT id FROM calls WHERE project_id = ? AND thread_id = ? AND deleted_at IS NULL
		AND `+cond, append([]any{pid, session}, srcArgs...)...)
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
