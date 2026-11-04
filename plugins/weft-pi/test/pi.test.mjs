// Recorded Pi extension events -> the calls sent to Weft.
//   node --test plugins/weft-pi/test/*.test.mjs
import assert from 'node:assert/strict';
import { test } from 'node:test';
import weftDefault, { createWeftExtension } from '../index.js';
import { chatMessage, registerWeft, resolveConfig } from '../lib/pi.mjs';
import { SESSION_ID, fakeCtx, fakePi, recorder, replay } from './helpers.mjs';

const settle = async (tracer) => {
  while (tracer.sending) await tracer.sending;
  await tracer.flush();
};

test('a recorded Pi session becomes one conversation with turns, LLM calls and tools', async () => {
  const rec = recorder();
  const { tracer, returns } = await replay({ fetchImpl: rec.fetchImpl });
  clearInterval(tracer.timer);
  assert.ok(returns.every((r) => r === undefined), 'handlers never return a value (no blocking, no rewriting)');
  await settle(tracer);
  const calls = [...rec.calls().values()];
  const runs = calls.filter((c) => c.name === 'pi.agent_run').sort((a, b) => a.started_at.localeCompare(b.started_at));
  assert.equal(runs.length, 2);
  const [r1, r2] = runs;
  assert.equal(r1.session_id, SESSION_ID);
  assert.equal(r1.turn_id, r1.id);
  assert.equal(r1.parent_id, null);
  assert.equal(r1.display_name, 'invoke_agent pi');
  assert.equal(r1.attributes['gen_ai.agent.name'], 'pi');
  assert.equal(r1.attributes['gen_ai.conversation.id'], SESSION_ID);
  assert.equal(r1.attributes['gen_ai.conversation.name'], 'Fix the failing test in math.js');
  assert.equal(r1.attributes['gen_ai.request.model'], 'claude-sonnet-4-5');
  assert.equal(r1.attributes['gen_ai.provider.name'], 'anthropic');
  assert.equal(r1.attributes.weft.span_kind, 'agent');
  assert.equal(r1.attributes.weft.source, 'weft-pi');
  assert.equal(r1.attributes.pi.cwd, '/work/calc');
  assert.equal(r1.attributes.pi.llm_calls, 3);
  assert.equal(r1.attributes.pi.cost_usd, 0.0123);
  assert.deepEqual(r1.inputs.messages, [
    { role: 'system', content: 'You are pi, a coding agent.' },
    { role: 'user', content: 'Fix the failing test in math.js' },
  ]);
  assert.equal(r1.output.messages[0].content, 'Fixed: add() subtracted instead of adding.');
  assert.equal(r1.started_at, '2026-11-02T12:00:00.100Z');
  assert.equal(r1.ended_at, '2026-11-02T12:00:02.700Z');
  assert.equal(r1.error, undefined);

  const llms = calls.filter((c) => c.name === 'pi.llm' && c.parent_id === r1.id).sort((a, b) => a.started_at.localeCompare(b.started_at));
  assert.equal(llms.length, 3, 'one per LLM response');
  assert.equal(llms[0].started_at, '2026-11-02T12:00:00.130Z');
  assert.equal(llms[0].ended_at, '2026-11-02T12:00:00.900Z');
  assert.equal(llms[0].attributes.weft.span_kind, 'llm');
  assert.deepEqual(llms[0].summary.usage['claude-sonnet-4-5'], { requests: 1, prompt_tokens: 4400, completion_tokens: 80, total_tokens: 4480, cache_read_input_tokens: 3000, cache_creation_input_tokens: 200 });
  const msg0 = llms[0].output.choices[0].message;
  assert.deepEqual(msg0.content, [{ type: 'thinking', thinking: 'Look first.' }, { type: 'text', text: 'Let me look.' }]);
  assert.deepEqual(msg0.tool_calls.map((t) => t.function.name), ['read', 'bash']);
  assert.equal(llms[0].output.choices[0].finish_reason, 'toolUse');
  const ctx1 = llms[1].inputs.messages;
  assert.equal(ctx1[0].role, 'system');
  assert.deepEqual(ctx1.slice(-2), [
    { role: 'tool', tool_call_id: 'tc-read', name: 'read', content: 'export const add = (a, b) => a - b;' },
    { role: 'tool', tool_call_id: 'tc-bash', name: 'bash', content: '1 failing' },
  ]);
  assert.equal(llms[2].display_name, 'Fixed: add() subtracted instead of adding.');

  const tools = calls.filter((c) => c.attributes.weft.span_kind === 'tool');
  assert.deepEqual(tools.map((t) => t.name).sort(), ['pi.tool.bash', 'pi.tool.edit', 'pi.tool.read']);
  const bash = tools.find((t) => t.name === 'pi.tool.bash');
  assert.equal(bash.parent_id, r1.id);
  assert.equal(bash.display_name, 'bash: npm test');
  assert.equal(bash.error, '1 failing');
  assert.equal(bash.attributes['gen_ai.tool.call.id'], 'tc-bash');
  assert.equal(bash.started_at, '2026-11-02T12:00:00.920Z');
  assert.equal(bash.ended_at, '2026-11-02T12:00:01.500Z');
  const read = tools.find((t) => t.name === 'pi.tool.read');
  assert.deepEqual(read.output, { text: 'export const add = (a, b) => a - b;', details: { lines: 1 } });
  assert.deepEqual(read.inputs, { path: 'math.js' });

  assert.equal(r2.inputs.messages.at(-1).content, 'Now delete node_modules');
  assert.equal(r2.error, '429 rate limited');
  const [failedLlm] = calls.filter((c) => c.name === 'pi.llm' && c.parent_id === r2.id);
  assert.equal(failedLlm.error, '429 rate limited');
  assert.equal(calls.length, 2 + 4 + 3);
});

