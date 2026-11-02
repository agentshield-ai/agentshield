/**
 * TypeSafe (`@typesafe-ai/sdk`): `client.systemOne(request, options)`.
 *
 * A port of Weave's TypeSafe integration. Each `systemOne` call becomes one
 * LLM (`chat`) span of provider `typesafe`:
 *
 * - input: `request.state` as a user message (a string as is, anything else
 *   as sorted-key JSON); `request.questions` (an array or an object, each with
 *   its `id`) as the `typesafe.questions` attribute;
 * - output: the `answers` as an assistant message (JSON), output type `json`,
 *   the response `model`, `usage` and the request id (`x-typesafe-request-id`
 *   when the client returns a Stainless `APIPromise`);
 * - parent: the current turn when one is running; else a new `invoke_agent`
 *   turn named `typesafe SDK` (in the current conversation, if any);
 * - errors: `error.type` and `http.response.status_code`; the provider error
 *   text (it carries the response body) is not copied.
 *
 * Content capture follows `OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT`
 * (`false` drops state, questions and answers), the `captureContent` option of
 * `wrapTypeSafe` and a conversation's `includeContent: false`.
 *
 * ```ts
 * import { TypeSafeClient } from '@typesafe-ai/sdk';
 * const ts = weft.wrapTypeSafe(new TypeSafeClient());
 * // or every client: weft.patchTypeSafeModule(await import('@typesafe-ai/sdk'))
 * ```
 */
import { LLM, Turn, getCurrentConversation, getCurrentTurn, type Message } from '../conversation.js';
import { integrationAttributes, integrationClient } from './settings.js';

const INTEGRATION = 'typesafe';
const PROVIDER_NAME = 'typesafe';
const AGENT_NAME = 'typesafe SDK';
const OPERATION = 'typesafe.system_one';
const REQUEST_ID_HEADER = 'x-typesafe-request-id';
const CAPTURE_ENV = 'OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT';
const CAPTURE_OFF = new Set(['0', 'false', 'no', 'off']);
const PATCHED = Symbol.for('weft.typesafe.patched');

const captureSettings = new WeakMap<object, boolean>();

function captureFor(client: object): boolean {
  const own = captureSettings.get(client);
  if (own !== undefined) return own;
  const raw = process.env[CAPTURE_ENV];
  if (raw == null) return true;
  return !CAPTURE_OFF.has(raw.trim().toLowerCase());
}

function sortValue(value: any): any {
  if (Array.isArray(value)) return value.map(sortValue);
  if (!value || typeof value !== 'object') return value;
  if (typeof value.toJSON === 'function') return sortValue(value.toJSON());
  const out: Record<string, unknown> = {};
  for (const k of Object.keys(value).sort()) if (value[k] !== undefined) out[k] = sortValue(value[k]);
  return out;
}

function stateMessage(state: unknown): Message {
  const v = sortValue(state);
  if (typeof v === 'string') return { role: 'user', content: v };
  return { role: 'user', parts: [{ type: 'text', content: JSON.stringify(v) }] } as Message;
}

function questionItem(id: string | number, q: unknown): Record<string, unknown> {
  return q && typeof q === 'object' && !Array.isArray(q) ? { id, ...(q as Record<string, unknown>) } : { id, value: q };
}

function questionsJson(questions: unknown): string | undefined {
  if (Array.isArray(questions)) return JSON.stringify(questions.map((q, i) => questionItem(i, q)));
  if (!questions || typeof questions !== 'object') return undefined;
  return JSON.stringify(Object.entries(questions).map(([id, q]) => questionItem(id, q)));
}

function errorName(e: any): string {
  return (e instanceof Error && e.name) || (typeof e?.name === 'string' && e.name) || 'Error';
}

function errorRequestId(e: any): string | undefined {
  if (typeof e?.requestId === 'string' && e.requestId) return e.requestId;
  try {
    const v = e?.headers?.get?.(REQUEST_ID_HEADER);
    return typeof v === 'string' && v ? v : undefined;
  } catch {
    return undefined;
  }
}

/** Mark a span failed without copying the provider error text. */
function stampError(span: LLM | Turn, error: unknown): void {
  const status = (error as any)?.status;
  if (typeof status === 'number' && Number.isInteger(status)) span.setAttributes({ http: { response: { status_code: status } } });
  const rid = errorRequestId(error);
  if (rid) {
    if (span instanceof LLM) span.responseId = rid; // gen_ai.response.id
    else span.setAttributes({ gen_ai: { response: { id: rid } } });
  }
  // error.type comes from the error's name; the message (the provider's response body) is dropped.
  const safe = new Error('');
  safe.name = errorName(error);
  safe.stack = '';
  span.recordError(safe);
}

function provenance(): Record<string, unknown> {
  return integrationAttributes(INTEGRATION, { weft: { integration_operation: OPERATION } });
}

interface Trace {
  llm: LLM;
  turn: Turn | null;
  ownsTurn: boolean;
  capture: boolean;
  closed: boolean;
}

