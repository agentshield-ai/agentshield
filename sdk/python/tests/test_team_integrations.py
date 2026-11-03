"""Reasoning capture, sessions and hand-offs in the integrations.

The installed client libraries (anthropic, openai, openai-agents, langgraph)
run for real against a local fake model API (tests/fake_models.py); the
frameworks that are not installed run as fakes built from their real
call shapes and event formats.
"""

import asyncio
import json
import sys
import types
from dataclasses import dataclass, field
from typing import Any, Optional

import pytest

import weft
from fake_models import (
    FakeModels,
    anthropic_events,
    anthropic_message,
    chat_chunks,
    chat_completion,
    function_call,
    message_item,
    reasoning_item,
    responses_response,
)
from weft.integrations import unpatch_all

THINK = {"type": "thinking", "thinking": "The user asks about Oslo; check the weather tool first.", "signature": "sig-1"}
REDACTED = {"type": "redacted_thinking", "data": "EmwKAhgBEgy3va3pzix/LafPsn4aDFIT2Xlxh0L5L8rLVyIwxtE3rAFBa8cr3qpP" * 4}


@pytest.fixture(autouse=True)
def _unpatch():
    yield
    unpatch_all()


def by_op(fake, name):
    calls = fake.calls_by_op(name)
    assert calls, f"no calls for {name}: {sorted({fake.op_base(c) for c in fake.calls.values()})}"
    return calls


def attr(c, *path):
    v = c.get("attributes") or {}
    for p in path:
        v = v.get(p) if isinstance(v, dict) else None
    return v


@pytest.fixture
def models():
    state = {"handler": None}
    srv = FakeModels(lambda path, body: state["handler"](path, body))
    srv.state = state
    yield srv
    srv.close()


# ---------------------------------------------------------------- Anthropic


def test_anthropic_thinking_and_redacted_thinking(client, fake, models):
    pytest.importorskip("anthropic")
    import anthropic

    from weft.integrations import patch_anthropic

    assert patch_anthropic()
    content = [THINK, REDACTED, {"type": "text", "text": "It is sunny."}]
    models.state["handler"] = lambda path, body: anthropic_events(content) if body.get("stream") else anthropic_message(content)
    c = anthropic.Anthropic(base_url=models.url, api_key="k")
    history = [{"role": "user", "content": "Weather in Oslo?"}, {"role": "assistant", "content": [THINK, REDACTED, {"type": "text", "text": "x"}]},
               {"role": "user", "content": "And tomorrow?"}]
    r = c.messages.create(model="claude-test", max_tokens=2000, messages=history, thinking={"type": "enabled", "budget_tokens": 1024})
    assert r.content[1].type == "redacted_thinking" and r.content[1].data == REDACTED["data"]  # the caller's response is untouched
    with c.messages.stream(model="claude-test", max_tokens=2000, messages=history[:1]) as s:
        s.get_final_message()
    for ev in c.messages.create(model="claude-test", max_tokens=2000, messages=history[:1], stream=True):
        pass
    client.flush()
    calls = by_op(fake, "anthropic.messages.create") + by_op(fake, "anthropic.messages.stream")
    assert len(calls) == 3
    for call in calls:
        blocks = call["output"]["content"]
        assert blocks[0]["type"] == "thinking" and blocks[0]["thinking"] == THINK["thinking"]
        # redacted thinking is kept as redacted, without its encrypted payload
        assert blocks[1]["type"] == "redacted_thinking" and blocks[1]["data"].startswith("sha256:") and len(blocks[1]["data"]) == 31
    # an input copy of the same redacted block digests the same way (the server hides it as a repeat)
    sent = calls[0]["inputs"]["messages"][1]["content"]
    assert sent[1]["data"] == calls[0]["output"]["content"][1]["data"]


