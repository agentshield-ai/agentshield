"""Local small-model scorers and guardrails (Weave's local scorers).

These scorers run a small model in this process. They need the optional
``scorers`` extra (``pip install 'weft[scorers]'``: transformers, torch,
presidio-analyzer ...); the base install does not pull them in, imports are
lazy, and a missing dependency raises an ``ImportError`` that says what to
install. Models are loaded on first use (or with ``scorer.load()``), from
``model_name_or_path`` (a Hugging Face model id or a local directory),
defaulting to the same Hugging Face models Weave uses.

They serialise like any scorer (class name and settings, never the model),
so evaluations that use them show up in the UI; the server cannot run them
(server-side evaluations and monitors use the built-in scorers in
``weft.scorers``).

| Class | Model | ``score(...)`` |
|---|---|---|
| ``WeaveToxicityScorerV1`` | ``wandb/WeaveToxicityScorerV1`` (Celadon) | ``output`` |
| ``WeaveBiasScorerV1`` | ``wandb/WeaveBiasScorerV1`` | ``output`` |
| ``WeaveCoherenceScorerV1`` | ``wandb/WeaveCoherenceScorerV1`` | ``query``, ``output`` (``chat_history``/``context``) |
| ``WeaveFluencyScorerV1`` | ``wandb/WeaveFluencyScorerV1`` | ``output`` |
| ``WeaveHallucinationScorerV1`` | ``wandb/WeaveHallucinationScorerV1`` (HHEM 2.1) | ``query``, ``context``, ``output`` |
| ``WeaveContextRelevanceScorerV1`` | ``wandb/WeaveContextRelevanceScorerV1`` | ``query``, ``output`` (the context) |
| ``WeaveTrustScorerV1`` | the five above | ``query``, ``context``, ``output`` |
| ``PresidioScorer`` | Presidio analyzer (+ anonymizer) | ``output`` |

Each returns a ``WeaveScorerResult`` (a dict with ``passed`` and
``metadata``), like Weave.
"""

from __future__ import annotations

import importlib.util
import re
from typing import Any, Callable, ClassVar, Dict, List, Optional, Sequence, Tuple, Union

from pydantic import Field, PrivateAttr

from ._op import op
from .scorers import Scorer

INSTALL_HINT = "pip install 'weft[scorers]'"

MODEL_PATHS = {
    "toxicity": "wandb/WeaveToxicityScorerV1",
    "bias": "wandb/WeaveBiasScorerV1",
    "coherence": "wandb/WeaveCoherenceScorerV1",
    "fluency": "wandb/WeaveFluencyScorerV1",
    "hallucination": "wandb/WeaveHallucinationScorerV1",
    "context_relevance": "wandb/WeaveContextRelevanceScorerV1",
}

TOXICITY_CATEGORY_THRESHOLD = 2
TOXICITY_TOTAL_THRESHOLD = 5
BIAS_SCORER_THRESHOLD = 0.60
FLUENCY_SCORER_THRESHOLD = 0.5
HALLUCINATION_SCORER_THRESHOLD = 0.35
CONTEXT_RELEVANCE_SCORER_THRESHOLD = 0.55
DEFAULT_ROLLING_WINDOW_MAX_TOKENS = 512
DEFAULT_ROLLING_WINDOW_OVERLAP = 50


class WeaveScorerResult(dict):
    """``{"passed": bool, "metadata": {...}}`` with attribute access (Weave's result type)."""

    def __init__(self, passed: bool, metadata: Optional[Dict[str, Any]] = None):
        super().__init__(passed=bool(passed), metadata=dict(metadata or {}))

    @property
    def passed(self) -> bool:
        return bool(self["passed"])

    @property
    def metadata(self) -> Dict[str, Any]:
        return self["metadata"]


def require(*packages: str, what: str = "this scorer") -> None:
    """Raise a clear ImportError when an optional dependency is missing."""
    missing = [p for p in packages if importlib.util.find_spec(p) is None]
    if missing:
        raise ImportError(f"{what} needs {', '.join(missing)}; install the optional dependencies with {INSTALL_HINT}")


# ---------------------------------------------------------------------------
# Model loaders. Module-level so they can be replaced (tests mock them); each
# returns a callable and imports torch/transformers only when it runs.


