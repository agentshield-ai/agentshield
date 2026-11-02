import { AsyncLocalStorage } from 'node:async_hooks';
import type { Call } from './call.js';
import { uuid7 } from './ids.js';

export interface ThreadContext {
  threadId: string;
  entryParentId: string | null;
}

export interface WeftContext {
  stack: Call[];
  attributes: Record<string, unknown>;
  thread: ThreadContext | null;
  suppressed: boolean;
}

const EMPTY: WeftContext = { stack: [], attributes: {}, thread: null, suppressed: false };
export const storage = new AsyncLocalStorage<WeftContext>();

export function currentContext(): WeftContext {
  return storage.getStore() ?? EMPTY;
}

/** The innermost call being traced in the current async context. */
export function getCurrentCall(): Call | null {
  const s = currentContext().stack;
  return s.length ? s[s.length - 1] : null;
}

export function runWith<T>(patch: Partial<WeftContext>, fn: () => T): T {
  return storage.run({ ...currentContext(), ...patch }, fn);
}

/** Run `fn` with attributes attached to every call started inside it. */
export function attributes<T>(attrs: Record<string, unknown>, fn: () => T): T {
  return runWith({ attributes: { ...currentContext().attributes, ...attrs } }, fn);
}

/**
 * Run `fn` inside a conversation thread. Calls started directly inside are
 * turns (turn_id = their id); descendants inherit thread and turn ids.
 */
export function withThread<T>(fn: (threadId: string) => T): T;
export function withThread<T>(threadId: string | null | undefined, fn: (threadId: string) => T): T;
export function withThread<T>(a: unknown, b?: unknown): T {
  const fn = (typeof a === 'function' ? a : b) as (threadId: string) => T;
  const threadId = (typeof a === 'string' && a) || uuid7();
  const parent = getCurrentCall();
  return runWith({ thread: { threadId, entryParentId: parent ? parent.id : null } }, () => fn(threadId));
}

/** Run `fn` without recording any calls (ops still execute). */
export function withTracingDisabled<T>(fn: () => T): T {
  return runWith({ suppressed: true }, fn);
}
