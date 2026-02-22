package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/agentshield-ai/agentshield/internal/config"
	"github.com/agentshield-ai/agentshield/internal/engine"
	"github.com/agentshield-ai/agentshield/internal/evaluate"
	"github.com/agentshield-ai/agentshield/internal/models"
	"github.com/agentshield-ai/agentshield/internal/store"
)

// TestResolveExecutionContext tests the context resolution logic
func TestResolveExecutionContext(t *testing.T) {
	tests := []struct {
		name            string
		cfg             *config.Config
		useNilRequest   bool
		contextHeader   string
		contextToken    string
		expectedContext string
	}{
		{
			name: "nil request returns prod",
			cfg: &config.Config{
				TestContext: config.TestContextConfig{
					Enabled: true,
					Token:   "secret-token",
				},
			},
			useNilRequest:   true,
			expectedContext: "prod",
		},
		{
			name:            "nil config returns prod",
			cfg:             nil,
			expectedContext: "prod",
		},
		{
			name: "test context disabled in config",
			cfg: &config.Config{
				TestContext: config.TestContextConfig{
					Enabled: false,
					Token:   "secret-token",
				},
			},
			contextHeader:   "test",
			contextToken:    "secret-token",
			expectedContext: "prod",
		},
		{
			name: "test context requested but wrong header value",
			cfg: &config.Config{
				TestContext: config.TestContextConfig{
					Enabled: true,
					Token:   "secret-token",
				},
			},
			contextHeader:   "staging",
			contextToken:    "secret-token",
			expectedContext: "prod",
		},
		{
			name: "test context requested but no token",
			cfg: &config.Config{
				TestContext: config.TestContextConfig{
					Enabled: true,
					Token:   "secret-token",
				},
			},
			contextHeader:   "test",
			contextToken:    "",
			expectedContext: "prod",
		},
		{
			name: "test context requested with wrong token",
			cfg: &config.Config{
				TestContext: config.TestContextConfig{
					Enabled: true,
					Token:   "secret-token",
				},
			},
			contextHeader:   "test",
			contextToken:    "wrong-token",
			expectedContext: "prod",
		},
		{
			name: "test context accepted with correct token",
			cfg: &config.Config{
				TestContext: config.TestContextConfig{
					Enabled: true,
					Token:   "secret-token",
				},
			},
			contextHeader:   "test",
			contextToken:    "secret-token",
			expectedContext: "test",
		},
		{
			name: "test context header case insensitive TEST",
			cfg: &config.Config{
				TestContext: config.TestContextConfig{
					Enabled: true,
					Token:   "secret-token",
				},
			},
			contextHeader:   "TEST",
			contextToken:    "secret-token",
			expectedContext: "test",
		},
		{
			name: "test context header case insensitive Test",
			cfg: &config.Config{
				TestContext: config.TestContextConfig{
					Enabled: true,
					Token:   "secret-token",
				},
			},
			contextHeader:   "Test",
			contextToken:    "secret-token",
			expectedContext: "test",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var r *http.Request
			if !tt.useNilRequest {
				r = httptest.NewRequest(http.MethodPost, "/", nil)
				if tt.contextHeader != "" {
					r.Header.Set("X-AgentShield-Context", tt.contextHeader)
				}
				if tt.contextToken != "" {
					r.Header.Set("X-AgentShield-Context-Token", tt.contextToken)
				}
			}

			result := resolveExecutionContext(r, tt.cfg)
			if result != tt.expectedContext {
				t.Errorf("resolveExecutionContext() = %q, want %q", result, tt.expectedContext)
			}
		})
	}
}

