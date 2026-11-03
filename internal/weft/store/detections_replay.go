package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
)

// Replay jobs (POST /api/v1/shield/replay) and the recorded tool calls they
// re-evaluate.

func init() {
	registerSchema(`
CREATE TABLE IF NOT EXISTS shield_replay_jobs (
	project_id TEXT NOT NULL,
	id TEXT NOT NULL,
	status TEXT NOT NULL,
	label TEXT NOT NULL DEFAULT '',
	request TEXT NOT NULL DEFAULT '{}',
	result TEXT NOT NULL DEFAULT '{}',
	error TEXT,
	created_by TEXT,
	created_at INTEGER NOT NULL,
	started_at INTEGER,
	finished_at INTEGER,
	PRIMARY KEY (project_id, id)
);
CREATE INDEX IF NOT EXISTS shield_replay_jobs_created ON shield_replay_jobs(project_id, created_at);
`)
}

// Replay job statuses.
const (
	ReplayQueued  = "queued"
	ReplayRunning = "running"
	ReplayDone    = "done"
	ReplayFailed  = "failed"
)

// ReplayJob is a stored replay job. Request and Result are JSON objects owned
// by the shield package.
type ReplayJob struct {
	ProjectID  string          `json:"project_id"`
	ID         string          `json:"job_id"`
	Status     string          `json:"status"`
	Label      string          `json:"label"`
	Request    json.RawMessage `json:"request"`
	Result     json.RawMessage `json:"result"`
	Error      *string         `json:"error"`
	CreatedBy  *string         `json:"created_by"`
	CreatedAt  string          `json:"created_at"`
	StartedAt  *string         `json:"started_at"`
	FinishedAt *string         `json:"finished_at"`
}

// CreateReplayJob records a queued job.
func (s *Store) CreateReplayJob(ctx context.Context, projectID, id, label string, request any, createdBy *string) error {
	req, err := json.Marshal(request)
	if err != nil {
		return err
	}
	_, err = s.w.ExecContext(ctx, `INSERT INTO shield_replay_jobs(project_id, id, status, label, request, created_by, created_at) VALUES(?,?,?,?,?,?,?)`,
		projectID, id, ReplayQueued, label, string(req), nullStr(createdBy), nowMicros())
	return err
}

// UpdateReplayJob sets a job's status, label, result and error. A running
// job gets started_at, a finished or failed one finished_at.
func (s *Store) UpdateReplayJob(ctx context.Context, projectID, id, status, label string, result any, errMsg string) error {
	res := "{}"
	if result != nil {
		b, err := json.Marshal(result)
		if err != nil {
			return err
		}
		res = string(b)
	}
	now := nowMicros()
	var started, finished any
	switch status {
	case ReplayRunning:
		started = now
	case ReplayDone, ReplayFailed:
		finished = now
	}
	_, err := s.w.ExecContext(ctx, `UPDATE shield_replay_jobs SET status = ?, label = CASE WHEN ? <> '' THEN ? ELSE label END, result = ?,
			error = ?, started_at = COALESCE(started_at, ?), finished_at = COALESCE(?, finished_at)
		WHERE project_id = ? AND id = ?`, status, label, label, res, detNullIfEmpty(errMsg), started, finished, projectID, id)
	return err
}

// GetReplayJob reads a job.
func (s *Store) GetReplayJob(ctx context.Context, projectID, id string) (*ReplayJob, error) {
	var j ReplayJob
	var req, res string
	var errMsg, by sql.NullString
	var created int64
	var started, finished sql.NullInt64
	err := s.r.QueryRowContext(ctx, `SELECT project_id, id, status, label, request, result, error, created_by, created_at, started_at, finished_at
		FROM shield_replay_jobs WHERE project_id = ? AND id = ?`, projectID, id).
		Scan(&j.ProjectID, &j.ID, &j.Status, &j.Label, &req, &res, &errMsg, &by, &created, &started, &finished)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	j.Request, j.Result = json.RawMessage(req), json.RawMessage(res)
	j.Error, j.CreatedBy = strPtr(errMsg), strPtr(by)
	j.CreatedAt = FormatMicros(created)
	j.StartedAt, j.FinishedAt = fmtNullMicros(started), fmtNullMicros(finished)
	return &j, nil
}

