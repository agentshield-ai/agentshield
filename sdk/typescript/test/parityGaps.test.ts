/**
 * Bedrock Agents, OpenAI Realtime, NER PII redaction, registry linking, call
 * records, run linkage and the retry/queue/timeout/log-level environment
 * variables, against fakes and the in-process fake Weft server.
 */
import { createServer } from 'node:http';
import type { AddressInfo } from 'node:net';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import * as weft from '../src/index.js';
import { FakeWeft } from './fakeServer.js';
import { FakeRealtimeWS, InvokeAgentCommand, InvokeInlineAgentCommand, MODEL, agentEvents, agentRuntime, modelIO, realtimeEvents, tr } from './parityFixtures.js';

let fake: FakeWeft;
let client: weft.WeftClient;

beforeEach(async () => {
  fake = await new FakeWeft().start();
  client = await weft.init('ts-entity/gaps', { serverUrl: fake.url, sender: { flushIntervalMs: 5 }, settings: { printCallLink: false }, autopatchSettings: {} });
});

afterEach(async () => {
  await weft.finish();
  weft.configureIntegrations();
  await fake.close();
});

async function settle(c: weft.WeftClient = client): Promise<void> {
  await new Promise((r) => setTimeout(r, 10));
  await c.flush();
}

const kind = (c: any) => c.attributes?.weft?.span_kind;

