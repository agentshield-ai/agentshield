package session

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/agentshield-ai/agentshield/internal/engine"
	"github.com/agentshield-ai/agentshield/internal/models"
)

func TestRegistry_Record_and_Get(t *testing.T) {
	r := NewRegistry(10, 5*time.Minute)
	r.Record("sess-1", "ls", nil)
	r.Record("sess-1", "cat", nil)
	r.Record("sess-1", "curl", nil)

	window := r.Get("sess-1")
	if window == nil {
		t.Fatal("expected non-nil window")
	}
	if len(window.Events) != 3 {
		t.Errorf("expected 3 events, got %d", len(window.Events))
	}
	if window.Events[0].Tool != "ls" {
		t.Errorf("expected first tool 'ls', got %q", window.Events[0].Tool)
	}
}

func TestRegistry_Get_UnknownSession(t *testing.T) {
	r := NewRegistry(10, 5*time.Minute)
	if r.Get("nonexistent") != nil {
		t.Error("expected nil for unknown session")
	}
}

func TestRegistry_MaxEvents_Evicts(t *testing.T) {
	r := NewRegistry(3, 5*time.Minute)
	r.Record("sess-1", "tool-1", nil)
	r.Record("sess-1", "tool-2", nil)
	r.Record("sess-1", "tool-3", nil)
	r.Record("sess-1", "tool-4", nil)

	window := r.Get("sess-1")
	if len(window.Events) != 3 {
		t.Errorf("expected 3 events (max), got %d", len(window.Events))
	}
	if window.Events[0].Tool != "tool-2" {
		t.Errorf("expected oldest to be 'tool-2', got %q", window.Events[0].Tool)
	}
}

func TestRegistry_Record_EmptySessionID_Ignored(t *testing.T) {
	r := NewRegistry(10, 5*time.Minute)
	r.Record("", "ls", nil)
	if r.Get("") != nil {
		t.Error("expected empty session ID to be ignored")
	}
}

func TestRegistry_DeriveFields(t *testing.T) {
	r := NewRegistry(10, 5*time.Minute)
	r.Record("sess-1", "ls", nil)
	r.Record("sess-1", "cat", nil)
	r.Record("sess-1", "curl", nil)

	fields := r.DeriveFields("sess-1")
	if fields == nil {
		t.Fatal("expected non-nil fields")
	}
	if fields["session.tool_count"] != "3" {
		t.Errorf("expected tool_count=3, got %q", fields["session.tool_count"])
	}
	if fields["session.recent_tools"] != "ls,cat,curl" {
		t.Errorf("expected recent_tools, got %q", fields["session.recent_tools"])
	}
	if fields["session.unique_tool_count"] != "3" {
		t.Errorf("expected unique_tool_count=3, got %q", fields["session.unique_tool_count"])
	}
	if fields["session.alert_count"] != "0" {
		t.Errorf("expected alert_count=0, got %q", fields["session.alert_count"])
	}
	if fields["session.approval_count"] != "0" {
		t.Errorf("expected approval_count=0, got %q", fields["session.approval_count"])
	}
	if fields["session.override_count"] != "0" {
		t.Errorf("expected override_count=0, got %q", fields["session.override_count"])
	}
}

func TestRegistry_DeriveFields_ApprovalAndOverride(t *testing.T) {
	r := NewRegistry(10, 5*time.Minute)
	r.RecordWithVerdict("sess-1", "curl", "evt-1", nil, models.ActionRequireApproval, false)
	r.RecordWithVerdict("sess-1", "wget", "evt-2", nil, models.ActionRequireApproval, false)
	r.RecordWithVerdict("sess-1", "nc", "evt-3", nil, models.ActionBlock, true) // overridden block
	r.RecordWithVerdict("sess-1", "ls", "evt-4", nil, models.ActionAllow, false)

	fields := r.DeriveFields("sess-1")
	if fields == nil {
		t.Fatal("expected non-nil fields")
	}
	if fields["session.approval_count"] != "2" {
		t.Errorf("expected approval_count=2, got %q", fields["session.approval_count"])
	}
	if fields["session.override_count"] != "1" {
		t.Errorf("expected override_count=1, got %q", fields["session.override_count"])
	}
}

