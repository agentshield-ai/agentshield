import type { Call } from './call.js';

/** Add LLM usage for `model` to the call's own `summary.usage` (the server rolls up descendants). */
export function addUsage(call: Call | null | undefined, model: string | null | undefined, promptTokens = 0, completionTokens = 0, totalTokens?: number): void {
  if (!call) return;
  const m = model || 'unknown';
  const usage = (call.summary.usage ??= {}) as Record<string, Record<string, number>>;
  const u = (usage[m] ??= { requests: 0, prompt_tokens: 0, completion_tokens: 0, total_tokens: 0 });
  u.requests += 1;
  u.prompt_tokens += promptTokens || 0;
  u.completion_tokens += completionTokens || 0;
  u.total_tokens += totalTokens ?? (promptTokens || 0) + (completionTokens || 0);
}

/** Record OpenAI chat (prompt/completion) or responses (input/output) usage from a response. */
export function recordOpenAIUsage(call: Call | null | undefined, resp: any, defaultModel?: string): void {
  const u = resp?.usage;
  if (!call || !u || typeof u !== 'object') return;
  const p = u.prompt_tokens ?? u.input_tokens;
  const c = u.completion_tokens ?? u.output_tokens;
  if (p == null && c == null) return;
  addUsage(call, resp.model ?? defaultModel, p ?? 0, c ?? 0, u.total_tokens ?? undefined);
}
