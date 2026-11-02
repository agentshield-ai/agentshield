"""OpenAI Realtime: WAV audio capture and raw websocket clients (websockets,
websocket-client, aiohttp), against a local server that speaks the Realtime
event format (GA ``gpt-realtime`` events: session.created, server-VAD speech
markers, input_audio_buffer.committed, conversation.item.added,
response.output_audio.delta / .done, response.done with usage)."""

import asyncio
import base64
import io
import json
import math
import struct
import threading
import wave

import pytest

from weft.integrations import RealtimeTracer, patch_openai_realtime, unpatch_all
from weft.integrations import openai_realtime as orl


@pytest.fixture(autouse=True)
def _unpatch():
    yield
    unpatch_all()


def by_op(fake, name):
    return fake.calls_by_op(name)


def pcm16(ms, rate=24000, freq=440.0, phase=0):
    """``ms`` milliseconds of a 16-bit mono sine wave."""
    n = int(rate * ms / 1000)
    return b"".join(struct.pack("<h", int(12000 * math.sin(2 * math.pi * freq * (i + phase) / rate))) for i in range(n))


def b64(data):
    return base64.b64encode(data).decode()


def wav_of(fake, media):
    assert media["_type"] == "Audio" and media["mime_type"] == "audio/wav"
    return fake.files[media["file_digest"]]


def read_wav(data):
    with wave.open(io.BytesIO(data)) as w:
        return w.getframerate(), w.getnchannels(), w.getsampwidth(), w.readframes(w.getnframes())


SESSION = {"type": "realtime", "object": "realtime.session", "id": "sess_1", "model": "gpt-realtime",
           "output_modalities": ["audio"], "instructions": "Be brief.",
           "audio": {"input": {"format": {"type": "audio/pcm", "rate": 24000}, "turn_detection": {"type": "server_vad"}},
                     "output": {"format": {"type": "audio/pcm", "rate": 24000}, "voice": "alloy"}}}

USAGE = {"total_tokens": 230, "input_tokens": 150, "output_tokens": 80,
         "input_token_details": {"text_tokens": 30, "audio_tokens": 120, "cached_tokens": 0},
         "output_token_details": {"text_tokens": 20, "audio_tokens": 60}}


def response_events(rid, item_id, out_pcm, transcript="Hello there."):
    chunks = [out_pcm[i:i + 4800] for i in range(0, len(out_pcm), 4800)]
    evs = [{"type": "response.created", "event_id": "ev_r1", "response": {"id": rid, "object": "realtime.response", "status": "in_progress", "output": []}},
           {"type": "response.output_item.added", "response_id": rid, "output_index": 0,
            "item": {"id": item_id, "type": "message", "role": "assistant", "status": "in_progress", "content": []}},
           {"type": "response.content_part.added", "response_id": rid, "item_id": item_id, "output_index": 0, "content_index": 0,
            "part": {"type": "audio", "transcript": ""}}]
    for c in chunks:
        evs.append({"type": "response.output_audio.delta", "response_id": rid, "item_id": item_id, "output_index": 0, "content_index": 0, "delta": b64(c)})
    evs += [{"type": "response.output_audio_transcript.delta", "response_id": rid, "item_id": item_id, "output_index": 0, "content_index": 0, "delta": transcript},
            {"type": "response.output_audio.done", "response_id": rid, "item_id": item_id, "output_index": 0, "content_index": 0},
            {"type": "response.done", "response": {"id": rid, "object": "realtime.response", "status": "completed",
                                                    "output": [{"id": item_id, "type": "message", "role": "assistant", "status": "completed",
                                                                "content": [{"type": "output_audio", "transcript": transcript}]}],
                                                    "usage": USAGE}}]
    return with_ids(evs)


_ev_counter = [0]


def with_ids(evs):
    """Every server event carries an event_id, as the real API's do."""
    for e in evs:
        _ev_counter[0] += 1
        e.setdefault("event_id", f"event_{_ev_counter[0]}")
    return evs


