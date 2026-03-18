package daemon

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/agentshield-ai/agentshield/internal/config"
)

// TestNewDaemon tests daemon creation with various configurations
func TestNewDaemon(t *testing.T) {
	t.Run("valid config", func(t *testing.T) {
		cfg := &config.Config{
			LogLevel: "info",
			Store: config.StoreConfig{
				SQLitePath: "/tmp/test.db",
			},
		}

		daemon, err := NewDaemon(cfg)
		if err != nil {
			t.Fatalf("NewDaemon() error = %v", err)
		}

		if daemon.config != cfg {
			t.Error("Expected config to be set")
		}

		expectedPidFile := "/tmp/agentshield.pid"
		if daemon.pidFile != expectedPidFile {
			t.Errorf("Expected PID file %s, got %s", expectedPidFile, daemon.pidFile)
		}

		if daemon.logger == nil {
			t.Error("Expected logger to be initialized")
		}

		if !daemon.managePIDFile {
			t.Error("Expected PID file management to be enabled by default")
		}
	})

	t.Run("custom database path affects PID file location", func(t *testing.T) {
		tmpDir := t.TempDir()
		dbPath := filepath.Join(tmpDir, "custom.db")

		cfg := &config.Config{
			LogLevel: "debug",
			Store: config.StoreConfig{
				SQLitePath: dbPath,
			},
		}

		daemon, err := NewDaemon(cfg)
		if err != nil {
			t.Fatalf("NewDaemon() error = %v", err)
		}

		expectedPidFile := filepath.Join(tmpDir, "agentshield.pid")
		if daemon.pidFile != expectedPidFile {
			t.Errorf("Expected PID file %s, got %s", expectedPidFile, daemon.pidFile)
		}
	})

	t.Run("different log levels", func(t *testing.T) {
		logLevels := []string{"debug", "info", "warn", "error", "invalid"}

		for _, level := range logLevels {
			t.Run(level, func(t *testing.T) {
				cfg := &config.Config{
					LogLevel: level,
					Store: config.StoreConfig{
						SQLitePath: "/tmp/test.db",
					},
				}

				daemon, err := NewDaemon(cfg)
				if err != nil {
					t.Fatalf("NewDaemon() error = %v for log level %s", err, level)
				}

				if daemon.logger == nil {
					t.Error("Expected logger to be initialized")
				}
			})
		}
	})
}

func TestDaemonPIDOptions(t *testing.T) {
	cfg := &config.Config{
		LogLevel: "info",
		Store: config.StoreConfig{
			SQLitePath: "/tmp/test.db",
		},
	}

	daemon, err := NewDaemon(cfg)
	if err != nil {
		t.Fatalf("NewDaemon() error = %v", err)
	}

	daemon.SetPIDFile(" /tmp/custom-agentshield.pid ")
	if daemon.pidFile != "/tmp/custom-agentshield.pid" {
		t.Fatalf("expected custom PID file to be applied, got %q", daemon.pidFile)
	}

	daemon.SetPIDFileManagement(false)
	if daemon.managePIDFile {
		t.Fatal("expected PID management to be disabled")
	}
}

