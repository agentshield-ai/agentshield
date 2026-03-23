# AgentShield

Real-time security monitoring for AI agents. Evaluates every tool call against [Sigma rules](https://sigmahq.io/) and blocks threats before they execute.

![Go Version](https://img.shields.io/badge/Go-1.24+-blue)
![License](https://img.shields.io/badge/License-Apache%202.0-green)
![Build Status](https://github.com/agentshield-ai/agentshield/actions/workflows/bench.yml/badge.svg)

```
Agent tool call → AgentShield engine → Sigma rule match? → block / require approval / allow / log
```

## Install

```bash
openclaw skill install agentshield-ai/agentshield
```

This downloads the engine binary, clones the Sigma rule corpus, generates an auth token, and starts the engine as a background service. Restart your OpenClaw session afterwards.

### Verify

```bash
agentshield status
```

## Build from Source

**Prerequisites:** Go 1.24+, Make, Git

```bash
git clone https://github.com/agentshield-ai/agentshield.git
cd agentshield
make deps     # go mod tidy && download
make build    # binary → bin/agentshield
make test     # run all tests
```

Start the engine:

```bash
./bin/agentshield serve -rules ./rules -config config.yaml
```

### Docker

```bash
make docker-build    # build image: agentshield-engine:test
```

## How It Works

AgentShield sits between an AI agent and its tools. Every tool call (shell commands, file writes, network requests) is intercepted by a platform plugin and sent to the detection engine. The engine evaluates the call against 45+ Sigma rules covering prompt injection, data exfiltration, privilege escalation, credential theft, and more.

```
┌─────────────┐     ┌──────────────────────────────────────────────────┐     ┌────────────┐
│   Plugin     │     │                   Engine                        │     │   Rules    │
│              │────▶│  Cache ──▶ Sigma Eval ──▶ Triage (optional LLM) │────▶│  45+ Sigma │
│  OpenClaw    │     │                                                 │     │  patterns  │
│              │◀────│  Action: block / require_approval / allow / log │     └────────────┘
└─────────────┘     └──────────────────────────────────────────────────┘
```

### Evaluation Modes

| Mode        | Behaviour                                                       |
|-------------|-----------------------------------------------------------------|
| **enforce** | Block on critical/high, require approval on medium, allow rest  |
| **audit**   | Log all alerts, never block                                     |
| **shadow**  | Silent monitoring, nothing surfaced                             |

### Session Behavioural Sequencing

AgentShield tracks tool-call sequences per session using an in-memory sliding window. Derived fields (`session.recent_tools`, `session.tool_count`, `session.unique_tool_count`, `session.alert_count`) are injected into the Sigma evaluation context, enabling temporal detection rules like "recon followed by exfiltration" or "high tool-call velocity".

### Verdict Caching

Identical tool calls are cached (LRU, 10k entries, 5-min TTL) to avoid re-evaluation. Cache is invalidated automatically on rule hot-reload (`SIGHUP`).

### OpenTelemetry Export

Evaluation telemetry can be exported as OTel traces and metrics to any OTLP-compatible backend (Jaeger, Grafana Tempo, Elastic, Datadog, Splunk). Each evaluation becomes a span with tool name, verdict, session context, and rule match events. Metrics include evaluation counts, alert counts, and cache hit/miss rates.

## Configuration

```yaml
server:
  port: 8433
auth:
  token: "${AGENTSHIELD_AUTH_TOKEN}"
rules:
  dir: "./rules"
  hot_reload: true
evaluation_mode: "audit"  # enforce, audit, shadow
triage:
  enabled: true
  provider: "openai"
  model: "gpt-4o-mini"
cache:
  max_entries: 10000
  ttl: "5m"
session:
  enabled: true
  window_sec: 900
  max_events: 50
telemetry:
  enabled: true
  endpoint: "https://otel-collector.example.com:4318"
  service_name: "agentshield"
  sample_rate: 1.0
  insecure: false
```

Config is loaded from `./config.yaml`, `~/.agentshield/config.yaml`, or `/etc/agentshield/config.yaml` (first found wins). See [docs/configuration.md](docs/configuration.md) for all options.

### Key Environment Variables

| Variable                   | Default | Purpose                    |
|----------------------------|---------|----------------------------|
| `AGENTSHIELD_PORT`         | 8433    | Server port                |
| `AGENTSHIELD_AUTH_TOKEN`   | —       | API bearer token           |
| `AGENTSHIELD_RULES_DIR`    | —       | Rules directory path       |
| `AGENTSHIELD_DB_PATH`      | —       | SQLite database path       |
| `AGENTSHIELD_MODE`         | —       | Evaluation mode            |
| `AGENTSHIELD_LOG_LEVEL`    | —       | Logging level              |
| `AGENTSHIELD_TRIAGE_API_KEY` | —    | LLM triage API key         |
| `AGENTSHIELD_OTEL_ENDPOINT`  | —    | OTel OTLP endpoint URL     |
| `AGENTSHIELD_OTEL_ENABLED`   | —    | Enable OTel export (true/false) |

## Development

### Running Tests

```bash
# All Go tests
make test

# Single package
go test -v ./internal/engine/...

# Single test function
go test -v -run TestEvaluate_PromptInjection ./internal/evaluate/...

# OpenClaw plugin
cd plugins/openclaw && npm install && npm test

# Integration tests (requires running engine)
cd plugins/openclaw && npm run test:integration
```

### Benchmarks

Requires the engine running on `localhost:8433`:

```bash
make bench SUITE=bench/suites/benign.yaml   # single suite
make bench-all                                # all suites
```

Suites live in `bench/suites/`, test cases in `bench/testcases/`.

### Project Layout

```
cmd/agentshield/        Entry point for the engine binary
cmd/agentshieldbench/   Benchmark runner
internal/
  server/               HTTP routes and middleware (Chi router)
  engine/               Sigma rule loading and evaluation
  evaluate/             Event evaluation orchestration
  cache/                LRU verdict cache with TTL
  triage/               LLM providers (OpenAI, Anthropic)
  store/                SQLite repository layer
  config/               YAML parsing and validation
  auth/                 Constant-time token comparison
  feedback/             Feedback collection and rule refinement
  daemon/               Service lifecycle, graceful shutdown
  telemetry/            OpenTelemetry traces and metrics export
  session/              Per-session behavioural sequencing
pkg/sigma/              Forked sigmalite library
plugins/
  openclaw/             TypeScript plugin (circuit-breaker pattern)
rules/rules/ai_agent/   45+ Sigma detection rules
docs/                   API reference, deployment, configuration guides
bench/                  Benchmark suites and test cases
```

### Hot Reloading Rules

Reload rules without restarting the engine:

```bash
kill -SIGHUP $(pgrep agentshield)
# or
agentshield rules reload
```

This also invalidates the verdict cache.

## API

`POST /api/v1/evaluate` — evaluate a tool call:

```bash
curl -s -X POST http://127.0.0.1:8433/api/v1/evaluate \
  -H "Authorization: Bearer $AGENTSHIELD_AUTH_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "event_id": "test-1",
    "session_id": "s1",
    "tool": "exec",
    "args": {"command": "ls -la"},
    "fields": {"event_type": "tool_call", "command": "ls -la"}
  }' | jq .action
# → "allow"
```

See [docs/api.md](docs/api.md) for the full API reference.

## Detection Rules

Rules are vendored from [agentshield-ai/sigma-ai](https://github.com/agentshield-ai/sigma-ai) via git subtree. Coverage includes:

- **Prompt injection** — direct, indirect, and exfiltration-oriented
- **Tool poisoning** — MCP config manipulation, rug pulls, tool substitution
- **Data exfiltration** — HTTP, DNS tunnelling, steganographic, LOTL techniques
- **Privilege escalation** — sudo abuse, container escapes, cloud IAM
- **Credential access** — token theft, keychain access, env var enumeration
- **Persistence** — shell config modification, cron jobs, rules-file backdoors

Browse all rules under [`rules/rules/ai_agent/`](rules/rules/ai_agent/). See [docs/rules.md](docs/rules.md) for authoring guidance.

## Platform Support

| Platform | Engine | Detection Rules |
|----------|--------|-----------------|
| Linux    | Yes    | Yes (primary)   |
| macOS    | Yes    | Yes             |
| Windows  | Yes    | Not yet — [contributions welcome](PLATFORMS.md) |

See [PLATFORMS.md](PLATFORMS.md) for details.

## Documentation

- [API Reference](docs/api.md) — endpoints, request/response examples
- [Configuration](docs/configuration.md) — all config options
- [Deployment Guide](docs/deployment.md) — production setup
- [Triage System](docs/triage.md) — LLM-powered alert analysis
- [Rules Guide](docs/rules.md) — authoring and testing rules
- [Contributing](CONTRIBUTING.md) — development setup, PR process

## Support

- **Issues** — [bug reports and feature requests](https://github.com/agentshield-ai/agentshield/issues)
- **Discussions** — [architecture and usage questions](https://github.com/agentshield-ai/agentshield/discussions)
- **Security** — security@agentshield.ai

## Licence

Apache 2.0 — see [LICENSE](LICENSE).

Built on RunReveal's [sigmalite](https://github.com/runreveal/sigmalite) (Apache 2.0).
