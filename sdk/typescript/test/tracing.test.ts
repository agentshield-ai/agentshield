import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import * as weft from '../src/index.js';
import { parseParams } from '../src/op.js';
import { FakeWeft } from './fakeServer.js';

let fake: FakeWeft;
let client: weft.WeftClient;

beforeEach(async () => {
  fake = await new FakeWeft().start();
  client = await weft.init('ts-entity/ts-proj', { serverUrl: fake.url, sender: { flushIntervalMs: 5, backoffMs: 5 } });
});

afterEach(async () => {
  await weft.finish();
  await fake.close();
});

describe('op tracing', () => {
  it('records nested sync and async calls with parent/trace ids', async () => {
    const inner = weft.op(function inner(x: number) {
      return x * 2;
    });
    const outer = weft.op(async function outer(x: number, y = 3) {
      await new Promise((r) => setTimeout(r, 5));
      const [a, b] = await Promise.all([Promise.resolve(inner(x)), Promise.resolve(inner(y))]);
      return a + b;
    });
    expect(await outer(2, 4)).toBe(12);
    await client.flush();
    const [o] = fake.callsByOp('outer');
    const kids = fake.callsByOp('inner');
    expect(o.inputs).toEqual({ x: 2, y: 4 });
    expect(o.output).toBe(12);
    expect(o.parent_id).toBeNull();
    expect(kids).toHaveLength(2);
    for (const k of kids) {
      expect(k.parent_id).toBe(o.id);
      expect(k.trace_id).toBe(o.trace_id);
    }
    expect(o.op_name).toMatch(/^weft:\/\/\/ts-entity\/ts-proj\/op\/outer:/);
    expect(o.id[14]).toBe('7'); // uuidv7
    const opVal = fake.objs.get('outer')![0];
    expect(opVal.val._type).toBe('Op');
    expect(opVal.val.code).toContain('function outer');
    expect(o.op_name.endsWith(':' + opVal.digest)).toBe(true); // local digest == server digest
    expect(fake.requests.some((r) => r.path === '/call/upsert_batch')).toBe(true);
  });

  it('keeps parents across concurrent async work', async () => {
    const leaf = weft.op(async function leaf(i: number) {
      await new Promise((r) => setTimeout(r, Math.random() * 10));
      return i;
    });
    const root = weft.op(async function root(n: number) {
      return (await Promise.all(Array.from({ length: n }, (_, i) => leaf(i)))).length;
    });
    await Promise.all([root(3), root(4)]);
    await client.flush();
    const roots = fake.callsByOp('root');
    for (const r of roots) expect(fake.children(r.id)).toHaveLength(r.inputs.n);
  });

  it('records and rethrows errors (sync and async)', async () => {
    const bad = weft.op(function bad() {
      throw new TypeError('nope');
    });
    const abad = weft.op(async function abad() {
      throw new RangeError('async nope');
    });
    expect(() => bad()).toThrow('nope');
    await expect(abad()).rejects.toThrow('async nope');
    await client.flush();
    expect(fake.callsByOp('bad')[0].exception).toMatch(/^TypeError: nope/);
    expect(fake.callsByOp('abad')[0].exception).toMatch(/^RangeError: async nope/);
  });

  it('accumulates async generators and sync generators', async () => {
    const child = weft.op(function child(i: number) {
      return i;
    });
    const agen = weft.op(async function* agen(n: number) {
      for (let i = 0; i < n; i++) yield child(i);
    });
    const sgen = weft.op(function* sgen() {
      yield 'a';
      yield 'b';
    }, { accumulator: (acc: string | undefined, x) => (acc ?? '') + x });
    const items: number[] = [];
    for await (const x of agen(3)) items.push(x as number);
    expect(items).toEqual([0, 1, 2]);
    expect([...sgen()]).toEqual(['a', 'b']);
    await client.flush();
    const [g] = fake.callsByOp('agen');
    expect(g.output).toEqual([0, 1, 2]);
    expect(fake.callsByOp('child').every((c) => c.parent_id === g.id)).toBe(true);
    expect(fake.callsByOp('sgen')[0].output).toBe('ab');
  });

  it('supports .call(), display names and postprocessing', async () => {
    const f = weft.op(
      function f(q: string, secret: string) {
        return `hi ${q}`;
      },
      {
        name: 'renamed',
        callDisplayName: (call) => `q=${call.inputs.q}`,
        postprocessInputs: ({ secret, ...rest }) => rest,
        postprocessOutput: (o) => ({ len: String(o).length }),
      },
    );
    const [res, call] = await f.call('x', 's3cret');
    expect(res).toBe('hi x');
    expect(call!.id).toBeTruthy();
    const failing = weft.op(function failing() {
      throw new Error('captured');
    });
    const [r2, c2] = await failing.call();
    expect(r2).toBeUndefined();
    expect(c2!.exception).toMatch(/^Error: captured/);
    await client.flush();
    const [c] = fake.callsByOp('renamed');
    expect(c.display_name).toBe('q=x');
    expect(c.inputs).toEqual({ q: 'x' });
    expect(c.output).toEqual({ len: 4 });
  });

  it('destructured single-object params become named inputs', async () => {
    const m = weft.op(async ({ datasetRow }: { datasetRow: { q: string } }) => datasetRow.q, { name: 'model' });
    await m({ datasetRow: { q: 'z' } });
    await client.flush();
    expect(fake.callsByOp('model')[0].inputs).toEqual({ datasetRow: { q: 'z' } });
    expect(parseParams(function (a: number, b = 2, ...rest: number[]) {} as any).names).toEqual(['a', 'b', 'rest']);
    expect(parseParams(((x: number) => x) as any).names).toEqual(['x']);
  });

  it('applies attributes, threads and usage', async () => {
    const tool = weft.op(function tool() {
      weft.addUsage(weft.getCurrentCall(), 'gpt-4o-mini', 10, 5);
      return 1;
    });
    const turn = weft.op(async function turn(i: number) {
      tool();
      return i;
    });
    await weft.withThread('conv-1', async () => {
      await weft.attributes({ env: 'test' }, async () => {
        await turn(1);
        await turn(2);
      });
    });
    await client.flush();
    const turns = fake.callsByOp('turn');
    for (const t of turns) {
      expect(t.thread_id).toBe('conv-1');
      expect(t.turn_id).toBe(t.id);
      expect(t.attributes.env).toBe('test');
    }
    for (const t of fake.callsByOp('tool')) {
      expect(t.turn_id).toBe(t.parent_id);
      expect(t.summary.usage['gpt-4o-mini']).toEqual({ requests: 1, prompt_tokens: 10, completion_tokens: 5, total_tokens: 15 });
    }
  });

  it('binds methods with op(this, fn)', async () => {
    class Agent {
      prefix = '>';
      run = weft.op(this, async function run(this: Agent, q: string) {
        return this.prefix + q;
      });
    }
    expect(await new Agent().run('a')).toBe('>a');
    await client.flush();
    const [c] = fake.callsByOp('Agent.run');
    expect(c.inputs).toEqual({ self: '<Agent>', q: 'a' });
  });

  it('uploads binary data as files', async () => {
    const png = new Uint8Array([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 1, 2, 3]);
    const f = weft.op(function img(data: Uint8Array) {
      return data.length;
    });
    f(png);
    await client.flush();
    const media = fake.callsByOp('img')[0].inputs.data;
    expect(media._type).toBe('Image');
    expect(media.mime_type).toBe('image/png');
    expect([...fake.files.get(media.file_digest)!]).toEqual([...png]);
  });

  it('is a no-op without init and never blocks when the server is down', async () => {
    await weft.finish();
    const f = weft.op(function f(x: number) {
      return x + 1;
    });
    expect(f(1)).toBe(2);
    const c = await weft.init('p', { serverUrl: 'http://127.0.0.1:9', sender: { backoffMs: 1, flushIntervalMs: 1 } });
    const t0 = Date.now();
    for (let i = 0; i < 50; i++) f(i);
    expect(Date.now() - t0).toBeLessThan(1000);
    await c.flush();
    expect(c.sender.failedRequests).toBeGreaterThan(0);
  });

  it('retries failed batches', async () => {
    fake.failPaths.set('/call/upsert_batch', 2);
    const f = weft.op(function f() {
      return 1;
    });
    f();
    await client.flush();
    expect(fake.callsByOp('f')).toHaveLength(1);
    expect(fake.requests.filter((r) => r.path === '/call/upsert_batch')).toHaveLength(3);
  });

  it('queries calls and manages feedback', async () => {
    const f = weft.op(function f(x: number) {
      return x;
    });
    f(1);
    f(2);
    const calls = await client.getCalls({ filter: { op_names: ['f'] } });
    expect(calls.map((c) => c.inputs.x)).toEqual([1, 2]);
    expect(calls[0].opBaseName).toBe('f');
    const call = (await client.getCall(calls[0].id))!;
    await call.feedback.addReaction('👍');
    await call.feedback.addNote('good');
    expect((await call.feedback.list()).map((x) => x.feedback_type)).toEqual(['weft.reaction', 'weft.note']);
    await call.setDisplayName('renamed');
    expect(fake.calls.get(call.id).display_name).toBe('renamed');
    await call.delete();
    expect(fake.calls.get(call.id).deleted_at).toBe('now');
    expect((await client.serverInfo()).version).toBe('fake');
    expect(await client.addCost('m', 1e-6, 2e-6)).toEqual([['c0', 'm']]);
  });

  it('sends the API key', async () => {
    await weft.init('keyed', { serverUrl: fake.url, apiKey: 'k1', sender: { flushIntervalMs: 1 } });
    weft.op(function g() {})();
    await weft.flush();
    const r = fake.requests.find((x) => x.path === '/call/upsert_batch')!;
    expect(r.headers.authorization).toBe('Bearer k1');
    expect(r.body.batch[0].req.start.project_id).toBe('default/keyed');
  });
});
