package store

import (
	"context"
	"database/sql"
	"strings"

	"github.com/agentshield-ai/agentshield/internal/weft/refs"
	"github.com/google/uuid"
)

// Dataset lineage (/dataset_sources/*): links from dataset rows to
// the spans (or conversations) they were built from.

func init() {
	registerSchema(`
CREATE TABLE IF NOT EXISTS dataset_source_links (
	id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL,
	dataset_object_id TEXT NOT NULL,
	dataset_digest TEXT NOT NULL,
	row_digest TEXT NOT NULL,
	source_kind TEXT NOT NULL,
	source_id TEXT NOT NULL,
	source_trace_id TEXT NOT NULL,
	link_metadata TEXT,
	added_by TEXT,
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	deleted_at INTEGER,
	UNIQUE (project_id, dataset_object_id, row_digest, source_kind, source_id)
);
CREATE INDEX IF NOT EXISTS dataset_source_links_source ON dataset_source_links(project_id, source_kind, source_id);`)
}

// DatasetSource identifies a provenance source.
type DatasetSource struct {
	SourceID      string `json:"source_id"`
	SourceKind    string `json:"source_kind"`
	SourceTraceID string `json:"source_trace_id"`
}

// DatasetSourceLinkInput is one row and its sources.
type DatasetSourceLinkInput struct {
	RowDigest    string          `json:"row_digest"`
	Sources      []DatasetSource `json:"sources"`
	LinkMetadata map[string]any  `json:"link_metadata"`
}

// DatasetLinkEntry is one result of LinkDatasetSources.
type DatasetLinkEntry struct {
	LinkID  string `json:"link_id"`
	Created *bool  `json:"created,omitempty"`
}

// Source kinds: a span (by id) or a conversation (session id).
const (
	SourceKindSpan         = "span"
	SourceKindConversation = "conversation"
)

func validSourceKind(k string) bool { return k == SourceKindSpan || k == SourceKindConversation }