// TestPIDFileOperations tests PID file read/write/remove operations
func TestPIDFileOperations(t *testing.T) {
	t.Run("write and read PID file", func(t *testing.T) {
		tmpDir := t.TempDir()
		pidFile := filepath.Join(tmpDir, "test.pid")

		cfg := &config.Config{
			LogLevel: "info",
			Store: config.StoreConfig{
				SQLitePath: "/tmp/test.db",
			},
		}

		daemon, err := NewDaemon(cfg)
		if err != nil {
			t.Fatal(err)
		}
		daemon.pidFile = pidFile

		// Write PID file
		err = daemon.writePIDFile()
		if err != nil {
			t.Fatalf("writePIDFile() error = %v", err)
		}

		// Check file exists
		if _, err := os.Stat(pidFile); os.IsNotExist(err) {
			t.Error("PID file should exist after writing")
		}

		// Read PID file
		pid, err := daemon.readPIDFile()
		if err != nil {
			t.Fatalf("readPIDFile() error = %v", err)
		}

		expectedPID := os.Getpid()
		if pid != expectedPID {
			t.Errorf("Expected PID %d, got %d", expectedPID, pid)
		}

		// Remove PID file
		daemon.removePIDFile()

		// Check file is removed
		if _, err := os.Stat(pidFile); !os.IsNotExist(err) {
			t.Error("PID file should not exist after removal")
		}
	})

	t.Run("write PID file creates directory", func(t *testing.T) {
		tmpDir := t.TempDir()
		nestedDir := filepath.Join(tmpDir, "nested", "directory")
		pidFile := filepath.Join(nestedDir, "test.pid")

		cfg := &config.Config{
			LogLevel: "info",
			Store: config.StoreConfig{
				SQLitePath: "/tmp/test.db",
			},
		}

		daemon, err := NewDaemon(cfg)
		if err != nil {
			t.Fatal(err)
		}
		daemon.pidFile = pidFile

		// Write PID file (should create directories)
		err = daemon.writePIDFile()
		if err != nil {
			t.Fatalf("writePIDFile() error = %v", err)
		}

		// Check directory was created
		if _, err := os.Stat(nestedDir); os.IsNotExist(err) {
			t.Error("Expected directory to be created")
		}

		// Check PID file exists
		if _, err := os.Stat(pidFile); os.IsNotExist(err) {
			t.Error("PID file should exist")
		}
	})

	t.Run("read non-existent PID file", func(t *testing.T) {
		tmpDir := t.TempDir()
		pidFile := filepath.Join(tmpDir, "nonexistent.pid")

		cfg := &config.Config{
			LogLevel: "info",
			Store: config.StoreConfig{
				SQLitePath: "/tmp/test.db",
			},
		}

		daemon, err := NewDaemon(cfg)
		if err != nil {
			t.Fatal(err)
		}
		daemon.pidFile = pidFile

		_, err = daemon.readPIDFile()
		if err == nil {
			t.Error("Expected error when reading non-existent PID file")
		}

		if !strings.Contains(err.Error(), "PID file does not exist") {
			t.Errorf("Expected specific error message, got: %v", err)
		}
	})

	t.Run("read corrupted PID file", func(t *testing.T) {
		tmpDir := t.TempDir()
		pidFile := filepath.Join(tmpDir, "corrupted.pid")

		// Write invalid PID content
		err := os.WriteFile(pidFile, []byte("not-a-number"), 0644)
		if err != nil {
			t.Fatal(err)
		}

		cfg := &config.Config{
			LogLevel: "info",
			Store: config.StoreConfig{
				SQLitePath: "/tmp/test.db",
			},
		}

		daemon, err := NewDaemon(cfg)
		if err != nil {
			t.Fatal(err)
		}
		daemon.pidFile = pidFile

		_, err = daemon.readPIDFile()
		if err == nil {
			t.Error("Expected error when reading corrupted PID file")
		}

		if !strings.Contains(err.Error(), "parsing PID from file") {
			t.Errorf("Expected specific error message, got: %v", err)
		}
	})

	t.Run("remove non-existent PID file", func(t *testing.T) {
		tmpDir := t.TempDir()
		pidFile := filepath.Join(tmpDir, "nonexistent.pid")

		cfg := &config.Config{
			LogLevel: "info",
			Store: config.StoreConfig{
				SQLitePath: "/tmp/test.db",
			},
		}

		daemon, err := NewDaemon(cfg)
		if err != nil {
			t.Fatal(err)
		}
		daemon.pidFile = pidFile

		// Should not panic or error when removing non-existent file
		daemon.removePIDFile()
	})
}

// TestIsRunning tests the daemon running check
func TestIsRunning(t *testing.T) {
	t.Run("daemon not running - no PID file", func(t *testing.T) {
		tmpDir := t.TempDir()
		pidFile := filepath.Join(tmpDir, "nonexistent.pid")

		cfg := &config.Config{
			LogLevel: "info",
			Store: config.StoreConfig{
				SQLitePath: "/tmp/test.db",
			},
		}

		daemon, err := NewDaemon(cfg)
		if err != nil {
			t.Fatal(err)
		}
		daemon.pidFile = pidFile

		if daemon.isRunning() {
			t.Error("Expected daemon to not be running when PID file doesn't exist")
		}
	})

	t.Run("daemon not running - invalid PID", func(t *testing.T) {
		tmpDir := t.TempDir()
		pidFile := filepath.Join(tmpDir, "invalid.pid")

		// Write invalid PID
		err := os.WriteFile(pidFile, []byte("invalid"), 0644)
		if err != nil {
			t.Fatal(err)
		}

		cfg := &config.Config{
			LogLevel: "info",
			Store: config.StoreConfig{
				SQLitePath: "/tmp/test.db",
			},
		}

		daemon, err := NewDaemon(cfg)
		if err != nil {
			t.Fatal(err)
		}
		daemon.pidFile = pidFile

		if daemon.isRunning() {
			t.Error("Expected daemon to not be running with invalid PID")
		}
	})

	t.Run("daemon running - current process", func(t *testing.T) {
		tmpDir := t.TempDir()
		pidFile := filepath.Join(tmpDir, "running.pid")

		// Write current process PID
		currentPID := os.Getpid()
		err := os.WriteFile(pidFile, []byte(strconv.Itoa(currentPID)), 0644)
		if err != nil {
			t.Fatal(err)
		}

		cfg := &config.Config{
			LogLevel: "info",
			Store: config.StoreConfig{
				SQLitePath: "/tmp/test.db",
			},
		}

		daemon, err := NewDaemon(cfg)
		if err != nil {
			t.Fatal(err)
		}
		daemon.pidFile = pidFile

		if !daemon.isRunning() {
			t.Error("Expected daemon to be running with current process PID")
		}
	})
}

