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
    wb_run_step: Optional[int] = None
    wb_run_step_end: Optional[int] = None
    wb_username: Optional[str] = None
    deleted_at: Optional[str] = None
    storage_size_bytes: Optional[int] = None
    raw_feedback: Optional[List[Dict[str, Any]]] = None
    _client: Optional["WeftClient"] = field(default=None, repr=False, compare=False)
    _ended: bool = field(default=False, repr=False, compare=False)
    _fetched: bool = field(default=False, repr=False, compare=False)
    _raw_inputs: Optional[Dict[str, Any]] = field(default=None, repr=False, compare=False)
    _raw_output: Any = field(default=None, repr=False, compare=False)
    _child_count: int = field(default=0, repr=False, compare=False)
    # True for calls recorded by weft.conversation spans (Turn, LLM, Tool,
    # SubAgent), which Weave records as OTel spans rather than calls.
    _conversation_span: bool = field(default=False, repr=False, compare=False)
    # The conversation span (Turn, LLM, Tool, SubAgent, Handoff) recording this call.
    _weft_span: Any = field(default=None, repr=False, compare=False)
    # Attributes changed after the start was sent: the start is sent again
    # (an upsert) before the end.
    _resend_start: bool = field(default=False, repr=False, compare=False)
    # weft.reasoning() steps recorded while the call ran.
    _weft_reasoning: Optional[List[Dict[str, Any]]] = field(default=None, repr=False, compare=False)
    # An LLM call was recorded under this one (its reasoning is not repeated here).
    _llm_descendant: bool = field(default=False, repr=False, compare=False)

    # ---- convenience -----------------------------------------------------
    _DICT_FIELDS = (
        "id", "project_id", "op_name", "display_name", "trace_id", "parent_id", "thread_id", "turn_id",
        "started_at", "ended_at", "inputs", "output", "exception", "attributes", "summary",
        "wb_user_id", "wb_username", "wb_run_id", "wb_run_step", "wb_run_step_end", "deleted_at",
    )

    def to_dict(self) -> Dict[str, Any]:
        """The call as a plain dict (server field names), as used by ``CallsIter.to_pandas()``."""
        d: Dict[str, Any] = {k: getattr(self, k) for k in self._DICT_FIELDS}
        d["status"] = self.status
        if self.storage_size_bytes is not None:
            d["storage_size_bytes"] = self.storage_size_bytes
        if self.raw_feedback is not None:
            d["feedback"] = self.raw_feedback
        return d

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
    def ui_url(self) -> str:
        """Link to this call in the Weft UI."""
        base = self._client.server_url if self._client is not None else ""
        return f"{base}/{self.project_id}/calls/{self.id}"

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

    # ---- guardrails / online scoring -------------------------------------
    def _scorer_plan(self, scorer: Any, additional_scorer_kwargs: Optional[Dict[str, Any]]) -> Any:
        from ._evaluation import _scorer_args, _scorer_fn, scorer_name
        from ._op import BoundOp, Op, op as make_op
        from .scorers import Scorer

        if not isinstance(scorer, (Scorer, Op, BoundOp)):
            if callable(scorer):
                scorer = make_op(scorer)
            else:
                raise TypeError("scorer must be a weft.Scorer or an op/function")
        client = self._require_client()
        if isinstance(scorer, Scorer):
            runnable_ref = client.publish(scorer).uri()
        else:
            runnable_ref = client._op_ref(scorer if isinstance(scorer, Op) else scorer.op).uri()
        inputs = self._raw_inputs if self._raw_inputs is not None else self.inputs
        row = {k: v for k, v in (inputs or {}).items() if k != "self"}
        row.update(additional_scorer_kwargs or {})
        output = self._raw_output if self._ended and not self._fetched else self.output
        fn, cmap = _scorer_fn(scorer)
        return client, fn, _scorer_args(fn, row, output, cmap), scorer_name(scorer), runnable_ref

    def _scorer_done(self, client: "WeftClient", res: Any, score_call: Any, name: str, runnable_ref: str) -> "ApplyScorerResult":
        if score_call is not None and score_call.exception:
            raise RuntimeError(f"scorer {name} failed: {score_call.exception.splitlines()[0]}")
        fid = None
        if score_call is not None:
            fid = self.feedback.add(
                f"wandb.runnable.{name}",
                {"output": res},
                runnable_ref=runnable_ref,
                call_ref=score_call.uri(),
            )
        return ApplyScorerResult(result=res, score_call=score_call, feedback_id=fid)

    async def apply_scorer(self, scorer: Any, additional_scorer_kwargs: Optional[Dict[str, Any]] = None) -> "ApplyScorerResult":
        """Score this call (guardrail / online evaluation).

        Runs ``scorer`` on the call's inputs and output (argument names are
        matched to input names; ``output`` receives the call output), records
        the scorer call, and attaches ``wandb.runnable.<scorer>`` feedback with
        ``runnable_ref`` and ``call_ref``. Returns the result so callers can
        block on it.
        """
        import inspect

        from . import _context

        client, fn, args, name, runnable_ref = self._scorer_plan(scorer, additional_scorer_kwargs)
        token = _context._call_stack.set(())  # scorer calls are their own traces
        try:
            out = fn.call(**args)
            if inspect.isawaitable(out):
                out = await out
        finally:
            _context._call_stack.reset(token)
        res, score_call = out
        return self._scorer_done(client, res, score_call, name, runnable_ref)

    def apply_scorer_sync(self, scorer: Any, additional_scorer_kwargs: Optional[Dict[str, Any]] = None) -> "ApplyScorerResult":
        """Synchronous ``apply_scorer`` (async scorers run in a new event loop)."""
        import asyncio
        import inspect

        from . import _context

        client, fn, args, name, runnable_ref = self._scorer_plan(scorer, additional_scorer_kwargs)
        token = _context._call_stack.set(())
        try:
            out = fn.call(**args)
        finally:
            _context._call_stack.reset(token)
        if inspect.isawaitable(out):
            async def _run() -> Any:
                t = _context._call_stack.set(())
                try:
                    return await out
                finally:
                    _context._call_stack.reset(t)

            out = asyncio.run(_run())
        res, score_call = out
        return self._scorer_done(client, res, score_call, name, runnable_ref)

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
            wb_run_step=d.get("wb_run_step"),
            wb_run_step_end=d.get("wb_run_step_end"),
            wb_username=d.get("wb_username"),
            deleted_at=d.get("deleted_at"),
            storage_size_bytes=d.get("storage_size_bytes"),
            raw_feedback=d.get("feedback"),
        )
        c._client = client
        c._ended = True
        c._fetched = True
        return c


