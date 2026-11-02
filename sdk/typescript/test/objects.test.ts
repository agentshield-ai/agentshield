import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import * as weft from '../src/index.js';
import { createHash } from 'node:crypto';
import { b64digest, digest, digestJson, tableDigest } from '../src/canonical.js';
import { FakeWeft } from './fakeServer.js';

let fake: FakeWeft;
let client: weft.WeftClient;

beforeEach(async () => {
  fake = await new FakeWeft().start();
  client = await weft.init('ts-entity/ts-proj', { serverUrl: fake.url, sender: { flushIntervalMs: 5 } });
});

afterEach(async () => {
  await weft.finish();
  await fake.close();
});

const ROWS = [
  { question: '1+1', expected: '2' },
  { question: '2+2', expected: '4' },
  { question: '3+3', expected: '6' },
  { question: 'capital of France', expected: 'Paris' },
];

describe('objects and evaluations', () => {
  it('digests follow the Weave scheme', () => {
    expect(digestJson({ b: 1, a: [1.5, true, null], c: '<&>' })).toBe('{"a": [1.5, true, null], "b": 1, "c": "<&>"}');
    // ensure_ascii escapes, surrogate pairs, code point key order
    expect(digestJson({ 'é': 1, z: 2, A: ['ü😀', String.fromCharCode(0x2028)] })).toBe('{"A": ["\\u00fc\\ud83d\\ude00", "\\u2028"], "z": 2, "\\u00e9": 1}');
    expect(digestJson('"q" \\ \n\t\u0001')).toBe('"\\"q\\" \\\\ \\n\\t\\u0001"');
    // numbers keep the text this SDK sends
    expect(digestJson({ f: 1.0, g: 1e-7, h: 1e21 })).toBe('{"f": 1, "g": 1e-7, "h": 1e+21}');
    const raw = createHash('sha256').update('abc').digest('base64url').replace(/-/g, 'X').replace(/_/g, 'Y');
    expect(b64digest('abc')).toBe(raw);
    expect(digest({ a: 1 })).toBe(b64digest('{"a": 1}'));
    expect(tableDigest(['d1', 'd2'])).toBe(createHash('sha256').update('d1d2').digest('hex'));
  });

  it('publishes datasets as tables and loads them back', async () => {
    const ds = new weft.Dataset({ name: 'math', rows: ROWS });
    const ref = await weft.publish(ds);
    expect(ref.uri()).toBe(`weft:///ts-entity/ts-proj/object/math:${ref.digest}`);
    const v = fake.objs.get('math')![0].val;
    expect(v).toMatchObject({ _type: 'Dataset', _class_name: 'Dataset', _bases: ['Object', 'BaseModel'], name: 'math' });
    expect(v.rows).toMatch(/^weft:\/\/\/ts-entity\/ts-proj\/table\//);
    const again = await weft.publish(new weft.Dataset({ name: 'math', rows: ROWS }));
    expect(again.digest).toBe(ref.digest);
    const loaded = await weft.ref('math:latest').get();
    expect(loaded).toBeInstanceOf(weft.Dataset);
    expect(loaded.rows).toEqual(ROWS);
    expect(loaded.rowDigests).toEqual(ds.rowDigests);
    expect((await weft.ref('math:v0').get()).rows).toHaveLength(4);
    const plain = await weft.publish({ temperature: 0.1 }, 'cfg');
    expect(await client.get(plain)).toEqual({ temperature: 0.1 });
  });

  it('runs an evaluation with the documented call structure', async () => {
    const model = weft.op(async function calc({ datasetRow }: { datasetRow: { question: string } }) {
      const q = datasetRow.question;
      if (!q.includes('+')) return 'Lyon';
      const [a, b] = q.split('+').map(Number);
      return String(a + b);
    });
    const exact = weft.op(function exact({ modelOutput, datasetRow }: { modelOutput: string; datasetRow: { expected: string } }) {
      return modelOutput === datasetRow.expected;
    });
    const detail = weft.op(function detail({ modelOutput, datasetRow }: { modelOutput: string; datasetRow: { expected: string } }) {
      return { len_diff: Math.abs(modelOutput.length - datasetRow.expected.length), same: modelOutput === datasetRow.expected };
    });
    const ev = new weft.Evaluation({ name: 'arith-eval', dataset: new weft.Dataset({ name: 'arith', rows: ROWS }), scorers: [exact, detail] });
    const summary = await ev.evaluate({ model });
    expect(summary.exact).toEqual({ true_count: 3, true_fraction: 0.75 });
    expect(summary.detail).toEqual({ len_diff: { mean: 0.25 }, same: { true_count: 3, true_fraction: 0.75 } });
    expect(Object.keys(summary.model_latency as object)).toEqual(['mean']);

    await client.flush();
    const [root] = fake.callsByOp('Evaluation.evaluate');
    const evObj = fake.objs.get('arith-eval')![0];
    expect(root.inputs.self).toBe(`weft:///ts-entity/ts-proj/object/arith-eval:${evObj.digest}`);
    expect(root.inputs.model).toMatch(/^weft:\/\/\/ts-entity\/ts-proj\/op\/calc:/);
    expect(root.output).toEqual(summary);
    expect(evObj.val).toMatchObject({ _type: 'Evaluation', _bases: ['Object', 'BaseModel'], trials: 1 });
    expect(evObj.val.dataset).toMatch(/^weft:\/\/\/ts-entity\/ts-proj\/object\/arith:/);
    expect(evObj.val.scorers[0]).toMatch(/\/op\/exact:/);
    const kids = fake.children(root.id);
    const pas = kids.filter((k) => fake.opBase(k) === 'Evaluation.predict_and_score');
    const summ = kids.filter((k) => fake.opBase(k) === 'Evaluation.summarize');
    expect(pas).toHaveLength(4);
    expect(summ).toHaveLength(1);
    expect(summ[0].inputs).toEqual({ self: root.inputs.self });
    expect(summ[0].output).toEqual(summary);
    for (const p of pas) {
      expect(p.inputs.self).toBe(root.inputs.self);
      expect(p.inputs.model).toBe(root.inputs.model);
      expect(p.inputs.example).toMatch(new RegExp(`^${evObj.val.dataset}/attr/rows/id/`));
      expect(Object.keys(p.output).sort()).toEqual(['model_latency', 'output', 'scores']);
      const names = fake.children(p.id).map((g) => fake.opBase(g)).sort();
      expect(names).toEqual(['calc', 'detail', 'exact']);
      const pred = fake.children(p.id).find((g) => fake.opBase(g) === 'calc');
      expect(pred.inputs.datasetRow.question).toBeTypeOf('string');
    }
  });

  it('supports trials, preprocessing and plain functions', async () => {
    const ev = new weft.Evaluation({
      dataset: [{ text: 'a', expected: 'A' }, { text: 'b', expected: 'x' }],
      scorers: [({ modelOutput, datasetRow }: any) => modelOutput === datasetRow.expected],
      trials: 2,
      preprocessModelInput: (row) => ({ q: row.text }),
    });
    const summary = await ev.evaluate({ model: async ({ datasetRow }: any) => String(datasetRow.q).toUpperCase() });
    const key = Object.keys(summary).find((k) => k !== 'model_latency')!;
    expect(summary[key]).toEqual({ true_count: 2, true_fraction: 0.5 });
    await client.flush();
    expect(fake.callsByOp('Evaluation.predict_and_score')).toHaveLength(4);
  });

  it('auto-summarizes like the Python SDK', () => {
    expect(weft.autoSummarize([true, false, null])).toEqual({ true_count: 1, true_fraction: 0.5 });
    expect(weft.autoSummarize([1, 2])).toEqual({ mean: 1.5 });
    expect(weft.autoSummarize([{ a: 1 }, { a: 3, b: true }])).toEqual({ a: { mean: 2 }, b: { true_count: 1, true_fraction: 1 } });
    expect(weft.autoSummarize(['x'])).toBeNull();
  });
});