def test_anthropic_capture_reasoning_off(fake, models, monkeypatch):
    pytest.importorskip("anthropic")
    import anthropic

    monkeypatch.delenv("WEFT_DISABLED", raising=False)
    weft.init("e/p", server_url=fake.url, autopatch=False, settings={"capture_reasoning": False, "print_call_link": False})
    try:
        from weft.integrations import patch_anthropic

        patch_anthropic()
        content = [THINK, REDACTED, {"type": "text", "text": "It is sunny."}]
        models.state["handler"] = lambda path, body: anthropic_message(content)
        c = anthropic.Anthropic(base_url=models.url, api_key="k")
        c.messages.create(model="claude-test", max_tokens=100,
                          messages=[{"role": "user", "content": "q"}, {"role": "assistant", "content": [THINK, {"type": "text", "text": "a"}]},
                                    {"role": "user", "content": "q2"}])
        weft.flush(5)
        [call] = by_op(fake, "anthropic.messages.create")
        assert call["output"]["content"] == [{"type": "text", "text": "It is sunny.", "citations": None}] or \
            [b["type"] for b in call["output"]["content"]] == ["text"]
        assert call["inputs"]["messages"][1]["content"] == [{"type": "text", "text": "a"}]
        assert call["summary"]["usage"]  # usage is still recorded
    finally:
        weft.finish()


# ---------------------------------------------------------------- OpenAI


def test_openai_chat_reasoning_content(client, fake, models):
    pytest.importorskip("openai")
    import openai

    from weft.integrations import patch_openai

    assert patch_openai()
    models.state["handler"] = lambda path, body: chat_chunks("42", "Let me add the numbers.") if body.get("stream") else chat_completion("42", "Let me add the numbers.")
    c = openai.OpenAI(base_url=models.url + "/v1", api_key="k")
    r = c.chat.completions.create(model="deepseek-reasoner", messages=[{"role": "user", "content": "6*7?"}])
    assert r.choices[0].message.content == "42"
    for _ in c.chat.completions.create(model="deepseek-reasoner", messages=[{"role": "user", "content": "6*7?"}], stream=True):
        pass
    client.flush()
    calls = by_op(fake, "openai.chat.completions.create")
    assert len(calls) == 2
    for call in calls:
        msg = call["output"]["choices"][0]["message"]
        assert msg["reasoning_content"] == "Let me add the numbers." and msg["content"] == "42"
        assert call["summary"]["usage"]["deepseek-reasoner"]["completion_tokens"] == 30


def test_openai_responses_reasoning_items(client, fake, models):
    pytest.importorskip("openai")
    import openai

    from weft.integrations import patch_openai

    assert patch_openai()
    out = [reasoning_item("**Planning** the lookup"), message_item("Done.")]
    models.state["handler"] = lambda path, body: responses_response(out)
    c = openai.OpenAI(base_url=models.url + "/v1", api_key="k")
    c.responses.create(model="o4-mini", input="hi", reasoning={"effort": "low", "summary": "auto"})
    client.flush()
    [call] = by_op(fake, "openai.responses.create")
    item = call["output"]["output"][0]
    assert item["type"] == "reasoning" and item["summary"][0]["text"] == "**Planning** the lookup"
    assert item["encrypted_content"].startswith("sha256:")
    assert call["summary"]["usage"]["o4-mini"]["reasoning_tokens"] == 64
    assert call["inputs"]["reasoning"] == {"effort": "low", "summary": "auto"}  # request settings are not reasoning


# ---------------------------------------------------------------- Gemini and Bedrock (event formats)


def test_gemini_stream_keeps_thought_parts():
    from weft.integrations.google_genai import GenerateContentAccumulator

    acc = GenerateContentAccumulator()
    for chunk in (
        {"candidates": [{"index": 0, "content": {"role": "model", "parts": [{"text": "**Thinking** about ", "thought": True}]}}]},
        {"candidates": [{"index": 0, "content": {"role": "model", "parts": [{"text": "the question", "thought": True, "thought_signature": "c2ln"}]}}]},
        {"candidates": [{"index": 0, "content": {"role": "model", "parts": [{"text": "Paris."}]}, "finish_reason": "STOP"}],
         "usage_metadata": {"prompt_token_count": 5, "candidates_token_count": 2, "thoughts_token_count": 40, "total_token_count": 47}},
    ):
        acc.add(chunk)
    res = acc.result()
    parts = res["candidates"][0]["content"]["parts"]
    assert parts == [{"text": "**Thinking** about the question", "thought": True, "thought_signature": "c2ln"}, {"text": "Paris."}]
    assert res["text"] == "Paris." and res["usage_metadata"]["thoughts_token_count"] == 40