function openTrace(client: any, request: any): Trace | null {
  if (!integrationClient(INTEGRATION)) return null;
  let turn: Turn | null = null;
  let ownsTurn = false;
  let llm: LLM | null = null;
  try {
    const capture = captureFor(client) && getCurrentConversation()?.includeContent !== false;
    const requested = request?.model ?? client?.defaultModel ?? 'jev-latest';
    const model = typeof requested === 'string' ? requested : '';
    const state = capture ? stateMessage(request?.state) : undefined;
    const questions = capture ? questionsJson(request?.questions) : undefined;
    const current = getCurrentTurn();
    if (current && !current.ended) {
      turn = current;
    } else {
      // Its own turn, in the current conversation (without making it the conversation's current turn).
      const conv = getCurrentConversation();
      turn = new Turn({ agentName: AGENT_NAME });
      if (conv) turn.conv = conv.info();
      turn.setAttributes(provenance());
      ownsTurn = true;
    }
    llm = turn.startLLM({ model, providerName: PROVIDER_NAME });
    if (state) llm.inputMessages = [state];
    llm.setAttributes({ ...provenance(), ...(questions !== undefined ? { typesafe: { questions } } : {}) });
    llm.start();
    return { llm, turn, ownsTurn, capture, closed: false };
  } catch {
    try {
      llm?.end();
      if (ownsTurn) turn?.end();
    } catch {
      /* ignore */
    }
    return null;
  }
}

function fill(trace: Trace, body: any, requestId?: string): void {
  const rec: Record<string, unknown> = { outputType: 'json' };
  if (body && typeof body === 'object') {
    if (typeof body.model === 'string' && body.model) rec.responseModel = body.model;
    const u = body.usage;
    if (u && typeof u === 'object' && (typeof u.input_tokens === 'number' || typeof u.output_tokens === 'number')) {
      rec.usage = { inputTokens: u.input_tokens ?? 0, outputTokens: u.output_tokens ?? 0 };
    }
    if (trace.capture && body.answers != null) rec.outputMessages = [{ role: 'assistant', parts: [{ type: 'text', content: JSON.stringify(sortValue(body.answers)) }] }];
  }
  if (requestId) rec.responseId = requestId;
  trace.llm.record(rec as any);
}

function close(trace: Trace, error?: unknown): void {
  if (trace.closed) return;
  trace.closed = true;
  if (error !== undefined) {
    stampError(trace.llm, error);
    if (trace.ownsTurn && trace.turn) stampError(trace.turn, error);
  }
  trace.llm.end();
  if (trace.ownsTurn && trace.turn) trace.turn.end();
}

function traceCall(original: (...a: any[]) => any, client: any, request: any, options: any): any {
  const trace = openTrace(client, request);
  if (!trace) return Reflect.apply(original, client, [request, options]);
  let result: any;
  try {
    result = Reflect.apply(original, client, [request, options]);
  } catch (e) {
    close(trace, e);
    throw e;
  }
  if (!result || typeof result.then !== 'function') {
    try {
      fill(trace, result);
    } catch {
      /* best effort */
    }
    close(trace);
    return result;
  }
  // A Stainless APIPromise: `withResponse()` shares its request and adds the request id.
  const observed: Promise<{ data: any; requestId?: string }> =
    typeof result.withResponse === 'function'
      ? result.withResponse().then((r: any) => ({ data: r?.data, requestId: r?.request_id ?? r?.response?.headers?.get?.(REQUEST_ID_HEADER) ?? undefined }))
      : Promise.resolve(result).then((data: any) => ({ data }));
  observed.then(
    ({ data, requestId }) => {
      try {
        fill(trace, data, requestId);
      } catch {
        /* best effort */
      }
      close(trace);
    },
    (e) => close(trace, e),
  );
  return result;
}

function patchSystemOne(target: any): boolean {
  if (!target || typeof target.systemOne !== 'function') return false;
  if (target[PATCHED]) return true;
  const original = target.systemOne;
  Object.defineProperty(target, PATCHED, { value: true, configurable: true });
  Object.defineProperty(target, 'systemOne', {
    value: function systemOne(this: any, request: any, options: any) {
      return traceCall(original, this, request, options);
    },
    writable: true,
    configurable: true,
  });
  return true;
}

/** Trace `systemOne` on this client; returns the same client. `captureContent` overrides the environment for it. */
export function wrapTypeSafe<T extends object>(client: T, options: { captureContent?: boolean } = {}): T {
  if (options.captureContent !== undefined) captureSettings.set(client, options.captureContent);
  const proto = Object.getPrototypeOf(client);
  if (!proto?.[PATCHED] && !(client as any)[PATCHED]) patchSystemOne(client);
  return client;
}

/** Patch the `TypeSafeClient` class of a module namespace (`await import('@typesafe-ai/sdk')`); returns it. */
export function patchTypeSafeModule<M>(mod: M): M {
  const m: any = mod;
  const cls = m?.TypeSafeClient ?? m?.default?.TypeSafeClient ?? m?.default;
  if (typeof cls?.prototype?.systemOne === 'function') patchSystemOne(cls.prototype);
  return mod;
}

/** Patch `TypeSafeClient.prototype.systemOne` (pass the module); whether it is patched. */
export function instrumentTypeSafe(mod: { TypeSafeClient?: any; default?: any }): boolean {
  patchTypeSafeModule(mod);
  const cls = (mod as any)?.TypeSafeClient ?? (mod as any)?.default?.TypeSafeClient ?? (mod as any)?.default;
  return !!cls?.prototype?.[PATCHED];
}
