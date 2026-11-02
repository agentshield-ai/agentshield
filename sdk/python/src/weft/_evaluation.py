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
            client.publish(self, name=self.name or "Evaluation")
            if isinstance(model, Object):
                client.publish(model)
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


class ScoreLogger:
    """Returned by ``EvaluationLogger.log_prediction``; log scores then ``finish()``."""

    def __init__(self, ev: "EvaluationLogger", call: Any, inputs: Dict[str, Any], output: Any, model_latency: Optional[float]):
        self._ev = ev
        self._call = call
        self.inputs = inputs
        self.output = output
        self.model_latency = model_latency
        self.scores: Dict[str, Any] = {}
        self._finished = False

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

    def finish(self) -> None:
        if self._finished:
            return
        self._finished = True
        client = self._ev._client
        out = {"output": self.output, "scores": dict(self.scores), "model_latency": self.model_latency}
        if client is not None and self._call is not None:
            client.finish_call(self._call, output=out)

    def __enter__(self) -> "ScoreLogger":
        return self

    def __exit__(self, *exc: Any) -> None:
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
    if isinstance(dataset, Dataset):
        return dataset
    if dataset is None:
        return Dataset(name="dataset", rows=[])
    if isinstance(dataset, str):
        return Dataset(name=dataset, rows=[])
    return Dataset(rows=list(dataset))


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
        if self._client is not None:
            self._client.publish(self.evaluation, name=self.evaluation.name)
            if isinstance(self.model, Object):
                self._client.publish(self.model)
            self._call = self._client.create_call(
                Evaluation.__dict__["_evaluate"], {"self": self.evaluation, "model": self.model}, display_name=name
            )

    @property
    def call(self) -> Any:
        return self._call

    def _predict_op(self) -> Any:
        m = self.model
        if isinstance(m, Model):
            pred = type(m).__dict__.get("predict")
            if isinstance(pred, Op):
                return pred
            return f"{type(m).__name__}.predict" if type(m) is not Model else f"{m.name or 'Model'}.predict"
        return m if isinstance(m, Op) else m.op

    def log_prediction(self, inputs: Dict[str, Any], output: Any, model_latency: Optional[float] = None) -> ScoreLogger:
        if self._finished:
            raise RuntimeError("evaluation already finished")
        call = None
        if self._client is not None:
            call = self._client.create_call(
                Evaluation.__dict__["predict_and_score"],
                {"self": self.evaluation, "model": self.model, "example": dict(inputs)},
                parent=self._call,
            )
            pinputs = dict(inputs)
            if isinstance(self.model, Model):
                pinputs = {"self": self.model, **pinputs}
            pc = self._client.create_call(self._predict_op(), pinputs, parent=call)
            self._client.finish_call(pc, output=output)
        sl = ScoreLogger(self, call, dict(inputs), output, model_latency)
        self._predictions.append(sl)
        return sl

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
