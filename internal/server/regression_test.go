package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"golang.org/x/time/rate"
)

// TestRateLimiterIPv6Extraction verifies that the rate limiter correctly
// parses IPv6 addresses using net.SplitHostPort instead of a naive
// strings.LastIndex approach that breaks on IPv6 "[::1]:8080" format.
func TestRateLimiterIPv6Extraction(t *testing.T) {
	tests := []struct {
		name       string
		remoteAddr string
		wantIP     string
	}{
		{"IPv4 with port", "192.168.1.1:12345", "192.168.1.1"},
		{"IPv6 with port", "[::1]:8080", "::1"},
		{"IPv6 full with port", "[2001:db8::1]:443", "2001:db8::1"},
		{"IPv4 no port", "10.0.0.1", "10.0.0.1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Use a fresh limiter so previous test IPs don't interfere
			freshRL := newIPRateLimiter(rate.Limit(100), 100)
			h := rateLimitMiddleware(freshRL)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))

			req := httptest.NewRequest("GET", "/test", nil)
			req.RemoteAddr = tt.remoteAddr
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Errorf("expected 200, got %d for RemoteAddr=%q", rec.Code, tt.remoteAddr)
			}

			// Verify the limiter was keyed by the extracted IP
			freshRL.mu.Lock()
			_, exists := freshRL.visitors[tt.wantIP]
			freshRL.mu.Unlock()
			if !exists {
				freshRL.mu.Lock()
				keys := make([]string, 0, len(freshRL.visitors))
				for k := range freshRL.visitors {
					keys = append(keys, k)
				}
				freshRL.mu.Unlock()
				t.Errorf("expected limiter keyed by %q, got keys: %v", tt.wantIP, keys)
			}
		})
	}
}

// TestRateLimiterBurst verifies the new x/time/rate based limiter
// correctly enforces burst limits.
func TestRateLimiterBurst(t *testing.T) {
	// Allow 1 request per second, burst of 2
	rl := newIPRateLimiter(rate.Limit(1), 2)

	handler := rateLimitMiddleware(rl)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	ip := "10.0.0.1:1234"

	// First 2 requests should succeed (burst)
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest("GET", "/test", nil)
		req.RemoteAddr = ip
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("request %d: expected 200, got %d", i, rec.Code)
		}
	}

	// Third request should be rate limited
	req := httptest.NewRequest("GET", "/test", nil)
	req.RemoteAddr = ip
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("expected 429 after burst, got %d", rec.Code)
	}
}

// TestRateLimiterXRealIP verifies that X-Real-Ip header is used
// for rate limiting when present (reverse proxy scenario).
func TestRateLimiterXRealIP(t *testing.T) {
	rl := newIPRateLimiter(rate.Limit(1), 1)

	handler := rateLimitMiddleware(rl)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// First request with X-Real-Ip
	req := httptest.NewRequest("GET", "/test", nil)
	req.RemoteAddr = "10.0.0.1:1234"
	req.Header.Set("X-Real-Ip", "203.0.113.1")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("first request: expected 200, got %d", rec.Code)
	}

	// Second request same X-Real-Ip should be limited
	req = httptest.NewRequest("GET", "/test", nil)
	req.RemoteAddr = "10.0.0.1:1234"
	req.Header.Set("X-Real-Ip", "203.0.113.1")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("second request same real IP: expected 429, got %d", rec.Code)
	}

	// Third request different X-Real-Ip should succeed
	req = httptest.NewRequest("GET", "/test", nil)
	req.RemoteAddr = "10.0.0.1:1234"
	req.Header.Set("X-Real-Ip", "203.0.113.2")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("different real IP: expected 200, got %d", rec.Code)
	}
}