// LinkDatasetSources upserts links; created reports whether each link is new
// (or was revived from a soft delete).
func (s *Store) LinkDatasetSources(ctx context.Context, projectID, objectID, digest string, links []DatasetSourceLinkInput,
	user *string, withStatus bool) ([]DatasetLinkEntry, error) {
	pid, err := refs.NormalizeProject(projectID)
	if err != nil {
		return nil, &InvalidError{Msg: err.Error()}
	}
	if objectID == "" || digest == "" {
		return nil, Invalidf("dataset_object_id and dataset_digest are required")
	}
	n := 0
	for _, l := range links {
		n += len(l.Sources)
		if l.RowDigest == "" {
			return nil, Invalidf("row_digest is required")
		}
		for _, src := range l.Sources {
			if !validSourceKind(src.SourceKind) || src.SourceID == "" {
				return nil, Invalidf("each source needs source_id and source_kind span or conversation")
			}
		}
	}
	if n > 10000 {
		return nil, Invalidf("too many links (max 10000)")
	}
	out := make([]DatasetLinkEntry, 0, n)
	now := nowMicros()
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		if err := s.touchProject(ctx, tx, pid); err != nil {
			return err
		}
		for _, l := range links {
			var meta any
			if l.LinkMetadata != nil {
				meta = mustJSON(l.LinkMetadata)
			}
			for _, src := range l.Sources {
				var id string
				var deleted sql.NullInt64
				err := tx.QueryRowContext(ctx, `SELECT id, deleted_at FROM dataset_source_links WHERE project_id = ? AND dataset_object_id = ?
					AND row_digest = ? AND source_kind = ? AND source_id = ?`, pid, objectID, l.RowDigest, src.SourceKind, src.SourceID).Scan(&id, &deleted)
				created := false
				switch {
				case err == sql.ErrNoRows:
					id, created = uuid.NewString(), true
					_, err = tx.ExecContext(ctx, `INSERT INTO dataset_source_links(id, project_id, dataset_object_id, dataset_digest, row_digest,
						source_kind, source_id, source_trace_id, link_metadata, added_by, created_at, updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
						id, pid, objectID, digest, l.RowDigest, src.SourceKind, src.SourceID, src.SourceTraceID, meta, nullStr(user), now, now)
				case err == nil:
					created = deleted.Valid
					_, err = tx.ExecContext(ctx, `UPDATE dataset_source_links SET dataset_digest = ?, source_trace_id = ?,
						link_metadata = COALESCE(?, link_metadata), updated_at = ?, deleted_at = NULL WHERE id = ?`,
						digest, src.SourceTraceID, meta, now, id)
				}
				if err != nil {
					return err
				}
				e := DatasetLinkEntry{LinkID: id}
				if withStatus {
					c := created
					e.Created = &c
				}
				out = append(out, e)
			}
		}
		return nil
	})
	return out, err
}

// DatasetSourceLink is one stored link with source details.
type DatasetSourceLink struct {
	ID                string         `json:"id"`
	DatasetObjectID   string         `json:"dataset_object_id"`
	DatasetDigest     string         `json:"dataset_digest"`
	RowDigest         string         `json:"row_digest"`
	SourceKind        string         `json:"source_kind"`
	SourceID          string         `json:"source_id"`
	SourceTraceID     string         `json:"source_trace_id"`
	SourceDisplayName string         `json:"source_display_name"`
	SourceStartedAt   string         `json:"source_started_at"`
	LinkMetadata      map[string]any `json:"link_metadata"`
	AddedBy           *string        `json:"added_by"`
	CreatedAt         string         `json:"created_at"`
	UpdatedAt         string         `json:"updated_at"`
	DeletedAt         *string        `json:"deleted_at"`
}

// DatasetSourcesQuery is the body of /dataset_sources/query.
type DatasetSourcesQuery struct {
	ProjectID       string   `json:"project_id"`
	DatasetObjectID string   `json:"dataset_object_id"`
	RowDigests      []string `json:"row_digests"`
	SourceKinds     []string `json:"source_kinds"`
	IncludeDeleted  bool     `json:"include_deleted"`
	Limit           *int     `json:"limit"`
	Offset          *int     `json:"offset"`
}

const linkSelect = `SELECT l.id, l.dataset_object_id, l.dataset_digest, l.row_digest, l.source_kind, l.source_id, l.source_trace_id,
	l.link_metadata, l.added_by, l.created_at, l.updated_at, l.deleted_at,
	COALESCE(c.display_name, c.op_base_name, l.source_id), COALESCE(c.started_at, l.created_at)
	FROM dataset_source_links l LEFT JOIN calls c ON l.source_kind = 'span' AND c.project_id = l.project_id AND c.id = l.source_id`

func scanLinks(rows *sql.Rows) ([]DatasetSourceLink, error) {
	defer rows.Close()
	out := []DatasetSourceLink{}
	for rows.Next() {
		var l DatasetSourceLink
		var meta, addedBy sql.NullString
		var created, updated, started int64
		var deleted sql.NullInt64
		if err := rows.Scan(&l.ID, &l.DatasetObjectID, &l.DatasetDigest, &l.RowDigest, &l.SourceKind, &l.SourceID, &l.SourceTraceID,
			&meta, &addedBy, &created, &updated, &deleted, &l.SourceDisplayName, &started); err != nil {
			return nil, err
		}
		if meta.Valid {
			l.LinkMetadata = decodeMap(meta.String)
		}
		l.AddedBy = strPtr(addedBy)
		l.CreatedAt, l.UpdatedAt, l.SourceStartedAt = FormatMicros(created), FormatMicros(updated), FormatMicros(started)
		l.DeletedAt = fmtNullMicros(deleted)
		out = append(out, l)
	}
	return out, rows.Err()
}

// QueryDatasetSources lists the sources linked to a dataset.
func (s *Store) QueryDatasetSources(ctx context.Context, q DatasetSourcesQuery) ([]DatasetSourceLink, error) {
	pid, err := refs.NormalizeProject(q.ProjectID)
	if err != nil {
		return nil, &InvalidError{Msg: err.Error()}
	}
	conds := []string{"l.project_id = ?", "l.dataset_object_id = ?"}
	args := []any{pid, q.DatasetObjectID}
	if !q.IncludeDeleted {
		conds = append(conds, "l.deleted_at IS NULL")
	}
	if len(q.RowDigests) > 0 {
		c, a := inList("l.row_digest", q.RowDigests)
		conds, args = append(conds, c), append(args, a...)
	}
	if len(q.SourceKinds) > 0 {
		c, a := inList("l.source_kind", q.SourceKinds)
		conds, args = append(conds, c), append(args, a...)
	}
	limit, offset := 1000, 0
	if q.Limit != nil && *q.Limit > 0 && *q.Limit <= 10000 {
		limit = *q.Limit
	}
	if q.Offset != nil && *q.Offset > 0 {
		offset = *q.Offset
	}
	rows, err := s.r.QueryContext(ctx, linkSelect+" WHERE "+strings.Join(conds, " AND ")+" ORDER BY l.created_at, l.rowid LIMIT ? OFFSET ?",
		append(args, limit, offset)...)
	if err != nil {
		return nil, err
	}
	return scanLinks(rows)
}

// DatasetMembership is one (source, dataset) pair of the reverse query.
type DatasetMembership struct {
	DatasetObjectID      string   `json:"dataset_object_id"`
	FirstSeenAt          string   `json:"first_seen_at"`
	RowDigests           []string `json:"row_digests"`
	RowDigestsTotalCount int      `json:"row_digests_total_count"`
	RowDigestsTruncated  bool     `json:"row_digests_truncated"`
	SourceID             string   `json:"source_id"`
	SourceKind           string   `json:"source_kind"`
	SourceTraceID        string   `json:"source_trace_id"`
}

const maxMembershipRows = 100

// SourceDatasets lists the datasets that contain each source.
func (s *Store) SourceDatasets(ctx context.Context, projectID string, sources []DatasetSource, includeDeleted bool) ([]DatasetMembership, error) {
	pid, err := refs.NormalizeProject(projectID)
	if err != nil {
		return nil, &InvalidError{Msg: err.Error()}
	}
	if len(sources) > 1000 {
		return nil, Invalidf("too many sources (max 1000)")
	}
	out := []DatasetMembership{}
	for _, src := range sources {
		q := `SELECT dataset_object_id, row_digest, created_at FROM dataset_source_links WHERE project_id = ? AND source_kind = ? AND source_id = ?`
		if !includeDeleted {
			q += ` AND deleted_at IS NULL`
		}
		rows, err := s.r.QueryContext(ctx, q+` ORDER BY dataset_object_id, created_at`, pid, src.SourceKind, src.SourceID)
		if err != nil {
			return nil, err
		}
		idx := map[string]*DatasetMembership{}
		var order []string
		for rows.Next() {
			var ds, row string
			var created int64
			if err := rows.Scan(&ds, &row, &created); err != nil {
				rows.Close()
				return nil, err
			}
			m := idx[ds]
			if m == nil {
				m = &DatasetMembership{DatasetObjectID: ds, FirstSeenAt: FormatMicros(created), RowDigests: []string{},
					SourceID: src.SourceID, SourceKind: src.SourceKind, SourceTraceID: src.SourceTraceID}
				idx[ds] = m
				order = append(order, ds)
			}
			m.RowDigestsTotalCount++
			if len(m.RowDigests) < maxMembershipRows {
				m.RowDigests = append(m.RowDigests, row)
			} else {
				m.RowDigestsTruncated = true
			}
		}
		rows.Close()
		for _, ds := range order {
			out = append(out, *idx[ds])
		}
	}
	return out, nil
}

// DeleteDatasetSourceLinks soft-deletes links; deleted is false for links
// that were already deleted or do not exist.
func (s *Store) DeleteDatasetSourceLinks(ctx context.Context, projectID string, ids []string) (map[string]bool, error) {
	pid, err := refs.NormalizeProject(projectID)
	if err != nil {
		return nil, &InvalidError{Msg: err.Error()}
	}
	out := map[string]bool{}
	now := nowMicros()
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		for _, id := range ids {
			r, err := tx.ExecContext(ctx, `UPDATE dataset_source_links SET deleted_at = ?, updated_at = ? WHERE project_id = ? AND id = ? AND deleted_at IS NULL`,
				now, now, pid, id)
			if err != nil {
				return err
			}
			n, _ := r.RowsAffected()
			out[id] = n > 0
		}
		return nil
	})
	return out, err
}
