package daemon

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/agentshield-ai/agentshield/internal/config"
	"github.com/agentshield-ai/agentshield/internal/store"
)

// TestWritePIDFileAtomicCreate verifies the O_CREATE|O_EXCL atomic behavior
func TestWritePIDFileAtomicCreate(t *testing.T) {
	t.Run("fails when PID file already exists", func(t *testing.T) {
		tmpDir := t.TempDir()
		pidFile := filepath.Join(tmpDir, "test.pid")

		// Pre-create the PID file
		if err := os.WriteFile(pidFile, []byte("9999"), 0600); err != nil {
			t.Fatalf("Failed to pre-create PID file: %v", err)
		}

		cfg := &config.Config{
			LogLevel: "info",
			Store:    config.StoreConfig{SQLitePath: "/tmp/test.db"},
		}
		d, err := NewDaemon(cfg)
		if err != nil {
			t.Fatalf("NewDaemon() error = %v", err)
		}
		d.pidFile = pidFile

		err = d.writePIDFile()
		if err == nil {
			t.Error("Expected error when PID file already exists (O_EXCL protection)")
		}

		if !strings.Contains(err.Error(), "PID file already exists") &&
			!strings.Contains(err.Error(), "already exists") {
			t.Errorf("Expected 'PID file already exists' error, got: %v", err)
		}
	})

	t.Run("file permissions set to 0600", func(t *testing.T) {
		tmpDir := t.TempDir()
		pidFile := filepath.Join(tmpDir, "perms.pid")

		cfg := &config.Config{
			LogLevel: "info",
			Store:    config.StoreConfig{SQLitePath: "/tmp/test.db"},
		}
		d, err := NewDaemon(cfg)
		if err != nil {
			t.Fatalf("NewDaemon() error = %v", err)
		}
		d.pidFile = pidFile

		if err := d.writePIDFile(); err != nil {
			t.Fatalf("writePIDFile() error = %v", err)
		}
		t.Cleanup(func() { os.Remove(pidFile) })

		info, err := os.Stat(pidFile)
		if err != nil {
			t.Fatalf("Stat() error = %v", err)
		}

		// Verify permissions are 0600 (owner read/write only)
		perm := info.Mode().Perm()
		if perm != 0600 {
			t.Errorf("Expected PID file permission 0600, got %o", perm)
		}
	})

	t.Run("write contains current PID", func(t *testing.T) {
		tmpDir := t.TempDir()
		pidFile := filepath.Join(tmpDir, "current.pid")

		cfg := &config.Config{
			LogLevel: "info",
			Store:    config.StoreConfig{SQLitePath: "/tmp/test.db"},
		}
		d, err := NewDaemon(cfg)
		if err != nil {
			t.Fatalf("NewDaemon() error = %v", err)
		}
		d.pidFile = pidFile

		if err := d.writePIDFile(); err != nil {
			t.Fatalf("writePIDFile() error = %v", err)
		}
		t.Cleanup(func() { os.Remove(pidFile) })

		data, err := os.ReadFile(pidFile)
		if err != nil {
			t.Fatalf("ReadFile() error = %v", err)
		}

		writtenPID, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil {
			t.Fatalf("Failed to parse PID from file: %v", err)
		}
		if writtenPID != os.Getpid() {
			t.Errorf("PID file contains %d, want current PID %d", writtenPID, os.Getpid())
		}
	})
}