@dataclass
class ApplyScorerResult:
    """Result of ``Call.apply_scorer``: the score, the scorer's call and the feedback id."""

    result: Any
    score_call: Optional[Call]
    feedback_id: Optional[str]


class CallFeedback:
    """Feedback attached to a call: iterate it, or add reactions/notes/payloads."""

    def __init__(self, call: Call):
        self.call = call

    def _client(self) -> "WeftClient":
        return self.call._require_client()

    def add(
        self,
        feedback_type: str,
        payload: Any,
        *,
        creator: Optional[str] = None,
        annotation_ref: Optional[str] = None,
        runnable_ref: Optional[str] = None,
        call_ref: Optional[str] = None,
        trigger_ref: Optional[str] = None,
        **extra: Any,
    ) -> str:
        """Attach feedback. ``annotation_ref``/``runnable_ref``/``call_ref``/``trigger_ref`` accept refs or URIs."""
        for k, v in (("annotation_ref", annotation_ref), ("runnable_ref", runnable_ref), ("call_ref", call_ref), ("trigger_ref", trigger_ref)):
            if v is not None:
                extra[k] = v.uri() if hasattr(v, "uri") else str(v)
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

    def add_annotation(self, spec: Any, value: Any, creator: Optional[str] = None) -> str:
        """Human annotation following an ``AnnotationSpec`` (published if needed)."""
        client = self._client()
        ref = getattr(spec, "_weft_ref", None) or client.publish(spec)
        if hasattr(spec, "value_is_valid") and not spec.value_is_valid(value):
            raise ValueError(f"value {value!r} does not match annotation spec {ref.name}")
        return self.add(f"wandb.annotation.{ref.name}", {"value": value}, creator=creator, annotation_ref=ref)

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
