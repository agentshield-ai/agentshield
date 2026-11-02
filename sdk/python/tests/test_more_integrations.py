"""litellm, google-genai, Bedrock, Mistral, Cohere, Groq, Together, LangChain,
OpenAI Agents SDK and DSPy integrations, against fake modules."""

import asyncio
import io
import json
import sys
import types
import uuid

import pytest
from pydantic import BaseModel

import weft
from weft.integrations import (
    autopatch,
    patch_bedrock,
    patch_cohere,
    patch_google_genai,
    patch_groq,
    patch_litellm,
    patch_mistral,
    patch_together,
    unpatch_all,
)


class Obj(BaseModel):
    model_config = {"extra": "allow"}


def _mods(monkeypatch, **mods):
    for name, attrs in mods.items():
        m = types.ModuleType(name)
        for k, v in attrs.items():
            setattr(m, k, v)
        monkeypatch.setitem(sys.modules, name, m)


@pytest.fixture(autouse=True)
def _unpatch():
    yield
    unpatch_all()


def _chat(model, content="hi", usage=(5, 2)):
    return Obj(id="x", model=model, choices=[{"index": 0, "message": {"role": "assistant", "content": content}, "finish_reason": "stop"}],
               usage={"prompt_tokens": usage[0], "completion_tokens": usage[1], "total_tokens": sum(usage)})


def _chunks(model, usage_key="usage"):
    yield Obj(id="s", model=model, choices=[{"index": 0, "delta": {"role": "assistant", "content": "Hel"}}])
    yield Obj(id="s", model=model, choices=[{"index": 0, "delta": {"content": "lo"}, "finish_reason": "stop"}])
    last = {"prompt_tokens": 3, "completion_tokens": 2, "total_tokens": 5}
    yield Obj(id="s", model=model, choices=[], **({"usage": last} if usage_key == "usage" else {"x_groq": {"usage": last}}))


# ---------------------------------------------------------------- litellm
def test_litellm(client, fake, monkeypatch):
    @weft.op
    def provider_sdk_call():
        return "nested"

    def completion(**kw):
        provider_sdk_call()  # e.g. the openai SDK call made by litellm: not traced again
        return iter(list(_chunks(kw["model"]))) if kw.get("stream") else _chat(kw["model"])

    async def acompletion(**kw):
        return _chat(kw["model"], usage=(1, 1))

    _mods(monkeypatch, litellm={"completion": completion, "acompletion": acompletion})
    patch_litellm()
    patch_litellm()
    import litellm

    assert litellm.completion(model="gpt-4o", messages=[]).model == "gpt-4o"
    assert len(list(litellm.completion(model="claude-3", messages=[], stream=True))) == 3
    asyncio.run(litellm.acompletion(model="gemini/x", messages=[]))
    client.flush()
    calls = fake.calls_by_op("litellm.completion")
    assert len(calls) == 2
    assert calls[0]["summary"]["usage"]["gpt-4o"]["total_tokens"] == 7
    assert calls[1]["output"]["choices"][0]["message"]["content"] == "Hello"
    assert calls[1]["summary"]["usage"]["claude-3"]["total_tokens"] == 5
    assert calls[0]["attributes"]["weft"]["span_kind"] == "llm"
    assert fake.calls_by_op("litellm.acompletion")[0]["summary"]["usage"]["gemini/x"]["requests"] == 1
    assert fake.calls_by_op("provider_sdk_call") == []


