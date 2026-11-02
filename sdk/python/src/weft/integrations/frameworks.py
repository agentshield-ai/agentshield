"""Smaller framework integrations built from method tables.

* **Google ADK** (``google.adk``): ``Runner.run``/``run_async`` (agent),
  ``BaseAgent.run_async`` and subclasses (agent, display name = the agent's
  name), tools' ``run_async`` (tool) and ``BaseLlm.generate_content_async``
  implementations (llm; the google-genai call underneath carries usage).
* **Pydantic AI** (``pydantic_ai``): ``Agent.run`` (agent; ``run_sync`` runs
  it), function tool calls (tool) and ``Model.request`` on every model
  class (llm; usage from ``ModelResponse.usage`` when no provider SDK call
  was traced underneath).
* **Not Diamond** (``notdiamond``): ``model_select``/``amodel_select`` (chain).
* **verifiers**: ``Environment.evaluate``/``generate``/``a_generate``
  (chain), ``MultiTurnEnv.rollout`` (agent), ``Rubric.score_rollouts`` (scorer).
* **GEPA** (``gepa``): ``optimize`` (chain).
"""

from __future__ import annotations

import importlib
from typing import Any, List

from .._usage import add_usage
from ._common import patch_attr
from ._methods import Method, make_op, patch_methods


def _self(call: Any) -> Any:
    return (call._raw_inputs or {}).get("self")


def _named(prefix: str) -> Any:
    def display(call: Any) -> str:
        name = getattr(_self(call), "name", None)
        return f"{prefix}: {name}" if isinstance(name, str) and name else prefix

    return display


def _arg_named(prefix: str, *keys: str) -> Any:
    def display(call: Any) -> str:
        raw = call._raw_inputs or {}
        for k in keys:
            v = raw.get(k)
            name = v if isinstance(v, str) else getattr(v, "name", None) or getattr(v, "tool_name", None)
            if isinstance(name, str) and name:
                return f"{prefix}: {name}"
        return prefix

    return display


# ---------------------------------------------------------------- Google ADK
def patch_google_adk() -> bool:
    return patch_methods("google_adk", None, [
        Method("Runner.run_async", "google_adk.Runner.run", "agent", module="google.adk.runners"),
        Method("Runner.run", "google_adk.Runner.run", "agent", module="google.adk.runners"),
        Method("BaseAgent.run_async", "google_adk.Agent.run", "agent", module="google.adk.agents.base_agent",
               display=_named("Agent"), subclasses=True),
        Method("BaseTool.run_async", "google_adk.Tool.run", "tool", module="google.adk.tools.base_tool",
               display=_named("Tool"), subclasses=True),
        Method("BaseLlm.generate_content_async", "google_adk.LLM.generate_content", "llm", module="google.adk.models.base_llm",
               subclasses=True),
    ])


# ---------------------------------------------------------------- Pydantic AI
def pydantic_ai_usage(call: Any, res: Any) -> None:
    u = getattr(res, "usage", None)
    if u is None:
        return
    p = getattr(u, "input_tokens", None)
    if p is None:
        p = getattr(u, "request_tokens", None)
    c = getattr(u, "output_tokens", None)
    if c is None:
        c = getattr(u, "response_tokens", None)
    if p is None and c is None:
        return
    add_usage(call, getattr(res, "model_name", None) or getattr(_self(call), "model_name", None), p or 0, c or 0)


def _run_output(res: Any) -> Any:
    out = getattr(res, "output", None)
    if out is None:
        out = getattr(res, "data", None)
    if out is None:
        return res
    d = {"output": out}
    u = getattr(res, "usage", None)
    try:
        d["usage"] = u() if callable(u) else u
    except Exception:
        pass
    return d


def patch_pydantic_ai() -> bool:
    return patch_methods("pydantic_ai", None, [
        Method("Agent.run", "pydantic_ai.Agent.run", "agent", module="pydantic_ai", display=_named("Agent"), output=_run_output),
        Method("FunctionToolset.call_tool", "pydantic_ai.Tool.call", "tool", module="pydantic_ai.toolsets.function",
               display=_arg_named("Tool", "name")),
        Method("Tool.run", "pydantic_ai.Tool.call", "tool", module="pydantic_ai.tools", display=_named("Tool")),
        Method("Model.request", "pydantic_ai.Model.request", "llm", module="pydantic_ai.models", subclasses=True, usage=pydantic_ai_usage),
    ])


# ---------------------------------------------------------------- Not Diamond
def patch_notdiamond() -> bool:
    return patch_methods("notdiamond", "notdiamond", [
        Method("NotDiamond.model_select", "notdiamond.NotDiamond.model_select", "chain"),
        Method("NotDiamond.amodel_select", "notdiamond.NotDiamond.amodel_select", "chain"),
    ])


# ---------------------------------------------------------------- verifiers
def patch_verifiers() -> bool:
    return patch_methods("verifiers", None, [
        Method("Environment.evaluate", "verifiers.Environment.evaluate", "chain", module="verifiers.envs.environment", subclasses=True),
        Method("Environment.generate", "verifiers.Environment.generate", "chain", module="verifiers.envs.environment"),
        Method("Environment.a_generate", "verifiers.Environment.a_generate", "chain", module="verifiers.envs.environment"),
        Method("MultiTurnEnv.rollout", "verifiers.MultiTurnEnv.rollout", "agent", module="verifiers.envs.multiturn_env", subclasses=True),
        Method("Rubric.score_rollouts", "verifiers.Rubric.score_rollouts", "scorer", module="verifiers.rubrics.rubric", subclasses=True),
    ])


# ---------------------------------------------------------------- GEPA
def patch_gepa() -> bool:
    patched = False
    m = Method("optimize", "gepa.optimize", "chain")
    for modname in ("gepa.api", "gepa"):
        try:
            mod = importlib.import_module(modname)
        except ImportError:
            continue
        if callable(getattr(mod, "optimize", None)):
            patched |= patch_attr(mod, "optimize", lambda orig: make_op("gepa", orig, m))
    return patched


FRAMEWORK_PATCHERS: List[Any] = [patch_google_adk, patch_pydantic_ai, patch_notdiamond, patch_verifiers, patch_gepa]
