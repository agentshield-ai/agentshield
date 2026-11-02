"""Local small-model scorers (model loading mocked) and NER-based PII redaction (analyzer mocked)."""

import importlib.util
import types

import pytest

import weft
from weft import local_scorers as ls
from weft._redact import Redactor, build_ner


# ---------------------------------------------------------------- scorers
def test_base_install_has_no_heavy_deps_and_errors_clearly(monkeypatch):
    real = importlib.util.find_spec
    monkeypatch.setattr(importlib.util, "find_spec", lambda name, *a: None if name in ("torch", "transformers", "presidio_analyzer") else real(name, *a))
    s = weft.scorers.WeaveToxicityScorerV1()
    with pytest.raises(ImportError, match=r"WeaveToxicityScorerV1 needs transformers, torch.*weft\[scorers\]"):
        s.score(output="hi")
    with pytest.raises(ImportError, match="PresidioScorer needs presidio_analyzer"):
        ls.PresidioScorer().score(output="hi")


def test_toxicity_and_bias(monkeypatch):
    seen = {}

    def fake_classifier(model, device="cpu", **kw):
        seen.setdefault("models", []).append((model, device, kw["head"]))
        if kw["head"] == "argmax":
            return lambda text: [3, 0, 0, 0, 1] if "hate" in text else [0, 0, 0, 0, 0]
        return lambda text: [0.8, 0.1]

    monkeypatch.setattr(ls, "load_sequence_classifier", fake_classifier)
    tox = ls.WeaveToxicityScorerV1(device="mps")
    bad = tox.score(output="I hate them")
    assert bad.passed is False and bad.metadata == {"Race/Origin": 3.0, "Gender/Sex": 0.0, "Religion": 0.0, "Ability": 0.0, "Violence": 1.0}
    assert tox.score(output="hello").passed
    bias = ls.WeaveBiasScorerV1(model_name_or_path="/models/bias")
    r = bias.score(output="text")
    assert r == {"passed": False, "metadata": {"gender_bias_score": 0.8, "gender_bias": True, "racial_bias_score": 0.1, "racial_bias": False}}
    assert seen["models"] == [("wandb/WeaveToxicityScorerV1", "mps", "argmax"), ("/models/bias", "cpu", "sigmoid")]  # loaded once each


def test_rolling_window_aggregate():
    assert ls.aggregate([[1, 0.2], [0, 0.6]]) == [1, 0.6]
    assert ls.aggregate([[1, 0.2], [0, 0.6]], "mean") == [0.5, 0.4]


def test_pipeline_scorers(monkeypatch):
    def fake_pipeline(task, model, device="cpu", **kw):
        if model.endswith("Coherence" + "ScorerV1"):
            p = lambda inputs: {"label": "Mostly Coherent", "score": 0.7}
            p.model = types.SimpleNamespace(config=types.SimpleNamespace(label2id={"Mostly Coherent": 3}))
            return p
        if model.endswith("FluencyScorerV1"):
            return lambda text: [[{"label": "fluent", "score": 0.9}, {"label": "non-fluent", "score": 0.1}]]
        if model.endswith("HallucinationScorerV1"):
            return lambda pair: 0.2 if "Paris" in pair[1] else 0.9
        raise AssertionError(model)

    monkeypatch.setattr(ls, "load_pipeline", fake_pipeline)
    coh = ls.WeaveCoherenceScorerV1().score(query="How are you?", output="Fine, thanks.")
    assert coh.passed and coh.metadata == {"coherence_label": "Mostly Coherent", "coherence_id": 3, "score": 0.7}
    with pytest.raises(ValueError):
        ls.WeaveCoherenceScorerV1().score(query="q", output="o", chat_history=[], context="c")
    assert ls.WeaveFluencyScorerV1().score(output="Fine.").metadata == {"score": 0.9}
    h = ls.WeaveHallucinationScorerV1()
    assert not h.score(query="capital?", context=["France"], output="Paris").passed  # 1 - 0.2 = 0.8 >= 0.35
    assert h.score(query="capital?", context="France", output="Lyon").metadata["score"] == pytest.approx(0.1)


def test_context_relevance(monkeypatch):
    def fake_loader(model, device="cpu", max_length=1280, **kw):
        def score_doc(query, doc, threshold):
            return ([{"text": "Paris", "score": 0.9}], 6, 10) if "Paris" in doc else ([], 0, 10)
        return score_doc

    monkeypatch.setattr(ls, "load_relevance_model", fake_loader)
    r = ls.WeaveContextRelevanceScorerV1(return_all_spans=True).score(query="capital?", output=["Paris is the capital", "Bread"])
    assert r.metadata["score"] == pytest.approx(0.3) and not r.passed and r.metadata["all_spans"] == [{"text": "Paris", "score": 0.9}]