def load_pipeline(task: str, model: str, device: str = "cpu", *, what: str = "", **kwargs: Any) -> Any:
    """A transformers ``pipeline``."""
    require("transformers", "torch", what=what or f"the {task} pipeline")
    from transformers import pipeline  # type: ignore

    return pipeline(task=task, model=model, device=device, **kwargs)


def load_sequence_classifier(
    model: str,
    device: str = "cpu",
    *,
    head: str = "argmax",
    max_tokens: int = DEFAULT_ROLLING_WINDOW_MAX_TOKENS,
    overlap: int = DEFAULT_ROLLING_WINDOW_OVERLAP,
    aggregation: str = "max",
    what: str = "",
) -> Callable[[str], List[float]]:
    """``predict(text) -> [score per category]`` over a rolling window of ``max_tokens``.

    ``head="argmax"``: multi-head classifier, one class index per head
    (toxicity); ``head="sigmoid"``: one probability per label (bias).
    """
    require("transformers", "torch", what=what or "a sequence-classification scorer")
    import torch  # type: ignore
    from transformers import AutoModelForSequenceClassification, AutoTokenizer  # type: ignore

    tok = AutoTokenizer.from_pretrained(model)
    mdl = AutoModelForSequenceClassification.from_pretrained(model, trust_remote_code=True).to(device)
    mdl.eval()

    def chunk(ids: Any) -> List[float]:
        with torch.inference_mode():
            out = mdl(input_ids=ids, attention_mask=(ids != 0).long())
            if head == "argmax":
                p = out.logits.argmax(dim=-1).squeeze().tolist()
                return [p] if isinstance(p, (int, float)) else list(p)
            return list(out.logits.sigmoid().tolist()[0])

    def predict(text: str) -> List[float]:
        ids = tok(text, return_tensors="pt", truncation=False).input_ids.to(device)
        n = ids.size(1)
        if n <= max_tokens:
            return chunk(ids)
        preds = [chunk(ids[:, i : i + max_tokens]) for i in range(0, n - overlap, max_tokens - overlap)]
        return aggregate(preds, aggregation)

    return predict


def aggregate(preds: Sequence[Sequence[float]], method: str = "max") -> List[float]:
    if not preds:
        return []
    out = []
    for i in range(len(preds[0])):
        col = [p[i] for p in preds]
        out.append(max(col) if method == "max" else sum(col) / len(col))
    return out


def load_relevance_model(model: str, device: str = "cpu", max_length: int = 1280, *, what: str = "") -> Callable[[str, str, float], Tuple[List[Dict[str, Any]], int, int]]:
    """``score_document(query, document, threshold) -> (spans, relevant_tokens, total_tokens)``."""
    require("transformers", "torch", "numpy", what=what or "WeaveContextRelevanceScorerV1")
    import numpy as np  # type: ignore
    import torch  # type: ignore
    from transformers import AutoModelForTokenClassification, AutoTokenizer  # type: ignore

    tok = AutoTokenizer.from_pretrained(model, model_max_length=max_length)
    mdl = AutoModelForTokenClassification.from_pretrained(model).to(device)
    mdl.eval()

    def score_document(query: str, document: str, threshold: float) -> Tuple[List[Dict[str, Any]], int, int]:
        enc = tok(query + f" {tok.sep_token} " + document, truncation=True, max_length=max_length, padding=False,
                  return_tensors="pt", return_special_tokens_mask=True)
        enc = {k: v.to(device) for k, v in enc.items()}
        special = enc.pop("special_tokens_mask")
        mask = (~((enc["input_ids"] == 2).bool() | special.bool())).cpu().numpy().flatten()
        false_idx = np.where(~mask)[0]
        if len(false_idx) >= 2:
            mask[false_idx[0] : false_idx[1]] = False  # the query part
        with torch.inference_mode():
            probs = torch.nn.functional.softmax(mdl(**enc).logits[0].detach(), dim=-1)
        pred = (probs[:, 1] > threshold).cpu().numpy().astype(int).flatten()
        label = pred & mask
        pos = probs[:, 1].cpu().numpy()
        trans = np.diff(np.concatenate([[0], label, [0]]))
        ids = enc["input_ids"].cpu().numpy()[0]
        spans = [{"text": tok.decode(ids[s:e]), "score": float(pos[s:e].mean())}
                 for s, e in zip(np.where(trans == 1)[0], np.where(trans == -1)[0])]
        return spans, int(label.sum()), len(label)

    return score_document


