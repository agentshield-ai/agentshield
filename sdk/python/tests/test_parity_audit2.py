"""Second parity audit follow-ups: TypeSafe, require_current_call,
threads_query, register_custom_runtime, conversation links, WEFT_DEBUG_HTTP
and WEFT_DISABLE_WAL_SENDER, against fakes and the fake server."""

import asyncio
import json
import sys
import types

import pytest

import weft
from weft.integrations import unpatch_all
from weft.trace_server_interface import SortBy, ThreadsQueryReq


@pytest.fixture(autouse=True)
def _unpatch():
    yield
    unpatch_all()


def kind(c):
    return (c.get("attributes") or {}).get("weft", {}).get("span_kind")


# ---------------------------------------------------------------- TypeSafe
class _Usage:
    def __init__(self, i, o):
        self.input_tokens, self.output_tokens = i, o


class _Result:
    """Shaped like typesafe_sdk's SystemOneResponse (pydantic-ish, request_id from the header)."""

    def __init__(self, answers, model="jev-2", usage=(42, 7), request_id="req_123"):
        self.answers = answers
        self.model = model
        self.usage = _Usage(*usage)
        self.request_id = request_id


class TypeSafeAPIError(Exception):
    def __init__(self, status, body):
        super().__init__(f"Error code: {status} - {body}")  # the SDK puts the body in the message
        self.status = status
        self.headers = {"x-typesafe-request-id": "req_err"}


def _install_typesafe(monkeypatch):
    class _Config:
        default_model = "jev-latest"

    class TypeSafeClient:
        def __init__(self):
            self._config = _Config()
            self.calls = []

        def system_one(self, state, questions, *, model=None, extra_body=None, timeout=None):
            self.calls.append((state, questions, model))
            if state == "boom":
                raise TypeSafeAPIError(422, '{"detail": "secret customer text"}')
            return _Result({"q0": "yes", "q1": 3})

    class AsyncTypeSafeClient(TypeSafeClient):
        async def system_one(self, state, questions, *, model=None, extra_body=None, timeout=None):
            return _Result({"ok": True}, usage=(5, 1))

    mod = types.ModuleType("typesafe_sdk")
    mod.TypeSafeClient = TypeSafeClient
    mod.AsyncTypeSafeClient = AsyncTypeSafeClient
    monkeypatch.setitem(sys.modules, "typesafe_sdk", mod)
    return mod


def test_typesafe_system_one(client, fake, monkeypatch):
    from weft.integrations import patch_typesafe

    mod = _install_typesafe(monkeypatch)
    assert patch_typesafe()
    ts = mod.TypeSafeClient()
    res = ts.system_one({"ticket": "refund please", "amount": 20}, [{"text": "Is it a refund?"}, {"text": "Amount?", "type": "int"}])
    assert res.answers == {"q0": "yes", "q1": 3} and ts.calls  # the SDK still runs
    with pytest.raises(TypeSafeAPIError):
        ts.system_one("boom", {"q": {"text": "x"}}, model="jev-3")
    asyncio.run(mod.AsyncTypeSafeClient().system_one("async state", ["q?"]))
    client.flush()
    llms = sorted(fake.calls_by_op("chat"), key=lambda c: c["started_at"])
    assert len(llms) == 3
    ok, bad, aio = llms
    turns = {c["id"]: c for c in fake.calls_by_op("invoke_agent")}
    assert ok["parent_id"] in turns and turns[ok["parent_id"]]["display_name"] == "invoke_agent typesafe SDK"
    assert kind(ok) == "llm" and ok["inputs"]["model"] == "jev-latest"
    msg = ok["inputs"]["messages"][0]
    assert msg["role"] == "user" and json.loads(msg["content"]) == {"amount": 20, "ticket": "refund please"}
    gen = ok["attributes"]["gen_ai"]
    assert gen["provider"]["name"] == "typesafe" and gen["response"]["id"] == "req_123" and gen["output"]["type"] == "json"
    assert json.loads(ok["attributes"]["typesafe"]["questions"]) == [{"id": 0, "text": "Is it a refund?"}, {"id": 1, "text": "Amount?", "type": "int"}]
    assert json.loads(ok["output"]["choices"][0]["message"]["content"]) == {"q0": "yes", "q1": 3}
    assert ok["summary"]["usage"]["jev-2"]["prompt_tokens"] == 42
    assert ok["attributes"]["weft"]["integration"] == "typesafe"
    # errors: type and status, never the provider's response body
    assert bad["inputs"]["model"] == "jev-3" and bad["error"].endswith("TypeSafeAPIError")
    assert "secret" not in json.dumps(bad) and "secret" not in json.dumps(turns[bad["parent_id"]])
    assert bad["attributes"]["http"]["response"]["status_code"] == 422 and bad["attributes"]["gen_ai"]["response"]["id"] == "req_err"
    assert turns[bad["parent_id"]]["error"]
    assert aio["summary"]["usage"]["jev-2"]["prompt_tokens"] == 5