def test_realtime_wav_audio_with_server_vad(client, fake):
    """Input audio is cut at the speech markers; the model's audio is saved per output item."""
    mic = pcm16(1000)  # 1 s of microphone audio, appended in 100 ms chunks
    out = pcm16(500, freq=220.0)
    rt = RealtimeTracer()
    rt.on_server_event({"type": "session.created", "event_id": "ev_1", "session": SESSION})
    for i in range(0, len(mic), 4800):
        rt.on_client_event({"type": "input_audio_buffer.append", "audio": b64(mic[i:i + 4800])})
    rt.on_server_event({"type": "input_audio_buffer.speech_started", "audio_start_ms": 200, "item_id": "item_u1"})
    rt.on_server_event({"type": "input_audio_buffer.speech_stopped", "audio_end_ms": 700, "item_id": "item_u1"})
    rt.on_server_event({"type": "input_audio_buffer.committed", "previous_item_id": None, "item_id": "item_u1"})
    rt.on_server_event({"type": "conversation.item.added", "previous_item_id": None,
                        "item": {"id": "item_u1", "type": "message", "status": "completed", "role": "user",
                                 "content": [{"type": "input_audio", "transcript": None}]}})
    for ev in response_events("resp_1", "item_a1", out):
        rt.on_server_event(json.dumps(ev))
    rt.close()
    client.flush()
    [sess] = by_op(fake, "openai.realtime.session")
    [resp] = by_op(fake, "openai.realtime.response")
    assert resp["parent_id"] == sess["id"] and resp["inputs"]["input_audio_bytes"] == len(mic)
    [user] = resp["inputs"]["input"]
    part = user["content"][0]
    assert part["type"] == "input_audio" and part["audio_bytes"] == 24000  # 500 ms of 24 kHz PCM16
    rate, ch, width, frames = read_wav(wav_of(fake, part["audio"]))
    assert (rate, ch, width) == (24000, 1, 2) and frames == mic[200 * 48:700 * 48], "the VAD segment, byte exact"
    opart = resp["output"]["output"][0]["content"][0]
    assert opart["transcript"] == "Hello there." and opart["audio_bytes"] == len(out)
    assert read_wav(wav_of(fake, opart["audio"]))[3] == out
    assert resp["output"]["audio_bytes"] == len(out) and resp["output"]["text"] == "Hello there."
    assert resp["summary"]["usage"]["gpt-realtime"]["input_audio_tokens"] == 120


def test_realtime_manual_commit_g711_and_client_items(client, fake):
    """No VAD: a commit takes everything appended since the last one. G.711 u-law
    audio is written as a WAV with format tag 7; base64 audio in a client
    ``conversation.item.create`` is converted too."""
    rt = RealtimeTracer()
    rt.on_server_event({"type": "session.created", "session": {"id": "s", "model": "gpt-4o-realtime-preview",
                                                                "input_audio_format": "g711_ulaw", "output_audio_format": "pcm16"}})
    first, second = bytes(range(256)) * 8, bytes(reversed(range(256))) * 4
    rt.on_client_event({"type": "input_audio_buffer.append", "audio": b64(first)})
    rt.on_client_event({"type": "input_audio_buffer.commit"})
    rt.on_server_event({"type": "input_audio_buffer.committed", "item_id": "item_1"})
    rt.on_client_event({"type": "input_audio_buffer.append", "audio": b64(b"\x01" * 100)})
    rt.on_client_event({"type": "input_audio_buffer.clear"})  # dropped, never part of an item
    rt.on_client_event({"type": "input_audio_buffer.append", "audio": b64(second)})
    rt.on_server_event({"type": "input_audio_buffer.committed", "item_id": "item_2"})
    for iid in ("item_1", "item_2"):
        rt.on_server_event({"type": "conversation.item.created", "item": {"id": iid, "type": "message", "role": "user",
                                                                         "content": [{"type": "input_audio", "transcript": None}]}})
    clip = pcm16(50)
    rt.on_client_event({"type": "conversation.item.create", "item": {"type": "message", "role": "user",
                                                                     "content": [{"type": "input_audio", "audio": b64(clip)}]}})
    rt.on_server_event({"type": "response.created", "response": {"id": "r1"}})
    rt.on_server_event({"type": "response.done", "response": {"id": "r1", "status": "completed", "output": []}})
    rt.close()
    client.flush()
    [resp] = by_op(fake, "openai.realtime.response")
    items = resp["inputs"]["input"]
    w1, w2 = wav_of(fake, items[0]["content"][0]["audio"]), wav_of(fake, items[1]["content"][0]["audio"])
    tag, channels, rate = struct.unpack("<HHI", w1[20:28])
    assert (tag, channels, rate) == (7, 1, 8000) and w1.endswith(first) and w2.endswith(second)
    w3 = wav_of(fake, items[2]["content"][0]["audio"])  # in the session's input format (u-law)
    assert w3.endswith(clip) and struct.unpack("<H", w3[20:22])[0] == 7
    assert items[2]["content"][0]["audio_bytes"] == len(clip) and "audio_bytes" not in json.dumps(resp["output"])


