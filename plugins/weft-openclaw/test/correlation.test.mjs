// Correlation with AgentShield (docs/weft/DESIGN.md §5.3): for the same
// before_tool_call events, the AgentShield OpenClaw plugin (plugins/openclaw)
// sends session_id and tool_call_id equal to the gen_ai.conversation.id and
// gen_ai.tool.call.id this plugin records on the tool spans, including a
// sub-agent's tool call in its child session. The plugin's own request
// builder (src/event-builder.ts) is loaded with Node's TypeScript stripping.
import assert from 'node:assert/strict';
import { copyFileSync, mkdtempSync, readFileSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { test } from 'node:test';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { harnessSessionId } from '../lib/openclaw.mjs';
import { recorder, replay } from './helpers.mjs';

const here = dirname(fileURLToPath(import.meta.url));
const SRC = join(here, '..', '..', 'openclaw', 'src');
const [major, minor] = process.versions.node.split('.').map(Number);
const canStrip = major > 22 || (major === 22 && minor >= 18) || process.features?.typescript;

/** Import the AgentShield plugin's event builder (copied with `.js` imports pointing at the `.ts` files). */
async function agentshieldBuilder() {
  const dir = mkdtempSync(join(tmpdir(), 'as-openclaw-'));
  for (const f of ['event-builder.ts', 'normalise.ts', 'types.ts']) {
    const text = readFileSync(join(SRC, f), 'utf8').replace(/from "\.\/([\w-]+)\.js"/g, 'from "./$1.ts"');
    writeFileSync(join(dir, f), text);
  }
  copyFileSync(join(here, '..', 'package.json'), join(dir, 'package.json')); // "type": "module"
  return import(pathToFileURL(join(dir, 'event-builder.ts')).href);
}

test('the AgentShield OpenClaw plugin sends the session and tool-call ids this plugin records', { skip: !canStrip && 'needs Node with TypeScript stripping (>= 22.18)' }, async () => {
  const { buildEvaluationRequest } = await agentshieldBuilder();
  const rec = recorder();
  const { tracer, fx } = replay({ fetchImpl: rec.fetchImpl });
  clearInterval(tracer.timer);
  tracer.sweep();
  await tracer.flush();
  const toolSpans = [...rec.calls().values()].filter((c) => c.attributes['gen_ai.tool.call.id']);
  const before = fx.events.filter((e) => e.hook === 'before_tool_call');
  assert.ok(before.length >= 3);
  let child = false;
  for (const ev of before) {
    const req = buildEvaluationRequest(ev.event, { ...ev.ctx, toolName: ev.event.toolName });
    assert.equal(req.session_id, harnessSessionId(ev.ctx), 'the plugins agree on the session id rule');
    const span = toolSpans.find((c) => c.attributes['gen_ai.tool.call.id'] === req.tool_call_id);
    assert.ok(span, `a Weft tool span for ${req.tool_call_id}`);
    assert.equal(span.attributes['gen_ai.conversation.id'], req.session_id, `${req.tool_call_id}: session id = gen_ai.conversation.id`);
    assert.equal(span.thread_id, req.session_id);
    if (ev.ctx.sessionKey?.includes('subagent')) child = true;
  }
  assert.ok(child, "a sub-agent's tool call (its child session) is covered");
});
