// Claude Code sessions -> Weft agent conversations.
//
// Hooks only append a small record to a per-session event log (no network on
// the hot path). On Stop / SessionEnd (and when a new prompt arrives after an
// unfinished turn) a detached worker reads the event log and the session
// transcript, builds the calls and sends them with a short timeout:
//
//   invoke_agent claude-code         one per prompt (a turn; conversation = session id)
//   ├─ chat <model>                  one per assistant message in the transcript, with usage
//   ├─ execute_tool <tool>           one per PreToolUse/PostToolUse pair
//   └─ invoke_agent <subagent_type>  Task/Agent tool calls; the sub-agent's tools and LLM calls nest here
//
// Call ids are derived from session, prompt and tool-use ids, so a failed send
// is retried by the next flush without duplicates.

import { spawn } from 'node:child_process';
import { closeSync, existsSync, mkdirSync, openSync, readFileSync, statSync, unlinkSync, writeFileSync, appendFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { spanItems, chatOutput, iso, loadConfig, makeLogger, parseMaybeJson, readJsonl, sendItems, shortText, stableId, toolDisplay, usageSummary } from './weft.mjs';

export const PLUGIN = 'weft-claude-code';
export const PLUGIN_VERSION = '0.1.0';
const AGENT_TOOLS = new Set(['Task', 'Agent']);

export function config(env = process.env, overrides = {}) {
  return loadConfig({ plugin: PLUGIN, defaultAgent: 'claude-code', stateDirEnv: 'WEFT_CLAUDE_CODE_STATE_DIR', env, overrides });
}

const sessionsDir = (cfg) => join(cfg.stateDir, 'sessions');
const safe = (sid) => String(sid || 'unknown').replace(/[^A-Za-z0-9_.-]/g, '_').slice(0, 128);
export const eventsPath = (cfg, sid) => join(sessionsDir(cfg), `${safe(sid)}.events.jsonl`);
const cursorPath = (cfg, sid) => join(sessionsDir(cfg), `${safe(sid)}.cursor.json`);
const lockPath = (cfg, sid) => join(sessionsDir(cfg), `${safe(sid)}.lock`);

// ---------------------------------------------------------------- hook side

const KEEP = ['session_id', 'transcript_path', 'cwd', 'permission_mode', 'source', 'model', 'prompt', 'tool_name', 'tool_input', 'tool_use_id', 'tool_response',
  'error', 'is_interrupt', 'stop_hook_active', 'agent_id', 'agent_type', 'agent_transcript_path', 'reason', 'last_assistant_message'];

/** Turn a hook payload into the record appended to the session log (cheap, local). */
export function hookRecord(payload, now = Date.now()) {
  const rec = { ev: payload.hook_event_name, ts: iso(now) };
  for (const k of KEEP) if (payload[k] !== undefined) rec[k] = payload[k];
  if (rec.ev === 'UserPromptSubmit' && payload.transcript_path) {
    try {
      rec.transcript_offset = statSync(payload.transcript_path).size;
    } catch {
      rec.transcript_offset = 0;
    }
  }
  return rec;
}

/**
 * Handle one hook invocation: append the record, and start a flush when a turn
 * may have ended. Never throws; returns what it did (for tests).
 */
export function handleHook(payload, { env = process.env, now = Date.now(), flush = 'background', cfg } = {}) {
  cfg = cfg ?? config(env);
  const log = makeLogger(cfg, 'hook');
  try {
    if (cfg.disabled || !payload || typeof payload !== 'object' || !payload.hook_event_name) return { recorded: false };
    const sid = payload.session_id;
    if (!sid) return { recorded: false };
    const rec = hookRecord(payload, now);
    const path = eventsPath(cfg, sid);
    let prevOpen = false;
    if (rec.ev === 'UserPromptSubmit') prevOpen = hasOpenTurn(path);
    mkdirSync(dirname(path), { recursive: true });
    appendFileSync(path, `${JSON.stringify(rec)}\n`);
    const trigger = rec.ev === 'Stop' || rec.ev === 'SessionEnd' || (rec.ev === 'UserPromptSubmit' && prevOpen);
    if (trigger) {
      if (flush === 'background') spawnFlush(sid, env);
      else if (flush === 'inline') return { recorded: true, flush: flushSession(cfg, sid) };
    }
    return { recorded: true, trigger };
  } catch (e) {
    log.warn(`hook ${payload?.hook_event_name}: ${e?.message ?? e}`);
    return { recorded: false, error: String(e) };
  }
}

function hasOpenTurn(path) {
  try {
    const text = readFileSync(path, 'utf8');
    const lines = text.trimEnd().split('\n').reverse();
    for (const l of lines) {
      const ev = JSON.parse(l).ev;
      if (ev === 'Stop' || ev === 'SessionEnd') return false;
      if (ev === 'UserPromptSubmit') return true;
    }
  } catch {
    /* no log yet */
  }
  return false;
}

/** Start the flush worker detached, so the hook returns at once. */
export function spawnFlush(sid, env = process.env) {
  const script = join(dirname(fileURLToPath(import.meta.url)), '..', 'hooks', 'weft-hook.mjs');
  const child = spawn(process.execPath, [script, '--flush', sid], { detached: true, stdio: 'ignore', env });
  child.unref();
}

// ---------------------------------------------------------------- worker side

function readEvents(cfg, sid) {
  try {
    return readFileSync(eventsPath(cfg, sid), 'utf8')
      .split('\n')
      .filter((l) => l.trim())
      .map((l) => {
        try {
          return JSON.parse(l);
        } catch {
          return null;
        }
      })
      .filter(Boolean);
  } catch {
    return [];
  }
}

function readCursor(cfg, sid) {
  try {
    return JSON.parse(readFileSync(cursorPath(cfg, sid), 'utf8'));
  } catch {
    return { next: 0, session: {} };
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
        /* already gone */
      }
    };
  } catch {
    return null;
  }
}

