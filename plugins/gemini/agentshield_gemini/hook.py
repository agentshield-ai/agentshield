"""Gemini CLI hook entry-point for AgentShield.

The Gemini CLI invokes hooks as subprocesses, delivering a JSON payload on
stdin and acting on the hook's exit code and stdout JSON.  A single
executable handles every event; it dispatches on the ``hook_event_name``
field:

* ``BeforeAgent`` -- evaluate the submitted prompt (user_input); detection
  only, never blocks the prompt.
* ``BeforeTool``  -- synchronous, blocking security evaluation. Emits
  ``{"decision": "deny", "reason": ...}`` and exits 0 to block a tool call
  (the engine's ``block`` and -- fail-closed -- ``require_approval`` verdicts).
* ``AfterTool``   -- fire-and-forget post-tool audit report.
* ``SessionStart``/``SessionEnd`` -- fire-and-forget lifecycle events.

Blocking is *real*: Gemini CLI honours a ``deny`` decision from a
``BeforeTool`` hook and aborts the tool call.  This is a genuine
synchronous pre-tool enforcement point, equivalent to the Hermes
``pre_tool_call`` hook.

Pipeline (per the AgentShield connector contract):
    normalise -> skip -> intercept -> circuit-breaker -> evaluate -> act
    -> (separate hook) post-tool audit -> lifecycle -> startup health.
"""

from __future__ import annotations

import json
import logging
import os
import sys
import uuid
from pathlib import Path
from typing import Any, Dict, Optional, Protocol, Tuple

from .circuit_breaker import CircuitBreaker
from .client import AgentShieldClient
from .config import AgentShieldConfig, load_config
from .correlation import CorrelationTracker
from .event_builder import (
    build_audit_report,
    build_evaluation_request,
    build_lifecycle_event,
    build_user_input_request,
)
from .normalise import normalise_tool_call

logger = logging.getLogger("agentshield")


class _Client(Protocol):
    """Structural interface for the subset of the client the handlers use.

    Both :class:`~agentshield_gemini.client.AgentShieldClient` and test
    doubles satisfy this protocol.
    """

    def evaluate(self, request: Dict[str, Any]) -> Dict[str, Any]: ...

    def send_audit(self, report: Dict[str, Any]) -> None: ...

    def send_lifecycle(self, event: Dict[str, Any]) -> None: ...

    def health_check(self) -> bool: ...


_SEVERITY_ORDER: Dict[str, int] = {"low": 0, "medium": 1, "high": 2, "critical": 3}
_NOTIFY_THRESHOLD: Dict[str, int] = {"all": 0, "high": 2, "critical": 3, "none": 999}


# ---------------------------------------------------------------------------
# State directory
# ---------------------------------------------------------------------------

def _state_dir() -> Path:
    """Return the directory holding cross-process connector state."""
    base = os.environ.get("AGENTSHIELD_GEMINI_STATE_DIR")
    if base:
        return Path(base)
    return Path.home() / ".agentshield" / "gemini-state"


def _breaker(config: AgentShieldConfig) -> CircuitBreaker:
    """Construct the disk-backed circuit breaker for this connector."""
    return CircuitBreaker(
        state_path=_state_dir() / "breaker.json",
        failure_threshold=config.circuit_breaker_failure_threshold,
        recovery_interval_ms=config.circuit_breaker_recovery_interval_ms,
    )


def _correlation() -> CorrelationTracker:
    """Construct the disk-backed evaluate -> audit correlation tracker."""
    return CorrelationTracker(state_path=_state_dir() / "correlation.json")


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

