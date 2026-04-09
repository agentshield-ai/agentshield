package session

import (
	"testing"
	"time"
)

// Test: computeToolOverlap ignores correlation window for primary session
func TestComputeToolOverlap_PrimarySessionTimeWindow(t *testing.T) {
	r := NewRegistry(10, 5*time.Minute)
	
	// Add old tool to primary session with old timestamp
	r.Record("sess-A", "old-tool", nil)
	
	// Manually make it old
	r.mu.Lock()
	r.sessions["sess-A"].events[0].Timestamp = time.Now().Add(-10 * time.Minute)
	r.mu.Unlock()
	
	// Add same tool to other session with current timestamp
	r.Record("sess-B", "old-tool", nil)
	
	// With 1-minute correlation window, the overlap should be 0.00
	// because sess-A's "old-tool" is outside the window
	overlap := r.CrossSessionFields("sess-A", 1*time.Minute)
	
	actual := overlap["session.cross_session_tool_overlap"]
	t.Logf("Overlap result: %s (bug: counts old tools, correct: should be 0.00)", actual)
	
	// If == "1.00", the bug exists (old tools counted)
	if actual == "1.00" {
		t.Error("BUG CONFIRMED: computeToolOverlap includes tools from ALL events, ignoring correlationWindow for primary session")
	}
}

// Test: cross-session-count decrement logic with excluded session having no recent events
func TestCrossSessionCount_ExcludedSessionNoRecentEvents(t *testing.T) {
	r := NewRegistry(10, 5*time.Minute)
	
	r.Record("sess-A", "curl", nil)
	r.Record("sess-B", "ls", nil)
	
	// Make sess-B old so it has no recent events within 1-minute window
	r.mu.Lock()
	r.sessions["sess-B"].events[0].Timestamp = time.Now().Add(-10 * time.Minute)
	r.sessions["sess-B"].lastSeen = time.Now().Add(-10 * time.Minute)
	r.mu.Unlock()
	
	// With 1-minute window, only sess-A has recent events
	// When computing cross-session count EXCLUDING sess-A:
	// - Only sess-B exists, but it has no recent events, so initial count = 0
	// - The code then checks if excluded session (sess-A) has recent events
	// - It does, so it decrements: 0 - 1 = -1
	// - Then clamped to 0
	fields := r.CrossSessionFields("sess-A", 1*time.Minute)
	count := fields["session.cross_session_count"]
	
	t.Logf("Cross-session count (sess-B has no recent): %s (expected 0, bug may produce 0 after clamp)", count)
	if count != "0" {
		t.Errorf("Unexpected count: %s", count)
	}
}

// Test the actual cross-session count bug more carefully
func TestCrossSessionCount_LogicError(t *testing.T) {
	r := NewRegistry(10, 5*time.Minute)
	
	// Scenario: sess-A is excluded, sess-B has old events (outside window)
	r.Record("sess-A", "curl", nil)
	r.Record("sess-B", "ls", nil)
	
	r.mu.Lock()
	// Make sess-B old
	r.sessions["sess-B"].events[0].Timestamp = time.Now().Add(-10 * time.Minute)
	r.sessions["sess-B"].lastSeen = time.Now().Add(-10 * time.Minute)
	r.mu.Unlock()
	
	// Expected behavior:
	// - Only sess-A has recent events in 1-minute window
	// - When excluding sess-A, no other sessions have recent events
	// - Result should be 0
	
	// But the code:
	// 1. Counts all sessions with recent: sess-A (count=1), sess-B skipped (no recent)
	// 2. At line 338-346, checks if excluded (sess-A) has recent → YES
	// 3. Decrements count: 1 - 1 = 0 ✓
	
	// So it works by accident because sess-A WAS counted initially
	
	fields := r.CrossSessionFields("sess-A", 1*time.Minute)
	count := fields["session.cross_session_count"]
	if count != "0" {
		t.Errorf("Expected 0, got %s", count)
	}
	t.Log("This test passes, but the logic is fragile")
}

// Test where the decrement actually causes negative
func TestCrossSessionCount_ActualNegative(t *testing.T) {
	r := NewRegistry(10, 5*time.Minute)
	
	// Only sess-B exists, it's excluded, and has recent events
	r.Record("sess-B", "ls", nil)
	
	// Expected: cross-session count of 0 (no other sessions have recent)
	// Actual: 
	// 1. Loop finds sess-B with recent: count = 1
	// 2. Check excluded (sess-B) has recent: decrement to 0
	// Result: 0 (correct by accident)
	
	fields := r.CrossSessionFields("sess-B", 5*time.Minute)
	count := fields["session.cross_session_count"]
	if count != "0" {
		t.Errorf("Expected 0, got %s", count)
	}
}
