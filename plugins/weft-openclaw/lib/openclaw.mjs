// OpenClaw agent runs -> Weft agent conversations.
//
// The plugin runs inside the OpenClaw gateway, so it keeps run state in
// memory and sends finished calls from a background timer; hook handlers only
// update state and return at once (they never await the network and never
// block a tool call).
//
//   invoke_agent <agent>        one per agent run (a turn); conversation = the OpenClaw session id
//   ├─ chat <model>             model calls (model_call_started/ended), with the llm_input /
//   │                           llm_output content and usage when conversation access is allowed
//   ├─ execute_tool <tool>      before_tool_call / after_tool_call (gen_ai.tool.call.id = toolCallId)
//   └─ invoke_agent <subagent>  subagent_spawned / subagent_ended; the child session's runs nest here
//
// Correlation with AgentShield (docs/weft/DESIGN.md §5.3): a run's conversation
// is its own OpenClaw session id (ctx.sessionId, else the session key), the
// value the AgentShield plugin sends as session_id, so a sub-agent's child
// session is a Weft session of its own (nested in the parent's trace), and
// each tool span carries the session id of the tool call's own context.

import { spanItems, chatOutput, iso, newId, sendItems, shortText, stableId, toolDisplay, usageSummary } from './weft.mjs';

export const PLUGIN = 'weft-openclaw';
export const PLUGIN_VERSION = '0.1.0';

/** Plugin config (api.pluginConfig) merged over the environment. */
export function resolveConfig(pluginConfig = {}, env = process.env) {
  const pc = pluginConfig && typeof pluginConfig === 'object' ? pluginConfig : {};
  const bool = (v, d) => (v === undefined || v === null || v === '' ? d : typeof v === 'boolean' ? v : !['0', 'false', 'no', 'off'].includes(String(v).toLowerCase()));
  let project = pc.project ?? env.WEFT_PROJECT ?? '';
  if (pc.entity && project && !String(project).includes('/')) project = `${pc.entity}/${project}`;
  const cfg = {
    plugin: PLUGIN,
    stateDir: '',
    serverUrl: String(pc.serverUrl ?? env.WEFT_SERVER_URL ?? 'http://localhost:6345').replace(/\/+$/, ''),
    apiKey: pc.apiKey ?? env.WEFT_API_KEY ?? '',
    project,
    agentName: String(pc.agentName ?? env.WEFT_AGENT_NAME ?? '').trim(),
    agentVersion: pc.agentVersion,
    captureContent: bool(pc.captureContent ?? env.WEFT_CAPTURE_CONTENT, true),
    timeoutMs: Number(pc.timeoutMs ?? env.WEFT_HOOK_TIMEOUT_MS ?? 2000) || 2000,
    flushIntervalMs: Number(pc.flushIntervalMs ?? 1000) || 1000,
    maxFieldChars: Number(pc.maxFieldChars ?? env.WEFT_MAX_FIELD_CHARS ?? 20000) || 20000,
    maxQueue: Number(pc.maxQueue ?? 20000) || 20000,
    runIdleMs: Number(pc.runIdleMs ?? 10 * 60_000) || 600_000,
    enabled: bool(pc.enabled ?? (env.WEFT_PLUGIN_DISABLED ? !bool(env.WEFT_PLUGIN_DISABLED, false) : undefined), true),
  };
  if (!cfg.project) cfg.enabled = false;
  return cfg;
}

/**
 * The session id the AgentShield OpenClaw plugin sends for a hook context
 * (plugins/openclaw/src/event-builder.ts sessionIdFor): the session id, else the session key.
 */
export function harnessSessionId(ctx = {}) {
  return ctx.sessionId ?? ctx.sessionKey ?? undefined;
}

function lastAssistantText(messages) {
  if (!Array.isArray(messages)) return '';
  for (let i = messages.length - 1; i >= 0; i--) {
    const m = messages[i];
    if (m?.role !== 'assistant') continue;
    const c = m.content;
    if (typeof c === 'string') return c;
    if (Array.isArray(c)) {
      const t = c.filter((b) => b?.type === 'text').map((b) => b.text ?? '').join('\n');
      if (t) return t;
    }
  }
  return '';
}

