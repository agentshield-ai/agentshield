"""LlamaIndex, CrewAI, smolagents, Instructor, MCP, Claude Agent SDK, Haystack,
AutoGen, Agno, OpenAI Realtime, Vertex AI, Hugging Face, Cerebras, NVIDIA NIM
(OpenAI-compatible), LangGraph, Google ADK, Pydantic AI and GEPA integrations,
against fake modules."""

import asyncio
import inspect
import sys
import types
import uuid
from dataclasses import dataclass, field
from typing import Any, List, Optional

import pytest
from pydantic import BaseModel

import weft
from weft.integrations import (
    patch_agno,
    patch_autogen,
    patch_cerebras,
    patch_claude_agent_sdk,
    patch_crewai,
    patch_gepa,
    patch_google_adk,
    patch_huggingface,
    patch_instructor,
    patch_mcp,
    patch_openai,
    patch_openai_realtime,
    patch_pydantic_ai,
    patch_smolagents,
    patch_vertexai,
    unpatch_all,
)


def _mods(monkeypatch, **mods):
    out = {}
    for name, attrs in mods.items():
        m = types.ModuleType(name)
        for k, v in attrs.items():
            setattr(m, k, v)
            if isinstance(v, type) and v.__module__ in ("tests.test_framework_integrations", __name__):
                try:
                    v.__module__ = name
                except TypeError:
                    pass
        monkeypatch.setitem(sys.modules, name, m)
        out[name] = m
    return out


@pytest.fixture(autouse=True)
def _unpatch():
    yield
    unpatch_all()


@weft.op(name="openai.chat.completions.create", kind="llm")
def provider_call(model="gpt-4o-mini", p=10, c=5):
    """Stands in for a provider SDK call traced by another integration."""
    weft.add_usage(weft.get_current_call(), model, p, c)
    return "ok"


def by_op(fake, name):
    calls = fake.calls_by_op(name)
    assert calls, f"no calls for {name}: {sorted({fake.op_base(c) for c in fake.calls.values()})}"
    return calls


def kind(c):
    return (c.get("attributes") or {}).get("weft", {}).get("span_kind")


def integ(c):
    return (c.get("attributes") or {}).get("weft", {}).get("integration")


def usage(c):
    return (c.get("summary") or {}).get("usage")


# ---------------------------------------------------------------- LlamaIndex
def test_llamaindex_span_handler(client, fake, monkeypatch):
    class BaseSpanHandler:
        pass

    class Dispatcher:
        def __init__(self):
            self.span_handlers = []

        def add_span_handler(self, h):
            self.span_handlers.append(h)

    disp = Dispatcher()
    _mods(monkeypatch, llama_index={}, **{"llama_index.core": {}, "llama_index.core.instrumentation": {"get_dispatcher": lambda name=None: disp},
                                          "llama_index.core.instrumentation.span_handlers": {},
                                          "llama_index.core.instrumentation.span_handlers.base": {"BaseSpanHandler": BaseSpanHandler}})
    import weft.integrations.llamaindex as li

    monkeypatch.setattr(li, "_CLS", None)
    monkeypatch.setattr(li, "_registered", None)
    assert li.patch_llamaindex() and li.patch_llamaindex()
    [h] = disp.span_handlers
    assert isinstance(h, BaseSpanHandler)

    class LLM:
        pass

    class OpenAI(LLM):
        model = "gpt-4o-mini"

    def query(self, str_or_query_bundle):
        pass

    def chat(self, messages, **kwargs):
        pass

    def bound(fn, *a, **kw):
        return inspect.signature(fn).bind(*a, **kw)

    q, r, l1, l2, bad = ("RetrieverQueryEngine.query-" + uuid.uuid4().hex, "VectorIndexRetriever.retrieve-" + uuid.uuid4().hex,
                         "OpenAI.chat-" + uuid.uuid4().hex, "OpenAI.chat-" + uuid.uuid4().hex, "FunctionTool.call-" + uuid.uuid4().hex)
    llm = OpenAI()
    h.new_span(q, bound(query, None, "what is weft?"), instance=object())
    h.new_span(r, bound(query, None, "what is weft?"), instance=object(), parent_span_id=q)
    h.prepare_to_exit_span(r, None, result=[{"text": "doc"}])
    h.new_span(l1, bound(chat, llm, [{"role": "user", "content": "hi"}]), instance=llm, parent_span_id=q)
    resp = types.SimpleNamespace(message={"role": "assistant", "content": "hello"},
                                 raw={"model": "gpt-4o-mini-2024", "usage": {"prompt_tokens": 7, "completion_tokens": 3, "total_tokens": 10}},
                                 additional_kwargs={})
    h.prepare_to_exit_span(l1, None, instance=llm, result=resp)
    # an LLM span whose provider SDK call is traced too: usage is not counted twice
    h.new_span(l2, bound(chat, llm, []), instance=llm, parent_span_id=q)
    provider_call()
    h.prepare_to_exit_span(l2, None, instance=llm, result=resp)
    h.new_span(bad, bound(query, None, "x"), instance=object(), parent_span_id=q)
    h.prepare_to_drop_span(bad, None, err=ValueError("tool failed"))
    h.prepare_to_exit_span(q, None, result="weft is a tracer")
    assert weft.get_current_call() is None
    client.flush()
    [root] = by_op(fake, "llama_index.RetrieverQueryEngine.query")
    assert root["parent_id"] is None and root["output"] == "weft is a tracer" and root["inputs"] == {"str_or_query_bundle": "what is weft?"}
    assert integ(root) == "llamaindex"
    [ret] = by_op(fake, "llama_index.VectorIndexRetriever.retrieve")
    assert ret["parent_id"] == root["id"] and kind(ret) == "retriever"
    l_calls = by_op(fake, "llama_index.OpenAI.chat")
    assert [kind(c) for c in l_calls] == ["llm", "llm"]
    assert usage(l_calls[0]) == {"gpt-4o-mini-2024": {"requests": 1, "prompt_tokens": 7, "completion_tokens": 3, "total_tokens": 10}}
    assert l_calls[0]["inputs"]["model"] == "gpt-4o-mini"
    assert usage(l_calls[1]) is None
    assert by_op(fake, "openai.chat.completions.create")[0]["parent_id"] == l_calls[1]["id"]
    [t] = by_op(fake, "llama_index.FunctionTool.call")
    assert kind(t) == "tool" and t["exception"].startswith("ValueError: tool failed")
    li.unpatch_llamaindex()
    assert disp.span_handlers == []


def test_llamaindex_span_kinds():
    from weft.integrations.llamaindex import span_kind

    assert span_kind("OpenAIEmbedding", "get_text_embedding") == "embedding"
    assert span_kind("SentenceTransformerRerank", "postprocess_nodes") == "reranker"
    assert span_kind("ReActAgent", "chat") == "agent"
    assert span_kind("MyWorkflow", "run") == "workflow"
    assert span_kind("CompactAndRefine", "get_response") == "chain"


