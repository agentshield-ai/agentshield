package store

// Monitor scope runs: which session, agent and hand-off targets a monitor
// has already scored (or decided by sampling not to score), so that a
// monitor scores each target at most once (internal/weft/evals,
// monitors_scope.go).

import (
	"context"
	"database/sql"
)

// Monitor scope run statuses.
const (
	MonitorTargetScored     = "scored"
	MonitorTargetSampledOut = "sampled_out"
	MonitorTargetFailed     = "failed"
)

func init() {
	registerSchema(`
CREATE TABLE IF NOT EXISTS monitor_scope_runs (
	project_id TEXT NOT NULL,
	monitor_id TEXT NOT NULL,
	scorer TEXT NOT NULL,
	scope TEXT NOT NULL,
	target TEXT NOT NULL,
	session_id TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL,
	error TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL,
	PRIMARY KEY (project_id, monitor_id, scorer, scope, target)
);
CREATE INDEX IF NOT EXISTS monitor_scope_runs_session ON monitor_scope_runs(project_id, session_id);
`)
}

// MonitorTargetKey identifies a scope target: the session id, the session id
// and agent name, or the hand-off id.
func MonitorTargetKey(t ScopeTarget) string {
	switch t.Scope {
	case ScopeAgent:
		return t.SessionID + "\x00" + t.Agent
	case ScopeHandoff:
		return t.HandoffID
	}
	return t.SessionID
}

// ClaimMonitorTarget records that a monitor's scorer is handling a target
// with the given status. It reports false when the target was claimed
// before, in which case the caller must not score it again.
func (s *Store) ClaimMonitorTarget(ctx context.Context, projectID, monitorID, scorer string, t ScopeTarget, status string) (bool, error) {
	r, err := s.w.ExecContext(ctx, `INSERT OR IGNORE INTO monitor_scope_runs(project_id, monitor_id, scorer, scope, target, session_id, status, created_at)
		VALUES(?,?,?,?,?,?,?,?)`, projectID, monitorID, scorer, t.Scope, MonitorTargetKey(t), t.SessionID, status, nowMicros())
	if err != nil {
		return false, err
	}
	n, err := r.RowsAffected()
	return n == 1, err
}

// SetMonitorTargetStatus updates a claimed target's status.
func (s *Store) SetMonitorTargetStatus(ctx context.Context, projectID, monitorID, scorer string, t ScopeTarget, status, errMsg string) error {
	_, err := s.w.ExecContext(ctx, `UPDATE monitor_scope_runs SET status = ?, error = ?
		WHERE project_id = ? AND monitor_id = ? AND scorer = ? AND scope = ? AND target = ?`,
		status, errMsg, projectID, monitorID, scorer, t.Scope, MonitorTargetKey(t))
	return err
}

// MonitorTargetStatus returns a target's recorded status, or "" if none.
func (s *Store) MonitorTargetStatus(ctx context.Context, projectID, monitorID, scorer string, t ScopeTarget) (string, error) {
	var st string
	err := s.r.QueryRowContext(ctx, `SELECT status FROM monitor_scope_runs
		WHERE project_id = ? AND monitor_id = ? AND scorer = ? AND scope = ? AND target = ?`,
		projectID, monitorID, scorer, t.Scope, MonitorTargetKey(t)).Scan(&st)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return st, err
}
