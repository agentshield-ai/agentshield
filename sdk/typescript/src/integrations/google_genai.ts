import type { Call } from '../call.js';
import { addUsage } from '../usage.js';
import { patchMethod, type Accumulator } from './common.js';
import { pickOpSettings, type IntegrationOpSettings } from './settings.js';

function textOf(cand: any): string {
  return (cand?.content?.parts ?? []).filter((p: any) => p && !p.thought).map((p: any) => p.text ?? '').join('');
}

export class GenerateContentAccumulator implements Accumulator {
  private last: any = {};
  private texts = new Map<number, string>();
  private usage: any = undefined;

  add(c: any): void {
    if (!c || typeof c !== 'object') return;
    this.last = c;
    if (c.usageMetadata) this.usage = c.usageMetadata;
    (c.candidates ?? []).forEach((cand: any, i: number) => {
      const idx = cand.index ?? i;
      this.texts.set(idx, (this.texts.get(idx) ?? '') + textOf(cand));
    });
  }

  result(): any {
    const candidates = [...this.texts.keys()].sort((a, b) => a - b).map((idx) => {
      const base = (this.last.candidates ?? []).find((c: any) => (c.index ?? 0) === idx) ?? {};
      return { ...base, content: { role: 'model', parts: [{ text: this.texts.get(idx) }] } };
    });
    return { ...this.last, candidates, ...(this.usage ? { usageMetadata: this.usage } : {}), text: this.texts.get(0) ?? '' };
  }
}

export function recordGenAIUsage(call: Call, out: any): void {
  const u = out?.usageMetadata ?? out?.usage_metadata;
  if (!u) return;
  const model = (call.inputs?.model as string) ?? out?.modelVersion;
  addUsage(call, model, u.promptTokenCount ?? 0, (u.candidatesTokenCount ?? 0) + (u.thoughtsTokenCount ?? 0), u.totalTokenCount);
}

/**
 * Trace a `@google/genai` client: `models.generateContent` and
 * `models.generateContentStream`. Returns the same client.
 */
export function wrapGoogleGenAI<T>(client: T, opts: IntegrationOpSettings & { opSettings?: IntegrationOpSettings } = {}): T {
  const models = (client as any)?.models;
  const opSettings = pickOpSettings(opts);
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
