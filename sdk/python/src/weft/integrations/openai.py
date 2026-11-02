"""OpenAI integration: chat.completions.create and responses.create (sync/async, streaming)."""

from __future__ import annotations

import importlib
from typing import Any, Dict, Optional

from .._usage import record_openai_usage
from ._common import Accumulator, LLMSpec, make_wrapper, patch_attr


class ChatCompletionAccumulator(Accumulator):
    """Rebuilds a ChatCompletion dict from streamed chunks."""

    def __init__(self) -> None:
        self.base: Dict[str, Any] = {}
        self.choices: Dict[int, Dict[str, Any]] = {}
        self.usage: Optional[Dict[str, Any]] = None

    def add(self, c: Dict[str, Any]) -> None:
        if not isinstance(c, dict):
            return
        for k in ("id", "model", "created", "system_fingerprint", "service_tier"):
            if c.get(k) is not None:
                self.base[k] = c[k]
        if c.get("usage"):
            self.usage = c["usage"]
        for ch in c.get("choices") or []:
            idx = ch.get("index", 0) or 0
            st = self.choices.setdefault(
                idx, {"index": idx, "message": {"role": "assistant", "content": None}, "finish_reason": None}
            )
            msg = st["message"]
            d = ch.get("delta") or {}
            if d.get("role"):
                msg["role"] = d["role"]
            if d.get("content") is not None:
                msg["content"] = (msg["content"] or "") + d["content"]
            if d.get("refusal"):
                msg["refusal"] = (msg.get("refusal") or "") + d["refusal"]
            for tc in d.get("tool_calls") or []:
                tcs = msg.setdefault("tool_calls", [])
                i = tc.get("index", len(tcs)) or 0
                while len(tcs) <= i:
                    tcs.append({"id": None, "type": "function", "function": {"name": "", "arguments": ""}})
                t = tcs[i]
                if tc.get("id"):
                    t["id"] = tc["id"]
                if tc.get("type"):
                    t["type"] = tc["type"]
                fn = tc.get("function") or {}
                if fn.get("name"):
                    t["function"]["name"] += fn["name"]
                if fn.get("arguments"):
                    t["function"]["arguments"] += fn["arguments"]
            if ch.get("finish_reason"):
                st["finish_reason"] = ch["finish_reason"]
            if ch.get("logprobs") is not None:
                st["logprobs"] = ch["logprobs"]

    def result(self) -> Dict[str, Any]:
        out = dict(self.base)
        out["object"] = "chat.completion"
        out["choices"] = [self.choices[k] for k in sorted(self.choices)]
        if self.usage is not None:
            out["usage"] = self.usage
        return out


class ResponsesAccumulator(Accumulator):
    """Keeps the final ``response`` from a Responses API event stream."""

    def __init__(self) -> None:
        self.final: Optional[Dict[str, Any]] = None
        self.text = ""

    def add(self, e: Dict[str, Any]) -> None:
        if not isinstance(e, dict):
            return
        t = e.get("type", "")
        if t in ("response.completed", "response.incomplete", "response.failed") and isinstance(e.get("response"), dict):
            self.final = e["response"]
        elif t == "response.output_text.delta" and isinstance(e.get("delta"), str):
            self.text += e["delta"]

    def result(self) -> Dict[str, Any]:
        if self.final is not None:
            return self.final
        return {"object": "response", "output_text": self.text}


def _chat_prepare(include_usage: bool):
    def prep(kwargs: Dict[str, Any]) -> Dict[str, Any]:
        if include_usage and kwargs.get("stream") and "stream_options" not in kwargs:
            kwargs["stream_options"] = {"include_usage": True}
        return kwargs

    return prep


def _usage(call: Any, out: Any) -> None:
    if isinstance(out, dict):
        record_openai_usage(out, None, call)


def chat_spec(prefix: str = "openai", include_usage: bool = True, skip_first_arg: bool = True) -> LLMSpec:
    return LLMSpec(
        f"{prefix}.chat.completions.create",
        prepare_kwargs=_chat_prepare(include_usage),
        accumulator=ChatCompletionAccumulator,
        record_usage=_usage,
        skip_first_arg=skip_first_arg,
    )


def responses_spec(prefix: str = "openai", skip_first_arg: bool = True) -> LLMSpec:
    return LLMSpec(
        f"{prefix}.responses.create",
        accumulator=ResponsesAccumulator,
        record_usage=_usage,
        skip_first_arg=skip_first_arg,
    )


def patch_openai() -> bool:
    """Patch the installed ``openai`` package (class level). Returns False if missing."""
    try:
        mod = importlib.import_module("openai.resources.chat.completions")
    except ImportError:
        return False
    patched = False
    for cls_name, is_async in (("Completions", False), ("AsyncCompletions", True)):
        cls = getattr(mod, cls_name, None)
        if cls is not None:
            patched |= patch_attr(cls, "create", lambda orig, a=is_async: make_wrapper(orig, chat_spec(), a))
    try:
        rmod = importlib.import_module("openai.resources.responses")
    except ImportError:
        rmod = None
    if rmod is not None:
        for cls_name, is_async in (("Responses", False), ("AsyncResponses", True)):
            cls = getattr(rmod, cls_name, None)
            if cls is not None:
                patched |= patch_attr(cls, "create", lambda orig, a=is_async: make_wrapper(orig, responses_spec(), a))
    return patched


def patch_openai_compatible(client: Any, name: str = "openai", include_usage: bool = True) -> Any:
    """Trace one OpenAI-shaped client instance (``client.chat.completions.create``).

    Useful for OpenAI-compatible SDKs or providers (Groq, Together, vLLM, Ollama, ...).
    ``name`` becomes the op name prefix, e.g. ``"groq"`` → ``groq.chat.completions.create``.
    Returns the client.
    """
    import inspect

    comp = getattr(getattr(client, "chat", None), "completions", None)
    if comp is not None and hasattr(comp, "create"):
        orig = comp.create
        patch_attr(comp, "create", lambda o: make_wrapper(o, chat_spec(name, include_usage, skip_first_arg=False), inspect.iscoroutinefunction(orig)))
    resp = getattr(client, "responses", None)
    if resp is not None and hasattr(resp, "create"):
        orig_r = resp.create
        patch_attr(resp, "create", lambda o: make_wrapper(o, responses_spec(name, skip_first_arg=False), inspect.iscoroutinefunction(orig_r)))
    return client
