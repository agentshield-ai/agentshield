"""Integration tests against a real Weft server.

Skipped unless WEFT_TEST_SERVER_URL is set, e.g.::

    WEFT_TEST_SERVER_URL=http://127.0.0.1:6348 pytest tests/test_integration_server.py
"""

import asyncio
import json
import os
import time
import urllib.request
import uuid

import pytest

import weft
from weft import scorers
from weft._canonical import digest

URL = os.environ.get("WEFT_TEST_SERVER_URL")
pytestmark = pytest.mark.skipif(not URL, reason="WEFT_TEST_SERVER_URL not set")


@pytest.fixture
def live():
    project = f"it/sdk-{uuid.uuid4().hex[:8]}"
    c = weft.init(project, server_url=URL, api_key=os.environ.get("WEFT_TEST_API_KEY"), autopatch=False)
    yield c
    weft.flush(10)
    weft.finish()


def _calls(c, **filter):
    return list(c.get_calls(filter=filter or None, sort_by=[{"field": "started_at", "direction": "asc"}]))


def test_trace_roundtrip_and_usage_rollup(live):
    @weft.op
    def llm(prompt: str) -> str:
        weft.add_usage(weft.get_current_call(), "gpt-4o-mini", 100, 20)
        return prompt[::-1]

    @weft.op
    def tool(x: int) -> int:
        return x * 2

    @weft.op
    async def agent(question: str) -> dict:
        a = llm(question)
        b = llm(a)
        return {"answer": b, "tool": tool(21)}

    with weft.attributes({"env": "it"}):
        out = asyncio.run(agent("hello"))
    assert out == {"answer": "hello", "tool": 42}
    live.flush()
    [root] = _calls(live, op_names=["agent"])
    assert root.parent_id is None and root.attributes["env"] == "it"
    assert root.output == out
    usage = root.summary["usage"]["gpt-4o-mini"]
    assert usage["requests"] == 2 and usage["prompt_tokens"] == 200 and usage["completion_tokens"] == 40
    assert root.summary.get("status_counts", {}).get("success", 0) >= 3
    kids = root.children()
    assert sorted(k.op_base_name for k in kids) == ["llm", "llm", "tool"]
    assert all(k.trace_id == root.trace_id for k in kids)
    full = live.get_call(root.id, include_costs=True)
    assert full.summary["weft"]["status"] == "success"
    assert "gpt-4o-mini" in full.summary["weft"].get("costs", {})


def test_op_digest_matches_server(live):
    @weft.op
    def f(x: str = "<&> é  ", y: float = 1.0) -> str:
        return x

    f()
    live.flush()
    ref = live._op_ref(f)
    obj = live._http.post("/obj/read", {"project_id": live.project_id, "object_id": "f", "digest": "latest"})["obj"]
    assert obj["digest"] == ref.digest == digest(f.op_val())
    assert obj["kind"] == "op"
    loaded = ref.get()
    assert isinstance(loaded, weft.Op) and loaded("x") == "x"


def test_exceptions_and_status(live):
    @weft.op
    def bad():
        raise ValueError("kaputt")

    @weft.op
    def parent():
        try:
            bad()
        except ValueError:
            pass

    parent()
    with pytest.raises(ValueError):
        bad()
    live.flush()
    errs = _calls(live, op_names=["bad"])
    assert len(errs) == 2 and all(c.exception.startswith("ValueError: kaputt") for c in errs)
    [p] = _calls(live, op_names=["parent"])
    assert p.summary["weft"]["status"] == "descendant_error"


def test_threads_and_turns(live):
    @weft.op
    def step(i):
        return i

    with weft.thread("conv-it"):
        step(1)
        step(2)
    live.flush()
    res = live._http.post("/threads/query", {"project_id": live.project_id})
    [t] = [t for t in res["threads"] if t["thread_id"] == "conv-it"]
    assert t["turn_count"] == 2


def test_publish_get_dataset_and_row_refs(live):
    rows = [{"q": "a", "expected": "A"}, {"q": "b", "expected": "B"}]
    ds = weft.Dataset(name="it-ds", rows=rows)
    ref = weft.publish(ds)
    assert weft.publish(weft.Dataset(name="it-ds", rows=rows)).digest == ref.digest
    loaded = weft.ref("it-ds:latest").get()
    assert isinstance(loaded, weft.Dataset) and list(loaded.rows) == rows
    assert loaded.rows.row_digests == ds.rows.row_digests
    row_ref = ref.with_extra("attr", "rows", "id", ds.rows.row_digests[1])
    assert live.get(row_ref) == rows[1]
    assert weft.ref("it-ds:v0").get().ref.digest == ref.digest
    p = weft.StringPrompt("hi {x}", name="pr")
    weft.publish(p)
    assert weft.ref("pr").get().format(x=1) == "hi 1"
    obj = live._http.post("/obj/read", {"project_id": live.project_id, "object_id": "pr", "digest": "latest"})["obj"]
    assert obj["base_object_class"] == "Prompt" and obj["leaf_object_class"] == "StringPrompt"


