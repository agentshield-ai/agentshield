"""Teams, hand-offs, reasoning capture and sessions (docs/weft/DESIGN.md §2, §2.1, §2.2, §4)."""

import pytest

import weft
from weft import _reasoning


def _by_op(fake, name):
    return [c for c in fake.calls.values() if name in c.get("name", "")]


def _attr(c, *path):
    v = c.get("attributes") or {}
    for p in path:
        if not isinstance(v, dict):
            return None
        v = v.get(p)
    return v


# ---------------------------------------------------------------- teams


def test_team_block_sets_team_on_ops_and_spans(client, fake):
    @weft.trace
    def step(x):
        return x

    with weft.team("trip-planners", "Trip planners") as t:
        assert weft.get_current_team().id == "trip-planners" and t.name == "Trip planners"
        step(1)
        with weft.start_conversation(agent_name="orchestrator") as conv:
            with conv.start_turn(user_message="hi") as turn:
                turn.output_messages = [weft.Message.assistant("hello")]
    step(2)
    weft.flush(5)
    ops = _by_op(fake, "step")
    inside = [c for c in ops if c["inputs"]["x"] == 1][0]
    outside = [c for c in ops if c["inputs"]["x"] == 2][0]
    assert _attr(inside, "weft", "team") == {"id": "trip-planners", "name": "Trip planners"}
    assert _attr(outside, "weft", "team") is None
    turn_call = _by_op(fake, "invoke_agent")[0]
    assert _attr(turn_call, "weft", "team") == {"id": "trip-planners", "name": "Trip planners"}
    assert weft.get_current_team() is None


def test_conversation_team_option(client, fake):
    @weft.trace
    def tool_op():
        return 1

    with weft.start_conversation(agent_name="a", team=weft.Team("t1", "Team one")) as conv:
        with conv.start_turn(user_message="q") as turn:
            with turn.start_llm(model="m") as llm:
                llm.output("x")
            tool_op()
    assert weft.get_current_team() is None
    weft.log_turn(conversation_id="c2", agent_name="b", team=("t2", "Team two"))
    weft.flush(5)
    for c in fake.calls.values():
        if _attr(c, "gen_ai", "conversation", "id") == "c2":
            assert _attr(c, "weft", "team") == {"id": "t2", "name": "Team two"}
        else:
            assert _attr(c, "weft", "team") == {"id": "t1", "name": "Team one"}, c["name"]
    with pytest.raises(ValueError):
        weft.team("  ")
    with pytest.raises(TypeError):
        weft.start_conversation(team=3)


# ---------------------------------------------------------------- hand-offs


def test_handoff_records_attributes_and_from_agent(client, fake):
    with weft.start_conversation(agent_name="triage", conversation_id="s-1") as conv:
        with conv.start_turn(user_message="refund please") as turn:
            weft.handoff("billing", input="refund order 7")
    weft.flush(5)
    h = _by_op(fake, "handoff")[0]
    assert _attr(h, "weft", "handoff") == {"to_agent": "billing", "kind": "handoff", "from_agent": "triage"}
    assert h["inputs"] == {"input": "refund order 7"}
    assert h["parent_id"] == turn.call_id and h["display_name"] == "handoff to billing"
    assert _attr(h, "gen_ai", "conversation", "id") == "s-1"
    assert _attr(h, "weft", "span_kind") == "handoff"
    # not an agent span: no gen_ai.operation.name, so the server does not count it as an agent
    assert _attr(h, "gen_ai", "operation") is None


def test_delegate_handoff_block_nests_the_other_agent(client, fake):
    with weft.start_conversation(agent_name="orchestrator", conversation_id="s-2") as conv:
        with conv.start_turn(user_message="facts about Oslo") as turn:
            with weft.start_handoff("researcher", input="find facts", kind="delegate") as h:
                # another agent's turn, started inside: nested under the hand-off (same trace)
                with weft.start_turn(agent_name="researcher", user_message="find facts") as rt:
                    rt.output_messages = [weft.Message.assistant("3 facts")]
                h.output = "3 facts"
    weft.flush(5)
    hc = _by_op(fake, "handoff")[0]
    rc = fake.calls[rt.call_id]
    assert _attr(hc, "weft", "handoff", "kind") == "delegate" and hc["output"] == "3 facts"
    assert rc["parent_id"] == hc["id"] and rc["trace_id"] == turn.trace_id
    with pytest.raises(ValueError):
        weft.start_handoff("x", kind="bogus")
    with pytest.raises(ValueError):
        weft.handoff("")


