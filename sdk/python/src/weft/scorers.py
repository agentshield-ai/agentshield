"""Scorers: the ``Scorer`` base class and built-in, server-runnable scorers.

Built-in scorers serialise exactly as the server expects (see API.md), so an
Evaluation that uses them can also be run server side via ``/evaluate_model``
and used in Monitors. They also run locally.
"""

from __future__ import annotations

import json
import re
from typing import Any, ClassVar, Dict, List, Optional

from pydantic import ConfigDict, Field

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

    @op(kind="scorer")
    def score(self, *, output: Any, **kwargs: Any) -> bool:
        target = kwargs.get(self.target_column)
        if output == target:
            return True
        return _as_text(output).strip() == _as_text(target).strip()


class ContainsScorer(_BuiltinScorer):
    """Whether the output contains ``row[target_column]``."""

    target_column: str = "expected"
    case_insensitive: bool = True

    @op(kind="scorer")
    def score(self, *, output: Any, **kwargs: Any) -> bool:
        target = _as_text(kwargs.get(self.target_column))
        text = _as_text(output)
        if self.case_insensitive:
            return target.lower() in text.lower()
        return target in text


class RegexScorer(_BuiltinScorer):
    """Whether ``pattern`` matches anywhere in the output."""

    pattern: str

    @op(kind="scorer")
    def score(self, *, output: Any, **kwargs: Any) -> bool:
        return re.search(self.pattern, _as_text(output)) is not None


class JSONValidScorer(_BuiltinScorer):
    """Whether the output is valid JSON (dicts and lists count as valid)."""

    @op(kind="scorer")
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

    @op(kind="scorer")
    def score(self, *, output: Any, **kwargs: Any) -> float:
        a, b = _as_text(output), _as_text(kwargs.get(self.target_column))
        longest = max(len(a), len(b))
        if longest == 0:
            return 1.0
        return 1.0 - levenshtein(a, b) / longest


def _run_judge(model: Any, scoring_prompt: str, response_format: Optional[str], output: Any, kwargs: Dict[str, Any]) -> Any:
    """Format ``scoring_prompt`` and run it through the server's /completions/create."""
    from ._client import require_client

    client = require_client()
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
    values = {k: _as_text(v) for k, v in kwargs.items()}
    values["inputs"] = _as_text({k: v for k, v in kwargs.items()})
    values["output"] = _as_text(output)
    prompt = safe_format(scoring_prompt, values)
    req: Dict[str, Any] = {"model": model_id, "messages": [{"role": "user", "content": prompt}], **params}
    if response_format and response_format != "text":
        req["response_format"] = {"type": response_format}
    resp = client.completions_create(req)
    record_openai_usage(resp, model_id, get_current_call())
    content = ((resp.get("choices") or [{}])[0].get("message") or {}).get("content") or ""
    if response_format == "json_object":
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


class LLMAsAJudgeScorer(_BuiltinScorer):
    """An LLM judge. ``scoring_prompt`` placeholders: ``{output}``, ``{inputs}`` (all
    columns as JSON) and any dataset column.

    ``model`` is a model id (``"gpt-4o-mini"``, ``"mock/json"``...) or an
    ``LLMStructuredCompletionModel``. Locally the completion runs through the
    Weft server's ``/completions/create`` (which holds the provider keys).
    """

    model: Any = "gpt-4o-mini"
    scoring_prompt: str
    response_format: Optional[str] = "json_object"

    @op(kind="scorer")
    def score(self, *, output: Any, **kwargs: Any) -> Any:
        return _run_judge(self.model, self.scoring_prompt, self.response_format, output, kwargs)


HALLUCINATION_PROMPT = (
    "You are verifying an AI response against its input and context.\n\n"
    "<input>\n{inputs}\n</input>\n\n<response>\n{output}\n</response>\n\n"
    "Is every factual claim in the response supported by the input/context? "
    'Reply with JSON: {{"hallucination_free": true|false, "reasoning": "..."}}'
)
SUMMARIZATION_PROMPT = (
    "You are grading a summary of a source text.\n\n<source>\n{inputs}\n</source>\n\n"
    "<summary>\n{output}\n</summary>\n\n"
    "Rate how faithful, complete and concise the summary is from 0 (useless) to 1 (excellent). "
    'Reply with JSON: {{"summarization_score": <number 0..1>, "is_faithful": true|false, "reasoning": "..."}}'
)
RELEVANCE_PROMPT = (
    "You are judging whether a response is relevant to the request.\n\n<request>\n{inputs}\n</request>\n\n"
    "<response>\n{output}\n</response>\n\n"
    'Reply with JSON: {{"is_relevant": true|false, "relevance_score": <number 0..1>, "reasoning": "..."}}'
)


