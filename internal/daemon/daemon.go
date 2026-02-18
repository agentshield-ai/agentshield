package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/agentshield-ai/agentshield/internal/config"
	"github.com/agentshield-ai/agentshield/internal/engine"
	"github.com/agentshield-ai/agentshield/internal/evaluate"
	"github.com/agentshield-ai/agentshield/internal/server"
	"github.com/agentshield-ai/agentshield/internal/store"
	"github.com/agentshield-ai/agentshield/internal/triage"
)

// Daemon manages the AgentShield server process
type Daemon struct {
	config    *config.Config
	pidFile   string
	logger    *slog.Logger
	engine    *engine.Engine
	store     *store.Store
	triager   *triage.Triager
	evaluator *evaluate.Evaluator
	server    *server.Server
}

// NewDaemon creates a new daemon instance
func NewDaemon(cfg *config.Config) (*Daemon, error) {
	// Initialize slog with the configured log level
	var level slog.Level
	switch strings.ToLower(cfg.LogLevel) {
	case "debug":
		level = slog.LevelDebug
	case "info":
		level = slog.LevelInfo
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: level,
	}))

	// Set the default logger for the package
	slog.SetDefault(logger)

	// Determine PID file path
	pidFile := "/tmp/agentshield.pid"
	if cfg.Store.SQLitePath != "" {
		// Place PID file next to database
		dir := filepath.Dir(cfg.Store.SQLitePath)
		pidFile = filepath.Join(dir, "agentshield.pid")
	}

	return &Daemon{
		config:  cfg,
		pidFile: pidFile,
		logger:  logger,
	}, nil
}

// Start starts the daemon
func (d *Daemon) Start() error {
	d.logger.Info("Starting AgentShield Engine v1.0.0")
	d.logger.Info("Configuration", "config", d.logConfig())

	// Check if already running
	if d.isRunning() {
		return fmt.Errorf("daemon already running (PID file exists: %s)", d.pidFile)
	}

	// Write PID file
	if err := d.writePIDFile(); err != nil {
		return fmt.Errorf("writing PID file: %w", err)
	}

	// Ensure PID file is cleaned up on exit
	defer d.removePIDFile()

	// Initialize components
	if err := d.initComponents(); err != nil {
		return fmt.Errorf("initializing components: %w", err)
	}

	// Setup signal handlers
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)

	// Start server in a goroutine
	serverErr := make(chan error, 1)
	go func() {
		serverErr <- d.server.Start()
	}()

	// Wait for signals or server error
	for {
		select {
		case err := <-serverErr:
			if err != nil {
				d.logger.Error("Server error", "error", err)
				return err
			}
			return nil

		case sig := <-sigChan:
			switch sig {
			case syscall.SIGHUP:
				d.logger.Info("Received SIGHUP, reloading rules...")
				if err := d.reloadRules(); err != nil {
					d.logger.Error("Failed to reload rules", "error", err)
				} else {
					d.logger.Info("Rules reloaded successfully")
				}

			case syscall.SIGTERM, syscall.SIGINT:
				d.logger.Info("Shutting down gracefully", "signal", sig.String())
				return d.shutdown()
			}
		}
	}
}

// Stop stops a running daemon by sending SIGTERM
func (d *Daemon) Stop() error {
	pid, err := d.readPIDFile()
	if err != nil {
		return fmt.Errorf("daemon not running: %w", err)
	}

	process, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("finding process %d: %w", pid, err)
	}

	// Send SIGTERM
	if err := process.Signal(syscall.SIGTERM); err != nil {
		return fmt.Errorf("sending SIGTERM to process %d: %w", pid, err)
	}

	d.logger.Info("Sent SIGTERM to process", "pid", pid)
	return nil
}

// Reload sends SIGHUP to reload rules
func (d *Daemon) Reload() error {
	pid, err := d.readPIDFile()
	if err != nil {
		return fmt.Errorf("daemon not running: %w", err)
	}

	process, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("finding process %d: %w", pid, err)
	}

	// Send SIGHUP
	if err := process.Signal(syscall.SIGHUP); err != nil {
		return fmt.Errorf("sending SIGHUP to process %d: %w", pid, err)
	}

	d.logger.Info("Sent SIGHUP to process", "pid", pid)
	return nil
}

// Status checks if the daemon is running
func (d *Daemon) Status() error {
	if !d.isRunning() {
		return fmt.Errorf("daemon not running")
	}

	pid, err := d.readPIDFile()
	if err != nil {
		return err
	}

	d.logger.Info("Daemon running", "pid", pid)
	return nil
}

