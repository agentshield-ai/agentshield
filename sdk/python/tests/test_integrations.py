"""Autopatching with fake ``openai`` / ``anthropic`` modules."""

import asyncio
import sys
import types

import pytest
from pydantic import BaseModel

import weft
from weft.integrations import autopatch, patch_openai_compatible, unpatch_all


class Obj(BaseModel):
    model_config = {"extra": "allow"}


def _completion(model="gpt-4o-mini", content="hello"):
    return Obj(id="c1", object="chat.completion", model=model,
               choices=[{"index": 0, "message": {"role": "assistant", "content": content}, "finish_reason": "stop"}],
               usage={"prompt_tokens": 11, "completion_tokens": 4, "total_tokens": 15})


def _chunks(model="gpt-4o-mini"):
    yield Obj(id="c2", model=model, choices=[{"index": 0, "delta": {"role": "assistant", "content": "Hel"}}])
    yield Obj(id="c2", model=model, choices=[{"index": 0, "delta": {"content": "lo"}}])
    yield Obj(id="c2", model=model, choices=[{"index": 0, "delta": {"tool_calls": [{"index": 0, "id": "t1", "function": {"name": "get", "arguments": "{\"a\""}}]}}])
    yield Obj(id="c2", model=model, choices=[{"index": 0, "delta": {"tool_calls": [{"index": 0, "function": {"arguments": ":1}"}}]}, "finish_reason": "tool_calls"}])
    yield Obj(id="c2", model=model, choices=[], usage={"prompt_tokens": 5, "completion_tokens": 2, "total_tokens": 7})


class FakeStream:
    def __init__(self, items):
        self._items = list(items)
        self.closed = False

    def __iter__(self):
        return iter(self._items)

    def __enter__(self):
        return self

    def __exit__(self, *a):
        self.closed = True

    def close(self):
        self.closed = True


class FakeAsyncStream:
    def __init__(self, items):
        self._items = list(items)

    def __aiter__(self):
        async def gen():
            for i in self._items:
                yield i

        return gen()


@pytest.fixture
def fake_openai(monkeypatch):
    seen = {}

    class Completions:
        def create(self, **kwargs):
            seen["kwargs"] = kwargs
            if kwargs.get("model") == "boom":
                raise RuntimeError("api down")
            if kwargs.get("stream"):
                return FakeStream(_chunks(kwargs["model"]))
            return _completion(kwargs["model"])

    class AsyncCompletions:
        async def create(self, **kwargs):
            seen["kwargs"] = kwargs
            if kwargs.get("stream"):
                return FakeAsyncStream(_chunks(kwargs["model"]))
            return _completion(kwargs["model"])

    class Responses:
        def create(self, **kwargs):
            if kwargs.get("stream"):
                return FakeStream([
                    Obj(type="response.output_text.delta", delta="hi"),
                    Obj(type="response.completed", response={"id": "r1", "model": kwargs["model"], "output_text": "hi",
                                                              "usage": {"input_tokens": 3, "output_tokens": 1, "total_tokens": 4}}),
                ])
            return Obj(id="r1", object="response", model=kwargs["model"], output_text="hi",
                       usage={"input_tokens": 3, "output_tokens": 1, "total_tokens": 4})

    class AsyncResponses:
        async def create(self, **kwargs):
            return Responses().create(**kwargs)

    mods = {}
    for name in ["openai", "openai.resources", "openai.resources.chat", "openai.resources.chat.completions", "openai.resources.responses"]:
        mods[name] = types.ModuleType(name)
    mods["openai.resources.chat.completions"].Completions = Completions
    mods["openai.resources.chat.completions"].AsyncCompletions = AsyncCompletions
    mods["openai.resources.responses"].Responses = Responses
    mods["openai.resources.responses"].AsyncResponses = AsyncResponses
    for name, m in mods.items():
        monkeypatch.setitem(sys.modules, name, m)

    class Client:
        def __init__(self):
            self.chat = types.SimpleNamespace(completions=Completions())
            self.responses = Responses()

    class AsyncClient:
        def __init__(self):
            self.chat = types.SimpleNamespace(completions=AsyncCompletions())

    yield types.SimpleNamespace(Client=Client, AsyncClient=AsyncClient, seen=seen, Completions=Completions)
    unpatch_all()


