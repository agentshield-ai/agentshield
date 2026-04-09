package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/agentshield-ai/agentshield/internal/config"
	"github.com/agentshield-ai/agentshield/internal/engine"
	"github.com/agentshield-ai/agentshield/internal/evaluate"
	"github.com/agentshield-ai/agentshield/internal/models"
	"github.com/agentshield-ai/agentshield/internal/store"
)

// Test validateStringInput function with all edge cases
func TestValidateStringInput(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		maxLength int
		fieldName string
		wantErr   bool
		errMsg    string
	}{
		{
			name:      "valid input",
			input:     "hello world",
			maxLength: 20,
			fieldName: "test_field",
			wantErr:   false,
		},
		{
			name:      "empty input is valid",
			input:     "",
			maxLength: 10,
			fieldName: "test_field",
			wantErr:   false,
		},
		{
			name:      "exceeds max length",
			input:     "this is a very long string that exceeds limit",
			maxLength: 10,
			fieldName: "test_field",
			wantErr:   true,
			errMsg:    "test_field exceeds maximum length of 10 characters",
		},
		{
			name:      "invalid UTF-8",
			input:     string([]byte{0xff, 0xfe, 0xfd}),
			maxLength: 100,
			fieldName: "test_field",
			wantErr:   true,
			errMsg:    "test_field contains invalid UTF-8 characters",
		},
		{
			name:      "control characters - null byte",
			input:     "hello\x00world",
			maxLength: 100,
			fieldName: "test_field",
			wantErr:   true,
			errMsg:    "test_field contains forbidden control characters",
		},
		{
			name:      "control characters - backspace",
			input:     "hello\x08world",
			maxLength: 100,
			fieldName: "test_field",
			wantErr:   true,
			errMsg:    "test_field contains forbidden control characters",
		},
		{
			name:      "control characters - ESC",
			input:     "hello\x1bworld",
			maxLength: 100,
			fieldName: "test_field",
			wantErr:   true,
			errMsg:    "test_field contains forbidden control characters",
		},
		{
			name:      "control characters - DEL",
			input:     "hello\x7fworld",
			maxLength: 100,
			fieldName: "test_field",
			wantErr:   true,
			errMsg:    "test_field contains forbidden control characters",
		},
		{
			name:      "newline allowed",
			input:     "hello\nworld",
			maxLength: 100,
			fieldName: "test_field",
			wantErr:   false,
		},
		{
			name:      "tab allowed",
			input:     "hello\tworld",
			maxLength: 100,
			fieldName: "test_field",
			wantErr:   false,
		},
		{
			name:      "script tag allowed (legitimate security tool content)",
			input:     "<script>alert('xss')</script>",
			maxLength: 100,
			fieldName: "test_field",
			wantErr:   false,
		},
		{
			name:      "script tag case insensitive allowed",
			input:     "<SCRIPT>alert('xss')</SCRIPT>",
			maxLength: 100,
			fieldName: "test_field",
			wantErr:   false,
		},
		{
			name:      "javascript protocol allowed",
			input:     "javascript:alert('xss')",
			maxLength: 100,
			fieldName: "test_field",
			wantErr:   false,
		},
		{
			name:      "vbscript protocol allowed",
			input:     "vbscript:msgbox('xss')",
			maxLength: 100,
			fieldName: "test_field",
			wantErr:   false,
		},
		{
			name:      "onload event handler allowed",
			input:     `<img onload="alert('xss')">`,
			maxLength: 100,
			fieldName: "test_field",
			wantErr:   false,
		},
		{
			name:      "onerror event handler allowed",
			input:     `<img onerror="alert('xss')">`,
			maxLength: 100,
			fieldName: "test_field",
			wantErr:   false,
		},
		{
			name:      "onclick event handler allowed",
			input:     `<div onclick="alert('xss')">`,
			maxLength: 100,
			fieldName: "test_field",
			wantErr:   false,
		},
		{
			name:      "onmouseover event handler allowed",
			input:     `<div onmouseover="alert('xss')">`,
			maxLength: 100,
			fieldName: "test_field",
			wantErr:   false,
		},
		{
			name:      "eval function allowed",
			input:     "eval('alert(1)')",
			maxLength: 100,
			fieldName: "test_field",
			wantErr:   false,
		},
		{
			name:      "document.cookie allowed",
			input:     "document.cookie",
			maxLength: 100,
			fieldName: "test_field",
			wantErr:   false,
		},
		{
			name:      "window.location allowed",
			input:     "window.location = 'http://evil.com'",
			maxLength: 100,
			fieldName: "test_field",
			wantErr:   false,
		},
		{
			name:      "unicode characters allowed",
			input:     "Hello 🌍 World",
			maxLength: 100,
			fieldName: "test_field",
			wantErr:   false,
		},
		{
			name:      "mixed case injection attempt allowed",
			input:     "OnClIcK=alert(1)",
			maxLength: 100,
			fieldName: "test_field",
			wantErr:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateStringInput(tt.input, tt.maxLength, tt.fieldName)

			if tt.wantErr {
				if err == nil {
					t.Errorf("validateStringInput() expected error, got nil")
					return
				}
				if !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("validateStringInput() error = %v, expected to contain %v", err.Error(), tt.errMsg)
				}
			} else {
				if err != nil {
					t.Errorf("validateStringInput() unexpected error = %v", err)
				}
			}
		})
	}
}

