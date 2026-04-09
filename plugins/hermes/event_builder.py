"""Build evaluation requests, audit reports, and lifecycle events.

Translates Hermes hook parameters into the AgentShield engine's
JSON contract (same wire format as the OpenClaw plugin).
"""

from __future__ import annotations

import uuid
from datetime import datetime, timezone
from typing import Any, Dict, Optional

from .normalise import normalise_tool_call

MAX_RESULT_SUMMARY_LENGTH = 1000


def build_evaluation_request(
    tool_name: str,
    args: Dict[str, Any],
    *,
    task_id: Optional[str] = None,
    agent_id: Optional[str] = None,
) -> Dict[str, Any]:
    """Build an ``EvaluationRequest`` payload for ``POST /api/v1/evaluate``."""
    canonical, command = normalise_tool_call(tool_name, args)

    params = dict(args)
    if command:
        params["command"] = command

    return {
        "event_id": str(uuid.uuid4()),
        "timestamp": datetime.now(timezone.utc).isoformat(),
        "event_type": "tool_call",
        "tool_name": canonical,
        "source": "hermes",
        "command": command,
        "params": params,
        "agent_id": agent_id,
        "session_id": task_id,
        "working_dir": None,
        "data": {},
    }


def build_audit_report(
    tool_name: str,
    args: Dict[str, Any],
    result: Any,
    *,
    correlation_id: str,
    task_id: Optional[str] = None,
    agent_id: Optional[str] = None,
    is_error: bool = False,
    error_message: Optional[str] = None,
    duration_ms: float = 0,
) -> Dict[str, Any]:
    """Build an ``AuditReport`` payload for ``POST /api/v1/audit``."""
    canonical, _ = normalise_tool_call(tool_name, args)

    return {
        "event_id": str(uuid.uuid4()),
        "correlation_id": correlation_id,
        "timestamp": datetime.now(timezone.utc).isoformat(),
        "event_type": "tool_result",
        "tool_name": canonical,
        "source": "hermes",
        "result_summary": _summarise_result(result),
        "is_error": is_error,
        "error_message": error_message,
        "duration_ms": duration_ms,
        "agent_id": agent_id,
        "session_id": task_id,
        "working_dir": None,
        "data": {},
    }


def build_lifecycle_event(
    event_type: str,
    *,
    task_id: Optional[str] = None,
    agent_id: Optional[str] = None,
    data: Optional[Dict[str, Any]] = None,
) -> Dict[str, Any]:
    """Build a ``LifecycleEvent`` payload for ``POST /api/v1/lifecycle``."""
    return {
        "event_id": str(uuid.uuid4()),
        "timestamp": datetime.now(timezone.utc).isoformat(),
        "event_type": event_type,
        "source": "hermes",
        "agent_id": agent_id,
        "session_id": task_id,
        "data": data or {},
    }


def _summarise_result(result: Any) -> Optional[str]:
    if result is None:
        return None

    if isinstance(result, str):
        text = result
    else:
        try:
            import json

            text = json.dumps(result)
        except (TypeError, ValueError):
            text = str(result)

    if len(text) > MAX_RESULT_SUMMARY_LENGTH:
        return text[:MAX_RESULT_SUMMARY_LENGTH]

    return text
