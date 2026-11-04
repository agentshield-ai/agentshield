import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import * as weft from '../src/index.js';
import { FakeWeft } from './fakeServer.js';

let fake: FakeWeft;
let client: weft.WeftClient;

beforeEach(async () => {
  fake = await new FakeWeft().start();
  client = await weft.init('ts-entity/ts-proj', { serverUrl: fake.url, sender: { flushIntervalMs: 5, backoffMs: 1 }, settings: { printCallLink: false } });
});

afterEach(async () => {
  await weft.finish();
  await fake.close();
});

const last = (path: string) => fake.requests.filter((r) => r.path === path).at(-1)!.body;

describe('SavedView', () => {
  it('builds, saves, loads, lists and applies a view', async () => {
    const answer = weft.trace(function answer(q: string) {
      return q.toUpperCase();
    });
    await answer('a');
    const view = new weft.SavedView({ label: 'Long answers' })
      .filterOp('answer')
      .addFilter('output', 'contains', 'B')
      .addFilter('summary.weft.latency_ms', '>=', 0)
      .addFilter('Status', 'is not empty')
      .sortBy('started_at', 'asc')
      .addColumn('inputs.q', 'Question')
      .pinColumnLeft('inputs.q')
      .pageSize(25);
    expect(() => view.addFilter('nonsense', 'equals', 1)).toThrow();
    expect(() => view.addFilter('output', '~=', 1)).toThrow();
    expect(() => view.addFilter('output', 'is empty', 'x')).toThrow();
    const body: any = view.toCallsQuery({ limit: 5 });
    expect(body.project_id).toBe('ts-entity/ts-proj');
    expect(body.filter).toEqual({ names: ['weft:///ts-entity/ts-proj/function/answer:*'] });
    expect(body.query.$expr.$and[0]).toEqual({ $contains: { input: { $getField: 'output' }, substr: { $literal: 'B' } } });
    expect(body.query.$expr.$and[1]).toEqual({ $gte: [{ $getField: 'summary.weft.latency_ms' }, { $literal: 0 }] });
    expect(view.filters.map((f) => f.operator)).toEqual(['(string): contains', '(number): >=', '(any): isNotEmpty']);
    expect(view.getTableColumns()).toEqual([{ path: 'inputs.q', label: 'Question' }]);
    await view.save();
    expect(view.name).toBe('Long-answers');
    const stored = fake.objs.get('Long-answers')![0].value;
    expect(stored._class_name).toBe('SavedView');
    expect(stored.definition.cols).toEqual(['inputs.q']);
    await view.spans();
    const q = last('/api/v1/spans/stream');
    expect(q.filter.names).toEqual(body.filter.names);
    expect(stored.definition.filter).toEqual({ names: ['weft:///ts-entity/ts-proj/function/answer:*'] });
    expect(q.sort_by).toEqual([{ field: 'started_at', direction: 'asc' }]);
    const loaded = await weft.SavedView.load('Long-answers');
    expect(loaded).toBeInstanceOf(weft.SavedView);
    expect(loaded.label).toBe('Long answers');
    expect(loaded.uiUrl()).toMatch(/\/ts-entity\/ts-proj\/traces\?view=Long-answers$/);
    expect((await client.getSavedViews()).map((v) => v.name)).toEqual(['Long-answers']);
    expect(await client.getSavedViews('evaluations')).toEqual([]);
    loaded.removeFilter('output').removeFilter(0).hideColumn('inputs.q').removeColumns().removeFilters().filterOp(null);
    expect(loaded.definition.query).toBeUndefined();
    expect(loaded.definition.filter).toBeUndefined();
  });

  it('round-trips every operator and drops stale ui_params', () => {
    const fs: weft.ViewFilter[] = [
      { field: 'output', operator: '(string): notContains', value: 'x' },
      { field: 'output', operator: '(string): notEquals', value: 'x' },
      { field: 'inputs.n', operator: '(number): =', value: 2 },
      { field: 'inputs.n', operator: '(number): !=', value: 2 },
      { field: 'inputs.n', operator: '(number): <', value: 2 },
      { field: 'inputs.n', operator: '(number): <=', value: 2 },
      { field: 'inputs.flag', operator: '(bool): is', value: true },
      { field: 'started_at', operator: '(date): after', value: 1700000000 },
      { field: 'started_at', operator: '(date): before', value: 1700000000 },
      { field: 'op_name', operator: '(string): in', value: ['a', 'b'] },
      { field: 'exception', operator: '(any): isEmpty' },
    ];
    for (const f of fs) expect(weft.queryToFilters({ $expr: { $and: [weft.filterToClause(f)] } })).toEqual([f]);
    expect(weft.filterToClause({ field: 'started_at', operator: '(date): after', value: '2024-01-01T00:00:00Z' })).toEqual({ $gt: [{ $getField: 'started_at' }, { $literal: 1704067200 }] });
    const v = new weft.SavedView({ viewType: 'evaluations', definition: { ui_params: { op: 'x' } } });
    expect((v.toCallsQuery() as any).filter).toEqual({ names: ['Evaluation.evaluate'] });
    v.search('foo').setFilter({ trace_roots_only: true });
    expect(v.definition.ui_params).toBeUndefined();
    expect(() => v.setFilter({ bogus: 1 })).toThrow();
  });
});

