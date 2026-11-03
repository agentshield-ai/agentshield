package store

import (
	"context"
	"strconv"
	"strings"
	"time"
)

// Run status: the outcome the shared status chip filters on, the same on
// Sessions and Traces (docs/weft/guides/filters.md). Values are the
// sessions status filter's: ok, error, failed_step, running and unknown.
//
// A trace's run status is read off its top-level span:
//
//   - error: the top-level span failed. A trace linked to a caller in
//     another trace (an OTel span link or weft.parent_call.id on its
//     top-level span, or a hand-off to its agent recorded in another trace
//     of the session) is a step of that caller's run, so its failure is
//     failed_step instead: a failed sub-agent is a failed step.
//   - running: the top-level span has not ended.
//   - failed_step: it ended without failing, but a span under it failed.
//   - ok: nothing in it failed.
//
// OTLP exports a span when it ends, so while a run is in progress its
// finished steps arrive before its top-level span. A trace whose spans have
// arrived but whose root (the span without a parent) has not is read as
// still running: its root is open. On Traces, "roots only" lists such a
// trace under a provisional root, its earliest-starting span among those
// whose parent has not arrived (provisionalRootSQL), and every top-level
// span of the trace has the trace's provisional run status:
//
//   - running while any span of the trace ended (or, if it has not, started)
//     within OrphanRunTimeout of now;
//   - after that the root is taken to be lost (the process exited before
//     exporting it), and the trace has its arrived spans' outcome:
//     failed_step if any of them failed (the root's own outcome is unknown,
//     so a failure under it is a failed step, as when a run recovers from
//     one), ok otherwise.
//
// When the root arrives the trace is read off it as usual.
//
// An AgentShield verdict record (a trace holding an engine evaluate span and
// no agent activity: no other span with a session id) is not an agent run.
// It takes its session's status: unknown when the session is known only
// from verdicts.
//
// A session's status combines the run statuses of the top-level spans of
// its traces (the traces holding its agent activity): error if any is
// error, else running if any is running, else failed_step if any is, else
// ok. SessionSummary reports failed_step as status ok with FailedStep set.
//
// The calls query field summary.weft.run_status (callsquery.go) is
// runStatusSQL("c"), and buildSessions reads its traces' run statuses with
// the same expression, so both pages always agree.

// OrphanRunTimeout is how long a trace whose root span has not arrived is
// read as running after its latest span; then its run status is its
// arrived spans' outcome. It is the scope monitors' wait for a running
// session at their default idle time (evals: 10 x 60 s), after which a
// session whose root spans never end counts as complete.
const OrphanRunTimeout = 10 * time.Minute

// Run status values beyond the stored session statuses.
const (
	RunStatusOK    = "ok"
	RunStatusError = "error"
	// RunStatusRunning is a top-level span that has not ended.
	RunStatusRunning = "running"
)

// runStatusRank orders run statuses for combining a session's traces.
var runStatusRank = map[string]int{RunStatusOK: 1, SessionStatusFailedStep: 2, RunStatusRunning: 3, RunStatusError: 4}

// combineRunStatuses is a session's status from its traces' run statuses:
// the stored status (error, running or ok) and whether a step failed.
func combineRunStatuses(statuses []string) (status string, failedStep bool) {
	best := 0
	for _, s := range statuses {
		if r := runStatusRank[s]; r > best {
			best = r
		}
	}
	switch best {
	case 0:
		return "", false
	case runStatusRank[RunStatusError]:
		return RunStatusError, false
	case runStatusRank[RunStatusRunning]:
		return RunStatusRunning, false
	case runStatusRank[SessionStatusFailedStep]:
		return RunStatusOK, true
	}
	return RunStatusOK, false
}

// parentCallColOf is parentCallCol for calls alias a.
func parentCallColOf(a, field string) string {
	parts := []string{`json_extract(` + a + `.attributes, '$.weft.parent_call.` + field + `')`,
		`json_extract(` + a + `.attributes, '$.otel.attributes."weft.parent_call.` + field + `"')`,
		`json_extract(` + a + `.attributes, '$."weft.parent_call.` + field + `"')`}
	return `COALESCE(` + strings.Join(parts, ", ") + `, '')`
}

