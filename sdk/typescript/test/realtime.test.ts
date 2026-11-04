/**
 * OpenAI Realtime: WAV audio capture, and the OpenAI Agents SDK's
 * `RealtimeSession` (`@openai/agents-realtime`), with a fake session whose
 * shape follows the real package (session `transport_event`s carry raw server
 * events; the transport's `sendEvent` carries client events, `sendAudio`
 * appends base64 PCM through it, `connection_change` reports a disconnect) and
 * events in the GA Realtime format.
 */
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import * as weft from '../src/index.js';
import { FakeWeft } from './fakeServer.js';
import { RealtimeSession, SESSION, b64, pcm16, responseEvents } from './realtimeFixtures.js';

let fake: FakeWeft;
let client: weft.WeftClient;

beforeEach(async () => {
  fake = await new FakeWeft().start();
  client = await weft.init('ts-entity/realtime', { serverUrl: fake.url, sender: { flushIntervalMs: 5 }, settings: { printSpanLink: false }, autopatchSettings: {} });
});

afterEach(async () => {
  await weft.finish();
  weft.configureIntegrations();
  delete process.env.WEFT_REALTIME_MAX_AUDIO_BYTES;
  await fake.close();
});

async function settle(): Promise<void> {
  await new Promise((r) => setTimeout(r, 10));
  await client.flush();
}

function wavOf(media: any): { tag: number; channels: number; rate: number; width: number; data: Buffer } {
  expect(media).toMatchObject({ _type: 'Audio', mime_type: 'audio/wav' });
  const w = fake.files.get(media.file_digest)!;
  expect(w.subarray(0, 4).toString()).toBe('RIFF');
  const fmtLen = w.readUInt32LE(16);
  let o = 20 + fmtLen;
  while (w.subarray(o, o + 4).toString() !== 'data') o += 8 + w.readUInt32LE(o + 4);
  return { tag: w.readUInt16LE(20), channels: w.readUInt16LE(22), rate: w.readUInt32LE(24), width: w.readUInt16LE(34) / 8, data: w.subarray(o + 8, o + 8 + w.readUInt32LE(o + 4)) };
}

describe('Realtime audio', () => {
  it('saves VAD-cut input audio and the model audio as WAV', async () => {
    const mic = pcm16(1000);
    const out = pcm16(500, 220);
    const rt = new weft.RealtimeTracer();
    rt.onServerEvent({ type: 'session.created', event_id: 'e1', session: SESSION });
    for (let i = 0; i < mic.length; i += 4800) rt.onClientEvent({ type: 'input_audio_buffer.append', audio: b64(mic.subarray(i, i + 4800)) });
    rt.onServerEvent({ type: 'input_audio_buffer.speech_started', audio_start_ms: 200, item_id: 'item_u1' });
    rt.onServerEvent({ type: 'input_audio_buffer.speech_stopped', audio_end_ms: 700, item_id: 'item_u1' });
    rt.onServerEvent({ type: 'input_audio_buffer.committed', previous_item_id: null, item_id: 'item_u1' });
    rt.onServerEvent({ type: 'conversation.item.added', item: { id: 'item_u1', type: 'message', status: 'completed', role: 'user', content: [{ type: 'input_audio', transcript: null }] } });
    for (const e of responseEvents('resp_1', 'item_a1', out)) rt.onServerEvent(JSON.stringify(e));
    rt.close();
    await settle();
    const [resp] = fake.callsByOp('openai.realtime.response');
    const part = resp.inputs.input[0].content[0];
    expect(part.audio_bytes).toBe(24000);
    const inWav = wavOf(part.audio);
    expect([inWav.tag, inWav.channels, inWav.rate, inWav.width]).toEqual([1, 1, 24000, 2]);
    expect(inWav.data.equals(mic.subarray(200 * 48, 700 * 48))).toBe(true);
    const opart = resp.output.output[0].content[0];
    expect(opart).toMatchObject({ type: 'output_audio', transcript: 'Hello there.', audio_bytes: out.length });
    expect(wavOf(opart.audio).data.equals(out)).toBe(true);
    expect(resp.summary.usage['gpt-realtime'].input_audio_tokens).toBe(120);
  });

  it('cuts audio at commits without VAD, writes G.711 WAVs, converts client items, and honours the cap', async () => {
    const rt = new weft.RealtimeTracer();
    rt.onServerEvent({ type: 'session.created', session: { id: 's', model: 'gpt-4o-realtime-preview', input_audio_format: 'g711_ulaw', output_audio_format: 'pcm16' } });
    const first = Buffer.alloc(2048, 7);
    const second = Buffer.alloc(1024, 9);
    rt.onClientEvent({ type: 'input_audio_buffer.append', audio: b64(first) });
    rt.onServerEvent({ type: 'input_audio_buffer.committed', item_id: 'item_1' });
    rt.onClientEvent({ type: 'input_audio_buffer.append', audio: b64(Buffer.alloc(100, 1)) });
    rt.onClientEvent({ type: 'input_audio_buffer.clear' });
    rt.onClientEvent({ type: 'input_audio_buffer.append', audio: b64(second) });
    rt.onServerEvent({ type: 'input_audio_buffer.committed', item_id: 'item_2' });
    for (const id of ['item_1', 'item_2']) rt.onServerEvent({ type: 'conversation.item.created', item: { id, type: 'message', role: 'user', content: [{ type: 'input_audio', transcript: null }] } });
    rt.onClientEvent({ type: 'conversation.item.create', item: { type: 'message', role: 'user', content: [{ type: 'input_audio', audio: b64(Buffer.alloc(300, 3)) }] } });
    rt.onServerEvent({ type: 'response.created', response: { id: 'r1' } });
    rt.onServerEvent({ type: 'response.done', response: { id: 'r1', status: 'completed', output: [] } });
    rt.close();

    process.env.WEFT_REALTIME_MAX_AUDIO_BYTES = '1000';
    const capped = new weft.RealtimeTracer();
    capped.onServerEvent({ type: 'session.created', session: SESSION });
    for (const e of responseEvents('r2', 'i2', pcm16(200))) capped.onServerEvent(e);
    capped.close();
    const off = new weft.RealtimeTracer({ maxAudioBytes: 0 });
    off.onServerEvent({ type: 'session.created', session: SESSION });
    for (const e of responseEvents('r3', 'i3', pcm16(200))) off.onServerEvent(e);
    off.close();
    await settle();
    const resps = fake.callsByOp('openai.realtime.response');
    const r1 = resps.find((r) => r.output.id === 'r1');
    const [w1, w2, w3] = r1.inputs.input.map((it: any) => wavOf(it.content[0].audio));
    expect([w1.tag, w1.rate, w1.width]).toEqual([7, 8000, 1]);
    expect(w1.data.equals(first) && w2.data.equals(second) && w3.data.equals(Buffer.alloc(300, 3))).toBe(true);
    const p2 = resps.find((r) => r.output.id === 'r2').output.output[0].content[0];
    expect(p2.audio_truncated).toBe(true);
    expect(wavOf(p2.audio).data.length).toBe(1000);
    const r3 = resps.find((r) => r.output.id === 'r3');
    expect(r3.output.output[0].content[0].audio).toBeUndefined(); // capture off: sizes only
    expect(r3.output.audio_bytes).toBe(9600);
  });
});

