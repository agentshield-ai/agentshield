package server

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/agentshield-ai/agentshield/internal/weft/otlp"
	"github.com/agentshield-ai/agentshield/internal/weft/store"
	"github.com/agentshield-ai/agentshield/internal/weft/weftlog"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// Bulk Export API:
//
//	POST /api/v1/exports {project_id, targets}          start a job (202)
//	GET  /api/v1/exports/{id}?project_id=                job status
//
// A job writes each requested target (calls, objects, feedback) as one
// gzip-compressed JSON Lines artifact, one row per line: calls as
// /calls/stream_query returns them (with costs and storage sizes), object
// versions as /objs/query returns them (with values) and feedback as
// /feedback/query returns it. Rows created after the job started are left
// out. Artifacts go to the S3 files backend when one is configured
// (s3://… URIs, presignable through /files/resolve_url) and otherwise into
// SQLite, served by the download route
//
//	GET /api/v1/exports/{id}/artifacts/{target}?project_id=
//
// which redirects to a presigned URL for S3 artifacts. One job runs at a time
// per server (409 otherwise). Starting and reading jobs needs the admin role
// on the project, or member on a personal project (entity = caller).

// The routes are POST /api/v1/exports, GET /api/v1/exports/{job_id} and
// GET /api/v1/exports/{job_id}/artifacts/{target} (native.go).

func init() {
	startExtensions = append(startExtensions, startBulkExport)
}

var bulkExportTargets = map[string]bool{"calls": true, "objects": true, "feedback": true}

const (
	bulkExportPage   = 500
	bulkExportChunk  = 1 << 20
	bulkExportFormat = "jsonl.gz"
)

type bulkExportState struct {
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

var bulkExportStates sync.Map // *Server -> *bulkExportState

func startBulkExport(s *Server) func() {
	ctx, cancel := context.WithCancel(context.Background())
	st := &bulkExportState{ctx: ctx, cancel: cancel}
	bulkExportStates.Store(s, st)
	if err := s.store.FailInterruptedExportJobs(ctx); err != nil {
		s.log.Warn("bulk export: marking interrupted jobs failed", "err", err)
	}
	return func() {
		cancel()
		st.wg.Wait()
		bulkExportStates.Delete(s)
	}
}

func (s *Server) bulkExportState() *bulkExportState {
	if v, ok := bulkExportStates.Load(s); ok {
		return v.(*bulkExportState)
	}
	return nil
}

// exportProblem writes ExportErrorResponse ({"detail": JobFailure}).
func exportProblem(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"detail": store.ExportFailure{Code: code, Message: msg}})
}

// exportAuthProblem writes ExportAuthorizationErrorResponse ({"detail": "..."}).
func exportAuthProblem(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"detail": msg})
}

// validationProblem writes an HTTPValidationError (422).
func validationProblem(w http.ResponseWriter, loc []any, msg, typ string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnprocessableEntity)
	_ = json.NewEncoder(w).Encode(map[string]any{"detail": []any{map[string]any{"loc": loc, "msg": msg, "type": typ}}})
}

// exportProject resolves and authorizes the {entity}/{project} of the path.
func (s *Server) exportProject(w http.ResponseWriter, r *http.Request) (string, bool) {
	ent, proj := chi.URLParam(r, "entity"), chi.URLParam(r, "project")
	pid, err := project(ent + "/" + proj)
	if err != nil {
		exportProblem(w, http.StatusBadRequest, "invalid_project", err.Error())
		return "", false
	}
	role := callerRole(r, pid)
	personal := false
	if u := userFrom(r, nil); u != nil && *u == ent {
		personal = true
	}
	if role.Rank() < store.RoleAdmin.Rank() && !(personal && role.Rank() >= store.RoleMember.Rank()) {
		exportAuthProblem(w, http.StatusForbidden, fmt.Sprintf("exporting %s needs the admin role on the project (or member on your personal project)", pid))
		return "", false
	}
	return pid, true
}

