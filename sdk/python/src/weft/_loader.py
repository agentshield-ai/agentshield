"""Rebuild runnable ops and objects from their stored values.

SECURITY: loading an op executes the Python source stored on the server
(``exec`` in a fresh namespace). Only load ops from servers and projects
you trust.
"""

from __future__ import annotations

import ast
import logging
import textwrap
from typing import TYPE_CHECKING, Any, Dict, Optional

from ._op import Op
from ._refs import ObjectRef, OpRef

if TYPE_CHECKING:  # pragma: no cover
    from ._client import WeftClient

logger = logging.getLogger("weft")


class OpLoadError(ValueError):
    """The stored code of an op could not be turned back into a function."""


def load_op(val: Dict[str, Any], ref: Optional[ObjectRef], client: "WeftClient") -> Op:
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

    namespace: Dict[str, Any] = {"__name__": f"weft_loaded.{func_name}", "weft": weft, "op": weft.op}
    try:
        exec(compile(tree, f"<weft op {where}>", "exec"), namespace)  # noqa: S102 - documented behaviour
    except Exception as e:
        raise OpLoadError(f"cannot load op {where}: executing stored code failed: {type(e).__name__}: {e}") from None
    fn = namespace.get(target.name)
    if not callable(fn):
        raise OpLoadError(f"cannot load op {where}: function {target.name!r} not defined by stored code")
    op = Op(fn, name=name, code=code)
    if ref is not None:
        op._refs[(client.server_url, client.project_id)] = OpRef(ref.entity, ref.project, ref.name, ref.digest)
    return op


def load_dynamic_object(val: Dict[str, Any], client: "WeftClient", ref: Optional[ObjectRef]) -> Any:
    """Build an instance of a class not defined locally (Model/Scorer subclasses).

    Fields become pydantic fields; op refs become loaded ops (methods).
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
    ops: Dict[str, Op] = {}
    fields: Dict[str, Any] = {}
    for k, v in val.items():
        if k in ("_type", "_class_name", "_bases"):
            continue
        if isinstance(v, str) and is_ref_uri(v):
            r = parse_uri(v)
            if isinstance(r, OpRef):
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
