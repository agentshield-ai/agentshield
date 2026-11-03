"""Live-server tests: a two-agent team session with a hand-off and reasoning,
from the conversation API and from real framework runs, checked through the
native API (/api/v1/sessions/{id}, /graph, /reasoning, /api/v1/teams).

Skipped unless WEFT_TEST_SERVER_URL is set.
"""

import json
import os
import time
import urllib.parse
import urllib.request
import uuid

import pytest

import weft

URL = os.environ.get("WEFT_TEST_SERVER_URL")
pytestmark = pytest.mark.skipif(not URL, reason="WEFT_TEST_SERVER_URL not set")


@pytest.fixture
def live():
    project = f"it/teams-{uuid.uuid4().hex[:8]}"
    c = weft.init(project, server_url=URL, api_key=os.environ.get("WEFT_TEST_API_KEY"), autopatch=False, settings={"print_call_link": False})
    yield c
    weft.flush(10)
    weft.finish()
    from weft.integrations import unpatch_all

    unpatch_all()


def api(c, path, **params):
    q = urllib.parse.urlencode({"project_id": c.project_id, **params})
    req = urllib.request.Request(f"{URL}{path}?{q}")
    key = os.environ.get("WEFT_TEST_API_KEY")
    if key:
        req.add_header("Authorization", f"Bearer {key}")
    with urllib.request.urlopen(req, timeout=10) as r:
        return json.loads(r.read())


def wait_for(fn, timeout=10.0):
    deadline = time.monotonic() + timeout
    last = None
    while time.monotonic() < deadline:
        try:
            last = fn()
            if last:
                return last
        except urllib.error.HTTPError:
            pass
        time.sleep(0.2)
    return last


def session_api(c, sid, want_handoffs=1):
    q = urllib.parse.quote(sid, safe="")
    s = wait_for(lambda: (lambda x: x if x.get("handoff_count", 0) >= want_handoffs else None)(api(c, f"/api/v1/sessions/{q}")))
    assert s, f"session {sid} not found or without hand-offs"
    return s, api(c, f"/api/v1/sessions/{q}/graph"), api(c, f"/api/v1/sessions/{q}/reasoning"), api(c, f"/api/v1/sessions/{q}/timeline")


def edges(graph):
    return {(e["from"], e["to"], e["kind"]) for e in graph["edges"]}


def test_conversation_team_with_handoff_and_reasoning(live):
    sid = f"trip-{uuid.uuid4().hex[:8]}"
    team_id = f"trip-planners-{uuid.uuid4().hex[:6]}"
    with weft.start_conversation(agent_name="orchestrator", conversation_id=sid, team=weft.Team(team_id, "Trip planners")) as conv:
        with conv.start_turn(user_message="Find three facts about Oslo and summarise them") as turn:
            with turn.start_llm(model="claude-sonnet-4", provider_name="anthropic") as llm:
                llm.input_messages = [weft.Message.user("Find three facts about Oslo and summarise them")]
                llm.think("The user wants facts; the researcher agent should gather them.")
                llm.output_messages = [weft.Message.assistant("", tool_calls=[
                    weft.conversation.ToolCallPart(id="toolu_1", name="delegate_research", arguments={"task": "Find three facts about Oslo"})])]
                llm.usage = weft.Usage(input_tokens=200, output_tokens=120)
            with turn.start_tool(name="delegate_research", arguments={"task": "Find three facts about Oslo"}, tool_call_id="toolu_1") as tool:
                with weft.start_handoff("researcher", input="Find three facts about Oslo", kind="delegate") as h:
                    with conv.start_turn(agent_name="researcher", user_message="Find three facts about Oslo") as rt:
                        weft.reasoning("Search the web first, then pick three facts.")
                        with rt.start_tool(name="web_search", arguments={"q": "Oslo facts"}, tool_call_id="call_9") as ws:
                            ws.result = ["Capital of Norway", "On the Oslofjord", "Nobel Peace Prize"]
                        rt.output_messages = [weft.Message.assistant("1. Capital of Norway 2. On the Oslofjord 3. Nobel Peace Prize")]
                    h.output = "1. Capital of Norway 2. On the Oslofjord 3. Nobel Peace Prize"
                tool.result = h.output
            weft.reasoning("Facts are in; the writer agent should summarise.", kind="summary")
            turn.handoff("writer", input="Summarise the three facts")
            with turn.start_subagent(name="writer", model="gpt-4o") as w:
                with w.start_llm(model="gpt-4o", provider_name="openai") as wl:
                    wl.output("Oslo is Norway's capital on the Oslofjord, home of the Nobel Peace Prize.")
            turn.output_messages = [weft.Message.assistant("Oslo is Norway's capital on the Oslofjord, home of the Nobel Peace Prize.")]
    weft.flush(10)

    s, graph, reasoning, timeline = session_api(live, sid, want_handoffs=2)
    assert s["team_id"] == team_id and s["team_name"] == "Trip planners"
    assert s["agents"][:3] == ["orchestrator", "researcher", "writer"]
    assert s["handoff_count"] == 2 and s["reasoning_step_count"] == 3
    assert {("orchestrator", "researcher", "delegate"), ("orchestrator", "writer", "handoff"), ("researcher", "orchestrator", "return")} <= edges(graph)
    steps = {st["text"]: st for st in reasoning["items"]}
    think = steps["The user wants facts; the researcher agent should gather them."]
    assert think["agent_name"] == "orchestrator" and think["kind"] == "thinking" and think["source"] == "output"
    assert think["next_span_id"] == tool.call_id  # the tool call the model asked for (toolu_1)
    research = steps["Search the web first, then pick three facts."]
    assert research["agent_name"] == "researcher" and research["next_span_id"] == ws.call_id
    summary = steps["Facts are in; the writer agent should summarise."]
    assert summary["kind"] == "summary" and summary["agent_name"] == "orchestrator"
    handoff_ids = {it["span_id"] for it in timeline["items"] if it["type"] == "handoff"}
    assert summary["next_span_id"] in handoff_ids
    teams = wait_for(lambda: [t for t in api(live, "/api/v1/teams")["items"] if t["team_id"] == team_id])
    assert teams and teams[0]["name"] == "Trip planners" and teams[0]["session_count"] == 1
    assert {"orchestrator", "researcher", "writer"} <= set(teams[0]["agents"])
    hs = api(live, "/api/v1/handoffs", session_id=sid)["items"]
    assert {(h["from_agent"], h["to_agent"], h["source"]) for h in hs if h["kind"] != "return"} == {
        ("orchestrator", "researcher", "attribute"), ("orchestrator", "writer", "attribute")}


