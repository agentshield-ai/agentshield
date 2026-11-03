// The one filter model (DESIGN.md §7.1 rule 5): the same chips and syntax on
// Sessions, Agents, Teams, Security and Traces. Filters live in the URL; the
// filter box also accepts `key:value` tokens (e.g. `agent:coder has:block`).

export type TimeRange = '1h' | '24h' | '7d' | '30d' | 'all' | 'custom'
export type HasFilter = 'block' | 'approval' | 'any'
export type StatusFilter = 'ok' | 'error' | 'running'

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

export const STATUS_LABEL: Record<StatusFilter, string> = { ok: 'Succeeded', error: 'Failed', running: 'Running' }
export const HAS_LABEL: Record<HasFilter, string> = { block: 'Has a block', approval: 'Needs approval', any: 'Any detection' }

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
const isStatus = (v: string | null): v is StatusFilter => v === 'ok' || v === 'error' || v === 'running'
const isHas = (v: string | null): v is HasFilter => v === 'block' || v === 'approval' || v === 'any'

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
      value: state.time === 'custom' ? `${state.from ? state.from.slice(0, 16).replace('T', ' ') : '…'} → ${state.to ? state.to.slice(0, 16).replace('T', ' ') : 'now'}` : TIME_LABEL[state.time],
    })
  if (state.has) out.push({ key: 'has', label: 'Security', value: HAS_LABEL[state.has] })
  if (state.scoreBelow)
    out.push({ key: 'score', label: 'Score below', value: `${state.scoreBelow.value}${state.scoreBelow.scorer ? ` (${state.scoreBelow.scorer})` : ''}` })
  if (state.model) out.push({ key: 'model', label: 'Model', value: state.model })
  if (state.tool) out.push({ key: 'tool', label: 'Tool', value: state.tool })
  if (state.costMin != null) out.push({ key: 'cost', label: 'Cost at least', value: `$${state.costMin}` })
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
        else return ` ${rawKey}:${rawVal}`
        break
      case 'has':
        if (low === 'block' || low === 'blocked') s.has = 'block'
        else if (low.startsWith('approval') || low === 'require_approval') s.has = 'approval'
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

/** /api/v1/sessions parameters for a filter state. Score-below without a scorer and cost are applied client-side. */
export function sessionsParams(state: FilterState, fallback: TimeRange, now = stableNow()): Record<string, string> {
  const p: Record<string, string> = {}
  if (state.agent) p.agent = state.agent
  if (state.team) p.team_id = state.team
  if (state.status) p.status = state.status
  if (state.has) p.has_detection = state.has === 'approval' ? 'require_approval' : state.has
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

export function sessionMatchesClientFilters(s: Scored & { cost_usd: number | null }, state: FilterState): boolean {
  if (state.scoreBelow && !scoreBelowMatches(s, state.scoreBelow)) return false
  if (state.costMin != null && (s.cost_usd ?? 0) < state.costMin) return false
  return true
}

/** Which chips each page offers; the syntax is the same everywhere. */
export const PAGE_FILTERS: Record<'sessions' | 'agents' | 'teams' | 'security' | 'traces', FilterKey[]> = {
  sessions: ['agent', 'team', 'status', 'time', 'has', 'score', 'model', 'tool', 'cost', 'q'],
  agents: ['agent', 'team', 'status', 'time', 'has', 'score', 'model', 'tool', 'q'],
  teams: ['agent', 'team', 'status', 'time', 'has', 'q'],
  security: ['agent', 'time', 'has', 'tool', 'q'],
  traces: ['agent', 'status', 'time', 'model', 'tool', 'q'],
}
