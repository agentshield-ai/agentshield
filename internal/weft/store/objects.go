package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"

	"github.com/agentshield-ai/agentshield/internal/weft/refs"
)

// Obj is the API representation of an object version.
type Obj struct {
	ProjectID       string   `json:"project_id"`
	ObjectID        string   `json:"object_id"`
	CreatedAt       string   `json:"created_at"`
	DeletedAt       *string  `json:"deleted_at"`
	Digest          string   `json:"digest"`
	VersionIndex    int      `json:"version_index"`
	IsLatest        bool     `json:"is_latest"`
	Kind            string   `json:"kind"`
	BaseObjectClass *string  `json:"base_object_class"`
	LeafObjectClass *string  `json:"leaf_object_class"`
	Val             any      `json:"val"`
	UserID          *string  `json:"user_id"`
	Aliases         []string `json:"aliases"`
	Tags            []string `json:"tags"`
	SizeBytes       int64    `json:"size_bytes"`
	VersionCount    *int     `json:"version_count,omitempty"`

	valRaw string // stored JSON with the publisher's key order
}

// MarshalJSON emits val with its original key order.
func (o *Obj) MarshalJSON() ([]byte, error) {
	type plain Obj
	if o.valRaw == "" || !json.Valid([]byte(o.valRaw)) {
		return json.Marshal((*plain)(o))
	}
	return json.Marshal(struct {
		*plain
		Val json.RawMessage `json:"val"`
	}{(*plain)(o), json.RawMessage(o.valRaw)})
}

// Ref returns the object's ref URI.
func (o *Obj) Ref() string {
	return refs.ObjectRef(o.ProjectID, o.ObjectID, o.Digest, o.Kind == "op")
}

// classHierarchy maps a known class to its base (most general known) class.
var classHierarchy = map[string]string{
	"Dataset":                      "Dataset",
	"Model":                        "Model",
	"LLMStructuredCompletionModel": "Model",
	"Evaluation":                   "Evaluation",
	"Scorer":                       "Scorer",
	"LLMAsAJudgeScorer":            "Scorer",
	"ExactMatchScorer":             "Scorer",
	"ContainsScorer":               "Scorer",
	"RegexScorer":                  "Scorer",
	"JSONValidScorer":              "Scorer",
	"LevenshteinScorer":            "Scorer",
	"RemoteScorer":                 "Scorer",
	"HallucinationFreeScorer":      "Scorer",
	"SummarizationScorer":          "Scorer",
	"RelevanceScorer":              "Scorer",
	"ValidXMLScorer":               "Scorer",
	"JSONSchemaScorer":             "Scorer",
	"OpenAIModerationScorer":       "Scorer",
	"EmbeddingSimilarityScorer":    "Scorer",
	"RuleScorer":                   "Scorer",
	"Automation":                   "Automation",
	"Prompt":                       "Prompt",
	"StringPrompt":                 "Prompt",
	"MessagesPrompt":               "Prompt",
	"Leaderboard":                  "Leaderboard",
	"Monitor":                      "Monitor",
	"AnnotationSpec":               "AnnotationSpec",
	"SavedView":                    "SavedView",
	"Provider":                     "Provider",
	"ProviderModel":                "ProviderModel",
	"Op":                           "Op",
	"Dashboard":                    "Dashboard",
}

