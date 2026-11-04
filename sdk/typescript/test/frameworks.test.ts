/**
 * Framework and provider integrations with fake modules/clients (no network
 * besides the in-process fake Weft server).
 */
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import * as weft from '../src/index.js';
import { FakeWeft } from './fakeServer.js';

let fake: FakeWeft;
let client: weft.WeftClient;

beforeEach(async () => {
  fake = await new FakeWeft().start();
  client = await weft.init('ts-entity/frameworks', { serverUrl: fake.url, sender: { flushIntervalMs: 5 }, settings: { printSpanLink: false }, autopatchSettings: {} });
});

afterEach(async () => {
  await weft.finish();
  weft.configureIntegrations();
  delete process.env.WEFT_DISABLED_INTEGRATIONS;
  delete process.env.WEFT_AUTOPATCH_MISTRAL;
  await fake.close();
});

async function settle(): Promise<void> {
  await new Promise((r) => setTimeout(r, 10));
  await client.flush();
}

const attr = (c: any, k: string) => c.attributes?.[k];

async function* gen<T>(items: T[]): AsyncGenerator<T> {
  for (const i of items) yield i;
}

class FakeStream<T> {
  constructor(private items: T[]) {}
  async *[Symbol.asyncIterator]() {
    for (const i of this.items) yield i;
  }
}

describe('integration settings', () => {
  it('applies postprocess hooks, op name, attributes and enable flags', async () => {
    weft.configureIntegrations({
      groq: { traceSettings: { postprocessInputs: (i) => ({ ...i, note: 'x' }), postprocessOutput: (o: any) => ({ redacted: true, model: o.model }), attributes: { team: 'ml' } } },
      together: false,
    });
    const make = () => ({ chat: { completions: { create: async (b: any) => ({ model: b.model, choices: [], usage: { prompt_tokens: 1, completion_tokens: 2 } }) } } });
    const groq = weft.wrapGroq(make());
    const together = weft.wrapTogether(make());
    const cerebras = weft.wrapCerebras(make(), { spanName: 'cb.chat', postprocessOutput: () => 'local' });
    await groq.chat.completions.create({ model: 'llama-3.3-70b', messages: [], note: 'k' } as any);
    await together.chat.completions.create({ model: 'mixtral', messages: [] } as any);
    await cerebras.chat.completions.create({ model: 'llama3.1-8b', messages: [] } as any);
    await settle();
    const [g] = fake.callsByOp('groq.chat.completions.create');
    expect(g.inputs.note).toBe('x');
    expect(g.output).toEqual({ redacted: true, model: 'llama-3.3-70b' });
    expect(g.attributes.team).toBe('ml');
    expect(g.attributes.weft.integration).toBe('groq');
    expect(attr(g, 'gen_ai.provider.name')).toBe('groq');
    expect(g.summary.usage['llama-3.3-70b'].total_tokens).toBe(3);
    expect(fake.callsByOp('together.chat.completions.create')).toHaveLength(0);
    const [cb] = fake.callsByOp('cb.chat');
    expect(cb.output).toBe('local');

    process.env.WEFT_DISABLED_INTEGRATIONS = 'groq-sdk';
    await groq.chat.completions.create({ model: 'llama-3.3-70b', messages: [] } as any);
    await settle();
    expect(fake.callsByOp('groq.chat.completions.create')).toHaveLength(1);
    expect(weft.integrationEnabled('groq')).toBe(false);
    expect(() => weft.configureIntegrations({ x: { nope: 1 } as any })).toThrow(/unknown integration setting/);
  });
});

