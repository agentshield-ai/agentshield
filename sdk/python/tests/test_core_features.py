"""Op kind/color, settings, tracing_disabled, WAL, Content/media, object management,
EvaluationLogger.log_example + dataset linking, and the new built-in scorers."""

import asyncio
import json
import os

import pytest
from pydantic import BaseModel

import weft
from weft import scorers


# ---------------------------------------------------------------- op options and settings
def test_kind_and_color_attributes(client, fake):
    @weft.trace(kind="agent", color="purple")
    def agent():
        return tool()

    @weft.trace(kind="tool")
    def tool():
        return 1

    agent()
    client.flush()
    [a] = fake.calls_by_op("agent")
    [t] = fake.calls_by_op("tool")
    assert a["attributes"]["weft"]["span_kind"] == "agent" and a["attributes"]["weft"]["color"] == "purple"
    assert "kind" not in a["attributes"]
    assert t["attributes"]["weft"]["span_kind"] == "tool" and "color" not in t["attributes"]["weft"]
    # built-in scorer ops are tagged as scorers
    scorers.ExactMatchScorer().score(output="a", expected="a")
    client.flush()
    assert fake.calls_by_op("ExactMatchScorer.score")[0]["attributes"]["weft"]["span_kind"] == "scorer"


def test_global_attributes_print_link_and_capture_code(fake, capsys):
    c = weft.init("ent/proj", server_url=fake.url, autopatch=False,
                  settings={"global_attributes": {"env": "staging", "team": "a"}, "capture_code": False})
    try:
        @weft.trace
        def root():
            return child()

        @weft.trace
        def child():
            return 1

        with weft.attributes({"team": "b"}):
            _, call = root.call()
        c.flush()
        err = capsys.readouterr().err
        assert f"weft: {fake.url}/ent/proj/spans/{call.id}" in err
        assert err.count("weft: http") == 1  # only root calls print a link
        assert call.ui_url == f"{fake.url}/ent/proj/spans/{call.id}"
        [r] = fake.calls_by_op("root")
        assert r["attributes"]["env"] == "staging" and r["attributes"]["team"] == "b"
        assert fake.objs["root"][0]["value"]["code"] == "# Code capture disabled\n"
    finally:
        weft.finish()
    c2 = weft.init("p", server_url=fake.url, autopatch=False, settings={"print_span_link": False})
    try:
        weft.trace(lambda: 1, name="quiet")()
        assert "weft: http" not in capsys.readouterr().err
    finally:
        weft.finish()


def test_per_op_code_capture_toggle(client, fake):
    @weft.trace(enable_code_capture=False)
    def secret_sauce():
        return 42

    secret_sauce()
    client.flush()
    assert fake.objs["secret_sauce"][0]["value"]["code"] == "# Code capture disabled\n"


def test_tracing_disabled(client, fake):
    @weft.trace
    def f():
        return 1

    with weft.tracing_disabled():
        assert f() == 1
    f()
    client.flush()
    assert len(fake.calls_by_op("f")) == 1


def test_retry_and_queue_settings(fake):
    c = weft.init("p", server_url=fake.url, autopatch=False,
                  settings={"retry_max_attempts": 5, "retry_backoff": 0.001, "retry_max_backoff": 0.002, "max_queue_size": 7})
    try:
        assert c._sender.max_retries == 5 and c._sender.max_queue == 7 and c._sender.max_backoff == 0.002
        fake.fail_paths["/api/v1/spans/batch"] = 4

        @weft.trace
        def f():
            return 1

        f()
        c.flush(5)
        assert len(fake.calls_by_op("f")) == 1
    finally:
        weft.finish()


def test_write_ahead_log_replays_on_next_start(fake, tmp_path):
    settings = {"enable_disk_fallback": True, "wal_dir": str(tmp_path), "retry_max_attempts": 1, "retry_backoff": 0.001}
    fake.fail_status = 503
    c = weft.init("wal-proj", server_url=fake.url, autopatch=False, settings=settings)

    @weft.trace
    def important(x):
        return x

    important(1)
    important(2)
    c.flush(5)
    assert c._sender.wal is not None and c._sender.wal.count() >= 2
    weft.finish()
    assert fake.calls_by_op("important") == []
    wal_files = list(tmp_path.iterdir())
    assert len(wal_files) == 1 and wal_files[0].suffix == ".jsonl"

    fake.fail_status = None
    c2 = weft.init("wal-proj", server_url=fake.url, autopatch=False, settings=settings)
    try:
        c2.flush(5)
        calls = fake.calls_by_op("important")
        assert sorted(x["inputs"]["x"] for x in calls) == [1, 2]
        assert all(x.get("ended_at") for x in calls)
        assert "important" in fake.objs  # the op object was replayed too
        assert c2._sender.wal.count() == 0
    finally:
        weft.finish()


