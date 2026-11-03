import type { Call } from './call.js';

/** Extra usage fields: counts (`cache_read_input_tokens`, ...) or one level of nested counts (`prompt_tokens_details`). */
export type UsageExtra = Record<string, number | Record<string, number> | undefined>;

/** Add LLM usage for `model` to the call's own `summary.usage` (the server rolls up descendants). */
export function addUsage(call: Call | null | undefined, model: string | null | undefined, promptTokens = 0, completionTokens = 0, totalTokens?: number, extra?: UsageExtra): void {
  if (!call) return;
  const m = model || 'unknown';
  const usage = (call.summary.usage ??= {}) as Record<string, Record<string, any>>;
  const u = (usage[m] ??= { requests: 0, prompt_tokens: 0, completion_tokens: 0, total_tokens: 0 });
  u.requests += 1;
  u.prompt_tokens += promptTokens || 0;
  u.completion_tokens += completionTokens || 0;
  u.total_tokens += totalTokens ?? (promptTokens || 0) + (completionTokens || 0);
  for (const [k, v] of Object.entries(extra ?? {})) {
    if (typeof v === 'number') {
      if (v) u[k] = (u[k] ?? 0) + v;
    } else if (v && typeof v === 'object') {
      const d = (u[k] ??= {});
      for (const [kk, vv] of Object.entries(v)) if (typeof vv === 'number' && vv) d[kk] = (d[kk] ?? 0) + vv;
    }
  }
}

// Prompt caching, the way the server prices it (docs/weft/API.md "Cached tokens"): prompt_tokens
// is the gross prompt count; cache_read_input_tokens / cache_creation_input_tokens are parts of it.

const n = (v: unknown): number => (typeof v === 'number' && Number.isFinite(v) ? v : Number(v) || 0);

/** Anthropic usage (`input_tokens` excludes the cache counts) as gross prompt tokens plus the cache fields. */
export function addAnthropicUsage(call: Call | null | undefined, model: string | null | undefined, u: any): void {
  if (!u || typeof u !== 'object' || (u.input_tokens == null && u.output_tokens == null)) return;
  const read = n(u.cache_read_input_tokens);
  const write = n(u.cache_creation_input_tokens);
  addUsage(call, model, n(u.input_tokens) + read + write, n(u.output_tokens), undefined, { cache_read_input_tokens: read, cache_creation_input_tokens: write });
}

/** Gemini / Vertex `usageMetadata` (camel or snake case; the prompt count includes cached content). */
export function addGeminiUsage(call: Call | null | undefined, model: string | null | undefined, u: any): void {
  if (!u || typeof u !== 'object') return;
  const g = (camel: string, snake: string) => u[camel] ?? u[snake];
  const thoughts = n(g('thoughtsTokenCount', 'thoughts_token_count'));
  const total = g('totalTokenCount', 'total_token_count');
  addUsage(call, model, n(g('promptTokenCount', 'prompt_token_count')), n(g('candidatesTokenCount', 'candidates_token_count')) + thoughts, total == null ? undefined : n(total), {
    cache_read_input_tokens: n(g('cachedContentTokenCount', 'cached_content_token_count')),
    reasoning_tokens: thoughts,
  });
}

/** Record OpenAI chat (prompt/completion) or responses (input/output) usage from a response, keeping cached prompt tokens. */
export function recordOpenAIUsage(call: Call | null | undefined, resp: any, defaultModel?: string): void {
  const u = resp?.usage;
  if (!call || !u || typeof u !== 'object') return;
  const p = u.prompt_tokens ?? u.input_tokens;
  const c = u.completion_tokens ?? u.output_tokens;
  if (p == null && c == null) return;
  const extra: UsageExtra = {};
  for (const key of ['prompt_tokens_details', 'input_tokens_details']) {
    const d = u[key];
    if (!d || typeof d !== 'object') continue;
    const cached = n(d.cached_tokens);
    const write = n(d.cache_write_tokens);
    if (cached || write) extra[key] = { ...(cached ? { cached_tokens: cached } : {}), ...(write ? { cache_write_tokens: write } : {}) };
    extra.cache_read_input_tokens = cached;
    extra.cache_creation_input_tokens = write;
    break;
  }
  const r = n((u.completion_tokens_details ?? u.output_tokens_details)?.reasoning_tokens);
  if (r) extra.reasoning_tokens = r;
  addUsage(call, resp.model ?? defaultModel, p ?? 0, c ?? 0, u.total_tokens ?? undefined, extra);
}