// classify returns kind, base class and leaf class of an object value.
// isOp recognises ops in another client's encoding (IngestHooks.IsOp).
func classify(val any, forced string, isOp func(map[string]any) bool) (kind string, base, leaf *string) {
	kind = "object"
	m, _ := val.(map[string]any)
	if m != nil {
		if t, _ := m["_type"].(string); t == "Op" {
			kind = "op"
		}
		if isOp != nil && isOp(m) {
			kind = "op"
		}
	}
	set := func(b, l string) {
		base, leaf = &b, &l
	}
	if forced != "" {
		if b, ok := classHierarchy[forced]; ok {
			set(b, forced)
		} else {
			set(forced, forced)
		}
		return
	}
	if kind == "op" {
		set("Op", "Op")
		return
	}
	if m == nil {
		return
	}
	var chain []string
	if c, ok := m["_class_name"].(string); ok {
		chain = append(chain, c)
	}
	if bs, ok := m["_bases"].([]any); ok {
		for _, b := range bs {
			if s, ok := b.(string); ok {
				chain = append(chain, s)
			}
		}
	}
	if len(chain) == 0 {
		if t, ok := m["_type"].(string); ok {
			chain = append(chain, t)
		}
	}
	for _, c := range chain {
		if b, ok := classHierarchy[c]; ok {
			set(b, c)
			return
		}
	}
	return
}

var objectIDRe = regexp.MustCompile(`^[A-Za-z0-9_.\-]{1,128}$`)

// SanitizeObjectID normalises a free-form name into a valid object id.
func SanitizeObjectID(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '.', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	s := strings.Trim(b.String(), "-")
	if len(s) > 128 {
		s = s[:128]
	}
	if s == "" {
		s = "object"
	}
	return s
}

// CreateObject stores a new object version (or returns the existing one with
// the same digest). Returns digest and version index.
func (s *Store) CreateObject(ctx context.Context, projectID, objectID string, val any, forcedClass string, userID *string) (string, int, error) {
	return s.CreateObjectRaw(ctx, projectID, objectID, val, nil, forcedClass, userID)
}

// prepValue normalises a value and returns it, its digest and the text to
// store. When raw JSON is supplied its key order is preserved in storage;
// the digest always uses canonical (sorted) JSON.
func prepValue(val any, raw []byte) (any, string, string, error) {
	if raw != nil {
		v, err := refs.Decode(raw)
		if err != nil {
			return nil, "", "", err
		}
		d, _, err := refs.Digest(v)
		if err != nil {
			return nil, "", "", err
		}
		var buf bytes.Buffer
		if err := json.Compact(&buf, raw); err != nil {
			return nil, "", "", err
		}
		return v, d, buf.String(), nil
	}
	d, canon, err := refs.Digest(val)
	if err != nil {
		return nil, "", "", err
	}
	return val, d, string(canon), nil
}

