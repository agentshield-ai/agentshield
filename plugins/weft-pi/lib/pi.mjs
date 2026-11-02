// Pi coding-agent sessions -> Weft agent conversations.
//
// A Pi extension runs inside the `pi` process (interactive, print, JSON and
// RPC modes, and the SDK's AgentSession), so run state is kept in memory and
// finished calls are sent from a background timer; handlers only update state
// and return at once. They never return a value, so they cannot block a tool
// call, change a message or alter the system prompt.
//
//   invoke_agent pi            one per prompt (agent_start .. agent_end): a turn;
//                              conversation = the Pi session id
//   ├─ chat <model>            one per LLM response (turn_start / context .. assistant message_end),
//   │                          with the context messages, the response and token usage
//   └─ execute_tool <tool>     tool_execution_start .. tool_execution_end

import { callItems, chatOutput, loadConfig, newId, sendItems, shortText, stableId, toolDisplay, truncate, usageSummary } from './weft.mjs';

export const PLUGIN = 'weft-pi';
export const PLUGIN_VERSION = '0.1.0';

/** Configuration: explicit options, then WEFT_* environment, then ~/.weft-pi/settings.json. */
export function resolveConfig(options = {}, env = process.env) {
  const { flushIntervalMs, maxQueue, maxContextMessages, ...overrides } = options ?? {};
  const cfg = loadConfig({ plugin: PLUGIN, defaultAgent: 'pi', stateDirEnv: 'WEFT_PI_STATE_DIR', env, overrides });
  cfg.flushIntervalMs = Number(flushIntervalMs ?? env.WEFT_FLUSH_INTERVAL_MS ?? 1000) || 1000;
  cfg.maxQueue = Number(maxQueue ?? 20000) || 20000;
  cfg.maxContextMessages = Number(maxContextMessages ?? 40) || 40;
  return cfg;
}

// ---------------------------------------------------------------- messages

function contentText(content) {
  if (typeof content === 'string') return content;
  if (!Array.isArray(content)) return '';
  return content
    .filter((b) => b?.type === 'text')
    .map((b) => b.text ?? '')
    .join('\n');
}

/** Pi content blocks -> chat content (images replaced by a placeholder; base64 is never sent). */
function chatContent(content) {
  if (typeof content === 'string') return content;
  if (!Array.isArray(content)) return '';
  const out = [];
  for (const b of content) {
    if (b?.type === 'text') out.push({ type: 'text', text: b.text ?? '' });
    else if (b?.type === 'image') out.push({ type: 'text', text: `[image ${b.mimeType ?? ''}]`.trim() });
    else if (b?.type === 'thinking') out.push({ type: 'thinking', thinking: b.thinking ?? '' });
  }
  return out.length === 1 && out[0].type === 'text' ? out[0].text : out;
}

function toolCalls(content) {
  if (!Array.isArray(content)) return [];
  return content.filter((b) => b?.type === 'toolCall').map((b) => ({ id: b.id, type: 'function', function: { name: b.name, arguments: JSON.stringify(b.arguments ?? {}) } }));
}

/** A Pi AgentMessage -> an OpenAI-style chat message (null for UI-only messages). */
export function chatMessage(m) {
  if (!m || typeof m !== 'object') return null;
  switch (m.role) {
    case 'user':
      return { role: 'user', content: chatContent(m.content) };
    case 'assistant': {
      const msg = { role: 'assistant', content: chatContent((m.content ?? []).filter((b) => b?.type !== 'toolCall')) };
      const tc = toolCalls(m.content);
      if (tc.length) msg.tool_calls = tc;
      return msg;
    }
    case 'toolResult':
      return { role: 'tool', tool_call_id: m.toolCallId, name: m.toolName, content: contentText(m.content) };
    case 'bashExecution':
      return { role: 'user', content: `$ ${m.command ?? ''}\n${m.output ?? ''}`.trim() };
    case 'custom':
      return m.content ? { role: 'user', content: chatContent(m.content) } : null;
    case 'branchSummary':
    case 'compactionSummary':
      return { role: 'user', content: m.summary ?? '' };
    default:
      return null;
  }
}

function resultValue(result) {
  if (result == null) return null;
  if (typeof result !== 'object') return result;
  const text = contentText(result.content);
  const out = {};
  if (text) out.text = text;
  if (Array.isArray(result.content) && result.content.some((b) => b?.type === 'image')) out.images = result.content.filter((b) => b?.type === 'image').length;
  if (result.details !== undefined && result.details !== null) out.details = result.details;
  return Object.keys(out).length ? out : result;
}

