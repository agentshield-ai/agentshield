"""TypeSafe (``pip install typesafe-sdk``): ``TypeSafeClient.system_one`` and
``AsyncTypeSafeClient.system_one``.

A port of Weave 0.54's ``weave/integrations/typesafe`` (which has no docs
page). Each ``system_one(state, questions, ...)`` call becomes one LLM
(``chat``) span of provider ``typesafe``:

* input: the ``state`` as a user message (a string as is, anything else as
  sorted-key JSON); the ``questions`` (a list or a mapping, each with its
  ``id``) as the ``typesafe.questions`` attribute;
* output: the ``answers`` as an assistant message (JSON), ``output_type``
  ``json``, the response ``model``, ``usage`` (input/output tokens) and the
  request id (``gen_ai.response.id``);
* parent: the current ``Turn`` when one is running; else the current call
  (``@weft.op``); else a new ``invoke_agent`` turn named ``typesafe SDK``.
  The span is not made the current call, so it nests nothing (as in Weave).
* errors: ``error.type`` and ``http.response.status_code``, with the
  exception recorded as its type only: TypeSafe's API errors carry the
  response body in their message, which is not copied.

Content capture follows ``OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT``
(``false`` drops state, questions and answers),
``patch_typesafe(capture_content=...)`` and a surrounding conversation's
``include_content=False``. The ``typesafe.questions`` attribute goes through
the client's PII redaction like inputs do.
"""

from __future__ import annotations

import importlib
import inspect
import json
import logging
import os
from collections.abc import Iterator, Mapping, Sequence
from dataclasses import dataclass
from typing import Any, Dict, Optional

from .. import _context
from . import _settings
from ._common import patch_attr

logger = logging.getLogger("weft")

INTEGRATION = "typesafe"
_PROVIDER_NAME = "typesafe"
_AGENT_NAME = "typesafe SDK"
_OPERATION = "typesafe.system_one"
_REQUEST_ID_HEADER = "x-typesafe-request-id"
_CAPTURE_ENV = "OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT"
_CAPTURE_OFF = {"0", "false", "no", "off"}

_capture_content_override: Optional[bool] = None


def set_capture_content(capture_content: Optional[bool]) -> None:
    """Set the process default (``None`` returns to the environment variable)."""
    global _capture_content_override
    _capture_content_override = capture_content


def _process_capture_content() -> bool:
    if _capture_content_override is not None:
        return _capture_content_override
    raw = os.environ.get(_CAPTURE_ENV)
    if raw is None:
        return True
    return raw.strip().lower() not in _CAPTURE_OFF


def _effective_capture() -> bool:
    if not _process_capture_content():
        return False
    from ..conversation import get_current_conversation

    conv = get_current_conversation()
    return not (conv is not None and not conv.include_content)


def _jsonable(value: Any) -> Any:
    if hasattr(value, "model_dump"):
        try:
            return value.model_dump(mode="json")
        except TypeError:
            return value.model_dump()
    if isinstance(value, Mapping):
        return {str(k): _jsonable(v) for k, v in value.items()}
    if isinstance(value, Sequence) and not isinstance(value, (str, bytes, bytearray)):
        return [_jsonable(v) for v in value]
    if isinstance(value, Iterator):  # reading it would leave the SDK an empty one
        return value
    if value is None or isinstance(value, (str, int, float, bool)):
        return value
    return str(value)


def _state_message(state: Any) -> Any:
    from ..conversation import Message, TextPart

    value = _jsonable(state)
    if isinstance(value, str):
        return Message(role="user", content=value)
    return Message(role="user", parts=[TextPart(content=json.dumps(value, sort_keys=True, default=str))])


def _question_wire(question: Any) -> Dict[str, Any]:
    dumped = _jsonable(question)
    return dumped if isinstance(dumped, dict) else {"value": dumped}


def _questions_json(questions: Any) -> Optional[str]:
    if isinstance(questions, Mapping):
        items = [{"id": k, **_question_wire(q)} for k, q in questions.items()]
    elif isinstance(questions, Sequence) and not isinstance(questions, (str, bytes)):
        items = [{"id": i, **_question_wire(q)} for i, q in enumerate(questions)]
    else:
        return None
    return json.dumps(items, default=str)


