import dataclasses
import datetime
import enum
import json

import pytest
from pydantic import BaseModel

import weft
from weft._canonical import canonical_json, digest
from weft._serialize import to_json


def test_digests_match_the_server_scheme():
    """Weft's digest (DESIGN.md §10.3): hex SHA-256 of canonical JSON."""
    import hashlib
    import json

    from weft._canonical import file_digest, table_digest

    vals = [
        {"b": 1, "a": [1.5, 2, True, None]},
        {"x": "<a&b> é \u2028 😀 \"q\" \\ \n\t \x01 \x7f"},
        {"f": 1.0, "g": 1e-07, "h": 1e16, "k": 10**20},
        {"é": 1, "z": 2, "A": {"nested": ["ü"]}, "\U0001f600": 0, "\uffff": 1},
    ]
    for v in vals:
        # exactly Python's json.dumps(v, sort_keys=True, separators=(",", ":"), ensure_ascii=False)
        assert canonical_json(v) == json.dumps(v, sort_keys=True, separators=(",", ":"), ensure_ascii=False)
        assert digest(v) == hashlib.sha256(canonical_json(v).encode("utf-8")).hexdigest()
    assert canonical_json({"f": 1.0}) == '{"f":1.0}'
    assert canonical_json("\x1f\x7f\b") == '"\\u001f\x7f\\b"'  # only control characters are escaped
    assert canonical_json("\ud83d\ude00 \ud800") == '"😀 \ufffd"'  # surrogates as the server decodes them
    assert file_digest(b"abc") == hashlib.sha256(b"abc").hexdigest()
    assert digest({"a": 1}) == hashlib.sha256(b'{"a":1}').hexdigest()
    assert digest({"a": 1}) != digest({"a": 2})
    assert table_digest(["d1", "d2"]) == digest(["d1", "d2"]) == hashlib.sha256(b'["d1","d2"]').hexdigest()


def test_to_json_handles_common_types(client):
    class Color(enum.Enum):
        RED = "red"

    @dataclasses.dataclass
    class Point:
        x: int
        y: int

    class PM(BaseModel):
        a: int
        p: Point

    class Weird:
        def __repr__(self):
            return "<Weird>"

    val = {
        "dt": datetime.datetime(2026, 1, 2, 3, 4, 5, tzinfo=datetime.timezone.utc),
        "enum": Color.RED,
        "dc": Point(1, 2),
        "pm": PM(a=1, p=Point(3, 4)),
        "set": {3, 1, 2},
        "tuple": (1, "a"),
        "nan": float("nan"),
        "weird": Weird(),
        1: "int key",
    }
    out = to_json(val, client)
    json.dumps(out)  # JSON safe
    assert out["dt"] == "2026-01-02T03:04:05Z"
    assert out["enum"] == "red"
    assert out["dc"] == {"x": 1, "y": 2}
    assert out["pm"] == {"a": 1, "p": {"x": 3, "y": 4}}
    assert out["set"] == [1, 2, 3]
    assert out["tuple"] == [1, "a"]
    assert out["nan"] == "nan"
    assert out["weird"] == "<Weird>"
    assert out["1"] == "int key"


def test_circular_reference_safe(client):
    a = {"x": 1}
    a["self"] = a
    out = to_json(a, client)
    assert out["self"] == "<circular reference>"


def test_op_output_pydantic_and_dataclass(client, fake):
    @dataclasses.dataclass
    class Answer:
        text: str
        score: float

    class Resp(BaseModel):
        answer: Answer

    @weft.trace
    def f():
        return Resp(answer=Answer("hi", 0.5))

    f()
    client.flush()
    assert fake.calls_by_op("f")[0]["output"] == {"answer": {"text": "hi", "score": 0.5}}


def test_bytes_and_images_uploaded(client, fake):
    png = b"\x89PNG\r\n\x1a\n" + b"\x00" * 20

    @weft.trace
    def f(data):
        return {"img": data}

    f(png)
    client.flush()
    [c] = fake.calls_by_op("f")
    media = c["inputs"]["data"]
    assert media["_type"] == "Image" and media["mime_type"] == "image/png"
    assert fake.files[media["file_digest"]] == png  # server digest == local digest
    assert c["output"]["img"] == media
    # uploaded once
    assert len([r for r in fake.requests if r["path"] == "/api/v1/files/upload"]) == 1

    PIL = pytest.importorskip("PIL.Image")
    img = PIL.new("RGB", (4, 4), "red")
    out = to_json(img, client)
    client.flush()
    assert out["_type"] == "Image" and out["file_digest"] in fake.files