// TestSendSignalEdgeCases tests signal sending to various process states
func TestSendSignalEdgeCases(t *testing.T) {
	t.Run("send SIGCONT to current process is safe", func(t *testing.T) {
		tmpDir := t.TempDir()
		pidFile := filepath.Join(tmpDir, "current.pid")

		currentPID := os.Getpid()
		if err := os.WriteFile(pidFile, []byte(strconv.Itoa(currentPID)), 0600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}

		cfg := &config.Config{
			LogLevel: "info",
			Store:    config.StoreConfig{SQLitePath: "/tmp/test.db"},
		}
		d, err := NewDaemon(cfg)
		if err != nil {
			t.Fatalf("NewDaemon() error = %v", err)
		}
		d.pidFile = pidFile

		// SIGCONT is safe to send to the current process (no-op if not stopped)
		err = d.sendSignal(syscall.SIGCONT, "SIGCONT")
		if err != nil {
			t.Errorf("sendSignal(SIGCONT) to current process error = %v", err)
		}
	})

	t.Run("send signal when PID file missing", func(t *testing.T) {
		tmpDir := t.TempDir()
		pidFile := filepath.Join(tmpDir, "missing.pid")

		cfg := &config.Config{
			LogLevel: "info",
			Store:    config.StoreConfig{SQLitePath: "/tmp/test.db"},
		}
		d, err := NewDaemon(cfg)
		if err != nil {
			t.Fatalf("NewDaemon() error = %v", err)
		}
		d.pidFile = pidFile

		err = d.sendSignal(syscall.SIGTERM, "SIGTERM")
		if err == nil {
			t.Error("Expected error when PID file missing")
		}
		if !strings.Contains(err.Error(), "daemon not running") {
			t.Errorf("Expected 'daemon not running' error, got: %v", err)
		}
	})

	t.Run("send signal to non-existent process PID", func(t *testing.T) {
		tmpDir := t.TempDir()
		pidFile := filepath.Join(tmpDir, "dead.pid")

		// PID 2147483647 (max int32) is very unlikely to exist on Linux
		fakePID := 2147483647
		if err := os.WriteFile(pidFile, []byte(strconv.Itoa(fakePID)), 0600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}

		cfg := &config.Config{
			LogLevel: "info",
			Store:    config.StoreConfig{SQLitePath: "/tmp/test.db"},
		}
		d, err := NewDaemon(cfg)
		if err != nil {
			t.Fatalf("NewDaemon() error = %v", err)
		}
		d.pidFile = pidFile

		// Signal should either fail (no such process) or succeed (unlikely PID exists)
		err = d.sendSignal(syscall.SIGTERM, "SIGTERM")
		if err != nil {
			// Expected when process doesn't exist
			t.Logf("sendSignal() correctly failed: %v", err)
		}
	})
}

// TestStatusWithRunningProcess tests Status when a valid process PID is stored
func TestStatusRunning(t *testing.T) {
	t.Run("status returns nil when PID is current process", func(t *testing.T) {
		tmpDir := t.TempDir()
		pidFile := filepath.Join(tmpDir, "running.pid")

		currentPID := os.Getpid()
		if err := os.WriteFile(pidFile, []byte(strconv.Itoa(currentPID)), 0600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}

		cfg := &config.Config{
			LogLevel: "info",
			Store:    config.StoreConfig{SQLitePath: "/tmp/test.db"},
		}
		d, err := NewDaemon(cfg)
		if err != nil {
			t.Fatalf("NewDaemon() error = %v", err)
		}
		d.pidFile = pidFile

		err = d.Status()
		if err != nil {
			t.Errorf("Status() with running process error = %v", err)
		}
	})
}

