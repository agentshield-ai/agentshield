"""Shared machinery for LLM client integrations."""

from __future__ import annotations

import functools
import inspect
import logging
from typing import Any, Callable, Dict, List, Optional, Tuple

from .. import _context
from .._context import pop_call, push_call, tracing_suppressed
from . import _settings

logger = logging.getLogger("weft")

_PATCHES: List[Tuple[Any, str, Any]] = []
_UNDO: List[Callable[[], None]] = []


def register_undo(fn: Callable[[], None]) -> None:
    """Run ``fn`` on ``unpatch_all()`` (for patches that are not a plain attribute swap)."""
    _UNDO.append(fn)


def to_dict(obj: Any) -> Any:
    if obj is None or isinstance(obj, (dict, list, str, int, float, bool)):
        return obj
    for meth in ("model_dump", "to_dict", "dict"):
        fn = getattr(obj, meth, None)
        if callable(fn):
            try:
                d = fn()
                if isinstance(d, dict):
                    return d
            except Exception:
                continue
    if hasattr(obj, "__dict__"):
        return {k: v for k, v in vars(obj).items() if not k.startswith("_")}
    return obj


def is_patched(fn: Any) -> bool:
    return bool(getattr(fn, "__weft_patched__", False))


def patch_attr(owner: Any, attr: str, make_wrapper: Callable[[Any], Any]) -> bool:
    """Replace ``owner.attr`` with ``make_wrapper(original)`` (idempotent)."""
    if isinstance(owner, type):
        orig = owner.__dict__.get(attr)
        if orig is None:
            orig = getattr(owner, attr, None)
    else:
        orig = getattr(owner, attr, None)
    if orig is None or is_patched(orig):
        return False
    wrapper = make_wrapper(orig)
    wrapper.__weft_patched__ = True
    wrapper.__weft_original__ = orig
    setattr(owner, attr, wrapper)
    _PATCHES.append((owner, attr, orig))
    return True


def unpatch_all() -> None:
    """Restore every method patched by weft integrations."""
    while _UNDO:
        try:
            _UNDO.pop()()
        except Exception:  # pragma: no cover
            pass
    while _PATCHES:
        owner, attr, orig = _PATCHES.pop()
        try:
            setattr(owner, attr, orig)
        except Exception:  # pragma: no cover
            pass


class Accumulator:
    def add(self, chunk: Dict[str, Any]) -> None:  # pragma: no cover - interface
        raise NotImplementedError

    def result(self) -> Any:  # pragma: no cover - interface
        raise NotImplementedError


class LLMSpec:
    """How to trace one LLM client method."""

    def __init__(
        self,
        op_name: str,
        *,
        prepare_kwargs: Optional[Callable[[Dict[str, Any]], Dict[str, Any]]] = None,
        is_stream: Optional[Callable[[Dict[str, Any], Any], bool]] = None,
        accumulator: Optional[Callable[[], Accumulator]] = None,
        record_usage: Optional[Callable[[Any, Any], None]] = None,
        skip_first_arg: bool = True,
        kind: Optional[str] = "llm",
        suppress_nested: bool = False,
        integration: Optional[str] = None,
        arg_names: Tuple[str, ...] = (),
        self_inputs: Optional[Callable[[Any], Dict[str, Any]]] = None,
        attributes: Optional[Callable[[Tuple[Any, ...], Dict[str, Any]], Optional[Dict[str, Any]]]] = None,
    ):
        self.op_name = op_name
        # Integration name for autopatch settings (defaults to the op name prefix).
        self.integration = _settings.canonical(integration or _INTEGRATION_OF.get(op_name.split(".")[0], op_name.split(".")[0]))
        # Names for positional arguments (after ``self``), so inputs are keyed by name.
        self.arg_names = tuple(arg_names)
        # Extra inputs read from the client instance (e.g. the model name held by it).
        self.self_inputs = self_inputs
        # Extra call attributes computed from (args, kwargs), e.g. the provider of an OpenAI-compatible client.
        self.attributes = attributes
        self.prepare_kwargs = prepare_kwargs
        self.is_stream = is_stream or (lambda kw, res: bool(kw.get("stream")))
        self.accumulator = accumulator
        self.record_usage = record_usage
        self.skip_first_arg = skip_first_arg
        self.kind = kind
        # Wrapper libraries (litellm, ...) call provider SDKs we also patch;
        # suppress those nested calls so usage is not counted twice.
        self.suppress_nested = suppress_nested


_INTEGRATION_OF = {"mistralai": "mistral", "google": "google_genai", "bedrock": "bedrock"}


