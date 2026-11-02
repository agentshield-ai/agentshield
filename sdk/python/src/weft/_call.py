"""Call records (local and fetched from the server) and feedback helpers."""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import TYPE_CHECKING, Any, Dict, Iterator, List, Optional

from ._refs import CallRef, is_ref_uri, parse_uri

if TYPE_CHECKING:  # pragma: no cover
    from ._client import WeftClient


@dataclass
class Call:
    id: str
    project_id: str
    op_name: str
    trace_id: str
    parent_id: Optional[str] = None
    display_name: Optional[str] = None
    thread_id: Optional[str] = None
    turn_id: Optional[str] = None
    started_at: Optional[str] = None
    ended_at: Optional[str] = None
    inputs: Dict[str, Any] = field(default_factory=dict)
    output: Any = None
    exception: Optional[str] = None
    attributes: Dict[str, Any] = field(default_factory=dict)
    summary: Dict[str, Any] = field(default_factory=dict)
    wb_user_id: Optional[str] = None
    wb_run_id: Optional[str] = None
    deleted_at: Optional[str] = None
    raw_feedback: Optional[List[Dict[str, Any]]] = None
    _client: Optional["WeftClient"] = field(default=None, repr=False, compare=False)
    _ended: bool = field(default=False, repr=False, compare=False)
    _fetched: bool = field(default=False, repr=False, compare=False)

    # ---- convenience -----------------------------------------------------
    @property
    def op_base_name(self) -> str:
        if is_ref_uri(self.op_name):
            try:
                return parse_uri(self.op_name).name  # type: ignore[attr-defined]
            except ValueError:
                return self.op_name
        return self.op_name

    @property
    def ref(self) -> CallRef:
        entity, _, project = self.project_id.partition("/")
        return CallRef(entity, project, self.id)

    def uri(self) -> str:
        return self.ref.uri()

    @property
    def status(self) -> str:
        weave = (self.summary or {}).get("weave") or {}
        if weave.get("status"):
            return weave["status"]
        if self.exception:
            return "error"
        return "success" if self.ended_at else "running"

    @property
    def feedback(self) -> "CallFeedback":
        return CallFeedback(self)

    def _require_client(self) -> "WeftClient":
        if self._client is None:
            from ._client import require_client

            return require_client()
        return self._client

    def set_display_name(self, name: Optional[str]) -> None:
        """Rename the call (sent immediately; pending writes are flushed first)."""
        client = self._require_client()
        self.display_name = name
        client.flush()
        client._http.post("/call/update", {"project_id": self.project_id, "call_id": self.id, "display_name": name})

    def delete(self) -> None:
        client = self._require_client()
        client.flush()
        client._http.post("/calls/delete", {"project_id": self.project_id, "call_ids": [self.id]})

    def children(self) -> List["Call"]:
        client = self._require_client()
        return list(client.get_calls(filter={"parent_ids": [self.id]}, sort_by=[{"field": "started_at", "direction": "asc"}]))

    @classmethod
    def from_server(cls, d: Dict[str, Any], client: Optional["WeftClient"]) -> "Call":
        c = cls(
            id=d.get("id", ""),
            project_id=d.get("project_id", ""),
            op_name=d.get("op_name", ""),
            trace_id=d.get("trace_id", ""),
            parent_id=d.get("parent_id"),
            display_name=d.get("display_name"),
            thread_id=d.get("thread_id"),
            turn_id=d.get("turn_id"),
            started_at=d.get("started_at"),
            ended_at=d.get("ended_at"),
            inputs=d.get("inputs") or {},
            output=d.get("output"),
            exception=d.get("exception"),
            attributes=d.get("attributes") or {},
            summary=d.get("summary") or {},
            wb_user_id=d.get("wb_user_id"),
            wb_run_id=d.get("wb_run_id"),
            deleted_at=d.get("deleted_at"),
            raw_feedback=d.get("feedback"),
        )
        c._client = client
        c._ended = True
        c._fetched = True
        return c


class CallFeedback:
    """Feedback attached to a call: iterate it, or add reactions/notes/payloads."""

    def __init__(self, call: Call):
        self.call = call

    def _client(self) -> "WeftClient":
        return self.call._require_client()

    def add(self, feedback_type: str, payload: Any, *, creator: Optional[str] = None, **extra: Any) -> str:
        client = self._client()
        client.flush()
        body: Dict[str, Any] = {
            "project_id": self.call.project_id,
            "weave_ref": self.call.uri(),
            "feedback_type": feedback_type,
            "payload": payload if isinstance(payload, dict) else {"value": payload},
        }
        if creator:
            body["creator"] = creator
        body.update({k: v for k, v in extra.items() if v is not None})
        res = client._http.post("/feedback/create", body)
        return res.get("id", "")

    def add_reaction(self, emoji: str, creator: Optional[str] = None) -> str:
        return self.add("wandb.reaction.1", {"emoji": emoji}, creator=creator)

    def add_note(self, note: str, creator: Optional[str] = None) -> str:
        return self.add("wandb.note.1", {"note": note}, creator=creator)

    def purge(self, feedback_id: str) -> None:
        client = self._client()
        client._http.post(
            "/feedback/purge",
            {"project_id": self.call.project_id, "query": {"$expr": {"$eq": [{"$getField": "id"}, {"$literal": feedback_id}]}}},
        )

    def _list(self) -> List[Dict[str, Any]]:
        client = self._client()
        client.flush()
        res = client._http.post(
            "/feedback/query",
            {
                "project_id": self.call.project_id,
                "query": {"$expr": {"$eq": [{"$getField": "weave_ref"}, {"$literal": self.call.uri()}]}},
                "sort_by": [{"field": "created_at", "direction": "asc"}],
            },
        )
        return list(res.get("result") or [])

    def __iter__(self) -> Iterator[Dict[str, Any]]:
        return iter(self._list())

    def __len__(self) -> int:
        return len(self._list())

    def __getitem__(self, i: int) -> Dict[str, Any]:
        return self._list()[i]

    def __repr__(self) -> str:
        return f"CallFeedback(call={self.call.id!r})"
