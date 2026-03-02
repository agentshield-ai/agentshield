# Architecture

This document describes the system architecture and data flow of AgentShield, an AI Agent Detection and Response (AADR) platform. AgentShield monitors AI agent activity and detects security threats using Sigma rules, with optional LLM-powered triage for false-positive reduction.

## System Overview

AgentShield comprises three main components: a plugin layer for event collection, a Go-based detection engine, and a corpus of Sigma rules.

```
                                ┌─────────────────────────────┐
                                │     AI Agent Activity       │
                                │   (OpenClaw Tool Calls)     │
                                └─────────────┬───────────────┘
                                              │
                                              ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                         AgentShield Detection System                        │
│                                                                             │
│  ┌─────────────┐    ┌─────────────┐    ┌─────────────┐    ┌─────────────┐ │
│  │  OpenClaw   │───▶│   Engine    │───▶│   Triage    │───▶│   Action    │ │
│  │  Plugin     │    │   (Go)      │    │   (LLM)     │    │  (Block/Log) │ │
│  │  (TypeScript)│   │             │    │             │    │             │ │
│  └─────────────┘    └─────────────┘    └─────────────┘    └─────────────┘ │
│         │                 │                  │                   │         │
│         ▼                 ▼                  ▼                   ▼         │
│  ┌─────────────────────────────────────────────────────────────────────┐  │
│  │                      SQLite Database                              │  │
│  │  (events, alerts, feedback, triage results)                       │  │
│  └─────────────────────────────────────────────────────────────────────┘  │
│                                    │                                        │
└────────────────────────────────────┼────────────────────────────────────────┘
                                     │
                          ┌──────────┴──────────┐
                          ▼                     ▼
                   ┌─────────────┐       ┌─────────────┐
                   │  Feedback   │       │  Rule Auto  │
                   │  Collection │       │  Refinement │
                   └─────────────┘       └─────────────┘
```

## Components

### 1. OpenClaw Plugin (TypeScript)

The OpenClaw plugin serves as the data collection layer, intercepting AI agent activity at the tool-call boundary.

**Key responsibilities:**
- Intercepts tool calls and agent actions
- Normalises events into a standard format
- Sends events to the AgentShield Engine via the HTTP API
- Handles enforcement actions (block, allow, require approval)
- Implements a circuit-breaker pattern for fault tolerance when the engine is unavailable

**Plugin structure:**
```typescript
// plugins/openclaw/index.ts
export class AgentShieldPlugin {
  async onToolCall(event: ToolCallEvent): Promise<Action> {
    const result = await this.evaluate(event);
    return result.action; // "allow", "block", "log", "require_approval"
  }
}
```

### 2. AgentShield Engine (Go)

The AgentShield Engine is a Go service built on a fork of RunReveal's [sigmalite](https://github.com/runreveal/sigmalite) library. It evaluates events against Sigma rules and returns enforcement actions.

**Key capabilities:**
- **Sigma rule evaluation** using a forked sigmalite engine
- **HTTP API** for event evaluation (see [API Reference](api.md))
- **Three evaluation modes**: enforce, audit, shadow (see [Evaluation](evaluation.md))
- **Hot rule reloading** via SIGHUP signal for zero-downtime updates
- **Verdict caching** -- in-memory LRU cache to avoid re-evaluating identical tool calls
- **SQLite storage** for alerts, feedback, and triage results (pure-Go driver, no CGO)
- **CLI interface** for management and monitoring
- **Two-tier triage** with LLM providers (see [Triage System](triage.md))

**Engine structure:**
```
agentshield/
├── cmd/agentshield/         # CLI entrypoint
├── internal/
│   ├── server/              # HTTP server and API routes (Chi router)
│   ├── engine/              # Sigmalite wrapper and rule evaluation
│   ├── evaluate/            # Event evaluation orchestration
│   ├── cache/               # LRU verdict cache with TTL
│   ├── triage/              # LLM-powered triage (OpenAI, Anthropic)
│   ├── config/              # YAML configuration parsing and validation
│   ├── store/               # SQLite repository layer
│   ├── auth/                # Constant-time token comparison
│   ├── feedback/            # Feedback collection and rule refinement
│   └── daemon/              # Service lifecycle (graceful shutdown, signals)
└── pkg/sigma/               # Forked sigmalite library (git subtree)
```

**API endpoints** (see [API Reference](api.md) for full details):
- `POST /api/v1/evaluate` -- Evaluate an event against loaded rules
- `GET /api/v1/health` -- Health check (unauthenticated)
- `GET /api/v1/alerts` -- Retrieve and filter alerts
- `POST /api/v1/feedback` -- Submit feedback on alert classifications
- `GET /api/v1/feedback` -- Query feedback for a specific rule

