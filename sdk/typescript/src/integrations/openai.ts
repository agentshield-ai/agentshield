import type { Call } from '../call.js';
import { recordOpenAIUsage } from '../usage.js';
import { patchMethod, type Accumulator } from './common.js';
import { pickOpSettings, type IntegrationOpSettings } from './settings.js';

export class ChatCompletionAccumulator implements Accumulator {
  private base: Record<string, any> = {};
  private choices = new Map<number, any>();
  private usage: any = undefined;

  add(c: any): void {
    if (!c || typeof c !== 'object') return;
    for (const k of ['id', 'model', 'created', 'system_fingerprint', 'service_tier']) if (c[k] != null) this.base[k] = c[k];
    if (c.usage) this.usage = c.usage;
    else if (c.x_groq?.usage) this.usage = c.x_groq.usage;
    for (const ch of c.choices ?? []) {
      const idx = ch.index ?? 0;
      if (!this.choices.has(idx)) this.choices.set(idx, { index: idx, message: { role: 'assistant', content: null }, finish_reason: null });
      const st = this.choices.get(idx);
      const d = ch.delta ?? {};
      if (d.role) st.message.role = d.role;
      if (d.content != null) st.message.content = (st.message.content ?? '') + d.content;
      // reasoning text streamed by DeepSeek / vLLM (reasoning_content) and OpenRouter (reasoning)
      for (const rk of ['reasoning_content', 'reasoning']) if (typeof d[rk] === 'string' && d[rk]) st.message[rk] = (st.message[rk] ?? '') + d[rk];
      if (d.refusal) st.message.refusal = (st.message.refusal ?? '') + d.refusal;
      for (const tc of d.tool_calls ?? []) {
        const tcs = (st.message.tool_calls ??= []);
        const i = tc.index ?? tcs.length;
        while (tcs.length <= i) tcs.push({ id: null, type: 'function', function: { name: '', arguments: '' } });
        const t = tcs[i];
        if (tc.id) t.id = tc.id;
        if (tc.type) t.type = tc.type;
        if (tc.function?.name) t.function.name += tc.function.name;
        if (tc.function?.arguments) t.function.arguments += tc.function.arguments;
      }
      if (ch.finish_reason) st.finish_reason = ch.finish_reason;
    }
  }

  result(): any {
    const out: any = { ...this.base, object: 'chat.completion', choices: [...this.choices.keys()].sort((a, b) => a - b).map((k) => this.choices.get(k)) };
    if (this.usage) out.usage = this.usage;
    return out;
  }
}

export class ResponsesAccumulator implements Accumulator {
  private final: any = null;
  private text = '';

  add(e: any): void {
    if (!e || typeof e !== 'object') return;
    if (['response.completed', 'response.incomplete', 'response.failed'].includes(e.type) && e.response) this.final = e.response;
    else if (e.type === 'response.output_text.delta' && typeof e.delta === 'string') this.text += e.delta;
  }

  result(): any {
    return this.final ?? { object: 'response', output_text: this.text };
  }
}

const usage = (call: Call, out: any) => recordOpenAIUsage(call, out);

export interface WrapOpenAIOptions extends Omit<IntegrationOpSettings, 'name'> {
  /** Op name prefix (default "openai"), e.g. "groq" for an OpenAI-compatible provider. */
  name?: string;
  /** Replace the op name of every call (same as `opSettings.name`). */
  opName?: string;
  /** Add stream_options.include_usage to streamed chat completions (default true). */
  includeUsage?: boolean;
  /** Integration name for settings (default: `name`, else "openai"). */
  integration?: string;
  /** `gen_ai.provider.name` recorded on each call (default: `name`). */
  providerName?: string;
  /** Op settings for this client (same fields as `autopatchSettings.<name>.opSettings`). */
  opSettings?: IntegrationOpSettings;
}

/**
 * Trace an OpenAI (or OpenAI-compatible) client instance:
 * `chat.completions.create` and `responses.create`, including streaming.
 * Returns the same client.
 */
export function wrapOpenAI<T>(client: T, opts: WrapOpenAIOptions = {}): T {
  const prefix = opts.name ?? 'openai';
  const includeUsage = opts.includeUsage ?? true;
  const integration = opts.integration ?? prefix;
  const opSettings = pickOpSettings(opts);
  const provider = opts.providerName ?? prefix;
  const attributes = () => ({ 'gen_ai.provider.name': provider });
  const c = client as any;
  patchMethod(c?.chat?.completions, 'create', {
    integration,
    opSettings,
    attributes,
    opName: `${prefix}.chat.completions.create`,
    prepare: (body) => (includeUsage && body.stream && body.stream_options === undefined ? { ...body, stream_options: { include_usage: true } } : body),
    accumulator: () => new ChatCompletionAccumulator(),
    recordUsage: usage,
  });
  patchMethod(c?.responses, 'create', {
    integration,
    opSettings,
    attributes,
    opName: `${prefix}.responses.create`,
    accumulator: () => new ResponsesAccumulator(),
    recordUsage: usage,
  });
  return client;
}

/**
 * Trace any OpenAI-compatible client (Groq, Together, Mistral's OpenAI endpoint,
 * vLLM, Ollama, ...). `name` becomes the op prefix, e.g. `groq.chat.completions.create`.
 */
export function wrapOpenAICompatible<T>(client: T, opts: WrapOpenAIOptions = {}): T {
  return wrapOpenAI(client, { includeUsage: false, ...opts });
}

/** Groq (`groq-sdk`, or an OpenAI client pointed at Groq): `groq.chat.completions.create`; usage from `x_groq.usage`. */
export function wrapGroq<T>(client: T, opts: WrapOpenAIOptions = {}): T {
  return wrapOpenAICompatible(client, { name: 'groq', ...opts });
}

/** Together AI (`together-ai`, or an OpenAI client pointed at Together): `together.chat.completions.create`. */
export function wrapTogether<T>(client: T, opts: WrapOpenAIOptions = {}): T {
  return wrapOpenAICompatible(client, { name: 'together', ...opts });
}

/** Cerebras (`@cerebras/cerebras_cloud_sdk`, or an OpenAI client pointed at Cerebras): `cerebras.chat.completions.create`. */
export function wrapCerebras<T>(client: T, opts: WrapOpenAIOptions = {}): T {
  return wrapOpenAICompatible(client, { name: 'cerebras', ...opts });
}
