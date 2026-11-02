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

function usageFromBody(b: any): { p: number; c: number } | null {
  if (!b || typeof b !== 'object') return null;
  const u = b.usage;
  if (u && typeof u === 'object' && (u.input_tokens != null || u.inputTokens != null || u.prompt_tokens != null)) {
    return { p: int(u.input_tokens ?? u.inputTokens ?? u.prompt_tokens), c: int(u.output_tokens ?? u.outputTokens ?? u.completion_tokens) };
  }
  const m = b['amazon-bedrock-invocationMetrics'];
  if (m && typeof m === 'object') return { p: int(m.inputTokenCount), c: int(m.outputTokenCount) };
  if (b.prompt_token_count != null || b.inputTextTokenCount != null) {
    const c = b.generation_token_count ?? (b.results ?? []).reduce((a: number, r: any) => a + int(r?.tokenCount), 0);
    return { p: int(b.prompt_token_count ?? b.inputTextTokenCount), c: int(c) };
  }
  return null;
}

class ConverseStreamAccumulator implements Accumulator {
  text = '';
  stopReason: string | null = null;
  usage: { p: number; c: number; t?: number } | null = null;
  toolUses: any[] = [];
  events = 0;

  add(ev: any): void {
    this.events++;
    if (ev?.contentBlockStart?.start?.toolUse) this.toolUses.push({ ...ev.contentBlockStart.start.toolUse, input: '' });
    const d = ev?.contentBlockDelta?.delta;
    if (d?.text) this.text += d.text;
    if (d?.toolUse?.input && this.toolUses.length) this.toolUses[this.toolUses.length - 1].input += d.toolUse.input;
    if (ev?.messageStop) this.stopReason = ev.messageStop.stopReason ?? null;
    const u = ev?.metadata?.usage;
    if (u) this.usage = { p: int(u.inputTokens), c: int(u.outputTokens), t: u.totalTokens };
  }

  result(): any {
    const content: any[] = this.text ? [{ text: this.text }] : [];
    for (const t of this.toolUses) {
      let input: unknown = t.input;
      try {
        input = t.input ? JSON.parse(t.input) : {};
      } catch {
        /* keep the raw string */
      }
      content.push({ toolUse: { toolUseId: t.toolUseId, name: t.name, input } });
    }
    return { output: { message: { role: 'assistant', content } }, stopReason: this.stopReason, events: this.events, ...(this.usage ? { usage: { inputTokens: this.usage.p, outputTokens: this.usage.c } } : {}) };
  }
}

class InvokeStreamAccumulator implements Accumulator {
  text = '';
  usage: { p: number; c: number } | null = null;
  events = 0;

  add(ev: any): void {
    this.events++;
    const chunk = decodeBody(ev?.chunk?.bytes) as any;
    if (!chunk || typeof chunk !== 'object') return;
    this.text += chunk.delta?.text ?? chunk.completion ?? chunk.generation ?? chunk.outputText ?? chunk.choices?.[0]?.delta?.content ?? '';
    const u = usageFromBody(chunk) ?? (chunk.message?.usage ? usageFromBody(chunk.message) : null);
    if (u) this.usage = this.usage ? { p: Math.max(this.usage.p, u.p), c: Math.max(this.usage.c, u.c) } : u;
  }

  result(): any {
    return { text: this.text, events: this.events };
  }
}

function strip(res: any): any {
  if (!res || typeof res !== 'object') return res;
  const { $metadata: _m, ...rest } = res;
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
        if (u) addUsage(call, model, int(u.inputTokens), int(u.outputTokens), u.totalTokens);
        client!.finishCall(call, strip(res), undefined, meta);
        return res;
      }
      if (name === 'ConverseStreamCommand' && res?.stream) {
        const acc = new ConverseStreamAccumulator();
        res.stream = proxyStream(res.stream, acc, (out, err) => {
          if (acc.usage) addUsage(call, model, acc.usage.p, acc.usage.c, acc.usage.t);
          client!.finishCall(call, out, err, meta);
        });
        return res;
      }
      if (name === 'InvokeModelWithResponseStreamCommand' && res?.body) {
        const acc = new InvokeStreamAccumulator();
        res.body = proxyStream(res.body, acc, (out, err) => {
          if (acc.usage) addUsage(call, model, acc.usage.p, acc.usage.c);
          client!.finishCall(call, out, err, meta);
        }, { raw: true });
        return res;
      }
      // InvokeModel: the body is a Uint8Array (blob adapter); decoding does not consume it.
      const body = decodeBody(res?.body);
      const u = usageFromBody(body);
      if (u) addUsage(call, model, u.p, u.c);
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
