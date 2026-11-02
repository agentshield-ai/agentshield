/**
 * Claude Agent SDK for TypeScript (`@anthropic-ai/claude-agent-sdk`).
 *
 * ```ts
 * import * as sdk from '@anthropic-ai/claude-agent-sdk';
 * const { query } = weft.wrapClaudeAgentSDK(sdk);
 * for await (const m of query({ prompt: 'Fix the bug', options })) { ... }
 * ```
 *
 * Each `query()` becomes an agent call (`claude_agent_sdk.query`, an
 * `invoke_agent` turn) whose conversation is the SDK session id, so resumed
 * sessions read as one conversation in Agents. Under it, every assistant
 * message is an LLM call (`claude_agent_sdk.llm`: model, content blocks and
 * that message's usage, counted once per message id) and every tool use a tool
 * call (`claude_agent_sdk.tool.<name>`) that ends when its result arrives (an
 * error result marks it failed). Sub-agent messages (`parent_tool_use_id`)
 * nest under the `Task`/`Agent` tool call that started them. The final result
 * message's cost, turns and duration are kept on the query call; when no
 * assistant message carried usage, the result's usage is recorded there.
 */
import { logger } from '../log.js';
import type { Call } from '../call.js';
import type { WeftClient } from '../client.js';
import { currentContext, runWith } from '../context.js';
import type { OpMeta } from '../op.js';
import { addUsage } from '../usage.js';
import { short } from './common.js';
import { integrationAttributes, integrationClient, integrationOp, pickOpSettings, type IntegrationOpSettings } from './settings.js';

const INTEGRATION = 'claude_agent_sdk';
const AGENT_TOOLS = new Set(['Task', 'Agent']);

export function claudeToolDisplay(name: string, input: any): string {
  if (input && typeof input === 'object') {
    for (const k of ['command', 'file_path', 'path', 'pattern', 'url', 'query', 'description', 'prompt']) {
      if (input[k]) return `${name}: ${short(input[k], 50)}`;
    }
  }
  return name;
}

function recordClaudeUsage(call: Call, model: string | undefined, u: any): boolean {
  if (!u || typeof u !== 'object') return false;
  const cacheRead = Number(u.cache_read_input_tokens ?? 0) || 0;
  const cacheWrite = Number(u.cache_creation_input_tokens ?? 0) || 0;
  const input = (Number(u.input_tokens ?? 0) || 0) + cacheRead + cacheWrite;
  const output = Number(u.output_tokens ?? 0) || 0;
  if (!input && !output) return false;
  addUsage(call, model || 'claude', input, output);
  const e = (call.summary.usage as any)?.[model || 'claude'];
  if (e) {
    if (cacheRead) e.cache_read_input_tokens = (e.cache_read_input_tokens ?? 0) + cacheRead;
    if (cacheWrite) e.cache_creation_input_tokens = (e.cache_creation_input_tokens ?? 0) + cacheWrite;
  }
  return true;
}

class QueryTracer {
  root: Call | null = null;
  rootMeta!: OpMeta;
  model: string | undefined;
  sessionId: string | undefined;
  tools = new Map<string, { call: Call; meta: OpMeta }>();
  messages: unknown[] = [];
  result: any = null;
  usageSeen = new Set<string>();
  llmUsage = false;
  readonly startedAt = new Date();
  readonly parent: Call | null;

  constructor(
    private client: WeftClient,
    private prompt: unknown,
    private options: any,
    private local: IntegrationOpSettings | undefined,
    private opName: string,
  ) {
    const s = currentContext().stack;
    this.parent = s.length ? s[s.length - 1] : null;
    this.sessionId = options?.resume ?? options?.sessionId;
  }

  private attrs(extra?: Record<string, unknown>): Record<string, unknown> {
    const a: Record<string, unknown> = { ...extra };
    if (this.sessionId) a['gen_ai.conversation.id'] = this.sessionId;
    return integrationAttributes(INTEGRATION, a, this.local);
  }

