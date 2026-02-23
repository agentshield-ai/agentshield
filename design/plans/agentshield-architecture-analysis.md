# AgentShield Architecture Analysis

**Author**: AgentShield Architect
**Date**: 2026-02-07
**Purpose**: Comprehensive architecture mapping for OpenClaw real-time integration design

---

## 1. Architecture Overview

AgentShield is a local SIEM-lite for AI agent security monitoring. It implements a classic **collect -> detect -> triage -> notify** pipeline, with a feedback loop for false-positive tuning.

### High-Level Component Diagram

```
Log Files (JSONL)
    |
    v
[Collectors] -- ClawdbotCollector, ClaudeCodeCollector (BaseCollector ABC)
    |
    v
[EventStore] -- SQLite (WAL mode) via aiosqlite
    |
    v
[DetectionEngine] -- Sigma rules compiled to matcher functions
    |
    v
[Alerts] -- Alert model (event + rule match + level)
    |
    v
[TriageAgent] -- Anthropic LLM (extended thinking) -> verdict
    |
    v
[AlertStore] -- SQLite persistence
    |
    v
[DesktopNotifier] -- osascript (macOS) / notify-send (Linux)
    |
    v
[FeedbackCollector] <-> [FeedbackStore] -> baseline / rule refinement
```

### Orchestration

The `MonitorDaemon` (`daemon.py`) is the central orchestrator. It runs an async polling loop:

1. `_refresh_collectors()` -- expand glob patterns, create/remove collectors for new/stale log files
2. `_collect_events()` -- iterate all collectors, yield `Event` objects
3. Store events in `EventStore`
4. `_detect_threats(events)` -- run `DetectionEngine.evaluate()` per event
5. For each `Alert`: triage via LLM, store in `AlertStore`, notify if needed

The daemon is configured via `config.py` (`Settings` Pydantic model with env/YAML merge) and exposed via `cli.py` (Typer CLI).

A separate `MCPServer` provides tool-based access (receive_alert, get_status, submit_feedback, generate_sigma_rule) for AI agent integration via MCP protocol (FastMCP, stdio/SSE/streamable-http transports).

---

## 2. Event Flow -- End to End

### 2.1 Ingestion

```
Log file on disk (JSONL)
  --> LogWatcher.get_current_files()  [glob pattern expansion]
  --> create_collector(log_path, position_file)
      - ".claude" + "projects" in path => ClaudeCodeCollector
      - else => ClawdbotCollector
  --> collector.collect()  [async iterator yielding Event objects]
      - reads from saved file position (incremental)
      - parses JSONL lines into Event(timestamp, source, event_type, command, working_dir, data)
      - saves new file position
```

**Key observations**:
- All ingestion is **pull-based** (file polling). There is no push/HTTP/WebSocket ingestion path.
- Position tracking via `.positions.json` enables incremental reads.
- The `BaseCollector` ABC is minimal: `async def collect() -> AsyncIterator[Event]`.
- Both collectors handle multiple log formats (tslog, session, legacy for Clawdbot; progress/assistant/user types for Claude Code).

### 2.2 Detection

```
Event
  --> DetectionEngine.evaluate(event) -> list[Alert]
      for each SigmaRule:
        --> _matches_rule(event, rule)
            --> compile rule to SelectionMatcher (cached)
            --> matcher(event) -> bool
      if match:
        --> _create_alert(event, rule) -> Alert
```

**Key observations**:
- `DetectionEngine.evaluate()` is **synchronous** and processes a **single event at a time**. This is already compatible with real-time evaluation.
- Rules are compiled once and cached (`_compiled_cache`). The compilation cost is amortised.
- The engine supports Sigma condition logic: `and`, `or`, `not`, parentheses, selection references.
- Field matching supports modifiers: `contains`, `startswith`, `endswith`, `re`, `all`.
- Field resolution checks `Event` attributes first, then `event.data` dict -- extensible.

### 2.3 Triage

```
Alert
  --> ContextGatherer.gather_context(alert) -> AlertContext
      - conversation_events (5-min window from EventStore)
      - similar_commands (7-day window by event_type)
      - baseline match (from FeedbackStore safe feedback)
      - rule FP rate (from FeedbackStore stats)
  --> TriageAgent.triage(alert, context) -> TriageDecision
      - builds prompt from alert + context
      - calls Anthropic API (extended thinking, 5000 token budget)
      - parses JSON response: {verdict, confidence, reasoning}
      - checks auto-approve: FP + high confidence + (baseline match OR high FP rate)
```

