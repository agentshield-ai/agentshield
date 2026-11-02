import type { Call } from '../call.js';
import { currentContext, storage } from '../context.js';
import { tracingClient } from '../global.js';
import { toJson } from '../serialize.js';

export interface Accumulator {
  add(chunk: any): void;
  result(): any;
}

export interface LLMSpec {
  opName: string;
  prepare?: (body: Record<string, any>) => Record<string, any>;
  accumulator: () => Accumulator;
  recordUsage: (call: Call, out: any) => void;
}

function isAsyncIterable(v: unknown): v is AsyncIterable<unknown> {
  return v != null && typeof (v as any)[Symbol.asyncIterator] === 'function';
}

function plain(v: unknown): any {
  return toJson(v, null);
}

/** Wrap a stream so iterating it accumulates chunks and ends the call at the end. */
export function proxyStream<T extends object>(stream: T, acc: Accumulator, done: (out: any, err?: unknown) => void): T {
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
          acc.add(plain(ch));
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
    const client = tracingClient();
    if (!client) return orig(...args);
    let [body, ...rest] = args;
    body = { ...(body ?? {}) };
    if (spec.prepare) body = spec.prepare(body);
    const inputs: Record<string, unknown> = {};
    for (const [k, v] of Object.entries(body)) if (v !== undefined) inputs[k] = v;
    const call = client.createCall(spec.opName, inputs);
    const ctx = { ...currentContext(), stack: [...currentContext().stack, call] };
    let res: any;
    try {
      res = await storage.run(ctx, () => orig(body, ...rest));
    } catch (e) {
      client.finishCall(call, undefined, e);
      throw e;
    }
    const finish = (out: any, err?: unknown) => {
      try {
        if (out) spec.recordUsage(call, out);
      } catch {
        /* ignore */
      }
      client.finishCall(call, out, err);
    };
    if (body.stream && isAsyncIterable(res)) return proxyStream(res, spec.accumulator(), finish);
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
