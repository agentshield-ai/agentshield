"""Agent conversation tracing (weft.start_conversation & co.) against the fake server."""

import datetime
import json
import warnings

import pytest

import weft
from weft import conversation as cv


def _flushed(fake, client):
    client.flush()
    return fake


def _by_op(fake, name):
    return [c for c in fake.calls.values() if fake.op_base(c) == name]


def _attr(c, dotted):
    v = c.get("attributes") or {}
    for p in dotted.split("."):
        v = v.get(p) if isinstance(v, dict) else None
    return v


def test_streaming_conversation_records_agent_calls(client, fake):
    @weft.trace
    def lookup(order_id: int) -> str:
        return "shipped"

    with weft.start_conversation(agent_name="support", conversation_name="Order help", attributes={"team": "cx"}) as conv:
        assert weft.get_current_conversation() is conv
        with conv.start_turn(user_message="Where is order 7?", system_instructions=["Be brief."]) as turn:
            assert weft.get_current_turn() is turn
            with turn.start_llm(model="gpt-4o-mini", provider_name="openai") as llm:
                assert weft.get_current_llm() is llm
                llm.input_messages = [weft.Message.user("Where is order 7?")]
                llm.output_messages = [weft.Message.assistant("Checking.", tool_calls=[cv.ToolCallPart(id="t1", name="lookup", arguments={"order_id": 7})])]
                llm.usage = weft.Usage(input_tokens=12, output_tokens=4, reasoning_tokens=2)
                llm.finish_reasons = ["tool_calls"]
                llm.think("need the order status")
            with turn.start_tool(name="lookup", arguments={"order_id": 7}, tool_call_id="t1") as tool:
                tool.result = {"status": lookup(7)}
            turn.output_messages = [weft.Message.assistant("Order 7 has shipped.")]
    assert weft.get_current_conversation() is None and weft.get_current_turn() is None
    _flushed(fake, client)

    [t] = _by_op(fake, "invoke_agent")
    [l] = _by_op(fake, "chat")
    [tl] = _by_op(fake, "execute_tool")
    [op] = _by_op(fake, "lookup")
    # tree, thread and turn ids
    assert t["parent_id"] is None and t["turn_id"] == t["id"] and t["session_id"] == conv.conversation_id
    assert l["parent_id"] == t["id"] and tl["parent_id"] == t["id"] and op["parent_id"] == tl["id"]
    assert {l["trace_id"], tl["trace_id"], op["trace_id"]} == {t["trace_id"]}
    assert l["turn_id"] == t["id"] and op["session_id"] == conv.conversation_id and op["turn_id"] == t["id"]
    # attributes the agents index reads
    assert _attr(t, "gen_ai.operation.name") == "invoke_agent" and _attr(t, "gen_ai.agent.name") == "support"
    assert _attr(t, "gen_ai.conversation.name") == "Order help" and _attr(t, "gen_ai.conversation.id") == conv.conversation_id
    assert _attr(t, "weft.span_kind") == "agent" and _attr(l, "weft.span_kind") == "llm" and _attr(tl, "weft.span_kind") == "tool"
    assert _attr(t, "team") == "cx" and _attr(tl, "team") == "cx"
    assert json.loads(_attr(t, "gen_ai.system_instructions")) == [{"type": "text", "content": "Be brief."}]
    assert t["display_name"] == "invoke_agent support" and l["display_name"] == "chat gpt-4o-mini" and tl["display_name"] == "execute_tool lookup"
    # messages, output, usage
    assert t["inputs"]["messages"] == [{"role": "system", "content": "Be brief."}, {"role": "user", "content": "Where is order 7?"}]
    assert t["output"] == {"messages": [{"role": "assistant", "content": "Order 7 has shipped."}]}
    assert l["inputs"]["model"] == "gpt-4o-mini" and l["inputs"]["messages"] == [{"role": "user", "content": "Where is order 7?"}]
    choice = l["output"]["choices"][0]
    assert choice["finish_reason"] == "tool_calls"
    assert choice["message"]["tool_calls"] == [{"id": "t1", "type": "function", "function": {"name": "lookup", "arguments": '{"order_id": 7}'}}]
    assert choice["message"]["content"][0] == {"type": "thinking", "thinking": "need the order status"}
    assert l["summary"]["usage"]["gpt-4o-mini"] == {"requests": 1, "prompt_tokens": 12, "completion_tokens": 4, "total_tokens": 16, "reasoning_tokens": 2}
    assert _attr(l, "gen_ai.provider.name") == "openai" and _attr(l, "gen_ai.response.finish_reasons") == ["tool_calls"]
    assert tl["inputs"] == {"order_id": 7} and tl["output"] == {"status": "shipped"}
    assert _attr(tl, "gen_ai.tool.name") == "lookup" and _attr(tl, "gen_ai.tool.call.id") == "t1"
    # local handles
    assert turn.span_id == t["id"] and turn.trace_id == t["trace_id"] and tool.duration_ms >= 0


