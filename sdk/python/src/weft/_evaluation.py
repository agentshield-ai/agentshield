"""Client-side evaluations: ``Evaluation`` and the imperative ``EvaluationLogger``.

Both record the call structure documented in API.md::

    Evaluation.evaluate            inputs {self: <evaluation ref>, model: <model ref>}
    ├── Evaluation.predict_and_score   inputs {self, model, example: <row ref | dict>}
    │   ├── <Model>.predict / <op>
    │   └── <scorer> calls
    └── Evaluation.summarize
"""

from __future__ import annotations

import asyncio
import contextvars
import inspect
import logging
import os
import time
from typing import Any, Callable, ClassVar, Dict, List, Optional, Tuple, Union

from pydantic import field_validator

from ._object import Dataset, Model, Object, Table, find_infer_method
from ._op import ASYNC, AGEN, BoundOp, Op, op
from .scorers import Scorer

logger = logging.getLogger("weft")


def auto_summarize(values: List[Any]) -> Optional[Dict[str, Any]]:
    """Summarise a list of scores: bools → {true_count, true_fraction}, numbers → {mean}, dicts recursively."""
    vals = [v for v in values if v is not None]
    if not vals:
        return None
    if all(isinstance(v, bool) for v in vals):
        tc = sum(1 for v in vals if v)
        return {"true_count": tc, "true_fraction": tc / len(vals)}
    if all(isinstance(v, (int, float)) and not isinstance(v, bool) for v in vals):
        return {"mean": sum(vals) / len(vals)}
    if all(isinstance(v, dict) for v in vals):
        keys: List[str] = []
        for v in vals:
            for k in v:
                if k not in keys:
                    keys.append(k)
        out: Dict[str, Any] = {}
        for k in keys:
            s = auto_summarize([v.get(k) for v in vals])
            if s is not None:
                out[k] = s
        return out or None
    return None


class _RowDict(dict):
    """A dataset row that serialises as its row ref."""

    _weft_ref: Any = None


def _is_async(fn: Any) -> bool:
    if isinstance(fn, BoundOp):
        return fn.op._kind in (ASYNC,)
    if isinstance(fn, Op):
        return fn._kind in (ASYNC,)
    return inspect.iscoroutinefunction(fn)


async def _invoke(fn: Any, kwargs: Dict[str, Any]) -> Any:
    if _is_async(fn):
        res = await fn(**kwargs)
    else:
        res = await asyncio.to_thread(fn, **kwargs)
    if inspect.isawaitable(res):
        res = await res
    return res


def _signature(fn: Any) -> Optional[inspect.Signature]:
    try:
        if isinstance(fn, BoundOp):
            return fn.__signature__
        if isinstance(fn, Op):
            return fn.signature
        return inspect.signature(fn)
    except (TypeError, ValueError):
        return None


def _select_model_args(fn: Any, row: Dict[str, Any]) -> Dict[str, Any]:
    sig = _signature(fn)
    if sig is None:
        return dict(row)
    params = sig.parameters
    if any(p.kind == inspect.Parameter.VAR_KEYWORD for p in params.values()):
        return dict(row)
    return {k: v for k, v in row.items() if k in params and k != "self"}


def _scorer_args(fn: Any, row: Dict[str, Any], output: Any, column_map: Dict[str, str]) -> Dict[str, Any]:
    sig = _signature(fn)
    params = sig.parameters if sig is not None else {}
    has_var_kw = sig is None or any(p.kind == inspect.Parameter.VAR_KEYWORD for p in params.values())
    args: Dict[str, Any] = {}
    if has_var_kw:
        args.update(row)
    for arg, col in (column_map or {}).items():
        if col in row:
            args[arg] = row[col]
    for name, p in params.items():
        if name == "self" or p.kind in (inspect.Parameter.VAR_KEYWORD, inspect.Parameter.VAR_POSITIONAL):
            continue
        if name in ("output", "model_output"):
            continue
        col = (column_map or {}).get(name, name)
        if col in row:
            args[name] = row[col]
    if "model_output" in params and "output" not in params:
        args["model_output"] = output
        args.pop("output", None)
    else:
        args["output"] = output
    if not has_var_kw:
        args = {k: v for k, v in args.items() if k in params}
    return args


def scorer_name(scorer: Any) -> str:
    if isinstance(scorer, Scorer):
        return scorer.name or type(scorer).__name__
    if isinstance(scorer, (Op, BoundOp)):
        return scorer.name
    return getattr(scorer, "__name__", type(scorer).__name__)


