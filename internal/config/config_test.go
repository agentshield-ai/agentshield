package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	tests := []struct {
		name        string
		configYAML  string
		expectError bool
		expected    *Config
	}{
		{
			name: "default config",
			configYAML: `
auth:
  token: "test-secure-token-with-32-chars-minimum"
`,
			expected: &Config{
				Server: ServerConfig{
					Addr: "127.0.0.1", // Updated default
					Port: 8433,
				},
				Auth: AuthConfig{
					Token: "test-secure-token-with-32-chars-minimum",
				},
				EvaluationMode: ModeEnforce,
				LogLevel:       "info",
			},
		},
		{
			name: "valid config",
			configYAML: `
server:
  addr: "127.0.0.1"
  port: 9000
auth:
  token: "test-secure-token-with-32-chars-minimum"
rules:
  dir: "./test-rules"
  hot_reload: false
store:
  sqlite_path: "./test.db"
evaluation_mode: "audit"
log_level: "debug"
`,
			expected: &Config{
				Server: ServerConfig{
					Addr: "127.0.0.1",
					Port: 9000,
				},
				Auth: AuthConfig{
					Token: "test-secure-token-with-32-chars-minimum",
				},
				Rules: RulesConfig{
					Dir:       "./test-rules",
					HotReload: false,
				},
				Store: StoreConfig{
					SQLitePath: "./test.db",
				},
				EvaluationMode: ModeAudit,
				LogLevel:       "debug",
			},
		},
		{
			name: "invalid evaluation mode",
			configYAML: `
evaluation_mode: "invalid"
`,
			expectError: true,
		},
		{
			name: "invalid port",
			configYAML: `
server:
  port: 99999
`,
			expectError: true,
		},
		{
			name: "invalid log level",
			configYAML: `
log_level: "invalid"
`,
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var configPath string
			if tt.configYAML != "" {
				// Create temporary config file
				tmpFile, err := os.CreateTemp("", "config_test_*.yaml")
				if err != nil {
					t.Fatalf("creating temp file: %v", err)
				}
				defer os.Remove(tmpFile.Name())

				if _, err := tmpFile.WriteString(tt.configYAML); err != nil {
					t.Fatalf("writing config file: %v", err)
				}
				tmpFile.Close()
				configPath = tmpFile.Name()
			}

			cfg, err := LoadConfig(configPath)
			if tt.expectError {
				if err == nil {
					t.Errorf("expected error but got none")
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			if cfg.Server.Addr != tt.expected.Server.Addr {
				t.Errorf("expected addr %s, got %s", tt.expected.Server.Addr, cfg.Server.Addr)
			}

			if cfg.Server.Port != tt.expected.Server.Port {
				t.Errorf("expected port %d, got %d", tt.expected.Server.Port, cfg.Server.Port)
			}

			if cfg.EvaluationMode != tt.expected.EvaluationMode {
				t.Errorf("expected evaluation mode %s, got %s", tt.expected.EvaluationMode, cfg.EvaluationMode)
			}

			if cfg.LogLevel != tt.expected.LogLevel {
				t.Errorf("expected log level %s, got %s", tt.expected.LogLevel, cfg.LogLevel)
			}
		})
	}
}