def test_turns_are_separate_traces_and_start_turn_ends_previous(client, fake):
    conv = weft.start_conversation(agent_name="a")
    t1 = conv.start_turn(user_message="one")
    t1.start()
    t2 = conv.start_turn(user_message="two")  # ends t1
    assert t1._ended
    with t2:
        pass
    conv.end()
    _flushed(fake, client)
    turns = sorted(_by_op(fake, "invoke_agent"), key=lambda c: c["started_at"])
    assert [c["inputs"]["messages"][0]["content"] for c in turns] == ["one", "two"]
    assert turns[0]["trace_id"] != turns[1]["trace_id"]
    assert {c["session_id"] for c in turns} == {conv.conversation_id}


def test_continue_parent_trace_nests_turn_under_current_call(client, fake):
    @weft.trace
    def handler() -> None:
        with weft.start_conversation(agent_name="nested", continue_parent_trace=True):
            with weft.start_turn(user_message="hi"):
                pass

    handler()
    _flushed(fake, client)
    [h] = _by_op(fake, "handler")
    [t] = _by_op(fake, "invoke_agent")
    assert t["parent_id"] == h["id"] and t["trace_id"] == h["trace_id"]


def test_module_level_functions_and_subagents(client, fake):
    with weft.start_conversation(agent_name="planner", model="gpt-4o"):
        with weft.start_turn(user_message="plan a trip"):
            llm = weft.start_llm(provider_name="openai")  # inherits the turn model
            with llm:
                llm.output("I'll ask the researcher.")
            weft.end_llm()  # no-op: already ended
            with weft.start_subagent(name="researcher", model="claude-3-5-haiku") as sub:
                sub.record(input_messages=[weft.Message.user("find flights")], agent_version="2")
                with sub.start_tool(name="search", arguments="flights to Lisbon") as tool:
                    tool.result = "3 results"
                with sub.start_llm() as inner:
                    inner.output("Found 3 flights.")
            with weft.start_tool(name="book") as book:
                book.arguments = {"flight": 1}
                book.result = True
            weft.end_turn()
    _flushed(fake, client)
    [turn] = [c for c in _by_op(fake, "invoke_agent") if _attr(c, "gen_ai.agent.name") == "planner"]
    [sub] = [c for c in _by_op(fake, "invoke_agent") if _attr(c, "gen_ai.agent.name") == "researcher"]
    chats = _by_op(fake, "chat")
    [search] = [c for c in _by_op(fake, "execute_tool") if _attr(c, "gen_ai.tool.name") == "search"]
    [book_c] = [c for c in _by_op(fake, "execute_tool") if _attr(c, "gen_ai.tool.name") == "book"]
    assert sub["parent_id"] == turn["id"] and sub["turn_id"] == turn["id"] and sub["session_id"] == turn["session_id"]
    assert search["parent_id"] == sub["id"] and book_c["parent_id"] == turn["id"]
    assert search["inputs"] == {"arguments": "flights to Lisbon"} and search["output"] == "3 results"
    assert book_c["inputs"] == {"flight": 1} and book_c["output"] is True
    assert {_attr(c, "gen_ai.request.model") for c in chats} == {"gpt-4o", "claude-3-5-haiku"}
    assert _attr(sub, "gen_ai.agent.version") == "2" and sub["inputs"]["messages"] == [{"role": "user", "content": "find flights"}]


