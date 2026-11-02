package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"

	"github.com/agentshield-ai/agentshield/internal/weft/refs"
	"github.com/google/uuid"
)

// Annotation queues: named lists of calls for human review. Each queue lists
// the AnnotationSpec refs reviewers fill in (scorer_refs, as in Weave), and
// progress is tracked per annotator.

func init() {
	registerSchema(`
CREATE TABLE IF NOT EXISTS annotation_queues (
	id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL,
	name TEXT NOT NULL,
	description TEXT NOT NULL DEFAULT '',
	scorer_refs TEXT NOT NULL DEFAULT '[]',
	created_at INTEGER NOT NULL,
	created_by TEXT NOT NULL DEFAULT '',
	updated_at INTEGER NOT NULL,
	deleted_at INTEGER
);
CREATE INDEX IF NOT EXISTS annotation_queues_project ON annotation_queues(project_id, created_at);
CREATE TABLE IF NOT EXISTS annotation_queue_items (
	id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL,
	queue_id TEXT NOT NULL,
	call_id TEXT NOT NULL,
	call_op_name TEXT NOT NULL DEFAULT '',
	call_trace_id TEXT NOT NULL DEFAULT '',
	call_started_at INTEGER,
	call_ended_at INTEGER,
	display_fields TEXT NOT NULL DEFAULT '[]',
	added_by TEXT,
	created_at INTEGER NOT NULL,
	created_by TEXT NOT NULL DEFAULT '',
	updated_at INTEGER NOT NULL,
	deleted_at INTEGER
);
CREATE INDEX IF NOT EXISTS annotation_queue_items_queue ON annotation_queue_items(queue_id, created_at);
CREATE UNIQUE INDEX IF NOT EXISTS annotation_queue_items_call ON annotation_queue_items(queue_id, call_id) WHERE deleted_at IS NULL;
CREATE TABLE IF NOT EXISTS annotation_queue_progress (
	item_id TEXT NOT NULL,
	queue_id TEXT NOT NULL,
	project_id TEXT NOT NULL,
	annotator_user_id TEXT NOT NULL,
	annotation_state TEXT NOT NULL,
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	PRIMARY KEY (item_id, annotator_user_id)
);
CREATE INDEX IF NOT EXISTS annotation_queue_progress_queue ON annotation_queue_progress(queue_id);
`)
}

