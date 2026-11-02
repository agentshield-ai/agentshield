"""LLM client and framework integrations. ``weft.init()`` calls ``autopatch()``.

Patching is idempotent and silently skipped when a library is not installed.
A library that is already imported is patched at once; one that is installed
but not imported yet is patched right after it is imported (so ``weft.init()``
does not import every framework you have installed).

| Integration (settings key) | Library | Traced |
|---|---|---|
| ``openai`` | openai | chat.completions.create, responses.create (sync/async/stream); OpenAI-compatible endpoints (NVIDIA NIM, Azure, OpenRouter, vLLM ...) tagged ``weft.provider`` |
| ``openai_realtime`` | openai | Realtime connections: session, response (usage) and function-call spans |
| ``anthropic`` | anthropic | messages.create (sync/async/stream), messages.stream() |
| ``litellm`` | litellm | completion, acompletion (incl. stream) |
| ``google_genai`` | google-genai | models.generate_content(_stream), sync and async |
| ``vertexai`` | google-cloud-aiplatform | GenerativeModel.generate_content(_async), ChatSession.send_message(_async), ImageGenerationModel |
| ``bedrock`` | boto3 | converse, converse_stream, invoke_model(_with_response_stream) |
| ``mistral`` | mistralai | chat.complete/stream (+ _async) |
| ``cohere`` | cohere | ClientV2/Client chat, chat_stream (sync/async) |
| ``groq``, ``together``, ``cerebras`` | groq, together, cerebras-cloud-sdk | chat.completions.create (OpenAI-shaped) |
| ``huggingface`` | huggingface_hub | InferenceClient/AsyncInferenceClient chat_completion, text_generation and task methods |
| ``langchain`` | langchain-core (incl. LangGraph) | WeftTracer callback handler (auto-registered) |
| ``llamaindex`` | llama-index-core | WeftSpanHandler on the instrumentation dispatcher |
| ``openai_agents`` | openai-agents | WeftTracingProcessor (auto-registered) |
| ``dspy`` | dspy | WeftDSPyCallback (auto-registered) |
| ``crewai`` | crewai | Crew, Agent, Task, LLM, tools, Flow |
| ``smolagents`` | smolagents | agents, steps, models, tools |
| ``instructor`` | instructor | create, create_with_completion, create_partial, create_iterable |
| ``mcp`` | mcp, fastmcp | client and server tool/resource/prompt calls |
| ``claude_agent_sdk`` | claude-agent-sdk | query() and ClaudeSDKClient turns, LLM messages, tools |
| ``haystack`` | haystack-ai | WeftHaystackTracer (pipelines and components) |
| ``autogen`` | autogen-agentchat | agents, teams, model clients, tools, memory |
| ``agno`` | agno | agents, teams, workflows, models, tools |
| ``google_adk`` | google-adk | runner, agents, tools, LLM calls |
| ``pydantic_ai`` | pydantic-ai | Agent.run, tools, model requests |
| ``notdiamond``, ``verifiers``, ``gepa`` | | see ``frameworks.py`` |

Per-integration settings: ``weft.init(..., autopatch_settings={"openai":
{"enabled": False}, "anthropic": {"op_settings": {"postprocess_inputs": fn}}})``
and ``WEFT_DISABLED_INTEGRATIONS`` / ``WEFT_AUTOPATCH_<NAME>``; see
``_settings.py``.
"""

from __future__ import annotations

import logging
from dataclasses import dataclass
from typing import Any, Callable, Dict, List, Tuple

from . import _hooks, _settings
from ._common import unpatch_all as _unpatch_attrs
from ._settings import AutopatchSettings, IntegrationSettings, OpSettings
from .agno import patch_agno
from .anthropic import patch_anthropic
from .autogen import TRIGGERS as _AUTOGEN_TRIGGERS
from .autogen import patch_autogen
from .bedrock import patch_bedrock
from .claude_agent_sdk import patch_claude_agent_sdk
from .crewai import patch_crewai
from .dspy import patch_dspy
from .frameworks import patch_gepa, patch_google_adk, patch_notdiamond, patch_pydantic_ai, patch_verifiers
from .google_genai import patch_google_genai
from .haystack import patch_haystack, unpatch_haystack
from .huggingface import patch_huggingface
from .instructor import patch_instructor
from .langchain import patch_langchain
from .litellm import patch_litellm
from .llamaindex import patch_llamaindex, unpatch_llamaindex
from .mcp import patch_mcp
from .openai import patch_openai, patch_openai_compatible
from .openai_agents import patch_openai_agents
from .openai_realtime import RealtimeTracer, patch_openai_realtime
from .other_llms import patch_cerebras, patch_cohere, patch_groq, patch_mistral, patch_together
from .smolagents import patch_smolagents
from .vertexai import patch_vertexai

logger = logging.getLogger("weft")

_PYDANTIC_AI_MODELS = tuple(f"pydantic_ai.models.{m}" for m in ("openai", "anthropic", "gemini", "google", "groq", "mistral", "bedrock", "cohere", "huggingface"))


@dataclass(frozen=True)
class Integration:
    name: str
    patch: Callable[[], bool]
    #: Modules whose import triggers (or re-runs) the patcher.
    modules: Tuple[str, ...]


