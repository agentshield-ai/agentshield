"""Spans (local and fetched from the server) and feedback helpers.

A span is one traced function or model call (docs/weft/DESIGN.md §10.2).
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import TYPE_CHECKING, Any, Dict, Iterator, List, Optional

from ._deprecation import deprecated_call
from ._refs import SpanRef, is_ref_uri, parse_uri

if TYPE_CHECKING:  # pragma: no cover
    from ._client import WeftClient


@dataclass
class Span:
    """One traced function or model call.

    ``name`` is the function ref (or a plain span name), ``error`` the error
    text of a failed span, ``session_id`` / ``turn_id`` the conversation and
    turn it belongs to.
    """

    id: str
    project_id: str
    name: str
    trace_id: str
    parent_id: Optional[str] = None
    display_name: Optional[str] = None
    session_id: Optional[str] = None
    turn_id: Optional[str] = None
    started_at: Optional[str] = None
    ended_at: Optional[str] = None
    inputs: Dict[str, Any] = field(default_factory=dict)
    output: Any = None
    error: Optional[str] = None
    attributes: Dict[str, Any] = field(default_factory=dict)
    summary: Dict[str, Any] = field(default_factory=dict)
    user_id: Optional[str] = None
    run_id: Optional[str] = None
    run_step: Optional[int] = None
    run_step_end: Optional[int] = None
    username: Optional[str] = None
    deleted_at: Optional[str] = None
    storage_size_bytes: Optional[int] = None
    raw_feedback: Optional[List[Dict[str, Any]]] = None
    _client: Optional["WeftClient"] = field(default=None, repr=False, compare=False)
    _ended: bool = field(default=False, repr=False, compare=False)
    _fetched: bool = field(default=False, repr=False, compare=False)
    _raw_inputs: Optional[Dict[str, Any]] = field(default=None, repr=False, compare=False)
    _raw_output: Any = field(default=None, repr=False, compare=False)
    _child_count: int = field(default=0, repr=False, compare=False)
    # True for spans recorded by weft.conversation (Turn, LLM, Tool,
    # SubAgent) rather than by traced functions.
    _conversation_span: bool = field(default=False, repr=False, compare=False)
    # The conversation span (Turn, LLM, Tool, SubAgent, Handoff) recording this span.
    _weft_span: Any = field(default=None, repr=False, compare=False)
    # Attributes changed after the start was sent: the start is sent again
    # (an upsert) before the end.
    _resend_start: bool = field(default=False, repr=False, compare=False)
    # weft.reasoning() steps recorded while the span ran.
    _weft_reasoning: Optional[List[Dict[str, Any]]] = field(default=None, repr=False, compare=False)
    # An LLM span was recorded under this one (its reasoning is not repeated here).
    _llm_descendant: bool = field(default=False, repr=False, compare=False)

    # ---- convenience -----------------------------------------------------
    _DICT_FIELDS = (
        "id", "project_id", "name", "display_name", "trace_id", "parent_id", "session_id", "turn_id",
        "started_at", "ended_at", "inputs", "output", "error", "attributes", "summary",
        "user_id", "username", "run_id", "run_step", "run_step_end", "deleted_at",
    )

    def to_dict(self) -> Dict[str, Any]:
        """The span as a plain dict (server field names), as used by ``SpansIter.to_pandas()``."""
        d: Dict[str, Any] = {k: getattr(self, k) for k in self._DICT_FIELDS}
        d["status"] = self.status
        if self.storage_size_bytes is not None:
            d["storage_size_bytes"] = self.storage_size_bytes
        if self.raw_feedback is not None:
            d["feedback"] = self.raw_feedback
        return d

    @property
    def base_name(self) -> str:
        """The function's name for a function ref (``my_fn``), else ``name``."""
        if is_ref_uri(self.name):
            try:
                return parse_uri(self.name).name  # type: ignore[attr-defined]
            except ValueError:
                return self.name
        return self.name

    # ---- earlier field names (deprecated) ---------------------------------
    @property
    def op_name(self) -> str:
        deprecated_call("Span.op_name", "Span.name")
        return self.name

    @op_name.setter
    def op_name(self, v: str) -> None:
        deprecated_call("Span.op_name", "Span.name")
        self.name = v

    @property
    def op_base_name(self) -> str:
        deprecated_call("Span.op_base_name", "Span.base_name")
        return self.base_name

    @property
    def exception(self) -> Optional[str]:
        deprecated_call("Span.exception", "Span.error")
        return self.error

    @exception.setter
    def exception(self, v: Optional[str]) -> None:
        deprecated_call("Span.exception", "Span.error")
        self.error = v

    @property
    def thread_id(self) -> Optional[str]:
        deprecated_call("Span.thread_id", "Span.session_id")
        return self.session_id

    @thread_id.setter
    def thread_id(self, v: Optional[str]) -> None:
        deprecated_call("Span.thread_id", "Span.session_id")
        self.session_id = v

    @property
    def ref(self) -> SpanRef:
        entity, _, project = self.project_id.partition("/")
        return SpanRef(entity, project, self.id)

    def uri(self) -> str:
        return self.ref.uri()

    @property
    def ui_url(self) -> str:
        """Link to this span in the Weft UI."""
        base = self._client.server_url if self._client is not None else ""
        return f"{base}/{self.project_id}/spans/{self.id}"

    @property
    def status(self) -> str:
        computed = (self.summary or {}).get("weft") or {}
        if computed.get("status"):
            return computed["status"]
        if self.error:
            return "error"
        return "success" if self.ended_at else "running"

    @property
    def feedback(self) -> "SpanFeedback":
        return SpanFeedback(self)

    def _require_client(self) -> "WeftClient":
        if self._client is None:
            from ._client import require_client

            return require_client()
        return self._client

    def set_display_name(self, name: Optional[str]) -> None:
        """Rename the span (sent immediately; pending writes are flushed first)."""
        client = self._require_client()
        self.display_name = name
        client.flush()
        client._http.post("/api/v1/spans/update", {"project_id": self.project_id, "id": self.id, "display_name": name})

    def delete(self) -> None:
        client = self._require_client()
        client.flush()
        client._http.post("/api/v1/spans/delete", {"project_id": self.project_id, "ids": [self.id]})

    def children(self) -> List["Span"]:
        client = self._require_client()
        return list(client.spans(filter={"parent_ids": [self.id]}, sort_by=[{"field": "started_at", "direction": "asc"}]))

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
            runnable_ref = client.save(scorer).uri()
        else:
            runnable_ref = client._op_ref(scorer if isinstance(scorer, Op) else scorer.op).uri()
        inputs = self._raw_inputs if self._raw_inputs is not None else self.inputs
        row = {k: v for k, v in (inputs or {}).items() if k != "self"}
        row.update(additional_scorer_kwargs or {})
        output = self._raw_output if self._ended and not self._fetched else self.output
        fn, cmap = _scorer_fn(scorer)
        return client, fn, _scorer_args(fn, row, output, cmap), scorer_name(scorer), runnable_ref

    def _scorer_done(self, client: "WeftClient", res: Any, score_span: Any, name: str, runnable_ref: str) -> "ApplyScorerResult":
        if score_span is not None and score_span.error:
            raise RuntimeError(f"scorer {name} failed: {score_span.error.splitlines()[0]}")
        fid = None
        if score_span is not None:
            fid = self.feedback.add(
                f"weft.scorer.{name}",
                {"output": res},
                runnable_ref=runnable_ref,
                span_ref=score_span.uri(),
            )
        return ApplyScorerResult(result=res, score_span=score_span, feedback_id=fid)

    async def apply_scorer(self, scorer: Any, additional_scorer_kwargs: Optional[Dict[str, Any]] = None) -> "ApplyScorerResult":
        """Score this span (guardrail / online evaluation).

        Runs ``scorer`` on the span's inputs and output (argument names are
        matched to input names; ``output`` receives the span output), records
        the scorer's own span, and attaches ``weft.scorer.<scorer>`` feedback
        with ``runnable_ref`` and ``span_ref``. Returns the result so callers
        can block on it.
        """
        import inspect

        from . import _context

        client, fn, args, name, runnable_ref = self._scorer_plan(scorer, additional_scorer_kwargs)
        token = _context._call_stack.set(())  # scorer spans are their own traces
        try:
            out = fn.call(**args)
            if inspect.isawaitable(out):
                out = await out
        finally:
            _context._call_stack.reset(token)
        res, score_span = out
        return self._scorer_done(client, res, score_span, name, runnable_ref)

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
        res, score_span = out
        return self._scorer_done(client, res, score_span, name, runnable_ref)

    @classmethod
    def from_server(cls, d: Dict[str, Any], client: Optional["WeftClient"]) -> "Span":
        c = cls(
            id=d.get("id", ""),
            project_id=d.get("project_id", ""),
            name=d.get("name") or "",
            trace_id=d.get("trace_id", ""),
            parent_id=d.get("parent_id"),
            display_name=d.get("display_name"),
            session_id=d.get("session_id"),
            turn_id=d.get("turn_id"),
            started_at=d.get("started_at"),
            ended_at=d.get("ended_at"),
            inputs=d.get("inputs") or {},
            output=d.get("output"),
            error=d.get("error"),
            attributes=d.get("attributes") or {},
            summary=d.get("summary") or {},
            user_id=d.get("user_id"),
            run_id=d.get("run_id"),
            run_step=d.get("run_step"),
            run_step_end=d.get("run_step_end"),
            username=d.get("username"),
            deleted_at=d.get("deleted_at"),
            storage_size_bytes=d.get("storage_size_bytes"),
            raw_feedback=d.get("feedback"),
        )
        c._client = client
        c._ended = True
        c._fetched = True
        return c