function normMessage(m) {
  if (!m || typeof m !== 'object') return { role: 'user', content: String(m ?? '') };
  const role = m.role === 'toolResult' ? 'tool' : m.role ?? 'user';
  const out = { role, content: m.content ?? m.text ?? '' };
  if (m.toolCallId) out.tool_call_id = m.toolCallId;
  return out;
}

function errText(e) {
  if (!e) return undefined;
  return typeof e === 'string' ? e : e.message ?? JSON.stringify(e);
}

/** In-memory state machine from OpenClaw hook events to Weft calls. */
export class WeftOpenClawTracer {
  constructor(cfg, { logger = console, fetchImpl, now = () => Date.now() } = {}) {
    this.cfg = cfg;
    this.log = logger;
    this.fetchImpl = fetchImpl;
    this.now = now;
    this.sessions = new Map(); // sessionId -> info
    this.runs = new Map(); // run key -> run
    this.subagents = new Map(); // childSessionKey -> span
    this.queue = [];
    this.sending = null;
    this.lastWarn = 0;
    this.stats = { sent: 0, failed: 0, dropped: 0 };
  }

  // ------------------------------------------------------------ helpers
  sessionOf(ctx = {}, event = {}) {
    const id = event.sessionId ?? ctx.sessionId ?? ctx.sessionKey ?? 'unknown';
    let s = this.sessions.get(id);
    if (!s) {
      s = { id, sessionKey: event.sessionKey ?? ctx.sessionKey, agentId: ctx.agentId, firstPrompt: null, currentRun: null };
      this.sessions.set(id, s);
    }
    if (!s.sessionKey && (event.sessionKey ?? ctx.sessionKey)) s.sessionKey = event.sessionKey ?? ctx.sessionKey;
    if (!s.agentId && ctx.agentId) s.agentId = ctx.agentId;
    return s;
  }

  runFor(event = {}, ctx = {}, { create = true } = {}) {
    const session = this.sessionOf(ctx, event);
    const runId = event.runId ?? ctx.runId;
    const key = runId ? `run:${runId}` : session.currentRun;
    let run = key ? this.runs.get(key) : null;
    if (run || !create) return run ?? null;
    const k = runId ? `run:${runId}` : `anon:${newId()}`;
    // A new run in a session closes the previous unfinished one (no agent_end without conversation access).
    if (session.currentRun && session.currentRun !== k && this.runs.has(session.currentRun)) this.finishRun(this.runs.get(session.currentRun), { reason: 'superseded' });
    const id = runId ? stableId('openclaw', 'run', runId) : newId();
    const sub = session.sessionKey ? this.subagents.get(session.sessionKey) : null;
    run = {
      key: k,
      runId,
      id,
      traceId: sub ? sub.traceId : id,
      parentId: sub ? sub.id : null,
      // The harness session (a sub-agent's child session included): what AgentShield sends as session_id.
      conversationId: session.id,
      turnId: id,
      session,
      agentId: ctx.agentId ?? session.agentId,
      startedAt: this.now(),
      lastSeen: this.now(),
      prompt: null,
      systemPrompt: null,
      history: null,
      model: ctx.modelId,
      provider: ctx.modelProviderId,
      llmInputs: [],
      llmOutputs: [],
      modelCalls: [],
      tools: [],
      channel: ctx.channelId ?? ctx.channel ?? ctx.messageProvider,
      trigger: ctx.trigger,
    };
    this.runs.set(k, run);
    session.currentRun = k;
    return run;
  }

  // ------------------------------------------------------------ hooks
  onSessionStart(event = {}, ctx = {}) {
    const s = this.sessionOf(ctx, event);
    s.resumedFrom = event.resumedFrom;
  }