// TestNormalizePluginRequest tests all branches of normalizePluginRequest
func TestNormalizePluginRequest(t *testing.T) {
	cfg := &config.Config{
		TestContext: config.TestContextConfig{
			Enabled: false,
		},
	}

	t.Run("tool_name alias applied when tool is empty", func(t *testing.T) {
		req := &models.EvaluationRequest{
			EventID:  "event-1",
			Tool:     "",
			ToolName: "my_tool",
		}
		r := httptest.NewRequest(http.MethodPost, "/", nil)
		normalizePluginRequest(req, r, cfg)

		if req.Tool != "my_tool" {
			t.Errorf("Expected Tool='my_tool', got '%s'", req.Tool)
		}
	})

	t.Run("tool_name alias NOT applied when tool already set", func(t *testing.T) {
		req := &models.EvaluationRequest{
			EventID:  "event-1",
			Tool:     "original_tool",
			ToolName: "alias_tool",
		}
		r := httptest.NewRequest(http.MethodPost, "/", nil)
		normalizePluginRequest(req, r, cfg)

		if req.Tool != "original_tool" {
			t.Errorf("Expected Tool='original_tool' (not overridden), got '%s'", req.Tool)
		}
	})

	t.Run("raw_params converted to args when args is nil", func(t *testing.T) {
		req := &models.EvaluationRequest{
			EventID: "event-1",
			Args:    nil,
			RawParams: map[string]interface{}{
				"string_val":  "hello",
				"int_val":     float64(42),
				"float_val":   float64(3.14),
				"bool_val":    true,
				"complex_val": map[string]interface{}{"nested": "obj"},
			},
		}
		r := httptest.NewRequest(http.MethodPost, "/", nil)
		normalizePluginRequest(req, r, cfg)

		if req.Args["string_val"] != "hello" {
			t.Errorf("Expected string_val='hello', got '%s'", req.Args["string_val"])
		}
		if req.Args["int_val"] != "42" {
			t.Errorf("Expected int_val='42', got '%s'", req.Args["int_val"])
		}
		if req.Args["float_val"] != "3.14" {
			t.Errorf("Expected float_val='3.14', got '%s'", req.Args["float_val"])
		}
		if req.Args["bool_val"] != "true" {
			t.Errorf("Expected bool_val='true', got '%s'", req.Args["bool_val"])
		}
		// Complex value should be JSON marshaled
		if req.Args["complex_val"] == "" {
			t.Error("Expected complex_val to be JSON marshaled")
		}
	})

	t.Run("raw_params NOT applied when args already set", func(t *testing.T) {
		req := &models.EvaluationRequest{
			EventID: "event-1",
			Args:    map[string]string{"existing": "value"},
			RawParams: map[string]interface{}{
				"from_params": "should_not_apply",
			},
		}
		r := httptest.NewRequest(http.MethodPost, "/", nil)
		normalizePluginRequest(req, r, cfg)

		if _, exists := req.Args["from_params"]; exists {
			t.Error("RawParams should NOT override existing args")
		}
		if req.Args["existing"] != "value" {
			t.Error("Existing args should be preserved")
		}
	})

	t.Run("command field added to args when not present", func(t *testing.T) {
		req := &models.EvaluationRequest{
			EventID: "event-1",
			Command: "ls -la",
			Args:    nil,
		}
		r := httptest.NewRequest(http.MethodPost, "/", nil)
		normalizePluginRequest(req, r, cfg)

		if req.Args["command"] != "ls -la" {
			t.Errorf("Expected args['command']='ls -la', got '%s'", req.Args["command"])
		}
	})

	t.Run("command NOT added to args when args already has command key", func(t *testing.T) {
		req := &models.EvaluationRequest{
			EventID: "event-1",
			Command: "ls -la",
			Args:    map[string]string{"command": "existing command"},
		}
		r := httptest.NewRequest(http.MethodPost, "/", nil)
		normalizePluginRequest(req, r, cfg)

		if req.Args["command"] != "existing command" {
			t.Errorf("Expected command to remain 'existing command', got '%s'", req.Args["command"])
		}
	})

	t.Run("fields built from tool and args", func(t *testing.T) {
		req := &models.EvaluationRequest{
			EventID: "event-1",
			Tool:    "bash",
			Args:    map[string]string{"command": "echo hello", "extra": "value"},
		}
		r := httptest.NewRequest(http.MethodPost, "/", nil)
		normalizePluginRequest(req, r, cfg)

		if req.Fields["tool"] != "bash" {
			t.Errorf("Expected fields['tool']='bash', got '%s'", req.Fields["tool"])
		}
		if req.Fields["event_type"] != "tool_call" {
			t.Errorf("Expected fields['event_type']='tool_call', got '%s'", req.Fields["event_type"])
		}
		if req.Fields["command"] != "echo hello" {
			t.Errorf("Expected fields['command']='echo hello', got '%s'", req.Fields["command"])
		}
		if req.Fields["extra"] != "value" {
			t.Errorf("Expected fields['extra']='value', got '%s'", req.Fields["extra"])
		}
	})

	t.Run("existing fields not overwritten", func(t *testing.T) {
		req := &models.EvaluationRequest{
			EventID: "event-1",
			Tool:    "bash",
			Args:    map[string]string{"command": "echo hello"},
			Fields: map[string]string{
				"tool":       "pre_existing_tool",
				"event_type": "pre_existing_type",
			},
		}
		r := httptest.NewRequest(http.MethodPost, "/", nil)
		normalizePluginRequest(req, r, cfg)

		if req.Fields["tool"] != "pre_existing_tool" {
			t.Errorf("Expected fields['tool']='pre_existing_tool', got '%s'", req.Fields["tool"])
		}
		if req.Fields["event_type"] != "pre_existing_type" {
			t.Errorf("Expected fields['event_type']='pre_existing_type', got '%s'", req.Fields["event_type"])
		}
	})

	t.Run("context field set to prod when test context disabled", func(t *testing.T) {
		req := &models.EvaluationRequest{
			EventID: "event-1",
		}
		r := httptest.NewRequest(http.MethodPost, "/", nil)
		normalizePluginRequest(req, r, cfg)

		// resolveExecutionContext returns "prod" when TestContext.Enabled=false
		if req.Fields["context"] != "prod" {
			t.Errorf("Expected fields['context']='prod', got '%s'", req.Fields["context"])
		}
	})

	t.Run("float64 integer vs decimal conversion", func(t *testing.T) {
		req := &models.EvaluationRequest{
			EventID: "event-1",
			RawParams: map[string]interface{}{
				"whole":    float64(42),
				"decimal":  float64(3.5),
				"negative": float64(-10),
			},
		}
		r := httptest.NewRequest(http.MethodPost, "/", nil)
		normalizePluginRequest(req, r, cfg)

		if req.Args["whole"] != "42" {
			t.Errorf("Expected '42', got '%s'", req.Args["whole"])
		}
		if req.Args["decimal"] != "3.5" {
			t.Errorf("Expected '3.5', got '%s'", req.Args["decimal"])
		}
		if req.Args["negative"] != "-10" {
			t.Errorf("Expected '-10', got '%s'", req.Args["negative"])
		}
	})
}

