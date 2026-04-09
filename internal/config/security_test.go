package config

import (
	"net"
	"net/netip"
	"testing"
)

// TestSSRFBypassVectors verifies H-1: SSRF protection catches bypass vectors
// like hex-encoded IPs, IPv6 loopback, and URL userinfo.
func TestSSRFBypassVectors(t *testing.T) {
	_, dnsErr := net.LookupIP("api.openai.com")
	hasDNS := dnsErr == nil

	tests := []struct {
		name        string
		url         string
		expected    bool // true = private/local
		requiresDNS bool
	}{
		{"plain localhost", "https://localhost/api", true, false},
		{"127.0.0.1", "https://127.0.0.1/api", true, false},
		{"IPv6 loopback", "https://[::1]/api", true, false},
		{"10.x private", "https://10.0.0.1/api", true, false},
		{"192.168.x private", "https://192.168.1.1/api", true, false},
		{"172.16.x private", "https://172.16.0.1/api", true, false},
		{"unspecified 0.0.0.0", "https://0.0.0.0/api", true, false},
		{"ip6-localhost", "https://ip6-localhost/api", true, false},
		{"malformed URL fails closed", "ht!tp://broken", true, false},
		{"empty hostname fails closed", "https:///path", true, false},
		{"public IP allowed", "https://8.8.8.8/api", false, false},
		{"public domain allowed", "https://api.openai.com/v1", false, true},
		// IPv4-mapped IPv6 bypass vectors (new hardening)
		{"IPv4-mapped loopback", "https://[::ffff:127.0.0.1]/api", true, false},
		{"IPv4-mapped private 10.x", "https://[::ffff:10.0.0.1]/api", true, false},
		{"IPv4-mapped private 192.168.x", "https://[::ffff:192.168.1.1]/api", true, false},
		{"IPv4-mapped public allowed", "https://[::ffff:8.8.8.8]/api", false, false},
		// URL userinfo SSRF bypass vector
		{"userinfo bypass attempt", "https://admin@127.0.0.1/api", true, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.requiresDNS && !hasDNS {
				t.Skip("DNS resolution unavailable")
			}
			result := IsPrivateOrLocalURL(tt.url)
			if result != tt.expected {
				t.Errorf("IsPrivateOrLocalURL(%q) = %v, want %v", tt.url, result, tt.expected)
			}
		})
	}
}

// TestIsPrivateAddrUnmap verifies that isPrivateAddr correctly unmaps
// IPv4-mapped IPv6 addresses before checking private ranges.
func TestIsPrivateAddrUnmap(t *testing.T) {
	tests := []struct {
		name     string
		addr     string
		expected bool
	}{
		{"IPv4 loopback", "127.0.0.1", true},
		{"IPv6 loopback", "::1", true},
		{"IPv4-mapped loopback", "::ffff:127.0.0.1", true},
		{"IPv4 private", "10.0.0.1", true},
		{"IPv4-mapped private", "::ffff:10.0.0.1", true},
		{"IPv4-mapped 192.168", "::ffff:192.168.0.1", true},
		{"IPv4 public", "8.8.8.8", false},
		{"IPv4-mapped public", "::ffff:8.8.8.8", false},
		{"IPv6 link-local", "fe80::1", true},
		{"IPv6 unspecified", "::", true},
		{"IPv4-mapped unspecified", "::ffff:0.0.0.0", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			addr, err := netip.ParseAddr(tt.addr)
			if err != nil {
				t.Fatalf("ParseAddr(%q): %v", tt.addr, err)
			}
			result := isPrivateAddr(addr)
			if result != tt.expected {
				t.Errorf("isPrivateAddr(%s) = %v, want %v", tt.addr, result, tt.expected)
			}
		})
	}
}

// TestPathTraversalValidation verifies that filepath.Clean is used for
// path traversal detection, preventing bypass via encoded or complex paths.
func TestPathTraversalValidation(t *testing.T) {
	tests := []struct {
		name      string
		rulesDir  string
		sqlPath   string
		expectErr bool
	}{
		{"normal paths", "./rules", "./data.db", false},
		{"absolute paths", "/opt/rules", "/opt/data.db", false},
		{"rules dir traversal", "../../../etc", "./data.db", true},
		{"sqlite path traversal", "./rules", "../../../etc/passwd", true},
		{"encoded traversal in rules", "rules/./../../etc", "./data.db", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{
				Server:         ServerConfig{Addr: "127.0.0.1", Port: 8433},
				Auth:           AuthConfig{Token: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
				Rules:          RulesConfig{Dir: tt.rulesDir},
				Store:          StoreConfig{SQLitePath: tt.sqlPath},
				EvaluationMode: ModeEnforce,
				LogLevel:       "info",
			}
			err := validateConfig(cfg)
			if tt.expectErr && err == nil {
				t.Error("expected validation error for path traversal, got nil")
			}
			if !tt.expectErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}