def test_bedrock_converse_reasoning(client, fake):
    from weft.integrations.bedrock import _StreamProxy, _handle
    from weft.integrations._common import LLMSpec, start_call

    res = {"output": {"message": {"role": "assistant", "content": [
        {"reasoningContent": {"reasoningText": {"text": "Compare both options.", "signature": "s1"}}},
        {"reasoningContent": {"redactedContent": b"\x01\x02secret"}},
        {"text": "Option A."}]}}, "stopReason": "end_turn", "usage": {"inputTokens": 10, "outputTokens": 5, "totalTokens": 15},
        "ResponseMetadata": {}}
    call = start_call(client, LLMSpec("bedrock.converse", integration="bedrock"), {"modelId": "anthropic.claude"})
    back = _handle(client, call, "Converse", "anthropic.claude", res)
    assert back["output"]["message"]["content"][0]["reasoningContent"]["reasoningText"]["text"] == "Compare both options."
    events = [
        {"messageStart": {"role": "assistant"}},
        {"contentBlockDelta": {"contentBlockIndex": 0, "delta": {"reasoningContent": {"text": "Think "}}}},
        {"contentBlockDelta": {"contentBlockIndex": 0, "delta": {"reasoningContent": {"text": "twice."}}}},
        {"contentBlockDelta": {"contentBlockIndex": 0, "delta": {"reasoningContent": {"signature": "s2"}}}},
        {"contentBlockDelta": {"contentBlockIndex": 1, "delta": {"text": "Answer."}}},
        {"messageStop": {"stopReason": "end_turn"}},
        {"metadata": {"usage": {"inputTokens": 3, "outputTokens": 4, "totalTokens": 7}}},
    ]
    scall = start_call(client, LLMSpec("bedrock.converse_stream", integration="bedrock"), {"modelId": "anthropic.claude"})
    list(_StreamProxy(events, client, scall, "anthropic.claude", converse=True))
    client.flush()
    out = fake.calls[call.id]["output"]["output"]["message"]["content"]
    assert out[0] == {"type": "thinking", "thinking": "Compare both options.", "signature": "s1"}
    assert out[1]["type"] == "redacted_thinking" and out[1]["data"].startswith("sha256:") and out[2] == {"text": "Option A."}
    sout = fake.calls[scall.id]["output"]
    assert sout["content"] == [{"type": "thinking", "thinking": "Think twice.", "signature": "s2"}, {"type": "text", "text": "Answer."}]


# ---------------------------------------------------------------- OpenAI Agents SDK (real library)


def _agents_handler(path, body):
    instr = body.get("instructions") or ""
    items = body.get("input") or []
    outputs = {i.get("call_id") for i in items if isinstance(i, dict) and i.get("type") == "function_call_output"}
    if "triage" in instr:
        if "c_research" not in outputs:
            return responses_response([reasoning_item("Need facts first: ask the researcher.", 1), function_call("ask_researcher", {"input": "Oslo facts"}, "c_research")])
        return responses_response([reasoning_item("Billing question: hand off.", 2), function_call("transfer_to_billing", {}, "c_handoff")])
    if "researcher" in instr:
        return responses_response([message_item("Oslo is the capital of Norway.", 3)])
    return responses_response([reasoning_item("Refund is allowed.", 4), message_item("Your refund is on its way.", 5)])


def run_openai_agents_team(models_url: str, group_id: str):
    """A triage agent that consults a researcher agent (as a tool) and hands off to billing."""
    import agents
    from agents import Agent, RunConfig, Runner
    from openai import AsyncOpenAI

    from weft.integrations import patch_openai
    from weft.integrations.openai_agents import WeftTracingProcessor

    patch_openai()
    agents.set_trace_processors([WeftTracingProcessor()])
    agents.set_default_openai_client(AsyncOpenAI(base_url=models_url + "/v1", api_key="k"), use_for_tracing=False)
    agents.set_default_openai_api("responses")
    researcher = Agent(name="researcher", instructions="You are the researcher.", model="o4-mini")
    billing = Agent(name="billing", instructions="You are billing.", model="o4-mini")
    triage = Agent(name="triage", instructions="You are triage.", model="o4-mini", handoffs=[billing],
                   tools=[researcher.as_tool(tool_name="ask_researcher", tool_description="Ask the researcher")])
    with weft.team("support-desk", "Support desk"):
        return Runner.run_sync(triage, "Tell me about Oslo, then refund order 7.", run_config=RunConfig(group_id=group_id, workflow_name="Support"))


