"""Live-server tests for agent tracing, SavedView, the response cache, eval links and BYOB media.

Skipped unless WEFT_TEST_SERVER_URL is set.
"""

import json
import os
import time
import urllib.request
import uuid

import pytest

import weft

URL = os.environ.get("WEFT_TEST_SERVER_URL")
pytestmark = pytest.mark.skipif(not URL, reason="WEFT_TEST_SERVER_URL not set")


@pytest.fixture
def live():
    project = f"it/agents-{uuid.uuid4().hex[:8]}"
    c = weft.init(project, server_url=URL, api_key=os.environ.get("WEFT_TEST_API_KEY"), autopatch=False, settings={"print_span_link": False})
    yield c
    weft.flush(10)
    weft.finish()


def _post(c, path, body):
    return c._http.post(path, {"project_id": c.project_id, **body})


def _wait(fn, timeout=10.0):
    deadline = time.monotonic() + timeout
    while True:
        v = fn()
        if v or time.monotonic() > deadline:
            return v
        time.sleep(0.2)


def test_conversation_appears_in_agents_api(live):
    @weft.trace
    def lookup(order_id: int) -> dict:
        return {"order": order_id, "status": "shipped"}

    with weft.start_conversation(agent_name="support-bot", conversation_name="Order 7") as conv:
        with conv.start_turn(user_message="Where is order 7?", system_instructions=["Be brief."]) as turn:
            with turn.start_llm(model="gpt-4o-mini", provider_name="openai") as llm:
                llm.input_messages = [weft.Message.user("Where is order 7?")]
                llm.output_messages = [weft.Message.assistant("", tool_calls=[weft.conversation.ToolCallPart(id="c1", name="lookup", arguments={"order_id": 7})])]
                llm.usage = weft.Usage(input_tokens=20, output_tokens=6)
            with turn.start_tool(name="lookup", arguments={"order_id": 7}, tool_call_id="c1") as tool:
                tool.result = lookup(7)
            with turn.start_subagent(name="writer", model="gpt-4o") as sub:
                with sub.start_llm() as w:
                    w.output("Your order has shipped.")
            turn.output_messages = [weft.Message.assistant("Order 7 has shipped.")]
        with conv.start_turn(user_message="Thanks!") as t2:
            t2.output_messages = [weft.Message.assistant("You're welcome.")]
    live.flush()

    agents = _wait(lambda: {a["agent_name"]: a for a in _post(live, "/agents/query", {"include_costs": True})["agents"]})
    bot = agents["support-bot"]
    assert bot["invocation_count"] == 2 and bot["conversation_count"] == 1
    assert bot["llm_call_count"] == 1 and bot["tool_call_count"] == 1 and bot["total_input_tokens"] == 20
    assert "writer" in agents

    chat = _post(live, "/agents/conversations/chat", {"conversation_id": conv.conversation_id})
    assert chat["total_turns"] == 2 and chat["conversation_name"] == "Order 7"
    first = chat["turns"][0]["messages"]
    kinds = [m["type"] for m in first]
    assert kinds[0] == "user_message" and first[0]["user_message"]["text"] == "Where is order 7?"
    assert "tool_call" in kinds and "agent_start" in kinds
    tc = next(m["tool_call"] for m in first if m["type"] == "tool_call")
    assert tc["tool_name"] == "lookup" and json.loads(tc["tool_arguments"]) == {"order_id": 7}
    texts = [m["assistant_message"]["text"] for m in first if m["type"] == "assistant_message"]
    assert "Your order has shipped." in texts and "Order 7 has shipped." in texts
    second = chat["turns"][1]["messages"]
    assert second[0]["user_message"]["text"] == "Thanks!"

    spans = _post(live, "/agents/spans/query", {"query": {"$expr": {"$eq": [{"$getField": "conversation_id"}, {"$literal": conv.conversation_id}]}}})
    ops = sorted(s["operation_name"] or "" for s in spans["spans"])
    assert ops.count("invoke_agent") == 3 and ops.count("chat") == 2 and ops.count("execute_tool") == 1
    # also a thread in the Threads view
    threads = _post(live, "/api/v1/spans/sessions", {})["items"]
    assert any(t["session_id"] == conv.conversation_id and t["turn_count"] == 2 for t in threads)


def test_batch_logged_conversation_is_queryable(live):
    t0 = weft.conversation._now()
    turns = [
        weft.Turn(messages=[weft.Message.user(f"question {i}")], output_messages=[weft.Message.assistant(f"answer {i}")],
                  spans=[weft.LLM(model="gpt-4o-mini", usage=weft.Usage(input_tokens=5, output_tokens=2), started_at=t0, ended_at=t0)])
        for i in range(3)
    ]
    res = weft.log_conversation(turns=turns, agent_name="replayer", conversation_name="replay")
    live.flush()
    chat = _wait(lambda: (lambda r: r if r["total_turns"] == 3 else None)(_post(live, "/agents/conversations/chat", {"conversation_id": res.conversation_id})))
    assert chat and [t["messages"][0]["user_message"]["text"] for t in chat["turns"]] == ["question 0", "question 1", "question 2"]
    assert chat["total_input_tokens"] == 15