# ---------------------------------------------------------------- google genai
def test_google_genai(client, fake, monkeypatch):
    def resp(text, usage=None, version="gemini-2.0-flash-001"):
        d = {"candidates": [{"index": 0, "content": {"role": "model", "parts": [{"text": text}]}, "finish_reason": "STOP"}], "model_version": version}
        if usage:
            d["usage_metadata"] = {"prompt_token_count": usage[0], "candidates_token_count": usage[1], "total_token_count": sum(usage)}
        return Obj(**d)

    class Models:
        def generate_content(self, *, model, contents, config=None):
            return resp("Bonjour", (8, 2))

        def generate_content_stream(self, *, model, contents, config=None):
            yield resp("Bon")
            yield resp("jour", (8, 3))

    class AsyncModels:
        async def generate_content(self, *, model, contents, config=None):
            return resp("Hola", (4, 1))

        async def generate_content_stream(self, *, model, contents, config=None):
            async def gen():
                yield resp("Ho")
                yield resp("la", (4, 2))

            return gen()

    _mods(monkeypatch, google={}, **{"google.genai": {}, "google.genai.models": {"Models": Models, "AsyncModels": AsyncModels}})
    patch_google_genai()
    m = Models()
    assert m.generate_content(model="gemini-2.0-flash", contents="hi").candidates[0]["content"]["parts"][0]["text"] == "Bonjour"
    assert len(list(m.generate_content_stream(model="gemini-2.0-flash", contents="hi"))) == 2

    async def run():
        am = AsyncModels()
        await am.generate_content(model="gemini-2.5-pro", contents="x")
        stream = await am.generate_content_stream(model="gemini-2.5-pro", contents="x")
        return [c async for c in stream]

    assert len(asyncio.run(run())) == 2
    client.flush()
    gc = fake.calls_by_op("google.genai.models.generate_content")
    gs = fake.calls_by_op("google.genai.models.generate_content_stream")
    assert gc[0]["summary"]["usage"] == {"gemini-2.0-flash": {"requests": 1, "prompt_tokens": 8, "completion_tokens": 2, "total_tokens": 10}}
    assert gc[0]["inputs"] == {"model": "gemini-2.0-flash", "contents": "hi"}
    assert gs[0]["output"]["text"] == "Bonjour" and gs[0]["summary"]["usage"]["gemini-2.0-flash"]["completion_tokens"] == 3
    assert gs[1]["output"]["candidates"][0]["content"]["parts"] == [{"text": "Hola"}]
    assert gc[1]["summary"]["usage"]["gemini-2.5-pro"]["total_tokens"] == 5


# ---------------------------------------------------------------- bedrock
def test_bedrock(client, fake, monkeypatch):
    class Body:
        def __init__(self, data):
            self._b = io.BytesIO(data)

        def read(self):
            return self._b.read()

    class BaseClient:
        def __init__(self, service):
            self.meta = types.SimpleNamespace(service_model=types.SimpleNamespace(service_name=service))

        def _make_api_call(self, operation_name, api_params):
            if operation_name == "Converse":
                return {"output": {"message": {"role": "assistant", "content": [{"text": "hi"}]}}, "stopReason": "end_turn",
                        "usage": {"inputTokens": 10, "outputTokens": 4, "totalTokens": 14}, "ResponseMetadata": {"RequestId": "r"}}
            if operation_name == "ConverseStream":
                return {"stream": iter([
                    {"messageStart": {"role": "assistant"}},
                    {"contentBlockDelta": {"delta": {"text": "Hel"}}},
                    {"contentBlockDelta": {"delta": {"text": "lo"}}},
                    {"messageStop": {"stopReason": "end_turn"}},
                    {"metadata": {"usage": {"inputTokens": 7, "outputTokens": 2, "totalTokens": 9}}},
                ])}
            if operation_name == "InvokeModel":
                body = json.dumps({"content": [{"type": "text", "text": "yo"}], "usage": {"input_tokens": 6, "output_tokens": 1}}).encode()
                return {"body": Body(body), "ResponseMetadata": {"HTTPHeaders": {"x-amzn-bedrock-input-token-count": "6", "x-amzn-bedrock-output-token-count": "1"}}}
            if operation_name == "InvokeModelWithResponseStream":
                chunks = [{"type": "content_block_delta", "delta": {"text": "a"}},
                          {"type": "message_stop", "amazon-bedrock-invocationMetrics": {"inputTokenCount": 3, "outputTokenCount": 5}}]
                return {"body": iter([{"chunk": {"bytes": json.dumps(c).encode()}} for c in chunks])}
            return {"other": True}

    _mods(monkeypatch, botocore={}, **{"botocore.client": {"BaseClient": BaseClient}})
    patch_bedrock()
    br = BaseClient("bedrock-runtime")
    model = "anthropic.claude-3-haiku"
    assert br._make_api_call("Converse", {"modelId": model, "messages": []})["stopReason"] == "end_turn"
    assert len(list(br._make_api_call("ConverseStream", {"modelId": model, "messages": []})["stream"])) == 5
    res = br._make_api_call("InvokeModel", {"modelId": model, "body": json.dumps({"prompt": "hi"})})
    assert json.loads(res["body"].read())["content"][0]["text"] == "yo"  # body still readable by the caller
    list(br._make_api_call("InvokeModelWithResponseStream", {"modelId": model, "body": "{}"})["body"])
    BaseClient("s3")._make_api_call("Converse", {})  # other services untouched
    br._make_api_call("ListThings", {})
    client.flush()
    [c] = fake.calls_by_op("bedrock.converse")
    assert c["summary"]["usage"][model] == {"requests": 1, "prompt_tokens": 10, "completion_tokens": 4, "total_tokens": 14}
    assert "ResponseMetadata" not in c["output"]
    [cs] = fake.calls_by_op("bedrock.converse_stream")
    assert cs["output"]["text"] == "Hello" and cs["summary"]["usage"][model]["total_tokens"] == 9
    [im] = fake.calls_by_op("bedrock.invoke_model")
    assert im["inputs"]["body"] == {"prompt": "hi"} and im["output"]["body"]["content"][0]["text"] == "yo"
    assert im["summary"]["usage"][model]["prompt_tokens"] == 6
    [ims] = fake.calls_by_op("bedrock.invoke_model_with_response_stream")
    assert ims["output"]["text"] == "a" and ims["summary"]["usage"][model]["completion_tokens"] == 5
    assert len(fake.calls) == 4


