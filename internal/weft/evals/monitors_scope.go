package evals

// Scope monitors (docs/weft/DESIGN.md §6: "Monitors can run any of these on
// new sessions automatically").
//
// A monitor scorer whose class scores a scope (GoalCompletionJudge,
// ReasoningConsistencyJudge, HandoffQualityJudge, ToolSafetyScorer; see
// ScorerScopes) does not score the individual call that ended. Instead the
// session that call belongs to is queued, and once the session is complete
// the scorer runs at its scope over the session's targets: the session, each
// agent in it, or each hand-off. Results are scope scores (weft.score.<name>)
// like those of POST /api/v1/scores/run, attributed to the monitor.
//
// Monitor fields (all optional):
//
//   - scopes: the scopes to score at ("session", "agent", "handoff", and
//     "span" for the per-span path; "call" in versions saved before the
//     rename). Without it a scope scorer runs at its first scope of
//     session, handoff, agent, and any other scorer per span.
//   - session_idle_seconds: a session is complete once no span of it has
//     arrived for this many seconds and none of its root spans is still
//     running (default 60). A session whose root spans never end counts as
//     complete after ten times that.
//   - sampling_rate: applied per target, once; a target sampled out is not
//     considered again.
//
// Each (monitor, scorer, scope, target) is scored at most once, recorded in
// monitor_scope_runs; a session that resumes later is checked again, and
// only targets that were not scored before (new agents or hand-offs) are.
// The queue of sessions waiting to complete is kept in memory: sessions that
// were waiting when the server stopped are scored when they next get spans.

import (
	"context"
	"errors"
	"math/rand/v2"
	"time"

	"github.com/agentshield-ai/agentshield/internal/weft/store"
)

const (
	defaultSessionIdle = 60 * time.Second
	maxSessionIdle     = 24 * time.Hour
	abandonedFactor    = 10
	scopeSpan          = "span"
	scopeSpanOld       = "call" // the per-span scope's earlier name
)

// scopeTick is how often waiting sessions are checked (tests shorten it).
var scopeTick = 5 * time.Second

type pendingKey struct{ projectID, sessionID string }

// monitorScorer is one scorer of a monitor and the scopes it runs at.
type monitorScorer struct {
	ref    string
	scorer *Scorer
	scopes []string // session / agent / handoff
	call   bool     // also (or only) per call
}

