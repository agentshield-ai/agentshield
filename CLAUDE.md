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
  → Sigma Rule Evaluation → Triage (optional, LLM-powered) → Action (block/allow/log)
  → SQLite persistence → Response to plugin
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

### Evaluation Modes

- **enforce** — Block matching tool calls before execution
- **audit** — Log alerts but allow execution
- **shadow** — Silent monitoring, no alerts surfaced

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

## Conventions

- **Go 1.24+**, no CGO. Standard library `log/slog` for structured logging.
- **Conventional commits**: `feat:`, `fix:`, `docs:`, `refactor:`, `test:`, `chore:`.
- **Branch naming**: `<type>/<short-description>` (e.g. `feat/new-rule`).
- **Security**: Constant-time auth comparison (`subtle.ConstantTimeCompare`), UTF-8 validation, length limits, and control character rejection on all input.
- **Tests**: Table-driven Go tests. Security-specific test files (`security_test.go`) in most packages.

## CI

GitHub Actions workflows in `.github/workflows/`:
- **bench.yml** — Runs `go test ./...`, then benchmark suites on PRs (subset) and nightly (full)
- **sigma-ai-sync-check.yml** — Validates upstream Sigma rule synchronisation
