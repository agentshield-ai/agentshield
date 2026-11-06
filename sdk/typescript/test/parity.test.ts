import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import * as weft from '../src/index.js';
import { digest } from '../src/canonical.js';
import { FakeWeft } from './fakeServer.js';

let fake: FakeWeft;
let client: weft.WeftClient;

beforeEach(async () => {
  fake = await new FakeWeft().start();
  client = await weft.init('ts-entity/ts-proj', { serverUrl: fake.url, sender: { flushIntervalMs: 5 } });
});

afterEach(async () => {
  delete process.env.WEFT_REDACT_KEYS;
  await weft.finish();
  await fake.close();
});

const ROWS = [
  { question: '1+1', expected: '2' },
  { question: '2+2', expected: '4' },
  { question: 'capital of France', expected: 'Paris' },
];

describe('scorer classes', () => {
  it('serialise per API.md and run locally', async () => {
    await weft.save(new weft.ExactMatchScorer({ name: 'exact' }));
    await weft.save(new weft.ContainsScorer({ name: 'contains', caseInsensitive: false }));
    await weft.save(new weft.RegexScorer({ name: 'digits', pattern: '^\\d+$' }));
    await weft.save(new weft.JSONValidScorer({ name: 'json' }));
    await weft.save(new weft.LevenshteinScorer({ name: 'lev' }));
    await weft.save(new weft.LLMAsAJudgeScorer({ name: 'judge', scoringPrompt: 'Is {output} right?' }));
    const exact = fake.objs.get('exact')![0].value;
    expect(exact).toEqual({ _type: 'ExactMatchScorer', _class_name: 'ExactMatchScorer', _bases: ['Scorer', 'Object', 'BaseModel'], name: 'exact', description: null, column_map: null, target_column: 'expected' });
    // same object published from Python has the same value, hence the same digest
    expect(fake.objs.get('exact')![0].digest).toBe(digest({ _type: 'ExactMatchScorer', _class_name: 'ExactMatchScorer', _bases: ['Scorer', 'Object', 'BaseModel'], name: 'exact', description: null, column_map: null, target_column: 'expected' }));
    expect(fake.objs.get('contains')![0].value).toMatchObject({ target_column: 'expected', case_insensitive: false });
    expect(fake.objs.get('digits')![0].value.pattern).toBe('^\\d+$');
    expect(fake.objs.get('judge')![0].value).toMatchObject({ model: 'gpt-4o-mini', scoring_prompt: 'Is {output} right?', response_format: 'json_object' });

    const row = { expected: '4' };
    expect(new weft.ExactMatchScorer().score({ modelOutput: ' 4 ', datasetRow: row })).toBe(true);
    expect(new weft.ContainsScorer().score({ modelOutput: 'It is FOUR', datasetRow: { expected: 'four' } })).toBe(true);
    expect(new weft.RegexScorer({ pattern: '^\\d+$' }).score({ modelOutput: '123', datasetRow: {} })).toBe(true);
    expect(new weft.JSONValidScorer().score({ modelOutput: '{"a":1}', datasetRow: {} })).toBe(true);
    expect(new weft.JSONValidScorer().score({ modelOutput: 'nope', datasetRow: {} })).toBe(false);
    expect(new weft.LevenshteinScorer().score({ modelOutput: 'kitten', datasetRow: { expected: 'sitting' } })).toBeCloseTo(1 - 3 / 7);
    const judge = new weft.LLMAsAJudgeScorer({ model: 'mock/json', scoringPrompt: 'Q {question} A {output}' });
    const verdict: any = await judge.scoreOp({ modelOutput: '4', datasetRow: { question: '2+2' } });
    expect(verdict).toEqual({ correct: true, echo: 'Q 2+2 A 4' });
    const req = fake.requests.find((r) => r.path === '/api/v1/completions')!.body;
    expect(req.track).toBe(false);
    await client.flush();
    const [jc] = fake.callsByOp('LLMAsAJudgeScorer.score');
    expect(jc.summary.usage['mock/json'].total_tokens).toBe(10);
    expect(jc.inputs.modelOutput).toBe('4');
  });

  it('work in an Evaluation alongside op scorers', async () => {
    const model = weft.trace(async function calc({ datasetRow }: any) {
      const q = String(datasetRow.question);
      if (!q.includes('+')) return 'Lyon';
      const [a, b] = q.split('+').map(Number);
      return String(a + b);
    });
    const exact = weft.trace(function exact({ modelOutput, datasetRow }: any) {
      return modelOutput === datasetRow.expected;
    });
    const ev = new weft.Evaluation({
      name: 'mixed',
      dataset: new weft.Dataset({ name: 'mixed-ds', rows: ROWS }),
      scorers: [exact, new weft.ExactMatchScorer({ name: 'em' }), new weft.LevenshteinScorer({ name: 'lev', columnMap: {} })],
    });
    const summary = await ev.evaluate({ model });
    expect(summary.exact).toEqual({ true_count: 2, true_fraction: 2 / 3 });
    expect(summary.em).toEqual({ true_count: 2, true_fraction: 2 / 3 });
    expect((summary.lev as any).mean).toBeGreaterThan(0.6);
    await client.flush();
    const evVal = fake.objs.get('mixed')![0].value;
    expect(evVal.scorers[1]).toMatch(/\/object\/em:/);
    const pas = fake.callsByOp('Evaluation.predict_and_score');
    const names = fake.children(pas[0].id).map((c) => fake.opBase(c)).sort();
    expect(names).toEqual(['ExactMatchScorer.score', 'LevenshteinScorer.score', 'calc', 'exact']);
    const em = fake.children(pas[0].id).find((c) => fake.opBase(c) === 'ExactMatchScorer.score');
    expect(em.inputs.self).toMatch(/\/object\/em:/);
  });
});

