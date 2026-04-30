package config

import (
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// ServerConfig holds server configuration
type ServerConfig struct {
	Addr string `yaml:"addr" json:"addr"`
	Port int    `yaml:"port" json:"port"`
}

// AuthConfig holds authentication configuration
type AuthConfig struct {
	Token string `yaml:"token" json:"-"`
}

// RulesConfig holds rules configuration
type RulesConfig struct {
	Dir       string `yaml:"dir" json:"dir"`
	HotReload bool   `yaml:"hot_reload" json:"hot_reload"`
}

// StoreConfig holds store configuration
type StoreConfig struct {
	SQLitePath           string `yaml:"sqlite_path" json:"sqlite_path"`
	RetentionDays        int    `yaml:"retention_days" json:"retention_days"`                 // 0 = disabled
	CleanupIntervalHours int    `yaml:"cleanup_interval_hours" json:"cleanup_interval_hours"` // how often retention runs
}

// EvaluationMode defines how the engine evaluates events
type EvaluationMode string

const (
	ModeEnforce EvaluationMode = "enforce"
	ModeAudit   EvaluationMode = "audit"
	ModeShadow  EvaluationMode = "shadow"
)

// TriageAgentConfig configures the OpenClaw triage agent's personality and capabilities.
// Users can customise this to create a specialised security analyst agent.
type TriageAgentConfig struct {
	SystemPrompt string   `yaml:"system_prompt"` // Custom agent personality (default: SOC analyst)
	Model        string   `yaml:"model"`         // Override model for triage agent
	AgentID      string   `yaml:"agent_id"`      // OpenClaw agent ID (optional)
	Thinking     string   `yaml:"thinking"`      // Thinking mode: "off", "low", "high"
	Tools        []string `yaml:"tools"`         // Tools the agent can use: web_search, web_fetch, memory_search, read
	TimeoutSec   int      `yaml:"timeout_sec"`   // Agent session timeout (default: 60)
}

// CorrelationConfig controls deterministic, short-window alert correlation used by triage.
type CorrelationConfig struct {
	Enabled              bool    `yaml:"enabled"`
	WindowSec            int     `yaml:"window_sec"`
	MaxAlerts            int     `yaml:"max_alerts"`
	RequireSameSession   bool    `yaml:"require_same_session"`
	RequireSameTool      bool    `yaml:"require_same_tool"`
	WeightCritical       float64 `yaml:"weight_critical"`
	WeightHigh           float64 `yaml:"weight_high"`
	WeightChainBonus     float64 `yaml:"weight_chain_bonus"`
	WeightRepeatBonus    float64 `yaml:"weight_repeat_bonus"`
	TimeDecayHalfLifeSec int     `yaml:"time_decay_half_life_sec"`
	EscalateThreshold    float64 `yaml:"escalate_threshold"`
}

// TriageConfig holds triage configuration (fast triage — synchronous, in request path)
type TriageConfig struct {
	Enabled         bool              `yaml:"enabled"`
	Provider        string            `yaml:"provider"`         // "openai", "anthropic", or "openclaw"
	Model           string            `yaml:"model"`            // e.g. "gpt-4o-mini", "claude-sonnet-4-20250514"
	APIKey          string            `yaml:"api_key" json:"-"` // env: AGENTSHIELD_TRIAGE_API_KEY
	BaseURL         string            `yaml:"base_url"`         // custom base URL (e.g. https://openrouter.ai/api/v1)
	MaxTokens       int               `yaml:"max_tokens"`
	TimeoutSec      int               `yaml:"timeout_sec"`
	HealthCheckMode string            `yaml:"health_check_mode"` // "full" (default) or "connectivity"
	Correlation     CorrelationConfig `yaml:"correlation"`
}

// DeepTriageConfig holds deep triage configuration (async, OpenClaw sub-agent with tools)
type DeepTriageConfig struct {
	Enabled      bool              `yaml:"enabled"`
	GatewayURL   string            `yaml:"gateway_url"`            // default: http://127.0.0.1:18789
	GatewayToken string            `yaml:"gateway_token" json:"-"` // env: OPENCLAW_GATEWAY_TOKEN
	Agent        TriageAgentConfig `yaml:"agent"`                  // Agent personality, model, tools
	MinSeverity  string            `yaml:"min_severity"`           // Minimum severity to trigger deep triage (default: critical)
	Webhook      string            `yaml:"webhook"`                // Optional webhook URL for deep triage results
}

// IsValid checks if the evaluation mode is valid
func (m EvaluationMode) IsValid() bool {
	switch m {
	case ModeEnforce, ModeAudit, ModeShadow:
		return true
	default:
		return false
	}
}

// TelemetryConfig configures OpenTelemetry export.
type TelemetryConfig struct {
	Enabled         bool    `yaml:"enabled"`
	Endpoint        string  `yaml:"endpoint"`
	ServiceName     string  `yaml:"service_name"`
	SampleRate      float64 `yaml:"sample_rate"`
	ExportAllEvents bool    `yaml:"export_all_events"`
	Insecure        bool    `yaml:"insecure"`
}

// SessionConfig configures per-session behavioural sequencing.
type SessionConfig struct {
	Enabled   bool `yaml:"enabled" json:"enabled"`
	WindowSec int  `yaml:"window_sec" json:"window_sec"`
	MaxEvents int  `yaml:"max_events" json:"max_events"`
}

// Config holds the complete application configuration
type TestContextConfig struct {
	Enabled bool   `yaml:"enabled" json:"enabled"`
	Token   string `yaml:"token" json:"-"`
}

// CacheConfig controls the verdict caching layer.
type CacheConfig struct {
	Enabled bool `yaml:"enabled" json:"enabled"`   // default true
	MaxSize int  `yaml:"max_size" json:"max_size"` // default 10000
	TTLSec  int  `yaml:"ttl_sec" json:"ttl_sec"`   // default 300 (5 minutes)
}

type Config struct {
	Server         ServerConfig      `yaml:"server" json:"server"`
	Auth           AuthConfig        `yaml:"auth" json:"auth"`
	Rules          RulesConfig       `yaml:"rules" json:"rules"`
	Store          StoreConfig       `yaml:"store" json:"store"`
	Cache          CacheConfig       `yaml:"cache" json:"cache"`
	Triage         TriageConfig      `yaml:"triage" json:"triage"`
	DeepTriage     DeepTriageConfig  `yaml:"deep_triage" json:"deep_triage"`
	TestContext    TestContextConfig `yaml:"test_context" json:"test_context"`
	Telemetry      TelemetryConfig   `yaml:"telemetry" json:"telemetry"`
	Session        SessionConfig     `yaml:"session" json:"session"`
	EvaluationMode EvaluationMode    `yaml:"evaluation_mode" json:"evaluation_mode"`
	LogLevel       string            `yaml:"log_level" json:"log_level"`
}

// LoadConfig loads configuration from file with environment variable overrides
func LoadConfig(path string) (*Config, error) {
	// Set secure defaults
	cfg := &Config{
		Server: ServerConfig{
			Addr: "127.0.0.1", // SECURITY: Bind to localhost by default
			Port: 8433,
		},
		Auth: AuthConfig{
			Token: "", // Will be validated later to require auth
		},
		Rules: RulesConfig{
			Dir:       "./rules",
			HotReload: true,
		},
		Store: StoreConfig{
			SQLitePath:           "./agentshield.db",
			RetentionDays:        90,
			CleanupIntervalHours: 24,
		},
		Cache: CacheConfig{
			Enabled: true,
			MaxSize: 10000,
			TTLSec:  300,
		},
		Triage: TriageConfig{
			Enabled:    false,
			Provider:   "openai",
			Model:      "gpt-4o-mini",
			APIKey:     "",
			MaxTokens:  500,
			TimeoutSec: 10,
			Correlation: CorrelationConfig{
				Enabled:              true,
				WindowSec:            900,
				MaxAlerts:            5,
				RequireSameSession:   true,
				RequireSameTool:      false,
				WeightCritical:       0.6,
				WeightHigh:           0.35,
				WeightChainBonus:     0.4,
				WeightRepeatBonus:    0.2,
				TimeDecayHalfLifeSec: 300,
				EscalateThreshold:    0.8,
			},
		},
		TestContext: TestContextConfig{
			Enabled: false,
			Token:   "",
		},
		EvaluationMode: ModeEnforce,
		LogLevel:       "info",
	}

	// Load from file if it exists
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("reading config file: %w", err)
		}

		if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("parsing config file: %w", err)
		}
	}

	// Apply environment variable overrides
	applyEnvOverrides(cfg)

	// Backward-compatible defaults for newly added fields
	if cfg.Store.CleanupIntervalHours == 0 {
		cfg.Store.CleanupIntervalHours = 24
	}

	// Validate configuration
	if err := validateConfig(cfg); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	// Resolve relative paths
	if err := resolveRelativePaths(cfg, path); err != nil {
		return nil, fmt.Errorf("resolving paths: %w", err)
	}

	return cfg, nil
}