def test_turn_of_another_agent_nests_under_the_running_agent(client, fake):
    """Nested agent runs of a different agent keep the trace, so the server's delegate rule fires."""
    with weft.start_conversation(agent_name="writer") as conv:
        with conv.start_turn(user_message="draft") as outer:
            inner = weft.Turn(agent_name="editor")
            with inner:
                pass
            same = weft.Turn(agent_name="writer")  # the same agent: a turn of its own
            with same:
                pass
    weft.flush(5)
    assert fake.calls[inner.call_id]["parent_id"] == outer.call_id
    assert fake.calls[inner.call_id]["trace_id"] == outer.trace_id
    assert fake.calls[same.call_id]["parent_id"] is None


def test_subagent_and_turn_handoff_helpers(client, fake):
    with weft.start_conversation(agent_name="a", conversation_id="s-3") as conv:
        with conv.start_turn(user_message="go") as turn:
            turn.handoff("b", input="x")
            with turn.start_subagent(name="c") as sub:
                with sub.start_handoff("d", kind="delegate"):
                    pass
    weft.flush(5)
    hs = {(_attr(h, "weft", "handoff", "from_agent"), _attr(h, "weft", "handoff", "to_agent")): h for h in _by_op(fake, "handoff")}
    assert set(hs) == {("a", "b"), ("c", "d")}
    assert hs[("c", "d")]["parent_id"] == sub.call_id


# ---------------------------------------------------------------- reasoning


def test_reasoning_on_conversation_span(client, fake):
    with weft.start_conversation(agent_name="a") as conv:
        with conv.start_turn(user_message="q") as turn:
            weft.reasoning("The user wants facts; delegate.", tokens=12)
            weft.reasoning("short summary", kind="summary")
            weft.reasoning("secret", kind="redacted")
            weft.reasoning("   ")  # blank: ignored
    weft.flush(5)
    steps = _attr(fake.calls[turn.call_id], "weft", "reasoning")
    assert [(s["kind"], s["text"]) for s in steps] == [("thinking", "The user wants facts; delegate."), ("summary", "short summary"), ("redacted", "")]
    assert steps[0]["tokens"] == 12 and steps[0]["time"].endswith("Z")


def test_reasoning_on_op_call_resends_start(client, fake):
    @weft.trace
    def plan(goal):
        weft.reasoning("first check the calendar")
        return "ok"

    plan("trip")
    weft.flush(5)
    c = _by_op(fake, "plan")[0]
    assert _attr(c, "weft", "reasoning")[0]["text"] == "first check the calendar"
    starts = [r for r in fake.requests if r["path"] == "/api/v1/spans/batch"]
    modes = [(mode, req["id"]) for r in starts for it in r["body"]["items"] for mode, req in it.items() if req["id"] == c["id"]]
    assert modes == [("start", c["id"]), ("start", c["id"]), ("end", c["id"])]


def test_reasoning_redacts_pii(fake, monkeypatch):
    monkeypatch.delenv("WEFT_DISABLED", raising=False)
    c = weft.init("e/p", server_url=fake.url, autopatch=False, settings={"redact_pii": True, "redact_pii_engine": "regex", "print_call_link": False})
    try:
        with weft.start_conversation(agent_name="a") as conv:
            with conv.start_turn(user_message="q") as turn:
                weft.reasoning("mail bob@example.com about it")
                with turn.start_llm(model="m") as llm:
                    llm.think("call 555-123-4567 first")
                    llm.output("done")
        weft.flush(5)
        assert _attr(fake.calls[turn.call_id], "weft", "reasoning")[0]["text"] == "mail <EMAIL_ADDRESS> about it"
        out = fake.calls[llm.call_id]["output"]["choices"][0]["message"]["content"]
        assert out[0] == {"type": "thinking", "thinking": "call <PHONE_NUMBER> first"}
    finally:
        weft.finish()