/** Assistant transcript entries grouped by message id, with the user content before each. */
export function transcriptMessages(entries) {
  const out = [];
  const byId = new Map();
  let lastBoundary = null;
  let pendingInput = [];
  for (const e of entries) {
    if (!e || typeof e !== 'object') continue;
    const m = e.message;
    if (e.type === 'assistant' && m && m.role === 'assistant') {
      const id = m.id ?? e.uuid;
      let rec = byId.get(id);
      if (!rec) {
        rec = { id, model: m.model, blocks: [], usage: null, stopReason: null, startedAt: lastBoundary ?? e.timestamp, endedAt: e.timestamp, sidechain: !!e.isSidechain, agentId: e.agentId, input: pendingInput, requestId: e.requestId };
        pendingInput = [];
        byId.set(id, rec);
        out.push(rec);
      }
      for (const b of Array.isArray(m.content) ? m.content : m.content ? [{ type: 'text', text: String(m.content) }] : []) rec.blocks.push(b);
      if (m.usage) rec.usage = m.usage;
      if (m.stop_reason) rec.stopReason = m.stop_reason;
      rec.endedAt = e.timestamp ?? rec.endedAt;
      lastBoundary = e.timestamp ?? lastBoundary;
    } else if (e.type === 'user' && m) {
      const content = m.content;
      pendingInput.push({ role: 'user', content: Array.isArray(content) ? content : String(content ?? '') });
      lastBoundary = e.timestamp ?? lastBoundary;
    }
  }
  return out;
}

function claudeUsage(u) {
  if (!u) return null;
  const cacheRead = Number(u.cache_read_input_tokens ?? 0) || 0;
  const cacheWrite = Number(u.cache_creation_input_tokens ?? 0) || 0;
  return { prompt: (Number(u.input_tokens ?? 0) || 0) + cacheRead + cacheWrite, completion: Number(u.output_tokens ?? 0) || 0, cacheRead, cacheWrite };
}

function lastText(blocks) {
  for (let i = blocks.length - 1; i >= 0; i--) if (blocks[i]?.type === 'text' && blocks[i].text) return blocks[i].text;
  return '';
}

function toolResultText(resp) {
  if (resp == null) return resp;
  if (typeof resp === 'string') return parseMaybeJson(resp);
  return resp;
}

/**
 * Split the session's events into turns (one per UserPromptSubmit). A turn
 * is closed by Stop, by the next prompt or by SessionEnd.
 */
export function splitTurns(events, start = 0) {
  const turns = [];
  let cur = null;
  for (let i = start; i < events.length; i++) {
    const e = events[i];
    if (e.ev === 'UserPromptSubmit') {
      if (cur) {
        cur.closed = cur.closed ?? 'next_prompt';
        cur.endIndex = i;
      }
      cur = { index: i, prompt: e, events: [], closed: null, endIndex: null };
      turns.push(cur);
    } else if (cur) {
      if (cur.closed && e.ev !== 'SessionEnd') {
        // events after Stop and before the next prompt (e.g. SubagentStop): keep them with the turn
        cur.events.push(e);
        continue;
      }
      cur.events.push(e);
      if (e.ev === 'Stop') {
        // A Stop hook may make Claude continue (stop_hook_active); the last Stop wins.
        cur.closed = 'stop';
        cur.stop = e;
      }
      if (e.ev === 'SessionEnd') {
        cur.closed = cur.closed ?? 'session_end';
        cur.end = cur.end ?? e;
      }
    }
  }
  return turns;
}

