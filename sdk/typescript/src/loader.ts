/**
 * Rebuild runnable ops from stored code.
 *
 * SECURITY: loading an op evaluates the JavaScript source stored on the server
 * (`new Function`). Only load ops from servers and projects you trust. Only
 * self-contained functions can be rebuilt: closures over module variables
 * fail when called (ReferenceError).
 */
import type { WeftClient } from './client.js';
import { op, type Op } from './op.js';
import type { Ref } from './refs.js';

export class OpLoadError extends Error {
  override name = 'OpLoadError';
}

export function loadOp(val: any, ref: Ref | undefined, client: WeftClient): Op<(...args: any[]) => any> {
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
  if (ref) meta.refs.set(`${client.serverUrl}|${client.projectId}`, ref);
  return loaded;
}
