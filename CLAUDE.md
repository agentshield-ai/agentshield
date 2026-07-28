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

### HuggingFace Traces Replay

```bash
make replay-build                              # Build bin/agentshield-replay
make replay DATASET=nlile/misc-merged-claude-code-traces-v1  # Build + run
make replay-score                              # Score rules against labelled ATBench

# Full usage:
./bin/agentshield-replay run \
  --dataset nlile/misc-merged-claude-code-traces-v1 \
  --max-traces 100 \
  --rules-dir rules/rules \
  --format json

# HTTP mode (against running engine on localhost:8433):
./bin/agentshield-replay run \
  --dataset sammshen/wildclaw-opus-traces \
  --http --endpoint http://localhost:8433

# Export triggered traces as benchmark testcases:
./bin/agentshield-replay run \
  --dataset nlile/misc-merged-claude-code-traces-v1 \
  --export-testcases bench/testcases/hf_traces/

# With session behavioural tracking:
./bin/agentshield-replay run \
  --dataset nlile/misc-merged-claude-code-traces-v1 \
  --sessions --mode enforce
```

Downloads public agent trace datasets from HuggingFace, extracts tool calls, replays through the Sigma rule engine, and reports rule coverage/quality. No auth needed for public datasets.

**Labelled scoring.** `AI45Research/ATBench` carries ground-truth labels, which unlocks a scoring section: a per-trace confusion matrix, precision/recall/F1/accuracy, and a per-`risk_source` breakdown. Two things about it are easy to get wrong, both documented in [docs/replay-scoring.md](docs/replay-scoring.md):

- Recall is reported **twice** — overall, and restricted to detections a shipped producer could reproduce. Replay can populate `description`, `arguments` and `response_text`, which no plugin sends, so the headline recall overstates what the deployed system would catch. Never quote the overall figure alone.
- ATBench rows are **ordered by label**, so `--max-traces` below ~450 scores a single-class prefix and reports a meaningless precision of 1.0. Run the full split. The report emits `sample_warnings` when the sample is skewed.

Use `--dump-fields` to tell a genuine rule-corpus blind spot from a field-mapping artefact; the two are indistinguishable in the summary metrics.

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
- `telemetry/` — OpenTelemetry TracerProvider, MeterProvider, and metrics recorder
- `session/` — Per-session event window registry for behavioural sequencing
- `replay/` — HuggingFace traces replay engine (fetch, extract, evaluate, report)

### OpenTelemetry Export (`internal/telemetry/`)

Exports evaluation telemetry as OTel traces and metrics:
- **Traces**: Each evaluation becomes a span within a session-scoped trace. Span attributes include `event.id`, `session.id`, `tool.name`, `verdict.action`, `verdict.mode`. Rule matches are recorded as span events.
- **Metrics**: `agentshield.evaluations.total` (counter), `agentshield.alerts.total` (counter), `agentshield.cache.hits` / `.misses` (counters).
- **Export**: OTLP/HTTP to any OTel-compatible backend (Elastic, Splunk, Grafana, Datadog).
- **Config**: `telemetry:` section in config.yaml with `enabled`, `endpoint`, `service_name`, `sample_rate`, `export_all_events`, `insecure`.

### Session Behavioural Sequencing (`internal/session/`)

Per-session sliding window that tracks tool call sequences:
- **Registry**: In-memory, concurrent-safe, per-session ring buffer with configurable TTL and max events.
- **Derived fields**: `session.recent_tools`, `session.tool_count`, `session.unique_tool_count`, `session.alert_count`, `session.approval_count`, `session.override_count` — injected into Sigma evaluation context before rule matching.
- **Cross-session correlation**: `session.cross_session_alert_count`, `session.cross_session_count`, `session.cross_session_tool_overlap` — derived from all active sessions within a 5-minute correlation window, injected for systemic attack detection.
- **Override tracking**: `POST /api/v1/override` endpoint for plugins to report user overrides of block/require_approval verdicts. Overrides update `session.override_count` for downstream Sigma rules.
- **Cleanup**: Background goroutine evicts expired sessions.
- **Config**: `session:` section in config.yaml with `enabled`, `window_sec`, `max_events`.
- **Sigma rules**: `ai_agent_recon_then_exfil.yml` (detects recon→exfil chains), `ai_agent_session_velocity_anomaly.yml` (detects high tool-call velocity), `ai_agent_approval_fatigue.yml` (detects excessive require_approval verdicts), `ai_agent_override_escalation.yml` (detects repeated user overrides of blocks).

### HuggingFace Traces Replay (`cmd/agentshield-replay/`, `internal/replay/`)

