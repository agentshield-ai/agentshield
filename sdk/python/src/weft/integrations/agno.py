"""Agno integration.

Traced: ``Agent.run``/``arun`` (agent; display name = the agent's name),
``Team.run``/``arun`` (agent), ``Workflow.run``/``arun`` (workflow), model
``response``/``aresponse`` (llm; on Agno's ``Model`` base class) and tool
calls (``FunctionCall.execute``/``aexecute``; tool, display name = the
function name).

Agno's model classes call provider SDKs (``openai``, ``anthropic``, ...) that
weft traces with usage; the Agno model span adds usage from its own metrics
only when nothing was traced under it.
"""

from __future__ import annotations

from typing import Any, List

from .._usage import add_usage
from ._methods import Method, patch_methods

INTEGRATION = "agno"


def _self(call: Any) -> Any:
    return (call._raw_inputs or {}).get("self")


def _named(prefix: str) -> Any:
    def display(call: Any) -> str:
        name = getattr(_self(call), "name", None)
        return f"{prefix}: {name}" if isinstance(name, str) and name else prefix

    return display


def _tool_display(call: Any) -> str:
    fn = getattr(_self(call), "function", None)
    name = getattr(fn, "name", None)
    return f"Tool: {name}" if name else "Tool"


def model_usage(call: Any, res: Any) -> None:
    """Agno ``ModelResponse`` carries ``response_usage`` (input/output tokens) in recent releases."""
    u = getattr(res, "response_usage", None) or getattr(res, "usage", None)
    if u is None:
        return
    p = getattr(u, "input_tokens", None)
    c = getattr(u, "output_tokens", None)
    if isinstance(u, dict):
        p, c = u.get("input_tokens", u.get("prompt_tokens")), u.get("output_tokens", u.get("completion_tokens"))
    if p is None and c is None:
        return
    me = _self(call)
    add_usage(call, getattr(me, "id", None) or getattr(me, "name", None), p or 0, c or 0)


def _model_inputs(inputs: Any) -> Any:
    me = inputs.get("self")
    mid = getattr(me, "id", None)
    if isinstance(mid, str):
        inputs = dict(inputs)
        inputs["model"] = mid
    return inputs


def _run_output(res: Any) -> Any:
    content = getattr(res, "content", None)
    if content is None or isinstance(res, (dict, list, str)):
        return res
    out = {"content": content}
    for k in ("run_id", "session_id", "agent_id", "team_id", "model", "metrics", "status"):
        v = getattr(res, k, None)
        if v is not None:
            out[k] = v
    return out


def methods() -> List[Method]:
    return [
        Method("Agent.run", "agno.Agent.run", "agent", module="agno.agent", display=_named("Agent"), output=_run_output),
        Method("Agent.arun", "agno.Agent.arun", "agent", module="agno.agent", display=_named("Agent"), output=_run_output),
        Method("Team.run", "agno.Team.run", "agent", module="agno.team", display=_named("Team"), output=_run_output),
        Method("Team.arun", "agno.Team.arun", "agent", module="agno.team", display=_named("Team"), output=_run_output),
        Method("Workflow.run", "agno.Workflow.run", "workflow", module="agno.workflow", display=_named("Workflow"), output=_run_output),
        Method("Workflow.arun", "agno.Workflow.arun", "workflow", module="agno.workflow", display=_named("Workflow"), output=_run_output),
        Method("Model.response", "agno.Model.response", "llm", module="agno.models.base", usage=model_usage, inputs=_model_inputs),
        Method("Model.aresponse", "agno.Model.aresponse", "llm", module="agno.models.base", usage=model_usage, inputs=_model_inputs),
        Method("FunctionCall.execute", "agno.FunctionCall.execute", "tool", module="agno.tools.function", display=_tool_display),
        Method("FunctionCall.aexecute", "agno.FunctionCall.aexecute", "tool", module="agno.tools.function", display=_tool_display),
    ]


def patch_agno() -> bool:
    return patch_methods(INTEGRATION, None, methods())
