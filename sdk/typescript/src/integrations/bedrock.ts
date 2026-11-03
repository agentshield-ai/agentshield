/**
 * AWS Bedrock (`@aws-sdk/client-bedrock-runtime`): `ConverseCommand`,
 * `ConverseStreamCommand`, `InvokeModelCommand` and
 * `InvokeModelWithResponseStreamCommand` sent through `client.send(...)`.
 *
 * ```ts
 * const br = weft.wrapBedrock(new BedrockRuntimeClient({ region }));
 * // or every client: weft.instrumentBedrock(BedrockRuntimeClient)
 * ```
 *
 * Bedrock Agents (`@aws-sdk/client-bedrock-agent-runtime` `InvokeAgentCommand`,
 * `InvokeInlineAgentCommand`) are handled by the same wrapper; see
 * `bedrock_agents.ts` (`wrapBedrockAgent` / `instrumentBedrockAgent` are aliases).
 *
 * Request bodies are decoded from JSON; Converse usage comes from `usage`,
 * InvokeModel usage from the body (`usage`, `amazon-bedrock-invocationMetrics`,
 * Titan/Llama counters). Streams are passed through and accumulated (text,
 * stop reason, usage); the call ends when the stream is consumed.
 */
import type { Call } from '../call.js';
import { addUsage } from '../usage.js';
import { agentCommandName, handleAgentResponse, startAgentCall, type AgentTraceProcessor } from './bedrock_agents.js';
import { decodeBody, patchWith, proxyStream, withCall, type Accumulator } from './common.js';
import { integrationAttributes, integrationClient, integrationOp, pickOpSettings, type IntegrationOpSettings } from './settings.js';
import { bedrockContent, bedrockReasoningBlock } from '../reasoning.js';

const INTEGRATION = 'bedrock';

const OPS: Record<string, string> = {
  ConverseCommand: 'bedrock.converse',
  ConverseStreamCommand: 'bedrock.converse_stream',
  InvokeModelCommand: 'bedrock.invoke_model',
  InvokeModelWithResponseStreamCommand: 'bedrock.invoke_model_with_response_stream',
};

function commandName(cmd: any): string | undefined {
  const n = cmd?.constructor?.name;
  if (n && OPS[n]) return n;
  // minified builds: fall back to the command's middleware stack name / input shape
  const input = cmd?.input;
  if (!input || typeof input !== 'object' || !input.modelId) return undefined;
  if ('messages' in input || 'system' in input || 'inferenceConfig' in input) return n?.includes('Stream') ? 'ConverseStreamCommand' : 'ConverseCommand';
  if ('body' in input) return n?.includes('Stream') ? 'InvokeModelWithResponseStreamCommand' : 'InvokeModelCommand';
  return undefined;
}

function int(v: unknown): number {
  const n = Number(v ?? 0);
  return Number.isFinite(n) ? n : 0;
}

/** Usage with a gross prompt count (`p`) and the cache read (`cr`) / write (`cw`) parts of it. */
type BUsage = { p: number; c: number; cr?: number; cw?: number; t?: number };

/** Bedrock Converse usage: `inputTokens` excludes `cacheReadInputTokens` / `cacheWriteInputTokens`. */
function converseUsage(u: any): BUsage {
  const cr = int(u.cacheReadInputTokens);
  const cw = int(u.cacheWriteInputTokens);
  return { p: int(u.inputTokens) + cr + cw, c: int(u.outputTokens), cr, cw, t: u.totalTokens };
}

function addB(call: any, model: string | undefined, u: BUsage): void {
  addUsage(call, model, u.p, u.c, u.t, { cache_read_input_tokens: u.cr ?? 0, cache_creation_input_tokens: u.cw ?? 0 });
}

function usageFromBody(b: any): BUsage | null {
  if (!b || typeof b !== 'object') return null;
  const u = b.usage;
  if (u && typeof u === 'object' && (u.input_tokens != null || u.output_tokens != null)) {
    // Anthropic Messages bodies: input_tokens excludes the cache counts
    const cr = int(u.cache_read_input_tokens);
    const cw = int(u.cache_creation_input_tokens);
    return { p: int(u.input_tokens) + cr + cw, c: int(u.output_tokens), cr, cw };
  }
  if (u && typeof u === 'object' && u.inputTokens != null) return { ...converseUsage(u), t: undefined };
  if (u && typeof u === 'object' && u.prompt_tokens != null) return { p: int(u.prompt_tokens), c: int(u.completion_tokens), cr: int(u.prompt_tokens_details?.cached_tokens) };
  const m = b['amazon-bedrock-invocationMetrics'];
  if (m && typeof m === 'object') {
    const cr = int(m.cacheReadInputTokenCount);
    const cw = int(m.cacheWriteInputTokenCount);
    return { p: int(m.inputTokenCount) + cr + cw, c: int(m.outputTokenCount), cr, cw };
  }
  if (b.prompt_token_count != null || b.inputTextTokenCount != null) {
    const c = b.generation_token_count ?? (b.results ?? []).reduce((a: number, r: any) => a + int(r?.tokenCount), 0);
    return { p: int(b.prompt_token_count ?? b.inputTextTokenCount), c: int(c) };
  }
  return null;
}

