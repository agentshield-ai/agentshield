package evals

import (
	"context"
	"math/rand/v2"
	"time"

	"github.com/agentshield-ai/agentshield/internal/weft/refs"
	"github.com/agentshield-ai/agentshield/internal/weft/store"
)

type monitorCacheEntry struct {
	at       time.Time
	monitors []*store.Obj
}

// Notify queues ended calls for monitor evaluation. It never blocks.
func (e *Engine) Notify(calls []store.EndedCall) {
	for _, c := range calls {
		select {
		case e.monitorCh <- c:
		default:
			e.Log.Warn("monitor queue full; dropping call", "call_id", c.ID)
			return
		}
	}
}

// InvalidateMonitors drops cached monitors for a project.
func (e *Engine) InvalidateMonitors(projectID string) {
	e.monMu.Lock()
	delete(e.monitorCache, projectID)
	e.monMu.Unlock()
	e.invalidateSignals(projectID)
}

func (e *Engine) activeMonitors(ctx context.Context, projectID string) ([]*store.Obj, error) {
	e.monMu.Lock()
	ent, ok := e.monitorCache[projectID]
	e.monMu.Unlock()
	if ok && time.Since(ent.at) < 10*time.Second {
		return ent.monitors, nil
	}
	objs, err := e.Store.QueryObjects(ctx, store.ObjsQuery{ProjectID: projectID,
		Filter: &store.ObjsFilter{BaseObjectClasses: []string{"Monitor"}, LatestOnly: true}})
	if err != nil {
		return nil, err
	}
	var active []*store.Obj
	for _, o := range objs {
		if v, ok := o.Val.(map[string]any); ok {
			if a, _ := v["active"].(bool); a {
				active = append(active, o)
			}
		}
	}
	e.monMu.Lock()
	e.monitorCache[projectID] = monitorCacheEntry{at: time.Now(), monitors: active}
	e.monMu.Unlock()
	return active, nil
}

func (e *Engine) monitorWorker() {
	defer e.wg.Done()
	for {
		select {
		case <-e.stop:
			return
		case c := <-e.monitorCh:
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			if err := e.runMonitors(ctx, c); err != nil {
				e.Log.Warn("monitor evaluation failed", "call_id", c.ID, "err", err)
			}
			if err := e.runAutoSignals(ctx, c); err != nil {
				e.Log.Warn("signal evaluation failed", "call_id", c.ID, "err", err)
			}
			cancel()
		}
	}
}

func isInternal(c *store.Call) bool {
	if w, ok := c.Attributes["weft"].(map[string]any); ok {
		if v, _ := w["internal"].(bool); v {
			return true
		}
	}
	return false
}

func (e *Engine) runMonitors(ctx context.Context, ended store.EndedCall) error {
	monitors, err := e.activeMonitors(ctx, ended.ProjectID)
	if err != nil || len(monitors) == 0 {
		return err
	}
	call, err := e.Store.GetCall(ctx, ended.ProjectID, ended.ID, false, false, false)
	if err != nil {
		return err
	}
	if isInternal(call) || call.EndedAt == nil {
		return nil
	}
	for _, m := range monitors {
		val := m.Val.(map[string]any)
		if !monitorMatchesOp(val, call) {
			continue
		}
		if q, ok := val["query"]; ok && q != nil {
			match, err := e.Store.CallMatches(ctx, ended.ProjectID, call.ID, q)
			if err != nil {
				e.Log.Warn("monitor query invalid", "monitor", m.ObjectID, "err", err)
				continue
			}
			if !match {
				continue
			}
		}
		rate := 1.0
		if r, ok := store.ToFloat(val["sampling_rate"]); ok {
			rate = r
		}
		if rate <= 0 || (rate < 1 && rand.Float64() >= rate) {
			continue
		}
		scorers, _ := val["scorers"].([]any)
		for _, s := range scorers {
			sr, ok := s.(string)
			if !ok {
				continue
			}
			if err := e.scoreForMonitor(ctx, m, sr, call); err != nil {
				e.Log.Warn("monitor scorer failed", "monitor", m.ObjectID, "scorer", sr, "err", err)
			}
		}
	}
	return nil
}

