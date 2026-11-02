package store

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"

	"github.com/agentshield-ai/agentshield/internal/weft/refs"
	"github.com/google/uuid"
)

// Reference buckets ("bring your own bucket" for media references): customer
// S3 or GCS buckets registered for a team (entity) or a single project. URIs
// that point into them (s3://bucket/key, gs://bucket/key) are presigned by
// /files/resolve_url for every project that may use the bucket.

func init() {
	registerSchema(`
CREATE TABLE IF NOT EXISTS reference_buckets (
	id TEXT PRIMARY KEY,
	entity TEXT NOT NULL,
	project_id TEXT NOT NULL DEFAULT '',
	provider TEXT NOT NULL,
	bucket TEXT NOT NULL,
	prefix TEXT NOT NULL DEFAULT '',
	region TEXT NOT NULL DEFAULT '',
	endpoint TEXT NOT NULL DEFAULT '',
	path_style INTEGER,
	secret_name TEXT NOT NULL DEFAULT '',
	test_object_key TEXT NOT NULL DEFAULT '',
	verified_at INTEGER,
	created_at INTEGER NOT NULL,
	created_by TEXT,
	UNIQUE (entity, project_id, provider, bucket, prefix)
);
CREATE INDEX IF NOT EXISTS reference_buckets_lookup ON reference_buckets(provider, bucket);`)
}

// Reference bucket providers.
const (
	BucketS3  = "s3"
	BucketGCS = "gcs"
)

// ReferenceBucket is a registered customer bucket.
type ReferenceBucket struct {
	ID string `json:"id"`
	// Scope is "entity" (every project of the team) or "project".
	Scope     string  `json:"scope"`
	Entity    string  `json:"entity"`
	ProjectID *string `json:"project_id"`
	// Provider is "s3" or "gcs".
	Provider string `json:"provider"`
	Bucket   string `json:"bucket"`
	// Prefix, when set, limits the keys that may be presigned.
	Prefix   string `json:"prefix"`
	Region   string `json:"region"`
	Endpoint string `json:"endpoint"`
	// PathStyle (S3) addresses objects as endpoint/bucket/key.
	PathStyle *bool `json:"path_style"`
	// SecretName names the stored secret (Settings → Secrets) holding the
	// credentials: S3 {"access_key_id","secret_access_key","session_token"},
	// GCS a service-account key file. Empty for S3 uses AWS_* env variables.
	SecretName    string  `json:"secret_name"`
	TestObjectKey string  `json:"test_object_key"`
	VerifiedAt    *string `json:"verified_at"`
	CreatedAt     string  `json:"created_at"`
	CreatedBy     *string `json:"created_by"`
}

// URIScheme is the scheme of URIs into this bucket.
func (b *ReferenceBucket) URIScheme() string {
	if b.Provider == BucketGCS {
		return "gs"
	}
	return "s3"
}