func monitorExplicitScopes(val map[string]any) []string {
	l, _ := val["scopes"].([]any)
	var out []string
	for _, x := range l {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// monitorIdle is the monitor's session_idle_seconds.
func monitorIdle(val map[string]any) time.Duration {
	v, ok := store.ToFloat(val["session_idle_seconds"])
	if !ok || v < 0 {
		return defaultSessionIdle
	}
	d := time.Duration(v * float64(time.Second))
	if d > maxSessionIdle {
		d = maxSessionIdle
	}
	return d
}

func monitorRate(val map[string]any) float64 {
	if r, ok := store.ToFloat(val["sampling_rate"]); ok {
		return r
	}
	return 1
}

func sampled(rate float64) bool { return rate > 0 && (rate >= 1 || rand.Float64() < rate) }

// monitorScorers resolves a monitor's scorers and where each runs.
func (e *Engine) monitorScorers(ctx context.Context, m *store.Obj) []monitorScorer {
	val := m.Val.(map[string]any)
	explicit := monitorExplicitScopes(val)
	refs, _ := val["scorers"].([]any)
	var out []monitorScorer
	for _, r := range refs {
		ref, ok := r.(string)
		if !ok {
			continue
		}
		sc, err := e.loadScorer(ctx, ref)
		if err != nil {
			e.Log.Warn("monitor scorer failed", "monitor", m.ObjectID, "scorer", ref, "err", err)
			continue
		}
		ms := monitorScorer{ref: ref, scorer: sc}
		allowed, scoped := ScorerScopes[className(sc.Val)]
		if len(explicit) > 0 {
			for _, s := range explicit {
				switch s {
				case scopeSpan, scopeSpanOld:
					ms.call = true
				case store.ScopeSession, store.ScopeAgent, store.ScopeHandoff:
					if !scoped || allowed[s] {
						ms.scopes = append(ms.scopes, s)
					}
				}
			}
		} else if scoped {
			for _, s := range []string{store.ScopeSession, store.ScopeHandoff, store.ScopeAgent} {
				if allowed[s] {
					ms.scopes = []string{s}
					break
				}
			}
		} else {
			ms.call = true
		}
		out = append(out, ms)
	}
	return out
}

// queueSession marks a session as active for scope monitors.
func (e *Engine) queueSession(projectID, sessionID string) {
	e.pendMu.Lock()
	e.pending[pendingKey{projectID, sessionID}] = time.Now()
	e.pendMu.Unlock()
}

func (e *Engine) scopeWorker() {
	defer e.wg.Done()
	t := time.NewTicker(scopeTick)
	defer t.Stop()
	for {
		select {
		case <-e.stop:
			return
		case <-t.C:
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			e.checkPendingSessions(ctx, time.Now())
			cancel()
		}
	}
}

// checkPendingSessions scores the waiting sessions that are now complete.
func (e *Engine) checkPendingSessions(ctx context.Context, now time.Time) {
	e.pendMu.Lock()
	snap := make(map[pendingKey]time.Time, len(e.pending))
	for k, v := range e.pending {
		snap[k] = v
	}
	e.pendMu.Unlock()
	for k, last := range snap {
		if ctx.Err() != nil {
			return
		}
		done, err := e.scoreSessionIfComplete(ctx, k, now.Sub(last))
		if err != nil {
			e.Log.Warn("scope monitor failed", "project", k.projectID, "session", k.sessionID, "err", err)
		}
		if done || err != nil {
			e.pendMu.Lock()
			if e.pending[k].Equal(last) {
				delete(e.pending, k)
			}
			e.pendMu.Unlock()
		}
	}
}

// scoreSessionIfComplete runs the project's scope monitors whose completion
// condition the session meets. It reports whether no monitor is still
// waiting for the session.
func (e *Engine) scoreSessionIfComplete(ctx context.Context, k pendingKey, idle time.Duration) (bool, error) {
	monitors, err := e.activeMonitors(ctx, k.projectID)
	if err != nil {
		return false, err
	}
	var sess *store.SessionSummary
	done := true
	for _, m := range monitors {
		val := m.Val.(map[string]any)
		var scoped []monitorScorer
		for _, ms := range e.monitorScorers(ctx, m) {
			if len(ms.scopes) > 0 {
				scoped = append(scoped, ms)
			}
		}
		if len(scoped) == 0 {
			continue
		}
		need := monitorIdle(val)
		if idle < need {
			done = false
			continue
		}
		if sess == nil {
			sess, err = e.Store.GetAgentSession(ctx, k.projectID, k.sessionID)
			if errors.Is(err, store.ErrNotFound) {
				return true, nil
			}
			if err != nil {
				return false, err
			}
		}
		if sess.Status == "running" && idle < need*abandonedFactor {
			done = false
			continue
		}
		e.scoreSessionForMonitor(ctx, m, scoped, k)
	}
	return done, nil
}

// scoreSessionForMonitor scores a complete session's targets that the
// monitor has not scored before.
func (e *Engine) scoreSessionForMonitor(ctx context.Context, m *store.Obj, scorers []monitorScorer, k pendingKey) {
	rate := monitorRate(m.Val.(map[string]any))
	monitorRef := m.Ref()
	creator := "monitor:" + m.ObjectID
	by := scoreBy{creator: &creator, triggerRef: &monitorRef}
	for _, ms := range scorers {
		for _, scope := range ms.scopes {
			targets, err := e.Store.ScopeTargets(ctx, store.ScopeTargetsReq{ProjectID: k.projectID, Scope: scope, SessionIDs: []string{k.sessionID}})
			if err != nil {
				e.Log.Warn("scope monitor targets failed", "monitor", m.ObjectID, "session", k.sessionID, "scope", scope, "err", err)
				continue
			}
			for _, t := range targets {
				st, err := e.Store.MonitorTargetStatus(ctx, k.projectID, m.ObjectID, ms.ref, t)
				if err != nil || st != "" {
					continue
				}
				if !sampled(rate) {
					_, _ = e.Store.ClaimMonitorTarget(ctx, k.projectID, m.ObjectID, ms.ref, t, store.MonitorTargetSampledOut)
					continue
				}
				ok, err := e.Store.ClaimMonitorTarget(ctx, k.projectID, m.ObjectID, ms.ref, t, store.MonitorTargetScored)
				if err != nil || !ok {
					continue
				}
				if res := e.scoreTarget(ctx, k.projectID, t, ms.scorer, by); res.Error != "" {
					e.Log.Warn("scope monitor scorer failed", "monitor", m.ObjectID, "scorer", ms.ref, "session", k.sessionID, "err", res.Error)
					_ = e.Store.SetMonitorTargetStatus(ctx, k.projectID, m.ObjectID, ms.ref, t, store.MonitorTargetFailed, res.Error)
				}
			}
		}
	}
}
