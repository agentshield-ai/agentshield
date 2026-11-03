package server

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
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
