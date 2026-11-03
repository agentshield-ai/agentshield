/**
 * TypeSafe (`@typesafe-ai/sdk`): `client.systemOne(request, options)`.
 *
 * Each `systemOne` call becomes one LLM (`chat`) span of provider `typesafe`:
 *
 * - input: `request.state` as a user message (a string as is, anything else
 *   as sorted-key JSON); `request.questions` (an array or an object, each with
 *   its `id`) as the `typesafe.questions` attribute;
 * - output: the `answers` as an assistant message (JSON), output type `json`,
 *   the response `model`, `usage` and the request id (`x-typesafe-request-id`
 *   when the client returns a Stainless `APIPromise`);
 * - parent: the current turn when one is running; else a new `invoke_agent`
 *   turn named `typesafe SDK` (in the current conversation, if any), the root
 *   of its own trace, also inside an op (the integration creates no op call of
 *   its own). When an op is running, the spans are linked to it by the
 *   `weft.parent_call.id` and `weft.parent_call.trace_id` attributes
 *   (DESIGN.md §4; the server exposes them as the agent span fields
 *   `parent_call_id` / `parent_call_trace_id`);
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
import { currentContext } from '../context.js';
import { LLM, Turn, getCurrentConversation, getCurrentTurn, type Message } from '../conversation.js';
import { integrationAttributes, integrationClient } from './settings.js';

const INTEGRATION = 'typesafe';
const PROVIDER = 'typesafe';
const OWN_TURN_AGENT = 'typesafe SDK';
const OPERATION = 'typesafe.system_one';
const REQUEST_ID_HEADER = 'x-typesafe-request-id';
const CAPTURE_ENV = 'OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT';
const FALSY = ['0', 'false', 'no', 'off'];
const DEFAULT_MODEL = 'jev-latest';
const PATCHED = Symbol.for('weft.typesafe.patched');

/** Per-client `captureContent` given to `wrapTypeSafe`. */
const clientCapture = new WeakMap<object, boolean>();

function captureEnabled(client: object): boolean {
  let allowed = clientCapture.get(client);
  if (allowed === undefined) {
    const env = process.env[CAPTURE_ENV];
    allowed = env == null || !FALSY.includes(env.trim().toLowerCase());
  }
  return allowed && getCurrentConversation()?.includeContent !== false;
}

/** A JSON-ready copy with object keys in sorted order (undefined values dropped). */
function canonical(value: any): any {
  if (value === null || typeof value !== 'object') return value;
  if (Array.isArray(value)) return value.map(canonical);
  if (typeof value.toJSON === 'function') return canonical(value.toJSON());
  return Object.fromEntries(
    Object.keys(value)
      .sort()
      .filter((k) => value[k] !== undefined)
      .map((k) => [k, canonical(value[k])]),
  );
}

function userStateMessage(state: unknown): Message {
  const plain = canonical(state);
  return typeof plain === 'string'
    ? { role: 'user', content: plain }
    : ({ role: 'user', parts: [{ type: 'text', content: JSON.stringify(plain) }] } as Message);
}

/** `[{id: key-or-index, ...question}]` as JSON, or undefined for anything that is not a list or an object. */
function encodeQuestions(questions: unknown): string | undefined {
  let pairs: Array<[string | number, unknown]>;
  if (Array.isArray(questions)) pairs = questions.map((q, i) => [i, q]);
  else if (questions && typeof questions === 'object') pairs = Object.entries(questions);
  else return undefined;
  return JSON.stringify(
    pairs.map(([id, q]) => (q && typeof q === 'object' && !Array.isArray(q) ? { id, ...(q as object) } : { id, value: q })),
  );
}

function requestIdOfError(error: any): string | undefined {
  if (typeof error?.requestId === 'string' && error.requestId) return error.requestId;
  let header: unknown;
  try {
    header = error?.headers?.get?.(REQUEST_ID_HEADER);
  } catch {
    return undefined;
  }
  return typeof header === 'string' && header ? header : undefined;
}