// AnnotationQueue is the AnnotationQueueSchema of the Weave API.
type AnnotationQueue struct {
	ID          string   `json:"id"`
	ProjectID   string   `json:"project_id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	ScorerRefs  []string `json:"scorer_refs"`
	CreatedAt   string   `json:"created_at"`
	CreatedBy   string   `json:"created_by"`
	UpdatedAt   string   `json:"updated_at"`
	DeletedAt   *string  `json:"deleted_at"`
}

// AnnotationQueueItem is one call in a queue, with the requesting
// annotator's state.
type AnnotationQueueItem struct {
	ID              string   `json:"id"`
	ProjectID       string   `json:"project_id"`
	QueueID         string   `json:"queue_id"`
	CallID          string   `json:"call_id"`
	CallOpName      string   `json:"call_op_name"`
	CallTraceID     string   `json:"call_trace_id"`
	CallStartedAt   string   `json:"call_started_at"`
	CallEndedAt     *string  `json:"call_ended_at"`
	DisplayFields   []string `json:"display_fields"`
	AddedBy         *string  `json:"added_by"`
	AnnotationState string   `json:"annotation_state"`
	AnnotatorUserID *string  `json:"annotator_user_id"`
	CreatedAt       string   `json:"created_at"`
	CreatedBy       string   `json:"created_by"`
	UpdatedAt       string   `json:"updated_at"`
	DeletedAt       *string  `json:"deleted_at"`
	PositionInQueue *int     `json:"position_in_queue,omitempty"`
}

// AnnotationQueueStat counts items in a queue.
type AnnotationQueueStat struct {
	QueueID        string `json:"queue_id"`
	TotalItems     int    `json:"total_items"`
	CompletedItems int    `json:"completed_items"`
}

// QueueSort is one sort key.
type QueueSort struct {
	Field     string `json:"field"`
	Direction string `json:"direction"`
}

// AnnotationStates are the valid per-annotator item states.
var AnnotationStates = map[string]bool{"unstarted": true, "in_progress": true, "completed": true, "skipped": true}

func queueUser(u *string) string {
	if u == nil || *u == "" {
		return "anonymous"
	}
	return *u
}

func normScorerRefs(in []string) ([]string, error) {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, r := range in {
		r = refs.Normalize(strings.TrimSpace(r))
		if r == "" || seen[r] {
			continue
		}
		if _, err := refs.Parse(r); err != nil {
			return nil, Invalidf("scorer_refs: %v", err)
		}
		seen[r] = true
		out = append(out, r)
	}
	return out, nil
}

const queueSelect = `SELECT id, project_id, name, description, scorer_refs, created_at, created_by, updated_at, deleted_at FROM annotation_queues`

func scanQueue(row interface{ Scan(...any) error }) (*AnnotationQueue, error) {
	var q AnnotationQueue
	var sr string
	var created, updated int64
	var deleted sql.NullInt64
	if err := row.Scan(&q.ID, &q.ProjectID, &q.Name, &q.Description, &sr, &created, &q.CreatedBy, &updated, &deleted); err != nil {
		return nil, err
	}
	q.ScorerRefs = []string{}
	_ = json.Unmarshal([]byte(sr), &q.ScorerRefs)
	q.CreatedAt, q.UpdatedAt, q.DeletedAt = FormatMicros(created), FormatMicros(updated), fmtNullMicros(deleted)
	return &q, nil
}

// CreateAnnotationQueue creates a queue and returns it.
func (s *Store) CreateAnnotationQueue(ctx context.Context, projectID, name, description string, scorerRefs []string, user *string) (*AnnotationQueue, error) {
	pid, err := refs.NormalizeProject(projectID)
	if err != nil {
		return nil, &InvalidError{Msg: err.Error()}
	}
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 256 {
		return nil, Invalidf("name is required (max 256 characters)")
	}
	if len(description) > 65536 {
		return nil, Invalidf("description too long")
	}
	sr, err := normScorerRefs(scorerRefs)
	if err != nil {
		return nil, err
	}
	id := uuid.Must(uuid.NewV7()).String()
	now := nowMicros()
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		if err := s.touchProject(ctx, tx, pid); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO annotation_queues(id, project_id, name, description, scorer_refs, created_at, created_by, updated_at)
			VALUES(?,?,?,?,?,?,?,?)`, id, pid, name, description, mustJSON(sr), now, queueUser(user), now)
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.GetAnnotationQueue(ctx, pid, id, false)
}

// GetAnnotationQueue reads a queue.
func (s *Store) GetAnnotationQueue(ctx context.Context, projectID, id string, includeDeleted bool) (*AnnotationQueue, error) {
	pid, err := refs.NormalizeProject(projectID)
	if err != nil {
		return nil, &InvalidError{Msg: err.Error()}
	}
	q := queueSelect + ` WHERE project_id = ? AND id = ?`
	if !includeDeleted {
		q += ` AND deleted_at IS NULL`
	}
	out, err := scanQueue(s.r.QueryRowContext(ctx, q, pid, id))
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	return out, err
}

// UpdateAnnotationQueue changes the given fields (nil leaves a field as is).
func (s *Store) UpdateAnnotationQueue(ctx context.Context, projectID, id string, name, description *string, scorerRefs []string, setRefs bool) (*AnnotationQueue, error) {
	cur, err := s.GetAnnotationQueue(ctx, projectID, id, false)
	if err != nil {
		return nil, err
	}
	if name != nil {
		n := strings.TrimSpace(*name)
		if n == "" || len(n) > 256 {
			return nil, Invalidf("name is required (max 256 characters)")
		}
		cur.Name = n
	}
	if description != nil {
		cur.Description = *description
	}
	if setRefs {
		sr, err := normScorerRefs(scorerRefs)
		if err != nil {
			return nil, err
		}
		cur.ScorerRefs = sr
	}
	_, err = s.w.ExecContext(ctx, `UPDATE annotation_queues SET name = ?, description = ?, scorer_refs = ?, updated_at = ? WHERE project_id = ? AND id = ?`,
		cur.Name, cur.Description, mustJSON(cur.ScorerRefs), nowMicros(), cur.ProjectID, id)
	if err != nil {
		return nil, err
	}
	return s.GetAnnotationQueue(ctx, cur.ProjectID, id, false)
}

