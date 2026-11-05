// Pure helpers for dashboards and custom charts (unit tested in dashboards.test.ts).
import type { Span, SpansFilter, Operation, Query } from '../../api/types'
import { spanDisplayName } from '../../api/refs'
import { callCost, callLatencyMs, callStatus, callTokens, fmtCost, fmtDuration, getPath } from '../../lib/format'
import type { ChartType, DashboardPanel, DashboardVal, MetricAgg, MetricSpec, MetricsReq, MetricsRes } from '../../api/dashboards'

export const TIME_RANGES: { id: string; label: string; seconds: number | null }[] = [
  { id: '15m', label: 'Last 15 minutes', seconds: 15 * 60 },
  { id: '1h', label: 'Last hour', seconds: 3600 },
  { id: '6h', label: 'Last 6 hours', seconds: 6 * 3600 },
  { id: '24h', label: 'Last 24 hours', seconds: 86400 },
  { id: '7d', label: 'Last 7 days', seconds: 7 * 86400 },
  { id: '30d', label: 'Last 30 days', seconds: 30 * 86400 },
  { id: '90d', label: 'Last 90 days', seconds: 90 * 86400 },
  { id: 'all', label: 'All time', seconds: null },
]

/** A custom range is stored as "custom:<start ISO>/<end ISO>". */
export function customRange(start: string, end: string): string {
  return `custom:${start}/${end}`
}

export function timeRangeLabel(id: string): string {
  const preset = TIME_RANGES.find((r) => r.id === id)
  if (preset) return preset.label
  const b = timeRangeBounds(id)
  if (b.start && b.end) return `${fmtShort(b.start)} – ${fmtShort(b.end)}`
  return 'All time'
}

function fmtShort(iso: string) {
  const d = new Date(iso)
  return d.toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' })
}

/** Start/end for a time range id, relative to `now`. */
export function timeRangeBounds(id: string | undefined, now: Date = new Date()): { start?: string; end?: string } {
  if (!id || id === 'all') return {}
  if (id.startsWith('custom:')) {
    const [s, e] = id.slice(7).split('/')
    const ok = (x?: string) => !!x && !Number.isNaN(new Date(x).getTime())
    return { start: ok(s) ? new Date(s).toISOString() : undefined, end: ok(e) ? new Date(e).toISOString() : undefined }
  }
  const preset = TIME_RANGES.find((r) => r.id === id)
  if (!preset?.seconds) return {}
  return { start: new Date(now.getTime() - preset.seconds * 1000).toISOString(), end: now.toISOString() }
}

/** Merge two structured filters; list fields are intersected only when both set them (the panel wins otherwise). */
export function mergeFilters(a?: SpansFilter, b?: SpansFilter): SpansFilter | undefined {
  if (!a) return b
  if (!b) return a
  const out: SpansFilter = { ...a, ...b }
  for (const k of Object.keys(a) as (keyof SpansFilter)[]) {
    const av = a[k]
    const bv = b[k]
    if (Array.isArray(av) && Array.isArray(bv) && av.length && bv.length) {
      const both = (av as string[]).filter((x) => (bv as string[]).includes(x))
      ;(out as Record<string, unknown>)[k] = both.length ? both : ['\u0000none']
    }
  }
  return out
}

function exprOf(q?: Query): Operation | null {
  if (!q) return null
  const e = (q as { $expr?: Operation }).$expr
  if (e && Object.keys(e).length) return e
  return null
}

export function mergeQueries(...qs: (Query | undefined)[]): Query | undefined {
  const ops = qs.map(exprOf).filter((x): x is Operation => !!x)
  if (!ops.length) return undefined
  return { $expr: ops.length === 1 ? ops[0] : { $and: ops } }
}

export function maxGroupsFor(chart: ChartType): number {
  return chart === 'table' ? 25 : chart === 'stat' ? 1 : 6
}

