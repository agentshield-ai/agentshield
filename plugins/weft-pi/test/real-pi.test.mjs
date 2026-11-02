// The extension inside a real Pi agent session (pi-coding-agent SDK with its
// faux LLM provider), against a running Weft server. Skipped unless both are given:
//   WEFT_TEST_SERVER_URL=http://127.0.0.1:6345 WEFT_TEST_PI_MODULES=/path/to/node_modules \
//     node --test plugins/weft-pi/test/*.test.mjs
// where node_modules has @mariozechner/pi-coding-agent (npm install @mariozechner/pi-coding-agent).
import assert from 'node:assert/strict';
import { mkdtempSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { test } from 'node:test';
import { pathToFileURL } from 'node:url';
import { createWeftExtension } from '../index.js';
import { post, waitFor } from './helpers.mjs';

const URL = process.env.WEFT_TEST_SERVER_URL;
const MODS = process.env.WEFT_TEST_PI_MODULES;

async function load(pkg) {
  const dir = join(MODS, ...pkg.split('/'));
  const { readFileSync } = await import('node:fs');
  const meta = JSON.parse(readFileSync(join(dir, 'package.json'), 'utf8'));
  const entry = meta.exports?.['.']?.import ?? meta.exports?.['.']?.default ?? meta.exports?.['.'] ?? meta.main ?? 'index.js';
  return import(pathToFileURL(join(dir, typeof entry === 'string' ? entry : entry.default)).href);
}

test('a real Pi agent session is traced', { skip: !(URL && MODS) && 'set WEFT_TEST_SERVER_URL and WEFT_TEST_PI_MODULES' }, async () => {
  const { AuthStorage, createAgentSession, DefaultResourceLoader, SessionManager } = await load('@mariozechner/pi-coding-agent');
  const { registerFauxProvider, fauxAssistantMessage, fauxText, fauxToolCall } = await load('@mariozechner/pi-ai');
  const work = mkdtempSync(join(tmpdir(), 'weft-pi-'));
  const project = `it/pi-real-${Date.now().toString(36)}`;
  const faux = registerFauxProvider({ models: [{ id: 'faux-coder' }] });
  faux.setResponses([
    fauxAssistantMessage([fauxText('Let me check.'), fauxToolCall('bash', { command: 'echo hello-from-pi' })], { stopReason: 'toolUse' }),
    fauxAssistantMessage('The command printed hello-from-pi.'),
  ]);
  const loader = new DefaultResourceLoader({ cwd: work, agentDir: work, noSkills: true, noContextFiles: true, extensionFactories: [createWeftExtension({ project, serverUrl: URL, apiKey: process.env.WEFT_TEST_API_KEY, flushIntervalMs: 100 })] });
  await loader.reload();
  const authStorage = AuthStorage.inMemory();
  authStorage.setRuntimeApiKey('faux', 'test');
  const { session } = await createAgentSession({ cwd: work, agentDir: work, authStorage, model: faux.getModel(), sessionManager: SessionManager.inMemory(), resourceLoader: loader });
  await session.bindExtensions({});
  await session.prompt('Run echo hello-from-pi and tell me what it printed');
  const sid = session.sessionManager.getSessionId();
  await session.dispose?.();
  faux.unregister();
  const chat = await waitFor(async () => {
    const r = await post('/agents/conversations/chat', { project_id: project, conversation_id: sid });
    return r.total_turns === 1 && r.turns[0].messages.some((m) => m.type === 'tool_call') ? r : null;
  });
  assert.ok(chat, 'conversation indexed');
  const msgs = chat.turns[0].messages;
  assert.equal(msgs[0].user_message.text, 'Run echo hello-from-pi and tell me what it printed');
  assert.equal(msgs.find((m) => m.type === 'tool_call').tool_call.tool_name, 'bash');
  assert.ok(msgs.some((m) => m.assistant_message?.text === 'The command printed hello-from-pi.'));
  const headers = { 'Content-Type': 'application/json', ...(process.env.WEFT_TEST_API_KEY ? { Authorization: `Bearer ${process.env.WEFT_TEST_API_KEY}` } : {}) };
  const res = await fetch(`${URL}/calls/stream_query`, { method: 'POST', headers, body: JSON.stringify({ project_id: project }) });
  const ops = (await res.text()).split('\n').filter(Boolean).map((l) => JSON.parse(l).op_name.split('/').pop().split(':')[0]);
  assert.deepEqual(ops.sort(), ['pi.agent_run', 'pi.llm', 'pi.llm', 'pi.tool.bash'], 'one call per LLM response, no phantom ones');
});
