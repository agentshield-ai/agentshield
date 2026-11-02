"""OpenAI Agents SDK integration: a tracing processor.

``weft.init()`` adds ``WeftTracingProcessor`` with ``agents.add_trace_processor``
when the ``openai-agents`` package is installed. Traces become root calls;
agent, function (tool), generation/response (LLM), handoff, guardrail and
custom spans become nested calls with the matching span kind.
"""

from __future__ import annotations

import importlib
from typing import Any, Dict, Optional, Tuple

from ._spans import RunTracker, as_exception

INTEGRATION = "openai_agents"

_registered = False


def _span_info(data: Any) -> Tuple[str, str, Dict[str, Any]]:
    t = getattr(data, "type", "custom")
    name = getattr(data, "name", None)
    if t == "agent":
        return f"agent.{name or 'agent'}", "agent", {"tools": getattr(data, "tools", None), "handoffs": getattr(data, "handoffs", None), "output_type": getattr(data, "output_type", None)}
    if t == "function":
        return f"tool.{name or 'function'}", "tool", {"input": getattr(data, "input", None)}
    if t == "generation":
        return "openai_agents.generation", "llm", {"input": getattr(data, "input", None), "model": getattr(data, "model", None), "model_config": getattr(data, "model_config", None)}
    if t == "response":
        return "openai_agents.response", "llm", {"input": getattr(data, "input", None)}
    if t == "handoff":
        return "openai_agents.handoff", "agent", {"from_agent": getattr(data, "from_agent", None), "to_agent": getattr(data, "to_agent", None)}
    if t == "guardrail":
        return f"guardrail.{name or 'guardrail'}", "guardrail", {}
    if t == "mcp_tools":
        return "openai_agents.mcp_list_tools", "tool", {"server": getattr(data, "server", None)}
    return f"openai_agents.{name or t}", t, {"data": getattr(data, "data", None)}


def _span_output(data: Any) -> Tuple[Any, Optional[Dict[str, Any]]]:
    t = getattr(data, "type", "custom")
    if t == "function":
        return getattr(data, "output", None), None
    if t == "generation":
        u = getattr(data, "usage", None) or {}
        usage = {"model": getattr(data, "model", None), "prompt_tokens": u.get("input_tokens", u.get("prompt_tokens")), "completion_tokens": u.get("output_tokens", u.get("completion_tokens"))}
        return getattr(data, "output", None), usage
    if t == "response":
        resp = getattr(data, "response", None)
        u = getattr(resp, "usage", None)
        usage = None
        if u is not None:
            usage = {"model": getattr(resp, "model", None), "prompt_tokens": getattr(u, "input_tokens", None), "completion_tokens": getattr(u, "output_tokens", None)}
        return resp, usage
    if t == "guardrail":
        return {"triggered": getattr(data, "triggered", None)}, None
    if t == "agent":
        return None, None
    try:
        return data.export(), None
    except Exception:
        return None, None


def _base() -> Any:
    try:
        mod = importlib.import_module("agents.tracing")
        return getattr(mod, "TracingProcessor", object)
    except ImportError:
        return object


_CLS: Any = None


def _build() -> Any:
    global _CLS
    if _CLS is not None:
        return _CLS

    class WeftTracingProcessor(_base()):  # type: ignore[misc]
        """Maps OpenAI Agents SDK traces and spans to weft calls."""

        def __init__(self) -> None:
            self.tracker = RunTracker(INTEGRATION)

        def on_trace_start(self, trace: Any) -> None:
            name = getattr(trace, "name", None) or "agent_run"
            attrs = {"openai_agents": {"trace_id": getattr(trace, "trace_id", None), "group_id": getattr(trace, "group_id", None)}}
            self.tracker.start(("trace", getattr(trace, "trace_id", id(trace))), "openai_agents.trace", {"name": name},
                               kind="agent", display_name=name, attributes=attrs)

        def on_trace_end(self, trace: Any) -> None:
            self.tracker.end(("trace", getattr(trace, "trace_id", id(trace))))

        def on_span_start(self, span: Any) -> None:
            data = getattr(span, "span_data", None)
            op_name, kind, inputs = _span_info(data)
            parent = getattr(span, "parent_id", None)
            parent_key = ("span", parent) if parent else ("trace", getattr(span, "trace_id", None))
            self.tracker.start(("span", getattr(span, "span_id", id(span))), op_name, inputs, parent_id=parent_key, kind=kind)

        def on_span_end(self, span: Any) -> None:
            out, usage = _span_output(getattr(span, "span_data", None))
            self.tracker.end(("span", getattr(span, "span_id", id(span))), out, error=as_exception(getattr(span, "error", None)), usage=usage)

        def shutdown(self) -> None:
            from .._client import get_client

            c = get_client()
            if c is not None:
                c.flush(timeout=10)

        def force_flush(self) -> None:
            self.shutdown()


    _CLS = WeftTracingProcessor
    return _CLS


def __getattr__(name: str) -> Any:
    if name == "WeftTracingProcessor":
        return _build()
    raise AttributeError(name)


def patch_openai_agents() -> bool:
    global _registered
    if _registered:
        return True
    try:
        mod = importlib.import_module("agents")
        importlib.import_module("agents.tracing")
    except ImportError:
        return False
    add = getattr(mod, "add_trace_processor", None)
    if add is None:
        return False
    add(_build()())
    _registered = True
    return True