def _apply_timeout_policy(policy: str) -> Optional[Dict[str, Any]]:
    """Return a blocking decision dict when policy is *block*, else ``None``.

    Args:
        policy: One of ``allow`` | ``block`` | ``log``.

    Returns:
        ``{"block": True, "reason": ...}`` to fail closed, or ``None`` to
        allow execution.
    """
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
    """Build a human-readable alert string, or ``None`` if below threshold.

    Args:
        response: The engine ``EvaluationResponse``.
        tool_name: The platform tool name (for display).
        action: A short human label such as ``"blocked"``.
        notify_level: The configured minimum severity to surface.

    Returns:
        A multi-line alert message, or ``None`` if no alert meets the
        notify threshold.
    """
    alerts = response.get("alerts") or []
    if not alerts:
        return None

    threshold = _NOTIFY_THRESHOLD.get(notify_level, 2)
    if not any(_SEVERITY_ORDER.get(a.get("severity", ""), 0) >= threshold for a in alerts):
        return None

    top = alerts[0]
    severity = top.get("severity", "unknown")
    parts = [
        f"AgentShield Alert - {action} ({severity})",
        f"Rule: {top.get('rule_name', 'unknown rule')}",
        f"Tool: {tool_name}",
    ]

    command = (top.get("matched_fields") or {}).get("process.command_line")
    if not isinstance(command, str):
        command = (top.get("matched_fields") or {}).get("command")
    if isinstance(command, str):
        truncated = command[:200] + "..." if len(command) > 200 else command
        parts.append(f"Command: {truncated}")

    triage = (response.get("triage_results") or [None])[0]
    if triage:
        parts.append(
            f"Triage: {triage.get('verdict')} (confidence: {triage.get('confidence')})"
        )
        parts.append(f"Reasoning: {triage.get('reasoning', '')}")

    if len(alerts) > 1:
        parts.append(f"(+{len(alerts) - 1} more alert{'s' if len(alerts) > 2 else ''})")

    return "\n".join(parts)


# ---------------------------------------------------------------------------
# Hook input parsing
# ---------------------------------------------------------------------------

def _read_payload(stream: Any = None) -> Dict[str, Any]:
    """Read and parse the JSON hook payload from stdin.

    Args:
        stream: Optional input stream (defaults to ``sys.stdin``); used by tests.

    Returns:
        The parsed payload, or an empty dict if input is missing or invalid.
    """
    stream = stream if stream is not None else sys.stdin
    try:
        raw = stream.read()
    except (OSError, ValueError):
        return {}
    if not raw or not raw.strip():
        return {}
    try:
        data = json.loads(raw)
    except ValueError:
        return {}
    return data if isinstance(data, dict) else {}


def _extract_tool(payload: Dict[str, Any]) -> Tuple[str, Dict[str, Any]]:
    """Extract ``(tool_name, tool_input)`` from a Gemini hook payload.

    Args:
        payload: The parsed hook payload.

    Returns:
        The tool name (``"unknown"`` if absent) and its argument dict.
    """
    tool_name = payload.get("tool_name") or payload.get("original_request_name") or "unknown"
    tool_input = payload.get("tool_input")
    if not isinstance(tool_input, dict):
        tool_input = {}
    return str(tool_name), tool_input


def _extract_result(payload: Dict[str, Any]) -> Tuple[Any, bool, Optional[str]]:
    """Extract ``(result_summary, is_error, error_message)`` from AfterTool.

    Gemini's ``tool_response`` carries ``llmContent``, ``returnDisplay`` and an
    optional ``error`` object.

    Args:
        payload: The parsed AfterTool payload.

    Returns:
        A tuple of a result value for summarising, an error flag, and the
        error message text (or ``None``).
    """
    response = payload.get("tool_response")
    if not isinstance(response, dict):
        return None, False, None

    error = response.get("error")
    if error:
        message = error.get("message") if isinstance(error, dict) else str(error)
        return message, True, message

    result = response.get("llmContent")
    if result is None:
        result = response.get("returnDisplay")
    return result, False, None


# ---------------------------------------------------------------------------
# Event handlers
# ---------------------------------------------------------------------------