// linkedToCallerSQL is true when calls row a is the top-level span of a
// trace that is a step of a caller in another trace.
func linkedToCallerSQL(a string) string {
	l, s, h := a+"_ln", a+"_as", a+"_ho"
	return `(` + a + `.parent_id IS NULL AND (
		EXISTS (SELECT 1 FROM agent_span_links ` + l + ` WHERE ` + l + `.project_id = ` + a + `.project_id AND ` + l + `.span_id = ` + a + `.id
			AND ` + l + `.linked_trace_id != ` + a + `.trace_id)
		OR ` + parentCallColOf(a, "id") + ` != ''
		OR EXISTS (SELECT 1 FROM agent_spans ` + s + ` JOIN agent_handoffs ` + h + ` ON ` + h + `.project_id = ` + s + `.project_id
				AND ` + h + `.session_id = ` + s + `.conversation_id
			WHERE ` + s + `.project_id = ` + a + `.project_id AND ` + s + `.id = ` + a + `.id AND ` + s + `.conversation_id != ''
				AND ` + s + `.agent_name != '' AND ` + h + `.kind != 'return' AND ` + h + `.to_span_id = ''
				AND ` + h + `.to_agent = ` + s + `.agent_name AND ` + h + `.trace_id != ` + a + `.trace_id)))`
}

// ownRunStatusSQL is the run status of calls row a read off the row alone
// (no verdict-record rule).
func ownRunStatusSQL(a string) string {
	return `(CASE WHEN ` + a + `.exception IS NOT NULL THEN (CASE WHEN ` + linkedToCallerSQL(a) + ` THEN 'failed_step' ELSE 'error' END)
		WHEN ` + a + `.ended_at IS NULL THEN 'running'
		WHEN COALESCE(json_extract(` + a + `.rollup, '$.status_counts.error'), 0) > 0 THEN 'failed_step'
		ELSE 'ok' END)`
}

// nowMicrosSQL is the current time in unix microseconds, fixed for the
// statement.
const nowMicrosSQL = `(CAST(strftime('%s', 'now') AS INTEGER) * 1000000)`

// provisionalRunStatusSQL is the run status of a trace whose root has not
// arrived, for a top-level span a of it (see the package comment above):
// running while a span of the trace arrived within OrphanRunTimeout,
// then failed_step when one of its spans failed, else ok.
func provisionalRunStatusSQL(a string) string {
	x, e := a+"_px", a+"_pe"
	return `(CASE WHEN EXISTS (SELECT 1 FROM calls ` + x + ` WHERE ` + x + `.project_id = ` + a + `.project_id AND ` + x + `.trace_id = ` + a + `.trace_id
			AND ` + x + `.deleted_at IS NULL AND COALESCE(` + x + `.ended_at, ` + x + `.started_at) > ` + nowMicrosSQL + ` - ` + strconv.FormatInt(OrphanRunTimeout.Microseconds(), 10) + `)
		THEN 'running'
		WHEN EXISTS (SELECT 1 FROM calls ` + e + ` WHERE ` + e + `.project_id = ` + a + `.project_id AND ` + e + `.trace_id = ` + a + `.trace_id
			AND ` + e + `.deleted_at IS NULL AND ` + e + `.exception IS NOT NULL) THEN 'failed_step'
		ELSE 'ok' END)`
}

// topRunStatusSQL is the run status of calls row a, a top-level span of its
// trace (topLevelSQL holds): its own when it is the root, the trace's
// provisional run status when the root has not arrived.
func topRunStatusSQL(a string) string {
	return `(CASE WHEN ` + a + `.parent_id IS NULL THEN ` + ownRunStatusSQL(a) + ` ELSE ` + provisionalRunStatusSQL(a) + ` END)`
}

// parentMissingSQL is true when calls row a has a parent that is not stored.
func parentMissingSQL(a string) string {
	rp := a + "_mp"
	return `(` + a + `.parent_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM calls ` + rp + ` WHERE ` + rp + `.project_id = ` + a + `.project_id
		AND ` + rp + `.id = ` + a + `.parent_id AND ` + rp + `.deleted_at IS NULL))`
}

