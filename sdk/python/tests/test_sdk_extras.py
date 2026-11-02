"""Server-response cache, SavedView query API, EvaluationLogger linking and Dataset helpers (fake server)."""

import sys
import types

import pytest

import weft
from weft._cache import ServerCache, digest_is_cacheable
from weft._saved_view import Filter, filter_to_clause, query_to_filters


def _count(fake, path):
    return sum(1 for r in fake.requests if r["path"] == path)


# ---------------------------------------------------------------- server cache


def test_cache_serves_immutable_reads(client, fake):
    ds = weft.Dataset(name="qa", rows=[{"q": "a"}, {"q": "b"}])
    ref = weft.publish(ds)
    data = weft.Content(b"hello file", mimetype="text/plain")
    fd = client._upload_file(data.data, data.mimetype)
    client.flush()

    first = client.get(ref)
    assert [r["q"] for r in first.rows] == ["a", "b"]
    n_obj, n_tab = _count(fake, "/obj/read"), _count(fake, "/table/query")
    again = client.get(ref)
    assert [r["q"] for r in again.rows] == ["a", "b"]
    assert _count(fake, "/obj/read") == n_obj and _count(fake, "/table/query") == n_tab
    assert client.read_file(fd) == b"hello file"
    n_file = _count(fake, "/file/content")
    assert client.read_file(fd) == b"hello file" and _count(fake, "/file/content") == n_file
    stats = client.cache_stats()
    assert stats["hits"] >= 3 and stats["misses"] >= 3

    # refs/read_batch is cached per ref
    row_ref = ref.with_extra("attr", "name").uri()
    call_ref = f"weft:///{client.project_id}/call/some-call"
    assert client._http.post("/refs/read_batch", {"refs": [row_ref, call_ref]})["vals"] == ["qa", None]
    n_refs = _count(fake, "/refs/read_batch")
    assert client._http.post("/refs/read_batch", {"refs": [row_ref]})["vals"] == ["qa"]
    assert _count(fake, "/refs/read_batch") == n_refs
    client._http.post("/refs/read_batch", {"refs": [row_ref, call_ref]})  # call refs are always fetched
    assert _count(fake, "/refs/read_batch") == n_refs + 1
    assert [r["body"]["refs"] for r in fake.requests if r["path"] == "/refs/read_batch"][-1] == [call_ref]

    # "latest" and aliases are never cached
    n_obj = _count(fake, "/obj/read")
    client.get("qa:latest")
    client.get("qa:latest")
    assert _count(fake, "/obj/read") == n_obj + 2


def test_cache_persists_on_disk_across_clients(fake, tmp_path, monkeypatch):
    monkeypatch.setenv("WEFT_SERVER_CACHE_DIR", str(tmp_path / "c"))
    c1 = weft.init("e/p", server_url=fake.url, autopatch=False, settings={"print_call_link": False})
    ref = weft.publish(weft.Dataset(name="d", rows=[{"x": 1}]))
    c1.get(ref)
    weft.finish()
    before = _count(fake, "/obj/read")
    c2 = weft.init("e/p", server_url=fake.url, autopatch=False, settings={"print_call_link": False})
    assert [r["x"] for r in c2.get(ref).rows] == [1]
    assert _count(fake, "/obj/read") == before
    assert (tmp_path / "c" / "server_cache.sqlite").exists()
    c2.clear_server_cache()
    c2.get(ref)
    assert _count(fake, "/obj/read") == before + 1
    weft.finish()


def test_cache_invalidated_on_delete_and_can_be_disabled(fake, monkeypatch):
    c = weft.init("e/p", server_url=fake.url, autopatch=False, settings={"print_call_link": False})
    ref = weft.publish(weft.Dataset(name="gone", rows=[{"x": 1}]))
    c.get(ref)
    assert c.delete_object_version(ref) == 1
    with pytest.raises(Exception):
        c.get(ref)
    weft.finish()

    c = weft.init("e/p", server_url=fake.url, autopatch=False, settings={"use_server_cache": False, "print_call_link": False})
    ref = weft.publish(weft.Dataset(name="nocache", rows=[{"x": 1}]))
    before = _count(fake, "/obj/read")
    c.get(ref)
    c.get(ref)
    assert _count(fake, "/obj/read") == before + 2
    assert c.cache_stats()["hits"] == 0
    weft.finish()

    monkeypatch.setenv("WEFT_USE_SERVER_CACHE", "false")
    c = weft.init("e/p", server_url=fake.url, autopatch=False, settings={"print_call_link": False})
    assert getattr(c._http, "enabled") is False
    weft.finish()


