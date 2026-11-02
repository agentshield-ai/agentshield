"""Helpers for recording a call's own LLM usage in ``summary.usage``."""

from __future__ import annotations

from typing import Any, Dict, Optional

from ._context import get_current_call


def _int(v: Any) -> int:
    try:
        return int(v or 0)
    except (TypeError, ValueError):
        return 0


def add_usage(
    call: Any,
    model: Optional[str],
    prompt_tokens: Any = 0,
    completion_tokens: Any = 0,
    total_tokens: Any = None,
    requests: int = 1,
    **extra: Any,
) -> None:
    """Add LLM usage for ``model`` to ``call.summary["usage"]`` (the call's own usage only)."""
    if call is None:
        return
    model = model or "unknown"
    p, c = _int(prompt_tokens), _int(completion_tokens)
    t = _int(total_tokens) if total_tokens is not None else p + c
    usage: Dict[str, Any] = call.summary.setdefault("usage", {})
    u = usage.setdefault(model, {"requests": 0, "prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0})
    u["requests"] = u.get("requests", 0) + requests
    u["prompt_tokens"] = u.get("prompt_tokens", 0) + p
    u["completion_tokens"] = u.get("completion_tokens", 0) + c
    u["total_tokens"] = u.get("total_tokens", 0) + t
    for k, v in extra.items():
        if isinstance(v, (int, float)) and v:
            u[k] = u.get(k, 0) + v


def usage_from_openai(resp: Dict[str, Any]) -> Optional[Dict[str, int]]:
    """Normalise OpenAI chat (prompt/completion) or responses (input/output) usage."""
    u = resp.get("usage") if isinstance(resp, dict) else None
    if not isinstance(u, dict):
        return None
    p = u.get("prompt_tokens", u.get("input_tokens"))
    c = u.get("completion_tokens", u.get("output_tokens"))
    if p is None and c is None:
        return None
    t = u.get("total_tokens")
    return {"prompt_tokens": _int(p), "completion_tokens": _int(c), "total_tokens": _int(t) if t is not None else _int(p) + _int(c)}


def record_openai_usage(resp: Dict[str, Any], default_model: Optional[str] = None, call: Any = None) -> None:
    call = call if call is not None else get_current_call()
    u = usage_from_openai(resp)
    if call is None or u is None:
        return
    add_usage(call, resp.get("model") or default_model, **u)
