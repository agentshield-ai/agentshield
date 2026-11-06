// Command weft runs the Weft trace server: an OpenTelemetry-native LLM
// tracing and evaluation backend with a built-in web UI.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/agentshield-ai/agentshield/internal/weft/compat"
	"github.com/agentshield-ai/agentshield/internal/weft/server"
	"github.com/agentshield-ai/agentshield/internal/weft/store"
	"github.com/agentshield-ai/agentshield/internal/weft/weftlog"
	"github.com/agentshield-ai/agentshield/internal/weft/webui"
)

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func defaultDB() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".weft", "weft.db")
	}
	return "weft.db"
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

const usageHeader = `usage: weft [flags]
       weft db <command> ...   (see weft db --help)

Weft trace server: OpenTelemetry ingest, the /api/v1 API and the web UI.

Flags:
`

const usageLogs = `
Logs (docs/weft/guides/setup.md, "Logs"):
  Weft logs to stderr (or --log-file) with log/slog, as key=value text or
  JSON (--log-format). Lines carry ids, names, counts, sizes, durations and
  reasons only: never keys, prompts, tool arguments or outputs, or bodies.
  info   startup; one "request" line per write, refusal (4xx) and server
         error (5xx, at error); one line per ingest batch ("otlp logs
         ingested", "otlp traces ingested", "shield verdicts ingested");
         session source changes, interrupted turns, replays, evaluations,
         automations, retention errors.
  debug  adds reads and the UI's files, the sessions each batch touched
         (session_ids), each turn rebuilt from log events and how each
         AgentShield verdict attached (tool call id, nearest time, or why not).
  Follow one Claude Code session: --log-level debug, then filter on its
  session id (grep <session id>: batch lines list it in session_ids,
  turn and verdict lines carry session_id=).
`