def test_server_cache_lru_and_size_limit(tmp_path):
    cache = ServerCache(str(tmp_path), size_limit=1000, memory_size=2)
    for i in range(5):
        cache.set(f"k{i}", b"x" * 300)
    assert cache.disk_size <= 1000
    assert cache.get("k0") is None and cache.get("k4") == b"x" * 300
    cache.set("big", b"y" * 5000)  # larger than the limit: not stored
    assert cache.get("big") is None
    assert cache.delete_prefix("k") >= 1 and cache.get("k4") is None
    cache.close()
    assert digest_is_cacheable("a" * 43) and not digest_is_cacheable("latest") and not digest_is_cacheable("v3")
    assert not digest_is_cacheable("production")


# ---------------------------------------------------------------- saved views


def test_saved_view_fluent_api_save_load_and_apply(client, fake):
    @weft.op
    def answer(q: str) -> str:
        return q.upper()

    for q in ("a", "bb", "ccc"):
        answer(q)
    view = (
        weft.SavedView(label="Long answers")
        .filter_op("answer")
        .add_filter("output", "contains", "B")
        .add_filter("summary.weave.latency_ms", ">=", 0)
        .add_filter("Status", "is not empty")
        .sort_by("started_at", "asc")
        .add_column("inputs.q", "Question")
        .pin_column_left("inputs.q")
        .page_size(25)
    )
    with pytest.raises(ValueError):
        view.add_filter("nonsense.field", "equals", 1)
    with pytest.raises(ValueError):
        view.add_filter("output", "~=", 1)
    with pytest.raises(ValueError):
        view.add_filter("output", "is empty", "x")
    body = view.to_calls_query(limit=5)
    assert body["project_id"] == "test-entity/test-proj"
    assert body["filter"] == {"op_names": ["weft:///test-entity/test-proj/op/answer:*"]}
    assert body["sort_by"] == [{"field": "started_at", "direction": "asc"}] and body["limit"] == 5
    clauses = body["query"]["$expr"]["$and"]
    assert clauses[0] == {"$contains": {"input": {"$getField": "output"}, "substr": {"$literal": "B"}}}
    assert clauses[1] == {"$gte": [{"$getField": "summary.weave.latency_ms"}, {"$literal": 0.0}]}
    assert view.get_table_columns() == [{"path": "inputs.q", "label": "Question"}]
    assert [f.operator for f in view.filters] == ["(string): contains", "(number): >=", "(any): isNotEmpty"]

    view.save()
    assert view.name == "Long-answers" and view.ref is not None
    stored = fake.objs["Long-answers"][-1]["val"]
    assert stored["_class_name"] == "SavedView" and stored["label"] == "Long answers" and stored["definition"]["cols"] == ["inputs.q"]

    list(view.get_calls())  # the fake server does not evaluate the query; check what was sent
    q = [r for r in fake.requests if r["path"] == "/calls/stream_query"][-1]["body"]
    assert q["filter"]["op_names"] == body["filter"]["op_names"] and q["query"] == body["query"]

    loaded = weft.SavedView.load("Long-answers")
    assert isinstance(loaded, weft.SavedView) and loaded.label == "Long answers" and loaded.ref.name == "Long-answers"
    assert loaded.definition["query"] == view.definition["query"]
    listed = client.get_saved_views()
    assert [v.name for v in listed] == ["Long-answers"] and client.get_saved_views(view_type="evaluations") == []
    assert loaded.ui_url().endswith("/test-entity/test-proj/traces?view=Long-answers")

    loaded.remove_filter("output").remove_filter(0).hide_column("inputs.q").remove_columns()
    assert [f.field for f in loaded.filters] == ["summary.weave.status"]
    loaded.remove_filters().filter_op(None)
    assert "query" not in loaded.definition and "filter" not in loaded.definition


