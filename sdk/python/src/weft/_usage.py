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
        if isinstance(v, bool):
            continue
        if isinstance(v, (int, float)) and v:
            u[k] = u.get(k, 0) + v
        elif isinstance(v, dict):  # e.g. prompt_tokens_details: {"cached_tokens": n}
            d = u.setdefault(k, {})
            if isinstance(d, dict):
                for kk, vv in v.items():
                    if isinstance(vv, (int, float)) and not isinstance(vv, bool) and vv:
                        d[kk] = d.get(kk, 0) + vv


# Prompt caching, the way the server prices it (docs/weft/API.md "Cached tokens"):
# prompt_tokens is the gross prompt count and cache_read_input_tokens /
# cache_creation_input_tokens are the parts of it read from / written to a cache.


def anthropic_usage(u: Any) -> Optional[Dict[str, int]]:
    """Anthropic usage (``input_tokens`` excludes the cache counts) as gross prompt tokens plus the cache fields."""
    if not isinstance(u, dict):
        return None
    if u.get("input_tokens") is None and u.get("output_tokens") is None:
        return None
    read, write = _int(u.get("cache_read_input_tokens")), _int(u.get("cache_creation_input_tokens"))
    return {"prompt_tokens": _int(u.get("input_tokens")) + read + write, "completion_tokens": _int(u.get("output_tokens")),
            "cache_read_input_tokens": read, "cache_creation_input_tokens": write}


def bedrock_usage(u: Any) -> Optional[Dict[str, int]]:
    """Bedrock Converse usage (``inputTokens`` excludes ``cacheReadInputTokens``/``cacheWriteInputTokens``)."""
    if not isinstance(u, dict):
        return None
    read, write = _int(u.get("cacheReadInputTokens")), _int(u.get("cacheWriteInputTokens"))
    out = {"prompt_tokens": _int(u.get("inputTokens")) + read + write, "completion_tokens": _int(u.get("outputTokens")),
           "cache_read_input_tokens": read, "cache_creation_input_tokens": write}
    if u.get("totalTokens") is not None:
        out["total_tokens"] = _int(u.get("totalTokens"))
    return out


def gemini_usage(u: Any) -> Optional[Dict[str, int]]:
    """Gemini / Vertex ``usage_metadata`` (snake or camel case; the prompt count includes cached content)."""
    if not isinstance(u, dict):
        return None

    def g(snake: str) -> Any:
        camel = "".join(w.capitalize() if i else w for i, w in enumerate(snake.split("_")))
        return u.get(snake, u.get(camel))

    out = {"prompt_tokens": _int(g("prompt_token_count")),
           "completion_tokens": _int(g("candidates_token_count")) + _int(g("thoughts_token_count")),
           "cache_read_input_tokens": _int(g("cached_content_token_count"))}
    if g("thoughts_token_count"):
        out["reasoning_tokens"] = _int(g("thoughts_token_count"))
    if g("total_token_count") is not None:
        out["total_tokens"] = _int(g("total_token_count"))
    return out


def usage_from_openai(resp: Dict[str, Any]) -> Optional[Dict[str, Any]]:
    """Normalise OpenAI chat (prompt/completion) or responses (input/output) usage, keeping
    cached prompt tokens (``prompt_tokens_details`` / ``input_tokens_details``)."""
    u = resp.get("usage") if isinstance(resp, dict) else None
    if not isinstance(u, dict):
        return None
    p = u.get("prompt_tokens", u.get("input_tokens"))
    c = u.get("completion_tokens", u.get("output_tokens"))
    if p is None and c is None:
        return None
    t = u.get("total_tokens")
    out: Dict[str, Any] = {"prompt_tokens": _int(p), "completion_tokens": _int(c), "total_tokens": _int(t) if t is not None else _int(p) + _int(c)}
    for key in ("prompt_tokens_details", "input_tokens_details"):
        d = u.get(key)
        if isinstance(d, dict):
            cached = _int(d.get("cached_tokens"))
            write = _int(d.get("cache_write_tokens"))
            if cached:
                out["cache_read_input_tokens"] = cached
                out[key] = {"cached_tokens": cached}
            if write:
                out["cache_creation_input_tokens"] = write
                out.setdefault(key, {})["cache_write_tokens"] = write
            break
    for key in ("completion_tokens_details", "output_tokens_details"):
        d = u.get(key)
        if isinstance(d, dict) and _int(d.get("reasoning_tokens")):
            out["reasoning_tokens"] = _int(d.get("reasoning_tokens"))
            break
    return out


def record_openai_usage(resp: Dict[str, Any], default_model: Optional[str] = None, call: Any = None) -> None:
    call = call if call is not None else get_current_call()
    u = usage_from_openai(resp)
    if call is None or u is None:
        return
    add_usage(call, resp.get("model") or default_model, **u)
