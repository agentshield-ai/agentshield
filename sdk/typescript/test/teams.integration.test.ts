/**
 * A two-agent team session with a hand-off and reasoning, against a real server,
 * checked through the native API (/api/v1/sessions/{id}, /graph, /reasoning, /api/v1/teams):
 *   WEFT_TEST_SERVER_URL=http://127.0.0.1:6620 npx vitest run test/teams.integration.test.ts
 */
import { randomUUID } from 'node:crypto';
import { afterAll, beforeAll, describe, expect, it } from 'vitest';
import * as weft from '../src/index.js';

const URL = process.env.WEFT_TEST_SERVER_URL;
let client: weft.WeftClient;

async function api(path: string, params: Record<string, string> = {}): Promise<any> {
  const q = new URLSearchParams({ project_id: client.projectId, ...params });
  const headers: Record<string, string> = {};
  if (process.env.WEFT_TEST_API_KEY) headers.Authorization = `Bearer ${process.env.WEFT_TEST_API_KEY}`;
  const r = await fetch(`${URL}${path}?${q}`, { headers });
  return r.ok ? r.json() : null;
}

async function waitFor<T>(fn: () => Promise<T | null | undefined | false>, ms = 10_000): Promise<T> {
  const end = Date.now() + ms;
  for (;;) {
    const v = await fn();
    if (v || Date.now() > end) return v as T;
    await new Promise((r) => setTimeout(r, 200));
  }
}

async function sessionApi(sid: string, handoffs = 1) {
  const p = `/api/v1/sessions/${encodeURIComponent(sid)}`;
  const s = await waitFor(async () => {
    const x = await api(p);
    return x && x.handoff_count >= handoffs ? x : null;
  });
  expect(s, `session ${sid}`).toBeTruthy();
  return { s, graph: await api(`${p}/graph`), reasoning: await api(`${p}/reasoning`), timeline: await api(`${p}/timeline`) };
}

const edges = (g: any) => new Set<string>(g.edges.map((e: any) => `${e.from}>${e.to}:${e.kind}`));

