"""AgentShield plugin for Hermes Agent.

Evaluates every tool call against the AgentShield detection engine and
returns block / require_approval / allow / log decisions.  Installs as a
standard Hermes plugin via ``~/.hermes/plugins/agentshield/``.

Hooks registered
~~~~~~~~~~~~~~~~
- ``pre_tool_call``  — synchronous security evaluation before each tool runs
- ``post_tool_call`` — fire-and-forget audit report after each tool completes
- ``on_session_start`` / ``on_session_end`` — lifecycle tracking
"""

from __future__ import annotations

import logging
import threading
import time
import uuid
from collections import deque
from typing import Any, Deque, Dict, Optional, Tuple

from .circuit_breaker import CircuitBreaker
from .client import AgentShieldClient
from .config import parse_config
from .event_builder import (
    build_audit_report,
    build_evaluation_request,
    build_lifecycle_event,
)
from .normalise import normalise_tool_name

logger = logging.getLogger("agentshield")

# Max age (seconds) for entries in the pending evaluations correlation map.
_CORRELATION_TTL_S = 60.0

# Severity ordering for notification filtering.
_SEVERITY_ORDER: Dict[str, int] = {"low": 0, "medium": 1, "high": 2, "critical": 3}
_NOTIFY_THRESHOLD: Dict[str, int] = {"all": 0, "high": 2, "critical": 3, "none": 999}


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

def _apply_timeout_policy(policy: str) -> Optional[Dict[str, Any]]:
    """Return a blocking dict when the policy says *block*, else ``None``."""
    if policy == "block":
        return {
            "block": True,
            "reason": "AgentShield unavailable (fail-closed policy)",
        }
    return None


def _format_alert_message(
    response: Dict[str, Any],
    tool_name: str,
    action: str,
    notify_level: str,
) -> Optional[str]:
    """Build a human-readable alert string for the user, or ``None``."""
    alerts = response.get("alerts") or []
    if not alerts:
        return None

    threshold = _NOTIFY_THRESHOLD.get(notify_level, 2)
    meets = any(_SEVERITY_ORDER.get(a.get("severity", ""), 0) >= threshold for a in alerts)
    if not meets:
        return None

    top = alerts[0]
    severity = top.get("severity", "unknown")
    emoji_map = {"critical": "\U0001f534", "high": "\U0001f7e0", "medium": "\U0001f7e1", "low": "\U0001f535"}
    emoji = emoji_map.get(severity, "\u26aa")

    parts = [
        f"\U0001f6e1\ufe0f AgentShield Alert \u2014 {action} ({severity})",
        f"{emoji} {top.get('rule_name', 'unknown rule')}",
        f"Tool: {tool_name}",
    ]

    command = (top.get("matched_fields") or {}).get("command")
    if isinstance(command, str):
        truncated = command[:200] + "..." if len(command) > 200 else command
        parts.append(f"Command: {truncated}")

    triage = (response.get("triage_results") or [None])[0]
    if triage:
        parts.append(f"Triage: {triage.get('verdict')} (confidence: {triage.get('confidence')})")
        parts.append(f"Reasoning: {triage.get('reasoning', '')}")

    if len(alerts) > 1:
        parts.append(f"(+{len(alerts) - 1} more alert{'s' if len(alerts) > 2 else ''})")

    return "\n".join(parts)


# ---------------------------------------------------------------------------
# Correlation tracker
# ---------------------------------------------------------------------------

class _CorrelationTracker:
    """Thread-safe FIFO queue mapping (task_id, tool_name) -> event_ids."""

    def __init__(self) -> None:
        self._lock = threading.Lock()
        self._pending: Dict[str, Deque[Tuple[str, float]]] = {}

    def push(self, task_id: Optional[str], tool_name: str, event_id: str) -> None:
        key = f"{task_id or ''}:{tool_name}"
        with self._lock:
            bucket = self._pending.setdefault(key, deque())
            bucket.append((event_id, time.monotonic()))

    def pop(self, task_id: Optional[str], tool_name: str) -> Optional[str]:
        key = f"{task_id or ''}:{tool_name}"
        now = time.monotonic()
        with self._lock:
            # Purge stale entries across all buckets opportunistically.
            for k in list(self._pending):
                q = self._pending[k]
                while q and (now - q[0][1]) > _CORRELATION_TTL_S:
                    q.popleft()
                if not q:
                    del self._pending[k]

            bucket = self._pending.get(key)
            if not bucket:
                return None
            event_id, _ = bucket.popleft()
            if not bucket:
                del self._pending[key]
            return event_id


# ---------------------------------------------------------------------------
# Plugin registration entry-point
# ---------------------------------------------------------------------------

