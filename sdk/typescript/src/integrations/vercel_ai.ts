import type { Call } from '../call.js';
import { addUsage } from '../usage.js';
import { short, withCall } from './common.js';
import { integrationAttributes, integrationClient, integrationOp, pickTraceSettings, type IntegrationTraceSettings, type WrapperTraceOptions } from './settings.js';

const INTEGRATION = 'vercel_ai';

function usageOf(u: any): { p: number; c: number; t?: number; reasoning?: number; cached?: number } | null {
  if (!u) return null;
  const p = u.inputTokens ?? u.promptTokens;
  const c = u.outputTokens ?? u.completionTokens;
  if (p === undefined && c === undefined) return null;
  const num = (x: any) => (typeof x === 'number' ? x : typeof x?.total === 'number' ? x.total : 0);
  return {
    p: num(p),
    c: num(c),
    t: typeof u.totalTokens === 'number' ? u.totalTokens : undefined,
    reasoning: typeof u.reasoningTokens === 'number' ? u.reasoningTokens : undefined,
    cached: typeof u.cachedInputTokens === 'number' ? u.cachedInputTokens : undefined,
  };
}

function recordUsage(call: Call, model: string | undefined, raw: any): void {
  const u = usageOf(raw);
  if (!u) return;
  addUsage(call, model, u.p, u.c, u.t);
  const entry = (call.summary.usage as any)?.[model || 'unknown'];
  if (entry && u.reasoning) entry.reasoning_tokens = (entry.reasoning_tokens ?? 0) + u.reasoning;
  if (entry && u.cached) entry.cache_read_input_tokens = (entry.cache_read_input_tokens ?? 0) + u.cached;
}

function inputsOf(params: any, model: any): Record<string, unknown> {
  const { abortSignal: _a, headers: _h, ...rest } = params ?? {};
  return { model: model?.modelId, provider: model?.provider, ...rest };
}

function modelAttrs(model: any): Record<string, unknown> {
  const out: Record<string, unknown> = { 'gen_ai.operation.name': 'chat' };
  if (model?.modelId) out['gen_ai.request.model'] = model.modelId;
  if (model?.provider) out['gen_ai.provider.name'] = String(model.provider).split('.')[0];
  return out;
}

export interface WeftMiddlewareOptions extends Omit<IntegrationTraceSettings, 'name'> {
  /** Span name prefix (default "vercel_ai"). */
  name?: string;
  /** Replace the name of every span (same as `traceSettings.name`). */
  spanName?: string;
  traceSettings?: IntegrationTraceSettings;
  /** @deprecated Use `traceSettings`. */
  opSettings?: IntegrationTraceSettings;
  /** @deprecated Use `spanName`. */
  opName?: string;
}

/**
 * Vercel AI SDK middleware: `wrapLanguageModel({ model, middleware: weftMiddleware() })`.
 * Each generate/stream request becomes an `llm` call with the model's usage.
 * Streams are accumulated (text, reasoning, tool calls, finish reason, usage).
 */
