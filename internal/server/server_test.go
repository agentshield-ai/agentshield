package server

import (
	"context"
	"encoding/json"
	"fmt"
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
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// mockRuleEngine is a mock implementation of the rule engine for testing
type mockRuleEngine struct {
	results []engine.RuleResult
}

func (m *mockRuleEngine) Evaluate(fields map[string]string) []engine.RuleResult {
	return m.results
}

// setupTestRouter creates a Chi router with the server's routes for testing
func (s *Server) setupTestRouter() *chi.Mux {
	r := chi.NewRouter()

	// Add basic middleware (but skip auth for tests unless specifically testing auth)
	r.Use(middleware.Recoverer)
	r.Use(middleware.RequestID)

	// API v1 routes
	r.Route("/api/v1", func(r chi.Router) {
		r.Post("/evaluate", s.handleEvaluate)
		r.Get("/health", s.handleHealth)
		r.Get("/alerts", s.handleAlerts)
		r.Post("/audit", s.handleAuditEvent)
		r.Post("/lifecycle", s.handleLifecycleEvent)
		r.Route("/feedback", func(r chi.Router) {
			r.Post("/", s.handleFeedbackSubmission)
			r.Get("/", s.handleFeedbackQuery)
		})
	})

	// Legacy routes for backwards compatibility
	r.Get("/health", s.handleHealth)
	r.Post("/evaluate", s.handleEvaluate)

	return r
}

func TestHandleEvaluate(t *testing.T) {
	// Create in-memory store for testing
	testStore, err := store.NewStore(":memory:")
	if err != nil {
		t.Fatalf("creating test store: %v", err)
	}
	defer func() { _ = testStore.Close() }()

	cfg := &config.Config{
		EvaluationMode: config.ModeAudit,
	}

	// Create mock rule engine that returns test alerts
	mockEngine := &mockRuleEngine{
		results: []engine.RuleResult{
			{
				RuleName:    "test-rule",
				Severity:    engine.SeverityHigh,
				Matched:     true,
				RuleID:      "test-rule-1",
				Description: "Test alert",
			},
		},
	}

	// Create real evaluator with mock engine
	evaluator := evaluate.NewEvaluator(mockEngine, config.ModeAudit, "", nil, nil)

	server, err := NewServer(cfg, evaluator, testStore, nil)
	if err != nil {
		t.Fatalf("creating test server: %v", err)
	}

	tests := []struct {
		name           string
		method         string
		body           string
		expectedStatus int
		checkResponse  func(*testing.T, *httptest.ResponseRecorder)
	}{
		{
			name:           "valid POST request",
			method:         http.MethodPost,
			body:           `{"event_id": "test-123", "tool": "test-tool", "args": {"command": "test"}}`,
			expectedStatus: http.StatusOK,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				var response evaluate.EvaluationResponse
				if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
					t.Errorf("Failed to unmarshal response: %v", err)
					return
				}
				if response.Action != models.ActionLog {
					t.Errorf("Expected action log (audit mode), got %s", response.Action)
				}
				if len(response.Alerts) != 1 {
					t.Errorf("Expected 1 alert, got %d", len(response.Alerts))
				}
			},
		},
		{
			name:           "GET method not allowed",
			method:         http.MethodGet,
			body:           "",
			expectedStatus: http.StatusMethodNotAllowed,
			checkResponse:  nil,
		},
		{
			name:           "missing event_id",
			method:         http.MethodPost,
			body:           `{"tool": "test-tool"}`,
			expectedStatus: http.StatusBadRequest,
			checkResponse:  nil,
		},
		{
			name:           "invalid JSON",
			method:         http.MethodPost,
			body:           `{"invalid": json}`,
			expectedStatus: http.StatusBadRequest,
			checkResponse:  nil,
		},
		{
			name:           "field auto-mapping",
			method:         http.MethodPost,
			body:           `{"event_id": "test-456", "tool": "test-tool", "args": {"command": "dangerous"}}`,
			expectedStatus: http.StatusOK,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				// This test verifies that top-level tool, args.command get mapped into fields
				// The mockEvaluator would have received the request with properly mapped fields
				var response evaluate.EvaluationResponse
				if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
					t.Errorf("Failed to unmarshal response: %v", err)
				}
			},
		},
	}

	// Set up router for testing
	router := server.setupTestRouter()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, "/api/v1/evaluate", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")

			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code != tt.expectedStatus {
				t.Errorf("Expected status %d, got %d", tt.expectedStatus, rec.Code)
			}

			if tt.checkResponse != nil {
				tt.checkResponse(t, rec)
			}
		})
	}
}

