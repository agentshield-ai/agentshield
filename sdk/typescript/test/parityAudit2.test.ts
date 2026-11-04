/**
 * Second parity audit follow-ups: TypeSafe, registerCustomRuntime,
 * conversation links, WEFT_DEBUG_HTTP and WEFT_DISABLE_WAL_SENDER, against
 * fakes and the in-process fake Weft server.
 */
import { mkdtempSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import * as weft from '../src/index.js';
import { FakeWeft } from './fakeServer.js';

let fake: FakeWeft;
let client: weft.WeftClient;

beforeEach(async () => {
  fake = await new FakeWeft().start();
  client = await weft.init('ts-entity/audit2', { serverUrl: fake.url, sender: { flushIntervalMs: 5 }, settings: { printCallLink: false }, autopatchSettings: {} });
});

afterEach(async () => {
  await weft.finish();
  weft.configureIntegrations();
  for (const k of ['WEFT_DEBUG_HTTP', 'WEFT_ENABLE_WAL', 'WEFT_WAL_DIR', 'WEFT_DISABLE_WAL_SENDER', 'OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT']) delete process.env[k];
  await fake.close();
});

async function settle(c: weft.WeftClient = client): Promise<void> {
  await new Promise((r) => setTimeout(r, 15));
  await c.flush();
}

// ------------------------------------------------------------------ TypeSafe
class TypeSafeAPIError extends Error {
  override name = 'UnprocessableEntityError';
  status = 422;
  headers = new Headers({ 'x-typesafe-request-id': 'req_err' });
  constructor() {
    super('422 {"detail":"secret customer text"}');
  }
}

/** Shaped like @typesafe-ai/sdk: systemOne returns a Stainless-style APIPromise. */
class TypeSafeClient {
  defaultModel = 'jev-latest';
  requests: any[] = [];
  systemOne(request: any, _options?: unknown): any {
    this.requests.push(request);
    const body = { answers: { q0: 'yes', q1: 3 }, model: 'jev-2', usage: { input_tokens: 42, output_tokens: 7 } };
    const response = Promise.resolve({ data: body, request_id: 'req_123' });
    if (request.state === 'boom') {
      const err = Promise.reject(new TypeSafeAPIError());
      err.catch(() => {});
      return Object.assign(err, { withResponse: () => err });
    }
    const p: any = response.then((r) => r.data);
    p.withResponse = () => response;
    return p;
  }
}

describe('TypeSafe', () => {
  it('records systemOne as an LLM span in its own turn, without provider error text', async () => {
    const mod = { TypeSafeClient };
    expect(weft.instrumentTypeSafe(mod)).toBe(true);
    const ts = new TypeSafeClient();
    const out = await ts.systemOne({ state: { ticket: 'refund please', amount: 20 }, questions: [{ text: 'Is it a refund?' }, { text: 'Amount?', type: 'int' }] });
    expect(out.answers).toEqual({ q0: 'yes', q1: 3 });
    await expect(ts.systemOne({ state: 'boom', questions: { q: { text: 'x' } }, model: 'jev-3' })).rejects.toThrow(/secret/);
    await settle();
    const llms = fake.callsByOp('chat').sort((a, b) => a.started_at.localeCompare(b.started_at));
    expect(llms).toHaveLength(2);
    const [ok, bad] = llms;
    const turns = new Map(fake.callsByOp('invoke_agent').map((t) => [t.id, t]));
    expect(turns.get(ok.parent_id)!.display_name).toBe('invoke_agent typesafe SDK');
    expect(ok.inputs.model).toBe('jev-latest');
    expect(JSON.parse(ok.inputs.messages[0].content)).toEqual({ amount: 20, ticket: 'refund please' });
    expect(JSON.parse(ok.attributes.typesafe.questions)).toEqual([{ id: 0, text: 'Is it a refund?' }, { id: 1, text: 'Amount?', type: 'int' }]);
    expect(ok.attributes.gen_ai.provider.name).toBe('typesafe');
    expect(ok.attributes.gen_ai.response.id).toBe('req_123');
    expect(ok.attributes.weft.integration).toBe('typesafe');
    expect(JSON.parse(ok.output.choices[0].message.content)).toEqual({ q0: 'yes', q1: 3 });
    expect(ok.summary.usage['jev-2'].prompt_tokens).toBe(42);
    expect(bad.inputs.model).toBe('jev-3');
    expect(bad.error).toMatch(/^UnprocessableEntityError/);
    expect(JSON.stringify(bad) + JSON.stringify(turns.get(bad.parent_id))).not.toMatch(/secret/);
    expect(bad.attributes.http.response.status_code).toBe(422);
    expect(bad.attributes.gen_ai.response.id).toBe('req_err');
  });

  it('nests in the current turn and honours capture settings', async () => {
    const ts = weft.wrapTypeSafe(new TypeSafeClient());
    const conv = weft.startConversation({ conversationId: 'conv-ts' });
    const turn = conv.startTurn({ agentName: 'support' });
    turn.start();
    await ts.systemOne({ state: 'inside a turn', questions: ['q'] });
    turn.end();
    conv.end();
    process.env.OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT = 'false';
    await ts.systemOne({ state: 'private state', questions: ['private question'] });
    delete process.env.OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT;
    const quiet = weft.wrapTypeSafe(new TypeSafeClient(), { captureContent: false });
    await quiet.systemOne({ state: 'also private', questions: ['q'] });
    weft.configureIntegrations({ typesafe: { enabled: false } });
    await ts.systemOne({ state: 'not traced', questions: ['q'] });
    await settle();
    const llms = fake.callsByOp('chat').sort((a, b) => a.started_at.localeCompare(b.started_at));
    expect(llms).toHaveLength(3);
    expect(llms[0].parent_id).toBe(turn.callId);
    expect(llms[0].attributes.gen_ai.conversation.id).toBe('conv-ts');
    expect(JSON.stringify(fake.callsByOp('chat'))).not.toMatch(/private|not traced/);
    expect(llms.every((c) => !c.attributes.weft?.parent_call)).toBe(true);
  });

  it('inside an op starts its own trace linked to the op by weft.parent_call.*', async () => {
    const ts = weft.wrapTypeSafe(new TypeSafeClient());
    const classify = weft.trace(async function classify(msg: string) {
      return (await ts.systemOne({ state: msg, questions: ['refund?'] })).answers;
    });
    await classify('refund please');
    let helperTurn = '';
    const handle = weft.trace(async function handle(msg: string) {
      const conv = weft.startConversation({ conversationId: 'conv-op' });
      const turn = conv.startTurn({ agentName: 'helper' });
      await turn.run(async () => {
        await ts.systemOne({ state: msg, questions: ['q'] });
      });
      helperTurn = turn.callId!;
      conv.end();
    });
    await handle('turn inside an op');
    await settle();
    const [op] = fake.callsByOp('classify');
    const [handleCall] = fake.callsByOp('handle');
    const llms = fake.callsByOp('chat').sort((a, b) => a.started_at.localeCompare(b.started_at));
    expect(llms).toHaveLength(2);
    const [underOp, underTurn] = llms;
    const turns = new Map(fake.callsByOp('invoke_agent').map((t) => [t.id, t]));
    const own = turns.get(underOp.parent_id)!;
    expect(own.display_name).toBe('invoke_agent typesafe SDK');
    expect(own.parent_id).toBeNull();
    expect(own.trace_id).toBe(underOp.trace_id);
    expect(own.trace_id).not.toBe(op.trace_id);
    const link = { id: op.id, trace_id: op.trace_id };
    expect(own.attributes.weft.parent_call).toEqual(link);
    expect(underOp.attributes.weft.parent_call).toEqual(link);
    expect([...fake.calls.values()].filter((c: any) => c.parent_id === op.id)).toHaveLength(0);
    // In a running turn inside an op: under the turn, linked to the op (not the turn).
    expect(underTurn.parent_id).toBe(helperTurn);
    expect(underTurn.attributes.weft.parent_call).toEqual({ id: handleCall.id, trace_id: handleCall.trace_id });
  });
});

// ------------------------------------------------------------------ custom runtimes, links
describe('registerCustomRuntime and conversation links', () => {
  it('PUTs the runtime and returns {data, error}', async () => {
    const res = await client.registerCustomRuntime({
      name: 'my-vllm',
      baseUrl: 'https://vllm.internal/v1',
      runtimeIds: ['llama-3-8b', { id: 'qwen2-7b', maxTokens: 8192 }],
      apiKeySecret: 'VLLM_KEY',
      headers: { 'X-Team': 'ml' },
    });
    const put = fake.requests.find((r) => r.method === 'PUT')!;
    expect(put.path).toBe('/api/v1/runtimes/my-vllm');
    expect(put.body.project_id).toBe('ts-entity/audit2');
    expect(put.body).toEqual({ project_id: 'ts-entity/audit2', base_url: 'https://vllm.internal/v1', api_key_secret: 'VLLM_KEY', headers: { 'X-Team': 'ml' }, runtime_ids: [{ id: 'llama-3-8b' }, { id: 'qwen2-7b', max_tokens: 8192 }] });
    expect(res.error).toBeNull();
    expect(res.data.runtime_ids.map((r) => r.playground_id)).toEqual(['my-vllm/llama-3-8b', 'my-vllm/qwen2-7b']);
  });

  it('builds conversation URLs', () => {
    expect(weft.agentConversationPath('e', 'p', 'conv/1 a')).toBe(`${fake.url}/e/p/conversations/conv%2F1%20a`);
    expect(client.conversationUrl('c-1')).toBe(`${fake.url}/ts-entity/audit2/conversations/c-1`);
    expect(weft.agentConversationPath('e', 'p', 'x', 'https://weft.example/')).toBe('https://weft.example/e/p/conversations/x');
  });
});

// ------------------------------------------------------------------ WEFT_DEBUG_HTTP, WEFT_DISABLE_WAL_SENDER
describe('environment', () => {
  it('WEFT_DEBUG_HTTP prints requests and responses to stderr, Authorization redacted', async () => {
    const lines: string[] = [];
    const spy = vi.spyOn(process.stderr, 'write').mockImplementation((s: any) => {
      lines.push(String(s));
      return true;
    });
    try {
      process.env.WEFT_DEBUG_HTTP = '1';
      const c = await weft.init('ts-entity/dbg', { serverUrl: fake.url, apiKey: 'sk-secret', settings: { printCallLink: false } });
      const f = weft.trace(function f(x: number) {
        return x + 1;
      });
      await f(1);
      await c.flush();
      await expect(c.serverInfo()).resolves.toBeTruthy();
      await expect((c as any).http.post('/no/such/path', { a: 1 })).rejects.toThrow();
      delete process.env.WEFT_DEBUG_HTTP;
      const before = lines.length;
      await c.serverInfo();
      expect(lines.length).toBe(before);
    } finally {
      spy.mockRestore();
    }
    const err = lines.join('');
    expect(err).toContain(`URL: ${fake.url}/api/v1/spans/batch`);
    expect(err).toContain('Method: POST');
    expect(err).toContain('----- Response below -----');
    expect(err).toContain('Status Code: 200');
    expect(err).toMatch(/Status Code: 404/);
    expect(err).toMatch(/Authorization: <Redacted>/i);
    expect(err).not.toContain('sk-secret');
  });

  it('WEFT_DISABLE_WAL_SENDER writes to the WAL and sends nothing; a later run sends it', async () => {
    const dir = mkdtempSync(join(tmpdir(), 'weft-wal-'));
    process.env.WEFT_ENABLE_WAL = 'true';
    process.env.WEFT_WAL_DIR = dir;
    process.env.WEFT_DISABLE_WAL_SENDER = 'true';
    const c = await weft.init('ts-entity/wal', { serverUrl: fake.url, sender: { flushIntervalMs: 5 }, settings: { printCallLink: false } });
    const g = weft.trace(function g(x: number) {
      return x * 2;
    });
    await g(2);
    await c.flush();
    expect(fake.requests.filter((r) => r.path === '/api/v1/spans/batch')).toHaveLength(0);
    expect((c as any).sender.wal.count()).toBeGreaterThanOrEqual(2);
    delete process.env.WEFT_DISABLE_WAL_SENDER;
    const c2 = await weft.init('ts-entity/wal', { serverUrl: fake.url, sender: { flushIntervalMs: 5 }, settings: { printCallLink: false } });
    await settle(c2);
    const [call] = fake.callsByOp('g');
    expect(call.output).toBe(4);
  });
});
