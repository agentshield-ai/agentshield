/**
 * Rebuild runnable ops from stored code.
 *
 * SECURITY: loading an op evaluates the JavaScript source stored on the server
 * (`new Function`). Anyone who can write to the project can store code there,
 * so the SDK never does this on its own: `get()` of a function returns an inert
 * `StoredOp`, and building a callable needs an explicit opt-in
 * (`loadOp(ref, { trustCode: true })`, `get(ref, { trustCode: true })` or
 * `WEFT_ALLOW_CODE_LOAD=1`). Only self-contained functions can be rebuilt:
 * closures over module variables fail when called (ReferenceError).
 */
import type { WeftClient } from './client.js';
import { getClient } from './global.js';
import { op, type Op } from './op.js';
import type { Ref } from './refs.js';

export class OpLoadError extends Error {
  override name = 'OpLoadError';
}

export const NOT_TRUSTED =
  'stored code is not run unless you opt in: use loadOp(ref, { trustCode: true }) ' +
  '(or get(ref, { trustCode: true }), or set WEFT_ALLOW_CODE_LOAD=1) for functions from projects you trust';

/** Whether stored function code may be evaluated: an explicit `trustCode: true` or `WEFT_ALLOW_CODE_LOAD=1`. */
export function codeLoadAllowed(trustCode?: boolean): boolean {
  if (trustCode) return true;
  const env = typeof process !== 'undefined' ? process.env?.WEFT_ALLOW_CODE_LOAD : undefined;
  return ['1', 'true', 'yes', 'on'].includes(String(env ?? '').trim().toLowerCase());
}

/**
 * A stored function version, as data: its name, source code and ref.
 * `get()` returns this for function refs instead of evaluating the stored code.
 * To run it, load it explicitly with `loadOp(ref, { trustCode: true })` (or
 * `stored.load({ trustCode: true })`) if you trust the project it came from.
 */
export class StoredOp {
  readonly _type = 'StoredOp';
  readonly name: string;
  readonly code: string;
  readonly language: string;
  readonly signature?: string;
  readonly value: Record<string, unknown>;
  __weftRef?: Ref;

  constructor(val: any, ref?: Ref) {
    this.value = { ...(val ?? {}) };
    this.name = String(val?.name ?? ref?.name ?? '');
    this.code = String(val?.code ?? '');
    this.language = String(val?.language ?? 'javascript');
    if (val?.signature != null) this.signature = String(val.signature);
    Object.defineProperty(this, '__weftRef', { value: ref, enumerable: false, writable: true });
  }

  get ref(): Ref | undefined {
    return this.__weftRef;
  }

  /** Build a runnable op from the stored code (needs `trustCode: true`). */
  load(opts: { trustCode?: boolean } = {}, client?: WeftClient): Op<(...args: any[]) => any> {
    if (!codeLoadAllowed(opts.trustCode)) throw new OpLoadError(`cannot load op ${this.where()}: ${NOT_TRUSTED}`);
    return evalOp(this.value, this.__weftRef, client ?? getClient() ?? undefined);
  }

  private where(): string {
    return this.__weftRef ? this.__weftRef.uri() : this.name;
  }

  toJSON(): unknown {
    return this.__weftRef ? this.__weftRef.uri() : this.value;
  }
}

/** The stored op `val`: a runnable op when code loading is allowed, else an inert `StoredOp`. */
export function loadOp(val: any, ref: Ref | undefined, client: WeftClient, trustCode?: boolean): Op<(...args: any[]) => any> | StoredOp {
  if (!codeLoadAllowed(trustCode)) return new StoredOp(val, ref);
  return evalOp(val, ref, client);
}

/** Evaluate stored code into a traced function. Callers must have checked `codeLoadAllowed`. */
export function evalOp(val: any, ref: Ref | undefined, client?: WeftClient): Op<(...args: any[]) => any> {
  const where = ref ? ref.uri() : String(val?.name ?? 'op');
  const lang = val?.language ?? 'javascript';
  if (lang !== 'javascript' && lang !== 'typescript') {
    throw new OpLoadError(`cannot load op ${where}: stored code is ${JSON.stringify(lang)}, not javascript`);
  }
  const code = String(val?.code ?? '').trim();
  let fn: ((...args: any[]) => any) | undefined;
  let lastErr: unknown;
  for (const body of [`return (${code}\n);`, `return ({${code}\n});`]) {
    try {
      const v = new Function(body)();
      if (typeof v === 'function') fn = v;
      else if (v && typeof v === 'object') fn = Object.values(v).find((x) => typeof x === 'function') as typeof fn;
      if (fn) break;
    } catch (e) {
      lastErr = e;
    }
  }
  if (!fn) {
    const why = lastErr instanceof Error ? ` (${lastErr.message})` : '';
    throw new OpLoadError(`cannot load op ${where}: stored code is not a self-contained function${why}`);
  }
  const loaded = op(fn, { name: String(val.name ?? fn.name) });
  const meta = loaded.__weftOp;
  meta.code = String(val.code ?? '');
  if (val.signature) meta.signature = String(val.signature);
  if (ref && client) meta.refs.set(`${client.serverUrl}|${client.projectId}`, ref);
  return loaded;
}
