// The one filter model (DESIGN.md §7.1 rule 5): the same chips and syntax on
// Sessions, Agents, Teams, Security and Traces. Filters live in the URL; the
// filter box also accepts `key:value` tokens (e.g. `agent:coder has:block`).

import { fmtCost } from './format'

export type TimeRange = '1h' | '24h' | '7d' | '30d' | 'all' | 'custom'
export type HasFilter = 'block' | 'approval' | 'flagged' | 'any'
export type StatusFilter = 'ok' | 'error' | 'failed_step' | 'running'

/** A session asking for approval this many times is approval-heavy (approval fatigue). */
export const APPROVAL_HEAVY = 3

export interface FilterState {
  agent?: string
  team?: string
  status?: StatusFilter
  /** Undefined means "the page default" (which may auto-widen). */
  time?: TimeRange
  from?: string
  to?: string
  has?: HasFilter
  /** Score below a value; `scorer` narrows it to one scorer. */
  scoreBelow?: { scorer?: string; value: number }
  model?: string
  tool?: string
  q?: string
  costMin?: number
}

export type FilterKey = 'agent' | 'team' | 'status' | 'time' | 'has' | 'score' | 'model' | 'tool' | 'q' | 'cost'

/** URL parameter names; everything else in the URL is left alone. */
export const FILTER_PARAMS = ['agent', 'team', 'status', 'time', 'from', 'to', 'has', 'score_below', 'model', 'tool', 'q', 'cost_min'] as const

export const TIME_OPTIONS: { id: TimeRange; label: string }[] = [
  { id: '1h', label: 'Last hour' },
  { id: '24h', label: 'Last 24 hours' },
  { id: '7d', label: 'Last 7 days' },
  { id: '30d', label: 'Last 30 days' },
  { id: 'all', label: 'All time' },
]
export const TIME_LABEL: Record<TimeRange, string> = {
  '1h': 'Last hour',
  '24h': 'Last 24 hours',
  '7d': 'Last 7 days',
  '30d': 'Last 30 days',
  all: 'All time',
  custom: 'Custom range',
}
const TIME_MS: Partial<Record<TimeRange, number>> = { '1h': 3600e3, '24h': 86400e3, '7d': 7 * 86400e3, '30d': 30 * 86400e3 }

/** The ranges tried, in order, when the default range is empty. */
export const WIDEN_SEQUENCE: TimeRange[] = ['24h', '7d', '30d', 'all']

export const STATUS_LABEL: Record<StatusFilter, string> = {
  ok: 'Succeeded',
  error: 'Failed',
  failed_step: 'Went on after a failed step',
  running: 'Running',
}
export const HAS_LABEL: Record<HasFilter, string> = {
  block: 'Has a block',
  approval: 'Needs approval',
  flagged: `Blocked or ${APPROVAL_HEAVY}+ approvals`,
  any: 'Any detection',
}

export const FILTER_KEY_LABEL: Record<FilterKey, string> = {
  agent: 'Agent',
  team: 'Team',
  status: 'Status',
  time: 'Time',
  has: 'Security',
  score: 'Score below',
  model: 'Model',
  tool: 'Tool',
  q: 'Text',
  cost: 'Cost at least',
}

const isTime = (v: string | null): v is TimeRange => !!v && ['1h', '24h', '7d', '30d', 'all', 'custom'].includes(v)
const isStatus = (v: string | null): v is StatusFilter => v === 'ok' || v === 'error' || v === 'failed_step' || v === 'running'
const isHas = (v: string | null): v is HasFilter => v === 'block' || v === 'approval' || v === 'flagged' || v === 'any'

export function parseScoreBelow(raw: string | null | undefined): FilterState['scoreBelow'] {
  if (!raw) return undefined
  const i = raw.lastIndexOf(':')
  const scorer = i > 0 ? raw.slice(0, i).trim() : undefined
  const value = Number(i > 0 ? raw.slice(i + 1) : raw)
  if (!Number.isFinite(value)) return undefined
  return scorer ? { scorer, value } : { value }
}

export function parseFilters(p: URLSearchParams): FilterState {
  const s: FilterState = {}
  const str = (k: string) => {
    const v = p.get(k)
    return v && v.trim() ? v : undefined
  }
  s.agent = str('agent')
  s.team = str('team')
  const st = p.get('status')
  if (isStatus(st)) s.status = st
  const t = p.get('time')
  if (isTime(t)) s.time = t
  s.from = str('from')
  s.to = str('to')
  if ((s.from || s.to) && !s.time) s.time = 'custom'
  const h = p.get('has')
  if (isHas(h)) s.has = h
  s.scoreBelow = parseScoreBelow(p.get('score_below'))
  s.model = str('model')
  s.tool = str('tool')
  s.q = str('q')
  const c = Number(p.get('cost_min'))
  if (p.get('cost_min') && Number.isFinite(c)) s.costMin = c
  for (const k of Object.keys(s) as (keyof FilterState)[]) if (s[k] === undefined) delete s[k]
  return s
}