/** The /api/v1/spans/metrics request for one panel of a dashboard. */
export function panelRequest(projectId: string, panel: DashboardPanel, dash?: Pick<DashboardVal, 'filter' | 'query' | 'time_range'>, now: Date = new Date()): MetricsReq {
  const range = timeRangeBounds(panel.time_range || dash?.time_range, now)
  const req: MetricsReq = {
    project_id: projectId,
    metrics: panel.metrics.length ? panel.metrics : [{ agg: 'count' }],
    filter: mergeFilters(dash?.filter, panel.filter),
    query: mergeQueries(dash?.query, panel.query),
    search: panel.search || undefined,
    group_by: panel.chart === 'stat' ? undefined : panel.group_by || undefined,
    max_groups: maxGroupsFor(panel.chart),
    ...range,
  }
  if (panel.bucket_seconds) req.bucket_seconds = panel.bucket_seconds
  return req
}

export function metricName(m: MetricSpec): string {
  if (m.name) return m.name
  return m.field ? `${m.agg}(${m.field})` : m.agg
}

const AGG_LABEL: Record<MetricAgg, string> = {
  count: 'Count',
  sum: 'Sum',
  avg: 'Avg',
  min: 'Min',
  max: 'Max',
  p50: 'p50',
  p90: 'p90',
  p95: 'p95',
  p99: 'p99',
  error_rate: 'Error rate',
}

export const FIELD_PRESETS: { field: string; label: string }[] = [
  { field: 'latency_ms', label: 'Latency (ms)' },
  { field: 'total_tokens', label: 'Total tokens' },
  { field: 'prompt_tokens', label: 'Prompt tokens' },
  { field: 'completion_tokens', label: 'Completion tokens' },
  { field: 'cost', label: 'Cost (USD)' },
]

export const GROUP_PRESETS: { id: string; label: string }[] = [
  { id: '', label: 'No grouping' },
  { id: 'name', label: 'Name' },
  { id: 'model', label: 'Model' },
  { id: 'status', label: 'Status' },
  { id: 'session', label: 'Session' },
  { id: 'user', label: 'User' },
]

export function fieldLabel(field?: string): string {
  if (!field) return 'spans'
  return FIELD_PRESETS.find((f) => f.field === field)?.label.toLowerCase() ?? field
}

/** Human label for a metric: "p95 latency (ms)", "Count", "Error rate". */
export function metricLabel(m: MetricSpec): string {
  if (m.name && m.name !== metricName({ ...m, name: undefined })) return m.name
  if (m.agg === 'count' && !m.field) return 'Spans'
  if (m.agg === 'error_rate') return 'Error rate'
  return `${AGG_LABEL[m.agg]} ${fieldLabel(m.field)}`
}

export type ValueKind = 'duration' | 'cost' | 'percent' | 'number'

export function valueKind(m: MetricSpec): ValueKind {
  if (m.agg === 'error_rate') return 'percent'
  if (m.agg === 'count') return 'number'
  if (m.field === 'latency_ms') return 'duration'
  if (m.field === 'cost' || m.field === 'cost_usd') return 'cost'
  return 'number'
}

export function fmtValue(v: number | null | undefined, kind: ValueKind): string {
  if (v == null || Number.isNaN(v)) return '—'
  if (v === 0) return kind === 'percent' ? '0%' : kind === 'cost' ? '$0' : '0'
  switch (kind) {
    case 'percent':
      return `${(v * 100).toFixed(v < 0.1 ? 2 : 1)}%`
    case 'duration':
      return fmtDuration(v)
    case 'cost':
      return fmtCost(v)
    default:
      if (Number.isInteger(v)) return v.toLocaleString()
      if (Math.abs(v) >= 100) return Math.round(v).toLocaleString()
      return Number(v.toPrecision(3)).toLocaleString()
  }
}

export function groupLabel(g: { group: string | null; other?: boolean }, groupBy?: string): string {
  if (g.other) return 'Other'
  if (g.group == null) return groupBy ? '(none)' : 'All'
  if (groupBy === 'status' && STATUS_WORDS[g.group]) return STATUS_WORDS[g.group]
  return g.group
}

/** Span statuses in words, for status-grouped charts. */
const STATUS_WORDS: Record<string, string> = {
  success: 'Succeeded',
  error: 'Failed',
  running: 'Running',
  descendant_error: 'Succeeded, a span under it failed',
}

export interface ChartSeries {
  key: string
  label: string
  metric: string
  group: string | null
  kind: ValueKind
}