describe('LangChain.js callback handler', () => {
  it('records chains, chat models, tools and retrievers with nesting and usage', async () => {
    const h = new weft.WeftCallbackHandler({ postprocessInputs: (i) => ({ ...i, pp: true }) });
    const parent = weft.trace(async function app() {
      h.handleChainStart({ id: ['langchain', 'RunnableSequence'] }, { question: 'q' }, 'r1', undefined, ['t1'], { user: 'u' });
      h.handleRetrieverStart({ id: ['x', 'VectorStoreRetriever'] }, 'q', 'r2', 'r1');
      h.handleRetrieverEnd([{ pageContent: 'doc', metadata: { s: 1 } }], 'r2');
      h.handleChatModelStart({ id: ['x', 'ChatOpenAI'] }, [[{ _getType: () => 'human', content: 'hi' }]], 'r3', 'r1', { invocation_params: { model: 'gpt-4o-mini' } }, [], { ls_provider: 'openai' });
      h.handleLLMEnd({ generations: [[{ text: 'yo', message: { _getType: () => 'ai', content: 'yo', usage_metadata: { input_tokens: 7, output_tokens: 3, total_tokens: 10 } } }]], llmOutput: {} }, 'r3');
      h.handleToolStart({ name: 'search' }, '{"q":"x"}', 'r4', 'r1', [], {}, undefined, 'call_1');
      h.handleToolError(new Error('boom'), 'r4');
      h.handleChainEnd({ answer: 'a' }, 'r1');
      return 'done';
    });
    await parent();
    await settle();
    const [root] = fake.callsByOp('app');
    const [chain] = fake.callsByOp('langchain.Chain.RunnableSequence');
    expect(chain.parent_id).toBe(root.id);
    expect(chain.inputs).toEqual({ question: 'q', pp: true });
    expect(chain.output).toEqual({ answer: 'a' });
    expect(chain.attributes.langchain).toEqual({ tags: ['t1'], metadata: { user: 'u' } });
    const [ret] = fake.callsByOp('langchain.Retriever.VectorStoreRetriever');
    expect(ret.parent_id).toBe(chain.id);
    expect(ret.output.documents[0].page_content).toBe('doc');
    const [llm] = fake.callsByOp('langchain.ChatModel.ChatOpenAI');
    expect(llm.parent_id).toBe(chain.id);
    expect(llm.inputs.messages).toEqual([{ role: 'user', content: 'hi' }]);
    expect(llm.summary.usage['gpt-4o-mini']).toEqual({ requests: 1, prompt_tokens: 7, completion_tokens: 3, total_tokens: 10 });
    expect(attr(llm, 'gen_ai.provider.name')).toBe('openai');
    expect(llm.attributes.weft.span_kind).toBe('llm');
    const [tool] = fake.callsByOp('langchain.Tool.search');
    expect(tool.inputs).toEqual({ q: 'x', pp: true });
    expect(attr(tool, 'gen_ai.tool.call.id')).toBe('call_1');
    expect(tool.error).toContain('boom');
  });
});

describe('LlamaIndex.TS', () => {
  it('maps query, retrieve, llm and tool events', async () => {
    const handlers = new Map<string, Array<(e: any) => void>>();
    const cm = {
      on(n: string, h: any) {
        handlers.set(n, [...(handlers.get(n) ?? []), h]);
        return this;
      },
      off(n: string, h: any) {
        handlers.set(n, (handlers.get(n) ?? []).filter((x) => x !== h));
        return this;
      },
      emit(n: string, detail: any) {
        for (const h of handlers.get(n) ?? []) h({ detail });
      },
    };
    const settings = { callbackManager: cm, llm: { metadata: { model: 'gpt-4o' } } };
    const inst = weft.instrumentLlamaIndex(settings);
    expect(weft.instrumentLlamaIndex(settings)).toBe(inst);
    cm.emit('query-start', { id: 'q1', query: { query: 'What?' } });
    cm.emit('retrieve-start', { id: 'rt', query: { query: 'What?' } });
    cm.emit('retrieve-end', { id: 'rt', query: {}, nodes: [{ node: { id_: 'n1', text: 'ctx', metadata: {} }, score: 0.9 }] });
    cm.emit('llm-start', { id: 'l1', messages: [{ role: 'user', content: 'What?' }] });
    cm.emit('llm-tool-call', { toolCall: { id: 'tc', name: 'calc', input: { a: 1 } } });
    cm.emit('llm-tool-result', { toolCall: { id: 'tc', name: 'calc' }, toolResult: { output: 2, isError: false } });
    cm.emit('llm-end', { id: 'l1', response: { message: { role: 'assistant', content: 'It.' }, raw: { model: 'gpt-4o', usage: { prompt_tokens: 4, completion_tokens: 2 } } } });
    cm.emit('query-end', { id: 'q1', response: { message: { content: 'It.' }, sourceNodes: [] } });
    inst.uninstrument();
    expect(handlers.get('llm-start')).toHaveLength(0);
    await settle();
    const [q] = fake.callsByOp('llamaindex.query');
    const [r] = fake.callsByOp('llamaindex.retrieve');
    const [l] = fake.callsByOp('llamaindex.llm.chat');
    const [t] = fake.callsByOp('llamaindex.tool.calc');
    expect(r.parent_id).toBe(q.id);
    expect(l.parent_id).toBe(q.id);
    expect(t.parent_id).toBe(q.id);
    expect(r.output.nodes[0]).toMatchObject({ id: 'n1', text: 'ctx', score: 0.9 });
    expect(l.summary.usage['gpt-4o']).toMatchObject({ prompt_tokens: 4, completion_tokens: 2 });
    expect(attr(t, 'gen_ai.tool.name')).toBe('calc');
    expect(t.output).toBe(2);
    expect(q.output.response).toBe('It.');
  });
});

