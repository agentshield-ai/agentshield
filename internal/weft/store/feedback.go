package store

import (
	"context"
	"database/sql"
	"strings"

	"github.com/agentshield-ai/agentshield/internal/weft/refs"
	"github.com/google/uuid"
)

// Feedback is one piece of feedback attached to a ref.
type Feedback struct {
	ID            string  `json:"id"`
	ProjectID     string  `json:"project_id"`
	WeaveRef      string  `json:"weave_ref"`
	FeedbackType  string  `json:"feedback_type"`
	Payload       any     `json:"payload"`
	Creator       *string `json:"creator"`
	CreatedAt     string  `json:"created_at"`
	WBUserID      *string `json:"wb_user_id"`
	AnnotationRef *string `json:"annotation_ref"`
	RunnableRef   *string `json:"runnable_ref"`
	CallRef       *string `json:"call_ref"`
	TriggerRef    *string `json:"trigger_ref"`
}

// FeedbackCreate is the body of /feedback/create.
type FeedbackCreate struct {
	ProjectID     string  `json:"project_id"`
	WeaveRef      string  `json:"weave_ref"`
	FeedbackType  string  `json:"feedback_type"`
	Payload       any     `json:"payload"`
	Creator       *string `json:"creator"`
	WBUserID      *string `json:"wb_user_id"`
	AnnotationRef *string `json:"annotation_ref"`
	RunnableRef   *string `json:"runnable_ref"`
	CallRef       *string `json:"call_ref"`
	TriggerRef    *string `json:"trigger_ref"`
}

func normRefPtr(p *string) *string {
	if p == nil || *p == "" {
		return nil
	}
	v := refs.Normalize(*p)
	return &v
}

// CreateFeedback stores feedback.
func (s *Store) CreateFeedback(ctx context.Context, f FeedbackCreate) (*Feedback, error) {
	pid, err := refs.NormalizeProject(f.ProjectID)
	if err != nil {
		return nil, &InvalidError{Msg: err.Error()}
	}
	f.WeaveRef = refs.Normalize(strings.TrimSpace(f.WeaveRef))
	if _, err := refs.Parse(f.WeaveRef); err != nil {
		return nil, Invalidf("weave_ref: %v", err)
	}
	if f.FeedbackType == "" || len(f.FeedbackType) > 256 {
		return nil, Invalidf("feedback_type is required")
	}
	switch f.FeedbackType {
	case "wandb.reaction.1":
		m, _ := f.Payload.(map[string]any)
		if e, _ := m["emoji"].(string); e == "" || len(e) > 64 {
			return nil, Invalidf("reaction payload needs an emoji")
		}
	case "wandb.note.1":
		m, _ := f.Payload.(map[string]any)
		if n, _ := m["note"].(string); n == "" || len(n) > 65536 {
			return nil, Invalidf("note payload needs a note (max 64KiB)")
		}
	}
	if f.Payload == nil {
		f.Payload = map[string]any{}
	}
	payload, err := encodeJSON(refs.NormalizeValue(f.Payload))
	if err != nil {
		return nil, Invalidf("payload is not JSON-serialisable")
	}
	if len(payload) > 1<<20 {
		return nil, Invalidf("payload too large")
	}
	user := f.WBUserID
	if user == nil {
		u := "anonymous"
		user = &u
	}
	fb := &Feedback{
		ID: uuid.Must(uuid.NewV7()).String(), ProjectID: pid, WeaveRef: f.WeaveRef, FeedbackType: f.FeedbackType,
		Payload: decodeJSON(payload), Creator: f.Creator, WBUserID: user,
		AnnotationRef: normRefPtr(f.AnnotationRef), RunnableRef: normRefPtr(f.RunnableRef),
		CallRef: normRefPtr(f.CallRef), TriggerRef: normRefPtr(f.TriggerRef),
	}
	now := nowMicros()
	fb.CreatedAt = FormatMicros(now)
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		if err := s.touchProject(ctx, tx, pid); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO feedback(id, project_id, weave_ref, feedback_type, payload, creator, created_at, wb_user_id,
			annotation_ref, runnable_ref, call_ref, trigger_ref) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
			fb.ID, pid, fb.WeaveRef, fb.FeedbackType, payload, nullStr(fb.Creator), now, nullStr(fb.WBUserID),
			nullStr(fb.AnnotationRef), nullStr(fb.RunnableRef), nullStr(fb.CallRef), nullStr(fb.TriggerRef))
		return err
	})
	if err != nil {
		return nil, err
	}
	return fb, nil
}

var feedbackColumns = map[string]bool{
	"id": true, "weave_ref": true, "feedback_type": true, "creator": true, "wb_user_id": true,
	"annotation_ref": true, "runnable_ref": true, "call_ref": true, "trigger_ref": true, "project_id": true,
}

func feedbackFieldResolver(path string) (string, []any, bool, error) {
	if feedbackColumns[path] {
		return "f." + path, nil, false, nil
	}
	if path == "created_at" {
		return "f.created_at", nil, true, nil
	}
	if path == "payload" {
		return "f.payload", nil, false, nil
	}
	segs := splitField(path)
	if len(segs) > 1 && segs[0] == "payload" {
		s, a := jsonField("f.payload", segs[1:])
		return s, a, false, nil
	}
	return "", nil, false, unknownField(path)
}

const feedbackSelect = `SELECT f.id, f.project_id, f.weave_ref, f.feedback_type, f.payload, f.creator, f.created_at, f.wb_user_id,
	f.annotation_ref, f.runnable_ref, f.call_ref, f.trigger_ref FROM feedback f`

