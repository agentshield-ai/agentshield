// A fake Pi ExtensionAPI and a recorded session (event order as emitted by
// @mariozechner/pi-coding-agent 0.73.1: `context` precedes `turn_start` after the first turn) (two prompts: a fix with read,
// bash and edit tools over three LLM calls, then a failed LLM call).
import { registerWeft } from '../lib/pi.mjs';

export const SESSION_ID = '0198f0c2-7d1e-7a51-9f00-5e55a0c0ffee';
const T0 = Date.parse('2026-11-02T12:00:00.000Z');
const MODEL = 'claude-sonnet-4-5';

const user = (text) => ({ role: 'user', content: text, timestamp: T0 });
const text = (t) => ({ type: 'text', text: t });
const call = (id, name, args) => ({ type: 'toolCall', id, name, arguments: args });
const assistant = (content, usage, stopReason, extra = {}) => ({ role: 'assistant', content, api: 'anthropic-messages', provider: 'anthropic', model: MODEL, usage, stopReason, timestamp: T0, ...extra });
const usage = (input, output, cacheRead = 0, cacheWrite = 0, cost = 0) => ({ input, output, cacheRead, cacheWrite, totalTokens: input + output + cacheRead + cacheWrite, cost: { total: cost } });
const result = (toolCallId, toolName, t, isError = false) => ({ role: 'toolResult', toolCallId, toolName, content: [text(t)], isError, timestamp: T0 });

export function recordedEvents(sessionId = SESSION_ID) {
  const p1 = 'Fix the failing test in math.js';
  const a1 = assistant([{ type: 'thinking', thinking: 'Look first.' }, text('Let me look.'), call('tc-read', 'read', { path: 'math.js' }), call('tc-bash', 'bash', { command: 'npm test' })], usage(1200, 80, 3000, 200, 0.0123), 'toolUse');
  const a2 = assistant([call('tc-edit', 'edit', { path: 'math.js', oldText: 'a-b', newText: 'a+b' })], usage(1500, 40, 4200), 'toolUse');
  const a3 = assistant([text('Fixed: add() subtracted instead of adding.')], usage(1700, 30, 4200), 'stop');
  const r1 = result('tc-read', 'read', 'export const add = (a, b) => a - b;');
  const r2 = result('tc-bash', 'bash', '1 failing', true);
  const r3 = result('tc-edit', 'edit', 'Edited math.js');
  const p2 = 'Now delete node_modules';
  const b1 = assistant([], usage(0, 0), 'error', { errorMessage: '429 rate limited' });
  return {
    sessionId,
    events: [
      { t: 0, name: 'session_start', event: { type: 'session_start', reason: 'startup' } },
      { t: 100, name: 'before_agent_start', event: { type: 'before_agent_start', prompt: p1, images: [], systemPrompt: 'You are pi, a coding agent.' } },
      { t: 110, name: 'agent_start', event: { type: 'agent_start' } },
      { t: 120, name: 'message_end', event: { type: 'message_end', message: user(p1) } },
      { t: 130, name: 'turn_start', event: { type: 'turn_start', turnIndex: 0, timestamp: T0 + 130 } },
      { t: 140, name: 'context', event: { type: 'context', messages: [user(p1)] } },
      { t: 900, name: 'message_end', event: { type: 'message_end', message: a1 } },
      { t: 910, name: 'tool_execution_start', event: { type: 'tool_execution_start', toolCallId: 'tc-read', toolName: 'read', args: { path: 'math.js' } } },
      { t: 920, name: 'tool_execution_start', event: { type: 'tool_execution_start', toolCallId: 'tc-bash', toolName: 'bash', args: { command: 'npm test' } } },
      { t: 950, name: 'tool_execution_end', event: { type: 'tool_execution_end', toolCallId: 'tc-read', toolName: 'read', result: { content: r1.content, details: { lines: 1 } }, isError: false } },
      { t: 1500, name: 'tool_execution_end', event: { type: 'tool_execution_end', toolCallId: 'tc-bash', toolName: 'bash', result: { content: r2.content }, isError: true } },
      { t: 1510, name: 'turn_end', event: { type: 'turn_end', turnIndex: 0, message: a1, toolResults: [r1, r2] } },
      { t: 1505, name: 'message_end', event: { type: 'message_end', message: r1 } },
      { t: 1506, name: 'message_end', event: { type: 'message_end', message: r2 } },
      { t: 1520, name: 'context', event: { type: 'context', messages: [user(p1), a1, r1, r2] } },
      { t: 1530, name: 'turn_start', event: { type: 'turn_start', turnIndex: 1, timestamp: T0 + 1530 } },
      { t: 2100, name: 'message_end', event: { type: 'message_end', message: a2 } },
      { t: 2110, name: 'tool_execution_start', event: { type: 'tool_execution_start', toolCallId: 'tc-edit', toolName: 'edit', args: { path: 'math.js', oldText: 'a-b', newText: 'a+b' } } },
      { t: 2200, name: 'tool_execution_end', event: { type: 'tool_execution_end', toolCallId: 'tc-edit', toolName: 'edit', result: { content: r3.content }, isError: false } },
      { t: 2210, name: 'context', event: { type: 'context', messages: [user(p1), a1, r1, r2, a2, r3] } },
      { t: 2220, name: 'turn_start', event: { type: 'turn_start', turnIndex: 2, timestamp: T0 + 2220 } },
      { t: 2600, name: 'message_end', event: { type: 'message_end', message: a3 } },
      { t: 2700, name: 'agent_end', event: { type: 'agent_end', messages: [user(p1), a1, r1, r2, a2, r3, a3] } },
      { t: 5000, name: 'before_agent_start', event: { type: 'before_agent_start', prompt: p2, images: [], systemPrompt: 'You are pi, a coding agent.' } },
      { t: 5010, name: 'agent_start', event: { type: 'agent_start' } },
      { t: 5020, name: 'turn_start', event: { type: 'turn_start', turnIndex: 0, timestamp: T0 + 5020 } },
      { t: 5030, name: 'context', event: { type: 'context', messages: [user(p1), a3, user(p2)] } },
      { t: 5400, name: 'message_end', event: { type: 'message_end', message: b1 } },
      { t: 5450, name: 'agent_end', event: { type: 'agent_end', messages: [user(p2), b1] } },
    ],
  };
}

