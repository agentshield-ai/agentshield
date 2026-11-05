// Generated words for the developer view (Traces, a span, span sessions,
// a comparison) (DESIGN.md §7.1 rule 1, §7.2 rule 8). Pure functions of
// what the API returns; table-driven tests in traces.test.ts.
import type { SpanSession, SpanStatus } from '../../api/types'
import type { StatusFilter, TimeRange } from '../filters'
import { fmtCost, fmtDuration } from '../format'
import { oneLine } from '../timeline'
import { countWord, durationWords, plural, relativeTime } from '../summaries'
import { rangePhrase } from './security'

/** A part of a generated sentence: text, or a clause that links somewhere. */
export type TracePart = string | { text: string; status: StatusFilter }

const num = (n: number) => n.toLocaleString('en-US')
const nplural = (n: number, one: string, many = `${one}s`) => `${num(n)} ${n === 1 ? one : many}`

export interface TracesSentenceInput {
  total: number
  /** Failed runs among them (null: not counted, e.g. a status filter is on). */
  failed: number | null
  /** Traces (top-level spans) or every span. */
  roots: boolean
  time: TimeRange
  filtered: boolean
}

/** "1,240 traces in the last 24 hours. 12 failed." The failed clause lists them. */
export function tracesSentence({ total, failed, roots, time, filtered }: TracesSentenceInput): TracePart[] {
  const what = roots ? 'trace' : 'span'
  const range = rangePhrase(time)
  const where = `${filtered ? ` match${total === 1 ? 'es' : ''} these filters` : ''}${range ? ` ${range}` : ''}`
  if (total === 0) return [`No ${what}s${filtered ? ' match these filters' : ''}${range ? ` ${range}` : ''}.`]
  const parts: TracePart[] = [`${nplural(total, what)}${filtered ? where : range ? ` ${range}` : ' recorded'}.`]
  if (failed != null) {
    if (failed > 0) parts.push(' ', { text: failed === total ? (total === 1 ? 'It failed' : `All ${num(total)} failed`) : `${num(failed)} failed`, status: 'error' }, '.')
    else parts.push(total === 1 ? ' It did not fail.' : ' None failed.')
  }
  return parts
}

/** The glyph and words of a run status (the status chip's values) or a span's own status. */
export function statusGlyph(s: StatusFilter | SpanStatus): { kind: 'ok' | 'failed' | 'warning' | 'running' | 'unknown'; label: string } {
  switch (s) {
    case 'ok':
    case 'success':
      return { kind: 'ok', label: 'Succeeded' }
    case 'error':
      return { kind: 'failed', label: 'Failed' }
    case 'failed_step':
      return { kind: 'warning', label: 'Went on after a failed step' }
    case 'descendant_error':
      return { kind: 'warning', label: 'Succeeded, a span under it failed' }
    case 'running':
      return { kind: 'running', label: 'Running' }
    default:
      return { kind: 'unknown', label: 'No agent data' }
  }
}

export interface SpanSentenceInput {
  status: SpanStatus
  latencyMs: number | null
  error?: string | null
  /** Spans under it (status_counts), and how many of them failed. */
  below: number
  belowFailed: number
  tokens: number
  cost: number | null
}

const firstLine = (s: string) => {
  const l = s.split('\n')[0].trim()
  return l.length > 140 ? l.slice(0, 139) + '…' : l
}

/** A span's outcome: "Failed after 150 milliseconds with “KeyError: x”. 2 spans ran under it. Used 15 tokens ($0.0001)." */
export function spanSentence(i: SpanSentenceInput): string {
  const took = durationWords(i.latencyMs)
  const xs: string[] = []
  if (i.status === 'running') xs.push('Still running.')
  else if (i.status === 'error') xs.push(`Failed${took ? ` after ${took}` : ''}${i.error ? ` with “${firstLine(i.error)}”` : ''}.`)
  else if (i.status === 'descendant_error' || i.belowFailed > 0) {
    const f = Math.max(1, i.belowFailed)
    const of = i.below > f ? `${f === 1 ? 'one' : f} of the ${num(i.below)} spans` : countWord(f, 'span')
    xs.push(`Succeeded${took ? ` in ${took}` : ''}, though ${of} under it failed.`)
  } else {
    xs.push(`Succeeded${took ? ` in ${took}` : ''}.`)
    if (i.below > 0) xs.push(`${nplural(i.below, 'span')} ran under it.`)
  }
  if (i.status === 'error' && i.below > 0) xs.push(`${nplural(i.below, 'span')} ran under it${i.belowFailed ? `, ${i.belowFailed} of them failed` : ''}.`)
  if (i.tokens > 0) xs.push(`Used ${nplural(i.tokens, 'token')}${i.cost ? ` (${fmtCost(i.cost)})` : ''}.`)
  else if (i.cost) xs.push(`Cost ${fmtCost(i.cost)}.`)
  return xs.join(' ')
}