func exportName(pid, jobID string) string {
	ent, proj, _ := strings.Cut(pid, "/")
	return "entities/" + ent + "/projects/" + proj + "/exports/" + jobID
}

func (s *Server) bulkExportStart(w http.ResponseWriter, r *http.Request) {
	pid, ok := s.exportProject(w, r)
	if !ok {
		return
	}
	body, err := otlp.ReadBody(http.MaxBytesReader(nil, r.Body, 1<<20), r.Header.Get("Content-Encoding"))
	if err != nil {
		validationProblem(w, []any{"body"}, "could not read the request body", "value_error")
		return
	}
	var req struct {
		Targets *[]string `json:"targets"`
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields() // ExportBody has additionalProperties: false
	if err := dec.Decode(&req); err != nil {
		msg := "invalid JSON body"
		if strings.Contains(err.Error(), "unknown field") {
			msg = "Extra inputs are not permitted: " + strings.TrimPrefix(err.Error(), "json: ")
		}
		validationProblem(w, []any{"body"}, msg, "value_error")
		return
	}
	if req.Targets == nil {
		validationProblem(w, []any{"body", "targets"}, "Field required", "missing")
		return
	}
	if n := len(*req.Targets); n < 1 || n > 3 {
		validationProblem(w, []any{"body", "targets"}, "targets must list 1 to 3 of calls, objects, feedback", "value_error")
		return
	}
	var targets []string
	seen := map[string]bool{}
	for i, t := range *req.Targets {
		if !bulkExportTargets[t] {
			validationProblem(w, []any{"body", "targets", i}, "Input should be 'calls', 'objects' or 'feedback'", "literal_error")
			return
		}
		if seen[t] {
			exportProblem(w, http.StatusBadRequest, "duplicate_target", fmt.Sprintf("target %q is listed twice", t))
			return
		}
		seen[t] = true
		targets = append(targets, t)
	}
	st := s.bulkExportState()
	if st == nil {
		exportProblem(w, http.StatusServiceUnavailable, "unavailable", "the export runner is not running")
		return
	}
	job := &store.ExportJob{JobID: uuid.NewString(), ProjectID: pid, Targets: targets, CreatedBy: userFrom(r, nil)}
	if err := s.store.CreateExportJob(r.Context(), job); err != nil {
		if errors.Is(err, store.ErrExportBusy) {
			exportProblem(w, http.StatusConflict, "export_in_progress", err.Error())
			return
		}
		writeErr(w, err)
		return
	}
	base := requestBaseURL(r)
	st.wg.Add(1)
	go func() {
		defer st.wg.Done()
		s.runBulkExport(st.ctx, job, base)
	}()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Location", "/api/v1/exports/"+job.JobID+"?"+url.Values{"project_id": {pid}}.Encode())
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"name": exportName(pid, job.JobID), "job_id": job.JobID, "created_at": job.CreatedAt.Format(time.RFC3339Nano),
	})
}

