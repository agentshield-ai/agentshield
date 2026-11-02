package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/agentshield-ai/agentshield/internal/weft/refs"
)

// FileBackend stores file contents outside SQLite ("bring your own bucket").
// The files table still records every file (project, digest, size); its
// location column names the backend key and its content column is empty.
type FileBackend interface {
	// Name identifies the backend kind ("s3").
	Name() string
	// Key maps a relative key ("<project_id>/<digest>") to the backend key.
	Key(rel string) string
	// URI renders a backend key as a URI (s3://bucket/key).
	URI(key string) string
	Put(ctx context.Context, key string, content []byte) error
	Get(ctx context.Context, key string) ([]byte, error)
}

// FilePresigner is implemented by backends that can issue time-limited
// direct download URLs.
type FilePresigner interface {
	PresignGet(key string, ttl time.Duration) (string, time.Time, error)
}

func init() {
	registerMigration(func(db *sql.DB) error {
		ok, err := HasColumn(db, "files", "location")
		if err != nil || ok {
			return err
		}
		_, err = db.Exec(`ALTER TABLE files ADD COLUMN location TEXT NOT NULL DEFAULT ''`)
		return err
	})
}

// SetFileBackend sends new file contents to b instead of SQLite. Files
// already stored in SQLite stay readable. nil restores the SQLite default.
func (s *Store) SetFileBackend(b FileBackend) { s.files = b }

// FileBackend returns the configured backend (nil for SQLite).
func (s *Store) FileBackend() FileBackend { return s.files }

// FileInfo describes where a stored file lives.
type FileInfo struct {
	ProjectID string
	Digest    string
	SizeBytes int64
	// Location is the backend key; empty when the content is in SQLite.
	Location string
}

// StatFile looks up a file without reading its content.
func (s *Store) StatFile(ctx context.Context, projectID, digest string) (*FileInfo, error) {
	pid, err := refs.NormalizeProject(projectID)
	if err != nil {
		return nil, &InvalidError{Msg: err.Error()}
	}
	fi := &FileInfo{ProjectID: pid, Digest: digest}
	err = s.r.QueryRowContext(ctx, `SELECT size_bytes, location FROM files WHERE project_id = ? AND digest = ?`, pid, digest).
		Scan(&fi.SizeBytes, &fi.Location)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return fi, nil
}

// createFile stores content in SQLite or the configured backend.
func (s *Store) createFile(ctx context.Context, pid, digest string, content []byte) error {
	var exists int
	err := s.r.QueryRowContext(ctx, `SELECT 1 FROM files WHERE project_id = ? AND digest = ?`, pid, digest).Scan(&exists)
	if err == nil {
		return nil // content-addressed: already stored
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	location, stored := "", content
	if s.files != nil {
		location = s.files.Key(pid + "/" + digest)
		if err := s.files.Put(ctx, location, content); err != nil {
			return fmt.Errorf("writing file to the %s backend: %w", s.files.Name(), err)
		}
		stored = []byte{}
	}
	return s.withTx(ctx, func(tx *sql.Tx) error {
		if err := s.touchProject(ctx, tx, pid); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO files(project_id, digest, content, size_bytes, created_at, location) VALUES(?,?,?,?,?,?)`,
			pid, digest, stored, len(content), nowMicros(), location)
		return err
	})
}

// fileContent reads a file from SQLite or its backend.
func (s *Store) fileContent(ctx context.Context, pid, digest string) ([]byte, string, error) {
	var b []byte
	var location string
	err := s.r.QueryRowContext(ctx, `SELECT content, location FROM files WHERE project_id = ? AND digest = ?`, pid, digest).Scan(&b, &location)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", ErrNotFound
	}
	if err != nil {
		return nil, "", err
	}
	if location != "" {
		if s.files == nil {
			return nil, "", fmt.Errorf("file %s is stored in an external backend (%s) but no files backend is configured", digest, location)
		}
		if b, err = s.files.Get(ctx, location); err != nil {
			return nil, "", fmt.Errorf("reading file from the %s backend: %w", s.files.Name(), err)
		}
	}
	return b, http.DetectContentType(b), nil
}