class ConverseStreamAccumulator implements Accumulator {
  text = '';
  stopReason: string | null = null;
  usage: BUsage | null = null;
  toolUses: any[] = [];
  events = 0;
  /** Reasoning blocks by content block index: text, signature, redacted bytes. */
  reasoning = new Map<number, { text: string; signature?: string; redacted?: unknown }>();

  add(ev: any): void {
    this.events++;
    if (ev?.contentBlockStart?.start?.toolUse) this.toolUses.push({ ...ev.contentBlockStart.start.toolUse, input: '' });
    const d = ev?.contentBlockDelta?.delta;
    if (d?.text) this.text += d.text;
    const rc = d?.reasoningContent;
    if (rc && typeof rc === 'object') {
      const i = Number(ev.contentBlockDelta.contentBlockIndex ?? 0);
      const r = this.reasoning.get(i) ?? { text: '' };
      r.text += rc.text ?? '';
      if (rc.signature) r.signature = rc.signature;
      if (rc.redactedContent !== undefined && rc.redactedContent !== null) r.redacted = rc.redactedContent;
      this.reasoning.set(i, r);
    }
    if (d?.toolUse?.input && this.toolUses.length) this.toolUses[this.toolUses.length - 1].input += d.toolUse.input;
    if (ev?.messageStop) this.stopReason = ev.messageStop.stopReason ?? null;
    const u = ev?.metadata?.usage;
    if (u) this.usage = converseUsage(u);
  }

  result(): any {
    const content: any[] = [];
    for (const i of [...this.reasoning.keys()].sort((a, b) => a - b)) {
      const r = this.reasoning.get(i)!;
      content.push(
        r.redacted !== undefined
          ? bedrockReasoningBlock({ reasoningContent: { redactedContent: r.redacted } })
          : bedrockReasoningBlock({ reasoningContent: { reasoningText: { text: r.text, signature: r.signature } } }),
      );
    }
    if (this.text) content.push({ text: this.text });
    for (const t of this.toolUses) {
      let input: unknown = t.input;
      try {
        input = t.input ? JSON.parse(t.input) : {};
      } catch {
        /* keep the raw string */
      }
      content.push({ toolUse: { toolUseId: t.toolUseId, name: t.name, input } });
    }
    return { output: { message: { role: 'assistant', content } }, stopReason: this.stopReason, events: this.events, ...(this.usage ? { usage: { inputTokens: this.usage.p - (this.usage.cr ?? 0) - (this.usage.cw ?? 0), outputTokens: this.usage.c, ...(this.usage.cr ? { cacheReadInputTokens: this.usage.cr } : {}), ...(this.usage.cw ? { cacheWriteInputTokens: this.usage.cw } : {}) } } : {}) };
  }
}

class InvokeStreamAccumulator implements Accumulator {
  text = '';
  usage: BUsage | null = null;
  events = 0;

  add(ev: any): void {
    this.events++;
    const chunk = decodeBody(ev?.chunk?.bytes) as any;
    if (!chunk || typeof chunk !== 'object') return;
    this.text += chunk.delta?.text ?? chunk.completion ?? chunk.generation ?? chunk.outputText ?? chunk.choices?.[0]?.delta?.content ?? '';
    const u = usageFromBody(chunk) ?? (chunk.message?.usage ? usageFromBody(chunk.message) : null);
    // message_start carries the prompt and cache counts, message_delta the output: keep the largest of each
    if (u) {
      const a = this.usage;
      this.usage = a ? { p: Math.max(a.p, u.p), c: Math.max(a.c, u.c), cr: Math.max(a.cr ?? 0, u.cr ?? 0), cw: Math.max(a.cw ?? 0, u.cw ?? 0) } : u;
    }
  }

  result(): any {
    return { text: this.text, events: this.events };
  }
}

function strip(res: any): any {
  if (!res || typeof res !== 'object') return res;
  const { $metadata: _m, ...rest } = res;
  // Converse reasoningContent blocks as thinking / redacted_thinking blocks, the shape the server reads
  const msg = rest.output?.message;
  if (msg && Array.isArray(msg.content) && msg.content.some((b: any) => b && typeof b === 'object' && 'reasoningContent' in b)) {
    return { ...rest, output: { ...rest.output, message: { ...msg, content: bedrockContent(msg.content) } } };
  }
  return rest;
}