func TestEvaluationModeIsValid(t *testing.T) {
	tests := []struct {
		mode  EvaluationMode
		valid bool
	}{
		{ModeEnforce, true},
		{ModeAudit, true},
		{ModeShadow, true},
		{"invalid", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(string(tt.mode), func(t *testing.T) {
			if got := tt.mode.IsValid(); got != tt.valid {
				t.Errorf("IsValid() = %v, want %v", got, tt.valid)
			}
		})
	}
}

func TestApplyEnvOverrides(t *testing.T) {
	// Save original environment
	originalEnv := make(map[string]string)
	envVars := []string{
		"AGENTSHIELD_PORT",
		"AGENTSHIELD_ADDR",
		"AGENTSHIELD_AUTH_TOKEN",
		"AGENTSHIELD_MODE",
	}

	for _, env := range envVars {
		if val := os.Getenv(env); val != "" {
			originalEnv[env] = val
		}
	}

	// Clean up environment
	defer func() {
		for _, env := range envVars {
			os.Unsetenv(env)
		}
		for env, val := range originalEnv {
			os.Setenv(env, val)
		}
	}()

	// Set test environment variables
	os.Setenv("AGENTSHIELD_PORT", "9999")
	os.Setenv("AGENTSHIELD_ADDR", "127.0.0.1")
	os.Setenv("AGENTSHIELD_AUTH_TOKEN", "env-token")
	os.Setenv("AGENTSHIELD_MODE", "audit")

	cfg := &Config{
		Server: ServerConfig{
			Addr: "0.0.0.0",
			Port: 8433,
		},
		Auth: AuthConfig{
			Token: "original-token",
		},
		EvaluationMode: ModeEnforce,
	}

	applyEnvOverrides(cfg)

	if cfg.Server.Port != 9999 {
		t.Errorf("expected port 9999, got %d", cfg.Server.Port)
	}

	if cfg.Server.Addr != "127.0.0.1" {
		t.Errorf("expected addr 127.0.0.1, got %s", cfg.Server.Addr)
	}

	if cfg.Auth.Token != "env-token" {
		t.Errorf("expected token env-token, got %s", cfg.Auth.Token)
	}

	if cfg.EvaluationMode != ModeAudit {
		t.Errorf("expected mode audit, got %s", cfg.EvaluationMode)
	}
}

func TestResolveRelativePaths(t *testing.T) {
	// Create temporary directory structure
	tmpDir, err := os.MkdirTemp("", "config_test_paths")
	if err != nil {
		t.Fatalf("creating temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	configPath := filepath.Join(tmpDir, "config.yaml")

	cfg := &Config{
		Rules: RulesConfig{
			Dir: "./rules",
		},
		Store: StoreConfig{
			SQLitePath: "./database.db",
		},
	}

	err = resolveRelativePaths(cfg, configPath)
	if err != nil {
		t.Fatalf("resolving paths: %v", err)
	}

	expectedRulesDir := filepath.Join(tmpDir, "rules")
	if cfg.Rules.Dir != expectedRulesDir {
		t.Errorf("expected rules dir %s, got %s", expectedRulesDir, cfg.Rules.Dir)
	}

	expectedDBPath := filepath.Join(tmpDir, "database.db")
	if cfg.Store.SQLitePath != expectedDBPath {
		t.Errorf("expected DB path %s, got %s", expectedDBPath, cfg.Store.SQLitePath)
	}
}

func TestLoadConfig_TelemetrySection(t *testing.T) {
	rulesDir := t.TempDir()
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	cfgData := []byte(`
server:
  port: 8433
auth:
  token: "test-token-that-is-at-least-32-characters-long"
rules:
  dir: "` + rulesDir + `"
telemetry:
  enabled: true
  endpoint: "https://otel-collector.example.com:4318"
  service_name: "agentshield-prod"
  sample_rate: 0.5
  export_all_events: true
  insecure: true
`)
	if err := os.WriteFile(cfgPath, cfgData, 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	if !cfg.Telemetry.Enabled {
		t.Error("expected Telemetry.Enabled = true")
	}
	if cfg.Telemetry.Endpoint != "https://otel-collector.example.com:4318" {
		t.Errorf("expected endpoint, got %q", cfg.Telemetry.Endpoint)
	}
	if cfg.Telemetry.ServiceName != "agentshield-prod" {
		t.Errorf("expected service_name, got %q", cfg.Telemetry.ServiceName)
	}
	if cfg.Telemetry.SampleRate != 0.5 {
		t.Errorf("expected sample_rate 0.5, got %f", cfg.Telemetry.SampleRate)
	}
	if !cfg.Telemetry.ExportAllEvents {
		t.Error("expected ExportAllEvents = true")
	}
	if !cfg.Telemetry.Insecure {
		t.Error("expected Telemetry.Insecure = true")
	}
}

func TestListenAddr(t *testing.T) {
	cfg := &Config{
		Server: ServerConfig{
			Addr: "127.0.0.1",
			Port: 9000,
		},
	}

	expected := "127.0.0.1:9000"
	if got := cfg.ListenAddr(); got != expected {
		t.Errorf("ListenAddr() = %s, want %s", got, expected)
	}
}

// validBaseConfig returns a Config that passes validateConfig. The rules
// directory is a real temp directory so the path-traversal check succeeds.
func validBaseConfig(t *testing.T) *Config {
	t.Helper()
	return &Config{
		Server: ServerConfig{
			Addr: "127.0.0.1",
			Port: 8433,
		},
		Auth: AuthConfig{
			Token: "test-token-that-is-at-least-32-characters-long",
		},
		Rules: RulesConfig{
			Dir: t.TempDir(),
		},
		Store: StoreConfig{
			SQLitePath:           filepath.Join(t.TempDir(), "test.db"),
			RetentionDays:        90,
			CleanupIntervalHours: 24,
		},
		EvaluationMode: ModeEnforce,
		LogLevel:       "info",
	}
}

func TestValidateConfig_TelemetryEndpointRequired(t *testing.T) {
	cfg := validBaseConfig(t)
	cfg.Telemetry.Enabled = true
	cfg.Telemetry.Endpoint = ""

	err := validateConfig(cfg)
	if err == nil {
		t.Fatal("expected validation error when telemetry is enabled but endpoint is empty")
	}
	if !strings.Contains(err.Error(), "telemetry.endpoint is required") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestValidateConfig_TelemetrySampleRateBounds(t *testing.T) {
	tests := []struct {
		name       string
		sampleRate float64
		wantErr    bool
	}{
		{name: "negative", sampleRate: -0.1, wantErr: true},
		{name: "zero_is_ok", sampleRate: 0.0, wantErr: false},
		{name: "half_is_ok", sampleRate: 0.5, wantErr: false},
		{name: "one_is_ok", sampleRate: 1.0, wantErr: false},
		{name: "above_one", sampleRate: 1.1, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validBaseConfig(t)
			cfg.Telemetry.Enabled = true
			cfg.Telemetry.Endpoint = "https://otel.example.com:4318"
			cfg.Telemetry.SampleRate = tt.sampleRate

			err := validateConfig(cfg)
			if tt.wantErr && err == nil {
				t.Fatalf("expected error for sample_rate=%f, got none", tt.sampleRate)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error for sample_rate=%f: %v", tt.sampleRate, err)
			}
		})
	}
}

func TestValidateConfig_TelemetryInsecureHTTP(t *testing.T) {
	cfg := validBaseConfig(t)
	cfg.Telemetry.Enabled = true
	cfg.Telemetry.Endpoint = "http://otel.example.com:4318"
	cfg.Telemetry.Insecure = false
	cfg.Telemetry.SampleRate = 1.0

	err := validateConfig(cfg)
	if err == nil {
		t.Fatal("expected error when endpoint is HTTP and insecure=false")
	}
	if !strings.Contains(err.Error(), "HTTPS") {
		t.Errorf("error should mention HTTPS, got: %v", err)
	}

	// Now allow HTTP via insecure flag
	cfg.Telemetry.Insecure = true
	err = validateConfig(cfg)
	if err != nil {
		t.Fatalf("unexpected error when insecure=true: %v", err)
	}
}

func TestValidateConfig_TelemetryDefaults(t *testing.T) {
	cfg := validBaseConfig(t)
	cfg.Telemetry.Enabled = true
	cfg.Telemetry.Endpoint = "https://otel.example.com:4318"
	cfg.Telemetry.ServiceName = ""
	cfg.Telemetry.SampleRate = 0

	err := validateConfig(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Telemetry.ServiceName != "agentshield" {
		t.Errorf("expected default service_name 'agentshield', got %q", cfg.Telemetry.ServiceName)
	}
	if cfg.Telemetry.SampleRate != 1.0 {
		t.Errorf("expected default sample_rate 1.0, got %f", cfg.Telemetry.SampleRate)
	}
}

func TestLoadConfig_SessionSection(t *testing.T) {
	rulesDir := t.TempDir()
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	cfgData := []byte(`
server:
  port: 8433
auth:
  token: "test-token-that-is-at-least-32-characters-long"
rules:
  dir: "` + rulesDir + `"
session:
  enabled: true
  window_sec: 600
  max_events: 100
`)
	if err := os.WriteFile(cfgPath, cfgData, 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	if !cfg.Session.Enabled {
		t.Error("expected Session.Enabled = true")
	}
	if cfg.Session.WindowSec != 600 {
		t.Errorf("expected WindowSec=600, got %d", cfg.Session.WindowSec)
	}
	if cfg.Session.MaxEvents != 100 {
		t.Errorf("expected MaxEvents=100, got %d", cfg.Session.MaxEvents)
	}
}

func TestValidateConfig_SessionDefaults(t *testing.T) {
	cfg := validBaseConfig(t)
	cfg.Session.Enabled = true
	cfg.Session.WindowSec = 0
	cfg.Session.MaxEvents = 0

	err := validateConfig(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Session.WindowSec != 900 {
		t.Errorf("expected default WindowSec=900, got %d", cfg.Session.WindowSec)
	}
	if cfg.Session.MaxEvents != 50 {
		t.Errorf("expected default MaxEvents=50, got %d", cfg.Session.MaxEvents)
	}
}

func TestApplyEnvOverrides_TelemetryEndpoint(t *testing.T) {
	// Save and restore env vars
	envVars := []string{"AGENTSHIELD_OTEL_ENDPOINT", "AGENTSHIELD_OTEL_ENABLED"}
	originalEnv := make(map[string]string)
	for _, env := range envVars {
		if val, ok := os.LookupEnv(env); ok {
			originalEnv[env] = val
		}
	}
	defer func() {
		for _, env := range envVars {
			os.Unsetenv(env)
		}
		for env, val := range originalEnv {
			os.Setenv(env, val)
		}
	}()

	t.Run("endpoint_and_enabled_true", func(t *testing.T) {
		os.Setenv("AGENTSHIELD_OTEL_ENDPOINT", "https://otel.test:4318")
		os.Setenv("AGENTSHIELD_OTEL_ENABLED", "true")

		cfg := &Config{}
		applyEnvOverrides(cfg)

		if cfg.Telemetry.Endpoint != "https://otel.test:4318" {
			t.Errorf("expected endpoint 'https://otel.test:4318', got %q", cfg.Telemetry.Endpoint)
		}
		if !cfg.Telemetry.Enabled {
			t.Error("expected Telemetry.Enabled = true")
		}
	})

	t.Run("enabled_with_1", func(t *testing.T) {
		os.Setenv("AGENTSHIELD_OTEL_ENABLED", "1")

		cfg := &Config{}
		applyEnvOverrides(cfg)

		if !cfg.Telemetry.Enabled {
			t.Error("expected Telemetry.Enabled = true when env is '1'")
		}
	})

	t.Run("enabled_with_false", func(t *testing.T) {
		os.Setenv("AGENTSHIELD_OTEL_ENABLED", "false")

		cfg := &Config{}
		cfg.Telemetry.Enabled = true // pre-set to true
		applyEnvOverrides(cfg)

		if cfg.Telemetry.Enabled {
			t.Error("expected Telemetry.Enabled = false when env is 'false'")
		}
	})
}
