package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/agentshield-ai/agentshield/internal/weft/refs"
)

// Bulk project export jobs (
// POST /v1/entities/{entity}/projects/{project}:export). The server runs one
// job at a time; job rows survive restarts, and a job interrupted by a
// restart is marked failed when the server starts again. Artifacts are
// stored in the files backend when one is configured, otherwise as chunks
// in bulk_export_chunks.

func init() {
	registerSchema(`
CREATE TABLE IF NOT EXISTS bulk_exports (
	job_id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL,
	targets TEXT NOT NULL,
	state TEXT NOT NULL,
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	artifacts TEXT NOT NULL DEFAULT '[]',
	failure TEXT,
	created_by TEXT
);
CREATE INDEX IF NOT EXISTS bulk_exports_project ON bulk_exports(project_id, created_at);
CREATE TABLE IF NOT EXISTS bulk_export_chunks (
	job_id TEXT NOT NULL,
	project_id TEXT NOT NULL,
	target TEXT NOT NULL,
	seq INTEGER NOT NULL,
	data BLOB NOT NULL,
	PRIMARY KEY (job_id, target, seq)
);`)
}

// Export job states (ExportStatusResponse.state).
const (
	ExportInProgress = "in_progress"
	ExportSucceeded  = "succeeded"
	ExportFailed     = "failed"
)

// ExportArtifact is one written target (JobArtifact).
type ExportArtifact struct {
	Target       string `json:"target"`
	URI          string `json:"uri"`
	ExpectedRows int64  `json:"expected_rows"`
	WrittenRows  int64  `json:"written_rows"`
	// Weft extensions: where the content lives, its size and format.
	Location  string `json:"-"`
	SizeBytes int64  `json:"size_bytes"`
	Format    string `json:"format"`
}

type exportArtifactRow struct {
	ExportArtifact
	Location string `json:"location"`
}

// ExportFailure is a job failure (JobFailure).
type ExportFailure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ExportJob is a stored export job.
type ExportJob struct {
	JobID     string
	ProjectID string
	Targets   []string
	State     string
	CreatedAt time.Time
	UpdatedAt time.Time
	Artifacts []ExportArtifact
	Failure   *ExportFailure
	CreatedBy *string
}

// ErrExportBusy means another export job is still running on this server.
var ErrExportBusy = errors.New("an export is already in progress on this server")

// CreateExportJob records a new in-progress job unless another one is running.
func (s *Store) CreateExportJob(ctx context.Context, j *ExportJob) error {
	pid, err := refs.NormalizeProject(j.ProjectID)
	if err != nil {
		return &InvalidError{Msg: err.Error()}
	}
	j.ProjectID = pid
	targets, _ := json.Marshal(j.Targets)
	now := time.Now().UTC()
	j.CreatedAt, j.UpdatedAt, j.State = now, now, ExportInProgress
	return s.withTx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM bulk_exports WHERE state = ?`, ExportInProgress).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return ErrExportBusy
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO bulk_exports(job_id, project_id, targets, state, created_at, updated_at, created_by)
			VALUES(?,?,?,?,?,?,?)`, j.JobID, pid, string(targets), j.State, now.UnixMicro(), now.UnixMicro(), nullStr(j.CreatedBy))
		return err
	})
}

// FinishExportJob stores the final state, artifacts and failure of a job.
func (s *Store) FinishExportJob(ctx context.Context, jobID, state string, arts []ExportArtifact, failure *ExportFailure) error {
	rows := make([]exportArtifactRow, len(arts))
	for i, a := range arts {
		rows[i] = exportArtifactRow{ExportArtifact: a, Location: a.Location}
	}
	ab, _ := json.Marshal(rows)
	var fb any
	if failure != nil {
		b, _ := json.Marshal(failure)
		fb = string(b)
	}
	_, err := s.w.ExecContext(ctx, `UPDATE bulk_exports SET state = ?, artifacts = ?, failure = ?, updated_at = ? WHERE job_id = ?`,
		state, string(ab), fb, time.Now().UTC().UnixMicro(), jobID)
	return err
}

// GetExportJob reads a job of a project.
func (s *Store) GetExportJob(ctx context.Context, projectID, jobID string) (*ExportJob, error) {
	pid, err := refs.NormalizeProject(projectID)
	if err != nil {
		return nil, &InvalidError{Msg: err.Error()}
	}
	var j ExportJob
	var targets, arts string
	var failure, by sql.NullString
	var created, updated int64
	err = s.r.QueryRowContext(ctx, `SELECT job_id, project_id, targets, state, created_at, updated_at, artifacts, failure, created_by
		FROM bulk_exports WHERE project_id = ? AND job_id = ?`, pid, jobID).
		Scan(&j.JobID, &j.ProjectID, &targets, &j.State, &created, &updated, &arts, &failure, &by)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	j.CreatedAt, j.UpdatedAt = time.UnixMicro(created).UTC(), time.UnixMicro(updated).UTC()
	_ = json.Unmarshal([]byte(targets), &j.Targets)
	var rows []exportArtifactRow
	_ = json.Unmarshal([]byte(arts), &rows)
	j.Artifacts = make([]ExportArtifact, len(rows))
	for i, r := range rows {
		j.Artifacts[i] = r.ExportArtifact
		j.Artifacts[i].Location = r.Location
	}
	if failure.Valid {
		var f ExportFailure
		if json.Unmarshal([]byte(failure.String), &f) == nil {
			j.Failure = &f
		}
	}
	j.CreatedBy = strPtr(by)
	return &j, nil
}