// Test validateEvaluationRequest with comprehensive edge cases
func TestValidateEvaluationRequest(t *testing.T) {
	tests := []struct {
		name    string
		req     *models.EvaluationRequest
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid request",
			req: &models.EvaluationRequest{
				EventID:   "event-123",
				SessionID: "session-456",
				Tool:      "test_tool",
				Args:      map[string]string{"arg1": "value1"},
				Fields:    map[string]string{"field1": "value1"},
			},
			wantErr: false,
		},
		{
			name: "missing event_id",
			req: &models.EvaluationRequest{
				EventID: "",
				Tool:    "test_tool",
			},
			wantErr: true,
			errMsg:  "event_id is required",
		},
		{
			name: "event_id too long",
			req: &models.EvaluationRequest{
				EventID: strings.Repeat("a", 300), // 300 chars, limit is 256
				Tool:    "test_tool",
			},
			wantErr: true,
			errMsg:  "event_id exceeds maximum length of 256 characters",
		},
		{
			name: "event_id with control characters",
			req: &models.EvaluationRequest{
				EventID: "event\x00123",
				Tool:    "test_tool",
			},
			wantErr: true,
			errMsg:  "event_id contains forbidden control characters",
		},
		{
			name: "session_id too long",
			req: &models.EvaluationRequest{
				EventID:   "event-123",
				SessionID: strings.Repeat("s", 300), // 300 chars, limit is 256
			},
			wantErr: true,
			errMsg:  "session_id exceeds maximum length of 256 characters",
		},
		{
			name: "session_id with script content allowed",
			req: &models.EvaluationRequest{
				EventID:   "event-123",
				SessionID: "<script>alert('xss')</script>",
			},
			wantErr: false,
		},
		{
			name: "tool too long",
			req: &models.EvaluationRequest{
				EventID: "event-123",
				Tool:    strings.Repeat("t", 150), // 150 chars, limit is 100
			},
			wantErr: true,
			errMsg:  "tool exceeds maximum length of 100 characters",
		},
		{
			name: "tool with eval content allowed",
			req: &models.EvaluationRequest{
				EventID: "event-123",
				Tool:    "eval(alert(1))",
			},
			wantErr: false,
		},
		{
			name: "too many args fields",
			req: &models.EvaluationRequest{
				EventID: "event-123",
				Args:    generateLargeMap(150), // 150 fields, limit is 100
			},
			wantErr: true,
			errMsg:  "too many args fields (max 100)",
		},
		{
			name: "args key too long",
			req: &models.EvaluationRequest{
				EventID: "event-123",
				Args: map[string]string{
					strings.Repeat("k", 150): "value", // 150 chars, limit is 100
				},
			},
			wantErr: true,
			errMsg:  "args key exceeds maximum length of 100 characters",
		},
		{
			name: "args key with javascript content allowed",
			req: &models.EvaluationRequest{
				EventID: "event-123",
				Args: map[string]string{
					"javascript:alert(1)": "value",
				},
			},
			wantErr: false,
		},
		{
			name: "args value too long",
			req: &models.EvaluationRequest{
				EventID: "event-123",
				Args: map[string]string{
					"key": strings.Repeat("v", 15000), // 15KB, limit is 10KB
				},
			},
			wantErr: true,
			errMsg:  "args value exceeds maximum length of 10000 characters",
		},
		{
			name: "args value with script content allowed",
			req: &models.EvaluationRequest{
				EventID: "event-123",
				Args: map[string]string{
					"key": "<script>document.cookie='stolen'</script>",
				},
			},
			wantErr: false,
		},
		{
			name: "too many fields",
			req: &models.EvaluationRequest{
				EventID: "event-123",
				Fields:  generateLargeMap(150), // 150 fields, limit is 100
			},
			wantErr: true,
			errMsg:  "too many fields (max 100)",
		},
		{
			name: "fields key too long",
			req: &models.EvaluationRequest{
				EventID: "event-123",
				Fields: map[string]string{
					strings.Repeat("k", 150): "value", // 150 chars, limit is 100
				},
			},
			wantErr: true,
			errMsg:  "fields key exceeds maximum length of 100 characters",
		},
		{
			name: "fields key with onload content allowed",
			req: &models.EvaluationRequest{
				EventID: "event-123",
				Fields: map[string]string{
					"onload=alert(1)": "value",
				},
			},
			wantErr: false,
		},
		{
			name: "fields value too long",
			req: &models.EvaluationRequest{
				EventID: "event-123",
				Fields: map[string]string{
					"key": strings.Repeat("v", 15000), // 15KB, limit is 10KB
				},
			},
			wantErr: true,
			errMsg:  "fields value exceeds maximum length of 10000 characters",
		},
		{
			name: "fields value with window.location allowed",
			req: &models.EvaluationRequest{
				EventID: "event-123",
				Fields: map[string]string{
					"key": "window.location='http://evil.com'",
				},
			},
			wantErr: false,
		},
		{
			name: "nil args is allowed",
			req: &models.EvaluationRequest{
				EventID: "event-123",
				Args:    nil,
			},
			wantErr: false,
		},
		{
			name: "nil fields is allowed",
			req: &models.EvaluationRequest{
				EventID: "event-123",
				Fields:  nil,
			},
			wantErr: false,
		},
		{
			name: "empty string values are allowed",
			req: &models.EvaluationRequest{
				EventID:   "event-123",
				SessionID: "", // Empty but not required
				Tool:      "", // Empty but not required
				Args:      map[string]string{"key": ""},
				Fields:    map[string]string{"key": ""},
			},
			wantErr: false,
		},
		{
			name: "unicode in all fields",
			req: &models.EvaluationRequest{
				EventID:   "événement-123-🚀",
				SessionID: "session-🌍",
				Tool:      "outil-🔧",
				Args:      map[string]string{"clé": "valeur-🎯"},
				Fields:    map[string]string{"champ": "données-📊"},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateEvaluationRequest(tt.req)

			if tt.wantErr {
				if err == nil {
					t.Errorf("validateEvaluationRequest() expected error, got nil")
					return
				}
				if !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("validateEvaluationRequest() error = %v, expected to contain %v", err.Error(), tt.errMsg)
				}
			} else {
				if err != nil {
					t.Errorf("validateEvaluationRequest() unexpected error = %v", err)
				}
			}
		})
	}
}