def test_realtime_audio_cap_and_off(client, fake, monkeypatch):
    out = pcm16(200)
    rt = RealtimeTracer(max_audio_bytes=1000)
    rt.on_server_event({"type": "session.created", "session": SESSION})
    for ev in response_events("r1", "i1", out):
        rt.on_server_event(ev)
    monkeypatch.setenv("WEFT_REALTIME_MAX_AUDIO_BYTES", "0")
    off = RealtimeTracer()
    off.on_server_event({"type": "session.created", "session": SESSION})
    for ev in response_events("r2", "i2", out):
        off.on_server_event(ev)
    rt.close()
    off.close()
    client.flush()
    r1, r2 = sorted(by_op(fake, "openai.realtime.response"), key=lambda c: c["output"]["id"])
    p1, p2 = r1["output"]["output"][0]["content"][0], r2["output"]["output"][0]["content"][0]
    assert p1["audio_truncated"] is True and len(read_wav(wav_of(fake, p1["audio"]))[3]) == 1000
    assert "audio" not in p2 and r2["output"]["audio_bytes"] == len(out), "capture off: sizes only"


def test_is_realtime_url(monkeypatch):
    monkeypatch.delenv("WEFT_REALTIME_HOSTS", raising=False)
    assert orl.is_realtime_url("wss://api.openai.com/v1/realtime?model=gpt-realtime")
    assert orl.is_realtime_url("wss://my-res.openai.azure.com/openai/realtime?deployment=rt")
    assert not orl.is_realtime_url("wss://api.openai.com/v1/responses")
    assert not orl.is_realtime_url("wss://xyz.supabase.co/realtime/v1/websocket")
    assert not orl.is_realtime_url("ws://127.0.0.1:9/v1/realtime")
    monkeypatch.setenv("WEFT_REALTIME_HOSTS", "127.0.0.1")
    assert orl.is_realtime_url("ws://127.0.0.1:9/v1/realtime")


