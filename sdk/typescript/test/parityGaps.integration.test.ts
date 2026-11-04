/**
 * Bedrock Agents, OpenAI Realtime, NER redaction, registry linking, call
 * records and run linkage against a real server:
 *   WEFT_TEST_SERVER_URL=http://127.0.0.1:6345 npx vitest run test/parityGaps.integration.test.ts
 */
import { randomUUID } from 'node:crypto';
import { createServer } from 'node:http';
import type { AddressInfo } from 'node:net';
import { afterAll, beforeAll, describe, expect, it } from 'vitest';
import * as weft from '../src/index.js';
import { FakeRealtimeWS, InvokeAgentCommand, MODEL, agentEvents, agentRuntime, realtimeEvents } from './parityFixtures.js';

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

describe.skipIf(!URL)('real server: parity gaps', () => {
  beforeAll(async () => {
    client = await weft.init(`it/ts-gaps-${randomUUID().slice(0, 8)}`, { serverUrl: URL, apiKey: process.env.WEFT_TEST_API_KEY, settings: { printSpanLink: false } });
  });
  afterAll(() => weft.finish());

  it('a Bedrock agent session becomes a conversation with LLM, tool and sub-agent spans', async () => {
    const sid = `bedrock-${randomUUID().slice(0, 8)}`;
    const Cls = agentRuntime(agentEvents(sid));
    const br = weft.wrapBedrockAgent(new Cls());
    const res: any = await br.send(new InvokeAgentCommand({ agentId: 'AGENT1', agentAliasId: 'ALIAS1', sessionId: sid, inputText: 'Weather in Oslo?', enableTrace: true }));
    for await (const _ of res.completion) {
      /* drain */
    }
    await client.flush();
    const [root] = await client.spans({ filter: { names: ['bedrock_agent.invoke_agent'] } });
    expect(root.sessionId).toBe(sid);
    const kids = await root.children();
    expect(new Set(kids.map((k) => k.baseName))).toEqual(
      new Set(['bedrock_agent.model_invocation', 'bedrock_agent.action_group', 'bedrock_agent.knowledge_base', 'bedrock_agent.collaborator', 'bedrock_agent.guardrail']),
    );
    const full = await client.span(root.id);
    expect(full!.summary.usage[MODEL].prompt_tokens).toBe(900);
    const chat = await waitFor(async () => {
      const r = await post('/agents/conversations/chat', { conversation_id: sid });
      return r.total_turns === 1 ? r : null;
    });
    expect(chat.agent_names).toContain('AGENT1');
    const msgs = chat.turns[0].messages;
    expect(msgs[0].user_message.text).toBe('Weather in Oslo?');
    expect(msgs.filter((m: any) => m.type === 'tool_call').map((m: any) => m.tool_call.tool_name)).toContain('weather.get_weather');
    const agents = (await post('/agents/query', {})).agents.map((a: any) => a.agent_name);
    expect(agents).toEqual(expect.arrayContaining(['AGENT1', 'researcher']));
  });

  it('records an OpenAI Realtime session', async () => {
    const rt = weft.wrapOpenAIRealtime(new FakeRealtimeWS());
    const evs = realtimeEvents();
    rt.send({ type: 'conversation.item.create', item: { type: 'message', role: 'user', content: [{ type: 'input_text', text: 'Weather in Paris?' }] } });
    for (const e of evs.slice(0, 7)) rt.receive(e);
    rt.send({ type: 'conversation.item.create', item: { type: 'function_call_output', call_id: 'call_1', output: '{"temp": 21}' } });
    rt.close();
    await client.flush();
    const [sess] = await client.spans({ filter: { names: ['openai.realtime.session'] } });
    const kids = await sess.children();
    const resp = kids.find((k) => k.baseName === 'openai.realtime.response')!;
    expect(resp.output).toMatchObject({ text: 'It is sunny.', status: 'completed' });
    expect(kids.find((k) => k.baseName === 'openai.realtime.tool.get_weather')!.output).toEqual({ temp: 21 });
    expect((await client.span(sess.id))!.summary.usage['gpt-realtime'].prompt_tokens).toBe(100);
  });

  it('NER-redacts through a Presidio-compatible analyzer before sending', async () => {
    const srv = createServer(async (req, res) => {
      const chunks: Buffer[] = [];
      for await (const ch of req) chunks.push(ch as Buffer);
      const { text } = JSON.parse(Buffer.concat(chunks).toString());
      const i = text.indexOf('Grace Hopper');
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify(i >= 0 ? [{ entity_type: 'PERSON', start: i, end: i + 12, score: 0.9 }] : []));
    });
    await new Promise<void>((r) => srv.listen(0, '127.0.0.1', () => r()));
    try {
      const c = new weft.WeftClient(client.projectId, {
        serverUrl: URL,
        apiKey: process.env.WEFT_TEST_API_KEY,
        settings: { printSpanLink: false, redactPii: true, redactPiiEngine: 'presidio', redactPiiPresidioUrl: `http://127.0.0.1:${(srv.address() as AddressInfo).port}` },
      });
      const call = c.createSpan('ner.echo', { text: 'Grace Hopper, grace@navy.mil' });
      c.finishSpan(call, 'Thanks Grace Hopper');
      await c.flush();
      const got = await client.span(call.id);
      expect(got!.inputs.text).toBe('<PERSON>, <EMAIL_ADDRESS>');
      expect(got!.output).toBe('Thanks <PERSON>');
    } finally {
      await new Promise((r) => srv.close(r));
    }
  });

  it('links a prompt and a dataset to a registry and reads them back', async () => {
    const reg = `registry-${randomUUID().slice(0, 6)}`;
    const p = new weft.StringPrompt({ name: 'ts-persona', content: 'You are {persona}.' });
    await weft.save(p);
    expect((await weft.linkPromptToRegistry(p, { targetPath: `${reg}/personas`, aliases: ['staging'] })).versionIndex).toBe(0);
    await weft.linkToRegistry(new weft.Dataset({ name: 'ts-evalset', rows: [{ q: 'hi' }] }), { targetPath: `${reg}/datasets` });
    const cols = await client.queryRegistry(reg);
    expect(cols.map((c) => c.name).sort()).toEqual(['datasets', 'personas']);
    const v = cols.find((c) => c.name === 'personas')!.versions[0];
    expect(v.aliases).toContain('staging');
    expect(v.source_project_id).toBe(client.projectId);
    expect((await client.getFromRegistry(`${reg}/personas`, 'staging')).format({ persona: 'a pirate' })).toBe('You are a pirate.');
  });

  it('returns call records with run linkage', async () => {
    const stepFn = weft.trace(function stepFn(x: number) {
      return x * 2;
    });
    weft.setRun('run-live', 5);
    await stepFn(1);
    await stepFn(2);
    weft.setRun(null);
    const recs = await client.spanRecords({ filter: { names: ['stepFn'] }, sortBy: [{ field: 'started_at', direction: 'asc' }] });
    expect(recs.map((r) => r.output)).toEqual([2, 4]);
    expect(recs.every((r) => r.run_id === `${client.projectId}/run-live` && r.status === 'success')).toBe(true);
  });
});