def test_wal_env_and_queue_overflow_spills(fake, tmp_path, monkeypatch):
    monkeypatch.setenv("WEFT_ENABLE_WAL", "1")
    monkeypatch.setenv("WEFT_WAL_DIR", str(tmp_path))
    c = weft.init("p", server_url="http://127.0.0.1:9", autopatch=False, settings={"max_queue_size": 2, "retry_max_attempts": 0})
    try:
        for i in range(10):
            weft.trace(lambda i: i, name="burst")(i)
        assert c._sender.dropped == 0 and c._sender.wal_written > 0
    finally:
        c._sender.max_retries = 0
        weft.finish()


# ---------------------------------------------------------------- Content and media
def test_content_and_media_types(client, fake, tmp_path):
    p = tmp_path / "notes.txt"
    p.write_text("hello file")
    pdf = b"%PDF-1.4 fake"
    mp4 = b"\x00\x00\x00\x18ftypmp42" + b"\x00" * 8

    @weft.trace
    def make():
        return {
            "path": weft.Content.from_path(p),
            "bytes": weft.Content.from_bytes(b"\x00\x01", extension="bin"),
            "text": weft.Content.from_text("plain", filename="a.txt"),
            "video": weft.Video(mp4),
            "pdf": weft.PDF(pdf, filename="doc.pdf"),
            "html": weft.Html("<b>hi</b>"),
            "md": weft.Markdown("# Title"),
            "sniffed_pdf": pdf,
        }

    make()
    client.flush()
    out = fake.calls_by_op("make")[0]["output"]
    assert out["path"] == {"_type": "Content", "mimetype": "text/plain", "filename": "notes.txt", "file_digest": out["path"]["file_digest"]}
    assert fake.files[out["path"]["file_digest"]] == b"hello file"
    assert out["bytes"]["mimetype"] == "application/octet-stream" and out["bytes"]["filename"] == "file.bin"
    assert out["text"]["mimetype"] == "text/plain"
    assert out["video"] == {"_type": "Video", "file_digest": out["video"]["file_digest"], "mime_type": "video/mp4", "filename": None}
    assert out["pdf"]["_type"] == "PDF" and out["pdf"]["mime_type"] == "application/pdf"
    assert out["html"]["_type"] == "Html" and fake.files[out["html"]["file_digest"]] == b"<b>hi</b>"
    assert out["md"]["_type"] == "Markdown" and out["md"]["mime_type"] == "text/markdown"
    assert out["sniffed_pdf"]["_type"] == "PDF"
    assert client.read_file(out["md"]["file_digest"]) == b"# Title"
    c = weft.Content.from_text("x")
    assert c.size == 1 and c.as_text() == "x" and c.digest == weft._canonical.file_digest(b"x")


# ---------------------------------------------------------------- object management
def test_object_management(client, fake):
    ref0 = weft.save({"v": 0}, name="cfg")
    ref1 = weft.save({"v": 1}, name="cfg")
    client.set_aliases(ref1, "production")
    client.set_alias("cfg:v0", ["staging", "dev"])
    assert fake.aliases[("cfg", "production")] == ref1.digest
    assert fake.aliases[("cfg", "staging")] == ref0.digest
    put = [r for r in fake.requests if r["path"] == "/api/v1/objects/cfg/aliases"][0]
    assert put["body"] == {"project_id": "test-entity/test-proj", "version": ref1.digest, "aliases": ["production"]}
    assert client.get_aliases(ref0) == ["dev", "staging"]
    client.remove_aliases("cfg", "dev")
    assert ("cfg", "dev") not in fake.aliases

    client.add_tags(ref1, ["reviewed", "golden"])
    client.add_tags("cfg:latest", ["golden"])
    assert client.get_tags(ref1) == ["reviewed", "golden"]
    client.remove_tags(ref1.uri(), ["reviewed"])
    assert client.get_tags(ref1) == ["golden"]
    assert client.list_tags() == ["golden"]
    tag_put = [r for r in fake.requests if r["path"].endswith("/tags")][0]
    assert tag_put["path"] == f"/api/v1/objects/cfg/versions/{ref1.digest}/tags"
    assert tag_put["body"] == {"project_id": "test-entity/test-proj", "tags": ["reviewed", "golden"]}

    assert client.delete_object_version(ref0) == 1
    assert [v["digest"] for v in fake.objs["cfg"]] == [ref1.digest]
    weft.save({"v": 2}, name="cfg")
    assert client.delete_object_versions("cfg", [ref1.digest]) == 1
    assert client.delete_all_object_versions("cfg") == 1
    assert fake.objs["cfg"] == []


