// Custom chart metrics, dashboards and server-side export. Mirrors the
// "Custom charts, dashboards and export" section of docs/weft/API.md.
import { api, ApiError, getApiKey, post } from './client'
import type { Any, CallsFilter, CallsQueryReq, ObjSchema, Query } from './types'

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
  filter?: CallsFilter
  query?: Query
  search?: string
  metrics: MetricSpec[]
  /** op_name, model, status, thread, user, or a call field path such as attributes.env */
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

export type ChartType = 'line' | 'bar' | 'stacked' | 'area' | 'table' | 'stat'

export interface DashboardPanel {
  id: string
  title: string
  chart: ChartType
  metrics: MetricSpec[]
  group_by?: string
  /** Panel-level filters, combined with the dashboard's. */
  filter?: CallsFilter
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
  filter?: CallsFilter
  query?: Query
  panels: DashboardPanel[]
}

export interface ExportReq extends CallsQueryReq {
  format: 'csv' | 'jsonl' | 'json'
  flatten?: boolean
}

const API_BASE: string = (import.meta.env.VITE_WEFT_API as string | undefined) ?? ''

export const dashboardsApi = {
  callsMetrics: (req: MetricsReq) => post<MetricsRes>('/calls/metrics', req),

  list: (project_id: string) =>
    api
      .objsQuery({ project_id, filter: { base_object_classes: ['Dashboard'], latest_only: true }, limit: 500 })
      .then((r) => r.objs.filter((o) => !o.deleted_at)),
  read: (project_id: string, object_id: string) => api.objRead(project_id, object_id, 'latest').then((r) => r.obj as ObjSchema & { val: DashboardVal }),
  save: (project_id: string, object_id: string, val: DashboardVal) => api.objCreate(project_id, object_id, val as Any, 'Dashboard'),
  remove: (project_id: string, object_id: string) => api.objDelete(project_id, object_id),

  /** Streams /calls/export into a file download. Returns the number of bytes received. */
  exportCalls: async (req: ExportReq, onProgress?: (bytes: number) => void, signal?: AbortSignal): Promise<number> => {
    const headers: Record<string, string> = { 'Content-Type': 'application/json' }
    const key = getApiKey()
    if (key) headers.Authorization = `Bearer ${key}`
    const res = await fetch(API_BASE + '/calls/export', { method: 'POST', headers, body: JSON.stringify(req), signal })
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
    const name = /filename="([^"]+)"/.exec(cd)?.[1] ?? `calls.${req.format}`
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