def _scorer_fn(scorer: Any) -> Tuple[Any, Dict[str, str]]:
    if isinstance(scorer, Scorer):
        return scorer.score, dict(scorer.column_map or {})
    return scorer, {}


_eval_display_name: contextvars.ContextVar[Optional[str]] = contextvars.ContextVar("weft_eval_display_name", default=None)


def _display_name(call: Any) -> Optional[str]:
    return _eval_display_name.get()


def _summarize_inputs(inputs: Dict[str, Any]) -> Dict[str, Any]:
    return {"self": inputs.get("self")}


class Evaluation(Object):
    """Evaluate a model (``weft.Model`` or op function) on a dataset with scorers.

    >>> evaluation = weft.Evaluation(dataset=ds, scorers=[exact, weft.scorers.ExactMatchScorer()])
    >>> summary = asyncio.run(evaluation.evaluate(model))
    """

    _weft_publish_ops: ClassVar[bool] = False
    _weft_exclude: ClassVar[Tuple[str, ...]] = ("preprocess_model_input", "evaluation_name")

    dataset: Any
    scorers: List[Any] = []
    trials: int = 1
    preprocess_model_input: Optional[Callable[[Dict[str, Any]], Dict[str, Any]]] = None
    evaluation_name: Optional[Union[str, Callable[[Any], str]]] = None

    @field_validator("dataset", mode="before")
    @classmethod
    def _conv_dataset(cls, v: Any) -> Any:
        if isinstance(v, (Dataset, str)) or type(v).__name__ == "WeftObject":
            return v
        if isinstance(v, (list, tuple, Table)):
            return Dataset(rows=v)
        return v

    @field_validator("scorers", mode="before")
    @classmethod
    def _conv_scorers(cls, v: Any) -> Any:
        out = []
        for s in v or []:
            if isinstance(s, (Scorer, Op, BoundOp, str)) or type(s).__name__ == "WeftObject":
                out.append(s)
            elif isinstance(s, type) and issubclass(s, Scorer):
                out.append(s())
            elif callable(s):
                out.append(op(s))
            else:
                raise TypeError(f"invalid scorer: {s!r}")
        return out

    @field_validator("trials")
    @classmethod
    def _check_trials(cls, v: int) -> int:
        if v < 1:
            raise ValueError("trials must be >= 1")
        return v

    # ------------------------------------------------------------------ public
    async def evaluate(self, model: Any) -> Dict[str, Any]:
        """Run the evaluation; returns the summary dict."""
        from ._client import tracing_client

        if not isinstance(model, (Model, Op, BoundOp)):
            if callable(model):
                model = op(model)
            else:
                raise TypeError("model must be a weft.Model or a function")
        client = tracing_client()
        if client is not None:
            client.save(self, name=self.name or "Evaluation")
            if isinstance(model, Object):
                client.save(model)
        name = self.evaluation_name
        display: Optional[str] = None
        if callable(name):
            try:
                display = name(model)
            except Exception:  # pragma: no cover
                display = None
        elif isinstance(name, str):
            display = name
        token = _eval_display_name.set(display)
        try:
            return await self._evaluate(model)
        finally:
            _eval_display_name.reset(token)

    def _examples(self) -> List[Dict[str, Any]]:
        ds = self.dataset
        rows = list(ds.rows) if hasattr(ds, "rows") else list(ds)
        ds_ref = getattr(ds, "_weft_ref", None)
        table = getattr(ds, "rows", None)
        digests = getattr(table, "row_digests", None)
        out = []
        for i, row in enumerate(rows):
            r = _RowDict(row if isinstance(row, dict) else {"input": row})
            if ds_ref is not None and digests is not None and i < len(digests) and digests[i]:
                r._weft_ref = ds_ref.with_extra("attr", "rows", "id", digests[i])
            out.append(r)
        return out

    @op(name="Evaluation.evaluate", call_display_name=_display_name)
    async def _evaluate(self, model: Any) -> Dict[str, Any]:
        examples = self._examples()
        try:
            parallelism = max(1, int(os.environ.get("WEFT_PARALLELISM", "10")))
        except ValueError:
            parallelism = 10
        sem = asyncio.Semaphore(parallelism)

        async def run_one(example: Dict[str, Any]) -> Dict[str, Any]:
            async with sem:
                return await self.predict_and_score(model, example)

        results = await asyncio.gather(*[run_one(ex) for ex in examples for _ in range(self.trials)])
        return self.summarize(list(results))

    @op(name="Evaluation.predict_and_score")
    async def predict_and_score(self, model: Any, example: Dict[str, Any]) -> Dict[str, Any]:
        row = dict(example)
        model_input = row
        if self.preprocess_model_input is not None:
            model_input = self.preprocess_model_input(dict(row))
        fn = find_infer_method(model)
        start = time.perf_counter()
        output: Any = None
        try:
            output = await _invoke(fn, _select_model_args(fn, model_input))
        except Exception as e:
            logger.debug("weft: model prediction failed: %s", e)
        latency = time.perf_counter() - start
        scores: Dict[str, Any] = {}
        for scorer in self.scorers:
            sname = scorer_name(scorer)
            sfn, cmap = _scorer_fn(scorer)
            try:
                scores[sname] = await _invoke(sfn, _scorer_args(sfn, row, output, cmap))
            except Exception as e:
                logger.debug("weft: scorer %s failed: %s", sname, e)
                scores[sname] = None
        return {"output": output, "scores": scores, "model_latency": latency}

    @op(name="Evaluation.summarize", postprocess_inputs=_summarize_inputs)
    def summarize(self, eval_table: List[Dict[str, Any]]) -> Dict[str, Any]:
        summary: Dict[str, Any] = {}
        for scorer in self.scorers:
            sname = scorer_name(scorer)
            vals = [(r.get("scores") or {}).get(sname) for r in eval_table]
            s = scorer.summarize(vals) if isinstance(scorer, Scorer) else auto_summarize(vals)
            if s is not None:
                summary[sname] = s
        lat = [r["model_latency"] for r in eval_table if isinstance(r.get("model_latency"), (int, float))]
        summary["model_latency"] = {"mean": (sum(lat) / len(lat)) if lat else 0.0}
        return summary


