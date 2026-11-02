"""Shared helper for callback/span based integrations (LangChain, OpenAI Agents, DSPy).

Each framework run becomes a call. While a run is open its call is pushed on
the weft context, so ops and provider SDK calls made inside nest under it. LLM
usage is only recorded on a framework LLM run when no nested call (e.g. the
patched OpenAI SDK) was traced under it, so usage is never counted twice.
"""

from __future__ import annotations

import logging
import threading
from typing import Any, Dict, Optional

from .. import _context
from .._usage import add_usage

logger = logging.getLogger("weft")


class RunTracker:
    def __init__(self) -> None:
        self._runs: Dict[Any, Any] = {}
        self._tokens: Dict[Any, Any] = {}
        self._lock = threading.Lock()

    def start(
        self,
        run_id: Any,
        op_name: str,
        inputs: Dict[str, Any],
        *,
        parent_id: Any = None,
        kind: Optional[str] = None,
        display_name: Optional[str] = None,
        attributes: Optional[Dict[str, Any]] = None,
        push: bool = True,
    ) -> Any:
        from .._client import _UNSET, _synthetic_op, tracing_client

        client = tracing_client()
        if client is None or _context.tracing_suppressed():
            return None
        with self._lock:
            parent = self._runs.get(parent_id) if parent_id is not None else None
        try:
            call = client.create_call(
                _synthetic_op(op_name, kind),
                inputs,
                parent=parent if parent is not None else _UNSET,
                display_name=display_name,
                attributes=attributes,
            )
        except Exception as e:  # pragma: no cover - never break the framework
            logger.debug("weft: could not start %s: %s", op_name, e)
            return None
        with self._lock:
            self._runs[run_id] = call
        if push:
            self._tokens[run_id] = _context.push_call(call)
        return call

    def get(self, run_id: Any) -> Any:
        with self._lock:
            return self._runs.get(run_id)

    def end(
        self,
        run_id: Any,
        output: Any = None,
        error: Optional[BaseException] = None,
        usage: Optional[Dict[str, Any]] = None,
    ) -> Any:
        with self._lock:
            call = self._runs.pop(run_id, None)
        token = self._tokens.pop(run_id, None)
        if token is not None:
            _context.pop_call(token)
        if call is None:
            return None
        if usage and call._child_count == 0 and (usage.get("prompt_tokens") or usage.get("completion_tokens")):
            add_usage(call, usage.get("model"), usage.get("prompt_tokens") or 0, usage.get("completion_tokens") or 0, usage.get("total_tokens"))
        if call._client is not None:
            call._client.finish_call(call, output=output, exception=error)
        return call


def as_exception(err: Any) -> Optional[BaseException]:
    if err is None:
        return None
    if isinstance(err, BaseException):
        return err
    if isinstance(err, dict):
        msg = err.get("message") or str(err)
        data = err.get("data")
        return RuntimeError(f"{msg}: {data}" if data else msg)
    return RuntimeError(str(err))
