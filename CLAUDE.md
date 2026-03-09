# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

AgentShield is an AI agent security monitoring platform providing real-time threat detection using Sigma rules. It consists of a high-performance Go detection engine, TypeScript plugins for agent platforms (OpenClaw, Claude Code), and a corpus of Sigma-based detection rules.

## Build & Test Commands

### Go Engine

```bash
make build                    # Build engine binary → bin/agentshield
make test                     # Run all Go tests (go test -v ./...)
make deps                     # go mod tidy && go mod download
make run                      # Build and run engine
make clean                    # Remove build artifacts

# Single package test
go test -v ./internal/server/...
go test -v ./internal/engine/...

# Single test function
go test -v -run TestEvaluate_PromptInjection ./internal/evaluate/...
```

### OpenClaw Plugin (TypeScript)

```bash
cd plugins/openclaw
npm install
npm test                      # Unit tests (vitest)
npm run test:integration      # Integration tests (requires running engine)
```

### Benchmarks

```bash
make bench SUITE=bench/suites/benign.yaml      # Single suite
make bench-all                                  # All suites
```

Requires engine running on `localhost:8433`. Suites live in `bench/suites/`, test cases in `bench/testcases/`.

### Docker

```bash
make docker-build             # Build image: agentshield-engine:test
make test-integration-docker  # Integration tests against Docker engine
```

## Architecture

### Data Flow

```
Tool Call → Plugin Hook → Event Normaliser → HTTP POST /api/v1/evaluate
  → Verdict Cache Check → Sigma Rule Evaluation → Triage (optional, LLM-powered)
  → Action (block/require_approval/allow/log) → SQLite persistence → Response to plugin
```

### Go Engine (`cmd/`, `internal/`, `pkg/sigma/`)

- **HTTP server**: Chi router, bearer token auth, rate limiting
- **Detection**: Forked `runreveal/sigmalite` for Sigma rule evaluation
- **Triage**: Two-tier system — fast (synchronous LLM) + deep (async sub-agent analysis)
- **Storage**: Pure-Go SQLite via `modernc.org/sqlite` (no CGO)
- **Config**: YAML with env var substitution (`${AGENTSHIELD_AUTH_TOKEN}`). Search order: `./config.yaml` → `~/.agentshield/config.yaml` → `/etc/agentshield/config.yaml`
- **Hot reload**: `SIGHUP` or `agentshield rules reload` for zero-downtime rule updates

Internal packages follow clear separation:
- `server/` — HTTP routes and middleware
- `engine/` — Sigma rule loading and evaluation wrapper
- `evaluate/` — Event evaluation orchestration
- `cache/` — LRU verdict cache with TTL (invalidated on SIGHUP rule reload)
- `triage/` — LLM providers (OpenAI, Anthropic) for false-positive reduction
- `store/` — SQLite repository layer
- `config/` — YAML parsing and validation
- `auth/` — Constant-time token comparison
- `feedback/` — Feedback collection and rule refinement
- `daemon/` — Service lifecycle (graceful shutdown, signal handling)

### OpenClaw Plugin (`plugins/openclaw/`)

TypeScript plugin using circuit-breaker pattern for fault tolerance:
- `client.ts` — HTTP client to engine API
- `event-builder.ts` — Constructs evaluation requests from tool calls
- `normalise.ts` — Maps platform-specific tool names to canonical forms
- `circuit-breaker.ts` — Handles engine unavailability gracefully
- `config.ts` — Plugin config validation

### Sigma Rules (`rules/`)

Vendored from upstream `agentshield-ai/sigma-ai` via git subtree. Flat structure under `rules/rules/ai_agent/`. Three maturity levels:
- **stable** — Standard Sigma syntax, production-ready
- **test** — Uses custom extensions (temporal correlation, behavioural analysis)
- **experimental** — Heavily customised, expect changes

Sync script: `scripts/sync_sigma_ai.sh`. Validation: `scripts/check_sigma_ai_sync.sh`.

### Evaluation Actions

Four possible actions returned by the evaluation pipeline:
- **block** — Prevent tool execution (critical/high severity in enforce mode)
- **require_approval** — Prompt user for approval before proceeding (medium severity in enforce mode)
- **allow** — Permit execution (low/no severity, or shadow mode)
- **log** — Record alert but allow execution (audit mode)

### Evaluation Modes

- **enforce** — Block on critical/high, require approval on medium, allow others
- **audit** — Log all alerts, never block or require approval
- **shadow** — Silent monitoring, no alerts surfaced

### Verdict Caching

In-memory LRU cache (default 10,000 entries, 5-min TTL) avoids re-evaluating identical tool calls. Cache key is SHA-256 of tool name + sorted args. Cache is automatically invalidated on rule hot-reload (SIGHUP). Cache stats exposed on the health endpoint. Configured via `cache:` in config.yaml.

## Key Environment Variables

| Variable | Purpose |
|---|---|
| `AGENTSHIELD_PORT` | Server port (default 8433) |
| `AGENTSHIELD_AUTH_TOKEN` | API bearer token |
| `AGENTSHIELD_RULES_DIR` | Rules directory path |
| `AGENTSHIELD_DB_PATH` | SQLite database path |
| `AGENTSHIELD_MODE` | Evaluation mode (enforce/audit/shadow) |
| `AGENTSHIELD_LOG_LEVEL` | Logging level |
| `AGENTSHIELD_TRIAGE_API_KEY` | LLM triage API key |

## Platform Support

See [PLATFORMS.md](PLATFORMS.md) for full details. Detection rules target Linux/macOS (Unix/POSIX commands). The Go engine runs on Windows but no Windows-specific rules exist. All 45+ rules are under `rules/rules/ai_agent/` using `logsource.product: ai_agent`.

## Competitive Landscape

[Avast Sage](https://github.com/avast/sage) is the closest comparable project. Key differences:
- **AgentShield**: Go engine, Sigma rules, LLM triage, temporal correlation, prompt injection detection, `require_approval` graduated response, verdict caching
- **Sage**: TypeScript in-process, flat regex YAML rules, URL/package reputation APIs, supply-chain validation, Windows rules
- They are complementary: Sage excels at "is this command/URL/package dangerous?"; AgentShield excels at "is this agent being manipulated?"

### Planned (v1.1 branches, not yet merged)
- `feat/supply-chain-checks` — npm/PyPI registry validation for hallucinated/typosquatted packages
- `feat/reputation-lookups` — k-anonymity hash prefix URL/file reputation lookups

## Conventions

- **Go 1.24+**, no CGO. Standard library `log/slog` for structured logging.
- **Conventional commits**: `feat:`, `fix:`, `docs:`, `refactor:`, `test:`, `chore:`.
- **Branch naming**: `<type>/<short-description>` (e.g. `feat/new-rule`).
- **Security**: Constant-time auth comparison (`subtle.ConstantTimeCompare`), UTF-8 validation, length limits, and control character rejection on all input.
- **Tests**: Table-driven Go tests. Security-specific test files (`security_test.go`) in most packages. Integration tests that depend on project rules use `runtime.Caller` to find the project root and `t.Skip` if rules are absent.
- **NewServer signature**: `NewServer(cfg, evaluator, store, verdictCache)` — the 4th parameter is `*cache.VerdictCache` (can be nil). The triager is injected into the evaluator, not passed to the server.

## CI

GitHub Actions workflows in `.github/workflows/`:
- **bench.yml** — Runs `go test ./...`, then benchmark suites on PRs (subset) and nightly (full)
- **sigma-ai-sync-check.yml** — Validates upstream Sigma rule synchronisation