def register(ctx: Any) -> None:
    """Hermes plugin entry-point — called once at startup."""

    # Build config from Hermes settings + env vars.
    raw_settings: Dict[str, Any] = {}
    for field_name in (
        "enabled", "endpoint", "auth_token", "timeout_ms", "timeout_policy",
        "intercept", "skip", "notify",
        "circuit_breaker_failure_threshold", "circuit_breaker_recovery_interval_ms",
    ):
        try:
            val = ctx.get_setting(field_name)
            if val is not None:
                raw_settings[field_name] = val
        except Exception:
            pass

    config = parse_config(raw_settings)

    if not config.enabled:
        logger.info("AgentShield plugin disabled")
        return

    client = AgentShieldClient(config)
    breaker = CircuitBreaker(
        failure_threshold=config.circuit_breaker_failure_threshold,
        recovery_interval_ms=config.circuit_breaker_recovery_interval_ms,
    )
    skip_set = set(config.skip)
    intercept_set = set(config.intercept) if config.intercept else None
    correlation = _CorrelationTracker()

    # ---- pre_tool_call: synchronous evaluation --------------------------

    def _on_pre_tool_call(
        tool_name: str,
        args: Dict[str, Any],
        task_id: Optional[str] = None,
        **kwargs: Any,
    ) -> Optional[Dict[str, Any]]:
        """Evaluate a tool call against the AgentShield engine.

        Returns ``{"block": True, "reason": "..."}`` to prevent execution,
        or ``None`` to allow.
        """
        canonical = normalise_tool_name(tool_name)

        # Skip list takes precedence.
        if tool_name in skip_set or canonical in skip_set:
            return None

        # If an intercept list is configured, only evaluate listed tools.
        if intercept_set and tool_name not in intercept_set and canonical not in intercept_set:
            return None

        # Circuit breaker check.
        if breaker.is_open():
            logger.warning(
                "AgentShield circuit breaker open, applying %s policy",
                config.timeout_policy,
            )
            return _apply_timeout_policy(config.timeout_policy)

        request = build_evaluation_request(
            tool_name,
            args if isinstance(args, dict) else {},
            task_id=task_id,
        )

        try:
            response = client.evaluate(request)
            breaker.record_success()

            effective_mode = response.get("effective_mode")
            if effective_mode:
                logger.debug(
                    "AgentShield effective mode for %s: %s",
                    tool_name,
                    effective_mode,
                )

            # Log triage results.
            for triage in response.get("triage_results") or []:
                logger.info(
                    "AgentShield triage: %s (%s) - %s",
                    triage.get("verdict"),
                    triage.get("confidence"),
                    triage.get("reasoning"),
                )

            action = response.get("action", "allow")

            if action == "block":
                triage_reason = (response.get("triage_results") or [{}])[0].get("reasoning") if response.get("triage_results") else None
                reason = response.get("reason") or "Blocked by AgentShield"
                if triage_reason:
                    reason = f"{reason} (Triage: {triage_reason})"

                logger.warning("AgentShield blocked %s: %s", tool_name, reason)
                msg = _format_alert_message(response, tool_name, "blocked", config.notify)
                if msg:
                    logger.warning(msg)

                return {"block": True, "reason": reason}

            if action == "require_approval":
                reason = (
                    response.get("reason")
                    or "AgentShield requires explicit approval before this tool call can proceed"
                )
                logger.warning(
                    "AgentShield requires approval for %s: %s",
                    tool_name,
                    reason,
                )
                msg = _format_alert_message(response, tool_name, "requires approval", config.notify)
                if msg:
                    logger.warning(msg)

                return {"block": True, "reason": reason}

            # High-confidence triage allow overrides rule alerts.
            has_hc_allow = any(
                t.get("verdict") == "allow" and (t.get("confidence") or 0) > 0.8
                for t in (response.get("triage_results") or [])
            )

            if response.get("alerts") and has_hc_allow:
                logger.info(
                    "AgentShield: %d alert(s) for %s, but triage overrides with high-confidence allow",
                    len(response["alerts"]),
                    tool_name,
                )
            elif action == "log" and response.get("alerts"):
                logger.info(
                    "AgentShield logged %d alert(s) for %s",
                    len(response["alerts"]),
                    tool_name,
                )
                msg = _format_alert_message(response, tool_name, "logged", config.notify)
                if msg:
                    logger.warning(msg)

            # Queue for post_tool_call correlation.
            correlation.push(task_id, tool_name, request["event_id"])
            return None  # allow

        except Exception as exc:
            breaker.record_failure()
            logger.warning("AgentShield evaluation failed: %s", exc)
            return _apply_timeout_policy(config.timeout_policy)

    # ---- post_tool_call: fire-and-forget audit --------------------------

    def _on_post_tool_call(
        tool_name: str,
        args: Dict[str, Any],
        result: Any = None,
        task_id: Optional[str] = None,
        **kwargs: Any,
    ) -> None:
        """Send a fire-and-forget audit report after tool execution."""
        corr_id = correlation.pop(task_id, tool_name) or str(uuid.uuid4())

        is_error = isinstance(result, str) and result.startswith("Error")
        error_message = result if is_error else None

        report = build_audit_report(
            tool_name,
            args if isinstance(args, dict) else {},
            result,
            correlation_id=corr_id,
            task_id=task_id,
            is_error=is_error,
            error_message=error_message,
        )
        client.send_audit(report)

    # ---- lifecycle hooks ------------------------------------------------

    def _on_session_start(task_id: Optional[str] = None, **kwargs: Any) -> None:
        event = build_lifecycle_event("session_start", task_id=task_id)
        client.send_lifecycle(event)

    def _on_session_end(task_id: Optional[str] = None, **kwargs: Any) -> None:
        event = build_lifecycle_event("session_end", task_id=task_id)
        client.send_lifecycle(event)

    # ---- register hooks -------------------------------------------------

    ctx.register_hook("pre_tool_call", _on_pre_tool_call)
    ctx.register_hook("post_tool_call", _on_post_tool_call)
    ctx.register_hook("on_session_start", _on_session_start)
    ctx.register_hook("on_session_end", _on_session_end)

    # Startup health check (non-blocking).
    def _health_check() -> None:
        try:
            ok = client.health_check()
            if ok:
                logger.info("AgentShield reachable at %s", config.endpoint)
            else:
                logger.warning(
                    "AgentShield not reachable at %s (will retry on first tool call)",
                    config.endpoint,
                )
        except Exception:
            logger.warning("AgentShield health check failed")

    threading.Thread(target=_health_check, daemon=True).start()
