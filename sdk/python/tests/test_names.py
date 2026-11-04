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
