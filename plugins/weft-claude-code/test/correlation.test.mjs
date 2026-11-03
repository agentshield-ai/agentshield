// Correlation with AgentShield (docs/weft/DESIGN.md §5.3): for the same hook
// payloads, the AgentShield Claude Code hook (plugins/claude) sends session_id
// and tool_call_id equal to the gen_ai.conversation.id and gen_ai.tool.call.id
// this plugin records on the tool spans. The real AgentShield hook runs against
// a local stand-in for the engine.
import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import { createServer } from 'node:http';
import { mkdtempSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { test } from 'node:test';
import { fileURLToPath } from 'node:url';
import { flushSession } from '../lib/claude.mjs';
import { loadFixture, recorder, replay } from './replay.mjs';

const here = dirname(fileURLToPath(import.meta.url));
const HOOK = join(here, '..', '..', 'claude', 'agentshield-hook.sh');
const have = (cmd) => spawnSync('sh', ['-c', `command -v ${cmd}`]).status === 0;

/** A stand-in AgentShield engine recording /api/v1/evaluate requests. */
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

/** Run the AgentShield hook on one payload (asynchronously: the stand-in engine runs in this process). */
function runHook(payload, env) {
  return new Promise((resolve) => {
    const child = spawn('bash', [HOOK], { env: { ...process.env, ...env }, stdio: ['pipe', 'pipe', 'pipe'] });
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

test('the AgentShield hook sends the session and tool-use ids this plugin records', { skip: !(have('jq') && have('curl') && have('bash')) && 'needs bash, jq and curl' }, async () => {
  const fx = loadFixture();
  const { cfg, sid, files } = replay(fx);
  const rec = recorder();
  await flushSession(cfg, sid, { fetchImpl: rec.fetchImpl });
  const toolSpans = [...rec.calls().values()].filter((c) => c.attributes['gen_ai.tool.call.id']);

  const eng = await engine();
  try {
    const env = { AGENTSHIELD_URL: eng.url, AGENTSHIELD_STATE_DIR: mkdtempSync(join(tmpdir(), 'as-claude-')), AGENTSHIELD_TIMEOUT_MS: '5000', AGENTSHIELD_SKIP: '', AGENTSHIELD_INTERCEPT: '' };
    const pre = fx.steps.filter((s) => s.hook?.hook_event_name === 'PreToolUse').map((s) => JSON.parse(JSON.stringify(s.hook).replaceAll('{{TRANSCRIPT}}', files.main)));
    assert.ok(pre.length >= 3);
    for (const p of pre) await runHook(p, env);
    assert.ok(eng.requests.length >= 2, `AgentShield evaluated ${eng.requests.length} tool calls`);
    let subagentTool = false;
    for (const req of eng.requests) {
      assert.ok(req.tool_call_id, 'AgentShield sends the tool-use id');
      const span = toolSpans.find((c) => c.attributes['gen_ai.tool.call.id'] === req.tool_call_id);
      assert.ok(span, `a Weft span for ${req.tool_call_id}`);
      assert.equal(span.attributes['gen_ai.conversation.id'], req.session_id, 'session id = gen_ai.conversation.id');
      assert.equal(span.thread_id, req.session_id);
      const payload = pre.find((p) => p.tool_use_id === req.tool_call_id);
      assert.equal(req.session_id, payload.session_id);
      if (payload.agent_id) subagentTool = true;
    }
    assert.ok(subagentTool, 'a sub-agent tool call (same Claude session id) is covered');
  } finally {
    await eng.close();
  }
});
