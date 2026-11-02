"""LlamaIndex integration: a span handler on LlamaIndex's instrumentation dispatcher.

``weft.init()`` adds ``WeftSpanHandler`` to the root dispatcher of
``llama_index.core.instrumentation`` (LlamaIndex >= 0.10.20). Every
instrumented method (query engines, retrievers, LLM ``chat``/``complete``,
embeddings, agents, workflows, tools ...) becomes a call nested by LlamaIndex's
span parentage. Span kinds: ``llm``, ``embedding``, ``retriever``, ``reranker``,
``tool``, ``agent``, ``workflow`` or ``chain``.

LLM usage is read from the response (``raw.usage`` or ``additional_kwargs``)
only when no provider SDK call (e.g. the patched OpenAI client) was traced
underneath, so tokens are not counted twice.
"""

from __future__ import annotations

import importlib
import inspect
from typing import Any, Dict, Optional, Tuple

from ._common import to_dict
from ._spans import RunTracker

INTEGRATION = "llamaindex"

_registered: Any = None
_CLS: Any = None
_TRACKER = RunTracker(INTEGRATION)


def _span_parts(id_: str) -> Tuple[str, str]:
    """``"OpenAI.chat-6f1c..."`` -> ("OpenAI", "chat")."""
    base = id_.split("-", 1)[0] if "-" in id_ else id_
    cls, _, meth = base.rpartition(".")
    return cls or base, meth or base


def span_kind(cls_name: str, method: str, instance: Any = None) -> str:
    c, m = cls_name.lower(), method.lower()
    if "embedding" in c or "embed" in m:
        return "embedding"
    if "rerank" in c or "postprocess" in m:
        return "reranker"
    if "retriev" in c or m in ("retrieve", "aretrieve", "_retrieve", "_aretrieve"):
        return "retriever"
    if "tool" in c or m in ("call", "acall") and "tool" in c:
        return "tool"
    if "agent" in c or "worker" in c:
        return "agent"
    if "workflow" in c or m in ("run", "_run_workflow"):
        return "workflow" if "workflow" in c else ("agent" if "agent" in c else "chain")
    if any(x in m for x in ("chat", "complete", "predict", "structured_predict")) and _is_llm(instance, c):
        return "llm"
    return "chain"


def _is_llm(instance: Any, cls_lower: str) -> bool:
    if instance is not None:
        for b in type(instance).__mro__:
            if b.__name__ in ("LLM", "BaseLLM", "FunctionCallingLLM", "CustomLLM"):
                return True
        return False
    return "llm" in cls_lower or cls_lower in ("openai", "anthropic", "gemini", "ollama", "mistralai", "bedrock", "groq")


def _bind(instance: Any, bound_args: Any) -> Dict[str, Any]:
    try:
        args = dict(bound_args.arguments)
    except Exception:
        return {}
    out: Dict[str, Any] = {}
    for k, v in args.items():
        if k == "self":
            continue
        if k == "kwargs" and isinstance(v, dict):
            out.update(v)
        else:
            out[k] = v
    return out


def _model_of(instance: Any) -> Optional[str]:
    for path in (("metadata", "model_name"), ("model",), ("model_name",)):
        obj = instance
        try:
            for p in path:
                obj = getattr(obj, p)
        except Exception:
            continue
        if isinstance(obj, str) and obj:
            return obj
    return None


def llm_usage(result: Any, instance: Any) -> Optional[Dict[str, Any]]:
    """Usage from a ChatResponse/CompletionResponse (``raw`` provider payload or ``additional_kwargs``)."""
    raw = getattr(result, "raw", None)
    raw = to_dict(raw) if raw is not None else None
    model = None
    if isinstance(raw, dict):
        model = raw.get("model")
        u = raw.get("usage") or raw.get("usage_metadata")
        if isinstance(u, dict) or (u is not None and not isinstance(u, dict)):
            u = to_dict(u)
        if isinstance(u, dict):
            p = u.get("prompt_tokens", u.get("input_tokens", u.get("prompt_token_count")))
            c = u.get("completion_tokens", u.get("output_tokens", u.get("candidates_token_count")))
            if p is not None or c is not None:
                return {"model": model or _model_of(instance), "prompt_tokens": p or 0, "completion_tokens": c or 0, "total_tokens": u.get("total_tokens")}
    ak = getattr(result, "additional_kwargs", None) or {}
    if isinstance(ak, dict) and ("prompt_tokens" in ak or "completion_tokens" in ak):
        return {"model": model or _model_of(instance), "prompt_tokens": ak.get("prompt_tokens") or 0, "completion_tokens": ak.get("completion_tokens") or 0, "total_tokens": ak.get("total_tokens")}
    return None


