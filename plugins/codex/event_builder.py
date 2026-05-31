"""Build evaluation requests, audit reports and lifecycle events.

Translates Codex CLI hook payloads into the AgentShield engine's JSON contract
(the same wire format as the OpenClaw and Hermes connectors).
"""

from __future__ import annotations

import json
import uuid
from datetime import datetime, timezone
from typing import Any, Dict, Optional

from .normalise import event_type_for_tool_call, normalise_tool_call

MAX_RESULT_SUMMARY_LENGTH = 1000

SOURCE = "codex"


def _base_envelope(
    event_type: str,
    *,
    session_id: Optional[str] = None,
    agent_id: Optional[str] = None,
) -> Dict[str, Any]:
    """Build the common fields shared by all event payloads.

    Args:
        event_type: The semantic event type.
        session_id: The Codex session id.
        agent_id: Optional agent identifier.

    Returns:
        A dict with the shared envelope fields populated.
    """
    return {
        "event_id": str(uuid.uuid4()),
        "timestamp": datetime.now(timezone.utc).isoformat(),
        "event_type": event_type,
        "source": SOURCE,
        "agent_id": agent_id,
        "session_id": session_id,
    }


def build_evaluation_request(
    tool_name: str,
    args: Dict[str, Any],
    *,
    session_id: Optional[str] = None,
    agent_id: Optional[str] = None,
    working_dir: Optional[str] = None,
    canonical_name: Optional[str] = None,
    command: Optional[str] = None,
) -> Dict[str, Any]:
    """Build an ``EvaluationRequest`` payload for ``POST /api/v1/evaluate``.

    Args:
        tool_name: The raw Codex tool name.
        args: The ``tool_input`` object.
        session_id: The Codex session id (drives behavioural sequencing).
        agent_id: Optional agent identifier.
        working_dir: The Codex ``cwd`` for the turn, if known.
        canonical_name: Pre-computed canonical name (skips normalisation).
        command: Pre-computed command string (skips normalisation).

    Returns:
        A fully populated ``EvaluationRequest`` envelope.
    """
    if canonical_name is None:
        canonical_name, command = normalise_tool_call(tool_name, args)

    params = dict(args)
    if command:
        params["command"] = command

    event_type = event_type_for_tool_call(canonical_name, args)
    envelope = _base_envelope(event_type, session_id=session_id, agent_id=agent_id)
    envelope.update(
        tool_name=canonical_name,
        command=command,
        params=params,
        working_dir=working_dir,
        data={},
    )
    return envelope


def build_audit_report(
    tool_name: str,
    args: Dict[str, Any],
    result: Any,
    *,
    correlation_id: str,
    session_id: Optional[str] = None,
    agent_id: Optional[str] = None,
    working_dir: Optional[str] = None,
    canonical_name: Optional[str] = None,
    is_error: Optional[bool] = None,
    duration_ms: float = 0,
) -> Dict[str, Any]:
    """Build an ``AuditReport`` payload for ``POST /api/v1/audit``.

    Args:
        tool_name: The raw Codex tool name.
        args: The ``tool_input`` object.
        result: The ``tool_response`` from Codex (any JSON value).
        correlation_id: The originating evaluate ``event_id`` (or a fresh UUID).
        session_id: The Codex session id.
        agent_id: Optional agent identifier.
        working_dir: The Codex ``cwd``, if known.
        canonical_name: Pre-computed canonical name (skips normalisation).
        is_error: Explicit error flag from Codex, if available; otherwise it is
            derived from *result*.
        duration_ms: Tool execution duration, if known.

    Returns:
        A fully populated ``AuditReport`` envelope.
    """
    if canonical_name is None:
        canonical_name, _ = normalise_tool_call(tool_name, args)

    if is_error is None:
        is_error = _is_error_result(result)

    error_message = _error_text(result) if is_error else None

    envelope = _base_envelope("tool_result", session_id=session_id, agent_id=agent_id)
    envelope.update(
        correlation_id=correlation_id,
        tool_name=canonical_name,
        result_summary=_summarise_result(result),
        is_error=is_error,
        error_message=error_message,
        duration_ms=duration_ms,
        working_dir=working_dir,
        data={},
    )
    return envelope


def build_lifecycle_event(
    event_type: str,
    *,
    session_id: Optional[str] = None,
    agent_id: Optional[str] = None,
    data: Optional[Dict[str, Any]] = None,
) -> Dict[str, Any]:
    """Build a ``LifecycleEvent`` payload for ``POST /api/v1/lifecycle``.

    Args:
        event_type: ``session_start`` or ``session_end``.
        session_id: The Codex session id.
        agent_id: Optional agent identifier.
        data: Optional extra metadata.

    Returns:
        A fully populated ``LifecycleEvent`` envelope.
    """
    envelope = _base_envelope(event_type, session_id=session_id, agent_id=agent_id)
    envelope["data"] = data or {}
    return envelope


def _is_error_result(result: Any) -> bool:
    """Heuristically determine whether a tool result represents an error.

    Codex's ``tool_response`` shape varies by tool.  We recognise an explicit
    ``is_error``/``error`` field on dict results, and fall back to the
    cross-connector convention of a string result beginning with ``"Error"``.

    Args:
        result: The tool response.

    Returns:
        ``True`` if the result looks like an error.
    """
    if isinstance(result, dict):
        if result.get("is_error") is True or result.get("error"):
            return True
    if isinstance(result, str):
        return result.startswith("Error")
    return False


def _error_text(result: Any) -> Optional[str]:
    """Extract a human-readable error message from a tool result.

    Args:
        result: The tool response.

    Returns:
        The error text, or ``None``.
    """
    if isinstance(result, dict):
        for key in ("error", "error_message", "message"):
            val = result.get(key)
            if isinstance(val, str) and val:
                return val
    if isinstance(result, str):
        return result
    return None


def _summarise_result(result: Any) -> Optional[str]:
    """Summarise a tool result, truncating to ``MAX_RESULT_SUMMARY_LENGTH``.

    Args:
        result: The tool response (any JSON value).

    Returns:
        The truncated string summary, or ``None`` when there is no result.
    """
    if result is None:
        return None

    if isinstance(result, str):
        text = result
    else:
        try:
            text = json.dumps(result)
        except (TypeError, ValueError):
            text = str(result)

    if len(text) > MAX_RESULT_SUMMARY_LENGTH:
        return text[:MAX_RESULT_SUMMARY_LENGTH]

    return text