/** Writes the filter state into a copy of `base`, keeping its other parameters. */
export function writeFilters(state: FilterState, base: URLSearchParams = new URLSearchParams()): URLSearchParams {
  const out = new URLSearchParams(base)
  for (const k of FILTER_PARAMS) out.delete(k)
  const set = (k: string, v: string | number | undefined) => {
    if (v !== undefined && v !== '') out.set(k, String(v))
  }
  set('agent', state.agent)
  set('team', state.team)
  set('status', state.status)
  if (state.time && state.time !== 'custom') set('time', state.time)
  if (state.time === 'custom' || (!state.time && (state.from || state.to))) {
    set('from', state.from)
    set('to', state.to)
  }
  set('has', state.has)
  if (state.scoreBelow) set('score_below', state.scoreBelow.scorer ? `${state.scoreBelow.scorer}:${state.scoreBelow.value}` : state.scoreBelow.value)
  set('model', state.model)
  set('tool', state.tool)
  set('q', state.q)
  set('cost_min', state.costMin)
  return out
}

export function filterQueryString(state: FilterState): string {
  return writeFilters(state).toString()
}

/** "Now" rounded down to the minute, so relative ranges give stable query keys between renders. */
export function stableNow(now = Date.now()): number {
  return Math.floor(now / 60_000) * 60_000
}

/** Resolves a time range to [from, to) ISO bounds; `all` has none. */
export function timeWindow(state: FilterState, fallback: TimeRange, now = stableNow()): { from?: string; to?: string } {
  const t = state.time ?? fallback
  if (t === 'custom') return { from: state.from, to: state.to }
  const ms = TIME_MS[t]
  return ms ? { from: new Date(now - ms).toISOString() } : {}
}

export interface Chip {
  key: FilterKey
  label: string
  value: string
}

export function chipsOf(state: FilterState, opts: { defaultTime?: TimeRange } = {}): Chip[] {
  const out: Chip[] = []
  if (state.agent) out.push({ key: 'agent', label: 'Agent', value: state.agent })
  if (state.team) out.push({ key: 'team', label: 'Team', value: state.team })
  if (state.status) out.push({ key: 'status', label: 'Status', value: STATUS_LABEL[state.status] })
  if (state.time && state.time !== opts.defaultTime)
    out.push({
      key: 'time',
      label: 'Time',
      value:
        state.time === 'custom'
          ? `${state.from ? state.from.slice(0, 16).replace('T', ' ') : '…'} → ${state.to ? state.to.slice(0, 16).replace('T', ' ') : 'now'}`
          : TIME_LABEL[state.time],
    })
  if (state.has) out.push({ key: 'has', label: 'Security', value: HAS_LABEL[state.has] })
  if (state.scoreBelow) out.push({ key: 'score', label: 'Score below', value: `${state.scoreBelow.value}${state.scoreBelow.scorer ? ` (${state.scoreBelow.scorer})` : ''}` })
  if (state.model) out.push({ key: 'model', label: 'Model', value: state.model })
  if (state.tool) out.push({ key: 'tool', label: 'Tool', value: state.tool })
  if (state.costMin != null) out.push({ key: 'cost', label: 'Cost at least', value: fmtCost(state.costMin) })
  if (state.q) out.push({ key: 'q', label: 'Text', value: `“${state.q}”` })
  return out
}

export function removeKey(state: FilterState, key: FilterKey): FilterState {
  const s = { ...state }
  switch (key) {
    case 'time':
      delete s.time
      delete s.from
      delete s.to
      break
    case 'score':
      delete s.scoreBelow
      break
    case 'cost':
      delete s.costMin
      break
    default:
      delete s[key]
  }
  return s
}

export function isEmptyFilter(state: FilterState, defaultTime?: TimeRange): boolean {
  return chipsOf(state, { defaultTime }).length === 0
}

/** Token syntax accepted by the filter box: `agent:x team:y status:failed has:block score<0.5 model:m tool:t time:7d cost>0.1`. */
const TOKEN = /(?:^|\s)(agent|team|status|has|model|tool|time|score|cost)\s*(:|<|>|=)\s*("[^"]*"|\S+)/gi