// applyEnvOverrides applies environment variable overrides to config
func applyEnvOverrides(cfg *Config) {
	if port := os.Getenv("AGENTSHIELD_PORT"); port != "" {
		if p, err := strconv.Atoi(port); err != nil {
			slog.Warn("Invalid AGENTSHIELD_PORT, using default", "value", port, "error", err)
		} else {
			cfg.Server.Port = p
		}
	}
	if addr := os.Getenv("AGENTSHIELD_ADDR"); addr != "" {
		cfg.Server.Addr = addr
	}
	if token := os.Getenv("AGENTSHIELD_AUTH_TOKEN"); token != "" {
		cfg.Auth.Token = token
	}
	if rulesDir := os.Getenv("AGENTSHIELD_RULES_DIR"); rulesDir != "" {
		cfg.Rules.Dir = rulesDir
	}
	if dbPath := os.Getenv("AGENTSHIELD_DB_PATH"); dbPath != "" {
		cfg.Store.SQLitePath = dbPath
	}
	if mode := os.Getenv("AGENTSHIELD_MODE"); mode != "" {
		cfg.EvaluationMode = EvaluationMode(mode)
	}
	if logLevel := os.Getenv("AGENTSHIELD_LOG_LEVEL"); logLevel != "" {
		cfg.LogLevel = logLevel
	}
	if triageAPIKey := os.Getenv("AGENTSHIELD_TRIAGE_API_KEY"); triageAPIKey != "" {
		cfg.Triage.APIKey = triageAPIKey
	}
	if v := os.Getenv("AGENTSHIELD_OTEL_ENDPOINT"); v != "" {
		cfg.Telemetry.Endpoint = v
	}
	if v := os.Getenv("AGENTSHIELD_OTEL_ENABLED"); v != "" {
		cfg.Telemetry.Enabled = v == "true" || v == "1"
	}
}

