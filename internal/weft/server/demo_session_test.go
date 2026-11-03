package server

import (
	"encoding/json"
	"net/url"
	"testing"
)

func TestDemoSession(t *testing.T) {
	e := newEnv(t)
	if code, _ := e.raw("/api/v1/demo/session", map[string]any{}, nil); code != 400 {
		t.Fatalf("missing project_id: got %d, want 400", code)
	}
	res := e.post("/api/v1/demo/session", map[string]any{"project_id": "demo/try"})
	sid, _ := res["session_id"].(string)
	if sid == "" || res["detections"].(float64) != 1 {
		t.Fatalf("response: %v", res)
	}
	q := "?project_id=" + url.QueryEscape("demo/try")
	get := func(path string) map[string]any {
		t.Helper()
		code, out := e.raw(path, nil, nil)
		if code != 200 {
			t.Fatalf("%s -> %d %s", path, code, out)
		}
		var m map[string]any
		_ = json.Unmarshal(out, &m)
		return m
	}
	s := get("/api/v1/sessions/" + url.PathEscape(sid) + q)
	agents, _ := s["agents"].([]any)
	if len(agents) != 2 || agents[0] != "lead" || agents[1] != "coder" {
		t.Errorf("agents = %v, want [lead coder]", agents)
	}
	if s["handoff_count"].(float64) != 1 {
		t.Errorf("handoff_count = %v, want 1", s["handoff_count"])
	}
	if s["reasoning_step_count"].(float64) < 5 {
		t.Errorf("reasoning_step_count = %v, want >= 5", s["reasoning_step_count"])
	}
	if s["team_id"] != "demo-team" {
		t.Errorf("team_id = %v", s["team_id"])
	}
	if dc, _ := s["detection_count"].(map[string]any); dc["block"] != float64(1) {
		t.Errorf("detection_count = %v, want block 1", s["detection_count"])
	}
	if s["title"] == "" || s["status"] != "ok" {
		t.Errorf("title %q status %v", s["title"], s["status"])
	}

	// The verdict is linked to the blocked Bash call, and reasoning points at it.
	dets := get("/api/v1/detections" + q + "&session_id=" + url.QueryEscape(sid))
	items := dets["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("detections: %v", dets)
	}
	d := items[0].(map[string]any)
	if d["link"] != "tool_call_id" || d["span_id"] == nil || d["action"] != "block" {
		t.Fatalf("detection not linked: %v", d)
	}
	tl := get("/api/v1/sessions/" + url.PathEscape(sid) + "/timeline" + q)
	var ledToBlocked, toolErr bool
	for _, it := range tl["items"].([]any) {
		m := it.(map[string]any)
		p := m["payload"].(map[string]any)
		if m["type"] == "reasoning" && p["next_span_id"] == d["span_id"] {
			ledToBlocked = true
		}
		if m["type"] == "tool_call" && m["span_id"] == d["span_id"] && p["status"] == "ERROR" {
			toolErr = true
		}
	}
	if !ledToBlocked || !toolErr {
		t.Errorf("reasoning→blocked tool link %v, blocked tool marked failed %v", ledToBlocked, toolErr)
	}

	// Each press writes a new session.
	again := e.post("/api/v1/demo/session", map[string]any{"project_id": "demo/try"})
	if again["session_id"] == sid {
		t.Errorf("second demo session reused id %v", sid)
	}
}
