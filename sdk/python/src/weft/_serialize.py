"""Conversion of arbitrary Python values to JSON-safe data for the server."""

from __future__ import annotations

import base64
import dataclasses
import datetime
import decimal
import enum
import io
import math
import pathlib
import uuid
from typing import TYPE_CHECKING, Any, Dict, Optional, Set

from ._refs import Ref

if TYPE_CHECKING:  # pragma: no cover
    from ._client import WeftClient

MAX_DEPTH = 64


def sniff_mime(data: bytes) -> str:
    if data.startswith(b"\x89PNG\r\n\x1a\n"):
        return "image/png"
    if data.startswith(b"\xff\xd8\xff"):
        return "image/jpeg"
    if data.startswith((b"GIF87a", b"GIF89a")):
        return "image/gif"
    if data[:4] == b"RIFF" and data[8:12] == b"WEBP":
        return "image/webp"
    if data[:4] == b"RIFF" and data[8:12] == b"WAVE":
        return "audio/wav"
    if data.startswith(b"ID3") or data[:2] in (b"\xff\xfb", b"\xff\xf3", b"\xff\xf2"):
        return "audio/mpeg"
    if data.startswith(b"OggS"):
        return "audio/ogg"
    if data.startswith(b"%PDF"):
        return "application/pdf"
    return "application/octet-stream"


def media_type_for(mime: str) -> str:
    if mime.startswith("image/"):
        return "Image"
    if mime.startswith("audio/"):
        return "Audio"
    return "File"


def _is_pil_image(v: Any) -> bool:
    mod = type(v).__module__ or ""
    return mod.startswith("PIL.") and hasattr(v, "save") and hasattr(v, "mode")


def pil_to_png(img: Any) -> bytes:
    buf = io.BytesIO()
    img.save(buf, format="PNG")
    return buf.getvalue()


def bytes_to_media(data: bytes, client: Optional["WeftClient"], mime: Optional[str] = None) -> Any:
    mime = mime or sniff_mime(data)
    if client is None:
        return f"data:{mime};base64," + base64.b64encode(data).decode("ascii")
    file_digest = client._upload_file(data, mime)
    return {"_type": media_type_for(mime), "file_digest": file_digest, "mime_type": mime}


