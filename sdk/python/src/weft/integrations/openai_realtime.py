"""OpenAI Realtime API integration.

Three routes are traced, all through the same ``RealtimeTracer``:

* the ``openai`` SDK's realtime connections (``client.realtime.connect(...)``
  and ``client.beta.realtime.connect(...)``, sync and async);
* raw websocket clients:
  ``websockets.connect`` (asyncio; also what the OpenAI Agents SDK's
  ``RealtimeRunner`` uses), ``websocket.WebSocketApp`` (``websocket-client``)
  and ``aiohttp.ClientSession.ws_connect``. Only connections to a Realtime
  URL are traced (host ``api.openai.com`` or ``*.openai.azure.com`` with
  ``realtime`` in the path; add hosts with ``WEFT_REALTIME_HOSTS=host1,host2``);
  other websocket traffic is untouched;
* anything else: feed events to a ``RealtimeTracer`` yourself, or wrap one
  connection with ``wrap_websocket_async`` / ``wrap_websocket_connect`` /
  ``wrap_websocket_sync`` (any URL).

Calls:

* ``openai.realtime.session`` (agent): one per connection, from the first
  event to ``close()``; output = the final session configuration and counts.
* ``openai.realtime.response`` (llm): one per ``response.created`` ...
  ``response.done``; inputs = the conversation items added since the
  previous response (text, input audio, transcripts, function-call outputs),
  the instructions, voice and modalities; output = the response's output
  items, the streamed text/transcript and the number of audio bytes. Usage
  (``response.usage``, including cached and audio token details) is recorded
  under the session's model.
* ``openai.realtime.tool.<name>`` (tool): a function call requested by the
  model, ended when the client sends its ``function_call_output``.

Audio is recorded as WAV media: the user's input audio
(``input_audio_buffer.append``, cut per item at the server-VAD speech markers
or at each commit) on the ``input_audio`` content parts of the response's
inputs, and the model's audio (``response.output_audio.delta``) on the
``output_audio`` content parts of its output. PCM16 (24 kHz or the session's
rate) and G.711 u-law/A-law are supported. Each clip is capped at
``WEFT_REALTIME_MAX_AUDIO_BYTES`` (default 25 MiB of raw audio, about 9
minutes of 24 kHz PCM16; the server accepts files up to 64 MiB); longer clips
are cut and marked ``audio_truncated``. ``0`` turns audio capture off (only
sizes are recorded).

Manual use::

    rt = RealtimeTracer()
    rt.on_client_event(event_dict)   # before sending
    rt.on_server_event(event_dict)   # after receiving
    rt.close()
"""

from __future__ import annotations

import base64
import binascii
import functools
import importlib
import inspect
import json
import os
import struct
import threading
from typing import Any, Dict, List, Optional, Tuple
from urllib.parse import parse_qs, urlparse

from .._content import Audio
from .._usage import add_usage
from . import _settings
from ._common import _tracing, finish_call, patch_attr, register_undo, to_dict

INTEGRATION = "openai_realtime"

MODULES = ("openai.resources.realtime.realtime", "openai.resources.beta.realtime.realtime")

_TEXT_DELTAS = (
    "response.text.delta",
    "response.output_text.delta",
    "response.audio_transcript.delta",
    "response.output_audio_transcript.delta",
)
_AUDIO_DELTAS = ("response.audio.delta", "response.output_audio.delta")
_OUTPUT_AUDIO_TYPES = ("audio", "output_audio")

#: Default per-clip cap on recorded audio (raw PCM bytes).
DEFAULT_MAX_AUDIO_BYTES = 25 * 1024 * 1024
#: Hosts whose websocket connections are traced by the websocket patches.
REALTIME_HOSTS = {"api.openai.com"}
REALTIME_HOST_SUFFIXES = (".openai.azure.com",)


def _env_max_audio_bytes() -> int:
    raw = os.environ.get("WEFT_REALTIME_MAX_AUDIO_BYTES", "").strip()
    if raw:
        try:
            return max(0, int(float(raw)))
        except ValueError:
            pass
    return DEFAULT_MAX_AUDIO_BYTES