def test_op_based_agents_with_team_handoff_and_reasoning(live):
    """Plain ops with agent attributes: nested runs of another agent are delegations."""
    sid = f"ops-{uuid.uuid4().hex[:8]}"

    @weft.op(kind="agent")
    def researcher(q):
        weft.reasoning("Only one source is needed.")
        return lookup(q)

    @weft.op(kind="tool")
    def lookup(q):
        return f"facts about {q}"

    @weft.op(kind="agent")
    def planner(q):
        weft.reasoning("Ask the researcher.")
        with weft.attributes({"gen_ai.agent.name": "researcher"}):
            return researcher(q)

    with weft.team("ops-team", "Ops team"), weft.attributes({"gen_ai.conversation.id": sid}):
        with weft.attributes({"gen_ai.agent.name": "planner"}):
            planner("Oslo")
    weft.flush(10)
    s, graph, reasoning, _ = session_api(live, sid)
    assert s["team_id"] == "ops-team" and ("planner", "researcher", "delegate") in edges(graph)
    texts = {st["text"]: st for st in reasoning["items"]}
    assert texts["Only one source is needed."]["agent_name"] == "researcher" and texts["Only one source is needed."]["next_span_id"]


def test_openai_agents_team_live(live):
    pytest.importorskip("agents")
    from fake_models import FakeModels
    from test_team_integrations import _agents_handler, run_openai_agents_team

    models = FakeModels(_agents_handler)
    group = f"thread-{uuid.uuid4().hex[:8]}"
    try:
        assert run_openai_agents_team(models.url, group).final_output == "Your refund is on its way."
    finally:
        models.close()
    weft.flush(10)
    s, graph, reasoning, _ = session_api(live, group, want_handoffs=2)
    assert s["team_id"] == "support-desk"
    assert {"triage", "researcher", "billing"} <= set(s["agents"])
    assert {("triage", "researcher", "delegate"), ("triage", "billing", "handoff")} <= edges(graph)
    summaries = [st for st in reasoning["items"] if st["kind"] == "summary"]
    assert {st["text"] for st in summaries} == {"Need facts first: ask the researcher.", "Billing question: hand off.", "Refund is allowed."}
    assert all(st["tokens"] == 64 for st in summaries)  # output_tokens_details.reasoning_tokens
    by_text = {st["text"]: st for st in summaries}
    assert by_text["Need facts first: ask the researcher."]["agent_name"] == "triage"
    assert by_text["Refund is allowed."]["agent_name"] == "billing"


def test_langgraph_team_live(live):
    pytest.importorskip("langgraph")
    from test_team_integrations import run_langgraph_team

    thread = f"lg-{uuid.uuid4().hex[:8]}"
    with weft.team("graph-team", "Graph team"):
        run_langgraph_team(thread)
    weft.flush(10)
    s, graph, _, _ = session_api(live, thread)
    assert s["team_id"] == "graph-team"
    assert ("researcher", "writer", "handoff") in edges(graph)
    hs = api(live, "/api/v1/handoffs", session_id=thread)["items"]
    assert any(h["source"] == "langgraph" and h["from_agent"] == "researcher" and h["to_agent"] == "writer" for h in hs)
