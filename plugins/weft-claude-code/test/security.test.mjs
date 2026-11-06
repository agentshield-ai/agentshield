// Security behaviour of the Claude Code plugin and the shared core (plugins/weft-harness-common):
// content capture off, private local state, redirects, plain-http warning, installer hardening.
//   node --test plugins/weft-claude-code/test/
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { cpSync, existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, statSync, utimesSync, writeFileSync } from 'node:fs';
import { createServer } from 'node:http';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { test } from 'node:test';
import { fileURLToPath } from 'node:url';
import { eventsPath, flushSession, hookRecord } from '../lib/claude.mjs';
import { hookCommand, plainHttpKeyWarning, sendItems, shellQuote, spanItems } from '../lib/weft.mjs';
import { loadFixture, replay } from './replay.mjs';

const here = dirname(fileURLToPath(import.meta.url));
const pluginDir = join(here, '..');
const posix = process.platform !== 'win32';

/** Every string the user, the model or a tool wrote in the fixture (plus local paths). */
function plantedStrings(fx, files) {
  const out = new Set();
  const walk = (v) => {
    if (typeof v === 'string') {
      if (v.length >= 4) out.add(v.split('\n')[0]);
    } else if (Array.isArray(v)) v.forEach(walk);
    else if (v && typeof v === 'object') for (const [k, x] of Object.entries(v)) if (!['type', 'id', 'tool_use_id', 'name', 'role', 'subagent_type'].includes(k)) walk(x);
  };
  for (const s of fx.steps) {
    if (s.hook) for (const k of ['prompt', 'tool_input', 'tool_response', 'error', 'last_assistant_message', 'cwd']) walk(s.hook[k]);
    if (s.transcript) walk(s.line.message?.content);
  }
  out.add(files.main);
  return [...out].filter((x) => x.trim().length >= 4);
}

function rawRecorder() {
  const bodies = [];
  const fetchImpl = async (url, init) => {
    bodies.push(String(init.body));
    return new Response('{"items":[]}', { status: 200 });
  };
  return { bodies, fetchImpl };
}

test('F3: capture_content=false sends none of the planted content (display names, conversation name, errors, paths)', async () => {
  const fx = loadFixture();
  const { cfg, sid, files } = replay(fx, { WEFT_CAPTURE_CONTENT: 'false' });
  const rec = rawRecorder();
  const res = await flushSession(cfg, sid, { fetchImpl: rec.fetchImpl });
  assert.equal(res.error, undefined);
  assert.ok(res.sent >= 2);
  const raw = rec.bodies.join('\n');
  const planted = plantedStrings(fx, files);
  assert.ok(planted.includes('List the files and review a.txt') && planted.some((p) => p.includes('Permission denied')), 'fixture strings found');
  for (const p of planted) assert.ok(!raw.includes(p), `content leaked: ${JSON.stringify(p)}`);
  for (const p of ['/home/dev/project', 'transcript_path', 'gen_ai.conversation.name']) assert.ok(!raw.includes(p), p);
  // spans are still useful: names, kinds, models, ids, usage, an error class
  const spans = rec.bodies.flatMap((b) => JSON.parse(b).items.map((i) => i.span));
  const bash = spans.filter((s) => s.name === 'claude_code.tool.Bash');
  assert.deepEqual(bash.map((s) => s.display_name), ['Bash', 'Bash']);
  assert.deepEqual(bash.map((s) => s.error ?? null).sort(), ['tool failed', null].sort());
  assert.ok(spans.some((s) => s.name === 'claude_code.subagent' && s.display_name === 'invoke_agent code-reviewer'));
  assert.ok(spans.some((s) => s.name === 'claude_code.llm' && s.display_name === 'chat claude-sonnet-4-5' && s.summary.usage));
  for (const s of spans) {
    assert.deepEqual(s.inputs, {});
    assert.equal(s.output, null);
  }
});

test('F3: the same session with capture on still carries content (control)', async () => {
  const fx = loadFixture();
  const { cfg, sid } = replay(fx);
  const rec = rawRecorder();
  await flushSession(cfg, sid, { fetchImpl: rec.fetchImpl });
  const raw = rec.bodies.join('\n');
  assert.ok(raw.includes('List the files and review a.txt'));
  assert.ok(raw.includes('gen_ai.conversation.name'));
});