describe('EvaluationLogger', () => {
  it('produces the Evaluation call structure', async () => {
    const ev = new weft.EvaluationLogger({ model: 'my-model', dataset: 'my-ds', name: 'imperative' });
    for (const [q, out, ok] of [['a', 'A', true], ['b', 'B', false]] as const) {
      const pred = ev.logPrediction({ q }, out, 0.5);
      pred.logScore('correct', ok);
      pred.logScore('quality', { stars: ok ? 4 : 2 });
      pred.finish();
    }
    const p3 = ev.logPrediction({ q: 'c' }, 'C');
    p3.logScore(new weft.ExactMatchScorer({ name: 'em' }), true);
    const summary = await ev.logSummary({ note: 'manual' });
    expect(summary).toEqual({
      correct: { true_count: 1, true_fraction: 0.5 },
      quality: { stars: { mean: 3 } },
      em: { true_count: 1, true_fraction: 1 },
      model_latency: { mean: 0.5 },
      note: 'manual',
    });
    await client.flush();
    const [root] = fake.callsByOp('Evaluation.evaluate');
    expect(root.display_name).toBe('imperative');
    const evObj = fake.objs.get('imperative')![0];
    expect(root.inputs.self).toBe(`weft:///ts-entity/ts-proj/object/imperative:${evObj.digest}`); // local digest == server digest
    const modelObj = fake.objs.get('my-model')![0];
    expect(modelObj.value).toEqual({ _type: 'Model', _class_name: 'Model', _bases: ['Object', 'BaseModel'], name: 'my-model', description: null });
    expect(root.inputs.model).toBe(`weft:///ts-entity/ts-proj/object/my-model:${modelObj.digest}`);
    expect(evObj.value.dataset).toBe(`weft:///ts-entity/ts-proj/object/my-ds:${fake.objs.get('my-ds')![0].digest}`);
    expect(root.output).toEqual(summary);
    const kids = fake.children(root.id);
    expect(kids.map((k) => fake.opBase(k))).toEqual([...Array(3).fill('Evaluation.predict_and_score'), 'Evaluation.summarize']);
    expect(kids[0].inputs.example).toEqual({ q: 'a' });
    expect(kids[0].output).toEqual({ output: 'A', scores: { correct: true, quality: { stars: 4 } }, model_latency: 0.5 });
    expect(fake.children(kids[0].id).map((g) => fake.opBase(g))).toEqual(['my-model.predict', 'correct', 'quality']);
    expect(fake.children(kids[2].id).map((g) => fake.opBase(g))).toEqual(['my-model.predict', 'ExactMatchScorer.score']);
  });
});