function errText(e) {
  if (!e) return undefined;
  return typeof e === 'string' ? e : e.message ?? JSON.stringify(e);
}

function sessionInfo(ctx) {
  const sm = ctx?.sessionManager;
  const call = (fn) => {
    try {
      return typeof sm?.[fn] === 'function' ? sm[fn]() : undefined;
    } catch {
      return undefined;
    }
  };
  return { id: call('getSessionId'), name: call('getSessionName'), file: call('getSessionFile'), cwd: ctx?.cwd ?? call('getCwd') };
}

// ---------------------------------------------------------------- tracer

/** In-memory state machine from Pi extension events to Weft calls. */
export class WeftPiTracer {
  constructor(cfg, { logger = console, fetchImpl, now = () => Date.now() } = {}) {
    this.cfg = cfg;
    this.log = logger;
    this.fetchImpl = fetchImpl;
    this.now = now;
    this.session = { id: null, name: null, file: null, cwd: null, reason: null, previousFile: null, firstPrompt: null };
    this.model = null; // { id, provider } from ctx.model / model_select
    this.pending = null; // before_agent_start data for the next run
    this.run = null;
    this.runCount = 0;
    this.queue = [];
    this.sending = null;
    this.lastWarn = 0;
    this.stats = { sent: 0, failed: 0, dropped: 0, runs: 0 };
  }

  // ------------------------------------------------------------ helpers
  syncSession(ctx) {
    const s = sessionInfo(ctx);
    if (s.id && s.id !== this.session.id) {
      this.session = { ...this.session, id: s.id, firstPrompt: null };
    }
    this.session.name = s.name ?? this.session.name;
    this.session.file = s.file ?? this.session.file;
    this.session.cwd = s.cwd ?? this.session.cwd;
    if (!this.session.id) this.session.id = newId();
    const m = ctx?.model;
    if (m && (m.id || m.name)) this.model = { id: m.id ?? m.name, provider: m.provider ?? m.api ?? this.model?.provider };
  }

  ensureRun(ctx) {
    if (this.run) return this.run;
    this.syncSession(ctx);
    const p = this.pending ?? {};
    this.pending = null;
    this.runCount++;
    const id = stableId('pi', this.session.id, p.at ?? this.now(), this.runCount);
    this.run = {
      id,
      sessionId: this.session.id,
      startedAt: p.at ?? this.now(),
      prompt: p.prompt ?? null,
      images: p.images ?? 0,
      systemPrompt: p.systemPrompt ?? null,
      model: this.model?.id,
      provider: this.model?.provider,
      llms: [],
      tools: new Map(),
      toolOrder: [],
      currentLlm: null,
      endMessages: null,
    };
    if (!this.session.firstPrompt && p.prompt) this.session.firstPrompt = shortText(p.prompt, 80);
    return this.run;
  }

  startLlm(run, at) {
    const llm = { key: run.llms.length, startedAt: at ?? this.now(), messages: null, message: null };
    run.llms.push(llm);
    run.currentLlm = llm;
    return llm;
  }

  // ------------------------------------------------------------ events
  onSessionStart(event = {}, ctx = {}) {
    this.syncSession(ctx);
    this.session.reason = event.reason ?? null;
    this.session.previousFile = event.previousSessionFile ?? null;
  }

  onModelSelect(event = {}) {
    const m = event.model;
    if (m) this.model = { id: m.id ?? m.name, provider: m.provider ?? m.api };
  }

  onBeforeAgentStart(event = {}, ctx = {}) {
    this.syncSession(ctx);
    // A prompt while a run is still open (no agent_end): close the old one.
    if (this.run) this.finishRun({ reason: 'superseded' });
    this.pending = { at: this.now(), prompt: event.prompt ?? null, images: Array.isArray(event.images) ? event.images.length : 0, systemPrompt: event.systemPrompt ?? null };
  }

  onAgentStart(_event = {}, ctx = {}) {
    if (this.run && !this.pending) return; // already started by an earlier event
    if (this.run) this.finishRun({ reason: 'superseded' });
    this.ensureRun(ctx);
  }

  onTurnStart(event = {}, ctx = {}) {
    const run = this.ensureRun(ctx);
    const llm = this.startLlm(run, typeof event.timestamp === 'number' ? event.timestamp : this.now());
    llm.turnIndex = event.turnIndex;
  }