// DeleteAnnotationQueue soft-deletes a queue and returns its final state.
func (s *Store) DeleteAnnotationQueue(ctx context.Context, projectID, id string) (*AnnotationQueue, error) {
	cur, err := s.GetAnnotationQueue(ctx, projectID, id, false)
	if err != nil {
		return nil, err
	}
	now := nowMicros()
	if _, err := s.w.ExecContext(ctx, `UPDATE annotation_queues SET deleted_at = ?, updated_at = ? WHERE project_id = ? AND id = ?`, now, now, cur.ProjectID, id); err != nil {
		return nil, err
	}
	return s.GetAnnotationQueue(ctx, cur.ProjectID, id, true)
}

var queueSortFields = map[string]string{"created_at": "created_at", "updated_at": "updated_at", "name": "name COLLATE NOCASE"}

// QueryAnnotationQueues lists a project's live queues.
func (s *Store) QueryAnnotationQueues(ctx context.Context, projectID, nameFilter string, sorts []QueueSort, limit, offset int) ([]*AnnotationQueue, error) {
	pid, err := refs.NormalizeProject(projectID)
	if err != nil {
		return nil, &InvalidError{Msg: err.Error()}
	}
	where := ` WHERE project_id = ? AND deleted_at IS NULL`
	args := []any{pid}
	if nameFilter != "" {
		where += ` AND instr(lower(name), lower(?)) > 0`
		args = append(args, nameFilter)
	}
	order, err := queueOrder(sorts, queueSortFields, "created_at DESC")
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 10000 {
		limit = 1000
	}
	args = append(args, limit, max(offset, 0))
	rows, err := s.r.QueryContext(ctx, queueSelect+where+" ORDER BY "+order+", id LIMIT ? OFFSET ?", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*AnnotationQueue{}
	for rows.Next() {
		q, err := scanQueue(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, q)
	}
	return out, rows.Err()
}

func queueOrder(sorts []QueueSort, fields map[string]string, def string) (string, error) {
	if len(sorts) == 0 {
		return def, nil
	}
	var parts []string
	for _, sb := range sorts {
		col, ok := fields[sb.Field]
		if !ok {
			return "", Invalidf("cannot sort by %q", sb.Field)
		}
		dir, err := sortDirection(sb.Direction)
		if err != nil {
			return "", &InvalidError{Msg: err.Error()}
		}
		parts = append(parts, col+" "+dir)
	}
	return strings.Join(parts, ", "), nil
}

// AnnotationQueueStats counts total and completed (completed or skipped by
// at least one annotator) items per queue.
func (s *Store) AnnotationQueueStats(ctx context.Context, projectID string, queueIDs []string) ([]AnnotationQueueStat, error) {
	pid, err := refs.NormalizeProject(projectID)
	if err != nil {
		return nil, &InvalidError{Msg: err.Error()}
	}
	out := make([]AnnotationQueueStat, 0, len(queueIDs))
	for _, qid := range queueIDs {
		st := AnnotationQueueStat{QueueID: qid}
		err := s.r.QueryRowContext(ctx, `SELECT COUNT(*),
			COALESCE(SUM(CASE WHEN EXISTS (SELECT 1 FROM annotation_queue_progress p WHERE p.item_id = i.id AND p.annotation_state IN ('completed','skipped')) THEN 1 ELSE 0 END), 0)
			FROM annotation_queue_items i WHERE i.project_id = ? AND i.queue_id = ? AND i.deleted_at IS NULL`, pid, qid).Scan(&st.TotalItems, &st.CompletedItems)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, nil
}

// AddAnnotationQueueItems adds calls to a queue, skipping calls already in it.
func (s *Store) AddAnnotationQueueItems(ctx context.Context, projectID, queueID string, callIDs, displayFields []string, user *string) (added, duplicates int, err error) {
	q, err := s.GetAnnotationQueue(ctx, projectID, queueID, false)
	if err != nil {
		return 0, 0, err
	}
	if len(callIDs) == 0 || len(callIDs) > 10000 {
		return 0, 0, Invalidf("call_ids must list 1 to 10000 calls")
	}
	if displayFields == nil {
		displayFields = []string{}
	}
	df := mustJSON(displayFields)
	type meta struct {
		op, trace string
		started   int64
		ended     sql.NullInt64
	}
	metas := map[string]meta{}
	for _, id := range callIDs {
		var m meta
		var started sql.NullInt64
		err := s.r.QueryRowContext(ctx, `SELECT op_name, trace_id, started_at, ended_at FROM calls WHERE project_id = ? AND id = ? AND deleted_at IS NULL`,
			q.ProjectID, id).Scan(&m.op, &m.trace, &started, &m.ended)
		if err == sql.ErrNoRows {
			return 0, 0, Invalidf("call %q not found", id)
		}
		if err != nil {
			return 0, 0, err
		}
		m.started = started.Int64
		metas[id] = m
	}
	who := queueUser(user)
	now := nowMicros()
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		seen := map[string]bool{}
		for _, id := range callIDs {
			if seen[id] {
				duplicates++
				continue
			}
			seen[id] = true
			var exists int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM annotation_queue_items WHERE queue_id = ? AND call_id = ? AND deleted_at IS NULL`, queueID, id).Scan(&exists); err != nil {
				return err
			}
			if exists > 0 {
				duplicates++
				continue
			}
			m := metas[id]
			var ended any
			if m.ended.Valid {
				ended = m.ended.Int64
			}
			// Distinct created_at values keep insertion order stable for positions.
			now++
			if _, err := tx.ExecContext(ctx, `INSERT INTO annotation_queue_items(id, project_id, queue_id, call_id, call_op_name, call_trace_id,
				call_started_at, call_ended_at, display_fields, added_by, created_at, created_by, updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				uuid.Must(uuid.NewV7()).String(), q.ProjectID, queueID, id, m.op, m.trace, m.started, ended, df, who, now, who, now); err != nil {
				return err
			}
			added++
		}
		_, err := tx.ExecContext(ctx, `UPDATE annotation_queues SET updated_at = ? WHERE id = ?`, now, queueID)
		return err
	})
	return added, duplicates, err
}

