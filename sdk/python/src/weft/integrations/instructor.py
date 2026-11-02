"""Instructor integration (structured outputs).

``Instructor.create`` / ``create_with_completion`` / ``create_partial`` /
``create_iterable`` and their ``AsyncInstructor`` counterparts are traced as
LLM calls. The ``response_model`` is recorded by name and JSON schema, and
the output is the validated object. The provider SDK call Instructor makes
(OpenAI, Anthropic, ...) is traced underneath with usage, including retries.
"""

from __future__ import annotations

from typing import Any, Dict, List

from ._methods import Method, patch_methods

INTEGRATION = "instructor"


def _response_model(inputs: Dict[str, Any]) -> Dict[str, Any]:
    rm = inputs.get("response_model")
    if isinstance(rm, type):
        inputs = dict(inputs)
        info: Dict[str, Any] = {"name": rm.__name__}
        schema = getattr(rm, "model_json_schema", None)
        if callable(schema):
            try:
                info["schema"] = schema()
            except Exception:
                pass
        inputs["response_model"] = info
    me = inputs.get("self")
    client = getattr(me, "client", None)
    if "model" not in inputs:
        m = getattr(me, "model", None) or getattr(client, "model", None)
        if isinstance(m, str):
            inputs = dict(inputs)
            inputs["model"] = m
    return inputs


def _with_completion(res: Any) -> Any:
    if isinstance(res, tuple) and len(res) == 2:
        return {"response": res[0], "completion": res[1]}
    return res


def methods() -> List[Method]:
    out: List[Method] = []
    for cls in ("Instructor", "AsyncInstructor"):
        out += [
            Method(f"{cls}.create", f"instructor.{cls}.create", "llm", inputs=_response_model),
            Method(f"{cls}.create_with_completion", f"instructor.{cls}.create_with_completion", "llm", inputs=_response_model, output=_with_completion),
            # partial objects: keep the last (most complete) one; iterables: keep every item
            Method(f"{cls}.create_partial", f"instructor.{cls}.create_partial", "llm", inputs=_response_model,
                   accumulator=lambda acc, item: item, returns_iter="async" if cls.startswith("Async") else "sync"),
            Method(f"{cls}.create_iterable", f"instructor.{cls}.create_iterable", "llm", inputs=_response_model,
                   returns_iter="async" if cls.startswith("Async") else "sync"),
        ]
    return out


def patch_instructor() -> bool:
    return patch_methods(INTEGRATION, "instructor.client", methods()) or patch_methods(INTEGRATION, "instructor", methods())