  onLlmInput(event = {}, ctx = {}) {
    const run = this.runFor(event, ctx);
    run.lastSeen = this.now();
    run.prompt = run.prompt ?? event.prompt;
    run.systemPrompt = run.systemPrompt ?? event.systemPrompt;
    run.history = event.historyMessages ?? run.history;
    run.model = event.model ?? run.model;
    run.provider = event.provider ?? run.provider;
    run.llmInputs.push({ at: this.now(), event });
    if (!run.session.firstPrompt && event.prompt) run.session.firstPrompt = shortText(event.prompt, 80);
  }

  onLlmOutput(event = {}, ctx = {}) {
    const run = this.runFor(event, ctx);
    run.lastSeen = this.now();
    run.model = event.model ?? run.model;
    run.provider = event.provider ?? run.provider;
    run.llmOutputs.push({ at: this.now(), event });
  }

  onModelCallStarted(event = {}, ctx = {}) {
    const run = this.runFor(event, ctx);
    run.lastSeen = this.now();
    run.modelCalls.push({ callId: event.callId, startedAt: this.now(), model: event.model, provider: event.provider });
  }

  onModelCallEnded(event = {}, ctx = {}) {
    const run = this.runFor(event, ctx);
    run.lastSeen = this.now();
    let mc = run.modelCalls.find((m) => m.callId === event.callId && !m.endedAt) ?? run.modelCalls.find((m) => !m.endedAt);
    if (!mc) {
      mc = { callId: event.callId, startedAt: this.now() - (event.durationMs ?? 0), model: event.model, provider: event.provider };
      run.modelCalls.push(mc);
    }
    mc.endedAt = this.now();
    mc.outcome = event.outcome;
    mc.error = event.outcome === 'error' ? event.errorCategory ?? event.failureKind ?? 'model call failed' : undefined;
  }

  onBeforeToolCall(event = {}, ctx = {}) {
    const run = this.runFor(event, ctx);
    run.lastSeen = this.now();
    run.tools.push({
      callId: event.toolCallId ?? ctx.toolCallId,
      name: event.toolName ?? ctx.toolName ?? 'tool',
      params: event.params,
      startedAt: this.now(),
      sessionId: harnessSessionId(ctx),
    });
    return undefined; // never block or modify the call
  }

  onAfterToolCall(event = {}, ctx = {}) {
    const run = this.runFor(event, ctx);
    run.lastSeen = this.now();
    const id = event.toolCallId ?? ctx.toolCallId;
    const name = event.toolName ?? ctx.toolName ?? 'tool';
    let t = (id && run.tools.find((x) => x.callId === id && !x.endedAt)) || run.tools.find((x) => x.name === name && !x.endedAt);
    if (!t) {
      t = { callId: id, name, params: event.params, startedAt: this.now() - (event.durationMs ?? 0) };
      run.tools.push(t);
    }
    t.endedAt = this.now();
    if (event.durationMs !== undefined && t.startedAt > t.endedAt - event.durationMs) t.startedAt = t.endedAt - event.durationMs;
    t.result = event.result;
    t.error = event.error;
  }

  onAgentEnd(event = {}, ctx = {}) {
    const run = this.runFor(event, ctx, { create: true });
    run.endMessages = event.messages;
    run.success = event.success;
    run.error = event.error;
    if (event.durationMs) run.startedAt = Math.min(run.startedAt, this.now() - event.durationMs);
    this.finishRun(run);
  }

  onSubagentSpawned(event = {}, ctx = {}) {
    const parent = this.runFor({ runId: ctx.runId }, ctx, { create: false }) ?? (ctx.sessionKey ? [...this.runs.values()].reverse().find((r) => r.session.sessionKey === ctx.sessionKey) : null);
    const id = stableId('openclaw', 'subagent', event.runId ?? '', event.childSessionKey ?? newId());
    this.subagents.set(event.childSessionKey, {
      id,
      childSessionKey: event.childSessionKey,
      // a label is free text: not used as the name when content capture is off
      name: event.agentId ?? (this.cfg.captureContent ? event.label : undefined) ?? 'subagent',
      label: event.label,
      model: event.resolvedModel,
      provider: event.resolvedProvider,
      mode: event.mode,
      traceId: parent ? parent.traceId : id,
      parentId: parent ? parent.id : null,
      conversationId: parent ? parent.conversationId : event.childSessionKey,
      turnId: parent ? parent.turnId : id,
      startedAt: this.now(),
      parentRun: parent,
    });
  }