# ---------------------------------------------------------------- CrewAI
def test_crewai(client, fake, monkeypatch):
    class LLM:
        def __init__(self, model):
            self.model = model

        def call(self, messages, tools=None):
            provider_call(self.model, 20, 4)  # LiteLLM call made by CrewAI
            return "final answer"

    class BaseTool:
        name = "base"

        def run(self, *args, **kwargs):
            return self._run(*args, **kwargs)

    class SearchTool(BaseTool):
        name = "search"

        def _run(self, query):
            return f"results for {query}"

    class Agent:
        def __init__(self, role, llm):
            self.role, self.llm = role, llm

        def execute_task(self, task, context=None, tools=None):
            SearchTool().run(query=task.description)
            return self.llm.call([{"role": "user", "content": task.description}])

    class TaskOutput:
        def __init__(self, raw, agent):
            self.raw, self.agent, self.description = raw, agent, "d"

    class Task:
        def __init__(self, description, agent):
            self.description, self.agent, self.name = description, agent, None

        def execute_sync(self, agent=None, context=None, tools=None):
            return TaskOutput(self.agent.execute_task(self), self.agent.role)

    class CrewOutput:
        def __init__(self, raw, tasks_output):
            self.raw, self.tasks_output, self.token_usage = raw, tasks_output, None

    class Crew:
        def __init__(self, tasks):
            self.tasks = tasks

        def kickoff(self, inputs=None):
            outs = [t.execute_sync() for t in self.tasks]
            return CrewOutput(outs[-1].raw, outs)

    class Flow:
        def kickoff(self, inputs=None):
            return "flow done"

    _mods(monkeypatch, crewai={"Crew": Crew, "Agent": Agent, "Task": Task, "LLM": LLM},
          **{"crewai.tools": {"BaseTool": BaseTool}, "crewai.flow": {}, "crewai.flow.flow": {"Flow": Flow}})
    assert patch_crewai()
    assert not patch_crewai()  # idempotent
    agent = Agent("Researcher", LLM("gpt-4o"))
    out = Crew([Task("Find weft docs", agent)]).kickoff(inputs={"topic": "weft"})
    assert out.raw == "final answer"
    Flow().kickoff()
    client.flush()
    [crew] = by_op(fake, "crewai.Crew.kickoff")
    assert kind(crew) == "agent" and crew["inputs"]["inputs"] == {"topic": "weft"} and crew["output"]["raw"] == "final answer"
    assert crew["inputs"]["self"] == "Crew"
    [task] = by_op(fake, "crewai.Task.execute_sync")
    assert task["parent_id"] == crew["id"] and task["display_name"] == "Task: Find weft docs" and task["output"]["raw"] == "final answer"
    [ag] = by_op(fake, "crewai.Agent.execute_task")
    assert ag["parent_id"] == task["id"] and ag["display_name"] == "Agent: Researcher" and kind(ag) == "agent"
    [tool] = by_op(fake, "crewai.Tool.run")
    assert tool["parent_id"] == ag["id"] and kind(tool) == "tool" and tool["display_name"] == "Tool: search"
    [llm] = by_op(fake, "crewai.LLM.call")
    assert kind(llm) == "llm" and llm["inputs"]["model"] == "gpt-4o" and usage(llm) is None
    [prov] = by_op(fake, "openai.chat.completions.create")
    assert prov["parent_id"] == llm["id"] and usage(prov)["gpt-4o"]["total_tokens"] == 24
    # integration attributes stay on the integration's own calls
    assert integ(llm) == "crewai" and integ(prov) is None
    assert kind(by_op(fake, "crewai.Flow.kickoff")[0]) == "workflow"


# ---------------------------------------------------------------- smolagents
def test_smolagents(client, fake, monkeypatch):
    @dataclass
    class TokenUsage:
        input_tokens: int
        output_tokens: int

    @dataclass
    class ChatMessage:
        role: str
        content: str
        token_usage: Optional[TokenUsage] = None

    class Model:
        def __init__(self, model_id):
            self.model_id = model_id

        def generate(self, messages, stop_sequences=None):
            raise NotImplementedError

        def __call__(self, *a, **kw):
            return self.generate(*a, **kw)

    class TransformersModel(Model):
        def generate(self, messages, stop_sequences=None):
            return ChatMessage("assistant", "use the tool", TokenUsage(30, 6))

    class OpenAIServerModel(Model):
        def generate(self, messages, stop_sequences=None):
            provider_call(self.model_id, 11, 2)
            return ChatMessage("assistant", "done", TokenUsage(11, 2))

    class Tool:
        name = "tool"

        def __call__(self, *args, **kwargs):
            return self.forward(*args, **kwargs)

    class WebSearch(Tool):
        name = "web_search"

        def forward(self, query):
            return f"found {query}"

    class MultiStepAgent:
        name = None

        def __init__(self, model, tools):
            self.model, self.tools = model, tools

        def step(self, memory_step):
            msg = self.model([{"role": "user", "content": "q"}])
            return self.tools[0](query=msg.content)

        def run(self, task, stream=False):
            self.step(1)
            return "answer"

    class CodeAgent(MultiStepAgent):
        pass

    _mods(monkeypatch, smolagents={"MultiStepAgent": MultiStepAgent, "CodeAgent": CodeAgent, "Model": Model,
                                   "TransformersModel": TransformersModel, "OpenAIServerModel": OpenAIServerModel,
                                   "Tool": Tool, "WebSearch": WebSearch})
    assert patch_smolagents()
    assert CodeAgent(TransformersModel("HuggingFaceTB/SmolLM2"), [WebSearch()]).run("find weft") == "answer"
    CodeAgent(OpenAIServerModel("gpt-4o"), [WebSearch()]).run("again")
    client.flush()
    runs = by_op(fake, "smolagents.Agent.run")
    assert len(runs) == 2 and kind(runs[0]) == "agent" and runs[0]["display_name"] == "CodeAgent" and runs[0]["output"] == "answer"
    steps = by_op(fake, "smolagents.Agent.step")
    assert steps[0]["parent_id"] == runs[0]["id"] and kind(steps[0]) == "chain"
    [tm] = by_op(fake, "smolagents.TransformersModel.generate")
    assert tm["parent_id"] == steps[0]["id"] and kind(tm) == "llm"
    assert usage(tm) == {"HuggingFaceTB/SmolLM2": {"requests": 1, "prompt_tokens": 30, "completion_tokens": 6, "total_tokens": 36}}
    [om] = by_op(fake, "smolagents.OpenAIServerModel.generate")
    assert usage(om) is None  # the nested provider call has it
    tools = by_op(fake, "smolagents.Tool")
    assert tools[0]["display_name"] == "Tool: web_search" and tools[0]["output"] == "found use the tool" and kind(tools[0]) == "tool"