# ---------------------------------------------------------------------------
# imperative logger


def _parse_ts(s: Any) -> Optional[float]:
    """Seconds since the epoch from an RFC 3339 timestamp (any fractional precision)."""
    import datetime as _dt
    import re as _re

    if not isinstance(s, str) or not s:
        return None
    m = _re.match(r"(\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d)(?:\.(\d+))?(Z|[+-]\d\d:?\d\d)?$", s.strip())
    if not m:
        return None
    base = _dt.datetime.fromisoformat(m.group(1))
    tz = m.group(3) or "Z"
    if tz == "Z":
        base = base.replace(tzinfo=_dt.timezone.utc)
    else:
        sign = 1 if tz[0] == "+" else -1
        hh, mm = int(tz[1:3]), int(tz[-2:])
        base = base.replace(tzinfo=_dt.timezone(sign * _dt.timedelta(hours=hh, minutes=mm)))
    frac = float("0." + m.group(2)) if m.group(2) else 0.0
    return base.timestamp() + frac


def _linked_calls(link: Any) -> "Tuple[List[Dict[str, Any]], List[str], List[Any]]":
    """Normalise what ``log_prediction(call=...)`` links to.

    Returns ``(calls, conversation_ids, call_objects)`` where ``calls`` is
    ``[{"call_id", "trace_id"?}]``. Accepts a Call, a call id or call URI, a
    conversation span (Turn, LLM, Tool, SubAgent), a ``LogResult``, a
    ``Conversation``, or a list of these.
    """
    from ._call import Call
    from ._refs import SpanRef, is_ref_uri, parse_uri

    calls: List[Dict[str, Any]] = []
    convs: List[str] = []
    objs: List[Any] = []
    items = link if isinstance(link, (list, tuple)) else [link]
    for it in items:
        if it is None:
            continue
        if isinstance(it, Call):
            calls.append({"call_id": it.id, "trace_id": it.trace_id})
            objs.append(it)
            continue
        if isinstance(it, str):
            if is_ref_uri(it):
                r = parse_uri(it)
                if not isinstance(r, SpanRef):
                    raise TypeError(f"{it} is not a span ref")
                calls.append({"call_id": r.id})
            else:
                calls.append({"call_id": it})
            objs.append(it)
            continue
        span_call = getattr(it, "call", None)
        if isinstance(span_call, Call) or hasattr(it, "_start") and hasattr(it, "call_id"):
            if getattr(it, "_started", True) is False and hasattr(it, "_start"):
                it._start(set_current=False)
            c = it.call
            if c is not None:
                calls.append({"call_id": c.id, "trace_id": c.trace_id})
                objs.append(c)
            continue
        root_ids = getattr(it, "root_span_ids", None)
        if root_ids is not None:  # conversation.LogResult
            for cid, tid in zip(root_ids, list(getattr(it, "trace_ids", []) or []) + [None] * len(root_ids)):
                calls.append({"call_id": cid, **({"trace_id": tid} if tid else {})})
            if getattr(it, "conversation_id", None):
                convs.append(it.conversation_id)
            continue
        if getattr(it, "conversation_id", None) and hasattr(it, "start_turn"):  # Conversation
            convs.append(it.conversation_id)
            continue
        raise TypeError(f"cannot link {type(it).__name__} to a prediction")
    return calls, list(dict.fromkeys(convs)), objs


