// Generated words for Dashboards and Monitors (DESIGN.md §7.1 rule 1, §7.2
// rule 8): the list sentences, each row's line, and the sentence of one
// dashboard or monitor. Pure functions of the stored objects and the
// scores the API returns; table-driven tests in dashboards.test.ts.
import { joinClauses, plural, relativeTime, scorerWords } from '../summaries'

const cap = (s: string) => (s ? s[0].toUpperCase() + s.slice(1) : s)
const quote = (s: string) => `“${s}”`
/** "“a”", "“a” or “b”", "“a”, “b”, or “c”". */
const orList = (xs: string[]) => (xs.length <= 1 ? xs.map(quote).join('') : `${xs.slice(0, -1).map(quote).join(', ')}${xs.length > 2 ? ',' : ''} or ${quote(xs[xs.length - 1])}`)

/** A dashboard time range in words: "the last 7 days", "all time", "a custom range". */
export function rangeWords(id: string | undefined, labelOf?: (id: string) => string): string {
  if (!id || id === 'all') return 'all time'
  if (id.startsWith('custom:')) return 'a custom range'
  const l = labelOf ? labelOf(id) : ''
  return l ? l.replace(/^Last/, 'the last') : id
}

/** Seconds as words: "every 30 seconds", "every minute", "every 5 minutes". */
export function everyWords(s: number): string {
  if (s % 60 === 0) return s === 60 ? 'every minute' : `every ${s / 60} minutes`
  return `every ${plural(s, 'second')}`
}

/** The Dashboards sentence: "3 dashboards, with 12 charts between them." */
export function dashboardsSentence(xs: { charts: number }[]): string {
  if (!xs.length) return 'No dashboards yet.'
  const charts = xs.reduce((a, x) => a + x.charts, 0)
  if (xs.length === 1) return `One dashboard, with ${plural(charts, 'chart')}.`
  return `${xs.length} dashboards, with ${plural(charts, 'chart')} between them.`
}

/** A dashboard row's line: "4 charts: Spans by status, Error rate, and Latency p50 / p95. Over the last 7 days." */
export function dashboardLine(i: { titles: string[]; range: string }): string {
  const n = i.titles.length
  const over = `Over ${i.range}.`
  if (!n) return `No charts yet. ${over}`
  const shown = i.titles.slice(0, 3)
  const list = n > 3 ? `${shown.join(', ')}, and ${n - 3} more` : joinClauses(shown)
  return `${plural(n, 'chart')}: ${list}. ${over}`
}

/** One dashboard's sentence: "4 charts of top-level spans named “answer” over the last 7 days, refreshed every 30 seconds." */
export function dashboardSentence(i: { charts: number; names: string[]; roots: boolean; search?: string; range: string; refreshSeconds: number }): string {
  if (!i.charts) return `No charts yet. Charts here would cover ${spanScope(i)} over ${i.range}.`
  const refresh = i.refreshSeconds > 0 ? `, refreshed ${everyWords(i.refreshSeconds)}` : ''
  return `${plural(i.charts, 'chart')} of ${spanScope(i)} over ${i.range}${refresh}.`
}

function spanScope(i: { names: string[]; roots: boolean; search?: string }): string {
  const kind = i.roots ? 'top-level spans' : 'spans'
  const named = i.names.length ? ` named ${orList(i.names)}` : ''
  const text = i.search ? ` matching ${quote(i.search)}` : ''
  return named || text ? `${kind}${named}${text}` : i.roots ? 'every top-level span' : 'every span'
}

export type MonitorScope = 'session' | 'agent' | 'handoff' | 'span'

export interface MonitorWordsInput {
  /** Scorer names (refName of each scorer ref). */
  scorers: string[]
  scopes: MonitorScope[]
  names: string[]
  /** 0..1 */
  rate: number
  active: boolean
}

/** What a monitor scores, in words: "goal completion on every finished session", "relevance on 20% of “answer” spans". */
export function monitorTarget(i: MonitorWordsInput): string {
  const scorers = i.scorers.length ? joinClauses(i.scorers.map(scorerWords)) : 'nothing yet'
  const pct = Math.round(i.rate * 100)
  const every = pct >= 100
  const named = i.names.length ? orList(i.names) : ''
  const one = (s: MonitorScope): [string, string] =>
    s === 'session'
      ? ['finished session', 'finished sessions']
      : s === 'agent'
        ? ['agent run', 'agent runs']
        : s === 'handoff'
          ? ['hand-off', 'hand-offs']
          : named
            ? [`${named} span`, `${named} spans`]
            : ['top-level span', 'top-level spans']
  const scopes: MonitorScope[] = i.scopes.length ? i.scopes : ['span']
  const targets = scopes.map((s) => (every ? `every ${one(s)[0]}` : `${pct}% of ${one(s)[1]}`))
  return `${scorers} on ${joinClauses(targets)}`
}

export interface MonitorActivity {
  /** The newest score's time. */
  lastAt?: string | null
  /** Scores in the last 24 hours, and whether the count stopped at the query's cap. */
  today: number
  capped?: boolean
}

/** A monitor row's line: "Scores goal completion on every finished session; last scored 5 min ago, 12 scores in the last 24 hours." */
export function monitorLine(i: MonitorWordsInput, a: MonitorActivity | null, now = Date.now()): string {
  const head = `${i.active ? 'Scores' : 'Paused; scored'} ${monitorTarget(i)}`
  if (!a) return `${head}.`
  if (!a.lastAt) return `${head}; ${i.active ? 'nothing scored yet' : 'it never scored anything'}.`
  const today = a.today ? `, ${a.capped ? 'at least ' : ''}${plural(a.today, 'score')} in the last 24 hours` : ''
  return `${head}; last scored ${relativeTime(a.lastAt, now)}${today}.`
}

/** The Monitors sentence: "3 monitors, 2 active. They recorded 34 scores in the last 24 hours." */
export function monitorsSentence(i: { total: number; active: number; today: number | null; capped?: boolean }): string {
  if (!i.total) return 'No monitors yet.'
  const head =
    i.total === 1 ? `One monitor, ${i.active ? 'active' : 'paused'}.` : `${i.total} monitors, ${i.active === i.total ? 'all active' : i.active === 0 ? 'all paused' : `${i.active} active`}.`
  if (i.today == null) return head
  const who = i.total === 1 ? 'It' : 'They'
  return `${head} ${who} recorded ${i.today ? `${i.capped ? 'at least ' : ''}${plural(i.today, 'score')}` : 'no scores'} in the last 24 hours.`
}

/** One monitor's sentence: "Scores relevance on 20% of “answer” spans. 34 scores so far, the last 5 min ago; relevance averages 0.82." */
export function monitorSentence(i: MonitorWordsInput, a: { total: number; capped?: boolean; lastAt?: string | null; averages: { scorer: string; text: string }[] }, now = Date.now()): string {
  const head = `${i.active ? 'Scores' : 'Paused. It scores'} ${monitorTarget(i)}.`
  if (!a.total) return `${head} ${i.active ? 'Nothing scored yet: matching spans are scored as they finish.' : 'Nothing scored yet; activate it to start.'}`
  const count = `${a.capped ? 'At least ' : ''}${plural(a.total, 'score')} so far${a.lastAt ? `, the last ${relativeTime(a.lastAt, now)}` : ''}`
  const avgs = a.averages.map((x) => `${scorerWords(x.scorer)} ${x.text}`)
  return `${head} ${cap(count)}${avgs.length ? `; ${joinClauses(avgs)}` : ''}.`
}
