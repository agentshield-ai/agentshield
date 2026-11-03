package evals

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/agentshield-ai/agentshield/internal/weft/refs"
	"github.com/agentshield-ai/agentshield/internal/weft/store"
)

// Rescore re-runs server-runnable scorers over the predictions of an existing
// evaluation run (/evaluations/rescore). It records a new
// Evaluation.evaluate call whose predict_and_score children reuse the source
// run's examples and model outputs, so no model is called. It returns the new
// call id; progress is reported by EvaluationStatus.
func (e *Engine) Rescore(ctx context.Context, projectID, sourceRunID string, scorerRefs []string, userID *string) (string, error) {
	pid, err := refs.NormalizeProject(projectID)
	if err != nil {
		return "", &store.InvalidError{Msg: err.Error()}
	}
	if len(scorerRefs) == 0 {
		return "", store.Invalidf("scorer_refs must not be empty")
	}
	src, err := e.Store.GetCall(ctx, pid, sourceRunID, false, false, false)
	if errors.Is(err, store.ErrNotFound) {
		return "", store.Invalidf("evaluation run %q not found", sourceRunID)
	}
	if err != nil {
		return "", err
	}
	if src.OpBaseName() != "Evaluation.evaluate" {
		return "", store.Invalidf("call %q is not an evaluation run", sourceRunID)
	}
	var scorers []*Scorer
	for _, r := range scorerRefs {
		sc, err := e.loadScorer(ctx, r)
		if err != nil {
			return "", err
		}
		scorers = append(scorers, sc)
	}
	children, err := e.Store.ListCalls(ctx, store.CallsQuery{ProjectID: pid,
		Filter: &store.CallsFilter{ParentIDs: []string{src.ID}, OpNames: []string{"Evaluation.predict_and_score"}},
		SortBy: []store.SortBy{{Field: "started_at", Direction: "asc"}}})
	if err != nil {
		return "", err
	}
	name := "rescore"
	if src.DisplayName != nil {
		name = *src.DisplayName + " · rescore"
	}
	inputs := map[string]any{"self": src.Inputs["self"], "model": src.Inputs["model"]}
	attrs := map[string]any{"weft": map[string]any{"server_side": true, "rescore": true, "source_evaluation_run_id": src.ID}}
	root, err := e.startCall(ctx, pid, "Evaluation.evaluate", nil, inputs, attrs, &name)
	if err != nil {
		return "", err
	}
	_ = userID
	e.setStatus(root.id, func(s *Status) { s.TotalRows = len(children) })
	go e.runRescore(pid, root, src, children, scorers)
	return root.id, nil
}

func (e *Engine) runRescore(pid string, root *callRec, src *store.Call, children []*store.Call, scorers []*Scorer) {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Hour)
	defer cancel()
	// Resolve example refs once.
	var exRefs []string
	seen := map[string]bool{}
	for _, ch := range children {
		if s, ok := ch.Inputs["example"].(string); ok && refs.IsRef(s) && !seen[s] {
			seen[s] = true
			exRefs = append(exRefs, s)
		}
	}
	resolved := map[string]any{}
	if vals, err := e.Store.ReadRefs(ctx, exRefs); err == nil {
		for i, r := range exRefs {
			resolved[r] = vals[i]
		}
	}
	type result struct {
		scores  map[string]any
		latency float64
	}
	results := make([]result, len(children))
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for i, ch := range children {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, ch *store.Call) {
			defer wg.Done()
			defer func() { <-sem }()
			ex := ch.Inputs["example"]
			example, _ := ex.(map[string]any)
			if s, ok := ex.(string); ok {
				example, _ = resolved[s].(map[string]any)
			}
			if example == nil {
				example = map[string]any{"input": ex}
			}
			var output, latency any
			if out, ok := ch.Output.(map[string]any); ok {
				output, latency = out["output"], out["model_latency"]
			}
			pas, err := e.startCall(ctx, pid, "Evaluation.predict_and_score", root,
				map[string]any{"self": ch.Inputs["self"], "model": ch.Inputs["model"], "example": ex}, nil, nil)
			if err != nil {
				e.Log.Error("recording rescore predict_and_score", "err", err)
				return
			}
			scores := map[string]any{}
			for _, sc := range scorers {
				if score, _, err := e.RunScorer(ctx, pid, sc, example, output, pas, nil); err == nil {
					scores[sc.Name] = jsonSafe(score)
				}
			}
			_ = pas.end(ctx, map[string]any{"output": output, "scores": scores, "model_latency": latency}, nil, nil)
			lat, _ := store.ToFloat(latency)
			results[i] = result{scores, lat}
			e.setStatus(root.id, func(s *Status) { s.CompletedRows++ })
		}(i, ch)
	}
	wg.Wait()
	summary := map[string]any{}
	for _, sc := range scorers {
		var vals []any
		for _, r := range results {
			if v, ok := r.scores[sc.Name]; ok && v != nil {
				vals = append(vals, v)
			}
		}
		if s := Summarize(vals); s != nil {
			summary[sc.Name] = s
		}
	}
	if len(results) > 0 {
		var lat float64
		for _, r := range results {
			lat += r.latency
		}
		summary["model_latency"] = map[string]any{"mean": lat / float64(len(results))}
	}
	var endErr error
	if ctx.Err() != nil {
		endErr = fmt.Errorf("rescore of %s: %w", src.ID, ctx.Err())
	}
	if err := root.end(ctx, summary, nil, endErr); err != nil {
		e.Log.Error("ending rescore call", "err", err)
	}
	e.setStatus(root.id, func(s *Status) {
		s.Code = "complete"
		if endErr != nil {
			s.Code, s.Error = "failed", endErr.Error()
		}
	})
}
