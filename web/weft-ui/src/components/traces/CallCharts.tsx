import { useQuery } from '@tanstack/react-query'
import { Area, AreaChart, Bar, BarChart, CartesianGrid, Line, LineChart, ResponsiveContainer, Tooltip, XAxis, YAxis } from 'recharts'
import { api } from '../../api/client'
import type { SpansFilter, SpansStatsBucket, Query } from '../../api/types'
import { fmtCost, fmtDuration, fmtNumber } from '../../lib/format'
import { ErrorBox, Spinner } from '../common'

const AXIS = { fontSize: 10, fill: 'var(--text-3)' }
const TOOLTIP_STYLE = {
  background: 'var(--panel)',
  border: '1px solid var(--border)',
  borderRadius: 6,
  fontSize: 12,
  color: 'var(--text)',
}

function tickFormatter(bucketSeconds: number) {
  return (iso: string) => {
    const d = new Date(iso)
    if (bucketSeconds < 86_400) return d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit', hourCycle: 'h23' })
    return d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' })
  }
}

function fmtCostTick(v: number) {
  if (v === 0) return '0'
  if (v < 1000) return fmtCost(v)
  return `$${compact(v)}`
}

function compact(n: number) {
  return Intl.NumberFormat(undefined, { notation: 'compact', maximumFractionDigits: 1 }).format(n)
}

function ChartCard({ title, value, children }: { title: string; value: string; children: React.ReactElement }) {
  return (
    <div className="chart-card">
      <div className="row">
        <span className="small muted grow">{title}</span>
        <span className="mono small">{value}</span>
      </div>
      <div style={{ height: 110 }}>
        <ResponsiveContainer width="100%" height="100%">
          {children}
        </ResponsiveContainer>
      </div>
    </div>
  )
}

export function CallCharts({ projectId, filter, query, search }: { projectId: string; filter?: SpansFilter; query?: Query; search?: string }) {
  const stats = useQuery({
    queryKey: ['calls-stats', projectId, filter, query, search],
    queryFn: () => api.spansStats({ project_id: projectId, filter, query, search }),
  })
  if (stats.isLoading)
    return (
      <div className="chart-strip" style={{ minHeight: 140, alignItems: 'center', justifyContent: 'center', display: 'flex' }}>
        <Spinner label="Loading charts…" />
      </div>
    )
  if (stats.error) return <ErrorBox error={stats.error} />
  const s = stats.data!
  if (!s.buckets.length) return <div className="chart-strip empty small">No data in range for charts.</div>
  const data = s.buckets.map((b: SpansStatsBucket) => ({ ...b, success: Math.max(0, b.count - b.error_count) }))
  const fmt = tickFormatter(s.bucket_seconds)
  const labelFmt = (iso: string) => new Date(iso).toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', hourCycle: 'h23' })
  const xAxis = <XAxis dataKey="start" tickFormatter={fmt} tick={AXIS} minTickGap={24} axisLine={false} tickLine={false} />
  const grid = <CartesianGrid vertical={false} stroke="var(--separator)" />
  const errPct = s.totals.count ? (s.totals.error_count / s.totals.count) * 100 : 0
  return (
    <div className="chart-strip">
      <ChartCard title="Spans" value={`${fmtNumber(s.totals.count)}${s.totals.error_count ? ` · ${errPct.toFixed(1)}% err` : ''}`}>
        <BarChart data={data} margin={{ top: 4, right: 4, bottom: 0, left: 0 }}>
          {grid}
          {xAxis}
          <YAxis tick={AXIS} tickFormatter={compact} axisLine={false} tickLine={false} allowDecimals={false} width={36} />
          <Tooltip contentStyle={TOOLTIP_STYLE} labelFormatter={labelFmt} cursor={{ fill: 'var(--panel-2)' }} />
          <Bar dataKey="success" name="Success" stackId="a" fill="var(--c1)" isAnimationActive={false} />
          <Bar dataKey="error_count" name="Errors" stackId="a" fill="var(--error)" isAnimationActive={false} />
        </BarChart>
      </ChartCard>
      <ChartCard title="Latency p50 / p95" value={`${fmtDuration(s.totals.latency_ms_p50)} / ${fmtDuration(s.totals.latency_ms_p95)}`}>
        <LineChart data={data} margin={{ top: 4, right: 4, bottom: 0, left: 0 }}>
          {grid}
          {xAxis}
          <YAxis tick={AXIS} tickFormatter={(v: number) => (v === 0 ? '0' : fmtDuration(v))} axisLine={false} tickLine={false} width={52} />
          <Tooltip contentStyle={TOOLTIP_STYLE} labelFormatter={labelFmt} formatter={(v: number) => fmtDuration(v)} />
          <Line dataKey="latency_ms_p50" name="p50" stroke="var(--c2)" dot={false} strokeWidth={1.5} isAnimationActive={false} connectNulls />
          <Line dataKey="latency_ms_p95" name="p95" stroke="var(--c3)" dot={false} strokeWidth={1.5} isAnimationActive={false} connectNulls />
        </LineChart>
      </ChartCard>
      <ChartCard title="Tokens" value={compact(s.totals.total_tokens)}>
        <BarChart data={data} margin={{ top: 4, right: 4, bottom: 0, left: 0 }}>
          {grid}
          {xAxis}
          <YAxis tick={AXIS} tickFormatter={compact} axisLine={false} tickLine={false} width={36} />
          <Tooltip contentStyle={TOOLTIP_STYLE} labelFormatter={labelFmt} cursor={{ fill: 'var(--panel-2)' }} formatter={(v: number) => fmtNumber(v)} />
          <Bar dataKey="prompt_tokens" name="Prompt" stackId="t" fill="var(--c6)" isAnimationActive={false} />
          <Bar dataKey="completion_tokens" name="Completion" stackId="t" fill="var(--c4)" isAnimationActive={false} />
        </BarChart>
      </ChartCard>
      <ChartCard title="Cost" value={fmtCost(s.totals.cost_usd)}>
        <AreaChart data={data} margin={{ top: 4, right: 4, bottom: 0, left: 0 }}>
          {grid}
          {xAxis}
          <YAxis tick={AXIS} tickFormatter={fmtCostTick} axisLine={false} tickLine={false} width={52} />
          <Tooltip contentStyle={TOOLTIP_STYLE} labelFormatter={labelFmt} formatter={(v: number) => fmtCost(v)} />
          <Area dataKey="cost_usd" name="Cost" stroke="var(--c5)" fill="var(--c5)" fillOpacity={0.18} isAnimationActive={false} />
        </AreaChart>
      </ChartCard>
    </div>
  )
}
