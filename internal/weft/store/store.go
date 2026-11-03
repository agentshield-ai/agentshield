// Package store is Weft's SQLite persistence layer: calls (with usage
// roll-up), versioned objects, content-addressed tables and files, feedback,
// cost overrides and secrets.
package store

import (
	"context"
	"crypto/cipher"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/agentshield-ai/agentshield/internal/weft/refs"
)

// ErrNotFound is returned when a requested record does not exist.
var ErrNotFound = errors.New("not found")

// InvalidError marks a client error (bad request).
type InvalidError struct{ Msg string }

func (e *InvalidError) Error() string { return e.Msg }

// Invalidf builds an InvalidError.
func Invalidf(format string, args ...any) error {
	return &InvalidError{Msg: fmt.Sprintf(format, args...)}
}

// Store holds separate read and write handles. SQLite allows one writer;
// WAL mode lets readers proceed concurrently.
type Store struct {
	w    *sql.DB
	r    *sql.DB
	aead cipher.AEAD // encrypts secrets; see secrets.go
	// files, when set, holds file contents outside SQLite; see filebackend.go.
	files FileBackend
	// pii, when set, is the NER analyzer used by sensitive-data redaction; see ner.go.
	pii *piiState
	// encrypted reports whether the database file is encrypted; see dbcrypt.go.
	encrypted bool
	// hooks adjust what clients send before it is stored; see SetIngestHooks.
	hooks IngestHooks
}

// IngestHooks let an adapter for another product's clients (package compat)
// translate what those clients send before it is stored.
type IngestHooks struct {
	// Attributes rewrites a call's attributes in place.
	Attributes func(map[string]any)
	// IsOp reports an object value that encodes an op in a client's own format.
	IsOp func(map[string]any) bool
}

// SetIngestHooks installs ingest hooks. Call it once, before serving.
func (s *Store) SetIngestHooks(h IngestHooks) { s.hooks = h }

func (s *Store) hookStart(st *CallStart) {
	if s.hooks.Attributes != nil && st != nil {
		s.hooks.Attributes(st.Attributes)
	}
}

