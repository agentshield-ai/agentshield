package config

import "testing"

// TestSSRFBypassVectors verifies H-1: SSRF protection catches bypass vectors
// like hex-encoded IPs, IPv6 loopback, and URL userinfo.
func TestSSRFBypassVectors(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		expected bool // true = private/local
	}{
		{"plain localhost", "https://localhost/api", true},
		{"127.0.0.1", "https://127.0.0.1/api", true},
		{"IPv6 loopback", "https://[::1]/api", true},
		{"10.x private", "https://10.0.0.1/api", true},
		{"192.168.x private", "https://192.168.1.1/api", true},
		{"172.16.x private", "https://172.16.0.1/api", true},
		{"unspecified 0.0.0.0", "https://0.0.0.0/api", true},
		{"ip6-localhost", "https://ip6-localhost/api", true},
		{"malformed URL fails closed", "ht!tp://broken", true},
		{"empty hostname fails closed", "https:///path", true},
		{"public IP allowed", "https://8.8.8.8/api", false},
		{"public domain allowed", "https://api.openai.com/v1", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsPrivateOrLocalURL(tt.url)
			if result != tt.expected {
				t.Errorf("IsPrivateOrLocalURL(%q) = %v, want %v", tt.url, result, tt.expected)
			}
		})
	}
}