// Helper function to generate a large map for testing
func generateLargeMap(size int) map[string]string {
	m := make(map[string]string)
	for i := 0; i < size; i++ {
		m[fmt.Sprintf("key%d", i)] = fmt.Sprintf("value%d", i)
	}
	return m
}

// Test NewServer with various configurations
func TestNewServerEdgeCases(t *testing.T) {
	t.Run("with auth token", func(t *testing.T) {
		cfg := &config.Config{
			Auth: config.AuthConfig{
				Token: "test-token-12345678901234567890123456",
			},
		}

		mockEngine := &mockRuleEngine{}
		evaluator := evaluate.NewEvaluator(mockEngine, config.ModeEnforce, "", nil, nil)
		testStore, _ := store.NewStore(":memory:")

		server, err := NewServer(cfg, evaluator, testStore, nil)
		if err != nil {
			t.Fatalf("NewServer() error = %v", err)
		}

		if server.auth == nil {
			t.Error("Expected auth middleware to be created")
		}
	})

	t.Run("with empty auth token", func(t *testing.T) {
		cfg := &config.Config{
			Auth: config.AuthConfig{
				Token: "", // Empty token should work (no auth middleware created)
			},
		}

		mockEngine := &mockRuleEngine{}
		evaluator := evaluate.NewEvaluator(mockEngine, config.ModeEnforce, "", nil, nil)
		testStore, _ := store.NewStore(":memory:")

		server, err := NewServer(cfg, evaluator, testStore, nil)
		if err != nil {
			t.Fatalf("NewServer() unexpected error = %v", err)
		}

		if server.auth != nil {
			t.Error("Expected no auth middleware when token is empty")
		}
	})
}

