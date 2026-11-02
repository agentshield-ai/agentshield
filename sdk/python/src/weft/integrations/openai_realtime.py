"""OpenAI Realtime API integration.

The ``openai`` SDK's realtime connections (``client.realtime.connect(...)`` and
``client.beta.realtime.connect(...)``, sync and async) are patched so the
events they send and receive are turned into calls:

* ``openai.realtime.session`` (agent): one per connection, from the first
  event to ``close()``; output = the final session configuration and counts.
* ``openai.realtime.response`` (llm): one per ``response.created`` ...
  ``response.done``; inputs = the conversation items added since the
  previous response (text, input-audio transcripts, function-call outputs),
  the instructions, voice and modalities; output = the response's output
  items, the streamed text/transcript and the number of audio bytes. Usage
  (``response.usage``, including cached and audio token details) is recorded
  under the session's model.
* ``openai.realtime.tool.<name>`` (tool): a function call requested by the
  model, ended when the client sends its ``function_call_output``.

Audio payloads are never recorded, only their size. For a raw websocket
client, feed events to ``RealtimeTracer`` yourself::

    rt = RealtimeTracer()
    rt.on_client_event(event_dict)   # before sending
    rt.on_server_event(event_dict)   # after receiving
    rt.close()
"""

from __future__ import annotations

import functools
import importlib
import inspect
import json
import threading
from typing import Any, Dict, List, Optional

from .._usage import add_usage
from . import _settings
from ._common import _tracing, finish_call, patch_attr, to_dict

INTEGRATION = "openai_realtime"

MODULES = ("openai.resources.realtime.realtime", "openai.resources.beta.realtime.realtime")

_TEXT_DELTAS = (
    "response.text.delta",
    "response.output_text.delta",
    "response.audio_transcript.delta",
    "response.output_audio_transcript.delta",
)
_AUDIO_DELTAS = ("response.audio.delta", "response.output_audio.delta")


def _event(e: Any) -> Dict[str, Any]:
    if isinstance(e, (bytes, str)):
        try:
            e = json.loads(e)
        except ValueError:
            return {}
    d = to_dict(e)
    return d if isinstance(d, dict) else {}


def _b64_len(s: Any) -> int:
    if not isinstance(s, str):
        return 0
    return (len(s) * 3) // 4 - s.count("=", -2)


def _strip_audio(v: Any, depth: int = 0) -> Any:
    """Drop base64 audio from items, keeping transcripts and sizes."""
    if depth > 8:
        return v
    if isinstance(v, dict):
        out = {}
        for k, x in v.items():
            if k in ("audio", "delta") and isinstance(x, str) and len(x) > 64:
                out[k + "_bytes"] = _b64_len(x)
            else:
                out[k] = _strip_audio(x, depth + 1)
        return out
    if isinstance(v, list):
        return [_strip_audio(x, depth + 1) for x in v]
    return v


