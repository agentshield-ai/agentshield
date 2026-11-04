/**
 * AWS Bedrock Agents (`@aws-sdk/client-bedrock-agent-runtime`):
 * `InvokeAgentCommand` and `InvokeInlineAgentCommand` sent through
 * `client.send(...)`.
 *
 * ```ts
 * const agents = weft.wrapBedrockAgent(new BedrockAgentRuntimeClient({ region }));
 * // or every client: weft.instrumentBedrockAgent(BedrockAgentRuntimeClient)
 * ```
 *
 * The response's `completion` stream is passed through; while the caller
 * iterates it, each event becomes calls (the same shapes as the Python SDK):
 *
 * - `bedrock_agent.invoke_agent` (agent): the request, a turn of the
 *   conversation `sessionId`; output = the streamed answer, citations,
 *   `returnControl` requests, file names. Ends when the stream is consumed.
 * - `bedrock_agent.model_invocation` (llm): each `modelInvocationInput` /
 *   `modelInvocationOutput` pair of the orchestration, pre-/post-processing
 *   and routing traces, with prompt, raw/parsed response and token usage.
 * - `bedrock_agent.action_group`, `bedrock_agent.code_interpreter` (tool) and
 *   `bedrock_agent.knowledge_base` (retriever): `invocationInput` → `observation`.
 * - `bedrock_agent.collaborator` (agent): multi-agent collaboration; the
 *   collaborator's own trace events nest under it.
 * - `bedrock_agent.guardrail` (guardrail), `bedrock_agent.return_control` (tool).
 *
 * Trace events need `enableTrace: true`. A `failureTrace` ends the open steps
 * and the agent call with an error.
 */
import type { Call } from '../call.js';
import type { WeftClient } from '../client.js';
import { currentContext, withSession } from '../context.js';
import type { OpMeta } from '../op.js';
import { addUsage } from '../usage.js';
import { integrationAttributes, integrationOp, type IntegrationOpSettings } from './settings.js';

const INTEGRATION = 'bedrock';
const PROVIDER = 'aws.bedrock';
export const AGENT_OPS: Record<string, string> = {
  InvokeAgentCommand: 'bedrock_agent.invoke_agent',
  InvokeInlineAgentCommand: 'bedrock_agent.invoke_inline_agent',
};
const STEPS: Record<string, string> = {
  orchestrationTrace: 'orchestration',
  preProcessingTrace: 'pre_processing',
  postProcessingTrace: 'post_processing',
  routingClassifierTrace: 'routing',
};

/** The agent command a `send` argument is, if any. */
export function agentCommandName(cmd: any): string | undefined {
  const n = cmd?.constructor?.name;
  if (n && AGENT_OPS[n]) return n;
  const input = cmd?.input;
  if (!input || typeof input !== 'object' || !input.sessionId) return undefined;
  if (input.agentId && input.agentAliasId) return 'InvokeAgentCommand';
  if (input.foundationModel && input.instruction !== undefined) return 'InvokeInlineAgentCommand';
  return undefined;
}

function int(v: unknown): number {
  const n = Number(v ?? 0);
  return Number.isFinite(n) ? n : 0;
}

function text(b: unknown): string {
  if (b instanceof Uint8Array) return new TextDecoder().decode(b);
  return typeof b === 'string' ? b : '';
}

function maybeJson(v: unknown): unknown {
  if (v instanceof Uint8Array) v = new TextDecoder().decode(v);
  if (typeof v === 'string') {
    const s = v.trim();
    if (s.startsWith('{') || s.startsWith('[')) {
      try {
        return JSON.parse(s);
      } catch {
        return v;
      }
    }
  }
  return v;
}

function params(ps: unknown): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  if (Array.isArray(ps)) for (const p of ps) if (p && typeof p === 'object' && (p as any).name != null) out[String((p as any).name)] = (p as any).value;
  return out;
}

function defined(o: Record<string, unknown>): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const [k, v] of Object.entries(o)) if (v !== undefined && v !== null && v !== '') out[k] = v;
  return out;
}