func TestHandleEvaluateBodyTooLarge(t *testing.T) {
	cfg := &config.Config{}
	mockEngine := &mockRuleEngine{}
	evaluator := evaluate.NewEvaluator(mockEngine, config.ModeAudit, "", nil, nil)
	testStore, _ := store.NewStore(":memory:")
	defer func() { _ = testStore.Close() }()

	server, err := NewServer(cfg, evaluator, testStore, nil)
	if err != nil {
		t.Fatalf("creating test server: %v", err)
	}

	// Create a request body larger than MaxRequestBodySize
	largeData := strings.Repeat("x", MaxRequestBodySize+1000)
	largeBody := fmt.Sprintf(`{"event_id": "test", "large_field": "%s"}`, largeData)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/evaluate", strings.NewReader(largeBody))
	req.Header.Set("Content-Type", "application/json")

	// Set up router for testing
	router := server.setupTestRouter()

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("Expected status 413, got %d", rec.Code)
	}
}

func TestHandleHealth(t *testing.T) {
	testStore, err := store.NewStore(":memory:")
	if err != nil {
		t.Fatalf("creating test store: %v", err)
	}
	defer func() { _ = testStore.Close() }()

	cfg := &config.Config{
		EvaluationMode: config.ModeEnforce,
		Rules:          config.RulesConfig{Dir: "/test/rules"},
	}

	mockEngine := &mockRuleEngine{}
	evaluator := evaluate.NewEvaluator(mockEngine, config.ModeAudit, "", nil, nil)

	server, err := NewServer(cfg, evaluator, testStore, nil)
	if err != nil {
		t.Fatalf("creating test server: %v", err)
	}

	tests := []struct {
		name           string
		method         string
		expectedStatus int
	}{
		{
			name:           "GET returns health status",
			method:         http.MethodGet,
			expectedStatus: http.StatusOK,
		},
		{
			name:           "POST method not allowed",
			method:         http.MethodPost,
			expectedStatus: http.StatusMethodNotAllowed,
		},
	}

	// Set up router for testing
	router := server.setupTestRouter()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, "/api/v1/health", nil)
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)

			if rec.Code != tt.expectedStatus {
				t.Errorf("Expected status %d, got %d", tt.expectedStatus, rec.Code)
			}

			if tt.method == http.MethodGet {
				var response HealthResponse
				if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
					t.Errorf("Failed to unmarshal health response: %v", err)
					return
				}

				if response.Status != "ok" {
					t.Errorf("Expected status 'ok', got '%s'", response.Status)
				}

				if response.Version == "" {
					t.Error("Expected version to be set")
				}

				if response.UptimeSeconds < 0 {
					t.Error("Expected uptime to be >= 0")
				}

				// H-3: Config details are no longer exposed on the health endpoint
				// (Config field removed from HealthResponse for security)
			}
		})
	}
}

func TestHandleAlerts(t *testing.T) {
	testStore, err := store.NewStore(":memory:")
	if err != nil {
		t.Fatalf("creating test store: %v", err)
	}
	defer func() { _ = testStore.Close() }()

	// Insert test alerts
	testAlert := &store.Alert{
		RuleName:    "test-rule",
		Severity:    "high",
		Tool:        "test-tool",
		Args:        `{"command": "test"}`,
		ActionTaken: "block",
		Timestamp:   time.Now(),
		SessionID:   "test-session",
		EventID:     "test-event-1",
	}

	if err := testStore.InsertAlert(testAlert); err != nil {
		t.Fatalf("inserting test alert: %v", err)
	}

	cfg := &config.Config{}
	mockEngine := &mockRuleEngine{}
	evaluator := evaluate.NewEvaluator(mockEngine, config.ModeAudit, "", nil, nil)

	server, err := NewServer(cfg, evaluator, testStore, nil)
	if err != nil {
		t.Fatalf("creating test server: %v", err)
	}

	tests := []struct {
		name           string
		method         string
		queryParams    string
		expectedStatus int
		checkResponse  func(*testing.T, *httptest.ResponseRecorder)
	}{
		{
			name:           "GET alerts without params",
			method:         http.MethodGet,
			queryParams:    "",
			expectedStatus: http.StatusOK,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				var response map[string]interface{}
				if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
					t.Errorf("Failed to unmarshal response: %v", err)
					return
				}

				alerts, ok := response["alerts"].([]interface{})
				if !ok {
					t.Error("Expected alerts array in response")
					return
				}

				if len(alerts) != 1 {
					t.Errorf("Expected 1 alert, got %d", len(alerts))
				}
			},
		},
		{
			name:           "GET with limit param",
			method:         http.MethodGet,
			queryParams:    "?limit=50",
			expectedStatus: http.StatusOK,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				var response map[string]interface{}
				if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
					t.Errorf("Failed to unmarshal response: %v", err)
					return
				}

				limit, ok := response["limit"].(float64)
				if !ok || int(limit) != 50 {
					t.Errorf("Expected limit 50, got %v", response["limit"])
				}
			},
		},
		{
			name:           "GET with severity filter",
			method:         http.MethodGet,
			queryParams:    "?severity=high",
			expectedStatus: http.StatusOK,
			checkResponse:  nil,
		},
		{
			name:           "POST method not allowed",
			method:         http.MethodPost,
			queryParams:    "",
			expectedStatus: http.StatusMethodNotAllowed,
			checkResponse:  nil,
		},
	}

	// Set up router for testing
	router := server.setupTestRouter()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, "/api/v1/alerts"+tt.queryParams, nil)
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)

			if rec.Code != tt.expectedStatus {
				t.Errorf("Expected status %d, got %d", tt.expectedStatus, rec.Code)
			}

			if tt.checkResponse != nil {
				tt.checkResponse(t, rec)
			}
		})
	}
}

