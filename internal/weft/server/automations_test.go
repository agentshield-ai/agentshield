package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

type hookSink struct {
	mu     sync.Mutex
	bodies []map[string]any
	srv    *httptest.Server
}

func newSink(t *testing.T, status int) *hookSink {
	h := &hookSink{}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		h.mu.Lock()
		h.bodies = append(h.bodies, m)
		h.mu.Unlock()
		w.WriteHeader(status)
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(h.srv.Close)
	return h
}

func (h *hookSink) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.bodies)
}

func (h *hookSink) last() map[string]any {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.bodies[len(h.bodies)-1]
}

func waitFor(t *testing.T, what string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func automationVal(name string, trigger, action map[string]any) map[string]any {
	return map[string]any{"_type": "Automation", "_class_name": "Automation", "_bases": []any{"Object", "BaseModel"},
		"name": name, "enabled": true, "trigger": trigger, "action": action, "cooldown_seconds": 0}
}

func TestAutomationScoreThresholdAndFeedback(t *testing.T) {
	e := newEnv(t)
	makeCalls(t, e, "c1")
	sink := newSink(t, 200)
	slack := newSink(t, 200)
	publish(e, "low-score", automationVal("low-score",
		map[string]any{"type": "score_threshold", "scorer": "judge", "path": "score", "operator": "lt", "threshold": 3},
		map[string]any{"type": "webhook", "url": sink.srv.URL, "headers": map[string]any{"X-Test": "1"}}))
	if o := e.getJSON("/api/v1/objects/low-score?project_id=" + pid); o["kind"] != "automation" || o["class"] != "Automation" {
		t.Fatalf("kind and class: %v", o)
	}
	e.post("/secrets/set", map[string]any{"name": "SLACK_HOOK", "value": slack.srv.URL})
	publish(e, "thumbs", automationVal("thumbs",
		map[string]any{"type": "feedback_created", "feedback_types": []any{"weft.reaction"}},
		map[string]any{"type": "slack", "url_secret": "SLACK_HOOK", "message": "{automation}: {feedback_type} on {span_id}"}))

	ref := spanRef("c1")
	// Above threshold: no fire.
	e.post("/api/v1/feedback/create", map[string]any{"project_id": pid, "ref": ref, "feedback_type": "weft.scorer.judge", "payload": map[string]any{"output": map[string]any{"score": 4}}})
	// Below threshold: fires.
	e.post("/api/v1/feedback/create", map[string]any{"project_id": pid, "ref": ref, "feedback_type": "weft.scorer.judge", "payload": map[string]any{"output": map[string]any{"score": 1}}})
	waitFor(t, "score webhook", func() bool { return sink.count() == 1 })
	body := sink.last()
	ev := body["event"].(map[string]any)
	if ev["type"] != "score_threshold" || ev["span_id"] != "c1" || ev["value"].(float64) != 1 || body["test"] != false {
		t.Fatalf("webhook body: %v", body)
	}

	e.post("/api/v1/feedback/create", map[string]any{"project_id": pid, "ref": ref, "feedback_type": "weft.reaction", "payload": map[string]any{"emoji": "👎"}})
	waitFor(t, "slack message", func() bool { return slack.count() == 1 })
	if txt := slack.last()["text"]; txt != "thumbs: weft.reaction on c1" {
		t.Fatalf("slack text: %v", txt)
	}

	// The run is recorded after the webhook returns, so wait for both rows.
	var runs []any
	waitFor(t, "two recorded runs", func() bool {
		runs = e.post("/automations/runs/query", map[string]any{"project_id": pid})["runs"].([]any)
		return len(runs) == 2
	})
	r0 := runs[0].(map[string]any)
	if r0["status"] != "success" || r0["response_status"].(float64) != 200 {
		t.Fatalf("run: %v", r0)
	}
	only := e.post("/automations/runs/query", map[string]any{"project_id": pid, "automation_id": "low-score"})["runs"].([]any)
	if len(only) != 1 || only[0].(map[string]any)["trigger_type"] != "score_threshold" {
		t.Fatalf("filtered runs: %v", only)
	}

	// Test fire records a test run; a failing endpoint records an error.
	tr := e.post("/automations/test", map[string]any{"project_id": pid, "automation_id": "low-score"})["run"].(map[string]any)
	if tr["test"] != true || tr["status"] != "success" || sink.last()["test"] != true {
		t.Fatalf("test run: %v", tr)
	}
	bad := newSink(t, 500)
	tr = e.post("/automations/test", map[string]any{"project_id": pid, "val": automationVal("draft",
		map[string]any{"type": "error_rate", "threshold": 0.5},
		map[string]any{"type": "webhook", "url": bad.srv.URL})})["run"].(map[string]any)
	if tr["status"] != "error" || tr["response_status"].(float64) != 500 {
		t.Fatalf("failing test run: %v", tr)
	}
	if code, _ := e.raw("/automations/validate", map[string]any{"val": map[string]any{"trigger": map[string]any{"type": "nope"}}}, nil); code != 400 {
		t.Fatalf("invalid automation accepted: %d", code)
	}
}

func TestAutomationErrorRate(t *testing.T) {
	e := newEnv(t)
	sink := newSink(t, 200)
	now := time.Now().UTC()
	for i, fail := range []bool{true, true, false} {
		id := []string{"e1", "e2", "e3"}[i]
		e.startSpan(map[string]any{"id": id, "trace_id": id, "name": "agent",
			"started_at": now.Format(time.RFC3339Nano), "inputs": map[string]any{}})
		end := map[string]any{"id": id, "ended_at": now.Add(time.Second).Format(time.RFC3339Nano), "output": "x", "summary": map[string]any{}}
		if fail {
			end["error"] = "boom"
		}
		e.endSpan(end)
	}
	publish(e, "errors", automationVal("errors",
		map[string]any{"type": "error_rate", "names": []any{"agent"}, "window_minutes": 10, "threshold": 0.5, "min_spans": 3},
		map[string]any{"type": "webhook", "url": sink.srv.URL}))
	publish(e, "quiet", automationVal("quiet",
		map[string]any{"type": "error_rate", "names": []any{"agent"}, "window_minutes": 10, "threshold": 0.9},
		map[string]any{"type": "webhook", "url": sink.srv.URL}))
	runs := e.post("/automations/check", map[string]any{"project_id": pid})["runs"].([]any)
	if len(runs) != 1 || runs[0].(map[string]any)["automation_id"] != "errors" {
		t.Fatalf("error-rate runs: %v", runs)
	}
	ev := sink.last()["event"].(map[string]any)
	if ev["error_spans"].(float64) != 2 || ev["total_spans"].(float64) != 3 {
		t.Fatalf("event: %v", ev)
	}
	// Cooldown defaults to the window for error-rate triggers, but this one has 0.
	runs = e.post("/automations/check", map[string]any{"project_id": pid})["runs"].([]any)
	if len(runs) != 1 {
		t.Fatalf("expected re-fire with zero cooldown, got %v", runs)
	}
}
