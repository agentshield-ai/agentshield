// Weft's SDK names (docs/weft/DESIGN.md §10.4) and the deprecated earlier names.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import * as weft from '../src/index.js';
import { DEPRECATION_CODE, deprecated, resetDeprecationWarnings } from '../src/deprecation.js';
import { loadPlatformObject } from '../src/platform.js';
import { FakeWeft } from './fakeServer.js';

let fake: FakeWeft;
let client: weft.WeftClient;
const listeners: Array<(w: Error) => void> = [];

/**
 * Collect the SDK's deprecation warnings: `process.emitWarning` DeprecationWarnings with
 * code WEFT_DEPRECATED, seen through `process.on('warning')` (emitted on the next tick).
 */
function captureDeprecations(): { messages(): Promise<string[]>; consoleWarn: ReturnType<typeof vi.spyOn> } {
  const seen: string[] = [];
  const onWarning = (w: Error & { code?: string }) => {
    if (w.name === 'DeprecationWarning' && w.code === DEPRECATION_CODE) seen.push(w.message);
  };
  process.on('warning', onWarning);
  listeners.push(onWarning);
  const consoleWarn = vi.spyOn(console, 'warn');
  return {
    consoleWarn,
    async messages() {
      await new Promise((r) => setImmediate(r));
      return [...seen];
    },
  };
}

beforeEach(async () => {
  fake = await new FakeWeft().start();
  client = await weft.init('ts-entity/ts-proj', { serverUrl: fake.url, sender: { flushIntervalMs: 5 }, settings: { printSpanLink: false } });
  resetDeprecationWarnings();
});

afterEach(async () => {
  for (const l of listeners.splice(0)) process.off('warning', l);
  vi.restoreAllMocks();
  await weft.finish();
  await fake.close();
});

