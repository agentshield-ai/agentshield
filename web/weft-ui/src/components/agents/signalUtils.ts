// Pure helpers for agent signals and the spans table (unit tested in signals.test.ts).
import type { InsightFilter, SignalFilters, SignalKind, SignalPreset, SignalRatingFilter, SignalResult, SignalSpec } from '../../api/agents'
import type { Query } from '../../api/types'

/** A signal the project has turned on, stored in the `agent-signals` object. */
export interface SignalConfig {
  id: string
  name: string
  kind: SignalKind
  description: string
  preset?: string
  enabled: boolean
  /** Share of new turns scored automatically (0–1, default 1). */
  sample_rate?: number
}

export interface SignalsVal {
  _class_name: 'AgentSignals'
  _bases: string[]
  name: string
  model?: string
  /** Score new turns as they end (needs `model`); default true. */
  auto?: boolean
  signals: SignalConfig[]
}

export const SIGNALS_OBJECT_ID = 'agent-signals'

export function signalsVal(signals: SignalConfig[], model?: string, auto = true): SignalsVal {
  return { _class_name: 'AgentSignals', _bases: ['Object', 'BaseModel'], name: SIGNALS_OBJECT_ID, model, auto, signals }
}

/** Reads the stored config, tolerating older or hand-written shapes. */
export function parseSignalsVal(v: unknown): { signals: SignalConfig[]; model?: string; auto: boolean } {
  const o = (v && typeof v === 'object' ? v : {}) as Record<string, unknown>
  const list = Array.isArray(o.signals) ? o.signals : []
  const signals: SignalConfig[] = []
  for (const x of list) {
    if (!x || typeof x !== 'object') continue
    const s = x as Record<string, unknown>
    const id = typeof s.id === 'string' ? s.id : ''
    const kind = s.kind === 'tag' || s.kind === 'rating' ? s.kind : null
    if (!id || !kind) continue
    signals.push({
      id,
      kind,
      name: typeof s.name === 'string' && s.name ? s.name : id,
      description: typeof s.description === 'string' ? s.description : '',
      preset: typeof s.preset === 'string' ? s.preset : undefined,
      enabled: s.enabled !== false,
      ...(typeof s.sample_rate === 'number' && s.sample_rate >= 0 && s.sample_rate <= 1 ? { sample_rate: s.sample_rate } : {}),
    })
  }
  return { signals, model: typeof o.model === 'string' ? o.model : undefined, auto: o.auto !== false }
}

export function presetConfig(p: SignalPreset): SignalConfig {
  return { id: p.id, name: p.name, kind: p.kind, description: p.description, preset: p.id, enabled: true }
}

/** Signal ids double as tag names: lowercase, ≤ 36 characters. */
export function signalId(name: string): string {
  return name
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '_')
    .replace(/^_+|_+$/g, '')
    .slice(0, 36)
}

export function validateSignal(s: Pick<SignalConfig, 'id' | 'name' | 'description' | 'kind'>, existing: string[] = []): string | null {
  if (!s.name.trim()) return 'Give the signal a name.'
  if (!/^[a-z0-9][a-z0-9_.-]{0,35}$/.test(s.id)) return 'The id must be 1–36 lowercase letters, digits, "_", "-" or ".".'
  if (existing.includes(s.id)) return `A signal with id "${s.id}" already exists.`
  if (!s.description.trim()) return 'Describe what the judge should look for.'
  if (s.description.length > 4000) return 'Keep the description under 4000 characters.'
  return null
}

/** The run spec for a stored signal: presets by id, custom signals in full. */
export function toSpec(s: SignalConfig): SignalSpec {
  if (s.preset && s.preset === s.id) return { preset: s.preset, name: s.name, description: s.description }
  return { id: s.id, name: s.name, kind: s.kind, description: s.description }
}

// ---------------------------------------------------------------- display

export function fmtScore(v: number | null | undefined): string {
  if (v == null || !Number.isFinite(v)) return '—'
  return v.toFixed(2)
}

/** good ≥ 0.7, bad < 0.4 on a 0–1 scale (1 is good, as Weave recommends). */
export function scoreTone(v: number | null | undefined): 'good' | 'mid' | 'bad' | 'none' {
  if (v == null || !Number.isFinite(v)) return 'none'
  if (v >= 0.7) return 'good'
  if (v < 0.4) return 'bad'
  return 'mid'
}

