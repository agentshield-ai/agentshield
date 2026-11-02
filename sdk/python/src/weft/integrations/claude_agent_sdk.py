"""Claude Agent SDK (``claude_agent_sdk``) integration.

* ``query(prompt=...)`` becomes an agent call ``claude_agent_sdk.query``.
* ``ClaudeSDKClient``: each ``query()`` + ``receive_response()`` exchange
  becomes an agent call ``claude_agent_sdk.turn``; the turns of one client
  share a weft thread, so the session reads as a conversation.
* Under them, every assistant message is an LLM call
  (``claude_agent_sdk.llm``, output = its text/thinking/tool-use blocks), and
  every tool use is a tool call (``claude_agent_sdk.tool.<name>``) that ends
  when its result arrives (an error result marks the call failed). Messages
  from a sub-agent (``parent_tool_use_id``) nest under the ``Task`` tool call
  that started it.
* Usage and cost come from the final ``ResultMessage`` and are recorded once,
  on the query/turn call, under the model name (input tokens include cache
  reads and writes, which are also kept as separate counters).
"""

from __future__ import annotations

import functools
import importlib
import json
import uuid
from typing import Any, Dict, Optional

from .. import _context
from .._usage import add_usage
from . import _settings
from ._common import _tracing, finish_call, patch_attr, to_dict

INTEGRATION = "claude_agent_sdk"


def _cls(obj: Any) -> str:
    return type(obj).__name__


def _short(text: Any, n: int = 60) -> str:
    s = text if isinstance(text, str) else json.dumps(text, default=str) if text is not None else ""
    s = " ".join(s.split())
    return s if len(s) <= n else s[: n - 1] + "…"


def _block(b: Any) -> Dict[str, Any]:
    d = to_dict(b)
    if not isinstance(d, dict):
        d = {"value": d}
    d = dict(d)
    d.setdefault("type", {"TextBlock": "text", "ThinkingBlock": "thinking", "ToolUseBlock": "tool_use", "ToolResultBlock": "tool_result"}.get(_cls(b), _cls(b)))
    return d


def tool_display(name: str, inp: Any) -> str:
    if isinstance(inp, dict):
        for k in ("command", "file_path", "path", "pattern", "url", "query", "description", "prompt"):
            if inp.get(k):
                return f"{name}: {_short(inp[k], 50)}"
    return name


def _usage_record(call: Any, model: Optional[str], usage: Any) -> None:
    u = to_dict(usage)
    if not isinstance(u, dict):
        return
    cache_read = int(u.get("cache_read_input_tokens") or 0)
    cache_write = int(u.get("cache_creation_input_tokens") or 0)
    prompt = int(u.get("input_tokens") or 0) + cache_read + cache_write
    add_usage(call, model or "claude", prompt, int(u.get("output_tokens") or 0),
              cache_read_input_tokens=cache_read, cache_creation_input_tokens=cache_write)


class _Stream:
    """Turns the streamed messages of one query/turn into child calls."""

    def __init__(self, client: Any, root: Any):
        self.client = client
        self.root = root
        self.model: Optional[str] = None
        self.tools: Dict[str, Any] = {}
        self.messages: list = []
        self.result: Any = None

    def _op(self, name: str, kind: Optional[str]) -> Any:
        return _settings.integration_op(INTEGRATION, name, kind)

    def _start(self, name: str, kind: Optional[str], inputs: Dict[str, Any], parent: Any, display: Optional[str]) -> Any:
        op = self._op(name, kind)
        call = self.client.create_call(op, inputs, parent=parent, display_name=display, attributes=_settings.integration_attributes(INTEGRATION))
        call._weft_op = op
        return call

    def _parent(self, msg: Any) -> Any:
        pid = getattr(msg, "parent_tool_use_id", None)
        return self.tools.get(pid, self.root) if pid else self.root

    def on_message(self, msg: Any) -> None:
        kind = _cls(msg)
        if kind == "ResultMessage":
            self.result = msg
            return
        if kind == "AssistantMessage":
            blocks = list(getattr(msg, "content", None) or [])
            model = getattr(msg, "model", None)
            self.model = self.model or model
            content = [_block(b) for b in blocks]
            self.messages.append({"role": "assistant", "content": content, "model": model})
            parent = self._parent(msg)
            texts = [getattr(b, "text", "") for b in blocks if _cls(b) == "TextBlock"]
            uses = [b for b in blocks if _cls(b) == "ToolUseBlock"]
            display = _short(texts[0]) if texts and texts[0] else (f"→ {uses[0].name}" if uses else None)
            llm = self._start("claude_agent_sdk.llm", "llm", {"model": model, "history_length": len(self.messages) - 1}, parent, display)
            out: Dict[str, Any] = {"role": "assistant", "content": content, "model": model}
            err = getattr(msg, "error", None)
            finish_call(self.client, llm, out, RuntimeError(str(err)) if err else None)
            for b in uses:
                name = getattr(b, "name", "tool")
                inp = getattr(b, "input", None)
                tkind = "agent" if name in ("Task", "Agent") else "tool"
                self.tools[getattr(b, "id", str(uuid.uuid4()))] = self._start(
                    f"claude_agent_sdk.tool.{name}", tkind, inp if isinstance(inp, dict) else {"input": inp}, parent, tool_display(name, inp)
                )
        elif kind == "UserMessage":
            content = getattr(msg, "content", None)
            blocks = content if isinstance(content, list) else []
            self.messages.append({"role": "user", "content": [_block(b) for b in blocks] if blocks else content})
            for b in blocks:
                if _cls(b) != "ToolResultBlock":
                    continue
                call = self.tools.pop(getattr(b, "tool_use_id", None), None)
                if call is None:
                    continue
                res = getattr(b, "content", None)
                exc = RuntimeError(_short(res, 500) or "tool error") if getattr(b, "is_error", False) else None
                finish_call(self.client, call, {"content": res, "is_error": bool(getattr(b, "is_error", False))}, exc)
        elif kind == "SystemMessage":
            data = getattr(msg, "data", None)
            self.messages.append({"role": "system", "subtype": getattr(msg, "subtype", None), "data": data})

    def finish(self, exc: Optional[BaseException] = None) -> None:
        for call in list(self.tools.values()):
            finish_call(self.client, call, None, exc)
        self.tools.clear()
        out: Dict[str, Any] = {"messages": self.messages}
        r = self.result
        if self.model:
            out["model"] = self.model
        if r is not None:
            for k in ("result", "session_id", "num_turns", "duration_ms", "duration_api_ms", "total_cost_usd", "subtype", "is_error"):
                v = getattr(r, k, None)
                if v is not None:
                    out[k] = v
            if getattr(r, "usage", None):
                out["usage"] = to_dict(r.usage)
                _usage_record(self.root, self.model, r.usage)
            if getattr(r, "is_error", False) and exc is None:
                exc = RuntimeError(str(getattr(r, "result", None) or "Claude Agent SDK query ended with an error"))
        finish_call(self.client, self.root, out, exc)


