package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewMiddleware(t *testing.T) {
	tests := []struct {
		name        string
		token       string
		expectError bool
	}{
		{
			name:        "valid token",
			token:       "test-token-123",
			expectError: false,
		},
		{
			name:        "empty token should fail",
			token:       "",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			middleware, err := NewMiddleware(tt.token)

			if tt.expectError {
				if err == nil {
					t.Errorf("expected error for empty token, got nil")
				}
				if middleware != nil {
					t.Errorf("expected nil middleware for empty token, got %+v", middleware)
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			if middleware == nil {
				t.Errorf("expected middleware instance, got nil")
			}
		})
	}
}

func TestHandler(t *testing.T) {
	testToken := "test-secret-token"
	middleware, err := NewMiddleware(testToken)
	if err != nil {
		t.Fatalf("creating middleware: %v", err)
	}

	// Mock handler that should only be called when auth succeeds
	mockHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("success"))
	})

	wrappedHandler := middleware.Handler(mockHandler)

	tests := []struct {
		name           string
		path           string
		authHeader     string
		expectedStatus int
		expectedBody   string
	}{
		{
			name:           "health endpoint bypasses auth",
			path:           "/health",
			authHeader:     "",
			expectedStatus: http.StatusOK,
			expectedBody:   "success",
		},
		{
			name:           "api health endpoint bypasses auth",
			path:           "/api/v1/health",
			authHeader:     "",
			expectedStatus: http.StatusOK,
			expectedBody:   "success",
		},
		{
			name:           "missing auth header",
			path:           "/api/v1/evaluate",
			authHeader:     "",
			expectedStatus: http.StatusUnauthorized,
		},
		{
			name:           "invalid auth format",
			path:           "/api/v1/evaluate",
			authHeader:     "InvalidFormat token123",
			expectedStatus: http.StatusUnauthorized,
		},
		{
			name:           "valid auth token",
			path:           "/api/v1/evaluate",
			authHeader:     "Bearer " + testToken,
			expectedStatus: http.StatusOK,
			expectedBody:   "success",
		},
		{
			name:           "invalid auth token",
			path:           "/api/v1/evaluate",
			authHeader:     "Bearer wrong-token",
			expectedStatus: http.StatusUnauthorized,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tt.path, nil)
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}

			rr := httptest.NewRecorder()
			wrappedHandler.ServeHTTP(rr, req)

			if rr.Code != tt.expectedStatus {
				t.Errorf("expected status %d, got %d", tt.expectedStatus, rr.Code)
			}

			if tt.expectedBody != "" && rr.Body.String() != tt.expectedBody {
				t.Errorf("expected body %q, got %q", tt.expectedBody, rr.Body.String())
			}
		})
	}
}

func TestValidateToken(t *testing.T) {
	testToken := "secret-token-123"
	middleware, err := NewMiddleware(testToken)
	if err != nil {
		t.Fatalf("creating middleware: %v", err)
	}

	tests := []struct {
		name     string
		provided string
		expected bool
	}{
		{
			name:     "correct token",
			provided: testToken,
			expected: true,
		},
		{
			name:     "incorrect token",
			provided: "wrong-token",
			expected: false,
		},
		{
			name:     "empty token",
			provided: "",
			expected: false,
		},
		{
			name:     "similar but different token",
			provided: testToken + "x",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := middleware.validateToken(tt.provided)
			if result != tt.expected {
				t.Errorf("validateToken(%q) = %v, want %v", tt.provided, result, tt.expected)
			}
		})
	}
}