class ScoreLogger:
    """Returned by ``EvaluationLogger.log_prediction``; log scores then ``finish()``.

    Also a context manager: calls made inside ``with ev.log_prediction(inputs) as pred:``
    become children of the prediction's predict call, conversation spans started
    there carry ``weft.eval.*`` link attributes, and set ``pred.output`` before
    the block ends.
    """

    def __init__(
        self,
        ev: "EvaluationLogger",
        call: Any,
        inputs: Dict[str, Any],
        output: Any,
        model_latency: Optional[float],
        predict_call: Any = None,
        eval_context: Optional[Dict[str, Any]] = None,
    ):
        self._ev = ev
        self._call = call
        self._predict_call = predict_call
        self._eval_context = eval_context
        self.inputs = inputs
        self.output = output
        self.model_latency = model_latency
        self.scores: Dict[str, Any] = {}
        self._finished = False
        self._started = time.perf_counter()
        self._tokens: List[Any] = []

    @property
    def predict_and_score_call(self) -> Any:
        return self._call

    @property
    def predict_call(self) -> Any:
        return self._predict_call

    def log_score(self, scorer: Union[str, Scorer], score: Any) -> None:
        if self._finished:
            raise RuntimeError("cannot log a score after finish()")
        client = self._ev._client
        name = scorer if isinstance(scorer, str) else scorer_name(scorer)
        if client is not None and self._call is not None:
            if isinstance(scorer, Scorer):
                sop: Any = type(scorer).__dict__.get("score") or Scorer.__dict__["score"]
                inputs = {"self": scorer, "output": self.output, **self.inputs}
            else:
                sop = name
                inputs = {"output": self.output, **self.inputs}
            c = client.create_call(sop, inputs, parent=self._call)
            client.finish_call(c, output=score)
        self.scores[name] = score
        self._ev._scorer_names.setdefault(name, None)

    def _finish_predict(self) -> None:
        client = self._ev._client
        pc = self._predict_call
        if client is not None and pc is not None and not pc._ended:
            if self.model_latency is None:
                self.model_latency = time.perf_counter() - self._started
            client.finish_call(pc, output=self.output)

    def finish(self) -> None:
        if self._finished:
            return
        self._finished = True
        self._finish_predict()
        client = self._ev._client
        out = {"output": self.output, "scores": dict(self.scores), "model_latency": self.model_latency}
        if client is not None and self._call is not None:
            client.finish_call(self._call, output=out)

    def __enter__(self) -> "ScoreLogger":
        from . import _context

        if self._predict_call is not None and not self._predict_call._ended:
            self._tokens.append(("call", _context.push_call(self._predict_call)))
        if self._eval_context:
            self._tokens.append(("eval", _context.set_eval_span_context(self._eval_context)))
        return self

    def __exit__(self, *exc: Any) -> None:
        from . import _context

        while self._tokens:
            kind, tok = self._tokens.pop()
            if kind == "call":
                _context.pop_call(tok)
            else:
                _context.reset_eval_span_context(tok)
        self.finish()


def _coerce_model(model: Any) -> Any:
    if isinstance(model, (Model, Op, BoundOp)):
        return model
    if model is None:
        return Model(name="model")
    if isinstance(model, str):
        return Model(name=model)
    if isinstance(model, dict):
        data = dict(model)
        return Model(name=str(data.pop("name", "model")), **data)
    raise TypeError("model must be a name, dict, weft.Model or op")