// TestIPRateLimiter tests the rate limiter behavior
func TestIPRateLimiter(t *testing.T) {
	t.Run("new IP gets separate limiter", func(t *testing.T) {
		rl := &ipRateLimiter{
			visitors: make(map[string]*ipVisitor),
			rps:      10,
			burst:    5,
		}

		lim1 := rl.getLimiter("192.168.1.1")
		lim2 := rl.getLimiter("192.168.1.2")

		if lim1 == lim2 {
			t.Error("Different IPs should get different limiters")
		}
	})

	t.Run("same IP gets same limiter", func(t *testing.T) {
		rl := &ipRateLimiter{
			visitors: make(map[string]*ipVisitor),
			rps:      10,
			burst:    5,
		}

		lim1 := rl.getLimiter("10.0.0.1")
		lim2 := rl.getLimiter("10.0.0.1")

		if lim1 != lim2 {
			t.Error("Same IP should get the same limiter instance")
		}
	})

	t.Run("burst allows multiple requests", func(t *testing.T) {
		rl := newIPRateLimiter(100, 10) // 100/s, burst 10

		lim := rl.getLimiter("test-ip")
		// Should allow burst number of requests immediately
		allowed := 0
		for i := 0; i < 10; i++ {
			if lim.Allow() {
				allowed++
			}
		}
		if allowed == 0 {
			t.Error("Rate limiter should allow some requests within burst")
		}
	})

	t.Run("middleware allows request within rate", func(t *testing.T) {
		rl := newIPRateLimiter(1000, 100) // Very permissive

		mw := rateLimitMiddleware(rl)
		handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "127.0.0.1:1234"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("Expected 200, got %d", rec.Code)
		}
	})

	t.Run("middleware uses X-Real-Ip header for IP detection", func(t *testing.T) {
		rl := newIPRateLimiter(1000, 100)
		mw := rateLimitMiddleware(rl)
		handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "10.0.0.1:1234"
		req.Header.Set("X-Real-Ip", "203.0.113.1")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("Expected 200, got %d", rec.Code)
		}

		// Verify real IP was tracked, not RemoteAddr
		rl.mu.Lock()
		_, hasRealIP := rl.visitors["203.0.113.1"]
		_, hasRemoteIP := rl.visitors["10.0.0.1"]
		rl.mu.Unlock()

		if !hasRealIP {
			t.Error("Expected X-Real-Ip to be tracked in rate limiter")
		}
		if hasRemoteIP {
			t.Error("Expected RemoteAddr NOT to be tracked when X-Real-Ip is set")
		}
	})

	t.Run("middleware blocks excessive requests", func(t *testing.T) {
		// Very tight rate limiting: burst of 1, very slow refill
		rl := newIPRateLimiter(0.001, 1)
		mw := rateLimitMiddleware(rl)
		handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))

		// First request uses burst token
		req1 := httptest.NewRequest(http.MethodGet, "/", nil)
		req1.RemoteAddr = "1.2.3.4:5678"
		rec1 := httptest.NewRecorder()
		handler.ServeHTTP(rec1, req1)

		// Second request should be rate-limited
		req2 := httptest.NewRequest(http.MethodGet, "/", nil)
		req2.RemoteAddr = "1.2.3.4:5678"
		rec2 := httptest.NewRecorder()
		handler.ServeHTTP(rec2, req2)

		if rec2.Code != http.StatusTooManyRequests {
			t.Errorf("Expected 429 (rate limited), got %d", rec2.Code)
		}
	})
}