func TestRegistry_RecordOverride(t *testing.T) {
	r := NewRegistry(10, 5*time.Minute)
	r.RecordWithVerdict("sess-1", "curl", "evt-1", nil, models.ActionBlock, false)
	r.RecordWithVerdict("sess-1", "wget", "evt-2", nil, models.ActionRequireApproval, false)

	// Override specific event by ID
	if !r.RecordOverride("sess-1", "evt-1") {
		t.Error("expected override to succeed")
	}

	fields := r.DeriveFields("sess-1")
	if fields["session.override_count"] != "1" {
		t.Errorf("expected override_count=1, got %q", fields["session.override_count"])
	}

	// Override most recent event (no eventID)
	if !r.RecordOverride("sess-1", "") {
		t.Error("expected override of latest to succeed")
	}
	fields = r.DeriveFields("sess-1")
	if fields["session.override_count"] != "2" {
		t.Errorf("expected override_count=2, got %q", fields["session.override_count"])
	}

	// Unknown session
	if r.RecordOverride("nonexistent", "") {
		t.Error("expected override of unknown session to fail")
	}

	// Unknown event ID
	if r.RecordOverride("sess-1", "evt-nonexistent") {
		t.Error("expected override of unknown event to fail")
	}
}

func TestRegistry_CrossSessionFields(t *testing.T) {
	r := NewRegistry(10, 5*time.Minute)

	// Session A: ls, cat, curl
	r.Record("sess-A", "ls", nil)
	r.Record("sess-A", "cat", nil)
	r.Record("sess-A", "curl", nil)

	// Session B: ls, wget (overlaps on "ls")
	r.Record("sess-B", "ls", nil)
	r.Record("sess-B", "wget", nil)

	// Session C: curl, nc (overlaps on "curl")
	r.Record("sess-C", "curl", nil)
	r.Record("sess-C", "nc", nil)

	// Cross-session fields for session A should see B and C
	fields := r.CrossSessionFields("sess-A", 5*time.Minute)
	if fields["session.cross_session_count"] != "2" {
		t.Errorf("expected 2 other sessions, got %q", fields["session.cross_session_count"])
	}
	if fields["session.cross_session_alert_count"] != "0" {
		t.Errorf("expected 0 cross-session alerts, got %q", fields["session.cross_session_alert_count"])
	}
	// sess-A has {ls, cat, curl}; B has ls, C has curl → 2/3 overlap
	if fields["session.cross_session_tool_overlap"] != "0.67" {
		t.Errorf("expected tool overlap 0.67, got %q", fields["session.cross_session_tool_overlap"])
	}
}

func TestRegistry_CrossSessionFields_Empty(t *testing.T) {
	r := NewRegistry(10, 5*time.Minute)
	r.Record("sess-1", "ls", nil)

	// Only one session, so cross-session should be empty
	fields := r.CrossSessionFields("sess-1", 5*time.Minute)
	if fields["session.cross_session_count"] != "0" {
		t.Errorf("expected 0 other sessions, got %q", fields["session.cross_session_count"])
	}
	if fields["session.cross_session_tool_overlap"] != "0.00" {
		t.Errorf("expected 0.00 overlap, got %q", fields["session.cross_session_tool_overlap"])
	}
}