// validateConfig validates the configuration
func validateConfig(cfg *Config) error {
	// Validate evaluation mode
	if !cfg.EvaluationMode.IsValid() {
		return fmt.Errorf("invalid evaluation mode: %s (must be enforce, audit, or shadow)", cfg.EvaluationMode)
	}

	// Validate required fields
	if cfg.Rules.Dir == "" {
		return fmt.Errorf("rules directory cannot be empty")
	}

	if cfg.Store.SQLitePath == "" {
		return fmt.Errorf("sqlite path cannot be empty")
	}
	if cfg.Store.RetentionDays < 0 || cfg.Store.RetentionDays > 3650 {
		return fmt.Errorf("store retention_days must be between 0 and 3650")
	}
	if cfg.Store.CleanupIntervalHours != 0 && (cfg.Store.CleanupIntervalHours < 1 || cfg.Store.CleanupIntervalHours > 168) {
		return fmt.Errorf("store cleanup_interval_hours must be between 1 and 168")
	}

	// SECURITY: Validate auth configuration
	if cfg.Auth.Token == "" {
		return fmt.Errorf("auth token is required - set AGENTSHIELD_AUTH_TOKEN environment variable or configure auth.token in config file")
	}

	if len(cfg.Auth.Token) < 32 {
		return fmt.Errorf("auth token must be at least 32 characters long for security")
	}

	// SECURITY: Validate bind address
	if IsNonLocalhost(cfg.Server.Addr) {
		slog.Warn("server binding to a non-localhost address; use a TLS-terminating reverse proxy in production",
			"addr", cfg.Server.Addr,
			"recommendation", "deploy behind nginx/Caddy with TLS or bind to 127.0.0.1",
		)
	}

	// Validate port range
	if cfg.Server.Port < 1 || cfg.Server.Port > 65535 {
		return fmt.Errorf("invalid port: %d (must be 1-65535)", cfg.Server.Port)
	}

	// SECURITY: Validate paths for directory traversal. Use filepath.Clean to
	// canonicalize, then reject if the cleaned path still contains "..".
	if strings.Contains(filepath.Clean(cfg.Rules.Dir), "..") {
		return fmt.Errorf("rules directory path contains directory traversal")
	}

	if strings.Contains(filepath.Clean(cfg.Store.SQLitePath), "..") {
		return fmt.Errorf("sqlite path contains directory traversal")
	}

	// SECURITY: Validate triage URLs for SSRF protection
	if cfg.Triage.BaseURL != "" {
		if !strings.HasPrefix(cfg.Triage.BaseURL, "https://") {
			return fmt.Errorf("triage base_url must use HTTPS for security")
		}
		if isPrivateOrLocalURL(cfg.Triage.BaseURL) {
			return fmt.Errorf("triage base_url cannot target internal/private networks")
		}
	}

	// SECURITY: Validate deep triage gateway URL for SSRF protection
	if cfg.DeepTriage.Enabled && cfg.DeepTriage.GatewayURL != "" {
		// gateway_url targeting localhost is expected for local OpenClaw; we
		// log a warning rather than reject (operators intentionally run it
		// locally), but only when the URL actually targets a private host.
		if isPrivateOrLocalURL(cfg.DeepTriage.GatewayURL) {
			slog.Warn("deep_triage gateway_url targets a private/local network; allowed for local OpenClaw", "url", cfg.DeepTriage.GatewayURL)
		}
	}

	// Validate correlation settings
	if cfg.Triage.Correlation.Enabled {
		if err := validateIntRange("triage correlation window_sec", cfg.Triage.Correlation.WindowSec, 60, 86400); err != nil {
			return err
		}
		if err := validateIntRange("triage correlation max_alerts", cfg.Triage.Correlation.MaxAlerts, 1, 100); err != nil {
			return err
		}
		if err := validateIntRange("triage correlation time_decay_half_life_sec", cfg.Triage.Correlation.TimeDecayHalfLifeSec, 30, 86400); err != nil {
			return err
		}
		if cfg.Triage.Correlation.EscalateThreshold < 0 || cfg.Triage.Correlation.EscalateThreshold > 10 {
			return fmt.Errorf("triage correlation escalate_threshold must be between 0 and 10")
		}
	}

	if cfg.TestContext.Enabled {
		if len(cfg.TestContext.Token) < 32 {
			return fmt.Errorf("test_context.token must be at least 32 characters when test_context.enabled=true")
		}
	}

	// Validate log level
	if !isValidLogLevel(cfg.LogLevel) {
		return fmt.Errorf("invalid log level: %s (must be debug, info, warn, or error)", cfg.LogLevel)
	}

	// Validate telemetry configuration
	if cfg.Telemetry.Enabled {
		if cfg.Telemetry.Endpoint == "" {
			return fmt.Errorf("telemetry.endpoint is required when telemetry is enabled")
		}
		if !cfg.Telemetry.Insecure && !strings.HasPrefix(cfg.Telemetry.Endpoint, "https://") {
			return fmt.Errorf("telemetry.endpoint must use HTTPS (set insecure: true to allow HTTP)")
		}
		if cfg.Telemetry.SampleRate < 0 || cfg.Telemetry.SampleRate > 1.0 {
			return fmt.Errorf("telemetry.sample_rate must be between 0.0 and 1.0")
		}
		if cfg.Telemetry.ServiceName == "" {
			cfg.Telemetry.ServiceName = "agentshield"
		}
		if cfg.Telemetry.SampleRate == 0 {
			cfg.Telemetry.SampleRate = 1.0
		}
	}

	// Apply session sequencing defaults when enabled
	if cfg.Session.Enabled {
		if cfg.Session.WindowSec <= 0 {
			cfg.Session.WindowSec = 900
		}
		if cfg.Session.MaxEvents <= 0 {
			cfg.Session.MaxEvents = 50
		}
	}

	return nil
}

