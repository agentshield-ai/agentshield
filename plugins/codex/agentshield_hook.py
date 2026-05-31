#!/usr/bin/env python3
"""AgentShield hook entry-point for the OpenAI Codex CLI.

Codex invokes this script once per lifecycle event, passing a JSON object on
stdin and reading a JSON decision (and/or an exit code) on stdout.  A single
script handles every event by dispatching on ``hook_event_name``:

- ``PreToolUse``      -- synchronous, blocking security evaluation. Emits a
  ``permissionDecision`` of ``allow`` or ``deny`` (deny == block).
- ``PermissionRequest`` -- synchronous evaluation when Codex asks for approval.
  Emits a ``decision.behavior`` of ``allow`` or ``deny``.
- ``PostToolUse``     -- fire-and-forget audit report after a tool runs.
- ``SessionStart``    -- ``session_start`` lifecycle event + a non-blocking
  engine health check.
- ``Stop``            -- ``session_end`` lifecycle event.

Enforcement model: **blocking**.  Codex's ``PreToolUse`` hook can synchronously
deny a tool call, so AgentShield can prevent dangerous commands from running.
``require_approval`` verdicts are treated fail-closed (deny) unless the operator
opts into surfacing them as Codex approval prompts (``AGENTSHIELD_APPROVAL_MODE``).

The hook output schema (``hookSpecificOutput.hookEventName`` /
``permissionDecision`` / ``permissionDecisionReason`` for ``PreToolUse`` and
``decision.behavior`` for ``PermissionRequest``) and the event names used here
are taken from the official OpenAI Codex CLI hooks reference
(https://developers.openai.com/codex/hooks).

Known limitation -- Codex hooks are a **guardrail, not a complete enforcement
boundary**.  Per the official documentation, hook interception is currently
*incomplete*: only "simple" shell calls are intercepted (the newer
``unified_exec`` streaming-shell path is not fully covered), and non-shell,
non-MCP tools such as ``WebSearch`` are not intercepted at all.  Because Codex
can often achieve equivalent work through a tool path the hook does not see, a
determined bypass is possible.  For full, transport-level enforcement across
every tool call, deploy the AgentShield **MCP Gateway** in front of the agent's
MCP servers in addition to (or instead of) this hook.

Pipeline (mirrors the Hermes connector): normalise -> skip -> intercept ->
circuit-breaker gate -> evaluate -> act -> (audit / lifecycle / health).  The
pre-call ``event_id`` is persisted via a disk-backed correlation tracker so the
``PostToolUse`` audit report can be linked back to its originating evaluation
(the two events run in separate subprocesses).

Exit codes: ``0`` is used for all paths; the decision is conveyed via the JSON
``permissionDecision``/``decision`` fields, which is the documented Codex
contract.  (Exit code ``2`` with a stderr reason is the documented fallback for
a block, but emitting structured JSON is preferred and unambiguous.)
"""

from __future__ import annotations

import json
import logging
import os
import sys
import uuid
from pathlib import Path
from typing import Any, Dict, Optional, Tuple

# Allow running both as a module (``python -m codex.agentshield_hook``) and as a
# standalone script (``python agentshield_hook.py``), which is how Codex invokes
# it.  When run as a script the package is not on ``sys.path``.
if __package__ in (None, ""):
    sys.path.insert(0, str(Path(__file__).resolve().parent.parent))
    from codex.circuit_breaker import CircuitBreaker
    from codex.client import AgentShieldClient
    from codex.config import AgentShieldConfig, load_config
    from codex.correlation import CorrelationTracker
    from codex.event_builder import (
        build_audit_report,
        build_evaluation_request,
        build_lifecycle_event,
    )
    from codex.normalise import normalise_tool_call
else:  # pragma: no cover - exercised only under package import
    from .circuit_breaker import CircuitBreaker
    from .client import AgentShieldClient
    from .config import AgentShieldConfig, load_config
    from .correlation import CorrelationTracker
    from .event_builder import (
        build_audit_report,
        build_evaluation_request,
        build_lifecycle_event,
    )
    from .normalise import normalise_tool_call