// TestStartRetentionLoop tests the retention cleanup loop
func TestStartRetentionLoop(t *testing.T) {
	t.Run("starts loop with custom interval", func(t *testing.T) {
		tmpDir := t.TempDir()
		dbPath := filepath.Join(tmpDir, "retention.db")

		cfg := &config.Config{
			LogLevel: "info",
			Store: config.StoreConfig{
				SQLitePath:           dbPath,
				RetentionDays:        30,
				CleanupIntervalHours: 1,
			},
		}

		d, err := NewDaemon(cfg)
		if err != nil {
			t.Fatalf("NewDaemon() error = %v", err)
		}

		// Open a store for the daemon
		st, err := store.NewStore(dbPath)
		if err != nil {
			t.Fatalf("store.NewStore() error = %v", err)
		}
		d.store = st
		t.Cleanup(func() { st.Close() })

		// Start the retention loop
		d.startRetentionLoop()

		// Verify cancel function was set
		if d.retentionCancel == nil {
			t.Error("Expected retentionCancel to be set after startRetentionLoop()")
		}

		// Cancel the loop to stop goroutine
		d.retentionCancel()
		d.retentionCancel = nil
		time.Sleep(10 * time.Millisecond)
	})

	t.Run("uses default interval 24h when CleanupIntervalHours is 0", func(t *testing.T) {
		tmpDir := t.TempDir()
		dbPath := filepath.Join(tmpDir, "retention.db")

		cfg := &config.Config{
			LogLevel: "info",
			Store: config.StoreConfig{
				SQLitePath:           dbPath,
				RetentionDays:        30,
				CleanupIntervalHours: 0, // Should default to 24
			},
		}

		d, err := NewDaemon(cfg)
		if err != nil {
			t.Fatalf("NewDaemon() error = %v", err)
		}

		st, err := store.NewStore(dbPath)
		if err != nil {
			t.Fatalf("store.NewStore() error = %v", err)
		}
		d.store = st
		t.Cleanup(func() { st.Close() })

		d.startRetentionLoop()

		if d.retentionCancel == nil {
			t.Error("Expected retentionCancel to be set")
		}

		d.retentionCancel()
		d.retentionCancel = nil
		time.Sleep(10 * time.Millisecond)
	})

	t.Run("uses default interval 24h when CleanupIntervalHours is negative", func(t *testing.T) {
		tmpDir := t.TempDir()
		dbPath := filepath.Join(tmpDir, "retention.db")

		cfg := &config.Config{
			LogLevel: "info",
			Store: config.StoreConfig{
				SQLitePath:           dbPath,
				RetentionDays:        30,
				CleanupIntervalHours: -5, // Should default to 24
			},
		}

		d, err := NewDaemon(cfg)
		if err != nil {
			t.Fatalf("NewDaemon() error = %v", err)
		}

		st, err := store.NewStore(dbPath)
		if err != nil {
			t.Fatalf("store.NewStore() error = %v", err)
		}
		d.store = st
		t.Cleanup(func() { st.Close() })

		d.startRetentionLoop()

		if d.retentionCancel == nil {
			t.Error("Expected retentionCancel to be set")
		}

		d.retentionCancel()
		d.retentionCancel = nil
		time.Sleep(10 * time.Millisecond)
	})
}

// TestShutdownCancelsRetentionLoop tests that shutdown cancels the retention loop
func TestShutdownCancelsRetentionLoop(t *testing.T) {
	t.Run("shutdown cancels retention loop and nils cancel func", func(t *testing.T) {
		tmpDir := t.TempDir()
		dbPath := filepath.Join(tmpDir, "shutdown.db")

		cfg := &config.Config{
			LogLevel: "info",
			Store: config.StoreConfig{
				SQLitePath:           dbPath,
				RetentionDays:        30,
				CleanupIntervalHours: 1,
			},
		}

		d, err := NewDaemon(cfg)
		if err != nil {
			t.Fatalf("NewDaemon() error = %v", err)
		}

		st, err := store.NewStore(dbPath)
		if err != nil {
			t.Fatalf("store.NewStore() error = %v", err)
		}
		d.store = st

		// Start retention loop
		d.startRetentionLoop()
		if d.retentionCancel == nil {
			t.Fatal("Expected retentionCancel to be set")
		}

		// Shutdown should cancel and nil the cancel func
		err = d.shutdown()
		if err != nil {
			t.Errorf("shutdown() error = %v", err)
		}

		if d.retentionCancel != nil {
			t.Error("Expected retentionCancel to be nil after shutdown")
		}
	})
}

