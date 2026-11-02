// Recorded Claude Code hook payloads -> the calls sent to Weft.
//   node --test plugins/weft-claude-code/test/
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { existsSync, readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { test } from 'node:test';
import { fileURLToPath } from 'node:url';
import { eventsPath, flushSession, handleHook } from '../lib/claude.mjs';
import { mergeHooks } from '../install.mjs';
import { loadFixture, recorder, replay } from './replay.mjs';

const here = dirname(fileURLToPath(import.meta.url));
const opBase = (c) => c.op_name;

test('a recorded session becomes one conversation with turns, LLM, tool and sub-agent calls', async () => {
  const fx = loadFixture();
  const { cfg, sid, results } = replay(fx);
  assert.ok(results.every((r) => r.recorded), 'every hook recorded');
  const rec = recorder();
  const res = await flushSession(cfg, sid, { fetchImpl: rec.fetchImpl });
  assert.equal(res.error, undefined);
  assert.equal(res.sent, 2);
  const calls = [...rec.calls().values()];
  for (const c of calls) {
    assert.equal(c.project_id, 'test/claude-code');
    assert.equal(c.thread_id, sid, `${c.op_name} thread`);
    assert.ok(c.started_at && c.ended_at, `${c.op_name} times`);
    assert.equal(c.attributes['gen_ai.conversation.id'], sid);
  }
  const turns = calls.filter((c) => c.op_name === 'claude_code.turn').sort((a, b) => a.started_at.localeCompare(b.started_at));
  assert.equal(turns.length, 2);
  const [t1, t2] = turns;
  assert.equal(t1.turn_id, t1.id);
  assert.equal(t1.parent_id, null);
  assert.equal(t1.trace_id, t1.id);
  assert.equal(t1.attributes['gen_ai.operation.name'], 'invoke_agent');
  assert.equal(t1.attributes['gen_ai.agent.name'], 'claude-code');
  assert.equal(t1.attributes['gen_ai.request.model'], 'claude-sonnet-4-5');
  assert.equal(t1.attributes['gen_ai.conversation.name'], 'List the files and review a.txt');
  assert.equal(t1.attributes.weft.span_kind, 'agent');
  assert.deepEqual(t1.inputs.messages, [{ role: 'user', content: 'List the files and review a.txt' }]);
  assert.equal(t1.output.messages[0].content, 'Done: there are two files and a.txt looks fine.');
  assert.equal(t1.started_at, '2026-11-02T10:00:01.000Z');
  assert.equal(t1.ended_at, '2026-11-02T10:00:06.600Z');
  assert.equal(t1.exception, undefined);

  const inT1 = calls.filter((c) => c.trace_id === t1.id);
  const llm = inT1.filter((c) => c.op_name === 'claude_code.llm');
  assert.equal(llm.length, 5, 'msg_01, msg_02, msg_03 and the sub-agent msg_s1, msg_s2');
  const m1 = llm.find((c) => c.attributes['gen_ai.response.id'] === 'msg_01');
  assert.equal(m1.parent_id, t1.id);
  assert.deepEqual(m1.summary.usage['claude-sonnet-4-5'], { requests: 1, prompt_tokens: 3212, completion_tokens: 40, total_tokens: 3252, cache_read_input_tokens: 3000, cache_creation_input_tokens: 200 });
  assert.deepEqual(m1.output.choices[0].message.content.map((b) => b.type), ['text', 'tool_use'], 'both transcript lines of one message');
  assert.equal(m1.inputs.messages[0].content, 'List the files and review a.txt');

  const bash = inT1.find((c) => c.op_name === 'claude_code.tool.Bash');
  assert.equal(bash.parent_id, t1.id);
  assert.equal(bash.display_name, 'Bash: ls');
  assert.equal(bash.attributes['gen_ai.tool.name'], 'Bash');
  assert.equal(bash.attributes['gen_ai.tool.call.id'], 'toolu_01');
  assert.equal(bash.started_at, '2026-11-02T10:00:02.200Z');
  assert.equal(bash.ended_at, '2026-11-02T10:00:02.500Z');
  assert.equal(bash.output.stdout, 'a.txt\nb.txt');

  const sub = inT1.find((c) => c.op_name === 'claude_code.subagent');
  assert.equal(sub.parent_id, t1.id);
  assert.equal(sub.attributes['gen_ai.agent.name'], 'code-reviewer');
  assert.equal(sub.attributes['gen_ai.operation.name'], 'invoke_agent');
  assert.equal(sub.output.messages[0].content, 'a.txt looks fine.');
  const read = inT1.find((c) => c.op_name === 'claude_code.tool.Read');
  assert.equal(read.parent_id, sub.id, 'sub-agent tool nests under the sub-agent');
  const subLLM = llm.filter((c) => c.parent_id === sub.id);
  assert.equal(subLLM.length, 2);
  assert.equal(subLLM[0].attributes['gen_ai.request.model'], 'claude-haiku-4-5');

  const failed = calls.find((c) => c.op_name === 'claude_code.tool.Bash' && c.trace_id === t2.id);
  assert.match(failed.exception, /Permission denied/);
  assert.equal(t2.exception, undefined);
  assert.equal(t2.output.messages[0].content, 'I could not delete b.txt: permission denied.');

  // SessionEnd: the session's local files are removed once everything is sent.
  assert.equal(existsSync(eventsPath(cfg, sid)), false);
});

test('ids are stable, failures are retried by the next flush, nothing is lost', async () => {
  const fx = loadFixture();
  const { cfg, sid } = replay(fx, {}, { upTo: 22 }); // up to the first Stop + the second prompt
  const down = recorder({ fail: 1 });
  const r1 = await flushSession(cfg, sid, { fetchImpl: down.fetchImpl });
  assert.match(r1.error, /ECONNREFUSED/);
  assert.equal(r1.sent, 0);
  const up = recorder();
  const r2 = await flushSession(cfg, sid, { fetchImpl: up.fetchImpl });
  assert.equal(r2.sent, 1, 'the closed first turn is sent; the open second turn waits');
  const first = up.calls();
  const again = recorder();
  // The first turn is no longer the last one, so it is not re-sent.
  await flushSession(cfg, sid, { fetchImpl: again.fetchImpl });
  assert.equal(again.items.length, 0);
  // A fresh replay of the same session produces the same call ids.
  const other = replay(fx, {}, { upTo: 22 });
  const rec = recorder();
  await flushSession(other.cfg, other.sid, { fetchImpl: rec.fetchImpl });
  assert.deepEqual([...rec.calls().keys()].sort(), [...first.keys()].sort());
});

test('an unfinished turn is closed as interrupted when the next prompt arrives', async () => {
  const fx = loadFixture();
  // Drop the first Stop: the turn is closed by the second prompt.
  fx.steps = fx.steps.filter((s, i) => !(s.hook?.hook_event_name === 'Stop' && i < 25));
  const { cfg, sid } = replay(fx);
  const rec = recorder();
  await flushSession(cfg, sid, { fetchImpl: rec.fetchImpl });
  const turns = [...rec.calls().values()].filter((c) => c.op_name === 'claude_code.turn').sort((a, b) => a.started_at.localeCompare(b.started_at));
  assert.match(turns[0].exception, /interrupted/);
  assert.equal(turns[1].exception, undefined);
});

test('content capture can be turned off; usage and names are kept', async () => {
  const { cfg, sid } = replay(loadFixture(), { WEFT_CAPTURE_CONTENT: 'false', WEFT_AGENT_NAME: 'my-claude' });
  const rec = recorder();
  await flushSession(cfg, sid, { fetchImpl: rec.fetchImpl });
  const calls = [...rec.calls().values()];
  assert.ok(calls.every((c) => Object.keys(c.inputs).length === 0 && c.output === null));
  const turn = calls.find((c) => c.op_name === 'claude_code.turn');
  assert.equal(turn.attributes['gen_ai.agent.name'], 'my-claude');
  assert.ok(calls.some((c) => c.summary?.usage));
});

test('hooks do nothing without a project, and never throw on bad input', () => {
  const r = handleHook({ hook_event_name: 'PreToolUse', session_id: 's' }, { env: { WEFT_CLAUDE_CODE_STATE_DIR: '/nonexistent/x' }, flush: 'off' });
  assert.equal(r.recorded, false);
  assert.equal(handleHook(null, { env: { WEFT_PROJECT: 'p' }, flush: 'off' }).recorded, false);
  assert.equal(handleHook('garbage', { env: { WEFT_PROJECT: 'p' }, flush: 'off' }).recorded, false);
});

test('the hook process is silent, fast and exits 0 even when the server is down', () => {
  const fx = loadFixture();
  const { env, dir } = replay(fx, { WEFT_SERVER_URL: 'http://127.0.0.1:9', WEFT_HOOK_TIMEOUT_MS: '300' }, { upTo: 1 });
  const hook = join(here, '..', 'hooks', 'weft-hook.mjs');
  const payload = { ...fx.steps[1].hook, transcript_path: join(dir, 'transcript.jsonl') };
  const t0 = Date.now();
  const p = spawnSync(process.execPath, [hook], { input: JSON.stringify(payload), env: { ...process.env, ...env, WEFT_FLUSH_MODE: 'off' }, encoding: 'utf8', timeout: 10000 });
  assert.equal(p.status, 0);
  assert.equal(p.stdout, '');
  assert.ok(Date.now() - t0 < 3000);
  const stop = { ...fx.steps[1].hook, hook_event_name: 'Stop' };
  const q = spawnSync(process.execPath, [hook], { input: JSON.stringify(stop), env: { ...process.env, ...env, WEFT_FLUSH_MODE: 'inline' }, encoding: 'utf8', timeout: 10000 });
  assert.equal(q.status, 0, q.stderr);
  assert.equal(q.stdout, '');
  const log = readFileSync(join(env.WEFT_CLAUDE_CODE_STATE_DIR, 'logs', 'flush.log'), 'utf8');
  assert.match(log, /send failed, will retry/);
  const bad = spawnSync(process.execPath, [hook], { input: '{not json', env: { ...process.env, ...env }, encoding: 'utf8', timeout: 10000 });
  assert.equal(bad.status, 0);
});

test('installer merges hooks idempotently and uninstalls cleanly', () => {
  const existing = { model: 'opus', hooks: { PreToolUse: [{ matcher: 'Bash', hooks: [{ type: 'command', command: '~/.agentshield/agentshield-hook.sh' }] }] } };
  const once = mergeHooks(existing, 'node "/x/hooks/weft-hook.mjs"');
  const twice = mergeHooks(once, 'node "/x/hooks/weft-hook.mjs"');
  assert.deepEqual(twice, once);
  assert.equal(once.model, 'opus');
  assert.equal(once.hooks.PreToolUse.length, 2);
  assert.equal(once.hooks.PreToolUse[1].matcher, '*');
  assert.ok(once.hooks.SessionEnd[0].hooks[0].command.includes('weft-hook.mjs'));
  const gone = mergeHooks(twice, '', { uninstall: true });
  assert.deepEqual(gone, existing);
});

test('the vendored core matches plugins/weft-harness-common/weft.mjs', () => {
  const canon = readFileSync(join(here, '..', '..', 'weft-harness-common', 'weft.mjs'), 'utf8');
  for (const p of ['weft-claude-code', 'weft-codex', 'weft-openclaw']) {
    const f = join(here, '..', '..', p, 'lib', 'weft.mjs');
    assert.equal(readFileSync(f, 'utf8'), canon, `${p}/lib/weft.mjs is stale: run node plugins/weft-harness-common/sync.mjs`);
  }
});