def test_module_level_tags_and_aliases(client, fake):
    """Module-level add_tags & co., on the global client."""
    ref0 = weft.save({"v": 0}, name="mod")
    ref1 = weft.save({"v": 1}, name="mod")
    weft.set_aliases(ref1, "production")
    weft.set_aliases(ref0, ["staging", "dev"])
    weft.remove_aliases(ref0, "dev")
    assert weft.get_aliases(ref0) == ["staging"] and weft.get_aliases(ref1) == ["production"]
    weft.add_tags(ref1, ["reviewed", "golden"])
    weft.remove_tags(ref1, ["reviewed"])
    assert weft.get_tags(ref1) == ["golden"]
    assert weft.get_tags_and_aliases(ref1) == (["golden"], ["production"])
    assert weft.list_tags() == ["golden"]
    assert weft.list_aliases() == ["production", "staging"]
    names = ["add_tags", "remove_tags", "get_tags", "set_aliases", "remove_aliases", "get_aliases", "get_tags_and_aliases", "list_tags", "list_aliases"]
    assert all(n in weft.__all__ and callable(getattr(weft, n)) for n in names)


def test_module_level_tags_need_init():
    weft.finish()
    with pytest.raises(RuntimeError, match="weft.init"):
        weft.list_aliases()


def test_alias_by_name_and_version(client, fake):
    ref = weft.save({"v": 0}, name="old")
    client.set_alias("old:v0", "prod")
    assert fake.aliases[("old", "prod")] == ref.digest
    assert weft.get("old:prod") == {"v": 0}
    client.remove_alias("old", "prod")
    assert ("old", "prod") not in fake.aliases


# ---------------------------------------------------------------- EvaluationLogger
def test_log_example_and_dataset_linking(client, fake):
    ds = weft.Dataset(name="qa", rows=[{"q": "a"}, {"q": "b"}])
    ref = weft.save(ds)
    ev = weft.EvaluationLogger(model="m", dataset=ds, name="linked")
    ev.log_example({"q": "a"}, "A", {"correct": True})
    ev.log_example({"q": "zzz"}, "Z", {"correct": False})  # not in the dataset
    summary = ev.log_summary()
    assert summary["correct"] == {"true_count": 1, "true_fraction": 0.5}
    client.flush()
    pas = fake.calls_by_op("Evaluation.predict_and_score")
    assert pas[0]["inputs"]["example"] == ref.with_extra("attr", "rows", "id", ds.rows.row_digests[0]).uri()
    assert pas[1]["inputs"]["example"] == {"q": "zzz"}
    assert pas[0]["output"]["scores"] == {"correct": True}
    # a dataset ref (URI) is loaded and linked the same way
    ev2 = weft.EvaluationLogger(model="m", dataset=ref.uri())
    ev2.log_example({"q": "b"}, "B", {"ok": 1})
    ev2.log_summary()
    client.flush()
    last = fake.calls_by_op("Evaluation.predict_and_score")[-1]
    assert last["inputs"]["example"].endswith("/id/" + ds.rows.row_digests[1])


# ---------------------------------------------------------------- scorers
def test_new_scorers_serialise(client, fake):
    s = scorers
    weft.save(s.RemoteScorer(name="remote", url="https://x/score", headers_secret="SCORER_HEADERS", timeout=5))
    weft.save(s.HallucinationFreeScorer(name="hall"))
    weft.save(s.SummarizationScorer(name="summ", model="gpt-4.1"))
    weft.save(s.RelevanceScorer(name="rel"))
    weft.save(s.ValidXMLScorer(name="xml"))
    weft.save(s.JSONSchemaScorer(name="js", schema={"type": "object"}))
    weft.save(s.OpenAIModerationScorer(name="mod"))
    weft.save(s.EmbeddingSimilarityScorer(name="emb", target_column="ref", model="text-embedding-3-small"))
    weft.save(s.RuleScorer(name="rule", rule={"$eq": [1, 1]}, threshold=0.5))
    base = {"_bases": ["Scorer", "Object", "BaseModel"], "description": None, "column_map": None}

    def val(n):
        return fake.objs[n][0]["value"]

    assert val("remote") == {**base, "_type": "RemoteScorer", "_class_name": "RemoteScorer", "name": "remote",
                             "url": "https://x/score", "headers_secret": "SCORER_HEADERS", "timeout": 5.0}
    assert val("hall") == {**base, "_type": "HallucinationFreeScorer", "_class_name": "HallucinationFreeScorer", "name": "hall",
                           "model": "gpt-4o-mini", "scoring_prompt": scorers.HALLUCINATION_PROMPT}
    assert val("summ")["model"] == "gpt-4.1" and set(val("rel")) == set(val("hall"))
    assert val("xml") == {**base, "_type": "ValidXMLScorer", "_class_name": "ValidXMLScorer", "name": "xml"}
    assert val("js")["schema"] == {"type": "object"} and "schema_" not in val("js")
    assert val("mod") == {**base, "_type": "OpenAIModerationScorer", "_class_name": "OpenAIModerationScorer", "name": "mod"}
    assert val("emb")["target_column"] == "ref" and val("emb")["model"] == "text-embedding-3-small"
    assert val("rule")["rule"] == {"$eq": [1, 1]} and val("rule")["threshold"] == 0.5
    back = weft.get("js")
    assert isinstance(back, s.JSONSchemaScorer) and back.schema_ == {"type": "object"}