def _get(result: Any, key: str) -> Any:
    v = getattr(result, key, None)
    if v is None and isinstance(result, Mapping):
        v = result.get(key)
    return v


def _answers_message(result: Any) -> Any:
    from ..conversation import Message, TextPart

    answers = _get(result, "answers")
    if answers is None:
        return None
    return Message(role="assistant", parts=[TextPart(content=json.dumps(_jsonable(answers), sort_keys=True, default=str))])


def _usage_from(result: Any) -> Any:
    from ..conversation import Usage

    usage = _get(result, "usage")
    if usage is None:
        return None
    i, o = (usage.get("input_tokens"), usage.get("output_tokens")) if isinstance(usage, Mapping) else (
        getattr(usage, "input_tokens", None), getattr(usage, "output_tokens", None))
    if not isinstance(i, int) and not isinstance(o, int):
        return None
    return Usage(input_tokens=i if isinstance(i, int) else 0, output_tokens=o if isinstance(o, int) else 0)


def _request_id(result: Any) -> Optional[str]:
    try:
        rid = result.request_id
    except Exception:
        return None
    return rid if isinstance(rid, str) and rid else None


def _error_type(exc: BaseException) -> str:
    cls = type(exc)
    return cls.__qualname__ if cls.__module__ == "builtins" else f"{cls.__module__}.{cls.__qualname__}"


def _error_request_id(exc: BaseException) -> Optional[str]:
    getter = getattr(getattr(exc, "headers", None), "get", None)
    if getter is None:
        return None
    try:
        v = getter(_REQUEST_ID_HEADER)
    except Exception:
        return None
    return v if isinstance(v, str) and v else None


def _stamp_error(span: Any, exc: BaseException) -> None:
    """Mark a span failed without copying the provider error text."""
    et = _error_type(exc)  # becomes the error.type attribute
    status = getattr(exc, "status", None)
    if isinstance(status, int) and not isinstance(status, bool):
        span.set_attributes({"http": {"response": {"status_code": status}}})
    rid = _error_request_id(exc)
    if rid is not None:
        if hasattr(span, "response_id"):
            span.response_id = rid  # gen_ai.response.id
        else:
            span.set_attributes({"gen_ai": {"response": {"id": rid}}})
    span._exception = et
    span._error_type = et


def _provenance() -> Dict[str, Any]:
    attrs = _settings.integration_attributes(INTEGRATION)
    attrs.setdefault("weft", {})["integration_operation"] = _OPERATION
    return attrs


@dataclass
class _Trace:
    llm: Any
    turn: Any
    owns_turn: bool
    capture: bool
    closed: bool = False


def _open_trace(client: Any, state: Any, questions: Any, kwargs: Mapping[str, Any]) -> Optional[_Trace]:
    from ..conversation import LLM, Turn, get_current_turn

    if not _settings.is_enabled(INTEGRATION) or _context.tracing_suppressed():
        return None
    from .._client import tracing_client

    weft_client = tracing_client()
    if weft_client is None:
        return None
    turn = None
    owns_turn = False
    llm = None
    try:
        capture = _effective_capture()
        config = getattr(client, "_config", None)
        default_model = getattr(config, "default_model", None) or getattr(client, "default_model", None) or "jev-latest"
        body: Dict[str, Any] = {"state": state, "model": kwargs.get("model") or default_model, "questions": questions}
        extra = kwargs.get("extra_body")
        if isinstance(extra, Mapping):
            body.update(extra)
        model = body.get("model") if isinstance(body.get("model"), str) else ""
        # Encode before any span opens, so a rejected state leaves no turn.
        state_message = _state_message(body.get("state")) if capture else None
        questions_json = _questions_json(body.get("questions")) if capture else None
        if questions_json is not None and weft_client._redact.pii:
            questions_json = weft_client._redact.redact_text(questions_json)
        cur = get_current_turn()
        if cur is not None and not cur._ended:
            turn = cur
            llm = turn.start_llm(model=model, provider_name=_PROVIDER_NAME)
        elif _context.get_current_call() is not None:
            llm = LLM(model=model, provider_name=_PROVIDER_NAME)  # nests under the current call
        else:
            turn = Turn(agent_name=_AGENT_NAME)
            turn.set_attributes(_provenance())
            turn.start(set_current=False)
            owns_turn = True
            llm = turn.start_llm(model=model, provider_name=_PROVIDER_NAME)
        if state_message is not None:
            llm.input_messages = [state_message]
        attrs = _provenance()
        if questions_json is not None:
            attrs["typesafe"] = {"questions": questions_json}  # the typesafe.questions attribute
        llm.set_attributes(attrs)
        llm.start(set_current=False)
        return _Trace(llm=llm, turn=turn, owns_turn=owns_turn, capture=capture)
    except Exception:
        logger.warning("weft: TypeSafe span setup failed", exc_info=True)
        if llm is not None and not llm._ended:
            llm.end()
        if owns_turn and turn is not None and not turn._ended:
            turn.end()
        return None