class RealtimeTracer:
    """Builds weft calls from a Realtime session's client and server events."""

    def __init__(self, model: Optional[str] = None):
        self.model = model
        self.session: Dict[str, Any] = {}
        self.session_call: Any = None
        self.client: Any = None
        self.pending: List[Dict[str, Any]] = []
        self.pending_audio_bytes = 0
        self.response_overrides: Dict[str, Any] = {}
        self.responses: Dict[str, Dict[str, Any]] = {}
        self.tools: Dict[str, Any] = {}
        self.counts = {"responses": 0, "client_events": 0, "server_events": 0}
        self.closed = False
        self._lock = threading.RLock()

    # ---------------------------------------------------------------- calls
    def _start(self, name: str, kind: Optional[str], inputs: Dict[str, Any], parent: Any = None, display: Optional[str] = None) -> Any:
        op = _settings.integration_op(INTEGRATION, name, kind)
        kw: Dict[str, Any] = {"display_name": display, "attributes": _settings.integration_attributes(INTEGRATION)}
        if parent is not None:
            kw["parent"] = parent
        call = self.client.create_call(op, inputs, **kw)
        call._weft_op = op
        return call

    def _ensure_session(self) -> Any:
        if self.session_call is not None or self.closed:
            return self.session_call
        client = _tracing(INTEGRATION)
        if client is None:
            return None
        self.client = client
        cfg = {k: v for k, v in self.session.items() if k not in ("id", "object", "client_secret")}
        self.session_call = self._start("openai.realtime.session", "agent", {"model": self.model, "session": _strip_audio(cfg)},
                                        display=f"Realtime: {self.model}" if self.model else None)
        return self.session_call

    # ---------------------------------------------------------------- events
    def on_client_event(self, event: Any) -> None:
        e = _event(event)
        if not e:
            return
        with self._lock:
            self.counts["client_events"] += 1
            t = e.get("type", "")
            if t == "session.update":
                sess = e.get("session") or {}
                self.session.update(sess)
                self.model = sess.get("model") or self.model
                return
            if t == "input_audio_buffer.append":
                self.pending_audio_bytes += _b64_len(e.get("audio"))
                return
            if t == "response.create":
                self.response_overrides = dict(e.get("response") or {})
                self._ensure_session()
                return
            if t == "conversation.item.create":
                item = e.get("item") or {}
                if item.get("type") == "function_call_output":
                    call = self.tools.pop(item.get("call_id"), None)
                    if call is not None:
                        out = item.get("output")
                        try:
                            out = json.loads(out) if isinstance(out, str) else out
                        except ValueError:
                            pass
                        finish_call(self.client, call, out)
                self.pending.append(_strip_audio(item))
                self._ensure_session()

    def on_server_event(self, event: Any) -> None:
        e = _event(event)
        if not e:
            return
        with self._lock:
            self.counts["server_events"] += 1
            t = e.get("type", "")
            if t in ("session.created", "session.updated"):
                sess = e.get("session") or {}
                self.session.update(sess)
                self.model = sess.get("model") or self.model
                if t == "session.created":
                    self._ensure_session()
            elif t == "conversation.item.input_audio_transcription.completed":
                tr = {"type": "input_audio_transcript", "item_id": e.get("item_id"), "transcript": e.get("transcript")}
                for it in self.pending:
                    if it.get("id") == e.get("item_id"):
                        it["transcript"] = e.get("transcript")
                        break
                else:
                    self.pending.append(tr)
            elif t in ("conversation.item.created", "conversation.item.added"):
                item = e.get("item") or {}
                if item.get("role") == "user" and not any(p.get("id") == item.get("id") for p in self.pending if item.get("id")):
                    self.pending.append(_strip_audio(item))
            elif t == "response.created":
                self._response_start(e.get("response") or {})
            elif t in _TEXT_DELTAS:
                st = self.responses.get(e.get("response_id"))
                if st is not None:
                    st["text"] += e.get("delta") or ""
            elif t in _AUDIO_DELTAS:
                st = self.responses.get(e.get("response_id"))
                if st is not None:
                    st["audio_bytes"] += _b64_len(e.get("delta"))
            elif t == "response.output_item.done":
                item = e.get("item") or {}
                if item.get("type") == "function_call" and self._ensure_session() is not None:
                    args = item.get("arguments")
                    try:
                        args = json.loads(args) if isinstance(args, str) else args
                    except ValueError:
                        pass
                    name = item.get("name") or "function"
                    self.tools[item.get("call_id")] = self._start(
                        f"openai.realtime.tool.{name}", "tool", args if isinstance(args, dict) else {"arguments": args},
                        parent=self.session_call, display=name,
                    )
            elif t == "response.done":
                self._response_end(e.get("response") or {})
            elif t == "error":
                err = e.get("error") or {}
                for rid in list(self.responses):
                    self._response_end({"id": rid, "status": "failed"}, RuntimeError(err.get("message") or str(err)))

    def _response_start(self, resp: Dict[str, Any]) -> None:
        if self._ensure_session() is None:
            return
        rid = resp.get("id") or f"resp_{len(self.responses)}"
        ov = self.response_overrides
        self.response_overrides = {}
        inputs: Dict[str, Any] = {"model": self.model, "input": list(self.pending)}
        if self.pending_audio_bytes:
            inputs["input_audio_bytes"] = self.pending_audio_bytes
        for k in ("instructions", "voice", "modalities", "output_modalities", "tools", "temperature", "max_response_output_tokens"):
            v = ov.get(k, self.session.get(k))
            if v is not None:
                inputs[k] = v
        self.pending = []
        self.pending_audio_bytes = 0
        call = self._start("openai.realtime.response", "llm", inputs, parent=self.session_call)
        self.responses[rid] = {"call": call, "text": "", "audio_bytes": 0}
        self.counts["responses"] += 1

    def _response_end(self, resp: Dict[str, Any], exc: Optional[BaseException] = None) -> None:
        st = self.responses.pop(resp.get("id"), None)
        if st is None:
            return
        call = st["call"]
        u = resp.get("usage")
        if isinstance(u, dict):
            ind = u.get("input_token_details") or {}
            outd = u.get("output_token_details") or {}
            add_usage(call, self.model, u.get("input_tokens") or 0, u.get("output_tokens") or 0, u.get("total_tokens"),
                      cached_tokens=ind.get("cached_tokens") or 0, input_audio_tokens=ind.get("audio_tokens") or 0,
                      output_audio_tokens=outd.get("audio_tokens") or 0)
        out: Dict[str, Any] = {"id": resp.get("id"), "status": resp.get("status"), "output": _strip_audio(resp.get("output") or [])}
        if st["text"]:
            out["text"] = st["text"]
        if st["audio_bytes"]:
            out["audio_bytes"] = st["audio_bytes"]
        if resp.get("status_details"):
            out["status_details"] = resp["status_details"]
        if u is not None:
            out["usage"] = u
        if exc is None and resp.get("status") == "failed":
            exc = RuntimeError(json.dumps(resp.get("status_details") or {"status": "failed"}, default=str))
        finish_call(self.client, call, out, exc)

    def close(self, exc: Optional[BaseException] = None) -> None:
        with self._lock:
            if self.closed:
                return
            self.closed = True
            for rid in list(self.responses):
                self._response_end({"id": rid, "status": "incomplete"}, exc)
            for call in self.tools.values():
                finish_call(self.client, call, None, exc)
            self.tools.clear()
            if self.session_call is not None:
                cfg = {k: v for k, v in self.session.items() if k not in ("client_secret",)}
                finish_call(self.client, self.session_call, {"session": _strip_audio(cfg), **self.counts}, exc)


