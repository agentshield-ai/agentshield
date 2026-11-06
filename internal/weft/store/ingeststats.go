package store

import (
	"context"
	"sort"
	"sync"
)

// IngestStats counts what one ingest request did, for its log line
// (server/otlp_logs.go, server.go otelTraces, server/shield.go): spans
// stored and dropped, log events new and re-sent, turns rebuilt, verdicts
// derived and how verdicts attached. Attach it with WithIngestStats; the
// store adds to it while it writes.
type IngestStats struct {
	mu sync.Mutex
	// SpansStored counts span starts written; SpansDropped those dropped
	// because their session has a higher-ranked source (sources.go);
	// SpansSampled those dropped by the project's ingest sampling.
	SpansStored, SpansDropped, SpansSampled int
	// EventsNew and EventsDuplicate count log events stored and re-sent.
	EventsNew, EventsDuplicate int
	// TurnsRebuilt counts event-log groups whose spans were rebuilt;
	// TurnsInterrupted those closed as interrupted by this request.
	TurnsRebuilt, TurnsInterrupted int
	// Verdicts counts detections created from AgentShield evaluate spans.
	Verdicts int
	// SourcesReplaced counts sessions whose lower-ranked spans were hidden.
	SourcesReplaced int
	// Links counts how detections attached: tool_call_id, nearest_time,
	// or unattached (waiting for their span, or no session).
	Links    map[string]int
	sessions map[string]bool
}

type ingestStatsKey struct{}

// WithIngestStats returns a context that collects ingest counts.
func WithIngestStats(ctx context.Context) (context.Context, *IngestStats) {
	st := &IngestStats{Links: map[string]int{}, sessions: map[string]bool{}}
	return context.WithValue(ctx, ingestStatsKey{}, st), st
}

func ingestStatsFrom(ctx context.Context) *IngestStats {
	st, _ := ctx.Value(ingestStatsKey{}).(*IngestStats)
	return st
}

// add applies fn to the context's stats, if any.
func ingestCount(ctx context.Context, fn func(st *IngestStats)) {
	if st := ingestStatsFrom(ctx); st != nil {
		st.mu.Lock()
		fn(st)
		st.mu.Unlock()
	}
}

func (st *IngestStats) addSession(id string) {
	if id != "" {
		st.sessions[id] = true
	}
}

// Sessions lists the sessions the request wrote spans or events for.
func (st *IngestStats) Sessions() []string {
	st.mu.Lock()
	defer st.mu.Unlock()
	out := make([]string, 0, len(st.sessions))
	for s := range st.sessions {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// LinkCounts copies the detection link outcomes.
func (st *IngestStats) LinkCounts() map[string]int {
	st.mu.Lock()
	defer st.mu.Unlock()
	out := make(map[string]int, len(st.Links))
	for k, v := range st.Links {
		out[k] = v
	}
	return out
}
