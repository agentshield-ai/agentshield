import { useQuery } from '@tanstack/react-query'
import { Area, AreaChart, Bar, BarChart, CartesianGrid, Line, LineChart, ResponsiveContainer, Tooltip, XAxis, YAxis } from 'recharts'
import { dashboardsApi, type DashboardPanel, type DashboardVal, type MetricsRes } from '../../api/dashboards'
import { ErrorBox, Spinner } from '../common'
import { SERIES_COLORS } from '../evals/evalUtils'
import { fmtValue, groupLabel, metricLabel, metricName, panelRequest, toChartData, valueKind, type ChartSeries } from './dashboardUtils'
import { ScatterPanel } from './ScatterChart'
import './dashboards.css'

const AXIS = { fontSize: 10, fill: 'var(--text-3)' }
const TOOLTIP = {
  contentStyle: { background: 'var(--panel)', border: '1px solid var(--border)', borderRadius: 6, fontSize: 12, color: 'var(--text)' },
  labelStyle: { color: 'var(--text-2)' },
  itemStyle: { color: 'var(--text)' },
}
const STATUS_COLORS: Record<string, string> = {
  success: 'var(--c1)',
  error: 'var(--error)',
  running: 'var(--running)',
  descendant_error: 'var(--warn)',
}

function colorFor(s: ChartSeries, i: number, groupBy: string): string {
  if (groupBy === 'status' && s.group && STATUS_COLORS[s.group]) return STATUS_COLORS[s.group]
  if (s.group === '__other__') return 'var(--text-3)'
  return SERIES_COLORS[i % SERIES_COLORS.length]
}

function tickTime(bucketSeconds: number, buckets: string[]) {
  const spanMs = buckets.length > 1 ? new Date(buckets[buckets.length - 1]).getTime() - new Date(buckets[0]).getTime() : 0
  return (iso: string) => {
    const d = new Date(iso)
    if (bucketSeconds >= 86_400) return d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' })
    const time = d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' })
    // Ranges over a day need the date too, or 09:00 on two days look the same.
    if (spanMs > 86_400_000) return `${d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' })} ${time}`
    return time
  }
}

function compact(n: number) {
  return Intl.NumberFormat(undefined, { notation: 'compact', maximumFractionDigits: 1 }).format(n)
}

/** Fetches and renders one panel. `refreshMs` polls; `nonce` forces a refetch. */
export function PanelChart({
  projectId,
  panel,
  dash,
  refreshMs,
  height = 200,
}: {
  projectId: string
  panel: DashboardPanel
  dash?: Pick<DashboardVal, 'filter' | 'query' | 'time_range'>
  refreshMs?: number
  height?: number
}) {
  const q = useQuery({
    queryKey: ['calls-metrics', projectId, panel, dash?.filter, dash?.query, dash?.time_range],
    queryFn: () => dashboardsApi.callsMetrics(panelRequest(projectId, panel, dash)),
    refetchInterval: refreshMs || false,
    placeholderData: (prev) => prev,
    enabled: panel.chart !== 'scatter',
  })
  if (panel.chart === 'scatter') return <ScatterPanel projectId={projectId} panel={panel} dash={dash} refreshMs={refreshMs} height={height} />
  if (q.isLoading)
    return (
      <div className="dash-chart-empty" style={{ height }}>
        <Spinner />
      </div>
    )
  if (q.error) return <ErrorBox error={q.error} />
  return <MetricChart res={q.data!} panel={panel} height={height} />
}

