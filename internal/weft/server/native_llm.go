package server

import (
	"context"
	"net/http"
	"time"

	"github.com/agentshield-ai/agentshield/internal/weft/evals"
	"github.com/agentshield-ai/agentshield/internal/weft/store"
)

// Model calls, evaluations and leaderboards with Weft's names.

type completionNativeReq struct {
	ProjectID string         `json:"project_id"`
	Inputs    map[string]any `json:"inputs"`
	// Track records the request as a span (default true).
	Track *bool `json:"track"`
}

// v1Completions runs a chat completion; the response is OpenAI chat
// completion shaped ({"error": ...} when the provider failed), span_id is
// the recorded span.
func (s *Server) v1Completions(ctx context.Context, r *http.Request, req *completionNativeReq) (any, error) {
	out, err := s.completionsCreate(ctx, r, &completionsReq{ProjectID: req.ProjectID, Inputs: req.Inputs, TrackLLMCall: req.Track})
	if err != nil {
		return nil, err
	}
	m := out.(map[string]any)
	res := map[string]any{"response": m["response"], "span_id": nil}
	if id, ok := m["call_id"]; ok {
		res["span_id"] = id
	}
	return res, nil
}

// v1CompletionsStream streams OpenAI chunk objects as NDJSON; the last line
// is {"_meta": {"span_id": ...}}.
func (s *Server) v1CompletionsStream(w http.ResponseWriter, r *http.Request) {
	var req completionNativeReq
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	pid, err := project(req.ProjectID)
	if err != nil {
		writeErr(w, err)
		return
	}
	if req.Inputs == nil {
		writeErr(w, store.Invalidf("inputs are required"))
		return
	}
	out := newNDJSON(w)
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	started := time.Now()
	final, streamErr := s.llm.Stream(r.Context(), pid, req.Inputs, func(ch map[string]any) error {
		if err := out.write(ch); err != nil {
			return err
		}
		out.flush()
		return nil
	})
	if streamErr != nil {
		_ = out.write(map[string]any{"error": streamErr.Error()})
	}
	meta := map[string]any{"span_id": nil}
	if req.Track == nil || *req.Track {
		id, err := s.engine.RecordStreamedCompletion(context.WithoutCancel(r.Context()), pid, req.Inputs, started, final, streamErr)
		if err == nil {
			meta["span_id"] = id
		}
	}
	_ = out.write(map[string]any{"_meta": meta})
	out.flush()
}

func (s *Server) v1Embeddings(ctx context.Context, r *http.Request, req *embeddingsReq) (any, error) {
	return s.embeddingsCreate(ctx, r, req)
}

type imageNativeReq struct {
	ProjectID string         `json:"project_id"`
	Inputs    map[string]any `json:"inputs"`
	UserID    *string        `json:"user_id"`
	Track     *bool          `json:"track"`
}

func (s *Server) v1Images(ctx context.Context, r *http.Request, req *imageNativeReq) (any, error) {
	out, err := s.imageCreate(ctx, r, &imageCreateReq{ProjectID: req.ProjectID, Inputs: req.Inputs, UserID: req.UserID, TrackLLMCall: req.Track})
	if err != nil {
		return nil, err
	}
	m := out.(map[string]any)
	return map[string]any{"response": m["response"], "span_id": m["call_id"]}, nil
}

type evaluationRunReq struct {
	ProjectID     string  `json:"project_id"`
	EvaluationRef string  `json:"evaluation_ref"`
	ModelRef      string  `json:"model_ref"`
	UserID        *string `json:"user_id"`
}

// v1EvaluationRun starts a server-side evaluation in the background; id is
// its Evaluation.evaluate span.
func (s *Server) v1EvaluationRun(ctx context.Context, r *http.Request, req *evaluationRunReq) (any, error) {
	id, err := s.engine.EvaluateModel(ctx, req.ProjectID, req.EvaluationRef, req.ModelRef, userFrom(r, req.UserID))
	if err != nil {
		return nil, err
	}
	return map[string]any{"id": id}, nil
}

