/**
 * Runs against a real Weft server when WEFT_TEST_SERVER_URL is set:
 *   WEFT_TEST_SERVER_URL=http://127.0.0.1:6348 npx vitest run test/server.integration.test.ts
 */
import { randomUUID } from 'node:crypto';
import { afterAll, beforeAll, describe, expect, it } from 'vitest';
import * as weft from '../src/index.js';
import { digest } from '../src/canonical.js';

const URL = process.env.WEFT_TEST_SERVER_URL;
let client: weft.WeftClient;

describe.skipIf(!URL)('real server', () => {
  beforeAll(async () => {
    client = await weft.init(`it/ts-${randomUUID().slice(0, 8)}`, { serverUrl: URL, apiKey: process.env.WEFT_TEST_API_KEY });
  });
  afterAll(() => weft.finish());

  it('traces, rolls up usage and agrees on digests', async () => {
    const llm = weft.op(function llm(prompt: string) {
      weft.addUsage(weft.getCurrentCall(), 'gpt-4o-mini', 100, 20);
      return prompt.split('').reverse().join('');
    });
    const agent = weft.op(async function agent(q: string) {
      return llm(llm(q));
    });
    expect(await weft.withThread('ts-thread', () => agent('hello <&> é'))).toBe('hello <&> é');
    const [root] = await client.getCalls({ filter: { op_names: ['agent'] } });
    expect(root.summary.usage['gpt-4o-mini'].requests).toBe(2);
    expect(root.summary.usage['gpt-4o-mini'].total_tokens).toBe(240);
    expect(root.threadId).toBe('ts-thread');
    expect(root.turnId).toBe(root.id);
    const meta = (agent as any).__weftOp;
    const obj = (await client.http.post('/obj/read', { project_id: client.projectId, object_id: 'agent', digest: 'latest' })).obj;
    expect(obj.digest).toBe(client.opRef(meta).digest);
    expect(obj.digest).toBe(digest({ _type: 'Op', name: 'agent', code: meta.code, language: 'javascript', signature: meta.signature }));
    const kids = await client.getCalls({ filter: { parent_ids: [root.id] } });
    expect(kids.map((k) => k.opBaseName)).toEqual(['llm', 'llm']);
  });

  it('runs an evaluation whose rows resolve server side', async () => {
    const model = weft.op(async function upper({ datasetRow }: any) {
      return String(datasetRow.q).toUpperCase();
    });
    const exact = weft.op(function exact({ modelOutput, datasetRow }: any) {
      return modelOutput === datasetRow.expected;
    });
    const ev = new weft.Evaluation({
      name: 'ts-eval',
      dataset: new weft.Dataset({ name: 'ts-ds', rows: [{ q: 'a', expected: 'A' }, { q: 'b', expected: 'c' }] }),
      scorers: [exact],
    });
    const summary = await ev.evaluate({ model });
    expect(summary.exact).toEqual({ true_count: 1, true_fraction: 0.5 });
    await client.flush();
    const [root] = await client.getCalls({ filter: { op_names: ['Evaluation.evaluate'] } });
    expect(root.output).toEqual(summary);
    const pas = await client.getCalls({ filter: { op_names: ['Evaluation.predict_and_score'] } });
    const vals = (await client.http.post('/refs/read_batch', { refs: pas.map((p) => p.inputs.example) })).vals;
    expect(vals.map((v: any) => v.q).sort()).toEqual(['a', 'b']);
    const cmp = await client.http.post('/evaluations/compare', { project_id: client.projectId, evaluation_call_ids: [root.id] });
    expect(cmp.rows).toHaveLength(2);
    const ds = await weft.ref('ts-ds').get();
    expect(ds.rows).toHaveLength(2);
  });

  it('uploads files and attaches feedback', async () => {
    const png = new Uint8Array([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 9, 9, 9]);
    const f = weft.op(function img(data: Uint8Array) {
      return data.length;
    });
    const [, call] = await f.call(png);
    await call!.feedback.addReaction('👍');
    const got = (await client.getCall(call!.id, { includeFeedback: true }))!;
    expect(got.raw.feedback).toHaveLength(1);
    const media = got.inputs.data as any;
    const res = await client.http.raw('POST', '/file/content', JSON.stringify({ project_id: client.projectId, digest: media.file_digest }));
    expect([...new Uint8Array(await res.arrayBuffer())]).toEqual([...png]);
  });
});