def test_openai_agents_team(client, fake, models):
    pytest.importorskip("agents")
    models.state["handler"] = _agents_handler
    res = run_openai_agents_team(models.url, "thread-42")
    assert res.final_output == "Your refund is on its way."
    client.flush()
    [root] = by_op(fake, "openai_agents.trace")
    assert attr(root, "gen_ai", "conversation", "id") == "thread-42" and attr(root, "weft", "span_kind") == "workflow"
    assert attr(root, "weft", "team") == {"id": "support-desk", "name": "Support desk"}
    agents_ = {attr(c, "gen_ai", "agent", "name"): c for c in fake.calls.values() if fake.op_base(c).startswith("agent.")}
    assert set(agents_) == {"triage", "researcher", "billing"}
    # the researcher runs inside triage's tool call: a nested invocation of another agent (delegate)
    def up(c, op):
        while c is not None and fake.op_base(c) != op:
            c = fake.calls.get(c.get("parent_id"))
        return c

    tool = up(agents_["researcher"], "tool.ask_researcher")
    assert tool is not None and up(tool, "agent.triage") is agents_["triage"]
    # the runner's task and turn spans are not agents
    assert all(attr(c, "weft", "span_kind") == "chain" for c in by_op(fake, "openai_agents.turn") + by_op(fake, "openai_agents.task"))
    [h] = by_op(fake, "openai_agents.handoff")
    assert attr(h, "weft", "handoff") == {"kind": "handoff", "from_agent": "triage", "to_agent": "billing"}
    assert h["display_name"] == "handoff to billing" and attr(h, "weft", "span_kind") == "handoff"
    # reasoning: on the nested openai.responses.create calls only, not repeated on the framework's response spans
    reqs = by_op(fake, "openai.responses.create")
    summaries = [i["summary"][0]["text"] for r in reqs for i in r["output"]["output"] if i["type"] == "reasoning"]
    assert sorted(summaries) == sorted(["Need facts first: ask the researcher.", "Billing question: hand off.", "Refund is allowed."])
    for r in by_op(fake, "openai_agents.response"):
        assert "reasoning" not in json.dumps(r["output"]["output"] if isinstance(r["output"], dict) else r["output"]).replace('"reasoning": null', "")


# ---------------------------------------------------------------- LangGraph (real library)


def run_langgraph_team(thread_id: str):
    """researcher -> writer, two agent nodes (each runs a chat model) of one graph."""
    from typing import TypedDict

    from langchain_core.language_models.fake_chat_models import FakeListChatModel
    from langgraph.checkpoint.memory import MemorySaver
    from langgraph.graph import END, START, StateGraph

    from weft.integrations.langchain import WeftTracer

    class State(TypedDict):
        topic: str
        notes: str
        draft: str

    research_llm = FakeListChatModel(responses=["Oslo: capital of Norway."])
    write_llm = FakeListChatModel(responses=["Oslo is Norway's capital."])

    def researcher(state):
        return {"notes": research_llm.invoke(f"research {state['topic']}").content}

    def writer(state):
        return {"draft": write_llm.invoke(f"write about {state['notes']}").content}

    g = StateGraph(State)
    g.add_node("researcher", researcher)
    g.add_node("writer", writer)
    g.add_edge(START, "researcher")
    g.add_edge("researcher", "writer")
    g.add_edge("writer", END)
    app = g.compile(checkpointer=MemorySaver())
    return app.invoke({"topic": "Oslo"}, config={"configurable": {"thread_id": thread_id}, "callbacks": [WeftTracer()]})


def test_langgraph_thread_and_agent_nodes(client, fake):
    pytest.importorskip("langgraph")
    out = run_langgraph_team("lg-thread-1")
    assert out["draft"] == "Oslo is Norway's capital."
    client.flush()
    [root] = by_op(fake, "langgraph.graph")
    assert attr(root, "gen_ai", "conversation", "id") == "lg-thread-1" and attr(root, "weft", "span_kind") == "workflow"
    nodes = {attr(c, "langgraph", "node"): c for c in fake.calls.values() if fake.op_base(c).startswith("langgraph.node.")}
    assert set(nodes) == {"researcher", "writer"}
    assert nodes["researcher"]["parent_id"] == root["id"] == nodes["writer"]["parent_id"]
    assert attr(nodes["researcher"], "langgraph", "step") < attr(nodes["writer"], "langgraph", "step")
    assert {attr(n, "gen_ai", "agent", "name") for n in nodes.values()} == {"researcher", "writer"}
    llms = [c for c in fake.calls.values() if attr(c, "weft", "span_kind") == "llm"]
    assert {fake.calls[c["parent_id"]]["id"] for c in llms} == {nodes["researcher"]["id"], nodes["writer"]["id"]}


