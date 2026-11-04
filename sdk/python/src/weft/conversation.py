"""Agent conversation tracing: conversations, turns, LLM calls, tools and sub-agents.

``weft.start_conversation`` and friends. Each span is recorded as a Weft call that
the server's agents index understands:

- a **turn** is an ``invoke_agent`` call (``attributes.weft.span_kind = "agent"``,
  ``attributes.gen_ai.agent.name``), the root of its own trace by default, with
  ``thread_id`` = the conversation id and ``turn_id`` = its own id;
- an **LLM call** is a ``chat`` call with ``inputs.messages`` and an OpenAI-style
  ``output`` plus ``summary.usage``;
- a **tool** is an ``execute_tool`` call with the parsed arguments as inputs and
  the result as output;
- a **sub-agent** is a nested ``invoke_agent`` call.

So conversations appear in the Agents views (``/agents/*``), in Threads and in
Traces. Spans are sent when they end (like OpenTelemetry spans), so their
messages, usage and attributes can be filled in until then. ``@weft.op``
calls made inside an entered span become its children.

>>> import weft
>>> weft.init("my-team/support-bot")
>>> with weft.start_conversation(agent_name="support") as conv:
...     with conv.start_turn(user_message="Where is my order?") as turn:
...         with turn.start_llm(model="gpt-4o-mini", provider_name="openai") as llm:
...             llm.input_messages = [weft.Message.user("Where is my order?")]
...             llm.output("Let me check.")
...         with turn.start_tool(name="lookup_order", arguments={"id": 42}) as tool:
...             tool.result = {"status": "shipped"}
...         turn.output_messages = [weft.Message.assistant("It has shipped.")]

Batch logging of finished spans (no context managers) uses ``log_turn`` and
``log_conversation``.
"""

from __future__ import annotations

import base64
import contextlib
import datetime
import json
import logging
import platform
import types
import uuid
import warnings
from contextvars import ContextVar, Token
from dataclasses import dataclass, field
from typing import TYPE_CHECKING, Annotated, Any, Dict, Iterator, List, Literal, Optional, Tuple, Union

from pydantic import BaseModel, BeforeValidator, ConfigDict, Field, PrivateAttr

from . import _context
from ._call import Span as Call
from ._ids import uuid7
from ._version import __version__

if TYPE_CHECKING:  # pragma: no cover
    from ._client import WeftClient

logger = logging.getLogger("weft")

__all__ = [
    "Handoff",
    "LLM",
    "Team",
    "BlobPart",
    "Conversation",
    "FilePart",
    "LogResult",
    "MediaAttachment",
    "Message",
    "MessagePart",
    "Reasoning",
    "ReasoningPart",
    "SubAgent",
    "TextPart",
    "Tool",
    "ToolCallPart",
    "ToolCallResponsePart",
    "Turn",
    "UriPart",
    "Usage",
    "agent_name_override",
    "end_conversation",
    "end_llm",
    "end_turn",
    "get_current_conversation",
    "get_current_llm",
    "get_current_team",
    "get_current_turn",
    "handoff",
    "log_conversation",
    "log_turn",
    "reasoning",
    "start_conversation",
    "start_handoff",
    "start_llm",
    "start_subagent",
    "start_tool",
    "start_turn",
    "team",
]


# ---------------------------------------------------------------------------
# Data types
# ---------------------------------------------------------------------------


def _to_json_string(value: Any) -> str:
    """Coerce a value for a field the wire format types as a string: text is kept,
    None becomes "", anything else is serialised as JSON."""
    if isinstance(value, str):
        return value
    return "" if value is None else json.dumps(value, ensure_ascii=False, default=str)


JSONString = Annotated[str, BeforeValidator(_to_json_string)]


def _parse_data_url(url: str) -> Tuple[str, str]:
    """``(mime type, encoded payload)`` of a ``data:`` URL; ``("", "")`` for any other URL.
    The payload is returned as written (base64 is not decoded)."""
    scheme, sep, rest = url.partition(":")
    if scheme != "data" or not sep:
        return ("", "")
    meta, _, payload = rest.partition(",")
    return (meta.split(";", 1)[0], payload)


class TextPart(BaseModel):
    type: Literal["text"] = "text"
    content: str = ""


class ReasoningPart(BaseModel):
    type: Literal["reasoning"] = "reasoning"
    content: str = ""


class ToolCallPart(BaseModel):
    """A tool invocation the model asked for. ``arguments`` accepts a dict (stored as JSON text)."""

    model_config = ConfigDict(validate_assignment=True)
    type: Literal["tool_call"] = "tool_call"
    id: str = ""
    name: str = ""
    arguments: JSONString = ""


class ToolCallResponsePart(BaseModel):
    """What a tool returned, matched to its call by ``id``. ``response`` accepts any JSON value."""

    model_config = ConfigDict(validate_assignment=True)
    type: Literal["tool_call_response"] = "tool_call_response"
    id: str = ""
    response: JSONString = ""


class BlobPart(BaseModel):
    """Media carried in the message itself, base64 in ``content``; stored as a file when the span is sent."""

    type: Literal["blob"] = "blob"
    mime_type: str = ""
    modality: str = ""
    content: str = ""


class UriPart(BaseModel):
    type: Literal["uri"] = "uri"
    mime_type: str = ""
    modality: str = ""
    uri: str = ""


class FilePart(BaseModel):
    type: Literal["file"] = "file"
    mime_type: str = ""
    modality: str = ""
    file_id: str = ""


MessagePart = Annotated[
    Union[TextPart, ReasoningPart, ToolCallPart, ToolCallResponsePart, BlobPart, UriPart, FilePart],
    Field(discriminator="type"),
]


class Message(BaseModel):
    """One chat message: flat ``content`` text, or a ``parts`` list (tool calls, reasoning, media)."""

    role: Literal["user", "assistant", "system", "tool"]
    content: str = ""
    tool_call_id: str = ""
    tool_name: str = ""
    parts: List[MessagePart] = Field(default_factory=list)

    @classmethod
    def user(cls, text: str) -> "Message":
        return cls(role="user", content=text)

    @classmethod
    def system(cls, text: str) -> "Message":
        return cls(role="system", content=text)

    @classmethod
    def assistant(cls, text: str = "", *, tool_calls: Optional[List[ToolCallPart]] = None) -> "Message":
        """An assistant reply, optionally requesting tools (``ToolCallPart``)."""
        if not tool_calls:
            return cls(role="assistant", content=text)
        parts: List[Any] = []
        if text:
            parts.append(TextPart(content=text))
        parts.extend(tool_calls)
        return cls(role="assistant", parts=parts)

    @classmethod
    def tool_result(cls, call_id: str, output: Any) -> "Message":
        return cls(role="tool", parts=[ToolCallResponsePart(id=call_id, response=output)])


class Usage(BaseModel):
    """Token counts reported for one LLM call (``total_tokens`` None means the provider gave none)."""

    input_tokens: int = 0
    output_tokens: int = 0
    total_tokens: Optional[int] = None
    reasoning_tokens: int = 0
    cache_creation_input_tokens: int = 0
    cache_read_input_tokens: int = 0


class Reasoning(BaseModel):
    content: str = ""


class MediaAttachment(BaseModel):
    """Media attached to an LLM call. ``ref`` holds the uploaded file value (a JSON string)."""

    ref: str
    modality: str = ""
    mime_type: str = ""


class LogResult(BaseModel):
    """Result of ``log_turn`` / ``log_conversation``: ids of what was recorded."""

    conversation_id: str = ""
    trace_ids: List[str] = Field(default_factory=list)
    root_span_ids: List[str] = Field(default_factory=list)
    span_count: int = 0


# ---------------------------------------------------------------------------
# helpers
# ---------------------------------------------------------------------------


def _now() -> datetime.datetime:
    return datetime.datetime.now(datetime.timezone.utc)


