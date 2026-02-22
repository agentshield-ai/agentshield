package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// testAuthToken is a 32+ char token used throughout auth security tests.
const testAuthToken = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaXX" // 34 chars

// ---------------------------------------------------------------------------
// Auth boundary tests — token validation edge cases
// Guards C-2 (hook auth), C-4 (constant-time comparison).
// ---------------------------------------------------------------------------

// TestAuth_EmptyBearerToken verifies that "Authorization: Bearer " (empty
// token after the prefix) is rejected. This guards against split-string bugs
// where an empty token might match an empty stored token.
func TestAuth_EmptyBearerToken(t *testing.T) {
	m, err := NewMiddleware(testAuthToken)
	if err != nil {
		t.Fatalf("creating middleware: %v", err)
	}

	handler := m.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/evaluate", nil)
	req.Header.Set("Authorization", "Bearer ")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("empty Bearer token: expected 401, got %d", rec.Code)
	}
}

// TestAuth_WhitespaceOnlyToken verifies that a token consisting only of
// whitespace is rejected.
func TestAuth_WhitespaceOnlyToken(t *testing.T) {
	m, err := NewMiddleware(testAuthToken)
	if err != nil {
		t.Fatalf("creating middleware: %v", err)
	}

	handler := m.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/evaluate", nil)
	req.Header.Set("Authorization", "Bearer    ")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("whitespace-only Bearer token: expected 401, got %d", rec.Code)
	}
}

// TestAuth_NullByteInToken verifies that a token containing a null byte
// is rejected. Null bytes can cause string-comparison bypasses in some
// implementations that use C-style string handling.
func TestAuth_NullByteInToken(t *testing.T) {
	m, err := NewMiddleware(testAuthToken)
	if err != nil {
		t.Fatalf("creating middleware: %v", err)
	}

	// Validate directly — the HTTP header layer may strip null bytes
	// before they reach middleware, but we test at the validateToken level.
	tokenWithNull := testAuthToken[:5] + "\x00" + testAuthToken[5:]
	if m.validateToken(tokenWithNull) {
		t.Error("token with null byte should be rejected")
	}
}

// TestAuth_ExtremelyLongToken verifies that an extremely long token is
// rejected without causing excessive memory allocation or timeout.
// Guards against algorithmic complexity attacks on token comparison.
func TestAuth_ExtremelyLongToken(t *testing.T) {
	m, err := NewMiddleware(testAuthToken)
	if err != nil {
		t.Fatalf("creating middleware: %v", err)
	}

	// 1MB token — should not match and should not panic/OOM
	hugeToken := strings.Repeat("A", 1<<20)
	if m.validateToken(hugeToken) {
		t.Error("extremely long token should not match")
	}
}

// TestAuth_ConstantTimeComparison verifies that validateToken uses
// constant-time comparison (subtle.ConstantTimeCompare). We can't directly
// measure timing in a unit test, but we can verify the function rejects
// tokens that are the wrong length — which constant-time compare handles
// by comparing byte slices that may differ in length.
func TestAuth_ConstantTimeComparisonEdgeCases(t *testing.T) {
	m, err := NewMiddleware(testAuthToken)
	if err != nil {
		t.Fatalf("creating middleware: %v", err)
	}

	tests := []struct {
		name     string
		token    string
		expected bool
	}{
		{"correct token", testAuthToken, true},
		{"one char shorter", testAuthToken[:len(testAuthToken)-1], false},
		{"one char longer", testAuthToken + "X", false},
		{"correct prefix wrong suffix", testAuthToken[:len(testAuthToken)-3] + "ZZZ", false},
		{"empty", "", false},
		{"all zeros", strings.Repeat("0", len(testAuthToken)), false},
		{"same length all same char", strings.Repeat("a", len(testAuthToken)), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := m.validateToken(tt.token)
			if result != tt.expected {
				t.Errorf("validateToken(%q) = %v, want %v", tt.token, result, tt.expected)
			}
		})
	}
}

// TestAuth_MalformedBearerPrefix verifies that various malformed Authorization
// header formats are rejected. Guards against prefix-confusion attacks.
func TestAuth_MalformedBearerPrefix(t *testing.T) {
	m, err := NewMiddleware(testAuthToken)
	if err != nil {
		t.Fatalf("creating middleware: %v", err)
	}

	handler := m.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	tests := []struct {
		name       string
		authHeader string
	}{
		{"no Bearer prefix — raw token", testAuthToken},
		{"Token prefix instead of Bearer", "Token " + testAuthToken},
		{"Basic auth format", "Basic " + testAuthToken},
		{"lowercase bearer", "bearer " + testAuthToken},
		{"bearer without space", "Bearer" + testAuthToken},
		{"multiple Bearer", "Bearer Bearer " + testAuthToken},
		{"Bearer with extra spaces", "Bearer  " + testAuthToken},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/evaluate", nil)
			req.Header.Set("Authorization", tt.authHeader)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Errorf("%s: expected 401, got %d", tt.name, rec.Code)
			}
		})
	}
}

// TestAuth_MissingAuthHeader verifies that a request with no Authorization
// header is rejected with 401.
func TestAuth_MissingAuthHeader(t *testing.T) {
	m, err := NewMiddleware(testAuthToken)
	if err != nil {
		t.Fatalf("creating middleware: %v", err)
	}

	handler := m.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/evaluate", nil)
	// No auth header set
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("missing auth header: expected 401, got %d", rec.Code)
	}
}

// TestAuth_HealthEndpointBypassesAuth verifies that the health endpoint
// is explicitly excluded from authentication to allow monitoring without
// exposing credentials.
func TestAuth_HealthEndpointBypassesAuth(t *testing.T) {
	m, err := NewMiddleware(testAuthToken)
	if err != nil {
		t.Fatalf("creating middleware: %v", err)
	}

	handler := m.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))

	healthPaths := []string{"/health", "/api/v1/health"}
	for _, path := range healthPaths {
		t.Run("path_"+path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			// No auth header
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Errorf("health path %s should bypass auth, got %d", path, rec.Code)
			}
		})
	}
}

// TestAuth_NewMiddlewareRefusesEmptyToken verifies that creating auth
// middleware with an empty token returns an error, preventing silent
// fail-open behavior.
func TestAuth_NewMiddlewareRefusesEmptyToken(t *testing.T) {
	m, err := NewMiddleware("")
	if err == nil {
		t.Error("NewMiddleware with empty token should return error to prevent fail-open")
	}
	if m != nil {
		t.Error("NewMiddleware with empty token should return nil middleware")
	}
}

// TestAuth_CaseSensitivity verifies the token comparison is case-sensitive.
// A token "ABC" must not match "abc".
func TestAuth_CaseSensitivity(t *testing.T) {
	token := "MySecretToken-aaaaaaaaaaaaaaaaaXX"
	m, err := NewMiddleware(token)
	if err != nil {
		t.Fatalf("creating middleware: %v", err)
	}

	if m.validateToken(strings.ToLower(token)) {
		t.Error("token comparison should be case-sensitive: lowercase of token should not match")
	}
	if m.validateToken(strings.ToUpper(token)) {
		t.Error("token comparison should be case-sensitive: uppercase of token should not match")
	}
	if !m.validateToken(token) {
		t.Error("exact token should match")
	}
}
