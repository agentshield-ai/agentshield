/**
 * Framework and provider integrations against a real Weft server (fake
 * framework modules, real ingest and agents index):
 *   WEFT_TEST_SERVER_URL=http://127.0.0.1:6486 npx vitest run test/frameworks.integration.test.ts
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

async function* gen<T>(items: T[]): AsyncGenerator<T> {
  for (const i of items) yield i;
}

describe.skipIf(!URL)('real server: framework integrations', () => {
  beforeAll(async () => {
    client = await weft.init(`it/ts-frameworks-${randomUUID().slice(0, 8)}`, {
      serverUrl: URL,
      apiKey: process.env.WEFT_TEST_API_KEY,
      settings: { printCallLink: false },
      autopatchSettings: { bedrock: { opSettings: { postprocessOutput: (o: any) => ({ ...o, scrubbed: true }) } } },
    });
  });
  afterAll(() => weft.finish());

  it('Claude Agent SDK queries become an agent conversation with LLM and tool spans', async () => {
    const sid = `sess-${randomUUID()}`;
    const turn = (prompt: string, n: number) => [
      { type: 'system', subtype: 'init', session_id: sid, model: 'claude-sonnet-4-5' },
      { type: 'assistant', session_id: sid, parent_tool_use_id: null, message: { id: `m${n}a`, model: 'claude-sonnet-4-5', content: [{ type: 'tool_use', id: `tu${n}`, name: 'Bash', input: { command: 'ls' } }], usage: { input_tokens: 10, output_tokens: 4 } } },
      { type: 'user', session_id: sid, parent_tool_use_id: null, message: { role: 'user', content: [{ type: 'tool_result', tool_use_id: `tu${n}`, content: 'a.txt' }] } },
      { type: 'assistant', session_id: sid, parent_tool_use_id: null, message: { id: `m${n}b`, model: 'claude-sonnet-4-5', content: [{ type: 'text', text: `answer to ${prompt}` }], usage: { input_tokens: 20, output_tokens: 6 } } },
      { type: 'result', subtype: 'success', session_id: sid, result: `answer to ${prompt}`, total_cost_usd: 0.001, num_turns: 2, is_error: false },
    ];
    const { query } = weft.wrapClaudeAgentSDK({ query: ({ prompt }: any) => gen(turn(prompt, prompt.length)) });
    for (const p of ['list files', 'and again?']) for await (const _ of query({ prompt: p, options: { resume: sid } })) void _;
    await client.flush();
    const chat = await waitFor(async () => {
      const r = await post('/agents/conversations/chat', { conversation_id: sid });
      return r.total_turns === 2 ? r : null;
    });
    expect(chat.agent_names).toContain('claude-agent-sdk');
    expect(chat.total_input_tokens).toBe(60);
    const msgs = chat.turns[0].messages;
    expect(msgs[0].user_message.text).toBe('list files');
    expect(msgs.find((m: any) => m.type === 'tool_call').tool_call.tool_name).toBe('Bash');
    const agents = (await post('/agents/query', {})).agents as any[];
    const a = agents.find((x) => x.agent_name === 'claude-agent-sdk');
    expect(a.llm_call_count).toBe(4);
    expect(a.tool_call_count).toBe(2);
  });

  it('Vercel AI tools, MCP, Bedrock and Mistral calls are stored with usage and settings applied', async () => {
    const ai = weft.wrapAISDK({
      async generateText(p: any) {
        const mw = weft.weftMiddleware();
        await mw.wrapGenerate({ doGenerate: async () => ({ text: '', usage: { inputTokens: 5, outputTokens: 1 } }), params: {}, model: { modelId: 'gpt-4o-mini', provider: 'openai.chat' } });
        return { text: String((await p.tools.add.execute({ a: 1, b: 2 }, { toolCallId: 'x1' })).sum) };
      },
    });
    await ai.generateText({ prompt: 'add', tools: { add: { execute: async ({ a, b }: any) => ({ sum: a + b }) } } });
    const mcp = weft.wrapMCPClient({ callTool: async (p: any) => ({ content: [{ type: 'text', text: `ok ${p.name}` }] }) });
    await mcp.callTool({ name: 'search', arguments: { q: 'x' } });
    class ConverseCommand {
      constructor(public input: any) {}
    }
    const br = weft.wrapBedrock({ send: async (_cmd: any) => ({ output: { message: { role: 'assistant', content: [{ text: 'hi' }] } }, usage: { inputTokens: 3, outputTokens: 2, totalTokens: 5 } }) });
    await br.send(new ConverseCommand({ modelId: 'amazon.nova-lite-v1:0', messages: [] }));
    const mistral = weft.wrapMistral({ chat: { complete: async (r: any) => ({ model: r.model, choices: [], usage: { promptTokens: 4, completionTokens: 1 } }) } });
    await mistral.chat.complete({ model: 'mistral-small-latest', messages: [] });
    await client.flush();

    const byOp = async (op: string) => client.spans({ filter: { names: [op] } });
    const [gt] = await byOp('vercel_ai.generateText');
    const [tool] = await byOp('vercel_ai.tool.add');
    expect(tool.parentId).toBe(gt.id);
    expect(tool.output).toEqual({ sum: 3 });
    const [llm] = await byOp('vercel_ai.generate');
    expect(llm.parentId).toBe(gt.id);
    const [root] = await client.spans({ filter: { ids: [gt.id] } });
    expect((root.summary as any).usage['gpt-4o-mini'].prompt_tokens).toBe(5);
    const [m] = await byOp('mcp.client.call_tool');
    expect(m.displayName).toBe('MCP tool: search');
    const [b] = await byOp('bedrock.converse');
    expect((b.output as any).scrubbed).toBe(true);
    expect((b.summary as any).usage['amazon.nova-lite-v1:0'].total_tokens).toBe(5);
    const [mi] = await byOp('mistral.chat.complete');
    expect((mi.summary as any).usage['mistral-small-latest'].prompt_tokens).toBe(4);

    const spans = await post('/agents/spans/query', { query: { $expr: { $eq: [{ $getField: 'operation_name' }, { $literal: 'execute_tool' }] } }, limit: 50 });
    const names = (spans.spans as any[]).map((s) => s.tool_name);
    expect(names).toEqual(expect.arrayContaining(['add', 'search']));
  });
});