def test_openai_sync_autopatch(client, fake, fake_openai):
    autopatch()
    autopatch()  # idempotent
    oc = fake_openai.Client()

    @weft.trace
    def agent(q):
        r = oc.chat.completions.create(model="gpt-4o-mini", messages=[{"role": "user", "content": q}], temperature=0)
        return r.choices[0]["message"]["content"]

    assert agent("hi") == "hello"
    client.flush()
    [root] = fake.calls_by_op("agent")
    [c] = fake.calls_by_op("openai.chat.completions.create")
    assert c["parent_id"] == root["id"]
    assert c["name"].startswith("weft:///test-entity/test-proj/function/openai.chat.completions.create:")
    assert c["inputs"] == {"model": "gpt-4o-mini", "messages": [{"role": "user", "content": "hi"}], "temperature": 0}
    assert c["output"]["choices"][0]["message"]["content"] == "hello"
    assert c["summary"]["usage"] == {"gpt-4o-mini": {"requests": 1, "prompt_tokens": 11, "completion_tokens": 4, "total_tokens": 15}}
    # the root only reports its own usage (none)
    assert "usage" not in (root.get("summary") or {})


def test_openai_streaming_sync(client, fake, fake_openai):
    autopatch()
    oc = fake_openai.Client()
    with oc.chat.completions.create(model="gpt-4o", messages=[], stream=True) as stream:
        chunks = list(stream)
    assert len(chunks) == 5
    assert fake_openai.seen["kwargs"]["stream_options"] == {"include_usage": True}
    client.flush()
    [c] = fake.calls_by_op("openai.chat.completions.create")
    msg = c["output"]["choices"][0]["message"]
    assert msg["content"] == "Hello"
    assert msg["tool_calls"] == [{"id": "t1", "type": "function", "function": {"name": "get", "arguments": "{\"a\":1}"}}]
    assert c["output"]["choices"][0]["finish_reason"] == "tool_calls"
    assert c["summary"]["usage"]["gpt-4o"]["total_tokens"] == 7


def test_openai_stream_options_respected(client, fake, fake_openai):
    autopatch()
    oc = fake_openai.Client()
    list(oc.chat.completions.create(model="m", messages=[], stream=True, stream_options={"include_usage": False}))
    assert fake_openai.seen["kwargs"]["stream_options"] == {"include_usage": False}


def test_openai_async_and_async_stream(client, fake, fake_openai):
    autopatch()
    ac = fake_openai.AsyncClient()

    async def main():
        r = await ac.chat.completions.create(model="gpt-4o-mini", messages=[])
        s = await ac.chat.completions.create(model="gpt-4o-mini", messages=[], stream=True)
        parts = [ch async for ch in s]
        return r, parts

    r, parts = asyncio.run(main())
    assert r.model == "gpt-4o-mini" and len(parts) == 5
    client.flush()
    calls = fake.calls_by_op("openai.chat.completions.create")
    assert len(calls) == 2
    assert calls[0]["summary"]["usage"]["gpt-4o-mini"]["total_tokens"] == 15
    assert calls[1]["summary"]["usage"]["gpt-4o-mini"]["total_tokens"] == 7
    assert calls[1]["output"]["choices"][0]["message"]["content"] == "Hello"


def test_openai_error_recorded(client, fake, fake_openai):
    autopatch()
    with pytest.raises(RuntimeError):
        fake_openai.Client().chat.completions.create(model="boom", messages=[])
    client.flush()
    [c] = fake.calls_by_op("openai.chat.completions.create")
    assert c["error"].startswith("RuntimeError: api down")


def test_openai_responses(client, fake, fake_openai):
    autopatch()
    oc = fake_openai.Client()
    oc.responses.create(model="gpt-4.1", input="hi")
    list(oc.responses.create(model="gpt-4.1", input="hi", stream=True))
    client.flush()
    calls = fake.calls_by_op("openai.responses.create")
    assert len(calls) == 2
    for c in calls:
        assert c["summary"]["usage"]["gpt-4.1"] == {"requests": 1, "prompt_tokens": 3, "completion_tokens": 1, "total_tokens": 4}
    assert calls[1]["output"]["output_text"] == "hi"


def test_untraced_when_not_initialised(fake, fake_openai):
    weft.finish()
    autopatch()
    r = fake_openai.Client().chat.completions.create(model="m", messages=[])
    assert r.model == "m"
    assert "stream_options" not in fake_openai.seen["kwargs"]


def test_patch_openai_compatible_instance(client, fake, fake_openai):
    class Comp:
        def create(self, **kw):
            return _completion(kw["model"], "groq!")

    c = types.SimpleNamespace(chat=types.SimpleNamespace(completions=Comp()))
    patch_openai_compatible(c, name="groq")
    patch_openai_compatible(c, name="groq")  # idempotent
    assert c.chat.completions.create(model="llama3", messages=[]).model == "llama3"
    client.flush()
    [call] = fake.calls_by_op("groq.chat.completions.create")
    assert call["summary"]["usage"]["llama3"]["requests"] == 1
    assert call["inputs"] == {"model": "llama3", "messages": []}