logger = logging.getLogger("agentshield")

_SEVERITY_ORDER: Dict[str, int] = {"low": 0, "medium": 1, "high": 2, "critical": 3}
_NOTIFY_THRESHOLD: Dict[str, int] = {"all": 0, "high": 2, "critical": 3, "none": 999}


# ---------------------------------------------------------------------------
# Decision helpers (Codex-native output shapes)
# ---------------------------------------------------------------------------

def _allow_pre_tool(updated_input: Optional[Dict[str, Any]] = None) -> Dict[str, Any]:
    """Build a ``PreToolUse`` allow decision.

    Args:
        updated_input: Optional rewritten tool input (unused here).

    Returns:
        The Codex hook output object that permits the tool call.
    """
    output: Dict[str, Any] = {
        "hookSpecificOutput": {
            "hookEventName": "PreToolUse",
            "permissionDecision": "allow",
        }
    }
    if updated_input is not None:
        output["hookSpecificOutput"]["updatedInput"] = updated_input
    return output


def _deny_pre_tool(reason: str, system_message: Optional[str] = None) -> Dict[str, Any]:
    """Build a ``PreToolUse`` deny (block) decision.

    Args:
        reason: The human-readable reason shown to the model.
        system_message: Optional extra message surfaced to the user.

    Returns:
        The Codex hook output object that blocks the tool call.
    """
    output: Dict[str, Any] = {
        "hookSpecificOutput": {
            "hookEventName": "PreToolUse",
            "permissionDecision": "deny",
            "permissionDecisionReason": reason,
        }
    }
    if system_message:
        output["systemMessage"] = system_message
    return output


def _allow_permission() -> Dict[str, Any]:
    """Build a ``PermissionRequest`` allow decision.

    Returns:
        The Codex hook output object that allows the approval request.
    """
    return {
        "hookSpecificOutput": {
            "hookEventName": "PermissionRequest",
            "decision": {"behavior": "allow"},
        }
    }


def _deny_permission(reason: str) -> Dict[str, Any]:
    """Build a ``PermissionRequest`` deny decision.

    Args:
        reason: The reason shown to the model/user.

    Returns:
        The Codex hook output object that denies the approval request.
    """
    return {
        "hookSpecificOutput": {
            "hookEventName": "PermissionRequest",
            "decision": {"behavior": "deny", "message": reason},
        }
    }


def _emit(output: Optional[Dict[str, Any]]) -> None:
    """Write a JSON hook decision to stdout, if any.

    Args:
        output: The decision object, or ``None`` to emit nothing (implicit allow).
    """
    if output is not None:
        json.dump(output, sys.stdout)
        sys.stdout.write("\n")
        sys.stdout.flush()


# ---------------------------------------------------------------------------
# Pipeline pieces
# ---------------------------------------------------------------------------

def _block_reason(response: Dict[str, Any], default: str) -> str:
    """Derive a block/approval reason from an evaluation response.

    Args:
        response: The ``EvaluationResponse``.
        default: Fallback text when the engine supplies no reason.

    Returns:
        The reason string, optionally enriched with the top triage reasoning.
    """
    reason = response.get("reason") or default
    triage = response.get("triage_results") or []
    if triage:
        triage_reason = triage[0].get("reasoning")
        if triage_reason:
            reason = f"{reason} (Triage: {triage_reason})"
    return reason


def _alert_summary(response: Dict[str, Any], notify_level: str) -> Optional[str]:
    """Build a short user-facing alert summary, honouring the notify threshold.

    Args:
        response: The ``EvaluationResponse``.
        notify_level: The configured notify level.

    Returns:
        A one-line summary string, or ``None`` if below the threshold.
    """
    alerts = response.get("alerts") or []
    if not alerts:
        return None
    threshold = _NOTIFY_THRESHOLD.get(notify_level, 2)
    if not any(_SEVERITY_ORDER.get(a.get("severity", ""), 0) >= threshold for a in alerts):
        return None
    top = alerts[0]
    extra = f" (+{len(alerts) - 1} more)" if len(alerts) > 1 else ""
    return (
        f"AgentShield: {top.get('rule_name', 'rule match')} "
        f"[{top.get('severity', 'unknown')}]{extra}"
    )