describe('Vercel AI SDK', () => {
  it('wrapAITools and wrapAISDK trace tools, the run and middleware model calls', async () => {
    const lookup = weft.trace(async function lookup(id: number) {
      return { id, status: 'shipped' };
    });
    const tools = { order: { description: 'd', execute: async (args: any) => lookup(args.id) } };
    const mw = weft.weftMiddleware();
    const model = { modelId: 'gpt-4o-mini', provider: 'openai.chat' };
    const fakeAI = {
      async generateText(p: any) {
        const r = await mw.wrapGenerate({ doGenerate: async () => ({ text: '', toolCalls: [{ toolName: 'order' }], usage: { inputTokens: 5, outputTokens: 1 } }), params: { prompt: p.prompt }, model });
        const out = await p.tools.order.execute({ id: 7 }, { toolCallId: 'tc1' });
        return { text: `status ${out.status}`, finishReason: 'stop', steps: [r, {}] };
      },
      streamText(p: any) {
        (async () => {
          const res = await mw.wrapStream({
            doStream: async () => ({
              stream: new ReadableStream({
                start(c) {
                  c.enqueue({ type: 'reasoning-delta', delta: 'think' });
                  c.enqueue({ type: 'text-delta', delta: 'Hel' });
                  c.enqueue({ type: 'text-delta', delta: 'lo' });
                  c.enqueue({ type: 'finish', finishReason: 'stop', usage: { inputTokens: 3, outputTokens: 2, reasoningTokens: 1 } });
                  c.close();
                },
              }),
            }),
            params: {},
            model,
          });
          const reader = res.stream.getReader();
          while (!(await reader.read()).done) {
            /* drain */
          }
          await p.onFinish({ text: 'Hello', finishReason: 'stop' });
        })();
        return { textStream: 'x' };
      },
    };
    const ai = weft.wrapAISDK(fakeAI);
    const r = await ai.generateText({ model, prompt: 'where is 7?', tools });
    expect(r.text).toBe('status shipped');
    let finished = '';
    ai.streamText({ model, prompt: 'hi', onFinish: (e: any) => (finished = e.text) });
    await new Promise((res) => setTimeout(res, 20));
    expect(finished).toBe('Hello');
    await settle();
    const [gt] = fake.callsByOp('vercel_ai.generateText');
    expect(gt.inputs).toMatchObject({ prompt: 'where is 7?', model: 'gpt-4o-mini', tools: ['order'] });
    expect(gt.output.text).toBe('status shipped');
    const [gen] = fake.callsByOp('vercel_ai.generate');
    expect(gen.parent_id).toBe(gt.id);
    expect(attr(gen, 'gen_ai.provider.name')).toBe('openai');
    const [tool] = fake.callsByOp('vercel_ai.tool.order');
    expect(tool.parent_id).toBe(gt.id);
    expect(attr(tool, 'gen_ai.tool.call.id')).toBe('tc1');
    expect(tool.output).toEqual({ id: 7, status: 'shipped' });
    const [lk] = fake.callsByOp('lookup');
    expect(lk.parent_id).toBe(tool.id);
    const [st] = fake.callsByOp('vercel_ai.streamText');
    expect(st.output.text).toBe('Hello');
    const [s] = fake.callsByOp('vercel_ai.stream');
    expect(s.parent_id).toBe(st.id);
    expect(s.output).toMatchObject({ text: 'Hello', reasoning: 'think', finishReason: 'stop' });
    expect(s.summary.usage['gpt-4o-mini']).toMatchObject({ prompt_tokens: 3, completion_tokens: 2, reasoning_tokens: 1 });
  });
});