func (s *Server) v1EvaluationStatus(w http.ResponseWriter, r *http.Request) {
	pid, err := project(r.URL.Query().Get("project_id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	st, err := s.engine.EvaluationStatus(r.Context(), pid, pathParam(r, "id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, st)
}

type evaluationsCompareReq struct {
	ProjectID string   `json:"project_id"`
	IDs       []string `json:"ids"`
}

// Comparison of evaluation runs with Weft's names (evals.Comparison).
type nativeComparison struct {
	Evaluations []nativeCompareEvaluation `json:"evaluations"`
	Metrics     []evals.CompareMetric     `json:"metrics"`
	Rows        []nativeCompareRow        `json:"rows"`
}

type nativeCompareEvaluation struct {
	ID             string   `json:"id"`
	DisplayName    *string  `json:"display_name"`
	EvaluationRef  string   `json:"evaluation_ref"`
	EvaluationName string   `json:"evaluation_name"`
	ModelRef       string   `json:"model_ref"`
	ModelName      string   `json:"model_name"`
	StartedAt      string   `json:"started_at"`
	EndedAt        *string  `json:"ended_at"`
	Status         string   `json:"status"`
	Summary        any      `json:"summary"`
	DatasetRef     *string  `json:"dataset_ref"`
	Scorers        []string `json:"scorers"`
	TotalTokens    float64  `json:"total_tokens"`
	CostUSD        float64  `json:"cost_usd"`
	RowCount       int      `json:"row_count"`
}

type nativeCompareRow struct {
	RowKey  string                              `json:"row_key"`
	Inputs  any                                 `json:"inputs"`
	Results map[string][]nativeCompareRowResult `json:"results"`
}

type nativeCompareRowResult struct {
	SpanID       string         `json:"span_id"`
	Output       any            `json:"output"`
	Scores       map[string]any `json:"scores"`
	ModelLatency any            `json:"model_latency"`
	TotalTokens  float64        `json:"total_tokens"`
	Trial        int            `json:"trial"`
}

// v1EvaluationsCompare compares evaluation runs (their Evaluation.evaluate
// spans): metrics are keyed by run id, row results by run id, each with
// the span of its predict_and_score step.
func (s *Server) v1EvaluationsCompare(ctx context.Context, _ *http.Request, req *evaluationsCompareReq) (any, error) {
	if len(req.IDs) == 0 || len(req.IDs) > 50 {
		return nil, store.Invalidf("ids must list 1 to 50 evaluation runs")
	}
	c, err := s.engine.Compare(ctx, req.ProjectID, req.IDs)
	if err != nil {
		return nil, err
	}
	out := nativeComparison{Evaluations: []nativeCompareEvaluation{}, Metrics: c.Metrics, Rows: []nativeCompareRow{}}
	for _, e := range c.Evaluations {
		out.Evaluations = append(out.Evaluations, nativeCompareEvaluation{ID: e.CallID, DisplayName: e.DisplayName,
			EvaluationRef: e.EvaluationRef, EvaluationName: e.EvaluationName, ModelRef: e.ModelRef, ModelName: e.ModelName,
			StartedAt: e.StartedAt, EndedAt: e.EndedAt, Status: e.Status, Summary: e.Summary, DatasetRef: e.DatasetRef,
			Scorers: e.Scorers, TotalTokens: e.TotalTokens, CostUSD: e.CostUSD, RowCount: e.RowCount})
	}
	for _, row := range c.Rows {
		nr := nativeCompareRow{RowKey: row.RowKey, Inputs: row.Inputs, Results: map[string][]nativeCompareRowResult{}}
		for run, results := range row.Results {
			for _, x := range results {
				nr.Results[run] = append(nr.Results[run], nativeCompareRowResult{SpanID: x.CallID, Output: x.Output,
					Scores: x.Scores, ModelLatency: x.ModelLatency, TotalTokens: x.TotalTokens, Trial: x.Trial})
			}
		}
		out.Rows = append(out.Rows, nr)
	}
	return out, nil
}

type leaderboardNativeReq struct {
	ProjectID string `json:"project_id"`
	Name      string `json:"name"`
	Version   string `json:"version"`
}

type nativeLeaderboardCell struct {
	Value           any    `json:"value"`
	EvaluationRunID string `json:"evaluation_run_id"`
	StartedAt       string `json:"started_at"`
}

// v1LeaderboardCompute computes a Leaderboard object's table: one row per
// model, each cell the most recent finished run of that column's
// evaluation.
func (s *Server) v1LeaderboardCompute(ctx context.Context, _ *http.Request, req *leaderboardNativeReq) (any, error) {
	lb, err := s.engine.ComputeLeaderboard(ctx, req.ProjectID, req.Name, req.Version)
	if err != nil {
		return nil, err
	}
	obj, err := objectFrom(lb.Leaderboard, true)
	if err != nil {
		return nil, err
	}
	rows := make([]map[string]any, 0, len(lb.Rows))
	for _, row := range lb.Rows {
		vals := map[string]nativeLeaderboardCell{}
		for k, c := range row.Values {
			vals[k] = nativeLeaderboardCell{Value: c.Value, EvaluationRunID: c.EvaluationCallID, StartedAt: c.StartedAt}
		}
		rows = append(rows, map[string]any{"model_ref": row.ModelRef, "model_name": row.ModelName, "values": vals})
	}
	return map[string]any{"leaderboard": obj, "columns": lb.Columns, "rows": rows}, nil
}