  onContext(event = {}, ctx = {}) {
    const run = this.ensureRun(ctx);
    let llm = run.currentLlm;
    if (!llm || llm.message) llm = this.startLlm(run);
    if (Array.isArray(event.messages)) {
      const msgs = event.messages.map(chatMessage).filter(Boolean);
      llm.messages = msgs.slice(-this.cfg.maxContextMessages);
      llm.droppedMessages = Math.max(0, msgs.length - llm.messages.length);
    }
  }

  onMessageEnd(event = {}, ctx = {}) {
    const m = event.message;
    if (!m) return;
    const run = this.ensureRun(ctx);
    if (m.role === 'user' && run.prompt == null) {
      run.prompt = contentText(m.content);
      if (!this.session.firstPrompt && run.prompt) this.session.firstPrompt = shortText(run.prompt, 80);
      return;
    }
    if (m.role !== 'assistant') return;
    let llm = run.currentLlm;
    if (!llm || llm.message) llm = this.startLlm(run, typeof m.timestamp === 'number' ? Math.min(m.timestamp, this.now()) : this.now());
    llm.message = m;
    llm.endedAt = this.now();
    if (m.model) {
      run.model = run.model ?? m.model;
      this.model = { id: m.model, provider: m.provider ?? this.model?.provider };
    }
    if (m.provider) run.provider = run.provider ?? m.provider;
  }

  onToolExecutionStart(event = {}, ctx = {}) {
    const run = this.ensureRun(ctx);
    const id = event.toolCallId ?? `tool-${run.toolOrder.length}`;
    if (!run.tools.has(id)) run.toolOrder.push(id);
    run.tools.set(id, { id, name: event.toolName ?? 'tool', args: event.args, startedAt: this.now(), llm: run.currentLlm?.key });
  }

  onToolExecutionEnd(event = {}, ctx = {}) {
    const run = this.ensureRun(ctx);
    const id = event.toolCallId ?? run.toolOrder.find((k) => !run.tools.get(k).endedAt && run.tools.get(k).name === event.toolName);
    let t = id ? run.tools.get(id) : null;
    if (!t) {
      t = { id: id ?? `tool-${run.toolOrder.length}`, name: event.toolName ?? 'tool', args: event.args, startedAt: this.now() };
      run.tools.set(t.id, t);
      run.toolOrder.push(t.id);
    }
    t.endedAt = this.now();
    t.result = event.result;
    t.isError = !!event.isError;
  }

  onAgentEnd(event = {}, ctx = {}) {
    if (!this.run) return;
    this.run.endMessages = event.messages ?? null;
    this.finishRun({ reason: 'agent_end' });
    this.flush();
  }

  // ------------------------------------------------------------ building calls
  finishRun({ reason } = {}) {
    const run = this.run;
    if (!run) return;
    this.run = null;
    this.stats.runs++;
    try {
      this.enqueue(this.runItems(run, reason));
    } catch (e) {
      this.warn(`could not build calls for a run: ${e?.message ?? e}`);
    }
  }