func scanFeedback(rows *sql.Rows) (Feedback, error) {
	var f Feedback
	var payload string
	var created int64
	var creator, user, ann, run, call, trig sql.NullString
	if err := rows.Scan(&f.ID, &f.ProjectID, &f.WeaveRef, &f.FeedbackType, &payload, &creator, &created, &user, &ann, &run, &call, &trig); err != nil {
		return f, err
	}
	f.Payload = decodeJSON(payload)
	f.CreatedAt = FormatMicros(created)
	f.Creator, f.WBUserID, f.AnnotationRef, f.RunnableRef, f.CallRef, f.TriggerRef =
		strPtr(creator), strPtr(user), strPtr(ann), strPtr(run), strPtr(call), strPtr(trig)
	return f, nil
}

// QueryFeedback lists feedback for a project.
func (s *Store) QueryFeedback(ctx context.Context, projectID string, query any, sorts []SortBy, limit, offset int) ([]Feedback, error) {
	pid, err := refs.NormalizeProject(projectID)
	if err != nil {
		return nil, &InvalidError{Msg: err.Error()}
	}
	where := "f.project_id = ?"
	args := []any{pid}
	if query != nil {
		qs, qa, err := compileQuery(query, feedbackFieldResolver)
		if err != nil {
			return nil, err
		}
		if qs != "" {
			where += " AND (" + qs + ")"
			args = append(args, qa...)
		}
	}
	order := "f.created_at DESC"
	if len(sorts) > 0 {
		var parts []string
		for _, sb := range sorts {
			dir, err := sortDirection(sb.Direction)
			if err != nil {
				return nil, &InvalidError{Msg: err.Error()}
			}
			e, a, _, err := feedbackFieldResolver(sb.Field)
			if err != nil {
				return nil, err
			}
			parts = append(parts, e+" "+dir)
			args = append(args, a...)
		}
		order = strings.Join(parts, ", ")
	}
	if limit <= 0 || limit > 100000 {
		limit = 10000
	}
	args = append(args, limit, offset)
	rows, err := s.r.QueryContext(ctx, feedbackSelect+" WHERE "+where+" ORDER BY "+order+" LIMIT ? OFFSET ?", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Feedback{}
	for rows.Next() {
		f, err := scanFeedback(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func (s *Store) feedbackForRefs(ctx context.Context, projectID string, refList []string) ([]Feedback, error) {
	var out []Feedback
	for start := 0; start < len(refList); start += 500 {
		end := min(start+500, len(refList))
		q, a := inList("f.weave_ref", refList[start:end])
		rows, err := s.r.QueryContext(ctx, feedbackSelect+" WHERE f.project_id = ? AND "+q+" ORDER BY f.created_at", append([]any{projectID}, a...)...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			f, err := scanFeedback(rows)
			if err != nil {
				rows.Close()
				return nil, err
			}
			out = append(out, f)
		}
		rows.Close()
	}
	return out, nil
}

// PurgeFeedback deletes feedback matching query.
func (s *Store) PurgeFeedback(ctx context.Context, projectID string, query any) error {
	pid, err := refs.NormalizeProject(projectID)
	if err != nil {
		return &InvalidError{Msg: err.Error()}
	}
	qs, qa, err := compileQuery(query, feedbackFieldResolver)
	if err != nil {
		return err
	}
	if qs == "" {
		return Invalidf("purge requires a query")
	}
	_, err = s.w.ExecContext(ctx, "DELETE FROM feedback AS f WHERE f.project_id = ? AND ("+qs+")", append([]any{pid}, qa...)...)
	return err
}

// ReplaceFeedback deletes feedbackID and creates a new entry.
func (s *Store) ReplaceFeedback(ctx context.Context, feedbackID string, f FeedbackCreate) (*Feedback, error) {
	pid, err := refs.NormalizeProject(f.ProjectID)
	if err != nil {
		return nil, &InvalidError{Msg: err.Error()}
	}
	res, err := s.w.ExecContext(ctx, `DELETE FROM feedback WHERE project_id = ? AND id = ?`, pid, feedbackID)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	return s.CreateFeedback(ctx, f)
}

// ---------------------------------------------------------------- secrets

// SetSecret stores a provider secret.
func (s *Store) SetSecret(ctx context.Context, name, value string) error {
	if !secretNameOK(name) {
		return Invalidf("invalid secret name %q (use A-Z, 0-9, _)", name)
	}
	if len(value) > 16384 {
		return Invalidf("secret too long")
	}
	_, err := s.w.ExecContext(ctx, `INSERT INTO secrets(name, value, updated_at) VALUES(?,?,?)
		ON CONFLICT(name) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, name, value, nowMicros())
	return err
}

func secretNameOK(name string) bool {
	if name == "" || len(name) > 128 {
		return false
	}
	for _, r := range name {
		if !(r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_') {
			return false
		}
	}
	return true
}

// GetSecret returns a stored secret.
func (s *Store) GetSecret(ctx context.Context, name string) (string, bool, error) {
	var v string
	err := s.r.QueryRowContext(ctx, `SELECT value FROM secrets WHERE name = ?`, name).Scan(&v)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	return v, err == nil, err
}

// ListSecrets returns stored secret names.
func (s *Store) ListSecrets(ctx context.Context) ([]string, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT name FROM secrets ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// DeleteSecret removes a stored secret.
func (s *Store) DeleteSecret(ctx context.Context, name string) error {
	_, err := s.w.ExecContext(ctx, `DELETE FROM secrets WHERE name = ?`, name)
	return err
}
