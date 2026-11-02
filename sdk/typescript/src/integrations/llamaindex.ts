/**
 * LlamaIndex.TS integration: subscribes to the global callback manager.
 *
 * ```ts
 * import { Settings } from 'llamaindex';
 * weft.instrumentLlamaIndex(Settings);          // or a CallbackManager
 * ```
 *
 * `query-start/end` become `llamaindex.query` calls (chain), `retrieve-start/end`
 * retriever calls with the retrieved nodes, `llm-start/end` LLM calls with the
 * messages, response and usage (from the provider's raw response), and
 * `llm-tool-call` / `llm-tool-result` tool calls with `gen_ai.tool.*`
 * attributes; `agent-start/end` become agent calls. Events arrive in the async
 * context that emitted them, so they nest under the current op.
 */
import { logger } from '../log.js';
import { asError, RunTracker } from './runs.js';
import { pickOpSettings, type IntegrationOpSettings } from './settings.js';

const INTEGRATION = 'llamaindex';

type Handler = (event: any) => void;

function detail(ev: any): any {
  const d = ev?.detail ?? ev;
  return d?.payload ?? d; // older versions nest the payload
}

function message(m: any): Record<string, unknown> {
  if (!m || typeof m !== 'object') return { role: 'user', content: m };
  const out: Record<string, unknown> = { role: m.role ?? 'user', content: m.content };
  if (m.options && Object.keys(m.options).length) out.options = m.options;
  return out;
}

function nodes(ns: any[]): unknown[] {
  return (ns ?? []).map((n: any) => {
    const node = n?.node ?? n;
    let text: unknown;
    try {
      text = typeof node?.getContent === 'function' ? node.getContent() : node?.text;
    } catch {
      text = node?.text;
    }
    return { id: node?.id_ ?? node?.id, score: n?.score, text, metadata: node?.metadata };
  });
}

function queryText(q: any): unknown {
  if (typeof q === 'string') return q;
  return q?.query ?? q?.queryStr ?? q;
}

function usageOf(raw: any, model?: string) {
  const u = raw?.usage ?? raw?.usageMetadata;
  if (!u) return null;
  return {
    model: raw?.model ?? model,
    promptTokens: u.prompt_tokens ?? u.input_tokens ?? u.promptTokens ?? u.inputTokens ?? u.promptTokenCount,
    completionTokens: u.completion_tokens ?? u.output_tokens ?? u.completionTokens ?? u.outputTokens ?? u.candidatesTokenCount,
    totalTokens: u.total_tokens ?? u.totalTokens ?? u.totalTokenCount,
  };
}

export interface LlamaIndexInstrumentation {
  /** Remove the event handlers. */
  uninstrument(): void;
}

const installed = new WeakMap<object, LlamaIndexInstrumentation>();

/**
 * Subscribe Weft to LlamaIndex.TS events. Pass `Settings` (from `llamaindex`
 * or `@llamaindex/core/global`) or a `CallbackManager`. Idempotent.
 */
