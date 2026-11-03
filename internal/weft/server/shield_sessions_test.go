package server

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/agentshield-ai/agentshield/internal/weft/store"
)

// session_ids narrows detections and their stats to a set of sessions; the
// Security page uses it to apply session filters (team, status, score, …).
func TestDetectionsBySessionIDs(t *testing.T) {
	e := newShieldEnv(t, "")
	code, out := e.do(http.MethodPost, "/api/v1/shield/ingest", shieldIngestBody(
		shieldBlockDet("evt-a", "s-a", "toolu_a"),
		shieldBlockDet("evt-b", "s-b", "toolu_b"),
		shieldBlockDet("evt-c", "s,c", "toolu_c"),
	), "")
	if code != http.StatusOK || out["created"].(float64) != 3 {
		t.Fatalf("ingest: %d %v", code, out)
	}
	sessionsOf := func(m map[string]any) string {
		var ids []string
		for _, d := range shieldItems(m) {
			ids = append(ids, d["session_id"].(string))
		}
		return strings.Join(ids, " ")
	}
	// Repeated, and never split on commas: "s,c" is one session id.
	list := e.get("/api/v1/detections", url.Values{"session_ids": {"s-a", "s,c"}, "sort_by": {"-created_at"}})
	if list["total_count"].(float64) != 2 || !strings.Contains(sessionsOf(list), "s-a") || !strings.Contains(sessionsOf(list), "s,c") {
		t.Fatalf("session_ids = %v", list)
	}
	stats := e.get("/api/v1/detections/stats", url.Values{"session_ids": {"s-b"}})
	if stats["total"].(float64) != 1 {
		t.Fatalf("stats with session_ids = %v", stats)
	}
	// It combines with the other filters.
	if n := e.get("/api/v1/detections", url.Values{"session_ids": {"s-a"}, "action": {"require_approval"}})["total_count"].(float64); n != 0 {
		t.Fatalf("session_ids + action = %v", n)
	}
	// Too many ids is a 400, not a slow query.
	q := url.Values{"project_id": {shieldProj}}
	for i := 0; i <= maxDetectionSessionIDs; i++ {
		q.Add("session_ids", "s")
	}
	if code, _ := e.do(http.MethodGet, "/api/v1/detections?"+q.Encode(), nil, ""); code != http.StatusBadRequest {
		t.Fatalf("too many session_ids: %d", code)
	}
}

// putRoot stores a session's root agent span, failed or not.
func putRoot(t *testing.T, st *store.Store, session string, failed bool) {
	t.Helper()
	start := store.CallStart{ProjectID: shieldProj, ID: "root-" + session, TraceID: "trace-" + session, OpName: "invoke_agent",
		StartedAt: "2026-05-04T10:00:00Z", Inputs: map[string]any{}, Attributes: map[string]any{"otel": map[string]any{"attributes": map[string]any{
			"gen_ai.operation.name": "invoke_agent", "gen_ai.agent.name": "coder", "gen_ai.conversation.id": session}}}}
	end := &store.CallEnd{EndedAt: "2026-05-04T10:00:05Z", Summary: map[string]any{}}
	if failed {
		e := "boom"
		end.Exception = &e
	}
	if _, err := st.UpsertComplete(context.Background(), []store.CompleteCall{{Start: start, End: end}}); err != nil {
		t.Fatal(err)
	}
}

// session.<filter> on the detections API applies the sessions filters on
// the server (no list of ids), and /api/v1/sessions/attention counts the
// attention cards over every matching session.
func TestDetectionsBySessionFilterAndAttention(t *testing.T) {
	e := newShieldEnv(t, "")
	putRoot(t, e.st, "s-a", true)
	putRoot(t, e.st, "s-b", false)
	if code, out := e.do(http.MethodPost, "/api/v1/shield/ingest", shieldIngestBody(shieldBlockDet("evt-a", "s-a", "toolu_a"),
		shieldBlockDet("evt-b", "s-b", "toolu_b")), ""); code != http.StatusOK {
		t.Fatalf("ingest: %d %v", code, out)
	}
	list := e.get("/api/v1/detections", url.Values{"session.status": {"error"}})
	if list["total_count"].(float64) != 1 || shieldItems(list)[0]["session_id"] != "s-a" {
		t.Fatalf("session.status=error: %v", list)
	}
	if n := e.get("/api/v1/detections/stats", url.Values{"session.status": {"ok"}})["total"].(float64); n != 1 {
		t.Fatalf("stats session.status=ok: %v", n)
	}
	if n := e.get("/api/v1/detections", url.Values{"session.agent": {"nobody"}})["total_count"].(float64); n != 0 {
		t.Fatalf("no matching session must match no detection: %v", n)
	}
	if code, _ := e.do(http.MethodGet, "/api/v1/detections?project_id="+url.QueryEscape(shieldProj)+"&session.status=bogus", nil, ""); code != http.StatusBadRequest {
		t.Fatalf("bad session.status: %d", code)
	}
	a := e.get("/api/v1/sessions/attention", url.Values{})
	got := map[string]float64{}
	for _, c := range shieldItems(a) {
		got[c["id"].(string)] = c["count"].(float64)
	}
	if got["failed"] != 1 || got["security"] != 2 || a["sessions"].(float64) != 2 {
		t.Fatalf("attention = %v", a)
	}
	if n := e.get("/api/v1/sessions", url.Values{"has_detection": {"flagged"}, "status": {"ok"}})["total_count"].(float64); n != 1 {
		t.Fatalf("sessions flagged+ok = %v", n)
	}
}