def _coerce_dataset(dataset: Any) -> Any:
    from ._refs import ObjectRef, is_ref_uri

    if isinstance(dataset, Dataset):
        return dataset
    if dataset is None:
        return Dataset(name="dataset", rows=[])
    if isinstance(dataset, ObjectRef) or (isinstance(dataset, str) and is_ref_uri(dataset)):
        from ._client import require_client

        loaded = require_client().get(dataset)
        if not isinstance(loaded, Dataset):
            raise TypeError(f"{dataset} is not a Dataset")
        return loaded
    if isinstance(dataset, str):
        return Dataset(name=dataset, rows=[])
    return Dataset(rows=list(dataset))


def _row_key(row: Any) -> Optional[str]:
    from ._canonical import digest
    from ._serialize import to_json

    try:
        return digest(to_json(row))
    except Exception:
        return None


class EvaluationLogger:
    """Log an evaluation imperatively, producing the same calls as ``Evaluation``.

    >>> ev = weft.EvaluationLogger(model="my-model", dataset="qa")
    >>> pred = ev.log_prediction(inputs={"q": "2+2"}, output="4")
    >>> pred.log_score("correct", True)
    >>> pred.finish()
    >>> ev.log_summary()
    """

    def __init__(
        self,
        model: Any = None,
        dataset: Any = None,
        name: Optional[str] = None,
        description: Optional[str] = None,
        scorers: Optional[List[Any]] = None,
    ):
        from ._client import tracing_client

        self._client = tracing_client()
        self.model = _coerce_model(model)
        self.dataset = _coerce_dataset(dataset)
        self.evaluation = Evaluation(name=name or "Evaluation", description=description, dataset=self.dataset, scorers=scorers or [])
        self._predictions: List[ScoreLogger] = []
        self._scorer_names: Dict[str, None] = {}
        self._finished = False
        self.summary: Optional[Dict[str, Any]] = None
        self._call = None
        self._row_refs: Dict[str, Any] = {}
        if self._client is not None:
            self._client.save(self.evaluation, name=self.evaluation.name)
            if isinstance(self.model, Object):
                self._client.save(self.model)
            self._index_dataset_rows()
            self._call = self._client.create_call(
                Evaluation.__dict__["_evaluate"], {"self": self.evaluation, "model": self.model}, display_name=name
            )

    @property
    def call(self) -> Any:
        return self._call

    def _index_dataset_rows(self) -> None:
        """Map each dataset row to its row ref so matching predictions link to it."""
        ds_ref = self.dataset._weft_ref
        table = self.dataset.rows
        rows = list(table.rows) if hasattr(table, "rows") else []  # loads lazy tables (and their digests)
        digests = getattr(table, "row_digests", None)
        if ds_ref is None or not digests:
            return
        for row, rd in zip(rows, digests):
            key = _row_key(row)
            if key is not None and rd:
                self._row_refs.setdefault(key, ds_ref.with_extra("attr", "rows", "id", rd))

    def _example(self, inputs: Dict[str, Any]) -> Any:
        ref = self._row_refs.get(_row_key(inputs) or "")
        if ref is None:
            return dict(inputs)
        row = _RowDict(inputs)
        row._weft_ref = ref
        return row

    def _predict_op(self) -> Any:
        m = self.model
        if isinstance(m, Model):
            pred = type(m).__dict__.get("predict")
            if isinstance(pred, Op):
                return pred
            return f"{type(m).__name__}.predict" if type(m) is not Model else f"{m.name or 'Model'}.predict"
        return m if isinstance(m, Op) else m.op

    def log_prediction(
        self,
        inputs: Dict[str, Any],
        output: Any = None,
        model_latency: Optional[float] = None,
        *,
        call: Any = None,
        example_id: Optional[str] = None,
        row_digest: Optional[str] = None,
        trial_index: Optional[int] = None,
        eval_kind: Optional[str] = "agent",
    ) -> ScoreLogger:
        """Log one prediction; returns a ``ScoreLogger`` (also usable as a context manager).

        ``call`` links the prediction to traces that already exist instead of
        re-recording them: a ``Call``, a call id or URI, a conversation span
        (``Turn``, ``LLM``, ...), a ``LogResult`` or a ``Conversation`` (or a
        list). The links are stored on the prediction as
        ``attributes.weft.linked_calls`` / ``attributes.weft.genai_span_ref``
        (``[{"trace_id", "span_id"}]``, as in the v2 predictions API) and
        ``attributes.weft.linked_conversation_ids``. When ``output`` is omitted
        the linked call's output is used, and ``model_latency`` defaults to its
        duration.

        Without ``output`` (and without a link) the predict call stays open:
        set ``pred.output`` inside ``with ev.log_prediction(inputs) as pred:``
        and calls made in the block become its children.
        """
        if self._finished:
            raise RuntimeError("evaluation already finished")
        linked, conv_ids, objs = _linked_calls(call) if call is not None else ([], [], [])
        if linked and output is None and len(objs) == 1:
            output, lat = self._linked_output(objs[0])
            if model_latency is None:
                model_latency = lat
        weft_attrs: Dict[str, Any] = {}
        if linked:
            weft_attrs["linked_calls"] = linked
            weft_attrs["genai_span_ref"] = [{"trace_id": c.get("trace_id"), "span_id": c["call_id"]} for c in linked]
        if conv_ids:
            weft_attrs["linked_conversation_ids"] = conv_ids
        eval_meta = {k: v for k, v in (("example_id", example_id), ("row_digest", row_digest), ("trial_index", trial_index), ("kind", eval_kind)) if v is not None}
        if eval_meta:
            weft_attrs["eval"] = eval_meta
        attrs = {"weft": weft_attrs} if weft_attrs else None
        pas = pc = None
        eval_ctx = None
        if self._client is not None:
            pas = self._client.create_call(
                Evaluation.__dict__["predict_and_score"],
                {"self": self.evaluation, "model": self.model, "example": self._example(inputs)},
                parent=self._call,
                attributes=attrs,
            )
            pinputs = dict(inputs)
            if isinstance(self.model, Model):
                pinputs = {"self": self.model, **pinputs}
            pc = self._client.create_call(self._predict_op(), pinputs, parent=pas, attributes=attrs)
            if output is not None or linked:
                self._client.finish_call(pc, output=output)
            eval_ctx = {
                "run_id": self._call.id if self._call is not None else None,
                "predict_and_score_call_id": pas.id,
                "project_id": self._client.project_id,
                "kind": eval_kind,
                "row_digest": row_digest or _row_key(inputs),
                "example_id": example_id,
                "trial_index": trial_index,
                "evaluation_name": self.evaluation.name,
            }
        sl = ScoreLogger(self, pas, dict(inputs), output, model_latency, predict_call=pc, eval_context=eval_ctx)
        self._predictions.append(sl)
        return sl

    def _linked_output(self, obj: Any) -> "Tuple[Any, Optional[float]]":
        from ._call import Call

        c = obj
        if isinstance(obj, str) and self._client is not None:
            from ._refs import is_ref_uri, parse_uri

            cid = parse_uri(obj).id if is_ref_uri(obj) else obj  # type: ignore[attr-defined]
            try:
                c = self._client.span(cid)
            except Exception as e:  # the link is still recorded
                logger.warning("weft: could not read linked call %s: %s", cid, e)
                return None, None
        if not isinstance(c, Call):
            return None, None
        out = c._raw_output if (c._raw_output is not None and not c._fetched) else c.output
        lat = None
        a, b = _parse_ts(c.started_at), _parse_ts(c.ended_at)
        if a is not None and b is not None:
            lat = max(0.0, b - a)
        return out, lat

    def log_example(
        self,
        inputs: Dict[str, Any],
        output: Any,
        scores: Optional[Dict[str, Any]] = None,
        model_latency: Optional[float] = None,
    ) -> ScoreLogger:
        """Log a prediction and its scores in one call (the prediction is finished)."""
        pred = self.log_prediction(inputs, output, model_latency=model_latency)
        for name, value in (scores or {}).items():
            pred.log_score(name, value)
        pred.finish()
        return pred

    def log_summary(self, summary: Optional[Dict[str, Any]] = None, auto_summarize_scores: bool = True) -> Dict[str, Any]:
        if self._finished:
            return self.summary or {}
        for p in self._predictions:
            p.finish()
        result: Dict[str, Any] = {}
        if auto_summarize_scores:
            for name in self._scorer_names:
                s = auto_summarize([p.scores.get(name) for p in self._predictions])
                if s is not None:
                    result[name] = s
            lat = [p.model_latency for p in self._predictions if isinstance(p.model_latency, (int, float))]
            if lat:
                result["model_latency"] = {"mean": sum(lat) / len(lat)}
        if summary:
            result.update(summary)
        if self._client is not None and self._call is not None:
            sc = self._client.create_call(Evaluation.__dict__["summarize"], {"self": self.evaluation}, parent=self._call)
            self._client.finish_call(sc, output=result)
            self._client.finish_call(self._call, output=result)
        self._finished = True
        self.summary = result
        return result

    def finish(self) -> Dict[str, Any]:
        return self.log_summary()