func TestRegistry_DeriveAllFields(t *testing.T) {
	r := NewRegistry(10, 5*time.Minute)
	r.Record("sess-A", "ls", nil)
	r.Record("sess-A", "curl", nil)
	r.Record("sess-B", "ls", nil)

	fields := r.DeriveAllFields("sess-A", 5*time.Minute)
	if fields == nil {
		t.Fatal("expected non-nil fields")
	}
	// Per-session fields
	if fields["session.tool_count"] != "2" {
		t.Errorf("expected tool_count=2, got %q", fields["session.tool_count"])
	}
	// Cross-session fields
	if fields["session.cross_session_count"] != "1" {
		t.Errorf("expected cross_session_count=1, got %q", fields["session.cross_session_count"])
	}
}

func TestRegistry_DeriveFields_UnknownSession(t *testing.T) {
	r := NewRegistry(10, 5*time.Minute)
	if r.DeriveFields("nonexistent") != nil {
		t.Error("expected nil for unknown session")
	}
}

func TestRegistry_Cleanup_ExpiredSession(t *testing.T) {
	r := NewRegistry(10, 50*time.Millisecond) // Very short TTL
	r.Record("sess-1", "ls", nil)

	time.Sleep(100 * time.Millisecond)
	r.Cleanup()

	if r.Get("sess-1") != nil {
		t.Error("expected expired session to be cleaned up")
	}
}

func TestRegistry_Cleanup_ActiveSession(t *testing.T) {
	r := NewRegistry(10, 5*time.Minute)
	r.Record("sess-1", "ls", nil)

	r.Cleanup()

	if r.Get("sess-1") == nil {
		t.Error("expected active session to survive cleanup")
	}
}

func TestRegistry_Stats(t *testing.T) {
	r := NewRegistry(10, 5*time.Minute)
	if r.Stats() != 0 {
		t.Error("expected 0 sessions")
	}
	r.Record("sess-1", "ls", nil)
	r.Record("sess-2", "cat", nil)
	if r.Stats() != 2 {
		t.Errorf("expected 2 sessions, got %d", r.Stats())
	}
}

func TestRegistry_MaxSessions_EvictsOldest(t *testing.T) {
	r := NewRegistry(10, 5*time.Minute, WithMaxSessions(3))
	r.Record("sess-1", "ls", nil)
	time.Sleep(time.Millisecond) // ensure distinct lastSeen
	r.Record("sess-2", "cat", nil)
	time.Sleep(time.Millisecond)
	r.Record("sess-3", "curl", nil)

	if r.Stats() != 3 {
		t.Fatalf("expected 3 sessions, got %d", r.Stats())
	}

	// Adding a 4th session should evict sess-1 (oldest)
	r.Record("sess-4", "wget", nil)
	if r.Stats() != 3 {
		t.Errorf("expected 3 sessions after eviction, got %d", r.Stats())
	}
	if r.Get("sess-1") != nil {
		t.Error("expected sess-1 to be evicted (oldest)")
	}
	if r.Get("sess-4") == nil {
		t.Error("expected sess-4 to exist")
	}
}

func TestRegistry_ConcurrentAccess(t *testing.T) {
	r := NewRegistry(50, 5*time.Minute)
	var wg sync.WaitGroup

	// Concurrent writers
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			sid := fmt.Sprintf("sess-%d", n%10)
			r.Record(sid, fmt.Sprintf("tool-%d", n), nil)
			r.DeriveFields(sid)
			r.Get(sid)
		}(i)
	}

	// Concurrent cleanup
	wg.Add(1)
	go func() {
		defer wg.Done()
		r.Cleanup()
	}()

	// Concurrent stats
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = r.Stats()
	}()

	wg.Wait()

	// Verify registry is in a consistent state
	stats := r.Stats()
	if stats < 1 || stats > 10 {
		t.Errorf("expected 1-10 sessions, got %d", stats)
	}
}

