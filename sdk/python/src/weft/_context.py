"""Context propagation (contextvars) for the call stack, attributes and threads.

asyncio tasks inherit the context automatically. For threads use
``weft.ThreadPoolExecutor`` / ``weft.Thread`` or ``contextvars.copy_context()``.
"""

from __future__ import annotations

import concurrent.futures
import contextlib
import contextvars
import threading
from dataclasses import dataclass
from typing import TYPE_CHECKING, Any, Dict, Iterator, Optional, Tuple

from ._ids import uuid7

if TYPE_CHECKING:  # pragma: no cover
    from ._call import Span as Call

_call_stack: contextvars.ContextVar[Tuple["Call", ...]] = contextvars.ContextVar("weft_call_stack", default=())
_attributes: contextvars.ContextVar[Dict[str, Any]] = contextvars.ContextVar("weft_attributes", default={})
_suppressed: contextvars.ContextVar[bool] = contextvars.ContextVar("weft_suppressed", default=False)


@dataclass
class ThreadContext:
    thread_id: str
    # The call that was current when the thread block was entered. Calls
    # created whose parent is this call are turns.
    entry_parent_id: Optional[str]
    turn_id: Optional[str] = None

    @property
    def session_id(self) -> str:
        """The session (conversation) id the block's spans get."""
        return self.thread_id


_thread: contextvars.ContextVar[Optional[ThreadContext]] = contextvars.ContextVar("weft_thread", default=None)


@dataclass(frozen=True)
class TeamInfo:
    """The agent team calls belong to (``weft.team.id`` / ``weft.team.name``)."""

    id: str
    name: str = ""

    def attributes(self) -> Dict[str, Any]:
        team: Dict[str, Any] = {"id": self.id}
        if self.name:
            team["name"] = self.name
        return {"weft": {"team": team}}


_team: contextvars.ContextVar[Optional[TeamInfo]] = contextvars.ContextVar("weft_team", default=None)


def current_team() -> Optional[TeamInfo]:
    """The team declared by the innermost ``weft.team(...)`` block, if any."""
    return _team.get()


def set_team(team: Optional[TeamInfo]) -> contextvars.Token:
    return _team.set(team)


def reset_team(token: contextvars.Token) -> None:
    try:
        _team.reset(token)
    except ValueError:  # reset from another context
        _team.set(None)


def get_current_span() -> Optional["Call"]:
    """Return the innermost span being recorded in this context, if any."""
    stack = _call_stack.get()
    return stack[-1] if stack else None


# Earlier name of get_current_span (used throughout the SDK).
get_current_call = get_current_span


class NoCurrentSpanError(Exception):
    """Raised by ``require_current_span`` outside a traced function."""


# Earlier name of NoCurrentSpanError.
NoCurrentCallError = NoCurrentSpanError


def require_current_span() -> "Call":
    """The span of the traced function that is running now.

    Raises ``NoCurrentSpanError`` when called outside a traced function (or
    before ``weft.init``)."""
    call = get_current_call()
    if call is None:
        raise NoCurrentSpanError(
            "no traced function is running: call require_current_span() from inside a @weft.trace function, after weft.init()"
        )
    return call


# Earlier name of require_current_span.
require_current_call = require_current_span


def push_call(call: "Call") -> contextvars.Token:
    return _call_stack.set(_call_stack.get() + (call,))


def pop_call(token: contextvars.Token) -> None:
    try:
        _call_stack.reset(token)
    except ValueError:
        # Token created in another context (e.g. generator consumed elsewhere).
        stack = _call_stack.get()
        if stack:
            _call_stack.set(stack[:-1])


def tracing_suppressed() -> bool:
    return _suppressed.get()


def suppress() -> contextvars.Token:
    return _suppressed.set(True)


def unsuppress(token: contextvars.Token) -> None:
    try:
        _suppressed.reset(token)
    except ValueError:
        _suppressed.set(False)


@contextlib.contextmanager
def tracing_disabled() -> Iterator[None]:
    """Run the block without recording any calls (ops still execute)."""
    token = _suppressed.set(True)
    try:
        yield
    finally:
        _suppressed.reset(token)


def current_attributes() -> Dict[str, Any]:
    return _attributes.get()


@contextlib.contextmanager
def attributes(attrs: Dict[str, Any]) -> Iterator[None]:
    """Attach attributes to every call started inside the block.

    >>> with weft.attributes({"env": "prod", "user": "u1"}):
    ...     my_op()
    """
    merged = dict(_attributes.get())
    merged.update(attrs)
    token = _attributes.set(merged)
    try:
        yield
    finally:
        _attributes.reset(token)


@contextlib.contextmanager
def thread(thread_id: Optional[str] = None) -> Iterator[ThreadContext]:
    """Group spans into a conversation (session).

    Every span started inside the block gets ``session_id`` = ``thread_id``.
    Spans started directly inside the block (not nested in another span from
    the block) are *turns*: their ``turn_id`` is their own id; descendants
    inherit it.
    """
    parent = get_current_call()
    ctx = ThreadContext(thread_id=thread_id or uuid7(), entry_parent_id=parent.id if parent else None)
    token = _thread.set(ctx)
    try:
        yield ctx
    finally:
        _thread.reset(token)


def current_thread_context() -> Optional[ThreadContext]:
    return _thread.get()


# Eval metadata of the EvaluationLogger prediction being recorded (``weft.eval.*``
# span attributes: run_id, predict_and_score_call_id, project_id, kind, ...).
_eval_span: contextvars.ContextVar[Optional[Dict[str, Any]]] = contextvars.ContextVar("weft_eval_span", default=None)


def current_eval_span_context() -> Optional[Dict[str, Any]]:
    return _eval_span.get()


def set_eval_span_context(ctx: Optional[Dict[str, Any]]) -> contextvars.Token:
    return _eval_span.set(ctx)


def reset_eval_span_context(token: contextvars.Token) -> None:
    try:
        _eval_span.reset(token)
    except ValueError:
        _eval_span.set(None)


class ThreadPoolExecutor(concurrent.futures.ThreadPoolExecutor):
    """A ThreadPoolExecutor that propagates the weft call context to workers."""

    def submit(self, fn, /, *args, **kwargs):  # type: ignore[override]
        ctx = contextvars.copy_context()
        return super().submit(ctx.run, fn, *args, **kwargs)

    def map(self, fn, *iterables, timeout=None, chunksize=1):  # type: ignore[override]
        ctx = contextvars.copy_context()

        def run(*args):
            return ctx.copy().run(fn, *args)

        return super().map(run, *iterables, timeout=timeout, chunksize=chunksize)


class Thread(threading.Thread):
    """A threading.Thread that runs its target in a copy of the creator's context."""

    def __init__(self, *args, **kwargs):
        super().__init__(*args, **kwargs)
        self._weft_ctx = contextvars.copy_context()

    def run(self) -> None:
        self._weft_ctx.run(super().run)