# ---------------------------------------------------------------- Instructor
def test_instructor(client, fake, monkeypatch):
    class User(BaseModel):
        name: str
        age: int

    class Instructor:
        def create(self, response_model, messages, model=None, max_retries=1):
            provider_call(model, 50, 9)
            return response_model(name="Ada", age=36)

        def create_with_completion(self, response_model, messages, model=None):
            return response_model(name="Ada", age=36), {"id": "chatcmpl-1"}

        def create_partial(self, response_model, messages, model=None):
            def gen():
                yield {"name": "A"}
                yield {"name": "Ada", "age": 36}
            return gen()

    class AsyncInstructor:
        async def create(self, response_model, messages, model=None):
            return response_model(name="Bob", age=7)

    _mods(monkeypatch, instructor={"Instructor": Instructor, "AsyncInstructor": AsyncInstructor},
          **{"instructor.client": {"Instructor": Instructor, "AsyncInstructor": AsyncInstructor}})
    assert patch_instructor()
    ic = Instructor()
    assert ic.create(response_model=User, messages=[{"role": "user", "content": "Ada is 36"}], model="gpt-4o").age == 36
    u, comp = ic.create_with_completion(User, [], model="gpt-4o")
    assert u.name == "Ada" and comp["id"] == "chatcmpl-1"
    parts = list(ic.create_partial(User, [], model="gpt-4o"))
    assert parts[-1] == {"name": "Ada", "age": 36}
    assert asyncio.run(AsyncInstructor().create(User, [], model="gpt-4o-mini")).name == "Bob"
    client.flush()
    [c] = by_op(fake, "instructor.Instructor.create")
    assert kind(c) == "llm" and c["inputs"]["response_model"]["name"] == "User"
    assert c["inputs"]["response_model"]["schema"]["properties"]["age"]["type"] == "integer"
    assert c["output"] == {"name": "Ada", "age": 36} and usage(c) is None
    assert usage(by_op(fake, "openai.chat.completions.create")[0])["gpt-4o"]["prompt_tokens"] == 50
    [w] = by_op(fake, "instructor.Instructor.create_with_completion")
    assert w["output"]["response"] == {"name": "Ada", "age": 36} and w["output"]["completion"] == {"id": "chatcmpl-1"}
    [p] = by_op(fake, "instructor.Instructor.create_partial")
    assert p["output"] == {"name": "Ada", "age": 36}  # last partial
    assert by_op(fake, "instructor.AsyncInstructor.create")[0]["output"]["name"] == "Bob"


# ---------------------------------------------------------------- MCP
def test_mcp_client_and_server(client, fake, monkeypatch):
    @weft.op
    def add(a, b):
        return a + b

    class CallToolResult(BaseModel):
        content: List[dict]
        isError: bool = False

    class FastMCP:
        async def call_tool(self, name, arguments):
            return [{"type": "text", "text": str(add(**arguments))}]

        async def list_tools(self):
            return [{"name": "add"}]

        async def read_resource(self, uri):
            return [{"content": "file body"}]

    class ClientSession:
        def __init__(self, server):
            self.server = server

        async def call_tool(self, name, arguments=None):
            content = await self.server.call_tool(name, arguments or {})
            return CallToolResult(content=content)

        async def list_tools(self):
            return {"tools": await self.server.list_tools()}

        async def get_prompt(self, name, arguments=None):
            return {"messages": []}

    _mods(monkeypatch, mcp={}, **{"mcp.client": {}, "mcp.client.session": {"ClientSession": ClientSession},
                                  "mcp.server": {}, "mcp.server.fastmcp": {"FastMCP": FastMCP}})
    assert patch_mcp()

    async def run():
        s = ClientSession(FastMCP())
        r = await s.call_tool("add", {"a": 2, "b": 3})
        await s.list_tools()
        await s.get_prompt("greeting")
        return r

    assert asyncio.run(run()).content[0]["text"] == "5"
    client.flush()
    [cc] = by_op(fake, "mcp.ClientSession.call_tool")
    assert kind(cc) == "tool" and cc["display_name"] == "MCP tool: add" and cc["inputs"]["arguments"] == {"a": 2, "b": 3}
    assert cc["output"]["content"][0]["text"] == "5" and integ(cc) == "mcp"
    [sc] = by_op(fake, "mcp.FastMCP.call_tool")
    assert sc["parent_id"] == cc["id"] and kind(sc) == "tool"
    assert by_op(fake, "add")[0]["parent_id"] == sc["id"]
    [lt] = by_op(fake, "mcp.ClientSession.list_tools")
    assert by_op(fake, "mcp.FastMCP.list_tools")[0]["parent_id"] == lt["id"]
    assert by_op(fake, "mcp.ClientSession.get_prompt")[0]["display_name"] == "MCP prompt: greeting"


def test_mcp_list_operations_opt_out(client, fake, monkeypatch):
    class ClientSession:
        async def list_tools(self):
            return []

        async def call_tool(self, name, arguments=None):
            return {}

    _mods(monkeypatch, mcp={}, **{"mcp.client": {}, "mcp.client.session": {"ClientSession": ClientSession}})
    monkeypatch.setenv("WEFT_MCP_TRACE_LIST_OPERATIONS", "false")
    patch_mcp()
    asyncio.run(ClientSession().list_tools())
    client.flush()
    assert fake.calls_by_op("mcp.ClientSession.list_tools") == []


# ---------------------------------------------------------------- Claude Agent SDK
@dataclass
class TextBlock:
    text: str


@dataclass
class ThinkingBlock:
    thinking: str
    signature: str = ""


@dataclass
class ToolUseBlock:
    id: str
    name: str
    input: dict


@dataclass
class ToolResultBlock:
    tool_use_id: str
    content: Any = None
    is_error: Optional[bool] = None


@dataclass
class AssistantMessage:
    content: list
    model: str
    parent_tool_use_id: Optional[str] = None


@dataclass
class UserMessage:
    content: Any
    parent_tool_use_id: Optional[str] = None


@dataclass
class SystemMessage:
    subtype: str
    data: dict


@dataclass
class ResultMessage:
    subtype: str
    duration_ms: int
    duration_api_ms: int
    is_error: bool
    num_turns: int
    session_id: str
    total_cost_usd: Optional[float] = None
    usage: Optional[dict] = None
    result: Optional[str] = None


MODEL = "claude-sonnet-4-5"


def _conversation():
    return [
        SystemMessage("init", {"tools": ["Bash", "Read", "Task"]}),
        AssistantMessage([ThinkingBlock("list files first"), TextBlock("Let me look."), ToolUseBlock("t1", "Bash", {"command": "ls -la"})], MODEL),
        UserMessage([ToolResultBlock("t1", "README.md\nsrc", False)]),
        AssistantMessage([ToolUseBlock("t2", "Read", {"file_path": "/nope"})], MODEL),
        UserMessage([ToolResultBlock("t2", "File does not exist", True)]),
        AssistantMessage([ToolUseBlock("t3", "Task", {"description": "summarise", "prompt": "summarise src"})], MODEL),
        AssistantMessage([TextBlock("src has one module")], MODEL, parent_tool_use_id="t3"),
        UserMessage([ToolResultBlock("t3", "summary: one module", False)]),
        AssistantMessage([TextBlock("There is a README and src.")], MODEL),
        ResultMessage("success", 1200, 900, False, 4, "sess-1", 0.0123,
                      {"input_tokens": 100, "output_tokens": 40, "cache_read_input_tokens": 1000, "cache_creation_input_tokens": 200},
                      "There is a README and src."),
    ]