def _event(e: Any) -> Dict[str, Any]:
    if isinstance(e, (bytes, bytearray, str)):
        try:
            e = json.loads(e)
        except (ValueError, UnicodeDecodeError):
            return {}
    d = to_dict(e)
    return d if isinstance(d, dict) else {}


def _b64_len(s: Any) -> int:
    if not isinstance(s, str):
        return 0
    return (len(s) * 3) // 4 - s.count("=", -2)


def _b64(s: Any) -> bytes:
    if not isinstance(s, str) or not s:
        return b""
    try:
        return base64.b64decode(s, validate=False)
    except (binascii.Error, ValueError):
        return b""


def _strip_audio(v: Any, depth: int = 0) -> Any:
    """Drop base64 audio from items, keeping transcripts, sizes and WAV media already attached."""
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


# ------------------------------------------------------------------ audio formats
# (WAV format tag, bytes per sample, sample rate)
_PCM16 = (1, 2, 24000)
_ULAW = (7, 1, 8000)
_ALAW = (6, 1, 8000)


def audio_format(fmt: Any) -> Tuple[int, int, int]:
    """WAV parameters for a session audio format (beta ``"pcm16"``/``"g711_ulaw"``
    or GA ``{"type": "audio/pcm", "rate": 24000}``)."""
    if isinstance(fmt, dict):
        t = str(fmt.get("type") or "audio/pcm").lower()
        if t in ("audio/pcmu", "g711_ulaw"):
            return _ULAW
        if t in ("audio/pcma", "g711_alaw"):
            return _ALAW
        rate = fmt.get("rate")
        return (1, 2, int(rate) if isinstance(rate, (int, float)) and rate > 0 else 24000)
    f = str(fmt or "pcm16").lower()
    if f in ("g711_ulaw", "audio/pcmu"):
        return _ULAW
    if f in ("g711_alaw", "audio/pcma"):
        return _ALAW
    return _PCM16


