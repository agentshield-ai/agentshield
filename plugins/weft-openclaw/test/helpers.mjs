import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import plugin from '../index.js';
import { registerWeft } from '../lib/openclaw.mjs';

const here = dirname(fileURLToPath(import.meta.url));

export function loadEvents(sessionId) {
  const raw = readFileSync(join(here, 'fixtures', 'events.json'), 'utf8');
  const fx = JSON.parse(raw);
  return sessionId ? JSON.parse(raw.replaceAll(fx.session_id, sessionId)) : fx;
}

/** A fake OpenClaw plugin API collecting handlers. */
export function fakeApi(pluginConfig = {}) {
  const handlers = new Map();
  const logs = [];
  return {
    pluginConfig,
    handlers,
    logs,
    logger: { info: (m) => logs.push(['info', m]), warn: (m) => logs.push(['warn', m]), error: (m) => logs.push(['error', m]), debug: () => {} },
    on(name, h, opts) {
      handlers.set(name, { h, opts });
    },
    emit(name, event, ctx) {
      return handlers.get(name)?.h(event, ctx);
    },
  };
}

/** Register the plugin with a fake API and a controllable clock, replay the fixture. */
export function replay({ pluginConfig = { project: 'test/openclaw' }, fetchImpl, sessionId, env = {} } = {}) {
  const fx = loadEvents(sessionId);
  const api = fakeApi(pluginConfig);
  let clock = Date.parse('2026-11-02T12:00:00.000Z');
  const now = () => clock;
  const tracer = registerWeft(api, { env, fetchImpl, now });
  const start = clock;
  const returns = [];
  for (const ev of fx.events) {
    clock = start + ev.t;
    returns.push(api.emit(ev.hook, ev.event, ev.ctx));
  }
  return { api, tracer, fx, returns, plugin };
}

export function recorder({ fail = 0 } = {}) {
  const items = [];
  let failures = fail;
  const fetchImpl = async (_url, init) => {
    if (failures > 0) {
      failures--;
      throw new Error('connect ECONNREFUSED');
    }
    items.push(...JSON.parse(init.body).batch);
    return new Response('{"res":[]}', { status: 200 });
  };
  const calls = () => {
    const m = new Map();
    for (const it of items) {
      const r = it.req[it.mode];
      m.set(r.id, { ...(m.get(r.id) ?? {}), ...r });
    }
    return m;
  };
  return { items, fetchImpl, calls };
}
