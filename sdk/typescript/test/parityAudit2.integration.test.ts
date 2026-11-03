/**
 * Second parity audit follow-ups against a real server: the Agents SDK
 * RealtimeSession with WAV audio, registerCustomRuntime and TypeSafe:
 *   WEFT_TEST_SERVER_URL=http://127.0.0.1:6345 npx vitest run test/parityAudit2.integration.test.ts
 */
import { randomUUID } from 'node:crypto';
import { afterAll, beforeAll, describe, expect, it } from 'vitest';
import * as weft from '../src/index.js';
import { RealtimeSession, SESSION, b64, pcm16, responseEvents } from './realtimeFixtures.js';

const URL = process.env.WEFT_TEST_SERVER_URL;
let client: weft.WeftClient;

function wavData(w: Uint8Array): Buffer {
  const b = Buffer.from(w);
  let o = 12;
  while (b.subarray(o, o + 4).toString() !== 'data') o += 8 + b.readUInt32LE(o + 4);
  return b.subarray(o + 8, o + 8 + b.readUInt32LE(o + 4));
}

describe.skipIf(!URL)('real server: parity audit 2', () => {
  beforeAll(async () => {
    client = await weft.init(`it/ts-audit2-${randomUUID().slice(0, 8)}`, { serverUrl: URL, apiKey: process.env.WEFT_TEST_API_KEY, settings: { printCallLink: false } });
  });
  afterAll(() => weft.finish());

  it('a RealtimeSession records input and output audio as WAV files', async () => {
    await weft.patchRealtimeSession({ RealtimeSession });
    const session = new RealtimeSession({ name: 'Assistant' }, { model: 'gpt-realtime' });
    await session.connect({});
    const mic = pcm16(250);
    session.sendAudio(new Uint8Array(mic).buffer, { commit: true });
    const t = session.transport;
    t.serverSend({ type: 'input_audio_buffer.committed', event_id: 'c', item_id: 'item_u', previous_item_id: null });
    t.serverSend({ type: 'conversation.item.added', event_id: 'd', item: { id: 'item_u', type: 'message', role: 'user', content: [{ type: 'input_audio', transcript: null }] } });
    const out = pcm16(120, 330);
    for (const e of responseEvents('resp_live', 'item_a', out)) t.serverSend(e);
    session.close();
    await client.flush();
    const [resp] = await client.getCalls({ filter: { op_names: ['openai.realtime.response'] } });
    const inAudio: any = (resp.inputs as any).input[0].content[0].audio;
    const outAudio: any = (resp.output as any).output[0].content[0].audio;
    expect(wavData(await client.readFile(inAudio.file_digest)).equals(mic)).toBe(true);
    expect(wavData(await client.readFile(outAudio.file_digest)).equals(out)).toBe(true);
    const [sess] = await client.getCalls({ filter: { op_names: ['openai.realtime.session'] } });
    expect((sess.inputs as any).session.model).toBe(SESSION.model);
    expect(b64(new Uint8Array([1]))).toBe('AQ==');
  });

  it('registerCustomRuntime creates and replaces a runtime', async () => {
    const r1 = await client.registerCustomRuntime({ name: 'ts-audit2-vllm', baseUrl: 'http://127.0.0.1:9/v1', runtimeIds: ['a-1', { id: 'b-2', maxTokens: 2048 }] });
    expect(r1.data.runtime_ids.map((r) => [r.playground_id, r.max_tokens])).toEqual([
      ['ts-audit2-vllm/a-1', 4096],
      ['ts-audit2-vllm/b-2', 2048],
    ]);
    const r2 = await client.registerCustomRuntime({ name: 'ts-audit2-vllm', baseUrl: 'http://127.0.0.1:9/v1', runtimeIds: ['a-1'] });
    expect(r2.data.runtime_ids.map((r) => r.id)).toEqual(['a-1']);
    expect(client.conversationUrl('c 1')).toBe(`${URL}/${client.projectId}/conversations/c%201`);
  });

  it('TypeSafe systemOne becomes an LLM span', async () => {
    class TypeSafeClient {
      defaultModel = 'jev-latest';
      systemOne(_req: any) {
        const response = Promise.resolve({ data: { answers: { a: 1 }, model: 'jev-2', usage: { input_tokens: 3, output_tokens: 1 } }, request_id: 'req_live' });
        const p: any = response.then((r) => r.data);
        p.withResponse = () => response;
        return p;
      }
    }
    const ts = weft.wrapTypeSafe(new TypeSafeClient());
    await ts.systemOne({ state: 'hello', questions: ['q'] });
    await client.flush();
    const [llm] = await client.getCalls({ filter: { op_names: ['chat'] } });
    expect((llm.attributes as any).gen_ai.response.id).toBe('req_live');
    expect((llm.summary as any).usage['jev-2'].prompt_tokens).toBe(3);
    // Inside an op: its own trace, which the server links back to the op.
    const classify = weft.op(async function classify(msg: string) {
      return (await ts.systemOne({ state: msg, questions: ['q'] })).answers;
    });
    await classify('refund');
    await client.flush();
    const [op] = await client.getCalls({ filter: { op_names: ['classify'] } });
    const headers: Record<string, string> = { 'Content-Type': 'application/json' };
    if (process.env.WEFT_TEST_API_KEY) headers.Authorization = `Bearer ${process.env.WEFT_TEST_API_KEY}`;
    const res = await fetch(`${URL}/agents/spans/query`, {
      method: 'POST',
      headers,
      body: JSON.stringify({ project_id: client.projectId, query: { $expr: { $eq: [{ $getField: 'parent_call_id' }, { $literal: op.id }] } } }),
    });
    const spans: any[] = (await res.json()).spans;
    expect(spans.map((s) => s.operation_name).sort()).toEqual(['chat', 'invoke_agent']);
    expect(spans.every((s) => s.parent_call_trace_id === op.traceId && s.trace_id !== op.traceId)).toBe(true);
  });
});
