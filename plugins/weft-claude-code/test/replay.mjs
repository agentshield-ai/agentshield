// Replays a recorded Claude Code session (hook payloads + transcript lines) into a temp state dir.
import { appendFileSync, mkdtempSync, readFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { config, handleHook } from '../lib/claude.mjs';

const here = dirname(fileURLToPath(import.meta.url));

export function loadFixture(name = 'session.json', sessionId) {
  const raw = readFileSync(join(here, 'fixtures', name), 'utf8');
  const fx = JSON.parse(raw);
  if (sessionId) return JSON.parse(raw.replaceAll(fx.session_id, sessionId));
  return fx;
}

/** Replay steps; returns { cfg, dir, sid, results }. `env` adds config (WEFT_PROJECT, ...). */
export function replay(fx, env = {}, { upTo } = {}) {
  const dir = mkdtempSync(join(tmpdir(), 'weft-cc-'));
  const files = { main: join(dir, 'transcript.jsonl'), agent: join(dir, 'agent-a1b2c3.jsonl') };
  const fullEnv = { WEFT_PROJECT: 'test/claude-code', WEFT_CLAUDE_CODE_STATE_DIR: join(dir, 'state'), ...env };
  const cfg = config(fullEnv);
  const fix = (v) => JSON.parse(JSON.stringify(v).replaceAll('{{TRANSCRIPT}}', files.main).replaceAll('{{AGENT_TRANSCRIPT}}', files.agent));
  const results = [];
  const steps = upTo ? fx.steps.slice(0, upTo) : fx.steps;
  for (const s of steps) {
    if (s.transcript) appendFileSync(files[s.transcript], `${JSON.stringify(s.line)}\n`);
    else results.push(handleHook(fix(s.hook), { cfg, now: Date.parse(s.t), flush: 'off' }));
  }
  return { cfg, dir, sid: fx.session_id, results, files, env: fullEnv };
}

/** A fetch stub that records /api/v1/spans/batch items. */
export function recorder({ fail = 0, status = 200 } = {}) {
  const items = [];
  let failures = fail;
  const fetchImpl = async (url, init) => {
    if (failures > 0) {
      failures--;
      throw new Error('connect ECONNREFUSED');
    }
    if (status !== 200) return new Response('{"reason":"nope"}', { status });
    const body = JSON.parse(init.body);
    items.push(...body.items);
    return new Response(JSON.stringify({ items: [] }), { status: 200 });
  };
  return { items, fetchImpl, calls: () => byId(items) };
}

/** Merge start/end items into call objects keyed by id. */
export function byId(items) {
  const m = new Map();
  for (const it of items) {
    const r = it.span ?? it.start ?? it.end;
    m.set(r.id, { ...(m.get(r.id) ?? {}), ...r });
  }
  return m;
}