  onSubagentEnded(event = {}) {
    const sub = this.subagents.get(event.targetSessionKey);
    if (!sub) return;
    this.subagents.delete(event.targetSessionKey);
    // Close runs of the child session that are still open.
    for (const r of [...this.runs.values()]) if (r.session.sessionKey === sub.childSessionKey) this.finishRun(r, { reason: 'subagent ended' });
    const ok = !event.outcome || event.outcome === 'ok';
    this.enqueue(
      spanItems(this.cfg, {
        id: sub.id,
        traceId: sub.traceId,
        parentId: sub.parentId,
        sessionId: sub.conversationId,
        turnId: sub.turnId,
        name: 'openclaw.subagent',
        kind: 'agent',
        displayName: `invoke_agent ${sub.name}`,
        startedAt: sub.startedAt,
        endedAt: event.endedAt ?? this.now(),
        inputs: { label: sub.label, mode: sub.mode, child_session_key: sub.childSessionKey },
        output: { outcome: event.outcome ?? 'ok', reason: event.reason },
        error: ok ? undefined : errText(event.error) ?? `subagent ${event.outcome}`,
        errorKind: ok ? undefined : `subagent ${event.outcome}`,
        genai: { 'gen_ai.operation.name': 'invoke_agent', 'gen_ai.agent.name': sub.name, 'gen_ai.request.model': sub.model, 'gen_ai.provider.name': sub.provider, 'gen_ai.conversation.id': sub.conversationId },
        pluginVersion: PLUGIN_VERSION,
      }),
    );
  }

  onSessionEnd(event = {}, ctx = {}) {
    const s = this.sessionOf(ctx, event);
    for (const r of [...this.runs.values()]) if (r.session === s) this.finishRun(r, { reason: `session ended (${event.reason ?? 'unknown'})` });
    this.sessions.delete(s.id);
  }

  /** Close runs nobody has heard from in `runIdleMs`. */
  sweep() {
    const cut = this.now() - this.cfg.runIdleMs;
    for (const r of [...this.runs.values()]) if (r.lastSeen < cut) this.finishRun(r, { reason: 'idle' });
  }

  // ------------------------------------------------------------ building calls
  finishRun(run, { reason } = {}) {
    if (!run || !this.runs.has(run.key)) return;
    this.runs.delete(run.key);
    if (run.session.currentRun === run.key) run.session.currentRun = null;
    try {
      this.enqueue(this.runItems(run, reason));
    } catch (e) {
      this.warn(`could not build calls for run ${run.runId ?? run.id}: ${e?.message ?? e}`);
    }
  }