// CreateObjectRaw is CreateObject with the value's original JSON bytes.
func (s *Store) CreateObjectRaw(ctx context.Context, projectID, objectID string, val any, raw []byte, forcedClass string, userID *string) (string, int, error) {
	pid, err := refs.NormalizeProject(projectID)
	if err != nil {
		return "", 0, &InvalidError{Msg: err.Error()}
	}
	if !objectIDRe.MatchString(objectID) {
		return "", 0, Invalidf("invalid object_id %q: use letters, digits, '_', '-', '.' (max 128)", objectID)
	}
	val, digest, canon, err := prepValue(val, raw)
	if err != nil {
		return "", 0, Invalidf("val is not JSON-serialisable: %v", err)
	}
	kind, base, leaf := classify(val, forcedClass, s.hooks.IsOp)
	var version int
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		if err := s.touchProject(ctx, tx, pid); err != nil {
			return err
		}
		var existing int
		var deleted sql.NullInt64
		err := tx.QueryRowContext(ctx, `SELECT version_index, deleted_at FROM objects WHERE project_id = ? AND object_id = ? AND digest = ?`,
			pid, objectID, digest).Scan(&existing, &deleted)
		if err == nil {
			version = existing
			if deleted.Valid {
				_, err = tx.ExecContext(ctx, `UPDATE objects SET deleted_at = NULL, created_at = ? WHERE project_id = ? AND object_id = ? AND digest = ?`,
					nowMicros(), pid, objectID, digest)
			}
			return err
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version_index) + 1, 0) FROM objects WHERE project_id = ? AND object_id = ?`,
			pid, objectID).Scan(&version); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO objects(project_id, object_id, digest, version_index, kind, base_object_class, leaf_object_class,
			val, created_at, user_id, size_bytes) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
			pid, objectID, digest, version, kind, nullStr(base), nullStr(leaf), canon, nowMicros(), nullStr(userID), len(canon))
		return err
	})
	return digest, version, err
}

const objSelect = `SELECT o.project_id, o.object_id, o.digest, o.version_index, o.kind, o.base_object_class, o.leaf_object_class,
	o.val, o.created_at, o.deleted_at, o.user_id, o.size_bytes,
	(o.version_index = (SELECT MAX(o2.version_index) FROM objects o2 WHERE o2.project_id = o.project_id AND o2.object_id = o.object_id AND o2.deleted_at IS NULL)) AS is_latest
	FROM objects o`

func scanObj(rows interface{ Scan(...any) error }, withVal bool) (*Obj, error) {
	var o Obj
	var base, leaf, user sql.NullString
	var val string
	var created int64
	var deleted sql.NullInt64
	var latest sql.NullBool
	if err := rows.Scan(&o.ProjectID, &o.ObjectID, &o.Digest, &o.VersionIndex, &o.Kind, &base, &leaf, &val, &created, &deleted, &user, &o.SizeBytes, &latest); err != nil {
		return nil, err
	}
	o.BaseObjectClass, o.LeafObjectClass, o.UserID = strPtr(base), strPtr(leaf), strPtr(user)
	o.CreatedAt = FormatMicros(created)
	o.DeletedAt = fmtNullMicros(deleted)
	o.IsLatest = latest.Valid && latest.Bool
	if withVal {
		o.Val = decodeJSON(val)
		o.valRaw = val
	}
	o.Aliases = []string{}
	o.Tags = []string{}
	return &o, nil
}

var versionRe = regexp.MustCompile(`^v(\d+)$`)

// ReadObject reads one object version. digest may be a digest, "latest", "v<N>" or an alias.
func (s *Store) ReadObject(ctx context.Context, projectID, objectID, digest string) (*Obj, error) {
	pid, err := refs.NormalizeProject(projectID)
	if err != nil {
		return nil, &InvalidError{Msg: err.Error()}
	}
	if digest == "" {
		digest = "latest"
	}
	var row *sql.Row
	switch {
	case digest == "latest":
		row = s.r.QueryRowContext(ctx, objSelect+` WHERE o.project_id = ? AND o.object_id = ? AND o.deleted_at IS NULL ORDER BY o.version_index DESC LIMIT 1`, pid, objectID)
	case versionRe.MatchString(digest):
		n, _ := strconv.Atoi(versionRe.FindStringSubmatch(digest)[1])
		row = s.r.QueryRowContext(ctx, objSelect+` WHERE o.project_id = ? AND o.object_id = ? AND o.version_index = ?`, pid, objectID, n)
	default:
		row = s.r.QueryRowContext(ctx, objSelect+` WHERE o.project_id = ? AND o.object_id = ? AND (o.digest = ? OR o.digest = (
			SELECT a.digest FROM object_aliases a WHERE a.project_id = ? AND a.object_id = ? AND a.alias = ?))
			ORDER BY (o.digest = ?) DESC LIMIT 1`, pid, objectID, digest, pid, objectID, digest, digest)
	}
	o, err := scanObj(row, true)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := s.attachAliases(ctx, []*Obj{o}); err != nil {
		return nil, err
	}
	return o, nil
}

func (s *Store) attachAliases(ctx context.Context, objs []*Obj) error {
	if len(objs) == 0 {
		return nil
	}
	pid := objs[0].ProjectID
	ids := map[string]bool{}
	var idList []string
	for _, o := range objs {
		if !ids[o.ObjectID] {
			ids[o.ObjectID] = true
			idList = append(idList, o.ObjectID)
		}
	}
	aliases := map[[2]string][]string{}
	for start := 0; start < len(idList); start += 500 {
		end := min(start+500, len(idList))
		q, a := inList("object_id", idList[start:end])
		rows, err := s.r.QueryContext(ctx, `SELECT object_id, digest, alias FROM object_aliases WHERE project_id = ? AND `+q+` ORDER BY alias`,
			append([]any{pid}, a...)...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var oid, d, al string
			if err := rows.Scan(&oid, &d, &al); err != nil {
				rows.Close()
				return err
			}
			aliases[[2]string{oid, d}] = append(aliases[[2]string{oid, d}], al)
		}
		rows.Close()
	}
	for _, o := range objs {
		var al []string
		if o.IsLatest {
			al = append(al, "latest")
		}
		al = append(al, aliases[[2]string{o.ObjectID, o.Digest}]...)
		if al == nil {
			al = []string{}
		}
		o.Aliases = al
	}
	return s.attachTags(ctx, objs)
}

// ObjsFilter filters object queries.
type ObjsFilter struct {
	BaseObjectClasses []string `json:"base_object_classes"`
	LeafObjectClasses []string `json:"leaf_object_classes"`
	ObjectIDs         []string `json:"object_ids"`
	IsOp              *bool    `json:"is_op"`
	LatestOnly        bool     `json:"latest_only"`
}

// ObjsQuery is the body of /objs/query.
type ObjsQuery struct {
	ProjectID      string      `json:"project_id"`
	Filter         *ObjsFilter `json:"filter"`
	Limit          int         `json:"limit"`
	Offset         int         `json:"offset"`
	SortBy         []SortBy    `json:"sort_by"`
	MetadataOnly   bool        `json:"metadata_only"`
	IncludeDeleted bool        `json:"include_deleted"`
}

// QueryObjects lists object versions.
func (s *Store) QueryObjects(ctx context.Context, q ObjsQuery) ([]*Obj, error) {
	pid, err := refs.NormalizeProject(q.ProjectID)
	if err != nil {
		return nil, &InvalidError{Msg: err.Error()}
	}
	conds := []string{"o.project_id = ?"}
	args := []any{pid}
	if !q.IncludeDeleted {
		conds = append(conds, "o.deleted_at IS NULL")
	}
	f := q.Filter
	if f == nil {
		f = &ObjsFilter{}
	}
	if len(f.BaseObjectClasses) > 0 {
		c, a := inList("o.base_object_class", f.BaseObjectClasses)
		conds = append(conds, c)
		args = append(args, a...)
	}
	if len(f.LeafObjectClasses) > 0 {
		c, a := inList("o.leaf_object_class", f.LeafObjectClasses)
		conds = append(conds, c)
		args = append(args, a...)
	}
	if len(f.ObjectIDs) > 0 {
		c, a := inList("o.object_id", f.ObjectIDs)
		conds = append(conds, c)
		args = append(args, a...)
	}
	if f.IsOp != nil {
		if *f.IsOp {
			conds = append(conds, "o.kind = 'op'")
		} else {
			conds = append(conds, "o.kind = 'object'")
		}
	}
	if f.LatestOnly {
		conds = append(conds, `o.version_index = (SELECT MAX(o2.version_index) FROM objects o2 WHERE o2.project_id = o.project_id
			AND o2.object_id = o.object_id AND o2.deleted_at IS NULL)`)
	}
	order := "o.created_at DESC, o.object_id, o.version_index DESC"
	if len(q.SortBy) > 0 {
		var parts []string
		for _, sb := range q.SortBy {
			dir, err := sortDirection(sb.Direction)
			if err != nil {
				return nil, &InvalidError{Msg: err.Error()}
			}
			switch sb.Field {
			case "created_at", "object_id", "version_index", "size_bytes", "base_object_class":
				parts = append(parts, "o."+sb.Field+" "+dir)
			default:
				return nil, Invalidf("cannot sort objects by %q", sb.Field)
			}
		}
		order = strings.Join(parts, ", ") + ", o.version_index DESC"
	}
	limit := q.Limit
	if limit <= 0 || limit > 10000 {
		limit = 10000
	}
	args = append(args, limit, q.Offset)
	rows, err := s.r.QueryContext(ctx, objSelect+" WHERE "+strings.Join(conds, " AND ")+" ORDER BY "+order+" LIMIT ? OFFSET ?", args...)
	if err != nil {
		return nil, err
	}
	out := []*Obj{}
	for rows.Next() {
		o, err := scanObj(rows, !q.MetadataOnly)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, o)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if f.LatestOnly && len(out) > 0 {
		counts := map[string]int{}
		crow, err := s.r.QueryContext(ctx, `SELECT object_id, COUNT(*) FROM objects WHERE project_id = ? AND deleted_at IS NULL GROUP BY object_id`, pid)
		if err != nil {
			return nil, err
		}
		for crow.Next() {
			var id string
			var n int
			if err := crow.Scan(&id, &n); err != nil {
				crow.Close()
				return nil, err
			}
			counts[id] = n
		}
		crow.Close()
		for _, o := range out {
			n := counts[o.ObjectID]
			o.VersionCount = &n
		}
	}
	return out, s.attachAliases(ctx, out)
}

// DeleteObject soft-deletes versions (all when digests is empty).
func (s *Store) DeleteObject(ctx context.Context, projectID, objectID string, digests []string) (int64, error) {
	pid, err := refs.NormalizeProject(projectID)
	if err != nil {
		return 0, &InvalidError{Msg: err.Error()}
	}
	var n int64
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		q := `UPDATE objects SET deleted_at = ? WHERE project_id = ? AND object_id = ? AND deleted_at IS NULL`
		args := []any{nowMicros(), pid, objectID}
		if len(digests) > 0 {
			resolved := make([]string, 0, len(digests))
			for _, d := range digests {
				if d == "latest" || versionRe.MatchString(d) {
					o, err := s.ReadObject(ctx, pid, objectID, d)
					if err != nil {
						return err
					}
					d = o.Digest
				}
				resolved = append(resolved, d)
			}
			c, a := inList("digest", resolved)
			q += " AND " + c
			args = append(args, a...)
		}
		res, err := tx.ExecContext(ctx, q, args...)
		if err != nil {
			return err
		}
		n, _ = res.RowsAffected()
		if len(digests) == 0 {
			_, err = tx.ExecContext(ctx, `DELETE FROM object_aliases WHERE project_id = ? AND object_id = ?`, pid, objectID)
		}
		return err
	})
	if err == nil && n == 0 {
		return 0, ErrNotFound
	}
	return n, err
}

var aliasRe = regexp.MustCompile(`^[A-Za-z0-9_.\-]{1,64}$`)

// SetAlias points alias at a version of an object.
func (s *Store) SetAlias(ctx context.Context, projectID, objectID, digest, alias string) error {
	if !aliasRe.MatchString(alias) || alias == "latest" || versionRe.MatchString(alias) {
		return Invalidf("invalid alias %q", alias)
	}
	o, err := s.ReadObject(ctx, projectID, objectID, digest)
	if err != nil {
		return err
	}
	_, err = s.w.ExecContext(ctx, `INSERT INTO object_aliases(project_id, object_id, alias, digest) VALUES(?,?,?,?)
		ON CONFLICT(project_id, object_id, alias) DO UPDATE SET digest = excluded.digest`, o.ProjectID, objectID, alias, o.Digest)
	return err
}

// RemoveAlias removes an alias.
func (s *Store) RemoveAlias(ctx context.Context, projectID, objectID, alias string) error {
	pid, err := refs.NormalizeProject(projectID)
	if err != nil {
		return &InvalidError{Msg: err.Error()}
	}
	_, err = s.w.ExecContext(ctx, `DELETE FROM object_aliases WHERE project_id = ? AND object_id = ? AND alias = ?`, pid, objectID, alias)
	return err
}