/** Recharts rows: one per bucket, one column per (group, metric) series. */
export function toChartData(res: MetricsRes, metrics: MetricSpec[]): { rows: Record<string, number | string | null>[]; series: ChartSeries[] } {
  const specs = res.metrics?.length ? res.metrics : metrics
  const series: ChartSeries[] = []
  const multiMetric = specs.length > 1
  const grouped = !!res.group_by
  res.groups.forEach((g, gi) => {
    specs.forEach((m, mi) => {
      const name = metricName(m)
      const gl = groupLabel(g, res.group_by)
      const label = grouped && multiMetric ? `${gl} · ${metricLabel(m)}` : grouped ? gl : metricLabel(m)
      series.push({ key: `s${gi}_${mi}`, label, metric: name, group: g.group, kind: valueKind(m) })
    })
  })
  // Series that never have a non-zero value only add legend noise.
  const live = series.filter((s, si) => {
    const g = res.groups[Math.floor(si / specs.length)]
    return (g.series[s.metric] ?? []).some((v) => v != null && v !== 0)
  })
  const rows = res.buckets.map((t, bi) => {
    const row: Record<string, number | string | null> = { t }
    res.groups.forEach((g, gi) => {
      specs.forEach((m, mi) => {
        row[`s${gi}_${mi}`] = g.series[metricName(m)]?.[bi] ?? null
      })
    })
    return row
  })
  return { rows, series: live.length ? live : series }
}

let idCounter = 0
export function newPanelId(): string {
  idCounter += 1
  return `p${Date.now().toString(36)}${idCounter.toString(36)}`
}

export function blankPanel(): DashboardPanel {
  return { id: newPanelId(), title: '', chart: 'line', metrics: [{ agg: 'count' }], group_by: '', width: 1 }
}

export function panelTitle(p: DashboardPanel): string {
  if (p.title) return p.title
  const ms = p.metrics.map(metricLabel).join(', ') || 'Spans'
  const g = GROUP_PRESETS.find((x) => x.id === p.group_by)?.label ?? p.group_by
  return p.group_by ? `${ms} by ${g?.toLowerCase()}` : ms
}

/** Panels of the "Overview" template. */
export function overviewPanels(): DashboardPanel[] {
  const id = newPanelId
  return [
    { id: id(), title: 'Spans by status', chart: 'stacked', metrics: [{ agg: 'count', name: 'Spans' }], group_by: 'status', width: 2 },
    { id: id(), title: 'Error rate', chart: 'stat', metrics: [{ agg: 'error_rate', name: 'Error rate' }, { agg: 'count', name: 'Spans' }], width: 1 },
    { id: id(), title: 'Latency p50 / p95', chart: 'line', metrics: [{ agg: 'p50', field: 'latency_ms', name: 'p50' }, { agg: 'p95', field: 'latency_ms', name: 'p95' }], width: 1 },
    { id: id(), title: 'Tokens by model', chart: 'stacked', metrics: [{ agg: 'sum', field: 'total_tokens', name: 'Tokens' }], group_by: 'model', filter: { trace_roots_only: false }, width: 1 },
    { id: id(), title: 'Cost by model', chart: 'bar', metrics: [{ agg: 'sum', field: 'cost', name: 'Cost' }], group_by: 'model', filter: { trace_roots_only: false }, width: 1 },
    { id: id(), title: 'Span names', chart: 'table', metrics: [{ agg: 'count', name: 'Spans' }, { agg: 'p95', field: 'latency_ms', name: 'p95 latency' }, { agg: 'sum', field: 'total_tokens', name: 'Tokens' }, { agg: 'error_rate', name: 'Error rate' }], group_by: 'name', width: 3 },
  ]
}

export function newDashboard(name: string, template: 'blank' | 'overview'): DashboardVal {
  return {
    _class_name: 'Dashboard',
    _bases: ['Object', 'BaseModel'],
    name,
    description: '',
    time_range: '7d',
    refresh_seconds: 0,
    filter: { trace_roots_only: true },
    panels: template === 'overview' ? overviewPanels() : [],
  }
}