@pytest.fixture
def fake_claude_sdk(monkeypatch):
    class InternalClient:
        async def process_query(self, prompt, options, transport=None):
            for m in _conversation():
                yield m

    class ClaudeSDKClient:
        def __init__(self, options=None):
            self.options = options
            self._pending = None

        async def query(self, prompt, session_id="default"):
            self._pending = prompt

        async def receive_response(self):
            for m in _conversation()[-2:]:
                yield m

    async def query(*, prompt, options=None, transport=None):
        async for m in InternalClient().process_query(prompt=prompt, options=options, transport=transport):
            yield m

    msgs = dict(AssistantMessage=AssistantMessage, UserMessage=UserMessage, SystemMessage=SystemMessage, ResultMessage=ResultMessage,
                TextBlock=TextBlock, ThinkingBlock=ThinkingBlock, ToolUseBlock=ToolUseBlock, ToolResultBlock=ToolResultBlock)
    _mods(monkeypatch, claude_agent_sdk={"ClaudeSDKClient": ClaudeSDKClient, "query": query, **msgs},
          **{"claude_agent_sdk._internal": {}, "claude_agent_sdk._internal.client": {"InternalClient": InternalClient}})
    return types.SimpleNamespace(query=query, ClaudeSDKClient=ClaudeSDKClient)


def test_claude_agent_sdk_query(client, fake, fake_claude_sdk):
    assert patch_claude_agent_sdk()

    async def run():
        return [m async for m in fake_claude_sdk.query(prompt="What is in this repo?", options={"model": MODEL, "max_turns": 5})]

    assert len(asyncio.run(run())) == len(_conversation())
    client.flush()
    [root] = by_op(fake, "claude_agent_sdk.query")
    assert kind(root) == "agent" and root["inputs"]["prompt"] == "What is in this repo?" and root["inputs"]["options"] == {"model": MODEL, "max_turns": 5}
    assert root["output"]["result"] == "There is a README and src." and root["output"]["total_cost_usd"] == 0.0123
    assert root["output"]["session_id"] == "sess-1" and root["display_name"] == "What is in this repo?"
    u = usage(root)[MODEL]
    assert u["prompt_tokens"] == 1300 and u["completion_tokens"] == 40 and u["cache_read_input_tokens"] == 1000
    llms = by_op(fake, "claude_agent_sdk.llm")
    assert len(llms) == 5 and all(kind(c) == "llm" and usage(c) is None for c in llms)
    assert llms[0]["output"]["content"][0] == {"thinking": "list files first", "signature": "", "type": "thinking"}
    assert llms[0]["display_name"] == "Let me look."
    [bash] = by_op(fake, "claude_agent_sdk.tool.Bash")
    assert kind(bash) == "tool" and bash["inputs"] == {"command": "ls -la"} and bash["display_name"] == "Bash: ls -la"
    assert bash["output"]["content"] == "README.md\nsrc" and not bash.get("exception") and bash["parent_id"] == root["id"]
    [read] = by_op(fake, "claude_agent_sdk.tool.Read")
    assert read["exception"].startswith("RuntimeError: File does not exist")
    [task] = by_op(fake, "claude_agent_sdk.tool.Task")
    assert kind(task) == "agent"
    sub = [c for c in llms if c["parent_id"] == task["id"]]
    assert len(sub) == 1 and sub[0]["output"]["content"][0]["text"] == "src has one module"


def test_claude_agent_sdk_client_turns(client, fake, fake_claude_sdk):
    assert patch_claude_agent_sdk()

    async def run():
        c = fake_claude_sdk.ClaudeSDKClient(options={"model": MODEL})
        for prompt in ("first question", "second question"):
            await c.query(prompt)
            async for _ in c.receive_response():
                pass

    asyncio.run(run())
    client.flush()
    turns = by_op(fake, "claude_agent_sdk.turn")
    assert [t["inputs"]["prompt"] for t in turns] == ["first question", "second question"]
    assert turns[0]["thread_id"] and turns[0]["thread_id"] == turns[1]["thread_id"]
    assert turns[0]["turn_id"] == turns[0]["id"] and kind(turns[0]) == "agent"
    assert usage(turns[1])[MODEL]["requests"] == 1
    llm = [c for c in by_op(fake, "claude_agent_sdk.llm") if c["parent_id"] == turns[0]["id"]]
    assert llm and llm[0]["thread_id"] == turns[0]["thread_id"]


# ---------------------------------------------------------------- Haystack
def test_haystack_tracer(client, fake, monkeypatch):
    import contextlib

    class Tracer:
        pass

    class ProxyTracer:
        is_content_tracing_enabled = False
        actual_tracer = None

    proxy = ProxyTracer()

    def enable_tracing(t):
        proxy.actual_tracer = t

    _mods(monkeypatch, haystack={}, **{"haystack.tracing": {"Tracer": Tracer, "tracer": proxy, "enable_tracing": enable_tracing,
                                                            "disable_tracing": lambda: setattr(proxy, "actual_tracer", None)}})
    import weft.integrations.haystack as hs

    monkeypatch.setattr(hs, "_CLS", None)
    monkeypatch.setattr(hs, "_registered", None)
    assert hs.patch_haystack()
    t = proxy.actual_tracer
    assert isinstance(t, Tracer) and proxy.is_content_tracing_enabled

    def component(name, ctype, inputs, outputs, nested=False):
        with t.trace("haystack.component.run", tags={"haystack.component.name": name, "haystack.component.type": ctype}) as span:
            span.set_content_tag("haystack.component.input", inputs)
            if nested:
                provider_call("gpt-4o-mini", 9, 9)
            span.set_content_tag("haystack.component.output", outputs)

    gen_out = {"replies": ["Paris"], "meta": [{"model": "gpt-4o-mini", "usage": {"prompt_tokens": 12, "completion_tokens": 2}}]}
    with t.trace("haystack.pipeline.run", tags={"haystack.pipeline.input_data": {"retriever": {"query": "capital?"}}}) as ps:
        assert t.current_span() is ps
        component("retriever", "InMemoryBM25Retriever", {"query": "capital?"}, {"documents": ["Paris is..."]})
        component("llm", "OpenAIGenerator", {"prompt": "capital?"}, gen_out, nested=True)
        component("local_llm", "HuggingFaceLocalGenerator", {"prompt": "capital?"}, gen_out)
        ps.set_tag("haystack.pipeline.output_data", {"llm": {"replies": ["Paris"]}})
    with pytest.raises(RuntimeError):
        with t.trace("haystack.component.run", tags={"haystack.component.name": "x", "haystack.component.type": "Broken"}):
            raise RuntimeError("component failed")
    client.flush()
    [pipe] = by_op(fake, "haystack.pipeline.run")
    assert kind(pipe) == "chain" and pipe["inputs"] == {"retriever": {"query": "capital?"}} and pipe["output"] == {"llm": {"replies": ["Paris"]}}
    [ret] = by_op(fake, "haystack.InMemoryBM25Retriever")
    assert ret["parent_id"] == pipe["id"] and kind(ret) == "retriever" and ret["display_name"] == "retriever" and ret["inputs"] == {"query": "capital?"}
    [gen] = by_op(fake, "haystack.OpenAIGenerator")
    assert kind(gen) == "llm" and usage(gen) is None and by_op(fake, "openai.chat.completions.create")[0]["parent_id"] == gen["id"]
    [loc] = by_op(fake, "haystack.HuggingFaceLocalGenerator")
    assert usage(loc) == {"gpt-4o-mini": {"requests": 1, "prompt_tokens": 12, "completion_tokens": 2, "total_tokens": 14}}
    [broken] = by_op(fake, "haystack.Broken")
    assert broken["exception"].startswith("RuntimeError: component failed")
    hs.unpatch_haystack()
    assert proxy.actual_tracer is None