  runItems(run, reason) {
    const cfg = this.cfg;
    const endedAt = this.now();
    const agentName = cfg.agentName || run.agentId || 'openclaw';
    const base = { traceId: run.traceId, sessionId: run.conversationId, turnId: run.turnId, pluginVersion: PLUGIN_VERSION };
    const conv = { 'gen_ai.conversation.id': run.conversationId, 'gen_ai.conversation.name': run.session.firstPrompt };
    const items = [];
    const lastOut = run.llmOutputs[run.llmOutputs.length - 1]?.event;
    const finalText = lastAssistantText(run.endMessages) || (lastOut?.assistantTexts ?? []).join('\n');
    const exception = run.error ? errText(run.error) : run.success === false ? 'agent run failed' : undefined;
    const exceptionKind = exception ? 'agent run failed' : undefined;
    items.push(
      ...spanItems(cfg, {
        ...base,
        id: run.id,
        parentId: run.parentId,
        name: 'openclaw.agent_run',
        kind: 'agent',
        displayName: `invoke_agent ${agentName}`,
        startedAt: run.startedAt,
        endedAt,
        inputs: { messages: [{ role: 'user', content: run.prompt ?? '' }] },
        output: finalText ? { messages: [{ role: 'assistant', content: finalText }] } : null,
        error: exception,
        errorKind: exceptionKind,
        genai: {
          ...conv,
          'gen_ai.operation.name': 'invoke_agent',
          'gen_ai.agent.name': agentName,
          'gen_ai.agent.id': run.agentId,
          'gen_ai.agent.version': cfg.agentVersion,
          'gen_ai.request.model': run.model,
          'gen_ai.provider.name': run.provider,
        },
        attributes: { openclaw: { run_id: run.runId, session_key: run.session.sessionKey, channel: run.channel, trigger: run.trigger, closed_by: reason ?? 'agent_end' } },
      }),
    );

    // LLM calls: one per model call when the diagnostic events came, else one per llm_input/llm_output pair.
    const llms = [];
    if (run.modelCalls.length) {
      run.modelCalls.forEach((mc, i) => llms.push({ startedAt: mc.startedAt, endedAt: mc.endedAt ?? endedAt, model: mc.model ?? run.model, provider: mc.provider ?? run.provider, error: mc.error, key: mc.callId ?? i }));
      const n = llms.length;
      run.llmInputs.forEach((x, i) => (llms[Math.min(i, n - 1)].input ??= x.event));
      if (run.llmOutputs.length === n) run.llmOutputs.forEach((x, i) => (llms[i].output = x.event));
      else if (run.llmOutputs.length) llms[n - 1].output = lastOut;
    } else {
      const n = Math.max(run.llmInputs.length, run.llmOutputs.length);
      for (let i = 0; i < n; i++) {
        const inp = run.llmInputs[i];
        const out = run.llmOutputs[i];
        llms.push({ startedAt: inp?.at ?? out?.at, endedAt: out?.at ?? endedAt, model: out?.event.model ?? inp?.event.model ?? run.model, provider: out?.event.provider ?? inp?.event.provider ?? run.provider, input: inp?.event, output: out?.event, key: i });
      }
    }
    llms.forEach((l) => {
      const messages = [];
      if (l.input) {
        if (l.input.systemPrompt) messages.push({ role: 'system', content: l.input.systemPrompt });
        for (const m of (l.input.historyMessages ?? []).slice(-20)) messages.push(normMessage(m));
        if (l.input.prompt) messages.push({ role: 'user', content: l.input.prompt });
      }
      const u = l.output?.usage;
      const usage = u ? { prompt: (u.input ?? 0) + (u.cacheRead ?? 0) + (u.cacheWrite ?? 0), completion: u.output ?? 0, total: u.total, cacheRead: u.cacheRead, cacheWrite: u.cacheWrite } : null;
      const text = (l.output?.assistantTexts ?? []).join('\n');
      const content = l.output?.lastAssistant?.content ?? text;
      items.push(
        ...spanItems(cfg, {
          ...base,
          id: stableId('openclaw', 'llm', run.id, l.key),
          parentId: run.id,
          name: 'openclaw.llm',
          kind: 'llm',
          displayName: text ? shortText(text) : `chat ${l.model ?? ''}`.trim(),
          startedAt: l.startedAt,
          endedAt: l.endedAt,
          inputs: { model: l.model, ...(messages.length ? { messages } : {}), ...(l.input?.tools ? { tools: l.input.tools.length } : {}) },
          output: l.output ? chatOutput(l.model, content, null) : null,
          summary: usageSummary(l.model, usage),
          error: l.error,
          genai: { ...conv, 'gen_ai.operation.name': 'chat', 'gen_ai.request.model': l.model, 'gen_ai.provider.name': l.provider },
        }),
      );
    });

    run.tools.forEach((t, i) => {
      const params = t.params && typeof t.params === 'object' && !Array.isArray(t.params) ? t.params : { input: t.params };
      // the session id AgentShield sent for this tool call (normally the run's)
      const sid = t.sessionId ?? run.conversationId;
      items.push(
        ...spanItems(cfg, {
          ...base,
          sessionId: sid,
          id: stableId('openclaw', 'tool', run.id, t.callId ?? i),
          parentId: run.id,
          name: `openclaw.tool.${t.name}`,
          kind: 'tool',
          displayName: toolDisplay(t.name, params),
          startedAt: t.startedAt,
          endedAt: t.endedAt ?? endedAt,
          inputs: params,
          output: t.result ?? null,
          error: t.error ? errText(t.error) : t.endedAt ? undefined : 'tool did not finish before the run ended',
          errorKind: t.error ? 'tool error' : 'tool did not finish before the run ended',
          genai: { ...conv, 'gen_ai.conversation.id': sid, 'gen_ai.operation.name': 'execute_tool', 'gen_ai.tool.name': t.name, 'gen_ai.tool.call.id': t.callId },
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
    this.sending = sendItems(this.cfg, batch, { fetchImpl: this.fetchImpl, warn: (m) => this.warn(m) })
      .then(() => {
        this.stats.sent += batch.length;
      })
      .catch((e) => {
        this.stats.failed++;
        this.queue.unshift(...batch); // retried on the next tick
        this.warn(`send failed (will retry): ${e?.message ?? e}`);
      })
      .finally(() => {
        this.sending = null;
      });
    return this.sending;
  }

  /** Close everything and send, waiting at most `timeoutMs`. */
  async shutdown(timeoutMs = 5000) {
    for (const r of [...this.runs.values()]) this.finishRun(r, { reason: 'gateway stopped' });
    await Promise.race([this.flush(), new Promise((r) => setTimeout(r, timeoutMs).unref?.())]);
  }

  warn(msg) {
    if (this.now() - this.lastWarn < 30_000) return;
    this.lastWarn = this.now();
    try {
      this.log.warn(`weft: ${msg}`);
    } catch {
      /* ignore */
    }
  }
}

export const HOOKS = {
  session_start: 'onSessionStart',
  llm_input: 'onLlmInput',
  llm_output: 'onLlmOutput',
  model_call_started: 'onModelCallStarted',
  model_call_ended: 'onModelCallEnded',
  before_tool_call: 'onBeforeToolCall',
  after_tool_call: 'onAfterToolCall',
  agent_end: 'onAgentEnd',
  subagent_spawned: 'onSubagentSpawned',
  subagent_ended: 'onSubagentEnded',
  session_end: 'onSessionEnd',
};

/** Register the tracer's hooks with an OpenClaw plugin API. Returns the tracer. */
export function registerWeft(api, { env = process.env, fetchImpl, now } = {}) {
  const cfg = resolveConfig(api.pluginConfig, env);
  const logger = api.logger ?? console;
  if (!cfg.enabled) {
    logger.info?.(cfg.project ? 'weft: plugin disabled' : 'weft: no project configured (set plugins.entries.weft.config.project or WEFT_PROJECT); tracing is off');
    return null;
  }
  const tracer = new WeftOpenClawTracer(cfg, { logger, fetchImpl, now });
  for (const [hook, method] of Object.entries(HOOKS)) {
    const handler = (event, ctx) => {
      try {
        return tracer[method](event ?? {}, ctx ?? {});
      } catch (e) {
        tracer.warn(`${hook} handler failed: ${e?.message ?? e}`);
        return undefined;
      }
    };
    try {
      // Observe late and never change anything: lowest priority, synchronous return.
      api.on(hook, handler, { priority: 1000 });
    } catch (e) {
      logger.debug?.(`weft: hook ${hook} not available: ${e?.message ?? e}`);
    }
  }
  const timer = setInterval(() => {
    tracer.sweep();
    tracer.flush();
  }, cfg.flushIntervalMs);
  timer.unref?.();
  tracer.timer = timer;
  try {
    api.on('gateway_stop', async () => {
      clearInterval(timer);
      await tracer.shutdown(Math.min(5000, cfg.timeoutMs * 2));
    });
  } catch {
    /* older gateways */
  }
  logger.info?.(`weft: tracing agent runs to ${cfg.serverUrl} (project ${cfg.project})`);
  return tracer;
}

export { iso };