describe('MCP', () => {
  it('traces client requests and server handlers', async () => {
    const mcpClient = {
      getServerVersion: () => ({ name: 'files', version: '1.0' }),
      async callTool(p: any) {
        return p.name === 'bad' ? { isError: true, content: [{ type: 'text', text: 'nope' }] } : { content: [{ type: 'text', text: `read ${p.arguments.path}` }] };
      },
      async listTools() {
        return { tools: [{ name: 'read' }] };
      },
      async readResource(p: any) {
        return { contents: [{ uri: p.uri, text: 'x' }] };
      },
    };
    weft.wrapMCPClient(mcpClient);
    await mcpClient.callTool({ name: 'read', arguments: { path: '/a' } });
    await mcpClient.callTool({ name: 'bad', arguments: {} });
    await mcpClient.listTools();
    await mcpClient.readResource({ uri: 'file:///a' });

    const inner = weft.trace(function inner(x: string) {
      return x.toUpperCase();
    });
    const handlers = new Map<string, any>();
    const server = { server: { _requestHandlers: handlers, _serverInfo: { name: 'srv', version: '2' } } };
    handlers.set('ping', async () => ({}));
    weft.wrapMCPServer(server);
    handlers.set('tools/call', async (req: any) => ({ content: [{ type: 'text', text: inner(req.params.arguments.text) }] }));
    const res = await handlers.get('tools/call')({ method: 'tools/call', params: { name: 'shout', arguments: { text: 'hi' }, _meta: { progressToken: 1 } } }, {});
    expect(res.content[0].text).toBe('HI');
    await handlers.get('ping')();
    await settle();
    const calls = fake.callsByOp('mcp.client.call_tool');
    expect(calls).toHaveLength(2);
    expect(calls[0].display_name).toBe('MCP tool: read');
    expect(calls[0].inputs).toEqual({ name: 'read', arguments: { path: '/a' } });
    expect(attr(calls[0], 'gen_ai.tool.name')).toBe('read');
    expect(calls[0].attributes.mcp.server.name).toBe('files');
    expect(calls[1].error).toContain('nope');
    expect(fake.callsByOp('mcp.client.list_tools')).toHaveLength(1);
    expect(fake.callsByOp('mcp.client.read_resource')[0].attributes.weft.span_kind).toBe('retriever');
    const [srv] = fake.callsByOp('mcp.server.call_tool');
    expect(srv.inputs).toEqual({ name: 'shout', arguments: { text: 'hi' } });
    expect(srv.attributes.mcp.server.name).toBe('srv');
    const [inn] = fake.callsByOp('inner');
    expect(inn.parent_id).toBe(srv.id);
    expect(fake.calls.size).toBe(6);
  });
});