def test_publish_object_and_get_back(client, fake):
    class Cfg(weft.Object):
        temperature: float = 0.2
        tags: list = []

    cfg = Cfg(name="cfg", tags=["a"])
    ref = weft.save(cfg)
    assert isinstance(ref, weft.ObjectRef)
    assert ref.uri() == f"weft:///test-entity/test-proj/object/cfg:{ref.digest}"
    assert cfg.ref == ref
    v = fake.objs["cfg"][0]["value"]
    assert v == {"_type": "Cfg", "_class_name": "Cfg", "_bases": ["Object", "BaseModel"], "name": "cfg",
                 "description": None, "temperature": 0.2, "tags": ["a"]}
    # content addressed
    ref2 = weft.save(Cfg(name="cfg", tags=["a"]))
    assert ref2.digest == ref.digest
    ref3 = weft.save(Cfg(name="cfg", tags=["b"]))
    assert ref3.digest != ref.digest
    assert len(fake.objs["cfg"]) == 2

    got = weft.get(ref)
    assert isinstance(got, Cfg) and got.tags == ["a"] and got.ref.digest == ref.digest
    assert weft.get("cfg:v1").tags == ["b"]
    assert weft.get("cfg").tags == ["b"]
    assert weft.get("cfg:latest").tags == ["b"]
    assert weft.get(weft.ref(ref.uri())).tags == ["a"]


def test_publish_plain_values_and_unknown_class(client, fake):
    ref = weft.save({"k": [1, 2]}, name="cfg-dict")
    assert weft.get(ref) == {"k": [1, 2]}
    fake.objs["mystery"] = [{"digest": "d1", "value": {"_class_name": "Unknown", "_bases": ["Object"], "x": 1},
                             "version": 0, "name": "mystery", "kind": "object", "class": None}]
    obj = weft.get("mystery")
    assert isinstance(obj, weft.WeftObject)
    assert obj.x == 1 and obj["_class_name"] == "Unknown"


def test_dataset_rows_uploaded_as_table(client, fake):
    rows = [{"q": "1+1", "expected": "2"}, {"q": "2+2", "expected": "4"}]
    ds = weft.Dataset(name="math", rows=rows)
    assert list(ds.rows) == rows and len(ds) == 2
    ref = weft.save(ds)
    val = fake.objs["math"][0]["value"]
    assert val["_type"] == "Dataset" and val["_class_name"] == "Dataset"
    assert val["_bases"] == ["Object", "BaseModel"]
    assert val["rows"].startswith("weft:///test-entity/test-proj/table/")
    tdigest = val["rows"].rsplit("/", 1)[1]
    assert [r["value"] for r in fake.tables[tdigest]] == rows
    assert ds.rows.row_digests == [r["digest"] for r in fake.tables[tdigest]]

    loaded = weft.get("math")
    assert isinstance(loaded, weft.Dataset)
    assert list(loaded.rows) == rows
    assert loaded.rows.row_digests == ds.rows.row_digests
    # row refs resolve via refs/read_batch
    row_ref = ref.with_extra("attr", "rows", "id", ds.rows.row_digests[1])
    assert client.get(row_ref) == rows[1]

    ds2 = loaded.add_rows([{"q": "3+3", "expected": "6"}])
    assert len(ds2) == 3 and len(fake.objs["math"]) == 2


def test_dataset_large_table_chunked(client, fake):
    rows = [{"i": i} for i in range(2500)]
    weft.save(weft.Dataset(name="big", rows=rows))
    creates = [r for r in fake.requests if r["path"] == "/api/v1/tables/create"]
    updates = [r for r in fake.requests if r["path"] == "/api/v1/tables/update"]
    assert len(creates) == 1 and len(updates) == 2
    loaded = weft.get("big")
    assert len(loaded.rows) == 2500 and loaded.rows[2499] == {"i": 2499}


def test_dataset_from_spans(client, fake):
    @weft.trace
    def f(x):
        return x * 2

    f(1)
    f(2)
    calls = list(client.spans(filter={"names": ["f"]}, sort_by=[{"field": "started_at", "direction": "asc"}]))
    ds = weft.Dataset.from_spans(calls)
    assert list(ds.rows) == [{"inputs": {"x": 1}, "output": 2}, {"inputs": {"x": 2}, "output": 4}]


def test_prompts(client, fake):
    p = weft.StringPrompt("Hello {name}, {missing}")
    assert p.format(name="Ada") == "Hello Ada, {missing}"
    mp = weft.MessagesPrompt([{"role": "system", "content": "You are {role}"}, {"role": "user", "content": "{q}"}])
    assert mp.format(role="kind", q="hi") == [{"role": "system", "content": "You are kind"}, {"role": "user", "content": "hi"}]
    ref = weft.save(p, name="greeting")
    v = fake.objs["greeting"][0]["value"]
    assert v["_class_name"] == "StringPrompt" and v["_bases"] == ["Prompt", "Object", "BaseModel"]
    assert v["content"] == "Hello {name}, {missing}"
    back = weft.get(ref)
    assert isinstance(back, weft.StringPrompt) and back.format(name="x") == "Hello x, {missing}"
    weft.save(mp, name="chat")
    assert fake.objs["chat"][0]["value"]["messages"][0]["content"] == "You are {role}"