function makeSend(orig: (...a: any[]) => any, local: IntegrationOpSettings | undefined) {
  return function (this: any, command: any, ...rest: any[]) {
    const agentCmd = agentCommandName(command);
    if (agentCmd) {
      const ac = integrationClient(INTEGRATION);
      if (!ac) return orig.call(this, command, ...rest);
      let proc: AgentTraceProcessor;
      try {
        proc = startAgentCall(ac, agentCmd, { ...(command.input ?? {}) }, local);
      } catch {
        return orig.call(this, command, ...rest);
      }
      const failAgent = (e: unknown) => {
        proc.finish(e);
        throw e;
      };
      let ap: any;
      try {
        ap = withCall(proc.root, () => orig.call(this, command, ...rest));
      } catch (e) {
        failAgent(e);
      }
      return Promise.resolve(ap).then((res: any) => {
        try {
          return handleAgentResponse(proc, res);
        } catch {
          proc.finish();
          return res;
        }
      }, failAgent);
    }
    const name = commandName(command);
    const client = name ? integrationClient(INTEGRATION) : null;
    if (!client || !name) return orig.call(this, command, ...rest);
    const input: Record<string, unknown> = { ...(command.input ?? {}) };
    if ('body' in input) input.body = decodeBody(input.body);
    const model = String(input.modelId ?? '');
    const meta = integrationOp(INTEGRATION, OPS[name], 'llm', {}, local);
    const attrs: Record<string, unknown> = { 'gen_ai.operation.name': 'chat', 'gen_ai.provider.name': 'aws.bedrock' };
    if (model) attrs['gen_ai.request.model'] = model;
    let call: Call;
    try {
      call = client.createCall(meta, { model, ...input }, { attributes: integrationAttributes(INTEGRATION, attrs, local) });
    } catch {
      return orig.call(this, command, ...rest);
    }
    const fail = (e: unknown) => {
      client.finishCall(call, undefined, e, meta);
      throw e;
    };
    let p: any;
    try {
      p = withCall(call, () => orig.call(this, command, ...rest));
    } catch (e) {
      fail(e);
    }
    return Promise.resolve(p).then((res: any) => {
      try {
        return handle(res);
      } catch {
        client.finishCall(call, strip(res), undefined, meta);
        return res;
      }
    }, fail);

    function handle(res: any): any {
      if (name === 'ConverseCommand') {
        const u = res?.usage;
        if (u) addB(call, model, converseUsage(u));
        client!.finishCall(call, strip(res), undefined, meta);
        return res;
      }
      if (name === 'ConverseStreamCommand' && res?.stream) {
        const acc = new ConverseStreamAccumulator();
        res.stream = proxyStream(res.stream, acc, (out, err) => {
          if (acc.usage) addB(call, model, acc.usage);
          client!.finishCall(call, out, err, meta);
        });
        return res;
      }
      if (name === 'InvokeModelWithResponseStreamCommand' && res?.body) {
        const acc = new InvokeStreamAccumulator();
        res.body = proxyStream(res.body, acc, (out, err) => {
          if (acc.usage) addB(call, model, acc.usage);
          client!.finishCall(call, out, err, meta);
        }, { raw: true });
        return res;
      }
      // InvokeModel: the body is a Uint8Array (blob adapter); decoding does not consume it.
      const body = decodeBody(res?.body);
      const u = usageFromBody(body);
      if (u) addB(call, model, u);
      client!.finishCall(call, { ...strip(res), body }, undefined, meta);
      return res;
    }
  };
}

/** Trace a `BedrockRuntimeClient` instance's `send`. Returns the same client. */
export function wrapBedrock<T>(client: T, opts: IntegrationOpSettings & { opSettings?: IntegrationOpSettings } = {}): T {
  const local = pickOpSettings(opts);
  patchWith(client as any, 'send', (orig) => makeSend(orig, local));
  return client;
}

/** Trace a `BedrockAgentRuntimeClient` instance (InvokeAgent / InvokeInlineAgent). Same as `wrapBedrock`. */
export const wrapBedrockAgent = wrapBedrock;

/** Patch `BedrockRuntimeClient.prototype.send` so every client is traced. */
export function instrumentBedrock(BedrockRuntimeClient: any, opts: IntegrationOpSettings & { opSettings?: IntegrationOpSettings } = {}): boolean {
  const proto = BedrockRuntimeClient?.prototype;
  if (!proto) return false;
  const local = pickOpSettings(opts);
  return patchWith(proto, 'send', (orig) => makeSend(orig, local));
}

/** Patch `BedrockAgentRuntimeClient.prototype.send` so every agent runtime client is traced. */
export const instrumentBedrockAgent = instrumentBedrock;
