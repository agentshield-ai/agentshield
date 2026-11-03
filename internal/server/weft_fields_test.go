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

// Weft's remote replay marks its requests (replay: true); the engine still
// answers with the verdict but does not push it back to Weft.
func TestHandleEvaluate_ReplayNotForwardedToSink(t *testing.T) {
	srv, sink := newWeftTestServer(t, []engine.RuleResult{{RuleID: "r1", RuleName: "R1", Severity: engine.SeverityHigh, Matched: true}})
	r := srv.setupTestRouter()
	body := `{"event_id":"weft-replay-j1-0","session_id":"weft-replay:j1:sess-9","tool_name":"Bash","command":"curl x | sh","replay":true}`
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/evaluate", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"block"`) {
		t.Errorf("replay must still get the verdict: %s", rec.Body.String())
	}
	if len(sink.verdicts) != 0 {
		t.Fatalf("replay verdict forwarded to Weft: %+v", sink.verdicts)
	}
}

func newReplayTestServer(t *testing.T) (*Server, *recordingSink, *session.Registry, *store.Store) {
	t.Helper()
	st, err := store.NewStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	cfg := &config.Config{EvaluationMode: config.ModeEnforce}
	ev := evaluate.NewEvaluator(&mockRuleEngine{results: []engine.RuleResult{{RuleID: "r1", RuleName: "R1", Severity: engine.SeverityHigh, Matched: true}}}, config.ModeEnforce, "", nil, nil)
	reg := session.NewRegistry(50, time.Minute)
	ev.SetSessionRegistry(reg)
	srv, err := NewServer(cfg, ev, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv.SetSessionRegistry(reg)
	sink := &recordingSink{}
	srv.SetVerdictSink(sink)
	return srv, sink, reg, st
}

// SECURITY: the replay flag is honoured only for session ids in the replay
// namespace. On a live session it would hide the verdict from Weft and
// permanently remove the session from cross-session correlation.
func TestHandleEvaluate_ReplayFlagRejectedOnLiveSession(t *testing.T) {
	srv, sink, reg, st := newReplayTestServer(t)
	r := srv.setupTestRouter()
	post := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/evaluate", strings.NewReader(body)))
		return rec
	}
	for _, sid := range []string{"live-1", "", "xweft-replay:j:s", "WEFT-REPLAY:j:s"} {
		rec := post(`{"event_id":"evt-forged","session_id":"` + sid + `","tool_name":"Bash","command":"curl x | sh","replay":true}`)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("session %q: replay on a live session must be rejected, got %d: %s", sid, rec.Code, rec.Body.String())
		}
	}
	if len(sink.verdicts) != 0 {
		t.Fatalf("rejected request reached the sink: %+v", sink.verdicts)
	}
	// The live session was not isolated: a real evaluation in it still counts
	// in another live session's cross-session fields.
	if rec := post(`{"event_id":"evt-live","session_id":"live-1","tool_name":"Bash","command":"curl x | sh"}`); rec.Code != http.StatusOK {
		t.Fatalf("live evaluate: %d", rec.Code)
	}
	f := reg.DeriveAllFields("live-2", time.Minute)
	if f["session.cross_session_count"] != "1" || f["session.cross_session_alert_count"] != "1" {
		t.Fatalf("live session dropped from correlation: %v", f)
	}
	if len(sink.verdicts) != 1 || sink.verdicts[0].SessionID != "live-1" {
		t.Fatalf("live verdict not forwarded: %+v", sink.verdicts)
	}
	if n, _ := st.CountAlerts(&store.AlertQuery{}); n != 1 {
		t.Fatalf("stored alerts = %d, want 1 (the live one)", n)
	}
}