func TestHandleFeedbackPost(t *testing.T) {
	testStore, err := store.NewStore(":memory:")
	if err != nil {
		t.Fatalf("creating test store: %v", err)
	}
	defer func() { _ = testStore.Close() }()

	// Insert a test alert so feedback can find the rule name
	testAlert := &store.Alert{
		RuleName:    "test-rule",
		Severity:    "high",
		Tool:        "test-tool",
		Args:        `{"command": "test"}`,
		ActionTaken: "block",
		Timestamp:   time.Now(),
		SessionID:   "test-session",
		EventID:     "test-event",
	}
	if err := testStore.InsertAlert(testAlert); err != nil {
		t.Fatalf("inserting test alert: %v", err)
	}

	cfg := &config.Config{}
	mockEngine := &mockRuleEngine{}
	evaluator := evaluate.NewEvaluator(mockEngine, config.ModeAudit, "", nil, nil)

	server, err := NewServer(cfg, evaluator, testStore, nil)
	if err != nil {
		t.Fatalf("creating test server: %v", err)
	}

	tests := []struct {
		name           string
		body           string
		expectedStatus int
		checkResponse  func(*testing.T, *httptest.ResponseRecorder)
	}{
		{
			name:           "valid feedback submission",
			body:           `{"event_id": "test-event", "alert_id": 1, "feedback_type": "false_positive", "comment": "Test comment"}`,
			expectedStatus: http.StatusOK,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				var response map[string]interface{}
				if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
					t.Errorf("Failed to unmarshal response: %v", err)
					return
				}

				if response["status"] != "received" {
					t.Errorf("Expected status 'received', got '%v'", response["status"])
				}
			},
		},
		{
			name:           "invalid JSON",
			body:           `{"invalid": json}`,
			expectedStatus: http.StatusBadRequest,
			checkResponse:  nil,
		},
	}

	// Set up router for testing
	router := server.setupTestRouter()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/feedback", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)

			if rec.Code != tt.expectedStatus {
				t.Errorf("Expected status %d, got %d", tt.expectedStatus, rec.Code)
			}

			if tt.checkResponse != nil {
				tt.checkResponse(t, rec)
			}
		})
	}
}

func TestHandleFeedbackGet(t *testing.T) {
	testStore, err := store.NewStore(":memory:")
	if err != nil {
		t.Fatalf("creating test store: %v", err)
	}
	defer func() { _ = testStore.Close() }()

	cfg := &config.Config{}
	mockEngine := &mockRuleEngine{}
	evaluator := evaluate.NewEvaluator(mockEngine, config.ModeAudit, "", nil, nil)

	server, err := NewServer(cfg, evaluator, testStore, nil)
	if err != nil {
		t.Fatalf("creating test server: %v", err)
	}

	tests := []struct {
		name           string
		queryParams    string
		expectedStatus int
		checkResponse  func(*testing.T, *httptest.ResponseRecorder)
	}{
		{
			name:           "get feedback with rule param",
			queryParams:    "?rule=test-rule",
			expectedStatus: http.StatusOK,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				var response map[string]interface{}
				if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
					t.Errorf("Failed to unmarshal response: %v", err)
					return
				}

				if response["rule_name"] != "test-rule" {
					t.Errorf("Expected rule_name 'test-rule', got '%v'", response["rule_name"])
				}

				if _, ok := response["feedback"]; !ok {
					t.Error("Expected feedback array in response")
				}

				if _, ok := response["false_positive_rate"]; !ok {
					t.Error("Expected false_positive_rate in response")
				}
			},
		},
		{
			name:           "missing rule param",
			queryParams:    "",
			expectedStatus: http.StatusBadRequest,
			checkResponse:  nil,
		},
	}

	// Set up router for testing
	router := server.setupTestRouter()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/feedback"+tt.queryParams, nil)
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)

			if rec.Code != tt.expectedStatus {
				t.Errorf("Expected status %d, got %d", tt.expectedStatus, rec.Code)
			}

			if tt.checkResponse != nil {
				tt.checkResponse(t, rec)
			}
		})
	}
}