class _JudgePreset(_BuiltinScorer):
    model: Any = "gpt-4o-mini"
    scoring_prompt: str = ""

    @op(kind="scorer")
    def score(self, *, output: Any, **kwargs: Any) -> Any:
        return _run_judge(self.model, self.scoring_prompt, "json_object", output, kwargs)


class HallucinationFreeScorer(_JudgePreset):
    """LLM judge: is the output free of claims unsupported by the inputs? → ``{hallucination_free, reasoning}``."""

    scoring_prompt: str = HALLUCINATION_PROMPT


class SummarizationScorer(_JudgePreset):
    """LLM judge for summaries → ``{summarization_score, is_faithful, reasoning}``."""

    scoring_prompt: str = SUMMARIZATION_PROMPT


class RelevanceScorer(_JudgePreset):
    """LLM judge: is the output relevant to the inputs? → ``{is_relevant, relevance_score, reasoning}``."""

    scoring_prompt: str = RELEVANCE_PROMPT


class ValidXMLScorer(_BuiltinScorer):
    """Whether the output is well-formed XML."""

    @op(kind="scorer")
    def score(self, *, output: Any, **kwargs: Any) -> bool:
        import xml.etree.ElementTree as ET

        if not isinstance(output, str) or not output.strip():
            return False
        try:
            ET.fromstring(output)
            return True
        except ET.ParseError:
            return False


def _schema_errors(value: Any, schema: Dict[str, Any], path: str = "$") -> List[str]:
    """Minimal JSON Schema validation (type, enum, const, properties, required,
    additionalProperties, items, min/max, lengths, pattern, anyOf/allOf/oneOf)."""
    errs: List[str] = []
    types = {
        "object": lambda v: isinstance(v, dict),
        "array": lambda v: isinstance(v, list),
        "string": lambda v: isinstance(v, str),
        "integer": lambda v: isinstance(v, int) and not isinstance(v, bool),
        "number": lambda v: isinstance(v, (int, float)) and not isinstance(v, bool),
        "boolean": lambda v: isinstance(v, bool),
        "null": lambda v: v is None,
    }
    t = schema.get("type")
    if t is not None:
        allowed = t if isinstance(t, list) else [t]
        if not any(types.get(x, lambda v: True)(value) for x in allowed):
            return [f"{path}: expected {t}"]
    if "enum" in schema and value not in schema["enum"]:
        errs.append(f"{path}: not one of {schema['enum']}")
    if "const" in schema and value != schema["const"]:
        errs.append(f"{path}: expected {schema['const']!r}")
    if isinstance(value, dict):
        props = schema.get("properties") or {}
        for req in schema.get("required") or []:
            if req not in value:
                errs.append(f"{path}: missing {req!r}")
        for k, v in value.items():
            if k in props:
                errs.extend(_schema_errors(v, props[k], f"{path}.{k}"))
            elif schema.get("additionalProperties") is False:
                errs.append(f"{path}: unexpected property {k!r}")
            elif isinstance(schema.get("additionalProperties"), dict):
                errs.extend(_schema_errors(v, schema["additionalProperties"], f"{path}.{k}"))
    if isinstance(value, list):
        if isinstance(schema.get("items"), dict):
            for i, v in enumerate(value):
                errs.extend(_schema_errors(v, schema["items"], f"{path}[{i}]"))
        if "minItems" in schema and len(value) < schema["minItems"]:
            errs.append(f"{path}: too few items")
        if "maxItems" in schema and len(value) > schema["maxItems"]:
            errs.append(f"{path}: too many items")
    if isinstance(value, str):
        if "minLength" in schema and len(value) < schema["minLength"]:
            errs.append(f"{path}: too short")
        if "maxLength" in schema and len(value) > schema["maxLength"]:
            errs.append(f"{path}: too long")
        if "pattern" in schema and re.search(schema["pattern"], value) is None:
            errs.append(f"{path}: does not match {schema['pattern']!r}")
    if isinstance(value, (int, float)) and not isinstance(value, bool):
        if "minimum" in schema and value < schema["minimum"]:
            errs.append(f"{path}: below minimum")
        if "maximum" in schema and value > schema["maximum"]:
            errs.append(f"{path}: above maximum")
        if "exclusiveMinimum" in schema and value <= schema["exclusiveMinimum"]:
            errs.append(f"{path}: not above exclusiveMinimum")
        if "exclusiveMaximum" in schema and value >= schema["exclusiveMaximum"]:
            errs.append(f"{path}: not below exclusiveMaximum")
    for sub in schema.get("allOf") or []:
        errs.extend(_schema_errors(value, sub, path))
    if schema.get("anyOf") and not any(not _schema_errors(value, sub, path) for sub in schema["anyOf"]):
        errs.append(f"{path}: matches none of anyOf")
    if schema.get("oneOf") and sum(1 for sub in schema["oneOf"] if not _schema_errors(value, sub, path)) != 1:
        errs.append(f"{path}: must match exactly one of oneOf")
    return errs