export function instrumentLlamaIndex(settingsOrManager: any, opts: IntegrationOpSettings & { opSettings?: IntegrationOpSettings } = {}): LlamaIndexInstrumentation {
  const cm = settingsOrManager?.callbackManager ?? settingsOrManager;
  if (!cm || typeof cm.on !== 'function') throw new TypeError('instrumentLlamaIndex: pass LlamaIndex Settings or a CallbackManager');
  const existing = installed.get(cm);
  if (existing) return existing;
  const tracker = new RunTracker(INTEGRATION, pickOpSettings(opts));
  const llmModels = new Map<string, string | undefined>();
  const toolKey = (tc: any) => `tool:${tc?.id ?? tc?.name}`;
  const pendingRetrieves: unknown[] = [];
  const pendingAgents: unknown[] = [];
  // Open query/agent runs: later events nest under the innermost one.
  const containers: unknown[] = [];
  const parent = () => containers.at(-1);
  const close = (key: unknown) => {
    const i = containers.lastIndexOf(key);
    if (i >= 0) containers.splice(i, 1);
  };
  let model: () => string | undefined = () => {
    try {
      return settingsOrManager?.llm?.metadata?.model;
    } catch {
      return undefined;
    }
  };

  const handlers: Record<string, Handler> = {
    'query-start': (ev) => {
      const d = detail(ev);
      tracker.start(`q:${d?.id}`, 'llamaindex.query', { query: queryText(d?.query) }, { kind: 'chain', parentId: parent() });
      containers.push(`q:${d?.id}`);
    },
    'query-end': (ev) => {
      const d = detail(ev);
      const r = d?.response;
      const out = r && typeof r[Symbol.asyncIterator] === 'function' ? { streaming: true } : { response: r?.message?.content ?? r?.response ?? r?.toString?.(), source_nodes: nodes(r?.sourceNodes) };
      close(`q:${d?.id}`);
      tracker.end(`q:${d?.id}`, out);
    },
    'retrieve-start': (ev) => {
      const d = detail(ev);
      const key = d?.id ? `r:${d.id}` : Symbol('retrieve');
      if (!d?.id) pendingRetrieves.push(key);
      tracker.start(key, 'llamaindex.retrieve', { query: queryText(d?.query) }, { kind: 'retriever', parentId: parent() });
    },
    'retrieve-end': (ev) => {
      const d = detail(ev);
      const key = d?.id ? `r:${d.id}` : pendingRetrieves.shift();
      tracker.end(key, { nodes: nodes(d?.nodes) });
    },
    'llm-start': (ev) => {
      const d = detail(ev);
      const m = model();
      llmModels.set(d?.id, m);
      const attrs: Record<string, unknown> = { 'gen_ai.operation.name': 'chat' };
      if (m) attrs['gen_ai.request.model'] = m;
      tracker.start(`l:${d?.id}`, 'llamaindex.llm.chat', { messages: (d?.messages ?? []).map(message), model: m }, { kind: 'llm', attributes: attrs, parentId: parent() });
    },
    'llm-end': (ev) => {
      const d = detail(ev);
      const r = d?.response;
      const m = llmModels.get(d?.id);
      llmModels.delete(d?.id);
      const out = { message: r?.message ? message(r.message) : r, raw: r?.raw ?? null };
      tracker.end(`l:${d?.id}`, out, undefined, usageOf(r?.raw, m));
    },
    'llm-tool-call': (ev) => {
      const tc = detail(ev)?.toolCall;
      if (!tc) return;
      const attrs: Record<string, unknown> = { 'gen_ai.operation.name': 'execute_tool', 'gen_ai.tool.name': tc.name };
      if (tc.id) attrs['gen_ai.tool.call.id'] = tc.id;
      const input = tc.input && typeof tc.input === 'object' && !Array.isArray(tc.input) ? { ...tc.input } : { input: tc.input };
      tracker.start(toolKey(tc), `llamaindex.tool.${tc.name ?? 'tool'}`, input, { kind: 'tool', displayName: tc.name, attributes: attrs, parentId: parent() });
    },
    'llm-tool-result': (ev) => {
      const d = detail(ev);
      const tr = d?.toolResult;
      tracker.end(toolKey(d?.toolCall), tr?.output ?? tr, tr?.isError ? asError(typeof tr.output === 'string' ? tr.output : 'tool error') : undefined);
    },
    'agent-start': (ev) => {
      const d = detail(ev);
      const key = Symbol('agent');
      pendingAgents.push(key);
      const step = d?.startStep;
      tracker.start(key, 'llamaindex.agent', { input: step?.input ?? step?.context?.store?.messages?.at?.(-1)?.content }, { kind: 'agent', parentId: parent() });
      containers.push(key);
    },
    'agent-end': (ev) => {
      const d = detail(ev);
      const step = d?.endStep;
      const key = pendingAgents.pop();
      close(key);
      tracker.end(key, { output: step?.output?.message?.content ?? step?.output ?? null });
    },
  };
  for (const [name, h] of Object.entries(handlers)) {
    const safe: Handler = (ev) => {
      try {
        h(ev);
      } catch (e) {
        logger.warn(`weft: llamaindex ${name} handler failed: ${(e as Error).message}`);
      }
    };
    handlers[name] = safe;
    cm.on(name, safe);
  }
  const inst: LlamaIndexInstrumentation = {
    uninstrument() {
      for (const [name, h] of Object.entries(handlers)) cm.off?.(name, h);
      installed.delete(cm);
      tracker.endAll();
      model = () => undefined;
    },
  };
  installed.set(cm, inst);
  return inst;
}