def test_model_publish_includes_predict_op_and_nested_refs(client, fake):
    class Inner(weft.Object):
        k: int = 1

    class MyModel(weft.Model):
        prompt: str
        inner: Inner

        @weft.trace
        def predict(self, question: str) -> str:
            return self.prompt + question

    m = MyModel(name="m", prompt="P:", inner=Inner(name="inner"))
    weft.save(m)
    v = fake.objs["m"][0]["value"]
    assert v["_bases"] == ["Model", "Object", "BaseModel"]
    assert v["predict"].startswith("weft:///test-entity/test-proj/function/MyModel.predict:")
    assert v["inner"].startswith("weft:///test-entity/test-proj/object/inner:")
    client.flush()
    assert "MyModel.predict" in fake.objs
    back = weft.get("m")
    assert isinstance(back, MyModel) and back.inner.k == 1 and back.predict("x") == "P:x"


def test_builtin_scorer_serialisation(client, fake):
    s = weft.scorers
    weft.save(s.ExactMatchScorer(name="exact"))
    weft.save(s.ContainsScorer(name="contains", case_insensitive=True))
    weft.save(s.RegexScorer(name="digits", pattern=r"^\d+$"))
    weft.save(s.JSONValidScorer(name="json"))
    weft.save(s.LevenshteinScorer(name="lev"))
    weft.save(s.LLMAsAJudgeScorer(name="judge", model="gpt-4o-mini", scoring_prompt="Is {output} right?"))
    m = weft.LLMStructuredCompletionModel(name="llm", llm_model_id="gpt-4o-mini",
                                          default_params={"messages_template": [{"role": "user", "content": "{q}"}], "temperature": 0})
    weft.save(m)
    exact = fake.objs["exact"][0]["value"]
    assert exact["_class_name"] == "ExactMatchScorer"
    assert exact["_bases"] == ["Scorer", "Object", "BaseModel"]
    assert exact["target_column"] == "expected"
    assert "score" not in exact  # builtin scorers carry no op refs
    assert fake.objs["contains"][0]["value"]["case_insensitive"] is True
    assert fake.objs["digits"][0]["value"]["pattern"] == r"^\d+$"
    j = fake.objs["judge"][0]["value"]
    assert j["_bases"] == ["Scorer", "Object", "BaseModel"] and j["response_format"] == "json_object"
    lv = fake.objs["llm"][0]["value"]
    assert lv["_bases"] == ["Model", "Object", "BaseModel"] and lv["llm_model_id"] == "gpt-4o-mini"
    assert "predict" not in lv


def test_builtin_scorers_run_locally(client, fake):
    s = weft.scorers
    assert s.ExactMatchScorer().score(output=" 4 ", expected="4") is True
    assert s.ContainsScorer().score(output="The answer is FOUR", expected="four") is True
    assert s.RegexScorer(pattern=r"^\d+$").score(output="123") is True
    assert s.JSONValidScorer().score(output='{"a":1}') is True
    assert s.JSONValidScorer().score(output="nope") is False
    assert s.LevenshteinScorer().score(output="kitten", expected="sitting") == pytest.approx(1 - 3 / 7)
    judged = s.LLMAsAJudgeScorer(model="mock/json", scoring_prompt="Q {question} A {output}").score(output="4", question="2+2")
    assert judged["correct"] is True and judged["echo"] == "Q 2+2 A 4"
    client.flush()
    [jc] = fake.calls_by_op("LLMAsAJudgeScorer.score")
    assert jc["summary"]["usage"]["mock/json"]["total_tokens"] == 10


def test_llm_structured_completion_model_predict(client, fake):
    m = weft.LLMStructuredCompletionModel(
        llm_model_id="mock/echo",
        default_params={"messages_template": [{"role": "system", "content": "be brief"}, {"role": "user", "content": "Q: {q}"}]},
    )
    assert m.predict(q="hi") == "Q: hi"
    client.flush()
    [c] = fake.calls_by_op("LLMStructuredCompletionModel.predict")
    assert c["inputs"]["q"] == "hi"
    assert c["summary"]["usage"]["mock/echo"]["requests"] == 1
    req = [r for r in fake.requests if r["path"] == "/api/v1/completions"][0]["body"]
    assert req["track"] is False
    assert req["inputs"]["messages"][1]["content"] == "Q: hi"