def _should_evaluate(
    tool_name: str,
    canonical: str,
    config: AgentShieldConfig,
) -> bool:
    """Apply the skip and intercept filters.

    Args:
        tool_name: The raw Codex tool name.
        canonical: The canonical tool name.
        config: The connector configuration.

    Returns:
        ``True`` if the tool call should be evaluated by the engine.
    """
    skip_set = set(config.skip)
    if tool_name in skip_set or canonical in skip_set:
        return False
    intercept_set = set(config.intercept) if config.intercept else None
    if intercept_set and tool_name not in intercept_set and canonical not in intercept_set:
        return False
    return True


def _extract_common(event: Dict[str, Any]) -> Tuple[str, Dict[str, Any], Optional[str], Optional[str]]:
    """Extract the common fields from a Codex hook event.

    Args:
        event: The parsed stdin JSON.

    Returns:
        A tuple of ``(tool_name, tool_input, session_id, cwd)``.
    """
    tool_name = event.get("tool_name")
    tool_name = tool_name if isinstance(tool_name, str) and tool_name else "unknown"
    tool_input = event.get("tool_input")
    tool_input = tool_input if isinstance(tool_input, dict) else {}
    session_id = event.get("session_id")
    session_id = session_id if isinstance(session_id, str) else None
    cwd = event.get("cwd")
    cwd = cwd if isinstance(cwd, str) else None
    return tool_name, tool_input, session_id, cwd


# ---------------------------------------------------------------------------
# Event handlers
# ---------------------------------------------------------------------------

def handle_pre_tool_use(
    event: Dict[str, Any],
    config: AgentShieldConfig,
    client: AgentShieldClient,
    breaker: CircuitBreaker,
    tracker: Optional[CorrelationTracker] = None,
) -> Optional[Dict[str, Any]]:
    """Handle a ``PreToolUse`` event with a synchronous, blocking evaluation.

    Args:
        event: The parsed stdin JSON.
        config: The connector configuration.
        client: The engine HTTP client.
        breaker: The circuit breaker.
        tracker: Optional correlation tracker; when supplied, the evaluation
            ``event_id`` is recorded for permitted tool calls so the matching
            ``PostToolUse`` audit can be linked back to it.

    Returns:
        A Codex ``PreToolUse`` decision object, or ``None`` for implicit allow.
    """
    tool_name, tool_input, session_id, cwd = _extract_common(event)
    canonical, command = normalise_tool_call(tool_name, tool_input)

    if not _should_evaluate(tool_name, canonical, config):
        return _allow_pre_tool()

    if breaker.is_open():
        logger.warning(
            "AgentShield circuit breaker open, applying %s policy", config.timeout_policy
        )
        if config.timeout_policy == "block":
            return _deny_pre_tool("AgentShield unavailable (fail-closed policy)")
        return _allow_pre_tool()

    request = build_evaluation_request(
        tool_name,
        tool_input,
        session_id=session_id,
        working_dir=cwd,
        canonical_name=canonical,
        command=command,
    )

    try:
        response = client.evaluate(request)
        breaker.record_success()
    except Exception as exc:  # noqa: BLE001 - any failure feeds the breaker
        breaker.record_failure()
        logger.warning("AgentShield evaluation failed: %s", exc)
        if config.timeout_policy == "block":
            return _deny_pre_tool("AgentShield unavailable (fail-closed policy)")
        return _allow_pre_tool()

    action = response.get("action", "allow")
    summary = _alert_summary(response, config.notify)

    def _remember() -> None:
        """Record this evaluation's event_id so the audit can correlate to it."""
        if tracker is not None:
            event_id = request.get("event_id")
            if isinstance(event_id, str) and event_id:
                tracker.push(session_id, canonical, event_id)

    if action == "block":
        reason = _block_reason(response, "Blocked by AgentShield")
        logger.warning("AgentShield blocked %s: %s", tool_name, reason)
        # Blocked: the tool will not run, so there is no PostToolUse to correlate.
        return _deny_pre_tool(reason, system_message=summary)

    if action == "require_approval":
        reason = _block_reason(
            response,
            "AgentShield requires explicit approval before this tool call can proceed",
        )
        if _approval_mode_enabled():
            # Surface to Codex's own approval flow rather than hard-denying.
            # The tool may proceed, so remember the evaluation for correlation.
            # If the user then denies at Codex's prompt the tool will not run and
            # this entry goes stale; the TTL purges it, so correlation stays
            # best-effort (a fresh UUID is used on any miss).
            _remember()
            logger.warning("AgentShield requires approval for %s: %s", tool_name, reason)
            return None  # Implicit allow -> Codex falls through to its prompt.
        logger.warning("AgentShield denied (fail-closed approval) %s: %s", tool_name, reason)
        return _deny_pre_tool(reason, system_message=summary)

    if action == "log" and summary:
        logger.warning(summary)

    # allow / log: the tool proceeds; remember the evaluation for the audit.
    _remember()
    return _allow_pre_tool()