// ListReplayJobs lists a project's jobs, newest first (results omitted).
func (s *Store) ListReplayJobs(ctx context.Context, projectID string, limit, offset int) ([]ReplayJob, int, error) {
	var total int
	if err := s.r.QueryRowContext(ctx, `SELECT COUNT(*) FROM shield_replay_jobs WHERE project_id = ?`, projectID).Scan(&total); err != nil {
		return nil, 0, err
	}
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := s.r.QueryContext(ctx, `SELECT id FROM shield_replay_jobs WHERE project_id = ? ORDER BY created_at DESC LIMIT ? OFFSET ?`,
		projectID, limit, max(offset, 0))
	if err != nil {
		return nil, 0, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	out := []ReplayJob{}
	for _, id := range ids {
		j, err := s.GetReplayJob(ctx, projectID, id)
		if err != nil {
			return nil, 0, err
		}
		j.Result = json.RawMessage("{}")
		out = append(out, *j)
	}
	return out, total, nil
}

// ToolSpan is a recorded tool call (an execute_tool span) to re-evaluate.
type ToolSpan struct {
	SpanID     string
	TraceID    string
	SessionID  string
	ToolName   string
	ToolCallID string
	AgentName  string
	StartedAt  int64
	// Args are the call's arguments: gen_ai.tool.call.arguments when set,
	// otherwise the call's inputs.
	Args map[string]any
}

// ToolSpanFilter selects tool spans for replay.
type ToolSpanFilter struct {
	SessionIDs []string
	Agent      string
	From, To   int64
	// Limit bounds the number of spans (default and maximum 10000).
	Limit int
}

// ToolSpansForReplay lists recorded tool calls in session and start order.
// Without session ids the filter must give a time range or an agent.
func (s *Store) ToolSpansForReplay(ctx context.Context, projectID string, f ToolSpanFilter) ([]ToolSpan, error) {
	conds := []string{"a.project_id = ?", "a.operation_name = 'execute_tool'", "c.deleted_at IS NULL", "a.conversation_id <> ''"}
	args := []any{projectID}
	if len(f.SessionIDs) > 0 {
		conds = append(conds, "a.conversation_id IN ("+placeholders(len(f.SessionIDs))+")")
		for _, id := range f.SessionIDs {
			args = append(args, id)
		}
	}
	if f.Agent != "" {
		conds = append(conds, "a.agent_name = ?")
		args = append(args, f.Agent)
	}
	if f.From > 0 {
		conds = append(conds, "c.started_at >= ?")
		args = append(args, f.From)
	}
	if f.To > 0 {
		conds = append(conds, "c.started_at < ?")
		args = append(args, f.To)
	}
	limit := f.Limit
	if limit <= 0 || limit > 10000 {
		limit = 10000
	}
	rows, err := s.r.QueryContext(ctx, `SELECT a.id, a.trace_id, a.conversation_id, a.tool_name, a.tool_call_id, a.agent_name,
			COALESCE(c.started_at, 0), c.attributes, c.inputs
		FROM agent_spans a JOIN calls c ON c.project_id = a.project_id AND c.id = a.id
		WHERE `+strings.Join(conds, " AND ")+` ORDER BY a.conversation_id, c.started_at, a.id LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ToolSpan
	for rows.Next() {
		var t ToolSpan
		var attrs, inputs string
		if err := rows.Scan(&t.SpanID, &t.TraceID, &t.SessionID, &t.ToolName, &t.ToolCallID, &t.AgentName, &t.StartedAt, &attrs, &inputs); err != nil {
			return nil, err
		}
		t.Args = toolArgs(decodeMap(attrs), decodeMap(inputs))
		out = append(out, t)
	}
	return out, rows.Err()
}

// toolArgs finds a tool call's arguments in its span attributes or inputs.
func toolArgs(attributes, inputs map[string]any) map[string]any {
	a := newSpanAttrs(attributes)
	for _, k := range []string{"gen_ai.tool.call.arguments", "gen_ai.tool.input", "tool_arguments", "ai.toolCall.args", "ai.toolCall.input", "tool_input"} {
		switch v := a.get(k).(type) {
		case map[string]any:
			return v
		case string:
			var m map[string]any
			if json.Unmarshal([]byte(v), &m) == nil && m != nil {
				return m
			}
		}
	}
	for _, k := range []string{"arguments", "args", "params", "tool_input", "input"} {
		switch v := inputs[k].(type) {
		case map[string]any:
			if len(inputs) <= 2 { // the arguments object, possibly next to self
				return v
			}
		case string:
			var m map[string]any
			if len(inputs) <= 2 && json.Unmarshal([]byte(v), &m) == nil && m != nil {
				return m
			}
		}
	}
	out := map[string]any{}
	for k, v := range inputs {
		if k != "self" {
			out[k] = v
		}
	}
	return out
}

// EngineDetectionsBySpan returns the engine detection linked to each tool
// span of the given sessions (the most severe, then the latest, when a span
// has several).
func (s *Store) EngineDetectionsBySpan(ctx context.Context, projectID string, sessionIDs []string) (map[string]*Detection, error) {
	out := map[string]*Detection{}
	for i := 0; i < len(sessionIDs); i += 500 {
		ids := sessionIDs[i:min(i+500, len(sessionIDs))]
		args := []any{projectID}
		for _, id := range ids {
			args = append(args, id)
		}
		rows, err := s.r.QueryContext(ctx, `SELECT `+detectionCols+` FROM detections d
			WHERE d.project_id = ? AND d.source = 'engine' AND d.span_id IS NOT NULL AND d.session_id IN (`+placeholders(len(ids))+`)
			ORDER BY d.severity_rank, d.created_at`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			d, err := scanDetection(rows)
			if err != nil {
				rows.Close()
				return nil, err
			}
			out[*d.SpanID] = &d // later rows (more severe, then newer) win
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}
