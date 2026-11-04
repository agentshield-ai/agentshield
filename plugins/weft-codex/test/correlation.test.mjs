// Correlation with AgentShield (docs/weft/DESIGN.md §5.3): for the PreToolUse
// payloads Codex sends for the tool calls of a rollout, the AgentShield Codex
// hook (plugins/codex) sends session_id and tool_call_id equal to the
// gen_ai.conversation.id and gen_ai.tool.call.id this plugin records on the
// tool spans. The real AgentShield hook runs against a local stand-in engine.
import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import { createServer } from 'node:http';
import { dirname, join } from 'node:path';
import { test } from 'node:test';
import { fileURLToPath } from 'node:url';
import { flushRollout } from '../lib/codex.mjs';
import { readJsonl } from '../lib/weft.mjs';
import { recorder, setup } from './helpers.mjs';

const here = dirname(fileURLToPath(import.meta.url));
const HOOK = join(here, '..', '..', 'codex', 'agentshield_hook.py');
const PY = ['python3', 'python'].find((p) => spawnSync(p, ['--version']).status === 0);

async function engine() {
  const requests = [];
  const srv = createServer((req, res) => {
    let body = '';
    req.on('data', (c) => (body += c));
    req.on('end', () => {
      if (req.url.endsWith('/evaluate')) requests.push(JSON.parse(body));
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({ action: 'allow', mode: 'audit', alerts: [] }));
    });
  });
  await new Promise((r) => srv.listen(0, '127.0.0.1', r));
  return { url: `http://127.0.0.1:${srv.address().port}`, requests, close: () => new Promise((r) => srv.close(r)) };
}

function runHook(payload, env) {
  return new Promise((resolve) => {
    const child = spawn(PY, [HOOK], { env: { ...process.env, ...env }, stdio: ['pipe', 'pipe', 'pipe'] });
    let out = '';
    child.stdout.on('data', (d) => (out += d));
    child.stderr.on('data', (d) => (out += d));
    const timer = setTimeout(() => child.kill(), 20000);
    child.on('close', (code) => {
      clearTimeout(timer);
      resolve({ code, out });
    });
    child.stdin.end(JSON.stringify(payload));
  });
}

test('the AgentShield Codex hook sends the session and tool-call ids this plugin records', { skip: !PY && 'needs python3' }, async () => {
  const s = setup();
  const rec = recorder();
  const res = await flushRollout(s.cfg, s.rollout, { sessionId: s.stop.session_id, fetchImpl: rec.fetchImpl });
  assert.equal(res.error, undefined);
  const toolSpans = [...rec.calls().values()].filter((c) => c.attributes['gen_ai.tool.call.id']);
  assert.ok(toolSpans.length >= 2);

  // The PreToolUse payload Codex sends for each function call of the rollout.
  const calls = readJsonl(s.rollout).lines.filter((l) => l.type === 'response_item' && ['function_call', 'custom_tool_call'].includes(l.payload?.type));
  const payloads = calls.map((l) => ({
    session_id: s.stop.session_id,
    transcript_path: s.rollout,
    cwd: '/home/dev/app',
    hook_event_name: 'PreToolUse',
    model: 'gpt-5-codex',
    tool_name: l.payload.name,
    tool_input: l.payload.type === 'function_call' ? JSON.parse(l.payload.arguments) : { input: l.payload.input },
    tool_use_id: l.payload.call_id,
  }));
  const eng = await engine();
  try {
    const env = { AGENTSHIELD_URL: eng.url, AGENTSHIELD_TIMEOUT_MS: '5000', AGENTSHIELD_ENABLED: 'true', HOME: s.dir };
    for (const p of payloads) await runHook(p, env);
    assert.ok(eng.requests.length >= 2, `AgentShield evaluated ${eng.requests.length} tool calls`);
    for (const req of eng.requests) {
      assert.ok(req.tool_call_id, 'AgentShield sends the tool-call id');
      const span = toolSpans.find((c) => c.attributes['gen_ai.tool.call.id'] === req.tool_call_id);
      assert.ok(span, `a Weft tool span for ${req.tool_call_id}`);
      assert.equal(span.attributes['gen_ai.conversation.id'], req.session_id, 'session id = gen_ai.conversation.id');
      assert.equal(span.session_id, req.session_id);
    }
  } finally {
    await eng.close();
  }
});