describe('names', () => {
  it('trace, save and get', async () => {
    const warn = captureDeprecations();
    const greet = weft.trace(function greet(name: string) {
      return `hello ${name}`;
    });
    expect(await greet('ada')).toBe('hello ada');
    const ref = await weft.save(new weft.StringPrompt({ content: 'Hello {name}' }), 'greeting');
    expect(ref.uri()).toMatch(/^weft:\/\/\/ts-entity\/ts-proj\/object\/greeting:[0-9a-f]{64}$/);
    expect((await weft.get(ref)).format({ name: 'Ada' })).toBe('Hello Ada');
    expect((await weft.get('greeting:v0')).format({ name: 'B' })).toBe('Hello B');
    const [span] = await client.spans({ filter: { names: ['greet'] } });
    expect(span).toBeInstanceOf(weft.Span);
    expect(span.baseName).toBe('greet');
    expect(span.name).toMatch(/^weft:\/\/\/ts-entity\/ts-proj\/function\/greet:[0-9a-f]{64}$/);
    expect(span.error).toBeNull();
    expect(span.sessionId).toBeNull();
    expect((await weft.save(greet)).uri()).toBe(span.name);
    expect((await client.span(span.id))!.output).toBe('hello ada');
    expect(await client.span('no-such-span')).toBeNull();
    expect((await weft.get(span.ref))!.id).toBe(span.id);
    expect(span.uri()).toBe(`weft:///ts-entity/ts-proj/span/${span.id}`);
    expect(await warn.messages()).toEqual([]);
  });

  it('reads refs written with the earlier kind names', () => {
    expect(weft.parseUri('weft:///e/p/op/f:abc').uri()).toBe('weft:///e/p/function/f:abc');
    expect(weft.parseUri('weft:///e/p/call/123').uri()).toBe('weft:///e/p/span/123');
  });

  it('earlier names still work and warn once each', async () => {
    const warn = captureDeprecations();
    const inc = weft.op(function inc(x: number) {
      return x + 1;
    });
    weft.op(function again() {});
    expect(await inc(1)).toBe(2);
    const ref = await weft.publish({ v: 1 }, 'dep');
    expect(await weft.ref('dep').get()).toEqual({ v: 1 });
    expect((await client.publish({ v: 2 }, 'dep')).digest).not.toBe(ref.digest);
    const spans = await client.getCalls({ filter: { op_names: ['inc'] } });
    expect(spans.length).toBe(1);
    const one = (await client.getCall(spans[0].id))!;
    expect(one.opName).toBe(one.name);
    expect(one.exception).toBeNull();
    expect(one.threadId).toBeNull();
    expect(new weft.Call({ id: 'x', project_id: 'a/b', name: 'n', trace_id: 't' }, null)).toBeInstanceOf(weft.Span);
    expect(weft.getCurrentCall()).toBeNull();
    const msgs = await warn.messages();
    expect(warn.consoleWarn).not.toHaveBeenCalled(); // Node: DeprecationWarning events, not console output
    for (const m of ['op() is deprecated; use trace()', 'publish() is deprecated; use save()', 'ref(...).get() is deprecated', 'WeftClient.getCalls()', 'WeftClient.getCall()', 'Span.opName', 'Span.exception', 'Span.threadId', 'Call is deprecated; use Span', 'getCurrentCall()'])
      expect(msgs.some((x) => x.includes(m))).toBe(true);
    expect(msgs.filter((x) => x.includes('op() is deprecated')).length).toBe(1); // once per name
  });
  it('platform objects use names; the earlier field names still work', async () => {
    const warn = captureDeprecations();
    const mon = new weft.Monitor({ name: 'm', opNames: ['f'] });
    expect(mon.names).toEqual(['f']);
    expect(mon.fields()).toMatchObject({ names: ['f'] });
    expect(mon.fields()).not.toHaveProperty('op_names');
    expect(mon.opNames).toEqual(['f']);
    const spec = new weft.AnnotationSpec({ name: 's', opScope: ['g'] });
    expect(spec.fields()).toMatchObject({ names: ['g'] });
    expect(await warn.messages()).toEqual([
      'weft: Monitor opNames is deprecated; use names',
      'weft: Monitor.opNames is deprecated; use Monitor.names',
      'weft: AnnotationSpec opScope is deprecated; use names',
    ]);
    const old = loadPlatformObject({ _class_name: 'Monitor', name: 'o', op_names: ['h'] }, undefined) as weft.Monitor;
    expect(old.names).toEqual(['h']);
    const view = new weft.SavedView({ definition: { filter: { op_names: ['a'], thread_ids: ['t'] } } });
    expect(view.toSpansQuery().filter).toEqual({ names: ['a'], session_ids: ['t'] });
    view.setFilter({ call_ids: ['c'] });
    expect(view.definition.filter).toEqual({ names: ['a'], session_ids: ['t'], ids: ['c'] });
  });
  it('saved view pins name; pins saved with op_name get Weft\'s column names', () => {
    const v = new weft.SavedView();
    v.pinColumnRight('started_at');
    expect(v.definition.pin).toEqual({ left: ['CustomCheckbox', 'name'], right: ['started_at'] });
    const old = new weft.SavedView({ definition: { pin: { left: ['CustomCheckbox', 'op_name'], right: ['thread_id'] } } });
    old.unpinColumn('session_id');
    expect(old.definition.pin).toEqual({ left: ['CustomCheckbox', 'name'], right: [] });
  });
  it('withSession, printSpanLink and updateSpan; the earlier names warn once', async () => {
    const warn = captureDeprecations();
    const f = weft.trace(function f() {
      return 1;
    });
    await weft.withSession('s-new', () => f());
    await weft.withThread('s-old', () => f());
    await client.flush();
    expect(fake.callsByOp('f').map((c) => c.session_id).sort()).toEqual(['s-new', 's-old']);
    const span = client.createSpan('upd', {});
    client.updateSpan(span, { a: 1 }, { sessionId: 'sess-1' });
    expect(span.sessionId).toBe('sess-1');
    client.updateCall(span, null, { threadId: 'sess-2' });
    expect(span.sessionId).toBe('sess-2');
    client.finishSpan(span, null);
    expect(client.printSpanLink).toBe(false);
    expect(client.printCallLink).toBe(false);
    const c2 = new weft.WeftClient('ts-entity/x', { serverUrl: fake.url, settings: { printCallLink: false } });
    expect(c2.printSpanLink).toBe(false);
    const prev = { span: process.env.WEFT_PRINT_SPAN_LINK, call: process.env.WEFT_PRINT_CALL_LINK };
    delete process.env.WEFT_PRINT_SPAN_LINK;
    process.env.WEFT_PRINT_CALL_LINK = 'false';
    try {
      expect(new weft.WeftClient('ts-entity/y', { serverUrl: fake.url }).printSpanLink).toBe(false);
      process.env.WEFT_PRINT_SPAN_LINK = 'true';
      expect(new weft.WeftClient('ts-entity/z', { serverUrl: fake.url }).printSpanLink).toBe(true);
    } finally {
      for (const [k, v] of [['WEFT_PRINT_SPAN_LINK', prev.span], ['WEFT_PRINT_CALL_LINK', prev.call]] as const) {
        if (v === undefined) delete process.env[k];
        else process.env[k] = v;
      }
    }
    const msgs = await warn.messages();
    expect(warn.consoleWarn).not.toHaveBeenCalled(); // Node: DeprecationWarning events, not console output
    for (const m of ['withThread() is deprecated; use withSession()', 'WeftClient.updateCall({ threadId })', 'WeftClient.printCallLink', 'settings.printCallLink', 'WEFT_PRINT_CALL_LINK is deprecated'])
      expect(msgs.some((x) => x.includes(m))).toBe(true);
  });
  it('getSessions, createSpan/finishSpan, logPrediction({ span }), predict*Span, conversation span/spanId, fromSpans and spanDisplayName', async () => {
    const warn = captureDeprecations();
    fake.routes.set('/api/v1/spans/sessions', (b) => ({ items: [{ session_id: 's1', started_at: 'x', echo: b }], total_count: 1 }));
    const [s] = await client.getSessions({ sessionIds: ['s1'], limit: 3 });
    expect(s).toMatchObject({ session_id: 's1', start_time: 'x', echo: { filter: { session_ids: ['s1'] }, limit: 3 } });
    expect(s).not.toHaveProperty('thread_id');
    const ask = weft.trace(function ask(q: number) {
      return q * 2;
    }, { spanDisplayName: (sp) => `q=${sp.inputs.q}` });
    await ask(1);
    const span = client.createSpan('explicit', { x: 1 });
    expect(span).toBeInstanceOf(weft.Span);
    client.finishSpan(span, { ok: true });
    const conv = weft.startConversation({ agentName: 'a' });
    const turn = conv.startTurn({ userMessage: 'hi' }).start();
    expect(turn.span).toBeInstanceOf(weft.Span);
    expect(turn.spanId).toBe(turn.span!.id);
    turn.end();
    conv.end();
    const ev = new weft.EvaluationLogger({ model: 'm', dataset: [{ q: 1 }] });
    expect(ev.span).toBeInstanceOf(weft.Span);
    const pred = ev.logPrediction({ q: 1 }, undefined, { span: turn });
    expect(pred.predictAndScoreSpan).toBeInstanceOf(weft.Span);
    expect(pred.predictSpan).toBeInstanceOf(weft.Span);
    pred.finish();
    await ev.logSummary();
    await client.flush();
    expect(fake.callsByOp('ask').map((c) => c.display_name)).toEqual(['q=1']);
    expect(fake.calls.get(span.id).output).toEqual({ ok: true });
    expect(fake.calls.get(pred.predictAndScoreSpan!.id).attributes.weft.linked_spans).toEqual([{ span_id: turn.spanId, trace_id: turn.traceId }]);
    expect(weft.Dataset.fromSpans([{ inputs: { self: 'x', q: 1 }, output: 2 }]).rows).toEqual([{ inputs: { q: 1 }, output: 2 }]);
    expect(client.spanUrl('s9')).toMatch(/\/spans\/s9$/);
    const prev = { spans: process.env.WEFT_MAX_SPANS_QUEUE_SIZE, calls: process.env.WEFT_MAX_CALLS_QUEUE_SIZE };
    process.env.WEFT_MAX_SPANS_QUEUE_SIZE = '77';
    process.env.WEFT_MAX_CALLS_QUEUE_SIZE = '33';
    try {
      expect(weft.senderOptionsFromEnv().maxQueue).toBe(77); // the new name wins
    } finally {
      for (const [k, v] of [['WEFT_MAX_SPANS_QUEUE_SIZE', prev.spans], ['WEFT_MAX_CALLS_QUEUE_SIZE', prev.calls]] as const) {
        if (v === undefined) delete process.env[k];
        else process.env[k] = v;
      }
    }
    expect(await warn.messages()).toEqual([]);
  });

  it('the earlier names of those still work and warn once each', async () => {
    const warn = captureDeprecations();
    fake.routes.set('/api/v1/spans/sessions', (b) => ({ items: [{ session_id: 't1', echo: b }], total_count: 1 }));
    const [t] = await client.getThreads({ threadIds: ['t1'] });
    expect(t).toMatchObject({ session_id: 't1', thread_id: 't1', echo: { filter: { session_ids: ['t1'] } } });
    await client.getThreads();
    const [s] = await client.getSessions({ threadIds: ['t1'] });
    expect(s.echo.filter).toEqual({ session_ids: ['t1'] });
    const old = weft.trace(function oldDn() {
      return 1;
    }, { callDisplayName: 'fixed' });
    await old();
    const span = client.createCall('explicit_old', {});
    client.finishCall(span, 5);
    const conv = weft.startConversation({ agentName: 'a' });
    const turn = conv.startTurn({ userMessage: 'hi' }).start();
    expect(turn.call).toBe(turn.span);
    expect(turn.callId).toBe(turn.spanId);
    turn.end();
    conv.end();
    const ev = new weft.EvaluationLogger({ model: 'm', dataset: [{ q: 1 }] });
    expect(ev.call).toBe(ev.span);
    const pred = ev.logPrediction({ q: 1 }, undefined, { call: turn });
    expect(pred.predictAndScoreCall).toBe(pred.predictAndScoreSpan);
    expect(pred.predictCall).toBe(pred.predictSpan);
    pred.finish();
    await ev.logSummary();
    await client.flush();
    expect(fake.callsByOp('oldDn').map((c) => c.display_name)).toEqual(['fixed']);
    expect(fake.calls.get(span.id).output).toBe(5);
    expect(fake.calls.get(pred.predictAndScoreSpan!.id).attributes.weft.linked_spans[0].span_id).toBe(turn.spanId);
    expect(weft.Dataset.fromCalls([{ inputs: { q: 1 }, output: 2 }]).rows).toEqual([{ inputs: { q: 1 }, output: 2 }]);
    expect(client.callUrl('c1')).toBe(client.spanUrl('c1'));
    const prev = { spans: process.env.WEFT_MAX_SPANS_QUEUE_SIZE, calls: process.env.WEFT_MAX_CALLS_QUEUE_SIZE, q: process.env.WEFT_MAX_QUEUE_SIZE };
    delete process.env.WEFT_MAX_SPANS_QUEUE_SIZE;
    process.env.WEFT_MAX_CALLS_QUEUE_SIZE = '33';
    process.env.WEFT_MAX_QUEUE_SIZE = '55';
    try {
      expect(weft.senderOptionsFromEnv().maxQueue).toBe(33);
    } finally {
      for (const [k, v] of [['WEFT_MAX_SPANS_QUEUE_SIZE', prev.spans], ['WEFT_MAX_CALLS_QUEUE_SIZE', prev.calls], ['WEFT_MAX_QUEUE_SIZE', prev.q]] as const) {
        if (v === undefined) delete process.env[k];
        else process.env[k] = v;
      }
    }
    const msgs = await warn.messages();
    expect(warn.consoleWarn).not.toHaveBeenCalled(); // Node: DeprecationWarning events, not console output
    for (const m of [
      'WeftClient.getThreads({ threadIds }) is deprecated; use WeftClient.getSessions({ sessionIds })',
      'WeftClient.getSessions({ threadIds }) is deprecated; use WeftClient.getSessions({ sessionIds })',
      'trace() option callDisplayName is deprecated; use spanDisplayName',
      'WeftClient.createCall() is deprecated; use WeftClient.createSpan()',
      'WeftClient.finishCall() is deprecated; use WeftClient.finishSpan()',
      'Turn.call is deprecated; use Turn.span',
      'Turn.callId is deprecated; use Turn.spanId',
      'EvaluationLogger.call is deprecated; use EvaluationLogger.span',
      'EvaluationLogger.logPrediction({ call }) is deprecated; use EvaluationLogger.logPrediction({ span })',
      'ScoreLogger.predictAndScoreCall is deprecated; use ScoreLogger.predictAndScoreSpan',
      'ScoreLogger.predictCall is deprecated; use ScoreLogger.predictSpan',
      'Dataset.fromCalls() is deprecated; use Dataset.fromSpans()',
      'WeftClient.callUrl() is deprecated; use WeftClient.spanUrl()',
      'WEFT_MAX_CALLS_QUEUE_SIZE is deprecated; use WEFT_MAX_SPANS_QUEUE_SIZE',
    ])
      expect(msgs).toContain(`weft: ${m}`);
    expect(msgs.filter((x) => x.includes('getThreads(')).length).toBe(1); // once per name
  });

  it('integration opSettings take spanDisplayName; callDisplayName still works and warns', async () => {
    const warn = captureDeprecations();
    const { integrationOp, pickOpSettings } = await import('../src/integrations/settings.js');
    expect(integrationOp(undefined, 'x.new', 'llm', {}, { spanDisplayName: 'new' }).options.spanDisplayName).toBe('new');
    expect(await warn.messages()).toEqual([]);
    expect(integrationOp(undefined, 'x.old', 'llm', {}, pickOpSettings({ callDisplayName: 'old' })).options.spanDisplayName).toBe('old');
    expect(await warn.messages()).toEqual(['weft: opSettings callDisplayName is deprecated; use spanDisplayName']);
  });
  it('deprecations are DeprecationWarning events once per name; console.warn where process.emitWarning is absent', async () => {
    const warn = captureDeprecations();
    deprecated('old()', 'new()');
    deprecated('old()', 'new()');
    expect(await warn.messages()).toEqual(['weft: old() is deprecated; use new()']);
    expect(warn.consoleWarn).not.toHaveBeenCalled();
    const emit = process.emitWarning;
    const out = vi.spyOn(console, 'warn').mockImplementation(() => {});
    (process as any).emitWarning = undefined;
    try {
      deprecated('older()', 'new()');
    } finally {
      process.emitWarning = emit;
    }
    expect(out.mock.calls.map((c) => String(c[0]))).toEqual(['weft: older() is deprecated; use new()']);
  });
});
