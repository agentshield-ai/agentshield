package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/agentshield-ai/agentshield/internal/weft/refs"
)

// HasTriggeredFeedback reports whether a span already has feedback of
// feedbackType triggered by any version of a monitor (triggerPrefix: the
// monitor's ref up to and including the ':' before its digest). Monitors
// score a span once: a span re-sent, or rebuilt from its event log, is not
// scored (and an LLM judge not paid) again.
func (s *Store) HasTriggeredFeedback(ctx context.Context, projectID, spanID, feedbackType, triggerPrefix string) (bool, error) {
	var one int
	err := s.r.QueryRowContext(ctx, `SELECT 1 FROM feedback WHERE project_id = ? AND ref = ? AND feedback_type = ?
		AND instr(trigger_ref, ?) = 1 LIMIT 1`,
		projectID, refs.SpanRef(projectID, spanID), feedbackType, triggerPrefix).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}
