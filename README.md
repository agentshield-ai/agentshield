# AgentShield

Real-time security monitoring for AI agents, using Sigma rules for threat detection.

![Go Version](https://img.shields.io/badge/Go-1.24+-blue)
![License](https://img.shields.io/badge/License-Apache%202.0-green)
![Build Status](https://github.com/agentshield-ai/agentshield/actions/workflows/bench.yml/badge.svg)

## Overview

AgentShield monitors the tool calls that AI agents make -- shell commands, file writes, network requests -- and evaluates each one against a corpus of [Sigma rules](https://sigmahq.io/) (a standardised format for describing log-based detection patterns). When a tool call matches a known threat pattern, AgentShield can block it, require human approval, or log it for later review.

The project comprises a high-performance Go detection engine, platform plugins for [OpenClaw](plugins/openclaw/) and [Claude Code](plugins/claude/), and a growing library of 45+ community-maintained detection rules.

## What AgentShield Does

- **Monitors tool usage** in real-time with typically sub-millisecond evaluation for the current rule set
- **Detects threats** using community-maintained Sigma rules covering prompt injection, data exfiltration, privilege escalation, and more
- **Reduces false positives** with optional LLM-powered triage (two-tier: fast synchronous + deep asynchronous analysis)
- **Enforces policies** with graduated response actions (block, require approval, allow, log) across three evaluation modes (enforce, audit, shadow)
- **Integrates with existing agent workflows** via platform plugins and a generic HTTP API

## Architecture Overview

```
┌─────────────┐    ┌─────────────┐    ┌─────────────┐
│   Plugins   │────│   Engine    │────│    Rules    │
│ (Platforms) │    │ (Detection) │    │ (Threats)   │
└─────────────┘    └─────────────┘    └─────────────┘
      │                    │                   │
   Collect              Evaluate           Patterns
   Events               & Triage           & Logic
```

## Platform Support

AgentShield targets **Linux** (server-side agent deployments) and **macOS** (local development). Detection rules assume Unix/POSIX command semantics; Windows-specific rules are not yet included. See [PLATFORMS.md](PLATFORMS.md) for full details, rationale, and contribution guidance.

## Components

### Go Detection Engine ([`cmd/`](cmd/), [`internal/`](internal/), [`pkg/sigma/`](pkg/sigma/))

High-performance detection engine built in Go with a Chi HTTP router:

- Real-time Sigma rule evaluation using a forked [sigmalite](https://github.com/runreveal/sigmalite) library
- Optional two-tier LLM triage for false-positive reduction (fast synchronous + deep asynchronous)
- Three evaluation modes: enforce, audit, and shadow
- Pure-Go SQLite storage (no CGO dependency) with automatic cleanup
- Hot rule reloading via `SIGHUP` with zero downtime

**Quick start:**
```bash
go build ./cmd/agentshield/
./agentshield serve -rules ./rules -config config.yaml
```

### OpenClaw Plugin ([`plugins/openclaw/`](plugins/openclaw/))

TypeScript integration for OpenClaw agents with a circuit-breaker pattern for fault tolerance:

- Synchronous `before_tool_call` evaluation with configurable timeout
- Fire-and-forget `after_tool_call` audit reporting
- Configurable enforcement modes and notification thresholds
- Session and agent lifecycle event tracking

See the [OpenClaw plugin README](plugins/openclaw/README.md) for full configuration options.

### Claude Code Hooks ([`plugins/claude/`](plugins/claude/))

Shell-based integration for Claude Code using the [hooks system](https://docs.anthropic.com/en/docs/claude-code/hooks):

- `PreToolUse` hook intercepts Bash, Write, and Edit tool calls
- Evaluates each call against the detection engine before execution
- Fail-open behaviour when the engine is unreachable (configurable)

See the [Claude Code plugin README](plugins/claude/README.md) for setup instructions.

### Detection Rules ([`rules/`](rules/))

AgentShield consumes engine-agnostic Sigma rules from the upstream [sigma-ai](https://github.com/agentshield-ai/sigma-ai) catalogue, vendored under [`rules/`](rules/) via git subtree. The current corpus of 45+ rules covers:

- **Prompt injection** -- direct, indirect, and exfiltration-oriented injection attempts
- **Tool poisoning** -- MCP configuration manipulation, rug pulls, and tool substitution
- **Data exfiltration** -- HTTP, DNS tunnelling, steganographic, and living-off-the-land techniques
- **Privilege escalation** -- `sudo` abuse, container escapes, and cloud IAM escalation
- **Credential access** -- token theft, keychain access, and environment variable enumeration
- **Persistence** -- shell configuration modification, cron jobs, and rules-file backdoors

All rules use `logsource.product: ai_agent` with `category: agent_events`. Browse the full set under [`rules/rules/ai_agent/`](rules/rules/ai_agent/).

### Documentation ([`docs/`](docs/))

- [API Reference](docs/api.md) -- HTTP endpoints and request/response examples
- [Configuration](docs/configuration.md) -- Complete configuration options
- [Deployment Guide](docs/deployment.md) -- Production setup and operations
- [Triage System](docs/triage.md) -- LLM-powered alert analysis
- [Rules Guide](docs/rules.md) -- Sigma rule authoring and testing

## Quick Start

### 1. Build the engine

```bash
git clone https://github.com/agentshield-ai/agentshield.git
cd agentshield
go build ./cmd/agentshield/
```

### 2. Start monitoring

```bash
# Basic setup with bundled rules
./agentshield serve -rules ./rules

# With a configuration file
./agentshield serve -config config.yaml
```

### 3. Install a platform plugin

**OpenClaw:**
```bash
cd plugins/openclaw/
npm install && openclaw plugin install .
```

**Claude Code:**
```bash
./plugins/claude/install.sh
```

### 4. View alerts

```bash
./agentshield alerts list
curl http://localhost:8433/api/v1/alerts
```

## Configuration Example

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
```

See [docs/configuration.md](docs/configuration.md) for the complete set of configuration options.

## Development

```bash
# Run all Go tests
go test ./...

# Run a single package
go test -v ./internal/engine/...

# Debug mode
./agentshield serve -log-level debug
```

## Community and Resources

- **Canonical rules repository**: [sigma-ai](https://github.com/agentshield-ai/sigma-ai) -- engine-agnostic AI-agent Sigma rules
- **Vendored upstream snapshot**: [`rules/`](rules/) -- imported via git subtree
- **Plugin development**: [`plugins/`](plugins/) -- platform integrations
- **Documentation**: [`docs/`](docs/) -- deployment, configuration, and rule-authoring guides

## Support

- **GitHub Issues** -- bug reports and feature requests
- **Discussions** -- architecture and usage questions
- **Security** -- security@agentshield.ai

## Licence

Apache 2.0 -- see [LICENSE](LICENSE) for details.

Built on RunReveal's [sigmalite](https://github.com/runreveal/sigmalite) (Apache 2.0) with enhancements for AI agent security.