// ------------------------------------------------------------------ Bedrock Agents
describe('AWS Bedrock Agents', () => {
  it('turns InvokeAgent trace events into agent, LLM, tool, retriever, collaborator and guardrail spans', async () => {
    const Cls = agentRuntime(agentEvents());
    expect(weft.instrumentBedrockAgent(Cls)).toBe(true);
    const res: any = await new Cls().send(new InvokeAgentCommand({ agentId: 'AGENT1', agentAliasId: 'ALIAS1', sessionId: 'sess-42', inputText: 'Weather in Oslo?', enableTrace: true }));
    let text = '';
    for await (const ev of res.completion) if (ev.chunk) text += new TextDecoder().decode(ev.chunk.bytes);
    expect(text).toBe('It is 12°C in Oslo; rain tomorrow.');
    await settle();
    const [root] = fake.callsByOp('bedrock_agent.invoke_agent');
    expect(kind(root)).toBe('agent');
    expect(root.session_id).toBe('sess-42');
    expect(root.turn_id).toBe(root.id);
    expect(root.attributes['gen_ai.agent.name']).toBe('AGENT1');
    expect(root.attributes['gen_ai.conversation.id']).toBe('sess-42');
    expect(root.inputs.messages).toEqual([{ role: 'user', content: 'Weather in Oslo?' }]);
    expect(root.output.completion).toBe(text);
    expect(root.output.usage).toEqual({ input_tokens: 900, output_tokens: 105 });
    expect(root.output.citations[0].retrievedReferences[0].location).toBe('s3://kb/oslo.txt');
    expect(root.output.sessionId).toBe('sess-42');
    expect(root.error).toBeFalsy();

    const llms = fake.callsByOp('bedrock_agent.model_invocation');
    expect(llms).toHaveLength(5);
    expect(llms.every((c) => kind(c) === 'llm' && c.session_id === 'sess-42' && c.turn_id === root.id)).toBe(true);
    const pre = llms.find((c) => c.inputs.stage === 'pre_processing');
    expect(pre.inputs.system).toBe('You are a weather agent.');
    expect(pre.output.parsed_response).toEqual({ isValid: true, rationale: 'fine' });
    expect(pre.summary.usage[MODEL]).toEqual({ requests: 1, prompt_tokens: 100, completion_tokens: 10, total_tokens: 110 });

    const [tool] = fake.callsByOp('bedrock_agent.action_group');
    expect(tool.parent_id).toBe(root.id);
    expect(tool.inputs).toEqual({ action_group: 'weather', function: 'get_weather', parameters: { city: 'Oslo' }, execution_type: 'LAMBDA' });
    expect(tool.output).toEqual({ temp: 12 });
    expect(tool.attributes['gen_ai.tool.name']).toBe('weather.get_weather');
    const [kb] = fake.callsByOp('bedrock_agent.knowledge_base');
    expect(kind(kb)).toBe('retriever');
    expect(kb.output.count).toBe(1);
    const [collab] = fake.callsByOp('bedrock_agent.collaborator');
    expect(kind(collab)).toBe('agent');
    expect(collab.output).toBe('Rain tomorrow.');
    expect(llms.filter((c) => c.parent_id === collab.id)).toHaveLength(1);
    const [g] = fake.callsByOp('bedrock_agent.guardrail');
    expect(g.output.action).toBe('NONE');
    expect([...fake.calls.values()].every((c) => c.ended_at)).toBe(true);
  });

  it('records failures, returnControl and stream errors; other commands pass through', async () => {
    const fail = [modelIO('orchestrationTrace', 't-0', [10, 1], 'x')[0], tr({ failureTrace: { traceId: 't-0', failureReason: 'Lambda timed out' } })];
    const F = agentRuntime(fail);
    const br = weft.wrapBedrockAgent(new F());
    for await (const _ of ((await br.send(new InvokeAgentCommand({ agentId: 'A', agentAliasId: 'B', sessionId: 's1', inputText: 'hi' }))) as any).completion) {
      /* drain */
    }
    const R = agentRuntime([{ returnControl: { invocationId: 'rc-1', invocationInputs: [{ functionInvocationInput: { actionGroup: 'billing', function: 'refund', parameters: [{ name: 'order', value: '42' }] } }] } }]);
    const inline: any = await weft.wrapBedrockAgent(new R()).send(new InvokeInlineAgentCommand({ sessionId: 's2', foundationModel: MODEL, instruction: 'help', inputText: 'refund 42' }));
    for await (const _ of inline.completion) break; // an early break still ends the call
    const E = agentRuntime([new Error('stream broke')]);
    const bad: any = await weft.wrapBedrockAgent(new E()).send(new InvokeAgentCommand({ agentId: 'A', agentAliasId: 'B', sessionId: 's3', inputText: 'x' }));
    await expect(
      (async () => {
        for await (const _ of bad.completion) {
          /* drain */
        }
      })(),
    ).rejects.toThrow('stream broke');
    expect(await weft.wrapBedrockAgent(new E()).send({ input: {} })).toEqual({ other: true });
    await settle();
    const roots = [...fake.calls.values()].filter((c) => fake.opBase(c).startsWith('bedrock_agent.invoke'));
    const by = Object.fromEntries(roots.map((c) => [c.inputs.sessionId, c]));
    expect(by.s1.error).toMatch(/Lambda timed out/);
    expect(fake.callsByOp('bedrock_agent.model_invocation')[0].error).toMatch(/Lambda timed out/);
    expect(fake.opBase(by.s2)).toBe('bedrock_agent.invoke_inline_agent');
    expect(by.s2.attributes['gen_ai.request.model']).toBe(MODEL);
    expect(by.s2.output.return_control[0].invocations[0]).toEqual({ tool: 'billing.refund', action_group: 'billing', parameters: { order: '42' }, function: 'refund' });
    expect(fake.callsByOp('bedrock_agent.return_control')[0].parent_id).toBe(by.s2.id);
    expect(by.s3.error).toMatch(/stream broke/);
  });
});

