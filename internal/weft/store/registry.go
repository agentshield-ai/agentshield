package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"sort"

	"github.com/agentshield-ai/agentshield/internal/weft/refs"
)

// Registry: project-level collections ("portfolios") that
// link object versions (models, datasets, prompts, ...) from any project.
// Each link gets the next version index in its collection; aliases point at
// one version each and move when reassigned. "latest" always names the
// newest version and "v<N>" names version N.

func init() {
	registerSchema(`
CREATE TABLE IF NOT EXISTS registry_versions (
	project_id TEXT NOT NULL,
	collection TEXT NOT NULL,
	version_index INTEGER NOT NULL,
	ref TEXT NOT NULL,
	source_project_id TEXT NOT NULL,
	object_id TEXT NOT NULL,
	digest TEXT NOT NULL,
	base_object_class TEXT,
	created_at INTEGER NOT NULL,
	user_id TEXT,
	PRIMARY KEY (project_id, collection, version_index)
);
CREATE UNIQUE INDEX IF NOT EXISTS registry_versions_ref ON registry_versions(project_id, collection, ref);
CREATE TABLE IF NOT EXISTS registry_aliases (
	project_id TEXT NOT NULL,
	collection TEXT NOT NULL,
	alias TEXT NOT NULL,
	version_index INTEGER NOT NULL,
	PRIMARY KEY (project_id, collection, alias)
);
`)
}

// RegistryVersion is one linked object version.
type RegistryVersion struct {
	VersionIndex    int      `json:"version_index"`
	Version         string   `json:"version"`
	Ref             string   `json:"ref"`
	SourceProjectID string   `json:"source_project_id"`
	ObjectID        string   `json:"object_id"`
	Digest          string   `json:"digest"`
	BaseObjectClass *string  `json:"base_object_class"`
	Aliases         []string `json:"aliases"`
	CreatedAt       string   `json:"created_at"`
	UserID          *string  `json:"user_id"`
}

// RegistryCollection is one collection with its versions (newest first).
type RegistryCollection struct {
	Name     string            `json:"name"`
	Versions []RegistryVersion `json:"versions"`
}

var (
	registryNameRe  = regexp.MustCompile(`^[A-Za-z0-9_.\- ]{1,128}$`)
	registryAliasRe = regexp.MustCompile(`^[A-Za-z0-9_.\-:]{1,128}$`)
	versionAliasRe  = regexp.MustCompile(`^v\d+$`)
)

// LinkToRegistry links ref into collection of registry project registryPID
// and returns the version index. Linking a ref that is already in the
// collection returns its existing index (and applies the aliases to it).
func (s *Store) LinkToRegistry(ctx context.Context, ref, registryPID, collection string, aliases []string, userID *string) (int, error) {
	rpid, err := refs.NormalizeProject(registryPID)
	if err != nil {
		return 0, &InvalidError{Msg: err.Error()}
	}
	if !registryNameRe.MatchString(collection) {
		return 0, Invalidf("invalid portfolio_name %q", collection)
	}
	for _, a := range aliases {
		if !registryAliasRe.MatchString(a) {
			return 0, Invalidf("invalid alias %q", a)
		}
		if a == "latest" || versionAliasRe.MatchString(a) {
			return 0, Invalidf("alias %q is reserved", a)
		}
	}
	r, err := refs.Parse(ref)
	if err != nil {
		return 0, &InvalidError{Msg: err.Error()}
	}
	if (r.Kind != refs.KindObject && r.Kind != refs.KindFunction) || len(r.Extra) > 0 {
		return 0, Invalidf("ref must name an object version, got %q", ref)
	}
	if err := CheckRefProject(ctx, ref, r.ProjectID()); err != nil {
		return 0, err
	}
	o, err := s.ReadObject(ctx, r.ProjectID(), r.Name, r.Digest)
	if errors.Is(err, ErrNotFound) {
		return 0, Invalidf("object %q not found", ref)
	}
	if err != nil {
		return 0, err
	}
	canon := o.Ref()
	var version int
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		if err := s.touchProject(ctx, tx, rpid); err != nil {
			return err
		}
		err := tx.QueryRowContext(ctx, `SELECT version_index FROM registry_versions WHERE project_id = ? AND collection = ? AND ref = ?`,
			rpid, collection, canon).Scan(&version)
		if errors.Is(err, sql.ErrNoRows) {
			if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version_index) + 1, 0) FROM registry_versions WHERE project_id = ? AND collection = ?`,
				rpid, collection).Scan(&version); err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO registry_versions(project_id, collection, version_index, ref, source_project_id, object_id, digest,
				base_object_class, created_at, user_id) VALUES(?,?,?,?,?,?,?,?,?,?)`,
				rpid, collection, version, canon, o.ProjectID, o.ObjectID, o.Digest, nullStr(o.BaseObjectClass), nowMicros(), nullStr(userID))
		}
		if err != nil {
			return err
		}
		for _, a := range aliases {
			if _, err := tx.ExecContext(ctx, `INSERT INTO registry_aliases(project_id, collection, alias, version_index) VALUES(?,?,?,?)
				ON CONFLICT(project_id, collection, alias) DO UPDATE SET version_index = excluded.version_index`,
				rpid, collection, a, version); err != nil {
				return err
			}
		}
		return nil
	})
	return version, err
}