// Test handleAlerts with extensive edge cases
func TestHandleAlertsEdgeCases(t *testing.T) {
	// Setup test server
	cfg := &config.Config{
		Rules: config.RulesConfig{Dir: "./rules"},
	}

	mockEngine := &mockRuleEngine{}
	evaluator := evaluate.NewEvaluator(mockEngine, config.ModeEnforce, "", nil, nil)
	testStore, _ := store.NewStore(":memory:")
	defer testStore.Close()

	server, _ := NewServer(cfg, evaluator, testStore, nil)

	// Insert test alerts
	testAlerts := []*store.Alert{
		{
			RuleName:    "test-rule-1",
			Severity:    "high",
			Tool:        "test_tool",
			Args:        `{"arg":"value"}`,
			ActionTaken: "block",
			Timestamp:   time.Now(),
			SessionID:   "session-1",
			EventID:     "event-1",
		},
		{
			RuleName:    "test-rule-2",
			Severity:    "critical",
			Tool:        "dangerous_tool",
			Args:        `{"path":"/etc/passwd"}`,
			ActionTaken: "block",
			Timestamp:   time.Now().Add(-time.Hour),
			SessionID:   "session-2",
			EventID:     "event-2",
		},
	}

	for _, alert := range testAlerts {
		if err := testStore.InsertAlert(alert); err != nil {
			t.Fatalf("Failed to insert test alert: %v", err)
		}
	}

	tests := []struct {
		name           string
		queryParams    string
		expectedStatus int
		expectedError  string
	}{
		{
			name:           "default query",
			queryParams:    "",
			expectedStatus: http.StatusOK,
		},
		{
			name:           "with limit",
			queryParams:    "limit=10",
			expectedStatus: http.StatusOK,
		},
		{
			name:           "with offset",
			queryParams:    "offset=1",
			expectedStatus: http.StatusOK,
		},
		{
			name:           "with since timestamp",
			queryParams:    "since=" + url.QueryEscape(time.Now().Add(-2*time.Hour).Format(time.RFC3339)),
			expectedStatus: http.StatusOK,
		},
		{
			name:           "with until timestamp",
			queryParams:    "until=" + url.QueryEscape(time.Now().Format(time.RFC3339)),
			expectedStatus: http.StatusOK,
		},
		{
			name:           "with severity filter",
			queryParams:    "severity=high",
			expectedStatus: http.StatusOK,
		},
		{
			name:           "with rule filter",
			queryParams:    "rule=test-rule-1",
			expectedStatus: http.StatusOK,
		},
		{
			name:           "with session_id filter",
			queryParams:    "session_id=session-1",
			expectedStatus: http.StatusOK,
		},
		{
			name:           "invalid severity",
			queryParams:    "severity=invalid",
			expectedStatus: http.StatusBadRequest,
			expectedError:  "Invalid severity",
		},
		{
			name:           "invalid limit (negative)",
			queryParams:    "limit=-1",
			expectedStatus: http.StatusOK, // Uses default limit
		},
		{
			name:           "invalid limit (too large)",
			queryParams:    "limit=2000",
			expectedStatus: http.StatusOK, // Uses default limit
		},
		{
			name:           "invalid offset (negative)",
			queryParams:    "offset=-1",
			expectedStatus: http.StatusOK, // Uses default offset
		},
		{
			name:           "invalid since timestamp",
			queryParams:    "since=invalid-timestamp",
			expectedStatus: http.StatusBadRequest,
			expectedError:  "Invalid since parameter",
		},
		{
			name:           "invalid until timestamp",
			queryParams:    "until=not-a-timestamp",
			expectedStatus: http.StatusBadRequest,
			expectedError:  "Invalid until parameter",
		},
		{
			name:           "rule filter with script content allowed",
			queryParams:    "rule=" + url.QueryEscape("<script>alert('xss')</script>"),
			expectedStatus: http.StatusOK,
			expectedError:  "",
		},
		{
			name:           "rule filter too long",
			queryParams:    "rule=" + url.QueryEscape(strings.Repeat("a", 250)),
			expectedStatus: http.StatusBadRequest,
			expectedError:  "Invalid rule parameter",
		},
		{
			name:           "session_id filter with javascript content allowed",
			queryParams:    "session_id=" + url.QueryEscape("javascript:alert(1)"),
			expectedStatus: http.StatusOK,
			expectedError:  "",
		},
		{
			name:           "session_id filter too long",
			queryParams:    "session_id=" + url.QueryEscape(strings.Repeat("s", 300)),
			expectedStatus: http.StatusBadRequest,
			expectedError:  "Invalid session_id parameter",
		},
		{
			name:           "multiple valid filters",
			queryParams:    "severity=high&limit=5&offset=0&rule=test-rule-1",
			expectedStatus: http.StatusOK,
		},
		{
			name:           "limit boundary values",
			queryParams:    "limit=1000", // Max allowed
			expectedStatus: http.StatusOK,
		},
		{
			name:           "limit over boundary",
			queryParams:    "limit=1001", // Over max, should use default
			expectedStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			url := "/api/v1/alerts"
			if tt.queryParams != "" {
				url += "?" + tt.queryParams
			}

			req := httptest.NewRequest(http.MethodGet, url, nil)
			w := httptest.NewRecorder()

			server.handleAlerts(w, req)

			if w.Code != tt.expectedStatus {
				t.Errorf("handleAlerts() status = %d, want %d", w.Code, tt.expectedStatus)
			}

			if tt.expectedError != "" {
				if !strings.Contains(w.Body.String(), tt.expectedError) {
					t.Errorf("handleAlerts() body = %v, want to contain %v", w.Body.String(), tt.expectedError)
				}
			} else if w.Code == http.StatusOK {
				// Verify JSON structure for successful responses
				var response map[string]interface{}
				if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
					t.Errorf("handleAlerts() invalid JSON response: %v", err)
				}

				// Check required fields
				if _, ok := response["alerts"]; !ok {
					t.Error("handleAlerts() missing 'alerts' field in response")
				}
				if _, ok := response["total_count"]; !ok {
					t.Error("handleAlerts() missing 'total_count' field in response")
				}
			}
		})
	}
}

