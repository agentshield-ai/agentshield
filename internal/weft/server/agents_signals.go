package server

import (
	"context"
	"net/http"
	"strings"
	"sync"

	"github.com/agentshield-ai/agentshield/internal/weft/evals"
	"github.com/agentshield-ai/agentshield/internal/weft/store"
	"github.com/go-chi/chi/v5"
)

// Agent signals (under /agents/*): turn-scoped tags and ratings scored by an
// LLM judge and stored as feedback with scorer fields (scorer_ratings,
// scorer_tags, confidences, reasons).

func init() {
	routeExtensions = append(routeExtensions, func(s *Server, r chi.Router) {
		r.Get("/agents/signals/presets", s.agentSignalPresets)
		r.Post("/agents/signals/presets", s.agentSignalPresets)
		r.Post("/agents/signals/run", handle(s.agentSignalsRun))
		r.Post("/agents/signals/query", handle(s.agentSignalsQuery))
		r.Post("/agents/feedback/batch/create", handle(s.agentFeedbackBatchCreate))
	})
}

func (s *Server) agentSignalPresets(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{"presets": store.SignalPresets})
}

func (s *Server) agentSignalsQuery(ctx context.Context, _ *http.Request, req *store.AgentSignalsQueryReq) (any, error) {
	return s.store.QuerySignalResults(ctx, *req)
}

type agentFeedbackBatchReq struct {
	Batch []store.AgentFeedbackItem `json:"batch"`
}

// agentFeedbackBatchCreate is /feedback/batch/create with the scorer and
// span fields of the batch schema kept (normalised) in the payload.
func (s *Server) agentFeedbackBatchCreate(ctx context.Context, r *http.Request, req *agentFeedbackBatchReq) (any, error) {
	if len(req.Batch) > 1000 {
		return nil, store.Invalidf("batch too large (max 1000)")
	}
	res := make([]any, 0, len(req.Batch))
	for _, it := range req.Batch {
		if _, err := project(it.ProjectID); err != nil {
			return nil, err
		}
		it.WBUserID = userFrom(r, it.WBUserID)
		f, err := s.store.CreateAgentFeedback(ctx, it)
		if err != nil {
			return nil, err
		}
		res = append(res, map[string]any{"id": f.ID, "created_at": f.CreatedAt, "wb_user_id": f.WBUserID, "payload": f.Payload})
	}
	return map[string]any{"res": res}, nil
}

type signalsRunReq struct {
	ProjectID      string             `json:"project_id"`
	Signals        []evals.SignalSpec `json:"signals"`
	Model          string             `json:"model"`
	AgentName      string             `json:"agent_name"`
	ConversationID string             `json:"conversation_id"`
	TraceIDs       []string           `json:"trace_ids"`
	StartedAfter   *string            `json:"started_after"`
	StartedBefore  *string            `json:"started_before"`
	Limit          int                `json:"limit"`
	Rescore        bool               `json:"rescore"`
}

func (s *Server) agentSignalsRun(ctx context.Context, r *http.Request, req *signalsRunReq) (any, error) {
	pid, err := project(req.ProjectID)
	if err != nil {
		return nil, err
	}
	if len(req.Signals) == 0 || len(req.Signals) > 12 {
		return nil, store.Invalidf("signals: give 1 to 12 signals")
	}
	if strings.TrimSpace(req.Model) == "" {
		return nil, store.Invalidf("model is required")
	}
	specs := make([]evals.SignalSpec, 0, len(req.Signals))
	seen := map[string]bool{}
	for _, sp := range req.Signals {
		sp, err := sp.Resolve()
		if err != nil {
			return nil, err
		}
		if seen[sp.ID] {
			return nil, store.Invalidf("signal %q given twice", sp.ID)
		}
		seen[sp.ID] = true
		specs = append(specs, sp)
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 200 {
		return nil, store.Invalidf("limit must be at most 200 turns")
	}
	turns, err := s.store.SignalTurns(ctx, pid, store.SignalTurnFilter{AgentName: req.AgentName, ConversationID: req.ConversationID,
		TraceIDs: req.TraceIDs, StartedAfter: req.StartedAfter, StartedBefore: req.StartedBefore, Limit: limit})
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(turns))
	for i, t := range turns {
		ids[i] = t.SpanID
	}
	type job struct {
		turn store.SignalTurn
		sp   evals.SignalSpec
	}
	var jobs []job
	skipped := 0
	for _, sp := range specs {
		done := map[string]bool{}
		if !req.Rescore && len(ids) > 0 {
			if done, err = s.store.SignalScoredTurns(ctx, pid, sp.ID, sp.Version(), ids); err != nil {
				return nil, err
			}
		}
		for _, t := range turns {
			if done[t.SpanID] {
				skipped++
				continue
			}
			jobs = append(jobs, job{t, sp})
		}
	}
	creator := "weft-signals"
	user := userFrom(r, nil)
	results := make([]evals.SignalRunResult, len(jobs))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i, j := range jobs {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, j job) {
			defer wg.Done()
			defer func() { <-sem }()
			results[i] = s.engine.ScoreTurnSignal(ctx, pid, j.turn, j.sp, req.Model, &creator, user)
		}(i, j)
	}
	wg.Wait()
	scored, failed := 0, 0
	for _, r := range results {
		if r.Error != "" {
			failed++
		} else {
			scored++
		}
	}
	return map[string]any{"turns": len(turns), "scored": scored, "skipped": skipped, "failed": failed, "results": results}, nil
}