/** The span sessions list: "38 sessions have spans with a session id. The latest was updated 5 min ago." */
export function spanSessionsSentence(i: { shown: number; more: boolean; offset: number; latest?: string | null; now?: number }): string {
  if (!i.shown && !i.offset) return 'No spans carry a session id yet.'
  const n = i.offset + i.shown
  const head = i.more ? `More than ${num(n)} sessions have spans with a session id.` : `${nplural(n, 'session')} ${n === 1 ? 'has' : 'have'} spans with a session id.`
  return i.latest ? `${head} The latest was updated ${relativeTime(i.latest, i.now)}.` : head
}

/** The page that groups spans by their session id, as people see it named (nav, back links). */
export const SPAN_SESSIONS_TITLE = 'Sessions by id'

const NAME_KEYS = ['question', 'query', 'input', 'prompt', 'message', 'text', 'task', 'request', 'content']

/** A message's text: a string, or the text parts of a content list. */
function messageText(m: unknown): string {
  if (!m || typeof m !== 'object') return ''
  const c = (m as { content?: unknown }).content
  if (typeof c === 'string') return c
  if (Array.isArray(c)) return c.map((p) => (typeof p === 'string' ? p : p && typeof p === 'object' && typeof (p as { text?: unknown }).text === 'string' ? (p as { text: string }).text : '')).join(' ')
  return ''
}

/**
 * A turn named by what was asked: the question-like input, else the last
 * user message, else the first text input. Empty when nothing reads as words.
 */
export function turnName(inputs: Record<string, unknown> | null | undefined, n = 90): string {
  const xs = Object.entries(inputs ?? {}).filter(([k]) => k !== 'self')
  const str = (v: unknown) => (typeof v === 'string' && v.trim() ? oneLine(v, n) : '')
  for (const k of NAME_KEYS) {
    const hit = xs.find(([key]) => key.toLowerCase() === k)
    if (hit && str(hit[1])) return str(hit[1])
  }
  for (const [, v] of xs) {
    if (Array.isArray(v) && v.some((m) => m && typeof m === 'object' && 'role' in m)) {
      const user = [...v].reverse().find((m) => (m as { role?: string }).role === 'user')
      const t = str(messageText(user))
      if (t) return t
    }
  }
  for (const [, v] of xs) if (str(v)) return str(v)
  return ''
}

/** A session-by-id row: "4 turns, 12 spans. Turns take about 1.4 s, up to 3.7 s. $0.0034." (median and 99th percentile) */
export function spanSessionLine(t: Pick<SpanSession, 'turn_count' | 'span_count' | 'p50_turn_duration_ms' | 'p99_turn_duration_ms' | 'cost_usd'>): string {
  const xs = [`${nplural(t.turn_count, 'turn')}, ${nplural(t.span_count, 'span')}.`]
  if (t.p50_turn_duration_ms != null) {
    const p50 = fmtDuration(t.p50_turn_duration_ms)
    const p99 = t.p99_turn_duration_ms != null ? fmtDuration(t.p99_turn_duration_ms) : ''
    xs.push(t.turn_count > 1 && p99 && p99 !== p50 ? `Turns take about ${p50}, up to ${p99}.` : `${t.turn_count === 1 ? 'The turn took' : 'Turns take about'} ${p50}.`)
  }
  if (t.cost_usd) xs.push(`${fmtCost(t.cost_usd)}.`)
  return xs.join(' ')
}

/** One span session: "4 turns and 41 spans over 12 minutes, costing $0.02. One turn failed." */
export function spanSessionSentence(i: { turns: number; spans: number; startedAt?: string | null; lastAt?: string | null; cost?: number | null; failedTurns: number }): string {
  const ms = i.startedAt && i.lastAt ? new Date(i.lastAt).getTime() - new Date(i.startedAt).getTime() : null
  const over = ms != null && ms > 0 ? ` over ${durationWords(ms)}` : ''
  const cost = i.cost ? `, costing ${fmtCost(i.cost)}` : ''
  const head = `${nplural(i.turns, 'turn')} and ${nplural(i.spans, 'span')}${over}${cost}.`
  if (!i.turns) return head
  if (!i.failedTurns) return `${head} ${i.turns === 1 ? 'It' : 'None'} ${i.turns === 1 ? 'did not fail' : 'failed'}.`
  return `${head} ${i.failedTurns === i.turns && i.turns > 1 ? `All ${i.turns}` : i.failedTurns === 1 ? 'One turn' : `${i.failedTurns} turns`} failed.`
}

/** A comparison: "5 of 14 fields differ between these 2 spans." */
export function compareSentence(i: { spans: number; fields: number; differ: number; missing: number; baseline?: string | null; diffMode: boolean }): string {
  if (i.spans < 2) return i.spans === 1 ? 'Only one of these spans was found; pick another on Traces to compare.' : 'None of these spans were found.'
  const head = i.differ === 0 ? `All ${i.fields} fields are the same in these ${i.spans} spans.` : `${i.differ} of ${plural(i.fields, 'field')} differ between these ${i.spans} spans.`
  const base = i.diffMode && i.baseline ? ` Changes are shown against ${i.baseline}.` : ''
  const miss = i.missing ? ` ${i.missing === 1 ? 'One span was' : `${i.missing} spans were`} not found.` : ''
  return head + base + miss
}
