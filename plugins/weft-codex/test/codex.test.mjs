// Recorded Codex rollout + Stop/notify payloads -> the calls sent to Weft.
//   node --test plugins/weft-codex/test/*.test.mjs
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { appendFileSync, readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { test } from 'node:test';
import { fileURLToPath } from 'node:url';
import { findRollout, flushRollout, handleTrigger, parseRollout } from '../lib/codex.mjs';
import { readJsonl } from '../lib/weft.mjs';
import { mergeStopHook, tomlSnippet } from '../install.mjs';
import { recorder, setup } from './helpers.mjs';

const here = dirname(fileURLToPath(import.meta.url));

test('the rollout parses into turns, model responses and tool calls', () => {
  const { rollout } = setup();
  const { session, turns } = parseRollout(readJsonl(rollout).lines);
  assert.equal(session.cliVersion, '0.58.0');
  assert.equal(turns.length, 2);
  const [t1, t2] = turns;
  assert.equal(t1.prompt, 'Fix the failing test in utils');
  assert.equal(t1.closed, 'complete');
  assert.equal(t1.model, 'gpt-5-codex');
  assert.equal(t1.llms.length, 4, 'four model responses, the duplicate token_count ignored');
  assert.deepEqual(t1.toolOrder, ['call_A1', 'call_A2', 'call_A3']);
  assert.equal(t1.tools.get('call_A3').mcpServer, 'docs');
  assert.equal(t2.llms.length, 2);
  assert.match(t2.tools.get('call_B1').error, /sandbox denied/);
});

test('a Stop hook sends one conversation with turn, LLM and tool calls', async () => {
  const { cfg, rollout, sid } = setup();
  const rec = recorder();
  const res = await flushRollout(cfg, rollout, { fetchImpl: rec.fetchImpl });
  assert.equal(res.error, undefined);
  assert.equal(res.sent, 2);
  const calls = [...rec.calls().values()];
  for (const c of calls) {
    assert.equal(c.session_id, sid);
    assert.equal(c.attributes['gen_ai.conversation.id'], sid);
  }
  const turns = calls.filter((c) => c.name === 'codex.turn').sort((a, b) => a.started_at.localeCompare(b.started_at));
  const [t1, t2] = turns;
  assert.equal(t1.turn_id, t1.id);
  assert.equal(t1.attributes['gen_ai.agent.name'], 'codex');
  assert.equal(t1.attributes['gen_ai.agent.version'], '0.58.0');
  assert.equal(t1.attributes['gen_ai.request.model'], 'gpt-5-codex');
  assert.equal(t1.attributes['gen_ai.conversation.name'], 'Fix the failing test in utils');
  assert.deepEqual(t1.inputs.messages, [{ role: 'user', content: 'Fix the failing test in utils' }]);
  assert.equal(t1.output.messages[0].content, 'Fixed the off-by-one in utils.js; the test passes now.');
  assert.equal(t1.started_at, '2026-11-02T11:00:01.010Z');
  assert.equal(t1.ended_at, '2026-11-02T11:00:10.200Z');

  const llms = calls.filter((c) => c.name === 'codex.llm' && c.parent_id === t1.id).sort((a, b) => a.started_at.localeCompare(b.started_at));
  assert.equal(llms.length, 4);
  assert.deepEqual(llms[0].summary.usage['gpt-5-codex'], { requests: 1, prompt_tokens: 5000, completion_tokens: 120, total_tokens: 5120, cache_read_input_tokens: 4096, reasoning_tokens: 64 });
  const blocks = llms[0].output.choices[0].message.content;
  assert.deepEqual(blocks.map((b) => b.type), ['reasoning', 'tool_use']);
  assert.deepEqual(blocks[0].summary, [{ type: 'summary_text', text: '**Running the tests** to see the failure' }], 'a reasoning summary (kind=summary)');
  assert.equal(blocks[1].name, 'shell');
  assert.equal(llms[0].started_at, '2026-11-02T11:00:01.010Z');
  assert.equal(llms[0].ended_at, '2026-11-02T11:00:03.200Z');
  assert.equal(llms[1].started_at, '2026-11-02T11:00:05.000Z', 'starts when the previous tool result arrived');
  assert.equal(llms[3].output.choices[0].message.content[0].text, 'Fixed the off-by-one in utils.js; the test passes now.');

  const shell = calls.find((c) => c.attributes['gen_ai.tool.call.id'] === 'call_A1');
  assert.equal(shell.name, 'codex.tool.shell');
  assert.equal(shell.parent_id, t1.id);
  assert.equal(shell.display_name, 'shell: bash -lc npm test -- utils');
  assert.equal(shell.output.metadata.exit_code, 1);
  assert.equal(shell.attributes.codex.exit_code, 1);
  assert.equal(shell.started_at, '2026-11-02T11:00:03.100Z');
  assert.equal(shell.ended_at, '2026-11-02T11:00:05.000Z');
  const patch = calls.find((c) => c.attributes['gen_ai.tool.call.id'] === 'call_A2');
  assert.equal(patch.attributes['gen_ai.tool.name'], 'apply_patch');
  assert.match(patch.inputs.input, /Update File: utils.js/);
  const mcp = calls.find((c) => c.attributes['gen_ai.tool.call.id'] === 'call_A3');
  assert.equal(mcp.attributes['mcp.server.name'], 'docs');
  assert.equal(mcp.attributes['gen_ai.tool.type'], 'mcp');
  assert.equal(mcp.output, 'see vitest.config.ts');
  const commit = calls.find((c) => c.attributes['gen_ai.tool.call.id'] === 'call_B1');
  assert.equal(commit.parent_id, t2.id);
  assert.match(commit.error, /sandbox denied/);
});

test('incremental flushes: a turn is re-sent with the same ids until the next one starts', async () => {
  const { cfg, rollout, all, split } = setup({ lines: 22 });
  const r1 = recorder();
  await flushRollout(cfg, rollout, { fetchImpl: r1.fetchImpl });
  const first = r1.calls();
  assert.equal([...first.values()].filter((c) => c.name === 'codex.turn').length, 1);
  appendFileSync(rollout, `${all.slice(split).join('\n')}\n`);
  const r2 = recorder();
  const res = await flushRollout(cfg, rollout, { fetchImpl: r2.fetchImpl });
  assert.equal(res.sent, 2);
  const second = r2.calls();
  for (const id of first.keys()) assert.ok(second.has(id), 'same ids for the re-sent turn');
  const r3 = recorder();
  await flushRollout(cfg, rollout, { fetchImpl: r3.fetchImpl });
  assert.equal([...r3.calls().values()].filter((c) => c.name === 'codex.turn').length, 1, 'only the last turn is re-sent');
});

test('a failed send is retried by the next run', async () => {
  const { cfg, rollout } = setup();
  const down = recorder({ fail: 1 });
  const r1 = await flushRollout(cfg, rollout, { fetchImpl: down.fetchImpl });
  assert.match(r1.error, /ECONNREFUSED/);
  const up = recorder();
  const r2 = await flushRollout(cfg, rollout, { fetchImpl: up.fetchImpl });
  assert.equal(r2.sent, 2);
});

test('Stop and notify payloads find the rollout; other payloads are ignored', () => {
  const { env, stop, notify, sid, rollout } = setup();
  assert.equal(findRollout(sid, env), rollout);
  assert.equal(handleTrigger(stop, { env, mode: 'off' }).rollout, rollout);
  assert.equal(handleTrigger({ ...stop, transcript_path: undefined }, { env, mode: 'off' }).rollout, rollout);
  assert.equal(handleTrigger(notify, { env, mode: 'off' }).rollout, rollout);
  assert.equal(handleTrigger({ ...stop, hook_event_name: 'PreToolUse' }, { env, mode: 'off' }).started, false);
  assert.equal(handleTrigger({ type: 'other' }, { env, mode: 'off' }).started, false);
  assert.equal(handleTrigger(stop, { env: { ...env, WEFT_PROJECT: '' }, mode: 'off' }).started, false);
});

test('the hook and notify processes are silent and exit 0 when the server is down', () => {
  const { env, stop, notify } = setup({ env: { WEFT_SERVER_URL: 'http://127.0.0.1:9', WEFT_HOOK_TIMEOUT_MS: '300' } });
  const hook = join(here, '..', 'hooks', 'weft-codex-hook.mjs');
  const base = { ...process.env, ...env, WEFT_FLUSH_MODE: 'inline' };
  const t0 = Date.now();
  const p = spawnSync(process.execPath, [hook], { input: JSON.stringify(stop), env: base, encoding: 'utf8', timeout: 10000 });
  assert.equal(p.status, 0, p.stderr);
  assert.equal(p.stdout, '');
  assert.ok(Date.now() - t0 < 5000);
  const n = spawnSync(process.execPath, [hook, JSON.stringify(notify)], { env: { ...base, WEFT_FLUSH_MODE: 'off' }, encoding: 'utf8', timeout: 10000 });
  assert.equal(n.status, 0);
  assert.equal(n.stdout, '');
  const log = readFileSync(join(env.WEFT_CODEX_STATE_DIR, 'logs', 'collector.log'), 'utf8');
  assert.match(log, /send failed/);
});

test('installer merges the Stop hook idempotently', () => {
  const doc = { hooks: { Stop: [{ hooks: [{ type: 'command', command: 'python3 agentshield_hook.py' }] }], PreToolUse: [{ matcher: '.*', hooks: [{ type: 'command', command: 'x' }] }] } };
  const once = mergeStopHook(doc, 'node "/p/hooks/weft-codex-hook.mjs"');
  assert.deepEqual(mergeStopHook(once, 'node "/p/hooks/weft-codex-hook.mjs"'), once);
  assert.equal(once.hooks.Stop.length, 2);
  assert.deepEqual(mergeStopHook(once, '', { uninstall: true }), doc);
  assert.match(tomlSnippet('/p/h.mjs'), /\[\[hooks\.Stop\]\][\s\S]*notify = \["node", "\/p\/h\.mjs"\]/);
});
