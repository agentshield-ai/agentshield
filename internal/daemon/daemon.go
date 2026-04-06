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

	"github.com/agentshield-ai/agentshield/internal/cache"
	"github.com/agentshield-ai/agentshield/internal/config"
	"github.com/agentshield-ai/agentshield/internal/engine"
	"github.com/agentshield-ai/agentshield/internal/evaluate"
	"github.com/agentshield-ai/agentshield/internal/server"
	"github.com/agentshield-ai/agentshield/internal/session"
	"github.com/agentshield-ai/agentshield/internal/store"
	"github.com/agentshield-ai/agentshield/internal/telemetry"
	"github.com/agentshield-ai/agentshield/internal/triage"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/trace"
)

// Daemon manages the AgentShield server process
type Daemon struct {
	config            *config.Config
	pidFile           string
	managePIDFile     bool
	logger            *slog.Logger
	engine            *engine.Engine
	store             *store.Store
	triager           *triage.Triager
	evaluator         *evaluate.Evaluator
	server            *server.Server
	verdictCache         *cache.VerdictCache
	sessionRegistry      *session.Registry
	sessionCleanupCancel func()
	tracerProvider       trace.TracerProvider
	meterProvider        *sdkmetric.MeterProvider
	retentionCancel      context.CancelFunc
	telemetryShutdown    telemetry.ShutdownFunc
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
		config:        cfg,
		pidFile:       pidFile,
		managePIDFile: true,
		logger:        logger,
	}, nil
}

// SetPIDFile overrides the default PID file path.
func (d *Daemon) SetPIDFile(path string) {
	if trimmed := strings.TrimSpace(path); trimmed != "" {
		d.pidFile = trimmed
	}
}

// SetPIDFileManagement toggles PID file lifecycle management.
func (d *Daemon) SetPIDFileManagement(enabled bool) {
	d.managePIDFile = enabled
}

// Start starts the daemon
func (d *Daemon) Start() error {
	d.logger.Info("Starting AgentShield Engine v1.0.0")
	d.logger.Info("Configuration", "config", d.logConfig())

	if d.managePIDFile {
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
	} else {
		d.logger.Info("PID file management disabled for this run")
	}

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

// sendSignal reads the PID file, locates the process, and delivers the given
// signal. sigName is used in log/error messages to preserve readable output.
func (d *Daemon) sendSignal(sig syscall.Signal, sigName string) error {
	pid, err := d.readPIDFile()
	if err != nil {
		return fmt.Errorf("daemon not running: %w", err)
	}

	process, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("finding process %d: %w", pid, err)
	}

	if err := process.Signal(sig); err != nil {
		return fmt.Errorf("sending %s to process %d: %w", sigName, pid, err)
	}

	d.logger.Info("Sent "+sigName+" to process", "pid", pid)
	return nil
}

// Stop stops a running daemon by sending SIGTERM
func (d *Daemon) Stop() error {
	return d.sendSignal(syscall.SIGTERM, "SIGTERM")
}

// Reload sends SIGHUP to reload rules
func (d *Daemon) Reload() error {
	return d.sendSignal(syscall.SIGHUP, "SIGHUP")
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

	// Initialise OpenTelemetry
	tp, otelShutdown, err := telemetry.Init(context.Background(), &d.config.Telemetry, "dev")
	if err != nil {
		return fmt.Errorf("initialising telemetry: %w", err)
	}
	d.tracerProvider = tp
	d.telemetryShutdown = otelShutdown
	if d.config.Telemetry.Enabled {
		d.logger.Info("Telemetry initialized", "endpoint", d.config.Telemetry.Endpoint)
	} else {
		d.logger.Info("Telemetry disabled")
	}

	// Initialize store
	st, err := store.NewStore(d.config.Store.SQLitePath)
	if err != nil {
		return fmt.Errorf("initializing store: %w", err)
	}
	d.store = st
	d.logger.Info("Store initialized", "path", d.config.Store.SQLitePath)

	if d.config.Store.RetentionDays > 0 {
		deleted, err := d.store.EnforceRetention(d.config.Store.RetentionDays)
		if err != nil {
			d.logger.Warn("Initial retention cleanup failed", "error", err)
		} else {
			d.logger.Info("Initial retention cleanup complete", "deleted_alerts", deleted, "retention_days", d.config.Store.RetentionDays)
		}
		d.startRetentionLoop()
	} else {
		d.logger.Info("Retention cleanup disabled", "retention_days", d.config.Store.RetentionDays)
	}

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

	// Initialize verdict cache
	if d.config.Cache.Enabled {
		ttl := time.Duration(d.config.Cache.TTLSec) * time.Second
		d.verdictCache = cache.NewVerdictCache(d.config.Cache.MaxSize, ttl)
		d.evaluator.SetCache(d.verdictCache)
		d.logger.Info("Verdict cache initialized", "max_size", d.config.Cache.MaxSize, "ttl_sec", d.config.Cache.TTLSec)
	} else {
		d.logger.Info("Verdict cache disabled")
	}

	// Initialize session registry for behavioural sequencing
	if d.config.Session.Enabled {
		ttl := time.Duration(d.config.Session.WindowSec) * time.Second
		d.sessionRegistry = session.NewRegistry(d.config.Session.MaxEvents, ttl)
		d.evaluator.SetSessionRegistry(d.sessionRegistry)
		d.sessionCleanupCancel = d.sessionRegistry.StartCleanupLoop(1 * time.Minute)
		d.logger.Info("Session sequencing enabled",
			"window_sec", d.config.Session.WindowSec,
			"max_events", d.config.Session.MaxEvents,
		)
	} else {
		d.logger.Info("Session sequencing disabled")
	}

	// Initialize metrics
	mp, err := telemetry.InitMeter(context.Background(), &d.config.Telemetry)
	if err != nil {
		return fmt.Errorf("initialising metrics: %w", err)
	}
	d.meterProvider = mp
	if mp != nil {
		metricsRec, err := telemetry.NewMetricsRecorder(mp.Meter("agentshield"))
		if err != nil {
			return fmt.Errorf("creating metrics recorder: %w", err)
		}
		d.evaluator.SetMetrics(metricsRec)
		d.logger.Info("OTel metrics initialized")
	}

	// Wire tracer into evaluator for evaluation-level spans
	if d.config.Telemetry.Enabled && d.tracerProvider != nil {
		d.evaluator.SetTracer(d.tracerProvider.Tracer("agentshield"))
	}

	// Initialize server
	srv, err := server.NewServer(d.config, d.evaluator, d.store, d.verdictCache)
	if err != nil {
		return fmt.Errorf("initializing server: %w", err)
	}
	if d.config.Telemetry.Enabled && d.tracerProvider != nil {
		srv.SetTracerProvider(d.tracerProvider)
	}
	if d.sessionRegistry != nil {
		srv.SetSessionRegistry(d.sessionRegistry)
	}
	d.server = srv
	d.logger.Info("Server initialized", "listen_addr", d.config.ListenAddr())

	return nil
}

