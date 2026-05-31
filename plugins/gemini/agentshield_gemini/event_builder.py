"""Build evaluation requests, audit reports, and lifecycle events.

Translates Gemini CLI hook payloads into the AgentShield engine's JSON
contract (the same wire envelope as the OpenClaw and Hermes connectors).
"""

from __future__ import annotations

import json
import uuid
from datetime import datetime, timezone
from typing import Any, Dict, Optional

from . import SOURCE
from .normalise import event_type_for_tool_call, normalise_tool_call

MAX_RESULT_SUMMARY_LENGTH = 1000


def _base_envelope(
    event_type: str,
    *,
    session_id: Optional[str] = None,
    agent_id: Optional[str] = None,
) -> Dict[str, Any]:
    """Return the fields shared by every event payload.

    Args:
        event_type: The semantic event type.
        session_id: The Gemini session id, mapped to ``session_id``.
        agent_id: An optional agent identifier.

    Returns:
        A new envelope dict with a fresh ``event_id`` and timestamp.
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
    canonical_name: Optional[str] = None,
    command: Optional[str] = None,
    working_dir: Optional[str] = None,
) -> Dict[str, Any]:
    """Build an ``EvaluationRequest`` for ``POST /api/v1/evaluate``.

    Args:
        tool_name: The Gemini CLI tool name.
        args: The ``tool_input`` arguments.
        session_id: The Gemini session id.
        agent_id: Optional agent identifier.
        canonical_name: Pre-computed canonical name (skips normalisation).
        command: Pre-computed command string (skips normalisation).
        working_dir: The Gemini ``cwd`` field.

    Returns:
        The evaluation request envelope.
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
    canonical_name: Optional[str] = None,
    is_error: Optional[bool] = None,
    error_message: Optional[str] = None,
    duration_ms: float = 0,
    working_dir: Optional[str] = None,
) -> Dict[str, Any]:
    """Build an ``AuditReport`` for ``POST /api/v1/audit``.

    Args:
        tool_name: The Gemini CLI tool name.
        args: The ``tool_input`` arguments.
        result: The tool result (string or JSON-serialisable object).
        correlation_id: Links back to the originating evaluate ``event_id``.
        session_id: The Gemini session id.
        agent_id: Optional agent identifier.
        canonical_name: Pre-computed canonical name (skips normalisation).
        is_error: Explicit error flag; derived from *result* if ``None``.
        error_message: Explicit error text; derived from *result* if ``None``.
        duration_ms: Tool execution duration in milliseconds.
        working_dir: The Gemini ``cwd`` field.

    Returns:
        The audit report envelope.
    """
    if canonical_name is None:
        canonical_name, _ = normalise_tool_call(tool_name, args)

    if is_error is None:
        is_error = isinstance(result, str) and result.startswith("Error")
    if error_message is None:
        error_message = result if (is_error and isinstance(result, str)) else None

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
    """Build a ``LifecycleEvent`` for ``POST /api/v1/lifecycle``.

    Args:
        event_type: ``session_start`` or ``session_end``.
        session_id: The Gemini session id.
        agent_id: Optional agent identifier.
        data: Extra metadata to attach.

    Returns:
        The lifecycle event envelope.
    """
    envelope = _base_envelope(event_type, session_id=session_id, agent_id=agent_id)
    envelope["data"] = data or {}
    return envelope


def _summarise_result(result: Any) -> Optional[str]:
    """Truncate a tool result to :data:`MAX_RESULT_SUMMARY_LENGTH` chars.

    Args:
        result: The raw tool result.

    Returns:
        The (possibly truncated) string summary, or ``None``.
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