// noRootSQL is true when the trace of calls row a has no root stored.
func noRootSQL(a string) string {
	r0 := a + "_nr"
	return `NOT EXISTS (SELECT 1 FROM calls ` + r0 + ` WHERE ` + r0 + `.project_id = ` + a + `.project_id AND ` + r0 + `.trace_id = ` + a + `.trace_id
			AND ` + r0 + `.parent_id IS NULL AND ` + r0 + `.deleted_at IS NULL)`
}

// provisionalRootSQL is true for the span that stands for its trace's root
// while the root has not arrived: of the spans whose parent has not
// arrived, the one that started first (then the smallest id).
func provisionalRootSQL(a string) string {
	o := a + "_po"
	return `(` + parentMissingSQL(a) + ` AND ` + noRootSQL(a) + `
		AND NOT EXISTS (SELECT 1 FROM calls ` + o + ` WHERE ` + o + `.project_id = ` + a + `.project_id AND ` + o + `.trace_id = ` + a + `.trace_id
			AND ` + o + `.deleted_at IS NULL AND (` + o + `.started_at < ` + a + `.started_at OR (` + o + `.started_at = ` + a + `.started_at AND ` + o + `.id < ` + a + `.id))
			AND ` + parentMissingSQL(o) + `))`
}

// traceRootSQL is true for the row the Traces list shows for a trace
// ("roots only"): its root, or its provisional root while the root has not
// arrived.
func traceRootSQL(a string) string {
	return `(` + a + `.parent_id IS NULL OR ` + provisionalRootSQL(a) + `)`
}

// activitySpanSQL is the condition on agent_spans s (calls sc, meta sm, both
// left-joined by the caller) for a span of agent activity in a session:
// what loadActivitySpans keeps.
func activitySpanSQL(s, sc, sm string) string {
	return s + `.conversation_id != '' AND ` + sc + `.deleted_at IS NULL AND COALESCE(` + sm + `.verdict, 0) = 0
		AND COALESCE(json_extract(` + sc + `.attributes, '$.weft.internal'), 0) = 0`
}

func activityJoin(s, sc, sm string) string {
	return `agent_spans ` + s + ` JOIN calls ` + sc + ` ON ` + sc + `.project_id = ` + s + `.project_id AND ` + sc + `.id = ` + s + `.id
		LEFT JOIN agent_span_meta ` + sm + ` ON ` + sm + `.project_id = ` + s + `.project_id AND ` + sm + `.id = ` + s + `.id`
}

// topLevelSQL is true for a top-level span of its trace: the span without a
// parent, or, while that has not arrived, a span whose parent has not. The
// parent lookup (by primary key) comes first, so a span whose parent is
// stored costs one lookup.
func topLevelSQL(a string) string {
	r0, rp := a+"_r0", a+"_rp"
	return `(` + a + `.parent_id IS NULL OR (
		NOT EXISTS (SELECT 1 FROM calls ` + rp + ` WHERE ` + rp + `.project_id = ` + a + `.project_id AND ` + rp + `.id = ` + a + `.parent_id
			AND ` + rp + `.deleted_at IS NULL)
		AND NOT EXISTS (SELECT 1 FROM calls ` + r0 + ` WHERE ` + r0 + `.project_id = ` + a + `.project_id AND ` + r0 + `.trace_id = ` + a + `.trace_id
			AND ` + r0 + `.parent_id IS NULL AND ` + r0 + `.deleted_at IS NULL)))`
}

