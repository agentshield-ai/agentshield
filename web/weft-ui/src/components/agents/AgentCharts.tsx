import { useQuery } from '@tanstack/react-query'
import { Area, AreaChart, Bar, BarChart, CartesianGrid, Line, LineChart, ResponsiveContainer, Tooltip, XAxis, YAxis } from 'recharts'
import { agentsApi, eqQuery } from '../../api/agents'
import type { Query } from '../../api/types'
import { fmtCost, fmtDuration, fmtNumber } from '../../lib/format'
import { ErrorBox, Spinner } from '../common'
import { compact, fmtPct, granularityFor, windowStart } from './agentUtils'
import './agents.css'

const AXIS = { fontSize: 10, fill: 'var(--text-3)' }
const TOOLTIP_STYLE = { background: 'var(--panel)', border: '1px solid var(--border)', borderRadius: 6, fontSize: 12, color: 'var(--text)' }

function ChartCard({ title, value, children }: { title: string; value: string; children: React.ReactElement }) {
  return (
    <div className="chart-card">
      <div className="row">
        <span className="small muted grow">{title}</span>
        <span className="mono small">{value}</span>
      </div>
      <div style={{ height: 120 }}>
        <ResponsiveContainer width="100%" height="100%">
          {children}
        </ResponsiveContainer>
      </div>
    </div>
  )
}

function andQuery(a: Query | undefined, b: Query | undefined): Query | undefined {
  if (!a) return b
  if (!b) return a
  return { $expr: { $and: [a.$expr, b.$expr] } } as Query
}

const num = (v: unknown) => (typeof v === 'number' ? v : 0)

