package auth

import (
	"crypto/subtle"
	"fmt"
	"net/http"
	"strings"
)

// Middleware provides HTTP authentication middleware
type Middleware struct {
	token string
}

// NewMiddleware creates a new auth middleware
// SECURITY: Returns error if token is empty to prevent silent bypass
func NewMiddleware(token string) (*Middleware, error) {
	if token == "" {
		return nil, fmt.Errorf("auth token cannot be empty - refusing to create middleware that would allow all requests")
	}
	
	return &Middleware{
		token: token,
	}, nil
}

// Handler returns an HTTP handler that enforces authentication
func (m *Middleware) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Skip auth for health endpoint
		if r.URL.Path == "/health" || r.URL.Path == "/api/v1/health" {
			next.ServeHTTP(w, r)
			return
		}

		// Get authorization header
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			http.Error(w, "Authorization header required", http.StatusUnauthorized)
			return
		}

		// Check Bearer token format
		const bearerPrefix = "Bearer "
		if !strings.HasPrefix(authHeader, bearerPrefix) {
			http.Error(w, "Authorization header must use Bearer token", http.StatusUnauthorized)
			return
		}

		token := strings.TrimPrefix(authHeader, bearerPrefix)
		
		// Validate token using constant-time comparison to prevent timing attacks
		if !m.validateToken(token) {
			http.Error(w, "Invalid token", http.StatusUnauthorized)
			return
		}

		// Authentication successful, proceed
		next.ServeHTTP(w, r)
	})
}

// validateToken performs constant-time token comparison to prevent timing attacks
func (m *Middleware) validateToken(provided string) bool {
	// SECURITY: Use constant-time comparison to prevent timing attacks
	return subtle.ConstantTimeCompare([]byte(m.token), []byte(provided)) == 1
}

// ChiMiddleware provides Chi-style middleware function
func (m *Middleware) ChiMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Skip auth for health endpoint
		if r.URL.Path == "/health" || r.URL.Path == "/api/v1/health" {
			next.ServeHTTP(w, r)
			return
		}

		// Get authorization header
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			http.Error(w, "Authorization header required", http.StatusUnauthorized)
			return
		}

		// Check Bearer token format
		const bearerPrefix = "Bearer "
		if !strings.HasPrefix(authHeader, bearerPrefix) {
			http.Error(w, "Authorization header must use Bearer token", http.StatusUnauthorized)
			return
		}

		token := strings.TrimPrefix(authHeader, bearerPrefix)
		
		// Validate token using constant-time comparison to prevent timing attacks
		if !m.validateToken(token) {
			http.Error(w, "Invalid token", http.StatusUnauthorized)
			return
		}

		// Authentication successful, proceed
		next.ServeHTTP(w, r)
	})
}

// RequireAuth wraps a handler function with authentication
func (m *Middleware) RequireAuth(handlerFunc http.HandlerFunc) http.HandlerFunc {
	return m.Handler(http.HandlerFunc(handlerFunc)).ServeHTTP
}