@pytest.fixture
def fake_anthropic(monkeypatch):
    def message(model):
        return Obj(id="m1", type="message", role="assistant", model=model,
                   content=[{"type": "text", "text": "Bonjour"}], stop_reason="end_turn",
                   usage={"input_tokens": 9, "output_tokens": 2})

    def events(model):
        return [
            Obj(type="message_start", message={"id": "m2", "type": "message", "role": "assistant", "model": model, "content": [],
                                               "usage": {"input_tokens": 12, "output_tokens": 1}}),
            Obj(type="content_block_start", index=0, content_block={"type": "text", "text": ""}),
            Obj(type="content_block_delta", index=0, delta={"type": "text_delta", "text": "Hi "}),
            Obj(type="content_block_delta", index=0, delta={"type": "text_delta", "text": "there"}),
            Obj(type="content_block_stop", index=0),
            Obj(type="content_block_start", index=1, content_block={"type": "tool_use", "id": "tu1", "name": "calc", "input": {}}),
            Obj(type="content_block_delta", index=1, delta={"type": "input_json_delta", "partial_json": "{\"x\": "}),
            Obj(type="content_block_delta", index=1, delta={"type": "input_json_delta", "partial_json": "2}"}),
            Obj(type="content_block_stop", index=1),
            Obj(type="message_delta", delta={"stop_reason": "tool_use"}, usage={"output_tokens": 6}),
            Obj(type="message_stop"),
        ]

    class StreamCM:
        def __init__(self, model):
            self.current_message_snapshot = message(model)

        def __enter__(self):
            return self

        def __exit__(self, *a):
            return None

        def __iter__(self):
            return iter([])

    class Messages:
        def create(self, **kw):
            if kw.get("stream"):
                return FakeStream(events(kw["model"]))
            return message(kw["model"])

        def stream(self, **kw):
            return StreamCM(kw["model"])

    class AsyncMessages:
        async def create(self, **kw):
            if kw.get("stream"):
                return FakeAsyncStream(events(kw["model"]))
            return message(kw["model"])

    for name in ["anthropic", "anthropic.resources"]:
        monkeypatch.setitem(sys.modules, name, types.ModuleType(name))
    m = types.ModuleType("anthropic.resources.messages")
    m.Messages = Messages
    m.AsyncMessages = AsyncMessages
    monkeypatch.setitem(sys.modules, "anthropic.resources.messages", m)
    yield types.SimpleNamespace(Messages=Messages, AsyncMessages=AsyncMessages)
    unpatch_all()


def test_anthropic_create_sync_async_and_stream(client, fake, fake_anthropic):
    autopatch()
    msgs = fake_anthropic.Messages()
    r = msgs.create(model="claude-sonnet-4-5", max_tokens=10, messages=[{"role": "user", "content": "hi"}])
    assert r.content[0]["text"] == "Bonjour"
    events = list(msgs.create(model="claude-haiku-4-5", max_tokens=10, messages=[], stream=True))
    assert len(events) == 11

    async def main():
        am = fake_anthropic.AsyncMessages()
        await am.create(model="claude-opus-4-5", max_tokens=5, messages=[])
        s = await am.create(model="claude-opus-4-5", max_tokens=5, messages=[], stream=True)
        return [e async for e in s]

    assert len(asyncio.run(main())) == 11
    with msgs.stream(model="claude-sonnet-4-5", max_tokens=5, messages=[]) as st:
        list(st)
    client.flush()
    calls = fake.calls_by_op("anthropic.messages.create")
    assert len(calls) == 4
    assert calls[0]["summary"]["usage"] == {"claude-sonnet-4-5": {"requests": 1, "prompt_tokens": 9, "completion_tokens": 2, "total_tokens": 11}}
    streamed = calls[1]["output"]
    assert streamed["content"][0] == {"type": "text", "text": "Hi there"}
    assert streamed["content"][1]["input"] == {"x": 2}
    assert streamed["stop_reason"] == "tool_use"
    assert calls[1]["summary"]["usage"]["claude-haiku-4-5"] == {"requests": 1, "prompt_tokens": 12, "completion_tokens": 6, "total_tokens": 18}
    assert calls[3]["summary"]["usage"]["claude-opus-4-5"]["completion_tokens"] == 6
    [sc] = fake.calls_by_op("anthropic.messages.stream")
    assert sc["output"]["content"][0]["text"] == "Bonjour"
    assert sc["summary"]["usage"]["claude-sonnet-4-5"]["prompt_tokens"] == 9


def test_init_autopatch_flag(fake, fake_openai):
    weft.init("p", server_url=fake.url, autopatch=True)
    try:
        assert getattr(fake_openai.Completions.create, "__weft_patched__", False)
    finally:
        weft.finish()


def test_autopatch_without_libraries_is_noop(monkeypatch):
    monkeypatch.setitem(sys.modules, "openai", None)
    monkeypatch.setitem(sys.modules, "anthropic", None)
    autopatch()  # must not raise
