"""Haystack 2.x integration: a ``haystack.tracing.Tracer``.

``weft.init()`` enables ``WeftHaystackTracer`` with
``haystack.tracing.enable_tracing`` and turns on Haystack's content tracing so
component inputs and outputs are recorded (set
``HAYSTACK_CONTENT_TRACING_ENABLED=false`` before ``weft.init()`` to keep them
out). Each pipeline run becomes a ``haystack.pipeline.run`` call (chain) and
each component run a ``haystack.<ComponentType>`` call named after the
component, with span kinds for generators (llm), retrievers, embedders,
rankers, tools and agents.

Generator usage (``meta[].usage``) is recorded only when no provider SDK call
was traced under the component, so tokens are not counted twice.
"""

from __future__ import annotations

import contextlib
import contextvars
import importlib
import os
from typing import Any, Dict, Iterator, List, Optional

from ._common import to_dict
from ._spans import RunTracker

INTEGRATION = "haystack"

_CLS: Any = None
_registered: Any = None
_current: "contextvars.ContextVar[Optional[WeftHaystackSpan]]" = contextvars.ContextVar("weft_haystack_span", default=None)
_previous: Any = None


def component_kind(type_name: str) -> Optional[str]:
    t = type_name.lower()
    if "generator" in t or "chatmodel" in t:
        return "llm"
    if "embedder" in t:
        return "embedding"
    if "retriever" in t:
        return "retriever"
    if "ranker" in t:
        return "reranker"
    if "toolinvoker" in t or t.endswith("tool"):
        return "tool"
    if "agent" in t:
        return "agent"
    return None


def generator_usage(output: Any) -> Optional[Dict[str, Any]]:
    """Sum usage over ``replies``/``meta`` of a generator component's output."""
    if not isinstance(output, dict):
        return None
    metas: List[Any] = list(output.get("meta") or [])
    for r in output.get("replies") or []:
        m = getattr(r, "meta", None) if not isinstance(r, dict) else r.get("meta")
        if isinstance(m, dict):
            metas.append(m)
    p = c = 0
    model = None
    found = False
    for m in metas:
        if not isinstance(m, dict):
            continue
        u = to_dict(m.get("usage")) or {}
        if not isinstance(u, dict):
            continue
        pp = u.get("prompt_tokens", u.get("input_tokens"))
        cc = u.get("completion_tokens", u.get("output_tokens"))
        if pp is None and cc is None:
            continue
        found = True
        p += int(pp or 0)
        c += int(cc or 0)
        model = model or m.get("model")
    return {"model": model, "prompt_tokens": p, "completion_tokens": c} if found else None


class WeftHaystackSpan:
    """A Haystack span backed by a weft call (created lazily so inputs are known)."""

    def __init__(self, tracer: "Any", operation_name: str, tags: Dict[str, Any], parent: Optional["WeftHaystackSpan"]):
        self._tracer = tracer
        self.operation_name = operation_name
        self.tags: Dict[str, Any] = dict(tags or {})
        self.parent = parent
        self.key = object()
        self.call: Any = None

    # haystack.tracing.Span API
    def set_tag(self, key: str, value: Any) -> None:
        self.tags[key] = value
        if self.call is None and key in ("haystack.component.input", "haystack.pipeline.input_data", "haystack.agent.input"):
            self.materialize()

    def set_tags(self, tags: Dict[str, Any]) -> None:
        for k, v in tags.items():
            self.set_tag(k, v)

    def set_content_tag(self, key: str, value: Any) -> None:
        self.set_tag(key, value)

    def raw_span(self) -> Any:
        return self.call

    def get_correlation_data_for_logs(self) -> Dict[str, Any]:
        c = self.call
        return {"weft.span_id": c.id, "weft.trace_id": c.trace_id} if c is not None else {}

    # weft
    def _op(self) -> Any:
        t = self.tags
        if self.operation_name.startswith("haystack.component"):
            ctype = str(t.get("haystack.component.type") or "Component")
            return f"haystack.{ctype}", component_kind(ctype), t.get("haystack.component.name")
        if "pipeline" in self.operation_name:
            return self.operation_name, "chain", None
        if "agent" in self.operation_name:
            return self.operation_name, "agent", None
        return self.operation_name, None, None

    def _inputs(self) -> Dict[str, Any]:
        t = self.tags
        for k in ("haystack.component.input", "haystack.pipeline.input_data", "haystack.agent.input"):
            if k in t:
                v = t[k]
                return dict(v) if isinstance(v, dict) else {"input": v}
        return {}

    def materialize(self) -> Any:
        if self.call is not None:
            return self.call
        if self.parent is not None:
            self.parent.materialize()
        name, kind, display = self._op()
        attrs = {"haystack": {k: v for k, v in self.tags.items()
                              if k.endswith((".name", ".type", ".metadata", ".max_runs_per_component", ".visits"))}}
        parent_key = self.parent.key if self.parent is not None else None
        self.call = self._tracer.tracker.start(self.key, name, self._inputs(), parent_id=parent_key, kind=kind,
                                               display_name=display, attributes=attrs)
        return self.call

    def end(self, error: Optional[BaseException]) -> None:
        if self.call is None:
            self.materialize()
        t = self.tags
        out = None
        for k in ("haystack.component.output", "haystack.pipeline.output_data", "haystack.agent.output"):
            if k in t:
                out = t[k]
                break
        usage = generator_usage(out) if self._op()[1] == "llm" else None
        self._tracer.tracker.end(self.key, out, error=error, usage=usage)