CLI tool that validates Sigma rules against real-world agent traces from public HuggingFace datasets:
- **Fetcher** (`fetcher.go`): Paginated HF Dataset Viewer API client (`/rows` endpoint) with `retryablehttp` retry. Implements `Fetcher` interface for future Parquet support.
- **Adapters** (`adapter_*.go`): `TraceAdapter` interface with implementations for three dataset formats:
  - `NlileAdapter` — `nlile/misc-merged-claude-code-traces-v1` (32K Claude Code conversations, `messages_json` column with `tool_use` blocks)
  - `WildClawAdapter` — `sammshen/wildclaw-opus-traces` (HTTP-level OpenClaw+Opus traces with `tool_calls[].function.name/arguments`)
  - `SmolAgentsAdapter` — `smolagents/synthetic-traces-toolcalling` (direct `tool_name`/`arguments` fields)
- **Field mapping** (`fieldmap.go`): Ports tool name → `event_type` + `command` mapping from `plugins/openclaw/src/normalise.ts` and field enrichment from `internal/server/server.go:454-486`. Handles both Claude Code names (`Bash`, `Read`, `Write`, `Edit`, `Glob`, `Grep`) and OpenClaw names (`exec`, `read`, `write`).
- **Runner** (`runner.go`): Pipeline orchestrator. Library mode (direct `evaluate.NewEvaluator()`) or HTTP mode (`--http` flag to POST `/api/v1/evaluate`). Stream-processes pages to bound memory.
- **Report** (`report.go`): Aggregates rule coverage, alert distribution by rule/severity, action distribution, FP candidates (benign-looking commands that triggered rules), latency stats.
- **Testcase export** (`testcase_export.go`): Writes triggered traces as `agentshieldbench`-compatible YAML testcases.

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
| `AGENTSHIELD_OTEL_ENDPOINT` | OTel OTLP endpoint URL |
| `AGENTSHIELD_OTEL_ENABLED` | Enable OTel export (true/false) |

## Platform Support

See [PLATFORMS.md](PLATFORMS.md) for full details. Detection rules target Linux/macOS (Unix/POSIX commands). The Go engine runs on Windows but no Windows-specific rules exist. All 50+ rules are under `rules/rules/ai_agent/` using `logsource.product: ai_agent`.

## Competitive Landscape

[Avast Sage](https://github.com/avast/sage) is the closest comparable project. Key differences:
- **AgentShield**: Go engine, Sigma rules, LLM triage, temporal correlation, prompt injection detection, `require_approval` graduated response, verdict caching
- **Sage**: TypeScript in-process, flat regex YAML rules, URL/package reputation APIs, supply-chain validation, Windows rules
- They are complementary: Sage excels at "is this command/URL/package dangerous?"; AgentShield excels at "is this agent being manipulated?"

### Academic Validation

[AI Agent Traps](https://papers.ssrn.com/sol3/papers.cfm?abstract_id=6372438) (Franklin et al., Google DeepMind, 2026) defines six trap categories targeting agent perception, reasoning, memory, action, multi-agent dynamics, and human oversight. AgentShield's detection rules map to all six categories:
- **Content Injection Traps** → 13 rules (prompt injection, CSS hiding, Unicode smuggling, steganography, MCP poisoning)
- **Semantic Manipulation Traps** → `ai_agent_authority_hijacking.yml`, `ai_agent_urgency_manipulation.yml`
- **Cognitive State Traps** → `ai_agent_memory_poisoning.yml`, `ai_agent_context_poisoning.yml`
- **Behavioral Control Traps** → 15 rules (exfiltration, RCE, reverse shells, persistence)
- **Systemic Traps** → `ai_agent_lateral_movement.yml`, `ai_agent_coordinated_tool_abuse.yml`
- **Human-in-the-Loop Traps** → `ai_agent_approval_fatigue.yml`, `ai_agent_override_escalation.yml`, `ai_agent_config_auto_approve.yml`

### Planned (v1.1 branches, not yet merged)
- `feat/supply-chain-checks` — npm/PyPI registry validation for hallucinated/typosquatted packages
- `feat/reputation-lookups` — k-anonymity hash prefix URL/file reputation lookups

## Conventions

- **Go 1.24+**, no CGO. Standard library `log/slog` for structured logging.
- **Conventional commits**: `feat:`, `fix:`, `docs:`, `refactor:`, `test:`, `chore:`.
- **Branch naming**: `<type>/<short-description>` (e.g. `feat/new-rule`).
- **Security**: Constant-time auth comparison (`subtle.ConstantTimeCompare`), UTF-8 validation, length limits, and control character rejection on all input.
- **Tests**: Table-driven Go tests. Security-specific test files (`security_test.go`) in most packages. Integration tests that depend on project rules use `runtime.Caller` to find the project root and `t.Skip` if rules are absent.
- **NewServer signature**: `NewServer(cfg, evaluator, store, triager, verdictCache)` — the 5th parameter is `*cache.VerdictCache` (can be nil).

## CI

GitHub Actions workflows in `.github/workflows/`:
- **bench.yml** — Runs `go test ./...`, then benchmark suites on PRs (subset) and nightly (full)
- **sigma-ai-sync-check.yml** — Validates upstream Sigma rule synchronisation