def handle_before_tool(
    payload: Dict[str, Any],
    config: AgentShieldConfig,
    client: _Client,
) -> Dict[str, Any]:
    """Evaluate a tool call and return a Gemini ``BeforeTool`` hook decision.

    Args:
        payload: The parsed ``BeforeTool`` payload.
        config: The connector configuration.
        client: The AgentShield HTTP client.

    Returns:
        A Gemini hook output dict. ``{}`` allows the tool to proceed;
        ``{"decision": "deny", "reason": ...}`` blocks it.
    """
    tool_name, args = _extract_tool(payload)
    session_id = payload.get("session_id")
    working_dir = payload.get("cwd")

    canonical, command = normalise_tool_call(tool_name, args)

    skip_set = set(config.skip)
    if tool_name in skip_set or canonical in skip_set:
        return {}

    intercept_set = set(config.intercept) if config.intercept else None
    if intercept_set and tool_name not in intercept_set and canonical not in intercept_set:
        return {}

    breaker = _breaker(config)
    if breaker.is_open():
        logger.warning(
            "AgentShield circuit breaker open, applying %s policy",
            config.timeout_policy,
        )
        return _decision(_apply_timeout_policy(config.timeout_policy))

    request = build_evaluation_request(
        tool_name,
        args,
        session_id=session_id,
        canonical_name=canonical,
        command=command,
        working_dir=working_dir,
    )

    try:
        response = client.evaluate(request)
        breaker.record_success()
    except Exception as exc:  # noqa: BLE001 - any failure -> breaker + policy.
        breaker.record_failure()
        logger.warning("AgentShield evaluation failed: %s", exc)
        return _decision(_apply_timeout_policy(config.timeout_policy))

    effective_mode = response.get("effective_mode")
    if effective_mode:
        logger.debug("AgentShield effective mode for %s: %s", tool_name, effective_mode)

    for triage in response.get("triage_results") or []:
        logger.info(
            "AgentShield triage: %s (%s) - %s",
            triage.get("verdict"),
            triage.get("confidence"),
            triage.get("reasoning"),
        )

    action = response.get("action", "allow")

    if action == "block":
        triage_results = response.get("triage_results") or []
        triage_reason = triage_results[0].get("reasoning") if triage_results else None
        reason = response.get("reason") or "Blocked by AgentShield"
        if triage_reason:
            reason = f"{reason} (Triage: {triage_reason})"
        logger.warning("AgentShield blocked %s: %s", tool_name, reason)
        msg = _format_alert_message(response, tool_name, "blocked", config.notify)
        if msg:
            logger.warning(msg)
        return {"decision": "deny", "reason": reason}

    if action == "require_approval":
        # Fail closed: Gemini CLI's own confirmation prompt is the genuine
        # approval mechanism, but AgentShield treats require_approval as a
        # block by default per the connector contract.
        reason = (
            response.get("reason")
            or "AgentShield requires explicit approval before this tool call can proceed"
        )
        logger.warning("AgentShield requires approval for %s: %s", tool_name, reason)
        msg = _format_alert_message(response, tool_name, "requires approval", config.notify)
        if msg:
            logger.warning(msg)
        return {"decision": "deny", "reason": reason}

    # allow / log paths.
    has_hc_allow = any(
        t.get("verdict") == "allow" and (t.get("confidence") or 0) > 0.8
        for t in (response.get("triage_results") or [])
    )

    if response.get("alerts") and has_hc_allow:
        logger.info(
            "AgentShield: %d alert(s) for %s, but triage overrides with "
            "high-confidence allow",
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

    # Record correlation for the matching AfterTool audit.
    _correlation().push(session_id, tool_name, request["event_id"])
    return {}


def handle_after_tool(
    payload: Dict[str, Any],
    config: AgentShieldConfig,
    client: _Client,
) -> Dict[str, Any]:
    """Send a fire-and-forget audit report for a completed tool call.

    Args:
        payload: The parsed ``AfterTool`` payload.
        config: The connector configuration.
        client: The AgentShield HTTP client.

    Returns:
        An empty dict (AfterTool never blocks here).
    """
    del config  # No config-driven behaviour for audit beyond client setup.
    tool_name, args = _extract_tool(payload)
    session_id = payload.get("session_id")
    working_dir = payload.get("cwd")

    corr_id = _correlation().pop(session_id, tool_name) or str(uuid.uuid4())
    canonical, _ = normalise_tool_call(tool_name, args)
    result, is_error, error_message = _extract_result(payload)

    report = build_audit_report(
        tool_name,
        args,
        result,
        correlation_id=corr_id,
        session_id=session_id,
        canonical_name=canonical,
        is_error=is_error,
        error_message=error_message,
        working_dir=working_dir,
    )
    client.send_audit(report)
    return {}


def handle_session_event(
    payload: Dict[str, Any],
    config: AgentShieldConfig,
    client: _Client,
    event_type: str,
) -> Dict[str, Any]:
    """Send a fire-and-forget lifecycle event.

    Args:
        payload: The parsed lifecycle payload.
        config: The connector configuration.
        client: The AgentShield HTTP client.
        event_type: ``session_start`` or ``session_end``.

    Returns:
        An empty dict.
    """
    del config
    session_id = payload.get("session_id")
    data: Dict[str, Any] = {}
    for key in ("source", "reason"):
        if payload.get(key) is not None:
            data[key] = payload[key]

    client.send_lifecycle(
        build_lifecycle_event(event_type, session_id=session_id, data=data)
    )
    # On session start, opportunistically probe engine reachability.
    if event_type == "session_start" and not client.health_check():
        logger.warning(
            "AgentShield not reachable (will retry on first tool call)",
        )
    return {}


def _decision(block: Optional[Dict[str, Any]]) -> Dict[str, Any]:
    """Translate an internal block dict into a Gemini hook decision.

    Args:
        block: ``{"block": True, "reason": ...}`` or ``None``.

    Returns:
        A Gemini ``deny`` decision, or an empty dict to allow.
    """
    if block and block.get("block"):
        return {"decision": "deny", "reason": block.get("reason", "Blocked by AgentShield")}
    return {}


# ---------------------------------------------------------------------------
# Entry-point
# ---------------------------------------------------------------------------

def handle_before_agent(
    payload: Dict[str, Any],
    config: AgentShieldConfig,
    client: _Client,
) -> Dict[str, Any]:
    """Evaluate a submitted prompt (``BeforeAgent``) for injection / manipulation.

    Detection-only: it NEVER blocks the prompt (always returns ``{}``). It posts
    the prompt as a ``user_input`` event so the direct prompt-injection and
    semantic-manipulation rules can fire, and logs a summary when alerts meet the
    notify threshold. Fails open on an empty prompt, an open breaker, or an
    engine error.

    Args:
        payload: The parsed ``BeforeAgent`` payload.
        config: The connector configuration.
        client: The AgentShield HTTP client.

    Returns:
        Always ``{}`` (allow / no-op) — a prompt is never blocked.
    """
    prompt = payload.get("prompt")
    if not isinstance(prompt, str) or not prompt.strip():
        return {}

    breaker = _breaker(config)
    if breaker.is_open():
        return {}

    session_id = payload.get("session_id")
    working_dir = payload.get("cwd")
    request = build_user_input_request(
        prompt, session_id=session_id, working_dir=working_dir
    )

    try:
        response = client.evaluate(request)
        breaker.record_success()
    except Exception as exc:  # noqa: BLE001 - any failure feeds the breaker.
        breaker.record_failure()
        logger.warning("AgentShield prompt evaluation failed: %s", exc)
        return {}

    msg = _format_alert_message(response, "user prompt", "flagged in", config.notify)
    if msg:
        logger.warning(msg)
    return {}


def dispatch(
    payload: Dict[str, Any],
    config: Optional[AgentShieldConfig] = None,
) -> Dict[str, Any]:
    """Route a hook payload to its handler and return the hook output.

    Args:
        payload: The parsed hook payload.
        config: Optional pre-loaded config (loaded from disk if ``None``).

    Returns:
        The Gemini hook output dict (``{}`` to allow / no-op).
    """
    config = config if config is not None else load_config()
    if not config.enabled:
        return {}

    event = payload.get("hook_event_name", "")
    client = AgentShieldClient(config)

    if event == "BeforeAgent":
        return handle_before_agent(payload, config, client)
    if event == "BeforeTool":
        return handle_before_tool(payload, config, client)
    if event == "AfterTool":
        return handle_after_tool(payload, config, client)
    if event == "SessionStart":
        return handle_session_event(payload, config, client, "session_start")
    if event == "SessionEnd":
        return handle_session_event(payload, config, client, "session_end")

    logger.debug("AgentShield: ignoring unsupported hook event %r", event)
    return {}


def main(argv: Optional[list[str]] = None) -> int:
    """CLI entry-point invoked by the Gemini CLI hooks runner.

    Reads the JSON hook payload from stdin, dispatches it, prints any hook
    output JSON to stdout, and returns an exit code (always ``0`` so that a
    block is conveyed via the JSON ``decision`` field rather than a crash).

    Args:
        argv: Unused; present for testability.

    Returns:
        Process exit code.
    """
    del argv
    logging.basicConfig(
        level=os.environ.get("AGENTSHIELD_LOG_LEVEL", "WARNING").upper(),
        stream=sys.stderr,
        format="[agentshield] %(message)s",
    )

    payload = _read_payload()
    try:
        output = dispatch(payload)
    except Exception as exc:  # noqa: BLE001 - never crash the agent loop.
        # On an unexpected internal error, apply the configured timeout_policy
        # (fail-closed by default); fall back to "block" if even config loading
        # fails, so a broken hook denies rather than silently allowing.
        logger.warning("AgentShield hook error: %s", exc)
        try:
            policy = load_config().timeout_policy
        except Exception:  # noqa: BLE001
            policy = "block"
        output = _decision(_apply_timeout_policy(policy))

    if output:
        sys.stdout.write(json.dumps(output))
        sys.stdout.flush()
    return 0


if __name__ == "__main__":  # pragma: no cover
    raise SystemExit(main())