// Test handleEvaluate with adversarial inputs
func TestHandleEvaluateAdversarialInputs(t *testing.T) {
	// Setup test server
	cfg := &config.Config{
		Rules: config.RulesConfig{Dir: "./rules"},
	}

	mockEngine := &mockRuleEngine{
		results: []engine.RuleResult{
			{
				RuleID:   "test-rule",
				RuleName: "Test Rule",
				Severity: engine.SeverityHigh,
				Matched:  true,
			},
		},
	}
	evaluator := evaluate.NewEvaluator(mockEngine, config.ModeEnforce, "", nil, nil)
	testStore, _ := store.NewStore(":memory:")
	defer testStore.Close()

	server, _ := NewServer(cfg, evaluator, testStore, nil)

	t.Run("request body too large", func(t *testing.T) {
		// Create a JSON request body larger than MaxRequestBodySize (1MB)
		payload := models.EvaluationRequest{
			EventID: "event-123",
			Tool:    "test_tool",
			Fields: map[string]string{
				"large_field": strings.Repeat("a", MaxRequestBodySize), // This makes the JSON > 1MB
			},
		}

		body, _ := json.Marshal(payload)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/evaluate", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		server.handleEvaluate(w, req)

		if w.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("handleEvaluate() status = %d, want %d", w.Code, http.StatusRequestEntityTooLarge)
		}

		if !strings.Contains(w.Body.String(), "Request body too large") {
			t.Errorf("handleEvaluate() body should contain 'Request body too large', got: %s", w.Body.String())
		}
	})

	t.Run("invalid JSON", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/evaluate", strings.NewReader("invalid json {"))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		server.handleEvaluate(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("handleEvaluate() status = %d, want %d", w.Code, http.StatusBadRequest)
		}

		if !strings.Contains(w.Body.String(), "Invalid request format") {
			t.Errorf("handleEvaluate() body should contain 'Invalid request format'")
		}
	})

	t.Run("script payload allowed", func(t *testing.T) {
		payload := models.EvaluationRequest{
			EventID: "event-123",
			Tool:    "<script>alert('xss')</script>",
			Args: map[string]string{
				"malicious": "javascript:alert(document.cookie)",
			},
		}

		body, _ := json.Marshal(payload)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/evaluate", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		server.handleEvaluate(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("handleEvaluate() status = %d, want %d", w.Code, http.StatusOK)
		}
	})

	t.Run("field building from args", func(t *testing.T) {
		payload := models.EvaluationRequest{
			EventID: "event-123",
			Tool:    "test_tool",
			Args: map[string]string{
				"command": "ls -la",
				"path":    "/tmp",
			},
			Fields: nil, // Will be auto-built from other fields
		}

		body, _ := json.Marshal(payload)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/evaluate", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		server.handleEvaluate(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("handleEvaluate() status = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
		}

		// Verify the response contains expected alerts
		var response evaluate.EvaluationResponse
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatalf("Failed to unmarshal response: %v", err)
		}

		if len(response.Alerts) == 0 {
			t.Error("Expected alerts to be present in response")
		}
	})
}