# Earlier name of Span.
Call = Span


@dataclass
class ApplyScorerResult:
    """Result of ``Span.apply_scorer``: the score, the scorer's span and the feedback id."""

    result: Any
    score_span: Optional[Span]
    feedback_id: Optional[str]

    @property
    def score_call(self) -> Optional[Span]:
        deprecated_call("ApplyScorerResult.score_call", "ApplyScorerResult.score_span")
        return self.score_span


class SpanFeedback:
    """Feedback attached to a span: iterate it, or add reactions/notes/payloads."""

    def __init__(self, span: Span):
        self.span = span

    @property
    def call(self) -> Span:
        return self.span

    def _client(self) -> "WeftClient":
        return self.span._require_client()

    def add(
        self,
        feedback_type: str,
        payload: Any,
        *,
        creator: Optional[str] = None,
        annotation_ref: Optional[str] = None,
        runnable_ref: Optional[str] = None,
        span_ref: Optional[str] = None,
        trigger_ref: Optional[str] = None,
        **extra: Any,
    ) -> str:
        """Attach feedback. ``annotation_ref``/``runnable_ref``/``span_ref``/``trigger_ref`` accept refs or URIs."""
        if span_ref is None and extra.get("call_ref") is not None:
            span_ref = extra.pop("call_ref")
        extra.pop("call_ref", None)
        for k, v in (("annotation_ref", annotation_ref), ("runnable_ref", runnable_ref), ("span_ref", span_ref), ("trigger_ref", trigger_ref)):
            if v is not None:
                extra[k] = v.uri() if hasattr(v, "uri") else str(v)
        client = self._client()
        client.flush()
        body: Dict[str, Any] = {
            "project_id": self.span.project_id,
            "ref": self.span.uri(),
            "feedback_type": feedback_type,
            "payload": payload if isinstance(payload, dict) else {"value": payload},
        }
        if creator:
            body["creator"] = creator
        body.update({k: v for k, v in extra.items() if v is not None})
        res = client._http.post("/api/v1/feedback/create", body)
        return res.get("id", "")

    def add_reaction(self, emoji: str, creator: Optional[str] = None) -> str:
        return self.add("weft.reaction", {"emoji": emoji}, creator=creator)

    def add_note(self, note: str, creator: Optional[str] = None) -> str:
        return self.add("weft.note", {"note": note}, creator=creator)

    def add_annotation(self, spec: Any, value: Any, creator: Optional[str] = None) -> str:
        """Human annotation following an ``AnnotationSpec`` (published if needed)."""
        client = self._client()
        ref = getattr(spec, "_weft_ref", None) or client.save(spec)
        if hasattr(spec, "value_is_valid") and not spec.value_is_valid(value):
            raise ValueError(f"value {value!r} does not match annotation spec {ref.name}")
        return self.add(f"weft.annotation.{ref.name}", {"value": value}, creator=creator, annotation_ref=ref)

    def purge(self, feedback_id: str) -> None:
        client = self._client()
        client._http.post("/api/v1/feedback/delete", {"project_id": self.span.project_id, "ids": [feedback_id]})

    def _list(self) -> List[Dict[str, Any]]:
        client = self._client()
        client.flush()
        res = client._http.post(
            "/api/v1/feedback/query",
            {
                "project_id": self.span.project_id,
                "query": {"$expr": {"$eq": [{"$getField": "ref"}, {"$literal": self.span.uri()}]}},
                "sort_by": [{"field": "created_at", "direction": "asc"}],
                "limit": 10000,
            },
        )
        return list(res.get("items") or [])

    def __iter__(self) -> Iterator[Dict[str, Any]]:
        return iter(self._list())

    def __len__(self) -> int:
        return len(self._list())

    def __getitem__(self, i: int) -> Dict[str, Any]:
        return self._list()[i]

    def __repr__(self) -> str:
        return f"SpanFeedback(span={self.span.id!r})"


# Earlier name of SpanFeedback.
CallFeedback = SpanFeedback