describe('Claude Agent SDK', () => {
  it('traces query as an agent turn with LLM, tool and sub-agent spans', async () => {
    const sid = 'sess-123';
    const usage = { input_tokens: 10, output_tokens: 5, cache_read_input_tokens: 100, cache_creation_input_tokens: 0 };
    const msgs = [
      { type: 'system', subtype: 'init', session_id: sid, model: 'claude-sonnet-4-5', tools: ['Bash', 'Task'] },
      { type: 'assistant', session_id: sid, parent_tool_use_id: null, message: { id: 'm1', model: 'claude-sonnet-4-5', content: [{ type: 'text', text: 'Let me check' }], usage } },
      { type: 'assistant', session_id: sid, parent_tool_use_id: null, message: { id: 'm1', model: 'claude-sonnet-4-5', content: [{ type: 'tool_use', id: 'tu1', name: 'Bash', input: { command: 'ls' } }], usage } },
      { type: 'user', session_id: sid, parent_tool_use_id: null, message: { role: 'user', content: [{ type: 'tool_result', tool_use_id: 'tu1', content: 'a.txt' }] } },
      { type: 'assistant', session_id: sid, parent_tool_use_id: null, message: { id: 'm2', model: 'claude-sonnet-4-5', content: [{ type: 'tool_use', id: 'tu2', name: 'Task', input: { subagent_type: 'reviewer', prompt: 'review' } }], usage: { input_tokens: 3, output_tokens: 2 } } },
      { type: 'assistant', session_id: sid, parent_tool_use_id: 'tu2', message: { id: 'm3', model: 'claude-haiku-4-5', content: [{ type: 'tool_use', id: 'tu3', name: 'Read', input: { file_path: '/a.txt' } }], usage: { input_tokens: 4, output_tokens: 1 } } },
      { type: 'user', session_id: sid, parent_tool_use_id: 'tu2', message: { role: 'user', content: [{ type: 'tool_result', tool_use_id: 'tu3', content: 'denied', is_error: true }] } },
      { type: 'user', session_id: sid, parent_tool_use_id: null, message: { role: 'user', content: [{ type: 'tool_result', tool_use_id: 'tu2', content: [{ type: 'text', text: 'LGTM' }] }] } },
      { type: 'assistant', session_id: sid, parent_tool_use_id: null, message: { id: 'm4', model: 'claude-sonnet-4-5', content: [{ type: 'text', text: 'Done.' }], usage: { input_tokens: 1, output_tokens: 1 } } },
      { type: 'result', subtype: 'success', session_id: sid, result: 'Done.', total_cost_usd: 0.01, num_turns: 3, duration_ms: 1200, is_error: false, usage: { input_tokens: 99, output_tokens: 99 } },
    ];
    let interrupted = false;
    const sdk = {
      query: ({ prompt }: any) => {
        const g: any = gen(msgs);
        g.interrupt = async () => {
          interrupted = true;
        };
        g.prompt = prompt;
        return g;
      },
      other: 1,
    };
    const { query, other } = weft.wrapClaudeAgentSDK(sdk);
    expect(other).toBe(1);
    const q: any = query({ prompt: 'Fix the bug', options: { model: 'claude-sonnet-4-5', allowedTools: ['Bash'] } });
    const seen: any[] = [];
    for await (const m of q) seen.push(m);
    await q.interrupt();
    expect(interrupted).toBe(true);
    expect(seen).toHaveLength(msgs.length);
    await settle();
    const [root] = fake.callsByOp('claude_agent_sdk.query');
    expect(root.session_id).toBe(sid);
    expect(root.turn_id).toBe(root.id);
    expect(attr(root, 'gen_ai.agent.name')).toBe('claude-agent-sdk');
    expect(root.output).toMatchObject({ result: 'Done.', total_cost_usd: 0.01, num_turns: 3 });
    expect(root.summary).toBeUndefined();
    const llms = fake.callsByOp('claude_agent_sdk.llm');
    expect(llms).toHaveLength(5);
    expect(llms[0].summary.usage['claude-sonnet-4-5']).toMatchObject({ prompt_tokens: 110, completion_tokens: 5, cache_read_input_tokens: 100 });
    expect(llms[1].summary).toBeUndefined(); // same message id: usage counted once
    const [bash] = fake.callsByOp('claude_agent_sdk.tool.Bash');
    expect(bash.parent_id).toBe(root.id);
    expect(bash.display_name).toBe('Bash: ls');
    expect(bash.output.content).toBe('a.txt');
    expect(bash.session_id).toBe(sid);
    const [task] = fake.callsByOp('claude_agent_sdk.tool.Task');
    expect(task.attributes.weft.span_kind).toBe('agent');
    expect(attr(task, 'gen_ai.agent.name')).toBe('reviewer');
    const [read] = fake.callsByOp('claude_agent_sdk.tool.Read');
    expect(read.parent_id).toBe(task.id);
    expect(read.error).toContain('denied');
    expect(llms.find((c) => c.parent_id === task.id)).toBeTruthy();
  });
});

