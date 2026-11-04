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
        span = client.create_call("upd", {})
        client.update_span(span, {"a": 1}, session_id="sess-1")
        assert span.session_id == "sess-1"
        client.finish_call(span, None)
        assert weft.api_types.ThreadsQueryReq(limit=1).limit == 1
        assert client.print_span_link is True
    with pytest.warns(DeprecationWarning, match=r"weft\.thread\(thread_id\) is deprecated; use weft\.session\(session_id\)"):
        with weft.thread("s-old") as t:
            f()
    assert _warns(lambda: t.thread_id, r"SessionContext\.thread_id is deprecated") == "s-old"
    client.flush()
    assert sorted(c["session_id"] for c in fake.calls_by_op("f")) == ["s-new", "s-old"]
    span = client.create_call("upd2", {})
    _warns(lambda: client.update_call(span, None, thread_id="sess-2"), r"update_call\(thread_id=\.\.\.\) is deprecated")
    assert span.session_id == "sess-2"
    client.finish_call(span, None)
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
    assert tsi.ThreadsQueryReq is weft.api_types.ThreadsQueryReq and tsi.threads_query_body is weft.api_types.threads_query_body


def test_saved_view_pins_span_name():
    v = weft.SavedView()
    v.pin_column_right("started_at")
    assert v.definition["pin"] == {"left": ["CustomCheckbox", "name"], "right": ["started_at"]}
    old = weft.SavedView(definition={"pin": {"left": ["CustomCheckbox", "op_name"], "right": ["thread_id"]}})
    old.unpin_column("session_id")
    assert old.definition["pin"] == {"left": ["CustomCheckbox", "name"], "right": []}