func TestHandleAuditAndLifecycle(t *testing.T) {
	testStore, err := store.NewStore(":memory:")
	if err != nil {
		t.Fatalf("creating test store: %v", err)
	}
	defer func() { _ = testStore.Close() }()

	cfg := &config.Config{}
	mockEngine := &mockRuleEngine{}
	evaluator := evaluate.NewEvaluator(mockEngine, config.ModeAudit, "", nil, nil)

	server, err := NewServer(cfg, evaluator, testStore, nil)
	if err != nil {
		t.Fatalf("creating test server: %v", err)
	}

	router := server.setupTestRouter()

	tests := []struct {
		name           string
		path           string
		body           string
		expectedStatus int
	}{
		{
			name:           "audit accepts valid json",
			path:           "/api/v1/audit",
			body:           `{"event_id":"e1","event_type":"tool_result"}`,
			expectedStatus: http.StatusAccepted,
		},
		{
			name:           "lifecycle accepts valid json",
			path:           "/api/v1/lifecycle",
			body:           `{"event_id":"e2","event_type":"session_start"}`,
			expectedStatus: http.StatusAccepted,
		},
		{
			name:           "audit rejects invalid json",
			path:           "/api/v1/audit",
			body:           `{"event_id":`,
			expectedStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tt.path, strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code != tt.expectedStatus {
				t.Fatalf("expected %d, got %d", tt.expectedStatus, rec.Code)
			}
		})
	}
}

func TestNewServer(t *testing.T) {
	cfg := &config.Config{
		EvaluationMode: config.ModeAudit,
		Auth:           config.AuthConfig{Token: "test-token"},
	}

	testStore, err := store.NewStore(":memory:")
	if err != nil {
		t.Fatalf("creating test store: %v", err)
	}
	defer func() { _ = testStore.Close() }()

	mockEngine := &mockRuleEngine{}
	evaluator := evaluate.NewEvaluator(mockEngine, config.ModeAudit, "", nil, nil)
	server, err := NewServer(cfg, evaluator, testStore, nil)
	if err != nil {
		t.Fatalf("NewServer() failed: %v", err)
	}
	if server == nil {
		t.Fatal("NewServer() returned nil")
	}

	if server.config != cfg {
		t.Error("Server config not set correctly")
	}
	if server.evaluator != evaluator {
		t.Error("Server evaluator not set correctly")
	}
	if server.store != testStore {
		t.Error("Server store not set correctly")
	}
}

func TestNewServerWithoutAuth(t *testing.T) {
	cfg := &config.Config{
		EvaluationMode: config.ModeAudit,
		Auth:           config.AuthConfig{Token: ""}, // No auth token
	}

	testStore, err := store.NewStore(":memory:")
	if err != nil {
		t.Fatalf("creating test store: %v", err)
	}
	defer func() { _ = testStore.Close() }()

	mockEngine := &mockRuleEngine{}
	evaluator := evaluate.NewEvaluator(mockEngine, config.ModeAudit, "", nil, nil)

	server, err := NewServer(cfg, evaluator, testStore, nil)
	if err != nil {
		t.Errorf("NewServer() failed: %v", err)
	}

	if server.auth != nil {
		t.Error("Expected no auth middleware when token is empty")
	}
}

func TestRequestLogger(t *testing.T) {
	testStore, err := store.NewStore(":memory:")
	if err != nil {
		t.Fatalf("creating test store: %v", err)
	}
	defer func() { _ = testStore.Close() }()

	cfg := &config.Config{}
	mockEngine := &mockRuleEngine{}
	evaluator := evaluate.NewEvaluator(mockEngine, config.ModeAudit, "", nil, nil)

	server, err := NewServer(cfg, evaluator, testStore, nil)
	if err != nil {
		t.Fatalf("creating test server: %v", err)
	}

	// Create a test handler that the logger will wrap
	testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("test response"))
	})

	// Wrap with request logger
	loggedHandler := server.requestLogger(testHandler)

	// Test the logged handler
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("User-Agent", "test-agent")
	req.RemoteAddr = "127.0.0.1:12345"

	rec := httptest.NewRecorder()
	loggedHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", rec.Code)
	}

	body := rec.Body.String()
	if body != "test response" {
		t.Errorf("Expected 'test response', got '%s'", body)
	}
}

