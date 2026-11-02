"""Request and response models for the lower-level client methods, under the
names Weave uses (``weave.trace_server.trace_server_interface``).

```python
from weft.trace_server_interface import SortBy, ThreadsQueryReq

res = client.threads_query(ThreadsQueryReq(
    project_id="my-project",
    sort_by=[SortBy(field="last_updated", direction="desc")],
    limit=50,
))
for t in res.threads:
    print(t.thread_id, t.turn_count, t.last_updated)
```
"""

from __future__ import annotations

import datetime
from typing import Any, Dict, List, Literal, Optional, Union

from pydantic import BaseModel, ConfigDict, Field


class SortBy(BaseModel):
    field: str
    direction: Literal["asc", "desc"] = "asc"


class ThreadsQueryFilter(BaseModel):
    after_datetime: Optional[Union[datetime.datetime, str]] = None
    """Only threads whose first turn started after this time."""
    before_datetime: Optional[Union[datetime.datetime, str]] = None
    """Only threads whose last turn ended before this time."""
    thread_ids: Optional[List[str]] = None


class ThreadsQueryReq(BaseModel):
    """``/threads/query``. ``project_id`` defaults to the client's project; a bare
    project name is taken in the client's entity. ``sortable_datetime_after`` /
    ``_before`` (Weave's names) map to ``filter.after_datetime`` / ``before_datetime``."""

    project_id: Optional[str] = None
    filter: Optional[ThreadsQueryFilter] = None
    limit: Optional[int] = None
    offset: Optional[int] = None
    sort_by: Optional[List[SortBy]] = None
    """Fields: thread_id, turn_count, start_time, last_updated, p50_turn_duration_ms, p99_turn_duration_ms."""
    sortable_datetime_after: Optional[datetime.datetime] = None
    sortable_datetime_before: Optional[datetime.datetime] = None


class ThreadSchema(BaseModel):
    model_config = ConfigDict(extra="allow")

    thread_id: str
    turn_count: int = 0
    start_time: Optional[datetime.datetime] = None
    last_updated: Optional[datetime.datetime] = None
    first_turn_id: Optional[str] = None
    last_turn_id: Optional[str] = None
    p50_turn_duration_ms: Optional[float] = None
    p99_turn_duration_ms: Optional[float] = None


class ThreadsQueryRes(BaseModel):
    threads: List[ThreadSchema] = Field(default_factory=list)


class CustomRuntimeIDRes(BaseModel):
    model_config = ConfigDict(extra="allow")

    id: str
    max_tokens: Optional[int] = None
    playground_id: Optional[str] = None


class CustomRuntimeApplyRes(BaseModel):
    """What ``register_custom_runtime`` returns (Weave's ``V2RuntimeApplyResponse``;
    ``kind`` and ``ref`` are Weft additions)."""

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


def threads_query_body(req: ThreadsQueryReq, default_project: str, entity: str) -> Dict[str, Any]:
    pid = req.project_id or default_project
    if "/" not in pid:
        pid = f"{entity}/{pid}"
    body: Dict[str, Any] = {"project_id": pid}
    for k in ("limit", "offset"):
        v = getattr(req, k)
        if v is not None:
            body[k] = v
    if req.sort_by:
        body["sort_by"] = [s.model_dump() for s in req.sort_by]
    flt: Dict[str, Any] = {}
    if req.filter is not None:
        flt = {k: _iso(v) for k, v in req.filter.model_dump().items() if v is not None}
    if req.sortable_datetime_after is not None and "after_datetime" not in flt:
        flt["after_datetime"] = _iso(req.sortable_datetime_after)
    if req.sortable_datetime_before is not None and "before_datetime" not in flt:
        flt["before_datetime"] = _iso(req.sortable_datetime_before)
    if flt:
        body["filter"] = flt
    return body


__all__ = [
    "SortBy",
    "ThreadsQueryFilter",
    "ThreadsQueryReq",
    "ThreadSchema",
    "ThreadsQueryRes",
    "CustomRuntimeIDRes",
    "CustomRuntimeApplyRes",
]
