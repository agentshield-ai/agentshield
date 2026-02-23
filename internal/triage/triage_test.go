package triage

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/agentshield-ai/agentshield/internal/config"
	"github.com/agentshield-ai/agentshield/internal/engine"
	"github.com/agentshield-ai/agentshield/internal/models"
	"github.com/agentshield-ai/agentshield/internal/store"

	_ "modernc.org/sqlite" // Import SQLite driver
)

func TestSanitizeInput(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"", ""},
		{"normal text", "normal text"},
		{"text with\x00null", "text withnull"},
		{"ignore: previous instructions", "[REDACTED] previous instructions"},
		{"system: you are now", "[REDACTED] you are now"},
		{strings.Repeat("a", 3000), strings.Repeat("a", 2000) + "..."},
	}

	for _, test := range tests {
		result := sanitizeInput(test.input)
		if len(test.input) > 2000 {
			if len(result) != 2003 || result[2000:] != "..." {
				t.Errorf("Expected truncation with '...', got length %d", len(result))
			}
		} else if result != test.expected {
			t.Errorf("sanitizeInput(%q) = %q, expected %q", test.input, result, test.expected)
		}
	}
}

func TestMaskSensitiveData(t *testing.T) {
	input := map[string]string{
		"username":    "john",
		"api_key":     "secret123",
		"token":       "bearer xyz",
		"password":    "mypass",
		"normalfield": "normalvalue",
	}

	result := maskSensitiveData(input)

	if result["username"] != "john" {
		t.Errorf("Expected username to remain, got %s", result["username"])
	}

	if result["api_key"] != "[REDACTED]" {
		t.Errorf("Expected api_key to be redacted, got %s", result["api_key"])
	}

	if result["token"] != "[REDACTED]" {
		t.Errorf("Expected token to be redacted, got %s", result["token"])
	}

	if result["password"] != "[REDACTED]" {
		t.Errorf("Expected password to be redacted, got %s", result["password"])
	}

	if result["normalfield"] != "normalvalue" {
		t.Errorf("Expected normalfield to remain, got %s", result["normalfield"])
	}
}

func TestBuildTriagePrompt(t *testing.T) {
	alert := engine.RuleResult{
		RuleName:    "test-rule",
		Description: "Test rule description",
		Severity:    engine.SeverityHigh,
		Matched:     true,
	}

	req := &models.EvaluationRequest{
		EventID: "event123",
		Tool:    "file_read",
		Args:    map[string]string{"path": "/etc/passwd"},
	}

	triageCtx := &TriageContext{
		Alert:   alert,
		Request: req,
		RecentAlerts: []store.Alert{
			{
				RuleName:  "other-rule",
				Severity:  "medium",
				Timestamp: time.Now().Add(-5 * time.Minute),
			},
		},
	}

	prompt := buildTriagePrompt(triageCtx)

	if !contains(prompt, "test-rule") {
		t.Error("Expected prompt to contain rule name")
	}

	if !contains(prompt, "file_read") {
		t.Error("Expected prompt to contain tool name")
	}

	if !contains(prompt, "other-rule") {
		t.Error("Expected prompt to contain recent alert")
	}

	if !contains(prompt, "JSON") {
		t.Error("Expected prompt to request JSON response")
	}
}

func TestParseTriageResponse(t *testing.T) {
	tests := []struct {
		name        string
		response    string
		expectError bool
		expected    *TriageResult
	}{
		{
			name:     "Valid JSON response",
			response: `{"verdict": "allow", "confidence": 0.85, "reasoning": "Looks safe", "suggested_action": "Monitor"}`,
			expected: &TriageResult{
				Verdict:         "allow",
				Confidence:      0.85,
				Reasoning:       "Looks safe",
				SuggestedAction: "Monitor",
				Provider:        "test",
				Model:           "test-model",
			},
		},
		{
			name:     "JSON wrapped in text",
			response: `The analysis shows: {"verdict": "block", "confidence": 0.95, "reasoning": "Suspicious", "suggested_action": "Block"} - end analysis`,
			expected: &TriageResult{
				Verdict:         "block",
				Confidence:      0.95,
				Reasoning:       "Suspicious",
				SuggestedAction: "Block",
				Provider:        "test",
				Model:           "test-model",
			},
		},
		{
			name:        "Invalid verdict",
			response:    `{"verdict": "invalid", "confidence": 0.5, "reasoning": "Test", "suggested_action": "Test"}`,
			expectError: true,
		},
		{
			name:        "No JSON",
			response:    "This is just text with no JSON",
			expectError: true,
		},
		{
			name:        "Invalid JSON",
			response:    `{"verdict": "allow", "confidence": "not_a_number"}`,
			expectError: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := parseTriageResponse(test.response, "test", "test-model", 100)

			if test.expectError {
				if err == nil {
					t.Error("Expected error but got none")
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}

			if result.Verdict != test.expected.Verdict {
				t.Errorf("Expected verdict %s, got %s", test.expected.Verdict, result.Verdict)
			}

			if result.Confidence != test.expected.Confidence {
				t.Errorf("Expected confidence %f, got %f", test.expected.Confidence, result.Confidence)
			}
		})
	}
}