def _base() -> Any:
    try:
        return getattr(importlib.import_module("haystack.tracing"), "Tracer")
    except (ImportError, AttributeError):
        return object


def _build() -> Any:
    global _CLS
    if _CLS is not None:
        return _CLS

    class WeftHaystackTracer(_base()):  # type: ignore[misc]
        """Records Haystack pipeline and component runs as weft calls."""

        def __init__(self) -> None:
            self.tracker = RunTracker(INTEGRATION)

        @contextlib.contextmanager
        def trace(self, operation_name: str, tags: Optional[Dict[str, Any]] = None, parent_span: Any = None) -> Iterator[Any]:
            parent = parent_span if isinstance(parent_span, WeftHaystackSpan) else _current.get()
            span = WeftHaystackSpan(self, operation_name, tags or {}, parent)
            if not operation_name.startswith("haystack.component"):
                span.materialize()  # pipelines/agents: inputs are in the start tags
            token = _current.set(span)
            err: Optional[BaseException] = None
            try:
                yield span
            except BaseException as e:
                err = e
                raise
            finally:
                try:
                    _current.reset(token)
                except ValueError:  # pragma: no cover
                    _current.set(parent)
                span.end(err)

        def current_span(self) -> Any:
            return _current.get()

    _CLS = WeftHaystackTracer
    return _CLS


def __getattr__(name: str) -> Any:
    if name == "WeftHaystackTracer":
        return _build()
    raise AttributeError(name)


def patch_haystack() -> bool:
    """Enable ``WeftHaystackTracer`` as Haystack's tracer (idempotent)."""
    global _registered, _previous
    if _registered is not None:
        return True
    try:
        tracing = importlib.import_module("haystack.tracing")
    except ImportError:
        return False
    enable = getattr(tracing, "enable_tracing", None)
    if enable is None:
        return False
    cur = getattr(getattr(tracing, "tracer", None), "actual_tracer", None)
    _previous = cur
    tracer = _build()()
    enable(tracer)
    proxy = getattr(tracing, "tracer", None)
    if proxy is not None and os.environ.get("HAYSTACK_CONTENT_TRACING_ENABLED", "").strip().lower() not in ("0", "false", "no", "off"):
        try:
            proxy.is_content_tracing_enabled = True
        except Exception:  # pragma: no cover
            pass
    _registered = tracer
    return True


def unpatch_haystack() -> None:
    global _registered
    if _registered is None:
        return
    try:
        tracing = importlib.import_module("haystack.tracing")
        if _previous is not None:
            tracing.enable_tracing(_previous)
        else:
            tracing.disable_tracing()
    except Exception:  # pragma: no cover
        pass
    _registered = None