// Replayed sessions (Isolate) must not change live sessions' cross-session fields.
func TestRegistry_IsolatedSessionsExcludedFromCrossSession(t *testing.T) {
	r := NewRegistry(50, time.Minute)
	alert := []engine.RuleResult{{RuleID: "r", Matched: true}}
	r.Record("live-1", "Bash", alert)
	r.Isolate("weft-replay:j:s")
	r.Record("weft-replay:j:s", "Bash", alert)
	r.Record("weft-replay:j:s", "Read", alert)
	f := r.DeriveAllFields("live-2", time.Minute)
	if f["session.cross_session_alert_count"] != "1" || f["session.cross_session_count"] != "1" {
		t.Fatalf("replay leaked into cross-session fields: %v", f)
	}
	r.Record("live-2", "Read", nil)
	if got := r.DeriveAllFields("live-2", time.Minute)["session.cross_session_tool_overlap"]; got != "0.00" {
		t.Fatalf("tool overlap counts the replay session: %s", got)
	}
	// The replayed session still has its own window, but does not see live
	// sessions.
	own := r.DeriveAllFields("weft-replay:j:s", time.Minute)
	if own["session.tool_count"] != "2" || own["session.cross_session_alert_count"] != "0" || own["session.cross_session_count"] != "0" {
		t.Fatalf("isolated session fields = %v", own)
	}
}

// A replayed session's cross-session fields cover the other sessions of its
// own replay job ("weft-replay:<job>:"), whatever live traffic or other jobs
// do, and regardless of the correlation window, so replaying the same
// recording always gives the same fields.
func TestRegistry_ReplaySessionsSeeOnlyTheirJob(t *testing.T) {
	r := NewRegistry(50, time.Hour)
	alert := []engine.RuleResult{{RuleID: "r", Matched: true}}
	replay := func(id string, tools ...string) {
		r.Isolate(id)
		for _, tool := range tools {
			r.Record(id, tool, alert)
		}
	}
	replay("weft-replay:j1:a", "Bash", "Read")
	replay("weft-replay:j1:a:b", "Bash") // a session key with a colon is still job j1
	replay("weft-replay:j2:a", "Bash", "Read", "Write")
	replay("weft-replay:j10:a", "Grep") // "j1" is not a prefix of job "j10"
	r.Isolate("weft-replay:j1:c")
	want := func(label string) {
		t.Helper()
		f := r.DeriveAllFields("weft-replay:j1:c", time.Nanosecond)
		if f["session.cross_session_count"] != "2" || f["session.cross_session_alert_count"] != "3" || f["session.cross_session_tool_overlap"] != "0.00" {
			t.Fatalf("%s: job j1 fields = %v", label, f)
		}
	}
	want("quiet engine")
	for i := 0; i < 20; i++ {
		r.Record(fmt.Sprintf("live-%d", i), "Bash", alert)
	}
	want("after live traffic")
	r.Record("weft-replay:j1:c", "Bash", nil)
	if f := r.DeriveAllFields("weft-replay:j1:c", time.Minute); f["session.cross_session_tool_overlap"] != "1.00" {
		t.Fatalf("overlap within the job = %v", f)
	}
	if f := r.DeriveAllFields("weft-replay:j2:a", time.Minute); f["session.cross_session_count"] != "0" {
		t.Fatalf("job j2 sees other jobs or live sessions: %v", f)
	}
	// Live sessions see neither job.
	if f := r.DeriveAllFields("live-0", time.Minute); f["session.cross_session_count"] != "19" || f["session.cross_session_alert_count"] != "19" {
		t.Fatalf("live fields = %v", f)
	}
	// Evicted replay sessions leave their job's scope.
	r.mu.Lock()
	r.deleteLocked("weft-replay:j1:a")
	r.mu.Unlock()
	if f := r.DeriveAllFields("weft-replay:j1:c", time.Minute); f["session.cross_session_count"] != "1" {
		t.Fatalf("after eviction = %v", f)
	}
	r.mu.Lock()
	r.deleteLocked("weft-replay:j1:a:b")
	r.deleteLocked("weft-replay:j1:c")
	_, left := r.scopes["weft-replay:j1:"]
	r.mu.Unlock()
	if left {
		t.Fatal("empty replay scope not removed")
	}
}
