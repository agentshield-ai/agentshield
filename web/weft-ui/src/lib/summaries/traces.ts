// Generated words for the developer view (Traces, a span, span sessions,
// a comparison) (DESIGN.md §7.1 rule 1, §7.2 rule 8). Pure functions of
// what the API returns; table-driven tests in traces.test.ts.
import type { SpanStatus } from '../../api/types'
import type { StatusFilter, TimeRange } from '../filters'
import { fmtCost } from '../format'
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