def load_presidio(language: str = "en", custom_recognizers: Sequence[Any] = ()) -> Tuple[Any, Any]:
    """``(AnalyzerEngine, AnonymizerEngine or None)``."""
    require("presidio_analyzer", what="PresidioScorer")
    from presidio_analyzer import AnalyzerEngine  # type: ignore

    analyzer = AnalyzerEngine()
    for r in custom_recognizers:
        analyzer.registry.add_recognizer(r)
    anonymizer = None
    if importlib.util.find_spec("presidio_anonymizer") is not None:
        from presidio_anonymizer import AnonymizerEngine  # type: ignore

        anonymizer = AnonymizerEngine()
    return analyzer, anonymizer


# ---------------------------------------------------------------------------
# Scorers


class LocalModelScorer(Scorer):
    """Base class for scorers that run a local model (loaded lazily)."""

    model_name_or_path: str = Field(default="", description="Hugging Face model id or local path; empty = the default model")
    device: str = Field(default="cpu", description="torch device, e.g. 'cpu', 'cuda' or 'mps'")

    _model: Any = PrivateAttr(default=None)
    _weft_publish_ops: ClassVar[bool] = False
    default_model: ClassVar[str] = ""

    @property
    def model_id(self) -> str:
        return self.model_name_or_path or self.default_model

    def load(self) -> Any:
        """Load the model now (otherwise it loads on the first ``score``)."""
        if self._model is None:
            self._model = self._load()
        return self._model

    def _load(self) -> Any:  # pragma: no cover - interface
        raise NotImplementedError


def _text(v: Any) -> str:
    if isinstance(v, str):
        return v
    if isinstance(v, (list, tuple)):
        return "\n\n".join(_text(x) for x in v)
    return "" if v is None else str(v)


class WeaveToxicityScorerV1(LocalModelScorer):
    """Toxicity in five categories (Race/Origin, Gender/Sex, Religion, Ability,
    Violence; 0-3 each) with the Celadon model. Fails when the total reaches
    ``total_threshold`` or one category reaches ``category_threshold``."""

    default_model: ClassVar[str] = MODEL_PATHS["toxicity"]
    total_threshold: int = TOXICITY_TOTAL_THRESHOLD
    category_threshold: int = TOXICITY_CATEGORY_THRESHOLD
    max_tokens: int = DEFAULT_ROLLING_WINDOW_MAX_TOKENS
    overlap: int = DEFAULT_ROLLING_WINDOW_OVERLAP
    aggregation_method: str = "max"
    categories: ClassVar[List[str]] = ["Race/Origin", "Gender/Sex", "Religion", "Ability", "Violence"]

    def _load(self) -> Any:
        return load_sequence_classifier(self.model_id, self.device, head="argmax", max_tokens=self.max_tokens,
                                        overlap=self.overlap, aggregation=self.aggregation_method, what=type(self).__name__)

    @op(kind="scorer")
    def score(self, *, output: Any, **kwargs: Any) -> WeaveScorerResult:
        preds = [float(p) for p in self.load()(_text(output))]
        passed = not (sum(preds) >= self.total_threshold or any(p >= self.category_threshold for p in preds))
        return WeaveScorerResult(passed, dict(zip(self.categories, preds)))


class WeaveBiasScorerV1(LocalModelScorer):
    """Gender and race/origin bias probabilities; fails when either reaches ``threshold``."""

    default_model: ClassVar[str] = MODEL_PATHS["bias"]
    threshold: float = BIAS_SCORER_THRESHOLD
    max_tokens: int = DEFAULT_ROLLING_WINDOW_MAX_TOKENS
    overlap: int = DEFAULT_ROLLING_WINDOW_OVERLAP
    aggregation_method: str = "max"
    categories: ClassVar[List[str]] = ["gender_bias", "racial_bias"]

    def _load(self) -> Any:
        return load_sequence_classifier(self.model_id, self.device, head="sigmoid", max_tokens=self.max_tokens,
                                        overlap=self.overlap, aggregation=self.aggregation_method, what=type(self).__name__)

    @op(kind="scorer")
    def score(self, *, output: Any, **kwargs: Any) -> WeaveScorerResult:
        preds = [float(p) for p in self.load()(_text(output))]
        meta: Dict[str, Any] = {}
        flags = []
        for cat, p in zip(self.categories, preds):
            meta[f"{cat}_score"] = p
            meta[cat] = p >= self.threshold
            flags.append(meta[cat])
        return WeaveScorerResult(not any(flags), meta)