### 3. Sigma Rules (YAML)

[Sigma](https://sigmahq.io/) is an open standard for writing detection rules in a structured YAML format, independent of any specific SIEM or log format. The AgentShield rule repository contains Sigma detection rules organised by [MITRE ATT&CK](https://attack.mitre.org/) tactics. All rules use `logsource.product: ai_agent`.

**Rule categories** (rule counts are approximate and grow with each release):
- Prompt injection
- Tool poisoning
- Defence evasion
- Credential access
- Data exfiltration
- Privilege escalation
- Execution
- Persistence
- Discovery
- Collection
- Initial access
- Lateral movement

**Rule format** (see [Rule Authoring Guide](rules.md) for full syntax):
```yaml
title: Direct Prompt Injection Attempt
id: agent-prompt-injection-direct-001
status: experimental
description: Detects direct prompt injection attempts
author: AgentShield Team
date: "2024-01-15"
tags:
    - attack.initial_access
    - attack.t1566
logsource:
    product: ai_agent
    category: agent_events
detection:
    selection:
        event_type: 'tool_call'
        message|contains:
            - 'ignore previous'
            - 'new instructions'
    condition: selection
level: critical
```

### 4. LLM Triage System

The triage system provides alert classification using Large Language Models (LLMs) to reduce false positives. It operates in two tiers; see [Triage System](triage.md) for full details.

**Fast triage (synchronous, ~4 seconds):**
- **OpenAI** -- Direct API integration
- **Anthropic** -- Direct API integration

**Deep triage (asynchronous, 30 seconds to several minutes):**
- **OpenClaw sub-agent** -- Background investigation via the OpenClaw gateway, with tool access for comprehensive analysis

**Triage process:**
1. **Context gathering** -- Collects recent events, similar commands, and baseline patterns
2. **LLM analysis** -- Sends alert and context to an LLM for classification
3. **Verdict assignment** -- `block`, `allow`, or `investigate`
4. **Confidence scoring** -- Confidence level (0.0--1.0) for the verdict
5. **Result integration** -- Verdict is included in the evaluation response

**Example fast triage configuration:**
```yaml
triage:
  enabled: true
  provider: "openai"
  model: "gpt-4o-mini"
  api_key: "${OPENAI_API_KEY}"
  timeout_sec: 10
```

## Data Flow

### Event Processing Pipeline

1. **Event collection** -- The OpenClaw plugin captures tool calls and agent actions.
2. **Normalisation** -- Events are standardised into a common format with canonical field names.
3. **Verdict cache check** -- The engine checks whether an identical tool call has been evaluated recently (SHA-256 key of tool name + sorted arguments).
4. **Rule evaluation** -- The engine evaluates the event against all loaded Sigma rules.
5. **Alert generation** -- Matching events produce alert objects with rule metadata and severity.
6. **Triage classification** (optional) -- For high/critical alerts, the LLM analyses the alert with contextual information.
7. **Action determination** -- Based on the evaluation mode and alert severity, the system returns `block`, `require_approval`, `allow`, or `log`.
8. **Feedback collection** -- Users may submit feedback on alert accuracy via the API.
9. **Rule refinement** -- Feedback data supports iterative rule improvement.

### Evaluation Modes

See [Evaluation](evaluation.md) for a detailed treatment of each mode and how actions are determined.

**Enforce mode** (production):
- Blocks high/critical-severity matches; requires approval for medium severity
- Generates alerts for all rule matches
- Recommended for production deployments

**Audit mode** (monitoring):
- Logs all activity but never blocks or requires approval
- Generates alerts for analysis
- Suitable for testing new rules before enforcement

**Shadow mode** (baseline):
- Silent monitoring only; events are processed but no alerts are surfaced
- Useful for collecting baseline data during initial deployment

### Triage Pipeline

```
Alert ──▶ Context Gathering ──▶ LLM Analysis ──▶ Verdict & Action
   │              │                    │              │
   │              ├─ Recent events     ├─ Reasoning    ├─ Block/Allow
   │              ├─ Similar commands  ├─ Confidence   ├─ User notify
   │              ├─ Baseline patterns └─ Evidence     └─ Store result
   │              └─ Rule FP rate
   │
   ▼
SQLite Storage
```

### Feedback Loop

```
Alert (SUSPICIOUS) ──▶ User Feedback ──▶ Verdict Update
                             │              │
                             ▼              ▼
                    FeedbackStore      AlertStore
                             │              │
                             ▼              ▼
                    ┌─────────┴──┐    ┌─────┴─────┐
                    │            │    │           │
                    ▼            ▼    ▼           ▼
              Baseline        Rule         Triage
              Update          Refinement   Improvement
```

## Technology Stack

### Core Technologies
- **Go 1.24+** -- Engine implementation (no CGO)
- **TypeScript** -- OpenClaw plugin
- **SQLite** -- Local data persistence via `modernc.org/sqlite` (pure-Go driver)
- **Sigmalite** -- Forked from RunReveal (Apache 2.0 licence)

### Libraries and Dependencies
- **Chi v5** -- HTTP router (`github.com/go-chi/chi/v5`)
- **database/sql + modernc.org/sqlite** -- Direct SQL queries (no ORM)
- **log/slog** -- Structured logging (standard library)
- **gopkg.in/yaml.v3** -- Configuration parsing
- **spf13/cobra** -- CLI framework

### Integration Points
- **OpenClaw** -- Plugin architecture and LLM access for deep triage
- **HTTP API** -- Engine communication and external integration
- **Sigma format** -- Open standard detection rule format
- **MITRE ATT&CK** -- Threat taxonomy alignment for rule tagging

## Deployment Architecture

### Single-Node Deployment (Default)
```
┌─────────────────────────────────┐
│        OpenClaw Node            │
│                                 │
│  ┌─────────────┐               │
│  │ AgentShield │               │
│  │ Plugin      │               │
│  └──────┬──────┘               │
│         │ HTTP                 │
│  ┌──────▼──────┐               │
│  │ AgentShield │               │
│  │ Engine      │               │
│  └─────────────┘               │
│                                 │
└─────────────────────────────────┘
```

### Multi-Node Deployment

Multiple plugin instances may point to a shared engine instance. Note that the engine is a single-process service and does not natively support clustering; external load balancing is required for high-availability scenarios.

```
┌─────────────────┐    ┌─────────────────┐    ┌─────────────────┐
│   Agent Node 1  │    │   Agent Node 2  │    │   Agent Node N  │
│                 │    │                 │    │                 │
│ ┌─────────────┐ │    │ ┌─────────────┐ │    │ ┌─────────────┐ │
│ │ Plugin      │ │    │ │ Plugin      │ │    │ │ Plugin      │ │
│ └──────┬──────┘ │    │ └──────┬──────┘ │    │ └──────┬──────┘ │
│        │        │    │        │        │    │        │        │
└────────┼────────┘    └────────┼────────┘    └────────┼────────┘
         │ HTTP                 │ HTTP                 │ HTTP
         └──────────────────────┼──────────────────────┘
                                │
                    ┌───────────▼────────────┐
                    │  Central AgentShield   │
                    │       Engine           │
                    │                        │
                    │ ┌─────────────────────┐│
                    │ │   Rule Management   ││
                    │ │   Alert Storage     ││
                    │ │   Triage System     ││
                    │ └─────────────────────┘│
                    └────────────────────────┘
```

## Security Considerations

### Engine Security
- **Single binary** -- Statically compiled Go binary with no CGO dependencies
- **Local SQLite storage** -- No cloud dependencies; all data remains on the host
- **Rate limiting** -- Per-IP token-bucket rate limiting to protect against abuse
- **Constant-time authentication** -- Bearer token comparison via `subtle.ConstantTimeCompare`
- **Input validation** -- UTF-8 validation, length limits, and control character rejection on all input

**Note:** AgentShield does not terminate TLS. For non-localhost deployments, place a TLS-terminating reverse proxy in front of the engine.

### Rule Security
- **Rule validation** -- Syntax and logic checking on load
- **Version control** -- Git-based rule management via subtree from upstream `sigma-ai`
- **Hot reloading** -- Updates via SIGHUP without service restart; verdict cache is automatically invalidated on reload
- **Signed rules** (roadmap) -- Planned support for cryptographic rule signing to prevent tampering

### Privacy
- **Local processing** -- No data leaves the deployment environment (unless triage is configured with an external LLM provider)
- **Audit logging** -- Structured logging via `log/slog`; secrets and PII are never logged

## Performance Characteristics

Rule evaluation is in-process and typically sub-millisecond per event for the current rule set, though actual throughput depends on rule count, rule complexity, and whether triage is enabled. When triage is active, the synchronous fast-triage step adds approximately 4 seconds of latency for high/critical alerts.

The engine is a single-process, single-node service. Horizontal scaling would require external load balancing across multiple engine instances.

The verdict cache (in-memory LRU, default 10,000 entries, 5-minute TTL) avoids redundant evaluation of identical tool calls. Cache statistics are exposed on the health endpoint.

## Extension Points

### Custom Triage Providers

The triage system uses a `Provider` interface (defined in `internal/triage/triage.go`):

```go
type Provider interface {
    Triage(ctx context.Context, triageCtx *TriageContext) (*TriageResult, error)
    Name() string
    HealthCheck(ctx context.Context) error
}
```

New LLM providers can be added by implementing this interface and registering them in `NewTriager`.