/**
 * Cohere (`cohere-ai`): `chat` and `chatStream` on `CohereClientV2` (v2 API)
 * and `CohereClient` (v1), plus `embed` and `rerank`. Streams are accumulated
 * into one response; usage comes from `usage.tokens` (v2) or `meta.tokens`
 * (v1), falling back to billed units.
 */
import type { Call } from '../call.js';
import { addUsage } from '../usage.js';
import { patchMethod, type Accumulator } from './common.js';
import { pickTraceSettings, type IntegrationTraceSettings, type WrapperTraceOptions } from './settings.js';

const INTEGRATION = 'cohere';

/** Accumulates v2 (`content-delta`, `message-end`) and v1 (`text-generation`, `stream-end`) events. */
export class CohereStreamAccumulator implements Accumulator {
  private text = '';
  private id: string | undefined;
  private final: any = {};
  private toolCalls: any[] = [];

  add(e: any): void {
    if (!e || typeof e !== 'object') return;
    const t = e.type ?? e.eventType ?? e.event_type;
    if (t === 'message-start') this.id = e.id;
    else if (t === 'content-delta') this.text += e.delta?.message?.content?.text ?? '';
    else if (t === 'tool-call-start') this.toolCalls.push({ ...(e.delta?.message?.toolCalls ?? {}) });
    else if (t === 'tool-call-delta') {
      const last = this.toolCalls.at(-1);
      const d = e.delta?.message?.toolCalls?.function?.arguments;
      if (last && d) last.function = { ...last.function, arguments: (last.function?.arguments ?? '') + d };
    } else if (t === 'text-generation') this.text += e.text ?? '';
    else if (t === 'message-end') this.final = { ...(e.delta ?? {}) };
    else if (t === 'stream-end') this.final = { finishReason: e.finishReason ?? e.finish_reason, usage: e.response?.meta, generationId: e.response?.generationId };
  }

  result(): any {
    const out: any = { id: this.id, message: { role: 'assistant', content: [{ type: 'text', text: this.text }] }, finishReason: this.final.finishReason ?? this.final.finish_reason, usage: this.final.usage };
    if (this.toolCalls.length) out.message.toolCalls = this.toolCalls;
    return out;
  }
}

export function recordCohereUsage(call: Call, out: any): void {
  const u = out?.usage ?? out?.meta;
  if (!u || typeof u !== 'object') return;
  const tk = u.tokens ?? u.billedUnits ?? u.billed_units;
  if (!tk) return;
  const p = tk.inputTokens ?? tk.input_tokens;
  const c = tk.outputTokens ?? tk.output_tokens;
  if (p == null && c == null) return;
  addUsage(call, (call.inputs?.model as string) || 'cohere', p ?? 0, c ?? 0);
}

/** Trace a `CohereClientV2` or `CohereClient` instance. Returns the same client. */
export function wrapCohere<T>(client: T, opts: WrapperTraceOptions = {}): T {
  const c = client as any;
  const opSettings = pickTraceSettings(opts);
  const attributes = () => ({ 'gen_ai.provider.name': 'cohere' });
  const common = { integration: INTEGRATION, opSettings, attributes, accumulator: () => new CohereStreamAccumulator(), recordUsage: recordCohereUsage };
  patchMethod(c, 'chat', { ...common, opName: 'cohere.chat', isStream: () => false });
  patchMethod(c, 'chatStream', { ...common, opName: 'cohere.chat_stream', isStream: () => true });
  patchMethod(c, 'embed', { ...common, opName: 'cohere.embed', kind: 'embedding', isStream: () => false });
  patchMethod(c, 'rerank', { ...common, opName: 'cohere.rerank', kind: 'reranker', isStream: () => false });
  return client;
}