# ---------------------------------------------------------------- a local Realtime server
class RealtimeServer:
    """A websockets server that answers like the Realtime API: session.created on
    connect, server-VAD events after appended audio is committed, and an audio
    response to response.create."""

    def __init__(self):
        self.received = []
        self.loop = asyncio.new_event_loop()
        self.ready = threading.Event()
        self.thread = threading.Thread(target=self._run, daemon=True)
        self.thread.start()
        assert self.ready.wait(10)

    def _run(self):
        from websockets.asyncio.server import serve

        asyncio.set_event_loop(self.loop)

        async def main():
            self.server = await serve(self.handler, "127.0.0.1", 0)
            self.port = self.server.sockets[0].getsockname()[1]
            self.ready.set()
            await self.server.serve_forever()

        try:
            self.loop.run_until_complete(main())
        except BaseException:  # cancelled on close
            pass

    async def handler(self, ws):
        await ws.send(json.dumps({"type": "session.created", "event_id": "ev_0", "session": SESSION}))
        appended = 0
        async for raw in ws:
            ev = json.loads(raw)
            self.received.append(ev["type"])
            if ev["type"] == "input_audio_buffer.append":
                appended += len(base64.b64decode(ev["audio"]))
            elif ev["type"] == "input_audio_buffer.commit":
                ms = appended // 48
                for e in with_ids([{"type": "input_audio_buffer.speech_started", "audio_start_ms": 0, "item_id": "item_u"},
                          {"type": "input_audio_buffer.speech_stopped", "audio_end_ms": ms, "item_id": "item_u"},
                          {"type": "input_audio_buffer.committed", "item_id": "item_u", "previous_item_id": None},
                          {"type": "conversation.item.added", "item": {"id": "item_u", "type": "message", "role": "user",
                                                                        "content": [{"type": "input_audio", "transcript": None}]}}]):
                    await ws.send(json.dumps(e))
            elif ev["type"] == "response.create":
                for e in response_events("resp_ws", "item_a", pcm16(100, freq=330.0)):
                    await ws.send(json.dumps(e))

    def url(self, path="/v1/realtime?model=gpt-realtime"):
        return f"ws://127.0.0.1:{self.port}{path}"

    def close(self):
        self.loop.call_soon_threadsafe(self.server.close)
        self.thread.join(5)


@pytest.fixture
def rtserver(monkeypatch):
    pytest.importorskip("websockets")
    monkeypatch.setenv("WEFT_REALTIME_HOSTS", "127.0.0.1")
    srv = RealtimeServer()
    yield srv
    srv.close()


def _check_ws_trace(fake, mic):
    [sess] = by_op(fake, "openai.realtime.session")
    [resp] = by_op(fake, "openai.realtime.response")
    assert sess["inputs"]["model"] == "gpt-realtime" and resp["parent_id"] == sess["id"]
    assert sess["output"]["responses"] == 1 and sess["output"]["client_events"] >= 3
    user = resp["inputs"]["input"][0]["content"][0]
    assert read_wav(wav_of(fake, user["audio"]))[3] == mic
    out = resp["output"]["output"][0]["content"][0]
    assert read_wav(wav_of(fake, out["audio"]))[3] == pcm16(100, freq=330.0)
    assert resp["summary"]["usage"]["gpt-realtime"]["prompt_tokens"] == 150


def test_websockets_connect_is_traced(client, fake, rtserver):
    """``websockets.connect`` (the OpenAI Agents SDK's RealtimeRunner transport): both
    ``async with`` and ``async for`` keep working, and the session is traced."""
    import websockets

    assert patch_openai_realtime()
    mic = pcm16(300)

    async def run():
        async with websockets.connect(rtserver.url()) as ws:
            assert type(ws).__name__ == "ClientConnection", "the real connection object, not a proxy"
            await ws.send(json.dumps({"type": "input_audio_buffer.append", "audio": b64(mic)}))
            await ws.send(json.dumps({"type": "input_audio_buffer.commit"}))
            await ws.send(json.dumps({"type": "response.create"}))
            async for raw in ws:
                if json.loads(raw)["type"] == "response.done":
                    break

    asyncio.run(run())
    client.flush()
    _check_ws_trace(fake, mic)


def test_websockets_untouched_for_other_hosts(client, fake, rtserver, monkeypatch):
    import websockets

    assert patch_openai_realtime()
    monkeypatch.setenv("WEFT_REALTIME_HOSTS", "")

    async def run():
        async with websockets.connect(rtserver.url()) as ws:
            assert orl._attached(ws) is None
            await ws.recv()

    asyncio.run(run())
    client.flush()
    assert not by_op(fake, "openai.realtime.session")