/** Build the calls (batch items) for one closed turn. */
export function buildTurnItems(cfg, sid, turn, ctx = {}) {
  const items = [];
  const prompt = turn.prompt;
  const turnId = stableId(sid, 'turn', prompt.ts, prompt.prompt ?? '');
  const traceId = turnId;
  const thread = { sessionId: sid, turnId };
  const startedAt = prompt.ts;
  const stopEv = turn.stop ?? turn.events.findLast?.((e) => e.ev === 'SessionEnd') ?? turn.events[turn.events.length - 1];
  const endedAt = stopEv?.ts ?? ctx.endTs ?? prompt.ts;
  const conversationName = ctx.conversationName;
  const common = (extra = {}) => ({ 'gen_ai.conversation.id': sid, 'gen_ai.conversation.name': conversationName, ...extra });

  // Transcript entries for this turn.
  const main = ctx.transcript ?? [];
  const msgs = transcriptMessages(main);
  const mainMsgs = msgs.filter((m) => !m.sidechain);
  const mainToolIds = new Set();
  for (const m of mainMsgs) for (const b of m.blocks) if (b?.type === 'tool_use' && b.id) mainToolIds.add(b.id);
  const model = mainMsgs.find((m) => m.model && m.model !== '<synthetic>')?.model ?? ctx.session?.model;

  // Tools from hook events.
  const tools = new Map();
  for (const e of turn.events) {
    if (e.ev === 'PreToolUse' && e.tool_use_id) {
      tools.set(e.tool_use_id, { id: e.tool_use_id, name: e.tool_name, input: e.tool_input, start: e.ts, agentId: e.agent_id });
    } else if ((e.ev === 'PostToolUse' || e.ev === 'PostToolUseFailure') && e.tool_use_id) {
      const t = tools.get(e.tool_use_id) ?? { id: e.tool_use_id, name: e.tool_name, input: e.tool_input, start: e.ts, agentId: e.agent_id };
      t.end = e.ts;
      t.response = e.tool_response;
      if (e.ev === 'PostToolUseFailure') t.error = e.error ?? (e.is_interrupt ? 'interrupted' : 'tool failed');
      else if (e.tool_response && typeof e.tool_response === 'object' && (e.tool_response.is_error || e.tool_response.isError)) t.error = shortText(e.tool_response.error ?? e.tool_response.content ?? 'tool error', 500);
      tools.set(e.tool_use_id, t);
    }
  }
  const subagentStops = turn.events.filter((e) => e.ev === 'SubagentStop');

  // Sub-agents: Task/Agent tools. Each gets an invoke_agent span.
  const agentTools = [...tools.values()].filter((t) => AGENT_TOOLS.has(t.name));
  const spanOf = new Map(); // tool_use_id -> call id
  for (const t of tools.values()) spanOf.set(t.id, stableId(sid, 'tool', t.id));

  // Which sub-agent ran a tool: an explicit agent id, else the open Task whose window holds it (not a main-agent tool).
  const agentById = new Map();
  for (const st of subagentStops) {
    if (!st.agent_id) continue;
    // match the sub-agent transcript's first prompt to a Task prompt
    const tr = ctx.agentTranscripts?.[st.agent_id] ?? [];
    const firstUser = tr.find((x) => x?.type === 'user')?.message?.content;
    const text = typeof firstUser === 'string' ? firstUser : Array.isArray(firstUser) ? firstUser.map((b) => b?.text ?? '').join('') : '';
    const task = agentTools.find((t) => text && t.input?.prompt && text.includes(String(t.input.prompt).slice(0, 200))) ?? agentTools.find((t) => !agentById.has(t.id) && ![...agentById.values()].includes(t));
    if (task) agentById.set(st.agent_id, task);
  }
  const ownerOf = (t) => {
    if (t.agentId && agentById.has(t.agentId)) return agentById.get(t.agentId);
    if (mainToolIds.has(t.id) || AGENT_TOOLS.has(t.name)) return null;
    if (!mainToolIds.size && !t.agentId) return null; // no transcript: keep at the top level
    const open = agentTools.filter((a) => a.start <= t.start && (!a.end || a.end >= (t.end ?? t.start)));
    return open.length ? open[open.length - 1] : null;
  };

  // Turn.
  const finalText = turn.stop?.last_assistant_message ?? lastText(mainMsgs.flatMap((m) => m.blocks));
  items.push(
    ...spanItems(cfg, {
      id: turnId,
      traceId,
      parentId: null,
      ...thread,
      name: 'claude_code.turn',
      kind: 'agent',
      displayName: `invoke_agent ${cfg.agentName}`,
      startedAt,
      endedAt,
      inputs: { messages: [{ role: 'user', content: prompt.prompt ?? '' }] },
      output: finalText ? { messages: [{ role: 'assistant', content: finalText }] } : null,
      error: turn.stop ? undefined : turn.closed === 'session_end' ? 'session ended before the turn finished' : turn.closed === 'next_prompt' ? 'interrupted: a new prompt was submitted' : undefined,
      genai: common({ 'gen_ai.operation.name': 'invoke_agent', 'gen_ai.agent.name': cfg.agentName, 'gen_ai.request.model': model, 'gen_ai.provider.name': 'anthropic' }),
      attributes: { claude_code: { cwd: prompt.cwd, permission_mode: prompt.permission_mode, session_source: ctx.session?.source, transcript_path: prompt.transcript_path } },
      pluginVersion: PLUGIN_VERSION,
    }),
  );

  // Tools and sub-agents.
  for (const t of tools.values()) {
    const owner = ownerOf(t);
    const parentId = owner ? spanOf.get(owner.id) : turnId;
    const isAgent = AGENT_TOOLS.has(t.name);
    const id = spanOf.get(t.id);
    const result = toolResultText(t.response);
    const exception = t.error ?? (t.end ? undefined : 'tool did not finish before the turn ended');
    if (isAgent) {
      const subType = t.input?.subagent_type ?? t.name;
      items.push(
        ...spanItems(cfg, {
          id,
          traceId,
          parentId,
          ...thread,
          name: 'claude_code.subagent',
          kind: 'agent',
          displayName: `invoke_agent ${subType}`,
          startedAt: t.start,
          endedAt: t.end ?? endedAt,
          inputs: { messages: [{ role: 'user', content: t.input?.prompt ?? '' }], description: t.input?.description },
          output: result != null ? { messages: [{ role: 'assistant', content: typeof result === 'string' ? result : Array.isArray(result?.content) ? result.content.map((b) => b?.text ?? '').join('') : JSON.stringify(result) }] } : null,
          error: exception,
          genai: common({ 'gen_ai.operation.name': 'invoke_agent', 'gen_ai.agent.name': subType, 'gen_ai.tool.name': t.name, 'gen_ai.tool.call.id': t.id }),
          pluginVersion: PLUGIN_VERSION,
        }),
      );
    } else {
      const genai = common({ 'gen_ai.operation.name': 'execute_tool', 'gen_ai.tool.name': t.name, 'gen_ai.tool.call.id': t.id });
      if (String(t.name).startsWith('mcp__')) {
        genai['gen_ai.tool.type'] = 'mcp';
        genai['mcp.server.name'] = String(t.name).split('__')[1];
      }
      items.push(
        ...spanItems(cfg, {
          id,
          traceId,
          parentId,
          ...thread,
          name: `claude_code.tool.${t.name}`,
          kind: 'tool',
          displayName: toolDisplay(t.name, t.input),
          startedAt: t.start,
          endedAt: t.end ?? endedAt,
          inputs: t.input && typeof t.input === 'object' && !Array.isArray(t.input) ? t.input : { input: t.input },
          output: result ?? null,
          error: exception,
          genai,
          pluginVersion: PLUGIN_VERSION,
        }),
      );
    }
  }

  // LLM calls: main transcript, sidechain entries, and sub-agent transcripts.
  const llmCall = (m, parentId) => {
    const u = claudeUsage(m.usage);
    const text = lastText(m.blocks);
    const uses = m.blocks.filter((b) => b?.type === 'tool_use');
    items.push(
      ...spanItems(cfg, {
        id: stableId(sid, 'llm', m.id),
        traceId,
        parentId,
        ...thread,
        name: 'claude_code.llm',
        kind: 'llm',
        displayName: text ? shortText(text) : uses.length ? `→ ${uses.map((b) => b.name).join(', ')}` : `chat ${m.model ?? ''}`.trim(),
        startedAt: m.startedAt ?? m.endedAt,
        endedAt: m.endedAt,
        inputs: { model: m.model, messages: m.input },
        output: chatOutput(m.model, m.blocks, m.stopReason),
        summary: usageSummary(m.model, u),
        genai: common({ 'gen_ai.operation.name': 'chat', 'gen_ai.request.model': m.model, 'gen_ai.provider.name': 'anthropic', 'gen_ai.response.id': m.id }),
        pluginVersion: PLUGIN_VERSION,
      }),
    );
  };
  for (const m of mainMsgs) llmCall(m, turnId);
  for (const m of msgs.filter((x) => x.sidechain)) {
    const owner = agentTools.length ? (m.agentId && agentById.get(m.agentId)) || agentTools.findLast((a) => !a.end || a.end >= m.endedAt) || agentTools[agentTools.length - 1] : null;
    llmCall(m, owner ? spanOf.get(owner.id) : turnId);
  }
  for (const [agentId, tr] of Object.entries(ctx.agentTranscripts ?? {})) {
    const owner = agentById.get(agentId);
    if (!owner) continue;
    for (const m of transcriptMessages(tr)) llmCall(m, spanOf.get(owner.id));
  }
  return items;
}