// RegistryQuery selects registry versions.
type RegistryQuery struct {
	ProjectID string `json:"project_id"`
	// PortfolioName limits the result to one collection.
	PortfolioName string `json:"portfolio_name"`
	// Alias ("latest", "v3" or a custom alias) or VersionIndex selects one version.
	Alias        string `json:"alias"`
	VersionIndex *int   `json:"version_index"`
}

// QueryRegistry lists collections and their versions (newest first).
func (s *Store) QueryRegistry(ctx context.Context, q RegistryQuery) ([]RegistryCollection, error) {
	rpid, err := refs.NormalizeProject(q.ProjectID)
	if err != nil {
		return nil, &InvalidError{Msg: err.Error()}
	}
	if (q.Alias != "" || q.VersionIndex != nil) && q.PortfolioName == "" {
		return nil, Invalidf("alias and version_index need portfolio_name")
	}
	where, args := "project_id = ?", []any{rpid}
	if q.PortfolioName != "" {
		where += " AND collection = ?"
		args = append(args, q.PortfolioName)
	}
	aliases := map[string]map[int][]string{}
	arows, err := s.r.QueryContext(ctx, `SELECT collection, alias, version_index FROM registry_aliases WHERE `+where+` ORDER BY alias`, args...)
	if err != nil {
		return nil, err
	}
	for arows.Next() {
		var c, a string
		var v int
		if err := arows.Scan(&c, &a, &v); err != nil {
			arows.Close()
			return nil, err
		}
		if aliases[c] == nil {
			aliases[c] = map[int][]string{}
		}
		aliases[c][v] = append(aliases[c][v], a)
	}
	arows.Close()
	rows, err := s.r.QueryContext(ctx, `SELECT collection, version_index, ref, source_project_id, object_id, digest, base_object_class, created_at, user_id
		FROM registry_versions WHERE `+where+` ORDER BY collection, version_index DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RegistryCollection{}
	idx := map[string]int{}
	for rows.Next() {
		var c string
		var v RegistryVersion
		var base, user sql.NullString
		var created int64
		if err := rows.Scan(&c, &v.VersionIndex, &v.Ref, &v.SourceProjectID, &v.ObjectID, &v.Digest, &base, &created, &user); err != nil {
			return nil, err
		}
		v.BaseObjectClass, v.UserID = strPtr(base), strPtr(user)
		v.CreatedAt = FormatMicros(created)
		v.Version = fmt.Sprintf("v%d", v.VersionIndex)
		v.Aliases = append([]string{}, aliases[c][v.VersionIndex]...)
		i, ok := idx[c]
		if !ok {
			v.Aliases = append(v.Aliases, "latest") // rows are newest first
			i = len(out)
			idx[c] = i
			out = append(out, RegistryCollection{Name: c, Versions: []RegistryVersion{}})
		}
		sort.Strings(v.Aliases)
		out[i].Versions = append(out[i].Versions, v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if q.Alias == "" && q.VersionIndex == nil {
		return out, nil
	}
	for ci := range out {
		var keep []RegistryVersion
		for _, v := range out[ci].Versions {
			match := q.VersionIndex != nil && v.VersionIndex == *q.VersionIndex
			if q.Alias != "" && (q.Alias == v.Version || registryHasAlias(v.Aliases, q.Alias)) {
				match = true
			}
			if match {
				keep = append(keep, v)
			}
		}
		if keep == nil {
			return nil, ErrNotFound
		}
		out[ci].Versions = keep
	}
	if len(out) == 0 {
		return nil, ErrNotFound
	}
	return out, nil
}

func registryHasAlias(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
