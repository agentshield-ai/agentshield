"""LiteLLM integration: ``litellm.completion`` / ``litellm.acompletion`` (incl. streaming).

Provider SDK calls made inside LiteLLM are not traced again (no double usage).
"""

from __future__ import annotations

import importlib

from ._common import LLMSpec, make_wrapper, patch_attr
from .openai import ChatCompletionAccumulator, _usage


def _spec(name: str) -> LLMSpec:
    return LLMSpec(name, accumulator=ChatCompletionAccumulator, record_usage=_usage, skip_first_arg=False, suppress_nested=True)


def patch_litellm() -> bool:
    try:
        mod = importlib.import_module("litellm")
    except ImportError:
        return False
    a = patch_attr(mod, "completion", lambda orig: make_wrapper(orig, _spec("litellm.completion"), False))
    b = patch_attr(mod, "acompletion", lambda orig: make_wrapper(orig, _spec("litellm.acompletion"), True))
    return a or b