// initComponents initializes all daemon components
func (d *Daemon) initComponents() error {
	d.logger.Info("Initializing components...")

	// Initialize engine
	eng, err := engine.NewEngine(d.config.Rules.Dir)
	if err != nil {
		return fmt.Errorf("initializing engine: %w", err)
	}
	d.engine = eng
	d.logger.Info("Engine initialized", "rule_count", len(eng.GetLoadedRules()))

	// Initialize store
	st, err := store.NewStore(d.config.Store.SQLitePath)
	if err != nil {
		return fmt.Errorf("initializing store: %w", err)
	}
	d.store = st
	d.logger.Info("Store initialized", "path", d.config.Store.SQLitePath)

	// Initialize triage (if enabled)
	var triager *triage.Triager
	if d.config.Triage.Enabled {
		triager, err = triage.NewTriager(&d.config.Triage, d.store)
		if err != nil {
			d.logger.Warn("Failed to initialize triage", "error", err)
		} else {
			d.logger.Info("Triage initialized", "provider", d.config.Triage.Provider)
		}
	} else {
		d.logger.Info("Triage disabled")
	}
	d.triager = triager

	// Initialize deep triage (if enabled)
	var deepTriager *triage.DeepTriager
	if d.config.DeepTriage.Enabled {
		deepTriager, err = triage.NewDeepTriager(&d.config.DeepTriage)
		if err != nil {
			d.logger.Warn("Failed to initialize deep triage", "error", err)
		} else {
			d.logger.Info("Deep triage initialized", "min_severity", d.config.DeepTriage.MinSeverity)
		}
	}

	// Initialize evaluator
	feedbackURL := fmt.Sprintf("http://%s/api/v1/feedback", d.config.ListenAddr())
	d.evaluator = evaluate.NewEvaluator(d.engine, d.config.EvaluationMode, feedbackURL, triager, deepTriager)
	d.logger.Info("Evaluator initialized", "mode", string(d.config.EvaluationMode))

	// Initialize server
	srv, err := server.NewServer(d.config, d.evaluator, d.store, triager)
	if err != nil {
		return fmt.Errorf("initializing server: %w", err)
	}
	d.server = srv
	d.logger.Info("Server initialized", "listen_addr", d.config.ListenAddr())

	return nil
}

// reloadRules reloads the rule engine
func (d *Daemon) reloadRules() error {
	if d.engine == nil {
		return fmt.Errorf("engine not initialized")
	}

	return d.engine.LoadRules()
}

// shutdown gracefully shuts down all components
func (d *Daemon) shutdown() error {
	d.logger.Info("Starting graceful shutdown...")

	// Create shutdown context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Shutdown server
	if d.server != nil {
		if err := d.server.Shutdown(ctx); err != nil {
			d.logger.Error("Server shutdown error", "error", err)
		} else {
			d.logger.Info("Server shut down successfully")
		}
	}

	// Close store
	if d.store != nil {
		if err := d.store.Close(); err != nil {
			d.logger.Error("Store close error", "error", err)
		} else {
			d.logger.Info("Store closed successfully")
		}
	}

	d.logger.Info("Graceful shutdown completed")
	return nil
}

// isRunning checks if the daemon is currently running
func (d *Daemon) isRunning() bool {
	pid, err := d.readPIDFile()
	if err != nil {
		return false
	}

	// Check if process exists
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}

	// Send signal 0 to check if process is alive
	err = process.Signal(syscall.Signal(0))
	return err == nil
}

// writePIDFile writes the current process ID to the PID file
func (d *Daemon) writePIDFile() error {
	pid := os.Getpid()
	
	// Ensure directory exists
	dir := filepath.Dir(d.pidFile)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating PID directory: %w", err)
	}

	// Write PID to file
	pidStr := strconv.Itoa(pid)
	if err := os.WriteFile(d.pidFile, []byte(pidStr), 0644); err != nil {
		return fmt.Errorf("writing PID file: %w", err)
	}

	d.logger.Info("Written PID file", "pid", pid, "path", d.pidFile)
	return nil
}

// readPIDFile reads the PID from the PID file
func (d *Daemon) readPIDFile() (int, error) {
	data, err := os.ReadFile(d.pidFile)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, fmt.Errorf("PID file does not exist")
		}
		return 0, fmt.Errorf("reading PID file: %w", err)
	}

	pidStr := strings.TrimSpace(string(data))
	pid, err := strconv.Atoi(pidStr)
	if err != nil {
		return 0, fmt.Errorf("parsing PID from file: %w", err)
	}

	return pid, nil
}

// removePIDFile removes the PID file
func (d *Daemon) removePIDFile() {
	if err := os.Remove(d.pidFile); err != nil && !os.IsNotExist(err) {
		d.logger.Error("Failed to remove PID file", "error", err)
	} else {
		d.logger.Info("Removed PID file", "path", d.pidFile)
	}
}

// logConfig returns a summary of the configuration for logging
func (d *Daemon) logConfig() string {
	return fmt.Sprintf("addr=%s, mode=%s, rules=%s, db=%s, auth=%t",
		d.config.ListenAddr(),
		d.config.EvaluationMode,
		d.config.Rules.Dir,
		d.config.Store.SQLitePath,
		d.config.Auth.Token != "",
	)
}