const schema = `
CREATE TABLE IF NOT EXISTS projects (
	project_id TEXT PRIMARY KEY,
	created_at INTEGER NOT NULL,
	last_activity INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS calls (
	project_id TEXT NOT NULL,
	id TEXT NOT NULL,
	op_name TEXT NOT NULL DEFAULT '',
	op_base_name TEXT NOT NULL DEFAULT '',
	display_name TEXT,
	trace_id TEXT NOT NULL DEFAULT '',
	parent_id TEXT,
	thread_id TEXT,
	turn_id TEXT,
	started_at INTEGER,
	ended_at INTEGER,
	exception TEXT,
	attributes TEXT NOT NULL DEFAULT '{}',
	inputs TEXT NOT NULL DEFAULT '{}',
	output TEXT,
	summary TEXT NOT NULL DEFAULT '{}',
	rollup TEXT NOT NULL DEFAULT '{}',
	propagated TEXT NOT NULL DEFAULT '{}',
	total_tokens INTEGER NOT NULL DEFAULT 0,
	input_refs TEXT NOT NULL DEFAULT '[]',
	output_refs TEXT NOT NULL DEFAULT '[]',
	wb_user_id TEXT,
	wb_run_id TEXT,
	deleted_at INTEGER,
	storage_size INTEGER NOT NULL DEFAULT 0,
	has_start INTEGER NOT NULL DEFAULT 0,
	cumulative INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (project_id, id)
);
CREATE INDEX IF NOT EXISTS calls_started ON calls(project_id, started_at);
CREATE INDEX IF NOT EXISTS calls_trace ON calls(project_id, trace_id);
CREATE INDEX IF NOT EXISTS calls_parent ON calls(project_id, parent_id);
CREATE INDEX IF NOT EXISTS calls_op ON calls(project_id, op_base_name, started_at);
CREATE INDEX IF NOT EXISTS calls_thread ON calls(project_id, thread_id);

CREATE TABLE IF NOT EXISTS objects (
	project_id TEXT NOT NULL,
	object_id TEXT NOT NULL,
	digest TEXT NOT NULL,
	version_index INTEGER NOT NULL,
	kind TEXT NOT NULL,
	base_object_class TEXT,
	leaf_object_class TEXT,
	val TEXT NOT NULL,
	created_at INTEGER NOT NULL,
	deleted_at INTEGER,
	wb_user_id TEXT,
	size_bytes INTEGER NOT NULL,
	PRIMARY KEY (project_id, object_id, digest)
);
CREATE INDEX IF NOT EXISTS objects_class ON objects(project_id, base_object_class);
CREATE TABLE IF NOT EXISTS object_aliases (
	project_id TEXT NOT NULL,
	object_id TEXT NOT NULL,
	alias TEXT NOT NULL,
	digest TEXT NOT NULL,
	PRIMARY KEY (project_id, object_id, alias)
);

CREATE TABLE IF NOT EXISTS table_rows (
	project_id TEXT NOT NULL,
	digest TEXT NOT NULL,
	val TEXT NOT NULL,
	PRIMARY KEY (project_id, digest)
);
CREATE TABLE IF NOT EXISTS tables (
	project_id TEXT NOT NULL,
	digest TEXT NOT NULL,
	row_digests TEXT NOT NULL,
	created_at INTEGER NOT NULL,
	PRIMARY KEY (project_id, digest)
);

CREATE TABLE IF NOT EXISTS files (
	project_id TEXT NOT NULL,
	digest TEXT NOT NULL,
	content BLOB NOT NULL,
	size_bytes INTEGER NOT NULL,
	created_at INTEGER NOT NULL,
	PRIMARY KEY (project_id, digest)
);

CREATE TABLE IF NOT EXISTS feedback (
	id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL,
	ref TEXT NOT NULL,
	feedback_type TEXT NOT NULL,
	payload TEXT NOT NULL,
	creator TEXT,
	created_at INTEGER NOT NULL,
	wb_user_id TEXT,
	annotation_ref TEXT,
	runnable_ref TEXT,
	call_ref TEXT,
	trigger_ref TEXT
);
CREATE INDEX IF NOT EXISTS feedback_ref ON feedback(project_id, ref);
CREATE INDEX IF NOT EXISTS feedback_trigger ON feedback(project_id, trigger_ref);

CREATE TABLE IF NOT EXISTS llm_prices (
	id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL,
	llm_id TEXT NOT NULL,
	provider_id TEXT NOT NULL,
	prompt_token_cost REAL NOT NULL,
	completion_token_cost REAL NOT NULL,
	prompt_token_cost_unit TEXT NOT NULL,
	completion_token_cost_unit TEXT NOT NULL,
	effective_date INTEGER NOT NULL,
	created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS llm_prices_project ON llm_prices(project_id, llm_id);

CREATE TABLE IF NOT EXISTS secrets (
	name TEXT PRIMARY KEY,
	value TEXT NOT NULL,
	updated_at INTEGER NOT NULL
);
`

// Open opens (creating if needed) the database at path, encrypted when
// WEFT_DB_ENCRYPTION_KEY is set; see OpenWith.
func Open(path string) (*Store, error) { return OpenWith(path, OpenOptionsFromEnv()) }

// OpenWith opens (creating if needed) the database at path. With
// opts.EncryptionKey the file is encrypted at rest (dbcrypt.go); a key that
// does not match the file is an error before anything is written.
func OpenWith(path string, opts OpenOptions) (*Store, error) {
	var key []byte
	if opts.EncryptionKey != "" {
		key = deriveDBKey(opts.EncryptionKey)
	}
	if err := checkDBFile(path, key != nil); err != nil {
		return nil, err
	}
	w, err := openDB(path, key, false)
	if err != nil {
		return nil, err
	}
	w.SetMaxOpenConns(1)
	if _, err := w.Exec(schema); err != nil {
		_ = w.Close()
		return nil, fmt.Errorf("initialising schema: %w", err)
	}
	if err := migrateLegacyNames(w); err != nil { // legacy_names.go
		_ = w.Close()
		return nil, fmt.Errorf("migrating legacy names: %w", err)
	}
	if err := runExtensions(w); err != nil {
		_ = w.Close()
		return nil, fmt.Errorf("initialising extension schema: %w", err)
	}
	if err := migrate(w); err != nil {
		_ = w.Close()
		return nil, fmt.Errorf("migrating schema: %w", err)
	}
	r, err := openDB(path, key, true)
	if err != nil {
		_ = w.Close()
		return nil, fmt.Errorf("opening read handle: %w", err)
	}
	r.SetMaxOpenConns(8)
	st := &Store{w: w, r: r, encrypted: key != nil}
	key2, err := LoadSecretKey(os.Getenv("WEFT_SECRET_KEY"), SecretKeyFile(path))
	if err != nil {
		_ = st.Close()
		return nil, err
	}
	if err := st.SetSecretKey(context.Background(), key2); err != nil {
		_ = st.Close()
		return nil, fmt.Errorf("initialising secret encryption: %w", err)
	}
	return st, nil
}