def start_call(client: Any, spec: "LLMSpec", inputs: Dict[str, Any], args: Tuple[Any, ...] = (), kwargs: Optional[Dict[str, Any]] = None) -> Any:
    op = _settings.integration_op(spec.integration, spec.op_name, spec.kind)
    attrs = _settings.integration_attributes(spec.integration)
    if spec.attributes is not None:
        try:
            extra = spec.attributes(args, kwargs or {})
        except Exception:  # pragma: no cover
            extra = None
        if extra:
            for k, v in extra.items():
                if k == "weft" and isinstance(v, dict):
                    attrs["weft"].update(v)
                else:
                    attrs[k] = v
    call = client.create_span(op, inputs, attributes=attrs)
    call._weft_op = op
    return call


def finish_call(client: Any, call: Any, output: Any = None, exception: Optional[BaseException] = None) -> None:
    """Finish an integration call, applying the integration's ``postprocess_output``."""
    client.finish_span(call, output=output, exception=exception, op=getattr(call, "_weft_op", None))


class _Enter:
    """Push the call (and optionally suppress nested tracing) while the client runs."""

    def __init__(self, call: Any, suppress: bool):
        self.call, self.suppress = call, suppress

    def __enter__(self) -> "_Enter":
        self.t1 = push_call(self.call)
        self.t2 = _context.suppress() if self.suppress else None
        return self

    def __exit__(self, *exc: Any) -> None:
        if self.t2 is not None:
            _context.unsuppress(self.t2)
        pop_call(self.t1)


def _tracing(integration: Optional[str] = None) -> Any:
    from .._client import tracing_client

    c = tracing_client()
    if c is None or tracing_suppressed():
        return None
    if integration is not None and not _settings.is_enabled(integration):
        return None
    return c


def _inputs(spec: LLMSpec, args: Tuple[Any, ...], kwargs: Dict[str, Any]) -> Dict[str, Any]:
    inputs: Dict[str, Any] = {}
    if spec.self_inputs is not None and spec.skip_first_arg and args:
        try:
            inputs.update({k: v for k, v in (spec.self_inputs(args[0]) or {}).items() if v is not None})
        except Exception:  # pragma: no cover
            pass
    extra = list(args[1:] if spec.skip_first_arg else args)
    for name in spec.arg_names:
        if not extra:
            break
        inputs[name] = extra.pop(0)
    inputs.update({k: v for k, v in kwargs.items() if not _is_not_given(v)})
    if extra:
        inputs["args"] = extra
    return inputs


def _is_not_given(v: Any) -> bool:
    return type(v).__name__ in ("NotGiven", "Omit")


def _finish(client: Any, call: Any, spec: LLMSpec, out: Any, exc: Optional[BaseException] = None) -> None:
    if spec.record_usage is not None and out is not None:
        try:
            spec.record_usage(call, out)
        except Exception as e:  # pragma: no cover
            logger.debug("weft: usage extraction failed: %s", e)
    finish_call(client, call, out, exc)


class SyncStreamProxy:
    def __init__(self, stream: Any, client: Any, call: Any, spec: LLMSpec):
        self._stream = stream
        self._client = client
        self._call = call
        self._spec = spec
        self._acc = spec.accumulator() if spec.accumulator else None
        self._it: Any = None
        self._done = False

    def _finish(self, exc: Optional[BaseException] = None) -> None:
        if self._done:
            return
        self._done = True
        out = self._acc.result() if self._acc is not None else None
        _finish(self._client, self._call, self._spec, out, exc)

    def __iter__(self) -> "SyncStreamProxy":
        return self

    def __next__(self) -> Any:
        if self._it is None:
            self._it = iter(self._stream)
        try:
            chunk = next(self._it)
        except StopIteration:
            self._finish()
            raise
        except BaseException as e:
            self._finish(e)
            raise
        if self._acc is not None:
            try:
                self._acc.add(to_dict(chunk))
            except Exception as e:  # pragma: no cover
                logger.debug("weft: stream accumulation failed: %s", e)
        return chunk

    def close(self) -> None:
        self._finish()
        fn = getattr(self._stream, "close", None)
        if callable(fn):
            fn()

    def __enter__(self) -> "SyncStreamProxy":
        enter = getattr(self._stream, "__enter__", None)
        if callable(enter):
            enter()
        return self

    def __exit__(self, *exc: Any) -> Any:
        self._finish(exc[1] if exc and isinstance(exc[1], BaseException) else None)
        ex = getattr(self._stream, "__exit__", None)
        return ex(*exc) if callable(ex) else None

    def __getattr__(self, item: str) -> Any:
        return getattr(self._stream, item)