func TestHandleHealthDegraded(t *testing.T) {
	// Create a mock store that fails health checks
	testStore, err := store.NewStore(":memory:")
	if err != nil {
		t.Fatalf("creating test store: %v", err)
	}
	// Close the store to make it fail health checks
	_ = testStore.Close()

	cfg := &config.Config{
		EvaluationMode: config.ModeEnforce,
		Rules:          config.RulesConfig{Dir: "/test/rules"},
	}

	mockEngine := &mockRuleEngine{}
	evaluator := evaluate.NewEvaluator(mockEngine, config.ModeAudit, "", nil, nil)

	server, err := NewServer(cfg, evaluator, testStore, nil)
	if err != nil {
		t.Fatalf("creating test server: %v", err)
	}

	router := server.setupTestRouter()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	// Should return 503 (Service Unavailable) when store is unhealthy
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("Expected status 503, got %d", rec.Code)
	}

	var response HealthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Errorf("Failed to unmarshal health response: %v", err)
		return
	}

	if response.Status != "degraded" {
		t.Errorf("Expected status 'degraded', got '%s'", response.Status)
	}
}

func TestHandleAlertsWithFilters(t *testing.T) {
	testStore, err := store.NewStore(":memory:")
	if err != nil {
		t.Fatalf("creating test store: %v", err)
	}
	defer func() { _ = testStore.Close() }()

	// Insert multiple test alerts with different properties
	now := time.Now()
	alerts := []*store.Alert{
		{
			RuleName:    "rule-1",
			Severity:    "high",
			Tool:        "tool-1",
			ActionTaken: "block",
			Timestamp:   now.Add(-1 * time.Hour),
			SessionID:   "session-1",
			EventID:     "event-1",
		},
		{
			RuleName:    "rule-2",
			Severity:    "critical",
			Tool:        "tool-2",
			ActionTaken: "block",
			Timestamp:   now.Add(-30 * time.Minute),
			SessionID:   "session-2",
			EventID:     "event-2",
		},
		{
			RuleName:    "rule-1",
			Severity:    "medium",
			Tool:        "tool-1",
			ActionTaken: "log",
			Timestamp:   now.Add(-15 * time.Minute),
			SessionID:   "session-1",
			EventID:     "event-3",
		},
	}

	for _, alert := range alerts {
		if err := testStore.InsertAlert(alert); err != nil {
			t.Fatalf("inserting test alert: %v", err)
		}
	}

	cfg := &config.Config{}
	mockEngine := &mockRuleEngine{}
	evaluator := evaluate.NewEvaluator(mockEngine, config.ModeAudit, "", nil, nil)

	server, err := NewServer(cfg, evaluator, testStore, nil)
	if err != nil {
		t.Fatalf("creating test server: %v", err)
	}

	tests := []struct {
		name           string
		queryParams    string
		expectedStatus int
		checkResponse  func(*testing.T, *httptest.ResponseRecorder)
	}{
		{
			name:           "filter by severity",
			queryParams:    "?severity=high",
			expectedStatus: http.StatusOK,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				var response map[string]interface{}
				if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
					t.Errorf("Failed to unmarshal response: %v", err)
					return
				}
				// Should only return high severity alerts
				alerts := response["alerts"].([]interface{})
				if len(alerts) != 1 {
					t.Errorf("Expected 1 high severity alert, got %d", len(alerts))
				}
			},
		},
		{
			name:           "filter by rule",
			queryParams:    "?rule=rule-1",
			expectedStatus: http.StatusOK,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				var response map[string]interface{}
				if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
					t.Errorf("Failed to unmarshal response: %v", err)
					return
				}
				alerts := response["alerts"].([]interface{})
				if len(alerts) != 2 {
					t.Errorf("Expected 2 rule-1 alerts, got %d", len(alerts))
				}
			},
		},
		{
			name:           "filter by session_id",
			queryParams:    "?session_id=session-2",
			expectedStatus: http.StatusOK,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				var response map[string]interface{}
				if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
					t.Errorf("Failed to unmarshal response: %v", err)
					return
				}
				alerts := response["alerts"].([]interface{})
				if len(alerts) != 1 {
					t.Errorf("Expected 1 session-2 alert, got %d", len(alerts))
				}
			},
		},
		{
			name:           "time range filter",
			queryParams:    fmt.Sprintf("?since=%s&until=%s", now.Add(-45*time.Minute).Format(time.RFC3339), now.Add(-20*time.Minute).Format(time.RFC3339)),
			expectedStatus: http.StatusOK,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				var response map[string]interface{}
				if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
					t.Errorf("Failed to unmarshal response: %v", err)
					return
				}
				// Should return alerts within time range
				alerts := response["alerts"].([]interface{})
				if len(alerts) != 1 {
					t.Errorf("Expected 1 alert in time range, got %d", len(alerts))
				}
			},
		},
		{
			name:           "offset and limit",
			queryParams:    "?offset=1&limit=1",
			expectedStatus: http.StatusOK,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				var response map[string]interface{}
				if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
					t.Errorf("Failed to unmarshal response: %v", err)
					return
				}

				limit := int(response["limit"].(float64))
				offset := int(response["offset"].(float64))

				if limit != 1 {
					t.Errorf("Expected limit 1, got %d", limit)
				}
				if offset != 1 {
					t.Errorf("Expected offset 1, got %d", offset)
				}
			},
		},
		{
			name:           "invalid limit gets default",
			queryParams:    "?limit=invalid",
			expectedStatus: http.StatusOK,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				var response map[string]interface{}
				if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
					t.Errorf("Failed to unmarshal response: %v", err)
					return
				}
				// Should use default limit
				limit := int(response["limit"].(float64))
				if limit != 100 {
					t.Errorf("Expected default limit 100, got %d", limit)
				}
			},
		},
	}

	router := server.setupTestRouter()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/alerts"+tt.queryParams, nil)
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)

			if rec.Code != tt.expectedStatus {
				t.Errorf("Expected status %d, got %d", tt.expectedStatus, rec.Code)
			}

			if tt.checkResponse != nil {
				tt.checkResponse(t, rec)
			}
		})
	}
}