/** Invocations, errors, latency, tokens and cost over time for spans matching `query`. */
export function AgentCharts({ projectId, query, range }: { projectId: string; query?: Query; range: string }) {
  const start = windowStart(range)
  const granularity = granularityFor(range)
  const inv = useQuery({
    queryKey: ['agent-stats', projectId, query, range, 'inv'],
    queryFn: () =>
      agentsApi.stats({
        project_id: projectId,
        start,
        granularity,
        query: andQuery(query, eqQuery({ is_invocation: true })),
        metrics: [
          { alias: 'latency', value: { key: 'duration_ms' }, value_type: 'number', percentiles: [50, 95] },
          { alias: 'err', value: { key: 'is_error' }, value_type: 'boolean', aggregations: ['count_true'] },
        ],
      }),
  })
  const all = useQuery({
    queryKey: ['agent-stats', projectId, query, range, 'all'],
    queryFn: () =>
      agentsApi.stats({
        project_id: projectId,
        start,
        granularity,
        query,
        metrics: [
          { alias: 'in', value: { key: 'input_tokens' }, value_type: 'number', aggregations: ['sum'] },
          { alias: 'out', value: { key: 'output_tokens' }, value_type: 'number', aggregations: ['sum'] },
          { alias: 'cost', value: { key: 'total_cost_usd' }, value_type: 'number', aggregations: ['sum'] },
          { alias: 'err', value: { key: 'is_error' }, value_type: 'boolean', aggregations: ['count_true'] },
        ],
      }),
  })
  if (inv.isLoading || all.isLoading)
    return (
      <div className="chart-strip" style={{ minHeight: 150, display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
        <Spinner label="Loading charts…" />
      </div>
    )
  if (inv.error || all.error) return <ErrorBox error={inv.error ?? all.error} />
  const byTs = new Map<string, Record<string, number | string | null>>()
  for (const r of inv.data!.rows)
    byTs.set(String(r.timestamp), {
      timestamp: String(r.timestamp),
      invocations: num(r.count),
      failed: num(r.err_count_true),
      ok: Math.max(0, num(r.count) - num(r.err_count_true)),
      p50: (r.latency_p50 as number | null) ?? null,
      p95: (r.latency_p95 as number | null) ?? null,
    })
  for (const r of all.data!.rows) {
    const row = byTs.get(String(r.timestamp)) ?? { timestamp: String(r.timestamp) }
    Object.assign(row, { spans: num(r.count), span_errors: num(r.err_count_true), in: num(r.in_sum), out: num(r.out_sum), cost: num(r.cost_sum) })
    byTs.set(String(r.timestamp), row)
  }
  const data = [...byTs.values()].sort((a, b) => String(a.timestamp).localeCompare(String(b.timestamp)))
  const total = (k: string) => data.reduce((s, r) => s + num(r[k]), 0)
  const invs = total('invocations')
  const p95s = data.map((r) => r.p95).filter((v): v is number => typeof v === 'number')
  const short = granularity < 86_400
  const tick = (iso: string) => {
    const d = new Date(iso)
    return short ? d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' }) : d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' })
  }
  const labelFmt = (iso: string) => new Date(iso).toLocaleString()
  const xAxis = <XAxis dataKey="timestamp" tickFormatter={tick} tick={AXIS} minTickGap={24} axisLine={false} tickLine={false} />
  const grid = <CartesianGrid vertical={false} stroke="var(--border)" />
  const margin = { top: 4, right: 4, bottom: 0, left: 0 }
  return (
    <div className="chart-strip">
      <ChartCard title="Invocations" value={`${fmtNumber(invs)}${total('failed') ? ` · ${fmtPct(total('failed') / invs)} err` : ''}`}>
        <BarChart data={data} margin={margin}>
          {grid}
          {xAxis}
          <YAxis tick={AXIS} tickFormatter={compact} axisLine={false} tickLine={false} allowDecimals={false} width={32} />
          <Tooltip contentStyle={TOOLTIP_STYLE} labelFormatter={labelFmt} cursor={{ fill: 'var(--panel-2)' }} />
          <Bar dataKey="ok" name="Succeeded" stackId="a" fill="var(--c1)" isAnimationActive={false} />
          <Bar dataKey="failed" name="Failed" stackId="a" fill="var(--error)" isAnimationActive={false} />
        </BarChart>
      </ChartCard>
      <ChartCard title="Latency p50 / p95" value={p95s.length ? `max p95 ${fmtDuration(Math.max(...p95s))}` : '—'}>
        <LineChart data={data} margin={margin}>
          {grid}
          {xAxis}
          <YAxis tick={AXIS} tickFormatter={(v: number) => (v === 0 ? '0' : fmtDuration(v))} axisLine={false} tickLine={false} width={48} />
          <Tooltip contentStyle={TOOLTIP_STYLE} labelFormatter={labelFmt} formatter={(v: number) => fmtDuration(v)} />
          <Line dataKey="p50" name="p50" stroke="var(--c2)" dot={false} strokeWidth={1.5} isAnimationActive={false} connectNulls />
          <Line dataKey="p95" name="p95" stroke="var(--c3)" dot={false} strokeWidth={1.5} isAnimationActive={false} connectNulls />
        </LineChart>
      </ChartCard>
      <ChartCard title="Tokens in / out" value={`${compact(total('in'))} / ${compact(total('out'))}`}>
        <BarChart data={data} margin={margin}>
          {grid}
          {xAxis}
          <YAxis tick={AXIS} tickFormatter={compact} axisLine={false} tickLine={false} width={36} />
          <Tooltip contentStyle={TOOLTIP_STYLE} labelFormatter={labelFmt} cursor={{ fill: 'var(--panel-2)' }} formatter={(v: number) => fmtNumber(v)} />
          <Bar dataKey="in" name="Input" stackId="t" fill="var(--c6)" isAnimationActive={false} />
          <Bar dataKey="out" name="Output" stackId="t" fill="var(--c4)" isAnimationActive={false} />
        </BarChart>
      </ChartCard>
      <ChartCard title="Cost" value={fmtCost(total('cost'))}>
        <AreaChart data={data} margin={margin}>
          {grid}
          {xAxis}
          <YAxis tick={AXIS} tickFormatter={(v: number) => (v === 0 ? '0' : v < 1 ? `$${Number(v.toPrecision(2))}` : `$${compact(v)}`)} axisLine={false} tickLine={false} width={56} />
          <Tooltip contentStyle={TOOLTIP_STYLE} labelFormatter={labelFmt} formatter={(v: number) => fmtCost(v)} />
          <Area dataKey="cost" name="Cost" stroke="var(--c5)" fill="var(--c5)" fillOpacity={0.2} isAnimationActive={false} />
        </AreaChart>
      </ChartCard>
    </div>
  )
}
