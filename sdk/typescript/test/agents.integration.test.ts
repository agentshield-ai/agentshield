/**
 * Agent tracing, SavedView, evaluation links and media references against a real server:
 *   WEFT_TEST_SERVER_URL=http://127.0.0.1:6348 npx vitest run test/agents.integration.test.ts
 */
import { randomUUID } from 'node:crypto';
import { afterAll, beforeAll, describe, expect, it } from 'vitest';
import * as weft from '../src/index.js';

const URL = process.env.WEFT_TEST_SERVER_URL;
let client: weft.WeftClient;

const post = (path: string, body: Record<string, unknown>) => client.http.post(path, { project_id: client.projectId, ...body });

async function waitFor<T>(fn: () => Promise<T | null | undefined | false>, ms = 10_000): Promise<T> {
  const end = Date.now() + ms;
  for (;;) {
    const v = await fn();
    if (v || Date.now() > end) return v as T;
    await new Promise((r) => setTimeout(r, 200));
  }
}

describe.skipIf(!URL)('real server: agents and SDK extras', () => {
  beforeAll(async () => {
    client = await weft.init(`it/ts-agents-${randomUUID().slice(0, 8)}`, { serverUrl: URL, apiKey: process.env.WEFT_TEST_API_KEY, settings: { printSpanLink: false } });
  });
  afterAll(() => weft.finish());

  it('conversations show in /agents/query and /agents/conversations/chat', async () => {
    const lookup = weft.trace(function lookup(id: number) {
      return { id, status: 'shipped' };
    });
    const conv = weft.startConversation({ agentName: 'ts-support', conversationName: 'TS order' });
    await conv.run(async (c) => {
      await c.startTurn({ userMessage: 'Where is order 7?' }).run(async (turn) => {
        await turn.startLLM({ model: 'gpt-4o-mini', providerName: 'openai' }).run((llm) => {
          llm.inputMessages = [weft.Message.user('Where is order 7?')];
          llm.outputMessages = [weft.Message.assistant('', { toolCalls: [{ id: 'c1', name: 'lookup', arguments: { id: 7 } }] })];
          llm.usage = { inputTokens: 20, outputTokens: 6 };
        });
        await turn.startTool({ name: 'lookup', arguments: { id: 7 }, toolCallId: 'c1' }).run(async (t) => {
          t.result = await lookup(7);
        });
        turn.outputMessages = [weft.Message.assistant('Order 7 has shipped.')];
      });
      await c.startTurn({ userMessage: 'Thanks' }).run((t) => {
        t.outputMessages = [weft.Message.assistant('Welcome!')];
      });
    });
    await client.flush();
    const agents = await waitFor(async () => {
      const a = (await post('/agents/query', {})).agents as any[];
      return a.find((x) => x.agent_name === 'ts-support');
    });
    expect(agents.invocation_count).toBe(2);
    expect(agents.llm_call_count).toBe(1);
    expect(agents.tool_call_count).toBe(1);
    expect(agents.total_input_tokens).toBe(20);
    const chat = await post('/agents/conversations/chat', { conversation_id: conv.conversationId });
    expect(chat.total_turns).toBe(2);
    const msgs = chat.turns[0].messages;
    expect(msgs[0].user_message.text).toBe('Where is order 7?');
    const tc = msgs.find((m: any) => m.type === 'tool_call').tool_call;
    expect(tc.tool_name).toBe('lookup');
    expect(JSON.parse(tc.tool_arguments)).toEqual({ id: 7 });
    expect(msgs.some((m: any) => m.assistant_message?.text === 'Order 7 has shipped.')).toBe(true);

    const res = weft.logConversation({
      agentName: 'ts-replayer',
      turns: [0, 1].map((i) => new weft.Turn({ messages: [weft.Message.user(`q${i}`)], outputMessages: [weft.Message.assistant(`a${i}`)] })),
    });
    await client.flush();
    const replay = await waitFor(async () => {
      const r = await post('/agents/conversations/chat', { conversation_id: res.conversationId });
      return r.total_turns === 2 ? r : null;
    });
    expect(replay.turns.map((t: any) => t.messages[0].user_message.text)).toEqual(['q0', 'q1']);
  });

  it('applies saved views, links eval predictions and resolves media URLs', async () => {
    const answer = weft.trace(function answer(q: string) {
      return q.toUpperCase();
    });
    for (const q of ['a', 'bb', 'ccc']) await answer(q);
    await client.flush();
    const view = await new weft.SavedView({ label: 'TS long answers' }).filterOp('answer').addFilter('output', 'contains', 'B').sortBy('started_at', 'asc').save();
    expect((await view.spans()).map((c) => c.inputs.q)).toEqual(['bb']);
    const loaded = await weft.SavedView.load(view.name!);
    expect((await loaded.spans()).map((c) => c.inputs.q)).toEqual(['bb']);
    expect((await client.getSavedViews('traces')).map((v) => v.name)).toEqual([view.name]);

    const turn = weft.startTurn({ userMessage: '2+2', agentName: 'calc' });
    turn.outputMessages = [weft.Message.assistant('4')];
    turn.end();
    const ev = new weft.EvaluationLogger({ model: 'calc', name: 'ts calc eval' });
    const p = ev.logPrediction({ q: '2+2' }, '4', { span: turn });
    p.logScore('correct', true);
    await ev.logSummary();
    await client.flush();
    const [pas] = await client.spans({ filter: { names: ['Evaluation.predict_and_score'] } });
    expect((pas.attributes as any).weft.linked_spans).toEqual([{ span_id: turn.spanId, trace_id: turn.traceId }]);

    const local = new weft.Content('ts stored', { mimetype: 'text/plain' });
    const look = weft.trace(function look(img: unknown, l: unknown) {
      return 'ok';
    });
    await look(weft.Image.fromUri('s3://customer-bucket/a.png'), local);
    await client.flush();
    const [c] = await client.spans({ filter: { names: ['look'] } });
    expect((c.inputs as any).img).toEqual({ _type: 'Image', uri: 's3://customer-bucket/a.png', mime_type: 'image/png', filename: 'a.png' });
    const url = (await client.resolveUrl(local)) as string;
    expect(url).toContain(local.digest);
    expect(await (await fetch(url)).text()).toBe('ts stored');
    expect(await weft.Content.fromUri(`weft-file://${local.digest}`).load().then((b) => Buffer.from(b).toString())).toBe('ts stored');
    await expect(client.resolveUrl('s3://customer-bucket/a.png')).rejects.toThrow(/400/);
  });
});