func (s *Server) bulkExportStatus(w http.ResponseWriter, r *http.Request) {
	pid, ok := s.exportProject(w, r)
	if !ok {
		return
	}
	jobID := chi.URLParam(r, "job_id")
	if _, err := uuid.Parse(jobID); err != nil {
		validationProblem(w, []any{"path", "job_id"}, "Input should be a valid UUID", "uuid_parsing")
		return
	}
	job, err := s.store.GetExportJob(r.Context(), pid, jobID)
	if errors.Is(err, store.ErrNotFound) {
		exportProblem(w, http.StatusNotFound, "not_found", "export job not found for this project")
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	arts := []store.ExportArtifact{}
	if job.State == store.ExportSucceeded { // partial output is never returned
		arts = job.Artifacts
	}
	var failure any
	if job.State == store.ExportFailed && job.Failure != nil {
		failure = job.Failure
	}
	writeJSON(w, map[string]any{
		"name": exportName(pid, job.JobID), "job_id": job.JobID,
		"created_at": job.CreatedAt.Format(time.RFC3339Nano), "updated_at": job.UpdatedAt.Format(time.RFC3339Nano),
		"state": job.State, "artifacts": arts, "failure": failure, "targets": job.Targets,
	})
}

func (s *Server) bulkExportDownload(w http.ResponseWriter, r *http.Request) {
	pid, ok := s.exportProject(w, r)
	if !ok {
		return
	}
	job, err := s.store.GetExportJob(r.Context(), pid, chi.URLParam(r, "job_id"))
	if err != nil || job.State != store.ExportSucceeded {
		exportProblem(w, http.StatusNotFound, "not_found", "no finished export artifact here")
		return
	}
	target := chi.URLParam(r, "target")
	var art *store.ExportArtifact
	for i := range job.Artifacts {
		if job.Artifacts[i].Target == target {
			art = &job.Artifacts[i]
		}
	}
	if art == nil {
		exportProblem(w, http.StatusNotFound, "not_found", "this export has no "+target+" artifact")
		return
	}
	filename := fmt.Sprintf("%s-%s-%s.%s", filenameSlug(pid), filenameSlug(target), filenameSlug(job.JobID[:min(8, len(job.JobID))]), bulkExportFormat)
	if art.Location != "" {
		backend := s.store.FileBackend()
		if p, ok := backend.(store.FilePresigner); ok {
			u, _, err := p.PresignGet(art.Location, defaultPresignTTL)
			if err != nil {
				writeErr(w, err)
				return
			}
			http.Redirect(w, r, u, http.StatusFound)
			return
		}
		if backend == nil {
			exportProblem(w, http.StatusServiceUnavailable, "storage_unavailable", "the artifact is in a files backend that is not configured")
			return
		}
		b, err := backend.Get(r.Context(), art.Location)
		if err != nil {
			writeErr(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/gzip")
		w.Header().Set("Content-Disposition", attachment(filename))
		_, _ = w.Write(b)
		return
	}
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", attachment(filename))
	w.Header().Set("Cache-Control", "no-store")
	if art.SizeBytes > 0 {
		w.Header().Set("Content-Length", fmt.Sprint(art.SizeBytes))
	}
	err = s.store.ExportChunks(r.Context(), job.JobID, target, func(b []byte) error {
		_, err := w.Write(b)
		return err
	})
	if err != nil {
		s.log.Warn("bulk export download stopped", "job", job.JobID, "err", err)
	}
}

// readerPutter is implemented by files backends that upload from a stream.
type readerPutter interface {
	PutReader(ctx context.Context, key string, r io.ReadSeeker, size int64, contentType string) error
}

// runBulkExport writes every target and records the outcome.
func (s *Server) runBulkExport(ctx context.Context, job *store.ExportJob, base string) {
	start := time.Now()
	cutoff := job.CreatedAt.UnixMicro()
	var arts []store.ExportArtifact
	var failure *store.ExportFailure
	for _, t := range job.Targets {
		art, err := s.exportTarget(ctx, job, t, cutoff, base)
		if err != nil {
			code := "export_failed"
			if errors.Is(err, context.Canceled) {
				code = "interrupted"
			}
			failure = &store.ExportFailure{Code: code, Message: fmt.Sprintf("exporting %s: %v", t, err)}
			break
		}
		arts = append(arts, *art)
	}
	state := store.ExportSucceeded
	if failure != nil {
		state = store.ExportFailed
		arts = nil
		if err := s.store.DeleteExportChunks(context.Background(), job.JobID); err != nil {
			s.log.Warn("bulk export: deleting the chunks of a failed job", "job", job.JobID, "project", job.ProjectID, "err", weftlog.Err(err))
		}
	}
	if err := s.store.FinishExportJob(context.Background(), job.JobID, state, arts, failure); err != nil {
		s.log.Error("bulk export: recording the result", "job", job.JobID, "err", err)
	}
	s.log.Info("bulk export finished", "job", job.JobID, "project", job.ProjectID, "status", state, "duration_ms", time.Since(start).Milliseconds())
}

// exportTarget writes one target to a temporary gzip JSONL file, then stores it.
func (s *Server) exportTarget(ctx context.Context, job *store.ExportJob, target string, cutoff int64, base string) (*store.ExportArtifact, error) {
	f, err := os.CreateTemp("", "weft-export-*.jsonl.gz")
	if err != nil {
		return nil, err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	gz := gzip.NewWriter(f)
	enc := json.NewEncoder(gz)
	enc.SetEscapeHTML(false)
	art := &store.ExportArtifact{Target: target, Format: bulkExportFormat}
	pid := job.ProjectID
	switch target {
	case "calls":
		pin := map[string]any{"$lte": []any{
			map[string]any{"$getField": "started_at"}, map[string]any{"$literal": store.FormatMicros(cutoff)},
		}}
		q := store.CallsQuery{ProjectID: pid, IncludeCosts: true, IncludeStorageSize: true, Query: andQuery(nil, pin),
			SortBy: []store.SortBy{{Field: "started_at", Direction: "asc"}}}
		if art.ExpectedRows, _, err = s.store.CountCalls(ctx, pid, nil, q.Query, ""); err != nil {
			return nil, err
		}
		err = forEachCallPage(ctx, s.store, q, 0, -1, func(page []*store.Call) error {
			for _, c := range page {
				if err := enc.Encode(c); err != nil {
					return err
				}
				art.WrittenRows++
			}
			return ctx.Err()
		})
	case "objects":
		if art.ExpectedRows, err = s.store.CountObjectVersions(ctx, pid, cutoff); err != nil {
			return nil, err
		}
		for off := 0; ; off += bulkExportPage {
			page, perr := s.store.ExportObjectsPage(ctx, pid, cutoff, bulkExportPage, off)
			if perr != nil {
				err = perr
				break
			}
			for _, o := range page {
				if err = enc.Encode(o); err != nil {
					break
				}
				art.WrittenRows++
			}
			if err != nil || len(page) < bulkExportPage {
				break
			}
			if err = ctx.Err(); err != nil {
				break
			}
		}
	case "feedback":
		if art.ExpectedRows, err = s.store.CountFeedbackBefore(ctx, pid, cutoff); err != nil {
			return nil, err
		}
		for off := 0; ; off += bulkExportPage {
			page, perr := s.store.ExportFeedbackPage(ctx, pid, cutoff, bulkExportPage, off)
			if perr != nil {
				err = perr
				break
			}
			for _, fb := range page {
				if err = enc.Encode(fb); err != nil {
					break
				}
				art.WrittenRows++
			}
			if err != nil || len(page) < bulkExportPage {
				break
			}
			if err = ctx.Err(); err != nil {
				break
			}
		}
	default:
		return nil, fmt.Errorf("unknown target %q", target)
	}
	if err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	art.SizeBytes = info.Size()
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	// Files backend (S3): upload under the project prefix.
	if backend := s.store.FileBackend(); backend != nil {
		if up, ok := backend.(readerPutter); ok {
			key := backend.Key(pid + "/exports/" + job.JobID + "/" + target + "." + bulkExportFormat)
			if err := up.PutReader(ctx, key, f, art.SizeBytes, "application/gzip"); err != nil {
				return nil, err
			}
			art.Location, art.URI = key, backend.URI(key)
			return art, nil
		}
	}
	// SQLite: chunks served by the download route.
	buf := make([]byte, bulkExportChunk)
	for seq := 0; ; seq++ {
		n, rerr := io.ReadFull(f, buf)
		if n > 0 {
			if err := s.store.PutExportChunk(ctx, job.JobID, pid, target, seq, append([]byte(nil), buf[:n]...)); err != nil {
				return nil, err
			}
		}
		if rerr == io.EOF || rerr == io.ErrUnexpectedEOF {
			break
		}
		if rerr != nil {
			return nil, rerr
		}
	}
	art.URI = base + "/api/v1/exports/" + job.JobID + "/artifacts/" + target + "?" + url.Values{"project_id": {job.ProjectID}}.Encode()
	return art, nil
}