var (
	bucketNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,220}[a-z0-9]$`)
	entityRe     = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)
)

// NormalizeReferenceBucket validates a registration and fills derived fields.
func NormalizeReferenceBucket(b *ReferenceBucket) error {
	b.Provider = strings.ToLower(strings.TrimSpace(b.Provider))
	switch b.Provider {
	case "s3", "aws":
		b.Provider = BucketS3
	case "gcs", "gs", "google", "gcp":
		b.Provider = BucketGCS
	default:
		return Invalidf("provider must be s3 or gcs")
	}
	b.Bucket = strings.TrimSpace(b.Bucket)
	for _, p := range []string{"s3://", "gs://"} {
		b.Bucket = strings.TrimPrefix(b.Bucket, p)
	}
	b.Bucket = strings.TrimRight(b.Bucket, "/")
	if !bucketNameRe.MatchString(b.Bucket) || strings.Contains(b.Bucket, "..") {
		return Invalidf("invalid bucket name %q (give the name only, without s3:// or gs://)", b.Bucket)
	}
	b.Prefix = strings.TrimLeft(strings.TrimSpace(b.Prefix), "/")
	if strings.Contains(b.Prefix, "..") {
		return Invalidf("invalid prefix")
	}
	b.TestObjectKey = strings.TrimLeft(strings.TrimSpace(b.TestObjectKey), "/")
	if b.ProjectID != nil && *b.ProjectID != "" {
		pid, err := refs.NormalizeProject(*b.ProjectID)
		if err != nil {
			return &InvalidError{Msg: err.Error()}
		}
		b.ProjectID = &pid
		b.Entity = strings.SplitN(pid, "/", 2)[0]
		b.Scope = "project"
	} else {
		b.ProjectID = nil
		b.Entity = strings.TrimSpace(b.Entity)
		if !entityRe.MatchString(b.Entity) {
			return Invalidf("entity or project_id is required")
		}
		b.Scope = "entity"
	}
	b.Endpoint = strings.TrimRight(strings.TrimSpace(b.Endpoint), "/")
	if b.Endpoint != "" && !strings.HasPrefix(b.Endpoint, "https://") && !strings.HasPrefix(b.Endpoint, "http://") {
		return Invalidf("endpoint must be an http(s) URL")
	}
	b.Region = strings.TrimSpace(b.Region)
	b.SecretName = strings.TrimSpace(b.SecretName)
	if b.Provider == BucketGCS && b.SecretName == "" {
		return Invalidf("secret_name (a stored secret holding a service-account key) is required for gcs buckets")
	}
	return nil
}

const refBucketSelect = `SELECT id, entity, project_id, provider, bucket, prefix, region, endpoint, path_style, secret_name,
	test_object_key, verified_at, created_at, created_by FROM reference_buckets`

func scanRefBucket(rows interface{ Scan(...any) error }) (*ReferenceBucket, error) {
	var b ReferenceBucket
	var pid string
	var ps sql.NullBool
	var verified sql.NullInt64
	var created int64
	var by sql.NullString
	if err := rows.Scan(&b.ID, &b.Entity, &pid, &b.Provider, &b.Bucket, &b.Prefix, &b.Region, &b.Endpoint, &ps, &b.SecretName,
		&b.TestObjectKey, &verified, &created, &by); err != nil {
		return nil, err
	}
	b.Scope = "entity"
	if pid != "" {
		b.ProjectID, b.Scope = &pid, "project"
	}
	if ps.Valid {
		v := ps.Bool
		b.PathStyle = &v
	}
	b.VerifiedAt = fmtNullMicros(verified)
	b.CreatedAt = FormatMicros(created)
	b.CreatedBy = strPtr(by)
	return &b, nil
}

// CreateReferenceBucket registers a bucket (normalized by the caller).
func (s *Store) CreateReferenceBucket(ctx context.Context, b *ReferenceBucket, verified bool) (*ReferenceBucket, error) {
	if err := NormalizeReferenceBucket(b); err != nil {
		return nil, err
	}
	b.ID = uuid.NewString()
	pid := ""
	if b.ProjectID != nil {
		pid = *b.ProjectID
	}
	now := nowMicros()
	var ver any
	if verified {
		ver = now
	}
	var ps any
	if b.PathStyle != nil {
		ps = *b.PathStyle
	}
	_, err := s.w.ExecContext(ctx, `INSERT INTO reference_buckets(id, entity, project_id, provider, bucket, prefix, region, endpoint,
		path_style, secret_name, test_object_key, verified_at, created_at, created_by) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		b.ID, b.Entity, pid, b.Provider, b.Bucket, b.Prefix, b.Region, b.Endpoint, ps, b.SecretName, b.TestObjectKey, ver, now, nullStr(b.CreatedBy))
	if isUniqueErr(err) {
		return nil, Invalidf("bucket %s://%s is already registered for this %s", b.URIScheme(), b.Bucket, b.Scope)
	}
	if err != nil {
		return nil, err
	}
	return s.GetReferenceBucket(ctx, b.ID)
}

// GetReferenceBucket reads one registration.
func (s *Store) GetReferenceBucket(ctx context.Context, id string) (*ReferenceBucket, error) {
	b, err := scanRefBucket(s.r.QueryRowContext(ctx, refBucketSelect+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return b, err
}

// DeleteReferenceBucket removes a registration.
func (s *Store) DeleteReferenceBucket(ctx context.Context, id string) error {
	r, err := s.w.ExecContext(ctx, `DELETE FROM reference_buckets WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListReferenceBuckets lists registrations: all of them (entity and
// projectID empty), those of a team (entity), or those a project may use
// (its own and its team's).
func (s *Store) ListReferenceBuckets(ctx context.Context, entity, projectID string) ([]*ReferenceBucket, error) {
	q, args := refBucketSelect, []any{}
	switch {
	case projectID != "":
		pid, err := refs.NormalizeProject(projectID)
		if err != nil {
			return nil, &InvalidError{Msg: err.Error()}
		}
		q += ` WHERE project_id = ? OR (project_id = '' AND entity = ?)`
		args = append(args, pid, strings.SplitN(pid, "/", 2)[0])
	case entity != "":
		q += ` WHERE entity = ?`
		args = append(args, entity)
	}
	rows, err := s.r.QueryContext(ctx, q+` ORDER BY entity, project_id, provider, bucket, prefix`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*ReferenceBucket{}
	for rows.Next() {
		b, err := scanRefBucket(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// ReferenceBucketFor returns the registration that lets projectID read key
// in provider/bucket: the project's own registrations first, then the
// team's, with the longest matching prefix. ErrNotFound when none.
func (s *Store) ReferenceBucketFor(ctx context.Context, projectID, provider, bucket, key string) (*ReferenceBucket, error) {
	all, err := s.ListReferenceBuckets(ctx, "", projectID)
	if err != nil {
		return nil, err
	}
	var best *ReferenceBucket
	rank := func(b *ReferenceBucket) int {
		r := len(b.Prefix) * 2
		if b.ProjectID != nil {
			r += 1 << 20
		}
		return r
	}
	for _, b := range all {
		if b.Provider != provider || b.Bucket != bucket || !strings.HasPrefix(key, b.Prefix) {
			continue
		}
		if best == nil || rank(b) > rank(best) {
			best = b
		}
	}
	if best == nil {
		return nil, ErrNotFound
	}
	return best, nil
}
