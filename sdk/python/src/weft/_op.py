"""The ``@weft.op`` decorator: traced functions, methods, coroutines and generators."""

from __future__ import annotations

import functools
import inspect
import logging
import random
import textwrap
from typing import Any, Callable, Dict, Optional, Tuple, TypeVar, Union, overload

from . import _context

logger = logging.getLogger("weft")

F = TypeVar("F", bound=Callable[..., Any])

SYNC, ASYNC, GEN, AGEN = "sync", "async", "gen", "agen"

#: Common values for ``@weft.op(kind=...)`` (any string is accepted).
OP_KINDS = ("agent", "llm", "tool", "chain", "retriever", "embedding", "scorer", "search", "guardrail", "reranker", "workflow")
#: Values for ``@weft.op(color=...)``.
OP_COLORS = ("red", "orange", "yellow", "green", "blue", "purple")
CODE_CAPTURE_DISABLED = "# Code capture disabled\n"


def _kind_of(fn: Callable[..., Any]) -> str:
    target = inspect.unwrap(fn) if not isinstance(fn, functools.partial) else fn.func
    if inspect.isasyncgenfunction(target):
        return AGEN
    if inspect.isgeneratorfunction(target):
        return GEN
    if inspect.iscoroutinefunction(target):
        return ASYNC
    return SYNC


def _default_name(fn: Callable[..., Any]) -> str:
    qual = getattr(fn, "__qualname__", None) or getattr(fn, "__name__", None) or type(fn).__name__
    if "<locals>." in qual:
        qual = qual.rsplit("<locals>.", 1)[1]
    return qual


def _source(fn: Callable[..., Any]) -> str:
    try:
        return textwrap.dedent(inspect.getsource(fn))
    except (OSError, TypeError):
        return ""


def _signature_str(sig: Optional[inspect.Signature]) -> str:
    if sig is None:
        return "(...)"
    try:
        return str(sig)
    except Exception:  # pragma: no cover
        return "(...)"


def _list_accumulator(acc: Any, item: Any) -> Any:
    if acc is None:
        acc = []
    acc.append(item)
    return acc


class Op:
    """A traced function. Created by ``@weft.op``.

    Calling the op runs the function and records a call when a client is
    initialised. ``op.call(...)`` returns ``(result, Call)`` and never raises
    (an exception is recorded on the returned call and the result is None).
    """

    def __init__(
        self,
        fn: Callable[..., Any],
        *,
        name: Optional[str] = None,
        call_display_name: Optional[Union[str, Callable[[Any], str]]] = None,
        postprocess_inputs: Optional[Callable[[Dict[str, Any]], Dict[str, Any]]] = None,
        postprocess_output: Optional[Callable[[Any], Any]] = None,
        tracing_sample_rate: float = 1.0,
        accumulator: Optional[Callable[[Any, Any], Any]] = None,
        code: Optional[str] = None,
        kind: Optional[str] = None,
        color: Optional[str] = None,
        enable_code_capture: bool = True,
    ):
        if not callable(fn):
            raise TypeError("weft.op requires a callable")
        if not 0.0 <= float(tracing_sample_rate) <= 1.0:
            raise ValueError("tracing_sample_rate must be between 0 and 1")
        self.resolve_fn = fn
        self.name = name or _default_name(fn)
        self.call_display_name = call_display_name
        self.postprocess_inputs = postprocess_inputs
        self.postprocess_output = postprocess_output
        self.tracing_sample_rate = float(tracing_sample_rate)
        self.accumulator = accumulator
        self._code = code
        self.kind = kind
        self.color = color
        self.enable_code_capture = enable_code_capture
        self._kind = _kind_of(fn)
        try:
            self.signature: Optional[inspect.Signature] = inspect.signature(fn)
        except (TypeError, ValueError):
            self.signature = None
        self._refs: Dict[str, Any] = {}
        functools.update_wrapper(self, fn)
        if self._kind == ASYNC and hasattr(inspect, "markcoroutinefunction"):
            inspect.markcoroutinefunction(self)

    # -- descriptor protocol (methods) ------------------------------------
    def __get__(self, obj: Any, objtype: Any = None) -> Any:
        if obj is None:
            return self
        return BoundOp(self, obj)

    def __set_name__(self, owner: type, name: str) -> None:
        self._owner_name = owner.__name__

    def __call__(self, *args: Any, **kwargs: Any) -> Any:
        return _execute(self, args, kwargs, return_call=False)

    def call(self, *args: Any, **kwargs: Any) -> Any:
        """Run the op and return ``(result, Call)``. Exceptions are captured."""
        return _execute(self, args, kwargs, return_call=True)

    @property
    def ref(self) -> Any:
        from ._client import get_client

        client = get_client()
        return client._op_ref(self) if client is not None else None

    def op_val(self, capture_code: bool = True) -> Dict[str, Any]:
        if self._code is not None:
            code = self._code
        elif capture_code and self.enable_code_capture:
            code = _source(self.resolve_fn)
        else:
            code = CODE_CAPTURE_DISABLED
        return {
            "_type": "Op",
            "name": self.name,
            "code": code,
            "language": "python",
            "signature": _signature_str(self.signature),
        }

    def __repr__(self) -> str:
        return f"<weft.Op {self.name}>"