def _tracer(conn: Any) -> RealtimeTracer:
    t = conn.__dict__.get("_weft_rt") if hasattr(conn, "__dict__") else None
    if t is None:
        t = RealtimeTracer()
        try:
            conn._weft_rt = t
        except Exception:  # pragma: no cover
            pass
    return t


def _safe(fn: Any, *args: Any) -> None:
    try:
        fn(*args)
    except Exception:  # pragma: no cover - never break the connection
        pass


def _wrap_send(orig: Any) -> Any:
    if inspect.iscoroutinefunction(orig):

        @functools.wraps(orig)
        async def asend(self: Any, event: Any, *a: Any, **kw: Any) -> Any:
            _safe(_tracer(self).on_client_event, event)
            return await orig(self, event, *a, **kw)

        return asend

    @functools.wraps(orig)
    def send(self: Any, event: Any, *a: Any, **kw: Any) -> Any:
        _safe(_tracer(self).on_client_event, event)
        return orig(self, event, *a, **kw)

    return send


def _wrap_recv(orig: Any) -> Any:
    if inspect.iscoroutinefunction(orig):

        @functools.wraps(orig)
        async def arecv(self: Any, *a: Any, **kw: Any) -> Any:
            try:
                ev = await orig(self, *a, **kw)
            except Exception as e:
                _safe(_tracer(self).close, e if type(e).__name__ != "ConnectionClosedOK" else None)
                raise
            _safe(_tracer(self).on_server_event, ev)
            return ev

        return arecv

    @functools.wraps(orig)
    def recv(self: Any, *a: Any, **kw: Any) -> Any:
        try:
            ev = orig(self, *a, **kw)
        except Exception as e:
            _safe(_tracer(self).close, e if type(e).__name__ != "ConnectionClosedOK" else None)
            raise
        _safe(_tracer(self).on_server_event, ev)
        return ev

    return recv


def _wrap_close(orig: Any) -> Any:
    if inspect.iscoroutinefunction(orig):

        @functools.wraps(orig)
        async def aclose(self: Any, *a: Any, **kw: Any) -> Any:
            try:
                return await orig(self, *a, **kw)
            finally:
                _safe(_tracer(self).close)

        return aclose

    @functools.wraps(orig)
    def close(self: Any, *a: Any, **kw: Any) -> Any:
        try:
            return orig(self, *a, **kw)
        finally:
            _safe(_tracer(self).close)

    return close


def patch_openai_realtime() -> bool:
    patched = False
    for modname in MODULES:
        try:
            mod = importlib.import_module(modname)
        except ImportError:
            continue
        for cls_name in ("RealtimeConnection", "AsyncRealtimeConnection"):
            cls = getattr(mod, cls_name, None)
            if cls is None:
                continue
            for attr, wrap in (("send", _wrap_send), ("recv", _wrap_recv), ("close", _wrap_close)):
                if hasattr(cls, attr):
                    patched |= patch_attr(cls, attr, wrap)
    return patched


__all__ = ["RealtimeTracer", "patch_openai_realtime"]