def _output(result: Any) -> Any:
    if result is None:
        return None
    if type(result).__name__ == "StopEvent":
        return getattr(result, "result", None)
    if inspect.isgenerator(result) or inspect.isasyncgen(result):
        return {"stream": type(result).__name__}
    return result


def _base() -> Any:
    try:
        return getattr(importlib.import_module("llama_index.core.instrumentation.span_handlers.base"), "BaseSpanHandler")
    except (ImportError, AttributeError):
        return object


def _build() -> Any:
    global _CLS
    if _CLS is not None:
        return _CLS
    base = _base()

    class WeftSpanHandler(base):  # type: ignore[misc, valid-type]
        """LlamaIndex span handler that records spans as weft calls."""

        @classmethod
        def class_name(cls) -> str:
            return "WeftSpanHandler"

        # BaseSpanHandler is a pydantic model: keep the tracker at module level.
        @property
        def _tracker(self) -> RunTracker:
            return _TRACKER

        def new_span(self, id_: str, bound_args: Any, instance: Any = None, parent_span_id: Optional[str] = None,
                     tags: Optional[Dict[str, Any]] = None, **kwargs: Any) -> Any:
            cls_name, meth = _span_parts(id_)
            kind = span_kind(cls_name, meth, instance)
            inputs = _bind(instance, bound_args)
            if kind == "llm" and instance is not None:
                m = _model_of(instance)
                if m:
                    inputs.setdefault("model", m)
            attrs: Dict[str, Any] = {"llama_index": {"span_id": id_}}
            if tags:
                attrs["llama_index"]["tags"] = tags
            self._tracker.start(id_, f"llama_index.{cls_name}.{meth}", inputs, parent_id=parent_span_id, kind=kind, attributes=attrs)
            return None

        def prepare_to_exit_span(self, id_: str, bound_args: Any, instance: Any = None, result: Any = None, **kwargs: Any) -> Any:
            usage = None
            call = self._tracker.get(id_)
            if call is not None and (call.attributes.get("weft") or {}).get("span_kind") == "llm":
                usage = llm_usage(result, instance)
            self._tracker.end(id_, _output(result), usage=usage)
            return None

        def prepare_to_drop_span(self, id_: str, bound_args: Any, instance: Any = None, err: Optional[BaseException] = None, **kwargs: Any) -> Any:
            if err is not None and type(err).__name__ == "WorkflowDone":
                err = None
            self._tracker.end(id_, None, error=err)
            return None

    _CLS = WeftSpanHandler
    return _CLS


def __getattr__(name: str) -> Any:
    if name == "WeftSpanHandler":
        return _build()
    raise AttributeError(name)


def patch_llamaindex() -> bool:
    """Add ``WeftSpanHandler`` to LlamaIndex's root dispatcher (idempotent)."""
    global _registered
    if _registered is not None:
        return True
    try:
        instrument = importlib.import_module("llama_index.core.instrumentation")
    except ImportError:
        return False
    get_dispatcher = getattr(instrument, "get_dispatcher", None)
    if get_dispatcher is None:
        return False
    dispatcher = get_dispatcher()
    cls = _build()
    if any(isinstance(h, cls) for h in getattr(dispatcher, "span_handlers", []) or []):
        _registered = dispatcher
        return True
    handler = cls()
    dispatcher.add_span_handler(handler)
    _registered = dispatcher
    return True


def unpatch_llamaindex() -> None:
    global _registered
    d = _registered
    _registered = None
    if d is None or _CLS is None:
        return
    try:
        d.span_handlers = [h for h in d.span_handlers if not isinstance(h, _CLS)]
    except Exception:  # pragma: no cover
        pass