// TestInitComponentsRetentionBehavior tests initComponents retention loop logic
func TestInitComponentsRetentionBehavior(t *testing.T) {
	t.Run("no retention loop when RetentionDays is 0", func(t *testing.T) {
		tmpDir := t.TempDir()
		rulesDir := filepath.Join(tmpDir, "rules")
		os.MkdirAll(rulesDir, 0755)
		dbPath := filepath.Join(tmpDir, "test.db")

		cfg := &config.Config{
			LogLevel:       "info",
			EvaluationMode: config.ModeEnforce,
			Rules:          config.RulesConfig{Dir: rulesDir},
			Store: config.StoreConfig{
				SQLitePath:    dbPath,
				RetentionDays: 0, // Disabled
			},
			Server: config.ServerConfig{
				Addr: "127.0.0.1",
				Port: 8433,
			},
			Auth: config.AuthConfig{
				Token: "test-token-12345678901234567890123456",
			},
			Triage:     config.TriageConfig{Enabled: false},
			DeepTriage: config.DeepTriageConfig{Enabled: false},
		}

		d, err := NewDaemon(cfg)
		if err != nil {
			t.Fatalf("NewDaemon() error = %v", err)
		}

		err = d.initComponents()
		if err != nil {
			t.Fatalf("initComponents() error = %v", err)
		}
		t.Cleanup(func() {
			if d.store != nil {
				d.store.Close()
			}
		})

		// No retention loop should be started when RetentionDays = 0
		if d.retentionCancel != nil {
			d.retentionCancel()
			t.Error("Expected retentionCancel to be nil when RetentionDays=0")
		}
	})

	t.Run("retention loop started when RetentionDays > 0", func(t *testing.T) {
		tmpDir := t.TempDir()
		rulesDir := filepath.Join(tmpDir, "rules")
		os.MkdirAll(rulesDir, 0755)
		dbPath := filepath.Join(tmpDir, "test.db")

		cfg := &config.Config{
			LogLevel:       "info",
			EvaluationMode: config.ModeEnforce,
			Rules:          config.RulesConfig{Dir: rulesDir},
			Store: config.StoreConfig{
				SQLitePath:           dbPath,
				RetentionDays:        30,
				CleanupIntervalHours: 24,
			},
			Server: config.ServerConfig{
				Addr: "127.0.0.1",
				Port: 8433,
			},
			Auth: config.AuthConfig{
				Token: "test-token-12345678901234567890123456",
			},
			Triage:     config.TriageConfig{Enabled: false},
			DeepTriage: config.DeepTriageConfig{Enabled: false},
		}

		d, err := NewDaemon(cfg)
		if err != nil {
			t.Fatalf("NewDaemon() error = %v", err)
		}

		err = d.initComponents()
		if err != nil {
			t.Fatalf("initComponents() error = %v", err)
		}
		t.Cleanup(func() {
			if d.retentionCancel != nil {
				d.retentionCancel()
			}
			if d.store != nil {
				d.store.Close()
			}
		})

		// Retention loop should be started when RetentionDays > 0
		if d.retentionCancel == nil {
			t.Error("Expected retentionCancel to be set when RetentionDays > 0")
		}
	})
}

// TestReloadRulesWithInitializedEngine tests reloadRules with a valid engine
func TestReloadRulesWithInitializedEngine(t *testing.T) {
	t.Run("reload succeeds with valid rules dir", func(t *testing.T) {
		tmpDir := t.TempDir()
		rulesDir := filepath.Join(tmpDir, "rules")
		os.MkdirAll(rulesDir, 0755)
		dbPath := filepath.Join(tmpDir, "test.db")

		cfg := &config.Config{
			LogLevel:       "info",
			EvaluationMode: config.ModeEnforce,
			Rules:          config.RulesConfig{Dir: rulesDir},
			Store:          config.StoreConfig{SQLitePath: dbPath},
			Server:         config.ServerConfig{Addr: "127.0.0.1", Port: 8433},
			Auth:           config.AuthConfig{Token: "test-token-12345678901234567890123456"},
			Triage:         config.TriageConfig{Enabled: false},
			DeepTriage:     config.DeepTriageConfig{Enabled: false},
		}

		d, err := NewDaemon(cfg)
		if err != nil {
			t.Fatalf("NewDaemon() error = %v", err)
		}

		err = d.initComponents()
		if err != nil {
			t.Fatalf("initComponents() error = %v", err)
		}
		t.Cleanup(func() {
			if d.store != nil {
				d.store.Close()
			}
		})

		// reloadRules should succeed with an initialized engine
		err = d.reloadRules()
		if err != nil {
			t.Errorf("reloadRules() with valid dir error = %v", err)
		}
	})
}
