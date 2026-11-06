"""Redaction, op loading, add_rows, apply_scorer, platform objects and client parity."""

import asyncio

import pytest

import weft
from weft import scorers
from weft._redact import Redactor, redact_pii_text


# ---------------------------------------------------------------- redaction
def test_default_key_redaction(client, fake):
    @weft.trace
    def call_api(prompt, api_key, headers):
        return {"text": "ok", "Authorization": "Bearer x", "nested": [{"password": "p"}], "max_tokens": 5}

    call_api("hi", "sk-123", {"Authorization": "Bearer sk", "X-Other": 1})
    client.flush()
    [c] = fake.calls_by_op("call_api")
    assert c["inputs"]["api_key"] == "REDACTED"
    assert c["inputs"]["headers"] == {"Authorization": "REDACTED", "X-Other": 1}
    assert c["inputs"]["prompt"] == "hi"
    assert c["output"] == {"text": "ok", "Authorization": "REDACTED", "nested": [{"password": "REDACTED"}], "max_tokens": 5}


def test_redaction_settings_and_env(fake, monkeypatch):
    c = weft.init("p", server_url=fake.url, autopatch=False, settings={"redact_keys": ["ssn"], "redact_pii": True})
    try:

        @weft.trace
        def f(ssn, api_key, note):
            return note

        f("123", "k", "mail bob@example.com or call 555-123-4567")
        c.flush()
        [call] = fake.calls_by_op("f")
        assert call["inputs"]["ssn"] == "REDACTED"
        assert call["inputs"]["api_key"] == "k"  # custom list replaces the defaults
        assert call["output"] == "mail <EMAIL_ADDRESS> or call <PHONE_NUMBER>"
    finally:
        weft.finish()
    monkeypatch.setenv("WEFT_REDACT_KEYS", "0")
    assert not Redactor().active
    assert Redactor(pii=True)("4111 1111 1111 1111") == "<CREDIT_CARD>"
    assert redact_pii_text("ssn 123-45-6789") == "ssn <US_SSN>"


def test_redaction_env_opt_out(fake, monkeypatch):
    monkeypatch.setenv("WEFT_REDACT_KEYS", "0")
    c = weft.init("p", server_url=fake.url, autopatch=False)
    try:

        @weft.trace
        def g(token):
            return token

        g("t0k")
        c.flush()
        assert fake.calls_by_op("g")[0]["inputs"]["token"] == "t0k"
    finally:
        weft.finish()


# ---------------------------------------------------------------- op loading
def _stored_op(fake, name, code, language="python"):
    fake.objs[name] = [{"digest": "d" + name, "value": {"_type": "Op", "name": name, "code": code, "language": language},
                        "version": 0, "name": name, "kind": "function", "class": None}]


def test_get_of_stored_code_is_inert(client, fake, tmp_path, monkeypatch):
    """A writer to the project stores an op whose module code has a side effect:
    get() must hand back data, never execute it (security, F1)."""
    monkeypatch.delenv("WEFT_ALLOW_CODE_LOAD", raising=False)
    mark = tmp_path / "PWNED"
    code = f"import pathlib\npathlib.Path({str(mark)!r}).write_text('x')\ndef evil(a=pathlib.Path({str(mark)!r}).write_text('y')):\n    return 1\n"
    _stored_op(fake, "evil", code)
    got = weft.get("evil")
    assert isinstance(got, weft.StoredOp) and not isinstance(got, weft.Op)
    assert got.name == "evil" and got.code == code and got.language == "python"
    assert got.ref.uri().endswith("/function/evil:devil")
    with pytest.raises(weft.OpLoadError, match="trust_code=True"):
        got()
    with pytest.raises(weft.OpLoadError, match="trust_code=True"):
        weft.load_op("evil")
    with pytest.raises(weft.OpLoadError, match="trust_code=True"):
        got.load()
    with pytest.raises(weft.OpLoadError, match="trust_code=True"):
        client.load_op(got)
    # nested: a list of function refs inside an object, and a ref read
    fake.objs["holder"] = [{"digest": "h", "value": {"fns": [got.ref.uri()]}, "version": 0, "name": "holder", "kind": "object", "class": None}]
    holder = weft.get("holder")
    assert isinstance(holder["fns"][0], (weft.StoredOp, str))
    assert not mark.exists()
    # the explicit opt-in runs it (that is what trust means)
    loaded = weft.load_op("evil", trust_code=True)
    assert isinstance(loaded, weft.Op) and loaded() == 1
    assert mark.exists()


