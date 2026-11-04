import { mkdtempSync, readdirSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import * as weft from '../src/index.js';
import { fileDigest } from '../src/canonical.js';
import { FakeWeft } from './fakeServer.js';

let fake: FakeWeft;
let client: weft.WeftClient;

beforeEach(async () => {
  fake = await new FakeWeft().start();
  client = await weft.init('ts-entity/ts-proj', { serverUrl: fake.url, sender: { flushIntervalMs: 5, backoffMs: 1 }, settings: { printSpanLink: false } });
});

afterEach(async () => {
  vi.restoreAllMocks();
  await weft.finish();
  await fake.close();
});

describe('op options and settings', () => {
  it('stores kind and color', async () => {
    const tool = weft.trace(function tool() {
      return 1;
    }, { kind: 'tool' });
    const agent = weft.trace(async function agent() {
      return tool();
    }, { kind: 'agent', color: 'purple' });
    await agent();
    await client.flush();
    const [a] = fake.callsByOp('agent');
    expect(a.attributes.weft.span_kind).toBe('agent');
    expect(a.attributes.weft.color).toBe('purple');
    expect((a.attributes as Record<string, unknown>).kind).toBeUndefined();
    expect(fake.callsByOp('tool')[0].attributes.weft.span_kind).toBe('tool');
    await new weft.ExactMatchScorer().scoreOp({ modelOutput: 'a', datasetRow: { expected: 'a' } });
    await client.flush();
    expect(fake.callsByOp('ExactMatchScorer.score')[0].attributes.weft.span_kind).toBe('scorer');
  });

  it('applies global attributes, prints root call links and can skip code capture', async () => {
    const lines: string[] = [];
    vi.spyOn(process.stderr, 'write').mockImplementation(((s: string) => {
      lines.push(String(s));
      return true;
    }) as any);
    const c = await weft.init('ent/proj', { serverUrl: fake.url, sender: { flushIntervalMs: 1 }, settings: { globalAttributes: { env: 'staging', team: 'a' }, captureCode: false } });
    const child = weft.trace(function child() {
      return 1;
    });
    const root = weft.trace(function root() {
      return child();
    });
    const [, call] = await weft.attributes({ team: 'b' }, () => root.call());
    await c.flush();
    expect(lines.filter((l) => l.startsWith('weft: '))).toEqual([`weft: ${fake.url}/ent/proj/spans/${call!.id}\n`]);
    const [r] = fake.callsByOp('root');
    expect(r.attributes.env).toBe('staging');
    expect(r.attributes.team).toBe('b');
    expect(fake.objs.get('root')![0].value.code).toBe('// Code capture disabled\n');
    const secret = weft.trace(function secret() {}, { enableCodeCapture: false });
    await weft.init('p2', { serverUrl: fake.url, settings: { printSpanLink: false } });
    secret();
    await weft.flush();
    expect(fake.objs.get('secret')![0].value.code).toBe('// Code capture disabled\n');
  });

  it('supports withTracingDisabled and per-op sampling', async () => {
    const child = weft.trace(function child() {
      return 1;
    });
    const never = weft.trace(function never() {
      return child();
    }, { tracingSampleRate: 0 });
    const f = weft.trace(function f() {
      return 1;
    });
    weft.withTracingDisabled(() => f());
    f();
    expect(never()).toBe(1);
    await client.flush();
    expect(fake.callsByOp('f')).toHaveLength(1);
    expect(fake.callsByOp('never')).toHaveLength(0);
    expect(fake.callsByOp('child')).toHaveLength(0);
  });

  it('retries with configured settings and replays the write-ahead log', async () => {
    const dir = mkdtempSync(join(tmpdir(), 'weft-wal-'));
    const settings = { enableDiskFallback: true, walDir: dir, retryMaxAttempts: 1, retryBackoffMs: 1, printSpanLink: false };
    fake.failStatus = 503;
    const c1 = await weft.init('wal-proj', { serverUrl: fake.url, sender: { flushIntervalMs: 1 }, settings });
    expect(c1.sender.maxRetries).toBe(1);
    const important = weft.trace(function important(x: number) {
      return x;
    });
    important(1);
    important(2);
    await c1.flush();
    expect(c1.sender.wal!.count()).toBeGreaterThanOrEqual(2);
    expect(readdirSync(dir)).toHaveLength(1);
    fake.failStatus = null;
    const c2 = await weft.init('wal-proj', { serverUrl: fake.url, sender: { flushIntervalMs: 1 }, settings });
    await c2.flush();
    const calls = fake.callsByOp('important');
    expect(calls.map((c) => c.inputs.x).sort()).toEqual([1, 2]);
    expect(calls.every((c) => c.ended_at)).toBe(true);
    expect(fake.objs.has('important')).toBe(true);
    expect(c2.sender.wal!.count()).toBe(0);
  });
});

describe('Content and media', () => {
  it('serialises Content and media types through /file/create', async () => {
    const dir = mkdtempSync(join(tmpdir(), 'weft-content-'));
    const p = join(dir, 'notes.txt');
    writeFileSync(p, 'hello file');
    const pdf = new Uint8Array(Buffer.from('%PDF-1.4 fake'));
    const make = weft.trace(function make() {
      return {
        path: weft.Content.fromPath(p),
        text: weft.Content.fromText('plain', { filename: 'a.txt' }),
        bytes: weft.Content.fromBytes(new Uint8Array([0, 1]), { extension: 'bin' }),
        video: new weft.Video(new Uint8Array(Buffer.concat([Buffer.from([0, 0, 0, 24]), Buffer.from('ftypmp42'), Buffer.alloc(8)]))),
        pdf: weft.PDF.fromBytes(pdf, { filename: 'doc.pdf' }),
        html: new weft.Html('<b>hi</b>'),
        md: new weft.Markdown('# Title'),
        sniffed: pdf,
      };
    });
    make();
    await client.flush();
    const out = fake.callsByOp('make')[0].output;
    expect(out.path).toEqual({ _type: 'Content', mimetype: 'text/plain', filename: 'notes.txt', file_digest: fileDigest('hello file') });
    expect(fake.files.get(out.path.file_digest)!.toString()).toBe('hello file');
    expect(out.bytes).toMatchObject({ mimetype: 'application/octet-stream', filename: 'file.bin' });
    expect(out.video).toMatchObject({ _type: 'Video', mime_type: 'video/mp4' });
    expect(out.pdf).toMatchObject({ _type: 'PDF', mime_type: 'application/pdf', filename: 'doc.pdf' });
    expect(out.html._type).toBe('Html');
    expect(fake.files.get(out.md.file_digest)!.toString()).toBe('# Title');
    expect(out.sniffed._type).toBe('PDF');
  });
});

describe('object management', () => {
  it('aliases, tags and deletion', async () => {
    const r0 = await weft.save({ v: 0 }, 'cfg');
    const r1 = await weft.save({ v: 1 }, 'cfg');
    await client.setAliases(r1, 'production');
    await client.setAliases('cfg:v0', ['staging', 'dev']);
    expect(fake.aliases.get('cfg|production')).toBe(r1.digest);
    const put = fake.requests.find((r) => r.path === '/api/v1/objects/cfg/aliases')!;
    expect(put.body).toEqual({ project_id: 'ts-entity/ts-proj', version: r1.digest, aliases: ['production'] });
    expect(await client.getAliases(r0)).toEqual(['dev', 'staging']);
    await client.removeAliases('cfg', 'dev');
    expect(fake.aliases.has('cfg|dev')).toBe(false);
    await client.addTags(r1, ['reviewed', 'golden']);
    await client.addTags('cfg:latest', ['golden']);
    expect(await client.getTags(r1)).toEqual(['reviewed', 'golden']);
    await client.removeTags(r1.uri(), ['reviewed']);
    expect(await client.listTags()).toEqual(['golden']);
    expect(await client.deleteObjectVersion(r0)).toBe(1);
    await weft.save({ v: 2 }, 'cfg');
    expect(await client.deleteObjectVersions('cfg', [r1.digest])).toBe(1);
    expect(await client.deleteAllObjectVersions('cfg')).toBe(1);
  });

  it('sets aliases by name and version', async () => {
    const r = await weft.save({ v: 0 }, 'old');
    await client.setAliases('old:v0', 'prod');
    expect(fake.aliases.get('old|prod')).toBe(r.digest);
    expect(await weft.get('old:prod')).toEqual({ v: 0 });
    await client.removeAliases('old', 'prod');
    expect(fake.aliases.has('old|prod')).toBe(false);
  });
});

describe('EvaluationLogger.logExample', () => {
  it('links predictions to dataset rows', async () => {
    const ds = new weft.Dataset({ name: 'qa', rows: [{ q: 'a' }, { q: 'b' }] });
    const ev = new weft.EvaluationLogger({ model: 'm', dataset: ds, name: 'linked' });
    ev.logExample({ q: 'a' }, 'A', { correct: true });
    ev.logExample({ q: 'zzz' }, 'Z', { correct: false });
    const summary = await ev.logSummary();
    expect(summary.correct).toEqual({ true_count: 1, true_fraction: 0.5 });
    await client.flush();
    const pas = fake.callsByOp('Evaluation.predict_and_score');
    expect(pas[0].inputs.example).toBe(ds.rowRef(0)!.uri());
    expect(pas[1].inputs.example).toEqual({ q: 'zzz' });
    expect(fake.objs.get('qa')![0].digest).toBe(ds.ref!.digest); // local digest == server digest
    const loaded = await weft.ref('qa').get();
    const ev2 = new weft.EvaluationLogger({ model: 'm', dataset: loaded });
    ev2.logExample({ q: 'b' }, 'B', { ok: 1 });
    await ev2.logSummary();
    await client.flush();
    expect(fake.callsByOp('Evaluation.predict_and_score').at(-1).inputs.example).toBe(loaded.rowRef(1).uri());
    expect(() => new weft.EvaluationLogger({ dataset: 'weft:///a/b/object/x:latest' })).toThrow(/load the dataset first/);
  });
});

describe('new scorers', () => {
  it('serialise like the Python SDK', async () => {
    await weft.save(new weft.RemoteScorer({ name: 'remote', url: 'https://x/score', headersSecret: 'H', timeout: 5 }));
    await weft.save(new weft.HallucinationFreeScorer({ name: 'hall' }));
    await weft.save(new weft.JSONSchemaScorer({ name: 'js', schema: { type: 'object' } }));
    await weft.save(new weft.EmbeddingSimilarityScorer({ name: 'emb', targetColumn: 'ref' }));
    await weft.save(new weft.RuleScorer({ name: 'rule', rule: { $eq: [1, 1] }, threshold: 0.5 }));
    await weft.save(new weft.ValidXMLScorer({ name: 'xml' }));
    await weft.save(new weft.OpenAIModerationScorer({ name: 'mod' }));
    const base = { _bases: ['Scorer', 'Object', 'BaseModel'], description: null, column_map: null };
    const val = (n: string) => fake.objs.get(n)![0].value;
    expect(val('remote')).toEqual({ ...base, _type: 'RemoteScorer', _class_name: 'RemoteScorer', name: 'remote', url: 'https://x/score', headers_secret: 'H', timeout: 5 });
    expect(val('hall')).toEqual({ ...base, _type: 'HallucinationFreeScorer', _class_name: 'HallucinationFreeScorer', name: 'hall', model: 'gpt-4o-mini', scoring_prompt: weft.HALLUCINATION_PROMPT });
    expect(val('js').schema).toEqual({ type: 'object' });
    expect(val('emb')).toMatchObject({ target_column: 'ref', model: 'text-embedding-3-small' });
    expect(val('rule')).toMatchObject({ rule: { $eq: [1, 1] }, threshold: 0.5 });
    expect(val('xml')).toEqual({ ...base, _type: 'ValidXMLScorer', _class_name: 'ValidXMLScorer', name: 'xml' });
    expect(val('mod')._class_name).toBe('OpenAIModerationScorer');
  });

  it('run locally', async () => {
    const row = { question: 'capital of France?' };
    const hall: any = await new weft.HallucinationFreeScorer({ model: 'mock/json' }).scoreOp({ modelOutput: 'Paris', datasetRow: row });
    expect(hall.correct).toBe(true);
    expect(hall.echo).toContain('capital of France?');
    expect(((await new weft.SummarizationScorer({ model: 'mock/json' }).scoreOp({ modelOutput: 's', datasetRow: {} })) as any).correct).toBe(true);
    const xml = new weft.ValidXMLScorer();
    expect(xml.score({ modelOutput: '<?xml version="1.0"?><a x="1"><b>t &amp; u</b><!-- c --><c/></a>', datasetRow: {} })).toBe(true);
    for (const bad of ['<a>', '<a></b>', '<a/><b/>', 'text', '<a>x & y</a>']) expect(xml.score({ modelOutput: bad, datasetRow: {} })).toBe(false);
    const js = new weft.JSONSchemaScorer({ schema: { type: 'object', properties: { n: { type: 'integer', minimum: 0 } }, required: ['n'], additionalProperties: false } });
    expect(js.score({ modelOutput: '{"n": 3}', datasetRow: {} })).toBe(true);
    expect(js.score({ modelOutput: { n: -1 }, datasetRow: {} })).toBe(false);
    expect(js.score({ modelOutput: { n: 1, x: 2 }, datasetRow: {} })).toBe(false);
    const rule = new weft.RuleScorer({ rule: { $contains: { input: { $getField: 'output' }, substr: { $getField: 'expected' }, case_insensitive: true } } });
    expect(rule.score({ modelOutput: 'It is PARIS', datasetRow: { expected: 'paris' } })).toBe(true);
    const num = new weft.RuleScorer({ rule: { $convert: { input: { $getField: 'output' }, to: 'double' } }, threshold: 0.5 });
    expect(num.score({ modelOutput: '0.7', datasetRow: {} })).toBe(true);
    expect(num.score({ modelOutput: '0.2', datasetRow: {} })).toBe(false);
    process.env.WEFT_TEST_SCORER_HEADERS = JSON.stringify({ 'X-Key': 'k' });
    const remote = new weft.RemoteScorer({ url: `${fake.url}/remote/score`, headersSecret: 'WEFT_TEST_SCORER_HEADERS' });
    const res: any = await remote.score({ modelOutput: 'hi', datasetRow: { q: 'x' } });
    expect(res).toEqual({ score: 0.9, received: { output: 'hi', inputs: { q: 'x' } } });
    expect(fake.requests.find((r) => r.path === '/remote/score')!.headers['x-key']).toBe('k');
    delete process.env.WEFT_TEST_SCORER_HEADERS;
    const emb = new weft.EmbeddingSimilarityScorer({ targetColumn: 'ref' });
    await expect(emb.score({ modelOutput: 'abc', datasetRow: { ref: 'abc' } })).rejects.toThrow(/no embeddings route/);
    fake.embeddingsEnabled = true;
    expect(await emb.score({ modelOutput: 'abc', datasetRow: { ref: 'abc' } })).toBeCloseTo(1);
    const saved = process.env.OPENAI_API_KEY;
    delete process.env.OPENAI_API_KEY;
    await expect(new weft.OpenAIModerationScorer().score({ modelOutput: 'x', datasetRow: {} })).rejects.toThrow(/OPENAI_API_KEY/);
    if (saved) process.env.OPENAI_API_KEY = saved;
  });
});

describe('more integrations', () => {
  it('wrapGoogleGenAI traces generateContent and generateContentStream', async () => {
    const resp = (text: string, usage?: [number, number]) => ({
      candidates: [{ index: 0, content: { role: 'model', parts: [{ text }] } }],
      modelVersion: 'gemini-2.0-flash-001',
      ...(usage ? { usageMetadata: { promptTokenCount: usage[0], candidatesTokenCount: usage[1], totalTokenCount: usage[0] + usage[1] } } : {}),
    });
    const ai = weft.wrapGoogleGenAI({
      models: {
        async generateContent(_p: any) {
          return resp('Bonjour', [8, 2]);
        },
        async generateContentStream(_p: any) {
          return (async function* () {
            yield resp('Bon');
            yield resp('jour', [8, 3]);
          })();
        },
      },
    });
    await ai.models.generateContent({ model: 'gemini-2.0-flash', contents: 'hi' });
    const chunks = [];
    for await (const c of await ai.models.generateContentStream({ model: 'gemini-2.0-flash', contents: 'hi' })) chunks.push(c);
    expect(chunks).toHaveLength(2);
    await client.flush();
    const [gc] = fake.callsByOp('google.genai.models.generate_content');
    expect(gc.summary.usage).toEqual({ 'gemini-2.0-flash': { requests: 1, prompt_tokens: 8, completion_tokens: 2, total_tokens: 10 } });
    expect(gc.attributes.weft.span_kind).toBe('llm');
    const [gs] = fake.callsByOp('google.genai.models.generate_content_stream');
    expect(gs.output.text).toBe('Bonjour');
    expect(gs.summary.usage['gemini-2.0-flash'].completion_tokens).toBe(3);
  });

  it('wrapOpenAICompatible uses the given prefix and x_groq usage', async () => {
    const groq = weft.wrapOpenAICompatible(
      {
        chat: {
          completions: {
            async create(body: any) {
              return (async function* () {
                yield { id: 's', model: body.model, choices: [{ index: 0, delta: { content: 'yo' } }] };
                yield { id: 's', model: body.model, choices: [], x_groq: { usage: { prompt_tokens: 3, completion_tokens: 1, total_tokens: 4 } } };
              })();
            },
          },
        },
      },
      { name: 'groq' },
    );
    const s: any = await groq.chat.completions.create({ model: 'llama-3.1-8b', messages: [], stream: true });
    for await (const _ of s) {
      /* consume */
    }
    await client.flush();
    const [c] = fake.callsByOp('groq.chat.completions.create');
    expect(c.inputs.stream_options).toBeUndefined();
    expect(c.summary.usage['llama-3.1-8b'].total_tokens).toBe(4);
    expect(c.output.choices[0].message.content).toBe('yo');
  });

  it('weftMiddleware traces Vercel AI SDK generate and stream', async () => {
    const mw = weft.weftMiddleware();
    const model = { modelId: 'gpt-4o-mini', provider: 'openai.chat' };
    const res = await mw.wrapGenerate({
      model,
      params: { prompt: [{ role: 'user', content: [{ type: 'text', text: 'hi' }] }], temperature: 0 },
      doGenerate: async () => ({ content: [{ type: 'text', text: 'hello' }], finishReason: 'stop', usage: { inputTokens: 5, outputTokens: 2, totalTokens: 7 } }),
    });
    expect(res.finishReason).toBe('stop');
    const parts = [
      { type: 'text-delta', delta: 'Hel' },
      { type: 'text-delta', delta: 'lo' },
      { type: 'finish', finishReason: 'stop', usage: { inputTokens: 4, outputTokens: 2, totalTokens: 6 } },
    ];
    const streamed = await mw.wrapStream({
      model,
      params: { prompt: [] },
      doStream: async () => ({
        stream: new ReadableStream({
          start(c) {
            for (const p of parts) c.enqueue(p);
            c.close();
          },
        }),
      }),
    });
    const got = [];
    for await (const p of streamed.stream) got.push(p);
    expect(got).toHaveLength(3);
    await client.flush();
    const [g] = fake.callsByOp('vercel_ai.generate');
    expect(g.inputs).toMatchObject({ model: 'gpt-4o-mini', provider: 'openai.chat', temperature: 0 });
    expect(g.summary.usage['gpt-4o-mini']).toEqual({ requests: 1, prompt_tokens: 5, completion_tokens: 2, total_tokens: 7 });
    const [st] = fake.callsByOp('vercel_ai.stream');
    expect(st.output).toMatchObject({ text: 'Hello', finishReason: 'stop' });
    expect(st.summary.usage['gpt-4o-mini'].total_tokens).toBe(6);
  });
});