export function weftMiddleware(opts: WeftMiddlewareOptions = {}): Record<string, any> {
  const prefix = opts.name ?? 'vercel_ai';
  const local = pickTraceSettings(opts);
  return {
    async wrapGenerate({ doGenerate, params, model }: any) {
      const client = integrationClient(INTEGRATION);
      if (!client) return doGenerate();
      const meta = integrationOp(INTEGRATION, `${prefix}.generate`, 'llm', {}, local);
      const call = client.createSpan(meta, inputsOf(params, model), { attributes: integrationAttributes(INTEGRATION, modelAttrs(model), local) });
      try {
        const res = await withCall(call, () => doGenerate());
        recordUsage(call, model?.modelId, res?.usage);
        const { request: _r, response: _resp, rawCall: _rc, rawResponse: _rr, ...out } = res ?? {};
        client.finishSpan(call, out, undefined, meta);
        return res;
      } catch (e) {
        client.finishSpan(call, undefined, e, meta);
        throw e;
      }
    },
    async wrapStream({ doStream, params, model }: any) {
      const client = integrationClient(INTEGRATION);
      if (!client) return doStream();
      const meta = integrationOp(INTEGRATION, `${prefix}.stream`, 'llm', {}, local);
      const call = client.createSpan(meta, inputsOf(params, model), { attributes: integrationAttributes(INTEGRATION, modelAttrs(model), local) });
      let res: any;
      try {
        res = await withCall(call, () => doStream());
      } catch (e) {
        client.finishSpan(call, undefined, e, meta);
        throw e;
      }
      let text = '';
      let reasoning = '';
      let finishReason: unknown = null;
      const toolCalls: unknown[] = [];
      let done = false;
      const finish = (err?: unknown) => {
        if (done) return;
        done = true;
        const out: Record<string, unknown> = { text, finishReason, toolCalls };
        if (reasoning) out.reasoning = reasoning;
        client.finishSpan(call, out, err, meta);
      };
      const transform = new TransformStream({
        transform(chunk: any, controller) {
          try {
            const t = chunk?.type;
            if (t === 'text-delta' || t === 'text') text += chunk.delta ?? chunk.textDelta ?? chunk.text ?? '';
            else if (t === 'reasoning-delta' || t === 'reasoning') reasoning += chunk.delta ?? chunk.textDelta ?? chunk.text ?? '';
            else if (t === 'tool-call') toolCalls.push(chunk);
            else if (t === 'finish') {
              finishReason = chunk.finishReason;
              recordUsage(call, model?.modelId, chunk.usage);
            } else if (t === 'error') finish(chunk.error);
          } catch {
            /* accumulation is best effort */
          }
          controller.enqueue(chunk);
        },
        flush() {
          finish();
        },
      });
      return { ...res, stream: res.stream.pipeThrough(transform) };
    },
  };
}

// ---------------------------------------------------------------- tools

function wrapExecute(name: string, execute: (...a: any[]) => any, local?: IntegrationTraceSettings): (...a: any[]) => any {
  if ((execute as any).__weftPatched) return execute;
  const wrapped = function (this: unknown, args: any, options: any, ...rest: any[]) {
    const client = integrationClient(INTEGRATION);
    if (!client) return execute.call(this, args, options, ...rest);
    const meta = integrationOp(INTEGRATION, `${INTEGRATION}.tool.${name}`, 'tool', { spanDisplayName: `${name}${args && typeof args === 'object' ? ': ' + short(args, 40) : ''}` }, local);
    const attrs: Record<string, unknown> = { 'gen_ai.operation.name': 'execute_tool', 'gen_ai.tool.name': name };
    if (options?.toolCallId) attrs['gen_ai.tool.call.id'] = options.toolCallId;
    const inputs = args && typeof args === 'object' && !Array.isArray(args) ? { ...args } : { input: args };
    const call = client.createSpan(meta, inputs, { attributes: integrationAttributes(INTEGRATION, attrs, local) });
    let res: any;
    try {
      res = withCall(call, () => execute.call(this, args, options, ...rest));
    } catch (e) {
      client.finishSpan(call, undefined, e, meta);
      throw e;
    }
    if (res && typeof res[Symbol.asyncIterator] === 'function' && typeof res.then !== 'function') {
      // streaming tool (async iterable of preliminary results): the last item is the output
      return (async function* () {
        let last: unknown;
        try {
          for await (const item of res) {
            last = item;
            yield item;
          }
          client.finishSpan(call, last, undefined, meta);
        } catch (e) {
          client.finishSpan(call, last, e, meta);
          throw e;
        }
      })();
    }
    return Promise.resolve(res).then(
      (out) => {
        client.finishSpan(call, out, undefined, meta);
        return out;
      },
      (e) => {
        client.finishSpan(call, undefined, e, meta);
        throw e;
      },
    );
  };
  Object.defineProperty(wrapped, '__weftPatched', { value: true });
  return wrapped;
}

/**
 * Trace the `execute` functions of a Vercel AI SDK tool set
 * (`generateText({ tools: weft.wrapAITools(tools) })`). Each execution
 * becomes a `tool` call (`vercel_ai.tool.<name>`) with `gen_ai.tool.name` and
 * `gen_ai.tool.call.id`; ops and wrapped clients used inside nest under it.
 */