def test_websocket_client_app_is_traced(client, fake, rtserver):
    websocket = pytest.importorskip("websocket")
    assert patch_openai_realtime()
    mic = pcm16(200)
    seen = []

    def on_open(ws):
        ws.send(json.dumps({"type": "input_audio_buffer.append", "audio": b64(mic)}))
        ws.send_text(json.dumps({"type": "input_audio_buffer.commit"}))
        ws.send(json.dumps({"type": "response.create"}))

    def on_message(ws, message):
        seen.append(json.loads(message)["type"])
        if seen[-1] == "response.done":
            ws.close()

    app = websocket.WebSocketApp(rtserver.url(), on_open=on_open, on_message=on_message)
    assert isinstance(app, websocket.WebSocketApp)
    t = threading.Thread(target=app.run_forever, daemon=True)
    t.start()
    t.join(10)
    assert "response.done" in seen
    client.flush()
    _check_ws_trace(fake, mic)


def test_aiohttp_ws_connect_is_traced(client, fake, rtserver):
    aiohttp = pytest.importorskip("aiohttp")
    assert patch_openai_realtime()
    mic = pcm16(250)

    async def run():
        async with aiohttp.ClientSession() as s:
            async with s.ws_connect(rtserver.url()) as ws:
                await ws.send_str(json.dumps({"type": "input_audio_buffer.append", "audio": b64(mic)}))
                await ws.send_json({"type": "input_audio_buffer.commit"})
                await ws.send_str(json.dumps({"type": "response.create"}))
                first = await ws.receive_json()
                assert first["type"] == "session.created"
                async for msg in ws:
                    if json.loads(msg.data)["type"] == "response.done":
                        break

    asyncio.run(run())
    client.flush()
    _check_ws_trace(fake, mic)


def test_manual_websocket_wrappers(client, fake, rtserver, monkeypatch):
    """Weave's wrap_websocket_connect / wrap_websocket_async, with autopatching off."""
    import websockets

    from weft.integrations import wrap_websocket_async, wrap_websocket_connect

    monkeypatch.setenv("WEFT_REALTIME_HOSTS", "")

    async def run():
        connect = wrap_websocket_connect(websockets.connect)
        async with connect(rtserver.url()) as ws:
            await ws.send(json.dumps({"type": "response.create"}))
            async for raw in ws:
                if json.loads(raw)["type"] == "response.done":
                    break
        ws2 = wrap_websocket_async(await websockets.connect(rtserver.url()))
        await ws2.recv()
        await ws2.close()

    asyncio.run(run())
    client.flush()
    assert len(by_op(fake, "openai.realtime.session")) == 2
    [resp] = by_op(fake, "openai.realtime.response")
    assert resp["output"]["status"] == "completed"


def test_openai_agents_realtime_runner_is_traced(client, fake, rtserver):
    """The real OpenAI Agents SDK (``agents.realtime.RealtimeRunner``, the route
    Weave's realtime page documents for Python) connects with ``websockets.connect``."""
    pytest.importorskip("agents.realtime")
    from agents.realtime import RealtimeAgent, RealtimeRunner
    from agents.realtime.model_inputs import RealtimeModelSendRawMessage

    assert patch_openai_realtime()
    mic = pcm16(200)

    async def run():
        agent = RealtimeAgent(name="Assistant", instructions="Be brief.")
        runner = RealtimeRunner(starting_agent=agent, config={"model_settings": {"model_name": "gpt-realtime"}})
        session = await runner.run(model_config={"url": rtserver.url(), "api_key": "sk-test"})
        async with session:
            await session.send_audio(mic, commit=True)
            await session.model.send_event(RealtimeModelSendRawMessage(message={"type": "response.create"}))

            async def consume():
                async for ev in session:
                    if ev.type == "agent_end":
                        return

            await asyncio.wait_for(consume(), 10)

    asyncio.run(run())
    client.flush()
    _check_ws_trace(fake, mic)