def test_trust_scorer(monkeypatch):
    monkeypatch.setattr(ls, "load_sequence_classifier", lambda *a, **k: lambda t: [0, 0, 0, 0, 0])
    monkeypatch.setattr(ls, "load_relevance_model", lambda *a, **k: lambda q, d, th: ([], 9, 10))

    def fake_pipeline(task, model, device="cpu", **kw):
        if "Coherence" in model:
            return lambda inputs: {"label": "Incoherent", "score": 0.6}
        if "Fluency" in model:
            return lambda text: [{"label": "fluent", "score": 0.95}]
        return lambda pair: 0.9

    monkeypatch.setattr(ls, "load_pipeline", fake_pipeline)
    r = ls.WeaveTrustScorerV1(run_in_parallel=False).score(query="capital?", context="Paris is the capital", output="Paris 🗼 .")
    assert r.passed and r.metadata["trust_level"] == "medium_advisory-issues-found"
    assert r.metadata["advisory_issues"] == ["WeaveCoherenceScorerV1"] and r.metadata["critical_issues"] == []
    assert r.metadata["scores"]["WeaveFluencyScorerV1"] == 0.95
    monkeypatch.setattr(ls, "load_sequence_classifier", lambda *a, **k: lambda t: [3, 3, 0, 0, 0])
    r2 = ls.WeaveTrustScorerV1().score(query="q", context="c", output="o")
    assert not r2.passed and "WeaveToxicityScorerV1" in r2.metadata["critical_issues"]


class _Result:
    def __init__(self, entity_type, start, end, score=0.85):
        self.entity_type, self.start, self.end, self.score = entity_type, start, end, score


class FakeAnalyzer:
    """Presidio-like analyzer: finds the names, places and organisations it knows."""

    KNOWN = {"Alice Smith": "PERSON", "Berlin": "LOCATION", "Acme Corp": "ORGANIZATION", "alice@example.com": "EMAIL_ADDRESS"}

    def __init__(self):
        self.calls = []

    def analyze(self, text, entities=None, language="en", **kw):
        self.calls.append((text, tuple(entities or ()), language))
        out = []
        for word, ent in self.KNOWN.items():
            i = text.find(word)
            if i >= 0 and (entities is None or ent in entities):
                out.append(_Result(ent, i, i + len(word)))
        return out


def test_presidio_scorer(monkeypatch):
    an = FakeAnalyzer()
    monkeypatch.setattr(ls, "load_presidio", lambda language="en", custom=(): (an, None))
    s = ls.PresidioScorer(selected_entities=["PERSON", "LOCATION"])
    r = s.score(output="Alice Smith lives in Berlin and works at Acme Corp")
    assert not r.passed
    assert r.metadata["detected_entities"] == {"PERSON": ["Alice Smith"], "LOCATION": ["Berlin"]}
    assert r.metadata["anonymized_text"] == "<PERSON> lives in <LOCATION> and works at Acme Corp"
    assert "PERSON: 1 instance(s)" in r.metadata["reason"]
    assert s.score(output="nothing here").passed


def test_local_scorers_serialise_and_run_in_evaluation(client, fake, monkeypatch):
    monkeypatch.setattr(ls, "load_sequence_classifier", lambda *a, **k: lambda t: [3, 0, 0, 0, 0] if "awful" in t else [0, 0, 0, 0, 0])
    tox = weft.scorers.ToxicityScorer()
    ds = weft.Dataset(name="tox-ds", rows=[{"text": "lovely"}, {"text": "awful people"}])

    @weft.op
    def echo(text):
        return text

    ev = weft.Evaluation(dataset=ds, scorers=[tox])
    summary = __import__("asyncio").run(ev.evaluate(echo))
    assert summary["WeaveToxicityScorerV1"]["passed"]["true_count"] == 1
    client.flush()
    objs = [o for versions in fake.objs.values() for o in versions]
    vals = [o.get("val") for o in objs if isinstance(o.get("val"), dict) and o["val"].get("_class_name") == "WeaveToxicityScorerV1"]
    assert vals and vals[0]["model_name_or_path"] == "" and vals[0]["total_threshold"] == 5 and "_model" not in vals[0]
    assert "LocalModelScorer" in vals[0]["_bases"]