/**
 * Record a failure as error type, HTTP status and request id. The error
 * message is dropped on purpose: TypeSafe's API errors embed the response body.
 */
function markFailed(span: LLM | Turn, error: any): void {
  if (Number.isInteger(error?.status)) span.setAttributes({ http: { response: { status_code: error.status } } });
  const requestId = requestIdOfError(error);
  if (requestId && span instanceof LLM) span.responseId = requestId; // gen_ai.response.id
  else if (requestId) span.setAttributes({ gen_ai: { response: { id: requestId } } });
  const redacted = new Error('');
  redacted.name = (error instanceof Error && error.name) || (typeof error?.name === 'string' && error.name) || 'Error';
  redacted.stack = '';
  span.recordError(redacted);
}

/** Link attributes to the innermost running op (conversation spans are skipped), or {}. */
function linkToRunningOp(): Record<string, unknown> {
  const calls = currentContext().stack.filter((c) => !c.conversationSpan);
  const op = calls[calls.length - 1];
  return op?.id && op.traceId ? { weft: { parent_call: { id: op.id, trace_id: op.traceId } } } : {};
}

function withLink(attrs: Record<string, unknown>, link: Record<string, unknown>): Record<string, unknown> {
  if (!link.weft) return attrs;
  return { ...attrs, weft: { ...((attrs.weft as Record<string, unknown>) ?? {}), ...(link.weft as Record<string, unknown>) } };
}

function integrationAttrs(): Record<string, unknown> {
  return integrationAttributes(INTEGRATION, { weft: { integration_operation: OPERATION } });
}

/** The span (and, when needed, the turn) of one traced `systemOne` call. */
class SystemOneSpan {
  private finished = false;

  private constructor(
    private readonly llm: LLM,
    private readonly turn: Turn,
    private readonly ownsTurn: boolean,
    private readonly capture: boolean,
  ) {}

  /** Open the spans; null when tracing is off or setup fails. */
  static begin(client: any, request: any): SystemOneSpan | null {
    if (!integrationClient(INTEGRATION)) return null;
    let turn: Turn | undefined;
    let llm: LLM | undefined;
    let ownsTurn = false;
    try {
      const capture = captureEnabled(client);
      const requested = request?.model ?? client?.defaultModel ?? DEFAULT_MODEL;
      // Encode first: state that cannot be encoded must not leave a turn behind.
      const message = capture ? userStateMessage(request?.state) : undefined;
      const questions = capture ? encodeQuestions(request?.questions) : undefined;
      const link = linkToRunningOp();
      const active = getCurrentTurn();
      if (active && !active.ended) {
        turn = active;
      } else {
        // A turn of our own: a new trace (also inside an op), attached to the
        // current conversation without becoming its current turn.
        turn = new Turn({ agentName: OWN_TURN_AGENT });
        const conversation = getCurrentConversation();
        if (conversation) turn.conv = conversation.info();
        turn.setAttributes(withLink(integrationAttrs(), link));
        ownsTurn = true;
      }
      llm = turn.startLLM({ model: typeof requested === 'string' ? requested : '', providerName: PROVIDER });
      if (message) llm.inputMessages = [message];
      llm.setAttributes({ ...withLink(integrationAttrs(), link), ...(questions === undefined ? {} : { typesafe: { questions } }) });
      llm.start();
      return new SystemOneSpan(llm, turn, ownsTurn, capture);
    } catch {
      try {
        llm?.end();
        if (ownsTurn) turn?.end();
      } catch {
        /* nothing more to clean up */
      }
      return null;
    }
  }

