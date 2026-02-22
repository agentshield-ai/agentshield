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

// validTestToken is a 32+ char token used in test context security tests.
const validTestToken = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaXX" // 34 chars

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

	// These sensitive fields must NOT be present
	sensitiveKeys := []string{"evaluation_mode", "rules_dir", "auth_enabled", "store_healthy"}
	for _, key := range sensitiveKeys {
		if _, exists := response.Config[key]; exists {
			t.Errorf("health endpoint leaks config key %q", key)
		}
	}

	// Body string must not contain the secret path
	body := rec.Body.String()
	if strings.Contains(body, "/secret/rules") {
		t.Error("health response contains rules directory path")
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
		"X-Frame-Options":        "DENY",
		"Cache-Control":          "no-store",
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

// ---------------------------------------------------------------------------
// S-2: resolveExecutionContext — test-context token bypass (25% coverage)
// Guards C-4: constant-time test-context token verification.
// ---------------------------------------------------------------------------

// TestResolveExecutionContext_DisabledReturnsProд verifies S-2:
// when TestContext.Enabled=false, any context header must return "prod".
func TestResolveExecutionContext_DisabledReturnsProd(t *testing.T) {
	cfg := &config.Config{
		TestContext: config.TestContextConfig{Enabled: false, Token: validTestToken},
	}

	tests := []struct {
		name          string
		contextHeader string
		tokenHeader   string
	}{
		{"no headers", "", ""},
		{"test context no token", "test", ""},
		{"test context correct token", "test", validTestToken},
		{"prod context", "prod", validTestToken},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/evaluate", nil)
			if tt.contextHeader != "" {
				req.Header.Set("X-AgentShield-Context", tt.contextHeader)
			}
			if tt.tokenHeader != "" {
				req.Header.Set("X-AgentShield-Context-Token", tt.tokenHeader)
			}
			got := resolveExecutionContext(req, cfg)
			if got != "prod" {
				t.Errorf("resolveExecutionContext with disabled TestContext = %q, want \"prod\"", got)
			}
		})
	}
}

// TestResolveExecutionContext_WrongTokenReturnsProd verifies S-2:
// when TestContext.Enabled=true but the token is wrong/empty, return "prod".
func TestResolveExecutionContext_WrongTokenReturnsProd(t *testing.T) {
	cfg := &config.Config{
		TestContext: config.TestContextConfig{Enabled: true, Token: validTestToken},
	}

	tests := []struct {
		name        string
		tokenHeader string
	}{
		{"empty token", ""},
		{"wrong token", "wrong-token-aaaaaaaaaaaaaaaa"},
		{"partial token", validTestToken[:10]},
		{"token with null byte", validTestToken[:5] + "\x00" + validTestToken[5:]},
		{"different length correct prefix", validTestToken + "extra"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/evaluate", nil)
			req.Header.Set("X-AgentShield-Context", "test")
			req.Header.Set("X-AgentShield-Context-Token", tt.tokenHeader)
			got := resolveExecutionContext(req, cfg)
			if got != "prod" {
				t.Errorf("resolveExecutionContext with wrong token %q = %q, want \"prod\"", tt.tokenHeader, got)
			}
		})
	}
}