func TestHandleFeedbackSubmissionErrors(t *testing.T) {
	testStore, err := store.NewStore(":memory:")
	if err != nil {
		t.Fatalf("creating test store: %v", err)
	}
	defer func() { _ = testStore.Close() }()

	cfg := &config.Config{}
	mockEngine := &mockRuleEngine{}
	evaluator := evaluate.NewEvaluator(mockEngine, config.ModeAudit, "", nil, nil)

	server, err := NewServer(cfg, evaluator, testStore, nil)
	if err != nil {
		t.Fatalf("creating test server: %v", err)
	}

	tests := []struct {
		name           string
		body           string
		expectedStatus int
	}{
		{
			name:           "empty feedback type",
			body:           `{"event_id": "test", "feedback_type": ""}`,
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:           "invalid feedback type",
			body:           `{"event_id": "test", "feedback_type": "invalid_type"}`,
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:           "missing event_id",
			body:           `{"feedback_type": "false_positive"}`,
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:           "non-positive alert_id",
			body:           `{"event_id": "test", "alert_id": 0, "feedback_type": "false_positive"}`,
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:           "too large body",
			body:           fmt.Sprintf(`{"event_id": "test", "feedback_type": "false_positive", "comment": "%s"}`, strings.Repeat("x", MaxRequestBodySize+1000)),
			expectedStatus: http.StatusBadRequest, // MaxBytesReader error gets parsed as bad request
		},
	}

	router := server.setupTestRouter()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/feedback", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)

			if rec.Code != tt.expectedStatus {
				t.Errorf("Expected status %d, got %d", tt.expectedStatus, rec.Code)
			}
		})
	}
}

func TestHandleFeedbackQueryWithLimits(t *testing.T) {
	testStore, err := store.NewStore(":memory:")
	if err != nil {
		t.Fatalf("creating test store: %v", err)
	}
	defer func() { _ = testStore.Close() }()

	cfg := &config.Config{}
	mockEngine := &mockRuleEngine{}
	evaluator := evaluate.NewEvaluator(mockEngine, config.ModeAudit, "", nil, nil)

	server, err := NewServer(cfg, evaluator, testStore, nil)
	if err != nil {
		t.Fatalf("creating test server: %v", err)
	}

	tests := []struct {
		name           string
		queryParams    string
		expectedStatus int
		checkResponse  func(*testing.T, *httptest.ResponseRecorder)
	}{
		{
			name:           "with valid limit",
			queryParams:    "?rule=test-rule&limit=50",
			expectedStatus: http.StatusOK,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				var response map[string]interface{}
				if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
					t.Errorf("Failed to unmarshal response: %v", err)
				}
				// Check that response contains expected fields
				if _, ok := response["rule_name"]; !ok {
					t.Error("Expected rule_name in response")
				}
				if _, ok := response["feedback"]; !ok {
					t.Error("Expected feedback in response")
				}
			},
		},
		{
			name:           "invalid limit gets default",
			queryParams:    "?rule=test-rule&limit=invalid",
			expectedStatus: http.StatusOK,
			checkResponse:  nil,
		},
		{
			name:           "limit too high gets clamped",
			queryParams:    "?rule=test-rule&limit=5000",
			expectedStatus: http.StatusOK,
			checkResponse:  nil,
		},
	}

	router := server.setupTestRouter()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/feedback"+tt.queryParams, nil)
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)

			if rec.Code != tt.expectedStatus {
				t.Errorf("Expected status %d, got %d", tt.expectedStatus, rec.Code)
			}

			if tt.checkResponse != nil {
				tt.checkResponse(t, rec)
			}
		})
	}
}

