package main

import (
	"flag"
	"log/slog"
	"os"
	"time"

	"github.com/agentshield-ai/agentshield/internal/weft/server"
	"github.com/agentshield-ai/agentshield/internal/weft/shield"
	"github.com/agentshield-ai/agentshield/internal/weft/store"
)

// shieldFlags configure AgentShield replay (docs/weft/DESIGN.md §5.4).
type shieldFlags struct {
	rulesDir, rulesRoots, url, token, timeout *string
}

func registerShieldFlags() *shieldFlags {
	return &shieldFlags{
		rulesDir: flag.String("shield-rules-dir", os.Getenv("WEFT_SHIELD_RULES_DIR"),
			"AgentShield Sigma rules directory for in-process replay of recorded tool calls (env WEFT_SHIELD_RULES_DIR)"),
		rulesRoots: flag.String("shield-rules-roots", os.Getenv("WEFT_SHIELD_RULES_ROOTS"),
			"comma-separated directories under which a replay request may name its own rules_dir (env WEFT_SHIELD_RULES_ROOTS)"),
		url: flag.String("shield-url", os.Getenv("WEFT_SHIELD_URL"),
			"a running AgentShield engine to replay against instead of in-process, e.g. http://127.0.0.1:8433 (env WEFT_SHIELD_URL); operator configuration, not subject to the egress guard"),
		token: flag.String("shield-token", os.Getenv("WEFT_SHIELD_TOKEN"),
			"bearer token for --shield-url (env WEFT_SHIELD_TOKEN; prefer the env var, flags show in process lists)"),
		timeout: flag.String("shield-timeout", envOr("WEFT_SHIELD_TIMEOUT", "10s"), "time budget per remote evaluation (env WEFT_SHIELD_TIMEOUT)"),
	}
}

// apply installs the replay service on srv and returns its mode for the
// startup log ("off", "in-process" or "remote").
func (f *shieldFlags) apply(srv *server.Server, st *store.Store, log *slog.Logger) (string, error) {
	timeout, err := time.ParseDuration(*f.timeout)
	if err != nil || timeout <= 0 {
		timeout = 10 * time.Second
	}
	svc, err := shield.New(shield.Config{RulesDir: *f.rulesDir, RulesRoots: splitList(*f.rulesRoots), URL: *f.url, Token: *f.token,
		Timeout: timeout, Logger: log}, st)
	if err != nil {
		return "", err
	}
	srv.SetShield(svc)
	if m := svc.Mode(); m != "" {
		return m, nil
	}
	return "off", nil
}
