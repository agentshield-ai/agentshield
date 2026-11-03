package store

import (
	"context"
	"database/sql"
	"regexp"
	"strings"

	"github.com/agentshield-ai/agentshield/internal/weft/refs"
)

// Object version tags (/objs/{id}/versions/{digest}/tags). Aliases
// live in object_aliases (objects.go); this file adds tags and the
// project-wide alias and tag listings.

func init() {
	registerSchema(`
CREATE TABLE IF NOT EXISTS object_tags (
	project_id TEXT NOT NULL,
	object_id TEXT NOT NULL,
	digest TEXT NOT NULL,
	tag TEXT NOT NULL,
	created_at INTEGER NOT NULL,
	PRIMARY KEY (project_id, object_id, digest, tag)
);
CREATE INDEX IF NOT EXISTS object_tags_tag ON object_tags(project_id, tag);`)
}

var tagRe = regexp.MustCompile(`^[A-Za-z0-9_.:/\- ]{1,128}$`)

func cleanTags(tags []string) ([]string, error) {
	if len(tags) > 100 {
		return nil, Invalidf("too many tags (max 100)")
	}
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		t = strings.TrimSpace(t)
		if !tagRe.MatchString(t) {
			return nil, Invalidf("invalid tag %q (letters, digits, space and _ . : / -; max 128)", t)
		}
		out = append(out, t)
	}
	return out, nil
}

// AddTags tags an object version. digest may be a digest, "latest", v<N> or an alias.
func (s *Store) AddTags(ctx context.Context, projectID, objectID, digest string, tags []string) error {
	tags, err := cleanTags(tags)
	if err != nil {
		return err
	}
	o, err := s.ReadObject(ctx, projectID, objectID, digest)
	if err != nil {
		return err
	}
	now := nowMicros()
	return s.withTx(ctx, func(tx *sql.Tx) error {
		for _, t := range tags {
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO object_tags(project_id, object_id, digest, tag, created_at) VALUES(?,?,?,?,?)`,
				o.ProjectID, o.ObjectID, o.Digest, t, now); err != nil {
				return err
			}
		}
		return nil
	})
}

// RemoveTags removes tags from an object version.
func (s *Store) RemoveTags(ctx context.Context, projectID, objectID, digest string, tags []string) error {
	o, err := s.ReadObject(ctx, projectID, objectID, digest)
	if err != nil {
		return err
	}
	return s.withTx(ctx, func(tx *sql.Tx) error {
		for _, t := range tags {
			if _, err := tx.ExecContext(ctx, `DELETE FROM object_tags WHERE project_id = ? AND object_id = ? AND digest = ? AND tag = ?`,
				o.ProjectID, o.ObjectID, o.Digest, strings.TrimSpace(t)); err != nil {
				return err
			}
		}
		return nil
	})
}

// ListTags returns the distinct tags on live object versions of a project.
func (s *Store) ListTags(ctx context.Context, projectID string) ([]string, error) {
	pid, err := refs.NormalizeProject(projectID)
	if err != nil {
		return nil, &InvalidError{Msg: err.Error()}
	}
	return s.distinctStrings(ctx, `SELECT DISTINCT t.tag FROM object_tags t JOIN objects o ON o.project_id = t.project_id
		AND o.object_id = t.object_id AND o.digest = t.digest AND o.deleted_at IS NULL WHERE t.project_id = ? ORDER BY t.tag`, pid)
}

// ListAliases returns the distinct aliases set in a project ("latest" is virtual and not listed).
func (s *Store) ListAliases(ctx context.Context, projectID string) ([]string, error) {
	pid, err := refs.NormalizeProject(projectID)
	if err != nil {
		return nil, &InvalidError{Msg: err.Error()}
	}
	return s.distinctStrings(ctx, `SELECT DISTINCT alias FROM object_aliases WHERE project_id = ? ORDER BY alias`, pid)
}

func (s *Store) distinctStrings(ctx context.Context, q string, args ...any) ([]string, error) {
	rows, err := s.r.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// attachTags fills Obj.Tags.
func (s *Store) attachTags(ctx context.Context, objs []*Obj) error {
	if len(objs) == 0 {
		return nil
	}
	pid := objs[0].ProjectID
	var idList []string
	seen := map[string]bool{}
	for _, o := range objs {
		o.Tags = []string{}
		if !seen[o.ObjectID] {
			seen[o.ObjectID] = true
			idList = append(idList, o.ObjectID)
		}
	}
	tags := map[[2]string][]string{}
	for start := 0; start < len(idList); start += 500 {
		end := min(start+500, len(idList))
		q, a := inList("object_id", idList[start:end])
		rows, err := s.r.QueryContext(ctx, `SELECT object_id, digest, tag FROM object_tags WHERE project_id = ? AND `+q+` ORDER BY tag`,
			append([]any{pid}, a...)...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var oid, d, t string
			if err := rows.Scan(&oid, &d, &t); err != nil {
				rows.Close()
				return err
			}
			tags[[2]string{oid, d}] = append(tags[[2]string{oid, d}], t)
		}
		rows.Close()
	}
	for _, o := range objs {
		if t := tags[[2]string{o.ObjectID, o.Digest}]; t != nil {
			o.Tags = t
		}
	}
	return nil
}