def test_saved_view_ui_params_dropped_and_evaluations_view(client, fake):
    v = weft.SavedView(view_type="evaluations", label="evals", definition={"ui_params": {"op": "x"}, "search": "foo"})
    assert v.to_calls_query()["filter"] == {"op_names": ["Evaluation.evaluate"]}
    v.add_sort("started_at", "desc")
    assert "ui_params" not in v.definition and v.definition["search"] == "foo"
    with pytest.raises(ValueError):
        v.add_sort("x", "up")
    v.set_filter(trace_roots_only=True, wb_user_ids=["u"])
    assert v.to_calls_query()["filter"]["trace_roots_only"] is True
    with pytest.raises(ValueError):
        v.set_filter(bogus=1)


@pytest.mark.parametrize(
    "f",
    [
        Filter(field="output", operator="(string): notContains", value="x"),
        Filter(field="output", operator="(string): notEquals", value="x"),
        Filter(field="inputs.n", operator="(number): =", value=2.0),
        Filter(field="inputs.n", operator="(number): !=", value=2.0),
        Filter(field="inputs.n", operator="(number): <", value=2.0),
        Filter(field="inputs.n", operator="(number): <=", value=2.0),
        Filter(field="inputs.n", operator="(number): >", value=2.0),
        Filter(field="inputs.flag", operator="(bool): is", value=True),
        Filter(field="started_at", operator="(date): after", value=1700000000.0),
        Filter(field="started_at", operator="(date): before", value=1700000000.0),
        Filter(field="op_name", operator="(string): in", value=["a", "b"]),
        Filter(field="exception", operator="(any): isEmpty"),
    ],
)
def test_filter_roundtrip(f):
    assert query_to_filters({"$expr": {"$and": [filter_to_clause(f)]}}) == [f]


def test_date_filters_accept_iso_strings():
    c = filter_to_clause(Filter(field="started_at", operator="(date): after", value="2024-01-01T00:00:00Z"))
    assert c == {"$gt": [{"$getField": "started_at"}, {"$literal": 1704067200.0}]}
    with pytest.raises(ValueError):
        filter_to_clause(Filter(field="started_at", operator="(date): after", value="not a date"))


# ---------------------------------------------------------------- evaluation logger links


def test_log_prediction_links_existing_calls_and_turns(client, fake):
    @weft.op
    def agent(q: str) -> str:
        return "answer:" + q

    _, c1 = agent.call("one")
    with weft.start_conversation(agent_name="bot") as conv:
        with conv.start_turn(user_message="two") as turn:
            turn.output_messages = [weft.Message.assistant("answer:two")]
    batch = weft.log_turn(conversation_id="c-3", agent_name="bot", messages=[weft.Message.user("three")])

    ev = weft.EvaluationLogger(model="bot", dataset="agent-evals", name="agent eval")
    p1 = ev.log_prediction({"q": "one"}, call=c1)
    p1.log_score("correct", True)
    p1.finish()
    p2 = ev.log_prediction({"q": "two"}, output="answer:two", call=turn)
    p2.finish()
    p3 = ev.log_prediction({"q": "three"}, call=[batch, conv])
    p3.finish()
    p4 = ev.log_prediction({"q": "four"}, call=c1.id)
    p4.finish()
    summary = ev.log_summary()
    client.flush()

    assert p1.output == "answer:one" and p1.model_latency is not None
    pas = fake.calls[p1.predict_and_score_call.id]
    w = pas["attributes"]["weft"]
    assert w["linked_calls"] == [{"call_id": c1.id, "trace_id": c1.trace_id}]
    assert w["genai_span_ref"] == [{"trace_id": c1.trace_id, "span_id": c1.id}]
    assert w["eval"]["kind"] == "agent"
    pred = fake.calls[p1.predict_call.id]
    assert pred["output"] == "answer:one" and pred["attributes"]["weft"]["linked_calls"][0]["call_id"] == c1.id
    assert fake.calls[p2.predict_and_score_call.id]["attributes"]["weft"]["linked_calls"][0]["call_id"] == turn.call_id
    w3 = fake.calls[p3.predict_and_score_call.id]["attributes"]["weft"]
    assert w3["linked_calls"][0]["call_id"] == batch.root_span_ids[0]
    assert w3["linked_conversation_ids"] == ["c-3", conv.conversation_id]
    assert p4.output == "answer:one"  # fetched from the server by id
    assert summary["correct"] == {"true_count": 1, "true_fraction": 1.0}
    with pytest.raises(TypeError):
        ev2 = weft.EvaluationLogger(model="m")
        ev2.log_prediction({"q": 1}, call=object())


