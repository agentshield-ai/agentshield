// End to end against a running Weft server (skipped without WEFT_TEST_SERVER_URL):
//   WEFT_TEST_SERVER_URL=http://127.0.0.1:6345 node --test plugins/weft-pi/test/*.test.mjs
import assert from 'node:assert/strict';
import { randomUUID } from 'node:crypto';
import { test } from 'node:test';
import { post, replay, waitFor } from './helpers.mjs';

const URL = process.env.WEFT_TEST_SERVER_URL;

test('a Pi session shows up in /agents/conversations/chat', { skip: !URL && 'set WEFT_TEST_SERVER_URL' }, async () => {
  const sid = randomUUID();
  const project = `it/pi-${sid.slice(0, 8)}`;
  const { tracer } = await replay({ sessionId: sid, shutdown: true, options: { project, serverUrl: URL, apiKey: process.env.WEFT_TEST_API_KEY, flushIntervalMs: 100 } });
  clearInterval(tracer.timer);
  assert.equal(tracer.queue.length, 0, 'everything sent');
  const chat = await waitFor(async () => {
    const r = await post('/agents/conversations/chat', { project_id: project, conversation_id: sid });
    return r.total_turns === 2 ? r : null;
  });
  assert.ok(chat, 'conversation indexed');
  assert.deepEqual(chat.agent_names, ['pi']);
  const [t1, t2] = chat.turns;
  assert.equal(t1.messages[0].user_message.text, 'Fix the failing test in math.js');
  const tools = t1.messages.filter((m) => m.type === 'tool_call').map((m) => m.tool_call.tool_name);
  assert.deepEqual(tools.sort(), ['bash', 'edit', 'read']);
  assert.ok(t1.messages.some((m) => m.assistant_message?.text === 'Fixed: add() subtracted instead of adding.'));
  assert.equal(t2.messages[0].user_message.text, 'Now delete node_modules');
  assert.ok(chat.error_count >= 1);
  const agents = await post('/agents/query', { project_id: project });
  const pi = agents.agents.find((a) => a.agent_name === 'pi');
  assert.equal(pi.invocation_count, 2);
});
