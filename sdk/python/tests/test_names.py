"""Weft's SDK names (docs/weft/DESIGN.md §10.4) and the deprecated earlier names."""

import warnings

import pytest

import weft


def _warns(fn, match):
    with pytest.warns(DeprecationWarning, match=match):
        return fn()


def test_trace_save_get(client, fake):
    @weft.trace
    def greet(name: str) -> str:
        return f"hello {name}"

    assert greet("ada") == "hello ada"
    ref = weft.save(weft.StringPrompt("Hello {name}"), "greeting")
    assert isinstance(ref, weft.ObjectRef) and ref.uri().startswith("weft:///test-entity/test-proj/object/greeting:")
    assert weft.get(ref).format(name="Ada") == "Hello Ada"
    assert weft.get("greeting:v0").format(name="B") == "Hello B"
    client.flush()
    [span] = list(client.spans(filter={"names": ["greet"]}))
    assert isinstance(span, weft.Span) and span.base_name == "greet" and span.error is None and span.session_id is None
    assert span.name.startswith("weft:///test-entity/test-proj/function/greet:")
    fn_ref = weft.save(greet)
    assert isinstance(fn_ref, weft.FunctionRef) and fn_ref.uri() == span.name
    assert client.span(span.id).output == "hello ada"
    assert client.span("no-such-span") is None
    assert weft.get(span.ref).id == span.id
    assert span.ref.uri() == f"weft:///test-entity/test-proj/span/{span.id}"


def test_refs_of_earlier_kind_names_are_read():
    r = weft.parse_uri("weft:///e/p/op/f:abc")
    assert isinstance(r, weft.FunctionRef) and r.uri() == "weft:///e/p/function/f:abc"
    c = weft.parse_uri("weft:///e/p/call/123")
    assert isinstance(c, weft.SpanRef) and c.uri() == "weft:///e/p/span/123"


def test_deprecated_names_warn_and_still_work(client, fake):
    def inc(x):
        return x + 1

    f = _warns(lambda: weft.op(inc), r"weft\.op is deprecated; use weft\.trace")
    assert f(1) == 2
    ref = _warns(lambda: weft.publish({"v": 1}, name="dep"), r"weft\.publish\(\) is deprecated; use weft\.save\(\)")
    assert _warns(lambda: weft.ref("dep").get(), r"Ref\.get\(\) is deprecated") == {"v": 1}
    assert _warns(lambda: client.publish({"v": 2}, name="dep"), "WeftClient.publish").digest != ref.digest
    client.flush()
    spans = _warns(lambda: list(client.get_calls(filter={"op_names": ["inc"]})), r"get_calls\(\) is deprecated; use WeftClient\.spans\(\)")
    assert len(spans) == 1
    one = _warns(lambda: client.get_call(spans[0].id), r"get_call\(\) is deprecated")
    assert one.id == spans[0].id
    assert _warns(lambda: one.op_name, r"Span\.op_name is deprecated") == one.name
    assert _warns(lambda: one.exception, r"Span\.exception is deprecated") is None
    assert _warns(lambda: one.thread_id, r"Span\.thread_id is deprecated") is None
    for old, new in (("Call", weft.Span), ("CallsIter", weft.SpansIter), ("CallRef", weft.SpanRef), ("OpRef", weft.FunctionRef),
                     ("get_current_call", weft.get_current_span), ("NoCurrentCallError", weft.NoCurrentSpanError)):
        assert _warns(lambda: getattr(weft, old), f"weft.{old} is deprecated") is new
    with warnings.catch_warnings():
        warnings.simplefilter("error")
        weft.save({"v": 3}, name="dep")  # the new names never warn
        list(client.spans(limit=1))


def test_platform_object_field_names(client, fake):
    """Monitors and annotation specs name spans ``names``; saved views store
    Weft's span filter names. The earlier names still work, with a warning
    for keywords, and are read silently from versions saved before the rename."""
    mon = _warns(lambda: weft.Monitor(name="m", op_names=["f"]), r"Monitor\(op_names=\.\.\.\) is deprecated")
    assert mon.names == ["f"] and _warns(lambda: mon.op_names, r"Monitor\.op_names is deprecated") == ["f"]
    weft.save(mon)
    assert fake.objs["m"][0]["value"]["names"] == ["f"] and "op_names" not in fake.objs["m"][0]["value"]
    spec = _warns(lambda: weft.AnnotationSpec(name="s", op_scope=["f"]), r"AnnotationSpec\(op_scope=\.\.\.\) is deprecated")
    assert spec.names == ["f"]

    old = {"_class_name": "Monitor", "_bases": ["Object", "BaseModel"], "name": "old", "active": True, "op_names": ["g"]}
    with warnings.catch_warnings():
        warnings.simplefilter("error", DeprecationWarning)
        assert weft.Monitor._weft_from_val(old, client, None).names == ["g"]
        spec_old = {"_class_name": "AnnotationSpec", "name": "o", "op_scope": ["h"]}
        assert weft.AnnotationSpec._weft_from_val(spec_old, client, None).names == ["h"]

    view = weft.SavedView(definition={"filter": {"op_names": ["a"], "thread_ids": ["t"]}})
    assert view.to_spans_query()["filter"] == {"names": ["a"], "session_ids": ["t"]}
    view.set_filter(call_ids=["c"])
    assert view.definition["filter"] == {"names": ["a"], "session_ids": ["t"], "ids": ["c"]}
    view.filter_op(None)
    assert view.definition["filter"] == {"session_ids": ["t"], "ids": ["c"]}