class JSONSchemaScorer(_BuiltinScorer):
    """Whether the output (a JSON string or value) validates against ``schema``."""

    schema_: Dict[str, Any] = Field(default_factory=dict, alias="schema")

    model_config = ConfigDict(populate_by_name=True)

    @op(kind="scorer")
    def score(self, *, output: Any, **kwargs: Any) -> bool:
        value = output
        if isinstance(output, str):
            try:
                value = json.loads(output)
            except ValueError:
                return False
        return not _schema_errors(value, self.schema_)


class OpenAIModerationScorer(_BuiltinScorer):
    """OpenAI moderation → ``{flagged, categories}``. Locally requires the ``openai``
    package and ``OPENAI_API_KEY``; the server runs it with its own key."""

    @op(kind="guardrail")
    def score(self, *, output: Any, **kwargs: Any) -> Dict[str, Any]:
        try:
            import openai  # type: ignore
        except ImportError:
            raise RuntimeError(
                "OpenAIModerationScorer runs locally only with the openai package installed; "
                "publish it and let the server run it (monitors, /evaluate_model)"
            ) from None
        res = openai.OpenAI().moderations.create(input=_as_text(output))
        r = res.results[0]
        cats = r.categories.model_dump() if hasattr(r.categories, "model_dump") else dict(r.categories)
        return {"flagged": bool(r.flagged), "categories": {k: v for k, v in cats.items() if v}}


class EmbeddingSimilarityScorer(_BuiltinScorer):
    """Cosine similarity between embeddings of the output and ``row[target_column]``.

    Locally this needs an embeddings endpoint on the Weft server
    (``/embeddings/create``); otherwise publish it and let the server run it.
    """

    target_column: str = "expected"
    model: str = "text-embedding-3-small"

    @op(kind="scorer")
    def score(self, *, output: Any, **kwargs: Any) -> float:
        import math

        from ._client import require_client
        from ._http import WeftHTTPError

        client = require_client()
        texts = [_as_text(output), _as_text(kwargs.get(self.target_column))]
        try:
            res = client._http.post("/embeddings/create", {"project_id": client.project_id, "inputs": {"model": self.model, "input": texts}})
        except WeftHTTPError as e:
            if e.status in (404, 405, 501):
                raise RuntimeError(
                    "EmbeddingSimilarityScorer cannot run locally: this Weft server has no /embeddings/create endpoint. "
                    "Publish the scorer and run it server side (monitors, /evaluate_model)."
                ) from None
            raise
        data = (res.get("response") or res).get("data") or []
        a, b = data[0]["embedding"], data[1]["embedding"]
        record_openai_usage(res.get("response") or res, self.model, get_current_call())
        dot = sum(x * y for x, y in zip(a, b))
        na, nb = math.sqrt(sum(x * x for x in a)), math.sqrt(sum(y * y for y in b))
        return dot / (na * nb) if na and nb else 0.0


