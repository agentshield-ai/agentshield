package config

import (
	"net/netip"
	"os"
	"strings"
	"testing"
)

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
		// IPv4-mapped IPv6 bypass vectors (new hardening)
		{"IPv4-mapped loopback", "https://[::ffff:127.0.0.1]/api", true},
		{"IPv4-mapped private 10.x", "https://[::ffff:10.0.0.1]/api", true},
		{"IPv4-mapped private 192.168.x", "https://[::ffff:192.168.1.1]/api", true},
		{"IPv4-mapped public allowed", "https://[::ffff:8.8.8.8]/api", false},
		// URL userinfo SSRF bypass vector
		{"userinfo bypass attempt", "https://admin@127.0.0.1/api", true},
		// URL-encoded hostname (not decoded by hostname parser, but test anyway)
		{"url with user:pass@host SSRF", "https://user:pass@127.0.0.1/api", true},
		// Additional private ranges
		{"172.31.x private (upper)", "https://172.31.255.255/api", true},
		{"172.17.x private (docker)", "https://172.17.0.1/api", true},
		{"169.254.x link-local", "https://169.254.169.254/api", true},
		// IPv6 private/link-local
		{"IPv6 link-local fe80", "https://[fe80::1]/api", true},
		{"IPv6 unspecified ::", "https://[::]/api", true},
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

// TestSSRF_DecimalIPBypass verifies H-1: decimal integer IP representation
// (e.g., 2130706433 = 127.0.0.1) is rejected. Go's net/netip does NOT accept
// decimal notation, so these are treated as hostnames and DNS-resolved.
// The test verifies that non-hostname strings are handled safely (fail-closed).
func TestSSRF_DecimalIPBypass(t *testing.T) {
	// 2130706433 = 0x7F000001 = 127.0.0.1 in decimal
	// Go's url.Parse treats this as a hostname (no dots), DNS will fail → fail-closed.
	result := IsPrivateOrLocalURL("https://2130706433/api")
	if !result {
		t.Error("decimal IP representation 2130706433 (=127.0.0.1) should be treated as private/local (fail-closed on unresolvable hostname)")
	}
}

// TestSSRF_OctalIPBypass verifies H-1: octal IP notation (0177.0.0.1 = 127.0.0.1)
// is rejected. net/netip rejects octal; net.ParseIP also rejects octal; Go's DNS
// resolver will treat it as a hostname and fail → fail-closed.
func TestSSRF_OctalIPBypass(t *testing.T) {
	// 0177 = 127 in octal; Go does NOT accept octal IPs
	result := IsPrivateOrLocalURL("https://0177.0.0.1/api")
	if !result {
		t.Error("octal IP 0177.0.0.1 (=127.0.0.1) should be treated as private/local (fail-closed)")
	}
}

