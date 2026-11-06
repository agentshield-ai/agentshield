import { createHash } from 'node:crypto';
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
    const llm = weft.trace(function llm(prompt: string) {
      weft.addUsage(weft.getCurrentSpan(), 'gpt-4o-mini', 100, 20);
      return prompt.split('').reverse().join('');
    });
    const agent = weft.trace(async function agent(q: string) {
      return llm(llm(q));
    });
    expect(await weft.withSession('ts-thread', () => agent('hello <&> é'))).toBe('hello <&> é');
    const [root] = await client.spans({ filter: { names: ['agent'] } });
    expect(root.summary.usage['gpt-4o-mini'].requests).toBe(2);
    expect(root.summary.usage['gpt-4o-mini'].total_tokens).toBe(240);
    expect(root.sessionId).toBe('ts-thread');
    expect(root.turnId).toBe(root.id);
    const meta = (agent as any).__weftOp;
    const obj = (await client.readObject(client.projectId, 'agent'));
    expect(obj.digest).toBe(client.opRef(meta).digest);
    expect(obj.digest).toBe(digest({ _type: 'Op', name: 'agent', code: meta.code, language: 'javascript', signature: meta.signature }));
    const kids = await client.spans({ filter: { parent_ids: [root.id] } });
    expect(kids.map((k) => k.baseName)).toEqual(['llm', 'llm']);
  });

  it('runs an evaluation whose rows resolve server side', async () => {
    const model = weft.trace(async function upper({ datasetRow }: any) {
      return String(datasetRow.q).toUpperCase();
    });
    const exact = weft.trace(function exact({ modelOutput, datasetRow }: any) {
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
    const [root] = await client.spans({ filter: { names: ['Evaluation.evaluate'] } });
    expect(root.output).toEqual(summary);
    const pas = await client.spans({ filter: { names: ['Evaluation.predict_and_score'] } });
    const vals = (await client.http.post('/api/v1/refs/read', { refs: pas.map((p) => p.inputs.example) })).items;
    expect(vals.map((v: any) => v.q).sort()).toEqual(['a', 'b']);
    const cmp = await client.compareEvaluations([root.id]);
    expect(cmp.rows).toHaveLength(2);
    const ds = await weft.ref('ts-ds').get();
    expect(ds.rows).toHaveLength(2);
  });

  it('uploads files and attaches feedback', async () => {
    const png = new Uint8Array([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 9, 9, 9]);
    const f = weft.trace(function img(data: Uint8Array) {
      return data.length;
    });
    const [, call] = await f.call(png);
    await call!.feedback.addReaction('👍');
    const got = (await client.span(call!.id, { includeFeedback: true }))!;
    expect(got.raw.feedback).toHaveLength(1);
    const media = got.inputs.data as any;
    expect(media.file_digest).toBe(createHash('sha256').update(png).digest('hex'));
    expect([...(await client.readFile(media.file_digest))]).toEqual([...png]);
  });

  it('parity features against the real server', async () => {
    // redaction
    const login = weft.trace(function login(user: string, password: string) {
      return { ok: true, token: 't-1' };
    });
    login('bob', 'hunter2');
    const [lc] = await client.spans({ filter: { names: ['login'] } });
    expect(lc.inputs.password).toBe('REDACTED');
    expect(lc.output).toEqual({ ok: true, token: 'REDACTED' });

    // op loading
    const triple = weft.trace(function triple(x: number) {
      return 3 * x;
    });
    const tref = await weft.save(triple);
    await client.flush();
    const stored = await weft.get('triple');
    expect(stored).toBeInstanceOf(weft.StoredOp);
    const loaded = await weft.loadOp('triple', { trustCode: true });
    const [res, call] = await loaded.call(3);
    expect(res).toBe(9);
    expect(call!.name).toBe(tref.uri());

    // built-in scorer classes + EvaluationLogger + server-side compare
    const model = weft.trace(async function upper({ datasetRow }: any) {
      return String(datasetRow.q).toUpperCase();
    });
    const ev = new weft.Evaluation({
      name: 'ts-builtin',
      dataset: [{ q: 'a', expected: 'A' }, { q: 'b', expected: 'x' }],
      scorers: [new weft.ExactMatchScorer({ name: 'exact' }), new weft.LLMAsAJudgeScorer({ name: 'judge', model: 'mock/json', scoringPrompt: '{"correct": true} {output}' })],
    });
    const summary = await ev.evaluate({ model });
    expect(summary.exact).toEqual({ true_count: 1, true_fraction: 0.5 });
    expect(summary.judge).toBeTruthy();
    const logger = new weft.EvaluationLogger({ model: 'ts-logged', dataset: 'ts-logged-ds', name: 'ts-logger' });
    for (let i = 0; i < 3; i++) {
      const p = logger.logPrediction({ i }, i * 2);
      p.logScore('even', i % 2 === 0);
      p.finish();
    }
    const ls = await logger.logSummary();
    expect(ls.even).toEqual({ true_count: 2, true_fraction: 2 / 3 });
    await client.flush();
    const roots = await client.spans({ filter: { names: ['Evaluation.evaluate'] } });
    const loggerRoot = roots.find((r) => r.displayName === 'ts-logger')!;
    expect(loggerRoot.output).toEqual(ls);
    const evObj = (await client.readObject(client.projectId, 'ts-logger'));
    expect(loggerRoot.inputs.self).toBe(`weft:///${client.projectId}/object/ts-logger:${evObj.digest}`);
    const cmp = await client.compareEvaluations([loggerRoot.id]);
    expect(cmp.evaluations[0].model_name).toBe('ts-logged');

    // addRows
    const ds = new weft.Dataset({ name: 'ts-grow', rows: [{ i: 0 }] });
    await weft.save(ds);
    const ds2 = await ds.addRows([{ i: 1 }]);
    const back = await weft.ref('ts-grow:latest').get();
    expect(back.rows).toEqual([{ i: 0 }, { i: 1 }]);
    expect(back.rowDigests).toEqual(ds2.rowDigests);

    // applyScorer + annotations
    const reply = weft.trace(function reply(prompt: string) {
      return 'no way';
    });
    const [, rc] = await reply.call('hi');
    const polite = weft.trace(function polite({ modelOutput }: any) {
      return String(modelOutput).includes('please');
    });
    const r = await rc!.applyScorer(polite);
    expect(r.result).toBe(false);
    const spec = new weft.AnnotationSpec({ name: 'ts-stars', fieldSchema: { type: 'integer', minimum: 1, maximum: 5 } });
    await rc!.feedback.addAnnotation(spec, 5);
    const fb = await rc!.feedback.list();
    expect(fb.map((f) => f.feedback_type).sort()).toEqual(['weft.annotation.ts-stars', 'weft.scorer.polite']);
    const runnable = fb.find((f) => f.feedback_type === 'weft.scorer.polite');
    expect(runnable.span_ref).toBe(r.scoreSpan!.uri());
    await weft.save(new weft.Leaderboard({ name: 'ts-lb', columns: [{ evaluationObjectRef: ev, scorerName: 'exact', summaryMetricPath: 'true_fraction' }] }));
    const lb = await client.leaderboard('ts-lb');
    expect(lb.rows[0].values['0'].value).toBe(0.5);
    expect((await client.whoami()).user_id).toBeTruthy();
  });

  it('batch 3 features against the real server', async () => {
    const planner = weft.trace(function planner(q: string) {
      return q;
    }, { kind: 'agent', color: 'green' });
    planner('x');
    const [p] = await client.spans({ filter: { names: ['planner'] } });
    expect(p.attributes.weft).toMatchObject({ span_kind: 'agent', color: 'green' });
    const q = await client.spans({ query: { $eq: [{ $getField: 'attributes.weft.span_kind' }, { $literal: 'agent' }] } });
    expect(q.map((c) => c.id)).toEqual([p.id]);

    const report = weft.trace(function report() {
      return { doc: weft.Content.fromText('hello', { filename: 'a.txt' }), md: new weft.Markdown('# T') };
    });
    report();
    const [r] = await client.spans({ filter: { names: ['report'] } });
    const doc = r.output as any;
    expect(doc.doc._type).toBe('Content');
    expect(Buffer.from(await client.readFile(doc.doc.file_digest)).toString()).toBe('hello');

    const r0 = await weft.save({ v: 0 }, 'ts-mgmt');
    const r1 = await weft.save({ v: 1 }, 'ts-mgmt');
    await client.setAliases(r1, 'production');
    expect(await client.getAliases(r1)).toContain('production');
    expect(await weft.ref('ts-mgmt:production').get()).toEqual({ v: 1 });
    await client.removeAliases('ts-mgmt', 'production');
    expect(await client.getAliases(r1)).not.toContain('production');
    await client.addTags(r1, ['golden', 'reviewed']);
    await client.removeTags(r1, ['reviewed']);
    expect(await client.getTags(r1)).toEqual(['golden']);
    expect(await client.listTags()).toContain('golden');
    expect(await client.deleteObjectVersion(r0)).toBe(1);
    expect(await client.deleteAllObjectVersions('ts-mgmt')).toBe(1);

    const ds = new weft.Dataset({ name: 'ts-linked', rows: [{ q: 'a' }, { q: 'b' }] });
    const ev = new weft.EvaluationLogger({ model: 'm', dataset: ds, name: 'ts-linked-eval' });
    ev.logExample({ q: 'b' }, 'B', { ok: true });
    await ev.logSummary();
    const [pas] = await client.spans({ filter: { names: ['Evaluation.predict_and_score'] }, sortBy: [{ field: 'started_at', direction: 'desc' }], limit: 1 });
    const vals = (await client.http.post('/api/v1/refs/read', { refs: [pas.inputs.example] })).items;
    expect(vals).toEqual([{ q: 'b' }]);

    for (const sc of [new weft.ValidXMLScorer({ name: 'ts-xml' }), new weft.RuleScorer({ name: 'ts-rule', rule: { $eq: [1, 1] } }), new weft.HallucinationFreeScorer({ name: 'ts-hall' })]) {
      await weft.save(sc);
      const obj = (await client.readObject(client.projectId, sc.name!));
      expect(obj.value._class_name).toBe(sc.className);
      expect(obj.digest).toBe(sc.ref!.digest);
    }
    const hall: any = await new weft.HallucinationFreeScorer({ model: 'mock/json' }).scoreOp({ modelOutput: 'x', datasetRow: { q: 'y' } });
    expect(hall).toBeTruthy();
    await expect(new weft.EmbeddingSimilarityScorer().score({ modelOutput: 'a', datasetRow: { expected: 'b' } })).rejects.toThrow(/embeddings/);
  });
});

