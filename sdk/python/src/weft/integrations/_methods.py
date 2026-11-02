"""Declarative method patching for framework integrations.

A framework method (``crewai.Crew.kickoff``, ``smolagents.Tool.__call__`` ...)
is wrapped in an integration ``Op`` so its calls are traced with a span kind,
inputs bound to parameter names, sync/async/generator support, and the
integration's autopatch settings. LLM-kind methods may record usage, but only
when no nested call (e.g. a patched provider SDK) was traced under them, so
tokens are never counted twice.
"""

from __future__ import annotations

import contextvars
import functools
import importlib
import inspect
import logging
from dataclasses import dataclass, field
from typing import Any, Callable, Dict, Iterable, List, Optional, Sequence, Tuple

from .. import _context
from . import _settings
from ._common import patch_attr

logger = logging.getLogger("weft")


class Brief:
    """Compact stand-in for ``self`` in recorded inputs (repr only)."""

    __slots__ = ("text",)

    def __init__(self, text: str):
        self.text = text

    def __repr__(self) -> str:
        return self.text


def describe(obj: Any) -> str:
    cls = type(obj).__name__
    for attr in ("name", "role", "agent_name", "model_id", "model", "model_name", "id"):
        try:
            v = getattr(obj, attr, None)
        except Exception:
            v = None
        if isinstance(v, (str, int)) and v != "":
            return f"{cls}({attr}={v!r})"
    return cls


def _brief_self(inputs: Dict[str, Any]) -> Dict[str, Any]:
    if "self" in inputs and not isinstance(inputs["self"], Brief):
        inputs = dict(inputs)
        inputs["self"] = Brief(describe(inputs["self"]))
    return inputs


@dataclass
class Method:
    """One method (``"Class.method"``) or module function (``"function"``) to trace."""

    target: str
    op_name: str
    kind: Optional[str] = None
    module: Optional[str] = None  # defaults to the patch_methods() module
    #: ``callable(call, result)`` recording usage on ``call`` (skipped when the call has children).
    usage: Optional[Callable[[Any, Any], None]] = None
    #: ``callable(call) -> str`` display name.
    display: Any = None
    inputs: Optional[Callable[[Dict[str, Any]], Dict[str, Any]]] = None
    output: Optional[Callable[[Any], Any]] = None
    accumulator: Optional[Callable[[Any, Any], Any]] = None
    #: Also patch subclasses (already defined) that override the method.
    subclasses: bool = False
    #: Keep the bound object as ``self`` (shortened) in the inputs; False drops it.
    keep_self: bool = True
    #: The method is a plain function that returns an iterator: ``"sync"`` or
    #: ``"async"``. The call then stays open until the iterator is exhausted.
    returns_iter: Optional[str] = None
    extra: Dict[str, Any] = field(default_factory=dict)


def _record_usage(m: Method, res: Any) -> None:
    if m.usage is None:
        return
    call = _context.get_current_call()
    if call is None or call._child_count:
        return
    try:
        m.usage(call, res)
    except Exception as e:  # pragma: no cover
        logger.debug("weft: usage extraction failed for %s: %s", m.op_name, e)


def make_op(integration: str, orig: Any, m: Method) -> Any:
    """Return a function that runs ``orig`` as an integration op."""
    is_async = inspect.iscoroutinefunction(inspect.unwrap(orig))

    if m.returns_iter is not None:
        body = _iter_body(orig, m.returns_iter == "async")
        is_async = False
    elif is_async:

        @functools.wraps(orig)
        async def body(*args: Any, **kwargs: Any) -> Any:
            with _outer_attrs():
                res = await orig(*args, **kwargs)
            _record_usage(m, res)
            return res

    elif inspect.isgeneratorfunction(inspect.unwrap(orig)) or inspect.isasyncgenfunction(inspect.unwrap(orig)):
        body = orig  # iterated after the attribute scope has ended
    else:

        @functools.wraps(orig)
        def body(*args: Any, **kwargs: Any) -> Any:
            with _outer_attrs():
                res = orig(*args, **kwargs)
            _record_usage(m, res)
            return res

    pre = m.inputs
    if m.keep_self:
        pre_inputs = (lambda d: _brief_self(pre(d))) if pre else _brief_self
    else:

        def pre_inputs(d: Dict[str, Any]) -> Dict[str, Any]:
            d = {k: v for k, v in d.items() if k != "self"}
            return pre(d) if pre else d

    op = _settings.integration_op(
        integration,
        m.op_name,
        m.kind,
        body,
        display=m.display,
        postprocess_inputs=pre_inputs,
        postprocess_output=m.output,
        accumulator=m.accumulator,
    )
    def run(*args: Any, **kwargs: Any) -> Any:
        if not _settings.is_enabled(integration):
            return orig(*args, **kwargs)
        with _context_attrs(_settings.integration_attributes(integration)):
            return op(*args, **kwargs)

    if is_async:

        @functools.wraps(orig)
        async def wrapper(*args: Any, **kwargs: Any) -> Any:
            if not _settings.is_enabled(integration):
                return await orig(*args, **kwargs)
            with _context_attrs(_settings.integration_attributes(integration)):
                return await op(*args, **kwargs)

    else:

        @functools.wraps(orig)
        def wrapper(*args: Any, **kwargs: Any) -> Any:
            return run(*args, **kwargs)

    wrapper.__weft_op__ = op  # type: ignore[attr-defined]
    return wrapper


