/**
 * Model reasoning in recorded calls (docs/weft/DESIGN.md §2.2).
 *
 * The server extracts reasoning steps from what model calls send and return
 * (Anthropic `thinking` / `redacted_thinking` blocks, OpenAI Responses
 * `reasoning` items, `reasoning_content` on chat messages, Gemini parts with
 * `thought: true`, GenAI `reasoning` parts) and from the `weft.reasoning` call
 * attribute. This module keeps those shapes consistent on the way out:
 *
 * - `scrubReasoning(value, true)` replaces opaque reasoning payloads (the
 *   encrypted `data` of redacted thinking, `encrypted_content` of Responses
 *   reasoning items, Bedrock `redactedContent`) with a short digest, so a
 *   redacted step is stored as redacted without its blob, and an input copy
 *   of it still matches its output step;
 * - `scrubReasoning(value, false)` removes reasoning altogether
 *   (`captureReasoning: false` / `WEFT_CAPTURE_REASONING=false`).
 */
import { createHash } from 'node:crypto';

export type ReasoningKind = 'thinking' | 'summary' | 'redacted';

/** One entry of the `weft.reasoning` call attribute. */
export interface ReasoningStep {
  kind: ReasoningKind;
  text: string;
  tokens?: number;
  time: string;
}

const DROP = Symbol('drop');
const MAX_DEPTH = 24;

export function normReasoningKind(kind: unknown): ReasoningKind {
  const k = String(kind ?? '').trim().toLowerCase();
  if (k === 'summary' || k === 'reasoning_summary' || k === 'summary_text') return 'summary';
  if (k === 'redacted' || k === 'redacted_thinking' || k === 'encrypted') return 'redacted';
  return 'thinking';
}

/** `sha256:<24 hex>` of an opaque payload (bytes, text or JSON). */
export function reasoningDigest(value: unknown): string {
  if (typeof value === 'string' && value.startsWith('sha256:')) return value;
  let data: Uint8Array | string;
  if (value instanceof Uint8Array) data = value;
  else if (typeof value === 'string') data = value;
  else data = JSON.stringify(value) ?? '';
  return `sha256:${createHash('sha256').update(data).digest('hex').slice(0, 24)}`;
}

function isPlainObject(v: unknown): v is Record<string, unknown> {
  return !!v && typeof v === 'object' && !Array.isArray(v) && !(v instanceof Uint8Array);
}

function isReasoningPart(d: Record<string, unknown>): boolean {
  const t = d.type;
  if (t === 'thinking' || t === 'redacted_thinking' || t === 'reasoning' || t === 'reasoning_text') return true;
  if (d.thought === true) return true; // Gemini thought part
  if ('reasoningContent' in d && !['text', 'toolUse', 'toolResult', 'image', 'document'].some((k) => k in d)) return true; // Bedrock
  return false;
}

function scrub(v: unknown, capture: boolean, depth: number): unknown {
  if (depth > MAX_DEPTH) return v;
  if (Array.isArray(v)) {
    const out: unknown[] = [];
    for (const x of v) {
      const y = scrub(x, capture, depth + 1);
      if (y !== DROP) out.push(y);
    }
    return out;
  }
  if (!isPlainObject(v)) return v;
  if (!capture && isReasoningPart(v)) return DROP;
  const out: Record<string, unknown> = {};
  const messageLike = 'role' in v;
  for (const [k, x] of Object.entries(v)) {
    if (!capture) {
      if (k === 'reasoning_content' || k === 'reasoning_details' || k === 'reasoningContent') continue;
      if ((k === 'reasoning' || k === 'thinking') && typeof x === 'string' && messageLike) continue;
    }
    const y = scrub(x, capture, depth + 1);
    if (y !== DROP) out[k] = y;
  }
  if (capture) {
    if (out.type === 'redacted_thinking' && out.data !== undefined && out.data !== null) out.data = reasoningDigest(out.data);
    else if (out.type === 'reasoning' && out.encrypted_content) out.encrypted_content = reasoningDigest(out.encrypted_content);
    const rc = out.reasoningContent;
    if (isPlainObject(rc) && rc.redactedContent !== undefined && rc.redactedContent !== null) {
      out.reasoningContent = { ...rc, redactedContent: reasoningDigest(rc.redactedContent) };
    }
  }
  return out;
}

/** `value` with reasoning removed (`capture=false`) or opaque reasoning payloads digested (`capture=true`). */
export function scrubReasoning<T = unknown>(value: T, capture = true): T {
  try {
    const r = scrub(value, capture, 0);
    return (r === DROP ? null : r) as T;
  } catch {
    return value;
  }
}

/** Whether a recorded value holds a reasoning part the server would extract. */
export function hasReasoning(value: unknown, depth = 0): boolean {
  if (depth > MAX_DEPTH) return false;
  if (Array.isArray(value)) return value.some((x) => hasReasoning(x, depth + 1));
  if (isPlainObject(value)) {
    if (isReasoningPart(value)) return true;
    if (typeof value.reasoning_content === 'string' && value.reasoning_content) return true;
    return Object.values(value).some((x) => (Array.isArray(x) || isPlainObject(x)) && hasReasoning(x, depth + 1));
  }
  return false;
}

/** A `weft.reasoning` attribute entry. Redacted steps keep no text. */
export function reasoningStep(text: string, kind: string = 'thinking', tokens?: number, at: Date = new Date()): ReasoningStep {
  const k = normReasoningKind(kind);
  const step: ReasoningStep = { kind: k, text: k === 'redacted' ? '' : String(text ?? ''), time: at.toISOString() };
  if (tokens) step.tokens = Math.trunc(tokens);
  return step;
}

/** A Bedrock Converse `reasoningContent` block as an Anthropic-style `thinking` / `redacted_thinking` block. */
export function bedrockReasoningBlock(block: Record<string, any>): Record<string, unknown> {
  const rc = block?.reasoningContent ?? {};
  const rt = rc.reasoningText;
  if (rt && (rt.text || rt.signature)) {
    const out: Record<string, unknown> = { type: 'thinking', thinking: rt.text ?? '' };
    if (rt.signature) out.signature = rt.signature;
    return out;
  }
  if (rc.redactedContent !== undefined && rc.redactedContent !== null) return { type: 'redacted_thinking', data: reasoningDigest(rc.redactedContent) };
  return block;
}

/** Rewrite the `reasoningContent` blocks of a Bedrock message content list. */
export function bedrockContent(content: unknown): unknown {
  if (!Array.isArray(content)) return content;
  return content.map((b) => (isPlainObject(b) && 'reasoningContent' in b ? bedrockReasoningBlock(b) : b));
}
