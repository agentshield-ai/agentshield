package compat

import (
	"errors"
	"net/http"

	"github.com/agentshield-ai/agentshield/internal/weft/refs"
	"github.com/agentshield-ai/agentshield/internal/weft/store"
	"github.com/go-chi/chi/v5"
)

func asInvalid(err error, target **store.InvalidError) bool { return errors.As(err, target) }

// completeCall is one item of POST /v2/{entity}/{project}/calls/complete.
type completeCall struct {
	ID          string         `json:"id"`
	ProjectID   string         `json:"project_id"`
	OpName      string         `json:"op_name"`
	DisplayName *string        `json:"display_name"`
	TraceID     string         `json:"trace_id"`
	ParentID    *string        `json:"parent_id"`
	ThreadID    *string        `json:"thread_id"`
	TurnID      *string        `json:"turn_id"`
	StartedAt   string         `json:"started_at"`
	EndedAt     string         `json:"ended_at"`
	Attributes  map[string]any `json:"attributes"`
	Inputs      map[string]any `json:"inputs"`
	Output      any            `json:"output"`
	Summary     map[string]any `json:"summary"`
	Exception   *string        `json:"exception"`
	// The client sends wb_user_id, wb_run_id, wb_run_step and
	// wb_run_step_end; the middleware has renamed them by now.
	UserID *string `json:"user_id"`
	RunID  *string `json:"run_id"`

	// Run steps (store/callfields.go).
	RunStep    *int64 `json:"run_step"`
	RunStepEnd *int64 `json:"run_step_end"`
}

// callsComplete ingests finished calls in one request (the client's
// calls_complete write path). The client's summaries already include
// descendants, so they are stored as cumulative.
func (a *Adapter) callsComplete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Batch []completeCall `json:"batch"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	pathProject := chi.URLParam(r, "entity") + "/" + chi.URLParam(r, "project")
	calls := make([]store.CompleteCall, 0, len(req.Batch))
	for _, c := range req.Batch {
		pid := c.ProjectID
		if pid == "" {
			pid = pathProject
		}
		st := store.CallStart{ProjectID: pid, ID: c.ID, OpName: c.OpName, DisplayName: c.DisplayName, TraceID: c.TraceID,
			ParentID: c.ParentID, ThreadID: c.ThreadID, TurnID: c.TurnID, StartedAt: c.StartedAt, Attributes: c.Attributes,
			Inputs: c.Inputs, UserID: a.host.User(r, c.UserID), RunID: c.RunID, RunStep: c.RunStep}
		en := &store.CallEnd{ProjectID: pid, ID: c.ID, EndedAt: c.EndedAt, Exception: c.Exception, Output: c.Output,
			Summary: c.Summary, Cumulative: true, RunStepEnd: c.RunStepEnd}
		calls = append(calls, store.CompleteCall{Start: st, End: en})
	}
	ended, err := a.host.Store().UpsertComplete(r.Context(), calls)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	a.host.CallsEnded(ended)
	writeJSON(w, map[string]any{})
}

// projectsInfo maps external project ids to internal ones. Weft uses the
// external "entity/project" id directly, so the mapping is the identity.
func (a *Adapter) projectsInfo(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProjectIDs []string `json:"project_ids"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	out := make([]map[string]string, 0, len(req.ProjectIDs))
	for _, p := range req.ProjectIDs {
		pid, err := refs.NormalizeProject(p)
		if err != nil {
			continue
		}
		out = append(out, map[string]string{"external_project_id": p, "internal_project_id": pid})
	}
	writeJSON(w, out)
}
