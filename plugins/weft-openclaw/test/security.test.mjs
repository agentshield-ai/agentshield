// Content capture off: nothing the user, the model or a tool wrote leaves the gateway.
//   node --test plugins/weft-openclaw/test/
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { test } from 'node:test';
import { fileURLToPath } from 'node:url';
import { replay } from './helpers.mjs';

const here = dirname(fileURLToPath(import.meta.url));
const PLANTED = [
  "What's the weather in Oslo", 'wttr.in', "Find tomorrow's forecast", 'Oslo forecast tomorrow', 'Rain, 9', 'Tomorrow: rain', 'It is sunny',
  'Delete my files', 'rm -rf ~/files', 'blocked by policy', "I can't delete", 'You are a helpful assistant', 'forecast',
];

test('F3: captureContent=false sends none of the planted strings', async () => {
  const bodies = [];
  const fetchImpl = async (_u, init) => (bodies.push(String(init.body)), new Response('{}', { status: 200 }));
  const { tracer } = replay({ pluginConfig: { project: 'test/openclaw', captureContent: false }, fetchImpl });
  clearInterval(tracer.timer);
  await tracer.flush();
  const raw = bodies.join('\n');
  assert.ok(raw.includes('openclaw.agent_run'), 'spans were sent');
  for (const p of PLANTED) assert.ok(!raw.includes(p), `content leaked: ${JSON.stringify(p)}`);
  assert.ok(!raw.includes('gen_ai.conversation.name'));
  const spans = bodies.flatMap((b) => JSON.parse(b).items.map((i) => i.span));
  assert.equal(spans.find((s) => s.attributes['gen_ai.tool.call.id'] === 'tc-3').error, 'tool error');
  assert.equal(spans.find((s) => s.attributes['gen_ai.tool.call.id'] === 'tc-1').display_name, 'web_fetch');
});

test('F3: with capture on the same strings are sent (control)', async () => {
  const bodies = [];
  const fetchImpl = async (_u, init) => (bodies.push(String(init.body)), new Response('{}', { status: 200 }));
  const { tracer } = replay({ fetchImpl });
  clearInterval(tracer.timer);
  await tracer.flush();
  const raw = bodies.join('\n');
  for (const p of ['Delete my files', 'blocked by policy']) assert.ok(raw.includes(p), p);
});

test('the manifest marks apiKey as sensitive', () => {
  const m = JSON.parse(readFileSync(join(here, '..', 'openclaw.plugin.json'), 'utf8'));
  assert.equal(m.uiHints.apiKey.sensitive, true);
  assert.ok(m.configSchema.properties.apiKey);
});
