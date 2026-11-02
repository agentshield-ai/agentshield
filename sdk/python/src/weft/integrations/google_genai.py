"""Google Gen AI SDK (``google.genai``): ``models.generate_content`` and
``generate_content_stream``, sync and async (``client.aio.models``)."""

from __future__ import annotations

import importlib
from typing import Any, Dict, List, Optional

from .._usage import add_usage, gemini_usage
from ._common import Accumulator, LLMSpec, make_wrapper, patch_attr


def _text_of(cand: Dict[str, Any]) -> str:
    parts = ((cand or {}).get("content") or {}).get("parts") or []
    return "".join(p.get("text") or "" for p in parts if isinstance(p, dict) and not p.get("thought"))


class GenerateContentAccumulator(Accumulator):
    def __init__(self) -> None:
        self.last: Dict[str, Any] = {}
        self.texts: Dict[int, str] = {}
        self.other_parts: Dict[int, List[Any]] = {}
        self.usage: Optional[Dict[str, Any]] = None

    def add(self, c: Dict[str, Any]) -> None:
        if not isinstance(c, dict):
            return
        self.last = c
        if c.get("usage_metadata"):
            self.usage = c["usage_metadata"]
        for i, cand in enumerate(c.get("candidates") or []):
            idx = cand.get("index", i) or i
            self.texts[idx] = self.texts.get(idx, "") + _text_of(cand)
            for p in ((cand.get("content") or {}).get("parts") or []):
                if isinstance(p, dict) and not p.get("text"):
                    self.other_parts.setdefault(idx, []).append(p)

    def result(self) -> Dict[str, Any]:
        out = dict(self.last)
        cands = []
        for idx in sorted(self.texts):
            base = {}
            for cand in self.last.get("candidates") or []:
                if (cand.get("index") or 0) == idx:
                    base = dict(cand)
            parts = ([{"text": self.texts[idx]}] if self.texts[idx] else []) + self.other_parts.get(idx, [])
            base["content"] = {"role": "model", "parts": parts}
            cands.append(base)
        out["candidates"] = cands
        if self.usage is not None:
            out["usage_metadata"] = self.usage
        out["text"] = self.texts.get(0, "")
        return out


def record_genai_usage(call: Any, out: Any) -> None:
    if not isinstance(out, dict):
        return
    u = out.get("usage_metadata") or out.get("usageMetadata")
    if not isinstance(u, dict):
        return
    model = (call.inputs or {}).get("model") or out.get("model_version") or out.get("modelVersion")
    n = gemini_usage(u)  # keeps cached_content_token_count as cache_read_input_tokens
    if n is not None:
        add_usage(call, model, **n)


def _spec(name: str, stream: bool) -> LLMSpec:
    return LLMSpec(
        name,
        is_stream=(lambda kw, res: True) if stream else (lambda kw, res: False),
        accumulator=GenerateContentAccumulator,
        record_usage=record_genai_usage,
    )


def patch_google_genai() -> bool:
    try:
        mod = importlib.import_module("google.genai.models")
    except ImportError:
        return False
    patched = False
    for cls_name, is_async in (("Models", False), ("AsyncModels", True)):
        cls = getattr(mod, cls_name, None)
        if cls is None:
            continue
        patched |= patch_attr(cls, "generate_content", lambda o, a=is_async: make_wrapper(o, _spec("google.genai.models.generate_content", False), a))
        patched |= patch_attr(cls, "generate_content_stream", lambda o, a=is_async: make_wrapper(o, _spec("google.genai.models.generate_content_stream", True), a))
    return patched
