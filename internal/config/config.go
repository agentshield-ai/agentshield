package config

import (
	"fmt"
	"os"
	"path/filepath"
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
	Token string `yaml:"token" json:"token"`
}

// RulesConfig holds rules configuration
type RulesConfig struct {
	Dir       string `yaml:"dir" json:"dir"`
	HotReload bool   `yaml:"hot_reload" json:"hot_reload"`
}

// StoreConfig holds store configuration
type StoreConfig struct {
	SQLitePath string `yaml:"sqlite_path" json:"sqlite_path"`
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
	Enabled     bool              `yaml:"enabled"`
	Provider    string            `yaml:"provider"` // "openai", "anthropic", or "openclaw"
	Model       string            `yaml:"model"`    // e.g. "gpt-4o-mini", "claude-sonnet-4-20250514"
	APIKey      string            `yaml:"api_key"`  // env: AGENTSHIELD_TRIAGE_API_KEY
	BaseURL     string            `yaml:"base_url"` // custom base URL (e.g. https://openrouter.ai/api/v1)
	MaxTokens   int               `yaml:"max_tokens"`
	TimeoutSec  int               `yaml:"timeout_sec"`
	Correlation CorrelationConfig `yaml:"correlation"`
}

// DeepTriageConfig holds deep triage configuration (async, OpenClaw sub-agent with tools)
type DeepTriageConfig struct {
	Enabled      bool              `yaml:"enabled"`
	GatewayURL   string            `yaml:"gateway_url"`   // default: http://127.0.0.1:18789
	GatewayToken string            `yaml:"gateway_token"` // env: OPENCLAW_GATEWAY_TOKEN
	Agent        TriageAgentConfig `yaml:"agent"`         // Agent personality, model, tools
	MinSeverity  string            `yaml:"min_severity"`  // Minimum severity to trigger deep triage (default: critical)
	Webhook      string            `yaml:"webhook"`       // Optional webhook URL for deep triage results
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

// Config holds the complete application configuration
type Config struct {
	Server         ServerConfig     `yaml:"server" json:"server"`
	Auth           AuthConfig       `yaml:"auth" json:"auth"`
	Rules          RulesConfig      `yaml:"rules" json:"rules"`
	Store          StoreConfig      `yaml:"store" json:"store"`
	Triage         TriageConfig     `yaml:"triage" json:"triage"`
	DeepTriage     DeepTriageConfig `yaml:"deep_triage" json:"deep_triage"`
	EvaluationMode EvaluationMode   `yaml:"evaluation_mode" json:"evaluation_mode"`
	LogLevel       string           `yaml:"log_level" json:"log_level"`
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
			SQLitePath: "./agentshield.db",
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
		fmt.Sscanf(port, "%d", &cfg.Server.Port)
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

	// SECURITY: Validate auth configuration
	if cfg.Auth.Token == "" {
		return fmt.Errorf("auth token is required - set AGENTSHIELD_AUTH_TOKEN environment variable or configure auth.token in config file")
	}

	if len(cfg.Auth.Token) < 32 {
		return fmt.Errorf("auth token must be at least 32 characters long for security")
	}

	// SECURITY: Validate bind address
	if cfg.Server.Addr == "0.0.0.0" {
		fmt.Printf("WARNING: Server binding to all interfaces (0.0.0.0). Consider using 127.0.0.1 for localhost only.\n")
	}

	// Validate port range
	if cfg.Server.Port < 1 || cfg.Server.Port > 65535 {
		return fmt.Errorf("invalid port: %d (must be 1-65535)", cfg.Server.Port)
	}

	// SECURITY: Validate paths for directory traversal
	if strings.Contains(cfg.Rules.Dir, "..") {
		return fmt.Errorf("rules directory path contains suspicious characters (..)")
	}

	if strings.Contains(cfg.Store.SQLitePath, "..") {
		return fmt.Errorf("sqlite path contains suspicious characters (..)")
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

	// Validate log level
	if !isValidLogLevel(cfg.LogLevel) {
		return fmt.Errorf("invalid log level: %s (must be debug, info, warn, or error)", cfg.LogLevel)
	}

	return nil
}

func validateIntRange(name string, value, min, max int) error {
	if value < min || value > max {
		return fmt.Errorf("%s must be between %d and %d", name, min, max)
	}
	return nil
}

func isPrivateOrLocalURL(url string) bool {
	privateMarkers := []string{"localhost", "127.0.0.1", "192.168.", "10.", "172."}
	for _, marker := range privateMarkers {
		if strings.Contains(url, marker) {
			return true
		}
	}
	return false
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
