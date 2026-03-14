package config

import (
	"os"
	"path/filepath"
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