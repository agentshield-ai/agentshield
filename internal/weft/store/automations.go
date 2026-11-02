package store

import (
	"context"
	"database/sql"

	"github.com/agentshield-ai/agentshield/internal/weft/refs"
	"github.com/google/uuid"
)

// Automation runs: one row per fired (or test-fired) Automation action.

func init() {
	registerSchema(`
CREATE TABLE IF NOT EXISTS automation_runs (
	id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL,
	automation_id TEXT NOT NULL,
	automation_ref TEXT NOT NULL DEFAULT '',
	trigger_type TEXT NOT NULL DEFAULT '',
	action_type TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL,
	test INTEGER NOT NULL DEFAULT 0,
	event TEXT NOT NULL DEFAULT '{}',
	response_status INTEGER,
	response_body TEXT,
	error TEXT,
	duration_ms REAL,
	created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS automation_runs_project ON automation_runs(project_id, created_at);
CREATE INDEX IF NOT EXISTS automation_runs_automation ON automation_runs(project_id, automation_id, created_at);
`)
}

// AutomationRun records one action delivery.
type AutomationRun struct {
	ID             string   `json:"id"`
	ProjectID      string   `json:"project_id"`
	AutomationID   string   `json:"automation_id"`
	AutomationRef  string   `json:"automation_ref"`
	TriggerType    string   `json:"trigger_type"`
	ActionType     string   `json:"action_type"`
	Status         string   `json:"status"` // success | error
	Test           bool     `json:"test"`
	Event          any      `json:"event"`
	ResponseStatus *int     `json:"response_status"`
	ResponseBody   *string  `json:"response_body"`
	Error          *string  `json:"error"`
	DurationMS     *float64 `json:"duration_ms"`
	CreatedAt      string   `json:"created_at"`
}

// RecordAutomationRun stores a run; ID and CreatedAt are filled in.
func (s *Store) RecordAutomationRun(ctx context.Context, r *AutomationRun) error {
	pid, err := refs.NormalizeProject(r.ProjectID)
	if err != nil {
		return &InvalidError{Msg: err.Error()}
	}
	r.ProjectID = pid
	r.ID = uuid.Must(uuid.NewV7()).String()
	now := nowMicros()
	r.CreatedAt = FormatMicros(now)
	ev, err := encodeJSON(r.Event)
	if err != nil {
		ev = "{}"
	}
	test := 0
	if r.Test {
		test = 1
	}
	var status, dur any
	if r.ResponseStatus != nil {
		status = *r.ResponseStatus
	}
	if r.DurationMS != nil {
		dur = *r.DurationMS
	}
	_, err = s.w.ExecContext(ctx, `INSERT INTO automation_runs(id, project_id, automation_id, automation_ref, trigger_type, action_type, status, test,
		event, response_status, response_body, error, duration_ms, created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.ID, pid, r.AutomationID, r.AutomationRef, r.TriggerType, r.ActionType, r.Status, test, ev, status,
		nullStr(r.ResponseBody), nullStr(r.Error), dur, now)
	return err
}

// QueryAutomationRuns lists runs, newest first. automationID filters when set.
func (s *Store) QueryAutomationRuns(ctx context.Context, projectID, automationID string, limit, offset int) ([]AutomationRun, error) {
	pid, err := refs.NormalizeProject(projectID)
	if err != nil {
		return nil, &InvalidError{Msg: err.Error()}
	}
	where := `project_id = ?`
	args := []any{pid}
	if automationID != "" {
		where += ` AND automation_id = ?`
		args = append(args, automationID)
	}
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	args = append(args, limit, max(offset, 0))
	rows, err := s.r.QueryContext(ctx, `SELECT id, project_id, automation_id, automation_ref, trigger_type, action_type, status, test, event,
		response_status, response_body, error, duration_ms, created_at FROM automation_runs WHERE `+where+` ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AutomationRun{}
	for rows.Next() {
		var r AutomationRun
		var test int
		var ev string
		var status sql.NullInt64
		var body, errS sql.NullString
		var dur sql.NullFloat64
		var created int64
		if err := rows.Scan(&r.ID, &r.ProjectID, &r.AutomationID, &r.AutomationRef, &r.TriggerType, &r.ActionType, &r.Status, &test, &ev,
			&status, &body, &errS, &dur, &created); err != nil {
			return nil, err
		}
		r.Test = test == 1
		r.Event = decodeJSON(ev)
		if status.Valid {
			v := int(status.Int64)
			r.ResponseStatus = &v
		}
		if dur.Valid {
			v := dur.Float64
			r.DurationMS = &v
		}
		r.ResponseBody, r.Error = strPtr(body), strPtr(errS)
		r.CreatedAt = FormatMicros(created)
		out = append(out, r)
	}
	return out, rows.Err()
}

// LastAutomationFire returns the time (micros) of the latest non-test run of
// an automation, or 0.
func (s *Store) LastAutomationFire(ctx context.Context, projectID, automationID string) (int64, error) {
	var t sql.NullInt64
	err := s.r.QueryRowContext(ctx, `SELECT MAX(created_at) FROM automation_runs WHERE project_id = ? AND automation_id = ? AND test = 0`,
		projectID, automationID).Scan(&t)
	return t.Int64, err
}

// ErrorRate counts finished calls started at or after sinceUS and how many
// raised. opBaseNames limits the ops; empty means trace roots. Internal
// (scorer, judge) calls are excluded.
func (s *Store) ErrorRate(ctx context.Context, projectID string, opBaseNames []string, sinceUS int64) (total, errors int64, err error) {
	where := `project_id = ? AND deleted_at IS NULL AND ended_at IS NOT NULL AND started_at >= ?
		AND COALESCE(json_extract(attributes, '$.weft.internal'), 0) = 0`
	args := []any{projectID, sinceUS}
	if len(opBaseNames) > 0 {
		in, a := inList("op_base_name", opBaseNames)
		where += " AND " + in
		args = append(args, a...)
	} else {
		where += " AND parent_id IS NULL"
	}
	err = s.r.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(CASE WHEN exception IS NOT NULL THEN 1 ELSE 0 END), 0) FROM calls WHERE `+where, args...).Scan(&total, &errors)
	return total, errors, err
}
