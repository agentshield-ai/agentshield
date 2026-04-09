"""Build evaluation requests, audit reports, and lifecycle events.

Translates Hermes hook parameters into the AgentShield engine's
JSON contract (same wire format as the OpenClaw plugin).
"""

from __future__ import annotations

import json
import uuid
from datetime import datetime, timezone
from typing import Any, Dict, Optional, Tuple

from .normalise import event_type_for_tool_call, normalise_tool_call

MAX_RESULT_SUMMARY_LENGTH = 1000


def _base_envelope(
    event_type: str,
    *,
    task_id: Optional[str] = None,
    agent_id: Optional[str] = None,
) -> Dict[str, Any]:
    """Common fields shared by all event payloads."""
    return {
        "event_id": str(uuid.uuid4()),
        "timestamp": datetime.now(timezone.utc).isoformat(),
        "event_type": event_type,
        "source": "hermes",
        "agent_id": agent_id,
        "session_id": task_id,
    }


def build_evaluation_request(
    tool_name: str,
    args: Dict[str, Any],
    *,
    task_id: Optional[str] = None,
    agent_id: Optional[str] = None,
    canonical_name: Optional[str] = None,
    command: Optional[str] = None,
) -> Dict[str, Any]:
    """Build an ``EvaluationRequest`` payload for ``POST /api/v1/evaluate``.

    If *canonical_name* and *command* are provided, normalization is skipped
    (the caller already did it).
    """
    if canonical_name is None:
        canonical_name, command = normalise_tool_call(tool_name, args)

    params = dict(args)
    if command:
        params["command"] = command

    event_type = event_type_for_tool_call(canonical_name, args)
    envelope = _base_envelope(event_type, task_id=task_id, agent_id=agent_id)
    envelope.update(
        tool_name=canonical_name,
        command=command,
        params=params,
        working_dir=None,
        data={},
    )
    return envelope


def build_audit_report(
    tool_name: str,
    args: Dict[str, Any],
    result: Any,
    *,
    correlation_id: str,
    task_id: Optional[str] = None,
    agent_id: Optional[str] = None,
    canonical_name: Optional[str] = None,
    duration_ms: float = 0,
) -> Dict[str, Any]:
    """Build an ``AuditReport`` payload for ``POST /api/v1/audit``.

    Error status is derived from *result* automatically.
    """
    if canonical_name is None:
        canonical_name, _ = normalise_tool_call(tool_name, args)

    is_error = isinstance(result, str) and result.startswith("Error")

    envelope = _base_envelope("tool_result", task_id=task_id, agent_id=agent_id)
    envelope.update(
        correlation_id=correlation_id,
        tool_name=canonical_name,
        result_summary=_summarise_result(result),
        is_error=is_error,
        error_message=result if is_error else None,
        duration_ms=duration_ms,
        working_dir=None,
        data={},
    )
    return envelope


def build_lifecycle_event(
    event_type: str,
    *,
    task_id: Optional[str] = None,
    agent_id: Optional[str] = None,
    data: Optional[Dict[str, Any]] = None,
) -> Dict[str, Any]:
    """Build a ``LifecycleEvent`` payload for ``POST /api/v1/lifecycle``."""
    envelope = _base_envelope(event_type, task_id=task_id, agent_id=agent_id)
    envelope["data"] = data or {}
    return envelope


def _summarise_result(result: Any) -> Optional[str]:
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