// TestLogConfig tests the configuration logging function
func TestLogConfig(t *testing.T) {
	tests := []struct {
		name     string
		config   *config.Config
		contains []string
	}{
		{
			name: "basic config",
			config: &config.Config{
				Server: config.ServerConfig{
					Addr: "127.0.0.1",
					Port: 8433,
				},
				EvaluationMode: config.ModeEnforce,
				Rules: config.RulesConfig{
					Dir: "./rules",
				},
				Store: config.StoreConfig{
					SQLitePath: "./test.db",
				},
				Auth: config.AuthConfig{
					Token: "test-token",
				},
			},
			contains: []string{
				"addr=127.0.0.1:8433",
				"mode=enforce",
				"rules=./rules",
				"db=./test.db",
				"auth=true",
			},
		},
		{
			name: "no auth token",
			config: &config.Config{
				Server: config.ServerConfig{
					Addr: "0.0.0.0",
					Port: 9000,
				},
				EvaluationMode: config.ModeAudit,
				Rules: config.RulesConfig{
					Dir: "/etc/rules",
				},
				Store: config.StoreConfig{
					SQLitePath: "/var/lib/test.db",
				},
				Auth: config.AuthConfig{
					Token: "", // No token
				},
			},
			contains: []string{
				"addr=0.0.0.0:9000",
				"mode=audit",
				"rules=/etc/rules",
				"db=/var/lib/test.db",
				"auth=false",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			daemon, err := NewDaemon(tt.config)
			if err != nil {
				t.Fatal(err)
			}

			logString := daemon.logConfig()

			for _, expected := range tt.contains {
				if !strings.Contains(logString, expected) {
					t.Errorf("Expected log config to contain '%s', got: %s", expected, logString)
				}
			}
		})
	}
}

