package evals

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/agentshield-ai/agentshield/internal/weft/refs"
	"github.com/agentshield-ai/agentshield/internal/weft/store"
)

type monitorCacheEntry struct {
	at       time.Time
	monitors []*store.Obj
}

// Monitor work is shared fairly between projects: ended spans wait in one
// queue per project, workers take the projects in turn, and one project
// occupies at most monitorsPerProject of the monitorWorkers at a time, so a
// project whose monitors are slow (a slow judge, a costly query) delays
// only its own spans.
const (
	monitorWorkers         = 8
	monitorsPerProject     = 4
	monitorQueueLimit      = 10000 // spans waiting, all projects
	monitorProjectQueueCap = 5000  // spans waiting, one project
)

// monitorRunHook, when set (tests), runs before a span's monitors.
var monitorRunHook func(ctx context.Context, c store.EndedCall)

type monitorQueue struct {
	mu      sync.Mutex
	cond    *sync.Cond
	byProj  map[string][]store.EndedCall
	order   []string // projects with spans waiting, in turn order
	active  map[string]int
	total   int
	stopped bool
}

func (q *monitorQueue) init() {
	q.cond = sync.NewCond(&q.mu)
	q.byProj = map[string][]store.EndedCall{}
	q.active = map[string]int{}
}

// push queues c; false when the queue (or the project's share) is full.
func (q *monitorQueue) push(c store.EndedCall) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.stopped || q.total >= monitorQueueLimit || len(q.byProj[c.ProjectID]) >= monitorProjectQueueCap {
		return false
	}
	if len(q.byProj[c.ProjectID]) == 0 {
		q.order = append(q.order, c.ProjectID)
	}
	q.byProj[c.ProjectID] = append(q.byProj[c.ProjectID], c)
	q.total++
	q.cond.Signal()
	return true
}

// next waits for a span of a project that has a worker to spare and takes
// it; false once the queue is closed.
func (q *monitorQueue) next() (store.EndedCall, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for {
		if q.stopped {
			return store.EndedCall{}, false
		}
		for i, pid := range q.order {
			if q.active[pid] >= monitorsPerProject {
				continue
			}
			list := q.byProj[pid]
			c := list[0]
			list[0] = store.EndedCall{}
			list = list[1:]
			q.order = append(q.order[:i:i], q.order[i+1:]...)
			if len(list) == 0 {
				delete(q.byProj, pid)
			} else {
				q.byProj[pid] = list
				q.order = append(q.order, pid) // its next span waits for the other projects' turn
			}
			q.total--
			q.active[pid]++
			return c, true
		}
		q.cond.Wait()
	}
}

// done releases the worker next took for projectID.
func (q *monitorQueue) done(projectID string) {
	q.mu.Lock()
	if q.active[projectID]--; q.active[projectID] <= 0 {
		delete(q.active, projectID)
	}
	q.mu.Unlock()
	q.cond.Broadcast()
}

func (q *monitorQueue) close() {
	q.mu.Lock()
	q.stopped = true
	q.mu.Unlock()
	q.cond.Broadcast()
}