def test_log_prediction_context_manager_nests_and_links_agent_spans(client, fake):
    @weft.op
    def retrieve(q: str) -> list:
        return [q]

    ev = weft.EvaluationLogger(model="agent-model", dataset=[{"q": "hi"}], name="ctx eval")
    with ev.log_prediction({"q": "hi"}, example_id="ex-1", trial_index=0) as pred:
        docs = retrieve("hi")
        with weft.start_conversation(agent_name="rag") as conv:
            with conv.start_turn(user_message="hi") as turn:
                pass
        pred.output = {"docs": docs}
        pred.log_score("has_docs", True)
    ev.log_summary()
    client.flush()
    pc = fake.calls[pred.predict_call.id]
    assert pc["output"] == {"docs": ["hi"]} and pc["ended_at"]
    [r] = [c for c in fake.calls.values() if fake.op_base(c) == "retrieve"]
    assert r["parent_id"] == pc["id"]
    t = fake.calls[turn.call_id]
    link = t["attributes"]["weave"]["eval"]
    assert link["predict_and_score_call_id"] == pred.predict_and_score_call.id
    assert link["run_id"] == ev.call.id and link["example_id"] == "ex-1" and link["trial_index"] == 0
    assert link["evaluation_name"] == "ctx eval" and link["kind"] == "agent"
    pas = fake.calls[pred.predict_and_score_call.id]
    assert pas["output"]["output"] == {"docs": ["hi"]} and pas["output"]["scores"] == {"has_docs": True}
    assert isinstance(pas["output"]["model_latency"], float)
    # after the block, new spans are not linked
    with weft.start_turn(user_message="later") as later:
        pass
    client.flush()
    assert "eval" not in (fake.calls[later.call_id]["attributes"].get("weave") or {})


# ---------------------------------------------------------------- dataset helpers


class _FakeHFDataset:
    def __init__(self, rows):
        self._rows = rows

    @classmethod
    def from_dict(cls, d):
        keys = list(d)
        n = len(d[keys[0]]) if keys else 0
        return cls([{k: d[k][i] for k in keys} for i in range(n)])

    def to_list(self):
        return list(self._rows)


class _FakeHFDatasetDict(dict):
    pass


@pytest.fixture
def fake_hf(monkeypatch):
    mod = types.ModuleType("datasets")
    mod.Dataset = _FakeHFDataset
    mod.DatasetDict = _FakeHFDatasetDict
    monkeypatch.setitem(sys.modules, "datasets", mod)
    return mod


def test_dataset_from_hf_to_hf_and_select(fake_hf):
    hf = _FakeHFDataset([{"q": "a", "a": 1}, {"q": "b", "a": 2}, {"q": "c", "a": 3}])
    ds = weft.Dataset.from_hf(hf, name="hf")
    assert ds.name == "hf" and [r["q"] for r in ds.rows] == ["a", "b", "c"]
    assert ds.columns_names == ["q", "a"] and ds.num_rows == 3
    back = ds.to_hf()
    assert isinstance(back, _FakeHFDataset) and back.to_list() == hf.to_list()
    assert weft.Dataset(rows=[]).to_hf().to_list() == []
    sel = ds.select([2, 0])
    assert [r["q"] for r in sel.rows] == ["c", "a"] and sel.name == "hf" and sel.ref is None
    with pytest.raises(ValueError):
        ds.select([])
    dd = _FakeHFDatasetDict(train=hf, test=_FakeHFDataset([{"q": "t"}]))
    with pytest.warns(UserWarning):
        assert len(weft.Dataset.from_hf(dd)) == 3
    assert [r["q"] for r in weft.Dataset.from_hf(dd, split="test").rows] == ["t"]
    with pytest.raises(ValueError):
        weft.Dataset.from_hf(_FakeHFDatasetDict(test=hf))
    with pytest.raises(TypeError):
        weft.Dataset.from_hf([{"q": 1}])


