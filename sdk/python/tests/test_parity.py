"""Redaction, op loading, add_rows, apply_scorer, platform objects and client parity."""

import asyncio

import pytest

import weft
from weft import scorers
from weft._redact import Redactor, redact_pii_text


# ---------------------------------------------------------------- redaction
def test_default_key_redaction(client, fake):
    @weft.op
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

        @weft.op
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

        @weft.op
        def g(token):
            return token

        g("t0k")
        c.flush()
        assert fake.calls_by_op("g")[0]["inputs"]["token"] == "t0k"
    finally:
        weft.finish()


# ---------------------------------------------------------------- op loading
def test_load_op_runs_and_links_to_same_ref(client, fake):
    @weft.op(name="adder")
    def adder(a: int, b: int = 2) -> int:
        return a + b

    ref = weft.publish(adder)
    client.flush()
    loaded = weft.ref("adder").get()
    assert isinstance(loaded, weft.Op)
    assert loaded(1) == 3
    res, call = loaded.call(5, b=5)
    assert res == 10
    assert call.op_name == ref.uri()
    client.flush()
    assert len(fake.objs["adder"]) == 1  # no new op version


def test_load_async_op_and_decorated_with_args(client, fake):
    @weft.op(name="aop", call_display_name="x")
    async def aop(x):
        return x * 3

    weft.publish(aop)
    client.flush()
    loaded = client.get("aop")
    assert asyncio.run(loaded(2)) == 6


def test_load_op_errors_are_clear(client, fake):
    fake.objs["broken"] = [{"digest": "d", "val": {"_type": "Op", "name": "broken", "code": "def broken(:\n", "language": "python"},
                            "version_index": 0, "kind": "op", "base_object_class": "Op"}]
    with pytest.raises(weft.OpLoadError, match="does not parse"):
        weft.ref("broken").get()
    fake.objs["synthetic"] = [{"digest": "d", "val": {"_type": "Op", "name": "synthetic", "code": "# recorded\n", "language": "python"},
                               "version_index": 0, "kind": "op", "base_object_class": "Op"}]
    with pytest.raises(weft.OpLoadError, match="no function definition"):
        weft.ref("synthetic").get()
    fake.objs["js"] = [{"digest": "d", "val": {"_type": "Op", "name": "js", "code": "function js(){}", "language": "javascript"},
                        "version_index": 0, "kind": "op", "base_object_class": "Op"}]
    with pytest.raises(weft.OpLoadError, match="javascript"):
        weft.ref("js").get()


def test_load_unknown_model_subclass_runs_predict(client, fake):
    class RemoteModel(weft.Model):
        prefix: str = ">"

        @weft.op
        def predict(self, question: str) -> str:
            return self.prefix + question.upper()

    weft.publish(RemoteModel(name="remote", prefix="#"))
    client.flush()
    # simulate another process: the class is unknown locally
    from weft import _object

    del _object._REGISTRY["RemoteModel"]
    loaded = weft.ref("remote").get()
    assert type(loaded).__name__ == "RemoteModel" and isinstance(loaded, weft.Model)
    assert loaded.prefix == "#"
    assert loaded.predict("hi") == "#HI"
    _, call = loaded.predict.call("x")
    assert call.op_name.startswith("weft:///test-entity/test-proj/op/RemoteModel.predict:")
    assert call.op_name == fake.objs["remote"][0]["val"]["predict"]
    assert "RemoteModel" not in _object._REGISTRY


def test_loaded_evaluation_has_runnable_function_scorers(client, fake):
    @weft.op
    def is_upper(output: str) -> bool:
        return output.isupper()

    weft.publish(weft.Evaluation(name="ev", dataset=[{"q": "a"}], scorers=[is_upper, scorers.ExactMatchScorer()]))
    client.flush()
    ev = weft.ref("ev").get()
    assert isinstance(ev.scorers[0], weft.Op) and ev.scorers[0](output="A") is True
    assert isinstance(ev.scorers[1], scorers.ExactMatchScorer)


