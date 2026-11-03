"""OpenAI Agents SDK integration: a tracing processor.

``weft.init()`` adds ``WeftTracingProcessor`` with ``agents.add_trace_processor``
when the ``openai-agents`` package is installed. Traces become root calls
(workflow); agent, function (tool), generation/response (LLM), handoff,
guardrail and custom spans become nested calls with the matching span kind.

* The session (``gen_ai.conversation.id``) is the run's ``group_id``
  (``RunConfig(group_id=...)``, e.g. a chat thread id), else the trace id.
* Agent spans are ``invoke_agent`` calls with ``gen_ai.agent.name``, so an
  agent run inside another agent's tool (``agent.as_tool()``) is a delegation.
* Hand-off spans carry ``weft.handoff.from_agent`` / ``to_agent`` (the target
  is known when the span ends), which the server records as a hand-off to the
  next run of that agent.
* Response spans keep the Responses output, so reasoning items (summaries)
  and ``reasoning_tokens`` are recorded; when the ``openai`` integration
  traced the request underneath, the reasoning stays on that call only.
"""

from __future__ import annotations

import importlib
from typing import Any, Dict, Optional, Tuple

from ._spans import RunTracker, as_exception

INTEGRATION = "openai_agents"

_registered = False


def _span_attrs(data: Any) -> Dict[str, Any]:
    """Attributes known when a span starts."""
    t = getattr(data, "type", "custom")
    name = getattr(data, "name", None)
    if t == "agent" and name:
        return {"gen_ai": {"operation": {"name": "invoke_agent"}, "agent": {"name": name}}}
    if t == "function" and name:
        return {"gen_ai": {"operation": {"name": "execute_tool"}, "tool": {"name": name}}}
    if t == "handoff":
        h: Dict[str, Any] = {"kind": "handoff"}
        if getattr(data, "from_agent", None):
            h["from_agent"] = data.from_agent
        if getattr(data, "to_agent", None):
            h["to_agent"] = data.to_agent
        return {"weft": {"handoff": h}}
    return {}


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
        return "openai_agents.handoff", "handoff", {"from_agent": getattr(data, "from_agent", None), "to_agent": getattr(data, "to_agent", None)}
    if t == "guardrail":
        return f"guardrail.{name or 'guardrail'}", "guardrail", {}
    if t == "mcp_tools":
        return "openai_agents.mcp_list_tools", "tool", {"server": getattr(data, "server", None)}
    # Runner bookkeeping (openai-agents >= 0.2x): a run is a task span, each model
    # round trip of an agent a turn span. Neither is an agent of its own.
    if t == "task":
        return "openai_agents.task", "chain", {"name": name}
    if t == "turn":
        return "openai_agents.turn", "chain", {"turn": getattr(data, "turn", None), "agent_name": getattr(data, "agent_name", None)}
    return f"openai_agents.{name or t}", t, {"data": getattr(data, "data", None)}


def _display(data: Any) -> Optional[str]:
    t = getattr(data, "type", None)
    if t == "handoff" and getattr(data, "to_agent", None):
        return f"handoff to {data.to_agent}"
    if t == "task" and getattr(data, "name", None):
        return str(data.name)
    if t == "turn" and getattr(data, "turn", None) is not None:
        return f"{getattr(data, 'agent_name', None) or 'agent'} turn {data.turn}"
    return None  # a hand-off's target is set later; it is named when the span ends


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
            usage = {"model": getattr(resp, "model", None), "prompt_tokens": getattr(u, "input_tokens", None), "completion_tokens": getattr(u, "output_tokens", None),
                     "reasoning_tokens": getattr(getattr(u, "output_tokens_details", None), "reasoning_tokens", None)}
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
            trace_id = getattr(trace, "trace_id", None)
            group_id = getattr(trace, "group_id", None)
            attrs: Dict[str, Any] = {"openai_agents": {"trace_id": trace_id, "group_id": group_id}}
            session = group_id or trace_id
            if session:
                attrs["gen_ai"] = {"conversation": {"id": str(session)}}
            self.tracker.start(("trace", trace_id or id(trace)), "openai_agents.trace", {"name": name},
                               kind="workflow", display_name=name, attributes=attrs)

        def on_trace_end(self, trace: Any) -> None:
            self.tracker.end(("trace", getattr(trace, "trace_id", None) or id(trace)))

        def on_span_start(self, span: Any) -> None:
            data = getattr(span, "span_data", None)
            op_name, kind, inputs = _span_info(data)
            parent = getattr(span, "parent_id", None)
            parent_key = ("span", parent) if parent else ("trace", getattr(span, "trace_id", None))
            self.tracker.start(("span", getattr(span, "span_id", id(span))), op_name, inputs, parent_id=parent_key, kind=kind,
                               attributes=_span_attrs(data), display_name=_display(data))

        def on_span_end(self, span: Any) -> None:
            data = getattr(span, "span_data", None)
            out, usage = _span_output(data)
            handoff = getattr(data, "type", None) == "handoff"
            self.tracker.end(("span", getattr(span, "span_id", id(span))), out, error=as_exception(getattr(span, "error", None)), usage=usage,
                             attributes=_span_attrs(data) if handoff else None, display_name=_display(data) if handoff else None)

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
