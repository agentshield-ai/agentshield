# Architecture Overview

AgentShield is a local SIEM-lite designed to monitor AI agent activity and detect security threats. This document describes the system architecture and data flow.

## System Overview

```
                                    ┌─────────────────────┐
                                    │  AI Agent Logs      │
                                    │  (JSONL files)      │
                                    └─────────┬───────────┘
                                              │
                                              ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                            AgentShield Pipeline                              │
│                                                                              │
│  ┌─────────────┐    ┌─────────────┐    ┌─────────────┐    ┌─────────────┐  │
│  │  Collector  │───▶│  Detection  │───▶│   Triage    │───▶│   Notify    │  │
│  │             │    │   Engine    │    │   Agent     │    │             │  │
│  └─────────────┘    └─────────────┘    └─────────────┘    └─────────────┘  │
│         │                 │                  │                   │          │
│         ▼                 ▼                  ▼                   ▼          │
│  ┌─────────────────────────────────────────────────────────────────────┐   │
│  │                         SQLite Database                              │   │
│  │  (events, alerts, feedback)                                          │   │
│  └─────────────────────────────────────────────────────────────────────┘   │
│                                    │                                         │
└────────────────────────────────────┼─────────────────────────────────────────┘
                                     │
                          ┌──────────┴──────────┐
                          ▼                     ▼
                   ┌─────────────┐       ┌─────────────┐
                   │  Feedback   │       │  Refinement │
                   │  Collector  │       │   Engine    │
                   └─────────────┘       └─────────────┘
```

## Components

### Collector (`src/agentshield/collectors/`)

The collector watches AI agent log files and parses events.

**ClawdbotCollector** (`clawdbot.py`):
- Reads JSONL log files asynchronously using `aiofiles`
- Tracks file position for incremental reads (survives restarts)
- Parses events to `Event` objects
- Handles malformed entries gracefully

**Key features**:
- Position tracking prevents re-processing events
- Detects log rotation and resets position
- Maps standard fields (`timestamp`, `event_type`, `command`) to Event model
- Preserves extra fields in `data` dict

### Detection Engine (`src/agentshield/detection/`)

The detection engine matches events against Sigma rules.

**SigmaRuleLoader** (`sigma.py`):
- Loads YAML rules from the rules directory
- Validates rule structure and required fields
- Supports hot-reload for rule updates

**DetectionEngine** (`engine.py`):
- Compiles Sigma rules to efficient matchers
- Evaluates events against all rules
- Generates `Alert` objects for matches
- Caches compiled matchers for performance

**Supported modifiers**:
- `contains`, `startswith`, `endswith` - String matching
- `re` - Regex patterns
- `all` - AND logic for multiple values

### Triage Agent (`src/agentshield/triage/`)

The triage agent uses LLM to classify alerts.

**ContextGatherer** (`context.py`):
- Collects conversation context (5-minute window before alert)
- Finds similar commands in history (7-day window)
- Checks baseline for known safe patterns
- Gets rule FP rate from feedback

**TriageAgent** (`agent.py`):
- Builds prompts with alert and context
- Calls Anthropic API with extended thinking
- Parses response to verdict (TRUE_POSITIVE, FALSE_POSITIVE, SUSPICIOUS)
- Auto-approves high-confidence FPs with supporting evidence

### Notification System (`src/agentshield/notify/`)

Sends alerts to users through various channels.

**DesktopNotifier** (`desktop.py`):
- macOS: Uses `osascript` for native notifications
- Linux: Uses `notify-send`
- Only notifies for TRUE_POSITIVE and SUSPICIOUS verdicts

### Feedback System (`src/agentshield/feedback/`)

Collects and processes user feedback.

**FeedbackCollector** (`collector.py`):
- Requests feedback for SUSPICIOUS verdicts
- Parses user response (safe/threat)
- Stores in FeedbackStore
- Updates alert verdict

**RuleRefinementEngine** (`refinement.py`):
- Identifies rules with high FP rates (>30%)
- Analyzes patterns in FP vs TP events
- Uses LLM to generate improved rules
- Applies suggestions with backup

### Storage Layer (`src/agentshield/store/`)

Persistent storage using SQLite with async access.