func main() {
	if len(os.Args) > 1 && os.Args[1] == "db" {
		os.Exit(runDB(os.Args[2:], os.Stdout, os.Stderr)) // db.go
	}
	addr := flag.String("addr", envOr("WEFT_ADDR", "127.0.0.1:6345"), "listen address (env WEFT_ADDR)")
	dbPath := flag.String("db", envOr("WEFT_DB", defaultDB()), "SQLite database path (env WEFT_DB)")
	apiKeys := flag.String("api-keys", os.Getenv("WEFT_API_KEYS"), "comma-separated API keys required on requests (env WEFT_API_KEYS); empty disables auth")
	cors := flag.String("cors-origins", os.Getenv("WEFT_CORS_ORIGINS"), "comma-separated browser origins allowed to call the API (env WEFT_CORS_ORIGINS)")
	noUI := flag.Bool("no-ui", false, "do not serve the web UI")
	logLevel := flag.String("log-level", envOr("WEFT_LOG_LEVEL", "info"),
		"log level: debug, info, warn or error (env WEFT_LOG_LEVEL). info: one line per write, ingest batch and refusal, server errors, "+
			"background jobs; debug adds reads, the UI's files, the sessions each batch touched, turn rebuilds and how each AgentShield verdict attached")
	logFormat := flag.String("log-format", envOr("WEFT_LOG_FORMAT", weftlog.FormatText), "log format: text (key=value) or json, one object per line (env WEFT_LOG_FORMAT)")
	logFile := flag.String("log-file", os.Getenv("WEFT_LOG_FILE"), "append logs to this file instead of stderr (env WEFT_LOG_FILE)")
	filesCfg := registerFilesFlags()   // files.go
	iamCfg := registerIAMFlags()       // iam.go
	piiCfg := registerPIIFlags()       // pii.go
	egressCfg := registerEgressFlags() // egress.go
	shieldCfg := registerShieldFlags() // shield.go
	compatFlag := flag.Bool(compat.FlagName, false,
		"turn on the compatibility adapter for the official weave client, see docs/weft/COMPATIBILITY.md (env "+compat.EnvName+"=true)")

	dbKeyCfg := registerDBKeyFlags(flag.CommandLine) // db.go
	flag.Usage = func() {
		fmt.Fprint(flag.CommandLine.Output(), usageHeader)
		flag.PrintDefaults()
		fmt.Fprint(flag.CommandLine.Output(), usageLogs)
	}
	flag.Parse()
	compatOn := compat.Enabled(flag.CommandLine, *compatFlag)
	var compatAliases []string
	if compatOn {
		compatAliases = compat.ApplyEnvAliases(flag.CommandLine, os.Getenv)
	}

	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(*logLevel)); err != nil {
		fmt.Fprintf(os.Stderr, "invalid log level %q\n", *logLevel)
		os.Exit(2)
	}
	var logOut io.Writer = os.Stderr
	if *logFile != "" {
		f, err := os.OpenFile(*logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			fmt.Fprintf(os.Stderr, "opening log file: %v\n", err)
			os.Exit(2)
		}
		defer f.Close()
		logOut = f
	}
	handler, err := weftlog.NewHandler(logOut, *logFormat, lvl)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	log := slog.New(handler)
	slog.SetDefault(log)
	for _, a := range compatAliases {
		log.Info("compatibility adapter: read setting from environment alias", "env", a)
	}
	if os.Getenv("WEFT_LEGACY_ROUTES") != "" {
		// The temporary --legacy-routes switch is gone (COMPATIBILITY.md, Route shapes).
		log.Warn("WEFT_LEGACY_ROUTES is no longer read: the older route shapes are served only by the compatibility adapter (--" + compat.FlagName + ")")
	}

	if dir := filepath.Dir(*dbPath); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			log.Error("creating database directory", "err", err)
			os.Exit(1)
		}
	}
	if err := store.PrepareDBFile(*dbPath); err != nil {
		log.Error("creating database file", "err", err)
		os.Exit(1)
	}
	var dbKey string
	dbKey, err = dbKeyCfg.resolve()
	if err != nil {
		log.Error("database encryption key", "err", err)
		os.Exit(2)
	}
	for _, w := range keyWarnings(dbKey, *dbKeyCfg.keyFile) {
		log.Warn("database encryption key", "problem", w)
	}
	st, err := store.OpenWith(*dbPath, store.OpenOptions{EncryptionKey: dbKey})
	if err != nil {
		log.Error("opening database", "err", err)
		os.Exit(1)
	}
	defer st.Close()
	filesBackend, err := filesCfg.apply(st)
	if err != nil {
		log.Error("configuring files backend", "err", err)
		os.Exit(2)
	}

	piiMode, err := piiCfg.apply(st)
	if err != nil {
		log.Error("configuring the PII analyzer", "err", err)
		os.Exit(2)
	}

	egressMode, err := egressCfg.apply(log)
	if err != nil {
		log.Error("configuring outbound request restrictions", "err", err)
		os.Exit(2)
	}

	permMsg := "database file permissions: the database is not encrypted at rest; restrict access, use an encrypted volume or set --db-encryption-key-file"
	if st.Encrypted() {
		permMsg = "database file permissions: restrict access to the database files"
	}
	for _, w := range store.DBPermissionWarnings(*dbPath) {
		log.Warn(permMsg, "problem", w)
	}

	cfg := server.Config{APIKeys: splitList(*apiKeys), CORSOrigins: splitList(*cors), Logger: log, Compat: compatOn}
	if err := iamCfg.apply(&cfg, *dbPath, log); err != nil {
		log.Error("configuring SSO and sessions", "err", err)
		os.Exit(2)
	}
	if !*noUI {
		cfg.UI = webui.Handler()
	}
	srv := server.New(cfg, st)
	defer srv.Close()
	shieldMode, err := shieldCfg.apply(srv, st, log)
	if err != nil {
		log.Error("configuring AgentShield replay", "err", err)
		os.Exit(2)
	}

	if len(cfg.APIKeys) == 0 {
		if host, _, err := net.SplitHostPort(*addr); err == nil {
			if ip := net.ParseIP(host); host == "" || (ip != nil && !ip.IsLoopback()) {
				log.Warn("authentication is disabled and the server listens on a non-loopback address; set --api-keys")
			}
		}
	}

	httpSrv := &http.Server{Addr: *addr, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutdown)
	}()
	projects, err := st.CountProjects(context.Background())
	if err != nil {
		log.Warn("counting projects", "err", err)
	}
	log.Info("weft listening", "version", server.Version, "addr", "http://"+*addr, "db", *dbPath, "db_encrypted", st.Encrypted(),
		"auth", srv.AuthRequired(), "static_keys", len(cfg.APIKeys), "sso", cfg.IAM.OIDC != nil, "projects", projects, "ui", cfg.UI != nil,
		"files", filesBackend, "pii", piiMode, "egress", egressMode, "shield_replay", shieldMode, "weave_compat", compatOn,
		"log_level", lvl.String(), "log_format", strings.ToLower(*logFormat))
	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("server failed", "err", err)
		os.Exit(1)
	}
}
