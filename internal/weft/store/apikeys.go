package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strings"

	"github.com/agentshield-ai/agentshield/internal/weft/refs"
	"github.com/google/uuid"
)

// API keys are stored as SHA-256 hashes; the secret is shown once at creation.

func init() {
	registerSchema(`
CREATE TABLE IF NOT EXISTS api_keys (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	user_id TEXT NOT NULL,
	key_hash TEXT NOT NULL UNIQUE,
	prefix TEXT NOT NULL,
	all_projects INTEGER NOT NULL DEFAULT 1,
	projects TEXT NOT NULL DEFAULT '[]',
	read_only INTEGER NOT NULL DEFAULT 0,
	admin INTEGER NOT NULL DEFAULT 0,
	created_by TEXT,
	created_at INTEGER NOT NULL,
	last_used_at INTEGER,
	revoked_at INTEGER
);`)
}

// APIKey describes a stored key (never the secret).
type APIKey struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	UserID      string   `json:"user_id"`
	Prefix      string   `json:"prefix"`
	AllProjects bool     `json:"all_projects"`
	Projects    []string `json:"projects"`
	ReadOnly    bool     `json:"read_only"`
	Admin       bool     `json:"admin"`
	CreatedBy   *string  `json:"created_by"`
	CreatedAt   string   `json:"created_at"`
	LastUsedAt  *string  `json:"last_used_at"`
	RevokedAt   *string  `json:"revoked_at"`
	// UserBound keys belong to a user in the users table and take their
	// roles from that user (narrowed by Projects and ReadOnly). Other keys
	// keep the flag-based access of earlier versions.
	UserBound bool `json:"user_bound"`

	// Hash is the hex SHA-256 of the secret (internal).
	Hash string `json:"-"`
}

// APIKeyCreate is the input of CreateAPIKey.
type APIKeyCreate struct {
	Name      string   `json:"name"`
	UserID    string   `json:"user_id"`
	Projects  []string `json:"projects"` // empty means all projects
	ReadOnly  bool     `json:"read_only"`
	Admin     bool     `json:"admin"`
	CreatedBy *string  `json:"-"`
	UserBound bool     `json:"-"`
}

var keyUserRe = regexp.MustCompile(`^[A-Za-z0-9_.@-]{1,64}$`)

// HashAPIKey returns the hex SHA-256 of a presented key.
func HashAPIKey(secret string) string {
	h := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(h[:])
}

// CreateAPIKey stores a new key and returns it with its secret.
func (s *Store) CreateAPIKey(ctx context.Context, c APIKeyCreate) (*APIKey, string, error) {
	c.Name = strings.TrimSpace(c.Name)
	if c.Name == "" || len(c.Name) > 128 {
		return nil, "", Invalidf("name is required (max 128 characters)")
	}
	if c.UserID == "" {
		c.UserID = "default"
	}
	if !keyUserRe.MatchString(c.UserID) {
		return nil, "", Invalidf("invalid user_id %q", c.UserID)
	}
	projects := []string{}
	seen := map[string]bool{}
	for _, p := range c.Projects {
		pid, err := refs.NormalizeProject(strings.TrimSpace(p))
		if err != nil {
			return nil, "", &InvalidError{Msg: err.Error()}
		}
		if !seen[pid] {
			seen[pid] = true
			projects = append(projects, pid)
		}
	}
	if c.Admin && len(projects) > 0 {
		return nil, "", Invalidf("admin keys cannot be limited to projects")
	}
	if c.UserBound && c.Admin {
		return nil, "", Invalidf("keys of a user inherit the user's roles; make the user an admin instead of the key")
	}
	if c.Admin && c.ReadOnly {
		return nil, "", Invalidf("admin keys cannot be read-only")
	}
	// 40 hex characters: the length W&B clients expect of an API key.
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return nil, "", err
	}
	secret := hex.EncodeToString(b)
	k := &APIKey{ID: uuid.NewString(), Name: c.Name, UserID: c.UserID, Prefix: secret[:6], AllProjects: len(projects) == 0,
		Projects: projects, ReadOnly: c.ReadOnly, Admin: c.Admin, CreatedBy: c.CreatedBy, Hash: HashAPIKey(secret), UserBound: c.UserBound}
	now := nowMicros()
	k.CreatedAt = FormatMicros(now)
	_, err := s.w.ExecContext(ctx, `INSERT INTO api_keys(id, name, user_id, key_hash, prefix, all_projects, projects, read_only, admin, created_by, created_at, user_bound)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, k.ID, k.Name, k.UserID, k.Hash, k.Prefix, k.AllProjects, mustJSON(projects), k.ReadOnly, k.Admin,
		nullStr(k.CreatedBy), now, k.UserBound)
	if err != nil {
		return nil, "", err
	}
	return k, secret, nil
}

// ListAPIKeys lists keys, newest first. Active-only unless includeRevoked.
func (s *Store) ListAPIKeys(ctx context.Context, includeRevoked bool) ([]APIKey, error) {
	q := `SELECT id, name, user_id, key_hash, prefix, all_projects, projects, read_only, admin, created_by, created_at, last_used_at, revoked_at, user_bound FROM api_keys`
	if !includeRevoked {
		q += ` WHERE revoked_at IS NULL`
	}
	rows, err := s.r.QueryContext(ctx, q+` ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []APIKey{}
	for rows.Next() {
		var k APIKey
		var projects string
		var created int64
		var createdBy sql.NullString
		var used, revoked sql.NullInt64
		if err := rows.Scan(&k.ID, &k.Name, &k.UserID, &k.Hash, &k.Prefix, &k.AllProjects, &projects, &k.ReadOnly, &k.Admin,
			&createdBy, &created, &used, &revoked, &k.UserBound); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(projects), &k.Projects)
		if k.Projects == nil {
			k.Projects = []string{}
		}
		k.CreatedBy = strPtr(createdBy)
		k.CreatedAt = FormatMicros(created)
		k.LastUsedAt = fmtNullMicros(used)
		k.RevokedAt = fmtNullMicros(revoked)
		out = append(out, k)
	}
	return out, rows.Err()
}

// GetAPIKey reads one key by id.
func (s *Store) GetAPIKey(ctx context.Context, id string) (*APIKey, error) {
	ks, err := s.ListAPIKeys(ctx, true)
	if err != nil {
		return nil, err
	}
	for i := range ks {
		if ks[i].ID == id {
			return &ks[i], nil
		}
	}
	return nil, ErrNotFound
}

// RevokeAPIKey marks a key revoked. Revoking twice is a no-op.
func (s *Store) RevokeAPIKey(ctx context.Context, id string) error {
	res, err := s.w.ExecContext(ctx, `UPDATE api_keys SET revoked_at = COALESCE(revoked_at, ?) WHERE id = ?`, nowMicros(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// TouchAPIKey records a use of a key.
func (s *Store) TouchAPIKey(ctx context.Context, id string) error {
	_, err := s.w.ExecContext(ctx, `UPDATE api_keys SET last_used_at = ? WHERE id = ?`, nowMicros(), id)
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}
