package config

import "testing"

func TestIsNonLocalhost(t *testing.T) {
	tests := []struct {
		addr string
		want bool
	}{
		// Localhost addresses (safe)
		{"127.0.0.1", false},
		{"localhost", false},
		{"::1", false},
		{"Localhost", false},   // case-insensitive
		{" 127.0.0.1 ", false}, // trimmed

		// Non-localhost addresses (should warn)
		{"0.0.0.0", true},
		{"192.168.1.1", true},
		{"10.0.0.1", true},
		{"::", true},
		{"0.0.0.0", true},
		{"example.com", true},
		{"", true},
	}

	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			got := IsNonLocalhost(tt.addr)
			if got != tt.want {
				t.Errorf("IsNonLocalhost(%q) = %v, want %v", tt.addr, got, tt.want)
			}
		})
	}
}
