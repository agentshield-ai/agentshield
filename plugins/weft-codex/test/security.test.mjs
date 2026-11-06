// Security behaviour of the Codex plugin: content capture off, private local state, installer hardening.
//   node --test plugins/weft-codex/test/
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { cpSync, existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, statSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { test } from 'node:test';
import { fileURLToPath } from 'node:url';
import { flushRollout } from '../lib/codex.mjs';
import { tomlSnippet } from '../install.mjs';
import { setup } from './helpers.mjs';

const here = dirname(fileURLToPath(import.meta.url));
const pluginDir = join(here, '..');
const posix = process.platform !== 'win32';
const SAFE_KEYS = new Set(['type', 'role', 'id', 'call_id', 'name', 'server', 'tool', 'model', 'status', 'timestamp', 'originator', 'cli_version', 'effort', 'summary', 'approval_policy', 'mode', 'branch']);

/** What the user, the model and the tools wrote in the rollout (strings of 6+ characters), plus cwd. */
function plantedStrings(rollout) {
  const out = new Set();
  const walk = (v, key) => {
    if (typeof v === 'string') {
      if (!SAFE_KEYS.has(key) && v.trim().length >= 6) out.add(v.split('\n')[0].slice(0, 60));
    } else if (Array.isArray(v)) v.forEach((x) => walk(x, key));
    else if (v && typeof v === 'object') for (const [k, x] of Object.entries(v)) walk(x, k);
  };
  for (const line of readFileSync(rollout, 'utf8').trim().split('\n')) {
    const l = JSON.parse(line);
    if (l.type === 'response_item' || l.type === 'event_msg' || l.type === 'session_meta' || l.type === 'turn_context') walk(l.payload, '');
  }
  return [...out].filter((s) => !/^[0-9a-f-]{20,}$/.test(s) && !/^\d{4}-\d\d-\d\dT/.test(s) && !['codex_cli_rs', 'gpt-5-codex', 'on-request', 'workspace-write'].includes(s));
}

test('F3: capture_content=false sends none of the rollout content', async () => {
  const { cfg, rollout } = setup({ env: { WEFT_CAPTURE_CONTENT: 'off' } });
  const bodies = [];
  const res = await flushRollout(cfg, rollout, { fetchImpl: async (_u, init) => (bodies.push(String(init.body)), new Response('{}', { status: 200 })) });
  assert.equal(res.error, undefined);
  assert.equal(res.sent, 2);
  const raw = bodies.join('\n');
  const planted = plantedStrings(rollout);
  assert.ok(planted.includes('Fix the failing test in utils') && planted.includes('/home/dev/app'), planted.join(' | '));
  for (const p of planted) assert.ok(!raw.includes(p), `content leaked: ${JSON.stringify(p)}`);
  const spans = bodies.flatMap((b) => JSON.parse(b).items.map((i) => i.span));
  const failed = spans.find((s) => s.attributes['gen_ai.tool.call.id'] === 'call_B1');
  assert.equal(failed.error, 'tool failed');
  assert.ok(spans.every((s) => !s.display_name.includes(':')), spans.map((s) => s.display_name).join(', '));
  // cursor: private and without the first prompt or cwd
  if (posix) {
    const dir = join(cfg.stateDir, 'cursors');
    assert.equal(statSync(dir).mode & 0o777, 0o700);
    for (const f of readdirSync(dir)) {
      assert.equal(statSync(join(dir, f)).mode & 0o777, 0o600, f);
      const text = readFileSync(join(dir, f), 'utf8');
      assert.ok(!text.includes('Fix the failing') && !text.includes('/home/dev/app'), text);
    }
  }
});

test('F8: the TOML snippet quotes the hook path and refuses unsafe paths', () => {
  const toml = tomlSnippet('/opt/weft plugins/hooks/weft-codex-hook.mjs', 'linux');
  assert.match(toml, /^command = "node '\/opt\/weft plugins\/hooks\/weft-codex-hook\.mjs'"$/m);
  assert.match(toml, /^# notify = \["node", "\/opt\/weft plugins\/hooks\/weft-codex-hook\.mjs"\]$/m);
  for (const bad of ['/x"$(touch I)"y/h.mjs', "/x'y/h.mjs", '/x\ny/h.mjs']) assert.throws(() => tomlSnippet(bad, 'linux'), /refusing/);
});

function env(home) {
  return { ...process.env, HOME: home, CODEX_HOME: join(home, '.codex'), WEFT_CODEX_STATE_DIR: join(home, '.weft-codex'), WEFT_API_KEY: '' };
}

test('F8: the installer keeps the first backup, writes atomically and never takes the API key from argv', { skip: !posix }, () => {
  const home = mkdtempSync(join(tmpdir(), 'weft-cxi-'));
  const hooks = join(home, '.codex', 'hooks.json');
  mkdirSync(dirname(hooks), { recursive: true });
  writeFileSync(hooks, '{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"other"}]}]}}\n');
  const run = (args, opts = {}) => spawnSync(process.execPath, [join(pluginDir, 'install.mjs'), '--hooks', hooks, ...args], { env: env(home), encoding: 'utf8', ...opts });
  assert.equal(run([]).status, 0);
  const original = readFileSync(`${hooks}.bak`, 'utf8');
  assert.match(original, /"other"/);
  assert.equal(run([]).status, 0);
  assert.equal(readFileSync(`${hooks}.bak`, 'utf8'), original, 'second run keeps the original backup');
  const doc = JSON.parse(readFileSync(hooks, 'utf8'));
  assert.equal(doc.hooks.Stop.length, 2);
  assert.match(doc.hooks.Stop[1].hooks[0].command, /^node '.*weft-codex-hook\.mjs'$/);
  assert.deepEqual(readdirSync(dirname(hooks)).filter((f) => f.endsWith('.tmp')), []);
  let r = run(['--api-key', 'ARGVKEY']);
  assert.equal(r.status, 2);
  const conf = join(home, '.weft-codex', 'settings.json');
  assert.ok(!existsSync(conf));
  r = run(['--api-key-stdin', '--project', 'team/codex'], { input: 'STDINKEY\n' });
  assert.equal(r.status, 0, r.stderr);
  assert.deepEqual(JSON.parse(readFileSync(conf, 'utf8')), { project: 'team/codex', api_key: 'STDINKEY' });
  assert.equal(statSync(conf).mode & 0o777, 0o600);
});

test('F8: an install directory with shell metacharacters is refused (no injection)', { skip: !posix }, () => {
  const base = mkdtempSync(join(tmpdir(), 'weft-cxinj-'));
  const evil = join(base, 'x"$(touch INJECTED)"y');
  mkdirSync(evil, { recursive: true });
  for (const p of ['install.mjs', 'lib', 'hooks', 'package.json']) cpSync(join(pluginDir, p), join(evil, p), { recursive: true });
  const hooks = join(base, 'hooks.json');
  for (const args of [['--hooks', hooks], ['--print-toml']]) {
    const r = spawnSync(process.execPath, [join(evil, 'install.mjs'), ...args], { cwd: base, env: env(base), encoding: 'utf8' });
    assert.equal(r.status, 1, args.join(' '));
    assert.match(r.stderr, /refusing/);
  }
  assert.ok(!existsSync(hooks));
  assert.ok(!existsSync(join(base, 'INJECTED')));
});
