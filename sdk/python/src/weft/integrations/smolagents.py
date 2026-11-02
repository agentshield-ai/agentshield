"""smolagents integration.

Traced: agent ``run`` (agent; ``MultiStepAgent`` and its subclasses such as
``CodeAgent`` and ``ToolCallingAgent``), each agent ``step`` (chain), model
``generate``/``__call__`` on every ``Model`` subclass (llm; usage from the
returned ``ChatMessage.token_usage``) and ``Tool.__call__`` (tool; display
name = the tool's name).

Models backed by a traced client (``InferenceClientModel`` via
huggingface_hub, ``LiteLLMModel``, ``OpenAIServerModel``) get usage from the
nested provider call; the smolagents model span records usage only when
nothing was traced under it (e.g. ``TransformersModel``).
"""

from __future__ import annotations

import importlib
from typing import Any, Dict, List

from .._usage import add_usage
from ._common import patch_attr
from ._methods import Method, patch_methods

INTEGRATION = "smolagents"


def _self(call: Any) -> Any:
    return (call._raw_inputs or {}).get("self")


def _tool_display(call: Any) -> str:
    name = getattr(_self(call), "name", None)
    return f"Tool: {name}" if name else "smolagents.Tool"


def _agent_display(call: Any) -> str:
    a = _self(call)
    name = getattr(a, "name", None)
    return f"{type(a).__name__}: {name}" if name else type(a).__name__


def model_usage(call: Any, res: Any) -> None:
    tu = getattr(res, "token_usage", None)
    model = None
    raw = getattr(res, "raw", None)
    if isinstance(raw, dict):
        model = raw.get("model")
    model = model or (call.inputs or {}).get("model")
    if tu is not None:
        add_usage(call, model, getattr(tu, "input_tokens", 0), getattr(tu, "output_tokens", 0))
        return
    me = (call._raw_inputs or {}).get("self")
    p, c = getattr(me, "last_input_token_count", None), getattr(me, "last_output_token_count", None)
    if p or c:
        add_usage(call, model, p or 0, c or 0)


def _model_inputs(inputs: Dict[str, Any]) -> Dict[str, Any]:
    me = inputs.get("self")
    mid = getattr(me, "model_id", None)
    if isinstance(mid, str):
        inputs = dict(inputs)
        inputs["model"] = mid
    return inputs


def _model_methods(mod: Any) -> List[Method]:
    base = getattr(mod, "Model", None)
    out: List[Method] = []
    classes = [base] if isinstance(base, type) else []
    if isinstance(base, type):
        classes += [c for c in base.__subclasses__()]
        # grandchildren (e.g. AzureOpenAIServerModel)
        for c in list(classes[1:]):
            classes += c.__subclasses__()
    seen = set()
    for cls in classes:
        if cls in seen:
            continue
        seen.add(cls)
        # Newer releases route ``__call__`` to ``generate``; patch one of them per class.
        if "generate" in cls.__dict__:
            meth = "generate"
        elif "__call__" in cls.__dict__ and not hasattr(cls, "generate"):
            meth = "__call__"
        else:
            continue
        out.append(Method(f"{cls.__name__}.{meth}", f"smolagents.{cls.__name__}.{meth.strip('_')}", "llm",
                          module=cls.__module__, usage=model_usage, inputs=_model_inputs,
                          # user-defined Model subclasses (created after weft.init) are traced too
                          subclasses=cls is base))
    # the base class last, so the library's own model classes keep their own op names
    out.sort(key=lambda m: m.subclasses)
    return out


def methods(mod: Any) -> List[Method]:
    ms: List[Method] = []
    agent = getattr(mod, "MultiStepAgent", None)
    if isinstance(agent, type):
        ms.append(Method("MultiStepAgent.run", "smolagents.Agent.run", "agent", display=_agent_display, subclasses=True))
        # recent releases run each step through the ``_step_stream`` generator; older ones through ``step``
        step = "_step_stream" if hasattr(agent, "_step_stream") else "step"
        ms.append(Method(f"MultiStepAgent.{step}", "smolagents.Agent.step", "chain", subclasses=True))
    ms += _model_methods(mod)
    ms.append(Method("Tool.__call__", "smolagents.Tool", "tool", display=_tool_display, subclasses=True))
    return ms


def patch_smolagents() -> bool:
    try:
        mod = importlib.import_module("smolagents")
    except ImportError:
        return False
    patched = patch_methods(INTEGRATION, "smolagents", methods(mod))
    # The local Python executor runs agent code (and so tool calls) in a worker
    # thread for its timeout; give it weft's context-propagating pool so those
    # calls nest under the agent step.
    try:
        lpe = importlib.import_module("smolagents.local_python_executor")
    except ImportError:
        lpe = None
    if lpe is not None and getattr(lpe, "ThreadPoolExecutor", None) is not None:
        from .._context import ThreadPoolExecutor as ContextPool

        if lpe.ThreadPoolExecutor is not ContextPool:
            patched |= patch_attr(lpe, "ThreadPoolExecutor", lambda orig: _context_pool(ContextPool))
    return patched


def _context_pool(pool: Any) -> Any:
    class WeftContextThreadPoolExecutor(pool):  # type: ignore[misc, valid-type]
        pass

    return WeftContextThreadPoolExecutor