describe('OpenAI Agents JS', () => {
  it('maps traces and spans to calls', async () => {
    const added: any[] = [];
    const p = weft.instrumentOpenAIAgents({ addTraceProcessor: (x: any) => added.push(x) });
    expect(added[0]).toBe(p);
    const trace = { traceId: 't1', name: 'Support run', groupId: 'conv-9' };
    await p.onTraceStart(trace);
    const span = (id: string, parentId: string | null, spanData: any, extra: any = {}) => ({ spanId: id, traceId: 't1', parentId, spanData, startedAt: '2026-01-01T00:00:00.000Z', endedAt: '2026-01-01T00:00:01.000Z', error: null, ...extra });
    const agent = span('s1', null, { type: 'agent', name: 'Triage', tools: ['lookup'] });
    const g = span('s2', 's1', { type: 'generation', model: 'gpt-4.1', input: [{ role: 'user', content: 'hi' }], output: [{ role: 'assistant', content: 'yo' }], usage: { input_tokens: 8, output_tokens: 2 } });
    const fn = span('s3', 's1', { type: 'function', name: 'lookup', input: '{"id":7}', output: '{"ok":true}' });
    const bad = span('s4', 's1', { type: 'function', name: 'boom', input: '{}', output: '' }, { error: { message: 'failed', data: { x: 1 } } });
    for (const s of [agent, g, fn, bad]) await p.onSpanStart(s);
    for (const s of [g, fn, bad, agent]) await p.onSpanEnd(s);
    await p.onTraceEnd(trace);
    await settle();
    const [root] = fake.callsByOp('openai_agents.trace');
    expect(root.display_name).toBe('Support run');
    expect(attr(root, 'gen_ai.conversation.id')).toBe('conv-9');
    const [a] = fake.callsByOp('agent.Triage');
    expect(a.parent_id).toBe(root.id);
    expect(attr(a, 'gen_ai.agent.name')).toBe('Triage');
    const [gc] = fake.callsByOp('openai_agents.generation');
    expect(gc.parent_id).toBe(a.id);
    expect(gc.summary.usage['gpt-4.1']).toMatchObject({ prompt_tokens: 8, completion_tokens: 2 });
    expect(gc.started_at).toBe('2026-01-01T00:00:00.000Z');
    expect(gc.ended_at).toBe('2026-01-01T00:00:01.000Z');
    const [f] = fake.callsByOp('tool.lookup');
    expect(f.inputs).toEqual({ id: 7 });
    expect(f.output).toEqual({ ok: true });
    const [b] = fake.callsByOp('tool.boom');
    expect(b.error).toContain('failed');
  });
});

describe('Mistral and Cohere', () => {
  it('wrapMistral traces complete and stream (camelCase usage)', async () => {
    const mistral = weft.wrapMistral({
      chat: {
        async complete(req: any) {
          return { id: 'x', model: req.model, choices: [{ index: 0, message: { role: 'assistant', content: 'hi' }, finishReason: 'stop' }], usage: { promptTokens: 6, completionTokens: 2, totalTokens: 8 } };
        },
        async stream(req: any) {
          return new FakeStream([
            { data: { id: 's', model: req.model, choices: [{ index: 0, delta: { role: 'assistant', content: 'Bon' }, finishReason: null }] } },
            { data: { id: 's', model: req.model, choices: [{ index: 0, delta: { content: 'jour', toolCalls: [{ id: 't', function: { name: 'f', arguments: { a: 1 } } }] }, finishReason: 'stop' }], usage: { promptTokens: 3, completionTokens: 4 } } },
          ]);
        },
      },
    });
    await mistral.chat.complete({ model: 'mistral-small-latest', messages: [] });
    const s: any = await mistral.chat.stream({ model: 'mistral-large-latest', messages: [] });
    for await (const _ of s) {
      /* drain */
    }
    process.env.WEFT_AUTOPATCH_MISTRAL = 'false';
    await mistral.chat.complete({ model: 'mistral-small-latest', messages: [] });
    await settle();
    const [c] = fake.callsByOp('mistral.chat.complete');
    expect(fake.callsByOp('mistral.chat.complete')).toHaveLength(1);
    expect(c.summary.usage['mistral-small-latest']).toEqual({ requests: 1, prompt_tokens: 6, completion_tokens: 2, total_tokens: 8 });
    expect(attr(c, 'gen_ai.provider.name')).toBe('mistral_ai');
    const [st] = fake.callsByOp('mistral.chat.stream');
    expect(st.output.choices[0].message.content).toBe('Bonjour');
    expect(st.output.choices[0].message.tool_calls[0].function).toEqual({ name: 'f', arguments: '{"a":1}' });
    expect(st.summary.usage['mistral-large-latest']).toMatchObject({ prompt_tokens: 3, completion_tokens: 4 });
  });

  it('wrapCohere traces chat and chatStream', async () => {
    const co = weft.wrapCohere({
      async chat(req: any) {
        return { id: 'c', message: { role: 'assistant', content: [{ type: 'text', text: 'hey' }] }, finishReason: 'COMPLETE', usage: { tokens: { inputTokens: 5, outputTokens: 1 }, billedUnits: { inputTokens: 4, outputTokens: 1 } }, model: req.model };
      },
      async chatStream(_req: any) {
        return new FakeStream([
          { type: 'message-start', id: 'm' },
          { type: 'content-delta', delta: { message: { content: { text: 'Hel' } } } },
          { type: 'content-delta', delta: { message: { content: { text: 'lo' } } } },
          { type: 'message-end', delta: { finishReason: 'COMPLETE', usage: { tokens: { inputTokens: 2, outputTokens: 2 } } } },
        ]);
      },
    });
    await co.chat({ model: 'command-r-plus', messages: [] });
    const s: any = await co.chatStream({ model: 'command-a', messages: [] });
    for await (const _ of s) {
      /* drain */
    }
    await settle();
    const [c] = fake.callsByOp('cohere.chat');
    expect(c.summary.usage['command-r-plus']).toMatchObject({ prompt_tokens: 5, completion_tokens: 1 });
    const [st] = fake.callsByOp('cohere.chat_stream');
    expect(st.output.message.content[0].text).toBe('Hello');
    expect(st.output.finishReason).toBe('COMPLETE');
    expect(st.summary.usage['command-a']).toMatchObject({ prompt_tokens: 2, completion_tokens: 2 });
  });
});