// reloadRules reloads the rule engine and invalidates the verdict cache.
func (d *Daemon) reloadRules() error {
	if d.engine == nil {
		return fmt.Errorf("engine not initialized")
	}

	if err := d.engine.LoadRules(); err != nil {
		return err
	}

	// Invalidate cached verdicts so stale results are not served
	if d.verdictCache != nil {
		d.verdictCache.Invalidate()
		d.logger.Info("Verdict cache invalidated after rule reload")
	}

	return nil
}

func (d *Daemon) startRetentionLoop() {
	intervalHours := d.config.Store.CleanupIntervalHours
	if intervalHours <= 0 {
		intervalHours = 24
	}
	interval := time.Duration(intervalHours) * time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	d.retentionCancel = cancel

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				deleted, err := d.store.EnforceRetention(d.config.Store.RetentionDays)
				if err != nil {
					d.logger.Warn("Scheduled retention cleanup failed", "error", err)
					continue
				}
				d.logger.Info("Scheduled retention cleanup complete", "deleted_alerts", deleted, "retention_days", d.config.Store.RetentionDays)
			}
		}
	}()

	d.logger.Info("Started retention cleanup loop", "interval_hours", intervalHours)
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

	// Flush and shutdown telemetry providers
	if d.telemetryShutdown != nil {
		d.shutdownComponent("Telemetry", d.telemetryShutdown)
	}
	if d.meterProvider != nil {
		d.shutdownComponent("MeterProvider", d.meterProvider.Shutdown)
	}

	if d.retentionCancel != nil {
		d.retentionCancel()
		d.retentionCancel = nil
	}

	if d.sessionCleanupCancel != nil {
		d.sessionCleanupCancel()
		d.sessionCleanupCancel = nil
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

// shutdownComponent shuts down a component with a 5-second timeout.
func (d *Daemon) shutdownComponent(name string, shutdown func(context.Context) error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := shutdown(ctx); err != nil {
		d.logger.Error(name+" shutdown error", "error", err)
	}
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

// writePIDFile writes the current process ID to the PID file atomically.
// Uses O_CREATE|O_EXCL to prevent TOCTOU race conditions.
func (d *Daemon) writePIDFile() error {
	pid := os.Getpid()

	// Ensure directory exists
	dir := filepath.Dir(d.pidFile)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating PID directory: %w", err)
	}

	// Atomic create — fails if file already exists (prevents TOCTOU race)
	f, err := os.OpenFile(d.pidFile, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("PID file already exists: %s (another instance may be running)", d.pidFile)
		}
		return fmt.Errorf("creating PID file: %w", err)
	}

	pidStr := strconv.Itoa(pid)
	if _, err := f.WriteString(pidStr); err != nil {
		f.Close()
		os.Remove(d.pidFile)
		return fmt.Errorf("writing PID file: %w", err)
	}

	if err := f.Close(); err != nil {
		return fmt.Errorf("closing PID file: %w", err)
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
	return fmt.Sprintf("addr=%s, mode=%s, rules=%s, db=%s, retention_days=%d, auth=%t",
		d.config.ListenAddr(),
		d.config.EvaluationMode,
		d.config.Rules.Dir,
		d.config.Store.SQLitePath,
		d.config.Store.RetentionDays,
		d.config.Auth.Token != "",
	)
}