func monitorMatchesOp(val map[string]any, call *store.Call) bool {
	ops, _ := val["op_names"].([]any)
	if len(ops) == 0 {
		return call.ParentID == nil
	}
	for _, o := range ops {
		s, _ := o.(string)
		if s == call.OpName || store.OpBaseName(s) == call.OpBaseName() {
			return true
		}
	}
	return false
}

// ScoreResult is the outcome of scoring one call on demand.
type ScoreResult struct {
	CallID       string `json:"call_id"`
	Scorer       string `json:"scorer"`
	Output       any    `json:"output"`
	ScorerCallID string `json:"scorer_call_id,omitempty"`
	FeedbackID   string `json:"feedback_id,omitempty"`
	Error        string `json:"error,omitempty"`
}

// ScoreCalls runs server-runnable scorers on existing calls and stores the
// results as weft.scorer.<scorer> feedback.
func (e *Engine) ScoreCalls(ctx context.Context, projectID string, callIDs, scorerRefs []string, user *string) ([]ScoreResult, error) {
	if len(callIDs) == 0 || len(callIDs) > 100 {
		return nil, store.Invalidf("call_ids must list 1 to 100 calls")
	}
	if len(scorerRefs) == 0 || len(scorerRefs) > 10 {
		return nil, store.Invalidf("scorer_refs must list 1 to 10 scorers")
	}
	scorers := make([]*Scorer, 0, len(scorerRefs))
	for _, r := range scorerRefs {
		sc, err := e.loadScorer(ctx, r)
		if err != nil {
			return nil, err
		}
		scorers = append(scorers, sc)
	}
	var out []ScoreResult
	for _, id := range callIDs {
		call, err := e.Store.GetCall(ctx, projectID, id, false, false, false)
		if err != nil {
			return nil, store.Invalidf("call %q not found", id)
		}
		for _, sc := range scorers {
			res := ScoreResult{CallID: id, Scorer: sc.Name}
			example := map[string]any{}
			for k, v := range call.Inputs {
				example[k] = v
			}
			attrs := map[string]any{"weft": map[string]any{"internal": true, "scored_call_id": id}}
			score, rec, err := e.RunScorer(ctx, projectID, sc, example, call.Output, nil, attrs)
			if rec != nil {
				res.ScorerCallID = rec.id
			}
			if err != nil {
				res.Error = err.Error()
				out = append(out, res)
				continue
			}
			res.Output = jsonSafe(score)
			callRef := refs.CallRef(projectID, rec.id)
			fb, err := e.Store.CreateFeedback(ctx, store.FeedbackCreate{
				ProjectID: projectID, Ref: refs.CallRef(projectID, id), FeedbackType: "weft.scorer." + sc.Name,
				Payload: map[string]any{"output": res.Output}, WBUserID: user, RunnableRef: &sc.Ref, CallRef: &callRef,
			})
			if err != nil {
				return nil, err
			}
			res.FeedbackID = fb.ID
			out = append(out, res)
		}
	}
	return out, nil
}

func (e *Engine) scoreForMonitor(ctx context.Context, monitor *store.Obj, scorerRef string, call *store.Call) error {
	sc, err := e.loadScorer(ctx, scorerRef)
	if err != nil {
		return err
	}
	example := map[string]any{}
	for k, v := range call.Inputs {
		example[k] = v
	}
	monitorRef := monitor.Ref()
	attrs := map[string]any{"weft": map[string]any{"internal": true, "monitor": monitorRef, "scored_call_id": call.ID}}
	score, rec, err := e.RunScorer(ctx, call.ProjectID, sc, example, call.Output, nil, attrs)
	if err != nil {
		return err
	}
	callRef := refs.CallRef(call.ProjectID, rec.id)
	creator := "monitor:" + monitor.ObjectID
	_, err = e.Store.CreateFeedback(ctx, store.FeedbackCreate{
		ProjectID: call.ProjectID, Ref: refs.CallRef(call.ProjectID, call.ID), FeedbackType: "weft.scorer." + sc.Name,
		Payload: map[string]any{"output": jsonSafe(score)}, Creator: &creator,
		RunnableRef: &sc.Ref, CallRef: &callRef, TriggerRef: &monitorRef,
	})
	return err
}