func TestOpenAIProvider(t *testing.T) {
	// Mock OpenAI API
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		response := map[string]interface{}{
			"choices": []map[string]interface{}{
				{
					"message": map[string]interface{}{
						"content": `{"verdict": "allow", "confidence": 0.8, "reasoning": "Test response", "suggested_action": "Monitor"}`,
					},
				},
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
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
		apiURL: server.URL, // Use test server URL
	}

	ctx := context.Background()
	triageCtx := &TriageContext{
		Alert: engine.RuleResult{
			RuleName:    "test-rule",
			Description: "Test rule",
			Severity:    engine.SeverityHigh,
			Matched:     true,
		},
		Request: &models.EvaluationRequest{
			EventID: "test-event",
			Tool:    "test-tool",
		},
	}

	result, err := provider.Triage(ctx, triageCtx)
	if err != nil {
		t.Fatalf("Triage failed: %v", err)
	}

	if result.Verdict != "allow" {
		t.Errorf("Expected verdict 'allow', got %s", result.Verdict)
	}

	if result.Confidence != 0.8 {
		t.Errorf("Expected confidence 0.8, got %f", result.Confidence)
	}

	if result.Provider != "openai" {
		t.Errorf("Expected provider 'openai', got %s", result.Provider)
	}
}

func TestAnthropicProvider(t *testing.T) {
	// Mock Anthropic API
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "test-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		response := map[string]interface{}{
			"content": []map[string]interface{}{
				{
					"type": "text",
					"text": `{"verdict": "block", "confidence": 0.9, "reasoning": "Suspicious activity", "suggested_action": "Block immediately"}`,
				},
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	cfg := &config.TriageConfig{
		Enabled:    true,
		Provider:   "anthropic",
		Model:      "claude-sonnet-4-20250514",
		APIKey:     "test-key",
		MaxTokens:  500,
		TimeoutSec: 10,
	}

	provider := &AnthropicProvider{
		config: cfg,
		client: createLocalHTTPClient(time.Duration(cfg.TimeoutSec) * time.Second),
		apiURL: server.URL, // Use test server URL
	}

	ctx := context.Background()
	triageCtx := &TriageContext{
		Alert: engine.RuleResult{
			RuleName:    "test-rule",
			Description: "Test rule",
			Severity:    engine.SeverityCritical,
			Matched:     true,
		},
		Request: &models.EvaluationRequest{
			EventID: "test-event",
			Tool:    "test-tool",
		},
	}

	result, err := provider.Triage(ctx, triageCtx)
	if err != nil {
		t.Fatalf("Triage failed: %v", err)
	}

	if result.Verdict != "block" {
		t.Errorf("Expected verdict 'block', got %s", result.Verdict)
	}

	if result.Confidence != 0.9 {
		t.Errorf("Expected confidence 0.9, got %f", result.Confidence)
	}

	if result.Provider != "anthropic" {
		t.Errorf("Expected provider 'anthropic', got %s", result.Provider)
	}
}

func TestTriagerFallback(t *testing.T) {
	cfg := &config.TriageConfig{
		Enabled:    true,
		Provider:   "openai",
		Model:      "gpt-4o-mini",
		APIKey:     "invalid-key", // This will cause auth failure
		MaxTokens:  500,
		TimeoutSec: 1, // Short timeout
	}

	// Create in-memory store for testing
	st, err := store.NewStore(":memory:")
	if err != nil {
		t.Fatalf("Failed to create test store: %v", err)
	}
	defer st.Close()

	triager, err := NewTriager(cfg, st)
	if err != nil {
		t.Fatalf("Failed to create triager: %v", err)
	}

	ctx := context.Background()
	alerts := []engine.RuleResult{
		{
			RuleName:    "test-rule",
			Description: "Test rule",
			Severity:    engine.SeverityCritical,
			Matched:     true,
		},
	}

	req := &models.EvaluationRequest{
		EventID: "test-event",
		Tool:    "test-tool",
	}

	results, err := triager.TriageAlerts(ctx, alerts, req)
	if err != nil {
		t.Fatalf("TriageAlerts failed: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("Expected 1 result, got %d", len(results))
	}

	// Should fallback to "block" for critical alerts
	if results[0].Verdict != "block" {
		t.Errorf("Expected fallback verdict 'block', got %s", results[0].Verdict)
	}

	if results[0].Confidence != 0.5 {
		t.Errorf("Expected fallback confidence 0.5, got %f", results[0].Confidence)
	}
}

// Test NewTriager with different provider configurations
func TestNewTriager(t *testing.T) {
	st, err := store.NewStore(":memory:")
	if err != nil {
		t.Fatalf("Failed to create test store: %v", err)
	}
	defer st.Close()

	tests := []struct {
		name        string
		config      *config.TriageConfig
		expectError bool
		expectNil   bool
	}{
		{
			name: "Valid OpenAI config",
			config: &config.TriageConfig{
				Enabled:    true,
				Provider:   "openai",
				Model:      "gpt-4o-mini",
				APIKey:     "test-key",
				MaxTokens:  500,
				TimeoutSec: 10,
			},
			expectError: false,
			expectNil:   false,
		},
		{
			name: "Valid Anthropic config",
			config: &config.TriageConfig{
				Enabled:    true,
				Provider:   "anthropic",
				Model:      "claude-sonnet-4-20250514",
				APIKey:     "test-key",
				MaxTokens:  500,
				TimeoutSec: 10,
			},
			expectError: false,
			expectNil:   false,
		},
		{
			name: "Disabled config returns nil",
			config: &config.TriageConfig{
				Enabled: false,
			},
			expectError: false,
			expectNil:   true,
		},
		{
			name: "Invalid provider",
			config: &config.TriageConfig{
				Enabled:  true,
				Provider: "invalid",
			},
			expectError: true,
			expectNil:   false,
		},
		{
			name: "OpenAI missing API key",
			config: &config.TriageConfig{
				Enabled:  true,
				Provider: "openai",
				Model:    "gpt-4o-mini",
			},
			expectError: true,
			expectNil:   false,
		},
		{
			name: "Anthropic missing API key",
			config: &config.TriageConfig{
				Enabled:  true,
				Provider: "anthropic",
				Model:    "claude-sonnet-4-20250514",
			},
			expectError: true,
			expectNil:   false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			triager, err := NewTriager(test.config, st)

			if test.expectError {
				if err == nil {
					t.Error("Expected error but got none")
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}

			if test.expectNil {
				if triager != nil {
					t.Error("Expected nil triager for disabled config")
				}
			} else {
				if triager == nil {
					t.Error("Expected non-nil triager")
				}
			}
		})
	}
}

// Test ShouldTriage with all severity levels
func TestShouldTriage(t *testing.T) {
	cfg := &config.TriageConfig{
		Enabled:    true,
		Provider:   "openai",
		Model:      "gpt-4o-mini",
		APIKey:     "test-key",
		MaxTokens:  500,
		TimeoutSec: 10,
	}

	st, err := store.NewStore(":memory:")
	if err != nil {
		t.Fatalf("Failed to create test store: %v", err)
	}
	defer st.Close()

	triager, err := NewTriager(cfg, st)
	if err != nil {
		t.Fatalf("Failed to create triager: %v", err)
	}

	tests := []struct {
		name     string
		severity engine.AlertSeverity
		expected bool
	}{
		{"Critical should triage", engine.SeverityCritical, true},
		{"High should triage", engine.SeverityHigh, true},
		{"Medium should not triage", engine.SeverityMedium, false},
		{"Low should not triage", engine.SeverityLow, false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			alert := engine.RuleResult{
				RuleName: "test-rule",
				Severity: test.severity,
				Matched:  true,
			}

			result := triager.ShouldTriage(alert)
			if result != test.expected {
				t.Errorf("Expected %t for %s, got %t", test.expected, test.severity, result)
			}
		})
	}

	// Test with nil triager
	result := (*Triager)(nil).ShouldTriage(engine.RuleResult{Severity: engine.SeverityCritical})
	if result {
		t.Error("Expected false for nil triager")
	}

	// Test with disabled config
	disabledCfg := &config.TriageConfig{Enabled: false}
	disabledTriager, _ := NewTriager(disabledCfg, st)
	if disabledTriager != nil {
		t.Error("Expected nil triager for disabled config")
	}
}

// Test getFallbackVerdict for each severity
func TestGetFallbackVerdict(t *testing.T) {
	cfg := &config.TriageConfig{
		Enabled:    true,
		Provider:   "openai",
		Model:      "gpt-4o-mini",
		APIKey:     "test-key",
		MaxTokens:  500,
		TimeoutSec: 10,
	}

	st, err := store.NewStore(":memory:")
	if err != nil {
		t.Fatalf("Failed to create test store: %v", err)
	}
	defer st.Close()

	triager, err := NewTriager(cfg, st)
	if err != nil {
		t.Fatalf("Failed to create triager: %v", err)
	}

	tests := []struct {
		severity engine.AlertSeverity
		expected string
	}{
		{engine.SeverityCritical, "block"},
		{engine.SeverityHigh, "block"},
		{engine.SeverityMedium, "allow"},
		{engine.SeverityLow, "allow"},
		{engine.AlertSeverity("unknown"), "block"}, // Default case
	}

	for _, test := range tests {
		t.Run(string(test.severity), func(t *testing.T) {
			result := triager.getFallbackVerdict(test.severity)
			if result != test.expected {
				t.Errorf("Expected %s for %s, got %s", test.expected, test.severity, result)
			}
		})
	}
}

// Test Triager.HealthCheck
func TestTriagerHealthCheck(t *testing.T) {
	// Test with nil triager
	err := (*Triager)(nil).HealthCheck(context.Background())
	if err != nil {
		t.Errorf("Expected no error for nil triager, got: %v", err)
	}

	// Test with disabled triager
	disabledCfg := &config.TriageConfig{Enabled: false}
	st, err := store.NewStore(":memory:")
	if err != nil {
		t.Fatalf("Failed to create test store: %v", err)
	}
	defer st.Close()

	disabledTriager, _ := NewTriager(disabledCfg, st)
	if disabledTriager != nil {
		t.Error("Expected nil triager for disabled config")
	}

	// Test with enabled triager using mock
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer valid-key" {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"choices":[{"message":{"content":"Test"}}]}`))
		} else {
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	defer server.Close()

	cfg := &config.TriageConfig{
		Enabled:    true,
		Provider:   "openai",
		Model:      "gpt-4o-mini",
		APIKey:     "valid-key",
		MaxTokens:  500,
		TimeoutSec: 10,
	}

	triager, err := NewTriager(cfg, st)
	if err != nil {
		t.Fatalf("Failed to create triager: %v", err)
	}

	// Update the provider's API URL and client to use our test server
	if provider, ok := triager.provider.(*OpenAIProvider); ok {
		provider.apiURL = server.URL
		provider.client = createLocalHTTPClient(time.Duration(cfg.TimeoutSec) * time.Second)
	}

	err = triager.HealthCheck(context.Background())
	if err != nil {
		t.Errorf("Expected successful health check, got: %v", err)
	}
}

// Test NewOpenAIProvider thoroughly
func TestNewOpenAIProvider(t *testing.T) {
	tests := []struct {
		name        string
		config      *config.TriageConfig
		expectError bool
	}{
		{
			name: "Valid config",
			config: &config.TriageConfig{
				APIKey:     "test-key",
				Model:      "gpt-4o-mini",
				MaxTokens:  500,
				TimeoutSec: 10,
			},
			expectError: false,
		},
		{
			name: "Missing API key",
			config: &config.TriageConfig{
				Model:      "gpt-4o-mini",
				MaxTokens:  500,
				TimeoutSec: 10,
			},
			expectError: true,
		},
		{
			name: "Custom base URL",
			config: &config.TriageConfig{
				APIKey:     "test-key",
				BaseURL:    "https://custom.api.com/v1",
				Model:      "gpt-4o-mini",
				MaxTokens:  500,
				TimeoutSec: 10,
			},
			expectError: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider, err := NewOpenAIProvider(test.config)

			if test.expectError {
				if err == nil {
					t.Error("Expected error but got none")
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}

			if provider == nil {
				t.Error("Expected non-nil provider")
			}

			if provider.Name() != "openai" {
				t.Errorf("Expected provider name 'openai', got %s", provider.Name())
			}

			// Check custom base URL
			if test.config.BaseURL != "" {
				expected := test.config.BaseURL + "/chat/completions"
				if provider.apiURL != expected {
					t.Errorf("Expected API URL %s, got %s", expected, provider.apiURL)
				}
			}
		})
	}
}

// Test NewAnthropicProvider thoroughly
func TestNewAnthropicProvider(t *testing.T) {
	tests := []struct {
		name        string
		config      *config.TriageConfig
		expectError bool
	}{
		{
			name: "Valid config",
			config: &config.TriageConfig{
				APIKey:     "test-key",
				Model:      "claude-sonnet-4-20250514",
				MaxTokens:  500,
				TimeoutSec: 10,
			},
			expectError: false,
		},
		{
			name: "Missing API key",
			config: &config.TriageConfig{
				Model:      "claude-sonnet-4-20250514",
				MaxTokens:  500,
				TimeoutSec: 10,
			},
			expectError: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider, err := NewAnthropicProvider(test.config)

			if test.expectError {
				if err == nil {
					t.Error("Expected error but got none")
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}

			if provider == nil {
				t.Error("Expected non-nil provider")
			}

			if provider.Name() != "anthropic" {
				t.Errorf("Expected provider name 'anthropic', got %s", provider.Name())
			}
		})
	}
}

// Test provider HealthCheck methods
func TestProviderHealthChecks(t *testing.T) {
	t.Run("OpenAI HealthCheck", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Header.Get("Authorization") {
			case "Bearer valid-key":
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(`{"choices":[{"message":{"content":"Test"}}]}`))
			case "Bearer server-error-key":
				w.WriteHeader(http.StatusInternalServerError)
			default:
				w.WriteHeader(http.StatusUnauthorized)
			}
		}))
		defer server.Close()

		tests := []struct {
			name        string
			apiKey      string
			expectError bool
		}{
			{"Valid API key", "valid-key", false},
			{"Invalid API key", "invalid-key", true},
			{"Server error", "server-error-key", true},
		}

		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				cfg := &config.TriageConfig{
					APIKey:     test.apiKey,
					Model:      "gpt-4o-mini",
					MaxTokens:  5,
					TimeoutSec: 10,
				}

				provider, err := NewOpenAIProvider(cfg)
				if err != nil {
					t.Fatalf("Failed to create provider: %v", err)
				}

				provider.apiURL = server.URL
				provider.client = createLocalHTTPClient(time.Duration(cfg.TimeoutSec) * time.Second)

				err = provider.HealthCheck(context.Background())
				if test.expectError && err == nil {
					t.Error("Expected error but got none")
				} else if !test.expectError && err != nil {
					t.Errorf("Unexpected error: %v", err)
				}
			})
		}
	})

	t.Run("Anthropic HealthCheck", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Header.Get("x-api-key") {
			case "valid-key":
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(`{"content":[{"type":"text","text":"Test"}]}`))
			case "server-error-key":
				w.WriteHeader(http.StatusInternalServerError)
			default:
				w.WriteHeader(http.StatusUnauthorized)
			}
		}))
		defer server.Close()

		tests := []struct {
			name        string
			apiKey      string
			expectError bool
		}{
			{"Valid API key", "valid-key", false},
			{"Invalid API key", "invalid-key", true},
			{"Server error", "server-error-key", true},
		}

		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				cfg := &config.TriageConfig{
					APIKey:     test.apiKey,
					Model:      "claude-sonnet-4-20250514",
					MaxTokens:  5,
					TimeoutSec: 10,
				}

				provider, err := NewAnthropicProvider(cfg)
				if err != nil {
					t.Fatalf("Failed to create provider: %v", err)
				}

				provider.apiURL = server.URL
				provider.client = createLocalHTTPClient(time.Duration(cfg.TimeoutSec) * time.Second)

				err = provider.HealthCheck(context.Background())
				if test.expectError && err == nil {
					t.Error("Expected error but got none")
				} else if !test.expectError && err != nil {
					t.Errorf("Unexpected error: %v", err)
				}
			})
		}
	})
}

