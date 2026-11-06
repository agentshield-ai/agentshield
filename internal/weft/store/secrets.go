package store

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/agentshield-ai/agentshield/internal/weft/refs"
	"golang.org/x/crypto/argon2"
)

// Secrets (provider API keys, webhook URLs, scorer credentials, reference
// bucket credentials) are encrypted at rest with AES-256-GCM.
//
// Every secret has a scope, which decides which projects may use it:
//
//   - server: managed by server admins. Used by the built-in provider routes
//     (openai/, anthropic/, ...) of every project and by reference buckets.
//     A project's own objects (custom providers, remote scorers, predict
//     models, OAuth configs, automations) may name a server secret only when
//     the secret lists allowed hosts and the request goes to one of them, or
//     while every principal of the server is a server admin (see
//     llm.Client.ServerSecretsOpen).
//   - team: one team (entity); usable by every project of that team.
//   - project: one project; managed by its members.
//
// A name is resolved from the narrowest scope that has it (project, then
// team, then server). Bucket credentials are a separate kind that only the
// reference-bucket code reads (GetBucketCredential); ResolveSecret never
// returns them.
//
// The encryption key: WEFT_SECRET_KEY (any string), or a random key file
// created next to the database with 0600 permissions. Databases created by
// this version stretch it with Argon2id and a random salt stored in the
// database (secret_key_info); databases that already held secrets keep using
// the key directly (SHA-256 of WEFT_SECRET_KEY) so they still open.

const (
	encPrefix   = "enc:v1:" // AAD "weft-secret" (rows written before scopes)
	encPrefixV2 = "enc:v2:" // AAD binds scope, scope id, name and kind
)

// Secret scopes.
const (
	SecretScopeServer  = "server"
	SecretScopeTeam    = "team"
	SecretScopeProject = "project"
)

// Secret kinds.
const (
	SecretKindSecret = "secret"
	// SecretKindBucket holds reference-bucket credentials; only the
	// reference-bucket code reads it.
	SecretKindBucket = "bucket"
)

// MaxSecretAllowedHosts bounds a secret's allowed-hosts list.
const MaxSecretAllowedHosts = 32

// SecretScopeRank orders scopes from widest (server, 1) to narrowest
// (project, 3); 0 for an unknown scope.
func SecretScopeRank(scope string) int {
	switch scope {
	case SecretScopeServer:
		return 1
	case SecretScopeTeam:
		return 2
	case SecretScopeProject:
		return 3
	}
	return 0
}

func init() {
	registerSchema(`
CREATE TABLE IF NOT EXISTS secret_key_info (
	id INTEGER PRIMARY KEY CHECK (id = 1),
	kdf TEXT NOT NULL,
	salt TEXT NOT NULL DEFAULT ''
);`)
	registerMigration(migrateSecrets)
}

