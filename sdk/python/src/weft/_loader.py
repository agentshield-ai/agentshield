"""Rebuild runnable ops and objects from their stored values.

SECURITY: loading an op executes the Python source stored on the server
(``exec`` in a fresh namespace). Anyone who can write to the project can
store code there, so the SDK never does this on its own: ``weft.get()`` of a
function returns an inert :class:`StoredOp`, and building a callable needs an
explicit opt-in (``weft.load_op(ref, trust_code=True)``,
``weft.get(ref, trust_code=True)`` or ``WEFT_ALLOW_CODE_LOAD=1``).
"""

from __future__ import annotations

import ast
import contextlib
import contextvars
import logging
import os
import textwrap
from typing import TYPE_CHECKING, Any, Dict, Iterator, Optional

from ._op import Op
from ._refs import FunctionRef, ObjectRef

if TYPE_CHECKING:  # pragma: no cover
    from ._client import WeftClient

logger = logging.getLogger("weft")


class OpLoadError(ValueError):
    """The stored code of an op could not be turned back into a function (or loading it was not allowed)."""


_TRUST_CODE: "contextvars.ContextVar[bool]" = contextvars.ContextVar("weft_trust_code", default=False)

_NOT_TRUSTED = (
    "stored code is not run unless you opt in: use weft.load_op(ref, trust_code=True) "
    "(or weft.get(ref, trust_code=True), or set WEFT_ALLOW_CODE_LOAD=1) for functions from projects you trust"
)


def code_load_allowed(trust_code: Optional[bool] = None) -> bool:
    """Whether stored function code may be executed: an explicit ``trust_code=True``,
    a surrounding ``get(..., trust_code=True)``, or ``WEFT_ALLOW_CODE_LOAD=1``."""
    if trust_code:
        return True
    if _TRUST_CODE.get():
        return True
    return os.environ.get("WEFT_ALLOW_CODE_LOAD", "").strip().lower() in ("1", "true", "yes", "on")


@contextlib.contextmanager
def trusting_code(trust: bool) -> Iterator[None]:
    """Allow stored code to load for the duration of the block (no-op when ``trust`` is false)."""
    if not trust:
        yield
        return
    tok = _TRUST_CODE.set(True)
    try:
        yield
    finally:
        _TRUST_CODE.reset(tok)


class StoredOp:
    """A stored function version, as data: its name, source code and ref.

    ``weft.get()`` returns this for function refs instead of executing the
    stored code. Calling it raises :class:`OpLoadError`; to run it, load it
    explicitly with ``weft.load_op(ref, trust_code=True)`` (or
    ``stored.load(trust_code=True)``) if you trust the project it came from.
    """

    __slots__ = ("name", "code", "language", "signature", "value", "_weft_ref")

    def __init__(self, val: Dict[str, Any], ref: Optional[ObjectRef] = None):
        self.value: Dict[str, Any] = dict(val or {})
        self.name: str = str(self.value.get("name") or (ref.name if ref else ""))
        self.code: str = str(self.value.get("code") or "")
        self.language: str = str(self.value.get("language") or "python")
        sig = self.value.get("signature")
        self.signature: Optional[str] = str(sig) if sig is not None else None
        self._weft_ref: Optional[ObjectRef] = ref

    @property
    def ref(self) -> Optional[ObjectRef]:
        return self._weft_ref

    def load(self, *, trust_code: bool = False) -> Op:
        """Build a runnable :class:`weft.Op` from the stored code (needs ``trust_code=True``)."""
        from ._client import require_client

        if not code_load_allowed(trust_code):
            raise OpLoadError(f"cannot load op {self._where()}: {_NOT_TRUSTED}")
        return _load_op(self.value, self._weft_ref, require_client())

    def _where(self) -> str:
        return self._weft_ref.uri() if self._weft_ref is not None else self.name

    def __call__(self, *args: Any, **kwargs: Any) -> Any:
        raise OpLoadError(f"{self._where()} is a stored function, not loaded: {_NOT_TRUSTED}")

    def __eq__(self, other: Any) -> bool:
        return isinstance(other, StoredOp) and other.value == self.value and other._weft_ref == self._weft_ref

    __hash__ = None  # type: ignore[assignment]

    def __repr__(self) -> str:
        return f"StoredOp(name={self.name!r}, language={self.language!r}, ref={self._where()!r})"