def test_session_span_link_update_and_api_types(client, fake, monkeypatch):
    """weft.session(), print_span_link, update_span(session_id=) and
    weft.api_types; the earlier names (weft.thread, print_call_link /
    WEFT_PRINT_CALL_LINK, update_call(thread_id=), weft.trace_server_interface)
    still work and warn."""
    import importlib
    import logging
    import sys

    @weft.trace
    def f():
        return 1

    with warnings.catch_warnings():
        warnings.simplefilter("error", DeprecationWarning)
        with weft.session("s-new") as s:
            assert s.session_id == "s-new"
            f()
        span = client.create_span("upd", {})
        client.update_span(span, {"a": 1}, session_id="sess-1")
        assert span.session_id == "sess-1"
        client.finish_span(span, None)
        assert weft.api_types.SessionsQueryReq(limit=1).limit == 1
        assert client.print_span_link is True
    with pytest.warns(DeprecationWarning, match=r"weft\.thread\(thread_id\) is deprecated; use weft\.session\(session_id\)"):
        with weft.thread("s-old") as t:
            f()
    assert _warns(lambda: t.thread_id, r"SessionContext\.thread_id is deprecated") == "s-old"
    client.flush()
    assert sorted(c["session_id"] for c in fake.calls_by_op("f")) == ["s-new", "s-old"]
    span = client.create_span("upd2", {})
    _warns(lambda: client.update_call(span, None, thread_id="sess-2"), r"update_call\(thread_id=\.\.\.\) is deprecated")
    assert span.session_id == "sess-2"
    client.finish_span(span, None)
    assert _warns(lambda: client.print_call_link, r"WeftClient\.print_call_link is deprecated") is True

    c2 = _warns(lambda: weft.WeftClient("e/p2", server_url=fake.url, settings={"print_call_link": False}),
                r'settings\["print_call_link"\] is deprecated')
    assert c2.print_span_link is False
    monkeypatch.delenv("WEFT_PRINT_SPAN_LINK", raising=False)
    monkeypatch.setenv("WEFT_PRINT_CALL_LINK", "false")
    logger = logging.getLogger("weft")
    seen = []
    handler = logging.Handler()
    handler.emit = lambda r: seen.append(r.getMessage())  # type: ignore[method-assign]
    logger.addHandler(handler)
    try:
        assert weft.WeftClient("e/p3", server_url=fake.url).print_span_link is False
    finally:
        logger.removeHandler(handler)
    assert any("WEFT_PRINT_CALL_LINK is deprecated; use WEFT_PRINT_SPAN_LINK" in m for m in seen)
    monkeypatch.setenv("WEFT_PRINT_SPAN_LINK", "true")
    assert weft.WeftClient("e/p4", server_url=fake.url).print_span_link is True

    sys.modules.pop("weft.trace_server_interface", None)
    with pytest.warns(DeprecationWarning, match=r"weft\.trace_server_interface is deprecated; use weft\.api_types"):
        tsi = importlib.import_module("weft.trace_server_interface")
    assert tsi.ThreadsQueryReq is weft.api_types.SessionsQueryReq and tsi.threads_query_body is weft.api_types.sessions_query_body
    assert tsi.SessionsQueryReq is weft.api_types.SessionsQueryReq


def test_saved_view_pins_span_name():
    v = weft.SavedView()
    v.pin_column_right("started_at")
    assert v.definition["pin"] == {"left": ["CustomCheckbox", "name"], "right": ["started_at"]}
    old = weft.SavedView(definition={"pin": {"left": ["CustomCheckbox", "op_name"], "right": ["thread_id"]}})
    old.unpin_column("session_id")
    assert old.definition["pin"] == {"left": ["CustomCheckbox", "name"], "right": []}