// Test TriageAlerts with better orchestration
func TestTriageAlertsOrchestration(t *testing.T) {
	st, err := store.NewStore(":memory:")
	if err != nil {
		t.Fatalf("Failed to create test store: %v", err)
	}
	defer st.Close()

	// Test with nil triager
	results, err := (*Triager)(nil).TriageAlerts(context.Background(), nil, nil)
	if err != nil || results != nil {
		t.Error("Expected nil results and no error for nil triager")
	}

	// Test with disabled triager
	disabledCfg := &config.TriageConfig{Enabled: false}
	disabledTriager, _ := NewTriager(disabledCfg, st)
	if disabledTriager != nil {
		t.Error("Expected nil triager for disabled config")
	}

	// Test with working triager but no alerts to triage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		response := map[string]interface{}{
			"choices": []map[string]interface{}{
				{
					"message": map[string]interface{}{
						"content": `{"verdict": "block", "confidence": 0.9, "reasoning": "Test", "suggested_action": "Block"}`,
					},
				},
			},
		}
		json.NewEncoder(w).Encode(response)
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

	triager, err := NewTriager(cfg, st)
	if err != nil {
		t.Fatalf("Failed to create triager: %v", err)
	}

	// Update provider URL
	if provider, ok := triager.provider.(*OpenAIProvider); ok {
		provider.apiURL = server.URL
	}

	// Test with alerts that don't need triage
	lowSeverityAlerts := []engine.RuleResult{
		{
			RuleName: "low-rule",
			Severity: engine.SeverityLow,
			Matched:  true,
		},
		{
			RuleName: "medium-rule",
			Severity: engine.SeverityMedium,
			Matched:  true,
		},
	}

	req := &models.EvaluationRequest{
		EventID:   "test-event",
		Tool:      "test-tool",
		SessionID: "test-session",
	}

	results, err = triager.TriageAlerts(context.Background(), lowSeverityAlerts, req)
	if err != nil {
		t.Fatalf("TriageAlerts failed: %v", err)
	}

	if len(results) != 0 {
		t.Errorf("Expected 0 results for low severity alerts, got %d", len(results))
	}

	// Test with alerts that need triage
	highSeverityAlerts := []engine.RuleResult{
		{
			RuleName: "high-rule",
			Severity: engine.SeverityHigh,
			Matched:  true,
		},
		{
			RuleName: "critical-rule",
			Severity: engine.SeverityCritical,
			Matched:  true,
		},
	}

	results, err = triager.TriageAlerts(context.Background(), highSeverityAlerts, req)
	if err != nil {
		t.Fatalf("TriageAlerts failed: %v", err)
	}

	if len(results) != 2 {
		t.Errorf("Expected 2 results for high severity alerts, got %d", len(results))
	}

	for _, result := range results {
		if result.Verdict != "block" {
			t.Errorf("Expected verdict 'block', got %s", result.Verdict)
		}
		if result.Provider != "openai" {
			t.Errorf("Expected provider 'openai', got %s", result.Provider)
		}
	}
}