def test_code_load_env_opt_in(client, fake, monkeypatch):
    _stored_op(fake, "seven", "def seven():\n    return 7\n")
    monkeypatch.setenv("WEFT_ALLOW_CODE_LOAD", "1")
    assert weft.get("seven")() == 7
    monkeypatch.setenv("WEFT_ALLOW_CODE_LOAD", "0")
    assert isinstance(weft.get("seven"), weft.StoredOp)


def test_load_op_runs_and_links_to_same_ref(client, fake):
    @weft.trace(name="adder")
    def adder(a: int, b: int = 2) -> int:
        return a + b

    ref = weft.save(adder)
    client.flush()
    stored = weft.get("adder")
    assert isinstance(stored, weft.StoredOp) and "return a + b" in stored.code
    loaded = weft.load_op("adder", trust_code=True)
    assert isinstance(loaded, weft.Op)
    assert loaded(1) == 3
    res, call = loaded.call(5, b=5)
    assert res == 10
    assert call.name == ref.uri()
    assert weft.load_op(stored, trust_code=True)(2) == 4
    assert isinstance(weft.get("adder", trust_code=True), weft.Op)
    client.flush()
    assert len(fake.objs["adder"]) == 1  # no new op version


def test_load_async_op_and_decorated_with_args(client, fake):
    @weft.trace(name="aop", span_display_name="x")
    async def aop(x):
        return x * 3

    weft.save(aop)
    client.flush()
    loaded = client.load_op("aop", trust_code=True)
    assert asyncio.run(loaded(2)) == 6


def test_load_op_errors_are_clear(client, fake):
    _stored_op(fake, "broken", "def broken(:\n")
    with pytest.raises(weft.OpLoadError, match="does not parse"):
        weft.load_op("broken", trust_code=True)
    _stored_op(fake, "synthetic", "# recorded\n")
    with pytest.raises(weft.OpLoadError, match="no function definition"):
        weft.load_op("synthetic", trust_code=True)
    _stored_op(fake, "js", "function js(){}", "javascript")
    with pytest.raises(weft.OpLoadError, match="javascript"):
        weft.get("js", trust_code=True)
    fake.objs["plain"] = [{"digest": "p", "value": {"k": 1}, "version": 0, "name": "plain", "kind": "object", "class": None}]
    with pytest.raises(TypeError, match="not a stored function"):
        weft.load_op("plain", trust_code=True)


def test_load_unknown_model_subclass_runs_predict(client, fake):
    class RemoteModel(weft.Model):
        prefix: str = ">"

        @weft.trace
        def predict(self, question: str) -> str:
            return self.prefix + question.upper()

    weft.save(RemoteModel(name="remote", prefix="#"))
    client.flush()
    # simulate another process: the class is unknown locally
    from weft import _object

    del _object._REGISTRY["RemoteModel"]
    inert = weft.get("remote")
    assert type(inert).__name__ == "RemoteModel" and inert.prefix == "#"
    assert isinstance(inert.predict, weft.StoredOp)
    with pytest.raises(weft.OpLoadError, match="trust_code=True"):
        inert.predict("hi")
    # saving the loaded object again keeps its method ref
    weft.save(inert, "remote2")
    client.flush()
    assert fake.objs["remote2"][0]["value"]["predict"] == fake.objs["remote"][0]["value"]["predict"]
    loaded = weft.get("remote", trust_code=True)
    assert type(loaded).__name__ == "RemoteModel" and isinstance(loaded, weft.Model)
    assert loaded.prefix == "#"
    assert loaded.predict("hi") == "#HI"
    _, call = loaded.predict.call("x")
    assert call.name.startswith("weft:///test-entity/test-proj/function/RemoteModel.predict:")
    assert call.name == fake.objs["remote"][0]["value"]["predict"]
    assert "RemoteModel" not in _object._REGISTRY


def test_loaded_evaluation_has_runnable_function_scorers(client, fake):
    @weft.trace
    def is_upper(output: str) -> bool:
        return output.isupper()

    weft.save(weft.Evaluation(name="ev", dataset=[{"q": "a"}], scorers=[is_upper, scorers.ExactMatchScorer()]))
    client.flush()
    assert isinstance(weft.get("ev").scorers[0], weft.StoredOp)
    ev = weft.get("ev", trust_code=True)
    assert isinstance(ev.scorers[0], weft.Op) and ev.scorers[0](output="A") is True
    assert isinstance(ev.scorers[1], scorers.ExactMatchScorer)


