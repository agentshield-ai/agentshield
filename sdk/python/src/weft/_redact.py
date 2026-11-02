"""Redaction of sensitive values in call inputs and outputs.

By default the values of dict keys named like credentials (``api_key``,
``authorization``, ``password``, ``token`` ...; case-insensitive, exact key
match) are replaced with ``"REDACTED"`` before anything leaves the process.
Configure with ``weft.init(..., settings={"redact_keys": [...], "redact_pii": True})``
or disable key redaction with ``WEFT_REDACT_KEYS=0``.

PII redaction (``redact_pii``, env ``WEFT_REDACT_PII``) replaces entities in
strings with ``<ENTITY_TYPE>``. Regular expressions always handle e-mail
addresses, US SSNs, credit cards, phone numbers and IP addresses. Names,
locations, organisations and Presidio's other entities need an NER engine,
chosen with ``redact_pii_engine`` (env ``WEFT_REDACT_PII_ENGINE``):

* ``"auto"`` (default): Presidio when ``presidio-analyzer`` is installed
  (``pip install 'weft[pii]'`` plus a spaCy model), else regex only.
* ``"presidio"``: Presidio's ``AnalyzerEngine``; an error if not installed.
* ``"transformers"``: a Hugging Face token-classification pipeline
  (``redact_pii_model``, default ``dslim/bert-base-NER``; PER/LOC/ORG map
  to PERSON/LOCATION/ORGANIZATION).
* ``"regex"``: regular expressions only.

``redact_pii_fields`` (env ``WEFT_REDACT_PII_FIELDS``, comma-separated) picks
the entity types (default ``DEFAULT_PII_ENTITIES``, Weave's list plus
ORGANIZATION); ``redact_pii_exclude_fields`` (env
``WEFT_REDACT_PII_EXCLUDE_FIELDS``) removes some. ``redact_pii_analyzer`` may
be any object with Presidio's ``analyze(text=, entities=, language=)`` method,
or a ``callable(text) -> [(entity_type, start, end), ...]``.
``redact_pii_score_threshold`` drops low-confidence NER spans (default 0.5
for transformers; Presidio's own default otherwise).
"""

from __future__ import annotations

import importlib.util
import logging
import os
import re
import threading
from typing import Any, Callable, Dict, Iterable, List, Mapping, Optional, Sequence, Set, Tuple

logger = logging.getLogger("weft")

REDACTED = "REDACTED"

DEFAULT_REDACT_KEYS = (
    "api_key",
    "apikey",
    "authorization",
    "auth",
    "password",
    "secret",
    "token",
    "access_token",
)

# Ordered: more specific patterns first.
_PII_PATTERNS = [
    (re.compile(r"[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}"), "<EMAIL_ADDRESS>"),
    (re.compile(r"\b\d{3}-\d{2}-\d{4}\b"), "<US_SSN>"),
    (re.compile(r"\b(?:\d[ -]?){13,16}\b"), "<CREDIT_CARD>"),
    (re.compile(r"(?<![\w])(?:\+?\d{1,3}[ .-]?)?\(?\d{3}\)?[ .-]?\d{3}[ .-]?\d{4}\b"), "<PHONE_NUMBER>"),
    (re.compile(r"\b(?:\d{1,3}\.){3}\d{1,3}\b"), "<IP_ADDRESS>"),
]


_REGEX_ENTITIES = {"EMAIL_ADDRESS", "US_SSN", "CREDIT_CARD", "PHONE_NUMBER", "IP_ADDRESS"}

#: Entities redacted by default when ``redact_pii`` is on (Weave's defaults plus ORGANIZATION).
DEFAULT_PII_ENTITIES = (
    "CREDIT_CARD",
    "CRYPTO",
    "EMAIL_ADDRESS",
    "IBAN_CODE",
    "IP_ADDRESS",
    "LOCATION",
    "PERSON",
    "ORGANIZATION",
    "PHONE_NUMBER",
    "US_SSN",
    "US_BANK_NUMBER",
    "US_DRIVER_LICENSE",
    "US_PASSPORT",
    "UK_NHS",
    "UK_NINO",
    "ES_NIF",
    "IN_AADHAAR",
    "IN_PAN",
    "FI_PERSONAL_IDENTITY_CODE",
)

#: Hugging Face NER labels -> Presidio entity names.
NER_LABELS = {"PER": "PERSON", "PERSON": "PERSON", "LOC": "LOCATION", "LOCATION": "LOCATION", "GPE": "LOCATION",
              "ORG": "ORGANIZATION", "ORGANIZATION": "ORGANIZATION"}

