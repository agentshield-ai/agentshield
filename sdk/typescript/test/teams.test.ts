/**
 * Teams, hand-offs, reasoning capture and sessions (docs/weft/DESIGN.md §2,
 * §2.1, §2.2, §4), in the conversation API and the integrations (fake
 * clients built from the providers' real response and event formats).
 */
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import * as weft from '../src/index.js';
import { FakeWeft } from './fakeServer.js';

let fake: FakeWeft;
let client: weft.WeftClient;

async function init(settings: weft.ClientSettings = {}): Promise<void> {
  client = await weft.init('ts-entity/teams', { serverUrl: fake.url, sender: { flushIntervalMs: 5, backoffMs: 1 }, settings: { printSpanLink: false, ...settings }, autopatchSettings: {} });
}

beforeEach(async () => {
  fake = await new FakeWeft().start();
  await init();
});

afterEach(async () => {
  weft.endConversation();
  await weft.finish();
  delete process.env.WEFT_CAPTURE_REASONING;
  await fake.close();
});

async function settle(): Promise<void> {
  await new Promise((r) => setTimeout(r, 10));
  await client.flush();
}

const attr = (c: any, dotted: string): any => {
  const flat = c.attributes?.[dotted];
  return flat !== undefined ? flat : dotted.split('.').reduce((v, k) => (v && typeof v === 'object' ? v[k] : undefined), c.attributes);
};
const byOpPrefix = (prefix: string) => [...fake.calls.values()].filter((c) => String(c.name ?? '').includes(`/function/${prefix}`));

describe('teams', () => {
  it('withTeam sets weft.team on ops and spans; conversations take a team option', async () => {
    const step = weft.trace(function step(x: number) {
      return x;
    });
    await weft.withTeam({ id: 'trip-planners', name: 'Trip planners' }, async () => {
      expect(weft.getCurrentTeam()).toEqual({ id: 'trip-planners', name: 'Trip planners' });
      await step(1);
      await weft.startTurn({ agentName: 'orchestrator', userMessage: 'hi' }).run(() => undefined);
    });
    await step(2);
    expect(weft.getCurrentTeam()).toBeNull();
    const conv = weft.startConversation({ agentName: 'a', team: ['t1', 'Team one'] });
    await conv.run(async (c) => {
      await c.startTurn({ userMessage: 'q' }).run(async () => {
        await step(3);
      });
    });
    weft.logTurn({ conversationId: 'c2', agentName: 'b', team: 't2' });
    await settle();
    const steps = byOpPrefix('step');
    const team = (x: number) => attr(steps.find((c) => c.inputs.x === x), 'weft.team');
    expect(team(1)).toEqual({ id: 'trip-planners', name: 'Trip planners' });
    expect(team(2)).toBeUndefined();
    expect(team(3)).toEqual({ id: 't1', name: 'Team one' });
    const turns = byOpPrefix('invoke_agent');
    expect(turns.map((t) => attr(t, 'weft.team.id')).sort()).toEqual(['t1', 't2', 'trip-planners']);
    expect(() => weft.withTeam('', () => 1)).toThrow();
  });
});

