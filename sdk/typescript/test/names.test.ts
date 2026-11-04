// Weft's SDK names (docs/weft/DESIGN.md §10.4) and the deprecated earlier names.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import * as weft from '../src/index.js';
import { resetDeprecationWarnings } from '../src/deprecation.js';
import { loadPlatformObject } from '../src/platform.js';
import { FakeWeft } from './fakeServer.js';

let fake: FakeWeft;
let client: weft.WeftClient;

beforeEach(async () => {
  fake = await new FakeWeft().start();
  client = await weft.init('ts-entity/ts-proj', { serverUrl: fake.url, sender: { flushIntervalMs: 5 }, settings: { printSpanLink: false } });
  resetDeprecationWarnings();
});

afterEach(async () => {
  vi.restoreAllMocks();
  await weft.finish();
  await fake.close();
});

describe('names', () => {
  it('trace, save and get', async () => {
    const warn = vi.spyOn(console, 'warn');
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
    expect(warn).not.toHaveBeenCalled();
  });

  it('reads refs written with the earlier kind names', () => {
    expect(weft.parseUri('weft:///e/p/op/f:abc').uri()).toBe('weft:///e/p/function/f:abc');
    expect(weft.parseUri('weft:///e/p/call/123').uri()).toBe('weft:///e/p/span/123');
  });

  it('earlier names still work and warn once each', async () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {});
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
    const msgs = warn.mock.calls.map((c) => String(c[0]));
    for (const m of ['op() is deprecated; use trace()', 'publish() is deprecated; use save()', 'ref(...).get() is deprecated', 'WeftClient.getCalls()', 'WeftClient.getCall()', 'Span.opName', 'Span.exception', 'Span.threadId', 'Call is deprecated; use Span', 'getCurrentCall()'])
      expect(msgs.some((x) => x.includes(m))).toBe(true);
    expect(msgs.filter((x) => x.includes('op() is deprecated')).length).toBe(1); // once per name
  });
  it('platform objects use names; the earlier field names still work', async () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {});
    const mon = new weft.Monitor({ name: 'm', opNames: ['f'] });
    expect(mon.names).toEqual(['f']);
    expect(mon.fields()).toMatchObject({ names: ['f'] });
    expect(mon.fields()).not.toHaveProperty('op_names');
    expect(mon.opNames).toEqual(['f']);
    const spec = new weft.AnnotationSpec({ name: 's', opScope: ['g'] });
    expect(spec.fields()).toMatchObject({ names: ['g'] });
    expect(warn.mock.calls.map((c) => String(c[0]))).toEqual([
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
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {});
    const f = weft.trace(function f() {
      return 1;
    });
    await weft.withSession('s-new', () => f());
    await weft.withThread('s-old', () => f());
    await client.flush();
    expect(fake.callsByOp('f').map((c) => c.session_id).sort()).toEqual(['s-new', 's-old']);
    const span = client.createCall('upd', {});
    client.updateSpan(span, { a: 1 }, { sessionId: 'sess-1' });
    expect(span.sessionId).toBe('sess-1');
    client.updateCall(span, null, { threadId: 'sess-2' });
    expect(span.sessionId).toBe('sess-2');
    client.finishCall(span, null);
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
    const msgs = warn.mock.calls.map((c) => String(c[0]));
    for (const m of ['withThread() is deprecated; use withSession()', 'WeftClient.updateCall({ threadId })', 'WeftClient.printCallLink', 'settings.printCallLink', 'WEFT_PRINT_CALL_LINK is deprecated'])
      expect(msgs.some((x) => x.includes(m))).toBe(true);
  });
});