def handle_permission_request(
    event: Dict[str, Any],
    config: AgentShieldConfig,
    client: AgentShieldClient,
    breaker: CircuitBreaker,
) -> Optional[Dict[str, Any]]:
    """Handle a ``PermissionRequest`` event with a synchronous evaluation.

    Args:
        event: The parsed stdin JSON.
        config: The connector configuration.
        client: The engine HTTP client.
        breaker: The circuit breaker.

    Returns:
        A Codex ``PermissionRequest`` decision object, or ``None`` to defer to
        Codex's normal approval flow.

    Note:
        This handler does not push to the correlation tracker. When both
        ``PreToolUse`` and ``PermissionRequest`` fire for a call, ``PreToolUse``
        already records the correlation; a call that reaches execution via
        ``PermissionRequest`` alone simply falls back to a fresh audit
        ``correlation_id``.
    """
    tool_name, tool_input, session_id, cwd = _extract_common(event)
    canonical, command = normalise_tool_call(tool_name, tool_input)

    if not _should_evaluate(tool_name, canonical, config):
        return None  # Defer to Codex.

    if breaker.is_open():
        if config.timeout_policy == "block":
            return _deny_permission("AgentShield unavailable (fail-closed policy)")
        return None

    request = build_evaluation_request(
        tool_name,
        tool_input,
        session_id=session_id,
        working_dir=cwd,
        canonical_name=canonical,
        command=command,
    )

    try:
        response = client.evaluate(request)
        breaker.record_success()
    except Exception as exc:  # noqa: BLE001
        breaker.record_failure()
        logger.warning("AgentShield evaluation failed: %s", exc)
        if config.timeout_policy == "block":
            return _deny_permission("AgentShield unavailable (fail-closed policy)")
        return None

    action = response.get("action", "allow")
    if action in ("block", "require_approval"):
        reason = _block_reason(response, "Denied by AgentShield")
        return _deny_permission(reason)

    # allow / log -> defer to Codex's normal approval handling.
    return None


def handle_post_tool_use(
    event: Dict[str, Any],
    config: AgentShieldConfig,
    client: AgentShieldClient,
    tracker: Optional[CorrelationTracker] = None,
) -> None:
    """Handle a ``PostToolUse`` event by sending a fire-and-forget audit report.

    Args:
        event: The parsed stdin JSON.
        config: The connector configuration (reserved for future filtering).
        client: The engine HTTP client.
        tracker: Optional correlation tracker; when supplied, the audit's
            ``correlation_id`` is taken from the matching ``PreToolUse``
            evaluation, falling back to a fresh UUID only on a miss.
    """
    del config  # Audit is always sent; intercept/skip only gate evaluation.
    tool_name, tool_input, session_id, cwd = _extract_common(event)
    canonical, _ = normalise_tool_call(tool_name, tool_input)
    result = event.get("tool_response")

    correlation_id: Optional[str] = None
    if tracker is not None:
        correlation_id = tracker.pop(session_id, canonical)
    if not correlation_id:
        correlation_id = str(uuid.uuid4())

    report = build_audit_report(
        tool_name,
        tool_input,
        result,
        correlation_id=correlation_id,
        session_id=session_id,
        working_dir=cwd,
        canonical_name=canonical,
    )
    client.send_audit(report)