# ---------------------------------------------------------------- add_rows
def test_add_rows_uses_table_update(client, fake):
    ds = weft.Dataset(name="grow", rows=[{"i": 0}, {"i": 1}])
    ref1 = weft.publish(ds)
    ds2 = ds.add_rows([{"i": 2}])
    updates = [r for r in fake.requests if r["path"] == "/table/update"]
    assert len(updates) == 1
    assert updates[0]["body"]["updates"] == [{"append": {"row": {"i": 2}}}]
    assert len([r for r in fake.requests if r["path"] == "/table/create"]) == 1
    assert ds2.ref is not None and ds2.ref.digest != ref1.digest and ds2.ref.name == "grow"
    versions = fake.objs["grow"]
    assert len(versions) == 2
    new_table = versions[1]["val"]["rows"].rsplit("/", 1)[1]
    assert updates[0]["body"]["base_digest"] == versions[0]["val"]["rows"].rsplit("/", 1)[1]
    assert new_table != updates[0]["body"]["base_digest"]
    assert [r["val"] for r in fake.tables[new_table]] == [{"i": 0}, {"i": 1}, {"i": 2}]
    assert ds2.rows.row_digests == [r["digest"] for r in fake.tables[new_table]]
    loaded = weft.ref("grow:latest").get()
    assert list(loaded.rows) == [{"i": 0}, {"i": 1}, {"i": 2}]
    # add_rows on a loaded (lazy) dataset also appends server side
    ds3 = loaded.add_rows([{"i": 3}])
    assert len(ds3) == 4 and len(fake.objs["grow"]) == 3
    # unpublished datasets stay local
    local = weft.Dataset(rows=[{"a": 1}]).add_rows([{"a": 2}])
    assert local.ref is None and len(local) == 2


# ---------------------------------------------------------------- apply_scorer
def test_apply_scorer_records_feedback(client, fake):
    @weft.op
    def generate(prompt: str) -> str:
        return "I hate this" if "bad" in prompt else "Lovely"

    @weft.op
    def toxicity(prompt: str, output: str, threshold: float = 0.5) -> dict:
        return {"flagged": "hate" in output, "threshold": threshold}

    class Length(weft.Scorer):
        @weft.op
        def score(self, *, output: str) -> int:
            return len(output)

    res, call = generate.call("bad prompt")
    r = asyncio.run(call.apply_scorer(toxicity, additional_scorer_kwargs={"threshold": 0.9}))
    assert r.result == {"flagged": True, "threshold": 0.9}
    assert r.score_call.parent_id is None  # scorer call is its own trace
    r2 = call.apply_scorer_sync(Length(name="length"))
    assert r2.result == len("I hate this")
    client.flush()
    fbs = [f for f in fake.feedback if f["ref"] == call.uri()]
    assert [f["feedback_type"] for f in fbs] == ["weft.scorer.toxicity", "weft.scorer.length"]
    assert fbs[0]["payload"] == {"output": {"flagged": True, "threshold": 0.9}}
    assert fbs[0]["runnable_ref"].startswith("weft:///test-entity/test-proj/op/toxicity:")
    assert fbs[0]["call_ref"] == r.score_call.uri()
    assert fbs[1]["runnable_ref"].startswith("weft:///test-entity/test-proj/object/length:")
    [tc] = fake.calls_by_op("toxicity")
    assert tc["inputs"]["prompt"] == "bad prompt" and tc["inputs"]["output"] == "I hate this"

    @weft.op
    def broken(output):
        raise ValueError("scorer bug")

    with pytest.raises(RuntimeError, match="scorer broken failed: ValueError"):
        call.apply_scorer_sync(broken)


def test_apply_scorer_on_fetched_call(client, fake):
    @weft.op
    def f(x):
        return x * 2

    f(4)
    [c] = list(client.get_calls(filter={"op_names": ["f"]}))
    r = c.apply_scorer_sync(scorers.ExactMatchScorer(target_column="x"))
    assert r.result is False
    r = c.apply_scorer_sync(weft.op(lambda x, output: output == 2 * x, name="doubled"))
    assert r.result is True


