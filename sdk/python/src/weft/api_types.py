"""Request and response models for the lower-level client methods (see
docs/weft/API.md). ``client.sessions_query`` lists sessions (spans grouped by
``session_id``, ``POST /api/v1/spans/sessions``); each ``SessionSchema``
carries the session id as ``session_id``.

```python
from weft.api_types import SortBy, SessionsQueryReq

busiest = client.sessions_query(SessionsQueryReq(
    project_id="acme/helpdesk",
    sort_by=[SortBy(field="turn_count", direction="desc")],
    limit=10,
))
for session in busiest.sessions:
    print(f"{session.session_id}: {session.turn_count} turns")
```
"""

from __future__ import annotations

import datetime
from typing import Any, Dict, List, Literal, Optional, Union

from pydantic import BaseModel, ConfigDict, Field, model_validator

from ._deprecation import deprecated_call


class SortBy(BaseModel):
    field: str
    direction: Literal["asc", "desc"] = "asc"


class SessionsQueryFilter(BaseModel):
    after_datetime: Optional[Union[datetime.datetime, str]] = None
    """Only sessions whose first turn started after this time (``started_after``)."""
    before_datetime: Optional[Union[datetime.datetime, str]] = None
    """Only sessions whose last turn ended before this time (``started_before``)."""
    session_ids: Optional[List[str]] = None
    """Only these sessions."""
    thread_ids: Optional[List[str]] = None
    """Deprecated: ``session_ids`` (still read)."""


class SessionsQueryReq(BaseModel):
    """``POST /api/v1/spans/sessions``. ``project_id`` defaults to the client's project; a bare
    project name is taken in the client's entity. ``sortable_datetime_after`` /
    ``_before`` map to ``filter.after_datetime`` / ``before_datetime``."""

    project_id: Optional[str] = None
    filter: Optional[SessionsQueryFilter] = None
    limit: Optional[int] = None
    offset: Optional[int] = None
    sort_by: Optional[List[SortBy]] = None
    """Fields: last_updated, started_at (or start_time), turn_count, span_count, total_tokens."""
    sortable_datetime_after: Optional[datetime.datetime] = None
    sortable_datetime_before: Optional[datetime.datetime] = None


class SessionSchema(BaseModel):
    """One session: spans sharing a ``session_id``."""

    model_config = ConfigDict(extra="allow")

    session_id: str
    turn_count: int = 0
    span_count: int = 0
    started_at: Optional[datetime.datetime] = None
    start_time: Optional[datetime.datetime] = None
    """``started_at`` (earlier name)."""
    last_updated: Optional[datetime.datetime] = None
    first_turn_id: Optional[str] = None
    last_turn_id: Optional[str] = None
    p50_turn_duration_ms: Optional[float] = None
    p99_turn_duration_ms: Optional[float] = None
    total_tokens: Optional[float] = None
    cost_usd: Optional[float] = None

    @model_validator(mode="before")
    @classmethod
    def _earlier_names(cls, data: Any) -> Any:
        if isinstance(data, dict) and "thread_id" in data:
            data = dict(data)
            tid = data.pop("thread_id")
            data.setdefault("session_id", tid)
        return data

    @property
    def thread_id(self) -> str:
        """Deprecated: ``SessionSchema.session_id``."""
        deprecated_call("SessionSchema.thread_id", "SessionSchema.session_id")
        return self.session_id


class SessionsQueryRes(BaseModel):
    sessions: List[SessionSchema] = Field(default_factory=list)

    @model_validator(mode="before")
    @classmethod
    def _earlier_names(cls, data: Any) -> Any:
        if isinstance(data, dict) and "threads" in data:
            deprecated_call("SessionsQueryRes(threads=...)", "SessionsQueryRes(sessions=...)", stacklevel=6)
            data = dict(data)
            threads = data.pop("threads")
            if data.get("sessions") is None:
                data["sessions"] = threads
        return data

    @property
    def threads(self) -> List[SessionSchema]:
        """Deprecated: ``SessionsQueryRes.sessions``."""
        deprecated_call("SessionsQueryRes.threads", "SessionsQueryRes.sessions")
        return self.sessions


