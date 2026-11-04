import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import * as weft from '../src/index.js';
import { createHash } from 'node:crypto';
import { canonicalJson, digest, fileDigest, tableDigest } from '../src/canonical.js';
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
  it('digests are Weft digests (DESIGN.md §10.3)', () => {
    expect(canonicalJson({ b: 1, a: [1.5, true, null], c: '<&>' })).toBe('{"a":[1.5,true,null],"b":1,"c":"<&>"}');
    // UTF-8 (no \\u escapes above U+001F), code point key order (not UTF-16 order)
    expect(canonicalJson({ 'é': 1, z: 2, A: ['ü😀', String.fromCharCode(0x2028)], '\u{1F600}': 3, '\uFFFF': 4 })).toBe(
      '{"A":["ü😀","\u2028"],"z":2,"é":1,"\uFFFF":4,"😀":3}',
    );
    expect(canonicalJson('"q" \\ \n\t\u0001\u007f')).toBe('"\\"q\\" \\\\ \\n\\t\\u0001\u007f"');
    expect(canonicalJson('a\ud800b')).toBe('"a\ufffdb"'); // a lone surrogate reaches the server as U+FFFD
    // numbers keep the text this SDK sends
    expect(canonicalJson({ f: 1.0, g: 1e-7, h: 1e21 })).toBe('{"f":1,"g":1e-7,"h":1e+21}');
    expect(fileDigest('abc')).toBe(createHash('sha256').update('abc').digest('hex'));
    expect(digest({ a: 1 })).toBe(fileDigest('{"a":1}'));
    expect(tableDigest(['d1', 'd2'])).toBe(digest(['d1', 'd2']));
  });

  it('publishes datasets as tables and loads them back', async () => {
    const ds = new weft.Dataset({ name: 'math', rows: ROWS });
    const ref = await weft.save(ds);
    expect(ref.uri()).toBe(`weft:///ts-entity/ts-proj/object/math:${ref.digest}`);
    const v = fake.objs.get('math')![0].value;
    expect(v).toMatchObject({ _type: 'Dataset', _class_name: 'Dataset', _bases: ['Object', 'BaseModel'], name: 'math' });
    expect(v.rows).toMatch(/^weft:\/\/\/ts-entity\/ts-proj\/table\//);
    const again = await weft.save(new weft.Dataset({ name: 'math', rows: ROWS }));
    expect(again.digest).toBe(ref.digest);
    const loaded = await weft.ref('math:latest').get();
    expect(loaded).toBeInstanceOf(weft.Dataset);
    expect(loaded.rows).toEqual(ROWS);
    expect(loaded.rowDigests).toEqual(ds.rowDigests);
    expect((await weft.ref('math:v0').get()).rows).toHaveLength(4);
    const plain = await weft.save({ temperature: 0.1 }, 'cfg');
    expect(await client.get(plain)).toEqual({ temperature: 0.1 });
  });

  it('runs an evaluation with the documented call structure', async () => {
    const model = weft.trace(async function calc({ datasetRow }: { datasetRow: { question: string } }) {
      const q = datasetRow.question;
      if (!q.includes('+')) return 'Lyon';
      const [a, b] = q.split('+').map(Number);
      return String(a + b);
    });
    const exact = weft.trace(function exact({ modelOutput, datasetRow }: { modelOutput: string; datasetRow: { expected: string } }) {
      return modelOutput === datasetRow.expected;
    });
    const detail = weft.trace(function detail({ modelOutput, datasetRow }: { modelOutput: string; datasetRow: { expected: string } }) {
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
    expect(root.inputs.model).toMatch(/^weft:\/\/\/ts-entity\/ts-proj\/function\/calc:/);
    expect(root.output).toEqual(summary);
    expect(evObj.value).toMatchObject({ _type: 'Evaluation', _bases: ['Object', 'BaseModel'], trials: 1 });
    expect(evObj.value.dataset).toMatch(/^weft:\/\/\/ts-entity\/ts-proj\/object\/arith:/);
    expect(evObj.value.scorers[0]).toMatch(/\/function\/exact:/);
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
      expect(p.inputs.example).toMatch(new RegExp(`^${evObj.value.dataset}/attr/rows/id/`));
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