  runItems(run, reason) {
    const cfg = this.cfg;
    const endedAt = this.now();
    const agentName = cfg.agentName || 'pi';
    const conv = this.session.id;
    const base = { traceId: run.id, threadId: conv, turnId: run.id, pluginVersion: PLUGIN_VERSION };
    const common = { 'gen_ai.conversation.id': conv, 'gen_ai.conversation.name': this.session.name || this.session.firstPrompt || undefined };
    const items = [];
    const assistants = run.llms.map((l) => l.message).filter(Boolean);
    const last = assistants[assistants.length - 1];
    const finalText = last ? contentText((last.content ?? []).filter((b) => b?.type === 'text')) : '';
    const failed = last && (last.stopReason === 'error' || last.stopReason === 'aborted');
    const exception = reason === 'agent_end' || reason === undefined ? (failed ? last.errorMessage ?? `run ${last.stopReason}` : undefined) : `run closed without agent_end (${reason})`;
    const totals = { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, cost: 0 };
    for (const m of assistants) {
      totals.input += m.usage?.input ?? 0;
      totals.output += m.usage?.output ?? 0;
      totals.cacheRead += m.usage?.cacheRead ?? 0;
      totals.cacheWrite += m.usage?.cacheWrite ?? 0;
      totals.cost += m.usage?.cost?.total ?? 0;
    }
    const userContent = run.images ? [{ type: 'text', text: run.prompt ?? '' }, { type: 'text', text: `[${run.images} image(s)]` }] : run.prompt ?? '';
    items.push(
      ...callItems(cfg, {
        ...base,
        id: run.id,
        parentId: null,
        opName: 'pi.agent_run',
        kind: 'agent',
        displayName: `invoke_agent ${agentName}`,
        startedAt: run.startedAt,
        endedAt,
        inputs: { messages: [...(run.systemPrompt ? [{ role: 'system', content: run.systemPrompt }] : []), { role: 'user', content: userContent }] },
        output: finalText ? { messages: [{ role: 'assistant', content: finalText }], stop_reason: last?.stopReason ?? null } : last ? { stop_reason: last.stopReason } : null,
        exception,
        genai: {
          ...common,
          'gen_ai.operation.name': 'invoke_agent',
          'gen_ai.agent.name': agentName,
          'gen_ai.request.model': run.model,
          'gen_ai.provider.name': run.provider,
        },
        attributes: {
          pi: {
            session_id: conv,
            session_file: this.session.file ?? undefined,
            session_reason: this.session.reason ?? undefined,
            parent_session_file: this.session.previousFile ?? undefined,
            cwd: this.session.cwd ?? undefined,
            llm_calls: assistants.length,
            tool_calls: run.toolOrder.length,
            cost_usd: totals.cost || undefined,
            closed_by: reason ?? 'agent_end',
          },
        },
      }),
    );

    run.llms.forEach((l) => {
      const m = l.message;
      const model = m?.model ?? run.model;
      const provider = m?.provider ?? run.provider;
      const messages = [];
      if (run.systemPrompt) messages.push({ role: 'system', content: run.systemPrompt });
      if (l.messages) messages.push(...l.messages);
      else if (l.key === 0 && run.prompt != null) messages.push({ role: 'user', content: run.prompt });
      const u = m?.usage;
      const usage = u ? { prompt: (u.input ?? 0) + (u.cacheRead ?? 0) + (u.cacheWrite ?? 0), completion: u.output ?? 0, total: u.totalTokens, cacheRead: u.cacheRead, cacheWrite: u.cacheWrite } : null;
      const text = m ? contentText((m.content ?? []).filter((b) => b?.type === 'text')) : '';
      const out = m ? chatOutput(model, chatContent((m.content ?? []).filter((b) => b?.type !== 'toolCall')), m.stopReason ?? null) : null;
      const tc = m ? toolCalls(m.content) : [];
      if (out && tc.length) out.choices[0].message.tool_calls = tc;
      const llmErr = m && (m.stopReason === 'error' || m.stopReason === 'aborted') ? m.errorMessage ?? `LLM call ${m.stopReason}` : m ? undefined : 'no response before the run ended';
      items.push(
        ...callItems(cfg, {
          ...base,
          id: stableId('pi', run.id, 'llm', l.key),
          parentId: run.id,
          opName: 'pi.llm',
          kind: 'llm',
          displayName: text ? shortText(text) : `chat ${model ?? ''}`.trim(),
          startedAt: l.startedAt,
          endedAt: l.endedAt ?? endedAt,
          inputs: { model, messages, ...(l.droppedMessages ? { earlier_messages_omitted: l.droppedMessages } : {}) },
          output: out,
          summary: usageSummary(model, usage),
          exception: llmErr,
          genai: { ...common, 'gen_ai.operation.name': 'chat', 'gen_ai.request.model': model, 'gen_ai.response.model': m?.model, 'gen_ai.provider.name': provider },
          attributes: u?.cost?.total ? { pi: { cost_usd: u.cost.total } } : undefined,
        }),
      );
    });

    run.toolOrder.forEach((key, i) => {
      const t = run.tools.get(key);
      const args = t.args && typeof t.args === 'object' && !Array.isArray(t.args) ? t.args : { input: t.args };
      items.push(
        ...callItems(cfg, {
          ...base,
          id: stableId('pi', run.id, 'tool', t.id ?? i),
          parentId: run.id,
          opName: `pi.tool.${t.name}`,
          kind: 'tool',
          displayName: toolDisplay(t.name, args),
          startedAt: t.startedAt,
          endedAt: t.endedAt ?? endedAt,
          inputs: args,
          output: resultValue(t.result),
          exception: t.isError ? contentText(t.result?.content) || 'tool failed' : t.endedAt ? undefined : 'tool did not finish before the run ended',
          genai: { ...common, 'gen_ai.operation.name': 'execute_tool', 'gen_ai.tool.name': t.name, 'gen_ai.tool.call.id': t.id },
        }),
      );
    });
    return items;
  }