# ---------------------------------------------------------------- add_rows
def test_add_rows_uses_table_update(client, fake):
    ds = weft.Dataset(name="grow", rows=[{"i": 0}, {"i": 1}])
    ref1 = weft.save(ds)
    ds2 = ds.add_rows([{"i": 2}])
    updates = [r for r in fake.requests if r["path"] == "/api/v1/tables/update"]
    assert len(updates) == 1
    assert updates[0]["body"]["updates"] == [{"append": {"row": {"i": 2}}}]
    assert len([r for r in fake.requests if r["path"] == "/api/v1/tables/create"]) == 1
    assert ds2.ref is not None and ds2.ref.digest != ref1.digest and ds2.ref.name == "grow"
    versions = fake.objs["grow"]
    assert len(versions) == 2
    new_table = versions[1]["value"]["rows"].rsplit("/", 1)[1]
    assert updates[0]["body"]["digest"] == versions[0]["value"]["rows"].rsplit("/", 1)[1]
    assert new_table != updates[0]["body"]["digest"]
    assert [r["value"] for r in fake.tables[new_table]] == [{"i": 0}, {"i": 1}, {"i": 2}]
    assert ds2.rows.row_digests == [r["digest"] for r in fake.tables[new_table]]
    loaded = weft.get("grow:latest")
    assert list(loaded.rows) == [{"i": 0}, {"i": 1}, {"i": 2}]
    # add_rows on a loaded (lazy) dataset also appends server side
    ds3 = loaded.add_rows([{"i": 3}])
    assert len(ds3) == 4 and len(fake.objs["grow"]) == 3
    # unpublished datasets stay local
    local = weft.Dataset(rows=[{"a": 1}]).add_rows([{"a": 2}])
    assert local.ref is None and len(local) == 2


# ---------------------------------------------------------------- apply_scorer
def test_apply_scorer_records_feedback(client, fake):
    @weft.trace
    def generate(prompt: str) -> str:
        return "I hate this" if "bad" in prompt else "Lovely"

    @weft.trace
    def toxicity(prompt: str, output: str, threshold: float = 0.5) -> dict:
        return {"flagged": "hate" in output, "threshold": threshold}

    class Length(weft.Scorer):
        @weft.trace
        def score(self, *, output: str) -> int:
            return len(output)

    res, call = generate.call("bad prompt")
    r = asyncio.run(call.apply_scorer(toxicity, additional_scorer_kwargs={"threshold": 0.9}))
    assert r.result == {"flagged": True, "threshold": 0.9}
    assert r.score_span.parent_id is None  # the scorer span is its own trace
    r2 = call.apply_scorer_sync(Length(name="length"))
    assert r2.result == len("I hate this")
    client.flush()
    fbs = [f for f in fake.feedback if f["ref"] == call.uri()]
    assert [f["feedback_type"] for f in fbs] == ["weft.scorer.toxicity", "weft.scorer.length"]
    assert fbs[0]["payload"] == {"output": {"flagged": True, "threshold": 0.9}}
    assert fbs[0]["runnable_ref"].startswith("weft:///test-entity/test-proj/function/toxicity:")
    assert fbs[0]["span_ref"] == r.score_span.uri()
    assert fbs[1]["runnable_ref"].startswith("weft:///test-entity/test-proj/object/length:")
    [tc] = fake.calls_by_op("toxicity")
    assert tc["inputs"]["prompt"] == "bad prompt" and tc["inputs"]["output"] == "I hate this"

    @weft.trace
    def broken(output):
        raise ValueError("scorer bug")

    with pytest.raises(RuntimeError, match="scorer broken failed: ValueError"):
        call.apply_scorer_sync(broken)


def test_apply_scorer_on_fetched_call(client, fake):
    @weft.trace
    def f(x):
        return x * 2

    f(4)
    [c] = list(client.spans(filter={"names": ["f"]}))
    r = c.apply_scorer_sync(scorers.ExactMatchScorer(target_column="x"))
    assert r.result is False
    r = c.apply_scorer_sync(weft.trace(lambda x, output: output == 2 * x, name="doubled"))
    assert r.result is True


