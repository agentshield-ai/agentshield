"""CrewAI integration.

Traced: ``Crew.kickoff``/``kickoff_async``/``kickoff_for_each`` (workflow;
a crew with its own ``name`` is the team, ``weft.team.id``, and each crew
instance is a session, ``gen_ai.conversation.id`` = ``crew.id``),
``Agent.execute_task`` (agent, ``gen_ai.agent.name`` = the agent's role),
``Task.execute_sync``/``execute_async`` (chain, display name = the task's
name or description), ``LLM.call`` (llm), tool runs (``BaseTool.run`` and
subclasses) and ``Flow.kickoff``/``kickoff_async`` (workflow).

Delegation (``allow_delegation=True``) runs the ``Delegate work to coworker``
and ``Ask question to coworker`` tools, recorded with ``gen_ai.tool.name`` and
their ``coworker`` argument, which the server reads as a hand-off to that
agent (DESIGN.md §2.1).

``LLM.call`` goes through LiteLLM (or a provider SDK), which the matching
integration traces with usage nested under it; the ``LLM.call`` span itself
records no usage, so tokens are counted once.
"""

from __future__ import annotations

from typing import Any, Dict

from ._methods import Method, _merge_attrs, agent_attrs, patch_methods, self_arg, session_attrs, team_attrs, tool_attrs

INTEGRATION = "crewai"


def _self(call: Any) -> Any:
    return (call._raw_inputs or {}).get("self")


def _agent_display(call: Any) -> str:
    a = _self(call)
    role = getattr(a, "role", None)
    return f"Agent: {role}" if role else "crewai.Agent.execute_task"


def _task_display(call: Any) -> str:
    t = _self(call)
    name = getattr(t, "name", None) or getattr(t, "description", None)
    if isinstance(name, str) and name:
        name = name.strip().splitlines()[0]
        return "Task: " + (name[:60] + "…" if len(name) > 60 else name)
    return "crewai.Task.execute_sync"


def _tool_display(call: Any) -> str:
    t = _self(call)
    name = getattr(t, "name", None)
    return f"Tool: {name}" if name else "crewai.Tool.run"


def _llm_inputs(inputs: Dict[str, Any]) -> Dict[str, Any]:
    llm = inputs.get("self")
    model = getattr(llm, "model", None) if llm is not None else None
    if isinstance(model, str):
        inputs = dict(inputs)
        inputs["model"] = model
    return inputs


def _task_output(res: Any) -> Any:
    raw = getattr(res, "raw", None)
    if raw is not None:
        return {"raw": raw, "agent": getattr(res, "agent", None), "description": getattr(res, "description", None)}
    return res


def _crew_output(res: Any) -> Any:
    raw = getattr(res, "raw", None)
    if raw is None:
        return res
    out: Dict[str, Any] = {"raw": raw}
    usage = getattr(res, "token_usage", None)
    if usage is not None:
        try:
            out["token_usage"] = usage.model_dump() if hasattr(usage, "model_dump") else dict(vars(usage))
        except Exception:
            pass
    tasks = getattr(res, "tasks_output", None)
    if tasks:
        out["tasks_output"] = [_task_output(t) for t in tasks]
    return out


def _crew_attrs(args: Any, kwargs: Dict[str, Any]) -> Dict[str, Any]:
    crew = self_arg(args)
    out: Dict[str, Any] = {}
    name = getattr(crew, "name", None)
    if isinstance(name, str) and name and name != "crew":  # "crew" is CrewAI's default name
        out = _merge_attrs(out, team_attrs(name, name))
    cid = getattr(crew, "id", None)
    if cid is not None:
        out = _merge_attrs(out, session_attrs(str(cid)))
    return out


def _agent_attrs(args: Any, kwargs: Dict[str, Any]) -> Dict[str, Any]:
    role = getattr(self_arg(args), "role", None)
    return agent_attrs(role.strip() if isinstance(role, str) else None)


def _tool_attrs(args: Any, kwargs: Dict[str, Any]) -> Dict[str, Any]:
    return tool_attrs(getattr(self_arg(args), "name", None))


def methods() -> list:
    return [
        Method("Crew.kickoff", "crewai.Crew.kickoff", "workflow", output=_crew_output, attrs=_crew_attrs),
        Method("Crew.kickoff_async", "crewai.Crew.kickoff_async", "workflow", output=_crew_output, attrs=_crew_attrs),
        Method("Crew.kickoff_for_each", "crewai.Crew.kickoff_for_each", "workflow", attrs=_crew_attrs),
        Method("Crew.kickoff_for_each_async", "crewai.Crew.kickoff_for_each_async", "workflow", attrs=_crew_attrs),
        Method("Agent.execute_task", "crewai.Agent.execute_task", "agent", display=_agent_display, attrs=_agent_attrs),
        Method("Task.execute_sync", "crewai.Task.execute_sync", "chain", display=_task_display, output=_task_output),
        Method("Task.execute_async", "crewai.Task.execute_async", "chain", display=_task_display),
        Method("LLM.call", "crewai.LLM.call", "llm", inputs=_llm_inputs),
        Method("BaseTool.run", "crewai.Tool.run", "tool", module="crewai.tools", display=_tool_display, subclasses=True, attrs=_tool_attrs),
        Method("Flow.kickoff", "crewai.Flow.kickoff", "workflow", module="crewai.flow.flow"),
        Method("Flow.kickoff_async", "crewai.Flow.kickoff_async", "workflow", module="crewai.flow.flow"),
    ]


def patch_crewai() -> bool:
    return patch_methods(INTEGRATION, "crewai", methods())