def test_span_names_for_sessions_spans_predictions_and_links(client, fake, monkeypatch):
    """sessions_query, create_span/finish_span, log_prediction(span=),
    ScoreLogger.predict_*_span, conversation .span/.span_id, Dataset.from_spans,
    urls.redirect_span, span_display_name and WEFT_MAX_SPANS_QUEUE_SIZE: the new
    names work without a warning."""
    from weft.api_types import SessionSchema, SessionsQueryReq, SessionsQueryRes

    @weft.trace(span_display_name=lambda s: f"q={s.inputs['q']}")
    def ask(q):
        return q * 2

    with warnings.catch_warnings():
        warnings.simplefilter("error", DeprecationWarning)
        with weft.session("sess-a"):
            ask(1)
            ask(2)
        span = client.create_span("explicit", {"x": 1})
        assert isinstance(span, weft.Span)
        client.finish_span(span, {"ok": True})
        with weft.start_conversation(conversation_id="conv-n") as conv:
            with conv.start_turn(user_message="hi", agent_name="a") as turn:
                pass
        assert turn.span is not None and turn.span_id == turn.span.id
        ev = weft.EvaluationLogger(model="m", dataset=[{"q": 1}])
        assert ev.span is not None
        pred = ev.log_prediction({"q": 1}, span=turn)
        assert pred.predict_and_score_span is not None and pred.predict_span is not None
        pred.finish()
        ev.log_summary()
        client.flush()
        res = client.sessions_query(SessionsQueryReq(limit=10))
        assert isinstance(res, SessionsQueryRes) and all(isinstance(x, SessionSchema) for x in res.sessions)
        assert "sess-a" in [x.session_id for x in res.sessions]
        assert [x.session_id for x in client.sessions_query_stream(limit=1)] == [res.sessions[0].session_id]
        assert weft.urls.redirect_span("e", "p", "s1").endswith("/e/p/spans/s1")
        ds = weft.Dataset.from_spans(list(client.spans(filter={"names": ["ask"]})))
        assert sorted(r["output"] for r in ds.rows) == [2, 4]
        assert weft.integrations._settings.OpSettings(span_display_name="d").span_display_name == "d"
        monkeypatch.delenv("WEFT_MAX_CALLS_QUEUE_SIZE", raising=False)
        monkeypatch.setenv("WEFT_MAX_SPANS_QUEUE_SIZE", "77")
        assert weft.WeftClient("e/q1", server_url=fake.url).max_queue_size == 77
    assert sorted(c["display_name"] for c in fake.calls_by_op("ask")) == ["q=1", "q=2"]
    assert fake.calls[span.id]["output"] == {"ok": True}
    link = fake.calls[pred.predict_and_score_span.id]["attributes"]["weft"]["linked_spans"]
    assert link == [{"span_id": turn.span_id, "trace_id": turn.trace_id}]


