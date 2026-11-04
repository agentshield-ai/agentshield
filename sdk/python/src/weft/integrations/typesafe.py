"""TypeSafe (``pip install typesafe-sdk``): ``TypeSafeClient.system_one`` and
``AsyncTypeSafeClient.system_one``.

Each ``system_one(state, questions, ...)`` call becomes one LLM (``chat``)
span of provider ``typesafe``:

* input: the ``state`` as a user message (a string as is, anything else as
  sorted-key JSON); the ``questions`` (a list or a mapping, each with its
  ``id``) as the ``typesafe.questions`` attribute;
* output: the ``answers`` as an assistant message (JSON), ``output_type``
  ``json``, the response ``model``, ``usage`` (input/output tokens) and the
  request id (``gen_ai.response.id``);
* parent: the current ``Turn`` when one is running; else a new
  ``invoke_agent`` turn named ``typesafe SDK``, the root of its own trace,
  also inside an ``@weft.op`` (the integration creates no op call of its
  own). When an op is running, both spans are linked to it by the
  ``weft.parent_span.trace_id`` and ``weft.parent_span.id`` attributes
  (DESIGN.md §4; the server exposes them as the agent span fields
  ``linked_parent_trace_id`` / ``linked_parent_span_id``). The span is not made
  the current call, so nothing nests under it.
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
from typing import Any, Dict, List, Optional, Tuple

from .. import _context
from . import _settings
from ._common import patch_attr

logger = logging.getLogger("weft")

INTEGRATION = "typesafe"
PROVIDER = "typesafe"
OWN_TURN_AGENT = "typesafe SDK"
OPERATION = "typesafe.system_one"
REQUEST_ID_HEADER = "x-typesafe-request-id"
CAPTURE_ENV = "OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT"
_FALSY = frozenset({"0", "false", "no", "off"})
_DEFAULT_MODEL = "jev-latest"

# None: follow CAPTURE_ENV. Set by set_capture_content / patch_typesafe.
_capture_default: Optional[bool] = None


def set_capture_content(capture_content: Optional[bool]) -> None:
    """Set the process default (``None`` returns to the environment variable)."""
    global _capture_default
    _capture_default = capture_content


def _capture_enabled() -> bool:
    """Whether state, questions and answers may be recorded right now."""
    if _capture_default is None:
        env = os.environ.get(CAPTURE_ENV)
        allowed = env is None or env.strip().lower() not in _FALSY
    else:
        allowed = _capture_default
    if not allowed:
        return False
    from ..conversation import get_current_conversation

    conv = get_current_conversation()
    return conv is None or bool(conv.include_content)


# ---------------------------------------------------------------- encoding


def _to_plain(value: Any) -> Any:
    """JSON-friendly copy of ``value`` (pydantic models dumped, unknown objects as text).

    An iterator is returned untouched: reading it here would hand the SDK an
    exhausted one."""
    if value is None or isinstance(value, (bool, int, float, str)):
        return value
    dump = getattr(value, "model_dump", None)
    if callable(dump):
        try:
            return dump(mode="json")
        except TypeError:
            return dump()
    if isinstance(value, Mapping):
        return {str(k): _to_plain(v) for k, v in value.items()}
    if isinstance(value, (bytes, bytearray)):
        return str(value)
    if isinstance(value, Sequence):
        return [_to_plain(v) for v in value]
    if isinstance(value, Iterator):
        return value
    return str(value)


def _sorted_json(value: Any) -> str:
    return json.dumps(value, sort_keys=True, default=str)


def _user_state_message(state: Any) -> Any:
    from ..conversation import Message, TextPart

    plain = _to_plain(state)
    if isinstance(plain, str):
        return Message(role="user", content=plain)
    return Message(role="user", parts=[TextPart(content=_sorted_json(plain))])


def _encode_questions(questions: Any) -> Optional[str]:
    """``[{"id": key-or-index, ...question}]`` as JSON, or None for anything else."""
    if isinstance(questions, Mapping):
        pairs: List[Tuple[Any, Any]] = list(questions.items())
    elif isinstance(questions, Sequence) and not isinstance(questions, (str, bytes)):
        pairs = list(enumerate(questions))
    else:
        return None
    rows = []
    for key, question in pairs:
        plain = _to_plain(question)
        rows.append({"id": key, **plain} if isinstance(plain, dict) else {"id": key, "value": plain})
    return json.dumps(rows, default=str)


def _field(result: Any, name: str) -> Any:
    value = getattr(result, name, None)
    if value is None and isinstance(result, Mapping):
        value = result.get(name)
    return value


def _result_fields(result: Any, capture: bool) -> Dict[str, Any]:
    """Keyword arguments for ``LLM.record`` describing a successful response."""
    from ..conversation import Message, TextPart, Usage

    fields: Dict[str, Any] = {"output_type": "json"}
    model = _field(result, "model")
    if isinstance(model, str) and model:
        fields["response_model"] = model
    usage = _field(result, "usage")
    if usage is not None:
        if isinstance(usage, Mapping):
            tin, tout = usage.get("input_tokens"), usage.get("output_tokens")
        else:
            tin, tout = getattr(usage, "input_tokens", None), getattr(usage, "output_tokens", None)
        if isinstance(tin, int) or isinstance(tout, int):
            fields["usage"] = Usage(
                input_tokens=tin if isinstance(tin, int) else 0,
                output_tokens=tout if isinstance(tout, int) else 0,
            )
    try:
        request_id = result.request_id
    except Exception:
        request_id = None
    if isinstance(request_id, str) and request_id:
        fields["response_id"] = request_id
    if capture:
        answers = _field(result, "answers")
        if answers is not None:
            text = _sorted_json(_to_plain(answers))
            fields["output_messages"] = [Message(role="assistant", parts=[TextPart(content=text)])]
    return fields


# ---------------------------------------------------------------- errors


def _qualified_error_name(exc: BaseException) -> str:
    kind = type(exc)
    if kind.__module__ == "builtins":
        return kind.__qualname__
    return kind.__module__ + "." + kind.__qualname__


def _request_id_from_error(exc: BaseException) -> Optional[str]:
    headers = getattr(exc, "headers", None)
    if headers is None or not hasattr(headers, "get"):
        return None
    try:
        value = headers.get(REQUEST_ID_HEADER)
    except Exception:
        return None
    return value if isinstance(value, str) and value else None


def _mark_failed(span: Any, exc: BaseException) -> None:
    """Record the failure on ``span`` as error type, HTTP status and request id.

    The exception message is left out on purpose: TypeSafe's API errors embed
    the response body there."""
    name = _qualified_error_name(exc)
    status = getattr(exc, "status", None)
    if isinstance(status, int) and not isinstance(status, bool):
        span.set_attributes({"http": {"response": {"status_code": status}}})
    request_id = _request_id_from_error(exc)
    if request_id is not None:
        if hasattr(span, "response_id"):  # an LLM span: gen_ai.response.id
            span.response_id = request_id
        else:
            span.set_attributes({"gen_ai": {"response": {"id": request_id}}})
    span._exception = name
    span._error_type = name


# ---------------------------------------------------------------- spans


def _link_to_running_op() -> Dict[str, Any]:
    """Link attributes to the innermost running op, skipping conversation spans."""
    for call in reversed(_context._call_stack.get()):
        if getattr(call, "_conversation_span", False):
            continue
        if call.id and call.trace_id:
            return {"weft": {"parent_span": {"trace_id": call.trace_id, "id": call.id}}}
        break
    return {}


def _with_link(attrs: Dict[str, Any], link: Dict[str, Any]) -> Dict[str, Any]:
    out = dict(attrs)
    if link:
        out["weft"] = {**(out.get("weft") or {}), **link["weft"]}
    return out


def _integration_attrs() -> Dict[str, Any]:
    attrs = _settings.integration_attributes(INTEGRATION)
    attrs.setdefault("weft", {})["integration_operation"] = OPERATION
    return attrs


def _request_body(client: Any, state: Any, questions: Any, kwargs: Mapping[str, Any]) -> Dict[str, Any]:
    """The request body as the SDK will send it (``extra_body`` keys win)."""
    config = getattr(client, "_config", None)
    fallback = getattr(config, "default_model", None) or getattr(client, "default_model", None) or _DEFAULT_MODEL
    body: Dict[str, Any] = {"state": state, "questions": questions, "model": kwargs.get("model") or fallback}
    extra = kwargs.get("extra_body")
    if isinstance(extra, Mapping):
        body.update(extra)
    return body


class _SystemOneSpan:
    """The span (and, when needed, the turn) of one traced ``system_one`` call."""

    __slots__ = ("llm", "turn", "owns_turn", "capture", "_finished")

    def __init__(self, llm: Any, turn: Any, owns_turn: bool, capture: bool) -> None:
        self.llm = llm
        self.turn = turn
        self.owns_turn = owns_turn
        self.capture = capture
        self._finished = False

    @classmethod
    def begin(cls, client: Any, state: Any, questions: Any, kwargs: Mapping[str, Any]) -> Optional["_SystemOneSpan"]:
        """Open the spans, or return None when tracing is off or setup fails."""
        if not _settings.is_enabled(INTEGRATION) or _context.tracing_suppressed():
            return None
        from .._client import tracing_client

        weft_client = tracing_client()
        if weft_client is None:
            return None
        from ..conversation import Turn, get_current_turn

        llm = turn = None
        owns_turn = False
        try:
            capture = _capture_enabled()
            body = _request_body(client, state, questions, kwargs)
            model = body["model"] if isinstance(body.get("model"), str) else ""
            # Encode first: state that cannot be encoded must not leave a turn behind.
            message = _user_state_message(body.get("state")) if capture else None
            questions_json = _encode_questions(body.get("questions")) if capture else None
            if questions_json is not None and weft_client._redact.pii:
                questions_json = weft_client._redact.redact_text(questions_json)
            link = _link_to_running_op()
            active = get_current_turn()
            if active is not None and not active._ended:
                turn = active
            else:
                # No running turn: a turn of our own, a new trace even inside an op.
                turn = Turn(agent_name=OWN_TURN_AGENT)
                turn.set_attributes(_with_link(_integration_attrs(), link))
                turn.start(set_current=False)
                owns_turn = True
            llm = turn.start_llm(model=model, provider_name=PROVIDER)
            if message is not None:
                llm.input_messages = [message]
            attrs = _with_link(_integration_attrs(), link)
            if questions_json is not None:
                attrs["typesafe"] = {"questions": questions_json}
            llm.set_attributes(attrs)
            llm.start(set_current=False)
            return cls(llm, turn, owns_turn, capture)
        except Exception:
            logger.warning("weft: could not open the TypeSafe span", exc_info=True)
            for opened in (llm, turn if owns_turn else None):  # close what was opened here
                if opened is not None and not opened._ended:
                    opened.end()
            return None

    def succeed(self, result: Any) -> None:
        try:
            self.llm.record(**_result_fields(result, self.capture))
        except Exception:
            logger.warning("weft: could not record the TypeSafe response", exc_info=True)
        self._finish(None)

    def fail(self, exc: BaseException) -> None:
        self._finish(exc)

    def _finish(self, exc: Optional[BaseException]) -> None:
        if self._finished:
            return
        self._finished = True
        own_turn = self.turn if self.owns_turn else None
        if exc is not None:
            _mark_failed(self.llm, exc)
            if own_turn is not None:
                _mark_failed(own_turn, exc)
        if not self.llm._ended:
            self.llm.end()
        if own_turn is not None and not own_turn._ended:
            own_turn.end()


def _wrap_system_one(original: Any) -> Any:
    if inspect.iscoroutinefunction(original):

        async def async_system_one(self: Any, state: Any, questions: Any, **kwargs: Any) -> Any:
            span = _SystemOneSpan.begin(self, state, questions, kwargs)
            if span is None:
                return await original(self, state, questions, **kwargs)
            try:
                result = await original(self, state, questions, **kwargs)
            except BaseException as exc:
                span.fail(exc)
                raise
            span.succeed(result)
            return result

        return async_system_one

    def system_one(self: Any, state: Any, questions: Any, **kwargs: Any) -> Any:
        span = _SystemOneSpan.begin(self, state, questions, kwargs)
        if span is None:
            return original(self, state, questions, **kwargs)
        try:
            result = original(self, state, questions, **kwargs)
        except BaseException as exc:
            span.fail(exc)
            raise
        span.succeed(result)
        return result

    return system_one


def patch_typesafe(capture_content: Optional[bool] = None) -> bool:
    """Trace ``system_one`` on ``TypeSafeClient`` and ``AsyncTypeSafeClient``.

    ``capture_content`` changes the process-wide content capture default
    (``None`` leaves it as it is)."""
    if capture_content is not None:
        set_capture_content(capture_content)
    try:
        module = importlib.import_module("typesafe_sdk")
    except ImportError:
        return False
    patched = False
    for name in ("TypeSafeClient", "AsyncTypeSafeClient"):
        cls = getattr(module, name, None)
        if cls is not None and hasattr(cls, "system_one"):
            patched = patch_attr(cls, "system_one", _wrap_system_one) or patched
    return patched


__all__ = ["patch_typesafe", "set_capture_content"]