/** Turns a Bedrock Agents response stream into Weft calls (usable directly for raw event lists). */
export class AgentTraceProcessor {
  text = '';
  citations: unknown[] = [];
  returnControl: Array<Record<string, unknown>> = [];
  files: Array<Record<string, unknown>> = [];
  rationales: string[] = [];
  finalResponse: string | undefined;
  failure: string | undefined;
  events = 0;
  foundationModel: string | undefined;
  usage = { input_tokens: 0, output_tokens: 0 };
  done = false;
  private open = new Map<string, { call: Call; meta: OpMeta }>();
  private collaborators = new Map<string, { call: Call; meta: OpMeta }>();
  private models = new Map<string, string>();

  constructor(
    private readonly client: WeftClient,
    readonly root: Call,
    private readonly rootMeta: OpMeta,
    readonly agentName: string,
    readonly sessionId: string | undefined,
    model?: string,
  ) {
    this.foundationModel = model;
  }

  private start(name: string, kind: string, inputs: Record<string, unknown>, parent: Call, displayName: string, attrs: Record<string, unknown>): { call: Call; meta: OpMeta } | null {
    const meta = integrationOp(INTEGRATION, name, kind);
    try {
      const call = this.client.createCall(meta, inputs, {
        parent,
        displayName,
        attributes: integrationAttributes(INTEGRATION, defined({ 'gen_ai.provider.name': PROVIDER, 'gen_ai.conversation.id': this.sessionId, ...attrs })),
      });
      return { call, meta };
    } catch {
      return null;
    }
  }

  private end(s: { call: Call; meta: OpMeta } | null | undefined, output?: unknown, err?: unknown): void {
    if (!s) return;
    try {
      this.client.finishCall(s.call, output ?? null, err, s.meta);
    } catch {
      /* never break the stream */
    }
  }

  private parent(outer: any): Call {
    const name = outer?.collaboratorName;
    const chain = outer?.callerChain ?? [];
    if (name && chain.length > 1 && this.collaborators.has(name)) return this.collaborators.get(name)!.call;
    return this.root;
  }

  add(ev: any): void {
    if (!ev || typeof ev !== 'object') return;
    this.events++;
    if (ev.chunk) {
      this.text += text(ev.chunk.bytes);
      this.citations.push(...(ev.chunk.attribution?.citations ?? []));
    } else if (ev.trace) {
      this.trace(ev.trace, ev.trace.trace ?? {});
    } else if (ev.returnControl) {
      this.returnCtl(ev.returnControl);
    } else if (ev.files) {
      for (const f of ev.files.files ?? []) this.files.push({ name: f.name, type: f.type, size: f.bytes?.length ?? null });
    }
  }

  private trace(outer: any, tr: any): void {
    const parent = this.parent(outer);
    for (const step of Object.keys(STEPS)) if (tr[step]) this.step(step, tr[step], parent);
    if (tr.guardrailTrace) {
      const { traceId, ...rest } = tr.guardrailTrace;
      this.end(this.start('bedrock_agent.guardrail', 'guardrail', { trace_id: traceId }, parent, 'guardrail', {}), rest);
    }
    if (tr.failureTrace) {
      this.failure = String(tr.failureTrace.failureReason ?? 'agent failure');
      const tid = tr.failureTrace.traceId;
      for (const [k, s] of [...this.open]) {
        if (tid && k.startsWith(`${tid}|`)) {
          this.open.delete(k);
          this.end(s, null, new Error(this.failure));
        }
      }
    }
    const custom = tr.customOrchestrationTrace?.event?.text;
    if (custom) this.rationales.push(String(custom));
  }

