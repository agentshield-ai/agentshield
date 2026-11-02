import asyncio
import threading
import uuid

import pytest

import weft


def _uuid7(s):
    u = uuid.UUID(s)
    return u.version == 7


def test_nested_ops_record_parent_and_trace(client, fake):
    @weft.op
    def inner(x: int) -> int:
        return x * 2

    @weft.op
    def outer(x: int, y: int = 3) -> int:
        return inner(x) + y

    assert outer(2) == 7
    client.flush()
    [o] = fake.calls_by_op("outer")
    [i] = fake.calls_by_op("inner")
    assert o["parent_id"] is None
    assert i["parent_id"] == o["id"]
    assert i["trace_id"] == o["trace_id"]
    assert o["inputs"] == {"x": 2, "y": 3}
    assert o["output"] == 7 and i["output"] == 4
    assert _uuid7(o["id"]) and _uuid7(o["trace_id"])
    assert o["op_name"].startswith("weft:///test-entity/test-proj/op/outer:")
    assert o["started_at"].endswith("Z") and o["ended_at"].endswith("Z")
    # op object published once with Op conventions
    [op_v] = fake.objs["outer"]
    assert op_v["val"]["_type"] == "Op"
    assert op_v["val"]["language"] == "python"
    assert "def outer" in op_v["val"]["code"]
    assert op_v["val"]["signature"] == "(x: int, y: int = 3) -> int"
    assert o["op_name"].endswith(":" + op_v["digest"])
    # batching: call ends ride in upsert_batch
    assert "/call/upsert_batch" in fake.paths()


def test_op_published_once(client, fake):
    @weft.op
    def f():
        return 1

    for _ in range(5):
        f()
    client.flush()
    assert len([r for r in fake.requests if r["path"] == "/obj/create"]) == 1
    assert len(fake.calls_by_op("f")) == 5


def test_async_ops_nest(client, fake):
    @weft.op
    async def leaf(i):
        await asyncio.sleep(0.01)
        return i

    @weft.op
    async def root():
        return sum(await asyncio.gather(*[leaf(i) for i in range(4)]))

    assert asyncio.run(root()) == 6
    client.flush()
    [r] = fake.calls_by_op("root")
    leaves = fake.calls_by_op("leaf")
    assert len(leaves) == 4
    assert all(lf["parent_id"] == r["id"] for lf in leaves)


def test_sync_generator_accumulates(client, fake):
    @weft.op
    def child(i):
        return i

    @weft.op
    def gen(n):
        for i in range(n):
            yield child(i)

    assert list(gen(3)) == [0, 1, 2]
    client.flush()
    [g] = fake.calls_by_op("gen")
    assert g["output"] == [0, 1, 2]
    assert all(c["parent_id"] == g["id"] for c in fake.calls_by_op("child"))


def test_async_generator_and_custom_accumulator(client, fake):
    @weft.op(accumulator=lambda acc, x: (acc or "") + x)
    async def stream():
        for t in ["a", "b", "c"]:
            yield t

    async def main():
        return [x async for x in stream()]

    assert asyncio.run(main()) == ["a", "b", "c"]
    client.flush()
    [s] = fake.calls_by_op("stream")
    assert s["output"] == "abc"
    assert s["ended_at"]


def test_generator_closed_early(client, fake):
    @weft.op
    def gen():
        for i in range(100):
            yield i

    g = gen()
    assert next(g) == 0 and next(g) == 1
    g.close()
    client.flush()
    [c] = fake.calls_by_op("gen")
    assert c["output"] == [0, 1]
    assert not c.get("exception")


def test_exception_recorded_and_reraised(client, fake):
    @weft.op
    def boom(x):
        raise ValueError(f"bad {x}")

    @weft.op
    def parent():
        try:
            boom(1)
        except ValueError:
            return "handled"

    with pytest.raises(ValueError, match="bad 2"):
        boom(2)
    assert parent() == "handled"
    client.flush()
    calls = fake.calls_by_op("boom")
    assert len(calls) == 2
    for c in calls:
        assert c["exception"].startswith("ValueError: bad")
        assert "Traceback" in c["exception"]
        assert c["ended_at"]


def test_async_exception(client, fake):
    @weft.op
    async def aboom():
        raise KeyError("k")

    with pytest.raises(KeyError):
        asyncio.run(aboom())
    client.flush()
    [c] = fake.calls_by_op("aboom")
    assert c["exception"].startswith("KeyError")


def test_call_method_returns_result_and_call(client, fake):
    @weft.op
    def add(a, b):
        return a + b

    @weft.op
    def fails():
        raise RuntimeError("x")

    res, call = add.call(1, 2)
    assert res == 3
    assert isinstance(call, weft.Call)
    assert call.output == 3 and call.inputs == {"a": 1, "b": 2}
    res2, call2 = fails.call()
    assert res2 is None and call2.exception.startswith("RuntimeError: x")

    @weft.op
    async def aadd(a, b):
        return a + b

    r3, c3 = asyncio.run(aadd.call(2, 2))
    assert r3 == 4 and c3.output == 4