// QueueItemsFilter is the items/query filter.
type QueueItemsFilter struct {
	ID               *string  `json:"id"`
	AddedBy          *string  `json:"added_by"`
	AnnotationStates []string `json:"annotation_states"`
	CallID           *string  `json:"call_id"`
	CallOpName       *string  `json:"call_op_name"`
	CallTraceID      *string  `json:"call_trace_id"`
}

var queueItemSortFields = map[string]string{
	"created_at": "i.created_at", "updated_at": "i.updated_at", "call_started_at": "i.call_started_at",
	"call_ended_at": "i.call_ended_at", "call_op_name": "i.call_op_name", "annotation_state": "state",
}

const queueItemSelect = `SELECT i.id, i.project_id, i.queue_id, i.call_id, i.call_op_name, i.call_trace_id, i.call_started_at, i.call_ended_at,
	i.display_fields, i.added_by, i.created_at, i.created_by, i.updated_at, i.deleted_at,
	COALESCE(p.annotation_state, 'unstarted') AS state, p.annotator_user_id,
	(SELECT COUNT(*) FROM annotation_queue_items j WHERE j.queue_id = i.queue_id AND j.deleted_at IS NULL
		AND (j.created_at < i.created_at OR (j.created_at = i.created_at AND j.id <= i.id))) AS pos
	FROM annotation_queue_items i
	LEFT JOIN annotation_queue_progress p ON p.item_id = i.id AND p.annotator_user_id = ?`

func scanQueueItem(row interface{ Scan(...any) error }, withPos bool) (*AnnotationQueueItem, error) {
	var it AnnotationQueueItem
	var started, created, updated int64
	var ended, deleted sql.NullInt64
	var df string
	var added, annotator sql.NullString
	var pos int
	if err := row.Scan(&it.ID, &it.ProjectID, &it.QueueID, &it.CallID, &it.CallOpName, &it.CallTraceID, &started, &ended, &df, &added,
		&created, &it.CreatedBy, &updated, &deleted, &it.AnnotationState, &annotator, &pos); err != nil {
		return nil, err
	}
	it.DisplayFields = []string{}
	_ = json.Unmarshal([]byte(df), &it.DisplayFields)
	it.CallStartedAt, it.CallEndedAt = FormatMicros(started), fmtNullMicros(ended)
	it.CreatedAt, it.UpdatedAt, it.DeletedAt = FormatMicros(created), FormatMicros(updated), fmtNullMicros(deleted)
	it.AddedBy, it.AnnotatorUserID = strPtr(added), strPtr(annotator)
	if withPos {
		it.PositionInQueue = &pos
	}
	return &it, nil
}