describe('hand-offs', () => {
  it('records weft.handoff.* with the running agent as from_agent', async () => {
    const conv = weft.startConversation({ agentName: 'triage', conversationId: 's-1' });
    const turn = conv.startTurn({ userMessage: 'refund please' });
    await turn.run(async () => {
      weft.handoff('billing', { input: 'refund order 7' });
    });
    await settle();
    const [h] = byOpPrefix('handoff');
    expect(attr(h, 'weft.handoff')).toEqual({ to_agent: 'billing', kind: 'handoff', from_agent: 'triage' });
    expect(h.inputs).toEqual({ input: 'refund order 7' });
    expect(h.parent_id).toBe(turn.spanId);
    expect(h.display_name).toBe('handoff to billing');
    expect(attr(h, 'gen_ai.conversation.id')).toBe('s-1');
    expect(attr(h, 'weft.span_kind')).toBe('handoff');
    expect(attr(h, 'gen_ai.operation')).toBeUndefined();
    expect(() => weft.startHandoff('x', { kind: 'bogus' })).toThrow();
    expect(() => weft.handoff('')).toThrow();
  });

  it('a delegate hand-off block nests the other agent; a turn of another agent nests under the running one', async () => {
    const conv = weft.startConversation({ agentName: 'orchestrator', conversationId: 's-2' });
    const outer = conv.startTurn({ userMessage: 'facts about Oslo' });
    let inner!: weft.Turn;
    let nested!: weft.Turn;
    let same!: weft.Turn;
    await outer.run(async () => {
      await weft.startHandoff('researcher', { input: 'find facts', kind: 'delegate' }).run(async (h) => {
        inner = conv.startTurn({ agentName: 'researcher', userMessage: 'find facts' });
        await inner.run((t) => {
          t.outputMessages = [weft.Message.assistant('3 facts')];
        });
        h.output = '3 facts';
      });
      nested = new weft.Turn({ agentName: 'editor' });
      await nested.run(() => undefined);
      same = new weft.Turn({ agentName: 'orchestrator' });
      await same.run(() => undefined);
    });
    await settle();
    const [h] = byOpPrefix('handoff');
    expect(attr(h, 'weft.handoff.kind')).toBe('delegate');
    expect(h.output).toBe('3 facts');
    const rc = fake.calls.get(inner.spanId!);
    expect(rc.parent_id).toBe(h.id);
    expect(rc.trace_id).toBe(outer.traceId);
    expect(rc.session_id).toBe('s-2');
    expect(fake.calls.get(outer.spanId!).ended_at).toBeTruthy(); // the outer turn stayed open and ended normally
    expect(fake.calls.get(nested.spanId!).parent_id).toBe(outer.spanId);
    expect(fake.calls.get(same.spanId!).parent_id).toBeNull();
  });

  it('turn and sub-agent helpers, and batch logging with a hand-off', async () => {
    const conv = weft.startConversation({ agentName: 'a', conversationId: 's-3' });
    const turn = conv.startTurn({ userMessage: 'go' });
    let sub!: weft.SubAgent;
    await turn.run(async (t) => {
      t.handoff('b', { input: 'x' });
      sub = t.startSubagent({ name: 'c' });
      await sub.run(async (s) => {
        await s.startHandoff('d', { kind: 'delegate' }).run(() => undefined);
      });
    });
    weft.logTurn({ conversationId: 's-4', agentName: 'e', spans: [new weft.Handoff({ toAgent: 'f', startedAt: new Date(), endedAt: new Date() })] });
    await settle();
    const hs = byOpPrefix('handoff').map((h) => [attr(h, 'weft.handoff.from_agent'), attr(h, 'weft.handoff.to_agent')].join('>'));
    expect(hs.sort()).toEqual(['a>b', 'c>d', 'e>f']);
  });
});