def test_file_upload_digest_matches(live):
    png = b"\x89PNG\r\n\x1a\n" + bytes(range(64))

    @weft.op
    def img(data):
        return len(data)

    img(png)
    live.flush()
    [c] = _calls(live, op_names=["img"])
    media = c.inputs["data"]
    assert media["_type"] == "Image"
    raw = live._http.request_raw("POST", "/file/content", {"project_id": live.project_id, "digest": media["file_digest"]})
    assert raw == png


def test_evaluation_end_to_end(live):
    class Upper(weft.Model):
        @weft.op
        def predict(self, q: str) -> str:
            return q.upper() if q != "b" else "x"

    @weft.op
    def exact(expected: str, output: str) -> bool:
        return expected == output

    ds = weft.Dataset(name="it-eval-ds", rows=[{"q": "a", "expected": "A"}, {"q": "b", "expected": "B"}])
    ev = weft.Evaluation(name="it-eval", dataset=ds, scorers=[exact, scorers.ExactMatchScorer(name="em")])
    summary = asyncio.run(ev.evaluate(Upper(name="upper")))
    assert summary["exact"] == {"true_count": 1, "true_fraction": 0.5}
    assert summary["em"] == {"true_count": 1, "true_fraction": 0.5}
    live.flush()
    [root] = _calls(live, op_names=["Evaluation.evaluate"])
    assert root.output["exact"]["true_fraction"] == 0.5
    pas = _calls(live, op_names=["Evaluation.predict_and_score"])
    assert len(pas) == 2
    # example row refs resolve on the server
    vals = live._http.post("/refs/read_batch", {"refs": [p.inputs["example"] for p in pas]})["vals"]
    assert sorted(v["q"] for v in vals) == ["a", "b"]
    cmp = live._http.post("/evaluations/compare", {"project_id": live.project_id, "evaluation_call_ids": [root.id]})
    assert cmp["evaluations"][0]["model_name"] == "upper"
    assert len(cmp["rows"]) == 2
    assert any(m["key"] == "exact.true_fraction" for m in cmp["metrics"])
    evs = live.get_evaluations()
    assert [e.name for e in evs] == ["it-eval"]


def test_server_side_evaluation_from_sdk_objects(live):
    model = weft.LLMStructuredCompletionModel(
        name="echo-model", llm_model_id="mock/echo",
        default_params={"messages_template": [{"role": "user", "content": "{q}"}], "temperature": 0},
    )
    ds = weft.Dataset(name="srv-ds", rows=[{"q": "hello", "expected": "hello"}, {"q": "x", "expected": "y"}])
    ev = weft.Evaluation(name="srv-eval", dataset=ds, scorers=[
        scorers.ExactMatchScorer(name="exact"),
        scorers.ContainsScorer(name="contains"),
        scorers.LevenshteinScorer(name="lev"),
        scorers.JSONValidScorer(name="json"),
        scorers.RegexScorer(name="re", pattern="^h"),
    ])
    ev_ref = weft.publish(ev)
    m_ref = weft.publish(model)
    # local run of the same objects
    local = asyncio.run(ev.evaluate(model))
    res = live._http.post("/evaluate_model", {"project_id": live.project_id, "evaluation_ref": ev_ref.uri(), "model_ref": m_ref.uri()})
    cid = res["call_id"]
    for _ in range(100):
        st = live._http.post("/evaluation_status", {"project_id": live.project_id, "call_id": cid})["status"]
        if st["code"] in ("complete", "failed"):
            break
        time.sleep(0.1)
    assert st["code"] == "complete", st
    remote = live.get_call(cid)
    for k in ("exact", "contains", "lev", "json", "re"):
        assert remote.output[k] == local[k], (k, remote.output[k], local[k])