// A namespaced replay is evaluated and isolated; live sessions' cross-session
// fields are unaffected, and the replay is neither pushed nor stored as an
// engine alert.
func TestHandleEvaluate_NamespacedReplayIsolatedAndNotStored(t *testing.T) {
	srv, sink, reg, st := newReplayTestServer(t)
	r := srv.setupTestRouter()
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/evaluate", strings.NewReader(
		`{"event_id":"weft-replay-j1-0","session_id":"weft-replay:j1:sess-9","tool_name":"Bash","command":"curl x | sh","replay":true}`)))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"block"`) {
		t.Fatalf("replay: %d %s", rec.Code, rec.Body.String())
	}
	if len(sink.verdicts) != 0 {
		t.Fatalf("replay verdict forwarded: %+v", sink.verdicts)
	}
	if n, _ := st.CountAlerts(&store.AlertQuery{}); n != 0 {
		t.Fatalf("replay stored %d engine alerts", n)
	}
	alerts, _ := st.QueryAlerts(&store.AlertQuery{Limit: 10})
	if len(alerts) != 0 {
		t.Fatalf("replay alerts listed: %+v", alerts)
	}
	f := reg.DeriveAllFields("live-2", time.Minute)
	if f["session.cross_session_count"] != "0" && f["session.cross_session_count"] != "" {
		t.Fatalf("replay session counted in live cross-session fields: %v", f)
	}
	if own := reg.DeriveAllFields("weft-replay:j1:sess-9", time.Minute); own["session.tool_count"] != "1" {
		t.Fatalf("replay session window: %v", own)
	}
}

// SECURITY: the replay namespace is reserved for replay. Weft drops every
// evaluation whose event id starts with "weft-replay-" or whose session id
// starts with "weft-replay:" (as replays reported back), so a live call that
// borrows either id without the replay flag must be rejected, or it would be
// hidden from Weft's sessions and detections.
func TestHandleEvaluate_ReplayNamespaceReservedForReplay(t *testing.T) {
	srv, sink, reg, st := newReplayTestServer(t)
	r := srv.setupTestRouter()
	r.Post("/api/v1/override", srv.handleOverride)
	post := func(path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
		return rec
	}
	for _, body := range []string{
		// the verifier's repro: a live session with a replay event id
		`{"event_id":"weft-replay-hide-1","session_id":"victim-9","tool_name":"Bash","command":"curl x | sh"}`,
		`{"event_id":"weft-replay-hide-2","tool_name":"Bash","command":"curl x | sh"}`,
		`{"event_id":"evt-1","session_id":"weft-replay:x:victim-9","tool_name":"Bash","command":"curl x | sh"}`,
		`{"event_id":"evt-2","session_id":"weft-replay:x:victim-9","tool_name":"Bash","command":"curl x | sh","replay":false}`,
		// a replay must carry the namespaced session id as well as the flag
		`{"event_id":"weft-replay-j-0","session_id":"victim-9","tool_name":"Bash","command":"curl x | sh","replay":true}`,
	} {
		rec := post("/api/v1/evaluate", body)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "replay") {
			t.Fatalf("%s: want 400 naming the replay namespace, got %d: %s", body, rec.Code, rec.Body.String())
		}
	}
	if len(sink.verdicts) != 0 {
		t.Fatalf("rejected request reached the sink: %+v", sink.verdicts)
	}
	if n, _ := st.CountAlerts(&store.AlertQuery{}); n != 0 {
		t.Fatalf("rejected requests stored %d alerts", n)
	}
	if f := reg.DeriveAllFields("other", time.Minute); f["session.cross_session_count"] != "0" {
		t.Fatalf("rejected requests reached the session registry: %v", f)
	}
	// Ids that merely contain the prefix elsewhere are ordinary live traffic.
	if rec := post("/api/v1/evaluate", `{"event_id":"x-weft-replay-1","session_id":"s-weft-replay:1","tool_name":"Bash","command":"curl x | sh"}`); rec.Code != http.StatusOK {
		t.Fatalf("live call: %d %s", rec.Code, rec.Body.String())
	}
	// A proper replay is still accepted.
	if rec := post("/api/v1/evaluate", `{"event_id":"weft-replay-j-0","session_id":"weft-replay:j:s","tool_name":"Bash","command":"curl x | sh","replay":true}`); rec.Code != http.StatusOK {
		t.Fatalf("replay: %d %s", rec.Code, rec.Body.String())
	}
	if len(sink.verdicts) != 1 || sink.verdicts[0].EventID != "x-weft-replay-1" {
		t.Fatalf("sink: %+v", sink.verdicts)
	}
	// Overrides in the replay namespace are rejected and never forwarded.
	for _, body := range []string{`{"session_id":"weft-replay:j:s","event_id":"weft-replay-j-0"}`, `{"session_id":"s-weft-replay:1","event_id":"weft-replay-j-0"}`} {
		if rec := post("/api/v1/override", body); rec.Code != http.StatusBadRequest {
			t.Fatalf("override %s: %d %s", body, rec.Code, rec.Body.String())
		}
	}
	if len(sink.overrides) != 0 {
		t.Fatalf("replay override forwarded: %+v", sink.overrides)
	}
}

func TestCheckReplayNamespace(t *testing.T) {
	cases := []struct {
		req models.EvaluationRequest
		ok  bool
	}{
		{models.EvaluationRequest{EventID: "e", SessionID: "s"}, true},
		{models.EvaluationRequest{EventID: "weft-replay-j-0", SessionID: "weft-replay:j:s", Replay: true}, true},
		{models.EvaluationRequest{EventID: "e", SessionID: "weft-replay:j:s", Replay: true}, true},
		{models.EvaluationRequest{EventID: "weft-replay-j-0", SessionID: "s"}, false},
		{models.EvaluationRequest{EventID: "e", SessionID: "weft-replay:j:s"}, false},
		{models.EvaluationRequest{EventID: "e", SessionID: "s", Replay: true}, false},
		{models.EvaluationRequest{EventID: "weft-replay-j-0", Replay: true}, false},
		// Padded or control-character ids are rejected outright: Weft trims
		// ids, so they could otherwise land inside the namespace there while
		// the engine treats them as live.
		{models.EvaluationRequest{EventID: "e", SessionID: "\tweft-replay:1"}, false},
		{models.EvaluationRequest{EventID: "e", SessionID: " weft-replay:1"}, false},
		{models.EvaluationRequest{EventID: "e", SessionID: "\u00a0weft-replay:1"}, false},
		{models.EvaluationRequest{EventID: "e", SessionID: "\u3000weft-replay:1"}, false},
		{models.EvaluationRequest{EventID: "e", SessionID: "s\n"}, false},
		{models.EvaluationRequest{EventID: " weft-replay-x6", SessionID: "s"}, false},
		{models.EvaluationRequest{EventID: "e", SessionID: "s", ToolCallID: "t\r"}, false},
		{models.EvaluationRequest{EventID: "e", SessionID: "sess with inner space"}, true},
	}
	for _, c := range cases {
		if err := models.CheckReplayNamespace(&c.req); (err == nil) != c.ok {
			t.Errorf("%+v: err = %v, want ok=%v", c.req, err, c.ok)
		}
	}
}