describe('reasoning', () => {
  it('weft.reasoning() on spans and ops (the op start is sent again)', async () => {
    const plan = weft.trace(function plan(goal: string) {
      weft.reasoning('first check the calendar');
      return 'ok';
    });
    const conv = weft.startConversation({ agentName: 'a' });
    const turn = conv.startTurn({ userMessage: 'q' });
    await turn.run(async () => {
      weft.reasoning('The user wants facts; delegate.', { tokens: 12 });
      weft.reasoning('short summary', 'summary');
      weft.reasoning('secret', { kind: 'redacted' });
      weft.reasoning('   ');
    });
    await plan('trip');
    await settle();
    const steps = attr(fake.calls.get(turn.spanId!), 'weft.reasoning');
    expect(steps.map((s: any) => [s.kind, s.text])).toEqual([
      ['thinking', 'The user wants facts; delegate.'],
      ['summary', 'short summary'],
      ['redacted', ''],
    ]);
    expect(steps[0].tokens).toBe(12);
    const [p] = byOpPrefix('plan');
    expect(attr(p, 'weft.reasoning')[0].text).toBe('first check the calendar');
    const modes = fake.requests
      .filter((r) => r.path === '/api/v1/spans/batch')
      .flatMap((r) => r.body.items)
      .filter((it: any) => (it.start ?? it.end).id === p.id)
      .map((it: any) => Object.keys(it)[0]);
    expect(modes).toEqual(['start', 'start', 'end']);
  });

  it('PII redaction applies to reasoning text', async () => {
    await weft.finish();
    await init({ redactPii: true } as weft.ClientSettings);
    const turn = weft.startConversation({ agentName: 'a' }).startTurn({ userMessage: 'q' });
    let llm!: weft.LLM;
    await turn.run(async (t) => {
      weft.reasoning('mail bob@example.com about it');
      llm = t.startLLM({ model: 'm' });
      await llm.run((l) => {
        l.think('call 555-123-4567 first');
        l.output('done');
      });
    });
    await settle();
    expect(attr(fake.calls.get(turn.spanId!), 'weft.reasoning')[0].text).toBe('mail <EMAIL_ADDRESS> about it');
    expect(fake.calls.get(llm.spanId!).output.choices[0].message.content[0]).toEqual({ type: 'thinking', thinking: 'call <PHONE_NUMBER> first' });
  });

  it('captureReasoning off (WEFT_CAPTURE_REASONING=false) drops reasoning everywhere', async () => {
    await weft.finish();
    process.env.WEFT_CAPTURE_REASONING = 'false';
    await init();
    expect(client.captureReasoning).toBe(false);
    const turn = weft.startConversation({ agentName: 'a' }).startTurn({ userMessage: 'q' });
    let llm!: weft.LLM;
    await turn.run(async (t) => {
      weft.reasoning('hidden');
      llm = t.startLLM({ model: 'm' });
      await llm.run((l) => {
        l.think('hidden too');
        l.output('visible');
      });
    });
    const anthropic = weft.wrapAnthropic({ messages: { create: async (_b: any) => ({ model: 'claude-test', content: [{ type: 'thinking', thinking: 'x', signature: 's' }, { type: 'text', text: 'a' }], usage: { input_tokens: 1, output_tokens: 1 } }) } });
    await anthropic.messages.create({ model: 'claude-test', messages: [{ role: 'assistant', content: [{ type: 'thinking', thinking: 'old' }, { type: 'text', text: 'b' }] }] } as any);
    await settle();
    expect(attr(fake.calls.get(turn.spanId!), 'weft.reasoning')).toBeUndefined();
    expect(JSON.stringify(fake.calls.get(llm.spanId!).output)).not.toContain('hidden');
    const [a] = fake.callsByOp('anthropic.messages.create');
    expect(a.output.content).toEqual([{ type: 'text', text: 'a' }]);
    expect(a.inputs.messages[0].content).toEqual([{ type: 'text', text: 'b' }]);
    expect(a.summary.usage['claude-test']).toBeTruthy();
    await weft.finish();
    await init({ captureReasoning: true });
    expect(client.captureReasoning).toBe(true);
  });

  it('scrubReasoning shapes', () => {
    const msg = { role: 'assistant', reasoning_content: 'deep', content: [{ type: 'thinking', thinking: 't', signature: 's' }, { type: 'redacted_thinking', data: 'ENC'.repeat(40) }, { type: 'text', text: 'answer' }] };
    const on: any = weft.scrubReasoning(msg);
    expect(on.content[1].data).toMatch(/^sha256:[0-9a-f]{24}$/);
    expect(weft.scrubReasoning(on)).toEqual(on);
    expect(weft.scrubReasoning(msg, false)).toEqual({ role: 'assistant', content: [{ type: 'text', text: 'answer' }] });
    const resp = { output: [{ type: 'reasoning', summary: [{ type: 'summary_text', text: 's' }], encrypted_content: 'gAAA'.repeat(10) }, { type: 'message', content: [] }], reasoning: { effort: 'high' } };
    expect((weft.scrubReasoning(resp) as any).output[0].encrypted_content).toMatch(/^sha256:/);
    expect(weft.scrubReasoning(resp, false)).toEqual({ output: [resp.output[1]], reasoning: { effort: 'high' } });
    const gemini = { candidates: [{ content: { parts: [{ text: 'plan', thought: true }, { text: 'answer' }] } }] };
    expect(weft.hasReasoning(gemini)).toBe(true);
    expect((weft.scrubReasoning(gemini, false) as any).candidates[0].content.parts).toEqual([{ text: 'answer' }]);
    expect(weft.normReasoningKind('reasoning_summary')).toBe('summary');
  });
});

