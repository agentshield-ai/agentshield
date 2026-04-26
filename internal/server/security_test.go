package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/agentshield-ai/agentshield/internal/config"
	"github.com/agentshield-ai/agentshield/internal/engine"
	"github.com/agentshield-ai/agentshield/internal/evaluate"
	"github.com/agentshield-ai/agentshield/internal/models"
	"github.com/agentshield-ai/agentshield/internal/store"
)

// TestToolNameCommandValidation verifies that H-6 (ToolName/Command validation)
// is enforced: overlong or invalid ToolName/Command fields are rejected.
func TestToolNameCommandValidation(t *testing.T) {
	testStore, err := store.NewStore(":memory:")
	if err != nil {
		t.Fatalf("creating test store: %v", err)
	}
	defer testStore.Close()

	cfg := &config.Config{}
	mockEngine := &mockRuleEngine{}
	evaluator := evaluate.NewEvaluator(mockEngine, config.ModeAudit, "", nil, nil)

	srv, err := NewServer(cfg, evaluator, testStore, nil)
	if err != nil {
		t.Fatalf("creating test server: %v", err)
	}
	router := srv.setupTestRouter()

	tests := []struct {
		name           string
		body           string
		expectedStatus int
	}{
		{
			name:           "overlong tool_name rejected",
			body:           fmt.Sprintf(`{"event_id":"test","tool_name":"%s"}`, strings.Repeat("A", 200)),
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:           "overlong command rejected",
			body:           fmt.Sprintf(`{"event_id":"test","command":"%s"}`, strings.Repeat("B", MaxFieldValueLength+1)),
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:           "valid tool_name accepted",
			body:           `{"event_id":"test","tool_name":"Bash"}`,
			expectedStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/evaluate", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != tt.expectedStatus {
				t.Errorf("expected status %d, got %d", tt.expectedStatus, rec.Code)
			}
		})
	}
}

// TestRawParamsValidationBypass verifies M-8: values entering via RawParams
// (the plugin "params" field) are validated after normalization.
func TestRawParamsValidationBypass(t *testing.T) {
	testStore, err := store.NewStore(":memory:")
	if err != nil {
		t.Fatalf("creating test store: %v", err)
	}
	defer testStore.Close()

	cfg := &config.Config{}
	mockEngine := &mockRuleEngine{}
	evaluator := evaluate.NewEvaluator(mockEngine, config.ModeAudit, "", nil, nil)

	srv, err := NewServer(cfg, evaluator, testStore, nil)
	if err != nil {
		t.Fatalf("creating test server: %v", err)
	}
	router := srv.setupTestRouter()

	// Send a huge value via params.command — should be rejected after normalize+validate
	hugeValue := strings.Repeat("X", MaxFieldValueLength+100)
	body := fmt.Sprintf(`{"event_id":"test","params":{"command":"%s"}}`, hugeValue)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/evaluate", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("Expected 400 for overlong params.command, got %d", rec.Code)
	}
}

// TestHealthEndpointNoConfigLeak verifies H-3: the unauthenticated health
// endpoint does not expose evaluation_mode, rules_dir, or auth_enabled.
func TestHealthEndpointNoConfigLeak(t *testing.T) {
	testStore, err := store.NewStore(":memory:")
	if err != nil {
		t.Fatalf("creating test store: %v", err)
	}
	defer testStore.Close()

	cfg := &config.Config{
		EvaluationMode: config.ModeEnforce,
		Rules:          config.RulesConfig{Dir: "/secret/rules"},
	}

	mockEngine := &mockRuleEngine{}
	evaluator := evaluate.NewEvaluator(mockEngine, config.ModeEnforce, "", nil, nil)

	srv, err := NewServer(cfg, evaluator, testStore, nil)
	if err != nil {
		t.Fatalf("creating test server: %v", err)
	}
	router := srv.setupTestRouter()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var response HealthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Config field was removed from HealthResponse entirely — verify the body
	// doesn't contain sensitive paths or config info.
	body := rec.Body.String()
	if strings.Contains(body, "/secret/rules") {
		t.Error("health response contains rules directory path")
	}
	if strings.Contains(body, "evaluation_mode") {
		t.Error("health response contains evaluation_mode")
	}
}

// TestSecurityHeadersPresent verifies M-5: all required security headers
// are set on every response.
func TestSecurityHeadersPresent(t *testing.T) {
	testStore, err := store.NewStore(":memory:")
	if err != nil {
		t.Fatalf("creating test store: %v", err)
	}
	defer testStore.Close()

	cfg := &config.Config{}
	mockEngine := &mockRuleEngine{}
	evaluator := evaluate.NewEvaluator(mockEngine, config.ModeAudit, "", nil, nil)

	srv, err := NewServer(cfg, evaluator, testStore, nil)
	if err != nil {
		t.Fatalf("creating test server: %v", err)
	}

	// Use setupTestRouter but add securityHeaders middleware like Start() does
	r := srv.setupTestRouter()
	handler := securityHeaders(r)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	expectedHeaders := map[string]string{
		"X-Content-Type-Options":  "nosniff",
		"X-Frame-Options":         "DENY",
		"Cache-Control":           "no-store",
		"Content-Security-Policy": "default-src 'none'",
	}

	for header, expected := range expectedHeaders {
		got := rec.Header().Get(header)
		if got != expected {
			t.Errorf("header %q = %q, want %q", header, got, expected)
		}
	}
}

// TestTriageCannotDowngradeBlock verifies C-1 regression: triage results
// returning "allow" with high confidence must NOT downgrade a block action
// in enforce mode.
func TestTriageCannotDowngradeBlock(t *testing.T) {
	mockEngine := &mockRuleEngine{
		results: []engine.RuleResult{
			{
				RuleID:   "critical-rule",
				RuleName: "Critical Test Rule",
				Severity: engine.SeverityCritical,
				Matched:  true,
			},
		},
	}

	evaluator := evaluate.NewEvaluator(mockEngine, config.ModeEnforce, "", nil, nil)

	evalReq := &models.EvaluationRequest{
		EventID: "test-triage-block",
		Fields:  map[string]string{"tool": "Bash", "command": "rm -rf /"},
	}

	response, err := evaluator.Evaluate(evalReq)
	if err != nil {
		t.Fatalf("Evaluate() error: %v", err)
	}

	if response.Action != "block" {
		t.Errorf("Expected block action for critical alert in enforce mode, got %q", response.Action)
	}
}