def test_llm_judge_and_model_locally(live):
    judge = scorers.LLMAsAJudgeScorer(model="mock/json", scoring_prompt='{"correct": true} {output}')
    out = judge.score(output="4", question="2+2")
    assert isinstance(out, dict)
    m = weft.LLMStructuredCompletionModel(llm_model_id="mock/echo", default_params={"messages_template": [{"role": "user", "content": "say {w}"}]})
    assert m.predict(w="hi") == "say hi"
    live.flush()
    [c] = _calls(live, op_names=["LLMStructuredCompletionModel.predict"])
    assert c.summary["usage"]["mock/echo"]["requests"] == 1


def test_evaluation_logger(live):
    ev = weft.EvaluationLogger(model="logged-model", dataset="logged-ds", name="logged")
    for i in range(3):
        p = ev.log_prediction({"i": i}, i * 2, model_latency=0.1)
        p.log_score("even", i % 2 == 0)
        p.finish()
    summary = ev.log_summary()
    assert summary["even"]["true_count"] == 2
    live.flush()
    [root] = _calls(live, op_names=["Evaluation.evaluate"])
    assert root.output == summary
    assert len(root.children()) == 4


def test_feedback_costs_and_call_ops(live):
    @weft.op
    def f():
        return 1

    _, call = f.call()
    call.feedback.add_reaction("👍")
    call.feedback.add_note("nice")
    fb = list(call.feedback)
    assert {x["feedback_type"] for x in fb} == {"weft.reaction", "weft.note"}
    got = live.get_call(call.id, include_feedback=True)
    assert len(got.raw_feedback) == 2
    call.set_display_name("renamed")
    assert live.get_call(call.id).display_name == "renamed"
    ids = live.add_cost("my-model", 1e-6, 2e-6)
    assert ids and ids[0][1] == "my-model"
    assert any(r["llm_id"] == "my-model" for r in live.query_costs(["my-model"]))
    assert live.server_info()["otel_endpoint"] == "/otel/v1/traces"
    assert len(live.get_calls(filter={"op_names": ["f"]})) == 1
    call.delete()
    assert len(live.get_calls(filter={"op_names": ["f"]})) == 0


def test_otel_configure_exports_spans(live):
    pytest.importorskip("opentelemetry.sdk")
    import weft.otel

    provider = weft.otel.configure(project=live.project_id, server_url=URL, api_key=os.environ.get("WEFT_TEST_API_KEY"), set_global=False, batch=False)
    tracer = provider.get_tracer("it")
    with tracer.start_as_current_span("chat gpt-4o") as span:
        span.set_attribute("gen_ai.request.model", "gpt-4o")
        span.set_attribute("gen_ai.usage.input_tokens", 12)
        span.set_attribute("gen_ai.usage.output_tokens", 3)
        span.set_attribute("input.value", json.dumps({"question": "hi"}))
        span.set_attribute("output.value", "hello")
        with tracer.start_as_current_span("tool") as child:
            weft.otel.set_inputs(child, {"x": 1})
            weft.otel.set_output(child, {"y": 2})
    provider.force_flush()
    provider.shutdown()
    res = live._http.post("/calls/query", {"project_id": live.project_id, "filter": {"trace_roots_only": True}})
    roots = [c for c in res["calls"] if c["op_name"] == "chat gpt-4o"]
    assert len(roots) == 1, res
    root = roots[0]
    assert len(root["id"]) == 16 and len(root["trace_id"]) == 32
    assert root["summary"]["usage"]["gpt-4o"]["prompt_tokens"] == 12
    assert root["output"] in ("hello", {"value": "hello"}) or "hello" in json.dumps(root["output"])
    kids = live._http.post("/calls/query", {"project_id": live.project_id, "filter": {"parent_ids": [root["id"]]}})["calls"]
    assert kids[0]["inputs"] == {"x": 1} and kids[0]["output"] == {"y": 2}


def test_auth_check_endpoint():
    with urllib.request.urlopen(URL + "/server_info") as r:
        assert json.loads(r.read())["otel_endpoint"] == "/otel/v1/traces"


