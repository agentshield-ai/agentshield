// End to end against a running Weft server (skipped without WEFT_TEST_SERVER_URL):
//   WEFT_TEST_SERVER_URL=http://127.0.0.1:6345 node --test plugins/weft-openclaw/test/*.test.mjs
import assert from 'node:assert/strict';
import { randomUUID } from 'node:crypto';
import { test } from 'node:test';
import { replay } from './helpers.mjs';

const URL = process.env.WEFT_TEST_SERVER_URL;

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

test('an OpenClaw session shows up in /agents/conversations/chat', { skip: !URL && 'set WEFT_TEST_SERVER_URL' }, async () => {
  const sid = randomUUID();
  const project = `it/openclaw-${sid.slice(0, 8)}`;
  const { tracer, api } = replay({ sessionId: sid, pluginConfig: { project, serverUrl: URL, apiKey: process.env.WEFT_TEST_API_KEY, flushIntervalMs: 100 } });
  await api.handlers.get('gateway_stop').h();
  assert.equal(tracer.queue.length, 0, 'everything sent');
  const chat = await waitFor(async () => {
    const r = await post('/agents/conversations/chat', { project_id: project, conversation_id: sid });
    return r.total_turns === 2 ? r : null;
  });
  assert.ok(chat, 'conversation indexed');
  assert.ok(chat.agent_names.includes('main'));
  const [t1, t2] = chat.turns;
  assert.equal(t1.messages[0].user_message.text, "What's the weather in Oslo? Ask the researcher too.");
  const tools = t1.messages.filter((m) => m.type === 'tool_call').map((m) => m.tool_call.tool_name);
  assert.ok(tools.includes('web_fetch') && tools.includes('web_search'), JSON.stringify(tools));
  assert.ok(t1.messages.some((m) => m.type === 'agent_start'), 'sub-agent');
  assert.ok(t1.messages.some((m) => m.assistant_message?.text === 'It is sunny and 12°C in Oslo now; tomorrow brings rain at 9°C.'));
  assert.equal(t2.messages[0].user_message.text, 'Delete my files');
  assert.ok(chat.error_count >= 1);
  const agents = await post('/agents/query', { project_id: project });
  const main = agents.agents.find((a) => a.agent_name === 'main');
  assert.equal(main.invocation_count, 2);
  assert.ok(agents.agents.some((a) => a.agent_name === 'researcher'));
});