func TestRequireAuth(t *testing.T) {
	testToken := "test-token"
	middleware, err := NewMiddleware(testToken)
	if err != nil {
		t.Fatalf("creating middleware: %v", err)
	}

	handlerFunc := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("authenticated"))
	}

	wrappedFunc := middleware.RequireAuth(handlerFunc)

	// Test with valid auth
	req := httptest.NewRequest(http.MethodPost, "/test", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()

	wrappedFunc(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	if rr.Body.String() != "authenticated" {
		t.Errorf("expected body 'authenticated', got %q", rr.Body.String())
	}

	// Test with invalid auth
	req = httptest.NewRequest(http.MethodPost, "/test", nil)
	rr = httptest.NewRecorder()

	wrappedFunc(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected status 401, got %d", rr.Code)
	}
}

func TestChiMiddleware(t *testing.T) {
	testToken := "test-secret-token"
	middleware, err := NewMiddleware(testToken)
	if err != nil {
		t.Fatalf("creating middleware: %v", err)
	}

	// Mock handler that should only be called when auth succeeds
	mockHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("success"))
	})

	// Create Chi-style middleware
	chiHandler := middleware.ChiMiddleware(mockHandler)

	tests := []struct {
		name           string
		path           string
		authHeader     string
		expectedStatus int
		expectedBody   string
	}{
		{
			name:           "health endpoint bypasses auth",
			path:           "/health",
			authHeader:     "",
			expectedStatus: http.StatusOK,
			expectedBody:   "success",
		},
		{
			name:           "api health endpoint bypasses auth",
			path:           "/api/v1/health",
			authHeader:     "",
			expectedStatus: http.StatusOK,
			expectedBody:   "success",
		},
		{
			name:           "missing auth header",
			path:           "/api/v1/evaluate",
			authHeader:     "",
			expectedStatus: http.StatusUnauthorized,
		},
		{
			name:           "invalid auth format - no Bearer prefix",
			path:           "/api/v1/evaluate",
			authHeader:     "Token " + testToken,
			expectedStatus: http.StatusUnauthorized,
		},
		{
			name:           "invalid auth format - just token",
			path:           "/api/v1/evaluate",
			authHeader:     testToken,
			expectedStatus: http.StatusUnauthorized,
		},
		{
			name:           "valid auth token",
			path:           "/api/v1/evaluate",
			authHeader:     "Bearer " + testToken,
			expectedStatus: http.StatusOK,
			expectedBody:   "success",
		},
		{
			name:           "invalid auth token",
			path:           "/api/v1/evaluate",
			authHeader:     "Bearer wrong-token",
			expectedStatus: http.StatusUnauthorized,
		},
		{
			name:           "empty token after Bearer",
			path:           "/api/v1/evaluate",
			authHeader:     "Bearer ",
			expectedStatus: http.StatusUnauthorized,
		},
		{
			name:           "different endpoint requires auth",
			path:           "/api/v1/alerts",
			authHeader:     "Bearer " + testToken,
			expectedStatus: http.StatusOK,
			expectedBody:   "success",
		},
		{
			name:           "root health endpoint bypasses auth",
			path:           "/health",
			authHeader:     "",
			expectedStatus: http.StatusOK,
			expectedBody:   "success",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tt.path, nil)
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}

			rr := httptest.NewRecorder()
			chiHandler.ServeHTTP(rr, req)

			if rr.Code != tt.expectedStatus {
				t.Errorf("expected status %d, got %d", tt.expectedStatus, rr.Code)
			}

			if tt.expectedBody != "" && rr.Body.String() != tt.expectedBody {
				t.Errorf("expected body %q, got %q", tt.expectedBody, rr.Body.String())
			}
		})
	}
}

func TestChiMiddlewareHealthEndpointVariations(t *testing.T) {
	testToken := "test-token"
	middleware, err := NewMiddleware(testToken)
	if err != nil {
		t.Fatalf("creating middleware: %v", err)
	}

	mockHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	chiHandler := middleware.ChiMiddleware(mockHandler)

	// Test various health endpoint paths that should bypass auth
	healthPaths := []string{
		"/health",
		"/api/v1/health",
	}

	for _, path := range healthPaths {
		t.Run("health_path_"+path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			// Intentionally no auth header

			rr := httptest.NewRecorder()
			chiHandler.ServeHTTP(rr, req)

			if rr.Code != http.StatusOK {
				t.Errorf("Health endpoint %s should bypass auth, got status %d", path, rr.Code)
			}

			if rr.Body.String() != "ok" {
				t.Errorf("Expected body 'ok', got %q", rr.Body.String())
			}
		})
	}
}
