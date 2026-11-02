"""Mistral, Cohere, Groq and Together integrations."""

from __future__ import annotations

import importlib
from typing import Any, Dict, Optional

from .._usage import add_usage, record_openai_usage
from ._common import Accumulator, LLMSpec, make_wrapper, patch_attr
from .openai import ChatCompletionAccumulator, chat_spec


# ---------------------------------------------------------------- mistral
class MistralStreamAccumulator(ChatCompletionAccumulator):
    """Mistral streams CompletionEvent objects whose ``data`` is an OpenAI-shaped chunk."""

    def add(self, c: Dict[str, Any]) -> None:
        super().add(c.get("data", c) if isinstance(c, dict) else c)


def _mistral_usage(call: Any, out: Any) -> None:
    if isinstance(out, dict):
        record_openai_usage(out, (call.inputs or {}).get("model"), call)


def patch_mistral() -> bool:
    try:
        mod = importlib.import_module("mistralai.chat")
    except ImportError:
        return False
    cls = getattr(mod, "Chat", None)
    if cls is None:
        return False
    patched = False
    for meth, is_async, stream in (("complete", False, False), ("complete_async", True, False), ("stream", False, True), ("stream_async", True, True)):
        name = "mistralai.chat.stream" if stream else "mistralai.chat.complete"
        spec = LLMSpec(
            name,
            is_stream=(lambda kw, res: True) if stream else (lambda kw, res: False),
            accumulator=MistralStreamAccumulator,
            record_usage=_mistral_usage,
        )
        patched |= patch_attr(cls, meth, lambda o, s=spec, a=is_async: make_wrapper(o, s, a))
    return patched


# ---------------------------------------------------------------- cohere
class CohereStreamAccumulator(Accumulator):
    def __init__(self) -> None:
        self.text = ""
        self.final: Dict[str, Any] = {}
        self.id: Optional[str] = None

    def add(self, e: Dict[str, Any]) -> None:
        if not isinstance(e, dict):
            return
        t = e.get("type") or e.get("event_type")
        if t == "message-start":
            self.id = e.get("id")
        elif t == "content-delta":
            self.text += ((((e.get("delta") or {}).get("message") or {}).get("content") or {}).get("text") or "")
        elif t == "text-generation":  # v1
            self.text += e.get("text") or ""
        elif t == "message-end":
            self.final = dict(e.get("delta") or {})
        elif t == "stream-end":  # v1
            resp = e.get("response") or {}
            self.final = {"finish_reason": e.get("finish_reason"), "usage": resp.get("meta")}

    def result(self) -> Dict[str, Any]:
        return {
            "id": self.id,
            "message": {"role": "assistant", "content": [{"type": "text", "text": self.text}]},
            "finish_reason": self.final.get("finish_reason"),
            "usage": self.final.get("usage"),
        }


def record_cohere_usage(call: Any, out: Any) -> None:
    if not isinstance(out, dict):
        return
    u = out.get("usage") or out.get("meta") or {}
    tokens = (u.get("tokens") or u.get("billed_units") or {}) if isinstance(u, dict) else {}
    if not tokens:
        return
    add_usage(call, (call.inputs or {}).get("model") or "cohere", tokens.get("input_tokens") or 0, tokens.get("output_tokens") or 0)


def patch_cohere() -> bool:
    try:
        mod = importlib.import_module("cohere")
    except ImportError:
        return False
    patched = False
    for cls_name, is_async in (("ClientV2", False), ("AsyncClientV2", True), ("Client", False), ("AsyncClient", True)):
        cls = getattr(mod, cls_name, None)
        if cls is None:
            continue
        chat = LLMSpec("cohere.chat", is_stream=lambda kw, res: False, accumulator=CohereStreamAccumulator, record_usage=record_cohere_usage)
        stream = LLMSpec("cohere.chat_stream", is_stream=lambda kw, res: True, accumulator=CohereStreamAccumulator, record_usage=record_cohere_usage)
        if hasattr(cls, "chat"):
            patched |= patch_attr(cls, "chat", lambda o, a=is_async: make_wrapper(o, chat, a))
        if hasattr(cls, "chat_stream"):
            # chat_stream is a (async) generator method: wrap without awaiting
            patched |= patch_attr(cls, "chat_stream", lambda o, a=is_async: make_wrapper(o, stream, False, returns_async_iter=a))
    return patched


# ---------------------------------------------------------------- OpenAI-compatible SDKs
def _patch_openai_shaped(module: str, classes: Dict[str, bool], prefix: str, include_usage: bool) -> bool:
    try:
        mod = importlib.import_module(module)
    except ImportError:
        return False
    patched = False
    for cls_name, is_async in classes.items():
        cls = getattr(mod, cls_name, None)
        if cls is not None:
            patched |= patch_attr(cls, "create", lambda o, a=is_async: make_wrapper(o, chat_spec(prefix, include_usage), a))
    return patched


def patch_groq() -> bool:
    """Groq reports streamed usage in ``x_groq.usage`` (no stream_options needed)."""
    return _patch_openai_shaped("groq.resources.chat.completions", {"Completions": False, "AsyncCompletions": True}, "groq", False)


def patch_together() -> bool:
    return _patch_openai_shaped("together.resources.chat.completions", {"ChatCompletions": False, "AsyncChatCompletions": True}, "together", False)