describe('reasoning in the provider integrations', () => {
  it('Anthropic thinking and redacted thinking, also streamed', async () => {
    const content = [
      { type: 'thinking', thinking: 'The user asks about Oslo; check the weather tool first.', signature: 'sig-1' },
      { type: 'redacted_thinking', data: 'EmwKAhgBEgy3va3pzix'.repeat(8) },
      { type: 'text', text: 'It is sunny.' },
    ];
    const events = [
      { type: 'message_start', message: { id: 'msg_1', type: 'message', role: 'assistant', model: 'claude-test', content: [], usage: { input_tokens: 30, output_tokens: 1 } } },
      { type: 'content_block_start', index: 0, content_block: { type: 'thinking', thinking: '', signature: '' } },
      { type: 'content_block_delta', index: 0, delta: { type: 'thinking_delta', thinking: 'The user asks about Oslo; ' } },
      { type: 'content_block_delta', index: 0, delta: { type: 'thinking_delta', thinking: 'check the weather tool first.' } },
      { type: 'content_block_delta', index: 0, delta: { type: 'signature_delta', signature: 'sig-1' } },
      { type: 'content_block_stop', index: 0 },
      { type: 'content_block_start', index: 1, content_block: content[1] },
      { type: 'content_block_stop', index: 1 },
      { type: 'content_block_start', index: 2, content_block: { type: 'text', text: '' } },
      { type: 'content_block_delta', index: 2, delta: { type: 'text_delta', text: 'It is sunny.' } },
      { type: 'content_block_stop', index: 2 },
      { type: 'message_delta', delta: { stop_reason: 'end_turn' }, usage: { output_tokens: 25 } },
    ];
    async function* stream() {
      for (const e of events) yield e;
    }
    const anthropic = weft.wrapAnthropic({
      messages: {
        create: async (body: any) => (body.stream ? stream() : { id: 'msg_1', type: 'message', role: 'assistant', model: 'claude-test', content, usage: { input_tokens: 30, output_tokens: 20 } }),
      },
    });
    const r: any = await anthropic.messages.create({ model: 'claude-test', max_tokens: 100, messages: [{ role: 'user', content: 'q' }, { role: 'assistant', content }] } as any);
    expect(r.content[1].data).toBe(content[1].data); // the caller's response is untouched
    const s: any = await anthropic.messages.create({ model: 'claude-test', max_tokens: 100, messages: [], stream: true } as any);
    for await (const _ of s) {
      /* drain */
    }
    await settle();
    const calls = fake.callsByOp('anthropic.messages.create');
    expect(calls).toHaveLength(2);
    for (const c of calls) {
      expect(c.output.content[0]).toMatchObject({ type: 'thinking', thinking: content[0].thinking });
      expect(c.output.content[1].type).toBe('redacted_thinking');
      expect(c.output.content[1].data).toMatch(/^sha256:[0-9a-f]{24}$/);
    }
    expect(calls[0].inputs.messages[1].content[1].data).toBe(calls[0].output.content[1].data);
  });

  it('OpenAI chat reasoning_content (streamed) and Responses reasoning items', async () => {
    const chunks = [
      { id: 'c', model: 'deepseek-reasoner', choices: [{ index: 0, delta: { role: 'assistant', content: null, reasoning_content: 'Let me ' } }] },
      { id: 'c', model: 'deepseek-reasoner', choices: [{ index: 0, delta: { reasoning_content: 'add.' } }] },
      { id: 'c', model: 'deepseek-reasoner', choices: [{ index: 0, delta: { content: '42' }, finish_reason: 'stop' }] },
      { id: 'c', model: 'deepseek-reasoner', choices: [], usage: { prompt_tokens: 12, completion_tokens: 30, total_tokens: 42, completion_tokens_details: { reasoning_tokens: 18 } } },
    ];
    async function* s() {
      for (const c of chunks) yield c;
    }
    const oai = weft.wrapOpenAI({
      chat: { completions: { create: async (_b: any) => s() } },
      responses: {
        create: async (_b: any) => ({
          id: 'resp_1', object: 'response', model: 'o4-mini', status: 'completed',
          output: [{ type: 'reasoning', id: 'rs_1', summary: [{ type: 'summary_text', text: '**Planning** the lookup' }], encrypted_content: 'gAAAA'.repeat(10) }, { type: 'message', role: 'assistant', content: [{ type: 'output_text', text: 'Done.' }] }],
          usage: { input_tokens: 100, output_tokens: 80, total_tokens: 180, output_tokens_details: { reasoning_tokens: 64 } },
        }),
      },
    });
    const st: any = await oai.chat.completions.create({ model: 'deepseek-reasoner', messages: [], stream: true } as any);
    for await (const _ of st) {
      /* drain */
    }
    await oai.responses.create({ model: 'o4-mini', input: 'hi', reasoning: { effort: 'low' } } as any);
    await settle();
    const [chat] = fake.callsByOp('openai.chat.completions.create');
    expect(chat.output.choices[0].message).toMatchObject({ reasoning_content: 'Let me add.', content: '42' });
    const [resp] = fake.callsByOp('openai.responses.create');
    expect(resp.output.output[0].summary[0].text).toBe('**Planning** the lookup');
    expect(resp.output.output[0].encrypted_content).toMatch(/^sha256:/);
    expect(resp.summary.usage['o4-mini'].reasoning_tokens).toBe(64);
    expect(resp.inputs.reasoning).toEqual({ effort: 'low' });
  });

  it('Gemini streamed thought parts are kept as one thought part', async () => {
    const chunks = [
      { candidates: [{ index: 0, content: { role: 'model', parts: [{ text: '**Thinking** about ', thought: true }] } }] },
      { candidates: [{ index: 0, content: { role: 'model', parts: [{ text: 'the question', thought: true, thoughtSignature: 'c2ln' }] } }] },
      { candidates: [{ index: 0, content: { role: 'model', parts: [{ text: 'Paris.' }] }, finishReason: 'STOP' }], usageMetadata: { promptTokenCount: 5, candidatesTokenCount: 2, thoughtsTokenCount: 40, totalTokenCount: 47 } },
    ];
    async function* s() {
      for (const c of chunks) yield c;
    }
    const g = weft.wrapGoogleGenAI({ models: { generateContent: async (_b: any) => ({}), generateContentStream: async (_b: any) => s() } });
    const st: any = await g.models.generateContentStream({ model: 'gemini-2.5-flash', contents: 'q' } as any);
    for await (const _ of st) {
      /* drain */
    }
    await settle();
    const [c] = fake.callsByOp('google.genai.models.generate_content_stream');
    expect(c.output.candidates[0].content.parts).toEqual([{ text: '**Thinking** about the question', thought: true, thoughtSignature: 'c2ln' }, { text: 'Paris.' }]);
    expect(c.summary.usage['gemini-2.5-flash'].reasoning_tokens).toBe(40);
  });

  it('Bedrock Converse reasoningContent becomes thinking blocks (also streamed)', async () => {
    const events = [
      { messageStart: { role: 'assistant' } },
      { contentBlockDelta: { contentBlockIndex: 0, delta: { reasoningContent: { text: 'Think ' } } } },
      { contentBlockDelta: { contentBlockIndex: 0, delta: { reasoningContent: { text: 'twice.' } } } },
      { contentBlockDelta: { contentBlockIndex: 0, delta: { reasoningContent: { signature: 's2' } } } },
      { contentBlockDelta: { contentBlockIndex: 1, delta: { text: 'Answer.' } } },
      { messageStop: { stopReason: 'end_turn' } },
      { metadata: { usage: { inputTokens: 3, outputTokens: 4, totalTokens: 7 } } },
    ];
    async function* s() {
      for (const e of events) yield e;
    }
    class ConverseCommand {
      constructor(public input: any) {}
    }
    class ConverseStreamCommand {
      constructor(public input: any) {}
    }
    const br = weft.wrapBedrock({
      async send(cmd: any) {
        if (cmd instanceof ConverseStreamCommand) return { stream: s() };
        return {
          output: { message: { role: 'assistant', content: [{ reasoningContent: { reasoningText: { text: 'Compare both options.', signature: 's1' } } }, { reasoningContent: { redactedContent: new Uint8Array([1, 2, 3]) } }, { text: 'Option A.' }] } },
          stopReason: 'end_turn', usage: { inputTokens: 10, outputTokens: 5, totalTokens: 15 }, $metadata: {},
        };
      },
    });
    const res: any = await br.send(new ConverseCommand({ modelId: 'anthropic.claude', messages: [] }));
    expect(res.output.message.content[0].reasoningContent.reasoningText.text).toBe('Compare both options.');
    const sres: any = await br.send(new ConverseStreamCommand({ modelId: 'anthropic.claude', messages: [] }));
    for await (const _ of sres.stream) {
      /* drain */
    }
    await settle();
    const [c] = fake.callsByOp('bedrock.converse');
    const content = c.output.output.message.content;
    expect(content[0]).toEqual({ type: 'thinking', thinking: 'Compare both options.', signature: 's1' });
    expect(content[1].type).toBe('redacted_thinking');
    expect(content[1].data).toMatch(/^sha256:/);
    const [sc] = fake.callsByOp('bedrock.converse_stream');
    expect(sc.output.output.message.content).toEqual([{ type: 'thinking', thinking: 'Think twice.', signature: 's2' }, { text: 'Answer.' }]);
  });
});