def handle_session_start(
    event: Dict[str, Any],
    client: AgentShieldClient,
) -> None:
    """Handle a ``SessionStart`` event: lifecycle + non-blocking health check.

    Args:
        event: The parsed stdin JSON.
        client: The engine HTTP client.
    """
    session_id = event.get("session_id")
    session_id = session_id if isinstance(session_id, str) else None
    source = event.get("source")
    data = {"source": source} if isinstance(source, str) else {}

    # Health check is best-effort and must never block the session.
    if not client.health_check():
        logger.warning("AgentShield not reachable (will retry on first tool call)")

    client.send_lifecycle(
        build_lifecycle_event("session_start", session_id=session_id, data=data)
    )


def handle_session_end(
    event: Dict[str, Any],
    client: AgentShieldClient,
) -> None:
    """Handle a ``Stop`` event by emitting a ``session_end`` lifecycle event.

    Args:
        event: The parsed stdin JSON.
        client: The engine HTTP client.
    """
    session_id = event.get("session_id")
    session_id = session_id if isinstance(session_id, str) else None
    client.send_lifecycle(build_lifecycle_event("session_end", session_id=session_id))


def _approval_mode_enabled() -> bool:
    """Return ``True`` if the operator opted into surfacing approval prompts.

    Returns:
        ``True`` when ``AGENTSHIELD_APPROVAL_MODE`` is a truthy value.
    """
    return os.environ.get("AGENTSHIELD_APPROVAL_MODE", "").strip().lower() in (
        "1",
        "true",
        "yes",
        "prompt",
    )


# ---------------------------------------------------------------------------
# Dispatch
# ---------------------------------------------------------------------------

def dispatch(event: Dict[str, Any]) -> Optional[Dict[str, Any]]:
    """Route a Codex hook event to its handler.

    Args:
        event: The parsed stdin JSON.

    Returns:
        A Codex decision object for synchronous events, or ``None`` otherwise.
    """
    config = load_config()
    if not config.enabled:
        return None

    client = AgentShieldClient(config)
    breaker = CircuitBreaker(
        failure_threshold=config.circuit_breaker_failure_threshold,
        recovery_interval_ms=config.circuit_breaker_recovery_interval_ms,
    )
    tracker = CorrelationTracker()

    name = event.get("hook_event_name")

    if name == "PreToolUse":
        return handle_pre_tool_use(event, config, client, breaker, tracker)
    if name == "PermissionRequest":
        return handle_permission_request(event, config, client, breaker)
    if name == "PostToolUse":
        handle_post_tool_use(event, config, client, tracker)
        return None
    if name == "SessionStart":
        handle_session_start(event, client)
        return None
    if name == "Stop":
        handle_session_end(event, client)
        return None

    logger.debug("AgentShield: unhandled hook event %r", name)
    return None


def main(argv: Optional[list[str]] = None) -> int:
    """CLI entry-point: read stdin JSON, dispatch, emit decision.

    Args:
        argv: Unused; present for testability.

    Returns:
        Process exit code (always ``0``; decisions are conveyed via JSON).
    """
    del argv
    logging.basicConfig(level=os.environ.get("AGENTSHIELD_LOG_LEVEL", "WARNING"))

    raw = sys.stdin.read()
    try:
        event = json.loads(raw) if raw.strip() else {}
    except ValueError as exc:
        logger.warning("AgentShield: invalid hook input JSON: %s", exc)
        # Malformed input: do not block Codex on our own parsing failure.
        return 0

    if not isinstance(event, dict):
        return 0

    try:
        output = dispatch(event)
    except Exception as exc:  # noqa: BLE001 - never crash the host CLI
        logger.warning("AgentShield: hook dispatch error: %s", exc)
        output = None

    _emit(output)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
