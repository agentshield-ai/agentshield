// Recorded OpenClaw hook events -> the calls sent to Weft.
//   node --test plugins/weft-openclaw/test/*.test.mjs
import assert from 'node:assert/strict';
import { test } from 'node:test';
import plugin from '../index.js';
import { resolveConfig } from '../lib/openclaw.mjs';
import { fakeApi, recorder, replay } from './helpers.mjs';

test('a recorded session becomes a conversation with runs, model calls, tools and a sub-agent', async () => {
  const rec = recorder();
  const { tracer, returns, fx } = replay({ fetchImpl: rec.fetchImpl });
  clearInterval(tracer.timer);
  assert.ok(returns.every((r) => r === undefined), 'handlers never return a decision (no blocking)');
  await tracer.flush();
  const sid = fx.session_id;
  const calls = [...rec.calls().values()];
  const runs = calls.filter((c) => c.op_name === 'openclaw.agent_run');
  assert.equal(runs.length, 3, 'two runs of the session and the sub-agent run');
  const [r1, r2] = runs.filter((c) => c.parent_id === null).sort((a, b) => a.started_at.localeCompare(b.started_at));
  assert.equal(r1.thread_id, sid);
  assert.equal(r1.turn_id, r1.id);
  assert.equal(r1.attributes['gen_ai.agent.name'], 'main');
  assert.equal(r1.attributes['gen_ai.agent.id'], 'main');
  assert.equal(r1.attributes['gen_ai.conversation.id'], sid);
  assert.equal(r1.attributes['gen_ai.request.model'], 'claude-sonnet-4-5');
  assert.equal(r1.attributes.openclaw.channel, 'telegram');
  assert.deepEqual(r1.inputs.messages, [{ role: 'user', content: "What's the weather in Oslo? Ask the researcher too." }]);
  assert.equal(r1.output.messages[0].content, 'It is sunny and 12°C in Oslo now; tomorrow brings rain at 9°C.');
  assert.equal(r1.started_at, '2026-11-02T12:00:00.100Z');
  assert.equal(r1.ended_at, '2026-11-02T12:00:03.700Z');

  const llm1 = calls.filter((c) => c.op_name === 'openclaw.llm' && c.parent_id === r1.id).sort((a, b) => a.started_at.localeCompare(b.started_at));
  assert.equal(llm1.length, 3, 'one per model call');
  assert.equal(llm1[0].inputs.messages[0].role, 'system');
  assert.equal(llm1[0].inputs.messages.at(-1).content, "What's the weather in Oslo? Ask the researcher too.");
  assert.equal(llm1[0].ended_at, '2026-11-02T12:00:00.900Z');
  assert.deepEqual(llm1[2].summary.usage['claude-sonnet-4-5'], { requests: 1, prompt_tokens: 3300, completion_tokens: 60, total_tokens: 3360, cache_read_input_tokens: 2000, cache_creation_input_tokens: 100 });
  assert.equal(llm1[2].output.choices[0].message.content[0].text, 'It is sunny and 12°C in Oslo now; tomorrow brings rain at 9°C.');

  const fetch1 = calls.find((c) => c.attributes['gen_ai.tool.call.id'] === 'tc-1');
  assert.equal(fetch1.parent_id, r1.id);
  assert.equal(fetch1.display_name, 'web_fetch: https://wttr.in/Oslo?format=3');
  assert.equal(fetch1.output.text, 'Oslo: ☀️ +12°C');
  assert.equal(fetch1.started_at, '2026-11-02T12:00:01.000Z');
  assert.equal(fetch1.ended_at, '2026-11-02T12:00:01.400Z');

  const sub = calls.find((c) => c.op_name === 'openclaw.subagent');
  assert.equal(sub.parent_id, r1.id);
  assert.equal(sub.trace_id, r1.trace_id);
  assert.equal(sub.attributes['gen_ai.agent.name'], 'researcher');
  assert.equal(sub.thread_id, sid);
  const child = runs.find((c) => c.parent_id === sub.id);
  assert.ok(child, 'the child session run nests under the sub-agent (same trace)');
  assert.equal(child.trace_id, r1.trace_id);
  // the child session is its own Weft session: the id AgentShield sends as session_id for its tool calls
  assert.equal(child.thread_id, 'oc-sess-child-01');
  assert.equal(child.attributes['gen_ai.conversation.id'], 'oc-sess-child-01');
  assert.equal(child.turn_id, child.id);
  const search = calls.find((c) => c.attributes['gen_ai.tool.call.id'] === 'tc-c1');
  assert.equal(search.parent_id, child.id);
  assert.equal(search.attributes['gen_ai.conversation.id'], 'oc-sess-child-01');
  const childLLM = calls.find((c) => c.op_name === 'openclaw.llm' && c.parent_id === child.id);
  assert.equal(childLLM.summary.usage['claude-haiku-4-5'].prompt_tokens, 300);

  const exec = calls.find((c) => c.attributes['gen_ai.tool.call.id'] === 'tc-3');
  assert.equal(exec.parent_id, r2.id);
  assert.equal(exec.exception, 'blocked by policy');
  const llm2 = calls.filter((c) => c.op_name === 'openclaw.llm' && c.parent_id === r2.id);
  assert.equal(llm2.length, 1, 'without model_call events: one per llm_input/llm_output pair');
  assert.equal(llm2[0].inputs.messages.length, 3);
  assert.equal(tracer.runs.size, 0);
});