export function parseFilterText(text: string, base: FilterState = {}): FilterState {
  const s: FilterState = { ...base }
  const rest = text.replace(TOKEN, (_m, rawKey: string, _op: string, rawVal: string) => {
    const key = rawKey.toLowerCase()
    const val = rawVal.replace(/^"|"$/g, '')
    const low = val.toLowerCase()
    switch (key) {
      case 'agent':
        s.agent = val
        break
      case 'team':
        s.team = val
        break
      case 'model':
        s.model = val
        break
      case 'tool':
        s.tool = val
        break
      case 'status':
        if (['error', 'failed', 'fail', 'errors'].includes(low)) s.status = 'error'
        else if (['ok', 'success', 'succeeded', 'done'].includes(low)) s.status = 'ok'
        else if (low === 'running') s.status = 'running'
        else if (['failed_step', 'step_failed', 'failed-step', 'step'].includes(low)) s.status = 'failed_step'
        else return ` ${rawKey}:${rawVal}`
        break
      case 'has':
        if (low === 'block' || low === 'blocked') s.has = 'block'
        else if (low.startsWith('approval') || low === 'require_approval') s.has = 'approval'
        else if (low === 'flagged' || low === 'attention') s.has = 'flagged'
        else if (low === 'any' || low === 'detection') s.has = 'any'
        else return ` ${rawKey}:${rawVal}`
        break
      case 'time':
        if (isTime(low) && low !== 'custom') {
          s.time = low
          delete s.from
          delete s.to
        } else return ` ${rawKey}:${rawVal}`
        break
      case 'score': {
        const sb = parseScoreBelow(val)
        if (!sb) return ` ${rawKey}:${rawVal}`
        s.scoreBelow = sb
        break
      }
      case 'cost': {
        const n = Number(val.replace(/^\$/, ''))
        if (!Number.isFinite(n)) return ` ${rawKey}:${rawVal}`
        s.costMin = n
        break
      }
    }
    return ' '
  })
  const q = rest.replace(/\s+/g, ' ').trim()
  if (q) s.q = q
  else delete s.q
  return s
}

// ---------------------------------------------------------------- per page

/** /api/v1/sessions parameters for a filter state. Score below without a scorer,
 * cost, "went on after a failed step" and "blocked or approval-heavy" are
 * applied client-side (sessionMatchesClientFilters); the server narrows them. */
export function sessionsParams(state: FilterState, fallback: TimeRange, now = stableNow()): Record<string, string> {
  const p: Record<string, string> = {}
  if (state.agent) p.agent = state.agent
  if (state.team) p.team_id = state.team
  if (state.status && state.status !== 'failed_step') p.status = state.status
  if (state.has) p.has_detection = state.has === 'approval' ? 'require_approval' : state.has === 'flagged' ? 'any' : state.has
  if (state.scoreBelow?.scorer) p.max_score = `${state.scoreBelow.scorer}:${state.scoreBelow.value}`
  if (state.model) p.model = state.model
  if (state.tool) p.tool = state.tool
  if (state.q) p.q = state.q
  const w = timeWindow(state, fallback, now)
  if (w.from) p.from = w.from
  if (w.to) p.to = w.to
  return p
}

interface Scored {
  scores?: Record<string, { mean: number; count: number }> | null
}

/** True when any (or the named) scorer's mean is below the value. */
export function scoreBelowMatches(x: Scored, sb: FilterState['scoreBelow']): boolean {
  if (!sb) return true
  const entries = Object.entries(x.scores ?? {})
  return entries.some(([name, v]) => (!sb.scorer || name === sb.scorer) && v.mean < sb.value)
}

type Counts = Partial<Record<string, number>> | null | undefined

/** Blocked at least once, or asked for approval APPROVAL_HEAVY or more times. */
export function isFlagged(counts: Counts): boolean {
  return (counts?.block ?? 0) > 0 || (counts?.require_approval ?? 0) >= APPROVAL_HEAVY
}

/** A tool or model call failed, but the session itself did not. */
export function wentOnAfterFailedStep(s: { status: string; error_count: number }): boolean {
  return s.status !== 'error' && (s.error_count ?? 0) > 0
}

type ClientSession = Scored & { cost_usd: number | null; status?: string; error_count?: number; detection_count?: Counts }

