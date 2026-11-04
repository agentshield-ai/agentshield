import { AsyncLocalStorage } from 'node:async_hooks';
import type { Call } from './call.js';
import { uuid7 } from './ids.js';
import { deprecated } from './deprecation.js';

/** The block of `withSession`: its session id, and the span current when it began (spans started under that one are turns). */
export interface SessionContext {
  sessionId: string;
  entryParentId: string | null;
}

/** @deprecated Use SessionContext. */
export type ThreadContext = SessionContext;

/** The agent team calls belong to (`weft.team.id` / `weft.team.name`). */
export interface TeamInfo {
  id: string;
  name?: string;
}

export interface WeftContext {
  stack: Call[];
  attributes: Record<string, unknown>;
  /** The team declared by the innermost `withTeam` (or team conversation). */
  team?: TeamInfo | null;
  session: SessionContext | null;
  suppressed: boolean;
  /** Active agent conversation / turn / LLM span (see conversation.ts). */
  conversation?: unknown;
  turn?: unknown;
  llm?: unknown;
  /** `weft.eval.*` link attributes of the EvaluationLogger prediction being recorded. */
  evalSpan?: Record<string, unknown> | null;
}

const EMPTY: WeftContext = { stack: [], attributes: {}, session: null, suppressed: false };
export const storage = new AsyncLocalStorage<WeftContext>();

export function currentContext(): WeftContext {
  return storage.getStore() ?? EMPTY;
}

/** The innermost span being recorded in this context, if any. */
export function getCurrentSpan(): Call | null {
  const s = currentContext().stack;
  return s.length ? s[s.length - 1] : null;
}

/** Earlier name of getCurrentSpan (used throughout the SDK). */
export const getCurrentCall = getCurrentSpan;

export function runWith<T>(patch: Partial<WeftContext>, fn: () => T): T {
  return storage.run({ ...currentContext(), ...patch }, fn);
}

/** Run `fn` with attributes attached to every call started inside it. */
export function attributes<T>(attrs: Record<string, unknown>, fn: () => T): T {
  return runWith({ attributes: { ...currentContext().attributes, ...attrs } }, fn);
}

/**
 * Run `fn` inside a session (conversation): every span started inside gets
 * its `session_id` (a new id when omitted). Spans started directly inside
 * are turns (turn_id = their id); descendants inherit session and turn ids.
 */
export function withSession<T>(fn: (sessionId: string) => T): T;
export function withSession<T>(sessionId: string | null | undefined, fn: (sessionId: string) => T): T;
export function withSession<T>(a: unknown, b?: unknown): T {
  const fn = (typeof a === 'function' ? a : b) as (sessionId: string) => T;
  const sessionId = (typeof a === 'string' && a) || uuid7();
  const parent = getCurrentCall();
  return runWith({ session: { sessionId, entryParentId: parent ? parent.id : null } }, () => fn(sessionId));
}

/** @deprecated Use `withSession(sessionId, fn)`. */
export function withThread<T>(fn: (threadId: string) => T): T;
/** @deprecated Use `withSession(sessionId, fn)`. */
export function withThread<T>(threadId: string | null | undefined, fn: (threadId: string) => T): T;
export function withThread<T>(a: unknown, b?: unknown): T {
  deprecated('withThread()', 'withSession()');
  return withSession(a as string, b as (sessionId: string) => T);
}

/** Run `fn` without recording any calls (ops still execute). */
export function withTracingDisabled<T>(fn: () => T): T {
  return runWith({ suppressed: true }, fn);
}

/** `{weft: {team: {id, name}}}` for a team. */
export function teamAttributes(team: TeamInfo): Record<string, unknown> {
  return { weft: { team: team.name ? { id: team.id, name: team.name } : { id: team.id } } };
}

/** A team from a TeamInfo, an id, an `[id, name]` pair, or null. */
export function toTeam(team: TeamInfo | string | [string, string?] | null | undefined): TeamInfo | null {
  if (team === null || team === undefined || team === '') return null;
  if (typeof team === 'string') return team.trim() ? { id: team.trim() } : null;
  if (Array.isArray(team)) return team[0] ? { id: String(team[0]), ...(team[1] ? { name: String(team[1]) } : {}) } : null;
  if (typeof team === 'object' && team.id) return { id: String(team.id).trim(), ...(team.name ? { name: String(team.name).trim() } : {}) };
  throw new TypeError('team must be {id, name?}, an id string or [id, name]');
}

/**
 * Run `fn` with an agent team declared: every call and span started inside
 * carries `weft.team.id` / `weft.team.name`, so the server groups the
 * sessions under that team.
 */
export function withTeam<T>(team: TeamInfo | string | [string, string?], fn: () => T): T {
  const t = toTeam(team);
  if (!t) throw new Error('withTeam needs a team id');
  return runWith({ team: t }, fn);
}

/** The team of the innermost `withTeam` block (or team conversation), if any. */
export function getCurrentTeam(): TeamInfo | null {
  return currentContext().team ?? null;
}