  private step(step: string, t: any, parent: Call): void {
    const stage = STEPS[step];
    const mi = t.modelInvocationInput;
    if (mi) {
      const tid = String(mi.traceId ?? '');
      const model = mi.foundationModel ?? this.foundationModel;
      if (mi.foundationModel) this.foundationModel ??= mi.foundationModel;
      if (tid && model) this.models.set(tid, model);
      const prompt = maybeJson(mi.text) as any;
      const inputs: Record<string, unknown> = { model, stage, type: mi.type };
      if (prompt && typeof prompt === 'object' && !Array.isArray(prompt) && ('messages' in prompt || 'system' in prompt)) {
        for (const k of ['system', 'messages']) if (k in prompt) inputs[k] = prompt[k];
      } else if (prompt !== undefined) inputs.prompt = prompt;
      for (const k of ['inferenceConfiguration', 'promptCreationMode', 'parserMode', 'overrideLambda']) if (mi[k] != null) inputs[k] = mi[k];
      const s = this.start('bedrock_agent.model_invocation', 'llm', inputs, parent, model ? `chat ${model}` : 'chat', {
        'gen_ai.operation.name': 'chat',
        'gen_ai.request.model': model,
        'bedrock_agent.stage': stage,
      });
      if (s) {
        this.end(this.open.get(`${tid}|llm`));
        this.open.set(`${tid}|llm`, s);
      }
    }
    const mo = t.modelInvocationOutput;
    if (mo) {
      const tid = String(mo.traceId ?? '');
      let s = this.open.get(`${tid}|llm`) ?? null;
      this.open.delete(`${tid}|llm`);
      const model = this.models.get(tid) ?? this.foundationModel;
      if (!s) s = this.start('bedrock_agent.model_invocation', 'llm', { model, stage }, parent, model ? `chat ${model}` : 'chat', { 'gen_ai.operation.name': 'chat', 'gen_ai.request.model': model });
      const meta = mo.metadata ?? {};
      const p = int(meta.usage?.inputTokens);
      const c = int(meta.usage?.outputTokens);
      if (s && (p || c)) addUsage(s.call, model ?? `bedrock-agent:${this.agentName}`, p, c);
      this.usage.input_tokens += p;
      this.usage.output_tokens += c;
      const out: Record<string, unknown> = { content: maybeJson(mo.rawResponse?.content) };
      if (mo.parsedResponse != null) out.parsed_response = mo.parsedResponse;
      if (mo.reasoningContent != null) out.reasoning = mo.reasoningContent;
      const { usage: _u, ...restMeta } = meta;
      if (Object.keys(restMeta).length) out.metadata = restMeta;
      this.end(s, out);
    }
    if (t.rationale?.text) this.rationales.push(String(t.rationale.text));
    if (t.invocationInput) this.invocation(t.invocationInput, parent);
    if (t.observation) this.observation(t.observation);
  }

  private invocation(ii: any, parent: Call): void {
    const tid = String(ii.traceId ?? '');
    let s: { call: Call; meta: OpMeta } | null = null;
    if (ii.actionGroupInvocationInput) {
      const ag = ii.actionGroupInvocationInput;
      const group = ag.actionGroupName ?? 'action_group';
      const fn = ag.function ?? ag.apiPath ?? '';
      const tool = fn ? `${group}.${fn}` : group;
      const inputs: Record<string, unknown> = { action_group: group };
      if (ag.function) inputs.function = ag.function;
      if (ag.apiPath) Object.assign(inputs, { api_path: ag.apiPath, verb: ag.verb });
      inputs.parameters = params(ag.parameters);
      if (ag.requestBody) inputs.request_body = ag.requestBody;
      if (ag.executionType) inputs.execution_type = ag.executionType;
      s = this.start('bedrock_agent.action_group', 'tool', inputs, parent, `execute_tool ${tool}`, {
        'gen_ai.operation.name': 'execute_tool',
        'gen_ai.tool.name': tool,
        'gen_ai.tool.call.id': ag.invocationId ?? tid,
      });
    } else if (ii.knowledgeBaseLookupInput) {
      const kb = ii.knowledgeBaseLookupInput;
      const name = `knowledge_base ${kb.knowledgeBaseId ?? ''}`.trim();
      s = this.start('bedrock_agent.knowledge_base', 'retriever', { knowledge_base_id: kb.knowledgeBaseId, query: kb.text }, parent, name, {
        'gen_ai.operation.name': 'execute_tool',
        'gen_ai.tool.name': name,
        'gen_ai.tool.call.id': tid,
      });
    } else if (ii.codeInterpreterInvocationInput) {
      const ci = ii.codeInterpreterInvocationInput;
      s = this.start('bedrock_agent.code_interpreter', 'tool', { code: ci.code, files: ci.files }, parent, 'execute_tool code_interpreter', {
        'gen_ai.operation.name': 'execute_tool',
        'gen_ai.tool.name': 'code_interpreter',
        'gen_ai.tool.call.id': tid,
      });
    } else if (ii.agentCollaboratorInvocationInput) {
      const co = ii.agentCollaboratorInvocationInput;
      const name = co.agentCollaboratorName ?? 'collaborator';
      const input = co.input && typeof co.input === 'object' && 'text' in co.input ? co.input.text : co.input;
      s = this.start('bedrock_agent.collaborator', 'agent', { collaborator: name, alias_arn: co.agentCollaboratorAliasArn, input }, parent, `invoke_agent ${name}`, {
        'gen_ai.operation.name': 'invoke_agent',
        'gen_ai.agent.name': name,
      });
      if (s) this.collaborators.set(name, s);
    } else if (ii.invocationType && ii.invocationType !== 'FINISH') {
      const t = String(ii.invocationType).toLowerCase();
      s = this.start('bedrock_agent.invocation', 'tool', { invocation_type: ii.invocationType }, parent, `execute_tool ${t}`, { 'gen_ai.operation.name': 'execute_tool', 'gen_ai.tool.name': t });
    }
    if (s) {
      this.end(this.open.get(`${tid}|tool`));
      this.open.set(`${tid}|tool`, s);
    }
  }