# ---------------------------------------------------------------- AutoGen
def test_autogen(client, fake, monkeypatch):
    @dataclass
    class RequestUsage:
        prompt_tokens: int
        completion_tokens: int

    @dataclass
    class CreateResult:
        content: str
        usage: RequestUsage
        finish_reason: str = "stop"

    class ChatCompletionClient:
        async def create(self, messages, tools=()):
            raise NotImplementedError

    class LocalClient(ChatCompletionClient):
        def __init__(self):
            self._create_args = {"model": "llama-3.1-8b"}

        async def create(self, messages, tools=()):
            return CreateResult("hi", RequestUsage(17, 3))

    class BaseTool:
        name = "tool"

        async def run_json(self, args, cancellation_token=None):
            return {"ok": args}

    class BaseChatAgent:
        def __init__(self, name, client):
            self.name, self.client = name, client

        async def on_messages(self, messages, cancellation_token=None):
            res = await self.client.create(messages)
            await BaseTool().run_json({"q": 1})
            return {"chat_message": res.content}

        async def run(self, task=None):
            return {"messages": [await self.on_messages([task])]}

    class BaseGroupChat:
        def __init__(self, agents):
            self.agents = agents

        async def run(self, task=None):
            return [await a.run(task=task) for a in self.agents]

    mods = _mods(monkeypatch, autogen_agentchat={}, autogen_core={},
                 **{"autogen_agentchat.agents": {"BaseChatAgent": BaseChatAgent}, "autogen_agentchat.teams": {"BaseGroupChat": BaseGroupChat},
                    "autogen_core.models": {"ChatCompletionClient": ChatCompletionClient}, "autogen_core.tools": {"BaseTool": BaseTool}})
    assert patch_autogen()

    # a model client defined later (autogen_ext imported after weft.init): patched when the patcher re-runs
    class OpenAIChatCompletionClient(ChatCompletionClient):
        def __init__(self):
            self._raw_config = {"model": "gpt-4o"}

        async def create(self, messages, tools=()):
            provider_call("gpt-4o", 5, 5)
            return CreateResult("hello", RequestUsage(5, 5))

    _mods(monkeypatch, **{"autogen_ext.models.openai": {"OpenAIChatCompletionClient": OpenAIChatCompletionClient}})
    patch_autogen()
    team = BaseGroupChat([BaseChatAgent("writer", LocalClient()), BaseChatAgent("critic", OpenAIChatCompletionClient())])
    asyncio.run(team.run(task="write a haiku"))
    client.flush()
    [t] = by_op(fake, "autogen.Team.run")
    assert kind(t) == "agent" and t["inputs"]["task"] == "write a haiku"
    runs = by_op(fake, "autogen.Agent.run")
    assert [r["display_name"] for r in runs] == ["Agent: writer", "Agent: critic"] and runs[0]["parent_id"] == t["id"]
    creates = by_op(fake, "autogen.ChatCompletionClient.create")
    assert len(creates) == 2 and all(kind(c) == "llm" for c in creates)
    assert usage(creates[0]) == {"llama-3.1-8b": {"requests": 1, "prompt_tokens": 17, "completion_tokens": 3, "total_tokens": 20}}
    assert creates[0]["inputs"]["model"] == "llama-3.1-8b"
    assert usage(creates[1]) is None  # provider call underneath
    tools = by_op(fake, "autogen.Tool.run_json")
    assert len(tools) == 2 and kind(tools[0]) == "tool" and tools[0]["output"] == {"ok": {"q": 1}}


# ---------------------------------------------------------------- Agno
def test_agno(client, fake, monkeypatch):
    @dataclass
    class Metrics:
        input_tokens: int
        output_tokens: int

    @dataclass
    class ModelResponse:
        content: str
        response_usage: Optional[Metrics] = None

    class Model:
        def __init__(self, id):
            self.id = id

        def response(self, messages, tools=None):
            return ModelResponse("calling tool", Metrics(40, 8))

    class Function:
        def __init__(self, name):
            self.name = name

    class FunctionCall:
        def __init__(self, function, arguments):
            self.function, self.arguments = function, arguments

        def execute(self):
            return f"{self.function.name}({self.arguments})"

    @dataclass
    class RunResponse:
        content: str
        run_id: str = "run-1"

    class Agent:
        def __init__(self, name, model):
            self.name, self.model = name, model

        def run(self, message, stream=False):
            self.model.response([message])
            FunctionCall(Function("get_weather"), {"city": "Paris"}).execute()
            return RunResponse("sunny")

    _mods(monkeypatch, agno={}, **{"agno.agent": {"Agent": Agent}, "agno.models": {}, "agno.models.base": {"Model": Model},
                                   "agno.tools": {}, "agno.tools.function": {"FunctionCall": FunctionCall}})
    assert patch_agno()
    assert Agent("weather", Model("gpt-4o")).run("weather in Paris?").content == "sunny"
    client.flush()
    [a] = by_op(fake, "agno.Agent.run")
    assert kind(a) == "agent" and a["display_name"] == "Agent: weather" and a["output"] == {"content": "sunny", "run_id": "run-1"}
    [m] = by_op(fake, "agno.Model.response")
    assert m["parent_id"] == a["id"] and usage(m)["gpt-4o"]["total_tokens"] == 48 and m["inputs"]["model"] == "gpt-4o"
    [f] = by_op(fake, "agno.FunctionCall.execute")
    assert f["display_name"] == "Tool: get_weather" and kind(f) == "tool"