**Key observations**:
- Triage is **async** and involves an LLM API call. Typical latency: 1-10 seconds.
- The triage path is **optional** -- if no API key, alerts are marked SUSPICIOUS.
- Auto-approve logic can skip human review for high-confidence FPs with baseline evidence.
- For real-time use, triage is too slow for inline decision-making (50ms budget). We need a separate fast-path.

### 2.4 Notification

```
Alert (triaged)
  --> DesktopNotifier.should_notify(alert) -> bool
      - only TRUE_POSITIVE and SUSPICIOUS
  --> DesktopNotifier.notify(alert) -> NotificationResult
      - macOS: osascript
      - Linux: notify-send
```

### 2.5 Feedback Loop

```
User reviews SUSPICIOUS alert
  --> FeedbackCollector.process_feedback(alert, response)
      - stores Feedback (alert_id, rule_id, feedback_type=safe|threat)
      - updates alert verdict in AlertStore
      - feedback available for:
        - baseline building (safe patterns)
        - rule FP rate calculation
        - rule refinement via LLM (RuleRefinementEngine)
```

---

## 3. Key Abstractions

### 3.1 BaseCollector Interface

```python
# src/agentshield/collectors/base.py
class BaseCollector(ABC):
    @abstractmethod
    async def collect(self) -> AsyncIterator[Event]:
        """Yield Event objects from the log source."""
        yield
```

**Minimal and extensible.** A new collector for real-time events needs only implement this single method. However, the current abstraction assumes the collector *pulls* data (reads a file). A push-based collector would need a different pattern -- it would need to buffer incoming events and yield them when `collect()` is called, or the daemon loop needs to support event callbacks.

### 3.2 Event Model

```python
# src/agentshield/models/events.py
class Event(BaseModel):
    id: str           # UUID, auto-generated
    timestamp: datetime
    source: str       # "clawdbot", "claude-code", "mcp"
    event_type: str   # "tool_call", "tool_use", "file_read", etc.
    command: str | None
    working_dir: str | None
    data: dict[str, Any]  # extensible key-value store
```

**Well-suited for extension.** The `data` dict can carry arbitrary fields. The `source` field can distinguish real-time events (e.g., `"openclaw"`). The `event_type` field is used for Sigma rule matching.

### 3.3 Alert Model

```python
# src/agentshield/models/alerts.py
class Alert(BaseModel):
    id: str
    timestamp: datetime
    rule_id: str
    rule_name: str
    level: AlertLevel  # low, medium, high, critical
    event: Event       # the triggering event (embedded)
    verdict: Verdict | None  # true_positive, false_positive, suspicious
    triage_reason: str | None
    context: dict[str, Any]
```

### 3.4 DetectionEngine API

```python
# src/agentshield/detection/engine.py
class DetectionEngine:
    def __init__(self, rules_dir: Path, loader=None)
    def load_rules(self) -> list[SigmaRule]
    def reload_rules(self) -> list[SigmaRule]
    def evaluate(self, event: Event) -> list[Alert]  # synchronous, single-event
```

**Critical finding**: `evaluate()` is already synchronous and per-event. This is ideal for real-time inline evaluation. No batching is required. Rule compilation is cached.

### 3.5 SigmaRule Model

```python
# src/agentshield/detection/sigma.py
class SigmaRule(BaseModel):
    id: str
    title: str
    description: str | None
    logsource: dict[str, str]
    detection: dict[str, Any]
    level: str
    status: str
    tags: list[str]
```

### 3.6 MCPServer

```python
# src/agentshield/mcp/server.py
class MCPServer:
    async def receive_alert(...)    # accept pre-built alerts
    async def get_status(...)       # query statistics
    async def submit_feedback(...)  # user feedback
    async def generate_sigma_rule(...)  # LLM-generated rules
```

The MCP server already accepts alerts from external sources via `receive_alert()`. However, it receives **alerts** (already-detected threats), not raw **events** for detection. For OpenClaw integration, we need event ingestion with detection.

---

## 4. Extension Points

### 4.1 Adding a New Collector Type

**Feasibility: High**

The `BaseCollector` ABC is designed for extension. A `RealtimeCollector` can implement `collect()` to yield events from an internal buffer fed by HTTP/WebSocket.

However, the current daemon loop calls `collector.collect()` on a timer (`poll_interval=5s`). For real-time events, we need either:
- (A) A very short poll interval (100ms) -- simple but introduces latency
- (B) An event-driven path where incoming events trigger processing immediately

### 4.2 Synchronous Detection Evaluation

**Feasibility: High -- already supported**

`DetectionEngine.evaluate(event)` is synchronous, per-event, and uses compiled/cached matchers. Benchmarking is needed, but the logic is simple string matching with compiled patterns. Expected latency: sub-millisecond per event for typical rule sets (5-10 rules).