class BoundOp:
    """An op bound to an instance (``model.predict``)."""

    def __init__(self, op: Op, obj: Any):
        self.op = op
        self.obj = obj
        self.__name__ = getattr(op, "__name__", op.name)
        self.__doc__ = op.__doc__
        if op._kind == ASYNC and hasattr(inspect, "markcoroutinefunction"):
            inspect.markcoroutinefunction(self)

    @property
    def __signature__(self) -> Optional[inspect.Signature]:
        sig = self.op.signature
        if sig is None:
            return None
        params = list(sig.parameters.values())
        return sig.replace(parameters=params[1:]) if params else sig

    def __call__(self, *args: Any, **kwargs: Any) -> Any:
        return _execute(self.op, (self.obj,) + args, kwargs, return_call=False)

    def call(self, *args: Any, **kwargs: Any) -> Any:
        return _execute(self.op, (self.obj,) + args, kwargs, return_call=True)

    @property
    def name(self) -> str:
        return self.op.name

    def __getattr__(self, item: str) -> Any:
        return getattr(self.op, item)

    def __repr__(self) -> str:
        return f"<weft.Op {self.op.name} bound to {type(self.obj).__name__}>"


@overload
def op(fn: F) -> F: ...


@overload
def op(
    fn: None = None,
    *,
    name: Optional[str] = None,
    call_display_name: Optional[Union[str, Callable[[Any], str]]] = None,
    postprocess_inputs: Optional[Callable[[Dict[str, Any]], Dict[str, Any]]] = None,
    postprocess_output: Optional[Callable[[Any], Any]] = None,
    tracing_sample_rate: float = 1.0,
    accumulator: Optional[Callable[[Any, Any], Any]] = None,
    kind: Optional[str] = None,
    color: Optional[str] = None,
    enable_code_capture: bool = True,
) -> Callable[[F], F]: ...


def op(fn: Any = None, **kwargs: Any) -> Any:
    """Decorate a function or method so its calls are traced.

    Works with sync and async functions, generators and async generators
    (yielded items are accumulated into the call output), and methods (``self``
    is recorded as a ref when the object has been published).

    ``kind`` (agent, llm, tool, chain, retriever, embedding, scorer, ...) and
    ``color`` are stored on every call as ``attributes.weft.span_kind`` /
    ``attributes.weft.color``.
    """
    if fn is None:
        return lambda f: Op(f, **kwargs)
    if isinstance(fn, Op):
        return fn
    return Op(fn, **kwargs)


def as_op(fn: Any) -> Union[Op, BoundOp]:
    if isinstance(fn, (Op, BoundOp)):
        return fn
    return Op(fn)


# ---------------------------------------------------------------------------
# execution


def _bind_inputs(op_: Op, args: Tuple[Any, ...], kwargs: Dict[str, Any]) -> Dict[str, Any]:
    sig = op_.signature
    if sig is None:
        return {"args": list(args), **kwargs} if args else dict(kwargs)
    try:
        bound = sig.bind(*args, **kwargs)
    except TypeError:
        return {"args": list(args), **kwargs} if args else dict(kwargs)
    bound.apply_defaults()
    inputs: Dict[str, Any] = {}
    for pname, value in bound.arguments.items():
        param = sig.parameters[pname]
        if param.kind == inspect.Parameter.VAR_KEYWORD:
            inputs.update(value)
        elif param.kind == inspect.Parameter.VAR_POSITIONAL:
            inputs[pname] = list(value)
        else:
            inputs[pname] = value
    return inputs


class _Tracer:
    """Hooks used by the executor for traced/untraced execution."""

    def __init__(self, client: Any, call: Any, op_: Op, return_call: bool):
        self.client = client
        self.call = call
        self.op = op_
        self.return_call = return_call
        self.token: Any = None

    def enter(self) -> None:
        if self.call is not None:
            self.token = _context.push_call(self.call)
        elif self.client == "suppress":
            self.token = _context.suppress()

    def exit(self) -> None:
        if self.token is None:
            return
        if self.call is not None:
            _context.pop_call(self.token)
        else:
            _context.unsuppress(self.token)
        self.token = None

    def ok(self, output: Any) -> None:
        if self.call is not None:
            self.client.finish_call(self.call, output=output, op=self.op)

    def err(self, exc: BaseException) -> None:
        if self.call is not None:
            self.client.finish_call(self.call, exception=exc, op=self.op)

    def result(self, value: Any) -> Any:
        return (value, self.call) if self.return_call else value


