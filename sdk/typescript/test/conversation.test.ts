import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import * as weft from '../src/index.js';
import { FakeWeft } from './fakeServer.js';

let fake: FakeWeft;
let client: weft.WeftClient;

beforeEach(async () => {
  fake = await new FakeWeft().start();
  client = await weft.init('ts-entity/ts-proj', { serverUrl: fake.url, sender: { flushIntervalMs: 5, backoffMs: 1 }, settings: { printSpanLink: false } });
});

afterEach(async () => {
  weft.endConversation();
  await weft.finish();
  await fake.close();
});

const attr = (c: any, dotted: string): any => dotted.split('.').reduce((v, k) => (v && typeof v === 'object' ? v[k] : undefined), c.attributes);

describe('agent conversations', () => {
  it('records turns, LLM calls, tools and nested ops', async () => {
    const lookup = weft.trace(function lookup(id: number) {
      return 'shipped';
    });
    const conv = weft.startConversation({ agentName: 'support', conversationName: 'Order help', attributes: { team: 'cx' } });
    expect(weft.getCurrentConversation()).toBe(conv);
    const turn = conv.startTurn({ userMessage: 'Where is order 7?', systemInstructions: ['Be brief.'] });
    expect(weft.getCurrentTurn()).toBe(turn);
    await turn.run(async (t) => {
      await t.startLLM({ model: 'gpt-4o-mini', providerName: 'openai' }).run((llm) => {
        expect(weft.getCurrentLLM()).toBe(llm);
        llm.inputMessages = [weft.Message.user('Where is order 7?')];
        llm.outputMessages = [weft.Message.assistant('Checking.', { toolCalls: [{ id: 't1', name: 'lookup', arguments: { id: 7 } }] })];
        llm.usage = { inputTokens: 12, outputTokens: 4 };
        llm.finishReasons = ['tool_calls'];
        llm.think('need status');
      });
      await t.startTool({ name: 'lookup', arguments: { id: 7 }, toolCallId: 't1' }).run(async (tool) => {
        tool.result = { status: await lookup(7) };
      });
      t.outputMessages = [weft.Message.assistant('Order 7 has shipped.')];
    });
    conv.end();
    expect(weft.getCurrentConversation()).toBeNull();
    await client.flush();
    const [tc] = fake.callsByOp('invoke_agent');
    const [l] = fake.callsByOp('chat');
    const [tl] = fake.callsByOp('execute_tool');
    const [op] = fake.callsByOp('lookup');
    expect(tc.parent_id).toBeNull();
    expect(tc.turn_id).toBe(tc.id);
    expect(tc.session_id).toBe(conv.conversationId);
    expect(l.parent_id).toBe(tc.id);
    expect(tl.parent_id).toBe(tc.id);
    expect(op.parent_id).toBe(tl.id);
    expect(new Set([l.trace_id, tl.trace_id, op.trace_id])).toEqual(new Set([tc.trace_id]));
    expect(op.session_id).toBe(conv.conversationId);
    expect(op.turn_id).toBe(tc.id);
    expect(attr(tc, 'gen_ai.agent.name')).toBe('support');
    expect(attr(tc, 'gen_ai.operation.name')).toBe('invoke_agent');
    expect(attr(tc, 'gen_ai.conversation.name')).toBe('Order help');
    expect(attr(tc, 'weft.span_kind')).toBe('agent');
    expect(attr(l, 'weft.span_kind')).toBe('llm');
    expect(attr(tl, 'team')).toBe('cx');
    expect(tc.display_name).toBe('invoke_agent support');
    expect(tc.inputs.messages).toEqual([
      { role: 'system', content: 'Be brief.' },
      { role: 'user', content: 'Where is order 7?' },
    ]);
    expect(tc.output).toEqual({ messages: [{ role: 'assistant', content: 'Order 7 has shipped.' }] });
    const choice = l.output.choices[0];
    expect(choice.finish_reason).toBe('tool_calls');
    expect(choice.message.tool_calls).toEqual([{ id: 't1', type: 'function', function: { name: 'lookup', arguments: '{"id":7}' } }]);
    expect(choice.message.content[0]).toEqual({ type: 'thinking', thinking: 'need status' });
    expect(l.summary.usage['gpt-4o-mini']).toEqual({ requests: 1, prompt_tokens: 12, completion_tokens: 4, total_tokens: 16 });
    expect(tl.inputs).toEqual({ id: 7 });
    expect(tl.output).toEqual({ status: 'shipped' });
    expect(attr(tl, 'gen_ai.tool.call.id')).toBe('t1');
    expect(turn.callId).toBe(tc.id);
  });

  it('starts a new trace per turn, ends the previous turn and nests with continueParentTrace', async () => {
    const conv = weft.startConversation({ agentName: 'a' });
    const t1 = conv.startTurn({ userMessage: 'one' }).start();
    const t2 = conv.startTurn({ userMessage: 'two' });
    expect(t1.ended).toBe(true);
    t2.end();
    const handler = weft.trace(async function handler() {
      const c = weft.startConversation({ agentName: 'nested', continueParentTrace: true });
      await c.run(async () => {
        await weft.startTurn({ userMessage: 'hi' }).run(() => undefined);
      });
    });
    await handler();
    await client.flush();
    const turns = fake.callsByOp('invoke_agent');
    const [one, two] = turns.filter((c) => attr(c, 'gen_ai.agent.name') === 'a');
    expect(one.trace_id).not.toBe(two.trace_id);
    const nested = turns.find((c) => attr(c, 'gen_ai.agent.name') === 'nested');
    const [h] = fake.callsByOp('handler');
    expect(nested.parent_id).toBe(h.id);
    expect(nested.trace_id).toBe(h.trace_id);
  });

  it('module functions, sub-agents, errors and includeContent=false', async () => {
    const conv = weft.startConversation({ agentName: 'planner', model: 'gpt-4o' });
    const turn = weft.startTurn({ userMessage: 'plan' });
    const llm = weft.startLLM({ providerName: 'openai' });
    llm.output('asking researcher');
    llm.end();
    const sub = turn.startSubagent({ name: 'researcher', model: 'claude' });
    await sub.run(async (s) => {
      s.record({ inputMessages: [weft.Message.user('find')], agentVersion: '2' });
      await s.startTool({ name: 'search', arguments: 'flights' }).run((t) => {
        t.result = '3 results';
      });
    });
    await expect(
      weft.startTool({ name: 'flaky' }).run(() => {
        throw new Error('tool broke');
      }),
    ).rejects.toThrow('tool broke');
    weft.endTurn();
    conv.end();
    const priv = weft.startConversation({ agentName: 'private', includeContent: false });
    await priv.run(async (c) => {
      await c.startTurn({ userMessage: 'secret' }).run(async (t) => {
        await t.startLLM({ model: 'm' }).run((l) => {
          l.output('secret answer');
          l.usage = { inputTokens: 3 };
        });
      });
    });
    await client.flush();
    const planner = fake.callsByOp('invoke_agent').find((c) => attr(c, 'gen_ai.agent.name') === 'planner');
    const researcher = fake.callsByOp('invoke_agent').find((c) => attr(c, 'gen_ai.agent.name') === 'researcher');
    const search = fake.callsByOp('execute_tool').find((c) => attr(c, 'gen_ai.tool.name') === 'search');
    const flaky = fake.callsByOp('execute_tool').find((c) => attr(c, 'gen_ai.tool.name') === 'flaky');
    expect(researcher.parent_id).toBe(planner.id);
    expect(search.parent_id).toBe(researcher.id);
    expect(search.inputs).toEqual({ arguments: 'flights' });
    expect(flaky.parent_id).toBe(planner.id);
    expect(flaky.error).toContain('tool broke');
    expect(attr(flaky, 'error.type')).toBe('Error');
    expect(attr(researcher, 'gen_ai.agent.version')).toBe('2');
    const chats = fake.callsByOp('chat');
    expect(chats.map((c) => attr(c, 'gen_ai.request.model')).sort()).toEqual(['gpt-4o', 'm']);
    const secret = JSON.stringify([...fake.calls.values()].filter((c) => attr(c, 'gen_ai.agent.name') === 'private' || attr(c, 'gen_ai.request.model') === 'm').map((c) => [c.inputs, c.output]));
    expect(secret).not.toContain('secret');
    expect(chats.find((c) => attr(c, 'gen_ai.request.model') === 'm').summary.usage.m.prompt_tokens).toBe(3);
  });

  it('batch logs turns and conversations', async () => {
    const t0 = new Date('2026-01-01T12:00:00Z');
    const llm = new weft.LLM({ model: 'gpt-4o-mini', startedAt: t0, endedAt: new Date(t0.getTime() + 1000), usage: { inputTokens: 5, outputTokens: 5 } });
    llm.output('hello');
    const tool = new weft.Tool({ name: 'clock', result: '12:00', startedAt: new Date(t0.getTime() + 1000), endedAt: new Date(t0.getTime() + 2000) });
    const res = weft.logTurn({ conversationId: 'conv-batch', agentName: 'batcher', messages: [weft.Message.user('time?')], spans: [llm, tool], attributes: { source: 'replay' } });
    expect(res.spanCount).toBe(3);
    const turns = [0, 1].map((i) => new weft.Turn({ messages: [weft.Message.user(`q${i}`)], spans: [new weft.Tool({ name: 't', result: i })] }));
    const res2 = weft.logConversation({ turns, agentName: 'batcher', conversationName: 'replayed' });
    expect(res2.spanCount).toBe(4);
    expect(res2.traceIds.length).toBe(2);
    await client.flush();
    const t = fake.calls.get(res.rootSpanIds[0]);
    expect(t.started_at).toBe('2026-01-01T12:00:00.000Z');
    expect(t.ended_at).toBe('2026-01-01T12:00:02.000Z');
    expect(t.session_id).toBe('conv-batch');
    expect(attr(t, 'source')).toBe('replay');
    expect(fake.children(t.id).map((c) => fake.opBase(c)).sort()).toEqual(['chat', 'execute_tool']);
    for (const id of res2.rootSpanIds) {
      const c = fake.calls.get(id);
      expect(c.session_id).toBe(res2.conversationId);
      expect(attr(c, 'gen_ai.conversation.name')).toBe('replayed');
    }
    expect(turns[0].agentName).toBe('');
    expect(turns[0].call).toBeNull();
  });

  it('uploads media attachments and honours agent-name overrides and tracing off', async () => {
    const png = new Uint8Array([0x89, 0x50, 0x4e, 0x47, 13, 10, 26, 10, 1, 2, 3]);
    await weft.withAgentNameOverride('auto', () =>
      weft.startTurn({ userMessage: 'what is this?' }).run(async (t) => {
        await t.startLLM({ model: 'gpt-4o' }).run((l) => {
          l.inputMessages = [weft.Message.user('what is this?')];
          l.attachMedia({ content: png, mimeType: 'image/png' });
          l.attachMediaUrl('https://example.com/cat.jpg', { modality: 'image' });
          expect(() => l.attachMedia({ content: png, uri: 'x' })).toThrow();
        });
      }),
    );
    let quiet: weft.Turn | null = null;
    await weft.withTracingDisabled(() => (quiet = weft.startTurn({ userMessage: 'x' })).run(() => undefined));
    await client.flush();
    const [t] = fake.callsByOp('invoke_agent');
    expect(attr(t, 'gen_ai.agent.name')).toBe('auto');
    expect(quiet!.call).toBeNull();
    const parts = fake.callsByOp('chat')[0].inputs.messages.at(-1).content;
    expect(parts[0]).toEqual({ type: 'text', text: 'what is this?' });
    expect(parts[1]._type).toBe('Image');
    expect(fake.files.has(parts[1].file_digest)).toBe(true);
    expect(parts[2]).toEqual({ type: 'uri', uri: 'https://example.com/cat.jpg', modality: 'image' });
  });
});