def test_dataset_hf_and_pandas_missing_dependency(monkeypatch):
    monkeypatch.setitem(sys.modules, "datasets", None)
    monkeypatch.setitem(sys.modules, "pandas", None)
    ds = weft.Dataset(rows=[{"q": 1}])
    with pytest.raises(ImportError, match="pip install datasets"):
        ds.to_hf()
    with pytest.raises(ImportError, match="pip install datasets"):
        weft.Dataset.from_hf(object())
    with pytest.raises(ImportError, match="pip install pandas"):
        ds.to_pandas()


def test_dataset_select_after_publish(client, fake):
    ds = weft.Dataset(name="sel", rows=[{"i": i} for i in range(5)])
    weft.publish(ds)
    loaded = client.get("sel:latest")
    sub = loaded.select(range(1, 3))
    assert [r["i"] for r in sub.rows] == [1, 2]
    ref = weft.publish(sub, name="sel-small")
    assert client.get(ref).rows[1]["i"] == 2


# ---------------------------------------------------------------- bring-your-own-bucket media


def test_content_from_uri_is_logged_by_reference(client, fake):
    img = weft.Image.from_uri("s3://my-bucket/photos/cat.png")
    doc = weft.Content.from_uri("https://example.com/report.pdf", size=1234)
    vid = weft.Video.from_uri("s3://my-bucket/clip.mp4", mimetype="video/mp4")
    assert img.is_reference and img.mimetype == "image/png" and img.filename == "cat.png"
    assert doc.mimetype == "application/pdf" and doc.size == 1234
    assert "s3://my-bucket" in repr(img)
    with pytest.raises(ValueError):
        weft.Content.from_uri("not-a-uri")

    @weft.op
    def describe(image, doc, clip) -> str:
        return "a cat"

    describe(img, doc, vid)
    client.flush()
    [c] = [c for c in fake.calls.values() if fake.op_base(c) == "describe"]
    assert c["inputs"]["image"] == {"_type": "Image", "uri": "s3://my-bucket/photos/cat.png", "mime_type": "image/png", "filename": "cat.png"}
    assert c["inputs"]["doc"] == {"_type": "Content", "mimetype": "application/pdf", "filename": "report.pdf", "uri": "https://example.com/report.pdf", "size": 1234}
    assert c["inputs"]["clip"]["_type"] == "Video" and c["inputs"]["clip"]["uri"] == "s3://my-bucket/clip.mp4"
    assert not fake.files  # nothing was uploaded
    assert not [r for r in fake.requests if r["path"] == "/file/create"]


def test_resolve_url_and_reading_referenced_content(client, fake):
    data = weft.Content(b"bytes in weft", mimetype="text/plain")

    @weft.op
    def keep(x) -> None:
        return None

    keep(data)  # uploads the file
    assert client.resolve_url(data) == f"{fake.url}/file/content?project_id=test-entity/test-proj&digest={data.digest}"
    urls = client.resolve_url(["s3://bucket/k.png", "https://x.test/a.png", f"weft-file://{data.digest}"], expires_in=60)
    assert urls[0].startswith("https://s3.fake/bucket/k.png") and urls[1] == "https://x.test/a.png" and urls[2].endswith(data.digest)
    body = [r["body"] for r in fake.requests if r["path"] == "/files/resolve_url"][-1]
    assert body["expires_in"] == 60 and body["project_id"] == "test-entity/test-proj"
    ref = weft.Content.from_uri(f"weft-file://{data.digest}", mimetype="text/plain")
    assert ref.as_text() == "bytes in weft"  # resolved, then downloaded
    assert ref.resolve_url().endswith(data.digest)
