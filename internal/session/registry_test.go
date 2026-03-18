package session

import (
	"testing"
	"time"
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
