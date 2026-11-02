"""AutoGen (``autogen-agentchat`` / ``autogen-core`` / ``autogen-ext``, v0.4+).

Traced: agent ``run``/``run_stream``/``on_messages``/``on_messages_stream``
(agent), team (group chat) ``run``/``run_stream`` (agent), model client
``create``/``create_stream`` on every ``ChatCompletionClient`` implementation
(llm; usage from ``CreateResult.usage``), tools ``BaseTool.run_json``/``run``
(tool; display name = the tool's name), memory ``query``/``update_context``
(retriever) and code executors' ``execute_code_blocks`` (tool).

Model clients that wrap a traced SDK (``OpenAIChatCompletionClient`` uses
``openai``) get usage from the nested call; the AutoGen span records usage
only when nothing was traced under it. ``autogen_ext`` model clients imported
after ``weft.init()`` are patched when their module is imported.
"""

from __future__ import annotations

from typing import Any, Dict, List

from .._usage import add_usage
from ._common import to_dict
from ._methods import Method, patch_methods

INTEGRATION = "autogen"

#: Modules whose import re-runs the patcher (model clients live in autogen_ext).
TRIGGERS = (
    "autogen_agentchat",
    "autogen_core",
    "autogen_ext.models.openai",
    "autogen_ext.models.anthropic",
    "autogen_ext.models.azure",
    "autogen_ext.models.ollama",
    "autogen_ext.models.semantic_kernel",
    "autogen_ext.models.llama_cpp",
)


def _self(call: Any) -> Any:
    return (call._raw_inputs or {}).get("self")


def _named(prefix: str) -> Any:
    def display(call: Any) -> str:
        me = _self(call)
        name = getattr(me, "name", None) or getattr(me, "_name", None)
        return f"{prefix}: {name}" if isinstance(name, str) and name else prefix

    return display


def create_usage(call: Any, res: Any) -> None:
    u = getattr(res, "usage", None)
    if u is None:
        return
    model = None
    me = _self(call)
    info = getattr(me, "_create_args", None) or getattr(me, "_raw_config", None)
    if isinstance(info, dict):
        model = info.get("model")
    model = model or getattr(me, "model", None) or (call.inputs or {}).get("model")
    add_usage(call, model if isinstance(model, str) else None, getattr(u, "prompt_tokens", 0), getattr(u, "completion_tokens", 0))


def _client_inputs(inputs: Dict[str, Any]) -> Dict[str, Any]:
    me = inputs.get("self")
    info = getattr(me, "_create_args", None) or getattr(me, "_raw_config", None)
    if isinstance(info, dict) and isinstance(info.get("model"), str):
        inputs = dict(inputs)
        inputs["model"] = info["model"]
    return inputs


def _result(res: Any) -> Any:
    d = to_dict(res)
    return d if d is not None else res


def methods() -> List[Method]:
    agents = "autogen_agentchat.agents"
    teams = "autogen_agentchat.teams"
    return [
        Method("BaseChatAgent.run", "autogen.Agent.run", "agent", module=agents, display=_named("Agent"), subclasses=True, output=_result),
        Method("BaseChatAgent.run_stream", "autogen.Agent.run_stream", "agent", module=agents, display=_named("Agent"), subclasses=True),
        Method("BaseChatAgent.on_messages", "autogen.Agent.on_messages", "agent", module=agents, display=_named("Agent"), subclasses=True, output=_result),
        Method("BaseChatAgent.on_messages_stream", "autogen.Agent.on_messages_stream", "agent", module=agents, display=_named("Agent"), subclasses=True),
        Method("BaseGroupChat.run", "autogen.Team.run", "agent", module=teams, display=_named("Team"), subclasses=True, output=_result),
        Method("BaseGroupChat.run_stream", "autogen.Team.run_stream", "agent", module=teams, display=_named("Team"), subclasses=True),
        Method("ChatCompletionClient.create", "autogen.ChatCompletionClient.create", "llm", module="autogen_core.models",
               subclasses=True, usage=create_usage, inputs=_client_inputs, output=_result),
        Method("ChatCompletionClient.create_stream", "autogen.ChatCompletionClient.create_stream", "llm", module="autogen_core.models",
               subclasses=True, inputs=_client_inputs),
        Method("BaseTool.run_json", "autogen.Tool.run_json", "tool", module="autogen_core.tools", display=_named("Tool"), subclasses=True),
        Method("Memory.query", "autogen.Memory.query", "retriever", module="autogen_core.memory", subclasses=True, output=_result),
        Method("Memory.update_context", "autogen.Memory.update_context", "retriever", module="autogen_core.memory", subclasses=True),
        Method("CodeExecutor.execute_code_blocks", "autogen.CodeExecutor.execute_code_blocks", "tool", module="autogen_core.code_executor",
               subclasses=True, output=_result),
    ]


def patch_autogen() -> bool:
    return patch_methods(INTEGRATION, None, methods())