  private observation(ob: any): void {
    const tid = String(ob.traceId ?? '');
    let s = this.open.get(`${tid}|tool`);
    this.open.delete(`${tid}|tool`);
    let out: unknown = null;
    let err: unknown;
    if (ob.actionGroupInvocationOutput) out = maybeJson(ob.actionGroupInvocationOutput.text);
    else if (ob.knowledgeBaseLookupOutput) {
      const refs = ob.knowledgeBaseLookupOutput.retrievedReferences ?? [];
      out = { retrieved_references: refs, count: refs.length };
    } else if (ob.codeInterpreterInvocationOutput) {
      const co = ob.codeInterpreterInvocationOutput;
      out = defined({ executionOutput: co.executionOutput, executionError: co.executionError, files: co.files, executionTimeout: co.executionTimeout });
      if (co.executionError) err = new Error(String(co.executionError));
    } else if (ob.agentCollaboratorInvocationOutput) {
      const co = ob.agentCollaboratorInvocationOutput;
      const o = co.output;
      out = o && typeof o === 'object' && 'text' in o ? o.text : o;
      const name = co.agentCollaboratorName;
      if (name && this.collaborators.get(name) === s) this.collaborators.delete(name);
      else if (!s && name && this.collaborators.has(name)) {
        s = this.collaborators.get(name);
        this.collaborators.delete(name);
      }
    }
    if (ob.finalResponse) this.finalResponse = ob.finalResponse.text;
    if (ob.repromptResponse && out === null) out = { reprompt: ob.repromptResponse };
    if (s) this.end(s, out, err);
  }

  private returnCtl(rc: any): void {
    const entry: Record<string, unknown> & { invocations: unknown[] } = { invocation_id: rc.invocationId, invocations: [] };
    for (const inv of rc.invocationInputs ?? []) {
      const fi = inv.functionInvocationInput ?? inv.apiInvocationInput ?? {};
      const group = fi.actionGroup ?? 'action_group';
      const fn = fi.function ?? fi.apiPath ?? '';
      const tool = fn ? `${group}.${fn}` : group;
      const inputs: Record<string, unknown> = { action_group: group, parameters: params(fi.parameters) };
      if (fi.function) inputs.function = fi.function;
      if (fi.apiPath) Object.assign(inputs, { api_path: fi.apiPath, verb: fi.httpMethod });
      entry.invocations.push({ tool, ...inputs });
      const s = this.start('bedrock_agent.return_control', 'tool', inputs, this.root, `execute_tool ${tool}`, {
        'gen_ai.operation.name': 'execute_tool',
        'gen_ai.tool.name': tool,
        'gen_ai.tool.call.id': rc.invocationId,
      });
      this.end(s, { status: 'returned_to_application', invocation_id: rc.invocationId });
    }
    this.returnControl.push(entry);
  }

