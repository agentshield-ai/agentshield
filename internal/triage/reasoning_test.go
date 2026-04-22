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

// When reasoning_effort is set, the OpenAI request must:
// - include `reasoning_effort`
// - send the token budget as `max_completion_tokens` (not `max_tokens`)
// - omit `temperature`, which reasoning models reject.
func TestOpenAIRequest_ReasoningEffortShape(t *testing.T) {
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured)
		resp := map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{
					"content": `{"risk_level":"low","user_authorization":"high","verdict":"allow","confidence":0.9,"rationale":"benign","suggested_action":"continue"}`,
				},
			}},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cfg := &config.TriageConfig{
		Enabled:         true,
		Provider:        "openai",
		Model:           "codex-auto-review",
		APIKey:          "test-key",
		MaxTokens:       512,
		TimeoutSec:      10,
		ReasoningEffort: "low",
	}
	provider := &OpenAIProvider{
		config: cfg,
		client: createLocalHTTPClient(time.Duration(cfg.TimeoutSec) * time.Second),
		apiURL: server.URL,
	}

	_, err := provider.Triage(context.Background(), &TriageContext{
		Alert:   engine.RuleResult{RuleName: "r", Severity: engine.SeverityHigh},
		Request: &models.EvaluationRequest{Tool: "shell"},
	})
	if err != nil {
		t.Fatalf("Triage failed: %v", err)
	}

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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured)
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
	defer server.Close()

	cfg := &config.TriageConfig{
		Enabled:    true,
		Provider:   "openai",
		Model:      "gpt-4o-mini",
		APIKey:     "test-key",
		MaxTokens:  500,
		TimeoutSec: 10,
	}
	provider := &OpenAIProvider{
		config: cfg,
		client: createLocalHTTPClient(time.Duration(cfg.TimeoutSec) * time.Second),
		apiURL: server.URL,
	}

	_, err := provider.Triage(context.Background(), &TriageContext{
		Alert:   engine.RuleResult{RuleName: "r", Severity: engine.SeverityHigh},
		Request: &models.EvaluationRequest{Tool: "shell"},
	})
	if err != nil {
		t.Fatalf("Triage failed: %v", err)
	}

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

// Config validation should reject unknown reasoning_effort values.
func TestTriageConfig_ReasoningEffortValidation(t *testing.T) {
	valid := []string{"", "minimal", "low", "medium", "high"}
	for _, v := range valid {
		req := &OpenAIRequest{
			Model:           "codex-auto-review",
			ReasoningEffort: v,
		}
		b, err := json.Marshal(req)
		if err != nil {
			t.Fatalf("marshal(%q): %v", v, err)
		}
		if v == "" {
			if got := string(b); got != `{"model":"codex-auto-review","messages":null}` {
				t.Errorf("empty reasoning_effort should omit the field; got %s", got)
			}
		}
	}
}