// TestSSRF_ProtocolHandlers verifies that non-HTTPS protocols targeting internal
// hosts are handled correctly. The SSRF check parses the URL host; protocol
// handlers like file:// and gopher:// don't change the host check.
func TestSSRF_ProtocolHandlers(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		expected bool
	}{
		// file:// has no meaningful host, empty hostname → fail-closed
		{"file protocol", "file:///etc/passwd", true},
		// gopher:// targeting localhost
		{"gopher loopback", "gopher://127.0.0.1/", true},
		// gopher targeting private
		{"gopher private", "gopher://192.168.1.1/", true},
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

// TestSSRF_PortVariations verifies H-1: SSRF protection applies regardless
// of port number — the IP check ignores port.
func TestSSRF_PortVariations(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		expected bool
	}{
		{"localhost port 80", "https://localhost:80/api", true},
		{"127.0.0.1 port 8080", "https://127.0.0.1:8080/api", true},
		{"127.0.0.1 port 443", "https://127.0.0.1:443/api", true},
		{"10.0.0.1 port 9200", "https://10.0.0.1:9200/api", true},
		{"10.0.0.1 port 6379", "https://10.0.0.1:6379/api", true},
		{"::1 port 8080", "https://[::1]:8080/api", true},
		{"public with port", "https://8.8.8.8:443/api", false},
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

// TestSSRF_URLWithCredentials verifies H-1: URL userinfo (user:pass@host) bypass
// is rejected. The isPrivateOrLocalURL function explicitly checks parsed.User.
func TestSSRF_URLWithCredentials(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		expected bool
	}{
		{"user only at loopback", "https://user@127.0.0.1/api", true},
		{"user:pass at loopback", "https://user:pass@127.0.0.1/api", true},
		{"user:pass at private", "https://admin:secret@192.168.0.1/api", true},
		// Credentials at public host — still blocked via userinfo check
		// (defense in depth: userinfo in URLs is suspicious regardless)
		{"user:pass at public", "https://user:pass@8.8.8.8/api", true},
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

// TestConfig_MalformedYAML verifies that malformed YAML config files produce
// an error rather than silently loading with defaults.
func TestConfig_MalformedYAML(t *testing.T) {
	badYAML := "server: {\n  not: valid yaml\n  missing: close"
	f, err := createTempConfigFile(t, badYAML)
	if err != nil {
		t.Fatalf("creating temp file: %v", err)
	}
	_, err = LoadConfig(f)
	if err == nil {
		t.Error("expected error for malformed YAML, got nil")
	}
}

// TestConfig_InvalidEvaluationMode verifies that an invalid evaluation mode
// is rejected by validateConfig.
func TestConfig_InvalidEvaluationMode(t *testing.T) {
	cfg := &Config{
		Server:         ServerConfig{Addr: "127.0.0.1", Port: 8433},
		Auth:           AuthConfig{Token: strings.Repeat("a", 32)},
		Rules:          RulesConfig{Dir: "./rules"},
		Store:          StoreConfig{SQLitePath: "./data.db"},
		EvaluationMode: EvaluationMode("hackme"),
		LogLevel:       "info",
	}
	err := validateConfig(cfg)
	if err == nil {
		t.Error("expected error for invalid evaluation mode 'hackme', got nil")
	}
}

// TestConfig_ShortAuthToken verifies that auth tokens shorter than 32 chars
// are rejected — prevents weak token configuration.
func TestConfig_ShortAuthToken(t *testing.T) {
	cfg := &Config{
		Server:         ServerConfig{Addr: "127.0.0.1", Port: 8433},
		Auth:           AuthConfig{Token: "short"},
		Rules:          RulesConfig{Dir: "./rules"},
		Store:          StoreConfig{SQLitePath: "./data.db"},
		EvaluationMode: ModeEnforce,
		LogLevel:       "info",
	}
	err := validateConfig(cfg)
	if err == nil {
		t.Error("expected error for auth token shorter than 32 chars, got nil")
	}
}

// TestConfig_EmptyAuthToken verifies that an empty auth token is rejected.
func TestConfig_EmptyAuthToken(t *testing.T) {
	cfg := &Config{
		Server:         ServerConfig{Addr: "127.0.0.1", Port: 8433},
		Auth:           AuthConfig{Token: ""},
		Rules:          RulesConfig{Dir: "./rules"},
		Store:          StoreConfig{SQLitePath: "./data.db"},
		EvaluationMode: ModeEnforce,
		LogLevel:       "info",
	}
	err := validateConfig(cfg)
	if err == nil {
		t.Error("expected error for empty auth token, got nil")
	}
}

// TestConfig_TriageBaseURLMustBeHTTPS verifies that triage base_url requires
// HTTPS — prevents downgrade to HTTP which exposes the API key.
func TestConfig_TriageBaseURLMustBeHTTPS(t *testing.T) {
	cfg := &Config{
		Server:         ServerConfig{Addr: "127.0.0.1", Port: 8433},
		Auth:           AuthConfig{Token: strings.Repeat("a", 32)},
		Rules:          RulesConfig{Dir: "./rules"},
		Store:          StoreConfig{SQLitePath: "./data.db"},
		EvaluationMode: ModeEnforce,
		LogLevel:       "info",
		Triage: TriageConfig{
			BaseURL: "http://api.openai.com/v1", // HTTP not HTTPS
		},
	}
	err := validateConfig(cfg)
	if err == nil {
		t.Error("expected error for HTTP triage base_url, got nil")
	}
}

// TestConfig_TriageBaseURLBlocksPrivate verifies that triage base_url cannot
// target private networks (SSRF via config).
func TestConfig_TriageBaseURLBlocksPrivate(t *testing.T) {
	cfg := &Config{
		Server:         ServerConfig{Addr: "127.0.0.1", Port: 8433},
		Auth:           AuthConfig{Token: strings.Repeat("a", 32)},
		Rules:          RulesConfig{Dir: "./rules"},
		Store:          StoreConfig{SQLitePath: "./data.db"},
		EvaluationMode: ModeEnforce,
		LogLevel:       "info",
		Triage: TriageConfig{
			BaseURL: "https://10.0.0.1/v1",
		},
	}
	err := validateConfig(cfg)
	if err == nil {
		t.Error("expected error for triage base_url targeting private IP, got nil")
	}
}

// TestConfig_TestContextRequires32CharToken verifies that test_context.enabled=true
// requires a token of at least 32 characters to prevent timing attacks.
func TestConfig_TestContextRequires32CharToken(t *testing.T) {
	cfg := &Config{
		Server:         ServerConfig{Addr: "127.0.0.1", Port: 8433},
		Auth:           AuthConfig{Token: strings.Repeat("a", 32)},
		Rules:          RulesConfig{Dir: "./rules"},
		Store:          StoreConfig{SQLitePath: "./data.db"},
		EvaluationMode: ModeEnforce,
		LogLevel:       "info",
		TestContext:    TestContextConfig{Enabled: true, Token: "short"},
	}
	err := validateConfig(cfg)
	if err == nil {
		t.Error("expected error for short test_context token, got nil")
	}
}

// TestConfig_RetentionDaysBoundary verifies boundary validation for retention_days.
func TestConfig_RetentionDaysBoundary(t *testing.T) {
	baseToken := strings.Repeat("a", 32)
	tests := []struct {
		name          string
		retentionDays int
		expectErr     bool
	}{
		{"zero (disabled)", 0, false},
		{"one day", 1, false},
		{"max (3650)", 3650, false},
		{"one over max", 3651, true},
		{"negative", -1, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{
				Server:         ServerConfig{Addr: "127.0.0.1", Port: 8433},
				Auth:           AuthConfig{Token: baseToken},
				Rules:          RulesConfig{Dir: "./rules"},
				Store:          StoreConfig{SQLitePath: "./data.db", RetentionDays: tt.retentionDays},
				EvaluationMode: ModeEnforce,
				LogLevel:       "info",
			}
			err := validateConfig(cfg)
			if tt.expectErr && err == nil {
				t.Errorf("expected validation error for retention_days=%d, got nil", tt.retentionDays)
			}
			if !tt.expectErr && err != nil {
				t.Errorf("unexpected error for retention_days=%d: %v", tt.retentionDays, err)
			}
		})
	}
}

// createTempConfigFile writes YAML content to a temp file and returns the path.
// The temp file is cleaned up when the test ends.
func createTempConfigFile(t *testing.T, content string) (string, error) {
	t.Helper()
	f, err := os.CreateTemp("", "config_sec_test_*.yaml")
	if err != nil {
		return "", err
	}
	t.Cleanup(func() { os.Remove(f.Name()) })
	if _, err := f.WriteString(content); err != nil {
		return "", err
	}
	f.Close()
	return f.Name(), nil
}