def _iter_body(orig: Any, is_async: bool) -> Any:
    if is_async:

        async def abody(*args: Any, **kwargs: Any) -> Any:
            res = orig(*args, **kwargs)
            if inspect.isawaitable(res):
                res = await res
            if hasattr(res, "__aiter__"):
                async for item in res:
                    yield item
            else:
                for item in res:
                    yield item

        body: Any = abody
    else:

        def gbody(*args: Any, **kwargs: Any) -> Any:
            yield from orig(*args, **kwargs)

        body = gbody
    # keep the signature for input binding, but not __wrapped__ (the op must see a generator)
    try:
        body.__signature__ = inspect.signature(orig)
    except (TypeError, ValueError):  # pragma: no cover
        pass
    body.__name__ = getattr(orig, "__name__", "fn")
    body.__qualname__ = getattr(orig, "__qualname__", body.__name__)
    return body


_OUTER: "contextvars.ContextVar[Optional[Dict[str, Any]]]" = contextvars.ContextVar("weft_integration_outer_attrs", default=None)


class _context_attrs:
    """Stamp the integration attributes on the op's own call only.

    The attributes are set while the op creates its call; ``_outer_attrs``
    restores the caller's attributes while the wrapped method runs, so calls
    nested inside (user ops, provider SDKs) do not inherit them.
    """

    __slots__ = ("attrs", "token", "otoken")

    def __init__(self, attrs: Dict[str, Any]):
        self.attrs = attrs

    def __enter__(self) -> None:
        cur = _context._attributes.get()
        merged = dict(cur)
        w = dict(cur.get("weft") or {}) if isinstance(cur.get("weft"), dict) else {}
        w.update(self.attrs.get("weft") or {})
        merged.update({k: v for k, v in self.attrs.items() if k != "weft"})
        merged["weft"] = w
        self.otoken = _OUTER.set(cur)
        self.token = _context._attributes.set(merged)

    def __exit__(self, *exc: Any) -> None:
        for var, tok in ((_context._attributes, self.token), (_OUTER, self.otoken)):
            try:
                var.reset(tok)
            except ValueError:  # pragma: no cover - reset from another context
                pass


class _outer_attrs:
    __slots__ = ("token",)

    def __enter__(self) -> None:
        outer = _OUTER.get()
        self.token = _context._attributes.set(outer) if outer is not None else None

    def __exit__(self, *exc: Any) -> None:
        if self.token is not None:
            try:
                _context._attributes.reset(self.token)
            except ValueError:  # pragma: no cover
                pass


def _make_any(integration: str, orig: Any, m: Method) -> Any:
    if isinstance(orig, staticmethod):
        return staticmethod(make_op(integration, orig.__func__, m))
    if isinstance(orig, classmethod):
        return classmethod(make_op(integration, orig.__func__, m))
    return make_op(integration, orig, m)


def _resolve(module: Any, target: str) -> Tuple[Any, str]:
    owner = module
    parts = target.split(".")
    for p in parts[:-1]:
        owner = getattr(owner, p)
    return owner, parts[-1]


def _subclasses(cls: type) -> List[type]:
    out: List[type] = []
    seen = set()
    stack = list(cls.__subclasses__())
    while stack:
        c = stack.pop()
        if c in seen:
            continue
        seen.add(c)
        out.append(c)
        try:
            stack.extend(c.__subclasses__())
        except TypeError:  # pragma: no cover
            pass
    return out


def patch_methods(integration: str, module: Optional[str], methods: Iterable[Method]) -> bool:
    """Patch every method that exists; missing modules/attributes are skipped."""
    patched = False
    mods: Dict[str, Any] = {}
    for m in methods:
        modname = m.module or module
        if modname is None:
            continue
        if modname not in mods:
            try:
                mods[modname] = importlib.import_module(modname)
            except Exception:
                mods[modname] = None
        mod = mods[modname]
        if mod is None:
            continue
        try:
            owner, attr = _resolve(mod, m.target)
        except AttributeError:
            continue
        if not hasattr(owner, attr):
            continue
        owners = [owner]
        if m.subclasses and isinstance(owner, type):
            owners += [c for c in _subclasses(owner) if attr in c.__dict__]
        for o in owners:
            if isinstance(o, type) and attr not in o.__dict__ and o is not owner:
                continue
            try:
                patched |= patch_attr(o, attr, lambda orig, mm=m: _make_any(integration, orig, mm))
            except (TypeError, AttributeError) as e:  # e.g. builtin/extension types
                logger.debug("weft: cannot patch %s.%s: %s", getattr(o, "__name__", o), attr, e)
    return patched


def first_present(module: str, names: Sequence[str]) -> Optional[Any]:
    try:
        mod = importlib.import_module(module)
    except Exception:
        return None
    for n in names:
        v = getattr(mod, n, None)
        if v is not None:
            return v
    return None