describe('AWS Bedrock', () => {
  it('traces Converse, ConverseStream, InvokeModel and InvokeModelWithResponseStream', async () => {
    class ConverseCommand {
      constructor(public input: any) {}
    }
    class ConverseStreamCommand {
      constructor(public input: any) {}
    }
    class InvokeModelCommand {
      constructor(public input: any) {}
    }
    class InvokeModelWithResponseStreamCommand {
      constructor(public input: any) {}
    }
    class OtherCommand {
      constructor(public input: any) {}
    }
    const enc = (o: unknown) => new TextEncoder().encode(JSON.stringify(o));
    class BedrockRuntimeClient {
      async send(cmd: any) {
        switch (cmd.constructor.name) {
          case 'ConverseCommand':
            return { $metadata: { httpStatusCode: 200 }, output: { message: { role: 'assistant', content: [{ text: 'hi' }] } }, stopReason: 'end_turn', usage: { inputTokens: 9, outputTokens: 3, totalTokens: 12 } };
          case 'ConverseStreamCommand':
            return {
              stream: gen([
                { messageStart: { role: 'assistant' } },
                { contentBlockDelta: { delta: { text: 'Hel' } } },
                { contentBlockDelta: { delta: { text: 'lo' } } },
                { messageStop: { stopReason: 'end_turn' } },
                { metadata: { usage: { inputTokens: 4, outputTokens: 2, totalTokens: 6 } } },
              ]),
            };
          case 'InvokeModelCommand':
            return { body: enc({ content: [{ type: 'text', text: 'ok' }], usage: { input_tokens: 7, output_tokens: 1 } }), contentType: 'application/json' };
          case 'InvokeModelWithResponseStreamCommand':
            return {
              body: gen([
                { chunk: { bytes: enc({ type: 'content_block_delta', delta: { text: 'A' } }) } },
                { chunk: { bytes: enc({ type: 'message_stop', 'amazon-bedrock-invocationMetrics': { inputTokenCount: 5, outputTokenCount: 8 } }) } },
              ]),
            };
          default:
            return { other: true };
        }
      }
    }
    expect(weft.instrumentBedrock(BedrockRuntimeClient)).toBe(true);
    const br = new BedrockRuntimeClient();
    const model = 'anthropic.claude-3-5-sonnet-20241022-v2:0';
    await br.send(new ConverseCommand({ modelId: model, messages: [{ role: 'user', content: [{ text: 'hi' }] }] }));
    const cs: any = await br.send(new ConverseStreamCommand({ modelId: model, messages: [] }));
    for await (const _ of cs.stream) {
      /* drain */
    }
    const im: any = await br.send(new InvokeModelCommand({ modelId: model, body: JSON.stringify({ messages: [], max_tokens: 5 }) }));
    expect(JSON.parse(new TextDecoder().decode(im.body)).content[0].text).toBe('ok');
    const ims: any = await br.send(new InvokeModelWithResponseStreamCommand({ modelId: 'meta.llama3', body: enc({ prompt: 'x' }) }));
    for await (const _ of ims.body) {
      /* drain */
    }
    await br.send(new OtherCommand({}));
    await settle();
    const [c] = fake.callsByOp('bedrock.converse');
    expect(c.inputs.messages[0].content[0].text).toBe('hi');
    expect(c.summary.usage[model]).toEqual({ requests: 1, prompt_tokens: 9, completion_tokens: 3, total_tokens: 12 });
    expect(c.output.$metadata).toBeUndefined();
    expect(attr(c, 'gen_ai.request.model')).toBe(model);
    const [s] = fake.callsByOp('bedrock.converse_stream');
    expect(s.output.output.message.content[0].text).toBe('Hello');
    expect(s.summary.usage[model].total_tokens).toBe(6);
    const [i] = fake.callsByOp('bedrock.invoke_model');
    expect(i.inputs.body).toEqual({ messages: [], max_tokens: 5 });
    expect(i.output.body.content[0].text).toBe('ok');
    expect(i.summary.usage[model]).toMatchObject({ prompt_tokens: 7, completion_tokens: 1 });
    const [is] = fake.callsByOp('bedrock.invoke_model_with_response_stream');
    expect(is.output.text).toBe('A');
    expect(is.summary.usage['meta.llama3']).toMatchObject({ prompt_tokens: 5, completion_tokens: 8 });
    expect(fake.calls.size).toBe(4);
  });
});

