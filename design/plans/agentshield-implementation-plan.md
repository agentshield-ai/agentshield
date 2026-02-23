# AgentShield Real-Time Receiver -- Implementation Plan

**Date**: 2026-02-07
**Author**: AgentShield Architect
**References**:
- [Architecture Analysis](agentshield-architecture-analysis.md)
- [Integration Contract](integration-contract.md)

---

## 1. Scope

Implement the AgentShield side of the OpenClaw integration: a real-time HTTP evaluation server that receives tool call events, runs Sigma rule detection synchronously, and returns allow/block/log decisions within a 50ms latency budget.

### In Scope
- New `realtime` package with HTTP server, request handling, and action logic
- New configuration fields for real-time mode
- Dual-mode daemon (polling + real-time server running concurrently)
- New CLI command to run the real-time server standalone
- Contract-compliant endpoints: `/api/v1/evaluate`, `/api/v1/audit`, `/api/v1/lifecycle`, `/api/v1/health`
- Authentication via shared secret token
- Tests for all new components

### Out of Scope
- OpenClaw plugin implementation (Task #5/#6)
- New Sigma rules for OpenClaw-specific events (follow-up task)
- Prometheus metrics export (future enhancement)
- Unix domain socket transport (future optimisation)

---

## 2. New Files

All new code lives under `src/agentshield/realtime/`:

```
src/agentshield/realtime/
    __init__.py
    server.py          # HTTP server (aiohttp Application)
    handlers.py        # Request handlers for each endpoint
    models.py          # Pydantic models for request/response schemas
    auth.py            # Authentication middleware
    action.py          # Action decision logic (alerts -> allow/block/log)
```

Additional modifications:
```
src/agentshield/config.py      # Add RealtimeConfig section
src/agentshield/daemon.py      # Add dual-mode support
src/agentshield/cli.py         # Add `realtime-server` command

tests/test_realtime_server.py  # HTTP server tests
tests/test_realtime_models.py  # Request/response schema tests
tests/test_realtime_action.py  # Action decision logic tests
tests/test_realtime_auth.py    # Authentication tests
tests/test_realtime_integration.py  # End-to-end integration tests
```

---

## 3. Implementation Details

### 3.1 Pydantic Models (`realtime/models.py`)

```python
"""Request/response models for the real-time evaluation API."""

from datetime import datetime
from typing import Any

from pydantic import BaseModel, Field


class EvaluationRequest(BaseModel):
    """POST /api/v1/evaluate request body.

    See integration-contract.md Section 2.1.
    """
    event_id: str
    timestamp: str  # ISO 8601 -- validated but stored as string for speed
    event_type: str
    tool_name: str
    source: str = "openclaw"
    command: str | None = None
    params: dict[str, Any] = Field(default_factory=dict)
    agent_id: str | None = None
    session_id: str | None = None
    working_dir: str | None = None
    data: dict[str, Any] = Field(default_factory=dict)


class AlertInfo(BaseModel):
    """Alert summary in evaluation response."""
    rule_id: str
    rule_name: str
    level: str
    description: str | None = None


class EvaluationResponse(BaseModel):
    """POST /api/v1/evaluate response body.

    See integration-contract.md Section 2.2.
    """
    action: str  # "allow" | "block" | "log"
    event_id: str
    alerts: list[AlertInfo] = Field(default_factory=list)
    reason: str | None = None


class AuditRequest(BaseModel):
    """POST /api/v1/audit request body.

    See integration-contract.md Section 2.3.
    """
    event_id: str
    correlation_id: str
    timestamp: str
    event_type: str = "tool_result"
    tool_name: str
    source: str = "openclaw"
    result_summary: str | None = None
    is_error: bool = False
    error_message: str | None = None
    duration_ms: int = 0
    agent_id: str | None = None
    session_id: str | None = None
    working_dir: str | None = None
    data: dict[str, Any] = Field(default_factory=dict)


class LifecycleRequest(BaseModel):
    """POST /api/v1/lifecycle request body.

    See integration-contract.md Section 15.1.
    """
    event_id: str
    timestamp: str
    event_type: str  # session_start, session_end, agent_start, agent_end, message_blocked
    source: str = "openclaw"
    agent_id: str | None = None
    session_id: str | None = None
    data: dict[str, Any] = Field(default_factory=dict)


class HealthResponse(BaseModel):
    """GET /api/v1/health response body."""
    status: str = "ok"
    version: str = "1.0.0"
    rules_loaded: int = 0
    uptime_seconds: float = 0.0
```

### 3.2 Action Decision Logic (`realtime/action.py`)

```python
"""Action decision logic for real-time evaluation."""

from agentshield.models.alerts import Alert, AlertLevel


# Ordered from most to least severe
_LEVEL_ORDER = {
    AlertLevel.CRITICAL: 4,
    AlertLevel.HIGH: 3,
    AlertLevel.MEDIUM: 2,
    AlertLevel.LOW: 1,
}

# Default: block on HIGH and above
_DEFAULT_BLOCK_THRESHOLD = AlertLevel.HIGH


def decide_action(
    alerts: list[Alert],
    block_threshold: AlertLevel = _DEFAULT_BLOCK_THRESHOLD,
) -> str:
    """Determine the action based on alert severity.

    Args:
        alerts: List of alerts from DetectionEngine.evaluate().
        block_threshold: Minimum alert level that triggers a block.

    Returns:
        "allow", "block", or "log".
    """
    if not alerts:
        return "allow"

    max_level = max(alerts, key=lambda a: _LEVEL_ORDER.get(a.level, 0)).level

    if _LEVEL_ORDER.get(max_level, 0) >= _LEVEL_ORDER.get(block_threshold, 3):
        return "block"

    return "log"


def build_block_reason(alerts: list[Alert]) -> str:
    """Build a human-readable block reason from alerts.

    Args:
        alerts: List of alerts that triggered the block.

    Returns:
        Human-readable reason string.
    """
    if not alerts:
        return ""

    # Use the highest-severity alert's rule name
    top_alert = max(alerts, key=lambda a: _LEVEL_ORDER.get(a.level, 0))

    if len(alerts) == 1:
        return f"Blocked by rule: {top_alert.rule_name} [{top_alert.level.value}]"

    return (
        f"Blocked by {len(alerts)} rules. "
        f"Highest severity: {top_alert.rule_name} [{top_alert.level.value}]"
    )
```

### 3.3 Authentication Middleware (`realtime/auth.py`)

```python
"""Authentication for the real-time evaluation API."""

import hmac
import logging

from aiohttp import web

logger = logging.getLogger(__name__)


def create_auth_middleware(
    auth_token: str,
    auth_required: bool = True,
) -> web.middleware:
    """Create authentication middleware.

    Args:
        auth_token: Expected shared secret token.
        auth_required: Whether authentication is enforced.

    Returns:
        aiohttp middleware function.
    """
    @web.middleware
    async def auth_middleware(request: web.Request, handler):
        # Skip auth for health endpoint
        if request.path == "/api/v1/health":
            return await handler(request)

        if not auth_required:
            return await handler(request)

        token = request.headers.get("X-AgentShield-Auth", "")

        if not hmac.compare_digest(token, auth_token):
            logger.warning("Authentication failed from %s", request.remote)
            return web.json_response(
                {"error": "unauthorized"},
                status=401,
            )

        return await handler(request)

    return auth_middleware
```

### 3.4 Request Handlers (`realtime/handlers.py`)

```python
"""HTTP request handlers for the real-time evaluation API."""

import asyncio
import logging
import time
from datetime import datetime

from aiohttp import web
from pydantic import ValidationError

from agentshield.detection.engine import DetectionEngine
from agentshield.models.events import Event
from agentshield.realtime.action import build_block_reason, decide_action
from agentshield.realtime.models import (
    AlertInfo,
    AuditRequest,
    EvaluationRequest,
    EvaluationResponse,
    HealthResponse,
    LifecycleRequest,
)
from agentshield.store.alerts import AlertStore
from agentshield.store.events import EventStore

logger = logging.getLogger(__name__)

CONTRACT_VERSION = "1.0.0"
MAX_REQUEST_SIZE = 65536  # 64KB


class RealtimeHandlers:
    """Handler class holding references to shared components."""

    def __init__(
        self,
        detection_engine: DetectionEngine,
        event_store: EventStore,
        alert_store: AlertStore,
        block_threshold: str = "high",
        start_time: float | None = None,
    ) -> None:
        self.detection_engine = detection_engine
        self.event_store = event_store
        self.alert_store = alert_store
        self.block_threshold = self._parse_threshold(block_threshold)
        self.start_time = start_time or time.monotonic()

    async def handle_evaluate(self, request: web.Request) -> web.Response:
        """Handle POST /api/v1/evaluate."""
        start = time.monotonic()

        # Check version header
        version = request.headers.get("X-AgentShield-Version", "")
        if version and not version.startswith("1."):
            return web.json_response(
                {"error": "unsupported_version", "supported": "1.x", "received": version},
                status=422,
            )

        # Check request size
        if request.content_length and request.content_length > MAX_REQUEST_SIZE:
            return web.json_response(
                {"error": "request_too_large"},
                status=413,
            )

        # Parse request
        try:
            body = await request.json()
            req = EvaluationRequest.model_validate(body)
        except (ValueError, ValidationError) as e:
            return web.json_response(
                {"error": "invalid_request", "detail": str(e)},
                status=400,
            )

        # Convert to Event model for detection
        event = self._request_to_event(req)

        # Synchronous detection (< 1ms for typical rule sets)
        try:
            alerts = self.detection_engine.evaluate(event)
        except Exception as e:
            logger.error("Detection evaluation failed: %s", e)
            alerts = []

        # Decide action
        action = decide_action(alerts, self.block_threshold)

        # Build response
        alert_infos = [
            AlertInfo(
                rule_id=a.rule_id,
                rule_name=a.rule_name,
                level=a.level.value,
                description=None,
            )
            for a in alerts
        ]

        reason = build_block_reason(alerts) if action == "block" else None

        response = EvaluationResponse(
            action=action,
            event_id=req.event_id,
            alerts=alert_infos,
            reason=reason,
        )

        # Background: store event and alerts
        asyncio.create_task(self._store_event_and_alerts(event, alerts))

        duration_ms = (time.monotonic() - start) * 1000

        return web.json_response(
            response.model_dump(),
            headers={
                "X-AgentShield-Version": CONTRACT_VERSION,
                "X-Request-Duration-Ms": str(int(duration_ms)),
            },
        )

    async def handle_audit(self, request: web.Request) -> web.Response:
        """Handle POST /api/v1/audit."""
        try:
            body = await request.json()
            req = AuditRequest.model_validate(body)
        except (ValueError, ValidationError) as e:
            return web.json_response(
                {"error": "invalid_request", "detail": str(e)},
                status=400,
            )

        # Store as event in background
        event = Event(
            id=req.event_id,
            timestamp=datetime.fromisoformat(req.timestamp.replace("Z", "+00:00")),
            source=req.source,
            event_type=req.event_type,
            command=f"{req.tool_name}: result",
            data={
                "correlation_id": req.correlation_id,
                "tool_name": req.tool_name,
                "result_summary": req.result_summary,
                "is_error": req.is_error,
                "error_message": req.error_message,
                "duration_ms": req.duration_ms,
                "agent_id": req.agent_id,
                "session_id": req.session_id,
                **req.data,
            },
        )
        asyncio.create_task(self._store_event(event))

        return web.Response(status=204)

    async def handle_lifecycle(self, request: web.Request) -> web.Response:
        """Handle POST /api/v1/lifecycle."""
        try:
            body = await request.json()
            req = LifecycleRequest.model_validate(body)
        except (ValueError, ValidationError) as e:
            return web.json_response(
                {"error": "invalid_request", "detail": str(e)},
                status=400,
            )

        # Store as event
        event = Event(
            id=req.event_id,
            timestamp=datetime.fromisoformat(req.timestamp.replace("Z", "+00:00")),
            source=req.source,
            event_type=req.event_type,
            data={
                "agent_id": req.agent_id,
                "session_id": req.session_id,
                **req.data,
            },
        )
        asyncio.create_task(self._store_event(event))

        return web.Response(status=204)

    async def handle_health(self, request: web.Request) -> web.Response:
        """Handle GET /api/v1/health."""
        uptime = time.monotonic() - self.start_time

        response = HealthResponse(
            status="ok",
            version=CONTRACT_VERSION,
            rules_loaded=len(self.detection_engine.rules),
            uptime_seconds=round(uptime, 1),
        )

        return web.json_response(response.model_dump())

    def _request_to_event(self, req: EvaluationRequest) -> Event:
        """Convert an EvaluationRequest to an Event model."""
        return Event(
            id=req.event_id,
            timestamp=datetime.fromisoformat(req.timestamp.replace("Z", "+00:00")),
            source=req.source,
            event_type=req.event_type,
            command=req.command,
            working_dir=req.working_dir,
            data={
                "tool_name": req.tool_name,
                "agent_id": req.agent_id,
                "session_id": req.session_id,
                **req.params,
                **req.data,
            },
        )

    async def _store_event_and_alerts(
        self, event: Event, alerts: list
    ) -> None:
        """Store event and alerts in background."""
        try:
            await self.event_store.insert(event)
            for alert in alerts:
                await self.alert_store.insert(alert)
        except Exception as e:
            logger.error("Background storage failed: %s", e)

    async def _store_event(self, event: Event) -> None:
        """Store a single event in background."""
        try:
            await self.event_store.insert(event)
        except Exception as e:
            logger.error("Background event storage failed: %s", e)

    @staticmethod
    def _parse_threshold(threshold_str: str):
        """Parse block threshold string to AlertLevel."""
        from agentshield.models.alerts import AlertLevel
        mapping = {
            "critical": AlertLevel.CRITICAL,
            "high": AlertLevel.HIGH,
            "medium": AlertLevel.MEDIUM,
            "low": AlertLevel.LOW,
        }
        return mapping.get(threshold_str.lower(), AlertLevel.HIGH)
```

### 3.5 HTTP Server (`realtime/server.py`)

```python
"""Real-time evaluation HTTP server."""

import logging
from pathlib import Path

from aiohttp import web

from agentshield.detection.engine import DetectionEngine
from agentshield.realtime.auth import create_auth_middleware
from agentshield.realtime.handlers import RealtimeHandlers
from agentshield.store.alerts import AlertStore
from agentshield.store.events import EventStore

logger = logging.getLogger(__name__)


class RealtimeServer:
    """HTTP server for real-time tool call evaluation."""

    def __init__(
        self,
        detection_engine: DetectionEngine,
        event_store: EventStore,
        alert_store: AlertStore,
        host: str = "127.0.0.1",
        port: int = 8432,
        auth_token: str = "",
        auth_required: bool = True,
        block_threshold: str = "high",
    ) -> None:
        self.host = host
        self.port = port

        self.handlers = RealtimeHandlers(
            detection_engine=detection_engine,
            event_store=event_store,
            alert_store=alert_store,
            block_threshold=block_threshold,
        )

        # Build aiohttp app
        middlewares = []
        if auth_token or auth_required:
            middlewares.append(create_auth_middleware(auth_token, auth_required))

        self.app = web.Application(middlewares=middlewares)
        self.app.router.add_post("/api/v1/evaluate", self.handlers.handle_evaluate)
        self.app.router.add_post("/api/v1/audit", self.handlers.handle_audit)
        self.app.router.add_post("/api/v1/lifecycle", self.handlers.handle_lifecycle)
        self.app.router.add_get("/api/v1/health", self.handlers.handle_health)

        self._runner: web.AppRunner | None = None

    async def start(self) -> None:
        """Start the HTTP server."""
        self._runner = web.AppRunner(self.app)
        await self._runner.setup()

        site = web.TCPSite(self._runner, self.host, self.port)
        await site.start()

        logger.info("Real-time evaluation server started on %s:%d", self.host, self.port)

    async def stop(self) -> None:
        """Stop the HTTP server."""
        if self._runner:
            await self._runner.cleanup()
            logger.info("Real-time evaluation server stopped")
```

### 3.6 Configuration Changes (`config.py`)

Add a new `RealtimeConfig` model nested inside `Settings`:

```python
class RealtimeConfig(BaseModel):
    """Configuration for the real-time evaluation server."""
    enabled: bool = False
    host: str = "127.0.0.1"
    port: int = 8432
    auth_required: bool = True
    auth_token: str = ""
    block_threshold: str = "high"  # critical | high | medium | low
    target_latency_ms: int = 50
    max_request_size_bytes: int = 65536
```

Add to `Settings`:
```python
realtime: RealtimeConfig = Field(default_factory=RealtimeConfig)
```

Update `load_config()` to parse the `realtime` section from YAML.

### 3.7 Daemon Dual-Mode (`daemon.py`)

Modify `MonitorDaemon` to optionally start the real-time server:

```python
# In MonitorDaemon.__init__():
self._realtime_server: RealtimeServer | None = None

# In MonitorDaemon.initialize():
if config.realtime.enabled:
    self._realtime_server = RealtimeServer(
        detection_engine=self._detection_engine,
        event_store=self._event_store,
        alert_store=self._alert_store,
        host=config.realtime.host,
        port=config.realtime.port,
        auth_token=config.realtime.auth_token,
        auth_required=config.realtime.auth_required,
        block_threshold=config.realtime.block_threshold,
    )

# In MonitorDaemon.run():
async def run(self):
    self._running = True
    self._stop_event = asyncio.Event()

    tasks = []

    # Start real-time server if enabled
    if self._realtime_server:
        await self._realtime_server.start()

    try:
        # Existing polling loop (unchanged)
        await self._poll_loop()
    finally:
        if self._realtime_server:
            await self._realtime_server.stop()
        self._running = False
```

The polling loop (`_poll_loop`) is extracted from the current `run()` method body -- no logic changes, just refactored into a named method for clarity.

### 3.8 CLI Command (`cli.py`)

Add a new `realtime-server` command for running the server standalone (without the log polling daemon):

```python
@app.command("realtime-server")
def realtime_server(
    host: str = typer.Option("127.0.0.1", "--host", "-H", help="Bind address"),
    port: int = typer.Option(8432, "--port", "-p", help="Bind port"),
):
    """Start the real-time evaluation server (standalone, without log monitoring)."""
    config = load_config()
    _setup_logging(config.log_level)

    async def run():
        # Initialise stores and detection engine
        event_store = EventStore(config.db_path)
        await event_store.initialize()
        alert_store = AlertStore(config.db_path)
        await alert_store.initialize()

        engine = DetectionEngine(config.rules_dir)
        engine.load_rules()

        server = RealtimeServer(
            detection_engine=engine,
            event_store=event_store,
            alert_store=alert_store,
            host=host,
            port=port,
            auth_token=config.realtime.auth_token,
            auth_required=config.realtime.auth_required,
            block_threshold=config.realtime.block_threshold,
        )

        await server.start()

        # Wait until interrupted
        stop_event = asyncio.Event()
        try:
            await stop_event.wait()
        except asyncio.CancelledError:
            pass
        finally:
            await server.stop()

    try:
        asyncio.run(run())
    except KeyboardInterrupt:
        console.print("\n[bold yellow]Server stopped.[/bold yellow]")
```

---

## 4. Database Schema Changes

**None required.** The existing schema handles real-time events without modification:

- `events` table: stores all events. Real-time events have `source="openclaw"`.
- `alerts` table: stores alerts. Real-time alerts have the same structure.
- `feedback` table: unchanged.

The existing indexes on `timestamp`, `event_type`, `rule_id`, and `verdict` remain sufficient. If query performance becomes an issue with high-volume real-time events, a future optimisation can add an index on `source`.

---

## 5. Backward Compatibility

| Existing Feature | Impact | Justification |
|---|---|---|
| Log polling daemon | None | Polling loop code unchanged; realtime server is additive |
| Sigma rules | None | Same `DetectionEngine.evaluate()` API; rules match on same fields |
| LLM triage | None | Async background; not on real-time critical path |
| Desktop notifications | Enhanced | Also fires for real-time alerts (background) |
| MCP server | None | Separate service; unchanged |
| CLI commands | Enhanced | New `realtime-server` command; existing commands unchanged |
| Database | None | Same schema; new events have `source="openclaw"` |
| Configuration | Compatible | New `realtime` section; all existing fields unchanged |
| Tests | None | Existing tests unaffected; new test files added |

---

## 6. Test Plan

### 6.1 Unit Tests (`test_realtime_models.py`)

- `test_evaluation_request_valid` -- valid request parses correctly
- `test_evaluation_request_missing_required_field` -- rejects missing `event_id`, `timestamp`, etc.
- `test_evaluation_request_extra_fields_ignored` -- forward compatibility
- `test_evaluation_response_serialisation` -- JSON output matches contract
- `test_audit_request_valid` -- valid audit request
- `test_lifecycle_request_valid` -- valid lifecycle request
- `test_health_response_serialisation` -- health JSON output

### 6.2 Action Logic Tests (`test_realtime_action.py`)

- `test_decide_action_no_alerts_returns_allow`
- `test_decide_action_low_alert_returns_log`
- `test_decide_action_medium_alert_returns_log`
- `test_decide_action_high_alert_returns_block`
- `test_decide_action_critical_alert_returns_block`
- `test_decide_action_custom_threshold_medium`
- `test_decide_action_custom_threshold_critical`
- `test_build_block_reason_single_alert`
- `test_build_block_reason_multiple_alerts`

### 6.3 Authentication Tests (`test_realtime_auth.py`)

- `test_valid_token_passes`
- `test_invalid_token_returns_401`
- `test_missing_token_returns_401`
- `test_health_endpoint_skips_auth`
- `test_auth_disabled_accepts_any_token`

### 6.4 Handler Tests (`test_realtime_server.py`)

Using `aiohttp.test_utils.AioHTTPTestCase`:

- `test_evaluate_benign_command_returns_allow`
- `test_evaluate_rce_command_returns_block`
- `test_evaluate_invalid_json_returns_400`
- `test_evaluate_missing_fields_returns_400`
- `test_evaluate_version_mismatch_returns_422`
- `test_evaluate_oversized_request_returns_413`
- `test_audit_stores_event`
- `test_lifecycle_stores_event`
- `test_health_returns_status`
- `test_evaluate_detection_error_returns_allow`
- `test_evaluate_stores_events_in_background`

### 6.5 Integration Tests (`test_realtime_integration.py`)

- `test_full_flow_evaluate_block_and_store` -- POST evaluate with RCE command, verify block response, verify event and alert stored in DB
- `test_full_flow_evaluate_allow_and_store` -- POST evaluate with benign command, verify allow, verify event stored
- `test_audit_event_stored_with_correlation` -- POST audit, verify correlation_id linkage
- `test_lifecycle_session_start_stored` -- POST lifecycle session_start, verify in EventStore
- `test_daemon_dual_mode_both_paths_work` -- start daemon with realtime enabled, verify both polling and HTTP work

### 6.6 Contract Compliance Tests

Using the canonical test fixtures from the integration contract (Section 13.2):

- `test_contract_benign_ls_command`
- `test_contract_rce_curl_pipe_bash`
- `test_contract_credential_file_read`

These tests validate that AgentShield's responses match the expected actions from the contract test fixture.

### 6.7 Latency Benchmarks

- `test_evaluate_latency_under_50ms` -- 100 sequential evaluations, assert p99 < 50ms
- `test_evaluate_latency_p50_under_10ms` -- assert p50 < 10ms

---

## 7. Implementation Order

The implementation proceeds in dependency order:

| Step | Module | Dependencies | Estimated Effort |
|---|---|---|---|
| 1 | `realtime/models.py` | None | Small |
| 2 | `realtime/action.py` | `models/alerts.py` | Small |
| 3 | `realtime/auth.py` | None | Small |
| 4 | `tests/test_realtime_models.py` | Step 1 | Small |
| 5 | `tests/test_realtime_action.py` | Step 2 | Small |
| 6 | `tests/test_realtime_auth.py` | Step 3 | Small |
| 7 | `realtime/handlers.py` | Steps 1-3 + detection engine + stores | Medium |
| 8 | `realtime/server.py` | Step 7 | Small |
| 9 | `config.py` changes | Step 8 | Small |
| 10 | `tests/test_realtime_server.py` | Steps 7-9 | Medium |
| 11 | `daemon.py` changes | Step 8 | Small |
| 12 | `cli.py` changes | Steps 8-9 | Small |
| 13 | `tests/test_realtime_integration.py` | Steps 7-12 | Medium |
| 14 | Contract compliance tests | Step 13 | Small |
| 15 | Latency benchmarks | Step 13 | Small |

Total: 15 steps, mostly small. The core implementation (Steps 1-8) is straightforward as it reuses existing `DetectionEngine`, `EventStore`, and `AlertStore`.

---

## 8. Dependencies

### New Python Dependencies

| Package | Purpose | Justification |
|---|---|---|
| `aiohttp` | HTTP server | Lightweight async HTTP, well-suited for low-latency. Already in the Python ecosystem alongside aiosqlite/aiofiles used by AgentShield. |

No other new dependencies required. All other imports (`pydantic`, `aiosqlite`, `asyncio`) are already in the project.

### Alternative: `uvicorn` + `starlette`

If the team prefers ASGI over aiohttp, the server can use `uvicorn` + `starlette`. The handler logic and models remain identical. The choice is an implementation detail that does not affect the contract.

---

## 9. Risks and Mitigations

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| aiohttp adds startup time | Low | Low | Server starts in < 100ms; parallel with daemon init |
| SQLite contention from concurrent writes | Medium | Low | WAL mode already handles this; writes are async/background |
| Large rule set slows evaluation | Low | Medium | Monitor p99 latency; rule count is expected to stay < 50 |
| Auth token exposed in config | Low | Medium | File permissions (600); documented security guidance |
| Port conflict with other services | Low | Low | Configurable port; clear error message on bind failure |

---

## 10. Open Questions

1. **Should `aiohttp` or `uvicorn`+`starlette` be the HTTP framework?** Recommendation: `aiohttp` for simplicity and direct async control. Can switch later without contract changes.

2. **Should real-time events trigger LLM triage?** Recommendation: yes, but only as a background task after the synchronous response. The triage result updates the alert record for future reference and feedback.

3. **Should we add a `/api/v1/rules` endpoint for OpenClaw to discover available rules?** Not in v1. Could be useful for displaying available protections in OpenClaw's UI.
