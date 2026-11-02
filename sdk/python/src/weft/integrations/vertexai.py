"""Google Vertex AI SDK (``vertexai.generative_models``).

``GenerativeModel.generate_content`` / ``generate_content_async`` (including
``stream=True``) and ``ChatSession.send_message`` / ``send_message_async``
are traced as LLM calls with usage from ``usage_metadata`` under the model
name. ``ImageGenerationModel.generate_images`` is traced too.
"""

from __future__ import annotations

import importlib
from typing import Any, Dict

from ._common import LLMSpec, make_wrapper, patch_attr
from ._methods import Method, patch_methods
from .google_genai import GenerateContentAccumulator, record_genai_usage

INTEGRATION = "vertexai"


def _model_name(model: Any) -> Any:
    name = getattr(model, "_model_name", None) or getattr(model, "model_name", None)
    if isinstance(name, str) and "/" in name:
        name = name.rsplit("/", 1)[-1]  # projects/.../publishers/google/models/gemini-1.5-pro
    return name


def _model_inputs(model: Any) -> Dict[str, Any]:
    return {"model": _model_name(model)}


def _chat_inputs(chat: Any) -> Dict[str, Any]:
    return {"model": _model_name(getattr(chat, "_model", None))}


def _spec(name: str, self_inputs: Any, arg: str, suppress: bool = False) -> LLMSpec:
    return LLMSpec(
        name,
        accumulator=GenerateContentAccumulator,
        record_usage=record_genai_usage,
        arg_names=(arg,),
        self_inputs=self_inputs,
        integration=INTEGRATION,
        # a chat session calls the model's generate path itself
        suppress_nested=suppress,
    )


def patch_vertexai() -> bool:
    try:
        mod = importlib.import_module("vertexai.generative_models")
    except ImportError:
        return False
    patched = False
    gm = getattr(mod, "GenerativeModel", None)
    if gm is not None:
        patched |= patch_attr(gm, "generate_content", lambda o: make_wrapper(o, _spec("vertexai.GenerativeModel.generate_content", _model_inputs, "contents"), False))
        if hasattr(gm, "generate_content_async"):
            patched |= patch_attr(gm, "generate_content_async", lambda o: make_wrapper(o, _spec("vertexai.GenerativeModel.generate_content_async", _model_inputs, "contents"), True))
    cs = getattr(mod, "ChatSession", None)
    if cs is not None:
        patched |= patch_attr(cs, "send_message", lambda o: make_wrapper(o, _spec("vertexai.ChatSession.send_message", _chat_inputs, "content", True), False))
        if hasattr(cs, "send_message_async"):
            patched |= patch_attr(cs, "send_message_async", lambda o: make_wrapper(o, _spec("vertexai.ChatSession.send_message_async", _chat_inputs, "content", True), True))
    patched |= patch_methods(
        INTEGRATION,
        "vertexai.preview.vision_models",
        [Method("ImageGenerationModel.generate_images", "vertexai.ImageGenerationModel.generate_images", "llm",
                output=lambda r: {"images": len(getattr(r, "images", None) or [])} if not isinstance(r, (dict, list)) else r)],
    )
    return patched
