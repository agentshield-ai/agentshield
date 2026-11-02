"""Bedrock Agents, Verdict, CallsIter.to_pandas(), registry linking and W&B
run linkage against a real Weft server. Skipped unless WEFT_TEST_SERVER_URL is
set, e.g.::

    WEFT_TEST_SERVER_URL=http://127.0.0.1:6345 pytest tests/test_parity_gaps_live.py
"""

import json
import os
import time
import urllib.request
import uuid

import pytest

import weft
from weft.integrations import patch_bedrock, patch_verdict, unpatch_all

from test_parity_gaps import MODEL, _AgentRuntime, _install_botocore, _install_verdict, agent_events

URL = os.environ.get("WEFT_TEST_SERVER_URL")
pytestmark = pytest.mark.skipif(not URL, reason="WEFT_TEST_SERVER_URL not set")


@pytest.fixture
def live():
    project = f"it/gaps-{uuid.uuid4().hex[:8]}"
    c = weft.init(project, server_url=URL, api_key=os.environ.get("WEFT_TEST_API_KEY"), autopatch=False,
                  settings={"print_call_link": False})
    yield c
    weft.flush(10)
    weft.finish()
    unpatch_all()


def _post(path, body):
    headers = {"Content-Type": "application/json"}
    if os.environ.get("WEFT_TEST_API_KEY"):
        headers["Authorization"] = f"Bearer {os.environ['WEFT_TEST_API_KEY']}"
    req = urllib.request.Request(URL + path, data=json.dumps(body).encode(), headers=headers, method="POST")
    with urllib.request.urlopen(req, timeout=10) as r:
        return json.loads(r.read() or b"{}")


def _wait(fn, timeout=10.0):
    end = time.time() + timeout
    while True:
        v = fn()
        if v or time.time() > end:
            return v
        time.sleep(0.2)


def test_bedrock_agent_conversation_in_agents_view(live, monkeypatch):
    _install_botocore(monkeypatch, _AgentRuntime)
    assert patch_bedrock()
    sid = f"bedrock-{uuid.uuid4().hex[:8]}"
    events = json.loads(json.dumps(agent_events(), default=lambda b: b.decode()).replace("sess-42", sid))
    for e in events:
        if "chunk" in e:
            e["chunk"]["bytes"] = e["chunk"]["bytes"].encode()
    res = _AgentRuntime(events)._make_api_call("InvokeAgent", {"agentId": "AGENT1", "agentAliasId": "ALIAS1", "sessionId": sid,
                                                                "inputText": "Weather in Oslo?", "enableTrace": True})
    assert sum(1 for _ in res["completion"]) == len(events)
    live.flush()
    [root] = live.get_calls(filter={"op_names": ["bedrock_agent.invoke_agent"]}).to_list()
    assert root.thread_id == sid and root.output["completion"] == "It is 12°C in Oslo; rain tomorrow."
    kids = root.children()
    names = sorted({c.op_base_name for c in kids})
    assert names == ["bedrock_agent.action_group", "bedrock_agent.collaborator", "bedrock_agent.guardrail",
                     "bedrock_agent.knowledge_base", "bedrock_agent.model_invocation"]
    # usage rolls up from the LLM spans (900 input tokens across 5 model invocations)
    root2 = live.get_call(root.id)
    assert root2.summary["usage"][MODEL]["prompt_tokens"] == 900

    chat = _wait(lambda: (lambda r: r if r.get("total_turns") == 1 else None)(
        _post("/agents/conversations/chat", {"project_id": live.project_id, "conversation_id": sid})))
    assert chat, "conversation indexed"
    assert "AGENT1" in chat["agent_names"]
    msgs = chat["turns"][0]["messages"]
    assert msgs[0]["user_message"]["text"] == "Weather in Oslo?"
    tools = [m["tool_call"]["tool_name"] for m in msgs if m["type"] == "tool_call"]
    assert "weather.get_weather" in tools, tools
    agents = _post("/agents/query", {"project_id": live.project_id})["agents"]
    assert any(a["agent_name"] == "AGENT1" for a in agents) and any(a["agent_name"] == "researcher" for a in agents)


def test_verdict_pipeline_live(live, monkeypatch):
    _, vp = _install_verdict(monkeypatch)
    assert patch_verdict()
    out = (vp.Pipeline("Judge") >> vp.JudgeUnit(lambda x: 3) >> vp.JudgeUnit(lambda x: 4)).run("an answer")
    assert out == {"scores": [3, 4]}
    live.flush()
    [pipe] = live.get_calls(filter={"op_names": ["verdict.Judge"]}).to_list()
    units = pipe.children()
    assert sorted(u.output["score"] for u in units) == [3, 4]
    assert all(u.attributes["weft"]["span_kind"] == "scorer" for u in units)


def test_to_pandas_registry_and_wb_run_live(live):
    pd = pytest.importorskip("pandas")

    @weft.op
    def step_fn(x: int) -> int:
        return x * 2

    weft.set_wb_run("run-live", step=5)
    for i in range(3):
        step_fn(i)
    weft.set_wb_run(None)
    live.flush()
    df = live.get_calls(filter={"op_names": ["step_fn"]}, sort_by=[{"field": "started_at", "direction": "asc"}]).to_pandas()
    assert isinstance(df, pd.DataFrame) and len(df) == 3
    assert list(df["output"]) == [0, 2, 4] and set(df["status"]) == {"success"}
    assert set(df["wb_run_id"]) == {f"{live.project_id}/run-live"}
    flat = live.get_calls(filter={"op_names": ["step_fn"]}).to_pandas(flatten=True)
    assert "inputs.x" in flat.columns

    prompt = weft.MessagesPrompt([{"role": "system", "content": "You are {persona}."}])
    weft.publish(prompt, name="persona-prompt")
    reg = f"registry-{uuid.uuid4().hex[:6]}"
    r = weft.link_prompt_to_registry(prompt, target_path=f"{reg}/personas", aliases=["staging"])
    assert r["version_index"] == 0
    ds = weft.Dataset(name="eval-set", rows=[{"q": "hi"}])
    assert weft.link_to_registry(ds, target_path=f"{reg}/datasets")["version_index"] == 0
    cols = {c["name"]: c for c in live.query_registry(reg)}
    assert set(cols) == {"personas", "datasets"}
    v = cols["personas"]["versions"][0]
    assert "staging" in v["aliases"] and v["object_id"] == "persona-prompt" and v["source_project_id"] == live.project_id
    loaded = live.get_from_registry(f"{reg}/personas", "staging")
    assert loaded.format(persona="a pirate")[0]["content"] == "You are a pirate."