// TestInitComponents tests component initialization
func TestInitComponents(t *testing.T) {
	t.Run("successful initialization", func(t *testing.T) {
		// Create temporary directory with test rules
		tmpDir := t.TempDir()
		rulesDir := filepath.Join(tmpDir, "rules")
		os.MkdirAll(rulesDir, 0755)

		// Create a basic Sigma rule for testing
		ruleContent := `title: Test Rule
id: test-rule-001
description: Test rule for daemon initialization
detection:
    selection:
        test: value
    condition: selection
level: high
`
		ruleFile := filepath.Join(rulesDir, "test.yml")
		os.WriteFile(ruleFile, []byte(ruleContent), 0644)

		dbPath := filepath.Join(tmpDir, "test.db")

		cfg := &config.Config{
			LogLevel:       "info",
			EvaluationMode: config.ModeEnforce,
			Rules: config.RulesConfig{
				Dir: rulesDir,
			},
			Store: config.StoreConfig{
				SQLitePath: dbPath,
			},
			Server: config.ServerConfig{
				Addr: "127.0.0.1",
				Port: 8433,
			},
			Auth: config.AuthConfig{
				Token: "test-token-12345678901234567890123456", // Valid length
			},
			Triage: config.TriageConfig{
				Enabled: false, // Disabled for testing
			},
			DeepTriage: config.DeepTriageConfig{
				Enabled: false, // Disabled for testing
			},
		}

		daemon, err := NewDaemon(cfg)
		if err != nil {
			t.Fatal(err)
		}

		err = daemon.initComponents()
		if err != nil {
			t.Fatalf("initComponents() error = %v", err)
		}

		// Check that all components were initialized
		if daemon.engine == nil {
			t.Error("Expected engine to be initialized")
		}

		if daemon.store == nil {
			t.Error("Expected store to be initialized")
		}

		if daemon.evaluator == nil {
			t.Error("Expected evaluator to be initialized")
		}

		if daemon.server == nil {
			t.Error("Expected server to be initialized")
		}

		// Clean up
		if daemon.store != nil {
			daemon.store.Close()
		}
	})

	t.Run("initialization failure - invalid rules directory", func(t *testing.T) {
		cfg := &config.Config{
			LogLevel: "info",
			Rules: config.RulesConfig{
				Dir: "/nonexistent/rules/directory",
			},
			Store: config.StoreConfig{
				SQLitePath: ":memory:",
			},
		}

		daemon, err := NewDaemon(cfg)
		if err != nil {
			t.Fatal(err)
		}

		err = daemon.initComponents()
		if err == nil {
			t.Error("Expected error when initializing with invalid rules directory")
		}

		if !strings.Contains(err.Error(), "initializing engine") {
			t.Errorf("Expected engine initialization error, got: %v", err)
		}
	})

	t.Run("triage enabled but fails", func(t *testing.T) {
		tmpDir := t.TempDir()
		rulesDir := filepath.Join(tmpDir, "rules")
		os.MkdirAll(rulesDir, 0755)
		dbPath := filepath.Join(tmpDir, "test.db")

		cfg := &config.Config{
			LogLevel:       "info",
			EvaluationMode: config.ModeEnforce,
			Rules: config.RulesConfig{
				Dir: rulesDir,
			},
			Store: config.StoreConfig{
				SQLitePath: dbPath,
			},
			Server: config.ServerConfig{
				Addr: "127.0.0.1",
				Port: 8433,
			},
			Auth: config.AuthConfig{
				Token: "test-token-12345678901234567890123456",
			},
			Triage: config.TriageConfig{
				Enabled:  true,
				Provider: "invalid-provider", // This should cause a warning
			},
			DeepTriage: config.DeepTriageConfig{
				Enabled: false,
			},
		}

		daemon, err := NewDaemon(cfg)
		if err != nil {
			t.Fatal(err)
		}

		// This should succeed but log a warning about triage failing
		err = daemon.initComponents()
		if err != nil {
			t.Fatalf("initComponents() should succeed even with triage failure: %v", err)
		}

		// Triage should be nil due to failure
		if daemon.triager != nil {
			t.Error("Expected triager to be nil when initialization fails")
		}

		// But other components should still work
		if daemon.engine == nil {
			t.Error("Expected engine to be initialized despite triage failure")
		}

		// Clean up
		if daemon.store != nil {
			daemon.store.Close()
		}
	})
}

// TestReloadRules tests rule reloading
func TestReloadRules(t *testing.T) {
	t.Run("engine not initialized", func(t *testing.T) {
		cfg := &config.Config{
			LogLevel: "info",
		}

		daemon, err := NewDaemon(cfg)
		if err != nil {
			t.Fatal(err)
		}

		err = daemon.reloadRules()
		if err == nil {
			t.Error("Expected error when engine not initialized")
		}

		if !strings.Contains(err.Error(), "engine not initialized") {
			t.Errorf("Expected specific error message, got: %v", err)
		}
	})
}

// TestShutdown tests graceful shutdown
func TestShutdown(t *testing.T) {
	t.Run("shutdown with initialized components", func(t *testing.T) {
		tmpDir := t.TempDir()
		rulesDir := filepath.Join(tmpDir, "rules")
		os.MkdirAll(rulesDir, 0755)
		dbPath := filepath.Join(tmpDir, "test.db")

		cfg := &config.Config{
			LogLevel:       "info",
			EvaluationMode: config.ModeEnforce,
			Rules: config.RulesConfig{
				Dir: rulesDir,
			},
			Store: config.StoreConfig{
				SQLitePath: dbPath,
			},
			Server: config.ServerConfig{
				Addr: "127.0.0.1",
				Port: 8433,
			},
			Auth: config.AuthConfig{
				Token: "test-token-12345678901234567890123456",
			},
			Triage: config.TriageConfig{
				Enabled: false,
			},
			DeepTriage: config.DeepTriageConfig{
				Enabled: false,
			},
		}

		daemon, err := NewDaemon(cfg)
		if err != nil {
			t.Fatal(err)
		}

		// Initialize components first
		err = daemon.initComponents()
		if err != nil {
			t.Fatal(err)
		}

		// Manually set httpServer to nil to simulate the issue
		// In reality, httpServer is only set when Start() is called
		daemon.server = nil

		// Shutdown should complete without error when server is nil
		err = daemon.shutdown()
		if err != nil {
			t.Errorf("shutdown() error = %v", err)
		}
	})

	t.Run("shutdown with no components", func(t *testing.T) {
		cfg := &config.Config{
			LogLevel: "info",
		}

		daemon, err := NewDaemon(cfg)
		if err != nil {
			t.Fatal(err)
		}

		// Shutdown should complete without error even with no components
		err = daemon.shutdown()
		if err != nil {
			t.Errorf("shutdown() error = %v", err)
		}
	})
}