func validateIntRange(name string, value, min, max int) error {
	if value < min || value > max {
		return fmt.Errorf("%s must be between %d and %d", name, min, max)
	}
	return nil
}

// IsNonLocalhost reports whether addr is NOT a localhost address.
// It returns true for addresses like "0.0.0.0", "192.168.1.1", or any
// address that is not "127.0.0.1", "localhost", or "::1".
func IsNonLocalhost(addr string) bool {
	switch strings.ToLower(strings.TrimSpace(addr)) {
	case "127.0.0.1", "localhost", "::1":
		return false
	default:
		return true
	}
}

// IsPrivateOrLocalURL checks whether a URL targets a private/local network.
// It parses the URL properly and resolves the hostname to check against
// RFC 1918, loopback, link-local, and other non-routable ranges.
// Uses net/netip for stricter IP parsing that rejects ambiguous forms
// (octal notation, IPv4-mapped IPv6 bypass vectors).
func IsPrivateOrLocalURL(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return true // fail closed on malformed URLs
	}

	hostname := parsed.Hostname()
	if hostname == "" {
		return true
	}

	// Check common literal names
	lower := strings.ToLower(hostname)
	if lower == "localhost" || lower == "ip6-localhost" || lower == "ip6-loopback" {
		return true
	}

	// Check if the hostname has a userinfo component (SSRF bypass vector)
	if parsed.User != nil {
		return true
	}

	// Try net/netip first — stricter than net.ParseIP, rejects ambiguous forms
	if addr, err := netip.ParseAddr(hostname); err == nil {
		return isPrivateAddr(addr)
	}

	// Fallback: try net.ParseIP for forms that netip rejects but are valid
	// (e.g., some IPv6 zone-scoped addresses)
	if ip := net.ParseIP(hostname); ip != nil {
		return isPrivateIP(ip)
	}

	// Resolve hostname to IPs and check each
	ips, err := net.LookupIP(hostname)
	if err != nil {
		return true // fail closed if DNS resolution fails
	}
	for _, resolved := range ips {
		if isPrivateIP(resolved) {
			return true
		}
	}

	return false
}