# ---------------------------------------------------------------- OpenAI Realtime
def _realtime_events():
    return [
        {"type": "session.created", "session": {"id": "sess_1", "model": "gpt-realtime", "voice": "alloy", "instructions": "Be brief."}},
        {"type": "response.created", "response": {"id": "resp_1", "status": "in_progress"}},
        {"type": "response.output_audio_transcript.delta", "response_id": "resp_1", "delta": "It is "},
        {"type": "response.output_audio_transcript.delta", "response_id": "resp_1", "delta": "sunny."},
        {"type": "response.output_audio.delta", "response_id": "resp_1", "delta": "A" * 400},
        {"type": "response.output_item.done", "response_id": "resp_1",
         "item": {"type": "function_call", "name": "get_weather", "call_id": "call_1", "arguments": '{"city": "Paris"}'}},
        {"type": "response.done", "response": {"id": "resp_1", "status": "completed",
                                               "output": [{"type": "message", "content": [{"type": "output_audio", "transcript": "It is sunny.", "audio": "B" * 200}]}],
                                               "usage": {"total_tokens": 150, "input_tokens": 100, "output_tokens": 50,
                                                         "input_token_details": {"cached_tokens": 20, "audio_tokens": 60},
                                                         "output_token_details": {"audio_tokens": 40}}}},
        {"type": "response.created", "response": {"id": "resp_2"}},
        {"type": "error", "error": {"message": "rate limited"}},
    ]


def test_openai_realtime(client, fake, monkeypatch):
    class RealtimeConnection:
        def __init__(self, events):
            self._events = list(events)
            self.sent = []
            self.closed = False

        def send(self, event):
            self.sent.append(event)

        def recv(self):
            if not self._events:
                raise RuntimeError("connection closed")
            return types.SimpleNamespace(**self._events.pop(0)) if False else self._events.pop(0)

        def __iter__(self):
            while self._events:
                yield self.recv()

        def close(self):
            self.closed = True

    class AsyncRealtimeConnection(RealtimeConnection):
        async def send(self, event):
            self.sent.append(event)

        async def recv(self):
            return self._events.pop(0)

        async def close(self):
            self.closed = True

    _mods(monkeypatch, openai={}, **{"openai.resources": {}, "openai.resources.realtime": {},
                                     "openai.resources.realtime.realtime": {"RealtimeConnection": RealtimeConnection,
                                                                            "AsyncRealtimeConnection": AsyncRealtimeConnection}})
    assert patch_openai_realtime() and not patch_openai_realtime()
    conn = RealtimeConnection(_realtime_events())
    conn.send({"type": "session.update", "session": {"instructions": "Be brief.", "voice": "alloy"}})
    conn.send({"type": "input_audio_buffer.append", "audio": "C" * 800})
    conn.send({"type": "conversation.item.create", "item": {"type": "message", "role": "user", "content": [{"type": "input_text", "text": "Weather in Paris?"}]}})
    conn.send({"type": "response.create"})
    for _ in range(7):
        conn.recv()
    conn.send({"type": "conversation.item.create", "item": {"type": "function_call_output", "call_id": "call_1", "output": '{"temp": 21}'}})
    conn.recv()
    conn.recv()  # error -> the open response fails
    conn.close()
    assert conn.closed
    client.flush()
    [sess] = by_op(fake, "openai.realtime.session")
    assert kind(sess) == "agent" and sess["inputs"]["model"] == "gpt-realtime"
    assert sess["output"]["responses"] == 2 and sess["output"]["server_events"] == 9 and not sess.get("exception")
    r1, r2 = by_op(fake, "openai.realtime.response")
    assert r1["parent_id"] == sess["id"] and kind(r1) == "llm"
    assert r1["inputs"]["input"][0]["content"][0]["text"] == "Weather in Paris?" and r1["inputs"]["input_audio_bytes"] == 600
    assert r1["inputs"]["instructions"] == "Be brief." and r1["inputs"]["voice"] == "alloy"
    assert r1["output"]["text"] == "It is sunny." and r1["output"]["audio_bytes"] == 300
    assert r1["output"]["output"][0]["content"][0] == {"type": "output_audio", "transcript": "It is sunny.", "audio_bytes": 150}
    u = usage(r1)["gpt-realtime"]
    assert u["prompt_tokens"] == 100 and u["completion_tokens"] == 50 and u["cached_tokens"] == 20 and u["input_audio_tokens"] == 60
    assert usage(sess) is None
    assert r2["exception"].startswith("RuntimeError: rate limited")
    assert r2["inputs"]["input"][0]["type"] == "function_call_output"
    [tool] = by_op(fake, "openai.realtime.tool.get_weather")
    assert kind(tool) == "tool" and tool["inputs"] == {"city": "Paris"} and tool["output"] == {"temp": 21} and tool["parent_id"] == sess["id"]

    async def run_async():
        c = AsyncRealtimeConnection(_realtime_events()[:7])
        await c.send({"type": "response.create"})
        for _ in range(7):
            await c.recv()
        await c.close()

    asyncio.run(run_async())
    client.flush()
    assert len(fake.calls_by_op("openai.realtime.session")) == 2
    assert len(fake.calls_by_op("openai.realtime.response")) == 3


def test_realtime_tracer_for_raw_websockets(client, fake):
    from weft.integrations import RealtimeTracer

    rt = RealtimeTracer()
    for e in _realtime_events()[:2]:
        rt.on_server_event(e)
    rt.on_server_event(b'{"type": "response.output_text.delta", "response_id": "resp_1", "delta": "hi"}')
    rt.close()
    client.flush()
    [r] = by_op(fake, "openai.realtime.response")
    assert r["output"]["status"] == "incomplete" and r["output"]["text"] == "hi"


# ---------------------------------------------------------------- Vertex AI
def test_vertexai(client, fake, monkeypatch):
    class Resp:
        def __init__(self, text, usage=None):
            self.d = {"candidates": [{"index": 0, "content": {"role": "model", "parts": [{"text": text}]}}]}
            if usage:
                self.d["usage_metadata"] = {"prompt_token_count": usage[0], "candidates_token_count": usage[1], "total_token_count": sum(usage)}

        def to_dict(self):
            return self.d

    class GenerativeModel:
        def __init__(self, name):
            self._model_name = f"projects/p/locations/us-central1/publishers/google/models/{name}"

        def generate_content(self, contents, *, generation_config=None, stream=False):
            if stream:
                return iter([Resp("Bon"), Resp("jour", (6, 2))])
            return Resp("Bonjour", (6, 2))

        async def generate_content_async(self, contents, *, stream=False):
            return Resp("Hola", (4, 1))

        def start_chat(self):
            return ChatSession(self)

    class ChatSession:
        def __init__(self, model):
            self._model = model

        def send_message(self, content, *, stream=False):
            return self._model.generate_content([content])  # not traced twice

    _mods(monkeypatch, vertexai={}, **{"vertexai.generative_models": {"GenerativeModel": GenerativeModel, "ChatSession": ChatSession}})
    assert patch_vertexai()
    m = GenerativeModel("gemini-1.5-pro")
    assert m.generate_content("hello in French").to_dict()["candidates"][0]["content"]["parts"][0]["text"] == "Bonjour"
    assert len(list(m.generate_content("hi", stream=True))) == 2
    asyncio.run(m.generate_content_async("hola"))
    m.start_chat().send_message("hi")
    client.flush()
    gc = by_op(fake, "vertexai.GenerativeModel.generate_content")
    assert len(gc) == 2 and gc[0]["inputs"]["contents"] == "hello in French" and gc[0]["inputs"]["model"] == "gemini-1.5-pro"
    assert usage(gc[0])["gemini-1.5-pro"]["total_tokens"] == 8 and kind(gc[0]) == "llm"
    assert gc[1]["output"]["text"] == "Bonjour" and usage(gc[1])["gemini-1.5-pro"]["completion_tokens"] == 2
    assert usage(by_op(fake, "vertexai.GenerativeModel.generate_content_async")[0])["gemini-1.5-pro"]["prompt_tokens"] == 4
    [chat] = by_op(fake, "vertexai.ChatSession.send_message")
    assert chat["inputs"]["content"] == "hi" and usage(chat)["gemini-1.5-pro"]["total_tokens"] == 8