class CustomRuntimeIDRes(BaseModel):
    model_config = ConfigDict(extra="allow")

    id: str
    max_tokens: Optional[int] = None
    playground_id: Optional[str] = None


class CustomRuntimeApplyRes(BaseModel):
    """What ``register_custom_runtime`` returns (``data`` and ``error``, plus
    ``kind`` and ``ref``)."""

    model_config = ConfigDict(extra="allow")

    name: str
    base_url: str
    api_key_secret: Optional[str] = None
    headers: Dict[str, str] = Field(default_factory=dict)
    runtime_ids: List[CustomRuntimeIDRes] = Field(default_factory=list)
    kind: Optional[str] = None
    ref: Optional[str] = None


def _iso(v: Any) -> Any:
    if isinstance(v, datetime.datetime):
        if v.tzinfo is None:
            v = v.astimezone()
        return v.isoformat()
    return v


_FILTER_NAMES = {"after_datetime": "started_after", "before_datetime": "started_before", "thread_ids": "session_ids"}
_SORT_NAMES = {"start_time": "started_at"}


def sessions_query_body(req: SessionsQueryReq, default_project: str, entity: str) -> Dict[str, Any]:
    """The ``POST /api/v1/spans/sessions`` body for ``req``."""
    pid = req.project_id or default_project
    if "/" not in pid:
        pid = f"{entity}/{pid}"
    body: Dict[str, Any] = {"project_id": pid}
    for k in ("limit", "offset"):
        v = getattr(req, k)
        if v is not None:
            body[k] = v
    if req.sort_by:
        body["sort_by"] = [{**s.model_dump(), "field": _SORT_NAMES.get(s.field, s.field)} for s in req.sort_by]
    flt: Dict[str, Any] = {}
    if req.filter is not None:
        for k, v in req.filter.model_dump().items():
            if v is not None:
                k = _FILTER_NAMES.get(k, k)
                flt[k] = list(flt.get(k) or []) + list(v) if k == "session_ids" else _iso(v)
    if req.sortable_datetime_after is not None and "started_after" not in flt:
        flt["started_after"] = _iso(req.sortable_datetime_after)
    if req.sortable_datetime_before is not None and "started_before" not in flt:
        flt["started_before"] = _iso(req.sortable_datetime_before)
    if flt:
        body["filter"] = flt
    return body


def session_from_item(item: Dict[str, Any]) -> Dict[str, Any]:
    """A ``SessionSchema`` dict from a ``/api/v1/spans/sessions`` item."""
    out = dict(item)
    out["session_id"] = item.get("session_id")
    out["start_time"] = item.get("started_at")
    return out


__all__ = [
    "SortBy",
    "SessionsQueryFilter",
    "SessionsQueryReq",
    "SessionSchema",
    "SessionsQueryRes",
    "CustomRuntimeIDRes",
    "CustomRuntimeApplyRes",
]

# Earlier names, kept as deprecated aliases for one release (CHANGELOG.md).
_DEPRECATED = {
    "ThreadsQueryFilter": "SessionsQueryFilter",
    "ThreadsQueryReq": "SessionsQueryReq",
    "ThreadSchema": "SessionSchema",
    "ThreadsQueryRes": "SessionsQueryRes",
    "threads_query_body": "sessions_query_body",
    "thread_from_session": "session_from_item",
}


def __getattr__(name: str) -> Any:
    if name in _DEPRECATED:
        new = _DEPRECATED[name]
        deprecated_call(f"weft.api_types.{name}", f"weft.api_types.{new}")
        return globals()[new]
    raise AttributeError(f"module 'weft.api_types' has no attribute {name!r}")