PII_INSTALL_HINT = (
    "PII redaction with names, locations and organisations needs an NER engine: "
    "pip install 'weft[pii]' (presidio-analyzer, plus `python -m spacy download en_core_web_lg`) "
    "or pip install 'weft[scorers]' and set redact_pii_engine='transformers'."
)


def redact_pii_text(s: str, entities: Optional[Iterable[str]] = None) -> str:
    """Regex redaction (e-mail, SSN, credit card, phone, IP)."""
    allowed = None if entities is None else set(entities)
    for pat, repl in _PII_PATTERNS:
        if allowed is not None and repl.strip("<>") not in allowed:
            continue
        s = pat.sub(repl, s)
    return s


Span = Tuple[str, int, int, float]
_ENGINES: Dict[Any, Any] = {}
_ENGINES_LOCK = threading.Lock()


def _load_presidio() -> Any:
    try:
        from presidio_analyzer import AnalyzerEngine  # type: ignore
    except ImportError as e:
        raise ImportError(PII_INSTALL_HINT) from e
    return AnalyzerEngine()


def _load_transformers_ner(model: str) -> Any:
    try:
        from transformers import pipeline  # type: ignore
    except ImportError as e:
        raise ImportError(PII_INSTALL_HINT) from e
    return pipeline("token-classification", model=model, aggregation_strategy="simple")


def _engine(kind: str, model: Optional[str] = None) -> Any:
    key = (kind, model)
    with _ENGINES_LOCK:
        eng = _ENGINES.get(key)
    if eng is None:
        eng = _load_presidio() if kind == "presidio" else _load_transformers_ner(model or "dslim/bert-base-NER")
        with _ENGINES_LOCK:
            _ENGINES[key] = eng
    return eng


def _env_list(name: str) -> Optional[List[str]]:
    raw = os.environ.get(name)
    if not raw:
        return None
    return [x.strip().upper() for x in raw.split(",") if x.strip()]


class NERRedactor:
    """Finds entity spans with Presidio, a transformers NER pipeline or a custom analyzer."""

    def __init__(self, engine: str, entities: Sequence[str], *, analyzer: Any = None, model: Optional[str] = None,
                 score_threshold: Optional[float] = None, language: str = "en"):
        self.engine = engine
        self.entities = list(entities)
        self.analyzer = analyzer
        self.model = model
        self.score_threshold = score_threshold
        self.language = language

    def _get(self) -> Any:
        if self.analyzer is None:
            self.analyzer = _engine(self.engine, self.model)
        return self.analyzer

    def spans(self, text: str) -> List[Span]:
        a = self._get()
        out: List[Span] = []
        if self.engine == "transformers" or (callable(a) and not hasattr(a, "analyze")):
            res = a(text)
            for r in res or []:
                if isinstance(r, (tuple, list)):
                    ent, start, end = r[0], int(r[1]), int(r[2])
                    score = float(r[3]) if len(r) > 3 else 1.0
                else:
                    label = str(r.get("entity_group") or r.get("entity") or "")
                    label = label.split("-", 1)[-1] if label[:2] in ("B-", "I-") else label
                    ent = NER_LABELS.get(label.upper(), label.upper())
                    start, end, score = int(r.get("start", 0)), int(r.get("end", 0)), float(r.get("score", 1.0))
                thr = 0.5 if self.score_threshold is None and self.engine == "transformers" else (self.score_threshold or 0.0)
                if ent in self.entities and score >= thr and end > start:
                    out.append((ent, start, end, score))
            return out
        kw: Dict[str, Any] = {"text": text, "entities": self.entities, "language": self.language}
        if self.score_threshold is not None:
            kw["score_threshold"] = self.score_threshold
        for r in a.analyze(**kw) or []:
            ent = getattr(r, "entity_type", None) or (r.get("entity_type") if isinstance(r, dict) else None)
            start = getattr(r, "start", None) if not isinstance(r, dict) else r.get("start")
            end = getattr(r, "end", None) if not isinstance(r, dict) else r.get("end")
            score = getattr(r, "score", 1.0) if not isinstance(r, dict) else r.get("score", 1.0)
            if ent and start is not None and end is not None and end > start:
                out.append((str(ent), int(start), int(end), float(score or 0.0)))
        return out

    def __call__(self, text: str) -> str:
        spans = self.spans(text)
        if not spans:
            return text
        # keep the longest/most confident of overlapping spans
        spans.sort(key=lambda s: (s[1], -(s[2] - s[1]), -s[3]))
        chosen: List[Span] = []
        for sp in spans:
            if chosen and sp[1] < chosen[-1][2]:
                continue
            chosen.append(sp)
        for ent, start, end, _ in reversed(chosen):
            text = text[:start] + f"<{ent}>" + text[end:]
        return text


