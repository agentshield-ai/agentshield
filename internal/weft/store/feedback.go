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
	Ref           string  `json:"ref"`
	FeedbackType  string  `json:"feedback_type"`
	Payload       any     `json:"payload"`
	Creator       *string `json:"creator"`
	CreatedAt     string  `json:"created_at"`
	WBUserID      *string `json:"wb_user_id"`
	AnnotationRef *string `json:"annotation_ref"`
	RunnableRef   *string `json:"runnable_ref"`
	CallRef       *string `json:"call_ref"`
	TriggerRef    *string `json:"trigger_ref"`
	// QueueID is the annotation queue the feedback was given in (nullable).
	QueueID *string `json:"queue_id"`
}

// FeedbackCreate is the body of /feedback/create.
type FeedbackCreate struct {
	// ID is an optional client-chosen id (UUID); generated when empty.
	ID            string  `json:"-"`
	ProjectID     string  `json:"project_id"`
	Ref           string  `json:"ref"`
	FeedbackType  string  `json:"feedback_type"`
	Payload       any     `json:"payload"`
	Creator       *string `json:"creator"`
	WBUserID      *string `json:"wb_user_id"`
	AnnotationRef *string `json:"annotation_ref"`
	RunnableRef   *string `json:"runnable_ref"`
	CallRef       *string `json:"call_ref"`
	TriggerRef    *string `json:"trigger_ref"`
	// QueueID references annotation_queues.id; nil outside annotation queues.
	QueueID *string `json:"queue_id"`
}

func normRefPtr(p *string) *string {
	if p == nil || *p == "" {
		return nil
	}
	v := *p
	return &v
}

// CreateFeedback stores feedback.
func (s *Store) CreateFeedback(ctx context.Context, f FeedbackCreate) (*Feedback, error) {
	pid, err := refs.NormalizeProject(f.ProjectID)
	if err != nil {
		return nil, &InvalidError{Msg: err.Error()}
	}
	f.Ref = strings.TrimSpace(f.Ref)
	if _, err := refs.Parse(f.Ref); err != nil {
		return nil, Invalidf("ref: %v", err)
	}
	if f.FeedbackType == "" || len(f.FeedbackType) > 256 {
		return nil, Invalidf("feedback_type is required")
	}
	switch f.FeedbackType {
	case "weft.reaction":
		m, _ := f.Payload.(map[string]any)
		if e, _ := m["emoji"].(string); e == "" || len(e) > 64 {
			return nil, Invalidf("reaction payload needs an emoji")
		}
	case "weft.note":
		m, _ := f.Payload.(map[string]any)
		if n, _ := m["note"].(string); n == "" || len(n) > 65536 {
			return nil, Invalidf("note payload needs a note (max 64KiB)")
		}
	}
	if f.Payload == nil {
		f.Payload = map[string]any{}
	}
	payload, err := encodeJSON(f.Payload)
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
		ID: feedbackID(f.ID), ProjectID: pid, Ref: f.Ref, FeedbackType: f.FeedbackType,
		Payload: decodeJSON(payload), Creator: f.Creator, WBUserID: user,
		AnnotationRef: normRefPtr(f.AnnotationRef), RunnableRef: normRefPtr(f.RunnableRef),
		CallRef: normRefPtr(f.CallRef), TriggerRef: normRefPtr(f.TriggerRef),
	}
	if f.QueueID != nil && *f.QueueID != "" {
		if len(*f.QueueID) > 64 {
			return nil, Invalidf("queue_id too long")
		}
		fb.QueueID = f.QueueID
	}
	now := nowMicros()
	fb.CreatedAt = FormatMicros(now)
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		if err := s.touchProject(ctx, tx, pid); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO feedback(id, project_id, ref, feedback_type, payload, creator, created_at, wb_user_id,
			annotation_ref, runnable_ref, call_ref, trigger_ref, queue_id) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			fb.ID, pid, fb.Ref, fb.FeedbackType, payload, nullStr(fb.Creator), now, nullStr(fb.WBUserID),
			nullStr(fb.AnnotationRef), nullStr(fb.RunnableRef), nullStr(fb.CallRef), nullStr(fb.TriggerRef), nullStr(fb.QueueID))
		return err
	})
	if err != nil {
		return nil, err
	}
	s.fireFeedbackCreated(fb)
	return fb, nil
}

var feedbackColumns = map[string]bool{
	"id": true, "ref": true, "feedback_type": true, "creator": true, "wb_user_id": true,
	"annotation_ref": true, "runnable_ref": true, "call_ref": true, "trigger_ref": true, "project_id": true, "queue_id": true,
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

const feedbackSelect = `SELECT f.id, f.project_id, f.ref, f.feedback_type, f.payload, f.creator, f.created_at, f.wb_user_id,
	f.annotation_ref, f.runnable_ref, f.call_ref, f.trigger_ref, f.queue_id FROM feedback f`

func scanFeedback(rows *sql.Rows) (Feedback, error) {
	var f Feedback
	var payload string
	var created int64
	var creator, user, ann, run, call, trig, queue sql.NullString
	if err := rows.Scan(&f.ID, &f.ProjectID, &f.Ref, &f.FeedbackType, &payload, &creator, &created, &user, &ann, &run, &call, &trig, &queue); err != nil {
		return f, err
	}
	f.Payload = decodeJSON(payload)
	f.CreatedAt = FormatMicros(created)
	f.Creator, f.WBUserID, f.AnnotationRef, f.RunnableRef, f.CallRef, f.TriggerRef =
		strPtr(creator), strPtr(user), strPtr(ann), strPtr(run), strPtr(call), strPtr(trig)
	f.QueueID = strPtr(queue)
	return f, nil
}

func feedbackID(given string) string {
	if u, err := uuid.Parse(given); err == nil {
		return u.String()
	}
	return uuid.Must(uuid.NewV7()).String()
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
		q, a := inList("f.ref", refList[start:end])
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