test('F3: spanItems with capture off uses safe display names, error classes and drops path attributes', () => {
  const cfg = { plugin: 'p', project: 'e/p', captureContent: false, maxFieldChars: 100 };
  const [{ span }] = spanItems(cfg, {
    id: 'i', traceId: 't', name: 'x.tool.Bash', kind: 'tool', displayName: 'Bash: cat /etc/secret', inputs: { command: 'cat /etc/secret' }, output: 'SECRET',
    error: 'cat: /etc/secret: denied', errorKind: 'tool failed',
    genai: { 'gen_ai.tool.name': 'Bash', 'gen_ai.conversation.name': 'first prompt SECRET' },
    attributes: { harness: { cwd: '/home/me/secret-project', transcript_path: '/home/me/.t.jsonl', exit_code: 1 } },
  });
  assert.equal(span.display_name, 'Bash');
  assert.equal(span.error, 'tool failed');
  assert.deepEqual(span.attributes.harness, { exit_code: 1 });
  assert.ok(!JSON.stringify(span).includes('secret') && !JSON.stringify(span).includes('SECRET'));
  const [{ span: llm }] = spanItems(cfg, { id: 'l', traceId: 't', name: 'x.llm', kind: 'llm', displayName: 'Sure, the password is hunter2', error: 'model said hunter2', genai: { 'gen_ai.request.model': 'm1' } });
  assert.equal(llm.display_name, 'chat m1');
  assert.equal(llm.error, 'llm error');
});

test('F7: local state is private (0700 dirs, 0600 files) and holds no content when capture is off', { skip: !posix }, async () => {
  const fx = loadFixture();
  const firstStop = fx.steps.findIndex((s) => s.hook?.hook_event_name === 'Stop');
  const { cfg, sid, files } = replay(fx, { WEFT_CAPTURE_CONTENT: '0' }, { upTo: firstStop + 1 });
  const ev = eventsPath(cfg, sid);
  for (const d of [cfg.stateDir, dirname(ev)]) assert.equal(statSync(d).mode & 0o777, 0o700, d);
  assert.equal(statSync(ev).mode & 0o777, 0o600);
  const disk = readFileSync(ev, 'utf8');
  for (const p of plantedStrings(fx, files)) if (p !== files.main) assert.ok(!disk.includes(p), `on disk: ${JSON.stringify(p)}`);
  // a failed send writes the cursor; logs are private too
  const res = await flushSession(cfg, sid, { fetchImpl: async () => new Response('no', { status: 500 }) });
  assert.ok(res.error || res.sent === 0);
  for (const f of readdirSync(dirname(ev))) assert.equal(statSync(join(dirname(ev), f)).mode & 0o077, 0, f);
  const logs = join(cfg.stateDir, 'logs');
  assert.equal(statSync(logs).mode & 0o777, 0o700);
  for (const f of readdirSync(logs)) assert.equal(statSync(join(logs, f)).mode & 0o777, 0o600, f);
  const cursor = readFileSync(join(dirname(ev), readdirSync(dirname(ev)).find((f) => f.endsWith('.cursor.json')) ?? 'none'), 'utf8');
  assert.ok(!cursor.includes('List the files'), 'no first prompt in the cursor');
});

test('F7: hookRecord with capture off keeps ids and timings only', () => {
  const rec = hookRecord(
    { hook_event_name: 'PostToolUseFailure', session_id: 's', cwd: '/home/me/p', tool_name: 'Task', tool_use_id: 't1', tool_input: { prompt: 'secret task', subagent_type: 'reviewer' }, tool_response: 'out', error: 'boom secret', is_interrupt: false, last_assistant_message: 'hi' },
    0,
    { captureContent: false },
  );
  assert.deepEqual(rec, { ev: 'PostToolUseFailure', ts: new Date(0).toISOString(), session_id: 's', tool_name: 'Task', tool_use_id: 't1', tool_input: { subagent_type: 'reviewer' }, error: 'tool failed', is_interrupt: false });
});