# ---------------------------------------------------------------- mistral / cohere / groq / together
def test_mistral(client, fake, monkeypatch):
    class Chat:
        def complete(self, *, model, messages):
            return _chat(model)

        def stream(self, *, model, messages):
            return iter([Obj(event="chunk", data=c.model_dump()) for c in _chunks(model)])

        async def complete_async(self, *, model, messages):
            return _chat(model)

        async def stream_async(self, *, model, messages):
            async def gen():
                for c in _chunks(model):
                    yield Obj(data=c.model_dump())

            return gen()

    _mods(monkeypatch, mistralai={}, **{"mistralai.chat": {"Chat": Chat}})
    patch_mistral()
    ch = Chat()
    ch.complete(model="mistral-large", messages=[])
    list(ch.stream(model="mistral-small", messages=[]))

    async def run():
        await ch.complete_async(model="mistral-large", messages=[])
        return [x async for x in await ch.stream_async(model="mistral-small", messages=[])]

    asyncio.run(run())
    client.flush()
    comp = fake.calls_by_op("mistralai.chat.complete")
    st = fake.calls_by_op("mistralai.chat.stream")
    assert len(comp) == 2 and len(st) == 2
    assert comp[0]["summary"]["usage"]["mistral-large"]["total_tokens"] == 7
    assert st[0]["output"]["choices"][0]["message"]["content"] == "Hello"
    assert st[1]["summary"]["usage"]["mistral-small"]["total_tokens"] == 5


