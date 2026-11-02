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
    from ._call import Call

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


_thread: contextvars.ContextVar[Optional[ThreadContext]] = contextvars.ContextVar("weft_thread", default=None)


def get_current_call() -> Optional["Call"]:
    """Return the innermost call being traced in this context, if any."""
    stack = _call_stack.get()
    return stack[-1] if stack else None


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
    """Group calls into a conversation thread.

    Every call started inside the block gets ``thread_id``. Calls started
    directly inside the block (not nested in another call from the block) are
    *turns*: their ``turn_id`` is their own id; descendants inherit it.
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
