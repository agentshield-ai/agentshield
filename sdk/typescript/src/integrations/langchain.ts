/**
 * LangChain.js callback handler.
 *
 * ```ts
 * const handler = new weft.WeftCallbackHandler();
 * await chain.invoke(input, { callbacks: [handler] });
 * // or for everything: model.withConfig({ callbacks: [handler] })
 * ```
 *
 * Chains, chat models / LLMs, tools, retrievers and agents become calls
 * (`langchain.<Kind>.<name>`) nested by LangChain's run ids; the outermost run
 * nests under the current op. LLM runs record token usage (from
 * `llmOutput.tokenUsage` or the message's `usage_metadata`) and the model;
 * tool runs carry `gen_ai.tool.name` and the tool call id. LangGraph runs
 * through the same callbacks: runs carry `langgraph.node` / `langgraph.step`,
 * the session (`gen_ai.conversation.id`) is LangGraph's `thread_id` (or a
 * `session_id` / `conversation_id` in the run metadata), and each node that
 * ran a model becomes an agent invocation named after the node, so the server
 * reads consecutive agent nodes as hand-offs.
 */
import { pickTraceSettings, type IntegrationTraceSettings, type WrapperTraceOptions } from './settings.js';
import { RunTracker, type RunUsage } from './runs.js';

const INTEGRATION = 'langchain';

const SESSION_KEYS = ['thread_id', 'session_id', 'conversation_id'];

/** LangGraph's node metadata of a run (`langgraph_node`, `langgraph_step`, ...), if any. */
function langgraphInfo(md: any): Record<string, unknown> | null {
  if (!md || typeof md !== 'object' || md.langgraph_node === undefined) return null;
  const info: Record<string, unknown> = {
    node: md.langgraph_node,
    step: md.langgraph_step,
    triggers: md.langgraph_triggers,
    path: md.langgraph_path,
    checkpoint_ns: md.langgraph_checkpoint_ns ?? md.checkpoint_ns,
    thread_id: md.thread_id,
  };
  for (const k of Object.keys(info)) if (info[k] === undefined || info[k] === null) delete info[k];
  return info;
}

/** A LangGraph node that ran a model is an agent: an `invoke_agent` of the node's name (known when it ends). */
function agentNodeAttrs(call: any): Record<string, unknown> | null {
  if (!call?.llmDescendant) return null;
  const node = call.attributes?.langgraph?.node;
  if (!node || !String(call.name ?? '').includes('langgraph.node.')) return null;
  return { gen_ai: { operation: { name: 'invoke_agent' }, agent: { name: String(node) } } };
}

function serializedName(s: any, fallback: string): string {
  if (!s || typeof s !== 'object') return fallback;
  if (typeof s.name === 'string' && s.name) return s.name;
  if (Array.isArray(s.id) && s.id.length) return String(s.id[s.id.length - 1]);
  return fallback;
}

function messageToDict(m: any): Record<string, unknown> {
  if (!m || typeof m !== 'object') return { role: 'user', content: m };
  let role: string = m.role ?? '';
  const type = typeof m._getType === 'function' ? m._getType() : typeof m.getType === 'function' ? m.getType() : m.type ?? m.lc_id?.at?.(-1);
  if (!role) {
    role = ({ human: 'user', ai: 'assistant', system: 'system', tool: 'tool', function: 'function', generic: 'user', developer: 'developer' } as Record<string, string>)[
      String(type).toLowerCase().replace(/messagechunk$|message$/, '')
    ] ?? String(type ?? 'user');
  }
  const kw = m.kwargs ?? m;
  const out: Record<string, unknown> = { role, content: kw.content };
  if (kw.tool_calls?.length) out.tool_calls = kw.tool_calls;
  if (kw.tool_call_id) out.tool_call_id = kw.tool_call_id;
  if (kw.name) out.name = kw.name;
  return out;
}

function usageFromResult(output: any, model: string | undefined): RunUsage | null {
  const tu = output?.llmOutput?.tokenUsage ?? output?.llmOutput?.estimatedTokenUsage ?? output?.llmOutput?.usage;
  if (tu && (tu.promptTokens != null || tu.completionTokens != null || tu.input_tokens != null)) {
    return {
      model,
      promptTokens: tu.promptTokens ?? tu.prompt_tokens ?? tu.input_tokens,
      completionTokens: tu.completionTokens ?? tu.completion_tokens ?? tu.output_tokens,
      totalTokens: tu.totalTokens ?? tu.total_tokens,
    };
  }
  let p = 0;
  let c = 0;
  let t = 0;
  let found = false;
  for (const gens of output?.generations ?? []) {
    for (const g of gens ?? []) {
      const um = g?.message?.usage_metadata ?? g?.message?.kwargs?.usage_metadata;
      if (um) {
        found = true;
        p += um.input_tokens ?? 0;
        c += um.output_tokens ?? 0;
        t += um.total_tokens ?? 0;
      }
    }
  }
  return found ? { model, promptTokens: p, completionTokens: c, totalTokens: t || p + c } : null;
}