export function wrapAITools<T extends Record<string, any>>(tools: T, opts: WrapperTraceOptions = {}): T {
  if (!tools || typeof tools !== 'object') return tools;
  const local = pickTraceSettings(opts);
  const out: Record<string, any> = {};
  for (const [name, t] of Object.entries(tools)) {
    out[name] = t && typeof t.execute === 'function' ? { ...t, execute: wrapExecute(name, t.execute, local) } : t;
  }
  return out as T;
}

// ---------------------------------------------------------------- top-level functions

const TOP = ['generateText', 'streamText', 'generateObject', 'streamObject'] as const;

function topInputs(params: any): Record<string, unknown> {
  const { abortSignal: _a, headers: _h, model, tools, onFinish: _f, onError: _e, onChunk: _c, onStepFinish: _s, experimental_telemetry: _t, ...rest } = params ?? {};
  const out: Record<string, unknown> = { ...rest };
  if (model) out.model = typeof model === 'string' ? model : model.modelId;
  if (tools) out.tools = Object.keys(tools);
  return out;
}

function topOutput(res: any): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const k of ['text', 'object', 'finishReason', 'toolCalls', 'toolResults', 'reasoningText']) {
    try {
      const v = res?.[k];
      if (v !== undefined && typeof v !== 'function' && !(v && typeof v.then === 'function')) out[k] = v;
    } catch {
      /* getters may throw */
    }
  }
  try {
    if (Array.isArray(res?.steps)) out.steps = res.steps.length;
  } catch {
    /* ignore */
  }
  return out;
}

/**
 * Wrap the AI SDK's `generateText`, `streamText`, `generateObject` and
 * `streamObject` (`const ai = weft.wrapAISDK(await import('ai'))`). Each call
 * becomes a parent `chain` call; its tools are wrapped with `wrapAITools`, and
 * models wrapped with `weftMiddleware()` nest their `llm` calls under it.
 * Streaming calls end when the stream finishes (`onFinish`/`onError`).
 * Usage is recorded on the model calls, not on the parent.
 */
export function wrapAISDK<T extends Record<string, any>>(ai: T, opts: WrapperTraceOptions = {}): T {
  const local = pickTraceSettings(opts);
  const out: Record<string, any> = { ...ai };
  for (const fn of TOP) {
    const orig = ai?.[fn];
    if (typeof orig !== 'function') continue;
    const streaming = fn.startsWith('stream');
    out[fn] = function (params: any, ...rest: any[]) {
      const client = integrationClient(INTEGRATION);
      if (!client) return orig.call(this, params, ...rest);
      const meta = integrationOp(INTEGRATION, `${INTEGRATION}.${fn}`, 'chain', {}, local);
      const call = client.createSpan(meta, topInputs(params), { attributes: integrationAttributes(INTEGRATION, undefined, local) });
      const p: any = { ...(params ?? {}) };
      if (p.tools) p.tools = wrapAITools(p.tools, opts);
      if (!streaming) {
        let res: any;
        try {
          res = withCall(call, () => orig.call(this, p, ...rest));
        } catch (e) {
          client.finishSpan(call, undefined, e, meta);
          throw e;
        }
        return Promise.resolve(res).then(
          (r) => {
            client.finishSpan(call, topOutput(r), undefined, meta);
            return r;
          },
          (e) => {
            client.finishSpan(call, undefined, e, meta);
            throw e;
          },
        );
      }
      let done = false;
      const end = (o: unknown, e?: unknown) => {
        if (done) return;
        done = true;
        client.finishSpan(call, o, e, meta);
      };
      const userFinish = p.onFinish;
      const userError = p.onError;
      p.onFinish = async (ev: any) => {
        end(topOutput(ev));
        if (userFinish) return userFinish(ev);
      };
      p.onError = async (ev: any) => {
        end(undefined, ev?.error ?? ev);
        if (userError) return userError(ev);
      };
      try {
        return withCall(call, () => orig.call(this, p, ...rest));
      } catch (e) {
        end(undefined, e);
        throw e;
      }
    };
  }
  return out as T;
}
