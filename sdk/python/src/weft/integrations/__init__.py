"""LLM client and framework integrations. ``weft.init()`` calls ``autopatch()``.

Patching is idempotent and silently skipped when a library is not installed.

| Library | Traced |
|---|---|
| openai | chat.completions.create, responses.create (sync/async/stream) |
| anthropic | messages.create (sync/async/stream), messages.stream() |
| litellm | completion, acompletion (incl. stream) |
| google-genai | models.generate_content(_stream), sync and async |
| boto3 (Bedrock) | converse, converse_stream, invoke_model(_with_response_stream) |
| mistralai | chat.complete/stream (+ _async) |
| cohere | ClientV2/Client chat, chat_stream (sync/async) |
| groq, together | chat.completions.create (OpenAI-compatible) |
| langchain-core | WeftTracer callback handler (auto-registered) |
| openai-agents | WeftTracingProcessor (auto-registered) |
| dspy | WeftDSPyCallback (auto-registered) |

LlamaIndex: use OpenInference (``openinference-instrumentation-llama-index``)
with ``weft.otel.configure`` — see the README.
"""

from __future__ import annotations

import logging

from ._common import unpatch_all
from .anthropic import patch_anthropic
from .bedrock import patch_bedrock
from .dspy import patch_dspy
from .google_genai import patch_google_genai
from .langchain import patch_langchain
from .litellm import patch_litellm
from .openai import patch_openai, patch_openai_compatible
from .openai_agents import patch_openai_agents
from .other_llms import patch_cohere, patch_groq, patch_mistral, patch_together

PATCHERS = (
    patch_openai,
    patch_anthropic,
    patch_litellm,
    patch_google_genai,
    patch_bedrock,
    patch_mistral,
    patch_cohere,
    patch_groq,
    patch_together,
    patch_langchain,
    patch_openai_agents,
    patch_dspy,
)


def autopatch() -> None:
    for fn in PATCHERS:
        try:
            fn()
        except Exception as e:  # pragma: no cover - never break init
            logging.getLogger("weft").debug("weft: autopatch %s failed: %s", fn.__name__, e)


__all__ = [
    "autopatch",
    "unpatch_all",
    "patch_openai",
    "patch_openai_compatible",
    "patch_anthropic",
    "patch_litellm",
    "patch_google_genai",
    "patch_bedrock",
    "patch_mistral",
    "patch_cohere",
    "patch_groq",
    "patch_together",
    "patch_langchain",
    "patch_openai_agents",
    "patch_dspy",
]
