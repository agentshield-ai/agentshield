import type { Call } from '../call.js';
import { addGeminiUsage } from '../usage.js';
import { patchMethod, type Accumulator } from './common.js';
import { pickTraceSettings, type IntegrationTraceSettings, type WrapperTraceOptions } from './settings.js';

function textOf(cand: any, thought = false): string {
  return (cand?.content?.parts ?? []).filter((p: any) => p && !!p.thought === thought).map((p: any) => p.text ?? '').join('');
}

/**
 * Rebuilds a response from streamed chunks. Thought parts (`thought: true`,
 * the model's reasoning) are joined into one thought part per candidate.
 */
export class GenerateContentAccumulator implements Accumulator {
  private last: any = {};
  private texts = new Map<number, string>();
  private thoughts = new Map<number, string>();
  private signatures = new Map<number, unknown>();
  private usage: any = undefined;

  add(c: any): void {
    if (!c || typeof c !== 'object') return;
    this.last = c;
    if (c.usageMetadata) this.usage = c.usageMetadata;
    (c.candidates ?? []).forEach((cand: any, i: number) => {
      const idx = cand.index ?? i;
      this.texts.set(idx, (this.texts.get(idx) ?? '') + textOf(cand));
      const th = textOf(cand, true);
      if (th) this.thoughts.set(idx, (this.thoughts.get(idx) ?? '') + th);
      for (const p of cand?.content?.parts ?? []) if (p?.thought && (p.thoughtSignature ?? p.thought_signature)) this.signatures.set(idx, p.thoughtSignature ?? p.thought_signature);
    });
  }

  result(): any {
    const idxs = [...new Set([...this.texts.keys(), ...this.thoughts.keys()])].sort((a, b) => a - b);
    const candidates = idxs.map((idx) => {
      const base = (this.last.candidates ?? []).find((c: any) => (c.index ?? 0) === idx) ?? {};
      const parts: any[] = [];
      if (this.thoughts.get(idx)) parts.push({ text: this.thoughts.get(idx), thought: true, ...(this.signatures.has(idx) ? { thoughtSignature: this.signatures.get(idx) } : {}) });
      if (this.texts.get(idx)) parts.push({ text: this.texts.get(idx) });
      return { ...base, content: { role: 'model', parts } };
    });
    return { ...this.last, candidates, ...(this.usage ? { usageMetadata: this.usage } : {}), text: this.texts.get(0) ?? '' };
  }
}

export function recordGenAIUsage(call: Call, out: any): void {
  const u = out?.usageMetadata ?? out?.usage_metadata;
  if (!u) return;
  const model = (call.inputs?.model as string) ?? out?.modelVersion;
  addGeminiUsage(call, model, u);
}

/**
 * Trace a `@google/genai` client: `models.generateContent` and
 * `models.generateContentStream`. Returns the same client.
 */
export function wrapGoogleGenAI<T>(client: T, opts: WrapperTraceOptions = {}): T {
  const models = (client as any)?.models;
  const opSettings = pickTraceSettings(opts);
  const attributes = () => ({ 'gen_ai.provider.name': (client as any)?.vertexai ? 'gcp.vertex_ai' : 'gcp.gemini' });
  patchMethod(models, 'generateContent', {
    integration: 'google_genai',
    opSettings,
    attributes,
    opName: 'google.genai.models.generate_content',
    isStream: () => false,
    accumulator: () => new GenerateContentAccumulator(),
    recordUsage: recordGenAIUsage,
  });
  patchMethod(models, 'generateContentStream', {
    integration: 'google_genai',
    opSettings,
    attributes,
    opName: 'google.genai.models.generate_content_stream',
    isStream: () => true,
    accumulator: () => new GenerateContentAccumulator(),
    recordUsage: recordGenAIUsage,
  });
  return client;
}
