import { useMemo } from 'react'
import { useNavigate } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { CartesianGrid, Legend, ResponsiveContainer, Scatter, ScatterChart, Tooltip, XAxis, YAxis, ZAxis } from 'recharts'
import { api } from '../../api/client'
import type { DashboardPanel, DashboardVal } from '../../api/dashboards'
import { useProjectId } from '../../hooks/useProject'
import { callUrl } from '../../lib/urls'
import { ErrorBox, Spinner } from '../common'
import { SERIES_COLORS } from '../evals/evalUtils'
import { fieldLabel, fmtValue, groupLabel, mergeFilters, mergeQueries, scatterSeries, timeRangeBounds, valueKind } from './dashboardUtils'
import './dashboards.css'

const AXIS = { fontSize: 10, fill: 'var(--text-3)' }
const MAX_CALLS = 2000

/** Scatter panel: one point per call, metrics[0].field on x against metrics[1].field on y. */
export function ScatterPanel({
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
  const navigate = useNavigate()
  const pid = useProjectId() || projectId
  const xField = panel.metrics[0]?.field ?? 'latency_ms'
  const yField = panel.metrics[1]?.field ?? 'total_tokens'
  const q = useQuery({
    queryKey: ['calls-scatter', projectId, panel.filter, panel.query, panel.search, panel.time_range, dash?.filter, dash?.query, dash?.time_range],
    queryFn: () => {
      const range = timeRangeBounds(panel.time_range || dash?.time_range)
      const filter = { ...(mergeFilters(dash?.filter, panel.filter) ?? {}) }
      if (range.start) filter.started_after = range.start
      if (range.end) filter.started_before = range.end
      const needsFeedback = [xField, yField].some((f) => f.startsWith('feedback.'))
      return api.callsQuery({
        project_id: projectId,
        filter,
        query: mergeQueries(dash?.query, panel.query),
        sort_by: [{ field: 'started_at', direction: 'desc' }],
        limit: MAX_CALLS,
        include_costs: true,
        include_feedback: needsFeedback,
      })
    },
    refetchInterval: refreshMs || false,
    placeholderData: (prev) => prev,
  })
  const series = useMemo(() => scatterSeries(q.data?.calls ?? [], xField, yField, panel.group_by), [q.data, xField, yField, panel.group_by])
  if (q.isLoading)
    return (
      <div className="dash-chart-empty" style={{ height }}>
        <Spinner />
      </div>
    )
  if (q.error) return <ErrorBox error={q.error} />
  const n = series.reduce((s, x) => s + x.points.length, 0)
  if (!n)
    return (
      <div className="dash-chart-empty small faint" style={{ height }}>
        No calls have both {fieldLabel(xField)} and {fieldLabel(yField)} in this range.
      </div>
    )
  const xKind = valueKind({ agg: 'avg', field: xField })
  const yKind = valueKind({ agg: 'avg', field: yField })
  const grouped = !!panel.group_by
  return (
    <div style={{ height }} aria-label={`Scatter of ${fieldLabel(yField)} against ${fieldLabel(xField)}`}>
      <ResponsiveContainer width="100%" height="100%">
        <ScatterChart margin={{ top: 6, right: 8, bottom: 4, left: 0 }}>
          <CartesianGrid stroke="var(--border)" />
          <XAxis type="number" dataKey="x" name={fieldLabel(xField)} tick={AXIS} tickFormatter={(v: number) => fmtValue(v, xKind)} axisLine={false} tickLine={false} />
          <YAxis type="number" dataKey="y" name={fieldLabel(yField)} tick={AXIS} tickFormatter={(v: number) => fmtValue(v, yKind)} axisLine={false} tickLine={false} width={52} />
          <ZAxis range={[28, 28]} />
          <Tooltip
            cursor={{ strokeDasharray: '3 3' }}
            contentStyle={{ background: 'var(--panel)', border: '1px solid var(--border)', borderRadius: 6, fontSize: 12, color: 'var(--text)' }}
            formatter={(v: number, name: string) => [fmtValue(v, name === fieldLabel(xField) ? xKind : yKind), name]}
            labelFormatter={() => ''}
          />
          {grouped && series.length > 1 && <Legend wrapperStyle={{ fontSize: 11 }} />}
          {series.map((s, i) => (
            <Scatter
              key={s.group}
              name={grouped ? groupLabel({ group: s.group === '__other__' ? null : s.group, other: s.group === '__other__' }, panel.group_by) : 'Calls'}
              data={s.points}
              fill={s.group === '__other__' ? 'var(--text-3)' : SERIES_COLORS[i % SERIES_COLORS.length]}
              fillOpacity={0.75}
              isAnimationActive={false}
              cursor="pointer"
              onClick={(p: { id?: string; payload?: { id: string } }) => {
                const id = p?.payload?.id ?? p?.id
                if (id) navigate(callUrl(pid, id))
              }}
            />
          ))}
        </ScatterChart>
      </ResponsiveContainer>
      {(q.data?.calls.length ?? 0) >= MAX_CALLS && <div className="small faint">Showing the latest {MAX_CALLS} calls.</div>}
    </div>
  )
}