/** Move a panel one slot left (-1) or right (+1). */
export function movePanel(panels: DashboardPanel[], id: string, dir: -1 | 1): DashboardPanel[] {
  const i = panels.findIndex((p) => p.id === id)
  const j = i + dir
  if (i < 0 || j < 0 || j >= panels.length) return panels
  const out = [...panels]
  ;[out[i], out[j]] = [out[j], out[i]]
  return out
}

// ---------------------------------------------------------------- scatter (metric vs metric per span)

function numOf(v: unknown): number | null {
  if (typeof v === 'number' && Number.isFinite(v)) return v
  if (typeof v === 'boolean') return v ? 1 : 0
  if (typeof v === 'string' && v.trim() !== '' && Number.isFinite(Number(v))) return Number(v)
  return null
}

/** A per-span value for a metric field: built-in fields, feedback.<type>.<path>, or a dotted span path. */
export function callFieldValue(c: Span, field: string): number | null {
  switch (field) {
    case 'latency_ms':
      return callLatencyMs(c)
    case 'total_tokens':
      return callTokens(c).total || null
    case 'prompt_tokens':
      return callTokens(c).prompt || null
    case 'completion_tokens':
      return callTokens(c).completion || null
    case 'cost':
    case 'cost_usd':
      return callCost(c)
  }
  if (field.startsWith('feedback.')) {
    const rest = field.slice('feedback.'.length)
    const fb = (c.feedback ?? []).find((f) => rest === f.feedback_type || rest.startsWith(f.feedback_type + '.'))
    if (!fb) return null
    const sub = rest.slice(fb.feedback_type.length + 1)
    return numOf(sub ? getPath(fb.payload, sub) : fb.payload)
  }
  return numOf(getPath(c, field))
}

/** The scatter series a span belongs to for a dashboard group-by. */
export function callGroupValue(c: Span, groupBy: string | undefined): string {
  switch (groupBy || '') {
    case '':
      return ''
    case 'name':
    case 'op_name':
      return spanDisplayName(c.name)
    case 'status':
      return callStatus(c)
    case 'model': {
      const models = Object.keys(c.summary?.usage ?? {})
      return models.length === 1 ? models[0] : models.length ? 'multiple' : '(none)'
    }
    case 'session':
    case 'thread':
      return c.session_id ?? '(none)'
    case 'user':
      return c.user_id ?? '(none)'
  }
  const v = getPath(c, groupBy!)
  return v == null ? '(none)' : String(v)
}

export interface ScatterSeries {
  group: string
  points: { x: number; y: number; id: string; label: string }[]
}

/** Points per group for calls that have both values; at most `maxGroups` groups, the rest folded into "Other". */
export function scatterSeries(calls: Span[], xField: string, yField: string, groupBy?: string, maxGroups = 6): ScatterSeries[] {
  const by = new Map<string, ScatterSeries['points']>()
  for (const c of calls) {
    const x = callFieldValue(c, xField)
    const y = callFieldValue(c, yField)
    if (x == null || y == null) continue
    const g = callGroupValue(c, groupBy)
    if (!by.has(g)) by.set(g, [])
    by.get(g)!.push({ x, y, id: c.id, label: c.display_name || spanDisplayName(c.name) })
  }
  const groups = [...by.entries()].sort((a, b) => b[1].length - a[1].length)
  const out: ScatterSeries[] = groups.slice(0, maxGroups).map(([group, points]) => ({ group, points }))
  const rest = groups.slice(maxGroups).flatMap(([, p]) => p)
  if (rest.length) out.push({ group: '__other__', points: rest })
  return out
}

/** Switching to a scatter needs two numeric fields; switching away keeps them as averages. */
export function withChart(p: DashboardPanel, chart: ChartType): DashboardPanel {
  if (chart !== 'scatter' || p.chart === 'scatter') return { ...p, chart }
  const fields = p.metrics.map((m) => m.field).filter((f): f is string => !!f)
  const x = fields[0] ?? 'latency_ms'
  const y = fields.find((f) => f !== x) ?? (x === 'total_tokens' ? 'latency_ms' : 'total_tokens')
  return { ...p, chart, metrics: [{ agg: 'avg', field: x }, { agg: 'avg', field: y }] }
}