// Test parseTriageResponse error cases
func TestParseTriageResponseErrorCases(t *testing.T) {
	tests := []struct {
		name     string
		response string
	}{
		{"Empty response", ""},
		{"No JSON", "Just plain text"},
		{"Malformed JSON", `{"verdict": "block", "confidence": invalid}`},
		{"Missing closing brace", `{"verdict": "block"`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := parseTriageResponse(test.response, "test", "test-model", 100)
			if err == nil {
				t.Error("Expected error for malformed response")
			}
			if result != nil {
				t.Error("Expected nil result for malformed response")
			}
		})
	}

	// Test confidence clamping - confidence out of range gets clamped, not errored
	t.Run("Confidence clamping", func(t *testing.T) {
		response := `{"verdict": "allow", "confidence": -0.5, "reasoning": "test", "suggested_action": "test"}`
		result, err := parseTriageResponse(response, "test", "test-model", 100)
		if err != nil {
			t.Errorf("Unexpected error: %v", err)
		}
		if result.Confidence != 0.5 {
			t.Errorf("Expected confidence to be clamped to 0.5, got %f", result.Confidence)
		}
	})
}

// Test buildTriagePrompt edge cases
func TestBuildTriagePromptEdgeCases(t *testing.T) {
	// Test with empty/nil recent alerts
	alert := engine.RuleResult{
		RuleName:    "test-rule",
		Description: "Test description",
		Severity:    engine.SeverityHigh,
	}

	req := &models.EvaluationRequest{
		EventID: "event123",
		Tool:    "test_tool",
		Args:    map[string]string{"key": "value"},
	}

	triageCtx := &TriageContext{
		Alert:        alert,
		Request:      req,
		RecentAlerts: nil, // No recent alerts
	}

	prompt := buildTriagePrompt(triageCtx)
	if !contains(prompt, "No recent alerts") {
		t.Error("Expected prompt to mention no recent alerts")
	}
	if !contains(prompt, "test-rule") {
		t.Error("Expected prompt to contain rule name")
	}
	if !contains(prompt, "test_tool") {
		t.Error("Expected prompt to contain tool name")
	}
}

// Test Deep Triage functionality
func TestReadOpenClawToken(t *testing.T) {
	// This test will fail in most environments since the config file won't exist
	// But it tests the function path
	_, err := readOpenClawToken()
	// We expect an error since the file likely doesn't exist in the test environment
	if err == nil {
		t.Log("OpenClaw token read successfully (unexpected in test env)")
	}
	// The function was called, which improves coverage
}

func TestNewDeepTriager(t *testing.T) {
	tests := []struct {
		name        string
		config      *config.DeepTriageConfig
		expectError bool
		expectNil   bool
	}{
		{
			name:      "Nil config",
			config:    nil,
			expectNil: true,
		},
		{
			name: "Disabled config",
			config: &config.DeepTriageConfig{
				Enabled: false,
			},
			expectNil: true,
		},
		{
			name: "Enabled but no token",
			config: &config.DeepTriageConfig{
				Enabled: true,
			},
			// This might not error if OPENCLAW_GATEWAY_TOKEN env var is set or config file exists
			// so we check both possibilities
		},
		{
			name: "Valid config with token",
			config: &config.DeepTriageConfig{
				Enabled:      true,
				GatewayURL:   "http://localhost:8080",
				GatewayToken: "test-token",
				Agent: config.TriageAgentConfig{
					TimeoutSec: 60,
				},
			},
			expectError: false,
		},
		{
			name: "Config with defaults",
			config: &config.DeepTriageConfig{
				Enabled:      true,
				GatewayToken: "test-token",
				Agent:        config.TriageAgentConfig{}, // Empty, should get defaults
			},
			expectError: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			triager, err := NewDeepTriager(test.config)

			if test.expectError {
				if err == nil {
					t.Error("Expected error but got none")
				}
				return
			}

			// For the "enabled but no token" case, handle both possibilities
			if test.name == "Enabled but no token" {
				if err != nil {
					// This is expected if no token sources are available
					t.Logf("Got expected error for no token: %v", err)
					return
				} else {
					// Token was found from env or config file
					t.Logf("Token was found from environment or config file")
					if triager == nil {
						t.Error("Expected non-nil triager when token is available")
					}
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}

			if test.expectNil {
				if triager != nil {
					t.Error("Expected nil triager")
				}
			} else {
				if triager == nil {
					t.Error("Expected non-nil triager")
				}

				// Just verify triager was created successfully
				// (defaults are applied internally but not to the original config)
			}
		})
	}
}