def _fill(trace: _Trace, result: Any) -> None:
    record: Dict[str, Any] = {"output_type": "json"}
    model = _get(result, "model")
    if isinstance(model, str) and model:
        record["response_model"] = model
    usage = _usage_from(result)
    if usage is not None:
        record["usage"] = usage
    rid = _request_id(result)
    if rid is not None:
        record["response_id"] = rid
    if trace.capture:
        answers = _answers_message(result)
        if answers is not None:
            record["output_messages"] = [answers]
    trace.llm.record(**record)


def _close(trace: _Trace, exc: Optional[BaseException]) -> None:
    if trace.closed:
        return
    trace.closed = True
    if exc is not None:
        _stamp_error(trace.llm, exc)
        if trace.owns_turn and trace.turn is not None:
            _stamp_error(trace.turn, exc)
    if not trace.llm._ended:
        trace.llm.end()
    if trace.owns_turn and trace.turn is not None and not trace.turn._ended:
        trace.turn.end()


def _trace_call(original: Any, client: Any, state: Any, questions: Any, kwargs: Dict[str, Any]) -> Any:
    trace = _open_trace(client, state, questions, kwargs)
    if trace is None:
        return original(client, state, questions, **kwargs)
    try:
        result = original(client, state, questions, **kwargs)
    except BaseException as exc:
        _close(trace, exc)
        raise
    try:
        _fill(trace, result)
    except Exception:
        logger.warning("weft: TypeSafe span capture failed", exc_info=True)
    _close(trace, None)
    return result


async def _trace_call_async(original: Any, client: Any, state: Any, questions: Any, kwargs: Dict[str, Any]) -> Any:
    trace = _open_trace(client, state, questions, kwargs)
    if trace is None:
        return await original(client, state, questions, **kwargs)
    try:
        result = await original(client, state, questions, **kwargs)
    except BaseException as exc:
        _close(trace, exc)
        raise
    try:
        _fill(trace, result)
    except Exception:
        logger.warning("weft: TypeSafe span capture failed", exc_info=True)
    _close(trace, None)
    return result


def _make_wrapper(original: Any) -> Any:
    if inspect.iscoroutinefunction(original):

        async def async_system_one(self: Any, state: Any, questions: Any, **kwargs: Any) -> Any:
            return await _trace_call_async(original, self, state, questions, kwargs)

        return async_system_one

    def system_one(self: Any, state: Any, questions: Any, **kwargs: Any) -> Any:
        return _trace_call(original, self, state, questions, kwargs)

    return system_one


def patch_typesafe(capture_content: Optional[bool] = None) -> bool:
    """Trace ``TypeSafeClient.system_one`` and ``AsyncTypeSafeClient.system_one``.

    ``capture_content`` sets the process default for state, questions and
    answers (``None`` keeps the current one)."""
    if capture_content is not None:
        set_capture_content(capture_content)
    try:
        mod = importlib.import_module("typesafe_sdk")
    except ImportError:
        return False
    patched = False
    for cls_name in ("TypeSafeClient", "AsyncTypeSafeClient"):
        cls = getattr(mod, cls_name, None)
        if cls is not None and hasattr(cls, "system_one"):
            patched |= patch_attr(cls, "system_one", _make_wrapper)
    return patched


__all__ = ["patch_typesafe", "set_capture_content"]
