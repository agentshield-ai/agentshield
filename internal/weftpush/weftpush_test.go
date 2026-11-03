package weftpush

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentshield-ai/agentshield/internal/config"
	"github.com/agentshield-ai/agentshield/internal/engine"
	"github.com/agentshield-ai/agentshield/internal/evaluate"
	"github.com/agentshield-ai/agentshield/internal/models"
)

func testPusher(t *testing.T, url string, send string, queue int) *Pusher {
	t.Helper()
	t.Setenv("WEFT_TEST_KEY", "k-123")
	p, err := New(config.WeftConfig{Endpoint: url, Project: "team/app", APIKeyEnv: "WEFT_TEST_KEY", Send: send, QueueSize: queue, TimeoutSec: 5})
	if err != nil {
		t.Fatal(err)
	}
	p.Backoff = func(int) time.Duration { return time.Millisecond }
	return p
}

func verdict(id string, action models.Action, alerts ...engine.RuleResult) (*models.EvaluationRequest, *evaluate.EvaluationResponse) {
	return &models.EvaluationRequest{EventID: id, SessionID: "sess", Tool: "Bash", ToolCallID: "tc-" + id, AgentName: "coder", Source: "claude-code"},
		&evaluate.EvaluationResponse{EventID: id, Action: action, Alerts: alerts, EffectiveMode: config.ModeEnforce, Timestamp: time.Unix(1700000000, 0)}
}

type collector struct {
	mu       sync.Mutex
	payloads []Payload
	auth     []string
}

func (c *collector) handler(status func(n int) int) http.HandlerFunc {
	var n atomic.Int32
	return func(w http.ResponseWriter, r *http.Request) {
		i := int(n.Add(1))
		if r.URL.Path != IngestPath {
			http.Error(w, "bad path", http.StatusNotFound)
			return
		}
		code := http.StatusOK
		if status != nil {
			code = status(i)
		}
		if code == http.StatusOK {
			var p Payload
			_ = json.NewDecoder(r.Body).Decode(&p)
			c.mu.Lock()
			c.payloads = append(c.payloads, p)
			c.auth = append(c.auth, r.Header.Get("Authorization"))
			c.mu.Unlock()
		}
		w.WriteHeader(code)
	}
}