function modelFrom(extra: any, metadata: any, output?: any): string | undefined {
  const inv = extra?.invocation_params ?? {};
  const fromOut = output?.llmOutput?.model_name ?? output?.llmOutput?.model ?? output?.generations?.[0]?.[0]?.message?.response_metadata?.model_name;
  return inv.model ?? inv.model_name ?? inv.modelName ?? metadata?.ls_model_name ?? fromOut;
}

function llmOutput(output: any): unknown {
  const gens = output?.generations;
  if (!Array.isArray(gens)) return output;
  const choices = gens.flat().map((g: any, i: number) => (g?.message ? { index: i, message: messageToDict(g.message), finish_reason: g.generationInfo?.finish_reason ?? null } : { index: i, text: g?.text }));
  return { choices, llm_output: output.llmOutput ?? null };
}

function errOf(e: unknown): unknown {
  return e instanceof Error ? e : new Error(String((e as any)?.message ?? e));
}

/** A LangChain.js callback handler that records runs as Weft calls. */
export class WeftCallbackHandler {
  readonly name = 'weft_callback_handler';
  /** Run handlers inline so the outermost run sees the caller's current op. */
  readonly awaitHandlers = true;
  readonly raiseError = false;
  readonly ignoreLLM = false;
  readonly ignoreChain = false;
  readonly ignoreAgent = false;
  readonly ignoreRetriever = false;
  readonly ignoreCustomEvent = true;
  readonly lc_serializable = false;
  private tracker: RunTracker;
  private models = new Map<string, string | undefined>();

  constructor(opts: WrapperTraceOptions = {}) {
    this.tracker = new RunTracker(INTEGRATION, pickTraceSettings(opts));
  }

  copy(): this {
    return this;
  }

  // chains (also LangGraph nodes and runnables)
  handleChainStart(chain: any, inputs: any, runId: string, parentRunId?: string, tags?: string[], metadata?: Record<string, unknown>, runType?: string, runName?: string): void {
    const name = runName ?? serializedName(chain, 'Chain');
    let kind = runType === 'agent' || /agent/i.test(name) ? 'agent' : 'chain';
    const ins = inputs && typeof inputs === 'object' && !Array.isArray(inputs) ? { ...inputs } : { input: inputs };
    let op = `langchain.Chain.${name}`;
    const lg = langgraphInfo(metadata);
    if (lg && lg.node === name) op = `langgraph.node.${name}`;
    else if (!lg && name === 'LangGraph' && !parentRunId) {
      op = 'langgraph.graph';
      kind = 'workflow'; // the graph run; its nodes that run a model are the agents
    }
    this.tracker.start(runId, op, ins, { parentId: parentRunId, kind, displayName: name, attributes: this.attrs(tags, metadata) });
  }

  handleChainEnd(outputs: any, runId: string): void {
    this.tracker.end(runId, outputs, undefined, null, undefined, { attributes: agentNodeAttrs(this.tracker.get(runId)) });
  }

  handleChainError(err: unknown, runId: string): void {
    this.tracker.end(runId, undefined, errOf(err), null, undefined, { attributes: agentNodeAttrs(this.tracker.get(runId)) });
  }

  // LLMs and chat models
  handleLLMStart(llm: any, prompts: string[], runId: string, parentRunId?: string, extraParams?: Record<string, unknown>, tags?: string[], metadata?: Record<string, unknown>, runName?: string): void {
    const name = runName ?? serializedName(llm, 'LLM');
    const model = modelFrom(extraParams, metadata);
    this.models.set(runId, model);
    this.tracker.start(runId, `langchain.Llm.${name}`, { prompts, model, invocation_params: (extraParams as any)?.invocation_params }, {
      parentId: parentRunId,
      kind: 'llm',
      displayName: name,
      attributes: { ...this.attrs(tags, metadata), ...this.llmAttrs(model, metadata) },
    });
  }

  handleChatModelStart(llm: any, messages: any[][], runId: string, parentRunId?: string, extraParams?: Record<string, unknown>, tags?: string[], metadata?: Record<string, unknown>, runName?: string): void {
    const name = runName ?? serializedName(llm, 'ChatModel');
    const model = modelFrom(extraParams, metadata);
    this.models.set(runId, model);
    const msgs = (messages?.[0] ?? []).map(messageToDict);
    this.tracker.start(runId, `langchain.ChatModel.${name}`, { messages: msgs, model, invocation_params: (extraParams as any)?.invocation_params }, {
      parentId: parentRunId,
      kind: 'llm',
      displayName: name,
      attributes: { ...this.attrs(tags, metadata), ...this.llmAttrs(model, metadata) },
    });
  }