def load_op(val: Dict[str, Any], ref: Optional[ObjectRef], client: "WeftClient", trust_code: Optional[bool] = None) -> Any:
    """The stored op ``val``: a runnable :class:`Op` when code loading is allowed, else a :class:`StoredOp`."""
    if not code_load_allowed(trust_code):
        return StoredOp(val, ref)
    return _load_op(val, ref, client)


def _load_op(val: Dict[str, Any], ref: Optional[ObjectRef], client: "WeftClient") -> Op:
    name = str(val.get("name") or (ref.name if ref else ""))
    code = val.get("code") or ""
    where = ref.uri() if ref is not None else name
    lang = val.get("language", "python")
    if lang != "python":
        raise OpLoadError(f"cannot load op {where}: stored code is {lang!r}, not python")
    func_name = name.rsplit(".", 1)[-1]
    try:
        tree = ast.parse(textwrap.dedent(code))
    except SyntaxError as e:
        raise OpLoadError(f"cannot load op {where}: stored code does not parse ({e.msg})") from None
    target = None
    for node in tree.body:
        if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)):
            if node.name == func_name or target is None:
                target = node
                if node.name == func_name:
                    break
    if target is None:
        raise OpLoadError(f"cannot load op {where}: no function definition found in stored code")
    target.decorator_list = []  # re-wrapped below; decorators may not be importable
    import weft

    from ._op import trace

    namespace: Dict[str, Any] = {"__name__": f"weft_loaded.{func_name}", "weft": weft, "trace": trace, "op": trace}
    try:
        exec(compile(tree, f"<weft op {where}>", "exec"), namespace)  # noqa: S102 - documented behaviour
    except Exception as e:
        raise OpLoadError(f"cannot load op {where}: executing stored code failed: {type(e).__name__}: {e}") from None
    fn = namespace.get(target.name)
    if not callable(fn):
        raise OpLoadError(f"cannot load op {where}: function {target.name!r} not defined by stored code")
    op = Op(fn, name=name, code=code)
    if ref is not None:
        op._refs[(client.server_url, client.project_id)] = FunctionRef(ref.entity, ref.project, ref.name, ref.digest)
    return op


def load_dynamic_object(val: Dict[str, Any], client: "WeftClient", ref: Optional[ObjectRef]) -> Any:
    """Build an instance of a class not defined locally (Model/Scorer subclasses).

    Fields become pydantic fields; function refs become loaded ops (methods)
    when code loading is allowed (see :func:`code_load_allowed`), and inert
    :class:`StoredOp` values otherwise.
    """
    from pydantic import create_model

    from ._object import Object, _is_obj_or_table_ref, registered_class, resolve_ref_value
    from ._refs import is_ref_uri, parse_uri

    cls_name = str(val.get("_class_name"))
    base: type = Object
    for b in val.get("_bases") or []:
        c = registered_class(b)
        if c is not None and isinstance(c, type) and issubclass(c, Object):
            base = c
            break
    ops: Dict[str, Any] = {}
    fields: Dict[str, Any] = {}
    for k, v in val.items():
        if k in ("_type", "_class_name", "_bases"):
            continue
        if isinstance(v, str) and is_ref_uri(v):
            r = parse_uri(v)
            if isinstance(r, FunctionRef):
                ops[k] = load_op(client._read_obj_val(r), r, client)
                continue
            if _is_obj_or_table_ref(v):
                v = resolve_ref_value(r, client)
        elif isinstance(v, list):
            v = [resolve_ref_value(parse_uri(x), client) if _is_obj_or_table_ref(x) else x for x in v]
        fields[k] = v
    new_fields = {k: (Any, None) for k in fields if k not in base.model_fields}
    dyn = create_model(cls_name, __base__=base, __module__="weft_loaded", **new_fields)  # type: ignore[call-overload]
    for k, o in ops.items():
        setattr(dyn, k, o)
    obj = dyn(**fields)
    obj._weft_ref = ref
    return obj