// migrateSecrets gives the secrets table scopes (once) and records how the
// encryption key is derived (once). Existing secrets become server secrets;
// those named by a reference bucket become bucket credentials.
func migrateSecrets(db *sql.DB) error {
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM secret_key_info`).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		var rows int
		if err := db.QueryRow(`SELECT count(*) FROM secrets`).Scan(&rows); err != nil {
			return err
		}
		kdf, salt := kdfArgon2, ""
		if rows > 0 {
			// Secrets were encrypted with the key used directly; keep it.
			kdf = kdfDirect
		} else {
			b := make([]byte, 16)
			if _, err := rand.Read(b); err != nil {
				return err
			}
			salt = base64.StdEncoding.EncodeToString(b)
		}
		if _, err := db.Exec(`INSERT INTO secret_key_info(id, kdf, salt) VALUES(1, ?, ?)`, kdf, salt); err != nil {
			return err
		}
	}
	ok, err := HasColumn(db, "secrets", "scope")
	if err != nil || ok {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	stmts := []string{
		`CREATE TABLE secrets_scoped (
	scope TEXT NOT NULL DEFAULT 'server',
	scope_id TEXT NOT NULL DEFAULT '',
	name TEXT NOT NULL,
	value TEXT NOT NULL,
	kind TEXT NOT NULL DEFAULT 'secret',
	allowed_hosts TEXT NOT NULL DEFAULT '',
	created_by TEXT,
	updated_at INTEGER NOT NULL,
	PRIMARY KEY (scope, scope_id, name)
)`,
		`INSERT INTO secrets_scoped(scope, scope_id, name, value, kind, updated_at)
	SELECT 'server', '', name, value,
		CASE WHEN name IN (SELECT secret_name FROM reference_buckets WHERE secret_name != '') THEN 'bucket' ELSE 'secret' END,
		updated_at FROM secrets`,
		`DROP TABLE secrets`,
		`ALTER TABLE secrets_scoped RENAME TO secrets`,
		`CREATE INDEX IF NOT EXISTS secrets_name ON secrets(name)`,
	}
	for _, q := range stmts {
		if _, err := tx.Exec(q); err != nil {
			return fmt.Errorf("migrating secrets: %w", err)
		}
	}
	return tx.Commit()
}

const (
	kdfDirect  = "direct"
	kdfArgon2  = "argon2id-v1" // t=3, m=64 MiB, p=4
	aadV1      = "weft-secret"
	secretsMax = 16384
)

// SecretKeyFile returns the default key file path for a database.
func SecretKeyFile(dbPath string) string { return dbPath + ".key" }

// LoadSecretKey resolves the base key: SHA-256 of the passphrase when one is
// given, else the key file (created when missing). SetSecretKey stretches it
// with the database's stored salt where the database asks for that.
func LoadSecretKey(passphrase, keyFile string) ([]byte, error) {
	if passphrase != "" {
		k := sha256.Sum256([]byte(passphrase))
		return k[:], nil
	}
	if b, err := os.ReadFile(keyFile); err == nil {
		k, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
		if err != nil || len(k) != 32 {
			return nil, fmt.Errorf("secret key file %s is invalid", keyFile)
		}
		return k, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("reading secret key file: %w", err)
	}
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(keyFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("creating secret key file: %w", err)
	}
	defer f.Close()
	if _, err := f.WriteString(base64.StdEncoding.EncodeToString(k) + "\n"); err != nil {
		return nil, err
	}
	return k, nil
}

// secretCipher is a store's secret encryption state. The AEAD key is derived
// on first use (Argon2id is deliberately slow), so stores that never touch a
// secret do not pay for it.
type secretCipher struct {
	once sync.Once
	base []byte
	kdf  string
	salt []byte
	aead cipher.AEAD
	err  error
}

var secretCiphers sync.Map // *Store -> *secretCipher

// derivedSecretKeys caches stretched keys per (base key, salt) in this process.
var derivedSecretKeys sync.Map

func (c *secretCipher) get() (cipher.AEAD, error) {
	c.once.Do(func() {
		key := c.base
		if c.kdf == kdfArgon2 {
			id := sha256.Sum256(append(append([]byte{}, c.base...), c.salt...))
			if k, ok := derivedSecretKeys.Load(id); ok {
				key = k.([]byte)
			} else {
				key = argon2.IDKey(c.base, c.salt, 3, 64*1024, 4, 32)
				derivedSecretKeys.Store(id, key)
			}
		}
		block, err := aes.NewCipher(key)
		if err != nil {
			c.err = err
			return
		}
		c.aead, c.err = cipher.NewGCM(block)
	})
	return c.aead, c.err
}

// SetSecretKey installs the base encryption key (32 bytes, from
// LoadSecretKey) and re-encrypts legacy plaintext secrets and rows written
// before scopes (enc:v1) that the key can read.
func (s *Store) SetSecretKey(ctx context.Context, key []byte) error {
	if len(key) != 32 {
		return errors.New("secret key must be 32 bytes")
	}
	c := &secretCipher{base: append([]byte{}, key...), kdf: kdfDirect}
	var kdf, salt string
	err := s.w.QueryRowContext(ctx, `SELECT kdf, salt FROM secret_key_info WHERE id = 1`).Scan(&kdf, &salt)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return err
	default:
		switch kdf {
		case kdfDirect:
		case kdfArgon2:
			c.kdf = kdfArgon2
			if c.salt, err = base64.StdEncoding.DecodeString(salt); err != nil || len(c.salt) < 16 {
				return errors.New("secret_key_info has an invalid salt")
			}
		default:
			return fmt.Errorf("secrets are encrypted with an unknown key derivation %q (written by a newer Weft?)", kdf)
		}
	}
	secretCiphers.Store(s, c)
	rows, err := s.w.QueryContext(ctx, `SELECT rowid, scope, scope_id, name, kind, value FROM secrets WHERE value NOT LIKE 'enc:v2:%'`)
	if err != nil {
		return err
	}
	type row struct {
		id                           int64
		scope, scopeID, name, kind, v string
	}
	var legacy []row
	for rows.Next() {
		var x row
		if err := rows.Scan(&x.id, &x.scope, &x.scopeID, &x.name, &x.kind, &x.v); err != nil {
			rows.Close()
			return err
		}
		legacy = append(legacy, x)
	}
	rows.Close()
	for _, x := range legacy {
		plain, err := s.decryptRow(x.v, x.scope, x.scopeID, x.name, x.kind)
		if err != nil {
			continue // wrong key: leave it; reading it reports the error
		}
		enc, err := s.encryptRow(plain, x.scope, x.scopeID, x.name, x.kind)
		if err != nil {
			return err
		}
		if _, err := s.w.ExecContext(ctx, `UPDATE secrets SET value = ? WHERE rowid = ?`, enc, x.id); err != nil {
			return err
		}
	}
	return nil
}

// SecretKeyDerivation reports how this database derives its secret key:
// "argon2id-v1" (salted, stretched) or "direct" (databases that held secrets
// before key stretching existed).
func (s *Store) SecretKeyDerivation(ctx context.Context) (string, error) {
	var kdf string
	err := s.r.QueryRowContext(ctx, `SELECT kdf FROM secret_key_info WHERE id = 1`).Scan(&kdf)
	return kdf, err
}

func (s *Store) secretAEAD() (cipher.AEAD, error) {
	v, ok := secretCiphers.Load(s)
	if !ok {
		return nil, errors.New("secret storage has no encryption key")
	}
	return v.(*secretCipher).get()
}

func aadV2(scope, scopeID, name, kind string) []byte {
	return []byte("weft-secret-v2\x00" + scope + "\x00" + scopeID + "\x00" + name + "\x00" + kind)
}

func (s *Store) encryptRow(plain, scope, scopeID, name, kind string) (string, error) {
	aead, err := s.secretAEAD()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ct := aead.Seal(nonce, nonce, []byte(plain), aadV2(scope, scopeID, name, kind))
	return encPrefixV2 + base64.StdEncoding.EncodeToString(ct), nil
}

func (s *Store) decryptRow(stored, scope, scopeID, name, kind string) (string, error) {
	var aad []byte
	var body string
	switch {
	case strings.HasPrefix(stored, encPrefixV2):
		aad, body = aadV2(scope, scopeID, name, kind), stored[len(encPrefixV2):]
	case strings.HasPrefix(stored, encPrefix):
		aad, body = []byte(aadV1), stored[len(encPrefix):]
	default:
		return stored, nil // legacy plaintext (re-encrypted when a key is set)
	}
	aead, err := s.secretAEAD()
	if err != nil {
		return "", err
	}
	raw, err := base64.StdEncoding.DecodeString(body)
	if err != nil || len(raw) < aead.NonceSize() {
		return "", errors.New("corrupt secret")
	}
	pt, err := aead.Open(nil, raw[:aead.NonceSize()], raw[aead.NonceSize():], aad)
	if err != nil {
		return "", errors.New("cannot decrypt secret: wrong WEFT_SECRET_KEY or key file")
	}
	return string(pt), nil
}

// ---------------------------------------------------------------- model

// SecretInfo describes a stored secret (never its value).
type SecretInfo struct {
	Name  string `json:"name"`
	Scope string `json:"scope"`
	// ScopeID is the team for team secrets, the project for project
	// secrets, and empty for server secrets.
	ScopeID      string   `json:"scope_id"`
	Kind         string   `json:"kind"`
	AllowedHosts []string `json:"allowed_hosts"`
	CreatedBy    *string  `json:"created_by,omitempty"`
	UpdatedAt    string   `json:"updated_at"`
}

// SecretRecord is a resolved secret with its value.
type SecretRecord struct {
	SecretInfo
	Value string `json:"-"`
}

// SecretWrite stores or replaces a secret.
type SecretWrite struct {
	Name    string
	Value   string
	Scope   string // default server
	ScopeID string // team name or project id
	// Kind defaults to the existing row's kind, or "secret".
	Kind string
	// AllowedHosts, when non-nil, replaces the allowed hosts (empty: any).
	AllowedHosts []string
	CreatedBy    *string
}

// NormalizeSecretScope validates a scope and its id and returns them in
// canonical form (project ids normalised; team names checked).
func NormalizeSecretScope(scope, scopeID string) (string, string, error) {
	scope = strings.TrimSpace(scope)
	if scope == "" {
		scope = SecretScopeServer
	}
	scopeID = strings.TrimSpace(scopeID)
	switch scope {
	case SecretScopeServer:
		if scopeID != "" {
			return "", "", Invalidf("a server secret has no scope id")
		}
		return scope, "", nil
	case SecretScopeTeam:
		if scopeID == "" || strings.Contains(scopeID, "/") || len(scopeID) > 128 {
			return "", "", Invalidf("a team secret needs the team (entity) name")
		}
		pid, err := refs.NormalizeProject(scopeID + "/x")
		if err != nil {
			return "", "", Invalidf("invalid team %q", scopeID)
		}
		ent, _, _ := strings.Cut(pid, "/")
		return scope, ent, nil
	case SecretScopeProject:
		pid, err := refs.NormalizeProject(scopeID)
		if err != nil {
			return "", "", Invalidf("a project secret needs a valid project_id: %v", err)
		}
		return scope, pid, nil
	}
	return "", "", Invalidf("scope must be server, team or project")
}

// NormalizeAllowedHosts validates allowed-host entries: "host", "host:port"
// or "*.domain" (subdomains at any depth), lowercased; IP literals allowed.
func NormalizeAllowedHosts(in []string) ([]string, error) {
	seen := map[string]bool{}
	out := []string{}
	for _, raw := range in {
		e := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(raw), "."))
		if e == "" {
			continue
		}
		if strings.Contains(e, "://") || strings.ContainsAny(e, "/?#@ \t,") {
			return nil, Invalidf("allowed host %q: use a host, host:port or *.domain, not a URL", raw)
		}
		host, port := e, ""
		if h, p, err := net.SplitHostPort(e); err == nil {
			host, port = h, p
			if n, err := strconv.Atoi(p); err != nil || n < 1 || n > 65535 {
				return nil, Invalidf("allowed host %q: invalid port", raw)
			}
		}
		host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
		if strings.HasPrefix(host, "*.") {
			if len(host) < 4 || strings.Contains(host[2:], "*") {
				return nil, Invalidf("allowed host %q: invalid wildcard", raw)
			}
		} else if strings.Contains(host, "*") {
			return nil, Invalidf("allowed host %q: a wildcard must be a leading \"*.\"", raw)
		}
		if host == "" || len(host) > 253 {
			return nil, Invalidf("allowed host %q is invalid", raw)
		}
		norm := host
		if strings.Contains(host, ":") {
			norm = "[" + host + "]"
		}
		if port != "" {
			norm += ":" + port
		}
		if !seen[norm] {
			seen[norm] = true
			out = append(out, norm)
		}
	}
	if len(out) > MaxSecretAllowedHosts {
		return nil, Invalidf("at most %d allowed hosts", MaxSecretAllowedHosts)
	}
	sort.Strings(out)
	return out, nil
}

// SecretHostAllowed reports whether a request to host:port may carry a
// secret with these allowed hosts (an empty list allows any host).
func SecretHostAllowed(allowed []string, host, port string) bool {
	if len(allowed) == 0 {
		return true
	}
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSuffix(strings.TrimPrefix(host, "["), "]"), "."))
	for _, a := range allowed {
		h, p := a, ""
		if hh, pp, err := net.SplitHostPort(a); err == nil {
			h, p = hh, pp
		}
		h = strings.TrimSuffix(strings.TrimPrefix(h, "["), "]")
		if p != "" && p != port {
			continue
		}
		if strings.HasPrefix(h, "*.") {
			if strings.HasSuffix(host, h[1:]) && len(host) > len(h)-1 {
				return true
			}
			continue
		}
		if h == host {
			return true
		}
	}
	return false
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

// ---------------------------------------------------------------- writes

// PutSecret stores (or replaces) a secret in its scope, encrypted.
func (s *Store) PutSecret(ctx context.Context, w SecretWrite) error {
	if !secretNameOK(w.Name) {
		return Invalidf("invalid secret name %q (use A-Z, 0-9, _)", w.Name)
	}
	if len(w.Value) > secretsMax {
		return Invalidf("secret too long")
	}
	scope, scopeID, err := NormalizeSecretScope(w.Scope, w.ScopeID)
	if err != nil {
		return err
	}
	kind := w.Kind
	var cur struct {
		kind, hosts string
		ok          bool
	}
	err = s.w.QueryRowContext(ctx, `SELECT kind, allowed_hosts FROM secrets WHERE scope = ? AND scope_id = ? AND name = ?`,
		scope, scopeID, w.Name).Scan(&cur.kind, &cur.hosts)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return err
	default:
		cur.ok = true
	}
	if kind == "" {
		kind = SecretKindSecret
		if cur.ok {
			kind = cur.kind
		}
	}
	switch kind {
	case SecretKindSecret:
	case SecretKindBucket:
		if scope != SecretScopeServer {
			return Invalidf("bucket credentials are server secrets")
		}
	default:
		return Invalidf("kind must be secret or bucket")
	}
	hosts := cur.hosts
	if w.AllowedHosts != nil {
		hs, err := NormalizeAllowedHosts(w.AllowedHosts)
		if err != nil {
			return err
		}
		hosts = strings.Join(hs, ",")
	}
	enc, err := s.encryptRow(w.Value, scope, scopeID, w.Name, kind)
	if err != nil {
		return err
	}
	_, err = s.w.ExecContext(ctx, `INSERT INTO secrets(scope, scope_id, name, value, kind, allowed_hosts, created_by, updated_at)
		VALUES(?,?,?,?,?,?,?,?)
		ON CONFLICT(scope, scope_id, name) DO UPDATE SET value = excluded.value, kind = excluded.kind,
			allowed_hosts = excluded.allowed_hosts, updated_at = excluded.updated_at`,
		scope, scopeID, w.Name, enc, kind, hosts, nullStr(w.CreatedBy), nowMicros())
	return err
}

// SetSecret stores a server secret (kind unchanged, "secret" when new).
func (s *Store) SetSecret(ctx context.Context, name, value string) error {
	return s.PutSecret(ctx, SecretWrite{Name: name, Value: value})
}

// DeleteScopedSecret removes a secret from one scope; false when absent.
func (s *Store) DeleteScopedSecret(ctx context.Context, scope, scopeID, name string) (bool, error) {
	scope, scopeID, err := NormalizeSecretScope(scope, scopeID)
	if err != nil {
		return false, err
	}
	res, err := s.w.ExecContext(ctx, `DELETE FROM secrets WHERE scope = ? AND scope_id = ? AND name = ?`, scope, scopeID, name)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// DeleteSecret removes a server secret.
func (s *Store) DeleteSecret(ctx context.Context, name string) error {
	_, err := s.DeleteScopedSecret(ctx, SecretScopeServer, "", name)
	return err
}

// MarkBucketCredential turns a server secret into bucket credentials, which
// ResolveSecret no longer returns. It is a no-op when the secret is absent.
func (s *Store) MarkBucketCredential(ctx context.Context, name string) error {
	var v, kind string
	err := s.w.QueryRowContext(ctx, `SELECT value, kind FROM secrets WHERE scope = 'server' AND scope_id = '' AND name = ?`, name).Scan(&v, &kind)
	if errors.Is(err, sql.ErrNoRows) || kind == SecretKindBucket {
		return nil
	}
	if err != nil {
		return err
	}
	plain, err := s.decryptRow(v, SecretScopeServer, "", name, kind)
	if err != nil {
		return err
	}
	enc, err := s.encryptRow(plain, SecretScopeServer, "", name, SecretKindBucket)
	if err != nil {
		return err
	}
	_, err = s.w.ExecContext(ctx, `UPDATE secrets SET value = ?, kind = 'bucket' WHERE scope = 'server' AND scope_id = '' AND name = ?`, enc, name)
	return err
}

// ---------------------------------------------------------------- reads

const secretCols = `scope, scope_id, name, kind, allowed_hosts, created_by, updated_at`

func scanSecretInfo(sc interface{ Scan(...any) error }, extra ...any) (SecretInfo, error) {
	var x SecretInfo
	var hosts string
	var by sql.NullString
	var at int64
	dest := append([]any{&x.Scope, &x.ScopeID, &x.Name, &x.Kind, &hosts, &by, &at}, extra...)
	if err := sc.Scan(dest...); err != nil {
		return x, err
	}
	x.AllowedHosts = []string{}
	if hosts != "" {
		x.AllowedHosts = strings.Split(hosts, ",")
	}
	if by.Valid {
		x.CreatedBy = &by.String
	}
	x.UpdatedAt = FormatMicros(at)
	return x, nil
}

// ResolveSecret returns the general secret (not bucket credentials) named
// name that projectID can see, from the narrowest scope that has it: the
// project, its team, then the server. With projectID "" only server secrets
// are considered. It returns nil when there is none. Whether the caller may
// actually use it (server secrets, allowed hosts, https) is decided by the
// caller (llm.Client.UseSecret).
func (s *Store) ResolveSecret(ctx context.Context, projectID, name string) (*SecretRecord, error) {
	if !secretNameOK(name) {
		return nil, nil
	}
	team := ""
	if projectID != "" {
		pid, err := refs.NormalizeProject(projectID)
		if err != nil {
			return nil, nil
		}
		projectID = pid
		team, _, _ = strings.Cut(pid, "/")
	}
	rows, err := s.r.QueryContext(ctx, `SELECT `+secretCols+`, value FROM secrets
		WHERE name = ? AND kind = 'secret' AND ((scope = 'server' AND scope_id = '')
			OR (scope = 'team' AND scope_id = ? AND ? != '') OR (scope = 'project' AND scope_id = ? AND ? != ''))`,
		name, team, team, projectID, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var best *SecretRecord
	for rows.Next() {
		var v string
		info, err := scanSecretInfo(rows, &v)
		if err != nil {
			return nil, err
		}
		if best == nil || SecretScopeRank(info.Scope) > SecretScopeRank(best.Scope) {
			best = &SecretRecord{SecretInfo: info, Value: v}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if best == nil {
		return nil, nil
	}
	pt, err := s.decryptRow(best.Value, best.Scope, best.ScopeID, best.Name, best.Kind)
	if err != nil {
		return nil, err
	}
	best.Value = pt
	return best, nil
}

// GetSecret returns a server secret of kind "secret", decrypted.
func (s *Store) GetSecret(ctx context.Context, name string) (string, bool, error) {
	rec, err := s.ResolveSecret(ctx, "", name)
	if err != nil || rec == nil {
		return "", false, err
	}
	return rec.Value, true, nil
}

// GetBucketCredential returns the server secret a reference bucket names
// (bucket credentials, or a server secret about to be marked as such when a
// server admin registers the bucket). Only the reference-bucket code calls it.
func (s *Store) GetBucketCredential(ctx context.Context, name string) (string, bool, error) {
	var v, kind string
	err := s.r.QueryRowContext(ctx, `SELECT value, kind FROM secrets WHERE scope = 'server' AND scope_id = '' AND name = ?`, name).Scan(&v, &kind)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	pt, err := s.decryptRow(v, SecretScopeServer, "", name, kind)
	if err != nil {
		return "", false, err
	}
	return pt, true, nil
}

// SecretListFilter selects secrets for ListSecretInfo.
type SecretListFilter struct {
	// All lists every secret (server admins).
	All bool
	// Projects and Teams list those scopes' secrets; ProjectsOfTeams lists
	// the project secrets of every project of these teams; AllScoped lists
	// every team and project secret.
	Projects        []string
	Teams           []string
	ProjectsOfTeams []string
	AllScoped       bool
	// SharedServer lists server secrets that have allowed hosts (those
	// projects may use toward their hosts) and, with ServerNames, server
	// secrets with these names (the built-in provider keys).
	SharedServer bool
	ServerNames  []string
}

// ListSecretInfo lists secrets (never values), ordered by name and scope.
func (s *Store) ListSecretInfo(ctx context.Context, f SecretListFilter) ([]SecretInfo, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT `+secretCols+` FROM secrets ORDER BY name, scope, scope_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	in := func(list []string, v string) bool {
		for _, x := range list {
			if x == v {
				return true
			}
		}
		return false
	}
	out := []SecretInfo{}
	for rows.Next() {
		x, err := scanSecretInfo(rows)
		if err != nil {
			return nil, err
		}
		keep := f.All
		switch x.Scope {
		case SecretScopeProject:
			ent, _, _ := strings.Cut(x.ScopeID, "/")
			keep = keep || f.AllScoped || in(f.Projects, x.ScopeID) || in(f.ProjectsOfTeams, ent)
		case SecretScopeTeam:
			keep = keep || f.AllScoped || in(f.Teams, x.ScopeID)
		case SecretScopeServer:
			keep = keep || (x.Kind == SecretKindSecret && ((f.SharedServer && len(x.AllowedHosts) > 0) || in(f.ServerNames, x.Name)))
		}
		if keep {
			out = append(out, x)
		}
	}
	return out, rows.Err()
}

// ListSecrets returns the names of server secrets.
func (s *Store) ListSecrets(ctx context.Context) ([]string, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT name FROM secrets WHERE scope = 'server' ORDER BY name`)
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