// FailInterruptedExportJobs marks jobs left in progress by a previous
// process as failed.
func (s *Store) FailInterruptedExportJobs(ctx context.Context) error {
	b, _ := json.Marshal(ExportFailure{Code: "interrupted", Message: "the server stopped while this export was running; start a new export"})
	_, err := s.w.ExecContext(ctx, `UPDATE bulk_exports SET state = ?, failure = ?, updated_at = ? WHERE state = ?`,
		ExportFailed, string(b), time.Now().UTC().UnixMicro(), ExportInProgress)
	return err
}

// PutExportChunk stores one chunk of an artifact kept in SQLite.
func (s *Store) PutExportChunk(ctx context.Context, jobID, projectID, target string, seq int, data []byte) error {
	_, err := s.w.ExecContext(ctx, `INSERT OR REPLACE INTO bulk_export_chunks(job_id, project_id, target, seq, data) VALUES(?,?,?,?,?)`,
		jobID, projectID, target, seq, data)
	return err
}

// DeleteExportChunks drops the SQLite chunks of a job.
func (s *Store) DeleteExportChunks(ctx context.Context, jobID string) error {
	_, err := s.w.ExecContext(ctx, `DELETE FROM bulk_export_chunks WHERE job_id = ?`, jobID)
	return err
}

// ExportChunks calls fn with each chunk of an artifact, in order, reading
// one chunk at a time.
func (s *Store) ExportChunks(ctx context.Context, jobID, target string, fn func([]byte) error) error {
	seq := 0
	for {
		var data []byte
		err := s.r.QueryRowContext(ctx, `SELECT data FROM bulk_export_chunks WHERE job_id = ? AND target = ? AND seq = ?`, jobID, target, seq).Scan(&data)
		if errors.Is(err, sql.ErrNoRows) {
			if seq == 0 {
				return ErrNotFound
			}
			return nil
		}
		if err != nil {
			return err
		}
		if err := fn(data); err != nil {
			return err
		}
		seq++
	}
}

// CountObjectVersions counts a project's live object versions created at or
// before cutoff (unix micros).
func (s *Store) CountObjectVersions(ctx context.Context, projectID string, cutoff int64) (int64, error) {
	var n int64
	err := s.r.QueryRowContext(ctx, `SELECT COUNT(*) FROM objects WHERE project_id = ? AND deleted_at IS NULL AND created_at <= ?`,
		projectID, cutoff).Scan(&n)
	return n, err
}

// CountFeedbackBefore counts a project's feedback created at or before cutoff.
func (s *Store) CountFeedbackBefore(ctx context.Context, projectID string, cutoff int64) (int64, error) {
	var n int64
	err := s.r.QueryRowContext(ctx, `SELECT COUNT(*) FROM feedback WHERE project_id = ? AND created_at <= ?`, projectID, cutoff).Scan(&n)
	return n, err
}

// ExportObjectsPage reads live object versions created at or before cutoff,
// oldest first, with values.
func (s *Store) ExportObjectsPage(ctx context.Context, projectID string, cutoff int64, limit, offset int) ([]*Obj, error) {
	rows, err := s.r.QueryContext(ctx, objSelect+` WHERE o.project_id = ? AND o.deleted_at IS NULL AND o.created_at <= ?
		ORDER BY o.created_at, o.object_id, o.version_index LIMIT ? OFFSET ?`, projectID, cutoff, limit, offset)
	if err != nil {
		return nil, err
	}
	out := []*Obj{}
	for rows.Next() {
		o, err := scanObj(rows, true)
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
	return out, s.attachAliases(ctx, out)
}

// ExportFeedbackPage reads feedback created at or before cutoff, oldest first.
func (s *Store) ExportFeedbackPage(ctx context.Context, projectID string, cutoff int64, limit, offset int) ([]Feedback, error) {
	rows, err := s.r.QueryContext(ctx, feedbackSelect+` WHERE f.project_id = ? AND f.created_at <= ? ORDER BY f.created_at, f.id LIMIT ? OFFSET ?`,
		projectID, cutoff, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Feedback{}
	for rows.Next() {
		f, err := scanFeedback(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}