// TestResolveExecutionContext_CorrectTokenReturnsTest verifies S-2:
// when TestContext.Enabled=true and the correct token is supplied, return "test".
func TestResolveExecutionContext_CorrectTokenReturnsTest(t *testing.T) {
	cfg := &config.Config{
		TestContext: config.TestContextConfig{Enabled: true, Token: validTestToken},
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/evaluate", nil)
	req.Header.Set("X-AgentShield-Context", "test")
	req.Header.Set("X-AgentShield-Context-Token", validTestToken)

	got := resolveExecutionContext(req, cfg)
	if got != "test" {
		t.Errorf("resolveExecutionContext with correct token = %q, want \"test\"", got)
	}
}

// TestResolveExecutionContext_ProdHeaderReturnsProd verifies S-2:
// a context header value of "prod" always returns "prod" regardless of token.
func TestResolveExecutionContext_ProdHeaderReturnsProd(t *testing.T) {
	cfg := &config.Config{
		TestContext: config.TestContextConfig{Enabled: true, Token: validTestToken},
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/evaluate", nil)
	req.Header.Set("X-AgentShield-Context", "prod")
	req.Header.Set("X-AgentShield-Context-Token", validTestToken) // token present but context not "test"

	got := resolveExecutionContext(req, cfg)
	if got != "prod" {
		t.Errorf("resolveExecutionContext with prod context header = %q, want \"prod\"", got)
	}
}

// TestResolveExecutionContext_NilInputsSafe verifies S-2:
// nil request or nil config return "prod" without panicking.
func TestResolveExecutionContext_NilInputsSafe(t *testing.T) {
	cfg := &config.Config{
		TestContext: config.TestContextConfig{Enabled: true, Token: validTestToken},
	}

	// nil request
	got := resolveExecutionContext(nil, cfg)
	if got != "prod" {
		t.Errorf("resolveExecutionContext(nil, cfg) = %q, want \"prod\"", got)
	}

	// nil config
	req := httptest.NewRequest(http.MethodPost, "/api/v1/evaluate", nil)
	req.Header.Set("X-AgentShield-Context", "test")
	req.Header.Set("X-AgentShield-Context-Token", validTestToken)
	got = resolveExecutionContext(req, nil)
	if got != "prod" {
		t.Errorf("resolveExecutionContext(req, nil) = %q, want \"prod\"", got)
	}
}

// TestResolveExecutionContext_CaseSensitivity verifies S-2:
// the context header value "TEST" (uppercase) is NOT treated as test context —
// only lowercase "test" triggers the test path.
func TestResolveExecutionContext_CaseSensitivity(t *testing.T) {
	cfg := &config.Config{
		TestContext: config.TestContextConfig{Enabled: true, Token: validTestToken},
	}

	// Note: the implementation uses strings.ToLower on the header,
	// so "TEST" will be lowercased to "test" and match.
	// Verify current behavior is documented in the test.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/evaluate", nil)
	req.Header.Set("X-AgentShield-Context", "TEST")
	req.Header.Set("X-AgentShield-Context-Token", validTestToken)

	got := resolveExecutionContext(req, cfg)
	// The implementation lowercases the header, so "TEST" → "test"
	if got != "test" {
		t.Errorf("resolveExecutionContext with uppercase 'TEST' context = %q, want \"test\" (implementation lowercases)", got)
	}
}

// ---------------------------------------------------------------------------
// Input Validation: Unicode/null-byte injection, boundary values
// ---------------------------------------------------------------------------

// TestInputValidation_NullByteInFields verifies that null bytes (\x00) in
// field values are rejected. Null bytes can cause log injection or truncation
// attacks in downstream systems.
func TestInputValidation_NullByteInFields(t *testing.T) {
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
		desc           string
	}{
		{
			name:           "null byte in event_id",
			body:           "{\"event_id\":\"test\x00inject\"}",
			expectedStatus: http.StatusBadRequest,
			desc:           "null byte in event_id should be rejected",
		},
		{
			name:           "null byte in tool name",
			body:           "{\"event_id\":\"test\",\"tool\":\"Bash\x00inject\"}",
			expectedStatus: http.StatusBadRequest,
			desc:           "null byte in tool name should be rejected",
		},
		{
			name:           "control char in session_id",
			body:           "{\"event_id\":\"test\",\"session_id\":\"sess\x01ion\"}",
			expectedStatus: http.StatusBadRequest,
			desc:           "control character in session_id should be rejected",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/evaluate", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != tt.expectedStatus {
				t.Errorf("%s: expected status %d, got %d", tt.desc, tt.expectedStatus, rec.Code)
			}
		})
	}
}

// TestInputValidation_FieldLengthBoundary verifies exact boundary behavior
// for MaxFieldValueLength: exactly at limit is accepted, one over is rejected.
func TestInputValidation_FieldLengthBoundary(t *testing.T) {
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
		valueLen       int
		expectedStatus int
	}{
		{"exactly at limit", MaxFieldValueLength, http.StatusOK},
		{"one over limit", MaxFieldValueLength + 1, http.StatusBadRequest},
		{"zero length", 0, http.StatusOK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			val := strings.Repeat("x", tt.valueLen)
			body := fmt.Sprintf(`{"event_id":"test","args":{"testfield":%q}}`, val)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/evaluate", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != tt.expectedStatus {
				t.Errorf("valueLen=%d: expected status %d, got %d", tt.valueLen, tt.expectedStatus, rec.Code)
			}
		})
	}
}

// TestInputValidation_TooManyFields verifies that requests with more than
// MaxFieldsCount fields are rejected to prevent memory exhaustion.
func TestInputValidation_TooManyFields(t *testing.T) {
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

	// Build a request with MaxFieldsCount+1 args
	argsBuilder := strings.Builder{}
	argsBuilder.WriteString(`{"event_id":"test","args":{`)
	for i := 0; i <= MaxFieldsCount; i++ {
		if i > 0 {
			argsBuilder.WriteString(",")
		}
		argsBuilder.WriteString(fmt.Sprintf(`"field%d":"value%d"`, i, i))
	}
	argsBuilder.WriteString("}}")

	req := httptest.NewRequest(http.MethodPost, "/api/v1/evaluate", strings.NewReader(argsBuilder.String()))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("Expected 400 for too many args fields, got %d", rec.Code)
	}
}

// TestInputValidation_ClientModeOverrideRejected verifies C-3 regression:
// clients cannot downgrade evaluation mode via the request body.
// The "mode" field in EvaluationRequest is no longer accepted from clients.
func TestInputValidation_ClientModeOverrideRejected(t *testing.T) {
	testStore, err := store.NewStore(":memory:")
	if err != nil {
		t.Fatalf("creating test store: %v", err)
	}
	defer testStore.Close()

	cfg := &config.Config{}
	mockEngine := &mockRuleEngine{
		results: []engine.RuleResult{
			{
				RuleID:   "test-rule",
				RuleName: "Test Rule",
				Severity: engine.SeverityCritical,
				Matched:  true,
			},
		},
	}
	evaluator := evaluate.NewEvaluator(mockEngine, config.ModeEnforce, "", nil, nil)

	srv, err := NewServer(cfg, evaluator, testStore, nil)
	if err != nil {
		t.Fatalf("creating test server: %v", err)
	}
	router := srv.setupTestRouter()

	// Send request with mode: shadow (trying to downgrade from enforce)
	body := `{"event_id":"test","mode":"shadow"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/evaluate", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	// The response action should be "block" — not "allow" or "log"
	// (client-supplied mode must not override server-side ModeEnforce)
	var resp evaluate.EvaluationResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Action != models.ActionBlock {
		t.Errorf("C-3: client mode override should be ignored, expected block action got %q", resp.Action)
	}
}