// TestValidateEvaluationRequestContext tests context field validation
func TestValidateEvaluationRequestContext(t *testing.T) {
	t.Run("valid context prod", func(t *testing.T) {
		req := &models.EvaluationRequest{EventID: "event-1", Context: "prod"}
		if err := validateEvaluationRequest(req); err != nil {
			t.Errorf("validateEvaluationRequest() with 'prod' context error = %v", err)
		}
	})

	t.Run("valid context test", func(t *testing.T) {
		req := &models.EvaluationRequest{EventID: "event-1", Context: "test"}
		if err := validateEvaluationRequest(req); err != nil {
			t.Errorf("validateEvaluationRequest() with 'test' context error = %v", err)
		}
	})

	t.Run("invalid context staging", func(t *testing.T) {
		req := &models.EvaluationRequest{EventID: "event-1", Context: "staging"}
		err := validateEvaluationRequest(req)
		if err == nil {
			t.Error("Expected error for invalid context 'staging'")
		}
		if !strings.Contains(err.Error(), "context must be") {
			t.Errorf("Expected 'context must be' error, got: %v", err)
		}
	})

	t.Run("empty context is allowed", func(t *testing.T) {
		req := &models.EvaluationRequest{EventID: "event-1", Context: ""}
		if err := validateEvaluationRequest(req); err != nil {
			t.Errorf("validateEvaluationRequest() with empty context error = %v", err)
		}
	})

	t.Run("command field length validated", func(t *testing.T) {
		req := &models.EvaluationRequest{
			EventID: "event-1",
			Command: strings.Repeat("a", MaxFieldValueLength+1),
		}
		err := validateEvaluationRequest(req)
		if err == nil {
			t.Error("Expected error for oversized command")
		}
		if !strings.Contains(err.Error(), "command exceeds maximum length") {
			t.Errorf("Expected command length error, got: %v", err)
		}
	})

	t.Run("tool_name field length validated", func(t *testing.T) {
		req := &models.EvaluationRequest{
			EventID:  "event-1",
			ToolName: strings.Repeat("t", 150),
		}
		err := validateEvaluationRequest(req)
		if err == nil {
			t.Error("Expected error for oversized tool_name")
		}
		if !strings.Contains(err.Error(), "tool_name exceeds maximum length") {
			t.Errorf("Expected tool_name length error, got: %v", err)
		}
	})
}