// isPrivateAddr checks if a netip.Addr is private/local. It handles
// IPv4-mapped IPv6 addresses by unmapping before the check.
func isPrivateAddr(addr netip.Addr) bool {
	// Unmap IPv4-mapped IPv6 addresses (e.g., ::ffff:127.0.0.1 -> 127.0.0.1)
	// to prevent bypass via IPv4-mapped IPv6 notation.
	addr = addr.Unmap()
	return addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() ||
		addr.IsLinkLocalMulticast() || addr.IsUnspecified()
}

func isPrivateIP(ip net.IP) bool {
	// Also check via netip if possible, to catch IPv4-mapped IPv6 bypass
	if addr, ok := netip.AddrFromSlice(ip); ok {
		return isPrivateAddr(addr)
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified()
}

// isPrivateOrLocalURL is the internal alias used by validateConfig.
func isPrivateOrLocalURL(rawURL string) bool {
	return IsPrivateOrLocalURL(rawURL)
}

func isValidLogLevel(level string) bool {
	switch strings.ToLower(level) {
	case "debug", "info", "warn", "error":
		return true
	default:
		return false
	}
}

// resolveRelativePaths resolves relative paths in config relative to config file
func resolveRelativePaths(cfg *Config, configPath string) error {
	var configDir string
	if configPath != "" {
		configDir = filepath.Dir(configPath)
	} else {
		var err error
		configDir, err = os.Getwd()
		if err != nil {
			return fmt.Errorf("getting working directory: %w", err)
		}
	}

	// Resolve rules directory
	if !filepath.IsAbs(cfg.Rules.Dir) {
		cfg.Rules.Dir = filepath.Join(configDir, cfg.Rules.Dir)
	}

	// Resolve SQLite path
	if !filepath.IsAbs(cfg.Store.SQLitePath) {
		cfg.Store.SQLitePath = filepath.Join(configDir, cfg.Store.SQLitePath)
	}

	return nil
}

// ListenAddr returns the full listen address
func (c *Config) ListenAddr() string {
	return fmt.Sprintf("%s:%d", c.Server.Addr, c.Server.Port)
}