  succeed(body: any, requestId?: string): void {
    try {
      const fields: Record<string, unknown> = { outputType: 'json' };
      if (body && typeof body === 'object') {
        if (typeof body.model === 'string' && body.model) fields.responseModel = body.model;
        const usage = body.usage;
        if (usage && (typeof usage.input_tokens === 'number' || typeof usage.output_tokens === 'number')) {
          fields.usage = { inputTokens: usage.input_tokens ?? 0, outputTokens: usage.output_tokens ?? 0 };
        }
        if (this.capture && body.answers != null) {
          fields.outputMessages = [{ role: 'assistant', parts: [{ type: 'text', content: JSON.stringify(canonical(body.answers)) }] }];
        }
      }
      if (requestId) fields.responseId = requestId;
      this.llm.record(fields as any);
    } catch {
      /* the call itself succeeded; a recording problem must not surface */
    }
    this.finish();
  }

  fail(error: unknown): void {
    this.finish(error, true);
  }

  private finish(error?: unknown, failed = false): void {
    if (this.finished) return;
    this.finished = true;
    if (failed) {
      markFailed(this.llm, error);
      if (this.ownsTurn) markFailed(this.turn, error);
    }
    this.llm.end();
    if (this.ownsTurn) this.turn.end();
  }
}

function tracedSystemOne(original: (...args: any[]) => any, client: any, request: any, options: any): any {
  const span = SystemOneSpan.begin(client, request);
  if (!span) return Reflect.apply(original, client, [request, options]);
  let result: any;
  try {
    result = Reflect.apply(original, client, [request, options]);
  } catch (e) {
    span.fail(e);
    throw e;
  }
  if (typeof result?.then !== 'function') {
    span.succeed(result);
    return result;
  }
  // A Stainless APIPromise exposes `withResponse()`, which shares the request
  // and also yields the request id; a plain promise only gives the body.
  const settled: Promise<{ data: any; requestId?: string }> =
    typeof result.withResponse === 'function'
      ? result.withResponse().then((r: any) => ({
          data: r?.data,
          requestId: r?.request_id ?? r?.response?.headers?.get?.(REQUEST_ID_HEADER) ?? undefined,
        }))
      : Promise.resolve(result).then((data: any) => ({ data }));
  settled.then(
    ({ data, requestId }) => span.succeed(data, requestId),
    (e) => span.fail(e),
  );
  return result;
}

function patchSystemOne(target: any): boolean {
  if (typeof target?.systemOne !== 'function') return false;
  if (target[PATCHED]) return true;
  const original = target.systemOne;
  Object.defineProperty(target, PATCHED, { value: true, configurable: true });
  Object.defineProperty(target, 'systemOne', {
    value: function systemOne(this: any, request: any, options: any) {
      return tracedSystemOne(original, this, request, options);
    },
    writable: true,
    configurable: true,
  });
  return true;
}

function clientClass(mod: any): any {
  return mod?.TypeSafeClient ?? mod?.default?.TypeSafeClient ?? mod?.default;
}

/** Trace `systemOne` calls made through `client` and return it. `captureContent` overrides the environment for this client. */
export function wrapTypeSafe<T extends object>(client: T, options: { captureContent?: boolean } = {}): T {
  if (options.captureContent !== undefined) clientCapture.set(client, options.captureContent);
  if (!Object.getPrototypeOf(client)?.[PATCHED] && !(client as any)[PATCHED]) patchSystemOne(client);
  return client;
}

/** Patch the `TypeSafeClient` class of a module namespace (`await import('@typesafe-ai/sdk')`); returns it. */
export function patchTypeSafeModule<M>(mod: M): M {
  const cls = clientClass(mod);
  if (typeof cls?.prototype?.systemOne === 'function') patchSystemOne(cls.prototype);
  return mod;
}

/** Patch `TypeSafeClient.prototype.systemOne` (pass the module); whether it is patched. */
export function instrumentTypeSafe(mod: { TypeSafeClient?: any; default?: any }): boolean {
  patchTypeSafeModule(mod);
  return !!clientClass(mod)?.prototype?.[PATCHED];
}