// TestHandleEvaluateWithTestContext tests test context header handling in the HTTP handler
func TestHandleEvaluateWithTestContext(t *testing.T) {
	cfg := &config.Config{
		EvaluationMode: config.ModeEnforce,
		TestContext: config.TestContextConfig{
			Enabled: true,
			Token:   "test-secret-token",
		},
	}

	testStore, err := store.NewStore(":memory:")
	if err != nil {
		t.Fatalf("store.NewStore() error = %v", err)
	}
	defer testStore.Close()

	t.Run("valid test context token sets context=test in fields", func(t *testing.T) {
		captureEngine := &mockFieldCapturingEngine{}
		evaltr := evaluate.NewEvaluator(captureEngine, config.ModeEnforce, "", nil, nil)
		srv, _ := NewServer(cfg, evaltr, testStore, nil)

		body := `{"event_id": "smoke-test-1", "tool": "bash", "args": {"command": "ls"}}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/evaluate", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-AgentShield-Context", "test")
		req.Header.Set("X-AgentShield-Context-Token", "test-secret-token")

		rec := httptest.NewRecorder()
		srv.handleEvaluate(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("Expected 200, got %d (body: %s)", rec.Code, rec.Body.String())
		}

		if captureEngine.capturedFields["context"] != "test" {
			t.Errorf("Expected context='test' in fields, got '%s'", captureEngine.capturedFields["context"])
		}
	})

	t.Run("invalid test context token defaults to prod", func(t *testing.T) {
		captureEngine := &mockFieldCapturingEngine{}
		evaltr := evaluate.NewEvaluator(captureEngine, config.ModeEnforce, "", nil, nil)
		srv, _ := NewServer(cfg, evaltr, testStore, nil)

		body := `{"event_id": "test-event-1", "tool": "bash", "args": {"command": "ls"}}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/evaluate", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-AgentShield-Context", "test")
		req.Header.Set("X-AgentShield-Context-Token", "wrong-token")

		rec := httptest.NewRecorder()
		srv.handleEvaluate(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("Expected 200, got %d", rec.Code)
		}

		if captureEngine.capturedFields["context"] != "prod" {
			t.Errorf("Expected context='prod' with invalid token, got '%s'", captureEngine.capturedFields["context"])
		}
	})
}

// TestSecurityHeadersMiddleware verifies security response headers
func TestSecurityHeadersMiddleware(t *testing.T) {
	handler := securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	expectedHeaders := map[string]string{
		"X-Content-Type-Options":  "nosniff",
		"X-Frame-Options":         "DENY",
		"Cache-Control":           "no-store",
		"Content-Security-Policy": "default-src 'none'",
	}

	for header, want := range expectedHeaders {
		got := rec.Header().Get(header)
		if got != want {
			t.Errorf("Header %s = %q, want %q", header, got, want)
		}
	}
}

