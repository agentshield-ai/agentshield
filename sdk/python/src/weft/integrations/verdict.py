"""Verdict (Haize Labs' LLM-judge framework, ``pip install verdict``).

Verdict has its own tracing interface (``verdict.util.tracing.Tracer``).
``patch_verdict`` adds a ``WeftVerdictTracer`` to every ``Pipeline`` (it wraps
the pipeline module's ``ensure_tracing_manager``, used by ``Pipeline(...)``
and ``Pipeline.run(tracers=...)``), first among the pipeline's tracers, so
Verdict's own call ids follow Weft's. As in Weave, each Verdict span becomes a
call:

* ``verdict.<Pipeline name>`` (workflow): the pipeline run, with
  ``input_data``, ``max_workers`` and the per-leaf outputs;
* ``verdict.<unit>`` (``JudgeUnit``, ``PairwiseJudgeUnit``, ``MapUnit``,
  ``MeanPoolUnit`` ...; kind ``scorer`` for judge units): each unit
  execution, with its input schema and response.

Units run in Verdict's thread pool; parents are resolved from Verdict's
``(trace_id, parent_id)`` like Weave does. Verdict starts each block of a
chain (``a >> b``) with the *finished* previous block as its parent, so
finished calls stay resolvable until the call that opened the trace (the
pipeline run) ends, and ``b`` nests under ``a`` in one trace, as in Weave.
Each unit call is the current weft call while it runs, so LLM requests made
through patched clients
(``litellm``, ``openai``, ``instructor`` ...) nest under the unit. A pipeline
run inside an ``@weft.op`` nests under that op. Pass ``tracer=`` to
``Pipeline`` as usual; Weft's tracer is added alongside it (a lone
``NoOpTracer`` is replaced).
"""

from __future__ import annotations

import contextlib
import importlib
import logging
import threading
from typing import Any, Dict, Iterator, Optional, Tuple

from .. import _context
from . import _settings
from ._common import _tracing, patch_attr

logger = logging.getLogger("weft")

INTEGRATION = "verdict"
_JUDGE_HINTS = ("judge", "pairwise", "rank", "score", "categorical", "verify", "verifier")

_tracer_cls: Any = None
_tracer: Any = None
_lock = threading.Lock()


def _kind(name: str, root: bool) -> Optional[str]:
    if root:
        return "workflow"
    low = name.lower()
    return "scorer" if any(h in low for h in _JUDGE_HINTS) else None


def _plain(v: Any, depth: int = 0) -> Any:
    """Verdict Schemas (pydantic) and units -> JSON-friendly values."""
    if depth > 6:
        return repr(v)
    if v is None or isinstance(v, (str, int, float, bool)):
        return v
    if isinstance(v, dict):
        return {str(k): _plain(x, depth + 1) for k, x in v.items()}
    if isinstance(v, (list, tuple)):
        return [_plain(x, depth + 1) for x in v]
    dump = getattr(v, "model_dump", None)
    if callable(dump):
        try:
            return _plain(dump(), depth + 1)
        except Exception:
            pass
    mod = type(v).__module__ or ""
    if mod.startswith("verdict"):
        # Units, layers, blocks: a short description instead of the object graph.
        name = getattr(v, "_char", None) or getattr(v, "char", None) or type(v).__name__
        out: Dict[str, Any] = {"_type": type(v).__name__, "name": str(name)}
        for attr in ("prefix", "should_pin_output"):
            x = getattr(v, attr, None)
            if x is not None and isinstance(x, (str, bool, int, list, tuple)):
                out[attr] = _plain(x, depth + 1)
        return out
    return v