# ---------------------------------------------------------------- platform objects
def test_platform_objects_serialise(client, fake):
    spec = weft.AnnotationSpec(name="quality", field_schema={"type": "integer", "minimum": 1, "maximum": 5})
    assert spec.value_is_valid(3) and not spec.value_is_valid(9) and not spec.value_is_valid("3")
    weft.save(spec)
    v = fake.objs["quality"][0]["value"]
    assert v == {"_type": "AnnotationSpec", "_class_name": "AnnotationSpec", "_bases": ["Object", "BaseModel"], "name": "quality",
                 "description": None, "field_schema": {"type": "integer", "minimum": 1, "maximum": 5},
                 "unique_among_creators": True, "names": None}

    judge = scorers.LLMAsAJudgeScorer(name="judge", model="gpt-4o-mini", scoring_prompt="{output}")
    mon = weft.Monitor(name="mon", names=["openai.chat.completions.create"], sampling_rate=0.5,
                       query={"$gt": [{"$getField": "summary.weft.latency_ms"}, {"$literal": 10}]}, scorers=[judge])
    weft.save(mon)
    mv = fake.objs["mon"][0]["value"]
    assert mv["_class_name"] == "Monitor" and mv["active"] is True and mv["sampling_rate"] == 0.5
    assert mv["names"] == ["openai.chat.completions.create"] and "op_names" not in mv
    assert mv["query"] == {"$expr": {"$gt": [{"$getField": "summary.weft.latency_ms"}, {"$literal": 10}]}}
    assert mv["scorers"][0].startswith("weft:///test-entity/test-proj/object/judge:")
    mon.deactivate()
    assert fake.objs["mon"][1]["value"]["active"] is False

    ev = weft.Evaluation(name="lb-eval", dataset=[{"a": 1}], scorers=[])
    ev_ref = weft.save(ev)
    lb = weft.Leaderboard(name="lb", columns=[
        weft.LeaderboardColumn(evaluation_object_ref=ev, scorer_name="exact", summary_metric_path="true_fraction"),
        weft.LeaderboardColumn(evaluation_object_ref=ev_ref, scorer_name="model_latency", summary_metric_path="mean", should_minimize=True),
    ])
    weft.save(lb)
    lv = fake.objs["lb"][0]["value"]
    assert lv["_class_name"] == "Leaderboard"
    assert lv["columns"][0] == {"evaluation_object_ref": ev_ref.uri(), "scorer_name": "exact", "summary_metric_path": "true_fraction", "should_minimize": False}
    back = weft.get("lb")
    assert isinstance(back.columns[1], weft.LeaderboardColumn) and back.columns[1].should_minimize

    sv = weft.SavedView(name="errors", label="Errors", definition={"filter": {"trace_roots_only": True}})
    weft.save(sv)
    assert fake.objs["errors"][0]["value"]["view_type"] == "traces"
    assert isinstance(weft.get("errors"), weft.SavedView)


# ---------------------------------------------------------------- client parity
def test_client_parity_methods(client, fake):
    @weft.trace
    def child(i):
        return i

    @weft.trace
    def parent():
        return [child(1), child(2)]

    _, call = parent.call()
    kids = call.children()
    assert [k.inputs["i"] for k in kids] == [1, 2]
    list(client.spans(columns=["id", "inputs.i"], expand_columns=["inputs.example"], include_costs=True, include_feedback=True))
    body = [r for r in fake.requests if r["path"] == "/api/v1/spans/stream"][-1]["body"]
    assert body["columns"] == ["id", "inputs.i"] and body["expand_columns"] == ["inputs.example"]
    assert body["include_costs"] is True and body["include_feedback"] is True

    spec = weft.AnnotationSpec(name="stars", field_schema={"type": "integer", "minimum": 1, "maximum": 5})
    sref = weft.save(spec)
    call.feedback.add("weft.annotation.stars", {"value": 4}, annotation_ref=sref)
    call.feedback.add_annotation(spec, 5)
    with pytest.raises(ValueError):
        call.feedback.add_annotation(spec, 7)
    fb = client.get_feedback(query={"$eq": [{"$getField": "ref"}, {"$literal": call.uri()}]}, limit=10)
    assert [f["annotation_ref"] for f in fb] == [sref.uri(), sref.uri()]
    assert fb[1]["feedback_type"] == "weft.annotation.stars"

    ids = client.add_cost("m1", 1e-6, 2e-6)
    assert [c["llm_id"] for c in client.query_costs(["m1"])] == ["m1"]
    client.purge_costs(ids)
    assert fake.costs == {}

    assert client.whoami() == {"user_id": "anonymous", "auth_required": False}
    lb = client.leaderboard("my-lb")
    assert lb["_request"] == {"project_id": "test-entity/test-proj", "name": "my-lb"}
    cmp = client.compare_evaluations(["a", "b"])
    assert [e["id"] for e in cmp["evaluations"]] == ["a", "b"]

    model = weft.LLMStructuredCompletionModel(name="m", llm_model_id="mock/echo")
    ev = weft.Evaluation(name="srv", dataset=[{"q": 1}], scorers=[scorers.ExactMatchScorer()])
    out = client.evaluate_model(ev, model, poll_interval=0.01)
    assert out["status"]["code"] == "complete" and out["summary"] == {"exact": {"true_count": 1, "true_fraction": 1.0}}
    req = [r for r in fake.requests if r["path"] == "/api/v1/evaluations/run"][0]["body"]
    assert req["evaluation_ref"].startswith("weft:///test-entity/test-proj/object/srv:")
    assert req["model_ref"].startswith("weft:///test-entity/test-proj/object/m:")
    assert client.evaluate_model("srv", "m", wait=False)["id"] == "srv-eval-1"
