package session

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/agentshield-ai/agentshield/internal/engine"
)

// TestRegistry_ConcurrentDeriveAndRecord exercises the evaluation hot path
// concurrently. Before the cross-cache locking fix, DeriveAllFields rebuilt
// the shared cross-session cache while holding only the read lock, so two
// concurrent evaluations raced on the cache fields; this test fails under
// -race against that implementation.
func TestRegistry_ConcurrentDeriveAndRecord(t *testing.T) {
	r := NewRegistry(20, time.Minute)

	alerts := []engine.RuleResult{{RuleID: "r1", RuleName: "Rule One", Matched: true}}
	const goroutines = 8
	const iterations = 200

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			sessionID := fmt.Sprintf("sess-%d", id)
			for i := 0; i < iterations; i++ {
				r.Record(sessionID, "curl", alerts)
				_ = r.DeriveAllFields(sessionID, 5*time.Minute)
				_ = r.CrossSessionFields(sessionID, 5*time.Minute)
			}
		}(g)
	}
	wg.Wait()

	if got := r.Stats(); got != goroutines {
		t.Errorf("expected %d sessions, got %d", goroutines, got)
	}

	// Sanity-check a derived field to make sure the concurrent path produced
	// coherent aggregates rather than corrupted state.
	fields := r.DeriveAllFields("sess-0", 5*time.Minute)
	if fields["session.cross_session_count"] != fmt.Sprintf("%d", goroutines-1) {
		t.Errorf("expected cross_session_count=%d, got %q", goroutines-1, fields["session.cross_session_count"])
	}
	if fields["session.cross_session_tool_overlap"] != "1.00" {
		t.Errorf("expected tool overlap 1.00 (all sessions use curl), got %q", fields["session.cross_session_tool_overlap"])
	}
}

// BenchmarkDeriveAllFields_ManySessions measures the evaluation hot path with
// a large number of active sessions. The cross-session aggregates are cached,
// so steady-state cost should be O(own session events), not O(all sessions).
func BenchmarkDeriveAllFields_ManySessions(b *testing.B) {
	r := NewRegistry(20, time.Hour)
	for i := 0; i < 1000; i++ {
		id := fmt.Sprintf("sess-%d", i)
		for j := 0; j < 10; j++ {
			r.Record(id, fmt.Sprintf("tool-%d", j%5), nil)
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = r.DeriveAllFields("sess-42", 5*time.Minute)
	}
}
