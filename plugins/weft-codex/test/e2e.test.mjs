// End to end against a running Weft server (skipped without WEFT_TEST_SERVER_URL):
//   WEFT_TEST_SERVER_URL=http://127.0.0.1:6345 node --test plugins/weft-codex/test/*.test.mjs
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { randomUUID } from 'node:crypto';
import { dirname, join } from 'node:path';
import { test } from 'node:test';
import { fileURLToPath } from 'node:url';
import { setup } from './helpers.mjs';

const URL = process.env.WEFT_TEST_SERVER_URL;
const here = dirname(fileURLToPath(import.meta.url));

async function post(path, body) {
  const headers = { 'Content-Type': 'application/json' };
  if (process.env.WEFT_TEST_API_KEY) headers.Authorization = `Bearer ${process.env.WEFT_TEST_API_KEY}`;
  const r = await fetch(`${URL}${path}`, { method: 'POST', headers, body: JSON.stringify(body) });
  assert.ok(r.ok, `${path} -> ${r.status}`);
  return r.json();
}

async function waitFor(fn, ms = 15000) {
  const end = Date.now() + ms;
  for (;;) {
    const v = await fn();
    if (v || Date.now() > end) return v;
    await new Promise((r) => setTimeout(r, 200));
  }
}

test('a Codex session (Stop hook, background collector) shows up in /agents/conversations/chat', { skip: !URL && 'set WEFT_TEST_SERVER_URL' }, async () => {
  const sid = randomUUID();
  const project = `it/codex-${sid.slice(0, 8)}`;
  const { env, stop } = setup({ sessionId: sid, env: { WEFT_PROJECT: project, WEFT_SERVER_URL: URL, WEFT_API_KEY: process.env.WEFT_TEST_API_KEY ?? '', WEFT_ROLLOUT_DELAY_MS: '0' } });
  const hook = join(here, '..', 'hooks', 'weft-codex-hook.mjs');
  const runEnv = { ...process.env, ...env };
  delete runEnv.WEFT_FLUSH_MODE;
  const t0 = Date.now();
  const p = spawnSync(process.execPath, [hook], { input: JSON.stringify(stop), env: runEnv, encoding: 'utf8' });
  assert.equal(p.status, 0);
  assert.equal(p.stdout, '');
  assert.ok(Date.now() - t0 < 2000, 'the hook returns at once');
  const chat = await waitFor(async () => {
    const r = await post('/agents/conversations/chat', { project_id: project, conversation_id: sid });
    return r.total_turns === 2 ? r : null;
  });
  assert.ok(chat, 'conversation delivered');
  assert.deepEqual(chat.agent_names, ['codex']);
  const [t1, t2] = chat.turns;
  assert.equal(t1.messages[0].user_message.text, 'Fix the failing test in utils');
  const tools = t1.messages.filter((m) => m.type === 'tool_call').map((m) => m.tool_call.tool_name);
  assert.deepEqual(tools, ['shell', 'apply_patch', 'docs__search']);
  assert.ok(t1.messages.some((m) => m.assistant_message?.text?.startsWith('Fixed the off-by-one')));
  assert.equal(t2.messages[0].user_message.text, 'Commit it');
  assert.equal(chat.total_input_tokens, 5000 + 5400 + 5600 + 5800 + 6000 + 6200);
  assert.ok(chat.error_count >= 1);
  const agents = await post('/agents/query', { project_id: project });
  const a = agents.agents.find((x) => x.agent_name === 'codex');
  assert.equal(a.invocation_count, 2);
  assert.equal(a.llm_call_count, 6);
  assert.equal(a.tool_call_count, 4);
});