describe('OpenAI Agents SDK RealtimeSession', () => {
  it('patchRealtimeSession traces every session: client events, raw server events, audio, disconnect', async () => {
    expect(await weft.patchRealtimeSession({ RealtimeSession })).toBe(true);
    expect(await weft.patchRealtimeSession({ RealtimeSession })).toBe(true); // idempotent
    const session = new RealtimeSession({ name: 'Assistant' }, { model: 'gpt-realtime' });
    await session.connect({ apiKey: 'sk-test' });
    const mic = pcm16(300);
    session.sendAudio(new Uint8Array(mic).buffer, { commit: true });
    const t = session.transport;
    t.serverSend({ type: 'input_audio_buffer.speech_started', event_id: 'a', audio_start_ms: 0, item_id: 'item_u' });
    t.serverSend({ type: 'input_audio_buffer.speech_stopped', event_id: 'b', audio_end_ms: 300, item_id: 'item_u' });
    t.serverSend({ type: 'input_audio_buffer.committed', event_id: 'c', item_id: 'item_u', previous_item_id: null });
    t.serverSend({ type: 'conversation.item.added', event_id: 'd', item: { id: 'item_u', type: 'message', role: 'user', content: [{ type: 'input_audio', transcript: null }] } });
    const out = pcm16(100, 330);
    for (const e of responseEvents('resp_a', 'item_a', out)) t.serverSend(e);
    t.serverSend({ type: 'response.output_item.done', event_id: 'f', response_id: 'resp_b', item: { type: 'function_call', name: 'get_weather', call_id: 'call_9', arguments: '{"city":"Oslo"}' } });
    t.sendEvent({ type: 'conversation.item.create', item: { type: 'function_call_output', call_id: 'call_9', output: '{"temp":4}' } });
    session.sendMessage('And tomorrow?');
    for (const e of responseEvents('resp_b', 'item_b', pcm16(50))) t.serverSend(e);
    session.close();
    // A reconnect starts a new session call.
    await session.connect({ apiKey: 'sk-test' });
    session.close();
    await settle();
    const sessions = fake.callsByOp('openai.realtime.session');
    expect(sessions).toHaveLength(2);
    const sess = sessions.find((s) => s.output?.responses === 2);
    expect(sess.inputs.model).toBe('gpt-realtime');
    expect(sess.ended_at).toBeTruthy();
    const resps = fake.callsByOp('openai.realtime.response').filter((r) => r.parent_id === sess.id);
    const ra = resps.find((r) => r.output.id === 'resp_a');
    expect(wavOf(ra.inputs.input[0].content[0].audio).data.equals(mic)).toBe(true);
    expect(wavOf(ra.output.output[0].content[0].audio).data.equals(out)).toBe(true);
    const rb = resps.find((r) => r.output.id === 'resp_b');
    expect(rb.inputs.input.map((i: any) => i.type)).toEqual(['function_call_output', 'message']);
    expect(rb.inputs.input[1].content[0].text).toBe('And tomorrow?');
    const [tool] = fake.callsByOp('openai.realtime.tool.get_weather');
    expect(tool.output).toEqual({ temp: 4 });
    expect(t.sent.map((e) => e.type)).toEqual(['input_audio_buffer.append', 'input_audio_buffer.commit', 'conversation.item.create', 'conversation.item.create', 'response.create']);
  });

  it('wrapRealtimeSession traces one session; a disabled integration records nothing', async () => {
    const one = weft.wrapRealtimeSession(new RealtimeSession({}, {}));
    await one.connect({});
    for (const e of responseEvents('r1', 'i1', pcm16(20))) one.transport.serverSend(e);
    one.transport.close(); // connection_change: disconnected
    weft.configureIntegrations({ openai_realtime: { enabled: false } });
    const off = weft.wrapRealtimeSession(new RealtimeSession({}, {}));
    await off.connect({});
    off.close();
    await settle();
    const [sess] = fake.callsByOp('openai.realtime.session');
    expect(fake.callsByOp('openai.realtime.session')).toHaveLength(1);
    expect(sess.inputs.model).toBe('gpt-realtime');
    expect(sess.ended_at).toBeTruthy();
  });
});
