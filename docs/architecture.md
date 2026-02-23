# AgentShield Architecture

AgentShield is a comprehensive AI Agent Detection & Response (AADR) system designed to monitor AI agent activity and detect security threats in real-time. This document describes the system architecture and data flow.

## System Overview

AgentShield follows a modern, distributed architecture with three main components:

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

## Architecture Components

### 1. OpenClaw Plugin (TypeScript)

The OpenClaw plugin acts as the data collection layer, monitoring AI agent activity.

**Key responsibilities:**
- Intercepts tool calls and agent actions
- Normalizes events into standard format
- Sends events to AgentShield Engine via HTTP API
- Handles enforcement actions (block/allow)
- Manages plugin configuration and lifecycle

**Plugin structure:**
```typescript
// plugin/index.ts
export class AgentShieldPlugin {
  async onToolCall(event: ToolCallEvent): Promise<Action> {
    const result = await this.evaluate(event);
    return result.action; // ALLOW, BLOCK, LOG
  }
}
```

### 2. AgentShield Engine (Go)

The AgentShield Engine is a high-performance Go service built on a fork of RunReveal's sigmalite library.

**Key features:**
- **Sigma rule evaluation** using forked sigmalite engine
- **HTTP API** for real-time event evaluation  
- **Three evaluation modes**: enforce, audit, shadow
- **Hot rule reloading** via SIGHUP signal
- **SQLite storage** for alerts and feedback
- **CLI interface** for management and monitoring
- **Triage integration** with LLM providers

**Engine structure:**
```
agentshield-engine/
├── cmd/agentshield/         # CLI entrypoint
├── internal/
│   ├── server/              # HTTP server + API routes
│   ├── engine/              # Sigmalite wrapper + evaluation
│   ├── triage/              # LLM-powered triage system
│   ├── config/              # Configuration management
│   ├── store/               # SQLite data persistence
│   ├── feedback/            # Feedback collection + rule refinement
│   └── daemon/              # Service lifecycle management
└── pkg/sigma/               # Forked sigmalite library (git subtree)
```

**API endpoints:**
- `POST /api/v1/evaluate` - Evaluate event against rules
- `GET /api/v1/health` - Health check (also aliased at `/health`)
- `GET /api/v1/alerts` - Retrieve alerts
- `POST /api/v1/feedback` - Submit feedback

### 3. Sigma Rules (YAML)

The rule repository contains 44 Sigma detection rules organized by MITRE ATT&CK tactics across 12 categories.

**Rule categories:**
- Prompt injection (4 rules)
- Tool poisoning (2 rules)
- Defense evasion (8 rules)
- Credential access (5 rules)
- Data exfiltration (6 rules)
- Privilege escalation (4 rules)
- Execution (6 rules)
- Persistence (4 rules)
- Discovery (2 rules)
- Collection (1 rule)
- Initial access (1 rule)
- Lateral movement (1 rule)

**Rule format:**
```yaml
title: Direct Prompt Injection Attempt
id: agent-prompt-injection-direct-001
status: experimental
description: Detects direct prompt injection attempts
author: AgentShield Team
date: 2024/01/15
tags:
    - attack.initial_access
    - attack.t1566
logsource:
    category: ai_agent
    product: openclaw
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

The triage system provides intelligent alert classification using Large Language Models.

**Fast triage providers (synchronous):**
- **OpenAI** - Direct API integration
- **Anthropic** - Direct API integration

**Deep triage (asynchronous):**
- **OpenClaw sub-agent** - Background investigation via OpenClaw gateway

**Triage process:**
1. **Context gathering** - Collects recent events, similar commands, baseline patterns
2. **LLM analysis** - Sends alert + context to LLM for classification
3. **Verdict assignment** - TRUE_POSITIVE, FALSE_POSITIVE, SUSPICIOUS
4. **Confidence scoring** - Confidence level for the verdict
5. **Auto-approval** - High-confidence false positives are auto-approved

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

1. **Event Collection**: OpenClaw plugin captures tool calls and agent actions
2. **Normalization**: Events are standardized into common format
3. **Rule Evaluation**: AgentShield Engine evaluates events against Sigma rules
4. **Alert Generation**: Matching events generate Alert objects with metadata
5. **Triage Classification**: LLM analyzes alerts with contextual information
6. **Action Execution**: Based on verdict, system blocks, logs, or allows action
7. **Feedback Collection**: Users provide feedback on alert accuracy
8. **Rule Refinement**: High false positive rules are automatically improved

### Evaluation Modes

**Enforce Mode** (Production):
- Blocks malicious actions in real-time
- Generates alerts for all rule matches
- Requires triage for SUSPICIOUS verdicts

**Audit Mode** (Monitoring):
- Logs all activity but doesn't block
- Generates alerts for analysis
- Good for testing new rules

**Shadow Mode** (Baseline):
- Silent monitoring only
- Collects data for tuning
- No user-facing alerts

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
- **Go 1.24+**: Engine implementation for performance
- **TypeScript**: OpenClaw plugin development
- **SQLite**: Local data persistence (modernc.org/sqlite, pure-Go driver)
- **Sigmalite**: Forked from RunReveal (Apache 2.0)

### Libraries & Dependencies
- **Sigmalite**: Rule parsing and evaluation (git subtree from RunReveal)
- **Chi v5**: HTTP router (`github.com/go-chi/chi/v5`)
- **database/sql + modernc.org/sqlite**: Direct SQL queries (no ORM)
- **log/slog**: Structured logging (stdlib)
- **gopkg.in/yaml.v3**: Configuration parsing
- **spf13/cobra**: CLI framework

### Integration Points
- **OpenClaw Framework**: Plugin architecture and LLM access
- **HTTP APIs**: Engine communication and external integration
- **Sigma Format**: Standard detection rule format
- **MITRE ATT&CK**: Threat taxonomy alignment

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

### Distributed Deployment (Enterprise)
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
                    │ │   Analytics         ││
                    │ └─────────────────────┘│
                    └────────────────────────┘
```

## Security Considerations

### Engine Security
- **No external dependencies** - Single Go binary
- **Local SQLite storage** - No cloud dependencies
- **HTTPS/TLS support** - Encrypted communication
- **Rate limiting** - Protection against abuse
- **Authentication tokens** - API access control

### Rule Security
- **Signed rules** (roadmap) - Prevent rule tampering
- **Rule validation** - Syntax and logic checking
- **Version control** - Git-based rule management
- **Hot reloading** - Updates without service restart

### Privacy Protection
- **Local processing** - No data leaves environment
- **Configurable retention** - Automatic data cleanup
- **Anonymization options** - Strip sensitive content
- **Audit logging** - Track all system access

## Performance Characteristics

Rule evaluation is in-process and typically sub-millisecond per event for the current rule set. Actual throughput depends on rule count, complexity, and whether triage is enabled. The engine is a single-process, single-node service; horizontal scaling would require external load balancing across multiple instances.

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