### 4.3 Dual-Mode Daemon (Polling + Push)

**Feasibility: Medium**

The `MonitorDaemon.run()` loop is purely polling-based:
```python
while self._running:
    await self._process_logs()
    await asyncio.wait_for(self._stop_event.wait(), timeout=self.poll_interval)
```

To support push mode, we can run a concurrent asyncio task that listens for HTTP/WebSocket events alongside the polling loop. Both paths feed into the same detection + storage pipeline.

### 4.4 MCP Server Extension

**Feasibility: Medium**

The MCP server could be extended with a new `evaluate_event` tool that:
1. Accepts a raw event
2. Runs detection
3. Returns block/allow decision synchronously

However, MCP protocol adds overhead (JSON-RPC framing, tool dispatch). For the 50ms latency budget, a lightweight HTTP endpoint is more appropriate.

### 4.5 HTTP/REST Endpoint

**Feasibility: High**

A new lightweight HTTP server (aiohttp or FastAPI) can:
1. Accept POST /evaluate with event JSON
2. Run `DetectionEngine.evaluate(event)` synchronously
3. Return {action: "allow"|"block"|"log", alerts: [...]} within the latency budget
4. Asynchronously store the event and any alerts

This is the recommended approach for real-time integration.

---

## 5. Candidate Integration Points

### 5.1 Can we add a new collector type for real-time HTTP/WebSocket events?

**Yes**, but it's not the optimal path for low-latency synchronous decisions.

A `RealtimeCollector` works well for **async monitoring** (fire-and-forget event ingestion) but the collector pattern is inherently pull-based. For synchronous block/allow decisions, a direct HTTP endpoint that calls `DetectionEngine.evaluate()` is better.

**Recommendation**: Use a new HTTP server for synchronous evaluation, not the collector pattern.

### 5.2 Can the DetectionEngine evaluate events synchronously (not just batch)?

**Yes -- it already does.** `DetectionEngine.evaluate(event: Event) -> list[Alert]` processes a single event synchronously. There is no batch assumption in the API. Rule compilation is cached after first use.

Estimated latency per evaluation: < 1ms for 5-10 rules with simple string matching.

### 5.3 Can the daemon support both polling (logs) and push (real-time) simultaneously?

**Yes**, using asyncio concurrency. The daemon's `run()` method can spawn a concurrent task for the HTTP server alongside the polling loop. Both paths share the same `DetectionEngine`, `EventStore`, and `AlertStore` instances.

```python
async def run(self):
    # Start HTTP server for real-time events
    http_task = asyncio.create_task(self._run_http_server())
    # Continue polling loop for log files
    try:
        await self._poll_loop()
    finally:
        http_task.cancel()
```

### 5.4 What about the MCP server -- can it be extended?

**Yes**, but MCP is not ideal for the real-time path due to:
- Protocol overhead (JSON-RPC framing)
- Tool dispatch latency
- Designed for AI agent interactions, not high-frequency event evaluation

MCP is better suited for:
- Human/agent review of alerts (existing `receive_alert`, `submit_feedback`)
- Rule generation (`generate_sigma_rule`)
- Status queries (`get_status`)

The MCP server could gain a new `evaluate_event` tool for agents that want to self-police, but OpenClaw's runtime hook should use the lightweight HTTP path.

---

## 6. Backward Compatibility

### 6.1 Safe Changes (no breaking impact)

- Adding a new HTTP server module (e.g., `src/agentshield/realtime/server.py`)
- Adding new `source` values to Event (e.g., `"openclaw"`)
- Adding new fields to `Event.data` dict
- Adding new CLI commands (e.g., `agentshield realtime-server`)
- Adding new config fields to `Settings` (with defaults)
- Extending `DetectionEngine` with a `evaluate_and_respond()` method that wraps `evaluate()`

### 6.2 Moderate Risk Changes

- Modifying `MonitorDaemon.run()` to support dual mode -- existing polling behaviour must be preserved
- Adding new database tables for real-time audit trail
- Adding new Sigma rules for OpenClaw-specific event types

### 6.3 Risky Changes (avoid)

- Changing the `BaseCollector` ABC signature
- Changing the `Event` model field names (would break existing collectors and rules)
- Changing the `DetectionEngine.evaluate()` return type
- Modifying existing Sigma rules (could affect current detection)
- Changing database schema for existing tables

---

## 7. Proposed AgentShield-Side Design for OpenClaw Integration

### 7.1 New Module: `RealtimeReceiver`

A new lightweight HTTP server for synchronous event evaluation.

