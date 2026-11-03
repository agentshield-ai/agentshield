"""Platform objects: AnnotationSpec, Monitor, Leaderboard and SavedView (see API.md)."""

from __future__ import annotations

from typing import Any, Dict, List, Optional

from pydantic import BaseModel, field_validator

from ._object import Object
from ._op import BoundOp, Op
from ._refs import Ref


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
    op_scope: Optional[List[str]] = None

    @field_validator("op_scope", mode="before")
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
    """Online evaluation: the server scores matching calls with server-runnable scorers."""

    active: bool = True
    sampling_rate: float = 1.0
    op_names: List[str] = []
    query: Optional[Dict[str, Any]] = None
    scorers: List[Any] = []

    @field_validator("op_names", mode="before")
    @classmethod
    def _op_names(cls, v: Any) -> Any:
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
        return self.publish(self.name)

    def deactivate(self) -> Any:
        """Set ``active=False`` and publish a new version."""
        self.active = False
        return self.publish(self.name)


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