def _make_tracer_cls(Tracer: Any, VerdictCall: Any, current_trace_context: Any, TraceContext: Any) -> Any:
    class WeftVerdictTracer(Tracer):  # type: ignore[misc, valid-type]
        """Logs Verdict spans as Weft calls (see the module docs)."""

        def __init__(self) -> None:
            # (trace_id, verdict call_id) -> weft Call. Finished calls stay here
            # until the call that opened their trace ends, because Verdict hands
            # a *finished* block's call id to the next block as its parent
            # (``GraphExecutor._on_task_complete``).
            self._calls: Dict[Tuple[Any, Any], Any] = {}
            # trace_id -> call id of the first call this tracer saw in it.
            self._owners: Dict[Any, Any] = {}
            self._lock = threading.Lock()

        def _release(self, trace_id: Any, call_id: Any) -> None:
            with self._lock:
                if self._owners.get(trace_id) != call_id:
                    return
                del self._owners[trace_id]
                for key in [k for k in self._calls if k[0] == trace_id]:
                    del self._calls[key]

        @contextlib.contextmanager
        def start_call(self, name: str, inputs: Dict[str, Any], trace_id: Optional[str] = None, parent_id: Optional[str] = None) -> Iterator[Any]:
            parent_ctx = current_trace_context.get()
            if parent_id is None and parent_ctx is not None:
                parent_id = parent_ctx.call_id
            if trace_id is None and parent_ctx is not None:
                trace_id = parent_ctx.trace_id
            client = _tracing(INTEGRATION)
            if client is None:
                vc = VerdictCall(name=name, inputs=inputs, trace_id=trace_id, parent_id=parent_id)
                token = current_trace_context.set(TraceContext(trace_id, vc.call_id, parent_id))
                try:
                    yield vc
                finally:
                    current_trace_context.reset(token)
                return
            with self._lock:
                parent = self._calls.get((trace_id, parent_id)) if parent_id else None
            root = parent is None and parent_id is None
            op = _settings.integration_op(INTEGRATION, f"verdict.{name}", _kind(name, root))
            attrs = _settings.integration_attributes(INTEGRATION)
            attrs["verdict"] = {"trace_id": trace_id, "parent_id": parent_id, "name": name}
            kw: Dict[str, Any] = {"display_name": name, "attributes": attrs}
            if parent is not None:
                kw["parent"] = parent
            call = None
            try:
                call = client.create_call(op, {k: _plain(v) for k, v in (inputs or {}).items()}, **kw)
            except Exception as e:  # pragma: no cover - never break the pipeline
                logger.debug("weft: could not start verdict span %s: %s", name, e)
            vc = VerdictCall(name=name, inputs=inputs, trace_id=trace_id, parent_id=parent_id,
                             **({"call_id": str(call.id)} if call is not None else {}))
            if call is not None:
                with self._lock:
                    self._calls[(trace_id, vc.call_id)] = call
                    self._owners.setdefault(trace_id, vc.call_id)
            ctx_token = current_trace_context.set(TraceContext(trace_id, vc.call_id, parent_id))
            weft_token = _context.push_call(call) if call is not None else None
            exc: Optional[BaseException] = None
            try:
                yield vc
            except BaseException as e:
                exc = e
                vc.exception = e
                raise
            finally:
                if weft_token is not None:
                    _context.pop_call(weft_token)
                current_trace_context.reset(ctx_token)
                if call is not None:
                    err = exc if exc is not None else (vc.exception if isinstance(vc.exception, BaseException) else None)
                    try:
                        client.finish_call(call, output=_plain(vc.outputs), exception=err, op=op)
                    except Exception as e:  # pragma: no cover
                        logger.debug("weft: could not end verdict span %s: %s", name, e)
                    self._release(trace_id, vc.call_id)

    return WeftVerdictTracer


def get_tracer() -> Any:
    """The shared ``WeftVerdictTracer`` (``None`` when verdict is not installed)."""
    global _tracer_cls, _tracer
    with _lock:
        if _tracer is not None:
            return _tracer
        try:
            tracing = importlib.import_module("verdict.util.tracing")
        except ImportError:
            return None
        _tracer_cls = _make_tracer_cls(tracing.Tracer, tracing.Call, tracing.current_trace_context, tracing.TraceContext)
        _tracer = _tracer_cls()
        return _tracer


def _add_tracer(mgr: Any, tracer: Any) -> None:
    tracers = getattr(mgr, "tracers", None)
    if not isinstance(tracers, list):
        return
    if any(t is tracer for t in tracers):
        return
    # First, so the call id Verdict propagates (calls[0]) is Weft's and gets the outputs.
    noop = [t for t in tracers if type(t).__name__ == "NoOpTracer"]
    for t in noop:
        tracers.remove(t)
    tracers.insert(0, tracer)


def _wrap_ensure(orig: Any) -> Any:
    def ensure_tracing_manager(tracer: Any = None) -> Any:
        mgr = orig(tracer)
        try:
            if _settings.is_enabled(INTEGRATION):
                wt = get_tracer()
                if wt is not None:
                    _add_tracer(mgr, wt)
        except Exception as e:  # pragma: no cover
            logger.debug("weft: could not add the verdict tracer: %s", e)
        return mgr

    return ensure_tracing_manager


def patch_verdict() -> bool:
    """Add Weft's tracer to every Verdict pipeline (``Pipeline(...)`` and ``run(tracers=...)``)."""
    try:
        mod = importlib.import_module("verdict.core.pipeline")
    except ImportError:
        return False
    if get_tracer() is None or not hasattr(mod, "ensure_tracing_manager"):
        return False
    return patch_attr(mod, "ensure_tracing_manager", _wrap_ensure) or getattr(getattr(mod, "ensure_tracing_manager"), "__weft_patched__", False)


def unpatch_verdict() -> None:
    global _tracer, _tracer_cls
    with _lock:
        _tracer = None
        _tracer_cls = None


__all__ = ["patch_verdict", "get_tracer"]