  output(extra: Record<string, unknown> = {}): Record<string, unknown> {
    const out: Record<string, unknown> = { completion: this.text };
    const answer = this.text || this.finalResponse;
    if (answer) out.messages = [{ role: 'assistant', content: answer }];
    if (this.finalResponse !== undefined && this.finalResponse !== this.text) out.final_response = this.finalResponse;
    if (this.citations.length) out.citations = this.citations;
    if (this.returnControl.length) out.return_control = this.returnControl;
    if (this.files.length) out.files = this.files;
    if (this.rationales.length) out.rationales = this.rationales;
    if (this.foundationModel) out.foundation_model = this.foundationModel;
    if (this.usage.input_tokens || this.usage.output_tokens) out.usage = { ...this.usage };
    out.event_count = this.events;
    return { ...out, ...extra };
  }

  finish(err?: unknown, extra: Record<string, unknown> = {}): void {
    if (this.done) return;
    this.done = true;
    const e = err ?? (this.failure ? new Error(this.failure) : undefined);
    for (const s of this.open.values()) this.end(s, null, e ?? new Error('the agent response ended before this step finished'));
    this.open.clear();
    for (const s of this.collaborators.values()) this.end(s, null, e);
    this.collaborators.clear();
    this.client.finishCall(this.root, this.output(extra), e, this.rootMeta);
  }
}

/** Start the agent call for an InvokeAgent / InvokeInlineAgent request. */
export function startAgentCall(client: WeftClient, name: string, input: Record<string, any>, local?: IntegrationOpSettings): AgentTraceProcessor {
  const inline = name === 'InvokeInlineAgentCommand';
  const agentName = String(input.agentName ?? input.agentId ?? (inline ? 'inline_agent' : 'bedrock_agent'));
  const sessionId: string | undefined = input.sessionId;
  const model: string | undefined = inline ? input.foundationModel : undefined;
  const meta = integrationOp(INTEGRATION, AGENT_OPS[name], 'agent', {}, local);
  const attrs = integrationAttributes(
    INTEGRATION,
    defined({
      'gen_ai.operation.name': 'invoke_agent',
      'gen_ai.agent.name': agentName,
      'gen_ai.agent.id': input.agentId,
      'gen_ai.agent.version': input.agentAliasId,
      'gen_ai.provider.name': PROVIDER,
      'gen_ai.conversation.id': sessionId,
      'gen_ai.request.model': model,
    }),
    local,
  );
  const inputs: Record<string, unknown> = { ...input };
  if (typeof input.inputText === 'string') inputs.messages = [{ role: 'user', content: input.inputText }];
  const make = () => client.createCall(meta, inputs, { displayName: `invoke_agent ${agentName}`, attributes: attrs });
  // A Bedrock session is a conversation: thread it unless the caller already did.
  const ctx = currentContext();
  const parent = ctx.stack[ctx.stack.length - 1];
  const call = sessionId && !ctx.session && !parent?.sessionId ? withSession(sessionId, make) : make();
  return new AgentTraceProcessor(client, call, meta, agentName, sessionId, model);
}

/** Wrap the response's `completion` stream so iterating it records the trace. */
export function handleAgentResponse(proc: AgentTraceProcessor, res: any): any {
  const extra = defined({ sessionId: res?.sessionId, memoryId: res?.memoryId, contentType: res?.contentType });
  const stream = res?.completion;
  if (!stream || typeof stream[Symbol.asyncIterator] !== 'function') {
    proc.finish(undefined, extra);
    return res;
  }
  async function* iterate() {
    try {
      for await (const ev of stream as AsyncIterable<unknown>) {
        try {
          proc.add(ev);
        } catch {
          /* best effort */
        }
        yield ev;
      }
    } catch (e) {
      proc.finish(e, extra);
      throw e;
    } finally {
      proc.finish(undefined, extra);
    }
  }
  res.completion = new Proxy(stream, {
    get(target, prop) {
      if (prop === Symbol.asyncIterator) return () => iterate();
      const v = Reflect.get(target, prop, target);
      return typeof v === 'function' ? v.bind(target) : v;
    },
  });
  return res;
}