test('failed sends are retried; content capture can be turned off', async () => {
  const rec = recorder({ fail: 1 });
  const { tracer } = replay({ fetchImpl: rec.fetchImpl, pluginConfig: { project: 'test/openclaw', captureContent: false, agentName: 'claw' } });
  clearInterval(tracer.timer);
  await tracer.flush();
  assert.equal(rec.items.length, 0);
  assert.ok(tracer.queue.length > 0);
  await tracer.flush();
  assert.equal(tracer.queue.length, 0);
  const calls = [...rec.calls().values()];
  assert.ok(calls.every((c) => Object.keys(c.inputs).length === 0 && c.output === null));
  assert.ok(calls.some((c) => c.op_name === 'openclaw.agent_run' && c.attributes['gen_ai.agent.name'] === 'claw'));
  assert.ok(calls.some((c) => c.summary?.usage));
});

test('runs close without agent_end (no conversation access): a new run or session_end closes them', async () => {
  const rec = recorder();
  const api = fakeApi({ project: 'test/openclaw' });
  const { registerWeft } = await import('../lib/openclaw.mjs');
  const tracer = registerWeft(api, { fetchImpl: rec.fetchImpl });
  clearInterval(tracer.timer);
  const ctx = { sessionId: 's1', sessionKey: 'k1', agentId: 'main' };
  api.emit('model_call_started', { runId: 'a', callId: 'c1', model: 'gpt-5', provider: 'openai' }, ctx);
  api.emit('model_call_ended', { runId: 'a', callId: 'c1', model: 'gpt-5', provider: 'openai', durationMs: 5, outcome: 'error', errorCategory: 'rate_limit' }, ctx);
  api.emit('before_tool_call', { toolName: 'read', params: { path: 'x' }, runId: 'b' }, ctx);
  api.emit('session_end', { sessionId: 's1', reason: 'idle' }, ctx);
  await tracer.flush();
  const calls = [...rec.calls().values()];
  assert.equal(calls.filter((c) => c.op_name === 'openclaw.agent_run').length, 2);
  const llm = calls.find((c) => c.op_name === 'openclaw.llm');
  assert.equal(llm.exception, 'rate_limit');
  const tool = calls.find((c) => c.op_name === 'openclaw.tool.read');
  assert.match(tool.exception, /did not finish/);
});

test('plugin shape, config and a disabled plugin', async () => {
  assert.equal(plugin.id, 'weft');
  assert.equal(typeof plugin.register, 'function');
  const api = fakeApi({});
  plugin.register(api);
  assert.equal(api.handlers.size, 0, 'no project: no hooks');
  assert.match(api.logs[0][1], /no project/);
  const cfg = resolveConfig({ entity: 'team', project: 'claw' }, { WEFT_SERVER_URL: 'http://w:1/' });
  assert.equal(cfg.project, 'team/claw');
  assert.equal(cfg.serverUrl, 'http://w:1');
  assert.equal(resolveConfig({}, { WEFT_PROJECT: 'a/b' }).enabled, true);
  assert.equal(resolveConfig({ enabled: false, project: 'a/b' }).enabled, false);
  const api2 = fakeApi({ project: 'a/b' });
  plugin.register(api2);
  assert.ok(api2.handlers.has('before_tool_call') && api2.handlers.has('gateway_stop'));
  assert.equal(api2.handlers.get('before_tool_call').opts.priority, 1000);
  await api2.handlers.get('gateway_stop').h(); // stops the flush timer
});

test('a handler error is swallowed and the server being down never blocks a hook', async () => {
  const api = fakeApi({ project: 'test/openclaw', serverUrl: 'http://127.0.0.1:9', timeoutMs: 200 });
  const { registerWeft } = await import('../lib/openclaw.mjs');
  const tracer = registerWeft(api);
  clearInterval(tracer.timer);
  tracer.onBeforeToolCall = () => {
    throw new Error('boom');
  };
  const t0 = Date.now();
  assert.equal(api.emit('before_tool_call', { toolName: 'exec', params: {} }, {}), undefined);
  api.emit('agent_end', { runId: 'r', messages: [], success: true }, { sessionId: 's' });
  assert.ok(Date.now() - t0 < 50, 'hooks return synchronously');
  await tracer.flush();
  assert.ok(tracer.queue.length > 0, 'kept for retry');
  await api.handlers.get('gateway_stop').h();
});
