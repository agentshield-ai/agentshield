"""Scorers: the ``Scorer`` base class and built-in, server-runnable scorers.

Built-in scorers serialise exactly as the server expects (see API.md), so an
Evaluation that uses them can also be run server side via ``/evaluate_model``
and used in Monitors. They also run locally.
"""

from __future__ import annotations

import json
import re
from typing import Any, ClassVar, Dict, List, Optional

from ._context import get_current_call
from ._object import LLMStructuredCompletionModel, Object, safe_format
from ._op import op
from ._refs import ObjectRef, is_ref_uri
from ._usage import record_openai_usage


class Scorer(Object):
    """Base class for scorers. Subclass and define ``@weft.op def score(self, *, output, **columns)``.

    ``column_map`` maps score() argument names to dataset column names.
    """

    column_map: Optional[Dict[str, str]] = None

    @op
    def score(self, *, output: Any, **kwargs: Any) -> Any:
        raise NotImplementedError("Scorer subclasses must implement score()")

    def summarize(self, score_rows: List[Any]) -> Optional[Dict[str, Any]]:
        from ._evaluation import auto_summarize

        return auto_summarize(score_rows)


def _as_text(v: Any) -> str:
    if isinstance(v, str):
        return v
    if v is None:
        return ""
    try:
        return json.dumps(v, sort_keys=True, ensure_ascii=False)
    except (TypeError, ValueError):
        return str(v)


class _BuiltinScorer(Scorer):
    _weft_publish_ops: ClassVar[bool] = False


class ExactMatchScorer(_BuiltinScorer):
    """``output == row[target_column]`` (strings compared after stripping whitespace)."""

    target_column: str = "expected"

    @op
    def score(self, *, output: Any, **kwargs: Any) -> bool:
        target = kwargs.get(self.target_column)
        if output == target:
            return True
        return _as_text(output).strip() == _as_text(target).strip()


class ContainsScorer(_BuiltinScorer):
    """Whether the output contains ``row[target_column]``."""

    target_column: str = "expected"
    case_insensitive: bool = True

    @op
    def score(self, *, output: Any, **kwargs: Any) -> bool:
        target = _as_text(kwargs.get(self.target_column))
        text = _as_text(output)
        if self.case_insensitive:
            return target.lower() in text.lower()
        return target in text


class RegexScorer(_BuiltinScorer):
    """Whether ``pattern`` matches anywhere in the output."""

    pattern: str

    @op
    def score(self, *, output: Any, **kwargs: Any) -> bool:
        return re.search(self.pattern, _as_text(output)) is not None


class JSONValidScorer(_BuiltinScorer):
    """Whether the output is valid JSON (dicts and lists count as valid)."""

    @op
    def score(self, *, output: Any, **kwargs: Any) -> bool:
        if isinstance(output, (dict, list)):
            return True
        if not isinstance(output, str):
            return False
        try:
            json.loads(output)
            return True
        except ValueError:
            return False


def levenshtein(a: str, b: str) -> int:
    if a == b:
        return 0
    if len(a) < len(b):
        a, b = b, a
    prev = list(range(len(b) + 1))
    for i, ca in enumerate(a, 1):
        cur = [i]
        for j, cb in enumerate(b, 1):
            cur.append(min(prev[j] + 1, cur[j - 1] + 1, prev[j - 1] + (ca != cb)))
        prev = cur
    return prev[-1]


class LevenshteinScorer(_BuiltinScorer):
    """Normalised Levenshtein similarity (0..1) between output and ``row[target_column]``."""

    target_column: str = "expected"

    @op
    def score(self, *, output: Any, **kwargs: Any) -> float:
        a, b = _as_text(output), _as_text(kwargs.get(self.target_column))
        longest = max(len(a), len(b))
        if longest == 0:
            return 1.0
        return 1.0 - levenshtein(a, b) / longest


class LLMAsAJudgeScorer(_BuiltinScorer):
    """An LLM judge. ``scoring_prompt`` placeholders: ``{output}`` and any dataset column.

    ``model`` is a model id (``"gpt-4o-mini"``, ``"mock/json"``...) or an
    ``LLMStructuredCompletionModel``. Locally the completion runs through the
    Weft server's ``/completions/create`` (which holds the provider keys).
    """

    model: Any = "gpt-4o-mini"
    scoring_prompt: str
    response_format: Optional[str] = "json_object"

    @op
    def score(self, *, output: Any, **kwargs: Any) -> Any:
        from ._client import require_client

        client = require_client()
        model = self.model
        if isinstance(model, str) and is_ref_uri(model):
            model = client.get(model)
        if isinstance(model, ObjectRef):
            model = model.get()
        params: Dict[str, Any] = {}
        if isinstance(model, LLMStructuredCompletionModel):
            model_id = model.llm_model_id
            for k in ("temperature", "max_tokens", "top_p"):
                if k in model.default_params:
                    params[k] = model.default_params[k]
        else:
            model_id = str(model)
        prompt = safe_format(self.scoring_prompt, {**{k: _as_text(v) for k, v in kwargs.items()}, "output": _as_text(output)})
        req: Dict[str, Any] = {"model": model_id, "messages": [{"role": "user", "content": prompt}], **params}
        if self.response_format and self.response_format != "text":
            req["response_format"] = {"type": self.response_format}
        resp = client.completions_create(req)
        record_openai_usage(resp, model_id, get_current_call())
        content = ((resp.get("choices") or [{}])[0].get("message") or {}).get("content") or ""
        if self.response_format == "json_object":
            try:
                return json.loads(content)
            except ValueError:
                m = re.search(r"\{.*\}", content, re.S)
                if m:
                    try:
                        return json.loads(m.group(0))
                    except ValueError:
                        pass
                return {"raw": content}
        return content


__all__ = [
    "Scorer",
    "ExactMatchScorer",
    "ContainsScorer",
    "RegexScorer",
    "JSONValidScorer",
    "LevenshteinScorer",
    "LLMAsAJudgeScorer",
    "levenshtein",
]
