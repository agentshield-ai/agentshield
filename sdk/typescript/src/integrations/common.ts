import type { Call } from '../call.js';
import { currentContext, storage } from '../context.js';
import { toJson } from '../serialize.js';
import { integrationAttributes, integrationClient, integrationOp, type IntegrationOpDefaults, type IntegrationOpSettings } from './settings.js';

export interface Accumulator {
  add(chunk: any): void;
  result(): any;
}

export interface LLMSpec {
  opName: string;
  prepare?: (body: Record<string, any>) => Record<string, any>;
  accumulator: () => Accumulator;
  recordUsage: (call: Call, out: any) => void;
  /** Span kind (default "llm"). */
  kind?: string;
  /** Treat the result as a stream (default: `body.stream` and the result is async iterable). */
  isStream?: (body: Record<string, any>, res: unknown) => boolean;
  /** Integration name for settings (`autopatchSettings`, `WEFT_DISABLED_INTEGRATIONS`). */
  integration?: string;
  /** Per-wrapper op settings (postprocess hooks, display name, ...), applied after the global ones. */
  opSettings?: IntegrationOpSettings;
  /** Integration defaults (e.g. a display name function). */
  defaults?: IntegrationOpDefaults;
  /** Extra attributes for each call (e.g. `gen_ai.provider.name`). */
  attributes?: (body: Record<string, any>) => Record<string, unknown> | undefined;
}

function isAsyncIterable(v: unknown): v is AsyncIterable<unknown> {
  return v != null && typeof (v as any)[Symbol.asyncIterator] === 'function';
}

function plain(v: unknown): any {
  return toJson(v, null);
}

/** Wrap a stream so iterating it accumulates chunks and ends the call at the end. */
export function proxyStream<T extends object>(stream: T, acc: Accumulator, done: (out: any, err?: unknown) => void, opts: { raw?: boolean } = {}): T {
  let finished = false;
  const finish = (err?: unknown) => {
    if (finished) return;
    finished = true;
    done(acc.result(), err);
  };
  async function* iterate() {
    try {
      for await (const ch of stream as AsyncIterable<unknown>) {
        try {
          acc.add(opts.raw ? ch : plain(ch));
        } catch {
          /* accumulation is best effort */
        }
        yield ch;
      }
    } catch (e) {
      finish(e);
      throw e;
    } finally {
      finish();
    }
  }
  return new Proxy(stream, {
    get(target, prop) {
      if (prop === Symbol.asyncIterator) return () => iterate();
      const v = Reflect.get(target, prop, target);
      return typeof v === 'function' ? v.bind(target) : v;
    },
  });
}

/** Wrap an LLM client method (bound) so each request becomes a traced call. */
export function wrapLLMMethod(orig: (...args: any[]) => any, spec: LLMSpec): (...args: any[]) => Promise<any> {
  const wrapped = async (...args: any[]) => {
    const client = integrationClient(spec.integration);
    if (!client) return orig(...args);
    let [body, ...rest] = args;
    body = { ...(body ?? {}) };
    if (spec.prepare) body = spec.prepare(body);
    const inputs: Record<string, unknown> = {};
    for (const [k, v] of Object.entries(body)) if (v !== undefined) inputs[k] = v;
    const meta = integrationOp(spec.integration, spec.opName, spec.kind ?? 'llm', spec.defaults, spec.opSettings);
    let extra: Record<string, unknown> | undefined;
    try {
      extra = spec.attributes?.(body);
    } catch {
      /* ignore */
    }
    const call = client.createSpan(meta, inputs, { attributes: integrationAttributes(spec.integration, extra, spec.opSettings) });
    const ctx = { ...currentContext(), stack: [...currentContext().stack, call] };
    let res: any;
    try {
      res = await storage.run(ctx, () => orig(body, ...rest));
    } catch (e) {
      client.finishSpan(call, undefined, e, meta);
      throw e;
    }
    const finish = (out: any, err?: unknown) => {
      try {
        if (out) spec.recordUsage(call, out);
      } catch {
        /* ignore */
      }
      client.finishSpan(call, out, err, meta);
    };
    const streaming = spec.isStream ? spec.isStream(body, res) : !!body.stream;
    if (streaming && isAsyncIterable(res)) return proxyStream(res, spec.accumulator(), finish);
    finish(plain(res));
    return res;
  };
  Object.defineProperty(wrapped, '__weftPatched', { value: true });
  return wrapped;
}

export function patchMethod(owner: any, attr: string, spec: LLMSpec): void {
  const orig = owner?.[attr];
  if (typeof orig !== 'function' || orig.__weftPatched) return;
  owner[attr] = wrapLLMMethod(orig.bind(owner), spec);
}

/** Run `fn` with `call` as the current call (ops and wrapped clients inside nest under it). */
export function withCall<T>(call: Call, fn: () => T): T {
  return storage.run({ ...currentContext(), stack: [...currentContext().stack, call] }, fn);
}

export { isAsyncIterable, plain };

/** Patch `owner[attr]` with `make(original)` once (idempotent). Returns whether it is patched. */
export function patchWith(owner: any, attr: string, make: (orig: (...args: any[]) => any) => (...args: any[]) => any): boolean {
  const orig = owner?.[attr];
  if (typeof orig !== 'function') return false;
  if (orig.__weftPatched) return true;
  const wrapped = make(orig);
  Object.defineProperty(wrapped, '__weftPatched', { value: true });
  Object.defineProperty(wrapped, '__weftOriginal', { value: orig });
  try {
    owner[attr] = wrapped;
  } catch {
    return false;
  }
  return true;
}

/** Undo `patchWith`. */
export function unpatch(owner: any, attr: string): void {
  const cur = owner?.[attr];
  if (cur?.__weftOriginal) owner[attr] = cur.__weftOriginal;
}

/** Decode bytes / JSON strings into a JSON value when possible. */
export function decodeBody(body: unknown): unknown {
  let v: unknown = body;
  if (v instanceof Uint8Array) v = new TextDecoder().decode(v);
  else if (v && typeof v === 'object' && typeof (v as any).transformToString === 'function') {
    try {
      v = (v as any).transformToString();
    } catch {
      return v;
    }
  }
  if (typeof v === 'string') {
    try {
      return JSON.parse(v);
    } catch {
      return v;
    }
  }
  return v;
}

/** Shorten a value to a one-line display string. */
export function short(v: unknown, n = 60): string {
  let s = typeof v === 'string' ? v : v == null ? '' : JSON.stringify(v) ?? '';
  s = s.replace(/\s+/g, ' ').trim();
  return s.length <= n ? s : s.slice(0, n - 1) + '…';
}