def test_new_scorers_run_locally(client, fake, monkeypatch):
    s = scorers
    hall = s.HallucinationFreeScorer(model="mock/json").score(output="Paris", question="capital of France?")
    assert hall["correct"] is True and "capital of France?" in hall["echo"]  # fake judge echoes the prompt
    assert s.RelevanceScorer(model="mock/json").score(output="x", q="y")["correct"] is True
    assert s.ValidXMLScorer().score(output="<a><b>1</b></a>") is True
    assert s.ValidXMLScorer().score(output="<a>") is False
    schema = {"type": "object", "properties": {"n": {"type": "integer", "minimum": 0}}, "required": ["n"], "additionalProperties": False}
    js = s.JSONSchemaScorer(schema=schema)
    assert js.score(output='{"n": 3}') is True
    assert js.score(output={"n": -1}) is False and js.score(output={"n": 1, "x": 2}) is False and js.score(output="nope") is False
    rule = s.RuleScorer(rule={"$contains": {"input": {"$getField": "output"}, "substr": {"$getField": "expected"}, "case_insensitive": True}})
    assert rule.score(output="The answer is PARIS", expected="paris") is True
    num = s.RuleScorer(rule={"$convert": {"input": {"$getField": "output"}, "to": "double"}}, threshold=0.5)
    assert num.score(output="0.7") is True and num.score(output="0.2") is False

    monkeypatch.setenv("SCORER_HEADERS", json.dumps({"X-Key": "k"}))
    remote = s.RemoteScorer(url=fake.url + "/remote/score", headers_secret="SCORER_HEADERS")
    res = remote.score(output="hi", question="q")
    assert res["score"] == 0.9 and res["received"] == {"output": "hi", "inputs": {"question": "q"}}
    req = [r for r in fake.requests if r["path"] == "/remote/score"][0]
    assert req["headers"]["X-Key"] == "k"
    monkeypatch.setenv("SCORER_HEADERS", "tok123")
    remote.score(output="hi")
    assert [r for r in fake.requests if r["path"] == "/remote/score"][-1]["headers"]["Authorization"] == "Bearer tok123"

    emb = s.EmbeddingSimilarityScorer(target_column="ref")
    with pytest.raises(RuntimeError, match="no embeddings route"):
        emb.score(output="abc", ref="abcd")
    fake.embeddings_enabled = True
    sim = emb.score(output="abc", ref="abc")
    assert sim == pytest.approx(1.0)

    import sys

    monkeypatch.setitem(sys.modules, "openai", None)
    with pytest.raises(RuntimeError, match="openai package"):
        s.OpenAIModerationScorer().score(output="x")

    class Answer(BaseModel):
        city: str

    ps = s.PydanticScorer(model=Answer)
    assert ps.score(output='{"city": "Paris"}') is True and ps.score(output={"town": 1}) is False


def test_new_scorers_in_evaluation(client, fake):
    ev = weft.Evaluation(dataset=[{"q": "a", "expected": "<x/>"}], scorers=[
        scorers.ValidXMLScorer(name="xml"),
        scorers.RuleScorer(name="has_x", rule={"$contains": {"input": {"$getField": "output"}, "substr": {"$literal": "x"}}}),
    ])
    summary = asyncio.run(ev.evaluate(weft.trace(lambda q: "<x/>", name="m")))
    assert summary["xml"]["true_fraction"] == 1.0 and summary["has_x"]["true_fraction"] == 1.0