INTEGRATIONS: Dict[str, Integration] = {
    i.name: i
    for i in (
        Integration("openai", patch_openai, ("openai",)),
        Integration("openai_realtime", patch_openai_realtime, ("openai",)),
        Integration("anthropic", patch_anthropic, ("anthropic",)),
        Integration("litellm", patch_litellm, ("litellm",)),
        Integration("google_genai", patch_google_genai, ("google.genai",)),
        Integration("vertexai", patch_vertexai, ("vertexai",)),
        Integration("bedrock", patch_bedrock, ("botocore",)),
        Integration("mistral", patch_mistral, ("mistralai",)),
        Integration("cohere", patch_cohere, ("cohere",)),
        Integration("groq", patch_groq, ("groq",)),
        Integration("together", patch_together, ("together",)),
        Integration("cerebras", patch_cerebras, ("cerebras.cloud.sdk",)),
        Integration("huggingface", patch_huggingface, ("huggingface_hub",)),
        Integration("langchain", patch_langchain, ("langchain_core",)),
        Integration("llamaindex", patch_llamaindex, ("llama_index.core",)),
        Integration("openai_agents", patch_openai_agents, ("agents",)),
        Integration("dspy", patch_dspy, ("dspy",)),
        Integration("crewai", patch_crewai, ("crewai",)),
        Integration("smolagents", patch_smolagents, ("smolagents",)),
        Integration("instructor", patch_instructor, ("instructor",)),
        Integration("mcp", patch_mcp, ("mcp", "fastmcp")),
        Integration("claude_agent_sdk", patch_claude_agent_sdk, ("claude_agent_sdk",)),
        Integration("haystack", patch_haystack, ("haystack",)),
        Integration("autogen", patch_autogen, _AUTOGEN_TRIGGERS),
        Integration("agno", patch_agno, ("agno",)),
        Integration("google_adk", patch_google_adk, ("google.adk",)),
        Integration("pydantic_ai", patch_pydantic_ai, ("pydantic_ai",) + _PYDANTIC_AI_MODELS),
        Integration("notdiamond", patch_notdiamond, ("notdiamond",)),
        Integration("verifiers", patch_verifiers, ("verifiers",)),
        Integration("gepa", patch_gepa, ("gepa",)),
    )
}

# Kept for compatibility: the patchers in registry order.
PATCHERS = tuple(i.patch for i in INTEGRATIONS.values())

#: Integrations whose patcher has run successfully (name -> True).
PATCHED: Dict[str, bool] = {}


def _run_patch(name: str) -> bool:
    integ = INTEGRATIONS[name]
    if not _settings.is_enabled(name):
        return False
    try:
        ok = bool(integ.patch())
    except Exception as e:  # pragma: no cover - never break init or an import
        logger.debug("weft: autopatch %s failed: %s", name, e)
        return False
    if ok:
        PATCHED[name] = True
    return ok


def _installed(modules: Tuple[str, ...]) -> bool:
    # Only the primary module is looked up: finding a dotted module imports its
    # parent packages, which for the primary ones are light namespace packages.
    return _hooks.is_installed(modules[0])


def autopatch(settings: Any = None) -> Dict[str, str]:
    """Patch every enabled integration whose library is installed.

    ``settings`` is ``autopatch_settings`` (see ``_settings.py``); when given
    it replaces the current per-integration settings. Returns a status per
    integration: ``"patched"``, ``"deferred"`` (patched on import),
    ``"disabled"`` or ``"absent"``.
    """
    if settings is not None:
        _settings.configure(settings)
    status: Dict[str, str] = {}
    for name, integ in INTEGRATIONS.items():
        if not _settings.is_enabled(name):
            status[name] = "disabled"
            continue
        if not _installed(integ.modules):
            status[name] = "absent"
            continue
        imported = [m for m in integ.modules if _hooks.is_imported(m)]
        if imported:
            status[name] = "patched" if _run_patch(name) else "absent"
        for m in integ.modules:
            if m not in imported:
                _hooks.when_imported(m, lambda n=name: _run_patch(n))
                status.setdefault(name, "deferred")
    return status


def patch(name: str) -> bool:
    """Patch one integration by name now (imports its library)."""
    return _run_patch(_settings.canonical(name))


def unpatch_all() -> None:
    """Restore every method patched by weft integrations and drop pending import hooks."""
    _unpatch_attrs()
    unpatch_llamaindex()
    unpatch_haystack()
    _hooks.clear()
    PATCHED.clear()


def configure(autopatch_settings: Any = None) -> None:
    """Set per-integration settings without patching (``None`` resets them)."""
    _settings.configure(autopatch_settings)


def enabled_integrations() -> List[str]:
    return [n for n in INTEGRATIONS if _settings.is_enabled(n)]


__all__ = [
    "autopatch",
    "patch",
    "configure",
    "unpatch_all",
    "enabled_integrations",
    "INTEGRATIONS",
    "AutopatchSettings",
    "IntegrationSettings",
    "OpSettings",
    "RealtimeTracer",
    "patch_openai",
    "patch_openai_compatible",
    "patch_openai_realtime",
    "patch_anthropic",
    "patch_litellm",
    "patch_google_genai",
    "patch_vertexai",
    "patch_bedrock",
    "patch_mistral",
    "patch_cohere",
    "patch_groq",
    "patch_together",
    "patch_cerebras",
    "patch_huggingface",
    "patch_langchain",
    "patch_llamaindex",
    "patch_openai_agents",
    "patch_dspy",
    "patch_crewai",
    "patch_smolagents",
    "patch_instructor",
    "patch_mcp",
    "patch_claude_agent_sdk",
    "patch_haystack",
    "patch_autogen",
    "patch_agno",
    "patch_google_adk",
    "patch_pydantic_ai",
    "patch_notdiamond",
    "patch_verifiers",
    "patch_gepa",
]