class AsyncStreamProxy:
    def __init__(self, stream: Any, client: Any, call: Any, spec: LLMSpec):
        self._stream = stream
        self._client = client
        self._call = call
        self._spec = spec
        self._acc = spec.accumulator() if spec.accumulator else None
        self._it: Any = None
        self._done = False

    def _finish(self, exc: Optional[BaseException] = None) -> None:
        if self._done:
            return
        self._done = True
        out = self._acc.result() if self._acc is not None else None
        _finish(self._client, self._call, self._spec, out, exc)

    def __aiter__(self) -> "AsyncStreamProxy":
        return self

    async def __anext__(self) -> Any:
        if self._it is None:
            self._it = self._stream.__aiter__()
        try:
            chunk = await self._it.__anext__()
        except StopAsyncIteration:
            self._finish()
            raise
        except BaseException as e:
            self._finish(e)
            raise
        if self._acc is not None:
            try:
                self._acc.add(to_dict(chunk))
            except Exception as e:  # pragma: no cover
                logger.debug("weft: stream accumulation failed: %s", e)
        return chunk

    async def close(self) -> None:
        self._finish()
        fn = getattr(self._stream, "close", None)
        if callable(fn):
            r = fn()
            if inspect.isawaitable(r):
                await r

    async def __aenter__(self) -> "AsyncStreamProxy":
        enter = getattr(self._stream, "__aenter__", None)
        if callable(enter):
            await enter()
        return self

    async def __aexit__(self, *exc: Any) -> Any:
        self._finish(exc[1] if exc and isinstance(exc[1], BaseException) else None)
        ex = getattr(self._stream, "__aexit__", None)
        return (await ex(*exc)) if callable(ex) else None

    def __getattr__(self, item: str) -> Any:
        return getattr(self._stream, item)


def make_wrapper(orig: Any, spec: LLMSpec, is_async: Optional[bool] = None, returns_async_iter: bool = False) -> Any:
    """Wrap an LLM client method so each invocation becomes a traced call.

    ``returns_async_iter`` is for methods that return an async iterator
    without being awaited (async generator methods).
    """
    if is_async is None:
        is_async = inspect.iscoroutinefunction(orig)

    if returns_async_iter:

        @functools.wraps(orig)
        def iwrapper(*args: Any, **kwargs: Any) -> Any:
            client = _tracing(spec.integration)
            if client is None:
                return orig(*args, **kwargs)
            if spec.prepare_kwargs is not None:
                kwargs = spec.prepare_kwargs(dict(kwargs))
            call = start_call(client, spec, _inputs(spec, args, kwargs), args, kwargs)
            try:
                with _Enter(call, spec.suppress_nested):
                    res = orig(*args, **kwargs)
            except BaseException as e:
                finish_call(client, call, None, e)
                raise
            return AsyncStreamProxy(res, client, call, spec)

        return iwrapper

    if is_async:

        @functools.wraps(orig)
        async def awrapper(*args: Any, **kwargs: Any) -> Any:
            client = _tracing(spec.integration)
            if client is None:
                return await orig(*args, **kwargs)
            if spec.prepare_kwargs is not None:
                kwargs = spec.prepare_kwargs(dict(kwargs))
            call = start_call(client, spec, _inputs(spec, args, kwargs), args, kwargs)
            try:
                with _Enter(call, spec.suppress_nested):
                    res = orig(*args, **kwargs)
                    if inspect.isawaitable(res):
                        res = await res
            except BaseException as e:
                finish_call(client, call, None, e)
                raise
            if spec.is_stream(kwargs, res):
                return AsyncStreamProxy(res, client, call, spec)
            _finish(client, call, spec, to_dict(res))
            return res

        return awrapper

    @functools.wraps(orig)
    def wrapper(*args: Any, **kwargs: Any) -> Any:
        client = _tracing(spec.integration)
        if client is None:
            return orig(*args, **kwargs)
        if spec.prepare_kwargs is not None:
            kwargs = spec.prepare_kwargs(dict(kwargs))
        call = start_call(client, spec, _inputs(spec, args, kwargs), args, kwargs)
        try:
            with _Enter(call, spec.suppress_nested):
                res = orig(*args, **kwargs)
        except BaseException as e:
            finish_call(client, call, None, e)
            raise
        if spec.is_stream(kwargs, res):
            return SyncStreamProxy(res, client, call, spec)
        _finish(client, call, spec, to_dict(res))
        return res

    return wrapper