# ---------------------------------------------------------------- Hugging Face
def test_huggingface_inference_client(client, fake, monkeypatch):
    def completion(model, text, usage=(9, 3)):
        return {"id": "x", "model": model, "choices": [{"index": 0, "message": {"role": "assistant", "content": text}, "finish_reason": "stop"}],
                "usage": {"prompt_tokens": usage[0], "completion_tokens": usage[1], "total_tokens": sum(usage)}}

    class InferenceClient:
        def __init__(self, model=None, provider=None):
            self.model, self.provider = model, provider

        def chat_completion(self, messages, *, model=None, stream=False, stream_options=None, max_tokens=None):
            if stream:
                return iter([{"model": "m", "choices": [{"index": 0, "delta": {"role": "assistant", "content": "Hel"}}]},
                             {"model": "m", "choices": [{"index": 0, "delta": {"content": "lo"}, "finish_reason": "stop"}],
                              "usage": {"prompt_tokens": 4, "completion_tokens": 2, "total_tokens": 6}}])
            return completion(model or self.model, "Hello")

        def text_generation(self, prompt, *, details=False, stream=False, max_new_tokens=None):
            if stream:
                return iter(["Once", " upon"])
            return {"generated_text": "Once upon", "details": {"generated_tokens": 2, "prefill": [1, 2, 3]}} if details else "Once upon"

        def summarization(self, text, *, model=None):
            return {"summary_text": "short"}

    class AsyncInferenceClient(InferenceClient):
        async def chat_completion(self, messages, *, model=None, stream=False):
            return completion(model or self.model, "Hi", (2, 1))

    _mods(monkeypatch, huggingface_hub={"InferenceClient": InferenceClient, "AsyncInferenceClient": AsyncInferenceClient})
    assert patch_huggingface()
    c = InferenceClient(model="meta-llama/Llama-3.1-8B-Instruct", provider="together")
    assert c.chat_completion([{"role": "user", "content": "hi"}], max_tokens=5)["choices"][0]["message"]["content"] == "Hello"
    assert len(list(c.chat_completion([], stream=True, stream_options={"include_usage": True}))) == 2
    c.text_generation("Once", details=True)
    assert "".join(c.text_generation("Once", stream=True)) == "Once upon"
    c.summarization("long text")
    asyncio.run(AsyncInferenceClient(model="Qwen/Qwen2.5").chat_completion([]))
    client.flush()
    cc = by_op(fake, "huggingface_hub.InferenceClient.chat_completion")
    assert cc[0]["inputs"]["messages"] == [{"role": "user", "content": "hi"}] and cc[0]["inputs"]["provider"] == "together"
    assert usage(cc[0])["meta-llama/Llama-3.1-8B-Instruct"]["total_tokens"] == 12 and kind(cc[0]) == "llm"
    assert cc[1]["output"]["choices"][0]["message"]["content"] == "Hello" and usage(cc[1])["m"]["total_tokens"] == 6
    tg = by_op(fake, "huggingface_hub.InferenceClient.text_generation")
    assert usage(tg[0])["meta-llama/Llama-3.1-8B-Instruct"] == {"requests": 1, "prompt_tokens": 3, "completion_tokens": 2, "total_tokens": 5}
    assert tg[1]["output"] == {"generated_text": "Once upon"}
    assert by_op(fake, "huggingface_hub.InferenceClient.summarization")[0]["output"] == {"summary_text": "short"}
    assert usage(by_op(fake, "huggingface_hub.AsyncInferenceClient.chat_completion")[0])["Qwen/Qwen2.5"]["total_tokens"] == 3