// TestStoreMatchedAlerts tests the storeMatchedAlerts helper
func TestStoreMatchedAlerts(t *testing.T) {
	testStore, err := store.NewStore(":memory:")
	if err != nil {
		t.Fatalf("store.NewStore() error = %v", err)
	}
	defer testStore.Close()

	cfg := &config.Config{}
	mockEng := &mockRuleEngine{}
	evaltr := evaluate.NewEvaluator(mockEng, config.ModeAudit, "", nil, nil)
	srv, _ := NewServer(cfg, evaltr, testStore, nil)

	t.Run("stores matched alert", func(t *testing.T) {
		response := &evaluate.EvaluationResponse{
			EventID: "test-event",
			Action:  "block",
			Alerts: []engine.RuleResult{
				{
					RuleName: "test-rule",
					Severity: engine.SeverityHigh,
					Matched:  true,
				},
			},
			Timestamp: time.Now(),
		}

		req := &models.EvaluationRequest{
			EventID:   "test-event",
			Tool:      "bash",
			Args:      map[string]string{"command": "ls"},
			SessionID: "test-session",
		}

		srv.storeMatchedAlerts(response, req)

		count, err := testStore.CountAlerts(&store.AlertQuery{})
		if err != nil {
			t.Fatalf("CountAlerts() error = %v", err)
		}
		if count != 1 {
			t.Errorf("Expected 1 stored alert, got %d", count)
		}
	})

	t.Run("does not store unmatched alerts", func(t *testing.T) {
		freshStore, _ := store.NewStore(":memory:")
		defer freshStore.Close()
		srv2, _ := NewServer(cfg, evaltr, freshStore, nil)

		response := &evaluate.EvaluationResponse{
			EventID: "test-event-2",
			Action:  "allow",
			Alerts: []engine.RuleResult{
				{
					RuleName: "test-rule",
					Severity: engine.SeverityLow,
					Matched:  false,
				},
			},
			Timestamp: time.Now(),
		}
		req := &models.EvaluationRequest{EventID: "test-event-2"}

		srv2.storeMatchedAlerts(response, req)

		count, err := freshStore.CountAlerts(&store.AlertQuery{})
		if err != nil {
			t.Fatalf("CountAlerts() error = %v", err)
		}
		if count != 0 {
			t.Errorf("Expected 0 stored unmatched alerts, got %d", count)
		}
	})

	t.Run("nil args stored as empty string", func(t *testing.T) {
		freshStore, _ := store.NewStore(":memory:")
		defer freshStore.Close()
		srv3, _ := NewServer(cfg, evaltr, freshStore, nil)

		response := &evaluate.EvaluationResponse{
			EventID: "test-event-3",
			Action:  "block",
			Alerts: []engine.RuleResult{
				{RuleName: "test-rule", Severity: engine.SeverityHigh, Matched: true},
			},
			Timestamp: time.Now(),
		}
		req := &models.EvaluationRequest{
			EventID: "test-event-3",
			Args:    nil, // nil args
		}

		srv3.storeMatchedAlerts(response, req)

		alerts, err := freshStore.QueryAlerts(&store.AlertQuery{Limit: 10})
		if err != nil {
			t.Fatalf("QueryAlerts() error = %v", err)
		}
		if len(alerts) != 1 {
			t.Fatalf("Expected 1 alert, got %d", len(alerts))
		}
		// nil args should result in empty string in DB
		if alerts[0].Args != "" {
			t.Errorf("Expected empty args string for nil args, got '%s'", alerts[0].Args)
		}
	})
}

// TestHandleFeedbackQueryStoreError covers the 500 path in handleFeedbackQuery
// when GetFeedbackForRule returns an error (closed store).
func TestHandleFeedbackQueryStoreError(t *testing.T) {
	brokenStore, err := store.NewStore(":memory:")
	if err != nil {
		t.Fatalf("creating store: %v", err)
	}
	// Close the store so all operations fail.
	brokenStore.Close()

	cfg := &config.Config{}
	evaluator := evaluate.NewEvaluator(&mockRuleEngine{}, config.ModeAudit, "", nil, nil)
	srv, err := NewServer(cfg, evaluator, brokenStore, nil)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/feedback?rule=some-rule", nil)
	w := httptest.NewRecorder()
	srv.handleFeedbackQuery(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 from closed store, got %d (body: %s)", w.Code, w.Body.String())
	}
}

// TestHandleAlertsStoreError covers the 500 path in handleAlerts
// when QueryAlerts returns an error (closed store).
func TestHandleAlertsStoreError(t *testing.T) {
	brokenStore, err := store.NewStore(":memory:")
	if err != nil {
		t.Fatalf("creating store: %v", err)
	}
	brokenStore.Close()

	cfg := &config.Config{}
	evaluator := evaluate.NewEvaluator(&mockRuleEngine{}, config.ModeAudit, "", nil, nil)
	srv, err := NewServer(cfg, evaluator, brokenStore, nil)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/alerts", nil)
	w := httptest.NewRecorder()
	srv.handleAlerts(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 from closed store, got %d (body: %s)", w.Code, w.Body.String())
	}
}

// TestHandleAlertsInvalidSeverity covers the 400 path for invalid severity.
func TestHandleAlertsInvalidSeverity(t *testing.T) {
	testStore, err := store.NewStore(":memory:")
	if err != nil {
		t.Fatalf("creating store: %v", err)
	}
	defer testStore.Close()

	cfg := &config.Config{}
	evaluator := evaluate.NewEvaluator(&mockRuleEngine{}, config.ModeAudit, "", nil, nil)
	srv, err := NewServer(cfg, evaluator, testStore, nil)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/alerts?severity=extreme", nil)
	w := httptest.NewRecorder()
	srv.handleAlerts(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid severity, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Invalid severity") {
		t.Errorf("expected 'Invalid severity' in response, got: %s", w.Body.String())
	}
}
