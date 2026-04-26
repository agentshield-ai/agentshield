package proxyadapter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/agentshield-ai/agentshield/internal/models"
)

// stubForwarder records every request the adapter would send to AgentShield,
// so tests can assert on the normalised shape end-to-end.
type stubForwarder struct {
	mu       sync.Mutex
	received []*models.EvaluationRequest
	failOnce bool
}

func (s *stubForwarder) Post(req *models.EvaluationRequest) (*EvalResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failOnce {
		s.failOnce = false
		return nil, http.ErrServerClosed // arbitrary non-nil error
	}
	cp := *req
	s.received = append(s.received, &cp)
	return &EvalResponse{Action: "allow"}, nil
}

func TestRun_NDJSON_HappyPath(t *testing.T) {
	input := strings.Join([]string{
		`{"request_id":"r1","method":"GET","url":"https://example.com/","session_id":"s1"}`,
		`{"request_id":"r2","method":"POST","url":"https://discord.com/api/webhooks/x","session_id":"s1","decision":"flag"}`,
		``, // blank line — should be skipped silently
		`   `, // whitespace-only — also skipped
	}, "\n")

	fwd := &stubForwarder{}
	stats, err := Run(context.Background(), strings.NewReader(input), fwd, "crabtrap")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.Lines != 4 || stats.Parsed != 2 || stats.Forwarded != 2 || stats.Dropped != 0 {
		t.Fatalf("stats = %+v", stats)
	}
	if len(fwd.received) != 2 {
		t.Fatalf("received %d, want 2", len(fwd.received))
	}
	if fwd.received[1].Fields["proxy.verdict"] != "flag" {
		t.Errorf("proxy.verdict = %q", fwd.received[1].Fields["proxy.verdict"])
	}
}

func TestRun_MalformedLines_Skipped(t *testing.T) {
	input := strings.Join([]string{
		`{not json`,
		`{"request_id":"r1","method":"GET","url":"https://x/"}`,
		`{"method":"GET"}`, // missing required fields → ErrInvalid
	}, "\n")

	fwd := &stubForwarder{}
	stats, err := Run(context.Background(), strings.NewReader(input), fwd, "crabtrap")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.Forwarded != 1 {
		t.Errorf("Forwarded = %d, want 1", stats.Forwarded)
	}
	if stats.Dropped != 2 {
		t.Errorf("Dropped = %d, want 2 (one parse failure + one normalise failure)", stats.Dropped)
	}
}

func TestRun_PostFailure_LogsContinues(t *testing.T) {
	input := strings.Join([]string{
		`{"request_id":"r1","method":"GET","url":"https://x/"}`,
		`{"request_id":"r2","method":"GET","url":"https://y/"}`,
	}, "\n")

	fwd := &stubForwarder{failOnce: true}
	stats, _ := Run(context.Background(), strings.NewReader(input), fwd, "crabtrap")
	if stats.Forwarded != 1 || stats.Dropped != 1 {
		t.Errorf("stats = %+v, want forwarded=1 dropped=1", stats)
	}
}

func TestRun_AgainstLiveHTTPServer(t *testing.T) {
	// End-to-end: spin up an httptest server that mimics /api/v1/evaluate,
	// hand the adapter a real HTTP Client, and assert that the request body
	// matches the normalised schema. Validates the wire shape AgentShield's
	// engine will receive.
	var got []map[string]any
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		got = append(got, body)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"action":"allow","cached":false}`))
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "")
	input := `{"request_id":"req-7","method":"POST","url":"https://discord.com/webhook","decision":"block"}`

	stats, err := Run(context.Background(), strings.NewReader(input), client, "crabtrap")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.Forwarded != 1 {
		t.Fatalf("Forwarded = %d", stats.Forwarded)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("server got %d requests", len(got))
	}
	body := got[0]
	if body["event_id"] != "req-7" {
		t.Errorf("event_id = %v", body["event_id"])
	}
	if body["tool"] != "http_egress" {
		t.Errorf("tool = %v", body["tool"])
	}
	fields, _ := body["fields"].(map[string]any)
	if fields["proxy.verdict"] != "block" {
		t.Errorf("fields.proxy.verdict = %v", fields["proxy.verdict"])
	}
	if fields["command"] != "POST https://discord.com/webhook" {
		t.Errorf("fields.command = %v", fields["command"])
	}
}