export function MetricChart({ res, panel, height = 200 }: { res: MetricsRes; panel: DashboardPanel; height?: number }) {
  const metrics = res.metrics?.length ? res.metrics : panel.metrics
  if (panel.chart === 'stat') return <StatView res={res} panel={panel} height={height} />
  if (panel.chart === 'table') return <TableView res={res} panel={panel} height={height} />
  if (!res.count)
    return (
      <div className="dash-chart-empty small faint" style={{ height }}>
        No calls in this time range.
      </div>
    )
  const { rows, series } = toChartData(res, metrics)
  const kind = series[0]?.kind ?? 'number'
  const fmtTick = (v: number) => (kind === 'number' ? compact(v) : fmtValue(v, kind))
  const xTick = tickTime(res.bucket_seconds, res.buckets)
  const labelFmt = (iso: string) => new Date(iso).toLocaleString()
  const kindOf = Object.fromEntries(series.map((s) => [s.key, s.kind]))
  const tooltipFmt = (v: number, _n: string, item: { dataKey?: string | number }) => fmtValue(v, kindOf[String(item?.dataKey)] ?? kind)
  const common = (
    <>
      <CartesianGrid vertical={false} stroke="var(--border)" />
      <XAxis dataKey="t" tickFormatter={xTick} tick={AXIS} minTickGap={28} axisLine={false} tickLine={false} />
      <YAxis tick={AXIS} tickFormatter={fmtTick} axisLine={false} tickLine={false} width={54} allowDecimals={kind !== 'number'} />
      <Tooltip {...TOOLTIP} labelFormatter={labelFmt} formatter={tooltipFmt as never} cursor={{ fill: 'var(--panel-2)' }} />
    </>
  )
  const chartH = series.length > 1 ? height - 26 : height
  const margin = { top: 6, right: 6, bottom: 0, left: 0 }
  let chart: React.ReactElement
  if (panel.chart === 'bar' || panel.chart === 'stacked') {
    chart = (
      <BarChart data={rows} margin={margin}>
        {common}
        {series.map((s, i) => (
          <Bar key={s.key} dataKey={s.key} name={s.label} fill={colorFor(s, i, res.group_by)} stackId={panel.chart === 'stacked' ? 'a' : undefined} isAnimationActive={false} />
        ))}
      </BarChart>
    )
  } else if (panel.chart === 'area') {
    chart = (
      <AreaChart data={rows} margin={margin}>
        {common}
        {series.map((s, i) => (
          <Area key={s.key} dataKey={s.key} name={s.label} stroke={colorFor(s, i, res.group_by)} fill={colorFor(s, i, res.group_by)} fillOpacity={0.2} stackId="a" isAnimationActive={false} connectNulls />
        ))}
      </AreaChart>
    )
  } else {
    chart = (
      <LineChart data={rows} margin={margin}>
        {common}
        {series.map((s, i) => (
          <Line key={s.key} dataKey={s.key} name={s.label} stroke={colorFor(s, i, res.group_by)} dot={rows.length < 3} strokeWidth={1.75} isAnimationActive={false} connectNulls />
        ))}
      </LineChart>
    )
  }
  return (
    <div>
      <div style={{ height: chartH }}>
        <ResponsiveContainer width="100%" height="100%">
          {chart}
        </ResponsiveContainer>
      </div>
      {series.length > 1 && (
        <div className="dash-legend" role="list">
          {series.map((s, i) => (
            <span key={s.key} className="dash-legend-item" role="listitem" title={s.label}>
              <span className="swatch" style={{ background: colorFor(s, i, res.group_by) }} />
              <span className="truncate">{s.label}</span>
            </span>
          ))}
        </div>
      )}
      {res.truncated && <div className="small faint">Showing the first 200,000 calls.</div>}
    </div>
  )
}

function StatView({ res, panel, height }: { res: MetricsRes; panel: DashboardPanel; height: number }) {
  const metrics = res.metrics?.length ? res.metrics : panel.metrics
  const [main, ...rest] = metrics
  if (!main) return null
  const kind = valueKind(main)
  const g = res.groups[0]
  const spark = g ? res.buckets.map((t, i) => ({ t, v: g.series[metricName(main)]?.[i] ?? null })) : []
  return (
    <div className="dash-stat" style={{ minHeight: height }}>
      <div className="dash-stat-value">{fmtValue(res.totals[metricName(main)], kind)}</div>
      <div className="small muted">{metricLabel(main)}</div>
      {rest.length > 0 && (
        <div className="row wrap small" style={{ gap: 12, justifyContent: 'center' }}>
          {rest.map((m) => (
            <span key={metricName(m)}>
              <span className="faint">{metricLabel(m)}</span> <span className="mono">{fmtValue(res.totals[metricName(m)], valueKind(m))}</span>
            </span>
          ))}
        </div>
      )}
      {spark.length > 1 && (
        <div style={{ height: 48, width: '100%' }} aria-hidden>
          <ResponsiveContainer width="100%" height="100%">
            <AreaChart data={spark} margin={{ top: 4, right: 0, bottom: 0, left: 0 }}>
              <Area dataKey="v" stroke="var(--c1)" fill="var(--c1)" fillOpacity={0.15} isAnimationActive={false} connectNulls />
            </AreaChart>
          </ResponsiveContainer>
        </div>
      )}
    </div>
  )
}

function TableView({ res, panel, height }: { res: MetricsRes; panel: DashboardPanel; height: number }) {
  const metrics = res.metrics?.length ? res.metrics : panel.metrics
  const hasCount = metrics.some((m) => m.agg === 'count' && !m.field)
  if (!res.count)
    return (
      <div className="dash-chart-empty small faint" style={{ height: Math.min(height, 120) }}>
        No calls in this time range.
      </div>
    )
  return (
    <div className="table-wrap" style={{ maxHeight: Math.max(height, 240), overflow: 'auto' }}>
      <table className="table">
        <thead>
          <tr>
            <th>{res.group_by ? res.group_by.replace(/_/g, ' ') : 'Group'}</th>
            {!hasCount && <th className="num">Spans</th>}
            {metrics.map((m) => (
              <th key={metricName(m)} className="num">
                {metricLabel(m)}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {res.groups.map((g) => (
            <tr key={`${g.group}-${g.other ? 'o' : ''}`}>
              <td className="truncate" style={{ maxWidth: 260 }} title={g.group ?? ''}>
                {groupLabel(g, res.group_by)}
              </td>
              {!hasCount && <td className="num">{g.count.toLocaleString()}</td>}
              {metrics.map((m) => (
                <td key={metricName(m)} className="num mono">
                  {fmtValue(g.totals[metricName(m)], valueKind(m))}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}