def _options(options: Any) -> Any:
    d = to_dict(options)
    if not isinstance(d, dict):
        return None
    keep = ("model", "system_prompt", "allowed_tools", "disallowed_tools", "permission_mode", "max_turns", "cwd", "resume", "agents")
    return {k: d.get(k) for k in keep if d.get(k) not in (None, [], {})}


def _root(client: Any, name: str, prompt: Any, options: Any, thread_id: Optional[str] = None) -> Any:
    op = _settings.integration_op(INTEGRATION, name, "agent")
    inputs: Dict[str, Any] = {"prompt": prompt if isinstance(prompt, str) else "<message stream>"}
    o = _options(options)
    if o:
        inputs["options"] = o
    display = _short(prompt) if isinstance(prompt, str) else None
    if thread_id is not None and _context.current_thread_context() is None:
        with _context.thread(thread_id):
            call = client.create_call(op, inputs, display_name=display, attributes=_settings.integration_attributes(INTEGRATION))
    else:
        call = client.create_call(op, inputs, display_name=display, attributes=_settings.integration_attributes(INTEGRATION))
    call._weft_op = op
    return call


def _wrap_process_query(orig: Any) -> Any:
    @functools.wraps(orig)
    async def process_query(self: Any, *args: Any, **kwargs: Any) -> Any:
        client = _tracing(INTEGRATION)
        if client is None:
            async for m in orig(self, *args, **kwargs):
                yield m
            return
        prompt = kwargs.get("prompt", args[0] if args else None)
        options = kwargs.get("options", args[1] if len(args) > 1 else None)
        st = _Stream(client, _root(client, "claude_agent_sdk.query", prompt, options))
        err: Optional[BaseException] = None
        try:
            async for m in orig(self, *args, **kwargs):
                try:
                    st.on_message(m)
                except Exception:  # pragma: no cover - never break the stream
                    pass
                yield m
        except BaseException as e:
            err = e
            raise
        finally:
            st.finish(err if isinstance(err, Exception) else None)

    return process_query


def _wrap_query(orig: Any) -> Any:
    @functools.wraps(orig)
    async def query(self: Any, prompt: Any, *args: Any, **kwargs: Any) -> Any:
        try:
            self._weft_prompt = prompt if isinstance(prompt, str) else None
        except Exception:  # pragma: no cover
            pass
        return await orig(self, prompt, *args, **kwargs)

    return query


def _wrap_receive_response(orig: Any) -> Any:
    @functools.wraps(orig)
    async def receive_response(self: Any, *args: Any, **kwargs: Any) -> Any:
        client = _tracing(INTEGRATION)
        if client is None:
            async for m in orig(self, *args, **kwargs):
                yield m
            return
        tid = getattr(self, "_weft_thread_id", None)
        if tid is None:
            tid = f"claude-session-{uuid.uuid4().hex[:12]}"
            try:
                self._weft_thread_id = tid
            except Exception:  # pragma: no cover
                pass
        prompt = getattr(self, "_weft_prompt", None)
        st = _Stream(client, _root(client, "claude_agent_sdk.turn", prompt, getattr(self, "options", None), thread_id=tid))
        err: Optional[BaseException] = None
        try:
            async for m in orig(self, *args, **kwargs):
                try:
                    st.on_message(m)
                except Exception:  # pragma: no cover
                    pass
                yield m
        except BaseException as e:
            err = e
            raise
        finally:
            st.finish(err if isinstance(err, Exception) else None)

    return receive_response


def patch_claude_agent_sdk() -> bool:
    patched = False
    try:
        internal = importlib.import_module("claude_agent_sdk._internal.client")
    except ImportError:
        internal = None
    ic = getattr(internal, "InternalClient", None) if internal is not None else None
    if ic is not None and hasattr(ic, "process_query"):
        patched |= patch_attr(ic, "process_query", _wrap_process_query)
    try:
        mod = importlib.import_module("claude_agent_sdk")
    except ImportError:
        return patched
    cli = getattr(mod, "ClaudeSDKClient", None)
    if cli is not None:
        if hasattr(cli, "query"):
            patched |= patch_attr(cli, "query", _wrap_query)
        if hasattr(cli, "receive_response"):
            patched |= patch_attr(cli, "receive_response", _wrap_receive_response)
    return patched