def test_earlier_span_names_warn_and_still_work(client, fake, monkeypatch):
    """The earlier names of the API above still work and emit a DeprecationWarning."""
    import importlib
    import logging
    import sys

    from weft.api_types import SessionSchema, SessionsQueryReq, SessionsQueryRes

    f = _warns(lambda: weft.trace(lambda q: q, name="old_dn", call_display_name="fixed"),
               r"trace\(call_display_name=\.\.\.\) is deprecated; use trace\(span_display_name=\.\.\.\)")
    assert f.span_display_name == "fixed"
    assert _warns(lambda: f.call_display_name, r"Op\.call_display_name is deprecated; use Op\.span_display_name") == "fixed"
    with weft.session("sess-old"):
        f(1)
    span = _warns(lambda: client.create_call("explicit_old", {}), r"WeftClient\.create_call\(\) is deprecated; use WeftClient\.create_span\(\)")
    _warns(lambda: client.finish_call(span, 5), r"WeftClient\.finish_call\(\) is deprecated; use WeftClient\.finish_span\(\)")
    with weft.start_conversation(conversation_id="conv-o") as conv:
        with conv.start_turn(user_message="hi", agent_name="a") as turn:
            pass
    assert _warns(lambda: turn.call, r"Turn\.call is deprecated; use Turn\.span") is turn.span
    assert _warns(lambda: turn.call_id, r"Turn\.call_id is deprecated; use Turn\.span_id") == turn.span_id
    ev = weft.EvaluationLogger(model="m", dataset=[{"q": 1}])
    assert _warns(lambda: ev.call, r"EvaluationLogger\.call is deprecated; use EvaluationLogger\.span") is ev.span
    pred = _warns(lambda: ev.log_prediction({"q": 1}, call=turn),
                  r"log_prediction\(call=\.\.\.\) is deprecated; use EvaluationLogger\.log_prediction\(span=\.\.\.\)")
    assert _warns(lambda: pred.predict_and_score_call, r"ScoreLogger\.predict_and_score_call is deprecated") is pred.predict_and_score_span
    assert _warns(lambda: pred.predict_call, r"ScoreLogger\.predict_call is deprecated; use ScoreLogger\.predict_span") is pred.predict_span
    pred.finish()
    ev.log_summary()
    client.flush()
    assert fake.calls[span.id]["output"] == 5 and [c["display_name"] for c in fake.calls_by_op("old_dn")] == ["fixed"]
    assert fake.calls[pred.predict_and_score_span.id]["attributes"]["weft"]["linked_spans"][0]["span_id"] == turn.span_id

    res = _warns(lambda: client.threads_query(limit=10), r"WeftClient\.threads_query\(\) is deprecated; use WeftClient\.sessions_query\(\)")
    assert isinstance(res, SessionsQueryRes)
    threads = _warns(lambda: res.threads, r"SessionsQueryRes\.threads is deprecated; use SessionsQueryRes\.sessions")
    assert threads == res.sessions and "sess-old" in [t.session_id for t in threads]
    assert _warns(lambda: threads[0].thread_id, r"SessionSchema\.thread_id is deprecated") == threads[0].session_id
    stream = _warns(lambda: client.threads_query_stream(limit=1), r"threads_query_stream\(\) is deprecated; use WeftClient\.sessions_query_stream\(\)")
    assert [t.session_id for t in stream] == [res.sessions[0].session_id]
    for old, new in (("ThreadsQueryReq", SessionsQueryReq), ("ThreadSchema", SessionSchema), ("ThreadsQueryRes", SessionsQueryRes),
                     ("ThreadsQueryFilter", weft.api_types.SessionsQueryFilter), ("threads_query_body", weft.api_types.sessions_query_body),
                     ("thread_from_session", weft.api_types.session_from_item)):
        assert _warns(lambda: getattr(weft.api_types, old), rf"weft\.api_types\.{old} is deprecated") is new
    with warnings.catch_warnings():
        warnings.simplefilter("ignore", DeprecationWarning)
        old_res = weft.api_types.ThreadsQueryRes(threads=[{"thread_id": "t1"}])
    assert old_res.sessions[0].session_id == "t1"  # earlier field names are read
    assert SessionsQueryReq(filter={"thread_ids": ["t"]}).filter.thread_ids == ["t"]
    body = weft.api_types.sessions_query_body(SessionsQueryReq(filter={"thread_ids": ["t"], "session_ids": ["s"]}), "e/p", "e")
    assert body["filter"] == {"session_ids": ["s", "t"]}

    ds = _warns(lambda: weft.Dataset.from_calls(list(client.spans(filter={"names": ["old_dn"]}))), r"Dataset\.from_calls\(\) is deprecated; use Dataset\.from_spans\(\)")
    assert [r["output"] for r in ds.rows] == [1]
    assert _warns(lambda: weft.urls.redirect_call("e", "p", "c1"), r"weft\.urls\.redirect_call\(\) is deprecated; use weft\.urls\.redirect_span\(\)") == weft.urls.redirect_span("e", "p", "c1")

    from weft.integrations._settings import OpSettings

    s = _warns(lambda: OpSettings(call_display_name="x"), r"OpSettings\(call_display_name=\.\.\.\) is deprecated")
    assert s.span_display_name == "x"
    assert _warns(lambda: s.call_display_name, r"OpSettings\.call_display_name is deprecated") == "x"

    monkeypatch.delenv("WEFT_MAX_SPANS_QUEUE_SIZE", raising=False)
    monkeypatch.delenv("WEFT_MAX_QUEUE_SIZE", raising=False)
    monkeypatch.setenv("WEFT_MAX_CALLS_QUEUE_SIZE", "33")
    logger = logging.getLogger("weft")
    seen = []
    handler = logging.Handler()
    handler.emit = lambda r: seen.append(r.getMessage())  # type: ignore[method-assign]
    logger.addHandler(handler)
    try:
        assert weft.WeftClient("e/q2", server_url=fake.url).max_queue_size == 33
    finally:
        logger.removeHandler(handler)
    assert any("WEFT_MAX_CALLS_QUEUE_SIZE is deprecated; use WEFT_MAX_SPANS_QUEUE_SIZE" in m for m in seen)
    monkeypatch.setenv("WEFT_MAX_SPANS_QUEUE_SIZE", "44")  # the new name wins
    assert weft.WeftClient("e/q3", server_url=fake.url).max_queue_size == 44

    sys.modules.pop("weft.trace_server_interface", None)
    with pytest.warns(DeprecationWarning):
        tsi = importlib.import_module("weft.trace_server_interface")
    assert tsi.ThreadSchema is SessionSchema and tsi.thread_from_session is weft.api_types.session_from_item