// Test concurrent requests to evaluate endpoint
func TestHandleEvaluateConcurrency(t *testing.T) {
	// Setup test server
	cfg := &config.Config{
		Rules: config.RulesConfig{Dir: "./rules"},
	}

	mockEngine := &mockRuleEngine{
		results: []engine.RuleResult{
			{RuleID: "test", Severity: engine.SeverityLow, Matched: true},
		},
	}
	evaluator := evaluate.NewEvaluator(mockEngine, config.ModeEnforce, "", nil, nil)
	testStore, _ := store.NewStore(":memory:")
	defer testStore.Close()

	server, _ := NewServer(cfg, evaluator, testStore, nil)

	// Run concurrent requests
	const numRequests = 50
	results := make(chan int, numRequests)

	for i := 0; i < numRequests; i++ {
		go func(id int) {
			payload := models.EvaluationRequest{
				EventID: fmt.Sprintf("concurrent-event-%d", id),
				Tool:    "concurrent_tool",
				Fields:  map[string]string{"test": "concurrent"},
			}

			body, _ := json.Marshal(payload)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/evaluate", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			server.handleEvaluate(w, req)
			results <- w.Code
		}(i)
	}

	// Check all requests succeeded
	for i := 0; i < numRequests; i++ {
		statusCode := <-results
		if statusCode != http.StatusOK {
			t.Errorf("Concurrent request %d failed with status %d", i, statusCode)
		}
	}
}

