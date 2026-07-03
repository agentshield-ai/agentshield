package session

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/agentshield-ai/agentshield/internal/engine"
	"github.com/agentshield-ai/agentshield/internal/models"
)

// Event represents a single tool call in a session timeline.
type Event struct {
	Tool       string
	EventID    string
	Alerts     []engine.RuleResult
	Action     models.Action
	Overridden bool
	Timestamp  time.Time
}

// Window holds the sliding window of events for a single session.
type Window struct {
	Events []Event
}

// Registry maintains per-session event windows for behavioural sequencing.
type Registry struct {
	mu          sync.RWMutex
	sessions    map[string]*sessionState
	maxEvents   int
	maxSessions int
	ttl         time.Duration

	// Cached cross-session aggregates to avoid rescanning all sessions on
	// every evaluation. The cache is only ever (re)built while holding the
	// write lock; readers holding the read lock either use a valid cache or
	// fall back to a read-only computation. Writers invalidate it by setting
	// cross to nil.
	cross            *crossAggregates
	crossCacheTime   time.Time
	crossCacheWindow time.Duration
	crossCacheTTL    time.Duration
}

// crossAggregates holds cross-session totals computed in a single scan.
type crossAggregates struct {
	totalAlerts   int
	totalSessions int
	// toolSessions maps tool name -> number of sessions that used it within
	// the correlation window, enabling O(own tools) overlap computation.
	toolSessions map[string]int
	// cutoff is the window boundary the aggregates were computed against.
	// Per-request exclusion of a session's own contribution must use the
	// same cutoff to stay consistent with the cached totals.
	cutoff time.Time
}

type sessionState struct {
	events   []Event
	lastSeen time.Time
}

// NewRegistry creates a Registry with the given max events per session and TTL.
// maxSessions caps total concurrent sessions to prevent unbounded memory growth;
// when exceeded, the oldest session is evicted. Pass 0 for the default (100,000).
func NewRegistry(maxEvents int, ttl time.Duration, opts ...RegistryOption) *Registry {
	if maxEvents <= 0 {
		maxEvents = 50
	}
	if ttl <= 0 {
		ttl = 15 * time.Minute
	}
	r := &Registry{
		sessions:      make(map[string]*sessionState),
		maxEvents:     maxEvents,
		maxSessions:   100_000,
		ttl:           ttl,
		crossCacheTTL: 1 * time.Second,
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// RegistryOption configures optional Registry parameters.
type RegistryOption func(*Registry)

// WithMaxSessions sets the maximum number of concurrent sessions.
func WithMaxSessions(n int) RegistryOption {
	return func(r *Registry) {
		if n > 0 {
			r.maxSessions = n
		}
	}
}

// Record adds a tool call event to the given session's window.
func (r *Registry) Record(sessionID, tool string, alerts []engine.RuleResult) {
	r.RecordWithVerdict(sessionID, tool, "", alerts, models.ActionAllow, false)
}

// RecordWithVerdict adds a tool call event with verdict metadata to the session window.
func (r *Registry) RecordWithVerdict(sessionID, tool, eventID string, alerts []engine.RuleResult, action models.Action, overridden bool) {
	if sessionID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	state, ok := r.sessions[sessionID]
	if !ok {
		// Evict oldest session if at capacity
		if len(r.sessions) >= r.maxSessions {
			r.evictOldest()
		}
		state = &sessionState{
			events: make([]Event, 0, r.maxEvents),
		}
		r.sessions[sessionID] = state
	}

	now := time.Now()
	state.events = append(state.events, Event{
		Tool:       tool,
		EventID:    eventID,
		Alerts:     alerts,
		Action:     action,
		Overridden: overridden,
		Timestamp:  now,
	})
	state.lastSeen = now

	if len(state.events) > r.maxEvents {
		state.events = state.events[len(state.events)-r.maxEvents:]
	}

	// Invalidate cross-session cache on any write
	r.cross = nil
}

// evictOldest removes the session with the oldest lastSeen timestamp.
// Must be called with r.mu held.
func (r *Registry) evictOldest() {
	var oldestID string
	var oldestTime time.Time
	first := true
	for id, state := range r.sessions {
		if first || state.lastSeen.Before(oldestTime) {
			oldestID = id
			oldestTime = state.lastSeen
			first = false
		}
	}
	if oldestID != "" {
		delete(r.sessions, oldestID)
	}
}

// Get returns the event window for a session, or nil if unknown.
func (r *Registry) Get(sessionID string) *Window {
	r.mu.RLock()
	defer r.mu.RUnlock()

	state, ok := r.sessions[sessionID]
	if !ok {
		return nil
	}

	events := make([]Event, len(state.events))
	copy(events, state.events)
	return &Window{Events: events}
}

// Cleanup removes sessions that have not been active within the TTL window.
func (r *Registry) Cleanup() {
	r.mu.Lock()
	defer r.mu.Unlock()

	cutoff := time.Now().Add(-r.ttl)
	for id, state := range r.sessions {
		if state.lastSeen.Before(cutoff) {
			delete(r.sessions, id)
			r.cross = nil
		}
	}
}

// StartCleanupLoop runs periodic session cleanup. Call the returned cancel
// function to stop the loop.
func (r *Registry) StartCleanupLoop(interval time.Duration) func() {
	ticker := time.NewTicker(interval)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-ticker.C:
				r.Cleanup()
			case <-done:
				ticker.Stop()
				return
			}
		}
	}()
	return func() { close(done) }
}