export interface TrendPoint {
  t: string
  /** Mean rating (ratings) or matched count (tags) in the bucket; null when empty. */
  value: number | null
  count: number
}

/** Buckets results by turn start (or creation time) for trend sparklines and the volume timeline. */
export function trendBuckets(results: Pick<SignalResult, 'kind' | 'score' | 'tags' | 'turn_started_at' | 'created_at'>[], start: Date, end: Date, n = 24): TrendPoint[] {
  const t0 = start.getTime()
  const span = Math.max(1, end.getTime() - t0)
  const width = span / n
  const sums = new Array<number>(n).fill(0)
  const counts = new Array<number>(n).fill(0)
  const matched = new Array<number>(n).fill(0)
  let anyRating = false
  for (const r of results) {
    const ts = new Date(r.turn_started_at ?? r.created_at).getTime()
    if (!Number.isFinite(ts) || ts < t0 || ts > end.getTime()) continue
    const i = Math.min(n - 1, Math.floor((ts - t0) / width))
    counts[i]++
    if (r.kind === 'rating' && r.score != null) {
      anyRating = true
      sums[i] += r.score
    } else if (r.tags.length) matched[i]++
  }
  return counts.map((c, i) => ({
    t: new Date(t0 + i * width).toISOString(),
    count: c,
    value: anyRating ? (c ? sums[i] / c : null) : matched[i],
  }))
}

/** Tags and ratings carried by a turn's feedback, with confidences and reasons. */
export function turnSignals(feedback: { scorer_ratings: Record<string, number>; scorer_tags: string[]; scorer_rating_confidences?: Record<string, number>; scorer_rating_reasons?: Record<string, string>; scorer_tag_confidences?: Record<string, number>; scorer_tag_reasons?: Record<string, string> }[] | null | undefined) {
  const ratings: { name: string; value: number; confidence?: number; reason?: string }[] = []
  const tags: { name: string; confidence?: number; reason?: string }[] = []
  const seenTags = new Set<string>()
  for (const f of feedback ?? []) {
    for (const [name, value] of Object.entries(f.scorer_ratings ?? {}))
      ratings.push({ name, value, confidence: f.scorer_rating_confidences?.[name], reason: f.scorer_rating_reasons?.[name] })
    for (const t of f.scorer_tags ?? []) {
      if (seenTags.has(t)) continue
      seenTags.add(t)
      const k = t.toLowerCase()
      tags.push({ name: t, confidence: f.scorer_tag_confidences?.[k], reason: f.scorer_tag_reasons?.[k] })
    }
  }
  return { ratings, tags }
}

// ---------------------------------------------------------------- spans table state

export interface SpanFilterState {
  op: string
  agent: string
  model: string
  tool: string
  status: '' | 'error' | 'ok'
  tags: string[]
  ratings: SignalRatingFilter[]
  sentiment: string[]
}

export const EMPTY_SPAN_FILTERS: SpanFilterState = { op: '', agent: '', model: '', tool: '', status: '', tags: [], ratings: [], sentiment: [] }

const RATING_OPS: SignalRatingFilter['op'][] = ['gte', 'gt', 'lte', 'lt', 'eq']
export const RATING_OP_LABEL: Record<SignalRatingFilter['op'], string> = { gte: '≥', gt: '>', lte: '≤', lt: '<', eq: '=' }

/** "key:op:value" items, comma separated, keys URI-encoded. */
export function encodeRatings(rs: SignalRatingFilter[]): string {
  return rs.map((r) => `${encodeURIComponent(r.scorer_key)}:${r.op}:${r.value}`).join(',')
}

export function decodeRatings(s: string | null | undefined): SignalRatingFilter[] {
  if (!s) return []
  const out: SignalRatingFilter[] = []
  for (const part of s.split(',')) {
    const [k, op, v] = part.split(':')
    const value = Number(v)
    if (!k || !RATING_OPS.includes(op as SignalRatingFilter['op']) || v === undefined || v === '' || !Number.isFinite(value)) continue
    out.push({ scorer_key: decodeURIComponent(k), op: op as SignalRatingFilter['op'], value })
  }
  return out
}

const list = (s: string | null) => (s ?? '').split(',').map((x) => x.trim()).filter(Boolean)