  /** The root call is created at the first message, once the session id is known. */
  ensureRoot(): Call {
    if (this.root) return this.root;
    this.rootMeta = integrationOp(INTEGRATION, this.opName, 'agent', {}, this.local);
    const inputs: Record<string, unknown> = { prompt: typeof this.prompt === 'string' ? this.prompt : '<message stream>' };
    const o = this.options;
    if (o && typeof o === 'object') {
      const keep: Record<string, unknown> = {};
      for (const k of ['model', 'systemPrompt', 'allowedTools', 'disallowedTools', 'permissionMode', 'maxTurns', 'cwd', 'resume', 'agents', 'mcpServers']) {
        const v = o[k];
        if (v !== undefined && v !== null && !(Array.isArray(v) && !v.length)) keep[k] = k === 'mcpServers' ? Object.keys(v) : v;
      }
      if (Object.keys(keep).length) inputs.options = keep;
    }
    if (typeof this.prompt === 'string') inputs.messages = [{ role: 'user', content: this.prompt }];
    const attrs: Record<string, unknown> = { 'gen_ai.operation.name': 'invoke_agent', 'gen_ai.agent.name': o?.agentName ?? 'claude-agent-sdk', 'gen_ai.provider.name': 'anthropic' };
    if (o?.model) attrs['gen_ai.request.model'] = o.model;
    const create = () =>
      this.client.createCall(this.rootMeta, inputs, {
        parent: this.parent,
        displayName: typeof this.prompt === 'string' ? short(this.prompt) : undefined,
        attributes: this.attrs(attrs),
        startedAt: this.startedAt,
      });
    this.root = this.sessionId ? runWith({ thread: { threadId: this.sessionId, entryParentId: this.parent?.id ?? null } }, create) : create();
    return this.root;
  }

  private start(name: string, kind: string, inputs: Record<string, unknown>, parent: Call, display: string | undefined, attrs: Record<string, unknown>) {
    const meta = integrationOp(INTEGRATION, name, kind, {}, this.local);
    const call = this.client.createCall(meta, inputs, { parent, displayName: display, attributes: this.attrs(attrs) });
    return { call, meta };
  }

  private parentOf(msg: any): Call {
    const pid = msg?.parent_tool_use_id;
    return (pid && this.tools.get(pid)?.call) || this.ensureRoot();
  }

  onMessage(msg: any): void {
    if (!msg || typeof msg !== 'object') return;
    if (msg.session_id && !this.sessionId) this.sessionId = msg.session_id;
    if (msg.type === 'system' && msg.subtype === 'init') {
      this.model = this.model ?? msg.model;
      this.ensureRoot();
      return;
    }
    this.ensureRoot();
    if (msg.type === 'result') {
      this.result = msg;
      return;
    }
    if (msg.type === 'assistant') {
      const m = msg.message ?? {};
      const blocks: any[] = Array.isArray(m.content) ? m.content : [];
      this.model = this.model ?? m.model;
      this.messages.push({ role: 'assistant', content: blocks, model: m.model });
      const parent = this.parentOf(msg);
      const text = blocks.find((b) => b?.type === 'text')?.text;
      const uses = blocks.filter((b) => b?.type === 'tool_use');
      const display = text ? short(text) : uses.length ? `→ ${uses[0].name}` : undefined;
      const attrs: Record<string, unknown> = { 'gen_ai.operation.name': 'chat', 'gen_ai.provider.name': 'anthropic' };
      if (m.model) attrs['gen_ai.request.model'] = m.model;
      if (m.id) attrs['gen_ai.response.id'] = m.id;
      const { call, meta } = this.start('claude_agent_sdk.llm', 'llm', { model: m.model, history_length: this.messages.length - 1 }, parent, display, attrs);
      if (m.usage && m.id && !this.usageSeen.has(m.id)) {
        this.usageSeen.add(m.id);
        if (recordClaudeUsage(call, m.model, m.usage)) this.llmUsage = true;
      }
      const out = { object: 'chat.completion', model: m.model, choices: [{ index: 0, message: { role: 'assistant', content: blocks }, finish_reason: m.stop_reason ?? null }] };
      this.client.finishCall(call, out, msg.error ? new Error(String(msg.error)) : undefined, meta);
      for (const b of uses) {
        const name = b.name ?? 'tool';
        const input = b.input && typeof b.input === 'object' && !Array.isArray(b.input) ? { ...b.input } : { input: b.input };
        const tattrs: Record<string, unknown> = { 'gen_ai.operation.name': 'execute_tool', 'gen_ai.tool.name': name };
        if (b.id) tattrs['gen_ai.tool.call.id'] = b.id;
        const kind = AGENT_TOOLS.has(name) ? 'agent' : 'tool';
        if (kind === 'agent') {
          tattrs['gen_ai.operation.name'] = 'invoke_agent';
          tattrs['gen_ai.agent.name'] = b.input?.subagent_type ?? name;
        }
        this.tools.set(b.id ?? `${name}-${this.tools.size}`, this.start(`claude_agent_sdk.tool.${name}`, kind, input, parent, claudeToolDisplay(name, b.input), tattrs));
      }
      return;
    }
    if (msg.type === 'user') {
      const content = msg.message?.content;
      const blocks: any[] = Array.isArray(content) ? content : [];
      this.messages.push({ role: 'user', content });
      for (const b of blocks) {
        if (b?.type !== 'tool_result') continue;
        const t = this.tools.get(b.tool_use_id);
        if (!t) continue;
        this.tools.delete(b.tool_use_id);
        const out: Record<string, unknown> = { content: b.content, is_error: !!b.is_error };
        if (msg.tool_use_result !== undefined) out.tool_use_result = msg.tool_use_result;
        const err = b.is_error ? new Error(short(Array.isArray(b.content) ? b.content.map((c: any) => c?.text ?? '').join(' ') : b.content, 500) || 'tool error') : undefined;
        this.client.finishCall(t.call, out, err, t.meta);
      }
    }
  }