describe('op loading', () => {
  afterEach(() => {
    delete process.env.WEFT_ALLOW_CODE_LOAD;
    delete (globalThis as any).__PWNED;
  });

  it('get() of stored code is inert: the code never runs (security)', async () => {
    delete process.env.WEFT_ALLOW_CODE_LOAD;
    // a project writer stores a "function" whose evaluation has a side effect
    const code = 'globalThis.__PWNED = 1, function evil(a = (globalThis.__PWNED = 2)) { return 1; }';
    fake.objs.set('evil', [{ name: 'evil', digest: 'dev', version: 0, kind: 'function', value: { _type: 'Op', name: 'evil', code, language: 'javascript' } }]);
    const got = await weft.get('evil');
    expect(got).toBeInstanceOf(weft.StoredOp);
    expect(weft.isOp(got)).toBe(false);
    expect(typeof got).toBe('object');
    expect(got.code).toBe(code);
    expect(got.ref.uri()).toMatch(/\/function\/evil:dev$/);
    expect(JSON.stringify({ f: got })).toBe(JSON.stringify({ f: got.ref.uri() }));
    await expect(weft.loadOp('evil')).rejects.toThrow(/trustCode: true/);
    await expect(client.loadOp(got)).rejects.toThrow(weft.OpLoadError);
    expect(() => got.load()).toThrow(/trustCode: true/);
    expect(await client.get('evil')).toBeInstanceOf(weft.StoredOp);
    expect((globalThis as any).__PWNED).toBeUndefined();
    // explicit opt-in evaluates it (that is what trust means)
    const loaded = await weft.loadOp('evil', { trustCode: true });
    expect((globalThis as any).__PWNED).toBe(1);
    expect(loaded()).toBe(1);
    expect((globalThis as any).__PWNED).toBe(2);
  });

  it('WEFT_ALLOW_CODE_LOAD=1 opts in', async () => {
    fake.objs.set('seven', [{ name: 'seven', digest: 'd7', version: 0, kind: 'function', value: { _type: 'Op', name: 'seven', code: 'function seven() { return 7; }', language: 'javascript' } }]);
    process.env.WEFT_ALLOW_CODE_LOAD = '1';
    expect((await weft.get('seven'))()).toBe(7);
    process.env.WEFT_ALLOW_CODE_LOAD = '0';
    expect(await weft.get('seven')).toBeInstanceOf(weft.StoredOp);
  });

  it('rebuilds a runnable op linked to the same ref (with trustCode)', async () => {
    const triple = weft.trace(function triple(x: number) {
      return x * 3;
    });
    const ref = await weft.save(triple);
    await client.flush();
    const stored = await weft.get('triple');
    expect(stored).toBeInstanceOf(weft.StoredOp);
    const loaded = await weft.loadOp('triple', { trustCode: true });
    expect(weft.isOp(loaded)).toBe(true);
    expect(loaded(2)).toBe(6);
    const [res, call] = await loaded.call(4);
    expect(res).toBe(12);
    expect(call!.name).toBe(ref.uri());
    expect((await client.loadOp(stored, { trustCode: true }))(1)).toBe(3);
    expect(weft.isOp(await weft.get('triple', { trustCode: true }))).toBe(true);
    const asyncArrow = weft.trace(async ({ a }: any) => a + 1, { name: 'inc' });
    await weft.save(asyncArrow);
    expect(await (await client.get('inc', { trustCode: true }))({ a: 1 })).toBe(2);
    const sc = new weft.ExactMatchScorer();
    const method = await client.loadOp(client.opRef(sc.scoreOp.__weftOp), { trustCode: true });
    expect(typeof method).toBe('function');
  });

  it('raises a clear error when code cannot be rebuilt', async () => {
    fake.objs.set('py', [{ name: 'py', digest: 'd', version: 0, kind: 'function', value: { _type: 'Op', name: 'py', code: 'def py(): pass', language: 'python' } }]);
    await expect(client.loadOp('py', { trustCode: true })).rejects.toThrow(/not javascript/);
    fake.objs.set('rec', [{ name: 'rec', digest: 'd', version: 0, kind: 'function', value: { _type: 'Op', name: 'rec', code: '// rec: recorded by the weft SDK\n', language: 'javascript' } }]);
    await expect(client.get('rec', { trustCode: true })).rejects.toThrow(weft.OpLoadError);
    fake.objs.set('plain', [{ name: 'plain', digest: 'p', version: 0, kind: 'object', value: { k: 1 } }]);
    await expect(client.loadOp('plain', { trustCode: true })).rejects.toThrow(/not a stored function/);
  });
});