def test_get_current_call_and_usage(client, fake):
    seen = {}

    @weft.op
    def llm():
        c = weft.get_current_call()
        seen["id"] = c.id
        weft.add_usage(c, "gpt-4o-mini", 10, 5)
        return "ok"

    llm()
    assert weft.get_current_call() is None
    client.flush()
    [c] = fake.calls_by_op("llm")
    assert c["id"] == seen["id"]
    assert c["summary"]["usage"] == {"gpt-4o-mini": {"requests": 1, "prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}}


def test_threads_with_context_propagation(client, fake):
    @weft.op
    def work(i):
        return i

    @weft.op
    def fan_out():
        with weft.ThreadPoolExecutor(max_workers=4) as ex:
            out = list(ex.map(work, range(4)))
        t = weft.Thread(target=work, args=(10,))
        t.start()
        t.join()
        return out

    fan_out()
    client.flush()
    [root] = fake.calls_by_op("fan_out")
    kids = fake.calls_by_op("work")
    assert len(kids) == 5
    assert all(k["parent_id"] == root["id"] for k in kids)


def test_plain_threads_start_new_traces(client, fake):
    @weft.op
    def work():
        return 1

    @weft.op
    def outer():
        t = threading.Thread(target=work)
        t.start()
        t.join()

    outer()
    client.flush()
    [w] = fake.calls_by_op("work")
    assert w["parent_id"] is None


def test_display_name_and_postprocess(client, fake):
    @weft.op(
        name="renamed",
        call_display_name=lambda call: f"q={call.inputs['q']}",
        postprocess_inputs=lambda d: {k: v for k, v in d.items() if k != "secret"},
        postprocess_output=lambda o: {"len": len(o)},
    )
    def f(q, secret):
        return "hello"

    assert f("x", "s3cr3t") == "hello"
    client.flush()
    [c] = fake.calls_by_op("renamed")
    assert c["display_name"] == "q=x"
    assert c["inputs"] == {"q": "x"}
    assert c["output"] == {"len": 5}

    @weft.op(call_display_name="static")
    def g():
        return 1

    g()
    client.flush()
    assert fake.calls_by_op("g")[0]["display_name"] == "static"


def test_attributes_context(client, fake):
    @weft.op
    def f():
        return 1

    with weft.attributes({"env": "test", "user": {"id": 1}}):
        with weft.attributes({"extra": True}):
            f()
    f()
    client.flush()
    a, b = fake.calls_by_op("f")
    assert a["attributes"]["env"] == "test" and a["attributes"]["extra"] is True
    assert a["attributes"]["user"] == {"id": 1}
    assert "env" not in b["attributes"]


def test_thread_context_sets_thread_and_turns(client, fake):
    @weft.op
    def tool():
        return 1

    @weft.op
    def turn(i):
        tool()
        return i

    with weft.thread("conv-1") as t:
        assert t.thread_id == "conv-1"
        turn(1)
        turn(2)
    with weft.thread() as t2:
        turn(3)
    client.flush()
    turns = fake.calls_by_op("turn")
    tools = fake.calls_by_op("tool")
    for c in turns:
        assert c["turn_id"] == c["id"]
    assert turns[0]["thread_id"] == turns[1]["thread_id"] == "conv-1"
    assert turns[2]["thread_id"] == t2.thread_id and len(t2.thread_id) == 36
    for tl in tools:
        parent = fake.calls[tl["parent_id"]]
        assert tl["thread_id"] == parent["thread_id"]
        assert tl["turn_id"] == parent["id"]


def test_thread_inside_op_marks_inner_calls_as_turns(client, fake):
    @weft.op
    def step():
        return 1

    @weft.op
    def session():
        with weft.thread("t"):
            step()

    session()
    client.flush()
    [s] = fake.calls_by_op("step")
    assert s["thread_id"] == "t" and s["turn_id"] == s["id"]
    assert not fake.calls_by_op("session")[0].get("thread_id")


def test_tracing_sample_rate_zero_skips_whole_trace(client, fake):
    @weft.op
    def child():
        return 1

    @weft.op(tracing_sample_rate=0.0)
    def root():
        return child()

    assert root() == 1
    client.flush()
    assert fake.calls_by_op("root") == []
    assert fake.calls_by_op("child") == []


def test_untraced_without_init(fake):
    weft.finish()

    @weft.op
    def f(x):
        return x

    @weft.op
    async def g(x):
        return x

    assert f(1) == 1
    assert asyncio.run(g(2)) == 2
    assert f.call(3)[0] == 3
    assert fake.requests == []


def test_disabled_env(fake, monkeypatch):
    monkeypatch.setenv("WEFT_DISABLED", "true")
    c = weft.init("p", server_url=fake.url, autopatch=False)

    @weft.op
    def f():
        return 1

    f()
    c.flush()
    weft.finish()
    assert fake.requests == []


def test_methods_capture_self_ref_when_published(client, fake):
    class Agent(weft.Object):
        temperature: float = 0.5

        @weft.op
        def run(self, q: str) -> str:
            return q.upper()

    a = Agent(name="agent")
    assert a.run("hi") == "HI"
    weft.publish(a)
    a.run("again")

    class Plain:
        def __repr__(self):
            return "<Plain>"

        @weft.op
        def go(self):
            return 1

    Plain().go()
    client.flush()
    first, second = fake.calls_by_op("Agent.run")
    assert first["inputs"]["self"]["temperature"] == 0.5  # unpublished: serialised value
    assert second["inputs"]["self"].startswith("weft:///test-entity/test-proj/object/agent:")
    assert fake.calls_by_op("Plain.go")[0]["inputs"]["self"] == "<Plain>"
    # published object val includes the op ref
    [v] = fake.objs["agent"]
    assert v["val"]["run"].startswith("weft:///test-entity/test-proj/op/Agent.run:")
    assert v["val"]["_bases"] == ["Object", "BaseModel"]


def test_bound_op_call_and_signature(client, fake):
    class M(weft.Model):
        @weft.op
        def predict(self, question: str) -> str:
            return question

    m = M()
    res, call = m.predict.call("q")
    assert res == "q" and call.inputs["question"] == "q"
    import inspect

    assert list(inspect.signature(m.predict).parameters) == ["question"]