# ---------------------------------------------------------------- Cerebras and NVIDIA NIM
def test_cerebras(client, fake, monkeypatch):
    class CompletionsResource:
        def create(self, *, model, messages, stream=False):
            return {"id": "c", "model": model, "choices": [{"index": 0, "message": {"role": "assistant", "content": "fast"}}],
                    "usage": {"prompt_tokens": 3, "completion_tokens": 1, "total_tokens": 4}, "time_info": {"total_time": 0.01}}

    class AsyncCompletionsResource:
        async def create(self, *, model, messages, stream=False):
            return {"id": "c", "model": model, "choices": [], "usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}}

    _mods(monkeypatch, cerebras={}, **{"cerebras.cloud": {}, "cerebras.cloud.sdk": {}, "cerebras.cloud.sdk.resources": {},
                                       "cerebras.cloud.sdk.resources.chat": {},
                                       "cerebras.cloud.sdk.resources.chat.completions": {"CompletionsResource": CompletionsResource,
                                                                                         "AsyncCompletionsResource": AsyncCompletionsResource}})
    assert patch_cerebras()
    CompletionsResource().create(model="llama3.1-8b", messages=[])
    asyncio.run(AsyncCompletionsResource().create(model="llama3.1-8b", messages=[]))
    client.flush()
    cs = by_op(fake, "cerebras.chat.completions.create")
    assert len(cs) == 2 and usage(cs[0])["llama3.1-8b"]["total_tokens"] == 4 and integ(cs[0]) == "cerebras"


def test_nvidia_nim_via_openai_compatible_patch(client, fake, monkeypatch):
    class Completions:
        def __init__(self, base_url):
            self._client = types.SimpleNamespace(base_url=base_url)

        def create(self, **kw):
            return {"id": "x", "model": kw["model"], "choices": [{"index": 0, "message": {"role": "assistant", "content": "hi"}}],
                    "usage": {"prompt_tokens": 2, "completion_tokens": 1, "total_tokens": 3}}

    _mods(monkeypatch, openai={}, **{"openai.resources": {}, "openai.resources.chat": {}, "openai.resources.chat.completions": {"Completions": Completions}})
    assert patch_openai()
    Completions("https://integrate.api.nvidia.com/v1/").create(model="meta/llama-3.1-8b-instruct", messages=[])
    Completions("http://nim.internal:8000/v1/").create(model="meta/llama-3.1-70b-instruct", messages=[])
    Completions("https://api.openai.com/v1/").create(model="gpt-4o", messages=[])
    client.flush()
    nim, local, oai = by_op(fake, "openai.chat.completions.create")
    assert nim["attributes"]["weft"]["provider"] == "nvidia_nim" and nim["attributes"]["weft"]["base_url"].startswith("https://integrate.api.nvidia.com")
    assert usage(nim)["meta/llama-3.1-8b-instruct"]["total_tokens"] == 3
    assert local["attributes"]["weft"]["provider"] == "openai_compatible"
    assert oai["attributes"]["weft"]["provider"] == "openai" and integ(oai) == "openai"


# ---------------------------------------------------------------- LangGraph (through the LangChain tracer)
def test_langgraph_nodes(client, fake, monkeypatch):
    class BaseCallbackHandler:
        def __init__(self):
            pass

    _mods(monkeypatch, langchain_core={}, **{"langchain_core.callbacks": {"BaseCallbackHandler": BaseCallbackHandler},
                                             "langchain_core.tracers": {},
                                             "langchain_core.tracers.context": {"register_configure_hook": lambda *a: None}})
    import weft.integrations.langchain as lc

    monkeypatch.setattr(lc, "_TRACER_CLS", None)
    monkeypatch.setattr(lc, "_registered", False)
    lc.patch_langchain()
    t = lc.WeftTracer()
    g, n1, n2, llm = (uuid.uuid4() for _ in range(4))
    t.on_chain_start({"id": ["langgraph", "CompiledStateGraph"]}, {"messages": []}, run_id=g, name="LangGraph")
    meta = {"langgraph_node": "agent", "langgraph_step": 1, "langgraph_triggers": ["start:agent"], "thread_id": "t-1"}
    t.on_chain_start({}, {"messages": []}, run_id=n1, parent_run_id=g, name="agent", metadata=meta)
    t.on_chat_model_start({"name": "ChatOpenAI"}, [[]], run_id=llm, parent_run_id=n1, metadata=meta, invocation_params={"model": "gpt-4o"})
    t.on_llm_end(types.SimpleNamespace(llm_output={"token_usage": {"prompt_tokens": 3, "completion_tokens": 1}}, generations=[]), run_id=llm)
    t.on_chain_end({"messages": ["hi"]}, run_id=n1)
    t.on_chain_start({}, {}, run_id=n2, parent_run_id=g, name="tools", metadata={**meta, "langgraph_node": "tools", "langgraph_step": 2})
    t.on_chain_end({}, run_id=n2)
    t.on_chain_end({"messages": ["hi"]}, run_id=g)
    client.flush()
    [graph] = by_op(fake, "langgraph.graph")
    assert kind(graph) == "agent"
    [agent] = by_op(fake, "langgraph.node.agent")
    assert agent["parent_id"] == graph["id"] and agent["display_name"] == "agent"
    assert agent["attributes"]["langgraph"] == {"node": "agent", "step": 1, "triggers": ["start:agent"], "thread_id": "t-1"}
    [m] = by_op(fake, "langchain.ChatModel.ChatOpenAI")
    assert m["parent_id"] == agent["id"] and m["attributes"]["langgraph"]["node"] == "agent"
    assert by_op(fake, "langgraph.node.tools")[0]["attributes"]["langgraph"]["step"] == 2


# ---------------------------------------------------------------- Google ADK, Pydantic AI, GEPA
def test_google_adk_pydantic_ai_and_gepa(client, fake, monkeypatch):
    class BaseAgent:
        def __init__(self, name):
            self.name = name

        async def run_async(self, ctx):
            yield {"author": self.name, "text": "hi"}

    class LlmAgent(BaseAgent):
        async def run_async(self, ctx):
            await FunctionTool("lookup").run_async(args={"q": 1}, tool_context=None)
            yield {"author": self.name, "text": "done"}

    class BaseTool:
        def __init__(self, name):
            self.name = name

        async def run_async(self, *, args, tool_context):
            return {"result": args}

    class FunctionTool(BaseTool):
        async def run_async(self, *, args, tool_context):
            return {"result": args["q"] + 1}

    class Runner:
        def __init__(self, agent):
            self.agent = agent

        async def run_async(self, *, user_id, session_id, new_message):
            async for e in self.agent.run_async(None):
                yield e

    @dataclass
    class Usage:
        input_tokens: int
        output_tokens: int

    @dataclass
    class ModelResponse:
        parts: list
        usage: Usage
        model_name: str

    class Model:
        async def request(self, messages, settings, params):
            raise NotImplementedError

    class TestModel(Model):
        model_name = "test-model"

        async def request(self, messages, settings, params):
            return ModelResponse(["ok"], Usage(13, 2), "test-model")

    @dataclass
    class RunResult:
        output: str

        def usage(self):
            return {"requests": 1}

    class Agent:
        name = "support"

        def __init__(self, model):
            self.model = model

        async def run(self, user_prompt):
            await self.model.request([user_prompt], None, None)
            return RunResult("ok")

    def optimize(seed_candidate, trainset, max_metric_calls=10):
        return {"best": seed_candidate}

    _mods(monkeypatch, google={}, **{"google.adk": {}, "google.adk.runners": {"Runner": Runner},
                                     "google.adk.agents": {}, "google.adk.agents.base_agent": {"BaseAgent": BaseAgent, "LlmAgent": LlmAgent},
                                     "google.adk.tools": {}, "google.adk.tools.base_tool": {"BaseTool": BaseTool, "FunctionTool": FunctionTool},
                                     "pydantic_ai": {"Agent": Agent}, "pydantic_ai.models": {"Model": Model, "TestModel": TestModel},
                                     "gepa": {"optimize": optimize}, "gepa.api": {"optimize": optimize}})
    assert patch_google_adk() and patch_pydantic_ai() and patch_gepa()

    async def run():
        evs = [e async for e in Runner(LlmAgent("helper")).run_async(user_id="u", session_id="s", new_message="hi")]
        await Agent(TestModel()).run("help me")
        return evs

    assert asyncio.run(run())[-1]["text"] == "done"
    import gepa

    assert gepa.optimize({"prompt": "p"}, [])["best"] == {"prompt": "p"}
    client.flush()
    [runner] = by_op(fake, "google_adk.Runner.run")
    [agent] = by_op(fake, "google_adk.Agent.run")
    assert agent["parent_id"] == runner["id"] and agent["display_name"] == "Agent: helper" and kind(agent) == "agent"
    assert agent["output"][-1]["text"] == "done"
    [tool] = by_op(fake, "google_adk.Tool.run")
    assert tool["parent_id"] == agent["id"] and tool["output"] == {"result": 2} and integ(tool) == "google_adk"
    [pa] = by_op(fake, "pydantic_ai.Agent.run")
    assert pa["output"] == {"output": "ok", "usage": {"requests": 1}} and pa["display_name"] == "Agent: support"
    [req] = by_op(fake, "pydantic_ai.Model.request")
    assert req["parent_id"] == pa["id"] and usage(req)["test-model"]["total_tokens"] == 15
    assert by_op(fake, "gepa.optimize")[0]["inputs"]["seed_candidate"] == {"prompt": "p"}