# ---------------------------------------------------------------- platform objects
def test_platform_objects_serialise(client, fake):
    spec = weft.AnnotationSpec(name="quality", field_schema={"type": "integer", "minimum": 1, "maximum": 5})
    assert spec.value_is_valid(3) and not spec.value_is_valid(9) and not spec.value_is_valid("3")
    weft.publish(spec)
    v = fake.objs["quality"][0]["val"]
    assert v == {"_type": "AnnotationSpec", "_class_name": "AnnotationSpec", "_bases": ["Object", "BaseModel"], "name": "quality",
                 "description": None, "field_schema": {"type": "integer", "minimum": 1, "maximum": 5},
                 "unique_among_creators": True, "op_scope": None}

    judge = scorers.LLMAsAJudgeScorer(name="judge", model="gpt-4o-mini", scoring_prompt="{output}")
    mon = weft.Monitor(name="mon", op_names=["openai.chat.completions.create"], sampling_rate=0.5,
                       query={"$gt": [{"$getField": "summary.weft.latency_ms"}, {"$literal": 10}]}, scorers=[judge])
    weft.publish(mon)
    mv = fake.objs["mon"][0]["val"]
    assert mv["_class_name"] == "Monitor" and mv["active"] is True and mv["sampling_rate"] == 0.5
    assert mv["query"] == {"$expr": {"$gt": [{"$getField": "summary.weft.latency_ms"}, {"$literal": 10}]}}
    assert mv["scorers"][0].startswith("weft:///test-entity/test-proj/object/judge:")
    mon.deactivate()
    assert fake.objs["mon"][1]["val"]["active"] is False

    ev = weft.Evaluation(name="lb-eval", dataset=[{"a": 1}], scorers=[])
    ev_ref = weft.publish(ev)
    lb = weft.Leaderboard(name="lb", columns=[
        weft.LeaderboardColumn(evaluation_object_ref=ev, scorer_name="exact", summary_metric_path="true_fraction"),
        weft.LeaderboardColumn(evaluation_object_ref=ev_ref, scorer_name="model_latency", summary_metric_path="mean", should_minimize=True),
    ])
    weft.publish(lb)
    lv = fake.objs["lb"][0]["val"]
    assert lv["_class_name"] == "Leaderboard"
    assert lv["columns"][0] == {"evaluation_object_ref": ev_ref.uri(), "scorer_name": "exact", "summary_metric_path": "true_fraction", "should_minimize": False}
    back = weft.ref("lb").get()
    assert isinstance(back.columns[1], weft.LeaderboardColumn) and back.columns[1].should_minimize

    sv = weft.SavedView(name="errors", label="Errors", definition={"filter": {"trace_roots_only": True}})
    weft.publish(sv)
    assert fake.objs["errors"][0]["val"]["view_type"] == "traces"
    assert isinstance(weft.ref("errors").get(), weft.SavedView)


# ---------------------------------------------------------------- client parity
def test_client_parity_methods(client, fake):
    @weft.op
    def child(i):
        return i

    @weft.op
    def parent():
        return [child(1), child(2)]

    _, call = parent.call()
    kids = call.children()
    assert [k.inputs["i"] for k in kids] == [1, 2]
    list(client.get_calls(columns=["id", "inputs.i"], expand_columns=["inputs.example"], include_costs=True, include_feedback=True))
    body = [r for r in fake.requests if r["path"] == "/calls/stream_query"][-1]["body"]
    assert body["columns"] == ["id", "inputs.i"] and body["expand_columns"] == ["inputs.example"]
    assert body["include_costs"] is True and body["include_feedback"] is True

    spec = weft.AnnotationSpec(name="stars", field_schema={"type": "integer", "minimum": 1, "maximum": 5})
    sref = weft.publish(spec)
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
    assert lb["_request"] == {"project_id": "test-entity/test-proj", "object_id": "my-lb"}
    cmp = client.compare_evaluations(["a", "b"])
    assert [e["call_id"] for e in cmp["evaluations"]] == ["a", "b"]

    model = weft.LLMStructuredCompletionModel(name="m", llm_model_id="mock/echo")
    ev = weft.Evaluation(name="srv", dataset=[{"q": 1}], scorers=[scorers.ExactMatchScorer()])
    out = client.evaluate_model(ev, model, poll_interval=0.01)
    assert out["status"]["code"] == "complete" and out["summary"] == {"exact": {"true_count": 1, "true_fraction": 1.0}}
    req = [r for r in fake.requests if r["path"] == "/evaluate_model"][0]["body"]
    assert req["evaluation_ref"].startswith("weft:///test-entity/test-proj/object/srv:")
    assert req["model_ref"].startswith("weft:///test-entity/test-proj/object/m:")
    assert client.evaluate_model("srv", "m", wait=False)["call_id"] == "srv-eval-1"