  handleLLMEnd(output: any, runId: string): void {
    const model = modelFrom(undefined, undefined, output) ?? this.models.get(runId);
    this.models.delete(runId);
    this.tracker.end(runId, llmOutput(output), undefined, usageFromResult(output, model));
  }

  handleLLMError(err: unknown, runId: string): void {
    this.models.delete(runId);
    this.tracker.end(runId, undefined, errOf(err));
  }

  // tools
  handleToolStart(tool: any, input: any, runId: string, parentRunId?: string, tags?: string[], metadata?: Record<string, unknown>, runName?: string, toolCallId?: string): void {
    const name = runName ?? serializedName(tool, 'tool');
    let parsed: unknown = input;
    if (typeof input === 'string') {
      try {
        parsed = JSON.parse(input);
      } catch {
        parsed = input;
      }
    }
    const ins = parsed && typeof parsed === 'object' && !Array.isArray(parsed) ? { ...(parsed as object) } : { input: parsed };
    const attrs: Record<string, unknown> = { ...this.attrs(tags, metadata), 'gen_ai.operation.name': 'execute_tool', 'gen_ai.tool.name': name };
    const tcid = toolCallId ?? (metadata as any)?.tool_call_id;
    if (tcid) attrs['gen_ai.tool.call.id'] = tcid;
    this.tracker.start(runId, `langchain.Tool.${name}`, ins as Record<string, unknown>, { parentId: parentRunId, kind: 'tool', displayName: name, attributes: attrs });
  }

  handleToolEnd(output: any, runId: string): void {
    const out = output && typeof output === 'object' && 'content' in output && ('tool_call_id' in output || 'lc_kwargs' in output) ? { content: output.content, tool_call_id: output.tool_call_id, status: output.status } : output;
    this.tracker.end(runId, out);
  }

  handleToolError(err: unknown, runId: string): void {
    this.tracker.end(runId, undefined, errOf(err));
  }

  // retrievers
  handleRetrieverStart(retriever: any, query: string, runId: string, parentRunId?: string, tags?: string[], metadata?: Record<string, unknown>, name?: string): void {
    const n = name ?? serializedName(retriever, 'Retriever');
    this.tracker.start(runId, `langchain.Retriever.${n}`, { query }, { parentId: parentRunId, kind: 'retriever', displayName: n, attributes: this.attrs(tags, metadata) });
  }

  handleRetrieverEnd(documents: any[], runId: string): void {
    const docs = (documents ?? []).map((d: any) => ({ page_content: d?.pageContent ?? d?.page_content, metadata: d?.metadata, id: d?.id }));
    this.tracker.end(runId, { documents: docs });
  }

  handleRetrieverError(err: unknown, runId: string): void {
    this.tracker.end(runId, undefined, errOf(err));
  }

  // agents (legacy AgentExecutor): actions are recorded as tool runs already
  handleAgentAction(): void {}
  handleAgentEnd(): void {}
  handleText(): void {}
  handleLLMNewToken(): void {}

  private attrs(tags?: string[], metadata?: Record<string, unknown>): Record<string, unknown> {
    const out: Record<string, unknown> = {};
    if (tags?.length) out.langchain = { tags };
    const md: Record<string, unknown> = {};
    for (const [k, v] of Object.entries(metadata ?? {})) if (v === null || ['string', 'number', 'boolean'].includes(typeof v)) md[k] = v;
    if (Object.keys(md).length) out.langchain = { ...(out.langchain as object), metadata: md };
    const lg = langgraphInfo(metadata);
    if (lg) out.langgraph = lg;
    const session = SESSION_KEYS.map((k) => (metadata as any)?.[k]).find((v) => (typeof v === 'string' || typeof v === 'number') && v !== '');
    if (session !== undefined) out['gen_ai.conversation.id'] = String(session);
    return out;
  }

  private llmAttrs(model: string | undefined, metadata?: Record<string, unknown>): Record<string, unknown> {
    const out: Record<string, unknown> = { 'gen_ai.operation.name': 'chat' };
    if (model) out['gen_ai.request.model'] = model;
    const provider = (metadata as any)?.ls_provider;
    if (provider) out['gen_ai.provider.name'] = provider;
    return out;
  }
}

/** A new `WeftCallbackHandler` (pass it in `callbacks`). */
export function weftCallbackHandler(opts: WrapperTraceOptions = {}): WeftCallbackHandler {
  return new WeftCallbackHandler(opts);
}