export function sessionMatchesClientFilters(s: ClientSession, state: FilterState): boolean {
  if (state.scoreBelow && !scoreBelowMatches(s, state.scoreBelow)) return false
  if (state.costMin != null && (s.cost_usd ?? 0) < state.costMin) return false
  if (state.status === 'failed_step' && !wentOnAfterFailedStep({ status: s.status ?? '', error_count: s.error_count ?? 0 })) return false
  if (state.has === 'flagged' && !isFlagged(s.detection_count)) return false
  return true
}

/** True when the sessions list must be filtered in the browser (so it is fetched whole, up to 1000). */
export function needsClientFilter(state: FilterState): boolean {
  return (!!state.scoreBelow && !state.scoreBelow.scorer) || state.costMin != null || state.status === 'failed_step' || state.has === 'flagged'
}

// ---------------------------------------------------------------- one chip set, five pages

export type FilterPage = 'sessions' | 'agents' | 'teams' | 'security' | 'traces'

/** Every page offers the same chips in this order. */
export const FILTER_KEYS: FilterKey[] = ['agent', 'team', 'status', 'time', 'has', 'score', 'model', 'tool', 'cost', 'q']

/**
 * How each page applies each chip. `page`: on the page's own rows. `sessions`:
 * through the sessions that match it (an agent, team, detection or trace is
 * kept when it belongs to a matching session). No chip is ever ignored.
 */
export type FilterSupport = 'page' | 'sessions'

export const FILTER_SUPPORT: Record<FilterPage, Record<FilterKey, FilterSupport>> = {
  sessions: { agent: 'page', team: 'page', status: 'page', time: 'page', has: 'page', score: 'page', model: 'page', tool: 'page', cost: 'page', q: 'page' },
  // Agents carry their own errors, verdicts, scores, models and tools.
  agents: { agent: 'page', team: 'page', status: 'page', time: 'page', has: 'page', score: 'page', model: 'page', tool: 'page', cost: 'sessions', q: 'page' },
  // A team matches when one of its sessions does.
  teams: { agent: 'page', team: 'page', status: 'sessions', time: 'page', has: 'sessions', score: 'sessions', model: 'sessions', tool: 'sessions', cost: 'sessions', q: 'page' },
  // Detections carry agent, tool, action and time; the rest belongs to their session.
  security: { agent: 'page', team: 'sessions', status: 'sessions', time: 'page', has: 'page', score: 'sessions', model: 'sessions', tool: 'page', cost: 'sessions', q: 'page' },
  // Spans carry agent, model, tool, status and time; a trace matches team,
  // security, score and cost through its session.
  traces: { agent: 'page', team: 'sessions', status: 'page', time: 'page', has: 'sessions', score: 'sessions', model: 'page', tool: 'page', cost: 'sessions', q: 'page' },
}

/** Values a page cannot read off its own rows, even for a chip it applies itself. */
function pageNeedsSessions(page: FilterPage, key: FilterKey, s: FilterState): boolean {
  if (key === 'status' && s.status === 'failed_step') return page !== 'sessions'
  // An agent is not "running"; a session it takes part in is.
  if (key === 'status' && s.status === 'running') return page === 'agents'
  if (key === 'has' && s.has === 'flagged') return page === 'security' || page === 'traces'
  return false
}

/** The part of `s` a page applies through matching sessions (time excluded: the page's range is used). */
export function sessionScope(page: FilterPage, s: FilterState): FilterState {
  const out: FilterState = {}
  for (const c of chipsOf(s)) {
    if (c.key === 'time' || c.key === 'q') continue
    if (FILTER_SUPPORT[page][c.key] !== 'sessions' && !pageNeedsSessions(page, c.key, s)) continue
    switch (c.key) {
      case 'score':
        out.scoreBelow = s.scoreBelow
        break
      case 'cost':
        out.costMin = s.costMin
        break
      default:
        ;(out as Record<string, unknown>)[c.key] = s[c.key as keyof FilterState]
    }
  }
  return out
}

const ROWS: Record<FilterPage, string> = {
  sessions: 'sessions',
  agents: 'agents that took part in',
  teams: 'teams with',
  security: 'detections in',
  traces: 'traces that belong to',
}

/** One line saying how chips applied through sessions work on this page, or null. */
export function sessionScopeNote(page: FilterPage, s: FilterState): string | null {
  const keys = Object.keys(sessionScope(page, s))
  if (!keys.length) return null
  const labels = chipsOf(s)
    .filter((c) => keys.includes(c.key === 'score' ? 'scoreBelow' : c.key === 'cost' ? 'costMin' : c.key))
    .map((c) => c.label)
  return `${labels.join(', ')}: showing ${ROWS[page]} a session that matches ${labels.length === 1 ? 'this filter' : 'these filters'}.`
}