describe('Dataset.addRows', () => {
  it('appends with /table/update and publishes a new version', async () => {
    const ds = new weft.Dataset({ name: 'grow', rows: [{ i: 0 }, { i: 1 }] });
    const r1 = await weft.save(ds);
    const ds2 = await ds.addRows([{ i: 2 }]);
    const updates = fake.requests.filter((r) => r.path === '/api/v1/tables/update');
    expect(updates).toHaveLength(1);
    expect(updates[0].body.updates).toEqual([{ append: { row: { i: 2 } } }]);
    expect(fake.requests.filter((r) => r.path === '/api/v1/tables/create')).toHaveLength(1);
    expect(ds2.ref!.digest).not.toBe(r1.digest);
    const versions = fake.objs.get('grow')!;
    expect(versions).toHaveLength(2);
    const loaded = await weft.ref('grow:latest').get();
    expect(loaded.rows).toEqual([{ i: 0 }, { i: 1 }, { i: 2 }]);
    expect(loaded.rowDigests).toEqual(ds2.rowDigests);
    const local = await new weft.Dataset({ rows: [{ a: 1 }] }).addRows([{ a: 2 }]);
    expect(local.ref).toBeUndefined();
    expect(local.rows).toHaveLength(2);
  });
});

describe('applyScorer', () => {
  it('scores a call and attaches runnable feedback', async () => {
    const generate = weft.trace(function generate(prompt: string) {
      return prompt.includes('bad') ? 'I hate this' : 'Lovely';
    });
    const toxicity = weft.trace(function toxicity({ modelOutput, datasetRow }: any) {
      return { flagged: String(modelOutput).includes('hate'), threshold: datasetRow.threshold };
    });
    const [, call] = await generate.call('bad prompt');
    const r = await call!.applyScorer(toxicity, { additionalScorerKwargs: { threshold: 0.9 } });
    expect(r.result).toEqual({ flagged: true, threshold: 0.9 });
    expect(r.scoreSpan!.parentId).toBeNull();
    const r2 = await call!.applyScorer(new weft.ContainsScorer({ name: 'mentions_prompt', targetColumn: 'prompt' }));
    expect(r2.result).toBe(false);
    const fb = fake.feedback.filter((f) => f.ref === call!.uri());
    expect(fb.map((f) => f.feedback_type)).toEqual(['weft.scorer.toxicity', 'weft.scorer.mentions_prompt']);
    expect(fb[0].payload).toEqual({ output: { flagged: true, threshold: 0.9 } });
    expect(fb[0].runnable_ref).toMatch(/^weft:\/\/\/ts-entity\/ts-proj\/function\/toxicity:/);
    expect(fb[0].span_ref).toBe(r.scoreSpan!.uri());
    expect(fb[1].runnable_ref).toMatch(/\/object\/mentions_prompt:/);
    const broken = weft.trace(function broken() {
      throw new Error('scorer bug');
    });
    await expect(call!.applyScorer(broken)).rejects.toThrow(/scorer broken failed: Error: scorer bug/);
    const [fetched] = await client.spans({ filter: { names: ['generate'] } });
    expect((await fetched.applyScorer(toxicity)).result).toEqual({ flagged: true });
  });
});