def test_errors_are_recorded_and_reraised(client, fake):
    with pytest.raises(RuntimeError):
        with weft.start_conversation(agent_name="a") as conv:
            with conv.start_turn(user_message="x"):
                with weft.start_tool(name="flaky"):
                    raise RuntimeError("tool broke")
    _flushed(fake, client)
    [tool] = _by_op(fake, "execute_tool")
    [turn] = _by_op(fake, "invoke_agent")
    assert "RuntimeError: tool broke" in tool["error"] and _attr(tool, "error.type") == "RuntimeError"
    assert "tool broke" in turn["error"]


def test_include_content_false_drops_messages(client, fake):
    with weft.start_conversation(agent_name="private", include_content=False) as conv:
        with conv.start_turn(user_message="secret question") as turn:
            with turn.start_llm(model="m") as llm:
                llm.input_messages = [weft.Message.user("secret question")]
                llm.output("secret answer")
                llm.usage = weft.Usage(input_tokens=3, output_tokens=2)
            with turn.start_tool(name="t", arguments={"q": "secret"}) as t:
                t.result = "secret"
    _flushed(fake, client)
    blob = json.dumps([c.get("inputs") for c in fake.calls.values()] + [c.get("output") for c in fake.calls.values()])
    assert "secret" not in blob
    [l] = _by_op(fake, "chat")
    assert l["summary"]["usage"]["m"]["prompt_tokens"] == 3  # usage is not content


def test_set_attributes_record_error_and_events(client, fake):
    with weft.start_conversation(agent_name="a") as conv:
        with conv.start_turn() as turn:
            turn.set_attributes({"team.tag": "x", "custom": 1})
            with warnings.catch_warnings():
                warnings.simplefilter("ignore", DeprecationWarning)
                turn.add_event("permission_request", {"tool": "rm"})
            turn.record_error(ValueError("soft failure"))
    turn.set_attributes({"late": True})  # ignored after end
    _flushed(fake, client)
    [t] = _by_op(fake, "invoke_agent")
    assert t["attributes"]["team.tag"] == "x" and t["attributes"]["custom"] == 1 and "late" not in t["attributes"]
    assert t["attributes"]["weft"]["events"][0]["name"] == "permission_request"
    assert "ValueError: soft failure" in t["error"]


