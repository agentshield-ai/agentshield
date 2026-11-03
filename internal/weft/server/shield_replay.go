package server

// Replay routes (docs/weft/DESIGN.md §3, §5.4):
//
//	POST /api/v1/shield/replay            start a replay job -> {job_id, status}
//	GET  /api/v1/shield/replay            list jobs (results omitted)
//	GET  /api/v1/shield/replay/{job_id}   job status and the diff against the engine's verdicts

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/agentshield-ai/agentshield/internal/weft/shield"
	"github.com/agentshield-ai/agentshield/internal/weft/store"
	"github.com/go-chi/chi/v5"
)

func (s *Server) shieldReplayStart(ctx context.Context, r *http.Request, req *shield.ReplayRequest) (any, error) {
	pid, err := project(req.ProjectID)
	if err != nil {
		return nil, err
	}
	req.ProjectID = pid
	svc := s.shieldService()
	if svc.Mode() == "" {
		return nil, store.Invalidf("replay is not configured: start weft with --shield-rules-dir or --shield-url")
	}
	id, err := svc.Start(ctx, *req, userFrom(r, nil))
	if err != nil {
		return nil, err
	}
	return map[string]any{"job_id": id, "status": store.ReplayQueued}, nil
}

// replayJobView flattens a job and its result into one object.
func replayJobView(j *store.ReplayJob, withResult bool) map[string]any {
	out := map[string]any{}
	if withResult {
		_ = json.Unmarshal(j.Result, &out)
	}
	var req any
	_ = json.Unmarshal(j.Request, &req)
	out["job_id"] = j.ID
	out["project_id"] = j.ProjectID
	out["status"] = j.Status
	out["error"] = j.Error
	out["request"] = req
	out["created_by"] = j.CreatedBy
	out["created_at"] = j.CreatedAt
	out["started_at"] = j.StartedAt
	out["finished_at"] = j.FinishedAt
	if _, ok := out["label"]; !ok || out["label"] == "" {
		out["label"] = j.Label
	}
	return out
}

func (s *Server) shieldReplayGet(w http.ResponseWriter, r *http.Request) {
	shieldGet(func(r *http.Request) (any, error) {
		pid, err := shieldQueryProject(r)
		if err != nil {
			return nil, err
		}
		j, err := s.store.GetReplayJob(r.Context(), pid, chi.URLParam(r, "job_id"))
		if err != nil {
			return nil, err
		}
		return replayJobView(j, true), nil
	})(w, r)
}

func (s *Server) shieldReplayList(w http.ResponseWriter, r *http.Request) {
	shieldGet(func(r *http.Request) (any, error) {
		pid, err := shieldQueryProject(r)
		if err != nil {
			return nil, err
		}
		limit, err := shieldQueryInt(r, "limit", 50)
		if err != nil {
			return nil, err
		}
		offset, err := shieldQueryInt(r, "offset", 0)
		if err != nil {
			return nil, err
		}
		jobs, total, err := s.store.ListReplayJobs(r.Context(), pid, limit, offset)
		if err != nil {
			return nil, err
		}
		items := make([]map[string]any, 0, len(jobs))
		for i := range jobs {
			items = append(items, replayJobView(&jobs[i], false))
		}
		return map[string]any{"items": items, "total_count": total}, nil
	})(w, r)
}