def test_cohere(client, fake, monkeypatch):
    def events():
        return [Obj(type="message-start", id="m"),
                Obj(type="content-delta", delta={"message": {"content": {"text": "Hi "}}}),
                Obj(type="content-delta", delta={"message": {"content": {"text": "there"}}}),
                Obj(type="message-end", delta={"finish_reason": "COMPLETE", "usage": {"tokens": {"input_tokens": 9, "output_tokens": 2}}})]

    class ClientV2:
        def chat(self, *, model, messages):
            return Obj(id="c", message={"role": "assistant", "content": [{"type": "text", "text": "Hey"}]},
                       usage={"billed_units": {"input_tokens": 5, "output_tokens": 1}, "tokens": {"input_tokens": 6, "output_tokens": 1}})

        def chat_stream(self, *, model, messages):
            yield from events()

    class AsyncClientV2:
        async def chat(self, *, model, messages):
            return ClientV2().chat(model=model, messages=messages)

        async def chat_stream(self, *, model, messages):
            for e in events():
                yield e

    _mods(monkeypatch, cohere={"ClientV2": ClientV2, "AsyncClientV2": AsyncClientV2})
    patch_cohere()
    c = ClientV2()
    c.chat(model="command-r", messages=[])
    assert len(list(c.chat_stream(model="command-r", messages=[]))) == 4

    async def run():
        await AsyncClientV2().chat(model="command-a", messages=[])
        return [e async for e in AsyncClientV2().chat_stream(model="command-a", messages=[])]

    assert len(asyncio.run(run())) == 4
    client.flush()
    chats = fake.calls_by_op("cohere.chat")
    streams = fake.calls_by_op("cohere.chat_stream")
    assert chats[0]["summary"]["usage"]["command-r"] == {"requests": 1, "prompt_tokens": 6, "completion_tokens": 1, "total_tokens": 7}
    assert streams[0]["output"]["message"]["content"][0]["text"] == "Hi there"
    assert streams[1]["summary"]["usage"]["command-a"]["total_tokens"] == 11


def test_groq_and_together(client, fake, monkeypatch):
    class GroqCompletions:
        def create(self, **kw):
            return iter(list(_chunks(kw["model"], usage_key="x_groq"))) if kw.get("stream") else _chat(kw["model"])

    class AsyncGroqCompletions:
        async def create(self, **kw):
            return _chat(kw["model"])

    class TogetherCompletions:
        def create(self, **kw):
            return _chat(kw["model"], usage=(2, 2))

    _mods(monkeypatch, groq={}, **{"groq.resources": {}, "groq.resources.chat": {},
                                  "groq.resources.chat.completions": {"Completions": GroqCompletions, "AsyncCompletions": AsyncGroqCompletions},
                                  "together": {}, "together.resources": {}, "together.resources.chat": {},
                                  "together.resources.chat.completions": {"ChatCompletions": TogetherCompletions}})
    assert patch_groq() and patch_together()
    GroqCompletions().create(model="llama-3.1-8b", messages=[])
    list(GroqCompletions().create(model="llama-3.1-8b", messages=[], stream=True))
    asyncio.run(AsyncGroqCompletions().create(model="llama-3.3-70b", messages=[]))
    TogetherCompletions().create(model="meta-llama/Llama-3", messages=[])
    client.flush()
    g = fake.calls_by_op("groq.chat.completions.create")
    assert len(g) == 3 and "stream_options" not in g[1]["inputs"]
    assert g[1]["summary"]["usage"]["llama-3.1-8b"]["total_tokens"] == 5  # from x_groq.usage
    assert fake.calls_by_op("together.chat.completions.create")[0]["summary"]["usage"]["meta-llama/Llama-3"]["total_tokens"] == 4


def test_autopatch_with_no_libraries(monkeypatch):
    for name in ["openai", "anthropic", "litellm", "google.genai", "botocore", "mistralai", "cohere", "groq", "together",
                 "langchain_core", "agents", "dspy"]:
        monkeypatch.setitem(sys.modules, name, None)
    autopatch()


# ---------------------------------------------------------------- langchain
@pytest.fixture
def fake_langchain(monkeypatch):
    hooks = []

    class BaseCallbackHandler:
        def __init__(self):
            pass

    _mods(monkeypatch, langchain_core={}, **{"langchain_core.callbacks": {"BaseCallbackHandler": BaseCallbackHandler},
                                             "langchain_core.tracers": {},
                                             "langchain_core.tracers.context": {"register_configure_hook": lambda *a: hooks.append(a)}})
    import weft.integrations.langchain as lc

    monkeypatch.setattr(lc, "_TRACER_CLS", None)
    monkeypatch.setattr(lc, "_registered", False)
    return lc, hooks