// Encrypted reports whether the database file is encrypted at rest.
func (s *Store) Encrypted() bool { return s.encrypted }

// Close closes both handles.
func (s *Store) Close() error {
	err1 := s.r.Close()
	err2 := s.w.Close()
	return errors.Join(err1, err2)
}

// ---------------------------------------------------------------- helpers

func nowMicros() int64 { return time.Now().UnixMicro() }

// Micros converts t to unix microseconds.
func Micros(t time.Time) int64 { return t.UnixMicro() }

// FormatMicros renders microseconds as RFC 3339 UTC.
func FormatMicros(us int64) string {
	return time.UnixMicro(us).UTC().Format("2006-01-02T15:04:05.000000Z")
}

func fmtNullMicros(v sql.NullInt64) *string {
	if !v.Valid {
		return nil
	}
	s := FormatMicros(v.Int64)
	return &s
}

// ParseTime parses an RFC 3339 timestamp (with or without zone) to micros.
func ParseTime(s string) (int64, error) {
	s = strings.TrimSpace(s)
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02 15:04:05.999999999", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UnixMicro(), nil
		}
	}
	return 0, Invalidf("invalid timestamp %q", s)
}

// encodeJSON marshals without HTML escaping.
func encodeJSON(v any) (string, error) {
	b, err := refs.CanonicalJSON(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func mustJSON(v any) string {
	s, err := encodeJSON(v)
	if err != nil {
		return "null"
	}
	return s
}

func decodeJSON(s string) any {
	if s == "" {
		return nil
	}
	v, err := refs.Decode([]byte(s))
	if err != nil {
		return nil
	}
	return v
}

func decodeMap(s string) map[string]any {
	if m, ok := decodeJSON(s).(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

func nullStr(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

func strPtr(ns sql.NullString) *string {
	if !ns.Valid {
		return nil
	}
	v := ns.String
	return &v
}

func (s *Store) touchProject(ctx context.Context, tx *sql.Tx, projectID string) error {
	now := nowMicros()
	_, err := tx.ExecContext(ctx, `INSERT INTO projects(project_id, created_at, last_activity) VALUES(?,?,?)
		ON CONFLICT(project_id) DO UPDATE SET last_activity = excluded.last_activity`, projectID, now, now)
	return err
}

func (s *Store) withTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// toFloat converts a JSON number-ish value to float64.
func toFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case json.Number:
		f, err := t.Float64()
		return f, err == nil
	case float64:
		return t, true
	case float32:
		return float64(t), true
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case bool:
		if t {
			return 1, true
		}
		return 0, true
	}
	return 0, false
}

// ToFloat is the exported form of toFloat.
func ToFloat(v any) (float64, bool) { return toFloat(v) }

// migrate adds columns introduced after a database was created.
func migrate(db *sql.DB) error {
	cols := map[string]bool{}
	rows, err := db.Query(`SELECT name FROM pragma_table_info('calls')`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return err
		}
		cols[n] = true
	}
	rows.Close()
	if !cols["cumulative"] {
		if _, err := db.Exec(`ALTER TABLE calls ADD COLUMN cumulative INTEGER NOT NULL DEFAULT 0`); err != nil {
			return err
		}
	}
	return nil
}

// txT shortens transaction signatures.
type txT = sql.Tx