describe('EvaluationLogger links', () => {
  it('links existing calls, turns and batch results', async () => {
    const agent = weft.trace(function agent(q: string) {
      return `answer:${q}`;
    });
    const [, c1] = await agent.call('one');
    const turn = weft.startTurn({ userMessage: 'two', agentName: 'bot' });
    turn.outputMessages = [weft.Message.assistant('answer:two')];
    turn.end();
    const batch = weft.logTurn({ conversationId: 'c-3', agentName: 'bot', messages: [weft.Message.user('three')] });
    const ev = new weft.EvaluationLogger({ model: 'bot', dataset: 'agent-evals', name: 'agent eval' });
    const p1 = ev.logPrediction({ q: 'one' }, undefined, { call: c1 });
    p1.logScore('correct', true);
    p1.finish();
    const p2 = ev.logPrediction({ q: 'two' }, 'answer:two', { call: turn });
    p2.finish();
    const p3 = ev.logPrediction({ q: 'three' }, undefined, { call: [batch] });
    p3.finish();
    const summary = await ev.logSummary();
    await client.flush();
    expect(p1.output).toBe('answer:one');
    expect(p1.modelLatency).not.toBeNull();
    const pas = fake.calls.get(p1.predictAndScoreCall!.id);
    expect(pas.attributes.weft.linked_calls).toEqual([{ call_id: c1!.id, trace_id: c1!.traceId }]);
    expect(pas.attributes.weft.genai_span_ref).toEqual([{ trace_id: c1!.traceId, span_id: c1!.id }]);
    expect(fake.calls.get(p1.predictCall!.id).output).toBe('answer:one');
    expect(fake.calls.get(p2.predictAndScoreCall!.id).attributes.weft.linked_calls[0].call_id).toBe(turn.callId);
    const w3 = fake.calls.get(p3.predictAndScoreCall!.id).attributes.weft;
    expect(w3.linked_calls[0].call_id).toBe(batch.rootSpanIds[0]);
    expect(w3.linked_conversation_ids).toEqual(['c-3']);
    expect(summary.correct).toEqual({ true_count: 1, true_fraction: 1 });
    expect(() => ev.logPrediction({ q: 1 }, undefined, { call: 42 })).toThrow();
  });

  it('pred.run nests calls under the predict call and stamps agent spans', async () => {
    const retrieve = weft.trace(function retrieve(q: string) {
      return [q];
    });
    const ev = new weft.EvaluationLogger({ model: 'm', dataset: [{ q: 'hi' }], name: 'ctx eval' });
    const pred = ev.logPrediction({ q: 'hi' }, undefined, { exampleId: 'ex-1', trialIndex: 0 });
    let turn: weft.Turn | null = null;
    await pred.run(async (p) => {
      const docs = await retrieve('hi');
      await (turn = weft.startTurn({ userMessage: 'hi', agentName: 'rag' })).run(() => undefined);
      p.output = { docs };
      p.logScore('has_docs', true);
    });
    await ev.logSummary();
    await client.flush();
    const pc = fake.calls.get(pred.predictCall!.id);
    expect(pc.output).toEqual({ docs: ['hi'] });
    expect(fake.callsByOp('retrieve')[0].parent_id).toBe(pc.id);
    const link = fake.calls.get(turn!.callId!).attributes.weft.eval;
    expect(link.predict_and_score_call_id).toBe(pred.predictAndScoreCall!.id);
    expect(link.run_id).toBe(ev.call!.id);
    expect(link.example_id).toBe('ex-1');
    expect(link.trial_index).toBe(0);
    expect(link.evaluation_name).toBe('ctx eval');
    const pas = fake.calls.get(pred.predictAndScoreCall!.id);
    expect(pas.output.scores).toEqual({ has_docs: true });
    expect(typeof pas.output.model_latency).toBe('number');
  });
});