# ---------------------------------------------------------------- NER PII redaction
def test_redactor_with_presidio_like_analyzer():
    an = FakeAnalyzer()
    r = Redactor(keys=[], pii=True, settings={"redact_pii_analyzer": an})
    out = r({"msg": "Alice Smith (alice@example.com, 555-123-4567) from Berlin at Acme Corp", "n": 3})
    assert out == {"msg": "<PERSON> (<EMAIL_ADDRESS>, <PHONE_NUMBER>) from <LOCATION> at <ORGANIZATION>", "n": 3}
    # entity selection and exclusion apply to both NER and regex
    r2 = Redactor(keys=[], pii=True, settings={"redact_pii_analyzer": FakeAnalyzer(), "redact_pii_fields": ["PERSON", "EMAIL_ADDRESS"]})
    assert r2("Alice Smith alice@example.com Berlin 10.0.0.1") == "<PERSON> <EMAIL_ADDRESS> Berlin 10.0.0.1"
    r3 = Redactor(keys=[], pii=True, settings={"redact_pii_analyzer": FakeAnalyzer(), "redact_pii_exclude_fields": ["location"]})
    assert r3("Alice Smith in Berlin") == "<PERSON> in Berlin"


def test_redactor_with_transformers_style_ner(monkeypatch):
    import weft._redact as rd

    def fake_pipeline(text):
        out = []
        for word, label, score in (("Alice", "PER", 0.99), ("Paris", "LOC", 0.97), ("Globex", "ORG", 0.93), ("Bob", "PER", 0.2), ("Thing", "MISC", 0.9)):
            i = text.find(word)
            if i >= 0:
                out.append({"entity_group": label, "start": i, "end": i + len(word), "score": score})
        return out

    monkeypatch.setattr(rd, "_load_transformers_ner", lambda model: fake_pipeline)
    monkeypatch.setattr(importlib.util, "find_spec", lambda name, *a: object() if name == "transformers" else None)
    r = Redactor(keys=[], pii=True, settings={"redact_pii_engine": "transformers"})
    assert r.ner is not None and r.ner.engine == "transformers"
    assert r("Alice met Bob in Paris at Globex about the Thing; mail a@b.io") == "<PERSON> met Bob in <LOCATION> at <ORGANIZATION> about the Thing; mail <EMAIL_ADDRESS>"


def test_redactor_engine_selection(monkeypatch):
    real = importlib.util.find_spec
    monkeypatch.setattr(importlib.util, "find_spec", lambda name, *a: None if name in ("presidio_analyzer", "transformers") else real(name, *a))
    assert build_ner({}, ["PERSON"]) is None  # auto without presidio: regex only
    assert Redactor(pii=True)("4111 1111 1111 1111") == "<CREDIT_CARD>"
    with pytest.raises(ImportError, match=r"weft\[pii\]"):
        build_ner({"redact_pii_engine": "presidio"}, ["PERSON"])
    with pytest.raises(ImportError):
        build_ner({"redact_pii_engine": "transformers"}, ["PERSON"])
    with pytest.raises(ValueError):
        build_ner({"redact_pii_engine": "spacy-magic"}, ["PERSON"])
    assert build_ner({"redact_pii_engine": "regex"}, ["PERSON"]) is None
    monkeypatch.setattr(importlib.util, "find_spec", lambda name, *a: object() if name == "presidio_analyzer" else real(name, *a))
    ner = build_ner({}, ["PERSON", "EMAIL_ADDRESS"])
    assert ner is not None and ner.engine == "presidio" and ner.entities == ["PERSON", "EMAIL_ADDRESS"]


def test_ner_failure_falls_back_to_regex():
    class Broken:
        def analyze(self, **kw):
            raise OSError("spaCy model en_core_web_lg not found")

    r = Redactor(keys=[], pii=True, settings={"redact_pii_analyzer": Broken()})
    assert r("Alice alice@example.com") == "Alice <EMAIL_ADDRESS>"


def test_pii_redaction_end_to_end(fake, monkeypatch):
    monkeypatch.delenv("WEFT_DISABLED", raising=False)
    monkeypatch.setenv("WEFT_REDACT_PII", "1")
    an = FakeAnalyzer()
    c = weft.init("pii", server_url=fake.url, autopatch=False, settings={"redact_pii_analyzer": an})
    try:
        @weft.op
        def greet(person, api_key):
            return f"Hello Alice Smith from Acme Corp, key {api_key}"

        greet("Alice Smith in Berlin", api_key="sk-123")
        c.flush()
        [call] = fake.calls_by_op("greet")
        assert call["inputs"] == {"person": "<PERSON> in <LOCATION>", "api_key": "REDACTED"}
        assert call["output"] == "Hello <PERSON> from <ORGANIZATION>, key sk-123"
        assert an.calls and an.calls[0][2] == "en"
    finally:
        weft.finish()