def _iso(dt: datetime.datetime) -> str:
    if dt.tzinfo is None:
        dt = dt.replace(tzinfo=datetime.timezone.utc)
    return dt.astimezone(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.%fZ")


def _span_name(operation: str, target: str) -> str:
    target = (target or "").strip()
    return f"{operation} {target}" if target else operation


def _parse_json(s: str) -> Any:
    if not s:
        return None
    try:
        return json.loads(s)
    except (TypeError, ValueError):
        return s


def _set_path(d: Dict[str, Any], dotted: str, value: Any) -> None:
    parts = dotted.split(".")
    cur = d
    for p in parts[:-1]:
        nxt = cur.get(p)
        if not isinstance(nxt, dict):
            nxt = {}
            cur[p] = nxt
        cur = nxt
    cur[parts[-1]] = value


def _nest(flat: Dict[str, Any]) -> Dict[str, Any]:
    """``{"gen_ai.agent.name": x}`` → ``{"gen_ai": {"agent": {"name": x}}}``."""
    out: Dict[str, Any] = {}
    for k in sorted(flat, key=lambda k: k.count(".")):
        _set_path(out, k, flat[k])
    return out


def _deep_merge(base: Dict[str, Any], extra: Dict[str, Any]) -> Dict[str, Any]:
    out = dict(base)
    for k, v in extra.items():
        if isinstance(v, dict) and isinstance(out.get(k), dict):
            out[k] = _deep_merge(out[k], v)
        else:
            out[k] = v
    return out


def _format_exception(exc: BaseException) -> str:
    from ._client import format_exception

    return format_exception(exc)


def _error_type(exc: BaseException) -> str:
    cls = type(exc)
    name = cls.__qualname__
    return name if cls.__module__ == "builtins" else f"{cls.__module__}.{name}"


def _tracing_client() -> Optional["WeftClient"]:
    from ._client import tracing_client

    if _context.tracing_suppressed():
        return None
    return tracing_client()


# ---- message conversion (GenAI parts → the chat shape Weft's views read) ----


def _media_value(mime_type: str, modality: str, data: bytes, client: Optional["WeftClient"]) -> Dict[str, Any]:
    """Upload bytes and return a media value the UI and agents views render."""
    from ._canonical import file_digest

    mt = mime_type or "application/octet-stream"
    modality = modality or (mt.split("/", 1)[0] if mt.split("/", 1)[0] in ("image", "audio", "video") else "")
    digest = client._upload_file(data, mt) if client is not None else file_digest(data)
    if modality == "image":
        return {"type": "image", "_type": "Image", "file_digest": digest, "mime_type": mt}
    if modality == "audio":
        return {"type": "audio", "_type": "Audio", "file_digest": digest, "mime_type": mt}
    if modality == "video":
        return {"type": "file", "_type": "Video", "file_digest": digest, "mime_type": mt, "modality": "video"}
    return {"type": "file", "_type": "Content", "file_digest": digest, "mimetype": mt, "mime_type": mt}


def _part_value(p: Any, client: Optional["WeftClient"]) -> Dict[str, Any]:
    if isinstance(p, BlobPart):
        try:
            data = base64.b64decode(p.content, validate=False)
        except (ValueError, TypeError):
            data = p.content.encode("utf-8")
        return _media_value(p.mime_type, p.modality, data, client)
    if isinstance(p, UriPart):
        if p.uri.startswith("data:"):
            mt, payload = _parse_data_url(p.uri)
            return _part_value(BlobPart(mime_type=p.mime_type or mt, modality=p.modality, content=payload), client)
        out: Dict[str, Any] = {"type": "uri", "uri": p.uri}
        if p.mime_type:
            out["mime_type"] = p.mime_type
        if p.modality:
            out["modality"] = p.modality
        return out
    if isinstance(p, FilePart):
        return {k: v for k, v in {"type": "file", "file_id": p.file_id, "mime_type": p.mime_type, "modality": p.modality}.items() if v}
    if isinstance(p, ReasoningPart):
        return {"type": "thinking", "thinking": p.content}
    if isinstance(p, TextPart):
        return {"type": "text", "text": p.content}
    return p.model_dump() if isinstance(p, BaseModel) else dict(p)


def _chat_message(msg: Message, client: Optional["WeftClient"], extra: Optional[List[Dict[str, Any]]] = None) -> Dict[str, Any]:
    """Convert a Message to an OpenAI-style chat message (tool_calls, tool_call_id, content parts)."""
    out: Dict[str, Any] = {"role": msg.role}
    if msg.tool_name:
        out["name"] = msg.tool_name
    if not msg.parts:
        if msg.role == "tool" and msg.tool_call_id:
            out["tool_call_id"] = msg.tool_call_id
        if extra:
            out["content"] = ([{"type": "text", "text": msg.content}] if msg.content else []) + list(extra)
        else:
            out["content"] = msg.content
        return out
    texts: List[str] = []
    content: List[Dict[str, Any]] = []
    tool_calls: List[Dict[str, Any]] = []
    responses = 0
    for p in msg.parts:
        if isinstance(p, TextPart):
            texts.append(p.content)
            content.append({"type": "text", "text": p.content})
        elif isinstance(p, ToolCallPart):
            tool_calls.append({"id": p.id, "type": "function", "function": {"name": p.name, "arguments": p.arguments}})
        elif isinstance(p, ToolCallResponsePart):
            responses += 1
            out["tool_call_id"] = p.id
            texts.append(p.response)
            content.append({"type": "text", "text": p.response})
        else:
            content.append(_part_value(p, client))
    if extra:
        content.extend(extra)
    if responses and responses == len(msg.parts):
        out["role"] = "tool"
    out["content"] = content if len(content) > len(texts) else "\n".join(texts)
    if tool_calls:
        out["tool_calls"] = tool_calls
    return out


def _system_parts_json(instructions: List[str]) -> str:
    return json.dumps([{"type": "text", "content": s} for s in instructions], ensure_ascii=False)


def _messages_with_system(
    system_instructions: List[str], messages: List[Message], client: Optional["WeftClient"], media: Optional[List[Dict[str, Any]]] = None
) -> List[Dict[str, Any]]:
    out: List[Dict[str, Any]] = [{"role": "system", "content": s} for s in system_instructions if s]
    last_user = max((i for i, m in enumerate(messages) if m.role == "user"), default=-1)
    for i, m in enumerate(messages):
        out.append(_chat_message(m, client, media if (i == last_user and media) else None))
    if media and last_user < 0:
        out.append({"role": "user", "content": list(media)})
    return out


# ---------------------------------------------------------------------------
# conversation info, eval links and agent-name override
# ---------------------------------------------------------------------------


@dataclass
class _ConvInfo:
    conversation_id: str = ""
    conversation_name: str = ""
    include_content: bool = True
    attributes: Dict[str, Any] = field(default_factory=dict)
    team: Optional[_context.TeamInfo] = None


# ---------------------------------------------------------------------------
# teams
# ---------------------------------------------------------------------------


class Team:
    """An agent team: the spans recorded while it is active carry ``weft.team.id``
    and ``weft.team.name``, so the server groups their sessions under it.

    >>> with weft.team("trip-planners", "Trip planners"):
    ...     run_orchestrator()

    Pass it (or its id) as ``team=`` to ``start_conversation``/``log_turn``/
    ``log_conversation`` to set it for one conversation.
    """

    def __init__(self, id: str, name: str = "") -> None:
        if not isinstance(id, str) or not id.strip():
            raise ValueError("a team needs a non-blank id")
        self.id = id.strip()
        self.name = (name or "").strip()
        self._tokens: List[Token] = []

    @property
    def info(self) -> _context.TeamInfo:
        return _context.TeamInfo(self.id, self.name)

    def __enter__(self) -> "Team":
        self._tokens.append(_context.set_team(self.info))
        return self

    def __exit__(self, *exc: Any) -> Literal[False]:
        if self._tokens:
            _context.reset_team(self._tokens.pop())
        return False

    def __repr__(self) -> str:
        return f"Team(id={self.id!r}, name={self.name!r})"


def team(id: str, name: str = "") -> Team:
    """Declare the agent team for the calls made in a ``with`` block (``weft.team.id`` / ``.name``)."""
    return Team(id, name)


def get_current_team() -> Optional[_context.TeamInfo]:
    """The team of the innermost ``weft.team(...)`` block or team conversation, if any."""
    return _context.current_team()


def _team_info(value: Any) -> Optional[_context.TeamInfo]:
    """A team from a Team, a TeamInfo, an id string, a ``(id, name)`` pair or ``{"id", "name"}``."""
    if value is None or value == "":
        return None
    if isinstance(value, Team):
        return value.info
    if isinstance(value, _context.TeamInfo):
        return value
    if isinstance(value, str):
        return _context.TeamInfo(value.strip()) if value.strip() else None
    if isinstance(value, (tuple, list)) and value:
        return _context.TeamInfo(str(value[0]), str(value[1]) if len(value) > 1 and value[1] else "")
    if isinstance(value, dict) and value.get("id"):
        return _context.TeamInfo(str(value["id"]), str(value.get("name") or ""))
    raise TypeError(f"team must be a weft.Team, an id string, (id, name) or {{'id', 'name'}}, not {type(value).__name__}")


_current_agent_name: ContextVar[Optional[str]] = ContextVar("weft_agent_name_override", default=None)


@contextlib.contextmanager
def agent_name_override(agent_name: str) -> Iterator[None]:
    """Name the agent of turns and sub-agents started in this block that do not name one."""
    if not isinstance(agent_name, str) or not agent_name.strip():
        raise ValueError("agent_name_override() needs a non-blank agent name")
    token = _current_agent_name.set(agent_name)
    try:
        yield
    finally:
        _current_agent_name.reset(token)


def _eval_link_attributes() -> Dict[str, Any]:
    """``weft.eval.*`` attributes linking spans made during an EvaluationLogger prediction."""
    ctx = _context.current_eval_span_context()
    if not ctx:
        return {}
    return {f"weft.eval.{k}": v for k, v in ctx.items() if v is not None}


# ---------------------------------------------------------------------------
# span base
# ---------------------------------------------------------------------------


class _SpanBase(BaseModel):
    """Shared behaviour: lazily-started Weft call, nesting, attributes, errors."""

    model_config = ConfigDict(protected_namespaces=(), arbitrary_types_allowed=True)

    _op_name: str = PrivateAttr(default="span")
    _kind: str = PrivateAttr(default="chain")
    _call: Optional[Call] = PrivateAttr(default=None)
    _client: Any = PrivateAttr(default=None)
    _conv: Optional[_ConvInfo] = PrivateAttr(default=None)
    _explicit_parent: Optional["_SpanBase"] = PrivateAttr(default=None)
    _fallback_parent: Optional["_SpanBase"] = PrivateAttr(default=None)
    _stack_token: Optional[Token] = PrivateAttr(default=None)
    _extra_attrs: Dict[str, Any] = PrivateAttr(default_factory=dict)
    _ctx_attrs: Dict[str, Any] = PrivateAttr(default_factory=dict)
    _events: List[Dict[str, Any]] = PrivateAttr(default_factory=list)
    _exception: Optional[str] = PrivateAttr(default=None)
    _error_type: Optional[str] = PrivateAttr(default=None)
    _started: bool = PrivateAttr(default=False)
    _ended: bool = PrivateAttr(default=False)
    _new_trace: bool = PrivateAttr(default=False)
    _team: Optional[_context.TeamInfo] = PrivateAttr(default=None)
    _reasoning: List[Dict[str, Any]] = PrivateAttr(default_factory=list)

    # ---- identity ------------------------------------------------------------
    @property
    def call(self) -> Optional[Call]:
        """The Weft call recording this span (None until started, or when tracing is off)."""
        return self._call

    @property
    def call_id(self) -> Optional[str]:
        return self._call.id if self._call is not None else None

    @property
    def trace_id(self) -> Optional[str]:
        return self._call.trace_id if self._call is not None else None

    def _span_display_name(self) -> str:  # pragma: no cover - overridden
        return self._op_name

    def _conv_info(self) -> _ConvInfo:
        if self._conv is not None:
            return self._conv
        conv = get_current_conversation()
        if conv is not None:
            return conv._info()
        return _ConvInfo()

    def _pin_parent(self, child: "_SpanBase") -> None:
        child._explicit_parent = self
        if child._conv is None and self._conv is not None:
            child._conv = self._conv

    # ---- lifecycle -----------------------------------------------------------
    def _start(self, *, set_current: bool = True) -> None:
        if self._started:
            return
        self._started = True
        if getattr(self, "started_at", None) is None:
            self.started_at = _now()  # type: ignore[attr-defined]
        if self._conv is None:
            self._conv = self._conv_info()
        client = self._client if self._client is not None else _tracing_client()
        if client is None:
            return
        self._client = client
        self._ctx_attrs = dict(_context.current_attributes())
        self._team = (self._conv.team if self._conv is not None else None) or _context.current_team()
        eval_attrs = _eval_link_attributes()
        if eval_attrs:
            self._ctx_attrs = {**self._ctx_attrs, **{"__eval__": eval_attrs}}
        parent_call: Optional[Call] = None
        if self._explicit_parent is not None:
            self._explicit_parent._start(set_current=False)
            parent_call = self._explicit_parent._call
        elif not self._new_trace:
            parent_call = _context.get_current_call()
            if parent_call is None and self._fallback_parent is not None and not self._fallback_parent._ended:
                self._fallback_parent._start(set_current=False)
                parent_call = self._fallback_parent._call
        call_id = uuid7()
        conv = self._conv
        thread_id = conv.conversation_id or None if conv else None
        if thread_id is None and parent_call is not None:
            thread_id = parent_call.session_id
        if isinstance(self, Turn):
            turn_id: Optional[str] = call_id
        else:
            turn_id = parent_call.turn_id if parent_call is not None else None
        from ._client import _synthetic_op

        op = _synthetic_op(self._op_name, kind=self._kind)
        self._call = Call(
            id=call_id,
            project_id=client.project_id,
            name=client._op_ref(op).uri(),
            trace_id=parent_call.trace_id if parent_call is not None else uuid7(),
            parent_id=parent_call.id if parent_call is not None else None,
            display_name=self._span_display_name(),
            session_id=thread_id,
            turn_id=turn_id,
            started_at=_iso(self.started_at),  # type: ignore[attr-defined]
        )
        self._call._client = client
        self._call._conversation_span = True
        self._call._weft_span = self
        if parent_call is not None:
            parent_call._child_count += 1
        if set_current:
            self._stack_token = _context.push_call(self._call)

    def _pop(self) -> None:
        if self._stack_token is not None:
            _context.pop_call(self._stack_token)
            self._stack_token = None

    def _finish(self) -> bool:
        """Common end logic; returns False when already ended."""
        if self._ended:
            return False
        self._ended = True
        if getattr(self, "ended_at", None) is None:
            self.ended_at = _now()  # type: ignore[attr-defined]
        if not self._started:
            self._start(set_current=False)
        self._pop()
        self._emit()
        return True

    def end(self) -> None:
        """End the span and send it."""
        self._finish()

    def start(self, *, set_current: bool = True) -> "_SpanBase":
        """Start the span without a ``with`` block. ``set_current=False`` keeps it off the call stack
        (use it for spans that overlap; their own ``start_*`` children still nest under them)."""
        self._start(set_current=set_current)
        return self

    def __enter__(self) -> Any:
        self._start()
        return self

    def __exit__(self, exc_type: Any, exc_val: Optional[BaseException], exc_tb: Optional[types.TracebackType]) -> Literal[False]:
        if exc_val is not None:
            self.record_error(exc_val)
        self.end()
        return False

    # ---- user API -----------------------------------------------------------
    def record_error(self, error: BaseException) -> Any:
        """Mark the span failed (it is still ended by ``end()`` / the ``with`` block)."""
        if self._ended:
            logger.warning("weft: record_error(%s) ignored: span already ended", type(error).__name__)
            return self
        self._exception = _format_exception(error)
        self._error_type = _error_type(error)
        return self

    def set_attributes(self, attributes: Dict[str, Any]) -> Any:
        """Add attributes to this span (any time before it ends)."""
        if self._ended:
            logger.warning("weft: set_attributes(%s) ignored: span already ended", list(attributes))
            return self
        self._extra_attrs.update(attributes)
        return self

    def add_event(self, name: str, attributes: Optional[Dict[str, Any]] = None, timestamp: Optional[datetime.datetime] = None) -> Any:
        """Deprecated: record a point-in-time event; prefer ``set_attributes``."""
        warnings.warn("`add_event` is kept for compatibility only; put the data in `set_attributes`.", DeprecationWarning, stacklevel=2)
        if self._ended:
            return self
        self._events.append({"name": name, "attributes": dict(attributes or {}), "timestamp": _iso(timestamp or _now())})
        return self

    # ---- emission ---------------------------------------------------------------
    def _payload(self, conv: _ConvInfo, client: Optional["WeftClient"]) -> Tuple[Dict[str, Any], Any, Dict[str, Any], Dict[str, Any]]:
        raise NotImplementedError  # pragma: no cover

    def _emit(self) -> None:
        client = self._client
        call = self._call
        if client is None or call is None:
            return
        conv = self._conv or _ConvInfo()
        try:
            inputs, output, gen_attrs, summary = self._payload(conv, client)
        except Exception as e:  # pragma: no cover - defensive
            logger.warning("weft: could not record %s span: %s", self._op_name, e)
            return
        from ._serialize import to_json

        flat: Dict[str, Any] = dict(gen_attrs)
        if conv.conversation_id:
            flat["gen_ai.conversation.id"] = conv.conversation_id
        if self._team is not None:
            flat["weft.team.id"] = self._team.id
            if self._team.name:
                flat["weft.team.name"] = self._team.name
        if self._error_type:
            flat["error.type"] = self._error_type
        flat.update(self._ctx_attrs.get("__eval__") or {})
        attrs: Dict[str, Any] = dict(client.global_attributes)
        attrs = _deep_merge(attrs, {k: v for k, v in self._ctx_attrs.items() if k != "__eval__"})
        attrs = _deep_merge(attrs, dict(conv.attributes))
        attrs = _deep_merge(attrs, _nest(flat))
        attrs = _deep_merge(attrs, dict(self._extra_attrs))
        weft_attrs = {"client_version": __version__, "source": "python-sdk", "python_version": platform.python_version(), "span_kind": self._kind}
        if isinstance(attrs.get("weft"), dict):
            weft_attrs.update(attrs["weft"])
        if self._events:
            weft_attrs["events"] = list(self._events)
        if self._reasoning and client.capture_reasoning:
            weft_attrs["reasoning"] = list(weft_attrs.get("reasoning") or []) + client._reasoning_attr(self._reasoning)
        attrs["weft"] = weft_attrs
        attrs = to_json(attrs, client)
        ser_inputs = client._scrub_reasoning(to_json(inputs, client))
        ser_output = client._scrub_reasoning(to_json(output, client))
        if client._redact.active:
            ser_inputs, ser_output = client._redact(ser_inputs), client._redact(ser_output)
        call.attributes = attrs
        call.inputs = ser_inputs
        call.output = ser_output
        call.ended_at = _iso(self.ended_at)  # type: ignore[attr-defined]
        call.error = self._exception
        call.summary = summary
        call._ended = True
        start: Dict[str, Any] = {
            "project_id": call.project_id,
            "id": call.id,
            "name": call.name,
            "trace_id": call.trace_id,
            "parent_id": call.parent_id,
            "started_at": call.started_at,
            "attributes": attrs,
            "inputs": ser_inputs,
        }
        for k in ("display_name", "session_id", "turn_id"):
            if getattr(call, k) is not None:
                start[k] = getattr(call, k)
        end: Dict[str, Any] = {"project_id": call.project_id, "id": call.id, "ended_at": call.ended_at, "output": ser_output}
        if self._exception:
            end["error"] = self._exception
        if summary:
            end["summary"] = to_json(summary, client)
        client._sender.enqueue("span_start", start)
        client._sender.enqueue("span_end", end)


# ---------------------------------------------------------------------------
# Tool
# ---------------------------------------------------------------------------


class Tool(_SpanBase):
    """One tool execution (an ``execute_tool`` call). ``arguments``/``result`` accept any JSON value."""

    model_config = ConfigDict(validate_assignment=True)

    name: str = ""
    arguments: JSONString = ""
    result: JSONString = ""
    tool_call_id: str = ""
    tool_type: str = ""
    tool_description: str = ""
    tool_definitions: str = ""
    duration_ms: int = 0
    started_at: Optional[datetime.datetime] = None
    ended_at: Optional[datetime.datetime] = None

    def model_post_init(self, context: Any, /) -> None:
        self._op_name = "execute_tool"
        self._kind = "tool"

    def _span_display_name(self) -> str:
        return _span_name("execute_tool", self.name)

    def end(self) -> None:
        if self._ended:
            return
        if self.ended_at is None:
            self.ended_at = _now()
        if self.started_at is not None:
            self.duration_ms = int((self.ended_at - self.started_at).total_seconds() * 1000)
        self._finish()

    def _payload(self, conv: _ConvInfo, client: Optional["WeftClient"]) -> Tuple[Dict[str, Any], Any, Dict[str, Any], Dict[str, Any]]:
        attrs: Dict[str, Any] = {"gen_ai.operation.name": "execute_tool", "gen_ai.tool.name": self.name}
        for k, v in (
            ("gen_ai.tool.call.id", self.tool_call_id),
            ("gen_ai.tool.type", self.tool_type),
            ("gen_ai.tool.description", self.tool_description),
            ("gen_ai.tool.definitions", self.tool_definitions),
        ):
            if v:
                attrs[k] = v
        if not conv.include_content:
            return {}, None, attrs, {}
        args = _parse_json(self.arguments)
        inputs = args if isinstance(args, dict) else ({} if args is None else {"arguments": args})
        return inputs, _parse_json(self.result), attrs, {}


# ---------------------------------------------------------------------------
# LLM
# ---------------------------------------------------------------------------


class LLM(_SpanBase):
    """One LLM API call (a ``chat`` call)."""

    model: str = ""
    provider_name: str = ""
    response_id: str = ""
    response_model: str = ""
    output_type: str = ""
    system_instructions: List[str] = Field(default_factory=list)
    usage: Usage = Field(default_factory=Usage)
    reasoning: Reasoning = Field(default_factory=Reasoning)
    finish_reasons: List[str] = Field(default_factory=list)
    input_messages: List[Message] = Field(default_factory=list)
    output_messages: List[Message] = Field(default_factory=list)
    media_attachments: List[MediaAttachment] = Field(default_factory=list)
    request_temperature: Optional[float] = None
    request_max_tokens: Optional[int] = None
    request_top_p: Optional[float] = None
    request_frequency_penalty: Optional[float] = None
    request_presence_penalty: Optional[float] = None
    request_seed: Optional[int] = None
    request_stop_sequences: List[str] = Field(default_factory=list)
    request_choice_count: Optional[int] = None
    started_at: Optional[datetime.datetime] = None
    ended_at: Optional[datetime.datetime] = None

    _token: Optional[Token] = PrivateAttr(default=None)

    def model_post_init(self, context: Any, /) -> None:
        self._op_name = "chat"
        self._kind = "llm"
        if self.started_at is None:
            self.started_at = _now()

    def _span_display_name(self) -> str:
        return _span_name("chat", self.model)

    def output(self, content: str) -> "LLM":
        """Add ``content`` as an assistant reply at the end of ``output_messages``."""
        self.output_messages.append(Message(role="assistant", content=content))
        return self

    def think(self, content: str) -> "LLM":
        """Record the model's reasoning text for this call."""
        self.reasoning = Reasoning(content=content)
        return self

    def attach_media(self, *, content: Union[bytes, str] = "", uri: str = "", file_id: str = "", mime_type: str = "", modality: str = "") -> "LLM":
        """Attach media (bytes, base64 text, a URI or a provider file id) to the last user input message.

        Bytes, base64 and ``data:`` URIs are uploaded as files; other URIs and file ids are kept as references.
        """
        if sum(bool(s) for s in (content, uri, file_id)) != 1:
            raise ValueError("attach_media takes one source: content=, uri= or file_id=")
        if not modality and mime_type:
            prefix = mime_type.split("/", 1)[0]
            if prefix in ("image", "audio", "video"):
                modality = prefix
        client = self._client if self._client is not None else _tracing_client()
        if content:
            data = content if isinstance(content, bytes) else base64.b64decode(content)
            value: Dict[str, Any] = _media_value(mime_type, modality, data, client)
        elif uri:
            value = _part_value(UriPart(uri=uri, mime_type=mime_type, modality=modality), client)
        else:
            value = _part_value(FilePart(file_id=file_id, mime_type=mime_type, modality=modality), client)
        self.media_attachments.append(MediaAttachment(ref=json.dumps(value), modality=modality or "unknown", mime_type=mime_type))
        return self

    def attach_media_url(self, url: str, *, modality: str = "") -> "LLM":
        """Attach a media URL (``data:`` URLs are decoded and uploaded). Empty URLs are ignored."""
        if not url:
            return self
        if url.startswith("data:"):
            mime_type, payload = _parse_data_url(url)
            return self.attach_media(content=payload, mime_type=mime_type, modality=modality)
        return self.attach_media(uri=url, modality=modality)

    def record(
        self,
        *,
        input_messages: Optional[List[Message]] = None,
        output_messages: Optional[List[Message]] = None,
        media_attachments: Optional[List[MediaAttachment]] = None,
        usage: Optional[Usage] = None,
        reasoning: Union[Reasoning, str, None] = None,
        response_id: Optional[str] = None,
        response_model: Optional[str] = None,
        finish_reasons: Optional[List[str]] = None,
        output_type: Optional[str] = None,
    ) -> "LLM":
        """Set several fields at once (only the ones passed)."""
        if input_messages is not None:
            self.input_messages = input_messages
        if output_messages is not None:
            self.output_messages = output_messages
        if media_attachments is not None:
            self.media_attachments = media_attachments
        if usage is not None:
            self.usage = usage
        if reasoning is not None:
            self.reasoning = Reasoning(content=reasoning) if isinstance(reasoning, str) else reasoning
        if response_id is not None:
            self.response_id = response_id
        if response_model is not None:
            self.response_model = response_model
        if finish_reasons is not None:
            self.finish_reasons = finish_reasons
        if output_type is not None:
            self.output_type = output_type
        return self

    def __enter__(self) -> "LLM":
        if self._token is None:
            self._token = _current_llm.set(self)
        return super().__enter__()

    def end(self) -> None:
        if self._ended:
            return
        if self._token is not None:
            try:
                _current_llm.reset(self._token)
            except ValueError:
                pass
            self._token = None
        self._finish()

    def _payload(self, conv: _ConvInfo, client: Optional["WeftClient"]) -> Tuple[Dict[str, Any], Any, Dict[str, Any], Dict[str, Any]]:
        attrs: Dict[str, Any] = {"gen_ai.operation.name": "chat", "gen_ai.request.model": self.model}
        for k, v in (
            ("gen_ai.provider.name", self.provider_name),
            ("gen_ai.response.id", self.response_id),
            ("gen_ai.response.model", self.response_model),
            ("gen_ai.output.type", self.output_type),
        ):
            if v:
                attrs[k] = v
        if self.finish_reasons:
            attrs["gen_ai.response.finish_reasons"] = list(self.finish_reasons)
        params = {
            "temperature": self.request_temperature,
            "max_tokens": self.request_max_tokens,
            "top_p": self.request_top_p,
            "frequency_penalty": self.request_frequency_penalty,
            "presence_penalty": self.request_presence_penalty,
            "seed": self.request_seed,
            "stop": list(self.request_stop_sequences) or None,
            "n": self.request_choice_count,
        }
        attr_names = {"stop": "stop_sequences", "n": "choice.count"}
        for k, v in params.items():
            if v is not None:
                attrs[f"gen_ai.request.{attr_names.get(k, k)}"] = v
        # usage
        summary: Dict[str, Any] = {}
        u = self.usage
        if u.input_tokens or u.output_tokens or u.total_tokens or u.reasoning_tokens or u.cache_read_input_tokens or u.cache_creation_input_tokens:
            entry: Dict[str, Any] = {
                "requests": 1,
                "prompt_tokens": u.input_tokens,
                "completion_tokens": u.output_tokens,
                "total_tokens": u.total_tokens if u.total_tokens is not None else u.input_tokens + u.output_tokens,
            }
            for k in ("reasoning_tokens", "cache_read_input_tokens", "cache_creation_input_tokens"):
                if getattr(u, k):
                    entry[k] = getattr(u, k)
            summary["usage"] = {self.response_model or self.model or "unknown": entry}
        inputs: Dict[str, Any] = {"model": self.model} if self.model else {}
        for k, v in params.items():
            if v is not None:
                inputs[k] = v
        if not conv.include_content:
            return inputs, None, attrs, summary
        client_ = client
        ins = self.input_messages
        outs = self.output_messages
        if self.system_instructions:
            attrs["gen_ai.system_instructions"] = _system_parts_json(self.system_instructions)
        media = []
        for m in self.media_attachments:
            try:
                media.append(json.loads(m.ref))
            except (TypeError, ValueError):
                media.append({"type": "uri", "uri": m.ref, "mime_type": m.mime_type, "modality": m.modality})
        inputs["messages"] = _messages_with_system(self.system_instructions, ins, client_, media)
        out_msgs = [_chat_message(m, client_) for m in outs]
        if self.reasoning.content and not any(isinstance(p, ReasoningPart) for m in outs for p in m.parts):
            rpart = {"type": "thinking", "thinking": self.reasoning.content}
            idx = max((i for i, m in enumerate(outs) if m.role == "assistant"), default=len(out_msgs) - 1)
            if idx < 0:
                out_msgs.append({"role": "assistant", "content": [rpart]})
            else:
                c = out_msgs[idx].get("content")
                parts = [{"type": "text", "text": c}] if isinstance(c, str) and c else (list(c) if isinstance(c, list) else [])
                out_msgs[idx]["content"] = [rpart] + parts
        output: Any = None
        if out_msgs:
            choices = []
            for i, m in enumerate(out_msgs):
                fr = self.finish_reasons[i] if i < len(self.finish_reasons) else (self.finish_reasons[-1] if self.finish_reasons else None)
                choices.append({"index": i, "message": m, "finish_reason": fr})
            output = {"object": "chat.completion", "model": self.response_model or self.model, "choices": choices}
            if self.response_id:
                output["id"] = self.response_id
        return inputs, output, attrs, summary


# ---------------------------------------------------------------------------
# SubAgent
# ---------------------------------------------------------------------------


def _agent_attrs(
    *, agent_name: str, conv: _ConvInfo, model: str, agent_id: str, agent_description: str, agent_version: str
) -> Dict[str, Any]:
    attrs: Dict[str, Any] = {"gen_ai.operation.name": "invoke_agent", "gen_ai.agent.name": agent_name}
    for k, v in (
        ("gen_ai.conversation.name", conv.conversation_name),
        ("gen_ai.request.model", model),
        ("gen_ai.agent.id", agent_id),
        ("gen_ai.agent.description", agent_description),
        ("gen_ai.agent.version", agent_version),
    ):
        if v:
            attrs[k] = v
    return attrs


class SubAgent(_SpanBase):
    """A delegated agent invocation within a turn (a nested ``invoke_agent`` call)."""

    model_config = ConfigDict(validate_assignment=True)

    name: str = ""
    model: str = ""
    agent_id: str = ""
    agent_description: str = ""
    agent_version: str = ""
    system_instructions: List[str] = Field(default_factory=list)
    input_messages: List[Message] = Field(default_factory=list)
    output_messages: List[Message] = Field(default_factory=list)
    tool_name: str = ""
    tool_call_id: str = ""
    tool_call_arguments: JSONString = ""
    tool_call_result: JSONString = ""
    started_at: Optional[datetime.datetime] = None
    ended_at: Optional[datetime.datetime] = None

    def model_post_init(self, context: Any, /) -> None:
        self._op_name = "invoke_agent"
        self._kind = "agent"
        if not self.name and _current_agent_name.get():
            self.name = _current_agent_name.get() or ""

    def _span_display_name(self) -> str:
        return _span_name("invoke_agent", self.name)

    def start_llm(self, *, model: str = "", provider_name: str = "", system_instructions: Optional[List[str]] = None) -> LLM:
        """Start an LLM call nested in this sub-agent."""
        llm = LLM(model=model or self.model, provider_name=provider_name, system_instructions=system_instructions or [])
        self._pin_parent(llm)
        llm._token = _current_llm.set(llm)
        return llm

    def start_tool(self, *, name: str, arguments: Any = "", tool_call_id: str = "") -> Tool:
        """Start a tool execution nested in this sub-agent."""
        tool = Tool(name=name, arguments=arguments, tool_call_id=tool_call_id)
        self._pin_parent(tool)
        return tool

    def start_subagent(self, *, name: str, model: str = "") -> "SubAgent":
        """Start a sub-agent nested in this one."""
        sub = SubAgent(name=name, model=model or self.model)
        self._pin_parent(sub)
        return sub

    def start_handoff(self, to_agent: str, input: Any = None, *, kind: str = "handoff") -> "Handoff":
        """Start a hand-off from this sub-agent (use it as a context manager)."""
        h = Handoff(to_agent=to_agent, from_agent=self.name, kind=kind, input=input)
        self._pin_parent(h)
        return h

    def llm(self, **kwargs: Any) -> LLM:
        """Deprecated alias of ``start_llm``."""
        warnings.warn("`llm` is deprecated; use `start_llm` instead.", DeprecationWarning, stacklevel=2)
        return self.start_llm(**kwargs)

    def tool(self, **kwargs: Any) -> Tool:
        """Deprecated alias of ``start_tool``."""
        warnings.warn("`tool` is deprecated; use `start_tool` instead.", DeprecationWarning, stacklevel=2)
        return self.start_tool(**kwargs)

    def record(
        self,
        *,
        name: Optional[str] = None,
        model: Optional[str] = None,
        system_instructions: Optional[List[str]] = None,
        input_messages: Optional[List[Message]] = None,
        output_messages: Optional[List[Message]] = None,
        tool_name: Optional[str] = None,
        tool_call_id: Optional[str] = None,
        tool_call_arguments: Optional[Any] = None,
        tool_call_result: Optional[Any] = None,
        agent_id: Optional[str] = None,
        agent_description: Optional[str] = None,
        agent_version: Optional[str] = None,
    ) -> "SubAgent":
        """Set several fields at once (only the ones passed)."""
        for k, v in locals().items():
            if k != "self" and v is not None:
                setattr(self, k, v)
        return self

    def _payload(self, conv: _ConvInfo, client: Optional["WeftClient"]) -> Tuple[Dict[str, Any], Any, Dict[str, Any], Dict[str, Any]]:
        attrs = _agent_attrs(
            agent_name=self.name, conv=conv, model=self.model, agent_id=self.agent_id,
            agent_description=self.agent_description, agent_version=self.agent_version,
        )
        if self.tool_name:
            attrs["gen_ai.tool.name"] = self.tool_name
        if self.tool_call_id:
            attrs["gen_ai.tool.call.id"] = self.tool_call_id
        if not conv.include_content:
            return {}, None, attrs, {}
        if self.tool_call_arguments:
            attrs["gen_ai.tool.call.arguments"] = self.tool_call_arguments
        if self.tool_call_result:
            attrs["gen_ai.tool.call.result"] = self.tool_call_result
        if self.system_instructions:
            attrs["gen_ai.system_instructions"] = _system_parts_json(self.system_instructions)
        ins = self.input_messages
        outs = self.output_messages
        inputs: Dict[str, Any] = {}
        msgs = _messages_with_system(self.system_instructions, ins, client)
        if msgs:
            inputs["messages"] = msgs
        output = {"messages": [_chat_message(m, client) for m in outs]} if outs else None
        return inputs, output, attrs, {}

    def __enter__(self) -> "SubAgent":
        return super().__enter__()


# ---------------------------------------------------------------------------
# Hand-offs
# ---------------------------------------------------------------------------

_HANDOFF_KINDS = ("handoff", "delegate", "return")


def _agent_of_call(call: Call) -> str:
    span = getattr(call, "_weft_span", None)
    if isinstance(span, Turn):
        return span.agent_name
    if isinstance(span, SubAgent):
        return span.name
    attrs = call.attributes if isinstance(call.attributes, dict) else {}
    agent = ((attrs.get("gen_ai") or {}).get("agent") or {}) if isinstance(attrs.get("gen_ai"), dict) else {}
    name = agent.get("name") if isinstance(agent, dict) else None
    if not name:
        name = attrs.get("gen_ai.agent.name")
    return name if isinstance(name, str) else ""


def current_agent_name() -> str:
    """The agent running now: the innermost agent span (turn, sub-agent, or a
    call with ``gen_ai.agent.name``) on the call stack, else the current turn's."""
    for call in reversed(_context._call_stack.get()):
        name = _agent_of_call(call)
        if name:
            return name
    t = get_current_turn()
    return t.agent_name if t is not None and not t._ended else ""


class Handoff(_SpanBase):
    """Control passing from one agent to another (``weft.handoff.*``, DESIGN §2.1).

    ``kind`` is ``"handoff"`` (the other agent takes over) or ``"delegate"``
    (it works for the caller and returns). Agent runs started inside a
    ``with weft.start_handoff(...)`` block are its children, so the server
    links the hand-off to them; an instant ``weft.handoff(...)`` is linked to
    the next run of ``to_agent`` in the same trace.
    """

    model_config = ConfigDict(validate_assignment=True)

    to_agent: str
    from_agent: str = ""
    kind: str = "handoff"
    input: Any = None
    output: Any = None
    started_at: Optional[datetime.datetime] = None
    ended_at: Optional[datetime.datetime] = None

    def model_post_init(self, context: Any, /) -> None:
        self._op_name = "handoff"
        self._kind = "handoff"
        if not isinstance(self.to_agent, str) or not self.to_agent.strip():
            raise ValueError("a hand-off needs the name of the agent it goes to")
        k = (self.kind or "handoff").strip().lower()
        if k not in _HANDOFF_KINDS:
            raise ValueError(f"hand-off kind must be one of {', '.join(_HANDOFF_KINDS)}, not {self.kind!r}")
        if k != self.kind:
            self.kind = k

    def _span_display_name(self) -> str:
        return f"{'delegate' if self.kind == 'delegate' else 'handoff'} to {self.to_agent}"

    def _start(self, *, set_current: bool = True) -> None:
        if not self.from_agent:
            self.from_agent = current_agent_name()
        super()._start(set_current=set_current)

    def _payload(self, conv: _ConvInfo, client: Optional["WeftClient"]) -> Tuple[Dict[str, Any], Any, Dict[str, Any], Dict[str, Any]]:
        attrs: Dict[str, Any] = {"weft.handoff.to_agent": self.to_agent, "weft.handoff.kind": self.kind}
        if self.from_agent and self.from_agent != self.to_agent:
            attrs["weft.handoff.from_agent"] = self.from_agent
        if not conv.include_content:
            return {}, None, attrs, {}
        inputs = {} if self.input is None else {"input": self.input}
        return inputs, self.output, attrs, {}


def start_handoff(to_agent: str, input: Any = None, *, kind: str = "handoff", from_agent: str = "") -> Handoff:
    """A hand-off span to use as a context manager; agent runs inside it are its children.

    >>> with weft.start_handoff("researcher", input=task, kind="delegate") as h:
    ...     h.output = researcher.run(task)
    """
    h = Handoff(to_agent=to_agent, from_agent=from_agent, kind=kind, input=input)
    h._fallback_parent = get_current_turn()
    return h


def handoff(to_agent: str, input: Any = None, *, kind: str = "handoff", from_agent: str = "", output: Any = None) -> Handoff:
    """Record that control passed to ``to_agent`` now (an instant hand-off span).

    ``from_agent`` defaults to the agent running now. Use ``start_handoff`` as a
    context manager to cover the other agent's run."""
    h = start_handoff(to_agent, input, kind=kind, from_agent=from_agent)
    h.output = output
    h._start(set_current=False)
    h.end()
    return h


# ---------------------------------------------------------------------------
# Reasoning
# ---------------------------------------------------------------------------


def reasoning(text: str = "", kind: str = "thinking", *, tokens: Optional[int] = None) -> None:
    """Record a reasoning step of the agent or model running now (``weft.reasoning``).

    ``kind`` is ``"thinking"``, ``"summary"`` or ``"redacted"`` (no text is kept).
    The step goes on the innermost running span or op call and is linked by
    the server to the tool call or hand-off that follows it. Nothing is
    recorded when ``capture_reasoning`` is off or tracing is not initialised.
    """
    from ._client import tracing_client
    from ._reasoning import step

    client = tracing_client()
    if client is None or _context.tracing_suppressed() or not client.capture_reasoning:
        return
    st = step(text, kind, tokens)
    if st["kind"] != "redacted" and not st["text"].strip():
        return
    call = _context.get_current_call()
    span = getattr(call, "_weft_span", None) if call is not None else None
    if call is None:
        span = get_current_llm() or get_current_turn()
    if span is not None:
        if span._ended:
            logger.warning("weft: reasoning() ignored: the current span already ended")
            return
        span._reasoning.append(st)
        return
    if call is not None and not call._ended:
        client.add_reasoning(call, [st])
    else:
        logger.debug("weft: reasoning() outside any span or op; nothing recorded")


# ---------------------------------------------------------------------------
# Turn and Conversation
# ---------------------------------------------------------------------------


class Turn(_SpanBase):
    """One user-agent exchange: an ``invoke_agent`` call, by default the root of its own trace.

    Set ``continue_parent_trace=True`` (on the conversation or the turn) to nest
    the turn under the current call instead.
    """

    agent_name: str = ""
    model: str = ""
    agent_id: str = ""
    agent_description: str = ""
    agent_version: str = ""
    system_instructions: List[str] = Field(default_factory=list)
    messages: List[Message] = Field(default_factory=list)
    output_messages: List[Message] = Field(default_factory=list)
    spans: List[Union[LLM, Tool, SubAgent, Handoff]] = Field(default_factory=list)
    continue_parent_trace: bool = False
    started_at: Optional[datetime.datetime] = None
    ended_at: Optional[datetime.datetime] = None

    _token: Optional[Token] = PrivateAttr(default=None)
    _batch: bool = PrivateAttr(default=False)

    def model_post_init(self, context: Any, /) -> None:
        self._op_name = "invoke_agent"
        self._kind = "agent"
        if self.started_at is None:
            self.started_at = _now()
        if not self.agent_name and _current_agent_name.get():
            self.agent_name = _current_agent_name.get() or ""

    def _span_display_name(self) -> str:
        return _span_name("invoke_agent", self.agent_name)

    def _start(self, *, set_current: bool = True) -> None:
        # A turn of another agent started while an agent is running is that
        # agent delegating: it nests (same trace) so the hand-off is recorded.
        running = current_agent_name() if self._explicit_parent is None and not self._batch else ""
        nested = bool(running) and running != self.agent_name and _context.get_current_call() is not None
        self._new_trace = not (self.continue_parent_trace or nested)
        super()._start(set_current=set_current)

    def user(self, content: str) -> "Turn":
        """Append a user message to the turn's input."""
        self.messages.append(Message(role="user", content=content))
        return self

    def start_llm(self, *, model: str = "", provider_name: str = "", system_instructions: Optional[List[str]] = None) -> LLM:
        """Start an LLM call (child of this turn); it becomes ``get_current_llm()``."""
        llm = LLM(model=model or self.model, provider_name=provider_name, system_instructions=system_instructions or [])
        self._pin_parent(llm)
        llm._token = _current_llm.set(llm)
        return llm

    def start_tool(self, *, name: str, arguments: Any = "", tool_call_id: str = "") -> Tool:
        """Start a tool execution (child of this turn)."""
        tool = Tool(name=name, arguments=arguments, tool_call_id=tool_call_id)
        self._pin_parent(tool)
        return tool

    def start_subagent(self, *, name: str, model: str = "", system_instructions: Optional[List[str]] = None) -> SubAgent:
        """Start a sub-agent invocation (nested ``invoke_agent``, same trace)."""
        sub = SubAgent(name=name, model=model or self.model, system_instructions=system_instructions or [])
        self._pin_parent(sub)
        return sub

    def start_handoff(self, to_agent: str, input: Any = None, *, kind: str = "handoff") -> "Handoff":
        """Start a hand-off from this turn's agent (use it as a context manager)."""
        h = Handoff(to_agent=to_agent, from_agent=self.agent_name, kind=kind, input=input)
        self._pin_parent(h)
        return h

    def handoff(self, to_agent: str, input: Any = None, *, kind: str = "handoff", output: Any = None) -> "Handoff":
        """Record an instant hand-off from this turn's agent."""
        h = self.start_handoff(to_agent, input, kind=kind)
        h.output = output
        h._start(set_current=False)
        h.end()
        return h

    def llm(self, **kwargs: Any) -> LLM:
        """Deprecated alias of ``start_llm``."""
        warnings.warn("`llm` is deprecated; use `start_llm` instead.", DeprecationWarning, stacklevel=2)
        return self.start_llm(**kwargs)

    def tool(self, **kwargs: Any) -> Tool:
        """Deprecated alias of ``start_tool``."""
        warnings.warn("`tool` is deprecated; use `start_tool` instead.", DeprecationWarning, stacklevel=2)
        return self.start_tool(**kwargs)

    def subagent(self, **kwargs: Any) -> SubAgent:
        """Deprecated alias of ``start_subagent``."""
        warnings.warn("`subagent` is deprecated; use `start_subagent` instead.", DeprecationWarning, stacklevel=2)
        return self.start_subagent(**kwargs)

    def record(
        self,
        *,
        messages: Optional[List[Message]] = None,
        output_messages: Optional[List[Message]] = None,
        system_instructions: Optional[List[str]] = None,
        agent_name: Optional[str] = None,
        model: Optional[str] = None,
        agent_id: Optional[str] = None,
        agent_description: Optional[str] = None,
        agent_version: Optional[str] = None,
    ) -> "Turn":
        """Set several fields at once (only the ones passed; ``messages`` replaces the input)."""
        for k, v in locals().items():
            if k != "self" and v is not None:
                setattr(self, k, v)
        return self

    def __enter__(self) -> "Turn":
        if self._token is None:
            self._token = _current_turn.set(self)
        return super().__enter__()

    def end(self) -> None:
        if self._ended:
            return
        if self._token is not None:
            try:
                _current_turn.reset(self._token)
            except ValueError:
                pass
            self._token = None
        self._finish()

    def _payload(self, conv: _ConvInfo, client: Optional["WeftClient"]) -> Tuple[Dict[str, Any], Any, Dict[str, Any], Dict[str, Any]]:
        attrs = _agent_attrs(
            agent_name=self.agent_name, conv=conv, model=self.model, agent_id=self.agent_id,
            agent_description=self.agent_description, agent_version=self.agent_version,
        )
        if not conv.include_content:
            return {}, None, attrs, {}
        if self.system_instructions:
            attrs["gen_ai.system_instructions"] = _system_parts_json(self.system_instructions)
        ins = self.messages
        outs = self.output_messages
        inputs: Dict[str, Any] = {}
        msgs = _messages_with_system(self.system_instructions, ins, client)
        if msgs:
            inputs["messages"] = msgs
        output = {"messages": [_chat_message(m, client) for m in outs]} if outs else None
        return inputs, output, attrs, {}


class Conversation(BaseModel):
    """A conversation: groups turns by ``conversation_id`` (recorded as the calls' ``thread_id``).

    Conversation-level ``agent_name``/``model``/``agent_*`` are defaults for
    its turns. ``attributes`` are added to every span it records;
    ``include_content=False`` drops messages, tool arguments and results.
    """

    model_config = ConfigDict(protected_namespaces=())

    conversation_id: str = ""
    conversation_name: str = ""
    agent_name: str = ""
    model: str = ""
    agent_id: str = ""
    agent_description: str = ""
    agent_version: str = ""
    include_content: bool = True
    continue_parent_trace: bool = False
    attributes: Dict[str, Any] = Field(default_factory=dict)
    #: The agent team (``weft.Team``, an id, ``(id, name)`` or ``{"id", "name"}``).
    team: Any = None

    _ended: bool = PrivateAttr(default=False)
    _token: Optional[Token] = PrivateAttr(default=None)
    _team_token: Optional[Token] = PrivateAttr(default=None)
    _current_turn: Optional[Turn] = PrivateAttr(default=None)
    _turns: List[Turn] = PrivateAttr(default_factory=list)

    def model_post_init(self, context: Any, /) -> None:
        if not self.conversation_id:
            self.conversation_id = str(uuid.uuid4())
        self.team = _team_info(self.team)

    def _info(self) -> _ConvInfo:
        return _ConvInfo(self.conversation_id, self.conversation_name, self.include_content, dict(self.attributes), self.team)

    def _activate_team(self) -> None:
        if self.team is not None and self._team_token is None:
            self._team_token = _context.set_team(self.team)

    def start_turn(
        self,
        *,
        user_message: str = "",
        model: str = "",
        agent_name: str = "",
        agent_id: str = "",
        agent_description: str = "",
        agent_version: str = "",
        system_instructions: Optional[List[str]] = None,
    ) -> Turn:
        """Create a turn (ending the previous one if still open); it becomes ``get_current_turn()``.

        Started from inside a running turn of another agent, the new turn is
        that agent delegating: the running turn stays open and the new one
        nests under it."""
        cur = self._current_turn
        if cur is not None and not cur._ended:
            inside = cur._call is not None and any(c is cur._call for c in _context._call_stack.get())
            if not (inside and (agent_name or self.agent_name) != cur.agent_name):
                cur.end()
        turn = Turn(
            agent_name=agent_name or self.agent_name,
            model=model or self.model,
            agent_id=agent_id or self.agent_id,
            agent_description=agent_description or self.agent_description,
            agent_version=agent_version or self.agent_version,
            system_instructions=system_instructions or [],
            continue_parent_trace=self.continue_parent_trace,
        )
        turn._conv = self._info()
        if user_message:
            turn.messages.append(Message(role="user", content=user_message))
        turn._token = _current_turn.set(turn)
        self._current_turn = turn
        self._turns = [t for t in self._turns if not t._ended] + [turn]
        return turn

    def end(self) -> None:
        if self._ended:
            return
        self._ended = True
        for t in reversed(self._turns):
            if not t._ended:
                t.end()
        if self._token is not None:
            try:
                _current_conversation.reset(self._token)
            except ValueError:
                pass
            self._token = None
        if self._team_token is not None:
            _context.reset_team(self._team_token)
            self._team_token = None

    def __enter__(self) -> "Conversation":
        if self._token is None:
            self._token = _current_conversation.set(self)
        self._activate_team()
        return self

    def __exit__(self, exc_type: Any, exc_val: Any, exc_tb: Any) -> Literal[False]:
        self.end()
        return False


# ---------------------------------------------------------------------------
# context variables and module functions
# ---------------------------------------------------------------------------

_current_conversation: ContextVar[Optional[Conversation]] = ContextVar("weft_current_conversation", default=None)
_current_turn: ContextVar[Optional[Turn]] = ContextVar("weft_current_turn", default=None)
_current_llm: ContextVar[Optional[LLM]] = ContextVar("weft_current_llm", default=None)


def start_conversation(
    *,
    agent_name: str = "",
    model: str = "",
    conversation_id: str = "",
    conversation_name: str = "",
    include_content: bool = True,
    continue_parent_trace: bool = False,
    attributes: Optional[Dict[str, Any]] = None,
    team: Any = None,
) -> Conversation:
    """Create and activate a conversation (usable as a context manager).

    ``team`` (a ``weft.Team``, an id, ``(id, name)`` or ``{"id", "name"}``)
    declares the agent team; every span of the conversation, and every call
    made while it is active, carries ``weft.team.id`` / ``weft.team.name``.
    """
    conversation = Conversation(
        agent_name=agent_name,
        model=model,
        conversation_id=conversation_id,
        conversation_name=conversation_name,
        include_content=include_content,
        continue_parent_trace=continue_parent_trace,
        attributes=dict(attributes or {}),
        team=team,
    )
    conversation._token = _current_conversation.set(conversation)
    conversation._activate_team()
    return conversation


def start_turn(*, user_message: str = "", model: str = "", agent_name: str = "", system_instructions: Optional[List[str]] = None) -> Turn:
    """Start a turn in the current conversation (a disconnected turn when there is none)."""
    conversation = get_current_conversation()
    if conversation is not None:
        return conversation.start_turn(user_message=user_message, model=model, agent_name=agent_name, system_instructions=system_instructions)
    turn = Turn(agent_name=agent_name, model=model, system_instructions=system_instructions or [])
    if user_message:
        turn.messages.append(Message(role="user", content=user_message))
    return turn


def start_llm(*, model: str = "", provider_name: str = "", system_instructions: Optional[List[str]] = None) -> LLM:
    """Start an LLM call in the current turn (a disconnected one when there is none)."""
    turn = get_current_turn()
    if turn is not None:
        return turn.start_llm(model=model, provider_name=provider_name, system_instructions=system_instructions)
    return LLM(model=model, provider_name=provider_name, system_instructions=system_instructions or [])


def start_tool(*, name: str, arguments: Any = "", tool_call_id: str = "") -> Tool:
    """Start a tool span; it nests under the current call (or the current turn)."""
    tool = Tool(name=name, arguments=arguments, tool_call_id=tool_call_id)
    tool._fallback_parent = get_current_turn()
    return tool


def start_subagent(*, name: str, model: str = "", system_instructions: Optional[List[str]] = None) -> SubAgent:
    """Start a sub-agent span; it nests under the current call (or the current turn)."""
    sub = SubAgent(name=name, model=model, system_instructions=system_instructions or [])
    sub._fallback_parent = get_current_turn()
    return sub


def end_conversation() -> None:
    c = get_current_conversation()
    if c is not None:
        c.end()


def end_turn() -> None:
    t = get_current_turn()
    if t is not None:
        t.end()


def end_llm() -> None:
    llm = get_current_llm()
    if llm is not None:
        llm.end()


def get_current_conversation() -> Optional[Conversation]:
    return _current_conversation.get()


def get_current_turn() -> Optional[Turn]:
    return _current_turn.get()


def get_current_llm() -> Optional[LLM]:
    return _current_llm.get()


# ---------------------------------------------------------------------------
# batch logging
# ---------------------------------------------------------------------------


def _resolve_turn_timestamps(
    started_at: Optional[datetime.datetime], ended_at: Optional[datetime.datetime], spans: List[Any]
) -> Tuple[datetime.datetime, datetime.datetime]:
    """Caller-given times win; otherwise the span of the children; otherwise now."""
    first = min((s.started_at for s in spans if s.started_at is not None), default=None)
    last = max((s.ended_at for s in spans if s.ended_at is not None), default=None)
    now = _now()
    return started_at or first or now, ended_at or last or now


def _emit_turn(turn: Turn, info: _ConvInfo, attributes: Optional[Dict[str, Any]]) -> LogResult:
    client = _tracing_client()
    if client is None:
        return LogResult(conversation_id=info.conversation_id)
    turn.started_at, turn.ended_at = _resolve_turn_timestamps(turn.started_at, turn.ended_at, turn.spans)
    if attributes:
        info = _ConvInfo(info.conversation_id, info.conversation_name, info.include_content, {**info.attributes, **attributes}, info.team)
    turn._conv = info
    turn._client = client
    turn._batch = True
    turn._start(set_current=False)
    for child in turn.spans:
        child._conv = info
        child._client = client
        child._explicit_parent = turn
        if isinstance(child, Handoff) and not child.from_agent:
            child.from_agent = turn.agent_name
        if child.started_at is None:
            child.started_at = turn.started_at
        if child.ended_at is None:
            child.ended_at = child.started_at
        child._start(set_current=False)
        child._ended = True
        child._emit()
    turn._ended = True
    turn._emit()
    return LogResult(
        conversation_id=info.conversation_id,
        trace_ids=[turn.trace_id] if turn.trace_id else [],
        root_span_ids=[turn.call_id] if turn.call_id else [],
        span_count=1 + len(turn.spans),
    )


def log_turn(
    *,
    conversation_id: str,
    agent_name: str = "",
    conversation_name: str = "",
    model: str = "",
    agent_id: str = "",
    agent_description: str = "",
    agent_version: str = "",
    messages: Optional[List[Message]] = None,
    output_messages: Optional[List[Message]] = None,
    system_instructions: Optional[List[str]] = None,
    spans: Optional[List[Union[LLM, Tool, SubAgent, Handoff]]] = None,
    started_at: Optional[datetime.datetime] = None,
    ended_at: Optional[datetime.datetime] = None,
    include_content: bool = True,
    continue_parent_trace: bool = False,
    attributes: Optional[Dict[str, Any]] = None,
    team: Any = None,
) -> LogResult:
    """Record one finished turn and its child spans (no context managers needed).

    Child spans should carry ``started_at``/``ended_at``; the turn's own times
    default to the earliest/latest child times, then now.
    """
    resolved = list(spans or [])
    s, e = _resolve_turn_timestamps(started_at, ended_at, resolved)
    turn = Turn(
        agent_name=agent_name,
        model=model,
        agent_id=agent_id,
        agent_description=agent_description,
        agent_version=agent_version,
        system_instructions=system_instructions or [],
        messages=messages or [],
        output_messages=output_messages or [],
        spans=resolved,
        started_at=s,
        ended_at=e,
        continue_parent_trace=continue_parent_trace,
    )
    return _emit_turn(turn, _ConvInfo(conversation_id, conversation_name, include_content, team=_team_info(team) or _context.current_team()), attributes)


def log_conversation(
    *,
    turns: List[Turn],
    conversation_id: str = "",
    conversation_name: str = "",
    agent_name: str = "",
    model: str = "",
    agent_id: str = "",
    agent_description: str = "",
    agent_version: str = "",
    include_content: bool = True,
    continue_parent_trace: bool = False,
    attributes: Optional[Dict[str, Any]] = None,
    team: Any = None,
) -> LogResult:
    """Record a complete conversation; each Turn's ``spans`` are its children.

    Conversation-level ``agent_name``/``model``/``agent_*`` fill in what a turn
    leaves empty. ``conversation_id`` is generated when empty.
    """
    sid = conversation_id or str(uuid.uuid4())
    info = _ConvInfo(sid, conversation_name, include_content, team=_team_info(team) or _context.current_team())
    result = LogResult(conversation_id=sid)
    for turn in turns:
        t = turn.model_copy(
            update={
                "agent_name": turn.agent_name or agent_name,
                "model": turn.model or model,
                "agent_id": turn.agent_id or agent_id,
                "agent_description": turn.agent_description or agent_description,
                "agent_version": turn.agent_version or agent_version,
                "continue_parent_trace": continue_parent_trace,
                "spans": [s.model_copy() for s in turn.spans],
            }
        )
        r = _emit_turn(t, info, attributes)
        result.trace_ids.extend(r.trace_ids)
        result.root_span_ids.extend(r.root_span_ids)
        result.span_count += r.span_count
    return result