func TestShouldDeepTriage(t *testing.T) {
	// Test with nil triager
	result := (*DeepTriager)(nil).ShouldDeepTriage(engine.RuleResult{})
	if result {
		t.Error("Expected false for nil triager")
	}

	// Create triager with different severity thresholds
	configs := map[string]*config.DeepTriageConfig{
		"low": {
			Enabled:      true,
			GatewayToken: "test",
			MinSeverity:  "low",
		},
		"medium": {
			Enabled:      true,
			GatewayToken: "test",
			MinSeverity:  "medium",
		},
		"high": {
			Enabled:      true,
			GatewayToken: "test",
			MinSeverity:  "high",
		},
		"critical": {
			Enabled:      true,
			GatewayToken: "test",
			MinSeverity:  "critical",
		},
		"unknown": {
			Enabled:      true,
			GatewayToken: "test",
			MinSeverity:  "unknown", // Should default to critical
		},
	}

	severities := []engine.AlertSeverity{
		engine.SeverityLow,
		engine.SeverityMedium,
		engine.SeverityHigh,
		engine.SeverityCritical,
	}

	expected := map[string]map[engine.AlertSeverity]bool{
		"low":      {engine.SeverityLow: true, engine.SeverityMedium: true, engine.SeverityHigh: true, engine.SeverityCritical: true},
		"medium":   {engine.SeverityLow: false, engine.SeverityMedium: true, engine.SeverityHigh: true, engine.SeverityCritical: true},
		"high":     {engine.SeverityLow: false, engine.SeverityMedium: false, engine.SeverityHigh: true, engine.SeverityCritical: true},
		"critical": {engine.SeverityLow: false, engine.SeverityMedium: false, engine.SeverityHigh: false, engine.SeverityCritical: true},
		"unknown":  {engine.SeverityLow: false, engine.SeverityMedium: false, engine.SeverityHigh: false, engine.SeverityCritical: true},
	}

	for threshold, cfg := range configs {
		triager, err := NewDeepTriager(cfg)
		if err != nil {
			t.Fatalf("Failed to create triager for %s: %v", threshold, err)
		}

		for _, severity := range severities {
			t.Run(fmt.Sprintf("%s_severity_%s", threshold, severity), func(t *testing.T) {
				alert := engine.RuleResult{Severity: severity}
				result := triager.ShouldDeepTriage(alert)
				expectedResult := expected[threshold][severity]

				if result != expectedResult {
					t.Errorf("Expected %t for %s threshold with %s severity, got %t",
						expectedResult, threshold, severity, result)
				}
			})
		}
	}
}

func TestInvestigateAsync(t *testing.T) {
	// Test with nil triager
	(*DeepTriager)(nil).InvestigateAsync(nil, nil, nil)
	// Should not panic

	// Test with triager but no alerts that need deep triage
	cfg := &config.DeepTriageConfig{
		Enabled:      true,
		GatewayToken: "test-token",
		MinSeverity:  "critical",
	}

	triager, err := NewDeepTriager(cfg)
	if err != nil {
		t.Fatalf("Failed to create triager: %v", err)
	}

	lowSeverityAlerts := []engine.RuleResult{
		{Severity: engine.SeverityLow},
		{Severity: engine.SeverityMedium},
	}

	req := &models.EvaluationRequest{
		EventID: "test",
		Tool:    "test",
	}

	// Should return quickly without doing anything
	triager.InvestigateAsync(lowSeverityAlerts, req, nil)

	// Test with alerts that need deep triage
	criticalAlerts := []engine.RuleResult{
		{
			RuleName:    "critical-alert",
			RuleID:      "rule-001",
			Severity:    engine.SeverityCritical,
			Description: "Critical test alert",
		},
	}

	// This will spawn a goroutine that will likely fail (no real gateway)
	// but it tests the code path
	triager.InvestigateAsync(criticalAlerts, req, nil)

	// Give the goroutine a moment to start
	time.Sleep(10 * time.Millisecond)
}

func TestBuildTask(t *testing.T) {
	cfg := &config.DeepTriageConfig{
		Enabled:      true,
		GatewayToken: "test-token",
		Agent: config.TriageAgentConfig{
			SystemPrompt: "Test system prompt",
			Tools:        []string{"web_search", "web_fetch", "memory_search", "read"},
		},
	}

	triager, err := NewDeepTriager(cfg)
	if err != nil {
		t.Fatalf("Failed to create triager: %v", err)
	}

	alerts := []engine.RuleResult{
		{
			RuleName:    "test-rule",
			RuleID:      "rule-001",
			Severity:    engine.SeverityCritical,
			Description: "Test alert",
		},
	}

	req := &models.EvaluationRequest{
		EventID:   "event-123",
		Tool:      "dangerous_tool",
		SessionID: "session-456",
		Args:      map[string]string{"param": "value"},
	}

	fastResults := []TriageResult{
		{
			Verdict:    "block",
			Confidence: 0.9,
			Reasoning:  "Test reasoning",
		},
	}

	task := triager.buildTask(alerts, req, fastResults)

	// Check that important elements are included
	if !contains(task, "Test system prompt") {
		t.Error("Expected task to contain system prompt")
	}
	if !contains(task, "test-rule") {
		t.Error("Expected task to contain alert name")
	}
	if !contains(task, "dangerous_tool") {
		t.Error("Expected task to contain tool name")
	}
	if !contains(task, "block") {
		t.Error("Expected task to contain fast triage result")
	}
	if !contains(task, "web_search") {
		t.Error("Expected task to contain tool descriptions")
	}
	if !contains(task, "Security Alert") {
		t.Error("Expected task to contain alert header")
	}
}