def test_capture_reasoning_off(fake, monkeypatch):
    monkeypatch.delenv("WEFT_DISABLED", raising=False)
    monkeypatch.setenv("WEFT_CAPTURE_REASONING", "false")
    c = weft.init("e/p", server_url=fake.url, autopatch=False, settings={"print_call_link": False})
    assert c.capture_reasoning is False
    try:
        with weft.start_conversation(agent_name="a") as conv:
            with conv.start_turn(user_message="q") as turn:
                weft.reasoning("hidden")
                with turn.start_llm(model="m") as llm:
                    llm.think("hidden too")
                    llm.output("visible")
        weft.flush(5)
        assert _attr(fake.calls[turn.call_id], "weft", "reasoning") is None
        msg = fake.calls[llm.call_id]["output"]["choices"][0]["message"]
        assert "hidden" not in str(msg) and "visible" in str(msg)
    finally:
        weft.finish()
    # the explicit setting wins over the environment
    c = weft.init("e/p", server_url=fake.url, autopatch=False, settings={"capture_reasoning": True, "print_call_link": False})
    assert c.capture_reasoning is True
    weft.finish()


def test_scrub_shapes():
    msg = {
        "role": "assistant",
        "reasoning_content": "deepseek thoughts",
        "content": [
            {"type": "thinking", "thinking": "t", "signature": "s"},
            {"type": "redacted_thinking", "data": "ENCRYPTED" * 20},
            {"type": "text", "text": "answer"},
        ],
    }
    resp = {"output": [{"type": "reasoning", "summary": [{"type": "summary_text", "text": "s"}], "encrypted_content": "gAAA" * 30},
                       {"type": "message", "content": [{"type": "output_text", "text": "hi"}]}],
            "reasoning": {"effort": "high"}}
    gemini = {"candidates": [{"content": {"parts": [{"text": "plan", "thought": True}, {"text": "answer"}]}}]}
    bedrock = {"output": {"message": {"content": [{"reasoningContent": {"reasoningText": {"text": "r", "signature": "x"}}}, {"text": "a"}]}}}
    on = _reasoning.scrub(msg)
    assert on["content"][1]["data"].startswith("sha256:") and len(on["content"][1]["data"]) == 31
    assert on["content"][0]["thinking"] == "t" and on["reasoning_content"] == "deepseek thoughts"
    assert _reasoning.scrub(resp)["output"][0]["encrypted_content"].startswith("sha256:")
    assert _reasoning.scrub(on) == on  # idempotent
    off = _reasoning.scrub(msg, capture=False)
    assert off == {"role": "assistant", "content": [{"type": "text", "text": "answer"}]}
    assert _reasoning.scrub(resp, capture=False) == {"output": [resp["output"][1]], "reasoning": {"effort": "high"}}
    assert _reasoning.scrub(gemini, capture=False)["candidates"][0]["content"]["parts"] == [{"text": "answer"}]
    assert _reasoning.scrub(bedrock, capture=False)["output"]["message"]["content"] == [{"text": "a"}]
    assert _reasoning.has_reasoning(gemini) and not _reasoning.has_reasoning(_reasoning.scrub(gemini, capture=False))
    assert _reasoning.bedrock_content(bedrock["output"]["message"]["content"])[0] == {"type": "thinking", "thinking": "r", "signature": "x"}
    red = _reasoning.bedrock_block({"reasoningContent": {"redactedContent": b"\x00\x01"}})
    assert red["type"] == "redacted_thinking" and red["data"].startswith("sha256:")
    assert _reasoning.norm_kind("reasoning_summary") == "summary" and _reasoning.norm_kind(None) == "thinking"


def test_framework_llm_call_does_not_repeat_nested_reasoning(client, fake):
    """A framework LLM call with a provider call under it keeps no reasoning (the provider call has it)."""
    from weft.integrations._spans import RunTracker

    tracker = RunTracker()
    outer = tracker.start("r1", "framework.llm", {"q": 1}, kind="llm")
    inner = client.create_call(weft._client._synthetic_op("anthropic.messages.create", "llm"), {"model": "m"})
    client.finish_call(inner, {"content": [{"type": "thinking", "thinking": "deep"}, {"type": "text", "text": "a"}]})
    tracker.end("r1", {"content": [{"type": "thinking", "thinking": "deep"}, {"type": "text", "text": "a"}]})
    weft.flush(5)
    assert fake.calls[inner.id]["output"]["content"][0]["type"] == "thinking"
    assert fake.calls[outer.id]["output"]["content"] == [{"type": "text", "text": "a"}]


def test_update_call_resends_start(client, fake):
    call = client.create_call("thing", {"a": 1})
    client.update_call(call, {"gen_ai": {"conversation": {"id": "sess"}}}, thread_id="sess")
    client.finish_call(call, 1)
    weft.flush(5)
    c = fake.calls[call.id]
    assert c["session_id"] == "sess" and _attr(c, "gen_ai", "conversation", "id") == "sess" and c["inputs"] == {"a": 1}