// Stats returns the number of tracked sessions.
func (r *Registry) Stats() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.sessions)
}

// RecordOverride marks the most recent event in a session as overridden by
// the user. Returns true if the override was recorded, false if the session
// or event was not found.
func (r *Registry) RecordOverride(sessionID, eventID string) bool {
	if sessionID == "" {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	state, ok := r.sessions[sessionID]
	if !ok || len(state.events) == 0 {
		return false
	}

	// If eventID is provided, find and mark that specific event.
	// Otherwise mark the most recent event.
	if eventID != "" {
		for i := len(state.events) - 1; i >= 0; i-- {
			if state.events[i].EventID == eventID {
				state.events[i].Overridden = true
				return true
			}
		}
		return false
	}

	state.events[len(state.events)-1].Overridden = true
	return true
}

// DeriveFields returns Sigma-compatible fields derived from the session's
// event window.
func (r *Registry) DeriveFields(sessionID string) map[string]string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return r.deriveFieldsLocked(sessionID)
}

// deriveFieldsLocked computes session fields. Must be called with r.mu held.
func (r *Registry) deriveFieldsLocked(sessionID string) map[string]string {
	state, ok := r.sessions[sessionID]
	if !ok || len(state.events) == 0 {
		return nil
	}

	tools := make([]string, len(state.events))
	uniqueTools := make(map[string]struct{})
	alertCount := 0
	approvalCount := 0
	overrideCount := 0
	for i, ev := range state.events {
		tools[i] = ev.Tool
		uniqueTools[ev.Tool] = struct{}{}
		alertCount += len(ev.Alerts)
		if ev.Action == models.ActionRequireApproval {
			approvalCount++
		}
		if ev.Overridden {
			overrideCount++
		}
	}

	return map[string]string{
		"session.tool_count":        fmt.Sprintf("%d", len(state.events)),
		"session.recent_tools":      strings.Join(tools, ","),
		"session.unique_tool_count": fmt.Sprintf("%d", len(uniqueTools)),
		"session.alert_count":       fmt.Sprintf("%d", alertCount),
		"session.approval_count":    fmt.Sprintf("%d", approvalCount),
		"session.override_count":    fmt.Sprintf("%d", overrideCount),
	}
}

// DeriveAllFields returns both per-session and cross-session Sigma-compatible
// fields. This is the preferred method for the evaluation hot path.
func (r *Registry) DeriveAllFields(sessionID string, correlationWindow time.Duration) map[string]string {
	r.ensureCrossCache(correlationWindow)

	r.mu.RLock()
	defer r.mu.RUnlock()

	fields := r.deriveFieldsLocked(sessionID)
	crossFields := r.crossSessionFieldsLocked(sessionID, correlationWindow)
	if fields == nil {
		return crossFields
	}
	for k, v := range crossFields {
		fields[k] = v
	}
	return fields
}

// CrossSessionFields returns Sigma-compatible fields derived from all active
// sessions, providing a global view for systemic/coordinated attack detection.
// The correlationWindow limits analysis to events within the given duration.
func (r *Registry) CrossSessionFields(excludeSessionID string, correlationWindow time.Duration) map[string]string {
	r.ensureCrossCache(correlationWindow)

	r.mu.RLock()
	defer r.mu.RUnlock()

	return r.crossSessionFieldsLocked(excludeSessionID, correlationWindow)
}