/** Transcript entries of one turn: from its offset up to the next turn's offset. */
function turnTranscript(turn, nextTurn) {
  const path = turn.prompt.transcript_path;
  if (!path) return [];
  const { lines } = readJsonl(path, turn.prompt.transcript_offset ?? 0);
  const end = nextTurn?.prompt?.transcript_path === path ? nextTurn.prompt.transcript_offset : undefined;
  return end !== undefined ? lines.filter((l) => l.__offset < end) : lines;
}

/**
 * Send every closed, unsent turn of a session. Returns
 * `{ sent: <turns>, items: <batch items>, error? }`. The cursor only advances
 * past turns the server accepted.
 */
export async function flushSession(cfg, sid, { fetchImpl, transcriptDelayMs = 0 } = {}) {
  const log = makeLogger(cfg, 'flush');
  const release = acquireLock(cfg, sid);
  if (!release) return { sent: 0, items: 0, locked: true };
  try {
    if (transcriptDelayMs) await new Promise((r) => setTimeout(r, transcriptDelayMs));
    const events = readEvents(cfg, sid);
    const cursor = readCursor(cfg, sid);
    const session = cursor.session ?? {};
    for (const e of events.slice(0, events.length)) {
      if (e.ev === 'SessionStart') Object.assign(session, { source: e.source, model: e.model ?? session.model, cwd: e.cwd });
    }
    const turns = splitTurns(events, cursor.next ?? 0);
    const sessionEnded = events.some((e, i) => i >= (cursor.next ?? 0) && e.ev === 'SessionEnd');
    let sent = 0;
    let itemsSent = 0;
    for (let i = 0; i < turns.length; i++) {
      const t = turns[i];
      if (!t.closed) break;
      if (!session.firstPrompt && t.prompt.prompt) session.firstPrompt = shortText(t.prompt.prompt, 80);
      const agentTranscripts = {};
      for (const st of t.events.filter((e) => e.ev === 'SubagentStop' && e.agent_id && e.agent_transcript_path)) {
        agentTranscripts[st.agent_id] = readJsonl(st.agent_transcript_path, 0).lines;
      }
      const items = buildTurnItems(cfg, sid, t, { transcript: turnTranscript(t, turns[i + 1]), agentTranscripts, session, conversationName: session.firstPrompt });
      try {
        await sendItems(cfg, items, { fetchImpl });
      } catch (e) {
        log.warn(`session ${sid}: send failed, will retry on the next flush: ${e?.message ?? e}`);
        writeFileSync(cursorPath(cfg, sid), JSON.stringify({ ...cursor, session }));
        return { sent, items: itemsSent, error: String(e?.message ?? e) };
      }
      sent++;
      itemsSent += items.length;
      // The last turn stays re-sendable until the next prompt: a Stop-hook continuation
      // or late transcript lines are picked up by the next flush (same call ids).
      cursor.next = turns[i + 1]?.index ?? t.index;
      writeFileSync(cursorPath(cfg, sid), JSON.stringify({ ...cursor, session }));
      log.debug(`session ${sid}: sent turn ${t.index} (${items.length / 2} calls)`);
    }
    if (sessionEnded && turns.every((t) => t.closed)) {
      for (const p of [eventsPath(cfg, sid), cursorPath(cfg, sid)]) {
        try {
          if (existsSync(p)) unlinkSync(p);
        } catch {
          /* ignore */
        }
      }
    }
    return { sent, items: itemsSent };
  } catch (e) {
    log.error(`session ${sid}: flush failed: ${e?.stack ?? e}`);
    return { sent: 0, items: 0, error: String(e) };
  } finally {
    release();
  }
}