test('F7: session logs of sessions that never ended are deleted after the retention period; existing dirs are tightened', { skip: !posix }, async () => {
  const fx = loadFixture();
  const { cfg, sid } = replay(fx, { WEFT_STATE_RETENTION_DAYS: '3' });
  const dir = dirname(eventsPath(cfg, sid));
  const { chmodSync } = await import('node:fs');
  chmodSync(dir, 0o755); // created by an older version
  const old = join(dir, 'crashed-session.events.jsonl');
  const fresh = join(dir, 'recent-session.events.jsonl');
  writeFileSync(old, '{"ev":"UserPromptSubmit","prompt":"old secret"}\n');
  writeFileSync(fresh, '{"ev":"UserPromptSubmit"}\n');
  const tenDaysAgo = (Date.now() - 10 * 86_400_000) / 1000;
  utimesSync(old, tenDaysAgo, tenDaysAgo);
  await flushSession(cfg, sid, { fetchImpl: rawRecorder().fetchImpl });
  assert.ok(!existsSync(old), 'old log deleted');
  assert.ok(existsSync(fresh), 'recent log kept');
  replay(fx, { WEFT_CLAUDE_CODE_STATE_DIR: cfg.stateDir }, { upTo: 1 }); // the next hook tightens the directory
  assert.equal(statSync(dir).mode & 0o777, 0o700);
});

// ---------------------------------------------------------------- F4 redirects (shared core)

async function server(handler) {
  const seen = [];
  const srv = createServer((req, res) => {
    let body = '';
    req.on('data', (c) => (body += c));
    req.on('end', () => {
      seen.push({ method: req.method, url: req.url, auth: req.headers.authorization, body });
      handler(req, res);
    });
  });
  await new Promise((r) => srv.listen(0, '127.0.0.1', r));
  return { url: `http://127.0.0.1:${srv.address().port}`, seen, close: () => new Promise((r) => srv.close(r)) };
}

test('F4: sendItems never follows a redirect; the target sees nothing', async () => {
  const target = await server((req, res) => res.end('{}'));
  try {
    for (const status of [301, 302, 307, 308]) {
      const redir = await server((req, res) => {
        res.writeHead(status, { Location: `${target.url}${req.url}` });
        res.end();
      });
      try {
        const cfg = { plugin: 'p', serverUrl: redir.url, apiKey: 'PLUGINKEY', project: 'e/p', timeoutMs: 2000 };
        await assert.rejects(sendItems(cfg, [{ span: { id: 'x', secret: 'body' } }]));
        assert.equal(redir.seen.length, 1);
      } finally {
        await redir.close();
      }
    }
    assert.deepEqual(target.seen, []);
  } finally {
    await target.close();
  }
});

test('plain http with an API key to a non-loopback host is warned about (once)', async () => {
  assert.match(plainHttpKeyWarning({ apiKey: 'k', serverUrl: 'http://weft.example.org:6345' }), /plain http/);
  assert.equal(plainHttpKeyWarning({ apiKey: 'k', serverUrl: 'https://weft.example.org' }), null);
  assert.equal(plainHttpKeyWarning({ apiKey: 'k', serverUrl: 'http://127.0.0.1:6345' }), null);
  assert.equal(plainHttpKeyWarning({ apiKey: 'k', serverUrl: 'http://localhost:6345' }), null);
  assert.equal(plainHttpKeyWarning({ apiKey: '', serverUrl: 'http://weft.example.org' }), null);
  const warnings = [];
  const cfg = { plugin: 'p', serverUrl: 'http://weft.example.net', apiKey: 'SECRETKEY', project: 'e/p', timeoutMs: 100 };
  const ok = async () => new Response('{}', { status: 200 });
  await sendItems(cfg, [{ span: {} }], { fetchImpl: ok, warn: (m) => warnings.push(m) });
  await sendItems(cfg, [{ span: {} }], { fetchImpl: ok, warn: (m) => warnings.push(m) });
  assert.equal(warnings.length, 1);
  assert.ok(!warnings[0].includes('SECRETKEY'));
});

// ---------------------------------------------------------------- F8 installer

test('F8: hook paths are quoted, and paths with shell metacharacters are refused', () => {
  assert.equal(hookCommand('/opt/weft plugins/hooks/weft-hook.mjs', 'linux'), "node '/opt/weft plugins/hooks/weft-hook.mjs'");
  assert.equal(hookCommand('C:\\Users\\me\\weft\\hooks\\weft-hook.mjs', 'win32'), 'node "C:\\Users\\me\\weft\\hooks\\weft-hook.mjs"');
  for (const bad of ['/x"$(touch INJECTED)"y/h.mjs', "/x'y/h.mjs", '/x`id`/h.mjs', '/x;rm -rf ~/h.mjs', '/x\ny/h.mjs', '/a&b/h.mjs', '/a|b/h.mjs', '/a$HOME/h.mjs', '/a%PATH%/h.mjs']) {
    assert.throws(() => shellQuote(bad, 'linux'), /refusing/, bad);
  }
});