// runStatusSQL is summary.weft.run_status of calls row a.
func runStatusSQL(a string) string {
	vm, vs, vsc, vsm := a+"_vm", a+"_vs", a+"_vsc", a+"_vsm"
	verdictRecord := `(EXISTS (SELECT 1 FROM agent_span_meta ` + vm + ` WHERE ` + vm + `.project_id = ` + a + `.project_id
			AND ` + vm + `.trace_id = ` + a + `.trace_id AND ` + vm + `.verdict = 1)
		AND NOT EXISTS (SELECT 1 FROM ` + activityJoin(vs, vsc, vsm) + `
			WHERE ` + vs + `.project_id = ` + a + `.project_id AND ` + vs + `.trace_id = ` + a + `.trace_id AND ` + activitySpanSQL(vs, vsc, vsm) + `))`
	sm, ss := a+"_sm", a+"_ss"
	// CROSS JOIN keeps SQLite on the trace's meta rows instead of scanning
	// every span of the project in conversation order.
	session := `(SELECT MIN(` + ss + `.conversation_id) FROM agent_span_meta ` + sm + ` CROSS JOIN agent_spans ` + ss + `
		WHERE ` + ss + `.project_id = ` + sm + `.project_id AND ` + ss + `.id = ` + sm + `.id
			AND ` + sm + `.project_id = ` + a + `.project_id AND ` + sm + `.trace_id = ` + a + `.trace_id AND ` + sm + `.verdict = 1
			AND ` + ss + `.conversation_id != '')`
	r, as, asc, asm := a+"_t", a+"_ts", a+"_tsc", a+"_tsm"
	sessionStatus := `(SELECT CASE MAX(CASE ` + topRunStatusSQL(r) + ` WHEN 'error' THEN 4 WHEN 'running' THEN 3 WHEN 'failed_step' THEN 2 ELSE 1 END)
			WHEN 4 THEN 'error' WHEN 3 THEN 'running' WHEN 2 THEN 'failed_step' WHEN 1 THEN 'ok' ELSE 'unknown' END
		FROM calls ` + r + ` WHERE ` + r + `.project_id = ` + a + `.project_id AND ` + r + `.deleted_at IS NULL AND ` + topLevelSQL(r) + `
			AND ` + r + `.trace_id IN (SELECT ` + as + `.trace_id FROM ` + activityJoin(as, asc, asm) + `
				WHERE ` + as + `.project_id = ` + a + `.project_id AND ` + as + `.conversation_id = ` + session + ` AND ` + activitySpanSQL(as, asc, asm) + `))`
	return `(CASE WHEN ` + verdictRecord + ` THEN ` + sessionStatus + `
		WHEN ` + parentMissingSQL(a) + ` AND ` + noRootSQL(a) + ` THEN ` + provisionalRunStatusSQL(a) + `
		ELSE ` + ownRunStatusSQL(a) + ` END)`
}

// traceRunStatuses returns the run statuses of the top-level spans of each
// of the given traces (agent-activity traces: no verdict-record rule).
func (s *Store) traceRunStatuses(ctx context.Context, pid string, traceIDs []string) (map[string][]string, error) {
	out := map[string][]string{}
	for start := 0; start < len(traceIDs); start += 400 {
		end := min(start+400, len(traceIDs))
		c, a := inList("r.trace_id", traceIDs[start:end])
		rows, err := s.r.QueryContext(ctx, `SELECT r.trace_id, `+topRunStatusSQL("r")+` FROM calls r
			WHERE r.project_id = ? AND r.deleted_at IS NULL AND `+c+` AND `+topLevelSQL("r"), append([]any{pid}, a...)...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var tid, st string
			if err := rows.Scan(&tid, &st); err != nil {
				rows.Close()
				return nil, err
			}
			out[tid] = append(out[tid], st)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// enrichRunStatus sets summary.weft.run_status on each call
// (include_run_status), computed as the calls query field is.
func (s *Store) enrichRunStatus(ctx context.Context, pid string, calls []*Call) error {
	byID := make(map[string]*Call, len(calls))
	ids := make([]string, 0, len(calls))
	for _, c := range calls {
		byID[c.ID] = c
		ids = append(ids, c.ID)
	}
	for start := 0; start < len(ids); start += 400 {
		end := min(start+400, len(ids))
		cond, args := inList("c.id", ids[start:end])
		rows, err := s.r.QueryContext(ctx, `SELECT c.id, `+runStatusSQL("c")+` FROM calls c WHERE c.project_id = ? AND `+cond, append([]any{pid}, args...)...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id, st string
			if err := rows.Scan(&id, &st); err != nil {
				rows.Close()
				return err
			}
			if c := byID[id]; c != nil {
				if w, ok := c.Summary["weft"].(map[string]any); ok {
					w["run_status"] = st
				}
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
	}
	return nil
}