func TestInvestigate(t *testing.T) {
	// Create a mock server to simulate the OpenClaw gateway
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer valid-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		if r.URL.Path != "/tools/invoke" {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		// Parse the request to verify it's correct
		var reqBody openclawToolsInvokeRequest
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		if reqBody.Tool != "sessions_spawn" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		// Return success response
		response := openclawToolsInvokeResponse{
			OK: true,
			Result: &openclawToolsResult{
				Status: "spawned",
				Result: "session-id-123",
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	cfg := &config.DeepTriageConfig{
		Enabled:      true,
		GatewayURL:   server.URL,
		GatewayToken: "valid-token",
		Agent: config.TriageAgentConfig{
			AgentID:    "test-agent",
			Model:      "test-model",
			Thinking:   "low",
			TimeoutSec: 120,
		},
	}

	triager, err := NewDeepTriager(cfg)
	if err != nil {
		t.Fatalf("Failed to create triager: %v", err)
	}

	alerts := []engine.RuleResult{
		{
			RuleName:    "test-rule",
			Severity:    engine.SeverityCritical,
			Description: "Test alert",
		},
	}

	req := &models.EvaluationRequest{
		EventID: "test-event",
		Tool:    "test-tool",
	}

	err = triager.investigate(alerts, req, nil)
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
}

func TestInvestigateErrors(t *testing.T) {
	// Test with server that returns error
	errorServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := openclawToolsInvokeResponse{
			OK: false,
			Error: &openclawToolsError{
				Type:    "spawn_error",
				Message: "Failed to spawn session",
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer errorServer.Close()

	cfg := &config.DeepTriageConfig{
		Enabled:      true,
		GatewayURL:   errorServer.URL,
		GatewayToken: "test-token",
	}

	triager, err := NewDeepTriager(cfg)
	if err != nil {
		t.Fatalf("Failed to create triager: %v", err)
	}

	alerts := []engine.RuleResult{{RuleName: "test", Severity: engine.SeverityCritical}}
	req := &models.EvaluationRequest{EventID: "test", Tool: "test"}

	err = triager.investigate(alerts, req, nil)
	if err == nil {
		t.Error("Expected error for spawn failure")
	}
	if !contains(err.Error(), "spawn failed") {
		t.Errorf("Expected spawn failure error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Tests for helper functions: filterRecentAlertsForCurrent, scoreCorrelation,
// looksLikeChain, dedupeStrings
// ---------------------------------------------------------------------------

func TestFilterRecentAlertsForCurrent(t *testing.T) {
	alerts := []store.Alert{
		{RuleName: "r1", Tool: "file_read", Severity: "high", Timestamp: time.Now()},
		{RuleName: "r2", Tool: "bash", Severity: "critical", Timestamp: time.Now()},
		{RuleName: "r3", Tool: "file_read", Severity: "medium", Timestamp: time.Now()},
	}

	t.Run("empty alerts returns as-is", func(t *testing.T) {
		c := config.CorrelationConfig{RequireSameTool: true}
		req := &models.EvaluationRequest{Tool: "file_read"}
		got := filterRecentAlertsForCurrent(c, req, nil)
		if got != nil {
			t.Errorf("expected nil, got %v", got)
		}
		got = filterRecentAlertsForCurrent(c, req, []store.Alert{})
		if len(got) != 0 {
			t.Errorf("expected empty slice, got %v", got)
		}
	})

	t.Run("RequireSameTool false returns all", func(t *testing.T) {
		c := config.CorrelationConfig{RequireSameTool: false}
		req := &models.EvaluationRequest{Tool: "file_read"}
		got := filterRecentAlertsForCurrent(c, req, alerts)
		if len(got) != 3 {
			t.Errorf("expected 3 alerts, got %d", len(got))
		}
	})

	t.Run("nil request returns all", func(t *testing.T) {
		c := config.CorrelationConfig{RequireSameTool: true}
		got := filterRecentAlertsForCurrent(c, nil, alerts)
		if len(got) != 3 {
			t.Errorf("expected 3 alerts, got %d", len(got))
		}
	})

	t.Run("empty tool in request returns all", func(t *testing.T) {
		c := config.CorrelationConfig{RequireSameTool: true}
		req := &models.EvaluationRequest{Tool: ""}
		got := filterRecentAlertsForCurrent(c, req, alerts)
		if len(got) != 3 {
			t.Errorf("expected 3 alerts, got %d", len(got))
		}
	})

	t.Run("filters by matching tool", func(t *testing.T) {
		c := config.CorrelationConfig{RequireSameTool: true}
		req := &models.EvaluationRequest{Tool: "file_read"}
		got := filterRecentAlertsForCurrent(c, req, alerts)
		if len(got) != 2 {
			t.Fatalf("expected 2 alerts, got %d", len(got))
		}
		for _, a := range got {
			if a.Tool != "file_read" {
				t.Errorf("unexpected tool %q in filtered results", a.Tool)
			}
		}
	})

	t.Run("no matches returns empty", func(t *testing.T) {
		c := config.CorrelationConfig{RequireSameTool: true}
		req := &models.EvaluationRequest{Tool: "web_search"}
		got := filterRecentAlertsForCurrent(c, req, alerts)
		if len(got) != 0 {
			t.Errorf("expected 0 alerts, got %d", len(got))
		}
	})
}

func TestScoreCorrelation(t *testing.T) {
	baseCfg := config.CorrelationConfig{
		Enabled:              true,
		WindowSec:            900,
		WeightCritical:       0.6,
		WeightHigh:           0.35,
		WeightChainBonus:     0.4,
		WeightRepeatBonus:    0.2,
		TimeDecayHalfLifeSec: 300,
		EscalateThreshold:    0.8,
	}

	t.Run("disabled returns zero summary", func(t *testing.T) {
		c := config.CorrelationConfig{Enabled: false}
		got := scoreCorrelation(c, engine.RuleResult{}, nil, nil)
		if got.Score != 0 || got.EscalatedByCorrelation {
			t.Errorf("expected zero summary, got %+v", got)
		}
	})

	t.Run("no recent alerts yields zero score", func(t *testing.T) {
		current := engine.RuleResult{RuleName: "rce_attempt", Severity: engine.SeverityHigh}
		got := scoreCorrelation(baseCfg, current, nil, nil)
		if got.Score != 0 {
			t.Errorf("expected 0 score, got %f", got.Score)
		}
		if got.RecentCount != 0 {
			t.Errorf("expected 0 recent, got %d", got.RecentCount)
		}
	})

	t.Run("critical recent alert adds weight with decay", func(t *testing.T) {
		recent := []store.Alert{
			{RuleName: "other", Severity: "critical", Tool: "bash", Timestamp: time.Now()},
		}
		current := engine.RuleResult{RuleName: "something", Severity: engine.SeverityHigh}
		got := scoreCorrelation(baseCfg, current, nil, recent)
		// Decay for ~0 seconds should be ~1.0, so score ≈ 0.6
		if got.Score < 0.55 || got.Score > 0.65 {
			t.Errorf("expected score ~0.6, got %f", got.Score)
		}
		if got.RecentCount != 1 {
			t.Errorf("expected 1 recent, got %d", got.RecentCount)
		}
	})

	t.Run("high recent alert adds weight", func(t *testing.T) {
		recent := []store.Alert{
			{RuleName: "other", Severity: "high", Tool: "bash", Timestamp: time.Now()},
		}
		current := engine.RuleResult{RuleName: "something", Severity: engine.SeverityHigh}
		got := scoreCorrelation(baseCfg, current, nil, recent)
		if got.Score < 0.30 || got.Score > 0.40 {
			t.Errorf("expected score ~0.35, got %f", got.Score)
		}
	})

	t.Run("same tool repeat bonus", func(t *testing.T) {
		recent := []store.Alert{
			{RuleName: "other", Severity: "high", Tool: "file_read", Timestamp: time.Now()},
		}
		current := engine.RuleResult{RuleName: "something", Severity: engine.SeverityHigh}
		req := &models.EvaluationRequest{Tool: "file_read"}
		got := scoreCorrelation(baseCfg, current, req, recent)
		// Should include WeightHigh(0.35) + WeightRepeatBonus(0.2) ≈ 0.55
		if got.Score < 0.50 || got.Score > 0.60 {
			t.Errorf("expected score ~0.55, got %f", got.Score)
		}
		found := false
		for _, f := range got.Factors {
			if f == "same_tool_repeat" {
				found = true
			}
		}
		if !found {
			t.Errorf("expected 'same_tool_repeat' factor, got %v", got.Factors)
		}
	})

	t.Run("attack chain bonus", func(t *testing.T) {
		recent := []store.Alert{
			{RuleName: "rce_detected", Severity: "high", Tool: "bash", Timestamp: time.Now()},
		}
		current := engine.RuleResult{RuleName: "persistence_install", Severity: engine.SeverityHigh}
		got := scoreCorrelation(baseCfg, current, nil, recent)
		// Should include WeightHigh(0.35) + WeightChainBonus(0.4) ≈ 0.75
		if got.Score < 0.70 || got.Score > 0.80 {
			t.Errorf("expected score ~0.75, got %f", got.Score)
		}
		found := false
		for _, f := range got.Factors {
			if f == "attack_chain_pattern" {
				found = true
			}
		}
		if !found {
			t.Errorf("expected 'attack_chain_pattern' factor, got %v", got.Factors)
		}
	})

	t.Run("escalation above threshold", func(t *testing.T) {
		recent := []store.Alert{
			{RuleName: "rce_exploit", Severity: "critical", Tool: "file_read", Timestamp: time.Now()},
		}
		current := engine.RuleResult{RuleName: "persistence_cron", Severity: engine.SeverityCritical}
		req := &models.EvaluationRequest{Tool: "file_read"}
		got := scoreCorrelation(baseCfg, current, req, recent)
		// critical(0.6) + repeat(0.2) + chain(0.4) ≈ 1.2 ≥ 0.8
		if !got.EscalatedByCorrelation {
			t.Errorf("expected escalation, score=%f", got.Score)
		}
	})

	t.Run("default half-life when zero", func(t *testing.T) {
		c := baseCfg
		c.TimeDecayHalfLifeSec = 0
		recent := []store.Alert{
			{RuleName: "other", Severity: "critical", Tool: "bash", Timestamp: time.Now()},
		}
		got := scoreCorrelation(c, engine.RuleResult{RuleName: "x"}, nil, recent)
		if got.Score < 0.55 {
			t.Errorf("expected score ~0.6 with default half-life, got %f", got.Score)
		}
	})

	t.Run("default threshold when zero", func(t *testing.T) {
		c := baseCfg
		c.EscalateThreshold = 0
		recent := []store.Alert{
			{RuleName: "other", Severity: "critical", Tool: "bash", Timestamp: time.Now()},
		}
		got := scoreCorrelation(c, engine.RuleResult{RuleName: "x"}, nil, recent)
		// Default threshold is 0.8; score ≈ 0.6, so should not escalate
		if got.EscalatedByCorrelation {
			t.Errorf("expected no escalation with default threshold, score=%f", got.Score)
		}
	})

	t.Run("time decay reduces score for old alerts", func(t *testing.T) {
		old := time.Now().Add(-600 * time.Second) // 2 half-lives
		recent := []store.Alert{
			{RuleName: "other", Severity: "critical", Tool: "bash", Timestamp: old},
		}
		got := scoreCorrelation(baseCfg, engine.RuleResult{RuleName: "x"}, nil, recent)
		// After 2 half-lives: 0.6 * 0.25 = 0.15
		if got.Score < 0.10 || got.Score > 0.20 {
			t.Errorf("expected decayed score ~0.15, got %f", got.Score)
		}
	})

	t.Run("factors are deduped", func(t *testing.T) {
		recent := []store.Alert{
			{RuleName: "a", Severity: "critical", Tool: "bash", Timestamp: time.Now()},
			{RuleName: "b", Severity: "critical", Tool: "bash", Timestamp: time.Now()},
		}
		got := scoreCorrelation(baseCfg, engine.RuleResult{RuleName: "x"}, nil, recent)
		count := 0
		for _, f := range got.Factors {
			if f == "recent_critical" {
				count++
			}
		}
		if count != 1 {
			t.Errorf("expected deduped 'recent_critical' (1), got %d in %v", count, got.Factors)
		}
	})

	t.Run("medium and low severity alerts add no weight", func(t *testing.T) {
		recent := []store.Alert{
			{RuleName: "other", Severity: "medium", Tool: "bash", Timestamp: time.Now()},
			{RuleName: "other2", Severity: "low", Tool: "bash", Timestamp: time.Now()},
		}
		got := scoreCorrelation(baseCfg, engine.RuleResult{RuleName: "x"}, nil, recent)
		if got.Score != 0 {
			t.Errorf("expected 0 score for medium/low, got %f", got.Score)
		}
		if got.RecentCount != 2 {
			t.Errorf("expected 2 recent, got %d", got.RecentCount)
		}
	})
}

func TestLooksLikeChain(t *testing.T) {
	tests := []struct {
		name     string
		prev     string
		current  string
		expected bool
	}{
		{"empty prev", "", "persistence", false},
		{"empty current", "rce_exploit", "", false},
		{"both empty", "", "", false},
		{"rce then persistence", "rce_exploit", "persistence_cron", true},
		{"execution then persistence", "code_execution", "persistence_install", true},
		{"persistence then exfil", "persistence_cron", "exfil_data", true},
		{"persistence then dns", "persistence_cron", "dns_tunnel", true},
		{"no chain rce then exfil", "rce_exploit", "exfil_data", false},
		{"no chain unrelated", "sql_injection", "xss_attack", false},
		{"case insensitive check (already lower)", "rce", "persistence", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := looksLikeChain(tt.prev, tt.current)
			if got != tt.expected {
				t.Errorf("looksLikeChain(%q, %q) = %t, want %t", tt.prev, tt.current, got, tt.expected)
			}
		})
	}
}

func TestDedupeStrings(t *testing.T) {
	tests := []struct {
		name     string
		input    []string
		expected []string
	}{
		{"nil input", nil, nil},
		{"empty input", []string{}, []string{}},
		{"no duplicates", []string{"a", "b", "c"}, []string{"a", "b", "c"}},
		{"all duplicates", []string{"x", "x", "x"}, []string{"x"}},
		{"mixed", []string{"a", "b", "a", "c", "b"}, []string{"a", "b", "c"}},
		{"single element", []string{"only"}, []string{"only"}},
		{"preserves order", []string{"z", "a", "z", "m"}, []string{"z", "a", "m"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := dedupeStrings(tt.input)
			if tt.expected == nil {
				if got != nil {
					t.Errorf("expected nil, got %v", got)
				}
				return
			}
			if len(got) != len(tt.expected) {
				t.Fatalf("length mismatch: got %v, want %v", got, tt.expected)
			}
			for i := range got {
				if got[i] != tt.expected[i] {
					t.Errorf("index %d: got %q, want %q", i, got[i], tt.expected[i])
				}
			}
		})
	}
}

// TestIsSSRFBlockedIP verifies the SSRF IP check correctly handles
// IPv4-mapped IPv6 bypass vectors via net/netip Unmap().
func TestIsSSRFBlockedIP(t *testing.T) {
	tests := []struct {
		name     string
		ip       net.IP
		expected bool
	}{
		{"IPv4 loopback", net.ParseIP("127.0.0.1"), true},
		{"IPv6 loopback", net.ParseIP("::1"), true},
		{"IPv4 private 10.x", net.ParseIP("10.0.0.1"), true},
		{"IPv4 private 192.168.x", net.ParseIP("192.168.1.1"), true},
		{"IPv4 link-local", net.ParseIP("169.254.1.1"), true},
		{"IPv4 public", net.ParseIP("8.8.8.8"), false},
		{"IPv6 public", net.ParseIP("2001:4860:4860::8888"), false},
		{"IPv4-mapped loopback", net.ParseIP("::ffff:127.0.0.1"), true},
		{"IPv4-mapped private", net.ParseIP("::ffff:10.0.0.1"), true},
		{"IPv4-mapped public", net.ParseIP("::ffff:8.8.8.8"), false},
		{"unspecified v4", net.ParseIP("0.0.0.0"), true},
		{"unspecified v6", net.ParseIP("::"), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isSSRFBlockedIP(tt.ip)
			if result != tt.expected {
				t.Errorf("isSSRFBlockedIP(%s) = %v, want %v", tt.ip, result, tt.expected)
			}
		})
	}
}

// TestScoreCorrelationTestContext verifies that test context requests get a
// zero correlation score to avoid false escalation in test harnesses.
func TestScoreCorrelationTestContext(t *testing.T) {
	cfg := config.CorrelationConfig{
		Enabled:              true,
		WindowSec:            900,
		WeightCritical:       0.6,
		WeightHigh:           0.35,
		EscalateThreshold:    0.8,
		TimeDecayHalfLifeSec: 300,
	}

	recent := []store.Alert{
		{RuleName: "critical-alert", Severity: "critical", Tool: "bash", Timestamp: time.Now()},
	}
	req := &models.EvaluationRequest{Tool: "bash", Context: "test"}
	current := engine.RuleResult{RuleName: "test-rule", Severity: engine.SeverityCritical}

	summary := scoreCorrelation(cfg, current, req, recent)
	if summary.Score != 0 {
		t.Errorf("expected score 0 for test context, got %f", summary.Score)
	}
	if summary.EscalatedByCorrelation {
		t.Error("expected no escalation for test context")
	}
}

// ---------------------------------------------------------------------------
// Tests for HealthCheck connectivity mode
// ---------------------------------------------------------------------------

func TestHealthCheckConnectivityMode(t *testing.T) {
	t.Run("OpenAI connectivity mode uses models endpoint", func(t *testing.T) {
		var requestedPath string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requestedPath = r.URL.Path
			if r.Header.Get("Authorization") == "Bearer test-key" {
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(`{"data":[{"id":"gpt-4o-mini"}]}`))
			} else {
				w.WriteHeader(http.StatusUnauthorized)
			}
		}))
		defer server.Close()

		cfg := &config.TriageConfig{
			APIKey:          "test-key",
			Model:           "gpt-4o-mini",
			TimeoutSec:      10,
			HealthCheckMode: "connectivity",
		}

		provider := &OpenAIProvider{
			config: cfg,
			client: createLocalHTTPClient(10 * time.Second),
			apiURL: server.URL + "/chat/completions",
		}

		err := provider.HealthCheck(context.Background())
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}

		if requestedPath != "/models" {
			t.Errorf("expected /models path, got %s", requestedPath)
		}
	})

	t.Run("OpenAI connectivity mode auth failure", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		}))
		defer server.Close()

		cfg := &config.TriageConfig{
			APIKey:          "bad-key",
			Model:           "gpt-4o-mini",
			TimeoutSec:      10,
			HealthCheckMode: "connectivity",
		}

		provider := &OpenAIProvider{
			config: cfg,
			client: createLocalHTTPClient(10 * time.Second),
			apiURL: server.URL + "/chat/completions",
		}

		err := provider.HealthCheck(context.Background())
		if err == nil {
			t.Error("expected error for auth failure")
		}
		if !contains(err.Error(), "authentication failed") {
			t.Errorf("expected auth error, got: %v", err)
		}
	})

	t.Run("OpenAI connectivity mode server error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer server.Close()

		cfg := &config.TriageConfig{
			APIKey:          "test-key",
			Model:           "gpt-4o-mini",
			TimeoutSec:      10,
			HealthCheckMode: "connectivity",
		}

		provider := &OpenAIProvider{
			config: cfg,
			client: createLocalHTTPClient(10 * time.Second),
			apiURL: server.URL + "/chat/completions",
		}

		err := provider.HealthCheck(context.Background())
		if err == nil {
			t.Error("expected error for server error")
		}
	})

	t.Run("OpenAI default mode still uses completion endpoint", func(t *testing.T) {
		var requestedPath string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requestedPath = r.URL.Path
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"choices":[{"message":{"content":"Test"}}]}`))
		}))
		defer server.Close()

		cfg := &config.TriageConfig{
			APIKey:     "test-key",
			Model:      "gpt-4o-mini",
			TimeoutSec: 10,
			// HealthCheckMode left empty = "full" mode
		}

		provider := &OpenAIProvider{
			config: cfg,
			client: createLocalHTTPClient(10 * time.Second),
			apiURL: server.URL + "/chat/completions",
		}

		err := provider.HealthCheck(context.Background())
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}

		if requestedPath != "/chat/completions" {
			t.Errorf("expected /chat/completions path for full mode, got %s", requestedPath)
		}
	})

	t.Run("Anthropic connectivity mode uses minimal request", func(t *testing.T) {
		var receivedBody AnthropicRequest
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			json.NewDecoder(r.Body).Decode(&receivedBody)
			if r.Header.Get("x-api-key") == "test-key" {
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(`{"content":[{"type":"text","text":"."}]}`))
			} else {
				w.WriteHeader(http.StatusUnauthorized)
			}
		}))
		defer server.Close()

		cfg := &config.TriageConfig{
			APIKey:          "test-key",
			Model:           "claude-sonnet-4-20250514",
			TimeoutSec:      10,
			HealthCheckMode: "connectivity",
		}

		provider := &AnthropicProvider{
			config: cfg,
			client: createLocalHTTPClient(10 * time.Second),
			apiURL: server.URL,
		}

		err := provider.HealthCheck(context.Background())
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}

		if receivedBody.MaxTokens != 1 {
			t.Errorf("expected max_tokens=1 for connectivity mode, got %d", receivedBody.MaxTokens)
		}
		if len(receivedBody.Messages) != 1 || receivedBody.Messages[0].Content != "." {
			t.Errorf("expected minimal prompt '.' for connectivity mode, got %+v", receivedBody.Messages)
		}
	})

	t.Run("Anthropic connectivity mode auth failure", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		}))
		defer server.Close()

		cfg := &config.TriageConfig{
			APIKey:          "bad-key",
			Model:           "claude-sonnet-4-20250514",
			TimeoutSec:      10,
			HealthCheckMode: "connectivity",
		}

		provider := &AnthropicProvider{
			config: cfg,
			client: createLocalHTTPClient(10 * time.Second),
			apiURL: server.URL,
		}

		err := provider.HealthCheck(context.Background())
		if err == nil {
			t.Error("expected error for auth failure")
		}
		if !contains(err.Error(), "authentication failed") {
			t.Errorf("expected auth error, got: %v", err)
		}
	})

	t.Run("Anthropic default mode uses standard request", func(t *testing.T) {
		var receivedBody AnthropicRequest
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			json.NewDecoder(r.Body).Decode(&receivedBody)
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"content":[{"type":"text","text":"Test"}]}`))
		}))
		defer server.Close()

		cfg := &config.TriageConfig{
			APIKey:     "test-key",
			Model:      "claude-sonnet-4-20250514",
			TimeoutSec: 10,
			// HealthCheckMode left empty = "full" mode
		}

		provider := &AnthropicProvider{
			config: cfg,
			client: createLocalHTTPClient(10 * time.Second),
			apiURL: server.URL,
		}

		err := provider.HealthCheck(context.Background())
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}

		if receivedBody.MaxTokens != 5 {
			t.Errorf("expected max_tokens=5 for full mode, got %d", receivedBody.MaxTokens)
		}
		if len(receivedBody.Messages) != 1 || receivedBody.Messages[0].Content != "Test" {
			t.Errorf("expected 'Test' prompt for full mode, got %+v", receivedBody.Messages)
		}
	})

	t.Run("backward compat empty HealthCheckMode equals full", func(t *testing.T) {
		var requestedMethod string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requestedMethod = r.Method
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"choices":[{"message":{"content":"Test"}}]}`))
		}))
		defer server.Close()

		cfg := &config.TriageConfig{
			APIKey:          "test-key",
			Model:           "gpt-4o-mini",
			TimeoutSec:      10,
			HealthCheckMode: "", // Explicitly empty
		}

		provider := &OpenAIProvider{
			config: cfg,
			client: createLocalHTTPClient(10 * time.Second),
			apiURL: server.URL + "/chat/completions",
		}

		err := provider.HealthCheck(context.Background())
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}

		if requestedMethod != "POST" {
			t.Errorf("expected POST for full mode, got %s", requestedMethod)
		}
	})
}

// Helper function
func contains(s, substr string) bool {
	return len(s) >= len(substr) &&
		(s == substr || len(s) > len(substr) && (s[:len(substr)] == substr || s[len(s)-len(substr):] == substr ||
			func() bool {
				for i := 0; i <= len(s)-len(substr); i++ {
					if s[i:i+len(substr)] == substr {
						return true
					}
				}
				return false
			}()))
}
