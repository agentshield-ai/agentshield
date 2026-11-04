"""Platform objects: AnnotationSpec, Monitor, Leaderboard and SavedView (see API.md)."""

from __future__ import annotations

from typing import Any, Dict, List, Optional

from pydantic import BaseModel, field_validator, model_validator

from ._deprecation import deprecated_call
from ._object import Object
from ._op import BoundOp, Op
from ._refs import Ref


def _renamed(data: Any, old: str, new: str, what: str) -> Any:
    """Constructor data with ``old`` (a deprecated keyword) given as ``new``."""
    if isinstance(data, dict) and old in data:
        data = dict(data)
        value = data.pop(old)
        deprecated_call(f"{what}({old}=...)", f"{what}({new}=...)", stacklevel=4)
        data.setdefault(new, value)
    return data


def _stored(val: Dict[str, Any], old: str, new: str) -> Dict[str, Any]:
    """A stored value with ``old`` read as ``new``: versions saved before the
    rename, and objects published by the weave client, use the earlier name."""
    if old not in val:
        return val
    out = {k: v for k, v in val.items() if k != old}
    out.setdefault(new, val[old])
    return out


def _as_uri(v: Any) -> Any:
    if isinstance(v, Ref):
        return v.uri()
    ref = getattr(v, "_weft_ref", None)
    if isinstance(ref, Ref):
        return ref.uri()
    if isinstance(v, Object):
        raise ValueError(f"{type(v).__name__} must be published before it can be referenced")
    return v


class AnnotationSpec(Object):
    """Schema for human feedback (``weft.annotation.<name>``).

    ``field_schema`` is a small JSON schema, e.g. ``{"type": "integer", "minimum": 1, "maximum": 5}``,
    ``{"type": "string", "enum": ["good", "bad"]}``, ``{"type": "boolean"}`` or
    ``{"type": "string", "maxLength": 500}``.
    """

    field_schema: Dict[str, Any] = {"type": "string"}
    unique_among_creators: bool = True
    #: The span names (functions, or their refs) it applies to; None: every span.
    names: Optional[List[str]] = None

    @model_validator(mode="before")
    @classmethod
    def _op_scope(cls, data: Any) -> Any:
        return _renamed(data, "op_scope", "names", "AnnotationSpec")

    @classmethod
    def _weft_from_val(cls, val: Dict[str, Any], client: Any, ref: Any) -> Any:
        return super()._weft_from_val(_stored(val, "op_scope", "names"), client, ref)

    @property
    def op_scope(self) -> Optional[List[str]]:
        """Deprecated: use ``names``."""
        deprecated_call("AnnotationSpec.op_scope", "AnnotationSpec.names")
        return self.names

    @field_validator("names", mode="before")
    @classmethod
    def _scope(cls, v: Any) -> Any:
        if v is None:
            return None
        out = []
        for x in v:
            if isinstance(x, (Op, BoundOp)):
                from ._client import require_client

                x = require_client()._op_ref(x if isinstance(x, Op) else x.op).uri()
            out.append(_as_uri(x))
        return out

    def value_is_valid(self, value: Any) -> bool:
        """Validate ``value`` against ``field_schema`` (type, enum, min/max, length)."""
        s = self.field_schema or {}
        t = s.get("type")
        checks = {
            "integer": lambda v: isinstance(v, int) and not isinstance(v, bool),
            "number": lambda v: isinstance(v, (int, float)) and not isinstance(v, bool),
            "string": lambda v: isinstance(v, str),
            "boolean": lambda v: isinstance(v, bool),
            "object": lambda v: isinstance(v, dict),
            "array": lambda v: isinstance(v, list),
        }
        if t in checks and not checks[t](value):
            return False
        if "enum" in s and value not in s["enum"]:
            return False
        if isinstance(value, (int, float)) and not isinstance(value, bool):
            if "minimum" in s and value < s["minimum"]:
                return False
            if "maximum" in s and value > s["maximum"]:
                return False
        if isinstance(value, str):
            if "maxLength" in s and len(value) > s["maxLength"]:
                return False
            if "minLength" in s and len(value) < s["minLength"]:
                return False
        return True


class Monitor(Object):
    """Online evaluation: the server scores matching spans with server-runnable scorers."""

    active: bool = True
    sampling_rate: float = 1.0
    #: Span names (traced functions) to score; empty: root spans.
    names: List[str] = []
    query: Optional[Dict[str, Any]] = None
    scorers: List[Any] = []

    @model_validator(mode="before")
    @classmethod
    def _op_names(cls, data: Any) -> Any:
        return _renamed(data, "op_names", "names", "Monitor")

    @classmethod
    def _weft_from_val(cls, val: Dict[str, Any], client: Any, ref: Any) -> Any:
        return super()._weft_from_val(_stored(val, "op_names", "names"), client, ref)

    @property
    def op_names(self) -> List[str]:
        """Deprecated: use ``names``."""
        deprecated_call("Monitor.op_names", "Monitor.names")
        return self.names

    @field_validator("names", mode="before")
    @classmethod
    def _names(cls, v: Any) -> Any:
        return [x.name if isinstance(x, (Op, BoundOp)) else x for x in (v or [])]

    @field_validator("query", mode="before")
    @classmethod
    def _query(cls, v: Any) -> Any:
        if v is None or "$expr" in v:
            return v
        return {"$expr": v}

    @field_validator("sampling_rate")
    @classmethod
    def _rate(cls, v: float) -> float:
        if not 0.0 <= v <= 1.0:
            raise ValueError("sampling_rate must be between 0 and 1")
        return v

    def activate(self) -> Any:
        """Set ``active=True`` and publish a new version."""
        self.active = True
        return self.save(self.name)

    def deactivate(self) -> Any:
        """Set ``active=False`` and publish a new version."""
        self.active = False
        return self.save(self.name)


class LeaderboardColumn(BaseModel):
    """One leaderboard column: an evaluation, a scorer and a summary metric path."""

    evaluation_object_ref: str
    scorer_name: str
    summary_metric_path: str
    should_minimize: bool = False

    @field_validator("evaluation_object_ref", mode="before")
    @classmethod
    def _ref(cls, v: Any) -> Any:
        return _as_uri(v)


class Leaderboard(Object):
    """Compares models across evaluation metrics (``client.leaderboard(name)`` computes it)."""

    columns: List[LeaderboardColumn] = []


from ._saved_view import SavedView  # noqa: E402  (fluent query API; see _saved_view.py)


__all__ = ["AnnotationSpec", "Monitor", "Leaderboard", "LeaderboardColumn", "SavedView"]