test('ids are stable, failed sends are retried, and shutdown closes an open run', async () => {
  const rec = recorder({ fail: 1 });
  const { tracer } = await replay({ fetchImpl: rec.fetchImpl });
  clearInterval(tracer.timer);
  await settle(tracer);
  assert.equal(tracer.stats.failed, 1, 'the first send failed');
  await settle(tracer);
  assert.equal(tracer.queue.length, 0, 'and was resent by a later flush');
  assert.equal(rec.calls().size, 9, 'nothing lost');
  const first = [...rec.calls().keys()].sort();
  const again = recorder();
  const { tracer: t2 } = await replay({ fetchImpl: again.fetchImpl });
  clearInterval(t2.timer);
  await settle(t2);
  assert.deepEqual([...again.calls().keys()].sort(), first, 'same session -> same call ids');

  // a run with no agent_end is closed by session_shutdown
  const rec3 = recorder();
  const pi = fakePi();
  registerForTest(pi, rec3.fetchImpl);
  const ctx = fakeCtx('s-open');
  await pi.emit('before_agent_start', { prompt: 'long task', systemPrompt: '' }, ctx);
  await pi.emit('agent_start', {}, ctx);
  await pi.emit('tool_execution_start', { toolCallId: 'x', toolName: 'bash', args: { command: 'sleep 100' } }, ctx);
  await pi.emit('session_shutdown', { reason: 'quit' }, ctx);
  const c3 = [...rec3.calls().values()];
  const run = c3.find((c) => c.name === 'pi.agent_run');
  assert.match(run.error, /run closed without agent_end \(session quit\)/);
  assert.equal(c3.find((c) => c.name === 'pi.tool.bash').error, 'tool did not finish before the run ended');
});

function registerForTest(pi, fetchImpl) {
  const t = registerWeft(pi, { project: 'test/pi' }, { env: {}, fetchImpl, logger: { warn() {}, info() {} } });
  clearInterval(t.timer);
  return t;
}

test('content capture off keeps structure, usage and names only', async () => {
  const rec = recorder();
  const { tracer } = await replay({ options: { project: 'test/pi', captureContent: false, agentName: 'my-pi' }, fetchImpl: rec.fetchImpl });
  clearInterval(tracer.timer);
  await settle(tracer);
  const calls = [...rec.calls().values()];
  assert.ok(calls.every((c) => Object.keys(c.inputs).length === 0 && c.output === null));
  const llm = calls.find((c) => c.name === 'pi.llm');
  assert.equal(llm.summary.usage['claude-sonnet-4-5'].completion_tokens, 80);
  assert.equal(calls.find((c) => c.name === 'pi.agent_run').attributes['gen_ai.agent.name'], 'my-pi');
});

test('configuration: disabled without a project; environment and options; the /weft command', async () => {
  const pi = fakePi();
  assert.equal(registerWeft(pi, {}, { env: {} }), null);
  assert.equal(pi.handlers.size, 0, 'no project -> nothing registered');
  const cfg = resolveConfig({}, { WEFT_PROJECT: 'team/pi', WEFT_SERVER_URL: 'http://h:1/', WEFT_CAPTURE_CONTENT: 'false', WEFT_PI_STATE_DIR: '/nonexistent' });
  assert.equal(cfg.project, 'team/pi');
  assert.equal(cfg.serverUrl, 'http://h:1');
  assert.equal(cfg.captureContent, false);
  assert.equal(cfg.disabled, false);
  assert.equal(resolveConfig({ project: 'a/b', disabled: true }, {}).disabled, true);

  const rec = recorder();
  const { pi: api, tracer, ctx } = await replay({ fetchImpl: rec.fetchImpl });
  clearInterval(tracer.timer);
  await api.commands.get('weft').handler('', ctx);
  assert.match(ctx.notes[0][1], /conversations\/0198f0c2-7d1e-7a51-9f00-5e55a0c0ffee/);
  assert.equal(typeof weftDefault, 'function');
  assert.equal(typeof createWeftExtension({ project: 'x/y' }), 'function');
});

test('Pi messages map to chat messages', () => {
  assert.deepEqual(chatMessage({ role: 'user', content: [{ type: 'text', text: 'hi' }, { type: 'image', data: 'AAAA', mimeType: 'image/png' }] }), {
    role: 'user',
    content: [{ type: 'text', text: 'hi' }, { type: 'text', text: '[image image/png]' }],
  });
  assert.deepEqual(chatMessage({ role: 'bashExecution', command: 'ls', output: 'a.txt' }), { role: 'user', content: '$ ls\na.txt' });
  assert.equal(chatMessage({ role: 'custom', content: '' }), null);
  assert.deepEqual(chatMessage({ role: 'compactionSummary', summary: 'earlier work' }), { role: 'user', content: 'earlier work' });
});

test('the vendored core matches plugins/weft-harness-common/weft.mjs', async () => {
  const { readFileSync } = await import('node:fs');
  const here = new URL('.', import.meta.url).pathname;
  assert.equal(readFileSync(`${here}../lib/weft.mjs`, 'utf8'), readFileSync(`${here}../../weft-harness-common/weft.mjs`, 'utf8'));
});
