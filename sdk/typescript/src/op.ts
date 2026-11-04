import { logger } from './log.js';
import { Call } from './call.js';
import { currentContext, storage, type WeftContext } from './context.js';
import { tracingClient } from './global.js';
import type { Ref } from './refs.js';

export interface OpOptions {
  /** Op name (default: the function name). */
  name?: string;
  /** Display name for each call, or a function of the call. */
  callDisplayName?: string | ((call: Call) => string);
  /** Input names for positional arguments (default: parsed from the function source). */
  parameterNames?: string[];
  postprocessInputs?: (inputs: Record<string, unknown>) => Record<string, unknown>;
  postprocessOutput?: (output: unknown) => unknown;
  /** Reduce streamed items into the call output (default: collect into an array). */
  accumulator?: (acc: any, item: unknown) => any;
  /** Span kind (agent, llm, tool, chain, retriever, embedding, scorer, ...): stored as attributes.weft.span_kind. */
  kind?: string;
  /** UI color (red, orange, yellow, green, blue, purple): stored as attributes.weft.color. */
  color?: string;
  /** Fraction of root calls to trace (children follow their root). Default 1. */
  tracingSampleRate?: number;
  /** Store the function source with the op (default true; see also the captureCode setting). */
  enableCodeCapture?: boolean;
}

export interface OpMeta {
  name: string;
  fn: (...args: any[]) => any;
  code: string;
  signature: string;
  paramNames: Array<string | null>;
  restIndex: number;
  options: OpOptions;
  thisArg?: unknown;
  refs: Map<string, Ref>;
}

export type Op<F extends (...args: any[]) => any> = F & {
  __weftOp: OpMeta;
  /** Run the op and resolve to `[result, call]`; errors are captured on the call. */
  call: (...args: Parameters<F>) => Promise<[Awaited<ReturnType<F>> | undefined, Call | null]>;
};

export function isOp(v: unknown): v is Op<(...args: any[]) => any> {
  return typeof v === 'function' && !!(v as { __weftOp?: unknown }).__weftOp;
}

function stripDefault(p: string): string {
  let depth = 0;
  for (let i = 0; i < p.length; i++) {
    const ch = p[i];
    if ('([{'.includes(ch)) depth++;
    else if (')]}'.includes(ch)) depth--;
    else if (ch === '=' && depth === 0 && p[i + 1] !== '>') return p.slice(0, i);
  }
  return p;
}

function splitTopLevel(s: string): string[] {
  const out: string[] = [];
  let depth = 0;
  let cur = '';
  let quote: string | null = null;
  for (const ch of s) {
    if (quote) {
      cur += ch;
      if (ch === quote) quote = null;
      continue;
    }
    if (ch === '"' || ch === "'" || ch === '`') quote = ch;
    if ('([{'.includes(ch)) depth++;
    if (')]}'.includes(ch)) depth--;
    if (ch === ',' && depth === 0) {
      out.push(cur);
      cur = '';
    } else cur += ch;
  }
  if (cur.trim()) out.push(cur);
  return out.map((p) => p.trim()).filter(Boolean);
}