// QueryAnnotationQueueItems lists a queue's items with the annotator's state.
func (s *Store) QueryAnnotationQueueItems(ctx context.Context, projectID, queueID string, f *QueueItemsFilter, sorts []QueueSort,
	limit, offset int, includePosition bool, annotator *string) ([]*AnnotationQueueItem, error) {
	q, err := s.GetAnnotationQueue(ctx, projectID, queueID, false)
	if err != nil {
		return nil, err
	}
	args := []any{queueUser(annotator)}
	where := ` WHERE i.project_id = ? AND i.queue_id = ? AND i.deleted_at IS NULL`
	args = append(args, q.ProjectID, queueID)
	if f != nil {
		eq := func(col string, v *string) {
			if v != nil {
				where += " AND " + col + " = ?"
				args = append(args, *v)
			}
		}
		eq("i.id", f.ID)
		eq("i.added_by", f.AddedBy)
		eq("i.call_id", f.CallID)
		eq("i.call_op_name", f.CallOpName)
		eq("i.call_trace_id", f.CallTraceID)
		if len(f.AnnotationStates) > 0 {
			for _, st := range f.AnnotationStates {
				if !AnnotationStates[st] {
					return nil, Invalidf("unknown annotation state %q", st)
				}
			}
			in, a := inList("COALESCE(p.annotation_state, 'unstarted')", f.AnnotationStates)
			where += " AND " + in
			args = append(args, a...)
		}
	}
	order, err := queueOrder(sorts, queueItemSortFields, "i.created_at ASC")
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 10000 {
		limit = 10000
	}
	args = append(args, limit, max(offset, 0))
	rows, err := s.r.QueryContext(ctx, queueItemSelect+where+" ORDER BY "+order+", i.id LIMIT ? OFFSET ?", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*AnnotationQueueItem{}
	for rows.Next() {
		it, err := scanQueueItem(rows, includePosition)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// SetAnnotationQueueItemState records an annotator's progress on an item.
func (s *Store) SetAnnotationQueueItemState(ctx context.Context, projectID, queueID, itemID, state string, annotator *string) (*AnnotationQueueItem, error) {
	if !AnnotationStates[state] {
		return nil, Invalidf("annotation_state must be one of unstarted, in_progress, completed, skipped")
	}
	q, err := s.GetAnnotationQueue(ctx, projectID, queueID, false)
	if err != nil {
		return nil, err
	}
	var n int
	if err := s.r.QueryRowContext(ctx, `SELECT COUNT(*) FROM annotation_queue_items WHERE id = ? AND queue_id = ? AND deleted_at IS NULL`, itemID, queueID).Scan(&n); err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, ErrNotFound
	}
	who := queueUser(annotator)
	now := nowMicros()
	if state == "unstarted" {
		_, err = s.w.ExecContext(ctx, `DELETE FROM annotation_queue_progress WHERE item_id = ? AND annotator_user_id = ?`, itemID, who)
	} else {
		_, err = s.w.ExecContext(ctx, `INSERT INTO annotation_queue_progress(item_id, queue_id, project_id, annotator_user_id, annotation_state, created_at, updated_at)
			VALUES(?,?,?,?,?,?,?) ON CONFLICT(item_id, annotator_user_id) DO UPDATE SET annotation_state = excluded.annotation_state, updated_at = excluded.updated_at`,
			itemID, queueID, q.ProjectID, who, state, now, now)
	}
	if err != nil {
		return nil, err
	}
	if _, err := s.w.ExecContext(ctx, `UPDATE annotation_queue_items SET updated_at = ? WHERE id = ?`, now, itemID); err != nil {
		return nil, err
	}
	items, err := s.QueryAnnotationQueueItems(ctx, q.ProjectID, queueID, &QueueItemsFilter{ID: &itemID}, nil, 1, 0, true, &who)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, ErrNotFound
	}
	return items[0], nil
}