  finish(err?: unknown): void {
    const root = this.ensureRoot();
    for (const t of this.tools.values()) this.client.finishCall(t.call, undefined, err ?? new Error('query ended before the tool returned'), t.meta);
    this.tools.clear();
    const out: Record<string, unknown> = { messages: this.messages };
    if (this.model) out.model = this.model;
    const r = this.result;
    let error = err;
    if (r) {
      for (const k of ['result', 'session_id', 'num_turns', 'duration_ms', 'duration_api_ms', 'total_cost_usd', 'subtype', 'is_error', 'modelUsage', 'usage']) if (r[k] !== undefined) out[k] = r[k];
      if (!this.llmUsage && r.usage) recordClaudeUsage(root, this.model, r.usage);
      if (r.is_error && error === undefined) error = new Error(String(r.result ?? r.subtype ?? 'Claude Agent SDK query ended with an error'));
      if (typeof r.result === 'string') out.messages = [...this.messages, { role: 'assistant', content: r.result }];
    }
    this.client.finishCall(root, out, error, this.rootMeta);
  }
}

function wrapIterator(gen: any, tracer: QueryTracer): any {
  if (!gen || typeof gen[Symbol.asyncIterator] !== 'function') return gen;
  let done = false;
  const finish = (e?: unknown) => {
    if (done) return;
    done = true;
    try {
      tracer.finish(e);
    } catch (x) {
      logger.warn(`weft: claude agent sdk tracing failed: ${(x as Error).message}`);
    }
  };
  const it = gen[Symbol.asyncIterator]();
  const wrappedIt = {
    async next(...a: any[]) {
      let r: IteratorResult<any>;
      try {
        r = await it.next(...a);
      } catch (e) {
        finish(e);
        throw e;
      }
      if (r.done) finish();
      else {
        try {
          tracer.onMessage(r.value);
        } catch {
          /* never break the stream */
        }
      }
      return r;
    },
    async return(v?: any) {
      finish();
      return it.return ? it.return(v) : { done: true, value: v };
    },
    async throw(e?: any) {
      finish(e);
      if (it.throw) return it.throw(e);
      throw e;
    },
    [Symbol.asyncIterator]() {
      return this;
    },
  };
  // Keep the Query object's control methods (interrupt, setPermissionMode, ...).
  return new Proxy(gen, {
    get(target, prop) {
      if (prop === Symbol.asyncIterator) return () => wrappedIt;
      if (prop === 'next' || prop === 'return' || prop === 'throw') return (wrappedIt as any)[prop].bind(wrappedIt);
      const v = Reflect.get(target, prop, target);
      return typeof v === 'function' ? v.bind(target) : v;
    },
  });
}

/** Wrap the SDK's `query` function. */
export function wrapClaudeAgentQuery<F extends (...a: any[]) => any>(query: F, opts: IntegrationOpSettings & { opSettings?: IntegrationOpSettings; opName?: string } = {}): F {
  if ((query as any).__weftPatched) return query;
  const local = pickOpSettings(opts);
  const wrapped = function (this: unknown, params: any, ...rest: any[]) {
    const client = integrationClient(INTEGRATION);
    const gen = query.call(this, params, ...rest);
    if (!client) return gen;
    try {
      return wrapIterator(gen, new QueryTracer(client, params?.prompt, params?.options, local, opts.opName ?? 'claude_agent_sdk.query'));
    } catch {
      return gen;
    }
  };
  Object.defineProperty(wrapped, '__weftPatched', { value: true });
  return wrapped as unknown as F;
}

/** Return the SDK module with a traced `query` (ES module namespaces cannot be patched in place). */
export function wrapClaudeAgentSDK<T extends Record<string, any>>(sdk: T, opts: IntegrationOpSettings & { opSettings?: IntegrationOpSettings } = {}): T {
  if (typeof sdk?.query !== 'function') throw new TypeError('wrapClaudeAgentSDK: expected the @anthropic-ai/claude-agent-sdk module');
  return { ...sdk, query: wrapClaudeAgentQuery(sdk.query, opts) } as T;
}