// TestDaemonLifecycle tests a more complete daemon lifecycle
func TestDaemonLifecycle(t *testing.T) {
	t.Run("check status when not running", func(t *testing.T) {
		tmpDir := t.TempDir()
		pidFile := filepath.Join(tmpDir, "test.pid")

		cfg := &config.Config{
			LogLevel: "info",
			Store: config.StoreConfig{
				SQLitePath: "/tmp/test.db",
			},
		}

		daemon, err := NewDaemon(cfg)
		if err != nil {
			t.Fatal(err)
		}
		daemon.pidFile = pidFile

		err = daemon.Status()
		if err == nil {
			t.Error("Expected error when checking status of non-running daemon")
		}

		if !strings.Contains(err.Error(), "daemon not running") {
			t.Errorf("Expected specific error message, got: %v", err)
		}
	})

	t.Run("stop non-running daemon", func(t *testing.T) {
		tmpDir := t.TempDir()
		pidFile := filepath.Join(tmpDir, "test.pid")

		cfg := &config.Config{
			LogLevel: "info",
			Store: config.StoreConfig{
				SQLitePath: "/tmp/test.db",
			},
		}

		daemon, err := NewDaemon(cfg)
		if err != nil {
			t.Fatal(err)
		}
		daemon.pidFile = pidFile

		err = daemon.Stop()
		if err == nil {
			t.Error("Expected error when stopping non-running daemon")
		}

		if !strings.Contains(err.Error(), "daemon not running") {
			t.Errorf("Expected specific error message, got: %v", err)
		}
	})

	t.Run("reload non-running daemon", func(t *testing.T) {
		tmpDir := t.TempDir()
		pidFile := filepath.Join(tmpDir, "test.pid")

		cfg := &config.Config{
			LogLevel: "info",
			Store: config.StoreConfig{
				SQLitePath: "/tmp/test.db",
			},
		}

		daemon, err := NewDaemon(cfg)
		if err != nil {
			t.Fatal(err)
		}
		daemon.pidFile = pidFile

		err = daemon.Reload()
		if err == nil {
			t.Error("Expected error when reloading non-running daemon")
		}

		if !strings.Contains(err.Error(), "daemon not running") {
			t.Errorf("Expected specific error message, got: %v", err)
		}
	})
}

func TestInitComponents_TelemetryDisabledHasNoopShutdown(t *testing.T) {
	tmpDir := t.TempDir()
	rulesDir := filepath.Join(tmpDir, "rules")
	os.MkdirAll(rulesDir, 0755)
	ruleFile := filepath.Join(rulesDir, "test.yml")
	os.WriteFile(ruleFile, []byte("title: Test\nid: t1\ndescription: test\ndetection:\n    selection:\n        test: val\n    condition: selection\nlevel: high\n"), 0644)
	dbPath := filepath.Join(tmpDir, "test.db")

	cfg := &config.Config{
		LogLevel:       "info",
		EvaluationMode: config.ModeEnforce,
		Rules:          config.RulesConfig{Dir: rulesDir},
		Store:          config.StoreConfig{SQLitePath: dbPath},
		Server:         config.ServerConfig{Addr: "127.0.0.1", Port: 18433},
		Auth:           config.AuthConfig{Token: "test-token-12345678901234567890123456"},
		Triage:         config.TriageConfig{Enabled: false},
		DeepTriage:     config.DeepTriageConfig{Enabled: false},
		Telemetry:      config.TelemetryConfig{Enabled: false},
	}

	daemon, err := NewDaemon(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := daemon.initComponents(); err != nil {
		t.Fatalf("initComponents: %v", err)
	}
	// Set server to nil to avoid panic on Shutdown (httpServer not started)
	daemon.server = nil
	defer daemon.shutdown()

	if daemon.telemetryShutdown == nil {
		t.Error("expected non-nil telemetryShutdown even when disabled")
	}
}

func TestShutdown_CallsTelemetryShutdown(t *testing.T) {
	called := false
	d := &Daemon{
		logger: slog.Default(),
		telemetryShutdown: func(_ context.Context) error {
			called = true
			return nil
		},
	}
	d.shutdown()
	if !called {
		t.Error("expected telemetryShutdown to be called during shutdown")
	}
}