// ------------------------------------------------------------------ OpenAI Realtime
describe('OpenAI Realtime', () => {
  it('records the session, responses with usage, and function calls', async () => {
    const rt = weft.wrapOpenAIRealtime(new FakeRealtimeWS());
    const seen: string[] = [];
    rt.on('event', (e: any) => seen.push(e.type));
    const evs = realtimeEvents();
    rt.send({ type: 'session.update', session: { instructions: 'Be brief.', voice: 'alloy' } });
    rt.send({ type: 'input_audio_buffer.append', audio: 'C'.repeat(800) });
    rt.send({ type: 'conversation.item.create', item: { type: 'message', role: 'user', content: [{ type: 'input_text', text: 'Weather in Paris?' }] } });
    rt.send({ type: 'response.create' });
    for (const e of evs.slice(0, 7)) rt.receive(e);
    rt.send({ type: 'conversation.item.create', item: { type: 'function_call_output', call_id: 'call_1', output: '{"temp": 21}' } });
    rt.receive(evs[7]);
    rt.receive(evs[8]);
    rt.close();
    expect(rt.closed).toBe(true);
    expect(seen).toHaveLength(9);
    expect(rt.sent).toHaveLength(5);
    await settle();
    const [sess] = fake.callsByOp('openai.realtime.session');
    expect(kind(sess)).toBe('agent');
    expect(sess.inputs.model).toBe('gpt-realtime');
    expect(sess.output).toMatchObject({ responses: 2, server_events: 9, client_events: 5 });
    const [r1, r2] = fake.callsByOp('openai.realtime.response');
    expect(r1.parent_id).toBe(sess.id);
    expect(kind(r1)).toBe('llm');
    expect(r1.inputs.input[0].content[0].text).toBe('Weather in Paris?');
    expect(r1.inputs.input_audio_bytes).toBe(600);
    expect(r1.inputs.instructions).toBe('Be brief.');
    expect(r1.output.text).toBe('It is sunny.');
    expect(r1.output.audio_bytes).toBe(300);
    expect(r1.output.output[0].content[0]).toMatchObject({ type: 'output_audio', transcript: 'It is sunny.', audio_bytes: 300, audio: { _type: 'Audio', mime_type: 'audio/wav' } });
    expect(r1.summary.usage['gpt-realtime']).toMatchObject({ prompt_tokens: 100, completion_tokens: 50, cached_tokens: 20, input_audio_tokens: 60, output_audio_tokens: 40 });
    expect(sess.summary?.usage).toBeUndefined();
    expect(r2.error).toMatch(/rate limited/);
    expect(r2.inputs.input[0].type).toBe('function_call_output');
    const [tool] = fake.callsByOp('openai.realtime.tool.get_weather');
    expect(kind(tool)).toBe('tool');
    expect(tool.inputs).toEqual({ city: 'Paris' });
    expect(tool.output).toEqual({ temp: 21 });
  });

  it('instruments a connection class, takes the model from the URL, and feeds raw websockets through RealtimeTracer', async () => {
    class OpenAIRealtimeWS extends FakeRealtimeWS {}
    expect(weft.instrumentOpenAIRealtime(OpenAIRealtimeWS)).toBe(true);
    const a = new OpenAIRealtimeWS();
    a.url = new URL('wss://x/v1/realtime?model=gpt-4o-realtime-preview');
    a.send({ type: 'response.create' });
    a.receive({ type: 'response.created', response: { id: 'r' } });
    a._emit('close', 1000, 'OK'); // recovery-mode close event ends the session
    const rt = new weft.RealtimeTracer();
    rt.onServerEvent(JSON.stringify(realtimeEvents()[0]));
    rt.onServerEvent(realtimeEvents()[1]);
    rt.onServerEvent('{"type": "response.output_text.delta", "response_id": "resp_1", "delta": "hi"}');
    rt.close();
    await settle();
    const sessions = fake.callsByOp('openai.realtime.session');
    expect(sessions.map((s) => s.inputs.model).sort()).toEqual(['gpt-4o-realtime-preview', 'gpt-realtime']);
    const resps = fake.callsByOp('openai.realtime.response');
    expect(resps.find((r) => r.output.text === 'hi').output.status).toBe('incomplete');
    expect(resps.every((r) => r.ended_at)).toBe(true);
  });
});