def test_langchain_tracer(client, fake, fake_langchain):
    lc, hooks = fake_langchain
    assert lc.patch_langchain()
    assert hooks and hooks[0][1] is True and hooks[0][3] == "WEFT_TRACE_LANGCHAIN"
    tracer_cls = hooks[0][2]
    assert tracer_cls is lc.WeftTracer
    t = lc.WeftTracer()
    chain, llm, tool, retr, llm2 = (uuid.uuid4() for _ in range(5))

    @weft.op
    def helper():
        return "inside the tool"

    @weft.op(name="openai.chat.completions.create")
    def nested_provider_call():
        weft.add_usage(weft.get_current_call(), "gpt-4o", 1, 1)

    t.on_chain_start({"id": ["langchain", "RunnableSequence"]}, {"question": "hi"}, run_id=chain, tags=["t1"])
    t.on_retriever_start({"name": "VectorStore"}, "hi", run_id=retr, parent_run_id=chain)
    t.on_retriever_end([{"page_content": "doc"}], run_id=retr)
    t.on_chat_model_start({"name": "ChatOpenAI"}, [[{"role": "user", "content": "hi"}]], run_id=llm, parent_run_id=chain,
                          invocation_params={"model_name": "gpt-4o-mini"})
    result = types.SimpleNamespace(llm_output={"token_usage": {"prompt_tokens": 12, "completion_tokens": 3, "total_tokens": 15}, "model_name": "gpt-4o-mini"},
                                   generations=[[types.SimpleNamespace(text="hello")]])
    t.on_llm_end(result, run_id=llm)
    t.on_tool_start({"name": "search"}, "query", run_id=tool, parent_run_id=chain)
    helper()  # an op called inside the tool nests under it
    t.on_tool_error(ValueError("tool broke"), run_id=tool)
    # an LLM run whose provider call was traced by another integration: usage not duplicated
    t.on_chat_model_start({"name": "ChatOpenAI"}, [[]], run_id=llm2, parent_run_id=chain, invocation_params={"model": "gpt-4o"})
    nested_provider_call()
    t.on_llm_end(result, run_id=llm2)
    t.on_chain_end({"answer": "hello"}, run_id=chain)
    assert weft.get_current_call() is None
    client.flush()
    [root] = fake.calls_by_op("langchain.Chain.RunnableSequence")
    assert root["parent_id"] is None and root["output"] == {"answer": "hello"}
    assert root["attributes"]["weft"]["span_kind"] == "chain" and root["attributes"]["langchain"]["tags"] == ["t1"]
    kids = {fake.op_base(k): k for k in fake.children(root["id"])}
    assert set(kids) == {"langchain.Retriever.VectorStore", "langchain.ChatModel.ChatOpenAI", "langchain.Tool.search"}
    llms = fake.calls_by_op("langchain.ChatModel.ChatOpenAI")
    assert llms[0]["summary"]["usage"] == {"gpt-4o-mini": {"requests": 1, "prompt_tokens": 12, "completion_tokens": 3, "total_tokens": 15}}
    assert "usage" not in (llms[1].get("summary") or {})
    assert fake.calls_by_op("openai.chat.completions.create")[0]["parent_id"] == llms[1]["id"]
    assert kids["langchain.Tool.search"]["exception"].startswith("ValueError: tool broke")
    assert fake.calls_by_op("helper")[0]["parent_id"] == kids["langchain.Tool.search"]["id"]
    assert kids["langchain.Retriever.VectorStore"]["attributes"]["weft"]["span_kind"] == "retriever"