def test_log_turn_and_log_conversation_batch(client, fake):
    t0 = datetime.datetime(2026, 1, 1, 12, 0, 0, tzinfo=datetime.timezone.utc)
    sec = datetime.timedelta(seconds=1)
    llm = weft.LLM(model="gpt-4o-mini", started_at=t0, ended_at=t0 + sec, usage=weft.Usage(input_tokens=5, output_tokens=5))
    llm.output("hello")
    tool = weft.Tool(name="clock", arguments={}, result="12:00", started_at=t0 + sec, ended_at=t0 + 2 * sec)
    res = weft.log_turn(
        conversation_id="conv-batch",
        agent_name="batcher",
        messages=[weft.Message.user("time?")],
        output_messages=[weft.Message.assistant("noon")],
        spans=[llm, tool],
        attributes={"source": "replay"},
    )
    assert res.conversation_id == "conv-batch" and res.span_count == 3 and len(res.root_span_ids) == 1
    turns = [weft.Turn(messages=[weft.Message.user(f"q{i}")], spans=[weft.Tool(name="t", result=i)]) for i in range(2)]
    res2 = weft.log_conversation(turns=turns, agent_name="batcher", conversation_name="replayed")
    assert res2.span_count == 4 and len(res2.trace_ids) == 2 and res2.conversation_id
    _flushed(fake, client)
    t = fake.calls[res.root_span_ids[0]]
    assert t["started_at"] == "2026-01-01T12:00:00.000000Z" and t["ended_at"] == "2026-01-01T12:00:02.000000Z"
    assert t["session_id"] == "conv-batch" and t["turn_id"] == t["id"] and _attr(t, "source") == "replay"
    kids = fake.children(t["id"])
    assert [fake.op_base(k) for k in kids] == ["chat", "execute_tool"]
    assert kids[0]["started_at"] == "2026-01-01T12:00:00.000000Z" and kids[1]["output"] == "12:00"
    later = [fake.calls[i] for i in res2.root_span_ids]
    assert {c["session_id"] for c in later} == {res2.conversation_id}
    assert all(_attr(c, "gen_ai.agent.name") == "batcher" and _attr(c, "gen_ai.conversation.name") == "replayed" for c in later)
    # the caller's turns were not mutated
    assert turns[0].agent_name == "" and turns[0].span is None


def test_media_attachments_are_uploaded(client, fake):
    png = b"\x89PNG\r\n\x1a\n" + b"0" * 20
    with weft.start_conversation(agent_name="vision") as conv:
        with conv.start_turn() as turn:
            with turn.start_llm(model="gpt-4o") as llm:
                llm.input_messages = [weft.Message.user("what is this?")]
                llm.attach_media(content=png, mime_type="image/png")
                llm.attach_media_url("https://example.com/cat.jpg", modality="image")
                llm.output("a cat")
    _flushed(fake, client)
    [l] = _by_op(fake, "chat")
    parts = l["inputs"]["messages"][-1]["content"]
    assert parts[0] == {"type": "text", "text": "what is this?"}
    assert parts[1]["_type"] == "Image" and parts[1]["file_digest"] in fake.files
    assert parts[2] == {"type": "uri", "uri": "https://example.com/cat.jpg", "modality": "image"}
    with pytest.raises(ValueError):
        weft.LLM().attach_media(content=b"x", uri="y")


def test_tracing_disabled_and_uninitialised_are_noops(client, fake):
    with weft.tracing_disabled():
        with weft.start_conversation(agent_name="quiet") as conv:
            with conv.start_turn(user_message="hi") as turn:
                with turn.start_llm(model="m") as llm:
                    llm.output("x")
    assert turn.span is None
    _flushed(fake, client)
    assert not _by_op(fake, "invoke_agent")


def test_agent_name_override_and_deprecated_aliases(client, fake):
    with cv.agent_name_override("auto-named"):
        with weft.start_turn(user_message="hi") as turn:
            with warnings.catch_warnings(record=True) as w:
                warnings.simplefilter("always")
                with turn.tool(name="x"):
                    pass
            assert any(issubclass(x.category, DeprecationWarning) for x in w)
    _flushed(fake, client)
    [t] = _by_op(fake, "invoke_agent")
    assert _attr(t, "gen_ai.agent.name") == "auto-named"
    assert t.get("session_id") is None and t["turn_id"] == t["id"]  # a turn outside a conversation
    with pytest.raises(ValueError):
        with cv.agent_name_override(" "):
            pass


def test_message_helpers_and_json_strings():
    m = weft.Message.tool_result("c1", {"ok": True})
    assert m.role == "tool" and m.parts[0].response == '{"ok": true}'
    t = weft.Tool(name="x", arguments={"a": 1})
    t.result = [1, 2]
    assert t.arguments == '{"a": 1}' and t.result == "[1, 2]"
    msg = cv._chat_message(m, None)
    assert msg == {"role": "tool", "tool_call_id": "c1", "content": '{"ok": true}'}