def test_parity_features_live(live):
    # redaction
    @weft.op
    def login(user: str, password: str) -> dict:
        return {"ok": True, "token": "t-123"}

    login("bob", "hunter2")
    live.flush()
    [c] = _calls(live, op_names=["login"])
    assert c.inputs["password"] == "REDACTED" and c.output == {"ok": True, "token": "REDACTED"}

    # op loading round trip, linked to the same op ref
    @weft.op
    def triple(x: int) -> int:
        return 3 * x

    ref = weft.publish(triple)
    loaded = weft.ref("triple").get()
    res, call = loaded.call(3)
    assert res == 9 and call.op_name == ref.uri()

    # unknown Model subclass with op-ref methods
    class LiveModel(weft.Model):
        suffix: str = "!"

        @weft.op
        def predict(self, q: str) -> str:
            return q + self.suffix

    weft.publish(LiveModel(name="live-model", suffix="?"))
    from weft import _object

    del _object._REGISTRY["LiveModel"]
    m = weft.ref("live-model").get()
    assert m.predict("hi") == "hi?"

    # add_rows through /table/update
    ds = weft.Dataset(name="live-grow", rows=[{"i": 0}])
    weft.publish(ds)
    ds2 = ds.add_rows([{"i": 1}, {"i": 2}])
    back = weft.ref("live-grow:latest").get()
    assert list(back.rows) == [{"i": 0}, {"i": 1}, {"i": 2}]
    assert back.rows.row_digests == ds2.rows.row_digests
    assert ds2.ref.digest != ds.ref.digest

    # apply_scorer -> runnable feedback
    @weft.op
    def reply(prompt: str) -> str:
        return "no way"

    @weft.op
    def polite(output: str) -> bool:
        return "please" in output

    _, rc = reply.call("hi")
    r = rc.apply_scorer_sync(polite)
    assert r.result is False
    fb = [f for f in live.get_feedback() if f["ref"] == rc.uri()]
    assert fb[0]["feedback_type"] == "weft.scorer.polite"
    assert fb[0]["runnable_ref"].startswith(f"weft:///{live.project_id}/op/polite:")
    assert fb[0]["call_ref"] == r.score_call.uri()
    got = list(live.get_calls(query={"$eq": [{"$getField": "feedback.weft.scorer.polite.output"}, {"$literal": False}]}))
    assert [g.id for g in got] == [rc.id]

    # annotations, platform objects, leaderboard, compare, server eval
    spec = weft.AnnotationSpec(name="stars", field_schema={"type": "integer", "minimum": 1, "maximum": 5})
    rc.feedback.add_annotation(spec, 4)
    obj = live._http.post("/obj/read", {"project_id": live.project_id, "object_id": "stars", "digest": "latest"})["obj"]
    assert obj["base_object_class"] == "AnnotationSpec"
    model = weft.LLMStructuredCompletionModel(
        name="echo", llm_model_id="mock/echo",
        default_params={"messages_template": [{"role": "user", "content": "{q}"}]},
    )
    ev = weft.Evaluation(name="live-srv", dataset=[{"q": "a", "expected": "a"}, {"q": "b", "expected": "c"}],
                         scorers=[scorers.ExactMatchScorer(name="exact")])
    out = live.evaluate_model(ev, model, poll_interval=0.1, timeout=60)
    assert out["summary"]["exact"] == {"true_count": 1, "true_fraction": 0.5}
    weft.publish(weft.Leaderboard(name="live-lb", columns=[
        weft.LeaderboardColumn(evaluation_object_ref=ev, scorer_name="exact", summary_metric_path="true_fraction")]))
    lb = live.leaderboard("live-lb")
    assert lb["rows"][0]["model_name"] == "echo"
    assert lb["rows"][0]["values"]["0"]["value"] == 0.5
    cmp = live.compare_evaluations([out["call_id"]])
    assert cmp["evaluations"][0]["call_id"] == out["call_id"]
    mon = weft.Monitor(name="live-mon", op_names=["reply"], scorers=[scorers.ContainsScorer(name="has_no", target_column="prompt")], active=False)
    weft.publish(mon)
    assert live._http.post("/obj/read", {"project_id": live.project_id, "object_id": "live-mon", "digest": "latest"})["obj"]["base_object_class"] == "Monitor"
    weft.publish(weft.SavedView(name="live-view", label="Errors", definition={"filter": {"trace_roots_only": True}}))
    assert isinstance(weft.ref("live-view").get(), weft.SavedView)
    ids = live.add_cost("tmp-model", 1e-6, 1e-6)
    live.purge_costs(ids)
    assert not live.query_costs(["tmp-model"])
    assert live.whoami()["user_id"]