def test_typesafe_parents_and_capture(client, fake, monkeypatch):
    from weft.integrations import patch_typesafe

    mod = _install_typesafe(monkeypatch)
    assert patch_typesafe()
    ts = mod.TypeSafeClient()

    @weft.trace
    def triage(text):
        return ts.system_one(text, ["urgent?"]).answers

    triage("server down")
    with weft.start_conversation(conversation_id="conv-ts") as conv:
        with conv.start_turn(user_message="hi", agent_name="support") as turn:
            ts.system_one("inside a turn", ["q"])

    @weft.trace
    def handle(text):
        with weft.start_conversation(conversation_id="conv-op") as c2:
            with c2.start_turn(user_message=text, agent_name="helper") as t2:
                ts.system_one("turn inside an op", ["q"])
        return t2.call_id

    helper_turn_id = handle("x")
    monkeypatch.setenv("OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT", "false")
    ts.system_one("private state", ["private question"])
    monkeypatch.delenv("OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT")
    patch_typesafe(capture_content=False)
    ts.system_one("also private", ["q"])
    from weft.integrations import typesafe as tsi

    tsi.set_capture_content(None)
    from weft.integrations import _settings

    _settings.configure({"typesafe": {"enabled": False}})
    try:
        ts.system_one("not traced", ["q"])
    finally:
        _settings.configure(None)
    client.flush()
    [op_call] = fake.calls_by_op("triage")
    [handle_call] = fake.calls_by_op("handle")
    llms = sorted(fake.calls_by_op("chat"), key=lambda c: c["started_at"])
    assert len(llms) == 5
    under_op, under_turn, op_turn, off_env, off_setting = llms
    turns = {c["id"]: c for c in fake.calls_by_op("invoke_agent")}
    # Inside an op: its own "typesafe SDK" turn on a new trace,
    # linked to the op by weft.parent_call.* on both spans, not nested.
    own = turns[under_op["parent_id"]]
    assert own["display_name"] == "invoke_agent typesafe SDK" and own["parent_id"] is None
    assert own["trace_id"] == under_op["trace_id"] != op_call["trace_id"]
    link = {"trace_id": op_call["trace_id"], "id": op_call["id"]}
    assert own["attributes"]["weft"]["parent_call"] == link and under_op["attributes"]["weft"]["parent_call"] == link
    assert own["attributes"]["weft"]["integration"] == "typesafe"
    assert not [c for c in fake.calls.values() if c.get("parent_id") == op_call["id"]], "nothing nests under the op"
    # Inside a running turn: under the turn; no op running, so no link.
    assert under_turn["parent_id"] == turn.call_id and under_turn["attributes"]["gen_ai"]["conversation"]["id"] == "conv-ts"
    assert "parent_call" not in under_turn["attributes"].get("weft", {})
    assert "parent_call" not in turns[under_turn["parent_id"]]["attributes"].get("weft", {})
    # A turn running inside an op: under the turn, linked to the op (the
    # innermost op, not the turn).
    assert op_turn["parent_id"] == helper_turn_id
    assert op_turn["attributes"]["weft"]["parent_call"] == {"trace_id": handle_call["trace_id"], "id": handle_call["id"]}
    for c in (off_env, off_setting):
        assert not c["inputs"].get("messages") and "questions" not in json.dumps(c["attributes"]) and not c.get("output")
    assert "private" not in json.dumps(fake.calls)
    # the op's own turn, support, helper, and the two capture-off calls' own turns
    assert len(turns) == 5
    for c in (off_env, off_setting):
        assert "parent_call" not in c["attributes"].get("weft", {})


# ---------------------------------------------------------------- require_current_call, urls
def test_require_current_call_and_urls(client, fake):
    @weft.trace
    def inner():
        return weft.require_current_span().id

    cid = inner()
    client.flush()
    assert fake.calls_by_op("inner")[0]["id"] == cid
    with pytest.raises(weft.NoCurrentSpanError):
        weft.require_current_span()
    url = weft.urls.agent_conversation_path("test-entity", "test-proj", "conv/1 a")
    assert url == f"{fake.url}/test-entity/test-proj/conversations/conv%2F1%20a"
    assert weft.urls.redirect_call("e", "p", "c1").endswith("/e/p/calls/c1")