# ---------------------------------------------------------------- openai agents
def test_openai_agents_processor(client, fake, monkeypatch):
    registered = []

    class TracingProcessor:
        pass

    _mods(monkeypatch, agents={"add_trace_processor": registered.append}, **{"agents.tracing": {"TracingProcessor": TracingProcessor}})
    import weft.integrations.openai_agents as oa

    monkeypatch.setattr(oa, "_registered", False)
    monkeypatch.setattr(oa, "_CLS", None)
    assert oa.patch_openai_agents() and oa.patch_openai_agents()
    assert len(registered) == 1
    p = registered[0]
    assert isinstance(p, TracingProcessor)

    def span(sid, parent, data, error=None):
        return types.SimpleNamespace(span_id=sid, parent_id=parent, trace_id="tr1", span_data=data, error=error)

    trace = types.SimpleNamespace(trace_id="tr1", name="Support flow", group_id="g")
    agent = span("s1", None, types.SimpleNamespace(type="agent", name="Triage", tools=["lookup"], handoffs=[], output_type="str"))
    gen = span("s2", "s1", types.SimpleNamespace(type="generation", input=[{"role": "user", "content": "hi"}], output=[{"role": "assistant", "content": "yo"}],
                                                 model="gpt-4o", model_config={}, usage={"input_tokens": 20, "output_tokens": 5}))
    fn = span("s3", "s1", types.SimpleNamespace(type="function", name="lookup", input='{"id": 1}', output="found"))
    bad = span("s4", "s1", types.SimpleNamespace(type="guardrail", name="pii", triggered=True), error={"message": "Guardrail tripped", "data": {"x": 1}})

    p.on_trace_start(trace)
    p.on_span_start(agent)
    p.on_span_start(gen)
    p.on_span_end(gen)
    p.on_span_start(fn)
    weft.op(lambda: 1, name="tool_impl")()  # nests under the function span
    p.on_span_end(fn)
    p.on_span_start(bad)
    p.on_span_end(bad)
    p.on_span_end(agent)
    p.on_trace_end(trace)
    client.flush()
    [root] = fake.calls_by_op("openai_agents.trace")
    assert root["display_name"] == "Support flow" and root["attributes"]["weft"]["span_kind"] == "agent"
    [a] = fake.calls_by_op("agent.Triage")
    assert a["parent_id"] == root["id"]
    [g] = fake.calls_by_op("openai_agents.generation")
    assert g["parent_id"] == a["id"] and g["summary"]["usage"]["gpt-4o"]["total_tokens"] == 25
    assert g["attributes"]["weft"]["span_kind"] == "llm"
    [f] = fake.calls_by_op("tool.lookup")
    assert f["inputs"] == {"input": '{"id": 1}'} and f["output"] == "found" and f["attributes"]["weft"]["span_kind"] == "tool"
    assert fake.calls_by_op("tool_impl")[0]["parent_id"] == f["id"]
    [gr] = fake.calls_by_op("guardrail.pii")
    assert gr["exception"].startswith("RuntimeError: Guardrail tripped") and gr["output"] == {"triggered": True}


# ---------------------------------------------------------------- dspy
def test_dspy_callback(client, fake, monkeypatch):
    class BaseCallback:
        pass

    class Settings:
        callbacks = []

        def configure(self, **kw):
            for k, v in kw.items():
                setattr(self, k, v)

    settings = Settings()
    _mods(monkeypatch, dspy={"settings": settings}, **{"dspy.utils": {}, "dspy.utils.callback": {"BaseCallback": BaseCallback}})
    import weft.integrations.dspy as wd

    monkeypatch.setattr(wd, "_registered", False)
    monkeypatch.setattr(wd, "_CLS", None)
    assert wd.patch_dspy()
    [cb] = settings.callbacks
    assert isinstance(cb, BaseCallback)

    class ChainOfThought:
        pass

    cb.on_module_start("m1", ChainOfThought(), {"question": "2+2"})
    cb.on_lm_start("l1", types.SimpleNamespace(model="openai/gpt-4o-mini"), {"prompt": "2+2"})
    cb.on_lm_end("l1", ["4"])
    cb.on_tool_start("t1", types.SimpleNamespace(name="calc"), {"expr": "2+2"})
    cb.on_tool_end("t1", None, ValueError("bad expr"))
    cb.on_module_end("m1", {"answer": "4"})
    client.flush()
    [m] = fake.calls_by_op("dspy.ChainOfThought")
    [lm] = fake.calls_by_op("dspy.LM")
    [tl] = fake.calls_by_op("dspy.Tool.calc")
    assert lm["parent_id"] == m["id"] and lm["inputs"]["model"] == "openai/gpt-4o-mini" and lm["output"] == ["4"]
    assert tl["exception"].startswith("ValueError") and m["output"] == {"answer": "4"}