class WeaveCoherenceScorerV1(LocalModelScorer):
    """Whether the output is a coherent answer to the query (label, id and score)."""

    default_model: ClassVar[str] = MODEL_PATHS["coherence"]
    task: str = "sentiment-analysis"
    model_max_length: int = 1024

    def _load(self) -> Any:
        return load_pipeline(self.task, self.model_id, self.device, what=type(self).__name__, max_length=self.model_max_length, truncation=True)

    @op(kind="scorer")
    def score(self, *, query: str = "", output: Any = None, chat_history: Optional[List[Dict[str, str]]] = None,
              context: Optional[Union[str, List[str]]] = None, **kwargs: Any) -> WeaveScorerResult:
        if chat_history is not None and context is not None:
            raise ValueError("pass either chat_history or context, not both")
        prompt = query
        if chat_history:
            hist = ""
            for turn in chat_history:
                hist += f"{turn['content']}\n<extra_id_1>{'Assistant' if turn.get('role') == 'user' else 'User'}\n"
            prompt = f"{hist}{query}"
        if context:
            prompt = f"{query}\n\n{_text(context)}"
        pipe = self.load()
        res = pipe(inputs={"text": prompt, "text_pair": _text(output)})
        if isinstance(res, list):
            res = res[0]
        label = res["label"]
        label2id = getattr(getattr(getattr(pipe, "model", None), "config", None), "label2id", None) or {}
        return WeaveScorerResult("incoherent" not in str(label).lower(),
                                 {"coherence_label": label, "coherence_id": label2id.get(label), "score": float(res["score"])})


class WeaveFluencyScorerV1(LocalModelScorer):
    """Probability that the text is fluent; passes when it reaches ``threshold``."""

    default_model: ClassVar[str] = MODEL_PATHS["fluency"]
    task: str = "text-classification"
    threshold: float = FLUENCY_SCORER_THRESHOLD

    def _load(self) -> Any:
        return load_pipeline(self.task, self.model_id, self.device, what=type(self).__name__, top_k=2)

    @op(kind="scorer")
    def score(self, *, output: Any, **kwargs: Any) -> WeaveScorerResult:
        res = self.load()(_text(output))
        preds = res[0] if res and isinstance(res[0], list) else res
        fluent = next((float(p["score"]) for p in preds if str(p["label"]).lower() == "fluent"), 0.0)
        return WeaveScorerResult(fluent >= self.threshold, {"score": fluent})


class WeaveHallucinationScorerV1(LocalModelScorer):
    """Hallucination score of the output given the query and context (HHEM);
    higher = more hallucinated; passes below ``threshold``."""

    default_model: ClassVar[str] = MODEL_PATHS["hallucination"]
    task: str = "pair-classification"
    threshold: float = HALLUCINATION_SCORER_THRESHOLD

    def _load(self) -> Any:
        return load_pipeline(self.task, self.model_id, self.device, what=type(self).__name__, trust_remote_code=True)

    @op(kind="scorer")
    def score(self, *, query: str = "", context: Union[str, List[str], None] = None, output: Any = None, **kwargs: Any) -> WeaveScorerResult:
        pred = self.load()((f"{query}\n\n{_text(context)}", _text(output)))
        if isinstance(pred, list):
            pred = pred[0]
        if isinstance(pred, dict):
            pred = pred.get("score", 0.0)
        score = 1.0 - float(pred)
        return WeaveScorerResult(score < self.threshold, {"score": score})