**EventStore** (`events.py`):
- Stores all parsed events
- Query by time range or event type

**AlertStore** (`alerts.py`):
- Stores generated alerts
- Updates verdicts after triage
- Query by level, rule, or time

**FeedbackStore** (`feedback.py`):
- Stores user feedback
- Calculates rule statistics (FP rate)
- Provides baseline data

**Database features**:
- WAL mode for concurrent read/write
- Indexed queries for performance
- JSON serialization for nested objects

### MCP Server (`src/agentshield/mcp/`)

Model Context Protocol server for agent integration.

**MCPServer** (`server.py`):
- `receive_alert`: Agents report security alerts
- `get_status`: Get system statistics
- `submit_feedback`: Submit feedback on alerts
- `generate_sigma_rule`: Generate rules using LLM

### Reports (`src/agentshield/reports/`)

Summary report generation.

**SummaryGenerator** (`generator.py`):
- Aggregates alerts by level and verdict
- Identifies top-triggering rules
- Calculates overall FP rate
- Identifies rules needing refinement

## Data Flow

### Event Processing Pipeline

1. **Collection**: Collector reads new entries from log files
2. **Detection**: Engine matches events against Sigma rules
3. **Alert Generation**: Matching events create Alert objects
4. **Triage**: LLM classifies alerts with context
5. **Notification**: Users are alerted for threats
6. **Feedback**: Users provide feedback on verdicts
7. **Learning**: Feedback improves future detection

### Triage Pipeline

```
Alert ──▶ Context ──▶ Prompt ──▶ LLM ──▶ Decision
            │                              │
            ├─ Recent events               ├─ Verdict
            ├─ Similar commands            ├─ Confidence
            ├─ Baseline match              └─ Reasoning
            └─ FP rate
```

### Feedback Loop

```
Alert (SUSPICIOUS) ──▶ User Feedback ──▶ Verdict Update
                              │
                              ▼
                      FeedbackStore
                              │
                              ▼
                    ┌─────────┴─────────┐
                    │                   │
                    ▼                   ▼
              Baseline Update    Rule Refinement
              (for FP cases)     (high FP rules)
```

## Data Models

### Event

```python
class Event(BaseModel):
    id: str              # UUID
    timestamp: datetime
    source: str          # e.g., "clawdbot"
    event_type: str      # e.g., "tool_call"
    command: str | None  # Executed command
    working_dir: str | None
    data: dict           # Additional fields
```

### Alert

```python
class Alert(BaseModel):
    id: str              # UUID
    timestamp: datetime
    rule_id: str
    rule_name: str
    level: AlertLevel    # LOW, MEDIUM, HIGH, CRITICAL
    event: Event         # Triggering event
    verdict: Verdict | None
    triage_reason: str | None
    context: dict
```

### Verdict

```python
class Verdict(str, Enum):
    TRUE_POSITIVE = "true_positive"
    FALSE_POSITIVE = "false_positive"
    SUSPICIOUS = "suspicious"
```

## Configuration

See [Configuration Guide](configuration.md) for details on:
- Environment variables
- YAML configuration
- Log paths and rules directories

## Extension Points

### Custom Collectors

Implement `BaseCollector`:

```python
class MyCollector(BaseCollector):
    async def collect(self) -> AsyncGenerator[Event, None]:
        # Parse your log format
        yield Event(...)
```

### Custom Notifiers

Implement `BaseNotifier`:

```python
class MyNotifier(BaseNotifier):
    def should_notify(self, alert: Alert) -> bool:
        return alert.verdict in [Verdict.TRUE_POSITIVE, Verdict.SUSPICIOUS]

    async def notify(self, alert: Alert) -> NotificationResult:
        # Send notification
        return NotificationResult(success=True)
```

### Custom Rules

Add Sigma rules to `~/.agentshield/rules/`. See [Rule Authoring Guide](rules.md).

## Technology Stack

- **Python 3.11+**: Async/await, type hints
- **Pydantic v2**: Data validation and settings
- **aiosqlite**: Async SQLite access
- **aiofiles**: Async file I/O
- **watchdog**: File system monitoring
- **Anthropic SDK**: LLM integration
- **Typer + Rich**: CLI interface
- **mcp**: Model Context Protocol SDK
