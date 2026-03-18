package session

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/agentshield-ai/agentshield/internal/engine"
)

// Event represents a single tool call in a session timeline.
type Event struct {
	Tool      string
	Alerts    []engine.RuleResult
	Timestamp time.Time
}

// Window holds the sliding window of events for a single session.
type Window struct {
	Events []Event
}

// Registry maintains per-session event windows for behavioural sequencing.
type Registry struct {
	mu        sync.RWMutex
	sessions  map[string]*sessionState
	maxEvents int
	ttl       time.Duration
}

type sessionState struct {
	events   []Event
	lastSeen time.Time
}

// NewRegistry creates a Registry with the given max events per session and TTL.
func NewRegistry(maxEvents int, ttl time.Duration) *Registry {
	if maxEvents <= 0 {
		maxEvents = 50
	}
	if ttl <= 0 {
		ttl = 15 * time.Minute
	}
	return &Registry{
		sessions:  make(map[string]*sessionState),
		maxEvents: maxEvents,
		ttl:       ttl,
	}
}

// Record adds a tool call event to the given session's window.
func (r *Registry) Record(sessionID, tool string, alerts []engine.RuleResult) {
	if sessionID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	state, ok := r.sessions[sessionID]
	if !ok {
		state = &sessionState{
			events: make([]Event, 0, r.maxEvents),
		}
		r.sessions[sessionID] = state
	}

	state.events = append(state.events, Event{
		Tool:      tool,
		Alerts:    alerts,
		Timestamp: time.Now(),
	})
	state.lastSeen = time.Now()

	if len(state.events) > r.maxEvents {
		state.events = state.events[len(state.events)-r.maxEvents:]
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

// DeriveFields returns Sigma-compatible fields derived from the session's
// event window.
func (r *Registry) DeriveFields(sessionID string) map[string]string {
	window := r.Get(sessionID)
	if window == nil || len(window.Events) == 0 {
		return nil
	}

	tools := make([]string, len(window.Events))
	uniqueTools := make(map[string]struct{})
	alertCount := 0
	for i, ev := range window.Events {
		tools[i] = ev.Tool
		uniqueTools[ev.Tool] = struct{}{}
		alertCount += len(ev.Alerts)
	}

	return map[string]string{
		"session.tool_count":        fmt.Sprintf("%d", len(window.Events)),
		"session.recent_tools":      strings.Join(tools, ","),
		"session.unique_tool_count": fmt.Sprintf("%d", len(uniqueTools)),
		"session.alert_count":       fmt.Sprintf("%d", alertCount),
	}
}
