package session

import (
	"testing"
	"time"
)

func TestComputeToolOverlap_RespectsCorrelationWindow(t *testing.T) {
	r := NewRegistry(10, 5*time.Minute)

	r.Record("sess-A", "old-tool", nil)

	// Make the event older than the correlation window
	r.mu.Lock()
	r.sessions["sess-A"].events[0].Timestamp = time.Now().Add(-10 * time.Minute)
	r.mu.Unlock()

	r.Record("sess-B", "old-tool", nil)

	// With 1-minute window, sess-A's "old-tool" is outside the window
	overlap := r.CrossSessionFields("sess-A", 1*time.Minute)
	if overlap["session.cross_session_tool_overlap"] != "0.00" {
		t.Errorf("expected 0.00 overlap for expired tools, got %q",
			overlap["session.cross_session_tool_overlap"])
	}
}

func TestCrossSessionCount_ExcludedSessionNoRecentEvents(t *testing.T) {
	r := NewRegistry(10, 5*time.Minute)

	r.Record("sess-A", "curl", nil)
	r.Record("sess-B", "ls", nil)

	// Make sess-B older than the correlation window
	r.mu.Lock()
	r.sessions["sess-B"].events[0].Timestamp = time.Now().Add(-10 * time.Minute)
	r.sessions["sess-B"].lastSeen = time.Now().Add(-10 * time.Minute)
	r.mu.Unlock()

	// Excluding sess-A, no other sessions have recent events
	fields := r.CrossSessionFields("sess-A", 1*time.Minute)
	if fields["session.cross_session_count"] != "0" {
		t.Errorf("expected 0, got %s", fields["session.cross_session_count"])
	}
}

func TestCrossSessionCount_SingleSessionExcluded(t *testing.T) {
	r := NewRegistry(10, 5*time.Minute)

	r.Record("sess-B", "ls", nil)

	// Only session is the excluded one — count should be 0
	fields := r.CrossSessionFields("sess-B", 5*time.Minute)
	if fields["session.cross_session_count"] != "0" {
		t.Errorf("expected 0, got %s", fields["session.cross_session_count"])
	}
}