// Notify queues ended calls for monitor evaluation. It never blocks.
func (e *Engine) Notify(calls []store.EndedCall) {
	dropped := 0
	for _, c := range calls {
		if !e.monQ.push(c) {
			dropped++
		}
	}
	if dropped > 0 {
		e.Log.Warn("monitor queue full; dropping spans", "project", calls[0].ProjectID, "dropped", dropped)
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
		c, ok := e.monQ.next()
		if !ok {
			return
		}
		ctx, cancel := context.WithTimeout(e.baseCtx, 5*time.Minute)
		if monitorRunHook != nil {
			monitorRunHook(ctx, c)
		}
		if err := e.runMonitors(ctx, c); err != nil {
			e.Log.Warn("monitor evaluation failed", "project", c.ProjectID, "span_id", c.ID, "err", err)
		}
		if err := e.runAutoSignals(ctx, c); err != nil {
			e.Log.Warn("signal evaluation failed", "project", c.ProjectID, "span_id", c.ID, "err", err)
		}
		cancel()
		e.monQ.done(c.ProjectID)
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
	ctx = store.WithRefHome(ctx, ended.ProjectID) // monitors resolve refs in their own project
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
		if !monitorMatchesName(val, call) {
			continue
		}
		if q, ok := val["query"]; ok && q != nil {
			match, err := e.Store.CallMatches(ctx, ended.ProjectID, call.ID, q)
			if err != nil {
				e.Log.Warn("monitor query invalid", "project", ended.ProjectID, "monitor", m.ObjectID, "err", err)
				continue
			}
			if !match {
				continue
			}
		}
		var perCall []monitorScorer
		queue := false
		for _, ms := range e.monitorScorers(ctx, m) {
			if ms.call {
				perCall = append(perCall, ms)
			}
			if len(ms.scopes) > 0 {
				queue = true
			}
		}
		if queue {
			// Scope scorers run on the call's session once it is complete
			// (monitors_scope.go).
			if sid, err := e.Store.ConversationOf(ctx, ended.ProjectID, call.ID); err == nil && sid != "" {
				e.queueSession(ended.ProjectID, sid)
			}
		}
		if len(perCall) == 0 || !sampled(monitorRate(val)) {
			continue
		}
		triggerPrefix := strings.TrimSuffix(m.Ref(), m.Digest) // "...:<digest>" -> "...:"
		for _, ms := range perCall {
			// A span is scored once per monitor and scorer: re-sent and
			// rebuilt spans are not scored again.
			if done, err := e.Store.HasTriggeredFeedback(ctx, ended.ProjectID, call.ID, "weft.scorer."+ms.scorer.Name, triggerPrefix); err != nil {
				return err
			} else if done {
				continue
			}
			start := time.Now()
			if err := e.scoreForMonitor(ctx, m, ms.scorer, call); err != nil {
				e.Log.Warn("monitor scorer failed", "project", ended.ProjectID, "monitor", m.ObjectID, "scorer", ms.ref, "span_id", call.ID, "err", err)
				continue
			}
			e.Log.Debug("monitor scored a span", "project", ended.ProjectID, "monitor", m.ObjectID, "scorer", ms.ref, "span_id", call.ID,
				"duration_ms", time.Since(start).Milliseconds())
		}
	}
	return nil
}

// monitorNames is a monitor's span names (functions) to score. Versions
// saved before the rename, and monitors published by the compatibility
// adapter's client, name them op_names.
func monitorNames(val map[string]any) []any {
	if names, ok := val["names"].([]any); ok {
		return names
	}
	ops, _ := val["op_names"].([]any)
	return ops
}

func monitorMatchesName(val map[string]any, call *store.Call) bool {
	ops := monitorNames(val)
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
		return nil, store.Invalidf("list 1 to 100 spans to score")
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
			return nil, store.Invalidf("span %q not found", id)
		}
		for _, sc := range scorers {
			res := ScoreResult{CallID: id, Scorer: sc.Name}
			example := map[string]any{}
			for k, v := range call.Inputs {
				example[k] = v
			}
			attrs := map[string]any{"weft": map[string]any{"internal": true, "scored_span_id": id}}
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
			callRef := refs.SpanRef(projectID, rec.id)
			fb, err := e.Store.CreateFeedback(ctx, store.FeedbackCreate{
				ProjectID: projectID, Ref: refs.SpanRef(projectID, id), FeedbackType: "weft.scorer." + sc.Name,
				Payload: map[string]any{"output": res.Output}, UserID: user, RunnableRef: &sc.Ref, CallRef: &callRef,
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

func (e *Engine) scoreForMonitor(ctx context.Context, monitor *store.Obj, sc *Scorer, call *store.Call) error {
	example := map[string]any{}
	for k, v := range call.Inputs {
		example[k] = v
	}
	monitorRef := monitor.Ref()
	attrs := map[string]any{"weft": map[string]any{"internal": true, "monitor": monitorRef, "scored_span_id": call.ID}}
	score, rec, err := e.RunScorer(ctx, call.ProjectID, sc, example, call.Output, nil, attrs)
	if err != nil {
		return err
	}
	callRef := refs.SpanRef(call.ProjectID, rec.id)
	creator := "monitor:" + monitor.ObjectID
	_, err = e.Store.CreateFeedback(ctx, store.FeedbackCreate{
		ProjectID: call.ProjectID, Ref: refs.SpanRef(call.ProjectID, call.ID), FeedbackType: "weft.scorer." + sc.Name,
		Payload: map[string]any{"output": jsonSafe(score)}, Creator: &creator,
		RunnableRef: &sc.Ref, CallRef: &callRef, TriggerRef: &monitorRef,
	})
	return err
}
