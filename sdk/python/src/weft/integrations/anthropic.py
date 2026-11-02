"""Anthropic integration: messages.create (sync/async, stream=True) and messages.stream()."""

from __future__ import annotations

import importlib
import json
from typing import Any, Dict, List, Optional

from .._usage import add_usage, anthropic_usage
from ._common import Accumulator, LLMSpec, _inputs, _tracing, finish_call, make_wrapper, patch_attr, start_call, to_dict


class MessageAccumulator(Accumulator):
    """Rebuilds a Message dict from ``stream=True`` server-sent events."""

    def __init__(self) -> None:
        self.msg: Dict[str, Any] = {}
        self.content: List[Dict[str, Any]] = []

    def add(self, e: Dict[str, Any]) -> None:
        if not isinstance(e, dict):
            return
        t = e.get("type")
        if t == "message_start":
            self.msg = dict(e.get("message") or {})
            self.msg["usage"] = dict(self.msg.get("usage") or {})
        elif t == "content_block_start":
            idx = e.get("index", len(self.content))
            while len(self.content) <= idx:
                self.content.append({})
            self.content[idx] = dict(e.get("content_block") or {})
        elif t == "content_block_delta":
            idx = e.get("index", 0)
            while len(self.content) <= idx:
                self.content.append({"type": "text", "text": ""})
            block = self.content[idx]
            d = e.get("delta") or {}
            dt = d.get("type")
            if dt == "text_delta":
                block["text"] = (block.get("text") or "") + d.get("text", "")
            elif dt == "input_json_delta":
                block["_partial_json"] = block.get("_partial_json", "") + d.get("partial_json", "")
            elif dt == "thinking_delta":
                block["thinking"] = (block.get("thinking") or "") + d.get("thinking", "")
            elif dt == "signature_delta":
                block["signature"] = d.get("signature")
        elif t == "content_block_stop":
            idx = e.get("index", 0)
            if idx < len(self.content):
                block = self.content[idx]
                raw = block.pop("_partial_json", None)
                if raw is not None:
                    try:
                        block["input"] = json.loads(raw) if raw else {}
                    except ValueError:
                        block["input"] = raw
        elif t == "message_delta":
            d = e.get("delta") or {}
            for k, v in d.items():
                if v is not None:
                    self.msg[k] = v
            if isinstance(e.get("usage"), dict):
                u = self.msg.setdefault("usage", {})
                for k, v in e["usage"].items():
                    if v is not None:
                        u[k] = v

    def result(self) -> Dict[str, Any]:
        out = dict(self.msg)
        for b in self.content:
            b.pop("_partial_json", None)
        out["content"] = self.content
        return out


def record_anthropic_usage(call: Any, out: Any) -> None:
    if not isinstance(out, dict):
        return
    u = out.get("usage")
    if not isinstance(u, dict):
        return
    n = anthropic_usage(u)  # gross prompt tokens plus the cache read/creation counts
    if n is not None:
        add_usage(call, out.get("model"), **n)


def create_spec(skip_first_arg: bool = True) -> LLMSpec:
    return LLMSpec(
        "anthropic.messages.create",
        accumulator=MessageAccumulator,
        record_usage=record_anthropic_usage,
        skip_first_arg=skip_first_arg,
    )


class _StreamManagerProxy:
    """Wraps ``client.messages.stream(...)`` managers; the call ends on context exit."""

    def __init__(self, mgr: Any, inputs: Dict[str, Any]):
        self._mgr = mgr
        self._inputs = inputs
        self._client: Any = None
        self._call: Any = None
        self._stream: Any = None

    def _start(self) -> None:
        self._client = _tracing("anthropic")
        if self._client is not None:
            self._call = start_call(self._client, LLMSpec("anthropic.messages.stream"), self._inputs)

    def _end(self, exc: Optional[BaseException]) -> None:
        if self._call is None:
            return
        out = None
        try:
            snap = getattr(self._stream, "current_message_snapshot", None)
            out = to_dict(snap) if snap is not None else None
        except Exception:
            out = None
        record_anthropic_usage(self._call, out)
        finish_call(self._client, self._call, out, exc)

    def __enter__(self) -> Any:
        self._start()
        self._stream = self._mgr.__enter__()
        return self._stream

    def __exit__(self, *exc: Any) -> Any:
        self._end(exc[1] if exc and isinstance(exc[1], BaseException) else None)
        return self._mgr.__exit__(*exc)

    async def __aenter__(self) -> Any:
        self._start()
        self._stream = await self._mgr.__aenter__()
        return self._stream

    async def __aexit__(self, *exc: Any) -> Any:
        self._end(exc[1] if exc and isinstance(exc[1], BaseException) else None)
        return await self._mgr.__aexit__(*exc)

    def __getattr__(self, item: str) -> Any:
        return getattr(self._mgr, item)


def _stream_wrapper(orig: Any) -> Any:
    spec = create_spec()

    def stream(*args: Any, **kwargs: Any) -> Any:
        mgr = orig(*args, **kwargs)
        if _tracing("anthropic") is None:
            return mgr
        return _StreamManagerProxy(mgr, _inputs(spec, args, kwargs))

    return stream


def patch_anthropic() -> bool:
    """Patch the installed ``anthropic`` package. Returns False if missing."""
    try:
        mod = importlib.import_module("anthropic.resources.messages")
    except ImportError:
        try:
            mod = importlib.import_module("anthropic.resources")
        except ImportError:
            return False
    patched = False
    for cls_name, is_async in (("Messages", False), ("AsyncMessages", True)):
        cls = getattr(mod, cls_name, None)
        if cls is None:
            continue
        patched |= patch_attr(cls, "create", lambda orig, a=is_async: make_wrapper(orig, create_spec(), a))
        if hasattr(cls, "stream"):
            patched |= patch_attr(cls, "stream", _stream_wrapper)
    return patched