describe('platform objects', () => {
  it('serialise AnnotationSpec, Leaderboard, Monitor and SavedView', async () => {
    const spec = new weft.AnnotationSpec({ name: 'quality', fieldSchema: { type: 'integer', minimum: 1, maximum: 5 } });
    expect(spec.valueIsValid(3)).toBe(true);
    expect(spec.valueIsValid(9)).toBe(false);
    await weft.save(spec);
    const sv = fake.objs.get('quality')![0];
    // identical to the Python SDK's value (same digest across SDKs)
    expect(sv.digest).toBe(
      digest({ _type: 'AnnotationSpec', _class_name: 'AnnotationSpec', _bases: ['Object', 'BaseModel'], name: 'quality', description: null, field_schema: { type: 'integer', minimum: 1, maximum: 5 }, unique_among_creators: true, names: null }),
    );
    const ev = new weft.Evaluation({ name: 'lb-eval', dataset: [{ a: 1 }], scorers: [] });
    const evRef = await weft.save(ev);
    await weft.save(new weft.Leaderboard({ name: 'lb', columns: [{ evaluationObjectRef: ev, scorerName: 'exact', summaryMetricPath: 'true_fraction' }] }));
    expect(fake.objs.get('lb')![0].value.columns).toEqual([{ evaluation_object_ref: evRef.uri(), scorer_name: 'exact', summary_metric_path: 'true_fraction', should_minimize: false }]);
    await weft.save(new weft.Monitor({ name: 'mon', names: ['f'], query: { $eq: [1, 1] }, scorers: [new weft.ExactMatchScorer({ name: 'm-exact' })] }));
    const mv = fake.objs.get('mon')![0].value;
    expect(mv).toMatchObject({ _class_name: 'Monitor', active: true, sampling_rate: 1, names: ['f'], query: { $expr: { $eq: [1, 1] } } });
    expect(mv.scorers[0]).toMatch(/\/object\/m-exact:/);
    await weft.save(new weft.SavedView({ name: 'view', label: 'Errors' }));
    const loaded = await weft.ref('lb').get();
    expect(loaded).toBeInstanceOf(weft.Leaderboard);
    expect(loaded.columns[0].scorerName).toBe('exact');
    expect(await weft.ref('quality').get()).toBeInstanceOf(weft.AnnotationSpec);
    const f = weft.trace(function f() {
      return 1;
    });
    const [, call] = await f.call();
    await call!.feedback.addAnnotation(spec, 4);
    await expect(call!.feedback.addAnnotation(spec, 8)).rejects.toThrow();
    const fb = fake.feedback.at(-1);
    expect(fb.feedback_type).toBe('weft.annotation.quality');
    expect(fb.annotation_ref).toBe(spec.ref!.uri());
    expect(await client.whoami()).toEqual({ user_id: 'anonymous', auth_required: false });
  });
});

describe('redaction', () => {
  it('redacts credential keys by default', async () => {
    const callApi = weft.trace(function callApi(prompt: string, apiKey: string, headers: Record<string, unknown>) {
      return { text: 'ok', Authorization: 'Bearer x', nested: [{ password: 'p' }], max_tokens: 5 };
    }, { parameterNames: ['prompt', 'api_key', 'headers'] });
    callApi('hi', 'sk-1', { Authorization: 'Bearer sk', other: 1 });
    await client.flush();
    const [c] = fake.callsByOp('callApi');
    expect(c.inputs).toEqual({ prompt: 'hi', api_key: 'REDACTED', headers: { Authorization: 'REDACTED', other: 1 } });
    expect(c.output).toEqual({ text: 'ok', Authorization: 'REDACTED', nested: [{ password: 'REDACTED' }], max_tokens: 5 });
  });

  it('is configurable and can be disabled', async () => {
    const c1 = await weft.init('p', { serverUrl: fake.url, settings: { redactKeys: ['ssn'], redactPii: true }, sender: { flushIntervalMs: 1 } });
    const f = weft.trace(function f({ ssn, api_key, note }: any) {
      return note;
    });
    f({ ssn: '1', api_key: 'k', note: 'mail bob@example.com or 555-123-4567' });
    await c1.flush();
    const [call] = fake.callsByOp('f');
    expect(call.inputs).toEqual({ ssn: 'REDACTED', api_key: 'k', note: 'mail <EMAIL_ADDRESS> or <PHONE_NUMBER>' });
    expect(call.output).toBe('mail <EMAIL_ADDRESS> or <PHONE_NUMBER>');
    process.env.WEFT_REDACT_KEYS = '0';
    const c2 = await weft.init('p2', { serverUrl: fake.url, sender: { flushIntervalMs: 1 } });
    const g = weft.trace(function g(token: string) {
      return token;
    });
    g('t0k');
    await c2.flush();
    expect(fake.callsByOp('g')[0].inputs.token).toBe('t0k');
  });
});