def test_saved_view_filters_on_the_server(live):
    @weft.trace
    def answer(q: str) -> str:
        if q == "boom":
            raise ValueError("boom")
        return q.upper()

    for q in ("a", "bb", "ccc", "boom"):
        try:
            answer(q)
        except ValueError:
            pass
    live.flush()
    view = (weft.SavedView(label="Successful long answers").filter_name("answer").add_filter("summary.weft.status", "equals", "success")
            .add_filter("output", "contains", "B").sort_by("started_at", "asc").add_column("inputs.q", "Question").save())
    assert [c.inputs["q"] for c in view.spans()] == ["bb"]
    assert [c.inputs["q"] for c in weft.SavedView().filter_name("answer").add_filter("inputs.q", "in", ["a", "ccc"]).sort_by("started_at", "asc").spans()] == ["a", "ccc"]
    assert len(list(weft.SavedView().filter_name("answer").add_filter("started_at", "after", "2020-01-01").spans())) == 4
    assert len(list(weft.SavedView().filter_name("answer").add_filter("summary.weft.status", "equals", "error").spans())) == 1
    loaded = weft.SavedView.load(view.name)
    assert loaded.label == "Successful long answers" and [c.inputs["q"] for c in loaded.spans()] == ["bb"]
    assert [v.name for v in live.get_saved_views(view_type="traces")] == [view.name]


def test_server_cache_hits_on_live_reads(live):
    ref = weft.save(weft.Dataset(name="cached", rows=[{"i": i} for i in range(3)]))
    live.clear_server_cache()
    before = live.cache_stats()
    assert [r["i"] for r in live.get(ref).rows] == [0, 1, 2]
    assert [r["i"] for r in live.get(ref).rows] == [0, 1, 2]
    after = live.cache_stats()
    assert after["hits"] - before["hits"] >= 2 and after["misses"] - before["misses"] >= 2


def test_evaluation_logger_links_existing_turns(live):
    turns = []
    for q in ("2+2", "3+3"):
        with weft.start_turn(user_message=q, agent_name="calc") as t:
            t.output_messages = [weft.Message.assistant(str(eval(q)))]
        turns.append(t)
    ev = weft.EvaluationLogger(model="calc", dataset="calc-qs", name="calc eval")
    for q, t in zip(("2+2", "3+3"), turns):
        p = ev.log_prediction({"q": q}, output=str(eval(q)), span=t)
        p.log_score("correct", True)
        p.finish()
    ev.log_summary()
    live.flush()
    [pas, _] = list(live.spans(filter={"names": ["Evaluation.predict_and_score"]}, sort_by=[{"field": "started_at", "direction": "asc"}]))
    links = pas.attributes["weft"]["linked_spans"]
    assert links == [{"span_id": turns[0].span_id, "trace_id": turns[0].trace_id}]
    q = {"$eq": [{"$getField": "attributes.weft.genai_span_ref.0.span_id"}, {"$literal": turns[1].span_id}]}
    found = list(live.spans(query=q))
    assert len(found) >= 1 and all(c.base_name in ("Evaluation.predict_and_score", "calc.predict") for c in found)


def test_byob_references_and_resolve_url(live):
    blob = weft.Content(b"stored in weft", mimetype="text/plain")

    @weft.trace
    def look(image, doc, local) -> str:
        return "ok"

    img = weft.Image.from_uri("s3://customer-bucket/cats/1.png")
    page = weft.Content.from_uri("https://example.com/doc.pdf")
    look(img, page, blob)
    live.flush()
    [c] = list(live.spans(filter={"names": ["look"]}))
    assert c.inputs["image"]["uri"] == "s3://customer-bucket/cats/1.png" and c.inputs["image"]["_type"] == "Image"
    assert c.inputs["doc"]["uri"] == "https://example.com/doc.pdf"
    url = live.resolve_url(blob)
    assert "/api/v1/files/" in url and blob.digest in url
    with urllib.request.urlopen(url) as resp:
        assert resp.read() == b"stored in weft"
    assert live.resolve_url(["https://example.com/doc.pdf"]) == ["https://example.com/doc.pdf"]
    assert weft.Content.from_uri(f"weft-file://{blob.digest}").as_text() == "stored in weft"
    with pytest.raises(weft.WeftHTTPError):  # this server has no S3 backend
        live.resolve_url("s3://customer-bucket/cats/1.png")
