/**
 * Mistral (`@mistralai/mistralai`): `chat.complete`, `chat.stream`,
 * `fim.complete`, `fim.stream`, `agents.complete`, `agents.stream` and
 * `embeddings.create`. Streams are accumulated into one chat completion with
 * usage. The SDK returns camelCase objects; usage is read from
 * `usage.promptTokens/completionTokens`.
 */
import type { Call } from '../call.js';
import { addUsage } from '../usage.js';
import { patchMethod, type Accumulator } from './common.js';
import { ChatCompletionAccumulator } from './openai.js';
import { pickOpSettings, type IntegrationOpSettings } from './settings.js';

const INTEGRATION = 'mistral';

function snakeDelta(d: any): any {
  if (!d || typeof d !== 'object') return d;
  const out: any = { role: d.role ?? undefined, content: typeof d.content === 'string' ? d.content : Array.isArray(d.content) ? d.content.map((c: any) => c?.text ?? '').join('') : d.content };
  const tcs = d.toolCalls ?? d.tool_calls;
  if (tcs) out.tool_calls = tcs.map((t: any, i: number) => ({ index: t.index ?? i, id: t.id, type: t.type ?? 'function', function: { name: t.function?.name, arguments: typeof t.function?.arguments === 'string' ? t.function.arguments : JSON.stringify(t.function?.arguments ?? {}) } }));
  return out;
}

function snakeUsage(u: any): any {
  if (!u) return u;
  return { prompt_tokens: u.promptTokens ?? u.prompt_tokens, completion_tokens: u.completionTokens ?? u.completion_tokens, total_tokens: u.totalTokens ?? u.total_tokens };
}

/** Accumulates Mistral stream events (`{ data: CompletionChunk }`). */
export class MistralStreamAccumulator implements Accumulator {
  private inner = new ChatCompletionAccumulator();

  add(ev: any): void {
    const c = ev?.data ?? ev;
    if (!c || typeof c !== 'object') return;
    this.inner.add({
      id: c.id,
      model: c.model,
      created: c.created,
      usage: snakeUsage(c.usage),
      choices: (c.choices ?? []).map((ch: any) => ({ index: ch.index ?? 0, delta: snakeDelta(ch.delta), finish_reason: ch.finishReason ?? ch.finish_reason ?? null })),
    });
  }

  result(): any {
    return this.inner.result();
  }
}

export function recordMistralUsage(call: Call, out: any): void {
  const u = out?.usage;
  if (!u) return;
  const p = u.promptTokens ?? u.prompt_tokens;
  const c = u.completionTokens ?? u.completion_tokens;
  if (p == null && c == null) return;
  addUsage(call, out.model ?? (call.inputs?.model as string), p ?? 0, c ?? 0, u.totalTokens ?? u.total_tokens);
}

/** Trace a `Mistral` client instance. Returns the same client. */
export function wrapMistral<T>(client: T, opts: IntegrationOpSettings & { opSettings?: IntegrationOpSettings } = {}): T {
  const c = client as any;
  const opSettings = pickOpSettings(opts);
  const attributes = () => ({ 'gen_ai.provider.name': 'mistral_ai' });
  const common = { integration: INTEGRATION, opSettings, attributes, accumulator: () => new MistralStreamAccumulator(), recordUsage: recordMistralUsage };
  for (const res of ['chat', 'fim', 'agents'] as const) {
    patchMethod(c?.[res], 'complete', { ...common, opName: `mistral.${res}.complete`, isStream: () => false });
    patchMethod(c?.[res], 'stream', { ...common, opName: `mistral.${res}.stream`, isStream: () => true });
  }
  patchMethod(c?.embeddings, 'create', { ...common, opName: 'mistral.embeddings.create', kind: 'embedding', isStream: () => false });
  return client;
}