**Location**: `src/agentshield/realtime/server.py`

**Interface**:
```
POST /api/v1/evaluate
Content-Type: application/json

Request:
{
    "event_id": "uuid",
    "timestamp": "ISO-8601",
    "event_type": "tool_call" | "file_read" | "file_write" | "network_request" | ...,
    "command": "rm -rf /",
    "working_dir": "/home/user",
    "source": "openclaw",
    "agent_id": "agent-123",
    "session_id": "session-456",
    "data": { ... arbitrary fields ... }
}

Response (< 50ms):
{
    "action": "allow" | "block" | "log",
    "alerts": [
        {
            "rule_id": "agent-rce-injection-001",
            "rule_name": "Remote Code Execution via Piped Script Download",
            "level": "critical"
        }
    ],
    "event_id": "uuid"
}
```

### 7.2 Action Decision Logic

The `action` field is determined by alert severity:

| Alerts Generated | Max Level | Action |
|---|---|---|
| None | -- | `allow` |
| Any | `low` or `medium` | `log` (allow but record) |
| Any | `high` | `log` (default) or `block` (if configured) |
| Any | `critical` | `block` |

This mapping should be configurable. The initial implementation can use a simple threshold:

```python
def decide_action(alerts: list[Alert]) -> str:
    if not alerts:
        return "allow"
    max_level = max(a.level for a in alerts)
    if max_level == AlertLevel.CRITICAL:
        return "block"
    if max_level == AlertLevel.HIGH:
        return "block"  # configurable
    return "log"
```

### 7.3 Synchronous vs. Asynchronous Paths

The real-time receiver has two paths:

**Synchronous (inline, < 50ms)**:
1. Parse event JSON into `Event` model
2. Run `DetectionEngine.evaluate(event)` -- sub-millisecond
3. Compute action from alert levels
4. Return response

**Asynchronous (background, fire-and-forget)**:
1. Store event in `EventStore`
2. Store any alerts in `AlertStore`
3. If alerts present and triage agent available, queue for LLM triage
4. Send desktop notifications for confirmed threats

The synchronous path returns immediately. The async path runs in background tasks.

### 7.4 Dual-Mode Daemon

The existing daemon gains a new configuration option:

```yaml
# ~/.agentshield/config.yaml
realtime:
  enabled: true
  host: "127.0.0.1"
  port: 8432
  block_threshold: "high"  # "critical" | "high" | "medium"
```

When `realtime.enabled` is true, the daemon starts the HTTP server alongside the polling loop:

```python
# daemon.py modifications
async def run(self):
    tasks = [asyncio.create_task(self._poll_loop())]
    if self._realtime_enabled:
        tasks.append(asyncio.create_task(self._realtime_server.start()))
    await asyncio.gather(*tasks)
```

### 7.5 Shared Components

The real-time receiver reuses existing components:

| Component | Reused? | Notes |
|---|---|---|
| `Event` model | Yes | As-is, `source="openclaw"` |
| `DetectionEngine` | Yes | Shared instance, same compiled rules |
| `EventStore` | Yes | Async background writes |
| `AlertStore` | Yes | Async background writes |
| `SigmaRule` | Yes | Same rule format, new OpenClaw-specific rules |
| `TriageAgent` | Yes | Async background only, not on critical path |
| `DesktopNotifier` | Yes | Async background notifications |
| `BaseCollector` | No | Not suitable for push model |

### 7.6 New Sigma Rules for OpenClaw

OpenClaw events can be detected with new rules:

```yaml
id: openclaw-dangerous-tool-001
title: OpenClaw Dangerous Tool Execution
logsource:
  product: agentshield
  category: agent_events
detection:
  selection:
    source: "openclaw"
    event_type: "tool_call"
    command|contains|all:
      - "curl"
      - "| bash"
  condition: selection
level: critical
```

The existing rule format works without modification. Rules can filter on `source: "openclaw"` for OpenClaw-specific detection.

### 7.7 Latency Budget Breakdown

Target: < 50ms end-to-end from OpenClaw request to response.

| Step | Estimated Time |
|---|---|
| HTTP parsing + JSON deserialization | ~1ms |
| Event model validation (Pydantic) | ~0.5ms |
| DetectionEngine.evaluate() (5-10 rules) | ~0.5ms |
| Action decision logic | ~0.1ms |
| Response serialization + HTTP write | ~0.5ms |
| **Total synchronous path** | **~2.5ms** |
| Network latency (localhost) | ~0.2ms |
| **Total with network** | **< 5ms** |

This leaves significant headroom within the 50ms budget. Even with 50+ rules, evaluation should stay well under 10ms.