/** Parse parameter names from a function's source; destructured params are null. */
export function parseParams(fn: (...args: any[]) => any): { names: Array<string | null>; rest: number; signature: string } {
  const src = Function.prototype.toString.call(fn).replace(/\/\*[\s\S]*?\*\//g, '').replace(/\/\/.*$/gm, '');
  let params = '';
  const arrowSingle = /^(?:async\s+)?([A-Za-z_$][\w$]*)\s*=>/.exec(src);
  if (arrowSingle) params = arrowSingle[1];
  else {
    const start = src.indexOf('(');
    if (start >= 0) {
      let depth = 0;
      for (let i = start; i < src.length; i++) {
        if (src[i] === '(') depth++;
        else if (src[i] === ')') {
          depth--;
          if (depth === 0) {
            params = src.slice(start + 1, i);
            break;
          }
        }
      }
    }
  }
  const parts = splitTopLevel(params);
  let rest = -1;
  const names = parts.map((p, i) => {
    let s = p;
    if (s.startsWith('...')) {
      rest = i;
      s = s.slice(3);
    }
    const eq = stripDefault(s).trim();
    if (eq.startsWith('{') || eq.startsWith('[')) return null;
    return /^[A-Za-z_$][\w$]*$/.test(eq) ? eq : null;
  });
  return { names, rest, signature: `(${parts.join(', ')})` };
}

function isPlainObject(v: unknown): v is Record<string, unknown> {
  if (v === null || typeof v !== 'object') return false;
  const p = Object.getPrototypeOf(v);
  return p === Object.prototype || p === null;
}

export function buildInputs(meta: OpMeta, args: unknown[]): Record<string, unknown> {
  const names = meta.options.parameterNames ?? meta.paramNames;
  const inputs: Record<string, unknown> = {};
  if (names.length === 1 && names[0] === null && args.length === 1 && isPlainObject(args[0])) {
    Object.assign(inputs, args[0]);
    return inputs;
  }
  for (let i = 0; i < args.length; i++) {
    if (i === meta.restIndex && names[i]) {
      inputs[names[i] as string] = args.slice(i);
      break;
    }
    inputs[(i < names.length && names[i]) || `arg${i}`] = args[i];
  }
  return inputs;
}

function listAcc(acc: any, item: unknown): any {
  const a = acc ?? [];
  a.push(item);
  return a;
}

/**
 * Trace a function: `weft.trace(fn, {name})`, or `weft.trace(thisArg, fn, opts)`
 * for a method (the instance is recorded as `self`). Every call is recorded as
 * a span named by the function's ref (the function is saved as a versioned
 * `function` object). Sync and async functions, (async) generators and
 * functions returning async iterables are supported.
 */
export function trace<F extends (...args: any[]) => any>(fn: F, options?: OpOptions): Op<F>;
export function trace<F extends (...args: any[]) => any>(thisArg: object, fn: F, options?: OpOptions): Op<F>;
export function trace(a: any, b?: any, c?: any): any {
  return makeTraced(a, b, c);
}

/** The SDK's own modules use this name; the public `op` export is a deprecated alias of `trace`. */
export const op = trace;

/** Options of `trace` (earlier name `OpOptions`). */
export type TraceOptions = OpOptions;

function makeTraced(a: any, b?: any, c?: any): any {
  let thisArg: unknown;
  let fn: (...args: any[]) => any;
  let options: OpOptions;
  if (typeof a === 'function' && (b === undefined || (typeof b === 'object' && b !== null))) {
    fn = a;
    options = b ?? {};
  } else {
    thisArg = a;
    fn = b;
    options = c ?? {};
  }
  if (isOp(fn) && thisArg === undefined && !Object.keys(options).length) return fn;
  const inner = isOp(fn) ? fn.__weftOp.fn : fn;
  const parsed = parseParams(inner);
  const owner = thisArg && (thisArg as object).constructor && (thisArg as object).constructor !== Object ? (thisArg as object).constructor.name : '';
  const baseName = inner.name ? inner.name.replace(/^bound /, '') : 'anonymous';
  const meta: OpMeta = {
    name: options.name ?? (owner ? `${owner}.${baseName}` : baseName),
    fn: inner,
    code: Function.prototype.toString.call(inner),
    signature: parsed.signature,
    paramNames: parsed.names,
    restIndex: parsed.rest,
    options,
    thisArg,
    refs: new Map(),
  };
  const wrapped = function (this: unknown, ...args: unknown[]) {
    return execute(meta, thisArg ?? this, args, false);
  };
  Object.defineProperty(wrapped, 'name', { value: meta.name });
  Object.defineProperty(wrapped, '__weftOp', { value: meta });
  Object.defineProperty(wrapped, 'call', {
    value: async function (this: unknown, ...args: unknown[]) {
      return execute(meta, thisArg ?? this, args, true);
    },
  });
  return wrapped;
}

interface Tracer {
  call: Call | null;
  ctx: WeftContext | null;
  finish: (output: unknown, err?: unknown) => void;
}

function startTracer(meta: OpMeta, self: unknown, args: unknown[]): Tracer {
  const client = tracingClient();
  if (!client) return { call: null, ctx: null, finish: () => {} };
  const rate = meta.options.tracingSampleRate;
  if (rate !== undefined && rate < 1 && currentContext().stack.length === 0 && Math.random() >= rate) {
    // sampled out: run the whole subtree untraced
    return { call: null, ctx: { ...currentContext(), suppressed: true }, finish: () => {} };
  }
  try {
    let inputs = buildInputs(meta, args);
    if (meta.thisArg !== undefined) inputs = { self: meta.thisArg, ...inputs };
    const call = client.createCall(meta, inputs);
    const ctx = { ...currentContext(), stack: [...currentContext().stack, call] };
    return { call, ctx, finish: (output, err) => client.finishCall(call, output, err, meta) };
  } catch (e) {
    logger.warn(`weft: failed to start call for ${meta.name}: ${(e as Error).message}`);
    return { call: null, ctx: null, finish: () => {} };
  }
}

function isAsyncIterable(v: unknown): v is AsyncIterable<unknown> {
  return v != null && typeof (v as any)[Symbol.asyncIterator] === 'function';
}

function isGenerator(v: unknown): v is Generator<unknown> {
  return v != null && typeof v === 'object' && typeof (v as any).next === 'function' && typeof (v as any)[Symbol.iterator] === 'function' && !Array.isArray(v);
}

function wrapAsyncIterable(meta: OpMeta, t: Tracer, it: AsyncIterable<unknown>): AsyncIterable<unknown> {
  const accFn = meta.options.accumulator ?? listAcc;
  async function* gen() {
    let acc: any = undefined;
    const iter = it[Symbol.asyncIterator]();
    let failed = false;
    try {
      while (true) {
        const r = t.ctx ? await storage.run(t.ctx, () => iter.next()) : await iter.next();
        if (r.done) break;
        acc = accFn(acc, r.value);
        yield r.value;
      }
    } catch (e) {
      failed = true;
      t.finish(acc ?? [], e);
      throw e;
    } finally {
      if (!failed) {
        t.finish(acc ?? []);
        await iter.return?.();
      }
    }
  }
  return gen();
}

function wrapGenerator(meta: OpMeta, t: Tracer, it: Generator<unknown>): Generator<unknown> {
  const accFn = meta.options.accumulator ?? listAcc;
  function* gen() {
    let acc: any = undefined;
    let failed = false;
    try {
      while (true) {
        const r = t.ctx ? storage.run(t.ctx, () => it.next()) : it.next();
        if (r.done) break;
        acc = accFn(acc, r.value);
        yield r.value;
      }
    } catch (e) {
      failed = true;
      t.finish(acc ?? [], e);
      throw e;
    } finally {
      if (!failed) {
        t.finish(acc ?? []);
        it.return?.(undefined);
      }
    }
  }
  return gen();
}

function execute(meta: OpMeta, self: unknown, args: unknown[], returnCall: boolean): any {
  const t = startTracer(meta, self, args);
  const run = () => meta.fn.apply(self, args);
  let res: unknown;
  try {
    res = t.ctx ? storage.run(t.ctx, run) : run();
  } catch (e) {
    t.finish(undefined, e);
    if (returnCall) return [undefined, t.call];
    throw e;
  }
  if (res instanceof Promise || (res != null && typeof (res as any).then === 'function' && typeof (res as any).catch === 'function')) {
    const p = (res as Promise<unknown>).then(
      (v) => {
        t.finish(v);
        return returnCall ? [v, t.call] : v;
      },
      (e) => {
        t.finish(undefined, e);
        if (returnCall) return [undefined, t.call];
        throw e;
      },
    );
    return p;
  }
  if (isAsyncIterable(res) && !(typeof res === 'string')) {
    const w = wrapAsyncIterable(meta, t, res);
    return returnCall ? [w, t.call] : w;
  }
  if (isGenerator(res)) {
    const w = wrapGenerator(meta, t, res);
    return returnCall ? [w, t.call] : w;
  }
  t.finish(res);
  return returnCall ? [res, t.call] : res;
}