# ---------------------------------------------------------------- frameworks not installed (fakes)


def _mods(monkeypatch, **mods):
    for name, attrs in mods.items():
        m = types.ModuleType(name)
        for k, v in attrs.items():
            setattr(m, k, v)
        monkeypatch.setitem(sys.modules, name, m)


def test_crewai_delegation(client, fake, monkeypatch):
    from weft.integrations import patch_crewai

    class BaseTool:
        name = "base"

        def run(self, *args, **kwargs):
            return self._run(*args, **kwargs)

    class Agent:
        def __init__(self, role):
            self.role = role

        def execute_task(self, task, context=None, tools=None):
            if self.role == "Editor":
                return DelegateWorkTool().run(task="Find three facts", context="Oslo", coworker="Researcher")
            return "three facts"

    class DelegateWorkTool(BaseTool):
        name = "Delegate work to coworker"

        def _run(self, task, context, coworker):
            return RESEARCHER.execute_task(types.SimpleNamespace(description=task))

    RESEARCHER = Agent("Researcher")

    class Crew:
        def __init__(self, name="crew"):
            self.name, self.id = name, "3f1e7c2a-crew"

        def kickoff(self, inputs=None):
            return Agent("Editor").execute_task(types.SimpleNamespace(description="write"))

    _mods(monkeypatch, crewai={"Crew": Crew, "Agent": Agent}, **{"crewai.tools": {"BaseTool": BaseTool}})
    assert patch_crewai()
    assert Crew(name="newsroom").kickoff() == "three facts"
    client.flush()
    [crew] = by_op(fake, "crewai.Crew.kickoff")
    assert attr(crew, "weft", "team") == {"id": "newsroom", "name": "newsroom"} and attr(crew, "gen_ai", "conversation", "id") == "3f1e7c2a-crew"
    agents_ = {attr(c, "gen_ai", "agent", "name"): c for c in by_op(fake, "crewai.Agent.execute_task")}
    assert set(agents_) == {"Editor", "Researcher"}
    [tool] = by_op(fake, "crewai.Tool.run")
    # the server reads this tool and its coworker argument as a delegation to Researcher
    assert attr(tool, "gen_ai", "tool", "name") == "Delegate work to coworker" and tool["inputs"]["coworker"] == "Researcher"
    assert agents_["Researcher"]["parent_id"] == tool["id"] and tool["parent_id"] == agents_["Editor"]["id"]


def test_google_adk_transfer_and_session(client, fake, monkeypatch):
    from weft.integrations import patch_google_adk

    class BaseTool:
        def __init__(self, name):
            self.name = name

        async def run_async(self, *, args, tool_context):
            return None

    class TransferTool(BaseTool):
        async def run_async(self, *, args, tool_context):
            return None

    class BaseAgent:
        def __init__(self, name, sub_agents=()):
            self.name, self.sub_agents = name, list(sub_agents)

        async def run_async(self, ctx):
            if self.sub_agents:
                await TransferTool("transfer_to_agent").run_async(args={"agent_name": "booker"}, tool_context=types.SimpleNamespace(function_call_id="adk-fc-1"))
                async for e in self.sub_agents[0].run_async(ctx):
                    yield e
            else:
                yield {"author": self.name, "text": "booked"}

    class Runner:
        def __init__(self, agent):
            self.agent = agent

        async def run_async(self, *, user_id, session_id, new_message):
            async for e in self.agent.run_async(None):
                yield e

    _mods(monkeypatch, google={}, **{"google.adk": {}, "google.adk.runners": {"Runner": Runner}, "google.adk.agents": {},
                                     "google.adk.agents.base_agent": {"BaseAgent": BaseAgent}, "google.adk.tools": {},
                                     "google.adk.tools.base_tool": {"BaseTool": BaseTool, "TransferTool": TransferTool}})
    assert patch_google_adk()

    async def run():
        root = BaseAgent("coordinator", [BaseAgent("booker")])
        return [e async for e in Runner(root).run_async(user_id="u", session_id="adk-session-9", new_message="book a flight")]

    assert asyncio.run(run())[-1]["text"] == "booked"
    client.flush()
    [runner] = by_op(fake, "google_adk.Runner.run")
    assert attr(runner, "gen_ai", "conversation", "id") == "adk-session-9" and attr(runner, "weft", "span_kind") == "workflow"
    names = {attr(c, "gen_ai", "agent", "name"): c for c in by_op(fake, "google_adk.Agent.run")}
    assert set(names) == {"coordinator", "booker"} and names["booker"]["parent_id"] == names["coordinator"]["id"]
    [tool] = by_op(fake, "google_adk.Tool.run")
    assert attr(tool, "gen_ai", "tool") == {"name": "transfer_to_agent", "call": {"id": "adk-fc-1"}}
    assert tool["inputs"]["args"] == {"agent_name": "booker"}


