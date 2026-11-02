import time

import pytest

import weft


def test_get_calls_and_get_call(client, fake):
    @weft.op
    def f(x):
        return x

    for i in range(3):
        f(i)
    calls = client.get_calls(filter={"op_names": ["f"]}, sort_by=[{"field": "started_at", "direction": "asc"}])
    lst = list(calls)
    assert [c.inputs["x"] for c in lst] == [0, 1, 2]
    assert all(isinstance(c, weft.Call) for c in lst)
    assert len(calls) == 3
    assert len(client.get_calls(limit=2)) == 2
    body = [r for r in fake.requests if r["path"] == "/calls/stream_query"][0]["body"]
    assert body["project_id"] == "test-entity/test-proj"
    assert body["filter"] == {"op_names": ["f"]}
    c = client.get_call(lst[0].id)
    assert c.output == 0 and c.op_base_name == "f" and c.status == "success"
    q = list(client.get_calls(query={"$eq": [{"$getField": "inputs.x"}, {"$literal": 1}]}))
    sent = [r for r in fake.requests if r["path"] == "/calls/stream_query"][-1]["body"]
    assert sent["query"] == {"$expr": {"$eq": [{"$getField": "inputs.x"}, {"$literal": 1}]}}
    assert q  # fake server ignores query


def test_feedback_display_name_delete(client, fake):
    @weft.op
    def f():
        return 1

    _, call = f.call()
    call.feedback.add_reaction("👍")
    call.feedback.add_note("great")
    call.feedback.add("custom.score", {"value": 3})
    fbs = list(call.feedback)
    assert [fb["feedback_type"] for fb in fbs] == ["wandb.reaction.1", "wandb.note.1", "custom.score"]
    assert fbs[0]["payload"] == {"emoji": "👍"}
    assert fbs[0]["weave_ref"] == f"weft:///test-entity/test-proj/call/{call.id}"
    call.feedback.purge(fbs[2]["id"])
    assert len(call.feedback) == 2
    call.set_display_name("renamed")
    assert fake.calls[call.id]["display_name"] == "renamed"
    call.delete()
    assert fake.calls[call.id]["deleted_at"]


def test_costs_server_info_evaluations(client, fake):
    client.add_cost("my-llm", prompt_token_cost=1e-6, completion_token_cost=2e-6)
    assert fake.costs["my-llm"]["prompt_token_cost"] == 1e-6
    assert client.server_info()["version"] == "fake"
    ds = weft.Dataset(name="d", rows=[{"a": 1}])
    weft.publish(weft.Evaluation(name="e1", dataset=ds, scorers=[weft.scorers.ExactMatchScorer()]))
    evs = client.get_evaluations()
    assert len(evs) == 1 and isinstance(evs[0], weft.Evaluation)
    assert evs[0].name == "e1" and list(evs[0].dataset.rows) == [{"a": 1}]
    assert isinstance(evs[0].scorers[0], weft.scorers.ExactMatchScorer)


def test_auth_header_and_project_normalisation(fake):
    c = weft.init("bare", server_url=fake.url, api_key="sekret", autopatch=False)
    try:
        assert c.project_id == "default/bare"

        @weft.op
        def f():
            return 1

        f()
        c.flush()
        r = [r for r in fake.requests if r["path"] == "/call/upsert_batch"][0]
        assert r["headers"]["Authorization"] == "Bearer sekret"
        assert r["body"]["batch"][0]["req"]["start"]["project_id"] == "default/bare"
    finally:
        weft.finish()


def test_env_configuration(fake, monkeypatch):
    monkeypatch.setenv("WEFT_SERVER_URL", fake.url)
    monkeypatch.setenv("WEFT_PROJECT", "envproj")
    monkeypatch.setenv("WEFT_API_KEY", "k2")
    c = weft.init(autopatch=False)
    try:
        assert c.server_url == fake.url and c.project_id == "default/envproj" and c.api_key == "k2"
    finally:
        weft.finish()


def test_sender_resilience_server_down(monkeypatch):
    # nothing listens on this port
    c = weft.init("p", server_url="http://127.0.0.1:9", autopatch=False)
    c._sender.backoff = 0.01
    try:

        @weft.op
        def f(x):
            return x + 1

        t0 = time.monotonic()
        assert [f(i) for i in range(50)] == list(range(1, 51))
        assert time.monotonic() - t0 < 1.0  # never blocks on the network
        assert c.flush(timeout=10)
        assert c._sender.failed_requests >= 1
        # explicit publish degrades to a local digest instead of raising
        ref = weft.publish(weft.Dataset(name="d", rows=[{"a": 1}]))
        assert ref.digest
    finally:
        weft.finish()


def test_sender_retries_then_succeeds(client, fake):
    client._sender.backoff = 0.01
    fake.fail_paths["/call/upsert_batch"] = 2

    @weft.op
    def f():
        return 1

    f()
    client.flush(5)
    assert len(fake.calls_by_op("f")) == 1
    assert len([r for r in fake.requests if r["path"] == "/call/upsert_batch"]) == 3


def test_bounded_queue_drops(fake):
    c = weft.init("p", server_url="http://127.0.0.1:9", autopatch=False, max_queue=5)
    c._sender.backoff = 0.2
    try:

        @weft.op
        def f():
            return 1

        for _ in range(50):
            f()
        assert c._sender.dropped > 0
    finally:
        c._sender.max_retries = 0
        weft.finish()


def test_upsert_batch_coalesces(client, fake):
    client._sender.flush_interval = 0.05

    @weft.op
    def f():
        return 1

    for _ in range(20):
        f()
    client.flush()
    batches = [r["body"]["batch"] for r in fake.requests if r["path"] == "/call/upsert_batch"]
    assert sum(len(b) for b in batches) == 40
    assert len(batches) < 10