def build_ner(settings: Mapping[str, Any], entities: Sequence[str]) -> Optional[NERRedactor]:
    engine = str(settings.get("redact_pii_engine") or os.environ.get("WEFT_REDACT_PII_ENGINE") or "auto").strip().lower()
    analyzer = settings.get("redact_pii_analyzer")
    threshold = settings.get("redact_pii_score_threshold")
    model = settings.get("redact_pii_model") or os.environ.get("WEFT_REDACT_PII_MODEL")
    # regex handles its entities; an NER model only needs the others
    ner_entities = [e for e in entities if e not in _REGEX_ENTITIES]
    if analyzer is not None:
        return NERRedactor("custom", entities, analyzer=analyzer, score_threshold=threshold)
    if engine == "regex" or not ner_entities:
        return None
    if engine == "auto":
        if importlib.util.find_spec("presidio_analyzer") is None:
            logger.debug("weft: presidio-analyzer not installed; PII redaction uses regular expressions only")
            return None
        engine = "presidio"
    if engine == "presidio":
        if importlib.util.find_spec("presidio_analyzer") is None:
            raise ImportError(PII_INSTALL_HINT)
        return NERRedactor("presidio", entities, score_threshold=threshold)
    if engine in ("transformers", "hf", "huggingface"):
        if importlib.util.find_spec("transformers") is None:
            raise ImportError(PII_INSTALL_HINT)
        return NERRedactor("transformers", ner_entities, model=model, score_threshold=threshold)
    raise ValueError(f"unknown redact_pii_engine {engine!r} (auto, presidio, transformers, regex)")


class Redactor:
    def __init__(self, keys: Optional[Iterable[str]] = None, pii: Optional[bool] = False, enabled: Optional[bool] = None,
                 settings: Optional[Mapping[str, Any]] = None):
        if enabled is None:
            enabled = os.environ.get("WEFT_REDACT_KEYS", "1").strip().lower() not in ("0", "false", "no", "off")
        self.keys: Set[str] = {k.lower() for k in (DEFAULT_REDACT_KEYS if keys is None else keys)} if enabled else set()
        if pii is None:
            pii = os.environ.get("WEFT_REDACT_PII", "").strip().lower() in ("1", "true", "yes", "on")
        self.pii = bool(pii)
        st = settings or {}
        fields = st.get("redact_pii_fields") or _env_list("WEFT_REDACT_PII_FIELDS") or list(DEFAULT_PII_ENTITIES)
        exclude = set(x.upper() for x in (st.get("redact_pii_exclude_fields") or _env_list("WEFT_REDACT_PII_EXCLUDE_FIELDS") or []))
        self.pii_entities: List[str] = [str(e).upper() for e in fields if str(e).upper() not in exclude]
        self.ner: Optional[NERRedactor] = build_ner(st, self.pii_entities) if self.pii else None
        self._ner_failed = False

    def redact_text(self, s: str) -> str:
        if self.ner is not None and not self._ner_failed and len(s) > 2 and any(c.isalpha() for c in s):
            try:
                s = self.ner(s)
            except Exception as e:  # never lose the call because NER failed
                self._ner_failed = True
                logger.warning("weft: NER PII redaction failed (%s); falling back to regular expressions", e)
        return redact_pii_text(s, self.pii_entities)

    @property
    def active(self) -> bool:
        return bool(self.keys) or self.pii

    def __call__(self, value: Any, _depth: int = 0) -> Any:
        """Redact JSON-safe data (dicts/lists/strings); returns a new value."""
        if not self.active or _depth > 64:
            return value
        if isinstance(value, dict):
            out = {}
            for k, v in value.items():
                if isinstance(k, str) and k.lower() in self.keys:
                    out[k] = REDACTED
                else:
                    out[k] = self(v, _depth + 1)
            return out
        if isinstance(value, list):
            return [self(v, _depth + 1) for v in value]
        if isinstance(value, str) and self.pii:
            return self.redact_text(value)
        return value