function installEnv(home) {
  return { ...process.env, HOME: home, USERPROFILE: home, WEFT_CLAUDE_CODE_STATE_DIR: join(home, '.weft-claude-code'), WEFT_API_KEY: '' };
}

test('F8: the installer keeps the first backup, writes atomically and never takes the API key from argv', { skip: !posix }, () => {
  const home = mkdtempSync(join(tmpdir(), 'weft-inst-'));
  // a copy of the plugin: the installer chmods its hook script
  const copy = join(home, 'plugin');
  for (const p of ['install.mjs', 'lib', 'hooks', 'package.json']) cpSync(join(pluginDir, p), join(copy, p), { recursive: true });
  const settings = join(home, '.claude', 'settings.json');
  mkdirSync(dirname(settings), { recursive: true });
  writeFileSync(settings, '{"theme":"dark"}\n');
  const run = (args, opts = {}) => spawnSync(process.execPath, [join(copy, 'install.mjs'), '--settings', settings, ...args], { env: installEnv(home), encoding: 'utf8', ...opts });
  let r = run(['--project', 'team/p']);
  assert.equal(r.status, 0, r.stderr);
  assert.equal(readFileSync(`${settings}.bak`, 'utf8'), '{"theme":"dark"}\n');
  const installed = JSON.parse(readFileSync(settings, 'utf8'));
  assert.equal(installed.theme, 'dark');
  assert.match(installed.hooks.Stop[0].hooks[0].command, /^node '.*\/hooks\/weft-hook\.mjs'$/);
  r = run(['--project', 'team/p']);
  assert.equal(r.status, 0, r.stderr);
  assert.equal(readFileSync(`${settings}.bak`, 'utf8'), '{"theme":"dark"}\n', 'second run keeps the original backup');
  assert.deepEqual(readdirSync(dirname(settings)).filter((f) => f.endsWith('.tmp')), [], 'no temp files left');
  // --api-key VALUE is refused and nothing is saved
  r = run(['--api-key', 'ARGVKEY']);
  assert.equal(r.status, 2);
  assert.match(r.stderr, /--api-key-stdin/);
  const confPath = join(home, '.weft-claude-code', 'settings.json');
  assert.ok(!existsSync(confPath) || !readFileSync(confPath, 'utf8').includes('ARGVKEY'));
  // the key from stdin is saved 0600 in a 0700 dir
  r = run(['--api-key-stdin'], { input: 'STDINKEY\n' });
  assert.equal(r.status, 0, r.stderr);
  assert.ok(!r.stdout.includes('STDINKEY'));
  assert.equal(JSON.parse(readFileSync(confPath, 'utf8')).api_key, 'STDINKEY');
  assert.equal(statSync(confPath).mode & 0o777, 0o600);
  assert.equal(statSync(dirname(confPath)).mode & 0o777, 0o700);
  r = spawnSync(process.execPath, [join(copy, 'install.mjs'), '--settings', settings, '--api-key-from-env'], { env: { ...installEnv(home), WEFT_API_KEY: 'ENVKEY' }, encoding: 'utf8' });
  assert.equal(r.status, 0, r.stderr);
  assert.equal(JSON.parse(readFileSync(confPath, 'utf8')).api_key, 'ENVKEY');
  // invalid JSON is left alone
  writeFileSync(settings, '{oops');
  r = run([]);
  assert.equal(r.status, 1);
  assert.equal(readFileSync(settings, 'utf8'), '{oops');
});

test('F8: an install directory with shell metacharacters is refused (no injection)', { skip: !posix }, () => {
  const base = mkdtempSync(join(tmpdir(), 'weft-inj-'));
  const evil = join(base, 'x"$(touch INJECTED)"y');
  mkdirSync(evil, { recursive: true });
  for (const p of ['install.mjs', 'lib', 'hooks', 'package.json']) cpSync(join(pluginDir, p), join(evil, p), { recursive: true });
  const settings = join(base, 'settings.json');
  const r = spawnSync(process.execPath, [join(evil, 'install.mjs'), '--settings', settings], { cwd: base, env: installEnv(base), encoding: 'utf8' });
  assert.equal(r.status, 1);
  assert.match(r.stderr, /refusing/);
  assert.ok(!existsSync(settings), 'settings not written');
  assert.ok(!existsSync(join(base, 'INJECTED')));
});