def test_batch3_features_live(live, tmp_path):
    # kind/color and global attributes
    @weft.op(kind="agent", color="green")
    def planner(q: str) -> str:
        return q

    planner("x")
    live.flush()
    [p] = _calls(live, op_names=["planner"])
    assert p.attributes["weft"]["span_kind"] == "agent" and p.attributes["weft"]["color"] == "green"
    assert p.ui_url.endswith(f"/{live.project_id}/calls/{p.id}")
    q = list(live.get_calls(query={"$eq": [{"$getField": "attributes.weft.span_kind"}, {"$literal": "agent"}]}))
    assert [c.id for c in q] == [p.id]

    # Content and media round trip through /file/create
    @weft.op
    def report() -> dict:
        return {"doc": weft.Content.from_text("hello", filename="a.txt"), "md": weft.Markdown("# T"), "pdf": weft.PDF(b"%PDF-1.4 x")}

    report()
    live.flush()
    [r] = _calls(live, op_names=["report"])
    assert r.output["doc"]["_type"] == "Content" and live.read_file(r.output["doc"]["file_digest"]) == b"hello"
    assert r.output["md"]["_type"] == "Markdown" and live.read_file(r.output["md"]["file_digest"]) == b"# T"
    assert r.output["pdf"]["_type"] == "PDF"

    # object management
    r0 = weft.publish({"v": 0}, name="mgmt")
    r1 = weft.publish({"v": 1}, name="mgmt")
    live.set_aliases(r1, ["production"])
    assert "production" in live.get_aliases(r1)
    assert weft.ref("mgmt:production").get() == {"v": 1}
    live.remove_aliases("mgmt", "production")
    assert "production" not in live.get_aliases(r1)
    live.add_tags(r1, ["golden", "reviewed"])
    assert sorted(live.get_tags(r1)) == ["golden", "reviewed"]
    live.remove_tags(r1, ["reviewed"])
    assert live.get_tags(r1) == ["golden"]
    assert "golden" in live.list_tags()
    assert live.delete_object_version(r0) == 1
    assert live.delete_all_object_versions("mgmt") == 1

    # EvaluationLogger.log_example linked to dataset rows (refs resolve server side)
    ds = weft.Dataset(name="live-linked", rows=[{"q": "a"}, {"q": "b"}])
    weft.publish(ds)
    ev = weft.EvaluationLogger(model="m", dataset=ds, name="live-linked-eval")
    ev.log_example({"q": "b"}, "B", {"ok": True})
    ev.log_summary()
    live.flush()
    [pas] = _calls(live, op_names=["Evaluation.predict_and_score"])
    assert live._http.post("/refs/read_batch", {"refs": [pas.inputs["example"]]})["vals"] == [{"q": "b"}]

    # new scorer classes publish with the expected class names
    for sc in (scorers.ValidXMLScorer(name="xml"), scorers.JSONSchemaScorer(name="js", schema={"type": "object"}),
               scorers.RuleScorer(name="rule", rule={"$eq": [1, 1]}), scorers.HallucinationFreeScorer(name="hall"),
               scorers.RemoteScorer(name="remote", url="https://example.invalid"), scorers.EmbeddingSimilarityScorer(name="emb"),
               scorers.OpenAIModerationScorer(name="mod"), scorers.RelevanceScorer(name="rel"), scorers.SummarizationScorer(name="sum")):
        weft.publish(sc)
        obj = live._http.post("/obj/read", {"project_id": live.project_id, "object_id": sc.name, "digest": "latest"})["obj"]
        assert obj["leaf_object_class"] in (type(sc).__name__, None) and obj["val"]["_class_name"] == type(sc).__name__
    assert scorers.HallucinationFreeScorer(model="mock/json").score(output="x", q="y")
    # The server provides /embeddings/create; mock/* models embed offline.
    same = scorers.EmbeddingSimilarityScorer(model="mock/embed").score(output="the cat sat", expected="the cat sat")
    other = scorers.EmbeddingSimilarityScorer(model="mock/embed").score(output="the cat sat", expected="quantum chromodynamics")
    assert same == pytest.approx(1.0, abs=1e-6) and 0 <= other < same

    # write-ahead log: unsent items replay against the live server
    wal = {"enable_disk_fallback": True, "wal_dir": str(tmp_path), "retry_max_attempts": 0}
    dead = weft.init(live.project_id, server_url="http://127.0.0.1:9", autopatch=False, settings=wal)
    weft.op(lambda: 1, name="wal_op")()
    dead.flush(5)
    weft.finish()
    import shutil

    [f] = list(tmp_path.iterdir())
    key = f.name.replace("http_127.0.0.1_9", URL.replace("://", "_").replace(":", "_").replace("/", "_"))
    shutil.move(str(f), str(tmp_path / key))
    again = weft.init(live.project_id, server_url=URL, api_key=os.environ.get("WEFT_TEST_API_KEY"), autopatch=False, settings=wal)
    again.flush(10)
    assert len(list(again.get_calls(filter={"op_names": ["wal_op"]}))) == 1