describe.skipIf(!URL)('real server: agent teams, hand-offs and reasoning', () => {
  beforeAll(async () => {
    client = await weft.init(`it/ts-teams-${randomUUID().slice(0, 8)}`, { serverUrl: URL, apiKey: process.env.WEFT_TEST_API_KEY, settings: { printCallLink: false } });
  });
  afterAll(() => weft.finish());

  it('a conversation team with a delegation, a hand-off and reasoning steps', async () => {
    const sid = `trip-${randomUUID().slice(0, 8)}`;
    const teamId = `trip-planners-${randomUUID().slice(0, 6)}`;
    const conv = weft.startConversation({ agentName: 'orchestrator', conversationId: sid, team: { id: teamId, name: 'Trip planners' } });
    let toolId = '';
    let searchId = '';
    await conv.run(async (c) => {
      await c.startTurn({ userMessage: 'Find three facts about Oslo and summarise them' }).run(async (turn) => {
        await turn.startLLM({ model: 'claude-sonnet-4', providerName: 'anthropic' }).run((llm) => {
          llm.inputMessages = [weft.Message.user('Find three facts about Oslo and summarise them')];
          llm.think('The user wants facts; the researcher agent should gather them.');
          llm.outputMessages = [weft.Message.assistant('', { toolCalls: [{ id: 'toolu_1', name: 'delegate_research', arguments: { task: 'Find three facts about Oslo' } }] })];
          llm.usage = { inputTokens: 200, outputTokens: 120 };
        });
        const tool = turn.startTool({ name: 'delegate_research', arguments: { task: 'Find three facts about Oslo' }, toolCallId: 'toolu_1' });
        await tool.run(async (t) => {
          toolId = t.callId!;
          await weft.startHandoff('researcher', { input: 'Find three facts about Oslo', kind: 'delegate' }).run(async (h) => {
            await c.startTurn({ agentName: 'researcher', userMessage: 'Find three facts about Oslo' }).run(async (rt) => {
              weft.reasoning('Search the web first, then pick three facts.');
              await rt.startTool({ name: 'web_search', arguments: { q: 'Oslo facts' }, toolCallId: 'call_9' }).run((ws) => {
                searchId = ws.callId!;
                ws.result = ['Capital of Norway', 'On the Oslofjord', 'Nobel Peace Prize'];
              });
              rt.outputMessages = [weft.Message.assistant('1. Capital of Norway 2. On the Oslofjord 3. Nobel Peace Prize')];
            });
            h.output = '1. Capital of Norway 2. On the Oslofjord 3. Nobel Peace Prize';
          });
          t.result = 'three facts';
        });
        weft.reasoning('Facts are in; the writer agent should summarise.', 'summary');
        turn.handoff('writer', { input: 'Summarise the three facts' });
        await turn.startSubagent({ name: 'writer', model: 'gpt-4o' }).run(async (w) => {
          await w.startLLM({ model: 'gpt-4o', providerName: 'openai' }).run((l) => {
            l.output("Oslo is Norway's capital on the Oslofjord, home of the Nobel Peace Prize.");
          });
        });
        turn.outputMessages = [weft.Message.assistant("Oslo is Norway's capital on the Oslofjord, home of the Nobel Peace Prize.")];
      });
    });
    await client.flush();
    const { s, graph, reasoning, timeline } = await sessionApi(sid, 2);
    expect(s.team_id).toBe(teamId);
    expect(s.team_name).toBe('Trip planners');
    expect(s.agents.slice(0, 3)).toEqual(['orchestrator', 'researcher', 'writer']);
    expect(s.handoff_count).toBe(2);
    expect(s.reasoning_step_count).toBe(3);
    const e = edges(graph);
    for (const x of ['orchestrator>researcher:delegate', 'orchestrator>writer:handoff', 'researcher>orchestrator:return']) expect(e.has(x), x).toBe(true);
    const steps = new Map<string, any>(reasoning.items.map((st: any) => [st.text, st]));
    const think = steps.get('The user wants facts; the researcher agent should gather them.');
    expect(think).toMatchObject({ agent_name: 'orchestrator', kind: 'thinking', source: 'output', next_span_id: toolId });
    expect(steps.get('Search the web first, then pick three facts.')).toMatchObject({ agent_name: 'researcher', next_span_id: searchId });
    const summary = steps.get('Facts are in; the writer agent should summarise.');
    expect(summary.kind).toBe('summary');
    const handoffSpans = new Set(timeline.items.filter((i: any) => i.type === 'handoff').map((i: any) => i.span_id));
    expect(handoffSpans.has(summary.next_span_id)).toBe(true);
    const teams = await waitFor(async () => ((await api('/api/v1/teams'))?.items ?? []).filter((t: any) => t.team_id === teamId));
    expect(teams[0]).toMatchObject({ name: 'Trip planners', session_count: 1 });
    expect(teams[0].agents).toEqual(expect.arrayContaining(['orchestrator', 'researcher', 'writer']));
  });

  it('OpenAI Agents JS spans: the group id is the session, a hand-off and an agent-as-tool delegation', async () => {
    const group = `thread-${randomUUID().slice(0, 8)}`;
    const p = new weft.WeftTracingProcessor();
    const trace = { traceId: `trace_${randomUUID().replace(/-/g, '')}`, name: 'Support', groupId: group };
    const t0 = Date.now() - 5000;
    const at = (ms: number) => new Date(t0 + ms).toISOString();
    const span = (id: string, parentId: string | null, spanData: any, a: number, b: number) => ({ spanId: id, traceId: trace.traceId, parentId, spanData, startedAt: at(a), endedAt: at(b), error: null });
    const triage = span('s1', null, { type: 'agent', name: 'triage' }, 0, 3000);
    const resp1 = span('s2', 's1', { type: 'response', _response: { id: 'r1', model: 'o4-mini', output: [{ type: 'reasoning', summary: [{ type: 'summary_text', text: 'Ask the researcher first.' }] }, { type: 'function_call', call_id: 'c1', name: 'ask_researcher', arguments: '{}' }], usage: { input_tokens: 10, output_tokens: 20, output_tokens_details: { reasoning_tokens: 16 } } } }, 100, 600);
    const tool = span('s3', 's1', { type: 'function', name: 'ask_researcher', input: '{}', output: 'facts' }, 700, 1500);
    const researcher = span('s4', 's3', { type: 'agent', name: 'researcher' }, 800, 1400);
    const hand = span('s5', 's1', { type: 'handoff', from_agent: 'triage' }, 1600, 1700);
    const billing = span('s6', null, { type: 'agent', name: 'billing' }, 1800, 2900);
    await p.onTraceStart(trace);
    for (const s of [triage, resp1]) await p.onSpanStart(s);
    await p.onSpanEnd(resp1);
    for (const s of [tool, researcher]) await p.onSpanStart(s);
    for (const s of [researcher, tool]) await p.onSpanEnd(s);
    await p.onSpanStart(hand);
    hand.spanData.to_agent = 'billing';
    await p.onSpanEnd(hand);
    await p.onSpanEnd(triage);
    await p.onSpanStart(billing);
    await p.onSpanEnd(billing);
    await p.onTraceEnd(trace);
    await client.flush();
    const { s, graph, reasoning } = await sessionApi(group, 2);
    expect(s.agents).toEqual(expect.arrayContaining(['triage', 'researcher', 'billing']));
    const e = edges(graph);
    expect(e.has('triage>researcher:delegate')).toBe(true);
    expect(e.has('triage>billing:handoff')).toBe(true);
    expect(reasoning.items.map((x: any) => [x.kind, x.text, x.agent_name, x.tokens])).toEqual([['summary', 'Ask the researcher first.', 'triage', 16]]);
  });
});