describe('framework integrations: sessions and hand-offs', () => {
  it('OpenAI Agents JS: session, agent names, a hand-off whose target is known at the end, task/turn spans', async () => {
    const p = new weft.WeftTracingProcessor();
    const trace = { traceId: 'trace_1', name: 'Support', groupId: null };
    await p.onTraceStart(trace);
    const span = (id: string, parentId: string | null, spanData: any) => ({ spanId: id, traceId: 'trace_1', parentId, spanData, error: null });
    const task = span('s0', null, { type: 'task', name: 'Support' });
    const triage = span('s1', 's0', { type: 'agent', name: 'triage' });
    const turn = span('s2', 's1', { type: 'turn', turn: 1, agent_name: 'triage' });
    const hand = span('s3', 's2', { type: 'handoff', from_agent: 'triage' });
    const billing = span('s4', 's0', { type: 'agent', name: 'billing' });
    for (const s of [task, triage, turn, hand]) await p.onSpanStart(s);
    hand.spanData.to_agent = 'billing'; // set by the SDK before the span ends
    for (const s of [hand, turn, triage]) await p.onSpanEnd(s);
    await p.onSpanStart(billing);
    await p.onSpanEnd(billing);
    await p.onSpanEnd(task);
    await p.onTraceEnd(trace);
    await settle();
    const [root] = fake.callsByOp('openai_agents.trace');
    expect(attr(root, 'gen_ai.conversation.id')).toBe('trace_1'); // no group id: the trace is the session
    expect(attr(root, 'weft.span_kind')).toBe('workflow');
    const [h] = fake.callsByOp('openai_agents.handoff');
    expect(attr(h, 'weft.handoff')).toEqual({ kind: 'handoff', from_agent: 'triage', to_agent: 'billing' });
    expect(h.display_name).toBe('handoff to billing');
    expect(attr(h, 'weft.span_kind')).toBe('handoff');
    expect(attr(fake.callsByOp('openai_agents.turn')[0], 'weft.span_kind')).toBe('chain');
    expect(fake.callsByOp('agent.billing')).toHaveLength(1);
  });

  it('LangChain.js / LangGraph: thread_id session, node attributes and agent nodes', async () => {
    const h = new weft.WeftCallbackHandler();
    const meta = (node: string, step: number) => ({ langgraph_node: node, langgraph_step: step, thread_id: 'lg-1' });
    h.handleChainStart({ id: ['langgraph', 'CompiledStateGraph'] }, {}, 'g', undefined, [], { thread_id: 'lg-1' }, undefined, 'LangGraph');
    h.handleChainStart({}, {}, 'n1', 'g', [], meta('researcher', 1), undefined, 'researcher');
    h.handleChatModelStart({ id: ['ChatOpenAI'] }, [[]], 'l1', 'n1', {}, [], meta('researcher', 1));
    h.handleLLMEnd({ generations: [[{ text: 'notes' }]] } as any, 'l1');
    h.handleChainEnd({}, 'n1');
    h.handleChainStart({}, {}, 'n2', 'g', [], meta('tools', 2), undefined, 'tools');
    h.handleChainEnd({}, 'n2');
    h.handleChainEnd({}, 'g');
    await settle();
    const [g] = fake.callsByOp('langgraph.graph');
    expect(attr(g, 'weft.span_kind')).toBe('workflow');
    expect(attr(g, 'gen_ai.conversation.id')).toBe('lg-1');
    const [r] = fake.callsByOp('langgraph.node.researcher');
    expect(attr(r, 'langgraph')).toMatchObject({ node: 'researcher', step: 1 });
    expect(attr(r, 'gen_ai.agent.name')).toBe('researcher');
    expect(attr(r, 'gen_ai.operation.name')).toBe('invoke_agent');
    expect(attr(fake.callsByOp('langgraph.node.tools')[0], 'gen_ai.agent.name')).toBeUndefined();
  });

  it('Claude Agent SDK: session id, tool-use ids, sub-agents and thinking', async () => {
    const sid = 'sess-claude-1';
    const msgs = [
      { type: 'system', subtype: 'init', session_id: sid, model: 'claude-test' },
      { type: 'assistant', session_id: sid, message: { id: 'm1', model: 'claude-test', content: [{ type: 'thinking', thinking: 'Delegate the review.', signature: 'x' }, { type: 'tool_use', id: 'toolu_1', name: 'Task', input: { subagent_type: 'reviewer', prompt: 'review' } }] } },
      { type: 'user', session_id: sid, message: { content: [{ type: 'tool_result', tool_use_id: 'toolu_1', content: 'ok' }] } },
      { type: 'result', session_id: sid, subtype: 'success', result: 'Done.', num_turns: 1 },
    ];
    const { query } = weft.wrapClaudeAgentSDK({
      query: (_p: any) =>
        (async function* () {
          for (const m of msgs) yield m;
        })(),
    });
    for await (const _ of query({ prompt: 'Review it' })) {
      /* drain */
    }
    await settle();
    const [root] = fake.callsByOp('claude_agent_sdk.query');
    expect(root.session_id).toBe(sid);
    expect(attr(root, 'gen_ai.conversation.id')).toBe(sid);
    const [task] = fake.callsByOp('claude_agent_sdk.tool.Task');
    expect(attr(task, 'gen_ai.agent.name')).toBe('reviewer');
    expect(attr(task, 'gen_ai.tool.call.id')).toBe('toolu_1');
    const [llm] = fake.callsByOp('claude_agent_sdk.llm');
    expect(llm.output.choices[0].message.content[0]).toMatchObject({ type: 'thinking', thinking: 'Delegate the review.' });
  });
});
