// Content capture off: nothing the user, the model or a tool wrote leaves the pi process.
//   node --test plugins/weft-pi/test/
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { replay } from './helpers.mjs';

const PLANTED = [
  'Fix the failing test in math.js', 'math.js', 'npm test', 'Let me look', 'Look first', 'export const add', '1 failing', 'Edited math.js',
  'Fixed: add()', 'Now delete node_modules', '429 rate limited', 'You are pi, a coding agent', '/work/calc', '/home/u/.pi/agent/sessions',
];

async function run(options) {
  const bodies = [];
  const fetchImpl = async (_u, init) => (bodies.push(String(init.body)), new Response('{}', { status: 200 }));
  const { tracer } = await replay({ options, fetchImpl });
  clearInterval(tracer.timer);
  while (tracer.sending) await tracer.sending;
  await tracer.flush();
  return bodies;
}

test('F3: captureContent=false sends none of the planted strings', async () => {
  const bodies = await run({ project: 'test/pi', captureContent: false });
  const raw = bodies.join('\n');
  assert.ok(raw.includes('pi.agent_run'), 'spans were sent');
  for (const p of PLANTED) assert.ok(!raw.includes(p), `content leaked: ${JSON.stringify(p)}`);
  assert.ok(!raw.includes('gen_ai.conversation.name'));
  const spans = bodies.flatMap((b) => JSON.parse(b).items.map((i) => i.span));
  assert.equal(spans.find((s) => s.attributes['gen_ai.tool.call.id'] === 'tc-bash').error, 'tool failed');
  assert.ok(spans.filter((s) => s.name === 'pi.llm').every((s) => s.display_name === 'chat claude-sonnet-4-5'));
  assert.ok(spans.some((s) => s.error === 'LLM call error'));
});

test('F3: with capture on the same strings are sent (control)', async () => {
  const raw = (await run({ project: 'test/pi' })).join('\n');
  for (const p of ['Fix the failing test in math.js', '429 rate limited', '/work/calc']) assert.ok(raw.includes(p), p);
});