# ---------------------------------------------------------------- custom runtimes, threads
def test_register_custom_runtime(client, fake):
    res = client.register_custom_runtime(
        name="my-vllm",
        base_url="https://vllm.internal/v1",
        runtime_ids=["llama-3-8b", weft.CustomRuntimeID(id="qwen2-7b", max_tokens=8192)],
        api_key_secret="VLLM_KEY",
        headers={"X-Team": "ml"},
    )
    [put] = [r for r in fake.requests if r["method"] == "PUT"]
    assert put["path"] == "/api/v1/runtimes/my-vllm" and put["body"]["project_id"] == "test-entity/test-proj"
    assert put["body"]["runtime_ids"] == [{"id": "llama-3-8b", "max_tokens": 4096}, {"id": "qwen2-7b", "max_tokens": 8192}]
    assert put["body"]["api_key_secret"] == "VLLM_KEY" and put["body"]["headers"] == {"X-Team": "ml"}
    assert res.name == "my-vllm" and [r.playground_id for r in res.runtime_ids] == ["my-vllm/llama-3-8b", "my-vllm/qwen2-7b"]
    with pytest.raises(ValueError):
        weft.CustomRuntimeID(id="has space")
    with pytest.raises(ValueError):
        weft.CustomRuntimeID(id="x", max_tokens=0)
    with pytest.raises(TypeError):
        client.register_custom_runtime(name="x", base_url="u", runtime_ids="llama")


def test_threads_query(client, fake):
    @weft.trace
    def turn(i):
        return i

    with weft.thread("t-a"):
        turn(1)
        turn(2)
    with weft.thread("t-b"):
        turn(3)
    client.flush()
    res = client.threads_query(ThreadsQueryReq(project_id="test-proj", sort_by=[SortBy(field="turn_count", direction="desc")], limit=10))
    assert [(t.session_id, t.turn_count) for t in res.threads] == [("t-a", 2), ("t-b", 1)]
    req = [r for r in fake.requests if r["path"] == "/api/v1/spans/sessions"][-1]["body"]
    assert req["project_id"] == "test-entity/test-proj" and req["sort_by"] == [{"field": "turn_count", "direction": "desc"}]
    import datetime

    client.threads_query(limit=1, sortable_datetime_after=datetime.datetime(2026, 1, 1, tzinfo=datetime.timezone.utc))
    req = [r for r in fake.requests if r["path"] == "/api/v1/spans/sessions"][-1]["body"]
    assert req == {"project_id": "test-entity/test-proj", "limit": 1, "offset": 0, "filter": {"started_after": "2026-01-01T00:00:00+00:00"}}
    assert [t.session_id for t in client.threads_query_stream({"limit": 1})] == ["t-a"]


# ---------------------------------------------------------------- WEFT_DEBUG_HTTP, WEFT_DISABLE_WAL_SENDER
def test_debug_http(fake, monkeypatch, capsys):
    monkeypatch.setenv("WEFT_DEBUG_HTTP", "1")
    c = weft.init("test-entity/dbg", server_url=fake.url, api_key="sk-secret", autopatch=False, settings={"print_call_link": False})
    try:
        @weft.trace
        def f(x):
            return x + 1

        f(1)
        c.flush()
        with pytest.raises(weft.WeftHTTPError):
            c._http.post("/no/such/path", {"a": 1})
    finally:
        weft.finish()
    err = capsys.readouterr().err
    assert "Method: POST" in err and f"URL: {fake.url}/api/v1/spans/batch" in err and "Status Code: 200" in err
    assert "Authorization: <Redacted>" in err and "sk-secret" not in err
    assert "----- Response below -----" in err and "Status Code: 404" in err and '"x": 1' in err
    monkeypatch.delenv("WEFT_DEBUG_HTTP")
    weft.init("test-entity/dbg", server_url=fake.url, autopatch=False, settings={"print_call_link": False}).flush()
    weft.finish()
    assert "Method:" not in capsys.readouterr().err


def test_disable_wal_sender(fake, monkeypatch, tmp_path):
    monkeypatch.setenv("WEFT_ENABLE_WAL", "true")
    monkeypatch.setenv("WEFT_WAL_DIR", str(tmp_path))
    monkeypatch.setenv("WEFT_DISABLE_WAL_SENDER", "true")
    c = weft.init("test-entity/wal", server_url=fake.url, autopatch=False, settings={"print_call_link": False})

    @weft.trace
    def g(x):
        return x * 2

    g(2)
    c.flush(5)
    weft.finish()
    assert not [r for r in fake.requests if r["path"] in ("/api/v1/spans/batch", "/api/v1/objects/save")], "nothing is sent"
    assert c._sender.wal is not None and c._sender.wal.count() >= 3  # op object, call start, call end
    monkeypatch.delenv("WEFT_DISABLE_WAL_SENDER")
    c2 = weft.init("test-entity/wal", server_url=fake.url, autopatch=False, settings={"print_call_link": False})
    c2.flush(5)
    weft.finish()
    [call] = fake.calls_by_op("g")
    assert call["output"] == 4, "a later run without the flag sends the WAL"
    assert c2._sender.wal.count() == 0