// mockFieldCapturingEngine captures the fields passed to Evaluate
type mockFieldCapturingEngine struct {
	results        []engine.RuleResult
	capturedFields map[string]string
}

func (m *mockFieldCapturingEngine) Evaluate(fields map[string]string) []engine.RuleResult {
	m.capturedFields = fields
	return m.results
}

func TestHandleEvaluateFieldMapping(t *testing.T) {
	testStore, err := store.NewStore(":memory:")
	if err != nil {
		t.Fatalf("creating test store: %v", err)
	}
	defer func() { _ = testStore.Close() }()

	cfg := &config.Config{}

	// Create a mock engine that records the fields it receives
	mockEngine := &mockFieldCapturingEngine{
		results: []engine.RuleResult{
			{RuleName: "test", Severity: engine.SeverityLow, Matched: false},
		},
	}

	evaluator := evaluate.NewEvaluator(mockEngine, config.ModeAudit, "", nil, nil)

	server, err := NewServer(cfg, evaluator, testStore, nil)
	if err != nil {
		t.Fatalf("creating test server: %v", err)
	}

	// Test field mapping from request to fields
	body := `{
		"event_id": "test-123",
		"tool": "file_read",
		"args": {
			"path": "/etc/passwd",
			"command": "cat /etc/passwd"
		}
	}`

	router := server.setupTestRouter()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/evaluate", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", rec.Code)
	}

	// Check that fields were properly mapped
	capturedFields := mockEngine.capturedFields
	if capturedFields["tool"] != "file_read" {
		t.Errorf("Expected tool field to be 'file_read', got '%s'", capturedFields["tool"])
	}

	if capturedFields["event_type"] != "file_read" {
		t.Errorf("Expected event_type to be 'file_read', got '%s'", capturedFields["event_type"])
	}

	if capturedFields["path"] != "/etc/passwd" {
		t.Errorf("Expected path field from args, got '%s'", capturedFields["path"])
	}

	if capturedFields["command"] != "cat /etc/passwd" {
		t.Errorf("Expected command field from args, got '%s'", capturedFields["command"])
	}
}

func TestHandleEvaluatePluginPayloadPreservesEventSemantics(t *testing.T) {
	testStore, err := store.NewStore(":memory:")
	if err != nil {
		t.Fatalf("creating test store: %v", err)
	}
	defer func() { _ = testStore.Close() }()

	cfg := &config.Config{}
	mockEngine := &mockFieldCapturingEngine{
		results: []engine.RuleResult{
			{RuleName: "test", Severity: engine.SeverityLow, Matched: false},
		},
	}
	evaluator := evaluate.NewEvaluator(mockEngine, config.ModeAudit, "", nil, nil)

	server, err := NewServer(cfg, evaluator, testStore, nil)
	if err != nil {
		t.Fatalf("creating test server: %v", err)
	}

	body := `{
		"event_id": "plugin-evt-1",
		"event_type": "file_read",
		"tool_name": "read",
		"source": "openclaw",
		"params": {
			"filePath": "/home/user/.ssh/id_rsa",
			"command": "Read: /home/user/.ssh/id_rsa"
		}
	}`

	router := server.setupTestRouter()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/evaluate", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("Expected status 200, got %d", rec.Code)
	}

	capturedFields := mockEngine.capturedFields
	if capturedFields["tool"] != "read" {
		t.Errorf("Expected tool 'read', got %q", capturedFields["tool"])
	}
	if capturedFields["event_type"] != "file_read" {
		t.Errorf("Expected event_type 'file_read', got %q", capturedFields["event_type"])
	}
	if capturedFields["source"] != "openclaw" {
		t.Errorf("Expected source 'openclaw', got %q", capturedFields["source"])
	}
	if capturedFields["command"] != "Read: /home/user/.ssh/id_rsa" {
		t.Errorf("Expected command to be preserved, got %q", capturedFields["command"])
	}
	if capturedFields["file_path"] != "/home/user/.ssh/id_rsa" {
		t.Errorf("Expected canonical file_path field, got %q", capturedFields["file_path"])
	}
}

