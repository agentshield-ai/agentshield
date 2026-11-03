"""Model reasoning in recorded calls (docs/weft/DESIGN.md §2.2).

The server extracts reasoning steps from what model calls send and return
(Anthropic ``thinking`` / ``redacted_thinking`` blocks, OpenAI Responses
``reasoning`` items, ``reasoning_content`` on chat messages, Gemini parts with
``thought: true``, GenAI ``reasoning`` parts) and from the ``weft.reasoning``
call attribute. This module keeps those shapes consistent on the way out:

* ``scrub(value, capture=True)`` replaces opaque reasoning payloads (the
  encrypted ``data`` of redacted thinking, ``encrypted_content`` of Responses
  reasoning items, Bedrock ``redactedContent``) with a short digest, so a
  redacted step is stored as redacted, without its blob, and an input copy of
  it still matches its output step;
* ``scrub(value, capture=False)`` removes reasoning altogether
  (``capture_reasoning=False`` / ``WEFT_CAPTURE_REASONING=false``).
"""

from __future__ import annotations

import base64
import datetime
import hashlib
import json
from typing import Any, Dict, Optional

KINDS = ("thinking", "summary", "redacted")

_DROP = object()
_MAX_DEPTH = 24


def norm_kind(kind: Any) -> str:
    """``thinking`` (default), ``summary`` or ``redacted``."""
    k = str(kind or "").strip().lower()
    if k in ("summary", "reasoning_summary", "summary_text"):
        return "summary"
    if k in ("redacted", "redacted_thinking", "encrypted"):
        return "redacted"
    return "thinking"


def digest(value: Any) -> str:
    """``sha256:<24 hex>`` of an opaque payload (bytes, text or JSON)."""
    if isinstance(value, str) and value.startswith("sha256:"):
        return value
    if isinstance(value, (bytes, bytearray)):
        data = bytes(value)
    elif isinstance(value, str):
        data = value.encode("utf-8", "replace")
    else:
        data = json.dumps(value, sort_keys=True, default=str).encode()
    return "sha256:" + hashlib.sha256(data).hexdigest()[:24]


def _is_reasoning_part(d: Dict[str, Any]) -> bool:
    t = d.get("type")
    if t in ("thinking", "redacted_thinking", "reasoning", "reasoning_text"):
        return True
    if d.get("thought") is True:  # Gemini thought part
        return True
    if "reasoningContent" in d and not any(k in d for k in ("text", "toolUse", "toolResult", "image", "document")):
        return True  # Bedrock Converse reasoning block
    return False


def _scrub(v: Any, capture: bool, depth: int) -> Any:
    if depth > _MAX_DEPTH:
        return v
    if isinstance(v, list):
        out = []
        for x in v:
            y = _scrub(x, capture, depth + 1)
            if y is not _DROP:
                out.append(y)
        return out
    if not isinstance(v, dict):
        return v
    if not capture and _is_reasoning_part(v):
        return _DROP
    out: Dict[str, Any] = {}
    message_like = "role" in v
    for k, x in v.items():
        if not capture:
            if k in ("reasoning_content", "reasoning_details", "reasoningContent"):
                continue
            if k in ("reasoning", "thinking") and isinstance(x, str) and message_like:
                continue
        y = _scrub(x, capture, depth + 1)
        if y is _DROP:
            continue
        out[k] = y
    if capture:
        t = out.get("type")
        if t == "redacted_thinking" and out.get("data") is not None:
            out["data"] = digest(out["data"])
        elif t == "reasoning" and out.get("encrypted_content"):
            out["encrypted_content"] = digest(out["encrypted_content"])
        rc = out.get("reasoningContent")
        if isinstance(rc, dict) and rc.get("redactedContent") is not None:
            out["reasoningContent"] = {**rc, "redactedContent": digest(rc["redactedContent"])}
    return out


def scrub(value: Any, capture: bool = True) -> Any:
    """Return ``value`` with reasoning removed (``capture=False``) or with
    opaque reasoning payloads replaced by digests (``capture=True``)."""
    try:
        res = _scrub(value, capture, 0)
    except Exception:  # pragma: no cover - never break recording
        return value
    return None if res is _DROP else res


def has_reasoning(value: Any, depth: int = 0) -> bool:
    """Whether a recorded value holds a reasoning part the server would extract."""
    if depth > _MAX_DEPTH:
        return False
    if isinstance(value, list):
        return any(has_reasoning(x, depth + 1) for x in value)
    if isinstance(value, dict):
        if _is_reasoning_part(value):
            return True
        if isinstance(value.get("reasoning_content"), str) and value["reasoning_content"]:
            return True
        return any(has_reasoning(x, depth + 1) for x in value.values() if isinstance(x, (dict, list)))
    return False


def step(text: str = "", kind: str = "thinking", tokens: Optional[int] = None, at: Optional[datetime.datetime] = None) -> Dict[str, Any]:
    """One entry of the ``weft.reasoning`` call attribute."""
    k = norm_kind(kind)
    out: Dict[str, Any] = {"kind": k, "text": "" if k == "redacted" else str(text or "")}
    if tokens:
        out["tokens"] = int(tokens)
    when = at or datetime.datetime.now(datetime.timezone.utc)
    out["time"] = when.astimezone(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.%fZ")
    return out


def bedrock_block(block: Dict[str, Any]) -> Dict[str, Any]:
    """A Bedrock Converse ``reasoningContent`` block as an Anthropic-style
    ``thinking`` / ``redacted_thinking`` block (the shape the server reads)."""
    rc = block.get("reasoningContent") or {}
    rt = rc.get("reasoningText") if isinstance(rc, dict) else None
    if isinstance(rt, dict) and (rt.get("text") or rt.get("signature")):
        out = {"type": "thinking", "thinking": rt.get("text") or ""}
        if rt.get("signature"):
            out["signature"] = rt["signature"]
        return out
    red = rc.get("redactedContent") if isinstance(rc, dict) else None
    if red is not None:
        if isinstance(red, str):
            try:
                red = base64.b64decode(red, validate=True)
            except Exception:
                pass
        return {"type": "redacted_thinking", "data": digest(red)}
    return block


def bedrock_content(content: Any) -> Any:
    """Rewrite the ``reasoningContent`` blocks of a Bedrock message content list."""
    if not isinstance(content, list):
        return content
    return [bedrock_block(b) if isinstance(b, dict) and "reasoningContent" in b else b for b in content]