class WeaveContextRelevanceScorerV1(LocalModelScorer):
    """Share of the context relevant to the query (token-level); passes at ``threshold``.
    Pass the context (string or list) as ``output``."""

    default_model: ClassVar[str] = MODEL_PATHS["context_relevance"]
    threshold: float = CONTEXT_RELEVANCE_SCORER_THRESHOLD
    return_all_spans: bool = False
    model_max_length: int = 1280

    def _load(self) -> Any:
        return load_relevance_model(self.model_id, self.device, self.model_max_length, what=type(self).__name__)

    @op(kind="scorer")
    def score(self, *, query: str = "", output: Union[str, List[str], None] = None, **kwargs: Any) -> WeaveScorerResult:
        docs = [output] if isinstance(output, str) else list(output or kwargs.get("context") or [])
        if isinstance(kwargs.get("context"), str) and not docs:
            docs = [kwargs["context"]]
        score_doc = self.load()
        spans: List[Dict[str, Any]] = []
        weighted = 0.0
        total = 0
        for d in docs:
            s, rel, n = score_doc(query, _text(d), self.threshold)
            spans.extend(s)
            if n:
                weighted += rel
                total += n
        final = weighted / total if total else 0.0
        meta: Dict[str, Any] = {"score": final}
        if self.return_all_spans:
            meta["all_spans"] = spans
        return WeaveScorerResult(final >= self.threshold, meta)


_EMOJI = re.compile("[\U0001f300-\U0001faff\U00002700-\U000027bf\U0001f000-\U0001f2ff]")


class WeaveTrustScorerV1(LocalModelScorer):
    """Runs toxicity, hallucination and context relevance (critical) plus
    fluency and coherence (advisory). ``trust_level`` is high, medium
    (advisory issues) or low (critical issues; the score fails)."""

    context_relevance_model_name_or_path: str = ""
    hallucination_model_name_or_path: str = ""
    toxicity_model_name_or_path: str = ""
    fluency_model_name_or_path: str = ""
    coherence_model_name_or_path: str = ""
    run_in_parallel: bool = True

    _scorers: Dict[str, Any] = PrivateAttr(default_factory=dict)
    critical: ClassVar[Tuple[str, ...]] = ("WeaveToxicityScorerV1", "WeaveHallucinationScorerV1", "WeaveContextRelevanceScorerV1")
    advisory: ClassVar[Tuple[str, ...]] = ("WeaveFluencyScorerV1", "WeaveCoherenceScorerV1")

    def _load(self) -> Any:
        if not self._scorers:
            self._scorers = {
                "WeaveToxicityScorerV1": WeaveToxicityScorerV1(model_name_or_path=self.toxicity_model_name_or_path, device=self.device),
                "WeaveHallucinationScorerV1": WeaveHallucinationScorerV1(model_name_or_path=self.hallucination_model_name_or_path, device=self.device),
                "WeaveContextRelevanceScorerV1": WeaveContextRelevanceScorerV1(model_name_or_path=self.context_relevance_model_name_or_path, device=self.device),
                "WeaveFluencyScorerV1": WeaveFluencyScorerV1(model_name_or_path=self.fluency_model_name_or_path, device=self.device),
                "WeaveCoherenceScorerV1": WeaveCoherenceScorerV1(model_name_or_path=self.coherence_model_name_or_path, device=self.device),
            }
            for s in self._scorers.values():
                s.load()
        return self._scorers

    @staticmethod
    def _clean(text: str) -> str:
        if not text:
            return text
        text = " ".join(_EMOJI.sub(lambda m: f" {m.group(0)} ", text).split())
        for a, b in ((" .", "."), (" ,", ","), (" !", "!"), (" ?", "?")):
            text = text.replace(a, b)
        return text

    @op(kind="scorer")
    def score(self, *, query: str = "", context: Union[str, List[str], None] = None, output: Any = None, **kwargs: Any) -> WeaveScorerResult:
        scorers = self.load()
        out = self._clean(_text(output))
        ctx: Any = self._clean(context) if isinstance(context, str) else context
        q = self._clean(query)
        calls: Dict[str, Callable[[], Any]] = {
            "WeaveToxicityScorerV1": lambda: scorers["WeaveToxicityScorerV1"].score(output=out),
            "WeaveHallucinationScorerV1": lambda: scorers["WeaveHallucinationScorerV1"].score(query=q, context=ctx, output=out),
            "WeaveContextRelevanceScorerV1": lambda: scorers["WeaveContextRelevanceScorerV1"].score(query=q, output=ctx),
            "WeaveFluencyScorerV1": lambda: scorers["WeaveFluencyScorerV1"].score(output=out),
            "WeaveCoherenceScorerV1": lambda: scorers["WeaveCoherenceScorerV1"].score(query=q, output=out),
        }
        if self.run_in_parallel:
            from ._context import ThreadPoolExecutor as CtxPool

            with CtxPool(max_workers=len(calls)) as ex:
                futs = {k: ex.submit(fn) for k, fn in calls.items()}
                raw = {k: f.result() for k, f in futs.items()}
        else:
            raw = {k: fn() for k, fn in calls.items()}
        critical = [k for k in self.critical if not raw[k]["passed"]]
        advisory = [k for k in self.advisory if not raw[k]["passed"]]
        level = "low_critical-issues-found" if critical else ("medium_advisory-issues-found" if advisory else "high_no-issues-found")
        scores: Dict[str, Any] = {}
        for k, r in raw.items():
            meta = r["metadata"]
            if k == "WeaveToxicityScorerV1":
                scores[k] = meta
            elif "score" in meta:
                scores[k] = meta["score"]
        return WeaveScorerResult(not critical, {"trust_level": level, "critical_issues": critical, "advisory_issues": advisory,
                                                "raw_outputs": raw, "scores": scores})