/** A fake ExtensionAPI collecting handlers and commands. */
export function fakePi() {
  const handlers = new Map();
  const commands = new Map();
  return {
    handlers,
    commands,
    on(name, h) {
      handlers.set(name, [...(handlers.get(name) ?? []), h]);
    },
    registerCommand(name, opts) {
      commands.set(name, opts);
    },
    async emit(name, event, ctx) {
      const out = [];
      for (const h of handlers.get(name) ?? []) out.push(await h(event, ctx));
      return out;
    },
  };
}

export function fakeCtx(sessionId = SESSION_ID) {
  const notes = [];
  return {
    cwd: '/work/calc',
    model: { id: MODEL, provider: 'anthropic' },
    notes,
    ui: { notify: (m, level) => notes.push([level, m]) },
    sessionManager: { getSessionId: () => sessionId, getSessionName: () => undefined, getSessionFile: () => `/home/u/.pi/agent/sessions/${sessionId}.jsonl`, getCwd: () => '/work/calc' },
  };
}

/** Register the extension with a fake API and clock and replay the recorded session. */
export async function replay({ options = { project: 'test/pi' }, fetchImpl, sessionId, env = {}, shutdown = false } = {}) {
  const fx = recordedEvents(sessionId);
  const pi = fakePi();
  let clock = T0;
  const now = () => clock;
  const logs = [];
  const logger = { warn: (m) => logs.push(m), info: (m) => logs.push(m) };
  const tracer = registerWeft(pi, options, { env, fetchImpl, now, logger });
  const ctx = fakeCtx(fx.sessionId);
  const returns = [];
  for (const ev of fx.events) {
    clock = T0 + ev.t;
    returns.push(...(await pi.emit(ev.name, ev.event, ctx)));
  }
  if (shutdown) {
    clock = T0 + 6000;
    await pi.emit('session_shutdown', { type: 'session_shutdown', reason: 'quit' }, ctx);
  }
  return { pi, tracer, fx, returns, ctx, logs };
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

// ---------------------------------------------------------------- live server helpers

export async function post(path, body) {
  const headers = { 'Content-Type': 'application/json' };
  if (process.env.WEFT_TEST_API_KEY) headers.Authorization = `Bearer ${process.env.WEFT_TEST_API_KEY}`;
  const r = await fetch(`${process.env.WEFT_TEST_SERVER_URL}${path}`, { method: 'POST', headers, body: JSON.stringify(body) });
  if (!r.ok) throw new Error(`${path} -> ${r.status}`);
  return r.json();
}

export async function waitFor(fn, ms = 15000) {
  const end = Date.now() + ms;
  for (;;) {
    const v = await fn();
    if (v || Date.now() > end) return v;
    await new Promise((r) => setTimeout(r, 200));
  }
}

