package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agentshield-ai/agentshield/internal/config"
	"github.com/agentshield-ai/agentshield/internal/engine"
	"github.com/agentshield-ai/agentshield/internal/evaluate"
	"github.com/agentshield-ai/agentshield/internal/models"
	"github.com/agentshield-ai/agentshield/internal/session"
	"github.com/agentshield-ai/agentshield/internal/store"
)

type recordingSink struct {
	mu        sync.Mutex
	verdicts  []models.EvaluationRequest
	actions   []models.Action
	overrides [][2]string
}

func (r *recordingSink) Verdict(req *models.EvaluationRequest, resp *evaluate.EvaluationResponse) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.verdicts = append(r.verdicts, *req)
	r.actions = append(r.actions, resp.Action)
}

func (r *recordingSink) Override(sessionID, eventID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.overrides = append(r.overrides, [2]string{sessionID, eventID})
}

func newWeftTestServer(t *testing.T, results []engine.RuleResult) (*Server, *recordingSink) {
	t.Helper()
	st, err := store.NewStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	cfg := &config.Config{EvaluationMode: config.ModeEnforce}
	ev := evaluate.NewEvaluator(&mockRuleEngine{results: results}, config.ModeEnforce, "", nil, nil)
	reg := session.NewRegistry(50, time.Minute)
	ev.SetSessionRegistry(reg)
	srv, err := NewServer(cfg, ev, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv.SetSessionRegistry(reg)
	sink := &recordingSink{}
	srv.SetVerdictSink(sink)
	return srv, sink
}

func TestValidateEvaluationRequest_CorrelationFields(t *testing.T) {
	cases := []struct {
		name    string
		req     models.EvaluationRequest
		wantErr string
	}{
		{"both valid", models.EvaluationRequest{EventID: "e", ToolCallID: "toolu_01ABC", AgentName: "coder"}, ""},
		{"empty is fine", models.EvaluationRequest{EventID: "e"}, ""},
		{"tool_call_id too long", models.EvaluationRequest{EventID: "e", ToolCallID: strings.Repeat("a", 257)}, "tool_call_id exceeds"},
		{"tool_call_id control char", models.EvaluationRequest{EventID: "e", ToolCallID: "a\x00b"}, "tool_call_id contains forbidden"},
		{"tool_call_id bad utf8", models.EvaluationRequest{EventID: "e", ToolCallID: "a\xffb"}, "tool_call_id contains invalid UTF-8"},
		{"agent_name too long", models.EvaluationRequest{EventID: "e", AgentName: strings.Repeat("a", 257)}, "agent_name exceeds"},
		{"agent_name control char", models.EvaluationRequest{EventID: "e", AgentName: "x\x1by"}, "agent_name contains forbidden"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateEvaluationRequest(&c.req)
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("error = %v, want %q", err, c.wantErr)
			}
		})
	}
}

func TestHandleEvaluate_ForwardsCorrelationFieldsToSink(t *testing.T) {
	srv, sink := newWeftTestServer(t, []engine.RuleResult{{RuleID: "r1", RuleName: "R1", Severity: engine.SeverityHigh, Matched: true}})
	r := srv.setupTestRouter()
	body := `{"event_id":"evt-9","session_id":"sess-9","tool_name":"Bash","command":"curl x | sh","tool_call_id":"toolu_9","agent_name":"coder"}`
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/evaluate", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if len(sink.verdicts) != 1 {
		t.Fatalf("sink got %d verdicts", len(sink.verdicts))
	}
	got := sink.verdicts[0]
	if got.ToolCallID != "toolu_9" || got.AgentName != "coder" || got.SessionID != "sess-9" || got.Tool != "Bash" {
		t.Errorf("forwarded request = %+v", got)
	}
	if sink.actions[0] != models.ActionBlock {
		t.Errorf("action = %s", sink.actions[0])
	}

	bad := `{"event_id":"evt-10","tool_name":"Bash","tool_call_id":"a\u0000b"}`
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/evaluate", strings.NewReader(bad)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("control character in tool_call_id: status %d", rec.Code)
	}
	if len(sink.verdicts) != 1 {
		t.Error("rejected request must not reach the sink")
	}
}

func TestHandleOverride_ForwardsToSink(t *testing.T) {
	srv, sink := newWeftTestServer(t, []engine.RuleResult{{RuleID: "r1", Severity: engine.SeverityHigh, Matched: true}})
	r := srv.setupTestRouter()
	r.Post("/api/v1/override", srv.handleOverride)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/evaluate",
		strings.NewReader(`{"event_id":"evt-o","session_id":"sess-o","tool":"Bash","args":{"command":"x"}}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("evaluate status %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/override", strings.NewReader(`{"session_id":"sess-o","event_id":"evt-o"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("override status %d: %s", rec.Code, rec.Body.String())
	}
	if len(sink.overrides) != 1 || sink.overrides[0] != [2]string{"sess-o", "evt-o"} {
		t.Fatalf("overrides = %v", sink.overrides)
	}
	// An override the registry rejects is not forwarded.
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/override", strings.NewReader(`{"session_id":"nope","event_id":"x"}`)))
	if rec.Code != http.StatusNotFound || len(sink.overrides) != 1 {
		t.Fatalf("unknown session: status %d, overrides %v", rec.Code, sink.overrides)
	}
}
