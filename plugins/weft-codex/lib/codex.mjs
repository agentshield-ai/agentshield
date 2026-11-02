// OpenAI Codex CLI sessions -> Weft agent conversations.
//
// Codex writes every session to a rollout file
// (~/.codex/sessions/YYYY/MM/DD/rollout-<time>-<session id>.jsonl). The Stop
// hook (or the `notify` program) starts a detached worker that reads the
// rollout from a per-session cursor and turns the new lines into calls:
//
//   invoke_agent codex        one per user turn; conversation = the Codex session id
//   ├─ chat <model>           one per model response (closed by its token_count), with usage
//   └─ execute_tool <name>    shell / exec_command / apply_patch / MCP / web search calls
//
// Timestamps come from the rollout, so durations are the real ones. Call ids
// are derived from the session id and rollout positions, so re-sending after
// a failure upserts the same calls.

import { spawn } from 'node:child_process';
import { closeSync, existsSync, mkdirSync, openSync, readdirSync, readFileSync, statSync, unlinkSync, writeFileSync } from 'node:fs';
import { homedir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { callItems, chatOutput, loadConfig, makeLogger, parseMaybeJson, readJsonl, sendItems, shortText, stableId, toolDisplay, usageSummary } from './weft.mjs';

export const PLUGIN = 'weft-codex';
export const PLUGIN_VERSION = '0.1.0';

export function config(env = process.env, overrides = {}) {
  return loadConfig({ plugin: PLUGIN, defaultAgent: 'codex', stateDirEnv: 'WEFT_CODEX_STATE_DIR', env, overrides });
}

const safe = (s) => String(s || 'unknown').replace(/[^A-Za-z0-9_.-]/g, '_').slice(0, 128);
const cursorPath = (cfg, sid) => join(cfg.stateDir, 'cursors', `${safe(sid)}.json`);
const lockPath = (cfg, sid) => join(cfg.stateDir, 'cursors', `${safe(sid)}.lock`);

// ---------------------------------------------------------------- locating rollouts

export function codexHome(env = process.env) {
  return env.CODEX_HOME || join(homedir(), '.codex');
}

/** Find the rollout file of a session id under $CODEX_HOME/sessions (newest first). */
export function findRollout(sessionId, env = process.env) {
  if (!sessionId) return null;
  const root = join(codexHome(env), 'sessions');
  const stack = [root];
  let best = null;
  while (stack.length) {
    const d = stack.pop();
    let entries = [];
    try {
      entries = readdirSync(d, { withFileTypes: true });
    } catch {
      continue;
    }
    for (const e of entries) {
      const p = join(d, e.name);
      if (e.isDirectory()) stack.push(p);
      else if (e.name.startsWith('rollout-') && e.name.endsWith('.jsonl') && e.name.includes(sessionId)) {
        const m = statSync(p).mtimeMs;
        if (!best || m > best.m) best = { p, m };
      }
    }
  }
  return best?.p ?? null;
}

// ---------------------------------------------------------------- rollout -> turns

function textOf(content) {
  if (typeof content === 'string') return content;
  if (!Array.isArray(content)) return '';
  return content
    .map((c) => (typeof c === 'string' ? c : c?.text ?? ''))
    .filter(Boolean)
    .join('\n');
}

function isContextMessage(text) {
  const t = text.trimStart();
  return t.startsWith('<environment_context>') || t.startsWith('<user_instructions>') || t.startsWith('# AGENTS.md') || t.startsWith('<permissions') || t.startsWith('<user_shell_command>');
}

function functionOutput(out) {
  // Codex stores either a string or {content, success}; shell output is often JSON {output, metadata}.
  let content = out;
  let success;
  if (out && typeof out === 'object' && !Array.isArray(out)) {
    content = out.content ?? out.output ?? out;
    success = out.success;
  }
  return { value: parseMaybeJson(content), success };
}

/** Model output item -> chat content block (text, thinking, tool_use). */
function outputBlock(p) {
  switch (p.type) {
    case 'message':
      return { type: 'text', text: textOf(p.content) };
    case 'reasoning': {
      const t = (p.summary ?? []).map((s) => s?.text ?? '').filter(Boolean).join('\n') || textOf(p.content);
      return t ? { type: 'thinking', thinking: t } : null;
    }
    case 'function_call':
      return { type: 'tool_use', id: p.call_id, name: p.name, input: parseMaybeJson(p.arguments) };
    case 'custom_tool_call':
      return { type: 'tool_use', id: p.call_id, name: p.name, input: { input: p.input } };
    case 'local_shell_call':
      return { type: 'tool_use', id: p.call_id ?? p.id, name: 'local_shell', input: p.action ?? {} };
    case 'web_search_call':
      return { type: 'tool_use', id: p.id, name: 'web_search', input: p.action ?? {} };
    default:
      return null;
  }
}

/**
 * Parse rollout lines into session info and turns. Each turn has the user
 * message, model responses (LLM calls) and tool calls.
 */
export function parseRollout(lines, state = {}) {
  const session = { ...(state.session ?? {}) };
  const turns = [];
  let turn = null;
  let model = state.model;
  let llm = null; // the model response being accumulated
  let boundary = null; // timestamp the next model response starts from
  let pendingInput = [];
  let lastUsage = null;

  const openLLM = (ts) => {
    if (!llm) {
      llm = { startedAt: boundary ?? ts, endedAt: ts, blocks: [], input: pendingInput, model, index: turn.llms.length, offset: null };
      pendingInput = [];
    }
    return llm;
  };
  const closeLLM = (ts, usage) => {
    if (!llm) return;
    llm.endedAt = ts ?? llm.endedAt;
    llm.usage = usage ?? null;
    turn.llms.push(llm);
    llm = null;
    boundary = ts;
  };
  const startTurn = (ts, text, offset, turnId, fromItem = false) => {
    if (turn) endTurn(ts, 'next_turn');
    turn = { offset, startedAt: ts, endedAt: ts, prompt: text, codexTurnId: turnId, model, llms: [], tools: new Map(), toolOrder: [], final: null, closed: null, aborted: null, fromItem };
    turns.push(turn);
    boundary = ts;
    pendingInput = [{ role: 'user', content: text }];
    lastUsage = null;
  };
  const endTurn = (ts, how) => {
    if (!turn || turn.closed) return;
    if (llm) closeLLM(ts, null);
    turn.endedAt = ts ?? turn.endedAt;
    turn.closed = turn.closed ?? how;
  };

  for (const line of lines) {
    const ts = line.timestamp;
    const p = line.payload ?? {};
    switch (line.type) {
      case 'session_meta':
        session.id = p.id ?? session.id;
        session.cwd = p.cwd ?? session.cwd;
        session.cliVersion = p.cli_version ?? session.cliVersion;
        session.originator = p.originator ?? session.originator;
        session.git = p.git ?? session.git;
        break;
      case 'turn_context':
        model = p.model ?? model;
        if (turn && !turn.model) turn.model = model;
        if (turn) turn.effort = p.effort ?? turn.effort;
        session.approvalPolicy = p.approval_policy ?? session.approvalPolicy;
        break;
      case 'event_msg':
        if (p.type === 'user_message') {
          if (turn && turn.fromItem && !turn.closed && !turn.llms.length && !turn.toolOrder.length && !llm) {
            turn.fromItem = false; // the response_item came first: same turn
            turn.prompt = p.message ?? turn.prompt;
            turn.codexTurnId = turn.codexTurnId ?? p.turn_id;
          } else startTurn(ts, p.message ?? '', line.__offset, p.turn_id);
        } else if (p.type === 'task_started') {
          if (turn && !turn.codexTurnId) turn.codexTurnId = p.turn_id;
        } else if (p.type === 'token_count') {
          const u = p.info?.last_token_usage;
          if (!turn || !u) break;
          const key = JSON.stringify(u);
          if (!llm && key === lastUsage) break; // repeated count for the same response
          lastUsage = key;
          if (!llm) openLLM(ts);
          closeLLM(ts, u);
        } else if (p.type === 'agent_message') {
          if (turn) turn.final = p.message ?? turn.final;
        } else if (p.type === 'task_complete') {
          if (turn) {
            if (p.last_agent_message) turn.final = p.last_agent_message;
            endTurn(ts, 'complete');
          }
        } else if (p.type === 'turn_aborted') {
          if (turn) {
            turn.aborted = p.reason ?? 'aborted';
            endTurn(ts, 'aborted');
          }
        } else if (p.type === 'mcp_tool_call_end' && turn) {
          const t = turn.tools.get(p.call_id);
          if (t && p.invocation?.server) t.mcpServer = p.invocation.server;
        } else if (p.type === 'exec_command_end' && turn) {
          const t = turn.tools.get(p.call_id);
          if (t && p.exit_code !== undefined) t.exitCode = p.exit_code;
        }
        break;
      case 'response_item': {
        if (p.type === 'message' && p.role === 'user') {
          const text = textOf(p.content);
          if (isContextMessage(text)) break;
          // A user message starts a turn unless the current one has not started working yet
          // (then it is the same prompt as the event_msg user_message).
          if (!turn || turn.closed || turn.llms.length || turn.toolOrder.length || llm) startTurn(ts, text, line.__offset, undefined, true);
          else if (!turn.prompt) turn.prompt = text;
          break;
        }
        if (!turn) break;
        if (p.type === 'message' && (p.role === 'developer' || p.role === 'system')) break;
        if (p.type === 'function_call_output' || p.type === 'custom_tool_call_output') {
          const t = turn.tools.get(p.call_id);
          const { value, success } = functionOutput(p.output);
          if (t) {
            t.endedAt = ts;
            t.output = value;
            if (success === false) t.error = typeof value === 'string' ? shortText(value, 500) : 'tool call failed';
          }
          pendingInput.push({ role: 'tool', tool_call_id: p.call_id, content: typeof value === 'string' ? value : JSON.stringify(value) });
          boundary = ts;
          break;
        }
        const block = outputBlock(p);
        if (!block) break;
        const l = openLLM(ts);
        l.endedAt = ts;
        if (block.type !== 'text' || block.text) l.blocks.push(block);
        if (block.type === 'text' && block.text) turn.final = block.text;
        if (block.type === 'tool_use') {
          const name = block.name ?? 'tool';
          const t = { id: block.id ?? `${name}-${turn.toolOrder.length}`, name, input: block.input, startedAt: ts, endedAt: null, output: undefined };
          if (p.type === 'web_search_call') t.endedAt = ts;
          if (p.type === 'local_shell_call' && p.status && p.status !== 'in_progress') t.status = p.status;
          if (String(name).includes('__')) t.mcpServer = String(name).split('__')[0];
          turn.tools.set(t.id, t);
          turn.toolOrder.push(t.id);
        }
        break;
      }
      default:
        break;
    }
  }
  return { session, turns, model, open: turn && !turn.closed ? turn : null };
}

function codexUsage(u) {
  if (!u) return null;
  return { prompt: u.input_tokens ?? 0, completion: u.output_tokens ?? 0, total: u.total_tokens, cacheRead: u.cached_input_tokens ?? 0, reasoning: u.reasoning_output_tokens ?? 0 };
}

/** Calls (batch items) for one parsed turn. */
export function buildTurnItems(cfg, sid, turn, session = {}) {
  const items = [];
  const turnId = stableId(sid, 'turn', turn.codexTurnId ?? turn.offset ?? turn.startedAt);
  const traceId = turnId;
  const base = { traceId, threadId: sid, turnId, pluginVersion: PLUGIN_VERSION };
  const conv = { 'gen_ai.conversation.id': sid, 'gen_ai.conversation.name': session.firstPrompt };
  const model = turn.model ?? turn.llms.find((l) => l.model)?.model;
  items.push(
    ...callItems(cfg, {
      ...base,
      id: turnId,
      parentId: null,
      opName: 'codex.turn',
      kind: 'agent',
      displayName: `invoke_agent ${cfg.agentName}`,
      startedAt: turn.startedAt,
      endedAt: turn.endedAt,
      inputs: { messages: [{ role: 'user', content: turn.prompt ?? '' }] },
      output: turn.final ? { messages: [{ role: 'assistant', content: turn.final }] } : null,
      exception: turn.aborted ? `turn aborted: ${turn.aborted}` : undefined,
      genai: { ...conv, 'gen_ai.operation.name': 'invoke_agent', 'gen_ai.agent.name': cfg.agentName, 'gen_ai.agent.version': session.cliVersion, 'gen_ai.request.model': model, 'gen_ai.provider.name': 'openai' },
      attributes: { codex: { cwd: session.cwd, originator: session.originator, approval_policy: session.approvalPolicy, reasoning_effort: turn.effort, turn_id: turn.codexTurnId } },
    }),
  );
  turn.llms.forEach((l, i) => {
    const text = l.blocks.filter((b) => b.type === 'text').map((b) => b.text).join('\n');
    const uses = l.blocks.filter((b) => b.type === 'tool_use');
    const m = l.model ?? model;
    items.push(
      ...callItems(cfg, {
        ...base,
        id: stableId(sid, 'llm', turn.codexTurnId ?? turn.offset ?? turn.startedAt, i),
        parentId: turnId,
        opName: 'codex.llm',
        kind: 'llm',
        displayName: text ? shortText(text) : uses.length ? `→ ${uses.map((b) => b.name).join(', ')}` : `chat ${m ?? ''}`.trim(),
        startedAt: l.startedAt,
        endedAt: l.endedAt,
        inputs: { model: m, messages: l.input },
        output: chatOutput(m, l.blocks, uses.length ? 'tool_calls' : 'stop'),
        summary: usageSummary(m, codexUsage(l.usage)),
        genai: { ...conv, 'gen_ai.operation.name': 'chat', 'gen_ai.request.model': m, 'gen_ai.provider.name': 'openai' },
      }),
    );
  });
  for (const id of turn.toolOrder) {
    const t = turn.tools.get(id);
    const genai = { ...conv, 'gen_ai.operation.name': 'execute_tool', 'gen_ai.tool.name': t.name, 'gen_ai.tool.call.id': t.id };
    if (t.mcpServer) Object.assign(genai, { 'gen_ai.tool.type': 'mcp', 'mcp.server.name': t.mcpServer });
    let exception = t.error;
    if (!exception && t.endedAt === null) exception = turn.aborted ? 'turn aborted before the tool finished' : undefined;
    const input = t.input && typeof t.input === 'object' && !Array.isArray(t.input) ? t.input : { input: t.input };
    const exit = t.exitCode ?? t.output?.metadata?.exit_code;
    items.push(
      ...callItems(cfg, {
        ...base,
        id: stableId(sid, 'tool', t.id),
        parentId: turnId,
        opName: `codex.tool.${t.name}`,
        kind: 'tool',
        displayName: toolDisplay(t.name, input),
        startedAt: t.startedAt,
        endedAt: t.endedAt ?? turn.endedAt,
        inputs: input,
        output: t.output ?? null,
        exception,
        genai,
        attributes: exit !== undefined ? { codex: { exit_code: exit } } : undefined,
      }),
    );
  }
  return items;
}

// ---------------------------------------------------------------- worker

function readCursor(cfg, sid) {
  try {
    return JSON.parse(readFileSync(cursorPath(cfg, sid), 'utf8'));
  } catch {
    return { offset: 0, session: {} };
  }
}

function acquireLock(cfg, sid) {
  const p = lockPath(cfg, sid);
  mkdirSync(dirname(p), { recursive: true });
  try {
    if (Date.now() - statSync(p).mtimeMs > 60_000) unlinkSync(p);
  } catch {
    /* no stale lock */
  }
  try {
    closeSync(openSync(p, 'wx'));
    return () => {
      try {
        unlinkSync(p);
      } catch {
        /* gone */
      }
    };
  } catch {
    return null;
  }
}

/**
 * Send the turns added to a rollout since the last run. `final` (the Stop hook
 * or notify fired) treats an unfinished last turn as finished; it stays
 * re-sendable until a later turn starts, so late lines are picked up.
 */
export async function flushRollout(cfg, rolloutPath, { sessionId, fetchImpl, final = true } = {}) {
  const log = makeLogger(cfg, 'collector');
  const head = readJsonl(rolloutPath, 0).lines.find((l) => l.type === 'session_meta');
  const sid = sessionId ?? head?.payload?.id;
  if (!sid) {
    log.warn(`no session id for ${rolloutPath}`);
    return { sent: 0, error: 'no session id' };
  }
  const release = acquireLock(cfg, sid);
  if (!release) return { sent: 0, locked: true };
  try {
    const cursor = readCursor(cfg, sid);
    const { lines } = readJsonl(rolloutPath, cursor.offset ?? 0);
    const metaLines = (cursor.offset ?? 0) > 0 && head ? [head, ...lines] : lines;
    const { session, turns } = parseRollout(metaLines, { session: cursor.session, model: cursor.model });
    session.id = session.id ?? sid;
    let sent = 0;
    let items = 0;
    for (let i = 0; i < turns.length; i++) {
      const t = turns[i];
      const isLast = i === turns.length - 1;
      if (!t.closed && !(final && isLast)) break;
      if (!session.firstPrompt && t.prompt) session.firstPrompt = shortText(t.prompt, 80);
      const batch = buildTurnItems(cfg, sid, t, session);
      try {
        await sendItems(cfg, batch, { fetchImpl });
      } catch (e) {
        log.warn(`session ${sid}: send failed, will retry on the next turn: ${e?.message ?? e}`);
        writeFileSync(cursorPath(cfg, sid), JSON.stringify({ ...cursor, session }));
        return { sent, items, error: String(e?.message ?? e) };
      }
      sent++;
      items += batch.length;
      // Keep the last turn re-sendable (same ids) until another turn starts after it.
      const next = turns[i + 1];
      cursor.offset = next ? next.offset : t.offset;
      cursor.model = t.model ?? cursor.model;
      writeFileSync(cursorPath(cfg, sid), JSON.stringify({ ...cursor, session }));
    }
    return { sent, items, sessionId: sid };
  } catch (e) {
    log.error(`session ${sid}: collector failed: ${e?.stack ?? e}`);
    return { sent: 0, error: String(e) };
  } finally {
    release();
  }
}

// ---------------------------------------------------------------- hook entry

/**
 * Handle a Stop hook payload (stdin JSON) or a notify payload
 * (`agent-turn-complete`, argv JSON). Never throws; starts the collector.
 */
export function handleTrigger(payload, { env = process.env, mode = 'background', cfg } = {}) {
  cfg = cfg ?? config(env);
  const log = makeLogger(cfg, 'hook');
  try {
    if (cfg.disabled || !payload || typeof payload !== 'object') return { started: false };
    const sid = payload.session_id ?? payload['thread-id'] ?? payload.thread_id ?? payload['session-id'];
    if (payload.type && payload.type !== 'agent-turn-complete') return { started: false };
    if (payload.hook_event_name && payload.hook_event_name !== 'Stop') return { started: false };
    let rollout = payload.transcript_path ?? payload.rollout_path;
    if (!rollout || !existsSync(rollout)) rollout = findRollout(sid, env);
    if (!rollout) {
      log.warn(`no rollout file found for session ${sid}`);
      return { started: false };
    }
    if (mode === 'inline') return { started: true, result: flushRollout(cfg, rollout, { sessionId: sid }) };
    if (mode === 'background') {
      const script = join(dirname(fileURLToPath(import.meta.url)), '..', 'hooks', 'weft-codex-hook.mjs');
      const child = spawn(process.execPath, [script, '--flush', rollout, sid ?? ''], { detached: true, stdio: 'ignore', env });
      child.unref();
    }
    return { started: true, rollout };
  } catch (e) {
    log.warn(`trigger failed: ${e?.message ?? e}`);
    return { started: false, error: String(e) };
  }
}