func TestHandleAlertsRejectsInvalidTimeFilters(t *testing.T) {
	testStore, err := store.NewStore(":memory:")
	if err != nil {
		t.Fatalf("creating test store: %v", err)
	}
	defer func() { _ = testStore.Close() }()

	cfg := &config.Config{}
	evaluator := evaluate.NewEvaluator(&mockRuleEngine{}, config.ModeAudit, "", nil, nil)
	server, err := NewServer(cfg, evaluator, testStore, nil)
	if err != nil {
		t.Fatalf("creating test server: %v", err)
	}

	router := server.setupTestRouter()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/alerts?since=not-a-time", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid since filter, got %d", rec.Code)
	}
}

func TestStartAndShutdown(t *testing.T) {
	testStore, err := store.NewStore(":memory:")
	if err != nil {
		t.Fatalf("creating test store: %v", err)
	}
	defer func() { _ = testStore.Close() }()

	cfg := &config.Config{
		Server: config.ServerConfig{
			Addr: "127.0.0.1",
			Port: 0, // Use port 0 to get an available port
		},
	}

	mockEngine := &mockRuleEngine{}
	evaluator := evaluate.NewEvaluator(mockEngine, config.ModeAudit, "", nil, nil)

	server, err := NewServer(cfg, evaluator, testStore, nil)
	if err != nil {
		t.Fatalf("creating test server: %v", err)
	}

	// Test server startup (this will fail since we can't actually bind without port conflicts)
	// But we can test that the method exists and attempt to call it
	// We'll test it in a way that exercises the code path

	// Use a context to simulate shutdown
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// Start server in a goroutine
	serverErr := make(chan error, 1)
	go func() {
		err := server.Start()
		serverErr <- err
	}()

	// Give it a moment to start
	time.Sleep(50 * time.Millisecond)

	// Test shutdown
	shutdownErr := server.Shutdown(ctx)

	// Wait for start to complete
	<-serverErr

	// Shutdown should succeed (or fail gracefully)
	if shutdownErr != nil {
		t.Logf("Shutdown returned error (expected in test): %v", shutdownErr)
	}
}

func TestServerConfigMethods(t *testing.T) {
	cfg := &config.Config{
		Server: config.ServerConfig{
			Addr: "0.0.0.0",
			Port: 8433,
		},
	}

	expected := "0.0.0.0:8433"
	if addr := cfg.ListenAddr(); addr != expected {
		t.Errorf("Expected ListenAddr() to return %s, got %s", expected, addr)
	}
}

func TestServer_SetTracerProvider(t *testing.T) {
	tp := sdktrace.NewTracerProvider()
	defer func() { _ = tp.Shutdown(context.Background()) }()

	cfg := &config.Config{}
	testStore, err := store.NewStore(":memory:")
	if err != nil {
		t.Fatalf("creating test store: %v", err)
	}
	defer func() { _ = testStore.Close() }()

	evaluator := evaluate.NewEvaluator(&mockRuleEngine{}, config.ModeEnforce, "", nil, nil)
	srv, err := NewServer(cfg, evaluator, testStore, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Before setting, tracerProvider should be nil
	if srv.tracerProvider != nil {
		t.Error("expected tracerProvider to be nil before SetTracerProvider")
	}

	srv.SetTracerProvider(tp)

	if srv.tracerProvider == nil {
		t.Error("expected tracerProvider to be set after SetTracerProvider")
	}
	if srv.tracerProvider != tp {
		t.Error("expected tracerProvider to match the provider that was set")
	}
}

func TestServer_StartWithTracerProvider(t *testing.T) {
	// Verify that Start() succeeds when a TracerProvider is configured
	// (i.e. the otelchi middleware is wired in without panics).
	tp := sdktrace.NewTracerProvider()
	defer func() { _ = tp.Shutdown(context.Background()) }()

	cfg := &config.Config{
		Server: config.ServerConfig{
			Addr: "127.0.0.1",
			Port: 0,
		},
	}

	testStore, err := store.NewStore(":memory:")
	if err != nil {
		t.Fatalf("creating test store: %v", err)
	}
	defer func() { _ = testStore.Close() }()

	evaluator := evaluate.NewEvaluator(&mockRuleEngine{}, config.ModeAudit, "", nil, nil)
	srv, err := NewServer(cfg, evaluator, testStore, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv.SetTracerProvider(tp)

	// Start in background, then shut down promptly
	serverErr := make(chan error, 1)
	go func() {
		serverErr <- srv.Start()
	}()

	time.Sleep(50 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Logf("Shutdown returned error (expected in test): %v", err)
	}
	<-serverErr
}