def pcm_to_wav(data: bytes, fmt: Tuple[int, int, int] = _PCM16, channels: int = 1) -> bytes:
    """Wrap raw PCM16 (or G.711) mono audio in a WAV container."""
    tag, width, rate = fmt
    if width == 2 and len(data) % 2:
        data = data + b"\x00"
    block = channels * width
    if tag == 1:
        fmt_chunk = struct.pack("<4sIHHIIHH", b"fmt ", 16, tag, channels, rate, rate * block, block, width * 8)
        extra = b""
    else:  # G.711: WAVE_FORMAT_EXTENSIBLE is not needed, but non-PCM needs cbSize and a fact chunk
        fmt_chunk = struct.pack("<4sIHHIIHHH", b"fmt ", 18, tag, channels, rate, rate * block, block, width * 8, 0)
        extra = struct.pack("<4sII", b"fact", 4, len(data) // block)
    body = b"WAVE" + fmt_chunk + extra + struct.pack("<4sI", b"data", len(data)) + data
    return struct.pack("<4sI", b"RIFF", len(body)) + body


class _InputAudio:
    """The session's input audio buffer, addressed by absolute byte offsets so the
    server's ``audio_start_ms``/``audio_end_ms`` (counted from the start of the
    session's input audio) can be cut out of it."""

    def __init__(self) -> None:
        self.buf = bytearray()
        self.base = 0  # absolute offset of buf[0]
        self.commit_from = 0  # first byte not yet committed to an item

    @property
    def total(self) -> int:
        return self.base + len(self.buf)

    def append(self, data: bytes, cap: int) -> None:
        self.buf += data
        if cap > 0 and len(self.buf) > cap:
            drop = len(self.buf) - cap
            del self.buf[:drop]
            self.base += drop

    def clear(self) -> None:
        self.base = self.total
        self.buf.clear()
        self.commit_from = self.base

    def segment(self, a: int, b: int) -> bytes:
        a, b = max(a, self.base), min(b, self.total)
        return bytes(self.buf[a - self.base:b - self.base]) if b > a else b""

    def trim(self, upto: int) -> None:
        n = min(max(0, upto - self.base), len(self.buf))
        if n:
            del self.buf[:n]
            self.base += n


class RealtimeTracer:
    """Builds weft calls from a Realtime session's client and server events."""

    def __init__(self, model: Optional[str] = None, max_audio_bytes: Optional[int] = None):
        self.model = model
        self.max_audio_bytes = _env_max_audio_bytes() if max_audio_bytes is None else max(0, int(max_audio_bytes))
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
        self._input = _InputAudio()
        self._markers: Dict[str, Dict[str, Optional[int]]] = {}
        self._item_audio: Dict[str, bytes] = {}
        self._lock = threading.RLock()

    # ---------------------------------------------------------------- audio
    def _input_format(self) -> Tuple[int, int, int]:
        s = self.session
        fmt = s.get("input_audio_format")
        if fmt is None:
            fmt = ((s.get("audio") or {}).get("input") or {}).get("format") if isinstance(s.get("audio"), dict) else None
        return audio_format(fmt)

    def _output_format(self) -> Tuple[int, int, int]:
        s = self.session
        fmt = s.get("output_audio_format")
        if fmt is None:
            fmt = ((s.get("audio") or {}).get("output") or {}).get("format") if isinstance(s.get("audio"), dict) else None
        return audio_format(fmt)

    def _media(self, pcm: bytes, fmt: Tuple[int, int, int], name: str, truncated: bool = False) -> Dict[str, Any]:
        """``{"audio": weft.Audio(WAV), ...}`` for a content part (sizes only when capture is off)."""
        out: Dict[str, Any] = {"audio_bytes": len(pcm)}
        if not pcm or self.max_audio_bytes <= 0:
            return out
        if len(pcm) > self.max_audio_bytes:
            pcm = pcm[: self.max_audio_bytes - self.max_audio_bytes % fmt[1]]
            truncated = True
        out["audio"] = Audio(pcm_to_wav(pcm, fmt), mimetype="audio/wav", filename=f"{name or 'audio'}.wav")
        if truncated:
            out["audio_truncated"] = True
        return out

    def _ms_to_offset(self, ms: Any) -> int:
        _, width, rate = self._input_format()
        n = int(float(ms) * rate / 1000.0) * width
        return n

    def _committed(self, item_id: Any) -> None:
        if self.max_audio_bytes <= 0 or not item_id:
            return
        m = self._markers.pop(item_id, None) or {}
        if m.get("start") is not None and m.get("end") is not None:
            a, b = self._ms_to_offset(m["start"]), self._ms_to_offset(m["end"])
        else:
            a, b = self._input.commit_from, self._input.total
        b = min(b, self._input.total)
        pcm = self._input.segment(a, b)
        if pcm:
            self._item_audio[item_id] = pcm
            while len(self._item_audio) > 64:  # bounded: unclaimed clips of items never sent in a response
                self._item_audio.pop(next(iter(self._item_audio)))
        self._input.commit_from = max(self._input.commit_from, b)
        self._input.trim(self._input.commit_from)

    def _with_audio(self, item: Dict[str, Any], from_client: bool = False) -> Dict[str, Any]:
        """A copy of a conversation item with its input audio as WAV media."""
        content = item.get("content")
        if not isinstance(content, list) or not any(isinstance(p, dict) and p.get("type") == "input_audio" for p in content):
            return _strip_audio(item)
        out = dict(item)
        parts = []
        for i, p in enumerate(content):
            if isinstance(p, dict) and p.get("type") == "input_audio" and isinstance(p.get("audio"), Audio):
                parts.append(p)  # already converted (a client-sent item)
            elif isinstance(p, dict) and p.get("type") == "input_audio":
                q = {k: v for k, v in p.items() if k != "audio"}
                pcm = _b64(p.get("audio")) if isinstance(p.get("audio"), str) else b""
                if not pcm and not from_client:
                    pcm = self._item_audio.pop(item.get("id"), b"") if item.get("id") else b""
                if pcm:
                    q.update(self._media(pcm, self._input_format(), f"{item.get('id') or 'input'}_{i}"))
                parts.append(q)
            else:
                parts.append(_strip_audio(p))
        out["content"] = parts
        return {k: (v if k == "content" else _strip_audio(v)) for k, v in out.items()}

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
                audio = e.get("audio")
                self.pending_audio_bytes += _b64_len(audio)
                if self.max_audio_bytes > 0:
                    self._input.append(_b64(audio), 2 * self.max_audio_bytes)
                return
            if t == "input_audio_buffer.clear":
                self._input.clear()
                return
            if t == "response.create":
                self.response_overrides = dict(e.get("response") or {})
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
                self.pending.append(self._with_audio(item, from_client=True))

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
            elif t == "input_audio_buffer.speech_started":
                self._markers[e.get("item_id") or ""] = {"start": e.get("audio_start_ms"), "end": None}
            elif t == "input_audio_buffer.speech_stopped":
                self._markers.setdefault(e.get("item_id") or "", {"start": None, "end": None})["end"] = e.get("audio_end_ms")
            elif t == "input_audio_buffer.committed":
                self._committed(e.get("item_id"))
            elif t == "input_audio_buffer.cleared":
                self._input.clear()
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
                    self.pending.append(dict(item))
            elif t == "response.created":
                self._response_start(e.get("response") or {})
            elif t in _TEXT_DELTAS:
                st = self.responses.get(e.get("response_id"))
                if st is not None:
                    st["text"] += e.get("delta") or ""
            elif t in _AUDIO_DELTAS:
                st = self.responses.get(e.get("response_id"))
                if st is not None:
                    delta = e.get("delta")
                    st["audio_bytes"] += _b64_len(delta)
                    if self.max_audio_bytes > 0:
                        buf = st["audio"].setdefault(e.get("item_id"), bytearray())
                        if len(buf) <= self.max_audio_bytes:  # stop buffering past the cap (the clip is cut there)
                            buf += _b64(delta)
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
        items = [self._with_audio(it) if it.get("type") == "message" else _strip_audio(it) for it in self.pending]
        inputs: Dict[str, Any] = {"model": self.model, "input": items}
        if self.pending_audio_bytes:
            inputs["input_audio_bytes"] = self.pending_audio_bytes
        for k in ("instructions", "voice", "modalities", "output_modalities", "tools", "temperature", "max_response_output_tokens"):
            v = ov.get(k, self.session.get(k))
            if v is not None:
                inputs[k] = v
        self.pending = []
        self.pending_audio_bytes = 0
        call = self._start("openai.realtime.response", "llm", inputs, parent=self.session_call)
        self.responses[rid] = {"call": call, "text": "", "audio_bytes": 0, "audio": {}}
        self.counts["responses"] += 1

    def _response_output(self, st: Dict[str, Any], output: Any) -> Any:
        items = _strip_audio(output or [])
        audio: Dict[Any, bytearray] = st.get("audio") or {}
        if not audio or not isinstance(items, list):
            return items
        fmt = self._output_format()
        for item in items:
            if not isinstance(item, dict) or item.get("type") != "message" or not isinstance(item.get("content"), list):
                continue
            pcm = audio.pop(item.get("id"), None)
            if not pcm:
                continue
            for i, part in enumerate(item["content"]):
                if isinstance(part, dict) and part.get("type") in _OUTPUT_AUDIO_TYPES:
                    part.pop("audio_bytes", None)
                    part.update(self._media(bytes(pcm), fmt, f"{item.get('id') or 'output'}_{i}", truncated=len(pcm) > self.max_audio_bytes))
                    break
        return items

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
        out: Dict[str, Any] = {"id": resp.get("id"), "status": resp.get("status"), "output": self._response_output(st, resp.get("output"))}
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
            self._input = _InputAudio()
            self._item_audio.clear()
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


def _attached(conn: Any) -> Optional[RealtimeTracer]:
    d = getattr(conn, "__dict__", None)
    return d.get("_weft_rt") if isinstance(d, dict) else None


def _safe(fn: Any, *args: Any) -> None:
    try:
        fn(*args)
    except Exception:  # pragma: no cover - never break the connection
        pass


def _closed_ok(e: BaseException) -> bool:
    return type(e).__name__ == "ConnectionClosedOK"


# ------------------------------------------------------------------ openai package
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
                _safe(_tracer(self).close, None if _closed_ok(e) else e)
                raise
            _safe(_tracer(self).on_server_event, ev)
            return ev

        return arecv

    @functools.wraps(orig)
    def recv(self: Any, *a: Any, **kw: Any) -> Any:
        try:
            ev = orig(self, *a, **kw)
        except Exception as e:
            _safe(_tracer(self).close, None if _closed_ok(e) else e)
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


def _patch_openai_package() -> bool:
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


# ------------------------------------------------------------------ raw websocket clients
def _extra_hosts() -> set:
    return {h.strip().lower() for h in os.environ.get("WEFT_REALTIME_HOSTS", "").split(",") if h.strip()}


def is_realtime_url(url: Any) -> bool:
    """True for an OpenAI (or Azure OpenAI) Realtime websocket URL, or a host in ``WEFT_REALTIME_HOSTS``."""
    try:
        s = str(url)
    except Exception:
        return False
    if "://" not in s:
        s = "wss://" + s
    try:
        u = urlparse(s)
    except ValueError:
        return False
    host = (u.hostname or "").lower()
    if host in _extra_hosts():
        return True
    if "realtime" not in u.path.lower():
        return False
    return host in REALTIME_HOSTS or any(host.endswith(sfx) for sfx in REALTIME_HOST_SUFFIXES)


def _model_from_url(url: Any) -> Optional[str]:
    try:
        q = parse_qs(urlparse(str(url)).query)
    except Exception:
        return None
    vals = q.get("model") or q.get("deployment")
    return vals[0] if vals else None


def _attach(conn: Any, url: Any = None) -> Any:
    """Trace ``conn`` (a websockets / aiohttp / websocket-client connection)."""
    if conn is None or _attached(conn) is not None:
        return conn
    if not _settings.is_enabled(INTEGRATION):
        return conn
    _patch_connection_class(type(conn))
    try:
        conn._weft_rt = RealtimeTracer(model=_model_from_url(url) if url is not None else None)
    except Exception:  # pragma: no cover - slotted class
        pass
    return conn


def _traced_send(orig: Any) -> Any:
    """``send``/``send_str``/``send_bytes`` that feeds the attached tracer (async or sync)."""
    if inspect.iscoroutinefunction(orig):

        @functools.wraps(orig)
        async def asend(self: Any, data: Any, *a: Any, **kw: Any) -> Any:
            rt = _attached(self)
            if rt is not None:
                _safe(rt.on_client_event, data)
            return await orig(self, data, *a, **kw)

        return asend

    @functools.wraps(orig)
    def send(self: Any, data: Any, *a: Any, **kw: Any) -> Any:
        rt = _attached(self)
        if rt is not None:
            _safe(rt.on_client_event, data)
        return orig(self, data, *a, **kw)

    return send


def _traced_recv(orig: Any) -> Any:
    """websockets ``recv``: feed received messages; a closed connection ends the session."""

    @functools.wraps(orig)
    async def recv(self: Any, *a: Any, **kw: Any) -> Any:
        rt = _attached(self)
        if rt is None:
            return await orig(self, *a, **kw)
        try:
            data = await orig(self, *a, **kw)
        except Exception as e:
            if type(e).__name__.startswith("ConnectionClosed"):
                _safe(rt.close, None if _closed_ok(e) else e)
            raise
        _safe(rt.on_server_event, data)
        return data

    return recv


def _traced_receive(orig: Any) -> Any:
    """aiohttp ``receive``: feed TEXT/BINARY messages; CLOSE/CLOSED/ERROR end the session."""

    @functools.wraps(orig)
    async def receive(self: Any, *a: Any, **kw: Any) -> Any:
        msg = await orig(self, *a, **kw)
        rt = _attached(self)
        if rt is not None:
            kind = getattr(getattr(msg, "type", None), "name", str(getattr(msg, "type", "")))
            if kind in ("TEXT", "BINARY"):
                _safe(rt.on_server_event, getattr(msg, "data", None))
            elif kind in ("CLOSE", "CLOSING", "CLOSED", "ERROR"):
                _safe(rt.close, getattr(msg, "data", None) if kind == "ERROR" and isinstance(getattr(msg, "data", None), BaseException) else None)
        return msg

    return receive


def _traced_close(orig: Any) -> Any:
    if inspect.iscoroutinefunction(orig):

        @functools.wraps(orig)
        async def aclose(self: Any, *a: Any, **kw: Any) -> Any:
            try:
                return await orig(self, *a, **kw)
            finally:
                rt = _attached(self)
                if rt is not None:
                    _safe(rt.close)

        return aclose

    @functools.wraps(orig)
    def close(self: Any, *a: Any, **kw: Any) -> Any:
        try:
            return orig(self, *a, **kw)
        finally:
            rt = _attached(self)
            if rt is not None:
                _safe(rt.close)

    return close


_class_lock = threading.Lock()


def _patch_connection_class(cls: Any) -> None:
    """Patch the methods of a websocket connection class so attached tracers see its traffic.
    Connections without a tracer pass straight through."""
    with _class_lock:
        if cls.__dict__.get("_weft_rt_class"):
            return
        if hasattr(cls, "send_str") and hasattr(cls, "receive"):  # aiohttp ClientWebSocketResponse
            plan = (("send_str", _traced_send), ("send_bytes", _traced_send), ("receive", _traced_receive), ("close", _traced_close))
        elif hasattr(cls, "recv"):  # websockets (asyncio) connection
            plan = (("send", _traced_send), ("recv", _traced_recv), ("close", _traced_close))
        else:  # websocket-client WebSocketApp (callbacks are wrapped per instance)
            plan = (("send", _traced_send), ("send_text", _traced_send), ("close", _traced_close))
        for attr, wrap in plan:
            if hasattr(cls, attr):
                patch_attr(cls, attr, wrap)
        try:
            cls._weft_rt_class = True
            register_undo(lambda c=cls: c.__dict__.get("_weft_rt_class") and delattr(c, "_weft_rt_class"))
        except (AttributeError, TypeError):  # pragma: no cover - builtin/extension class
            pass


class _TracedConnect:
    """Wraps ``websockets.connect(...)``: keeps ``await``, ``async with`` and the
    reconnecting ``async for`` working, and traces the connection it yields."""

    def __init__(self, inner: Any, url: Any):
        self._inner = inner
        self._url = url

    def __await__(self) -> Any:
        return self._connect().__await__()

    async def _connect(self) -> Any:
        return _attach(await self._inner, self._url)

    async def __aenter__(self) -> Any:
        return _attach(await self._inner.__aenter__(), self._url)

    async def __aexit__(self, *exc: Any) -> Any:
        return await self._inner.__aexit__(*exc)

    async def __aiter__(self) -> Any:
        async for conn in self._inner:
            yield _attach(conn, self._url)

    def __getattr__(self, name: str) -> Any:
        return getattr(self._inner, name)


def _url_arg(args: Tuple[Any, ...], kwargs: Dict[str, Any], pos: int = 0) -> Any:
    if len(args) > pos:
        return args[pos]
    for k in ("uri", "url", "wsuri"):
        if k in kwargs:
            return kwargs[k]
    return None


def _wrap_websockets_connect(orig: Any) -> Any:
    @functools.wraps(orig)
    def connect(*args: Any, **kwargs: Any) -> Any:
        url = _url_arg(args, kwargs)
        inner = orig(*args, **kwargs)
        if not _settings.is_enabled(INTEGRATION) or not is_realtime_url(url):
            return inner
        return _TracedConnect(inner, url)

    return connect


def _wrap_aiohttp_ws_connect(orig: Any) -> Any:
    @functools.wraps(orig)
    async def _ws_connect(self: Any, *args: Any, **kwargs: Any) -> Any:
        resp = await orig(self, *args, **kwargs)
        url = _url_arg(args, kwargs)
        try:
            full = url
            base = getattr(self, "_base_url", None)
            if base is not None and url is not None and "://" not in str(url):
                full = base.join(type(base)(str(url))) if hasattr(base, "join") else f"{base}{url}"
            if _settings.is_enabled(INTEGRATION) and is_realtime_url(full):
                _attach(resp, full)
        except Exception:  # pragma: no cover - never break the connection
            pass
        return resp

    return _ws_connect


def _wrap_websocket_app_init(orig: Any) -> Any:
    @functools.wraps(orig)
    def __init__(self: Any, url: Any, *args: Any, **kwargs: Any) -> None:
        orig(self, url, *args, **kwargs)
        if _settings.is_enabled(INTEGRATION) and is_realtime_url(url):
            _instrument_websocket_app(self, url)

    return __init__


def _instrument_websocket_app(app: Any, url: Any = None) -> Any:
    """Trace one ``websocket.WebSocketApp`` (wraps its ``on_message``/``on_close`` callbacks)."""
    if _attached(app) is not None:
        return app
    _attach(app, url if url is not None else getattr(app, "url", None))
    rt = _attached(app)
    if rt is None:
        return app
    user_msg = getattr(app, "on_message", None)
    user_close = getattr(app, "on_close", None)

    def on_message(ws: Any, message: Any, *rest: Any) -> Any:
        _safe(rt.on_server_event, message)
        if user_msg is not None:
            return user_msg(ws, message, *rest)
        return None

    def on_close(ws: Any, *rest: Any) -> Any:
        try:
            if user_close is not None:
                return user_close(ws, *rest)
            return None
        finally:
            _safe(rt.close)

    app.on_message = on_message
    app.on_close = on_close
    return app


# Manual wrappers, for when autopatching is off.
def wrap_websocket_async(connection: Any) -> Any:
    """Trace one connected async websocket (``websockets`` or ``aiohttp``); returns it."""
    return _attach(connection, getattr(connection, "request", None) and getattr(getattr(connection, "request", None), "path", None))


def wrap_websocket_connect(connect_func: Any) -> Any:
    """Wrap a connect function (e.g. ``websockets.connect``) so the connections it opens are traced (any URL)."""

    @functools.wraps(connect_func)
    def connect(*args: Any, **kwargs: Any) -> Any:
        return _TracedConnect(connect_func(*args, **kwargs), _url_arg(args, kwargs))

    return connect


def wrap_websocket_sync(websocket_app_class: Any) -> Any:
    """A ``websocket.WebSocketApp`` factory whose apps are traced (any URL)."""

    def factory(*args: Any, **kwargs: Any) -> Any:
        app = websocket_app_class(*args, **kwargs)
        return _instrument_websocket_app(app, _url_arg(args, kwargs))

    return factory


def _patch_websockets() -> bool:
    patched = False
    try:
        ws = importlib.import_module("websockets")
        if hasattr(ws, "connect"):
            patched |= patch_attr(ws, "connect", _wrap_websockets_connect)
    except ImportError:
        pass
    try:
        wsc = importlib.import_module("websocket")
        app = getattr(wsc, "WebSocketApp", None)
        if app is not None:
            patched |= patch_attr(app, "__init__", _wrap_websocket_app_init)
    except ImportError:
        pass
    try:
        aio = importlib.import_module("aiohttp")
        sess = getattr(aio, "ClientSession", None)
        if sess is not None and hasattr(sess, "_ws_connect"):
            patched |= patch_attr(sess, "_ws_connect", _wrap_aiohttp_ws_connect)
    except ImportError:
        pass
    return patched


def patch_openai_realtime() -> bool:
    """Trace the ``openai`` SDK's realtime connections and Realtime websockets opened with
    ``websockets``, ``websocket-client`` or ``aiohttp``."""
    a = _patch_openai_package()
    b = _patch_websockets()
    return a or b


__all__ = [
    "RealtimeTracer",
    "patch_openai_realtime",
    "pcm_to_wav",
    "is_realtime_url",
    "wrap_websocket_async",
    "wrap_websocket_connect",
    "wrap_websocket_sync",
]
