"""LangChain integration: ``WeftTracer`` callback handler.

``weft.init()`` registers it globally through LangChain's configure hook, so
every chain, chat model, LLM, tool and retriever run is traced (set
``WEFT_TRACE_LANGCHAIN=false`` to opt out). You can also pass it explicitly::

    from weft.integrations.langchain import WeftTracer
    chain.invoke(inputs, config={"callbacks": [WeftTracer()]})
"""

from __future__ import annotations

import importlib
import os
from typing import Any, Dict, List, Optional

from ._spans import RunTracker

_TRACER_CLS: Any = None
_registered = False
_tracer_var: Any = None


def _name(serialized: Optional[Dict[str, Any]], kwargs: Dict[str, Any], default: str) -> str:
    if kwargs.get("name"):
        return str(kwargs["name"])
    s = serialized or {}
    if s.get("name"):
        return str(s["name"])
    ids = s.get("id")
    if isinstance(ids, list) and ids:
        return str(ids[-1])
    return default


def _llm_usage(response: Any) -> Optional[Dict[str, Any]]:
    out = getattr(response, "llm_output", None) or {}
    tu = out.get("token_usage") or out.get("usage") or {}
    model = out.get("model_name") or out.get("model")
    if tu:
        return {
            "model": model,
            "prompt_tokens": tu.get("prompt_tokens", tu.get("input_tokens")),
            "completion_tokens": tu.get("completion_tokens", tu.get("output_tokens")),
            "total_tokens": tu.get("total_tokens"),
        }
    p = c = 0
    found = False
    for gens in getattr(response, "generations", None) or []:
        for g in gens:
            msg = getattr(g, "message", None)
            um = getattr(msg, "usage_metadata", None) if msg is not None else None
            if um:
                found = True
                p += um.get("input_tokens", 0) or 0
                c += um.get("output_tokens", 0) or 0
                meta = getattr(msg, "response_metadata", None) or {}
                model = model or meta.get("model_name") or meta.get("model")
    if found:
        return {"model": model, "prompt_tokens": p, "completion_tokens": c}
    return None


def _build() -> Any:
    from langchain_core.callbacks import BaseCallbackHandler  # type: ignore

    class WeftTracer(BaseCallbackHandler):  # type: ignore[misc, valid-type]
        """Records LangChain runs as weft calls."""

        run_inline = True  # keep callbacks in the run's context (parent tracking)
        raise_error = False

        def __init__(self) -> None:
            super().__init__()
            self.tracker = RunTracker()

        def _start(self, kind: str, prefix: str, serialized: Any, inputs: Any, run_id: Any, parent_run_id: Any, kwargs: Dict[str, Any]) -> None:
            name = _name(serialized, kwargs, kind)
            attrs: Dict[str, Any] = {"langchain": {"run_id": str(run_id), "tags": kwargs.get("tags") or [], "metadata": kwargs.get("metadata") or {}}}
            self.tracker.start(run_id, f"langchain.{prefix}.{name}", inputs if isinstance(inputs, dict) else {"input": inputs},
                               parent_id=parent_run_id, kind=kind, attributes=attrs)

        # chains
        def on_chain_start(self, serialized: Any, inputs: Any, *, run_id: Any, parent_run_id: Any = None, **kwargs: Any) -> None:
            self._start("chain", "Chain", serialized, inputs, run_id, parent_run_id, kwargs)

        def on_chain_end(self, outputs: Any, *, run_id: Any, **kwargs: Any) -> None:
            self.tracker.end(run_id, outputs)

        def on_chain_error(self, error: BaseException, *, run_id: Any, **kwargs: Any) -> None:
            self.tracker.end(run_id, error=error)

        # models
        def on_chat_model_start(self, serialized: Any, messages: List[Any], *, run_id: Any, parent_run_id: Any = None, **kwargs: Any) -> None:
            inputs = {"messages": messages, "invocation_params": kwargs.get("invocation_params") or {}}
            self._start("llm", "ChatModel", serialized, inputs, run_id, parent_run_id, kwargs)

        def on_llm_start(self, serialized: Any, prompts: List[str], *, run_id: Any, parent_run_id: Any = None, **kwargs: Any) -> None:
            inputs = {"prompts": prompts, "invocation_params": kwargs.get("invocation_params") or {}}
            self._start("llm", "LLM", serialized, inputs, run_id, parent_run_id, kwargs)

        def on_llm_end(self, response: Any, *, run_id: Any, **kwargs: Any) -> None:
            usage = _llm_usage(response)
            call = self.tracker.get(run_id)
            if usage is not None and not usage.get("model") and call is not None:
                ip = (call._raw_inputs or {}).get("invocation_params") or {}
                usage["model"] = ip.get("model_name") or ip.get("model")
            self.tracker.end(run_id, response, usage=usage)

        def on_llm_error(self, error: BaseException, *, run_id: Any, **kwargs: Any) -> None:
            self.tracker.end(run_id, error=error)

        # tools
        def on_tool_start(self, serialized: Any, input_str: str, *, run_id: Any, parent_run_id: Any = None, inputs: Any = None, **kwargs: Any) -> None:
            self._start("tool", "Tool", serialized, inputs if isinstance(inputs, dict) else {"input": input_str}, run_id, parent_run_id, kwargs)

        def on_tool_end(self, output: Any, *, run_id: Any, **kwargs: Any) -> None:
            self.tracker.end(run_id, output)

        def on_tool_error(self, error: BaseException, *, run_id: Any, **kwargs: Any) -> None:
            self.tracker.end(run_id, error=error)

        # retrievers
        def on_retriever_start(self, serialized: Any, query: str, *, run_id: Any, parent_run_id: Any = None, **kwargs: Any) -> None:
            self._start("retriever", "Retriever", serialized, {"query": query}, run_id, parent_run_id, kwargs)

        def on_retriever_end(self, documents: Any, *, run_id: Any, **kwargs: Any) -> None:
            self.tracker.end(run_id, documents)

        def on_retriever_error(self, error: BaseException, *, run_id: Any, **kwargs: Any) -> None:
            self.tracker.end(run_id, error=error)

    return WeftTracer


def __getattr__(name: str) -> Any:
    global _TRACER_CLS
    if name == "WeftTracer":
        if _TRACER_CLS is None:
            try:
                _TRACER_CLS = _build()
            except ImportError:
                raise ImportError("weft.integrations.langchain.WeftTracer requires langchain-core") from None
        return _TRACER_CLS
    raise AttributeError(name)


def patch_langchain() -> bool:
    """Register WeftTracer for all LangChain runs (via the configure hook)."""
    global _registered, _tracer_var, _TRACER_CLS
    if _registered:
        return True
    try:
        importlib.import_module("langchain_core.callbacks")
        ctx = importlib.import_module("langchain_core.tracers.context")
    except ImportError:
        return False
    hook = getattr(ctx, "register_configure_hook", None)
    if hook is None:
        return False
    import contextvars

    if _TRACER_CLS is None:
        _TRACER_CLS = _build()
    _tracer_var = contextvars.ContextVar("weft_langchain_tracer", default=None)
    os.environ.setdefault("WEFT_TRACE_LANGCHAIN", "true")
    hook(_tracer_var, True, _TRACER_CLS, "WEFT_TRACE_LANGCHAIN")
    _registered = True
    return True
