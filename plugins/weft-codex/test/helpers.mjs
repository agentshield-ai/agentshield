import { mkdirSync, mkdtempSync, readFileSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { config } from '../lib/codex.mjs';

const here = dirname(fileURLToPath(import.meta.url));

/** A temp CODEX_HOME with the recorded rollout (optionally only the first `lines` lines, or with a new session id). */
export function setup({ lines, sessionId, env = {} } = {}) {
  const fx = JSON.parse(readFileSync(join(here, 'fixtures', 'hooks.json'), 'utf8'));
  let raw = readFileSync(join(here, 'fixtures', 'rollout.jsonl'), 'utf8');
  const sid = sessionId ?? fx.session_id;
  if (sessionId) raw = raw.replaceAll(fx.session_id, sessionId);
  const all = raw.trimEnd().split('\n');
  const dir = mkdtempSync(join(tmpdir(), 'weft-codex-'));
  const codexHome = join(dir, 'codex');
  const rollout = join(codexHome, 'sessions', '2026', '11', '02', `rollout-2026-11-02T11-00-00-${sid}.jsonl`);
  mkdirSync(dirname(rollout), { recursive: true });
  writeFileSync(rollout, `${all.slice(0, lines ?? all.length).join('\n')}\n`);
  const fullEnv = { WEFT_PROJECT: 'test/codex', WEFT_CODEX_STATE_DIR: join(dir, 'state'), CODEX_HOME: codexHome, ...env };
  const fix = (v) => JSON.parse(JSON.stringify(v).replaceAll('{{ROLLOUT}}', rollout).replaceAll(fx.session_id, sid));
  return { cfg: config(fullEnv), env: fullEnv, rollout, sid, all, split: fx.split, stop: fix(fx.stop_hook), notify: fix(fx.notify), dir };
}

export function recorder({ fail = 0 } = {}) {
  const items = [];
  let failures = fail;
  const fetchImpl = async (_url, init) => {
    if (failures > 0) {
      failures--;
      throw new Error('connect ECONNREFUSED');
    }
    items.push(...JSON.parse(init.body).items);
    return new Response('{"items":[]}', { status: 200 });
  };
  const calls = () => {
    const m = new Map();
    for (const it of items) {
      const r = it.span ?? it.start ?? it.end;
      m.set(r.id, { ...(m.get(r.id) ?? {}), ...r });
    }
    return m;
  };
  return { items, fetchImpl, calls };
}