  // ------------------------------------------------------------ sending
  enqueue(items) {
    this.queue.push(...items);
    const over = this.queue.length - this.cfg.maxQueue;
    if (over > 0) {
      this.queue.splice(0, over);
      this.stats.dropped += over;
      this.warn(`queue full: dropped ${over} items`);
    }
  }

  /** Send what is queued (one request at a time). Never throws. */
  flush() {
    if (this.sending) return this.sending;
    if (!this.queue.length) return Promise.resolve();
    const batch = this.queue.splice(0, this.queue.length);
    this.sending = sendItems(this.cfg, batch, { fetchImpl: this.fetchImpl })
      .then(() => {
        this.stats.sent += batch.length;
      })
      .catch((e) => {
        this.stats.failed++;
        this.queue.unshift(...batch); // retried on the next tick; ids are stable, so resends upsert
        this.warn(`send failed (will retry): ${e?.message ?? e}`);
      })
      .finally(() => {
        this.sending = null;
      });
    return this.sending;
  }

  /** Close the open run and send, waiting at most `timeoutMs`. */
  async shutdown(reason = 'session shutdown', timeoutMs = 5000) {
    if (this.run) this.finishRun({ reason });
    const wait = (async () => {
      await this.flush();
      if (this.queue.length) await this.flush(); // items queued while a send was in flight
    })();
    await Promise.race([wait, new Promise((r) => setTimeout(r, timeoutMs).unref?.())]);
  }

  /** UI link of the current conversation. */
  conversationUrl() {
    return `${this.cfg.serverUrl}/${this.cfg.project}/threads/${encodeURIComponent(this.session.id ?? '')}`;
  }

  warn(msg) {
    if (this.now() - this.lastWarn < 30_000) return;
    this.lastWarn = this.now();
    try {
      this.log.warn?.(`weft: ${msg}`);
    } catch {
      /* ignore */
    }
  }
}

export const EVENTS = {
  session_start: 'onSessionStart',
  model_select: 'onModelSelect',
  before_agent_start: 'onBeforeAgentStart',
  agent_start: 'onAgentStart',
  turn_start: 'onTurnStart',
  context: 'onContext',
  message_end: 'onMessageEnd',
  tool_execution_start: 'onToolExecutionStart',
  tool_execution_end: 'onToolExecutionEnd',
  agent_end: 'onAgentEnd',
};

/** A quiet logger: Pi's TUI owns the terminal, so warnings go to the plugin log file. */
function fileLogger(cfg) {
  try {
    // makeLogger lives in the shared core; imported lazily to keep this module's surface small.
    return null;
  } catch {
    return null;
  }
}

/**
 * Register the tracer with a Pi `ExtensionAPI`. Returns the tracer (null when
 * no project is configured or the plugin is disabled).
 */
export function registerWeft(pi, options = {}, { env = process.env, fetchImpl, now, logger } = {}) {
  const cfg = resolveConfig(options, env);
  if (cfg.disabled) return null;
  const tracer = new WeftPiTracer(cfg, { logger: logger ?? options.logger ?? fileLogger(cfg) ?? console, fetchImpl, now });
  for (const [name, method] of Object.entries(EVENTS)) {
    try {
      pi.on(name, (event, ctx) => {
        try {
          tracer[method](event ?? {}, ctx ?? {});
        } catch (e) {
          tracer.warn(`${name} handler failed: ${e?.message ?? e}`);
        }
        return undefined; // observe only: never block, replace or modify anything
      });
    } catch {
      /* an older pi without this event */
    }
  }
  const timer = setInterval(() => void tracer.flush(), cfg.flushIntervalMs);
  timer.unref?.();
  tracer.timer = timer;
  try {
    pi.on('session_shutdown', async (event) => {
      // Session replacement (/new, /resume, /fork, /reload) keeps the process; quit ends it.
      if (event?.reason === 'quit') clearInterval(timer);
      await tracer.shutdown(`session ${event?.reason ?? 'shutdown'}`, Math.min(5000, cfg.timeoutMs * 2));
    });
  } catch {
    /* ignore */
  }
  try {
    pi.registerCommand?.('weft', {
      description: 'Show where this session is traced in Weft',
      handler: async (_args, ctx) => {
        await tracer.flush();
        const msg = `Weft: ${tracer.conversationUrl()} (project ${cfg.project}; ${tracer.stats.runs} turns, ${tracer.stats.sent} items sent, ${tracer.queue.length} queued)`;
        if (ctx?.ui?.notify) ctx.ui.notify(msg, 'info');
        else tracer.log.info?.(msg);
      },
    });
  } catch {
    /* commands are optional */
  }
  return tracer;
}

export { truncate };
