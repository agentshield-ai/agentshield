// Custom chart metrics, dashboards and server-side export. Mirrors the
// "Custom charts, dashboards and export" section of docs/weft/API.md.
import { api, ApiError, getApiKey, post } from './client'
import type { Any, Query, SpansFilter, SpansQueryReq, WeftObject } from './types'
import { normaliseSpansFilter } from './filters'

export type MetricAgg = 'count' | 'sum' | 'avg' | 'min' | 'max' | 'p50' | 'p90' | 'p95' | 'p99' | 'error_rate'
export const METRIC_AGGS: MetricAgg[] = ['count', 'sum', 'avg', 'min', 'max', 'p50', 'p90', 'p95', 'p99', 'error_rate']

export interface MetricSpec {
  name?: string
  /** latency_ms, total_tokens, prompt_tokens, completion_tokens, cost, or any inputs./output./attributes./summary./feedback. path */
  field?: string
  agg: MetricAgg
}

export interface MetricsReq {
  project_id: string
  filter?: SpansFilter
  query?: Query
  search?: string
  metrics: MetricSpec[]
  /** name, model, status, session, user, or a span field path such as attributes.env */
  group_by?: string
  bucket_seconds?: number
  start?: string
  end?: string
  max_groups?: number
}

export interface MetricsGroup {
  group: string | null
  other?: boolean
  count: number
  series: Record<string, (number | null)[]>
  totals: Record<string, number | null>
}

export interface MetricsRes {
  bucket_seconds: number
  start: string | null
  end: string | null
  buckets: string[]
  metrics: Required<MetricSpec>[]
  group_by: string
  groups: MetricsGroup[]
  totals: Record<string, number | null>
  count: number
  truncated: boolean
}

/** `scatter` plots metrics[0].field (x) against metrics[1].field (y), one point per span. */
export type ChartType = 'line' | 'bar' | 'stacked' | 'area' | 'table' | 'stat' | 'scatter'

export interface DashboardPanel {
  id: string
  title: string
  chart: ChartType
  metrics: MetricSpec[]
  group_by?: string
  /** Panel-level filters, combined with the dashboard's. */
  filter?: SpansFilter
  query?: Query
  search?: string
  /** Overrides the dashboard time range when set. */
  time_range?: string
  bucket_seconds?: number
  /** Width in grid columns (1–3). */
  width?: 1 | 2 | 3
}

export interface DashboardVal {
  _class_name: 'Dashboard'
  _bases: string[]
  name: string
  description?: string
  time_range: string
  refresh_seconds?: number
  filter?: SpansFilter
  query?: Query
  panels: DashboardPanel[]
}

export interface ExportReq extends SpansQueryReq {
  format: 'csv' | 'jsonl' | 'json'
  flatten?: boolean
}

/** A stored dashboard with its filters in Weft's names (dashboards saved
 * by earlier versions may name filter keys the older way). */
export function normaliseDashboard(v: Any): DashboardVal {
  const d = { ...(v ?? {}) } as DashboardVal
  if (d.filter) d.filter = normaliseSpansFilter(d.filter)
  d.panels = (Array.isArray(d.panels) ? d.panels : []).map((p) => {
    const q = { ...p }
    if (q.filter) q.filter = normaliseSpansFilter(q.filter)
    if (q.group_by) q.group_by = normaliseGroupBy(q.group_by)
    return q
  })
  return d
}

/** Group keys of earlier versions: op_name and thread. */
export function normaliseGroupBy(g: string): string {
  return g === 'op_name' || g === 'op' ? 'name' : g === 'thread' || g === 'thread_id' ? 'session' : g
}

const API_BASE: string = (import.meta.env.VITE_WEFT_API as string | undefined) ?? ''

export const dashboardsApi = {
  spansMetrics: (req: MetricsReq) => post<MetricsRes>('/api/v1/spans/metrics', req),

  list: (project_id: string) =>
    api
      .objectsQuery({ project_id, filter: { kinds: ['dashboard'], latest_only: true }, limit: 500 })
      .then((r) => r.items.filter((o) => !o.deleted_at)),
  read: (project_id: string, name: string) =>
    api.objectGet(project_id, name, 'latest').then((o) => ({ ...o, value: normaliseDashboard(o.value) }) as WeftObject & { value: DashboardVal }),
  save: (project_id: string, name: string, value: DashboardVal) => api.objectSave(project_id, name, value as Any, 'Dashboard'),
  remove: (project_id: string, name: string) => api.objectDelete(project_id, name),

  /** Streams /api/v1/spans/export into a file download. Returns the number of bytes received. */
  exportSpans: async (req: ExportReq, onProgress?: (bytes: number) => void, signal?: AbortSignal): Promise<number> => {
    const headers: Record<string, string> = { 'Content-Type': 'application/json' }
    const key = getApiKey()
    if (key) headers.Authorization = `Bearer ${key}`
    const res = await fetch(API_BASE + '/api/v1/spans/export', { method: 'POST', headers, body: JSON.stringify(req), signal })
    if (!res.ok) {
      let msg = res.statusText
      try {
        msg = (await res.json()).reason ?? msg
      } catch {
        /* not JSON */
      }
      throw new ApiError(res.status, msg)
    }
    const chunks: BlobPart[] = []
    let total = 0
    const reader = res.body!.getReader()
    for (;;) {
      const { done, value } = await reader.read()
      if (done) break
      chunks.push(value)
      total += value.byteLength
      onProgress?.(total)
    }
    const cd = res.headers.get('Content-Disposition') ?? ''
    const name = /filename="([^"]+)"/.exec(cd)?.[1] ?? `spans.${req.format}`
    const blob = new Blob(chunks, { type: res.headers.get('Content-Type') ?? 'application/octet-stream' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = name
    document.body.appendChild(a)
    a.click()
    a.remove()
    setTimeout(() => URL.revokeObjectURL(url), 2000)
    return total
  },
}