def test_autogen_swarm_handoff_and_team(client, fake, monkeypatch):
    from weft.integrations import patch_autogen

    class BaseTool:
        def __init__(self, name):
            self.name = name

        async def run_json(self, args, cancellation_token=None, call_id=None):
            return {"ok": True}

    class BaseChatAgent:
        def __init__(self, name):
            self.name = name

        async def on_messages(self, messages, cancellation_token=None):
            if self.name == "travel_agent":
                await BaseTool("transfer_to_flights_refunder").run_json({}, None, call_id="call_h1")
            return {"chat_message": "ok"}

    class BaseGroupChat:
        def __init__(self, agents, name=None):
            self.agents, self.name = agents, name or type(self).__name__

        async def run(self, task=None):
            return [await a.on_messages([task]) for a in self.agents]

    _mods(monkeypatch, autogen_agentchat={}, autogen_core={}, **{"autogen_agentchat.agents": {"BaseChatAgent": BaseChatAgent},
                                                                   "autogen_agentchat.teams": {"BaseGroupChat": BaseGroupChat},
                                                                   "autogen_core.tools": {"BaseTool": BaseTool}})
    assert patch_autogen()
    asyncio.run(BaseGroupChat([BaseChatAgent("travel_agent"), BaseChatAgent("flights_refunder")], name="travel_desk").run(task="refund"))
    asyncio.run(BaseGroupChat([BaseChatAgent("solo")]).run(task="x"))
    client.flush()
    teams = by_op(fake, "autogen.Team.run")
    assert [attr(t, "weft", "team") for t in teams] == [{"id": "travel_desk", "name": "travel_desk"}, None]
    [tool] = by_op(fake, "autogen.Tool.run_json")
    assert attr(tool, "weft", "handoff") == {"to_agent": "flights_refunder", "kind": "handoff"}
    assert attr(tool, "gen_ai", "tool") == {"name": "transfer_to_flights_refunder", "call": {"id": "call_h1"}}
    assert {attr(c, "gen_ai", "agent", "name") for c in by_op(fake, "autogen.Agent.on_messages")} == {"travel_agent", "flights_refunder", "solo"}


def test_pydantic_ai_agent_delegation(client, fake, monkeypatch):
    from weft.integrations import patch_pydantic_ai

    class Agent:
        def __init__(self, name, delegate=None):
            self.name, self.delegate = name, delegate

        async def run(self, user_prompt):
            if self.delegate is not None:
                return await FunctionToolset().call_tool("ask_joker", {"q": user_prompt}, types.SimpleNamespace(tool_call_id="pai-tc-1"), None,
                                                         agent=self.delegate)
            return "a joke"

    class FunctionToolset:
        async def call_tool(self, name, tool_args, ctx, tool, agent=None):
            return await agent.run(tool_args["q"])

    _mods(monkeypatch, pydantic_ai={"Agent": Agent}, **{"pydantic_ai.toolsets": {}, "pydantic_ai.toolsets.function": {"FunctionToolset": FunctionToolset}})
    assert patch_pydantic_ai()
    assert asyncio.run(Agent("selector", Agent("joker")).run("tell a joke")) == "a joke"
    client.flush()
    runs = {attr(c, "gen_ai", "agent", "name"): c for c in by_op(fake, "pydantic_ai.Agent.run")}
    [tool] = by_op(fake, "pydantic_ai.Tool.call")
    assert set(runs) == {"selector", "joker"}
    assert runs["joker"]["parent_id"] == tool["id"] and tool["parent_id"] == runs["selector"]["id"]
    assert attr(tool, "gen_ai", "tool") == {"name": "ask_joker", "call": {"id": "pai-tc-1"}}