func (c *collector) detections() []Detection {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []Detection
	for _, p := range c.payloads {
		out = append(out, p.Detections...)
	}
	return out
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestPusher_SendsNonAllowVerdictsWithAuthAndShape(t *testing.T) {
	c := &collector{}
	srv := httptest.NewServer(c.handler(nil))
	defer srv.Close()
	p := testPusher(t, srv.URL, "", 10)
	p.Start(context.Background())
	defer p.Close(context.Background())

	p.Verdict(verdict("a", models.ActionAllow))
	p.Verdict(verdict("b", models.ActionBlock, engine.RuleResult{RuleID: "r1", RuleName: "Pipe to shell", Severity: engine.SeverityCritical, Tags: []string{"attack.t1059"}}))
	waitFor(t, func() bool { return len(c.detections()) == 1 })

	d := c.detections()[0]
	if d.EventID != "b" || d.Action != "block" || d.Mode != "enforce" || d.Severity != "critical" || d.Source != "engine" ||
		d.ToolCallID != "tc-b" || d.AgentName != "coder" || d.SessionID != "sess" || d.ToolName != "Bash" || d.EventSource != "claude-code" {
		t.Errorf("detection = %+v", d)
	}
	if len(d.Rules) != 1 || d.Rules[0].ID != "r1" || d.Rules[0].Level != "critical" || d.Rules[0].Tags[0] != "attack.t1059" {
		t.Errorf("rules = %+v", d.Rules)
	}
	if d.Timestamp != "2023-11-14T22:13:20Z" {
		t.Errorf("timestamp = %s", d.Timestamp)
	}
	c.mu.Lock()
	if c.payloads[0].ProjectID != "team/app" || c.auth[0] != "Bearer k-123" {
		t.Errorf("project %q auth %q", c.payloads[0].ProjectID, c.auth[0])
	}
	c.mu.Unlock()
	if st := p.Stats(); st.Sent != 1 || st.Dropped != 0 {
		t.Errorf("stats = %+v", st)
	}
}

func TestPusher_SendAllIncludesAllow(t *testing.T) {
	c := &collector{}
	srv := httptest.NewServer(c.handler(nil))
	defer srv.Close()
	p := testPusher(t, srv.URL, config.WeftSendAll, 10)
	p.Start(context.Background())
	defer p.Close(context.Background())
	p.Verdict(verdict("a", models.ActionAllow))
	waitFor(t, func() bool { return len(c.detections()) == 1 })
	if c.detections()[0].Action != "allow" || c.detections()[0].Severity != "none" {
		t.Errorf("detection = %+v", c.detections()[0])
	}
}

// A hung Weft must never slow down the request path: the queue fills, further
// records are dropped and counted, and Verdict stays a non-blocking send.
func TestPusher_DropWhenFullWithoutAddedLatency(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)
	p := testPusher(t, srv.URL, config.WeftSendAll, 4)
	p.BatchSize = 1
	p.Start(context.Background())

	const n = 2000
	req, resp := verdict("x", models.ActionBlock, engine.RuleResult{RuleID: "r", Severity: engine.SeverityHigh})
	start := time.Now()
	for i := 0; i < n; i++ {
		p.Verdict(req, resp)
	}
	elapsed := time.Since(start)
	if per := elapsed / n; per > 200*time.Microsecond {
		t.Errorf("Verdict took %v per call with a hung server; it must not block", per)
	}
	st := p.Stats()
	if st.Dropped == 0 || st.Queued+st.Dropped != n {
		t.Errorf("stats = %+v, want drops and queued+dropped = %d", st, n)
	}
	if st.Queued > 4+1 { // queue capacity plus the batch the worker holds
		t.Errorf("queued %d exceeds the bound", st.Queued)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	go func() { p.Close(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not respect its context deadline")
	}
	p.Verdict(req, resp) // after Close: dropped, no panic
	if p.Stats().Dropped != st.Dropped+1 {
		t.Error("verdict after Close should be counted as dropped")
	}
}

func TestPusher_RetriesTransientFailures(t *testing.T) {
	c := &collector{}
	srv := httptest.NewServer(c.handler(func(n int) int {
		if n < 3 {
			return http.StatusServiceUnavailable
		}
		return http.StatusOK
	}))
	defer srv.Close()
	p := testPusher(t, srv.URL, "", 10)
	p.Start(context.Background())
	defer p.Close(context.Background())
	p.Verdict(verdict("r", models.ActionRequireApproval, engine.RuleResult{RuleID: "m", Severity: engine.SeverityMedium}))
	waitFor(t, func() bool { return len(c.detections()) == 1 })
	if st := p.Stats(); st.Sent != 1 || st.Failed != 0 {
		t.Errorf("stats = %+v", st)
	}
}

func TestPusher_ClientErrorIsNotRetried(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "bad", http.StatusBadRequest)
	}))
	defer srv.Close()
	p := testPusher(t, srv.URL, "", 10)
	p.Start(context.Background())
	defer p.Close(context.Background())
	p.Verdict(verdict("r", models.ActionBlock))
	waitFor(t, func() bool { return p.Stats().Failed == 1 })
	if calls.Load() != 1 {
		t.Errorf("calls = %d, want 1", calls.Load())
	}
}

func TestPusher_ForwardsOverrides(t *testing.T) {
	c := &collector{}
	srv := httptest.NewServer(c.handler(nil))
	defer srv.Close()
	p := testPusher(t, srv.URL, "", 10)
	p.Start(context.Background())
	defer p.Close(context.Background())
	p.Override("sess-1", "evt-1")
	p.Override("", "ignored") // no session: not forwarded
	waitFor(t, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return len(c.payloads) == 1
	})
	c.mu.Lock()
	defer c.mu.Unlock()
	o := c.payloads[0].Overrides
	if len(o) != 1 || o[0].SessionID != "sess-1" || o[0].EventID != "evt-1" || o[0].Timestamp == "" {
		t.Errorf("overrides = %+v", o)
	}
}

func TestPusher_StopsOnContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	p := testPusher(t, srv.URL, "", 10)
	p.Backoff = func(int) time.Duration { return time.Hour }
	ctx, cancel := context.WithCancel(context.Background())
	p.Start(ctx)
	p.Verdict(verdict("r", models.ActionBlock))
	time.Sleep(50 * time.Millisecond) // first attempt fails, worker waits on backoff
	cancel()
	select {
	case <-p.done:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not stop on context cancel")
	}
	if p.Stats().Failed != 1 {
		t.Errorf("stats = %+v", p.Stats())
	}
}

func TestPusher_FlushesQueueOnClose(t *testing.T) {
	c := &collector{}
	srv := httptest.NewServer(c.handler(nil))
	defer srv.Close()
	p := testPusher(t, srv.URL, "", 100)
	for i := 0; i < 20; i++ {
		p.Verdict(verdict(string(rune('a'+i)), models.ActionBlock))
	}
	p.Start(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p.Close(ctx)
	if got := len(c.detections()); got != 20 {
		t.Errorf("flushed %d of 20", got)
	}
}

func TestNew_RequiresEndpoint(t *testing.T) {
	if _, err := New(config.WeftConfig{}); err == nil {
		t.Error("expected error without an endpoint")
	}
}