describe('Google Vertex AI', () => {
  it('traces models from getGenerativeModel, streams and chat sessions', async () => {
    const resp = (t: string, p: number, c: number) => ({ candidates: [{ index: 0, content: { role: 'model', parts: [{ text: t }] }, finishReason: 'STOP' }], usageMetadata: { promptTokenCount: p, candidatesTokenCount: c, totalTokenCount: p + c } });
    class GenerativeModel {
      model: string;
      constructor(o: any) {
        this.model = `projects/p/locations/l/publishers/google/models/${o.model}`;
      }
      async generateContent(_req: any) {
        return { response: resp('hi', 4, 1) };
      }
      async generateContentStream(_req: any) {
        return { stream: gen([resp('h', 0, 0), resp('i', 0, 0)]), response: Promise.resolve(resp('hi', 5, 2)) };
      }
      startChat() {
        return {
          sendMessage: async (_m: string) => ({ response: resp('chat', 2, 2) }),
          sendMessageStream: async (_m: string) => ({ stream: gen([]), response: Promise.resolve(resp('cs', 1, 1)) }),
        };
      }
    }
    const vertex = weft.wrapVertexAI({ getGenerativeModel: (o: any) => new GenerativeModel(o), preview: { getGenerativeModel: (o: any) => new GenerativeModel(o) } });
    const m = vertex.getGenerativeModel({ model: 'gemini-2.0-flash' });
    await m.generateContent({ contents: [{ role: 'user', parts: [{ text: 'hi' }] }] });
    const s = await m.generateContentStream({ contents: [] });
    for await (const _ of s.stream) {
      /* drain */
    }
    await s.response;
    const chat = m.startChat();
    await chat.sendMessage('yo');
    await (await chat.sendMessageStream('yo')).response;
    await settle();
    const [g] = fake.callsByOp('vertexai.GenerativeModel.generate_content');
    expect(g.inputs.model).toBe('gemini-2.0-flash');
    expect(g.output.text).toBe('hi');
    expect(g.summary.usage['gemini-2.0-flash']).toEqual({ requests: 1, prompt_tokens: 4, completion_tokens: 1, total_tokens: 5 });
    const [gs] = fake.callsByOp('vertexai.GenerativeModel.generate_content_stream');
    expect(gs.summary.usage['gemini-2.0-flash'].total_tokens).toBe(7);
    const [cm] = fake.callsByOp('vertexai.ChatSession.send_message');
    expect(cm.output.text).toBe('chat');
    expect(fake.callsByOp('vertexai.ChatSession.send_message_stream')).toHaveLength(1);
  });
});