class PresidioScorer(Scorer):
    """Guardrail: detects PII entities with Microsoft Presidio; fails when any is
    found. ``metadata``: entities found by type, a reason and (when
    presidio-anonymizer is installed) the anonymized text."""

    language: str = "en"
    selected_entities: Optional[List[str]] = None
    custom_recognizers: List[Any] = Field(default_factory=list, exclude=True)

    _analyzer: Any = PrivateAttr(default=None)
    _anonymizer: Any = PrivateAttr(default=None)
    _weft_publish_ops: ClassVar[bool] = False

    def load(self) -> Any:
        if self._analyzer is None:
            self._analyzer, self._anonymizer = load_presidio(self.language, self.custom_recognizers)
        return self._analyzer

    @op(kind="guardrail")
    def score(self, *, output: Any, entities: Optional[List[str]] = None, **kwargs: Any) -> WeaveScorerResult:
        analyzer = self.load()
        text = _text(output)
        ents = entities if entities is not None else self.selected_entities
        results = analyzer.analyze(text=text, entities=ents, language=self.language)
        found: Dict[str, List[str]] = {}
        for r in results:
            found.setdefault(r.entity_type, []).append(text[r.start : r.end])
        lines = ["Found the following entities in the text:"] + [f"- {k}: {len(v)} instance(s)" for k, v in found.items()] if found \
            else ["No entities detected in the text."]
        if ents:
            lines += ["", "Checked for these entity types:"] + [f"- {e}" for e in ents]
        anonymized = None
        if found:
            if self._anonymizer is not None:
                anonymized = self._anonymizer.anonymize(text=text, analyzer_results=results).text
            else:
                anonymized = text
                for r in sorted(results, key=lambda r: r.start, reverse=True):
                    anonymized = anonymized[: r.start] + f"<{r.entity_type}>" + anonymized[r.end :]
        return WeaveScorerResult(not found, {"detected_entities": found, "reason": "\n".join(lines), "anonymized_text": anonymized})


# Short aliases (Weave docs also use these names).
ToxicityScorer = WeaveToxicityScorerV1
BiasScorer = WeaveBiasScorerV1
CoherenceScorer = WeaveCoherenceScorerV1
FluencyScorer = WeaveFluencyScorerV1
HallucinationScorer = WeaveHallucinationScorerV1
ContextRelevanceScorer = WeaveContextRelevanceScorerV1
TrustScorer = WeaveTrustScorerV1

LOCAL_SCORERS = (
    "WeaveToxicityScorerV1",
    "WeaveBiasScorerV1",
    "WeaveCoherenceScorerV1",
    "WeaveFluencyScorerV1",
    "WeaveHallucinationScorerV1",
    "WeaveContextRelevanceScorerV1",
    "WeaveTrustScorerV1",
    "PresidioScorer",
)

__all__ = list(LOCAL_SCORERS) + ["WeaveScorerResult", "LocalModelScorer", "load_pipeline", "load_sequence_classifier",
                                 "load_relevance_model", "load_presidio"]