// ------------------------------------------------------------------ NER PII redaction
describe('NER PII redaction', () => {
  it('runs a pluggable analyzer before calls leave the process', async () => {
    const analyzer = vi.fn(async (text: string, entities: string[]) => {
      expect(entities).toContain('PERSON');
      const out: weft.PiiSpan[] = [];
      for (const name of ['Alice Smith', 'Bob']) {
        const i = text.indexOf(name);
        if (i >= 0) out.push({ entity_type: 'PERSON', start: i, end: i + name.length, score: 0.9 });
      }
      const city = text.indexOf('Paris');
      if (city >= 0) out.push([`LOCATION`, city, city + 5, 0.2]); // below the threshold
      return out;
    });
    const c = await weft.init('ts-entity/ner', {
      serverUrl: fake.url,
      sender: { flushIntervalMs: 5 },
      settings: { printCallLink: false, redactPii: true, redactPiiAnalyzer: analyzer, redactPiiScoreThreshold: 0.5, redactPiiExcludeFields: ['PHONE_NUMBER'] },
    });
    const greet = weft.trace(function greet(msg: string, meta: Record<string, unknown>) {
      return `Hi Bob, mail alice@example.com or call 555-123-4567 from Paris`;
    });
    await greet('I am Alice Smith', { api_key: 'sk-1', note: 'Bob says hi' });
    await settle(c);
    const [call] = fake.callsByOp('greet');
    expect(call.inputs.msg).toBe('I am <PERSON>');
    expect(call.inputs.meta).toEqual({ api_key: 'REDACTED', note: '<PERSON> says hi' });
    expect(call.output).toBe('Hi <PERSON>, mail <EMAIL_ADDRESS> or call 555-123-4567 from Paris');
    expect(analyzer).toHaveBeenCalled();
  });

  it('calls a Presidio Analyzer REST service, and falls back to regex when it fails', async () => {
    const seen: any[] = [];
    let fail = false;
    const srv = createServer(async (req, res) => {
      const chunks: Buffer[] = [];
      for await (const ch of req) chunks.push(ch as Buffer);
      const body = JSON.parse(Buffer.concat(chunks).toString());
      seen.push({ path: req.url, body });
      if (fail) {
        res.writeHead(500);
        return res.end('boom');
      }
      const i = body.text.indexOf('Ada Lovelace');
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify(i >= 0 ? [{ entity_type: 'PERSON', start: i, end: i + 12, score: 0.85 }] : []));
    });
    await new Promise<void>((r) => srv.listen(0, '127.0.0.1', () => r()));
    const url = `http://127.0.0.1:${(srv.address() as AddressInfo).port}`;
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {});
    try {
      process.env.WEFT_PRESIDIO_ANALYZER_URL = url;
      process.env.WEFT_REDACT_PII = 'true';
      const c = await weft.init('ts-entity/presidio', { serverUrl: fake.url, sender: { flushIntervalMs: 5 }, settings: { printCallLink: false } });
      expect(c.redactor.ner).not.toBeNull();
      const echo = weft.trace(function echo(text: string) {
        return text;
      });
      await echo('Ada Lovelace wrote to ada@example.com');
      await settle(c);
      expect(seen[0].path).toBe('/analyze');
      expect(seen[0].body.language).toBe('en');
      expect(seen[0].body.entities).toContain('PERSON');
      expect(seen[0].body.entities).not.toContain('EMAIL_ADDRESS');
      const [call] = fake.callsByOp('echo');
      expect(call.inputs.text).toBe('<PERSON> wrote to <EMAIL_ADDRESS>');
      expect(call.output).toBe('<PERSON> wrote to <EMAIL_ADDRESS>');
      fail = true;
      await echo('Ada Lovelace again, ada@example.com');
      await settle(c);
      const second = fake.callsByOp('echo')[1];
      expect(second.inputs.text).toBe('Ada Lovelace again, <EMAIL_ADDRESS>');
      expect(warn.mock.calls.some((a) => String(a[0]).includes('NER PII redaction failed'))).toBe(true);
    } finally {
      delete process.env.WEFT_PRESIDIO_ANALYZER_URL;
      delete process.env.WEFT_REDACT_PII;
      warn.mockRestore();
      await new Promise((r) => srv.close(r));
    }
  });

  it('applyPiiSpans keeps the longest overlapping span', () => {
    expect(weft.applyPiiSpans('Mary Ann Jones', [['PERSON', 0, 4], ['PERSON', 0, 14], { entityType: 'LOCATION', start: 5, end: 8 }])).toBe('<PERSON>');
    expect(() => new weft.Redactor({ redactPii: true, redactPiiEngine: 'presidio' })).toThrow(/redactPiiPresidioUrl/);
  });
});