def _make_tracer(op_: Op, args: Tuple[Any, ...], kwargs: Dict[str, Any], return_call: bool) -> _Tracer:
    from ._client import tracing_client

    client = tracing_client()
    if client is None or _context.tracing_suppressed():
        return _Tracer(None, None, op_, return_call)
    parent = _context.get_current_call()
    if parent is None and op_.tracing_sample_rate < 1.0 and random.random() >= op_.tracing_sample_rate:
        return _Tracer("suppress", None, op_, return_call)
    try:
        inputs = _bind_inputs(op_, args, kwargs)
        call = client.create_call(op_, inputs, parent=parent, _from_op=True)
    except Exception as e:  # never break user code because of tracing
        logger.warning("weft: failed to start call for %s: %s", op_.name, e)
        return _Tracer(None, None, op_, return_call)
    return _Tracer(client, call, op_, return_call)


def _execute(op_: Op, args: Tuple[Any, ...], kwargs: Dict[str, Any], *, return_call: bool) -> Any:
    kind = op_._kind
    fn = op_.resolve_fn
    if kind == ASYNC:
        return _run_async(op_, fn, args, kwargs, return_call)
    tracer = _make_tracer(op_, args, kwargs, return_call)
    if kind == GEN:
        return tracer.result(_run_gen(tracer, fn, args, kwargs))
    if kind == AGEN:
        return tracer.result(_run_agen(tracer, fn, args, kwargs))
    tracer.enter()
    try:
        res = fn(*args, **kwargs)
    except BaseException as e:
        tracer.exit()
        tracer.err(e)
        if return_call and isinstance(e, Exception):
            return (None, tracer.call)
        raise
    tracer.exit()
    tracer.ok(res)
    return tracer.result(res)


async def _run_async(op_: Op, fn: Callable[..., Any], args: Tuple[Any, ...], kwargs: Dict[str, Any], return_call: bool) -> Any:
    tracer = _make_tracer(op_, args, kwargs, return_call)
    tracer.enter()
    try:
        res = await fn(*args, **kwargs)
    except BaseException as e:
        tracer.exit()
        tracer.err(e)
        if return_call and isinstance(e, Exception):
            return (None, tracer.call)
        raise
    tracer.exit()
    tracer.ok(res)
    return tracer.result(res)


def _accumulate(tracer: _Tracer, acc: Any, item: Any) -> Any:
    fn = tracer.op.accumulator or _list_accumulator
    try:
        return fn(acc, item)
    except Exception as e:  # pragma: no cover
        logger.debug("weft accumulator failed: %s", e)
        return acc


def _run_gen(tracer: _Tracer, fn: Callable[..., Any], args: Tuple[Any, ...], kwargs: Dict[str, Any]) -> Any:
    def wrapper() -> Any:
        acc: Any = None
        tracer.enter()
        try:
            it = fn(*args, **kwargs)
        except BaseException as e:
            tracer.exit()
            tracer.err(e)
            raise
        tracer.exit()
        done = False
        try:
            while True:
                tracer.enter()
                try:
                    item = next(it)
                except StopIteration:
                    break
                finally:
                    tracer.exit()
                acc = _accumulate(tracer, acc, item)
                yield item
        except GeneratorExit:
            done = True
            try:
                it.close()
            finally:
                tracer.ok(acc if acc is not None else [])
            raise
        except BaseException as e:
            done = True
            tracer.err(e)
            raise
        if not done:
            tracer.ok(acc if acc is not None else [])

    return wrapper()


def _run_agen(tracer: _Tracer, fn: Callable[..., Any], args: Tuple[Any, ...], kwargs: Dict[str, Any]) -> Any:
    async def wrapper() -> Any:
        acc: Any = None
        tracer.enter()
        try:
            it = fn(*args, **kwargs)
        except BaseException as e:
            tracer.exit()
            tracer.err(e)
            raise
        tracer.exit()
        done = False
        try:
            while True:
                tracer.enter()
                try:
                    item = await it.__anext__()
                except StopAsyncIteration:
                    break
                finally:
                    tracer.exit()
                acc = _accumulate(tracer, acc, item)
                yield item
        except GeneratorExit:
            done = True
            try:
                await it.aclose()
            finally:
                tracer.ok(acc if acc is not None else [])
            raise
        except BaseException as e:
            done = True
            tracer.err(e)
            raise
        if not done:
            tracer.ok(acc if acc is not None else [])

    return wrapper()
