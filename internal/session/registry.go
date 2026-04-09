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

	// Cached cross-session aggregates to avoid rescanning all sessions on every evaluation.
	crossCache             map[string]string // non-nil when cache is valid
	crossCacheTotalAlerts   int
	crossCacheTotalSessions int
	crossCacheTime         time.Time
	crossCacheTTL          time.Duration
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
	r.crossCache = nil
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
// fields in a single lock acquisition. This is the preferred method for the
// evaluation hot path.
func (r *Registry) DeriveAllFields(sessionID string, correlationWindow time.Duration) map[string]string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	fields := r.deriveFieldsLocked(sessionID)

	crossFields := r.crossSessionFieldsLocked(sessionID, correlationWindow)
	if fields == nil && crossFields != nil {
		fields = crossFields
	} else {
		for k, v := range crossFields {
			fields[k] = v
		}
	}

	return fields
}

// CrossSessionFields returns Sigma-compatible fields derived from all active
// sessions, providing a global view for systemic/coordinated attack detection.
// The correlationWindow limits analysis to events within the given duration.
func (r *Registry) CrossSessionFields(excludeSessionID string, correlationWindow time.Duration) map[string]string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return r.crossSessionFieldsLocked(excludeSessionID, correlationWindow)
}

// crossSessionFieldsLocked computes cross-session fields. Must be called with r.mu held.
// Caches aggregate totals across all sessions, then subtracts the excluded
// session's contribution at query time.
func (r *Registry) crossSessionFieldsLocked(excludeSessionID string, correlationWindow time.Duration) map[string]string {
	now := time.Now()

	if r.crossCache == nil || now.Sub(r.crossCacheTime) >= r.crossCacheTTL {
		r.rebuildCrossCache(correlationWindow, now)
	}

	// Subtract excluded session's contribution from cached totals
	totalAlerts := r.crossCacheTotalAlerts
	sessionCount := r.crossCacheTotalSessions
	if excludeSessionID != "" {
		if state, ok := r.sessions[excludeSessionID]; ok {
			cutoff := now.Add(-correlationWindow)
			hasRecent := false
			for _, ev := range state.events {
				if !ev.Timestamp.Before(cutoff) {
					hasRecent = true
					totalAlerts -= len(ev.Alerts)
				}
			}
			if hasRecent {
				sessionCount--
			}
		}
	}

	return map[string]string{
		"session.cross_session_alert_count":  fmt.Sprintf("%d", totalAlerts),
		"session.cross_session_count":        fmt.Sprintf("%d", sessionCount),
		"session.cross_session_tool_overlap": r.computeToolOverlap(excludeSessionID, correlationWindow),
	}
}

// rebuildCrossCache scans all sessions to populate aggregate counts.
// Must be called with r.mu held.
func (r *Registry) rebuildCrossCache(correlationWindow time.Duration, now time.Time) {
	cutoff := now.Add(-correlationWindow)
	totalAlerts := 0
	sessionCount := 0

	for _, state := range r.sessions {
		sessionHasRecent := false
		for _, ev := range state.events {
			if ev.Timestamp.Before(cutoff) {
				continue
			}
			sessionHasRecent = true
			totalAlerts += len(ev.Alerts)
		}
		if sessionHasRecent {
			sessionCount++
		}
	}

	r.crossCacheTotalAlerts = totalAlerts
	r.crossCacheTotalSessions = sessionCount
	r.crossCache = map[string]string{} // mark cache as valid
	r.crossCacheTime = now
}

// computeToolOverlap calculates what fraction of the given session's recent
// tools appear in other sessions' recent events. Must be called with r.mu held.
func (r *Registry) computeToolOverlap(excludeSessionID string, correlationWindow time.Duration) string {
	if excludeSessionID == "" {
		return "0.00"
	}
	state, ok := r.sessions[excludeSessionID]
	if !ok || len(state.events) == 0 {
		return "0.00"
	}

	cutoff := time.Now().Add(-correlationWindow)

	myTools := make(map[string]struct{})
	for _, ev := range state.events {
		if !ev.Timestamp.Before(cutoff) {
			myTools[ev.Tool] = struct{}{}
		}
	}
	if len(myTools) == 0 {
		return "0.00"
	}

	otherTools := make(map[string]struct{})
	for id, s := range r.sessions {
		if id == excludeSessionID {
			continue
		}
		for _, ev := range s.events {
			if !ev.Timestamp.Before(cutoff) {
				otherTools[ev.Tool] = struct{}{}
			}
		}
	}

	matchCount := 0
	for tool := range myTools {
		if _, ok := otherTools[tool]; ok {
			matchCount++
		}
	}

	return fmt.Sprintf("%.2f", float64(matchCount)/float64(len(myTools)))
}