export function readSpanFilters(p: URLSearchParams): SpanFilterState {
  const status = p.get('status')
  return {
    op: p.get('op') ?? '',
    agent: p.get('agent') ?? '',
    model: p.get('model') ?? '',
    tool: p.get('tool') ?? '',
    status: status === 'error' || status === 'ok' ? status : '',
    tags: list(p.get('tags')),
    ratings: decodeRatings(p.get('ratings')),
    sentiment: list(p.get('sentiment')),
  }
}

export function writeSpanFilters(f: SpanFilterState): Record<string, string | null> {
  return {
    op: f.op || null,
    agent: f.agent || null,
    model: f.model || null,
    tool: f.tool || null,
    status: f.status || null,
    tags: f.tags.join(',') || null,
    ratings: encodeRatings(f.ratings) || null,
    sentiment: f.sentiment.join(',') || null,
  }
}

const eq = (field: string, v: string | boolean) => ({ $eq: [{ $getField: field }, { $literal: v }] })

/** The /agents/spans/query filter parts for a filter state. `agent` pins the agent. */
export function spanQueryParts(f: SpanFilterState, agent?: string): { query?: Query; signal_filters?: SignalFilters; insight_filters?: InsightFilter[] } {
  const parts: unknown[] = []
  const a = agent ?? f.agent
  if (a) parts.push(eq('agent_name', a))
  if (f.op) parts.push(eq('operation_name', f.op))
  if (f.model) parts.push(eq('request_model', f.model))
  if (f.tool) parts.push(eq('tool_name', f.tool))
  if (f.status) parts.push(eq('is_error', f.status === 'error'))
  const out: ReturnType<typeof spanQueryParts> = {}
  if (parts.length) out.query = { $expr: parts.length === 1 ? parts[0] : { $and: parts } } as Query
  if (f.tags.length || f.ratings.length) out.signal_filters = { ...(f.tags.length ? { tags: f.tags } : {}), ...(f.ratings.length ? { ratings: f.ratings } : {}) }
  if (f.sentiment.length) out.insight_filters = [{ field: 'intent_sentiment', values: f.sentiment }]
  return out
}

export function activeFilterCount(f: SpanFilterState): number {
  return [f.op, f.agent, f.model, f.tool, f.status].filter(Boolean).length + f.tags.length + f.ratings.length + (f.sentiment.length ? 1 : 0)
}

export const SENTIMENTS = ['frustrated', 'dissatisfied', 'neutral', 'satisfied', 'delighted'] as const

/** Group-by choices for the spans table. Custom attributes are added at runtime. */
export const SPAN_GROUPS: { key: string; label: string }[] = [
  { key: '', label: 'No grouping' },
  { key: 'trace_id', label: 'Trace' },
  { key: 'conversation_id', label: 'Conversation' },
  { key: 'agent_name', label: 'Agent' },
  { key: 'agent_version', label: 'Agent version' },
  { key: 'operation_name', label: 'Operation' },
  { key: 'request_model', label: 'Model' },
  { key: 'provider_name', label: 'Provider' },
  { key: 'tool_name', label: 'Tool' },
  { key: 'status_code', label: 'Status' },
  { key: 'error_type', label: 'Error type' },
]

/** Span fields the server can sort on, by column id. */
export const SPAN_SORT_FIELDS: Record<string, string> = {
  span: 'display_name',
  op: 'operation_name',
  agent: 'agent_name',
  model: 'request_model',
  tool: 'tool_name',
  in: 'input_tokens',
  out: 'output_tokens',
  cost: 'total_cost_usd',
  duration: 'duration_ms',
  status: 'status_code',
  started: 'started_at',
}

/** Group fields the server can sort on, by column id. */
export const GROUP_SORT_FIELDS: Record<string, string> = {
  spans: 'span_count',
  errors: 'error_count',
  in: 'total_input_tokens',
  out: 'total_output_tokens',
  cost: 'total_cost_usd',
  p50: 'latency_ms_p50',
  p95: 'latency_ms_p95',
  last: 'last_seen',
}

/** "field:dir" ↔ SortBy, defaulting to `def`. */
export function parseSort(s: string | null, allowed: Record<string, string>, def: string): { col: string; field: string; direction: 'asc' | 'desc' } {
  const [col, dir] = (s ?? '').split(':')
  const c = col && allowed[col] ? col : def
  return { col: c, field: allowed[c], direction: dir === 'asc' ? 'asc' : 'desc' }
}