// crossCacheValidLocked reports whether the cached aggregates can serve a
// read for the given window. Must be called with r.mu held (read or write).
func (r *Registry) crossCacheValidLocked(correlationWindow time.Duration, now time.Time) bool {
	return r.cross != nil &&
		r.crossCacheWindow == correlationWindow &&
		now.Sub(r.crossCacheTime) < r.crossCacheTTL
}

// ensureCrossCache rebuilds the cross-session aggregates when they are
// missing, stale, or were built for a different correlation window. The
// rebuild happens under the write lock: rebuilding under a read lock would
// race with other readers (shared cache fields must never be written while
// only an RLock is held).
func (r *Registry) ensureCrossCache(correlationWindow time.Duration) {
	r.mu.RLock()
	valid := r.crossCacheValidLocked(correlationWindow, time.Now())
	r.mu.RUnlock()
	if valid {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	if !r.crossCacheValidLocked(correlationWindow, now) {
		r.cross = r.computeCrossAggregates(correlationWindow, now)
		r.crossCacheTime = now
		r.crossCacheWindow = correlationWindow
	}
}

// computeCrossAggregates scans all sessions once and returns the totals.
// Read-only with respect to registry state; must be called with r.mu held
// (read or write).
func (r *Registry) computeCrossAggregates(correlationWindow time.Duration, now time.Time) *crossAggregates {
	agg := &crossAggregates{
		toolSessions: make(map[string]int),
		cutoff:       now.Add(-correlationWindow),
	}

	for _, state := range r.sessions {
		hasRecent := false
		var sessionTools map[string]struct{}
		for _, ev := range state.events {
			if ev.Timestamp.Before(agg.cutoff) {
				continue
			}
			hasRecent = true
			agg.totalAlerts += len(ev.Alerts)
			if sessionTools == nil {
				sessionTools = make(map[string]struct{})
			}
			sessionTools[ev.Tool] = struct{}{}
		}
		if hasRecent {
			agg.totalSessions++
		}
		for tool := range sessionTools {
			agg.toolSessions[tool]++
		}
	}
	return agg
}

// crossSessionFieldsLocked derives the cross-session fields from the cached
// aggregates, subtracting the excluded session's own contribution at query
// time. Must be called with r.mu held (read lock is sufficient — this method
// never mutates shared state).
func (r *Registry) crossSessionFieldsLocked(excludeSessionID string, correlationWindow time.Duration) map[string]string {
	agg := r.cross
	if !r.crossCacheValidLocked(correlationWindow, time.Now()) {
		// A concurrent write invalidated the cache between ensureCrossCache
		// and the caller acquiring the read lock. Compute directly instead —
		// the cache itself must not be written while holding only an RLock.
		agg = r.computeCrossAggregates(correlationWindow, time.Now())
	}

	// Subtract excluded session's contribution from the totals, using the
	// same cutoff the aggregates were computed against.
	totalAlerts := agg.totalAlerts
	sessionCount := agg.totalSessions
	var myTools map[string]struct{}
	if excludeSessionID != "" {
		if state, ok := r.sessions[excludeSessionID]; ok {
			hasRecent := false
			for _, ev := range state.events {
				if ev.Timestamp.Before(agg.cutoff) {
					continue
				}
				hasRecent = true
				totalAlerts -= len(ev.Alerts)
				if myTools == nil {
					myTools = make(map[string]struct{})
				}
				myTools[ev.Tool] = struct{}{}
			}
			if hasRecent {
				sessionCount--
			}
		}
	}

	// Overlap: fraction of this session's recent tools that at least one
	// other session also used within the window. The excluded session
	// contributes exactly one count per tool in myTools, so "another session
	// used it" means a cached count of at least two.
	overlap := "0.00"
	if len(myTools) > 0 {
		matchCount := 0
		for tool := range myTools {
			if agg.toolSessions[tool] >= 2 {
				matchCount++
			}
		}
		overlap = fmt.Sprintf("%.2f", float64(matchCount)/float64(len(myTools)))
	}

	return map[string]string{
		"session.cross_session_alert_count":  fmt.Sprintf("%d", totalAlerts),
		"session.cross_session_count":        fmt.Sprintf("%d", sessionCount),
		"session.cross_session_tool_overlap": overlap,
	}
}