describe('objects, client queries and media references', () => {
  it('prompts, LLMStructuredCompletionModel and dataset helpers', async () => {
    const sp = new weft.StringPrompt({ name: 'greet', content: 'Hello {name}, {unknown}' });
    expect(sp.format({ name: 'Ada' })).toBe('Hello Ada, {unknown}');
    const mp = new weft.MessagesPrompt([{ role: 'user', content: 'Q: {q}' }, { role: 'user', content: [{ type: 'text', text: '{q}?' }] }]);
    expect(mp.format({ q: 'why' })).toEqual([{ role: 'user', content: 'Q: why' }, { role: 'user', content: [{ type: 'text', text: 'why?' }] }]);
    const model = new weft.LLMStructuredCompletionModel({
      name: 'judge',
      llmModelId: 'mock/json',
      defaultParams: { messages_template: [{ role: 'user', content: '{q}' }], response_format: 'json_object', temperature: 0 },
    });
    expect(await model.predict({ q: 'hi' })).toEqual({ correct: true, echo: 'hi' });
    expect(last('/api/v1/completions').inputs).toMatchObject({ model: 'mock/json', temperature: 0, response_format: { type: 'json_object' } });
    for (const o of [sp, mp, model]) await weft.save(o);
    expect(fake.objs.get('judge')![0].value).toMatchObject({ _class_name: 'LLMStructuredCompletionModel', llm_model_id: 'mock/json', _bases: ['Model', 'Object', 'BaseModel'] });
    const loadedPrompt = await weft.get('greet');
    expect(loadedPrompt).toBeInstanceOf(weft.StringPrompt);
    expect(loadedPrompt.format({ name: 'x' })).toBe('Hello x, {unknown}');
    expect(await weft.get('judge')).toBeInstanceOf(weft.LLMStructuredCompletionModel);
    const ds = new weft.Dataset({ name: 'd', rows: [{ i: 0 }, { i: 1 }, { i: 2 }] });
    expect(ds.select([2, 0]).rows).toEqual([{ i: 2 }, { i: 0 }]);
    expect(() => ds.select([])).toThrow();
    expect(() => ds.select([9])).toThrow();
    expect(ds.columnNames).toEqual(['i']);
    expect(ds.numRows).toBe(3);
    const fc = weft.Dataset.fromCalls([{ inputs: { self: 'x', q: 1 }, output: 2 }], 'from-calls');
    expect(fc.rows).toEqual([{ inputs: { q: 1 }, output: 2 }]);
    expect(weft.parseUri('weft:///e/p/object/x:abc').name).toBe('x');
  });

  it('client query APIs and call helpers', async () => {
    fake.routes.set('/api/v1/spans/query', (b) => ({ items: [], total_count: 7, echo: b }));
    fake.routes.set('/api/v1/spans/sessions', (b) => ({ items: [{ session_id: 't1', turn_count: 2, echo: b }], total_count: 1 }));
    fake.routes.set('/api/v1/prices/query', (b) => ({ items: [{ id: 'c0', llm_id: 'gpt-x', echo: b }], total_count: 1 }));
    fake.routes.set('/api/v1/prices/delete', () => ({}));
    fake.routes.set('/api/v1/evaluations/compare', (b) => ({ evaluations: b.ids.map((id: string) => ({ id })) }));
    fake.routes.set('/api/v1/leaderboards/compute', (b) => ({ leaderboard: { name: b.name }, columns: [], rows: [], req: b }));
    let polls = 0;
    fake.routes.set('/api/v1/evaluations/run', () => {
      fake.calls.set('srv', { id: 'srv', project_id: 'ts-entity/ts-proj', name: 'Evaluation.evaluate', trace_id: 't', output: { ok: 1 } });
      return { id: 'srv' };
    });
    fake.routes.set('/api/v1/evaluations/srv/status', () => ({ code: ++polls >= 2 ? 'complete' : 'running' }));
    const parent = weft.trace(async function parent() {
      await child();
      return 1;
    });
    const child = weft.trace(function child() {
      return 2;
    });
    const [, call] = await parent.call();
    expect(call!.status).toBe('success');
    expect(call!.uiUrl).toBe(`${fake.url}/ts-entity/ts-proj/spans/${call!.id}`);
    expect(call!.ref.uri()).toBe(call!.uri());
    expect((await call!.children()).map((c) => c.baseName)).toEqual(['child']);
    expect(await client.countSpans({ filter: { names: ['parent'] } })).toBe(7);
    await client.deleteSpans([call!.id]);
    expect(fake.calls.get(call!.id).deleted_at).toBe('now');
    const fid = await call!.feedback.addNote('hi');
    expect((await client.getFeedback({ query: { $eq: [{ $getField: 'ref' }, { $literal: call!.uri() }] } }))[0].id).toBe(fid);
    await call!.feedback.purge(fid);
    expect(last('/api/v1/feedback/delete').ids).toEqual([fid]);
    const [t] = await client.getThreads({ threadIds: ['t1'], limit: 5 });
    expect(t).toMatchObject({ session_id: 't1', thread_id: 't1' });
    expect(t.echo).toMatchObject({ filter: { session_ids: ['t1'] }, limit: 5 });
    expect((await client.queryCosts({ llmIds: ['gpt-x'] }))[0].echo.query.$expr.$in[1]).toEqual([{ $literal: 'gpt-x' }]);
    await client.purgeCosts([['c0', 'gpt-x'], { id: 'c1' }, 'c2']);
    expect(last('/api/v1/prices/delete').ids).toEqual(['c0', 'c1', 'c2']);
    expect((await client.compareEvaluations(['a', 'b'])).evaluations.length).toBe(2);
    expect(last('/api/v1/evaluations/compare').ids).toEqual(['a', 'b']);
    expect((await client.leaderboard('lb')).req).toEqual({ project_id: 'ts-entity/ts-proj', name: 'lb' });
    const res = await client.evaluateModel('weft:///ts-entity/ts-proj/object/ev:abc', 'weft:///ts-entity/ts-proj/object/m:def', { pollIntervalMs: 1 });
    expect(res).toEqual({ id: 'srv', callId: 'srv', status: { code: 'complete' }, summary: { ok: 1 } });
    expect(polls).toBeGreaterThanOrEqual(2);
    expect((await client.evaluationRuns()).map((c) => c.id)).toContain('srv');
    await weft.save(new weft.Evaluation({ name: 'e1', dataset: [{ q: 1 }], scorers: [] }), 'e1');
    expect((await client.getEvaluations()).length).toBe(1);
    expect(weft.otel.otlpEndpoint('http://h:1/')).toBe('http://h:1/otel/v1/traces');
    expect(weft.otel.otlpHeaders('proj', 'k')).toEqual({ project_id: 'default/proj', Authorization: 'Bearer k' });
  });

  it('logs media by reference (bring your own bucket) and resolves URLs', async () => {
    fake.routes.set('/api/v1/files/resolve', (b) => ({
      download_urls: b.uris.map((u: string) =>
        u.startsWith('http') ? u : u.startsWith('s3://') ? `https://s3.fake/${u.slice(5)}?sig=1` : `${fake.url}/api/v1/files/${u.split('://').at(-1)}?project_id=${b.project_id}`,
      ),
      expires_at: null,
    }));
    const img = weft.Image.fromUri('s3://bucket/photos/cat.png');
    const doc = weft.Content.fromUri('https://example.com/report.pdf', { size: 1234 });
    expect(img.isReference).toBe(true);
    expect(img.mimetype).toBe('image/png');
    expect(img.filename).toBe('cat.png');
    expect(doc.size).toBe(1234);
    expect(() => img.data).toThrow(/reference/);
    expect(() => weft.Content.fromUri('nope')).toThrow();
    const local = new weft.Content('stored bytes', { mimetype: 'text/plain' });
    const look = weft.trace(function look(image: unknown, d: unknown, l: unknown) {
      return 'ok';
    });
    await look(img, doc, local);
    await client.flush();
    const [c] = fake.callsByOp('look');
    expect(c.inputs.image).toEqual({ _type: 'Image', uri: 's3://bucket/photos/cat.png', mime_type: 'image/png', filename: 'cat.png' });
    expect(c.inputs.d).toEqual({ _type: 'Content', mimetype: 'application/pdf', filename: 'report.pdf', uri: 'https://example.com/report.pdf', size: 1234 });
    expect(fake.files.size).toBe(1); // only the local content was uploaded
    expect(await client.resolveUrl(local)).toBe(`${fake.url}/api/v1/files/${local.digest}?project_id=ts-entity/ts-proj`);
    expect(await client.resolveUrl(['s3://bucket/k', 'https://x.test/a'], { expiresIn: 60 })).toEqual(['https://s3.fake/bucket/k?sig=1', 'https://x.test/a']);
    expect(last('/api/v1/files/resolve').expires_in).toBe(60);
    const ref = weft.Content.fromUri(`weft-file://${local.digest}`, { mimetype: 'text/plain' });
    expect(Buffer.from(await ref.load()).toString()).toBe('stored bytes');
    expect(ref.asText()).toBe('stored bytes');
    expect(await img.resolveUrl()).toBe('https://s3.fake/bucket/photos/cat.png?sig=1');
  });
});