class RuleScorer(_BuiltinScorer):
    """A rule in the Weft query language evaluated on ``{output, inputs, <columns>}``.

    ``rule`` is an operation such as
    ``{"$contains": {"input": {"$getField": "output"}, "substr": {"$literal": "sorry"}}}``.
    Without ``threshold`` the rule's value is the score; with ``threshold`` a
    numeric value scores ``value >= threshold``.
    """

    rule: Dict[str, Any]
    threshold: Optional[float] = None

    @op(kind="scorer")
    def score(self, *, output: Any, **kwargs: Any) -> Any:
        from ._expr import evaluate

        doc = {**kwargs, "inputs": dict(kwargs), "output": output}
        value = evaluate(self.rule, doc)
        if self.threshold is not None and isinstance(value, (int, float)) and not isinstance(value, bool):
            return value >= self.threshold
        return value


class RemoteScorer(_BuiltinScorer):
    """Score with an HTTP endpoint: POSTs ``{"output", "inputs"}`` JSON to ``url``
    and returns the JSON response.

    ``headers_secret`` names a secret holding extra request headers (a JSON
    object, or a bare token sent as ``Authorization: Bearer``). The server
    reads it from its secret store; locally it is read from the environment
    variable of the same name.
    """

    url: str
    headers_secret: Optional[str] = None
    timeout: float = 30.0

    @op(kind="scorer")
    def score(self, *, output: Any, **kwargs: Any) -> Any:
        import os
        import urllib.request

        headers = {"Content-Type": "application/json", "Accept": "application/json"}
        if self.headers_secret:
            raw = os.environ.get(self.headers_secret)
            if raw is None:
                raise RuntimeError(f"RemoteScorer: environment variable {self.headers_secret!r} (headers_secret) is not set")
            try:
                extra = json.loads(raw)
                if not isinstance(extra, dict):
                    raise ValueError
                headers.update({str(k): str(v) for k, v in extra.items()})
            except ValueError:
                headers["Authorization"] = f"Bearer {raw}"
        from ._serialize import to_json

        body = json.dumps({"output": to_json(output), "inputs": to_json(kwargs)}).encode("utf-8")
        req = urllib.request.Request(self.url, data=body, method="POST", headers=headers)
        with urllib.request.urlopen(req, timeout=self.timeout) as resp:
            text = resp.read().decode("utf-8")
        try:
            return json.loads(text)
        except ValueError:
            return text


class PydanticScorer(Scorer):
    """Whether the output parses as the pydantic model ``model`` (local only)."""

    model: Any

    @op(kind="scorer")
    def score(self, *, output: Any, **kwargs: Any) -> bool:
        from pydantic import ValidationError

        try:
            if isinstance(output, str):
                self.model.model_validate_json(output)
            else:
                self.model.model_validate(output)
            return True
        except (ValidationError, ValueError, TypeError):
            return False


__all__ = [
    "Scorer",
    "ExactMatchScorer",
    "ContainsScorer",
    "RegexScorer",
    "JSONValidScorer",
    "LevenshteinScorer",
    "LLMAsAJudgeScorer",
    "HallucinationFreeScorer",
    "SummarizationScorer",
    "RelevanceScorer",
    "ValidXMLScorer",
    "JSONSchemaScorer",
    "OpenAIModerationScorer",
    "EmbeddingSimilarityScorer",
    "RuleScorer",
    "RemoteScorer",
    "PydanticScorer",
    "levenshtein",
]


_LOCAL = (
    "WeaveToxicityScorerV1", "WeaveBiasScorerV1", "WeaveCoherenceScorerV1", "WeaveFluencyScorerV1",
    "WeaveHallucinationScorerV1", "WeaveContextRelevanceScorerV1", "WeaveTrustScorerV1", "PresidioScorer",
    "WeaveScorerResult",
)


def __getattr__(name: str) -> Any:
    """Local small-model scorers live in ``weft.local_scorers`` (imported lazily; optional deps)."""
    if name in _LOCAL:
        from . import local_scorers

        return getattr(local_scorers, name)
    raise AttributeError(f"module 'weft.scorers' has no attribute {name!r}")
