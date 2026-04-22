package triage

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/agentshield-ai/agentshield/internal/config"
	"github.com/agentshield-ai/agentshield/internal/engine"
	"github.com/agentshield-ai/agentshield/internal/models"
)

// newCapturingOpenAIServer spins up an httptest server that records the
// decoded JSON request body into *captured and returns a canned triage
// response so the caller can assert on the outgoing request shape.
func newCapturingOpenAIServer(t *testing.T, captured *map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		if err := json.Unmarshal(body, captured); err != nil {
			t.Errorf("unmarshal request body: %v", err)
		}
		resp := map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{
					"content": `{"risk_level":"low","user_authorization":"high","verdict":"allow","confidence":0.9,"rationale":"r","suggested_action":"s"}`,
				},
			}},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

func newTestOpenAIProvider(cfg *config.TriageConfig, url string) *OpenAIProvider {
	return &OpenAIProvider{
		config: cfg,
		client: createLocalHTTPClient(time.Duration(cfg.TimeoutSec) * time.Second),
		apiURL: url,
	}
}

func runTriageAgainst(t *testing.T, provider *OpenAIProvider) {
	t.Helper()
	_, err := provider.Triage(context.Background(), &TriageContext{
		Alert:   engine.RuleResult{RuleName: "r", Severity: engine.SeverityHigh},
		Request: &models.EvaluationRequest{Tool: "shell"},
	})
	if err != nil {
		t.Fatalf("Triage failed: %v", err)
	}
}

// When reasoning_effort is set, the OpenAI request must:
// - include `reasoning_effort`
// - send the token budget as `max_completion_tokens` (not `max_tokens`)
// - omit `temperature`, which reasoning models reject.
func TestOpenAIRequest_ReasoningEffortShape(t *testing.T) {
	var captured map[string]any
	server := newCapturingOpenAIServer(t, &captured)
	defer server.Close()

	cfg := &config.TriageConfig{
		Enabled:         true,
		Provider:        "openai",
		Model:           "codex-auto-review",
		APIKey:          "test-key",
		MaxTokens:       512,
		TimeoutSec:      10,
		ReasoningEffort: config.ReasoningEffortLow,
	}
	runTriageAgainst(t, newTestOpenAIProvider(cfg, server.URL))

	if got := captured["reasoning_effort"]; got != "low" {
		t.Errorf("reasoning_effort=%v; want low", got)
	}
	if _, present := captured["temperature"]; present {
		t.Error("temperature must be omitted for reasoning models")
	}
	if _, present := captured["max_tokens"]; present {
		t.Error("max_tokens must be omitted for reasoning models; use max_completion_tokens")
	}
	if got := captured["max_completion_tokens"]; got != float64(512) {
		t.Errorf("max_completion_tokens=%v; want 512", got)
	}
	if got := captured["model"]; got != "codex-auto-review" {
		t.Errorf("model=%v; want codex-auto-review", got)
	}
}

// When reasoning_effort is unset (the default / gpt-4o-mini path), the
// request must keep the legacy shape: max_tokens + temperature + no
// reasoning_effort.
func TestOpenAIRequest_LegacyShapeWhenNoReasoningEffort(t *testing.T) {
	var captured map[string]any
	server := newCapturingOpenAIServer(t, &captured)
	defer server.Close()

	cfg := &config.TriageConfig{
		Enabled:    true,
		Provider:   "openai",
		Model:      "gpt-4o-mini",
		APIKey:     "test-key",
		MaxTokens:  500,
		TimeoutSec: 10,
	}
	runTriageAgainst(t, newTestOpenAIProvider(cfg, server.URL))

	if _, present := captured["reasoning_effort"]; present {
		t.Error("reasoning_effort must be omitted when config.ReasoningEffort is empty")
	}
	if got := captured["max_tokens"]; got != float64(500) {
		t.Errorf("max_tokens=%v; want 500", got)
	}
	if _, present := captured["max_completion_tokens"]; present {
		t.Error("max_completion_tokens must be omitted on the legacy path")
	}
	if got := captured["temperature"]; got != 0.1 {
		t.Errorf("temperature=%v; want 0.1", got)
	}
}

// Every documented reasoning_effort value round-trips through JSON, and the
// empty value is omitted so non-reasoning requests stay unchanged.
func TestOpenAIRequest_ReasoningEffortRoundtrip(t *testing.T) {
	cases := []struct {
		effort   config.ReasoningEffort
		wantKey  bool
		wantText string
	}{
		{"", false, ""},
		{config.ReasoningEffortMinimal, true, "minimal"},
		{config.ReasoningEffortLow, true, "low"},
		{config.ReasoningEffortMedium, true, "medium"},
		{config.ReasoningEffortHigh, true, "high"},
	}
	for _, c := range cases {
		t.Run(string(c.effort), func(t *testing.T) {
			req := &OpenAIRequest{Model: "codex-auto-review", ReasoningEffort: c.effort}
			b, err := json.Marshal(req)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var decoded map[string]any
			if err := json.Unmarshal(b, &decoded); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			got, present := decoded["reasoning_effort"]
			if c.wantKey != present {
				t.Errorf("reasoning_effort present=%v; want %v (json=%s)", present, c.wantKey, b)
			}
			if c.wantKey && got != c.wantText {
				t.Errorf("reasoning_effort=%v; want %s", got, c.wantText)
			}
		})
	}
}