### 7.8 Failure Modes and Resilience

| Failure | Behaviour |
|---|---|
| AgentShield not running | OpenClaw should default to `allow` (fail-open) |
| HTTP connection timeout | OpenClaw uses configurable timeout, defaults to `allow` |
| DetectionEngine error | Return `allow` with error logged |
| Database write failure | Synchronous path unaffected (writes are async) |
| Rule loading failure | Use last known good rules (cached in memory) |

The design is **fail-open** by default: if AgentShield cannot evaluate, OpenClaw proceeds. This prevents AgentShield from becoming a single point of failure. The fail-open/fail-closed mode should be configurable.

---

## 8. File Inventory

### Source Files Read

| Path | Purpose | Lines |
|---|---|---|
| `src/agentshield/collectors/base.py` | BaseCollector ABC | 22 |
| `src/agentshield/collectors/clawdbot.py` | Clawdbot log parser | 429 |
| `src/agentshield/collectors/claudecode.py` | Claude Code log parser | 291 |
| `src/agentshield/detection/engine.py` | Sigma rule engine | 501 |
| `src/agentshield/detection/sigma.py` | Sigma rule loader/model | 126 |
| `src/agentshield/triage/agent.py` | LLM triage agent | 340 |
| `src/agentshield/triage/context.py` | Context gathering | 264 |
| `src/agentshield/triage/prompts.py` | Triage prompt builder | 117 |
| `src/agentshield/models/events.py` | Event Pydantic model | 20 |
| `src/agentshield/models/alerts.py` | Alert/Verdict models | 62 |
| `src/agentshield/models/feedback.py` | Feedback model | 26 |
| `src/agentshield/store/events.py` | Event SQLite store | 113 |
| `src/agentshield/store/alerts.py` | Alert SQLite store | 188 |
| `src/agentshield/store/feedback.py` | Feedback SQLite store | 149 |
| `src/agentshield/store/schema.sql` | Database schema (3 tables) | 50 |
| `src/agentshield/daemon.py` | Monitor daemon orchestrator | 458 |
| `src/agentshield/config.py` | Settings/config management | 130 |
| `src/agentshield/log_watcher.py` | Glob-based file watcher | 80 |
| `src/agentshield/mcp/server.py` | MCP server (4 tools) | 597 |
| `src/agentshield/cli.py` | Typer CLI commands | 650 |
| `src/agentshield/notify/base.py` | BaseNotifier ABC | 49 |
| `src/agentshield/notify/desktop.py` | Desktop notifications | 266 |
| `src/agentshield/feedback/collector.py` | Feedback collection | 240 |
| `src/agentshield/feedback/refinement.py` | Rule refinement via LLM | 639 |
| `src/agentshield/reports/generator.py` | Summary report generator | 267 |
| `src/agentshield/service.py` | launchd/systemd installer | 445 |
| `rules/agent_rce_injection.yml` | RCE detection rule | 59 |
| `rules/agent_credential_access.yml` | Credential access rule | 67 |
| `tests/conftest.py` | Test fixtures | 44 |
| `tests/test_daemon.py` | Daemon tests (8 tests) | 701 |

### Sigma Rules

5 production rules in `rules/`:
- `agent_rce_injection.yml` -- curl/wget pipe to shell (critical)
- `agent_credential_access.yml` -- .env, .ssh, AWS, keychain (high)
- `agent_untrusted_skill_install.yml` -- untrusted skill installation
- `agent_persistence.yml` -- persistence mechanisms
- `agent_network_recon.yml` -- network reconnaissance

---

## 9. Summary of Key Findings for Integration

1. **DetectionEngine is already synchronous and per-event** -- no changes needed for real-time evaluation.

2. **The collector pattern is pull-based** -- not suitable for synchronous request/response. Use HTTP endpoint instead.

3. **The Event model is extensible** -- `source` and `data` dict handle new event types cleanly.

4. **The daemon can run dual-mode** via asyncio task concurrency -- polling + HTTP server.

5. **The MCP server is not the right path** for low-latency evaluation -- use lightweight HTTP.

6. **Existing Sigma rules work unchanged** for OpenClaw events. New rules can filter on `source: "openclaw"`.

7. **Latency budget is easily achievable** -- synchronous evaluation path estimated at ~2.5ms, well within 50ms.

8. **Fail-open design** prevents AgentShield from blocking agent operation if the monitor is unavailable.

9. **Triage stays asynchronous** -- LLM-based triage runs in background, not on the critical path.

10. **SQLite WAL mode** supports concurrent reads/writes, so the async background writes won't block the synchronous evaluation path.