def to_json(
    value: Any,
    client: Optional["WeftClient"] = None,
    *,
    publish_nested: bool = False,
    _depth: int = 0,
    _seen: Optional[Set[int]] = None,
) -> Any:
    """Convert ``value`` into JSON-safe data.

    Published objects, ops, refs and dataset rows become ref strings; bytes and
    images are uploaded as files (``{"_type": "Image", "file_digest", ...}``);
    pydantic models and dataclasses become dicts; anything unknown becomes
    ``repr(value)``. With ``publish_nested`` unpublished ``weft.Object``
    values are published first so they too become refs.
    """
    from ._object import Object, Table
    from ._op import BoundOp, Op

    if value is None or isinstance(value, (bool, str)):
        return value
    if isinstance(value, int) and not isinstance(value, bool):
        return value
    if isinstance(value, float):
        if math.isnan(value) or math.isinf(value):
            return repr(value)
        return value
    if _depth > MAX_DEPTH:
        return repr(value)
    if isinstance(value, Ref):
        return value.uri()

    ref = _get_ref(value)
    if ref is not None:
        return ref.uri()

    if isinstance(value, (Op, BoundOp)):
        op = value if isinstance(value, Op) else value.op
        if client is None:
            return op.name
        return client._op_ref(op).uri()

    seen = _seen if _seen is not None else set()
    oid = id(value)
    if oid in seen:
        return "<circular reference>"

    def rec(v: Any) -> Any:
        return to_json(v, client, publish_nested=publish_nested, _depth=_depth + 1, _seen=seen)

    if isinstance(value, Object):
        if publish_nested and client is not None:
            return client.publish(value).uri()
        seen.add(oid)
        try:
            return object_val(value, client, publish_nested=False, _depth=_depth + 1, _seen=seen)
        finally:
            seen.discard(oid)

    if isinstance(value, Table):
        if publish_nested and client is not None:
            return client._publish_table(value).uri()
        seen.add(oid)
        try:
            return [rec(r) for r in value.rows]
        finally:
            seen.discard(oid)

    if isinstance(value, (bytes, bytearray, memoryview)):
        return bytes_to_media(bytes(value), client)
    if _is_pil_image(value):
        try:
            return bytes_to_media(pil_to_png(value), client, "image/png")
        except Exception:
            return repr(value)

    if isinstance(value, dict):
        seen.add(oid)
        try:
            return {str(k) if not isinstance(k, enum.Enum) else str(k.value): rec(v) for k, v in value.items()}
        finally:
            seen.discard(oid)
    if isinstance(value, (list, tuple, set, frozenset)):
        seen.add(oid)
        try:
            items = list(value)
            if isinstance(value, (set, frozenset)):
                try:
                    items = sorted(items)
                except TypeError:
                    pass
            return [rec(v) for v in items]
        finally:
            seen.discard(oid)

    if isinstance(value, enum.Enum):
        return rec(value.value)
    if isinstance(value, datetime.datetime):
        if value.tzinfo is None:
            return value.isoformat()
        return value.astimezone(datetime.timezone.utc).isoformat().replace("+00:00", "Z")
    if isinstance(value, (datetime.date, datetime.time)):
        return value.isoformat()
    if isinstance(value, datetime.timedelta):
        return value.total_seconds()
    if isinstance(value, uuid.UUID):
        return str(value)
    if isinstance(value, decimal.Decimal):
        f = float(value)
        return f if math.isfinite(f) else str(value)
    if isinstance(value, pathlib.PurePath):
        return str(value)
    if isinstance(value, complex):
        return str(value)
    if isinstance(value, BaseException):
        return {"type": type(value).__name__, "message": str(value)}

    # pydantic models (incl. OpenAI/Anthropic response objects)
    if hasattr(value, "model_dump") and hasattr(type(value), "model_fields"):
        seen.add(oid)
        try:
            try:
                dumped = value.model_dump(mode="python")
            except Exception:
                dumped = {k: getattr(value, k, None) for k in type(value).model_fields}
            return rec(dumped)
        finally:
            seen.discard(oid)
    if dataclasses.is_dataclass(value) and not isinstance(value, type):
        seen.add(oid)
        try:
            return {f.name: rec(getattr(value, f.name)) for f in dataclasses.fields(value)}
        finally:
            seen.discard(oid)

    mod = type(value).__module__ or ""
    if mod.startswith("numpy"):
        if hasattr(value, "tolist"):
            return rec(value.tolist())
        if hasattr(value, "item"):
            return rec(value.item())
    if mod.startswith("pandas") and hasattr(value, "to_dict"):
        try:
            return rec(value.to_dict(orient="records"))
        except Exception:
            pass
    for meth in ("to_dict", "dict"):
        fn = getattr(value, meth, None)
        if callable(fn) and not isinstance(value, type):
            try:
                d = fn()
                if isinstance(d, dict):
                    return rec(d)
            except Exception:
                pass
    if isinstance(value, type):
        return f"{value.__module__}.{value.__qualname__}"
    try:
        return repr(value)
    except Exception:
        return f"<{type(value).__name__}>"


def _get_ref(value: Any) -> Optional[Ref]:
    try:
        ref = getattr(value, "_weft_ref", None)
    except Exception:
        return None
    return ref if isinstance(ref, Ref) else None


def class_bases(cls: type) -> list:
    names = []
    for base in cls.__mro__[1:]:
        if base is object:
            break
        if base.__name__.startswith("_") or base.__name__ == "Generic":
            continue
        names.append(base.__name__)
        if base.__name__ == "BaseModel":
            break
    return names


def object_val(
    obj: Any,
    client: Optional["WeftClient"],
    *,
    publish_nested: bool,
    _depth: int = 0,
    _seen: Optional[Set[int]] = None,
) -> Dict[str, Any]:
    """The stored JSON value of a ``weft.Object`` (see API.md conventions)."""
    from ._op import Op

    cls = type(obj)
    name = cls.__name__
    val: Dict[str, Any] = {"_type": name, "_class_name": name, "_bases": class_bases(cls)}
    exclude = getattr(cls, "_weft_exclude", ()) or ()
    for fname in cls.model_fields:
        if fname in exclude:
            continue
        val[fname] = to_json(getattr(obj, fname), client, publish_nested=publish_nested, _depth=_depth + 1, _seen=_seen)
    extra = getattr(obj, "model_extra", None) or {}
    for k, v in extra.items():
        val[k] = to_json(v, client, publish_nested=publish_nested, _depth=_depth + 1, _seen=_seen)
    if getattr(cls, "_weft_publish_ops", True):
        seen_names: Set[str] = set()
        for klass in cls.__mro__:
            if klass.__name__ in ("Object", "BaseModel", "object"):
                break
            for attr, v in vars(klass).items():
                if attr in seen_names or attr in val:
                    continue
                if isinstance(v, Op):
                    seen_names.add(attr)
                    val[attr] = client._op_ref(v).uri() if client is not None else v.name
    return val