// Test feedback handling with edge cases
func TestHandleFeedbackEdgeCases(t *testing.T) {
	// Setup test server
	cfg := &config.Config{
		Rules: config.RulesConfig{Dir: "./rules"},
	}

	mockEngine := &mockRuleEngine{}
	evaluator := evaluate.NewEvaluator(mockEngine, config.ModeEnforce, "", nil, nil)
	testStore, _ := store.NewStore(":memory:")
	defer testStore.Close()

	// Insert a test alert for feedback tests
	testAlert := &store.Alert{
		RuleName:    "test-rule",
		EventID:     "event-123",
		SessionID:   "test-session",
		Severity:    "high",
		Tool:        "test_tool",
		Args:        `{"test":"value"}`,
		ActionTaken: "log",
		Timestamp:   time.Now().Add(-time.Hour),
	}
	if err := testStore.InsertAlert(testAlert); err != nil {
		t.Fatalf("Failed to insert test alert: %v", err)
	}

	server, _ := NewServer(cfg, evaluator, testStore, nil)

	t.Run("invalid feedback type", func(t *testing.T) {
		feedback := FeedbackRequest{
			EventID:      "event-123",
			FeedbackType: "invalid_type",
			Comment:      "test comment",
		}

		body, _ := json.Marshal(feedback)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/feedback", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		server.handleFeedbackSubmission(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("handleFeedbackSubmission() status = %d, want %d", w.Code, http.StatusBadRequest)
		}

		if !strings.Contains(w.Body.String(), "Invalid feedback_type") {
			t.Errorf("Expected 'Invalid feedback_type' error")
		}
	})

	t.Run("comment too long", func(t *testing.T) {
		feedback := FeedbackRequest{
			EventID:      "event-123",
			FeedbackType: "false_positive",
			Comment:      strings.Repeat("a", 2500), // 2500 chars, limit is 2000
		}

		body, _ := json.Marshal(feedback)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/feedback", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		server.handleFeedbackSubmission(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("handleFeedbackSubmission() status = %d, want %d", w.Code, http.StatusBadRequest)
		}

		if !strings.Contains(w.Body.String(), "Invalid comment") {
			t.Errorf("Expected 'Invalid comment' error")
		}
	})

	t.Run("script comment allowed", func(t *testing.T) {
		alertID := int64(1)
		feedback := FeedbackRequest{
			EventID:      "event-123",
			AlertID:      &alertID,
			FeedbackType: "false_positive", // Changed from "improvement" which is invalid
			Comment:      "<script>alert('xss')</script>",
		}

		body, _ := json.Marshal(feedback)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/feedback", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		server.handleFeedbackSubmission(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("handleFeedbackSubmission() status = %d, want %d", w.Code, http.StatusOK)
		}
	})

	t.Run("feedback query missing rule parameter", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/feedback", nil)
		w := httptest.NewRecorder()

		server.handleFeedbackQuery(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("handleFeedbackQuery() status = %d, want %d", w.Code, http.StatusBadRequest)
		}

		if !strings.Contains(w.Body.String(), "rule parameter is required") {
			t.Errorf("Expected 'rule parameter is required' error")
		}
	})

	t.Run("feedback query with valid parameters", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/feedback?rule=test-rule&limit=10", nil)
		w := httptest.NewRecorder()

		server.handleFeedbackQuery(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("handleFeedbackQuery() status = %d, want %d", w.Code, http.StatusOK)
		}

		// Verify JSON structure
		var response map[string]interface{}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Errorf("handleFeedbackQuery() invalid JSON response: %v", err)
		}

		expectedFields := []string{"rule_name", "feedback", "false_positive_rate", "total_feedback"}
		for _, field := range expectedFields {
			if _, ok := response[field]; !ok {
				t.Errorf("handleFeedbackQuery() missing field: %s", field)
			}
		}
	})
}
