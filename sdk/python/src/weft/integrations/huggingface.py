"""Hugging Face Hub ``InferenceClient`` / ``AsyncInferenceClient``.

``chat_completion`` (also reached through ``client.chat.completions.create``)
is traced as an LLM call with usage, including streams (``stream=True``;
usage arrives when ``stream_options={"include_usage": True}`` is passed or
the provider sends it). ``text_generation`` and the other task methods are
traced as calls with their inputs and outputs.
"""

from __future__ import annotations

import importlib
from typing import Any, Dict, Optional

from .._usage import add_usage
from ._common import Accumulator, LLMSpec, make_wrapper, patch_attr, to_dict
from ._methods import Method, patch_methods
from .openai import ChatCompletionAccumulator, _usage

INTEGRATION = "huggingface"

TASKS = (
    "feature_extraction",
    "fill_mask",
    "question_answering",
    "sentence_similarity",
    "summarization",
    "table_question_answering",
    "text_classification",
    "token_classification",
    "translation",
    "zero_shot_classification",
    "text_to_image",
    "image_to_text",
    "document_question_answering",
    "visual_question_answering",
    "automatic_speech_recognition",
)


def _self_inputs(client: Any) -> Dict[str, Any]:
    m = getattr(client, "model", None)
    p = getattr(client, "provider", None)
    out: Dict[str, Any] = {}
    if isinstance(m, str):
        out["model"] = m
    if isinstance(p, str):
        out["provider"] = p
    return out


def _hf_usage(call: Any, out: Any) -> None:
    if not isinstance(out, dict):
        return
    if not out.get("model"):
        out = dict(out)
        out["model"] = (call.inputs or {}).get("model")
    _usage(call, out)


def _chat_spec(name: str) -> LLMSpec:
    return LLMSpec(
        name,
        accumulator=ChatCompletionAccumulator,
        record_usage=_hf_usage,
        arg_names=("messages",),
        self_inputs=_self_inputs,
        integration=INTEGRATION,
    )


class _TextGenAccumulator(Accumulator):
    def __init__(self) -> None:
        self.text = ""
        self.details: Optional[Dict[str, Any]] = None

    def add(self, c: Any) -> None:
        if isinstance(c, str):
            self.text += c
            return
        if not isinstance(c, dict):
            return
        tok = c.get("token") or {}
        if isinstance(tok, dict) and not tok.get("special"):
            self.text += tok.get("text") or ""
        if c.get("details"):
            self.details = c["details"]
        if c.get("generated_text") and not self.text:
            self.text = c["generated_text"]

    def result(self) -> Dict[str, Any]:
        out: Dict[str, Any] = {"generated_text": self.text}
        if self.details is not None:
            out["details"] = self.details
        return out


def _textgen_usage(call: Any, out: Any) -> None:
    if not isinstance(out, dict):
        return
    d = out.get("details") or {}
    if not isinstance(d, dict) or "generated_tokens" not in d:
        return
    prefill = d.get("prefill") or []
    add_usage(call, (call.inputs or {}).get("model") or "huggingface", len(prefill) if isinstance(prefill, list) else 0, d.get("generated_tokens") or 0)


def _textgen_spec(name: str) -> LLMSpec:
    return LLMSpec(
        name,
        accumulator=_TextGenAccumulator,
        record_usage=_textgen_usage,
        arg_names=("prompt",),
        self_inputs=_self_inputs,
        integration=INTEGRATION,
    )


def patch_huggingface() -> bool:
    try:
        mod = importlib.import_module("huggingface_hub")
    except ImportError:
        return False
    patched = False
    for cls_name, is_async in (("InferenceClient", False), ("AsyncInferenceClient", True)):
        cls = getattr(mod, cls_name, None)
        if cls is None:
            continue
        if hasattr(cls, "chat_completion"):
            patched |= patch_attr(cls, "chat_completion", lambda o, n=cls_name, a=is_async: make_wrapper(o, _chat_spec(f"huggingface_hub.{n}.chat_completion"), a))
        if hasattr(cls, "text_generation"):
            patched |= patch_attr(cls, "text_generation", lambda o, n=cls_name, a=is_async: make_wrapper(o, _textgen_spec(f"huggingface_hub.{n}.text_generation"), a))
        patched |= patch_methods(
            INTEGRATION,
            "huggingface_hub",
            [Method(f"{cls_name}.{t}", f"huggingface_hub.{cls_name}.{t}", "llm" if t in ("summarization", "translation", "image_to_text") else None) for t in TASKS],
        )
    return patched


__all__ = ["patch_huggingface", "to_dict"]
