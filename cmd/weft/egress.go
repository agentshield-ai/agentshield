package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"github.com/agentshield-ai/agentshield/internal/weft/egress"
	"github.com/agentshield-ai/agentshield/internal/weft/evals"
	"github.com/agentshield-ai/agentshield/internal/weft/server"
)

// egressFlags configure which internal destinations outbound requests made
// for users and projects (remote scorers and models, webhooks, OAuth token
// endpoints, LLM provider base URLs, reference-bucket tests) may reach.
type egressFlags struct {
	allow        *string
	allowPrivate *bool
	insecureHTTP *bool
	bucketEnv    *bool
}

func registerEgressFlags() *egressFlags {
	priv, _ := strconv.ParseBool(strings.TrimSpace(os.Getenv("WEFT_EGRESS_ALLOW_PRIVATE")))
	insecure := envBool("WEFT_REMOTE_SCORER_ALLOW_INSECURE_HTTP") || envBool("WF_SCORING_WORKER_REMOTE_SCORER_ALLOW_INSECURE_HTTP")
	return &egressFlags{
		bucketEnv: flag.Bool("ref-buckets-env-credentials", envBool("WEFT_REF_BUCKETS_ENV_CREDENTIALS"),
			"let S3 reference buckets registered without secret_name use the server's AWS_* environment credentials (AWS endpoints only; env WEFT_REF_BUCKETS_ENV_CREDENTIALS=true)"),
		insecureHTTP: flag.Bool("remote-scorer-allow-insecure-http", insecure,
			"allow http:// RemoteScorer URLs; https is required otherwise (env WEFT_REMOTE_SCORER_ALLOW_INSECURE_HTTP=true, or Weave's WF_SCORING_WORKER_REMOTE_SCORER_ALLOW_INSECURE_HTTP)"),
		allow: flag.String("egress-allow", os.Getenv("WEFT_EGRESS_ALLOW"),
			"comma-separated destinations outbound requests may reach even when internal: host, host:port, *.domain, IP or CIDR, "+
				"e.g. localhost:11434,10.0.0.0/8 (env WEFT_EGRESS_ALLOW). Loopback, private, link-local, CGNAT and cloud metadata addresses are refused otherwise"),
		allowPrivate: flag.Bool("egress-allow-private", priv,
			"allow outbound requests to loopback, private, link-local and CGNAT addresses; cloud metadata addresses stay blocked (env WEFT_EGRESS_ALLOW_PRIVATE=true)"),
	}
}

// operatorBaseURLEnv are provider base URLs read from the server's own
// environment. They are operator configuration, like the command line, so
// their host:port is allowed automatically. The same names stored as project
// or server secrets are not.
var operatorBaseURLEnv = []string{"OPENAI_BASE_URL", "ANTHROPIC_BASE_URL", "OLLAMA_BASE_URL"}

// apply installs the process-wide egress policy and returns a description
// for the startup log.
func (f *egressFlags) apply(log *slog.Logger) (string, error) {
	allow := egress.ParseList(*f.allow)
	for _, name := range operatorBaseURLEnv {
		if hp := egress.HostPortOf(os.Getenv(name)); hp != "" {
			allow = append(allow, hp)
			log.Info("egress: allowing operator-configured provider base URL", "env", name, "host", hp)
		}
	}
	p, err := egress.New(egress.Config{Allow: allow, AllowPrivate: *f.allowPrivate, Logger: log})
	if err != nil {
		return "", fmt.Errorf("--egress-allow: %w", err)
	}
	egress.SetDefault(p)
	evals.AllowInsecureRemoteScorerHTTP = f.insecureHTTP != nil && *f.insecureHTTP
	server.RefBucketsEnvCredentials = f.bucketEnv != nil && *f.bucketEnv
	if evals.AllowInsecureRemoteScorerHTTP {
		log.Warn("remote scorers may use http:// URLs (--remote-scorer-allow-insecure-http)")
	}
	switch {
	case *f.allowPrivate:
		return "private-allowed", nil
	case len(allow) > 0:
		return "public+allowlist", nil
	}
	return "public-only", nil
}

// envBool reads a boolean environment variable ("true", "1", ...); unset or
// invalid values are false.
func envBool(name string) bool {
	v, _ := strconv.ParseBool(strings.TrimSpace(os.Getenv(name)))
	return v
}
