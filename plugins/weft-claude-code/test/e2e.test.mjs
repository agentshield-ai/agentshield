// End to end against a running Weft server (skipped without WEFT_TEST_SERVER_URL):
//   WEFT_TEST_SERVER_URL=http://127.0.0.1:6345 node --test plugins/weft-claude-code/test/*.test.mjs
import assert from 'node:assert/strict';
import { randomUUID } from 'node:crypto';
import { test } from 'node:test';
import { flushSession } from '../lib/claude.mjs';
import { loadFixture, replay } from './replay.mjs';

const URL = process.env.WEFT_TEST_SERVER_URL;

async function post(path, body) {
  const headers = { 'Content-Type': 'application/json' };
  if (process.env.WEFT_TEST_API_KEY) headers.Authorization = `Bearer ${process.env.WEFT_TEST_API_KEY}`;
  const r = await fetch(`${URL}${path}`, { method: 'POST', headers, body: JSON.stringify(body) });
  assert.ok(r.ok, `${path} -> ${r.status}`);
  return r.json();
}

async function waitFor(fn, ms = 10000) {
  const end = Date.now() + ms;
  for (;;) {
    const v = await fn();
    if (v || Date.now() > end) return v;
    await new Promise((r) => setTimeout(r, 200));
  }
}

test('a Claude Code session shows up in /agents/conversations/chat', { skip: !URL && 'set WEFT_TEST_SERVER_URL' }, async () => {
  const sid = randomUUID();
  const project = `it/claude-code-${sid.slice(0, 8)}`;
  const { cfg } = replay(loadFixture('session.json', sid), { WEFT_PROJECT: project, WEFT_SERVER_URL: URL, WEFT_API_KEY: process.env.WEFT_TEST_API_KEY ?? '' });
  const res = await flushSession(cfg, sid);
  assert.equal(res.error, undefined);
  assert.equal(res.sent, 2);
  const chat = await waitFor(async () => {
    const r = await post('/agents/conversations/chat', { project_id: project, conversation_id: sid });
    return r.total_turns === 2 ? r : null;
  });
  assert.ok(chat, 'conversation indexed');
  assert.ok(chat.agent_names.includes('claude-code'));
  assert.equal(chat.conversation_name, 'List the files and review a.txt');
  const [t1, t2] = chat.turns;
  assert.equal(t1.messages[0].user_message.text, 'List the files and review a.txt');
  const toolNames = t1.messages.filter((m) => m.type === 'tool_call').map((m) => m.tool_call.tool_name);
  assert.ok(toolNames.includes('Bash') && toolNames.includes('Read'), JSON.stringify(toolNames));
  assert.ok(t1.messages.some((m) => m.type === 'agent_start'), 'sub-agent start');
  assert.ok(t1.messages.some((m) => m.assistant_message?.text === 'Done: there are two files and a.txt looks fine.'));
  assert.equal(t2.messages[0].user_message.text, 'Now delete b.txt');
  assert.ok(chat.total_input_tokens > 0);
  assert.ok(chat.error_count >= 1, 'the failed rm is an error span');
  const agents = await post('/agents/query', { project_id: project });
  const cc = agents.agents.find((a) => a.agent_name === 'claude-code');
  assert.equal(cc.invocation_count, 2);
  assert.equal(cc.tool_call_count, 2, 'Bash twice; Read belongs to the sub-agent');
  assert.equal(cc.llm_call_count, 5);
  const sub = agents.agents.find((a) => a.agent_name === 'code-reviewer');
  assert.equal(sub.tool_call_count, 1);
  assert.equal(sub.llm_call_count, 2);
  // Re-sending is idempotent.
  await flushSession(cfg, sid);
  const again = await post('/agents/conversations/chat', { project_id: project, conversation_id: sid });
  assert.equal(again.total_turns, 2);
});

test('the real hook processes (background flush) deliver a session', { skip: !URL && 'set WEFT_TEST_SERVER_URL' }, async () => {
  const { spawnSync } = await import('node:child_process');
  const { appendFileSync, mkdtempSync } = await import('node:fs');
  const { tmpdir } = await import('node:os');
  const { join, dirname } = await import('node:path');
  const { fileURLToPath } = await import('node:url');
  const sid = randomUUID();
  const project = `it/claude-code-hooks-${sid.slice(0, 8)}`;
  const fx = loadFixture('session.json', sid);
  const dir = mkdtempSync(join(tmpdir(), 'weft-cc-e2e-'));
  const files = { main: join(dir, 'transcript.jsonl'), agent: join(dir, 'agent.jsonl') };
  const hook = join(dirname(fileURLToPath(import.meta.url)), '..', 'hooks', 'weft-hook.mjs');
  const env = { ...process.env, WEFT_PROJECT: project, WEFT_SERVER_URL: URL, WEFT_API_KEY: process.env.WEFT_TEST_API_KEY ?? '', WEFT_CLAUDE_CODE_STATE_DIR: join(dir, 'state'), WEFT_TRANSCRIPT_DELAY_MS: '0' };
  delete env.WEFT_FLUSH_MODE;
  let slowest = 0;
  for (const s of fx.steps) {
    if (s.transcript) {
      appendFileSync(files[s.transcript], `${JSON.stringify(s.line)}\n`);
      continue;
    }
    const payload = JSON.parse(JSON.stringify(s.hook).replaceAll('{{TRANSCRIPT}}', files.main).replaceAll('{{AGENT_TRANSCRIPT}}', files.agent));
    const t0 = Date.now();
    const p = spawnSync(process.execPath, [hook], { input: JSON.stringify(payload), env, encoding: 'utf8' });
    slowest = Math.max(slowest, Date.now() - t0);
    assert.equal(p.status, 0);
    assert.equal(p.stdout, '');
  }
  assert.ok(slowest < 2000, `hook took ${slowest} ms`);
  const chat = await waitFor(async () => {
    const r = await post('/agents/conversations/chat', { project_id: project, conversation_id: sid });
    return r.total_turns === 2 ? r : null;
  }, 15000);
  assert.ok(chat, 'conversation delivered by the background worker');
  assert.equal(chat.turns[1].messages[0].user_message.text, 'Now delete b.txt');
});