// ------------------------------------------------------------------ SDK extras
describe('SDK extras', () => {
  it('links prompts and any object to a registry', async () => {
    const reg = new Map<string, any[]>();
    fake.routes.set('/api/v1/registry/link', (b) => {
      const key = `${b.project_id}|${b.collection}`;
      const vs = reg.get(key) ?? [];
      let v = vs.find((x) => x.ref === b.ref);
      if (!v) vs.push((v = { version_index: vs.length, version: `v${vs.length}`, ref: b.ref, aliases: [] }));
      for (const a of b.aliases ?? []) {
        for (const x of vs) x.aliases = x.aliases.filter((y: string) => y !== a);
        v.aliases.push(a);
      }
      reg.set(key, vs);
      return { version: v.version_index };
    });
    fake.routes.set('/api/v1/registry/query', (b) => ({
      collections: [...reg.entries()]
        .filter(([k]) => k.startsWith(`${b.project_id}|`) && (!b.portfolio_name || k.endsWith(`|${b.portfolio_name}`)))
        .map(([k, vs]) => ({
          name: k.split('|')[1],
          versions: [...vs].reverse().filter((v) => (!b.alias || v.aliases.includes(b.alias)) && (b.version_index === undefined || v.version_index === b.version_index)),
        })),
    }));
    const p1 = new weft.StringPrompt({ name: 'support', content: 'Answer {q} briefly.' });
    await weft.save(p1);
    const r = await weft.linkPromptToRegistry(p1, { targetPath: 'prompts/support', aliases: ['production'] });
    expect(r).toMatchObject({ versionIndex: 0, targetPath: 'ts-entity/prompts/support' });
    const req = fake.requests.find((q) => q.path === '/api/v1/registry/link')!.body;
    expect(req).toMatchObject({ project_id: 'ts-entity/prompts', collection: 'support' });
    expect(req.ref).toBe(r.ref);
    const ds = new weft.Dataset({ name: 'golden', rows: [{ q: '1+1' }] });
    expect((await weft.linkToRegistry(ds, { targetPath: 'other/datasets/golden' })).versionIndex).toBe(0);
    const p2 = new weft.StringPrompt({ name: 'support', content: 'Answer {q} in one word.' });
    await weft.save(p2);
    expect((await weft.linkToRegistry('support:latest', { targetPath: 'prompts/support', aliases: ['production'] })).versionIndex).toBe(1);
    const [col] = await client.queryRegistry('prompts', { collection: 'support' });
    expect(col.versions.map((v) => v.version_index)).toEqual([1, 0]);
    const prod = await client.getFromRegistry('prompts/support', 'production');
    expect(prod.format({ q: 'x' })).toBe('Answer x in one word.');
    expect((await client.getFromRegistry('prompts/support', 'v0')).format({ q: 'x' })).toBe('Answer x briefly.');
    await expect(weft.linkToRegistry(p1, { targetPath: 'nope' })).rejects.toThrow(/targetPath/);
  });

  it('returns calls as records and passes include_usernames / include_storage_size', async () => {
    const f = weft.trace(function f(x: number) {
      return x + 1;
    });
    await f(1);
    await f(2);
    const recs = await client.spanRecords({ filter: { names: ['f'] }, includeUsernames: true, includeStorageSize: true, includeRunStatus: true });
    expect(recs.map((r) => r.output).sort()).toEqual([2, 3]);
    expect(recs[0]).toMatchObject({ status: 'success', name: expect.stringContaining('/function/f:'), error: null, session_id: null, run_id: null });
    const q = fake.requests.filter((r) => r.path === '/api/v1/spans/stream').pop()!.body;
    expect(q.include_usernames).toBe(true);
    expect(q.include_storage_size).toBe(true);
    expect(q.include_run_status).toBe(true);
    expect(new weft.Span({ id: 'x', project_id: 'a/b', name: 'o', trace_id: 't', username: 'ann', run_step: 2 }, null).toRecord()).toMatchObject({ username: 'ann', run_step: 2 });
  });

  it('sends run_id / run_step / run_step_end', async () => {
    let step = 3;
    const trainStep = weft.trace(function trainStep() {
      step += 1;
      return step;
    });
    weft.setRun('run-abc', () => step);
    await trainStep();
    weft.setRun(null);
    await trainStep();
    client.setRun('ent/proj/run-x', 9);
    await trainStep();
    await settle();
    const [a, b, c] = fake.callsByOp('trainStep');
    expect(a).toMatchObject({ run_id: 'ts-entity/gaps/run-abc', run_step: 3, run_step_end: 4 });
    expect(b.run_id).toBeUndefined();
    expect(c).toMatchObject({ run_id: 'ent/proj/run-x', run_step: 9, run_step_end: 9 });
  });

  it('reads retry, queue, timeout and log-level settings from the environment', async () => {
    const env = { WEFT_RETRY_MAX_ATTEMPTS: '7', WEFT_RETRY_BACKOFF: '0.5', WEFT_RETRY_MAX_INTERVAL: '9', WEFT_MAX_CALLS_QUEUE_SIZE: '0', WEFT_HTTP_TIMEOUT: '4.5' };
    Object.assign(process.env, env);
    try {
      const c = new weft.WeftClient('e/p', { serverUrl: fake.url });
      expect([c.sender.maxRetries, c.sender.backoffMs, c.sender.maxBackoffMs, c.sender.maxQueue]).toEqual([7, 500, 9000, Infinity]);
      expect(c.http.timeoutMs).toBe(4500);
      const c2 = new weft.WeftClient('e/p', { serverUrl: fake.url, sender: { maxRetries: 2 }, settings: { retryMaxAttempts: 1, httpTimeoutMs: 1000, maxQueueSize: 5 } });
      expect([c2.sender.maxRetries, c2.sender.maxQueue, c2.http.timeoutMs]).toEqual([1, 5, 1000]);
      process.env.WEFT_HTTP_TIMEOUT = 'soon';
      const warn = vi.spyOn(console, 'warn').mockImplementation(() => {});
      expect(new weft.WeftClient('e/p', { serverUrl: fake.url }).http.timeoutMs).toBe(30_000);
      expect(warn).toHaveBeenCalled();
      warn.mockRestore();
    } finally {
      for (const k of Object.keys(env)) delete process.env[k];
    }
    const d = new weft.WeftClient('e/p', { serverUrl: fake.url });
    expect([d.sender.maxRetries, d.sender.backoffMs, d.sender.maxBackoffMs, d.sender.maxQueue, d.http.timeoutMs]).toEqual([3, 250, 4000, 10_000, 30_000]);

    const before = weft.getLogLevel();
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {});
    const debug = vi.spyOn(console, 'debug').mockImplementation(() => {});
    try {
      new weft.WeftClient('e/p', { serverUrl: fake.url, settings: { logLevel: 'error' } });
      weft.logger.warn('hidden');
      expect(warn).not.toHaveBeenCalled();
      weft.setLogLevel('DEBUG');
      weft.logger.debug('shown');
      expect(debug).toHaveBeenCalledWith('shown');
      expect(weft.parseLogLevel('warn')).toBe('warning');
      expect(() => weft.setLogLevel('loud')).toThrow();
    } finally {
      weft.setLogLevel(before);
      warn.mockRestore();
      debug.mockRestore();
    }
  });
});
