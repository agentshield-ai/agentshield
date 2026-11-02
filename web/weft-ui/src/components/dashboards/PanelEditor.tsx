import { useEffect, useMemo, useState, type ReactNode } from 'react'
import { useQuery } from '@tanstack/react-query'
import { api } from '../../api/client'
import { METRIC_AGGS, type ChartType, type DashboardPanel, type DashboardVal, type MetricAgg, type MetricSpec } from '../../api/dashboards'
import type { Any } from '../../api/types'
import { ErrorBox, Modal } from '../common'
import { discoverFeedbackFields, discoverFields, getDotted } from '../traces/callUtils'
import { PanelChart } from './MetricChart'
import { TimeRangePicker } from './TimeRangePicker'
import { FIELD_PRESETS, GROUP_PRESETS, metricLabel, panelTitle, withChart } from './dashboardUtils'

const CHARTS: { id: ChartType; label: string }[] = [
  { id: 'line', label: 'Line' },
  { id: 'bar', label: 'Bar' },
  { id: 'stacked', label: 'Stacked' },
  { id: 'area', label: 'Area' },
  { id: 'table', label: 'Table' },
  { id: 'stat', label: 'Stat' },
  { id: 'scatter', label: 'Scatter' },
]

const AGG_NAMES: Record<MetricAgg, string> = {
  count: 'count',
  sum: 'sum',
  avg: 'average',
  min: 'min',
  max: 'max',
  p50: 'p50',
  p90: 'p90',
  p95: 'p95',
  p99: 'p99',
  error_rate: 'error rate',
}

/** Numeric and groupable fields seen on recent calls, for the field pickers. */
function useFieldSuggestions(projectId: string) {
  const sample = useQuery({
    queryKey: ['calls', projectId, 'field-sample'],
    queryFn: () => api.callsQuery({ project_id: projectId, limit: 200, include_feedback: true, sort_by: [{ field: 'started_at', direction: 'desc' }] }),
    staleTime: 60_000,
  })
  return useMemo(() => {
    const calls = sample.data?.calls ?? []
    const paths = [...discoverFields(calls, ['inputs', 'output', 'attributes']), ...discoverFeedbackFields(calls)]
    const numeric: string[] = []
    const text: string[] = []
    for (const p of paths) {
      let isNum = false
      let isText = false
      for (const c of calls) {
        let v: Any
        if (p.startsWith('feedback.')) {
          const rest = p.slice('feedback.'.length)
          const fb = (c.feedback ?? []).find((f) => rest.startsWith(f.feedback_type + '.') || rest === f.feedback_type)
          if (!fb) continue
          const sub = rest.slice(fb.feedback_type.length + 1)
          v = sub ? getDotted(fb.payload, sub) : fb.payload
        } else v = getDotted(c, p)
        if (typeof v === 'number' || typeof v === 'boolean') isNum = true
        else if (typeof v === 'string' && v.length < 80) isText = true
        if (isNum && isText) break
      }
      if (isNum) numeric.push(p)
      if (isText && !p.startsWith('feedback.')) text.push(p)
    }
    return { numeric, text }
  }, [sample.data])
}

function MetricRow({ m, onChange, onRemove, listId }: { m: MetricSpec; onChange: (m: MetricSpec) => void; onRemove?: () => void; listId: string }) {
  const needsField = m.agg !== 'count' && m.agg !== 'error_rate'
  return (
    <div className="dash-metric-row">
      <select className="input" aria-label="Aggregation" value={m.agg} onChange={(e) => onChange({ ...m, agg: e.target.value as MetricAgg, field: e.target.value === 'error_rate' ? undefined : m.field })}>
        {METRIC_AGGS.map((a) => (
          <option key={a} value={a}>
            {AGG_NAMES[a]}
          </option>
        ))}
      </select>
      <input
        className="input mono"
        list={listId}
        placeholder={needsField ? 'field, e.g. latency_ms' : 'of all calls'}
        aria-label="Field"
        value={m.agg === 'error_rate' ? '' : (m.field ?? '')}
        disabled={m.agg === 'error_rate'}
        onChange={(e) => onChange({ ...m, field: e.target.value || undefined })}
      />
      <button className="btn ghost sm icon" aria-label="Remove metric" disabled={!onRemove} onClick={onRemove}>
        ✕
      </button>
      <input className="input sm dash-metric-name" placeholder={`Series name (${metricLabel({ ...m, name: undefined })})`} aria-label="Series name" value={m.name ?? ''} onChange={(e) => onChange({ ...m, name: e.target.value || undefined })} />
    </div>
  )
}

/**
 * Edit one chart: metrics, chart type, group-by, filters and time range, with
 * a live preview. `header` adds controls above the form (e.g. a dashboard picker).
 */
export function PanelEditor({
  projectId,
  initial,
  dash,
  title,
  header,
  saveLabel = 'Save',
  saving,
  error,
  onSave,
  onClose,
}: {
  projectId: string
  initial: DashboardPanel
  dash?: Pick<DashboardVal, 'filter' | 'query' | 'time_range'>
  title: ReactNode
  header?: ReactNode
  saveLabel?: string
  saving?: boolean
  error?: unknown
  onSave: (p: DashboardPanel) => void
  onClose: () => void
}) {
  const [p, setP] = useState<DashboardPanel>(initial)
  const { numeric, text } = useFieldSuggestions(projectId)
  const ops = useQuery({ queryKey: ['calls', projectId, 'op_counts', false], queryFn: () => api.opCounts(projectId, false) })
  const opNames = useMemo(() => [...new Set((ops.data?.ops ?? []).map((o) => o.op_base_name))].sort(), [ops.data])
  const listId = 'dash-fields'
  const groupIsPreset = GROUP_PRESETS.some((g) => g.id === (p.group_by ?? ''))
  const [customGroup, setCustomGroup] = useState(!groupIsPreset)
  // The preview only refreshes once inputs settle into a valid request.
  const valid = p.metrics.every((m) => m.agg === 'count' || m.agg === 'error_rate' || !!m.field)
  // Debounce the preview so typing a field path doesn't fire a request per key.
  const [preview, setPreview] = useState(p)
  useEffect(() => {
    const t = setTimeout(() => setPreview(p), 400)
    return () => clearTimeout(t)
  }, [p])
  const setMetric = (i: number, m: MetricSpec) => setP({ ...p, metrics: p.metrics.map((x, j) => (j === i ? m : x)) })
  const op = p.filter?.op_names?.[0] ?? ''
  const roots = p.filter?.trace_roots_only
  return (
    <Modal
      wide
      title={title}
      onClose={onClose}
      footer={
        <>
          <ErrorBox error={error} />
          <span className="grow" />
          <button className="btn" onClick={onClose}>
            Cancel
          </button>
          <button className="btn primary" disabled={!valid || saving} onClick={() => onSave({ ...p, title: p.title.trim() })}>
            {saveLabel}
          </button>
        </>
      }
    >
      <datalist id={listId}>
        {FIELD_PRESETS.map((f) => (
          <option key={f.field} value={f.field}>
            {f.label}
          </option>
        ))}
        {numeric.map((f) => (
          <option key={f} value={f} />
        ))}
      </datalist>
      <datalist id="dash-group-fields">
        {text.map((f) => (
          <option key={f} value={f} />
        ))}
      </datalist>
      {header}
      <div className="dash-editor">
        <div className="col" style={{ gap: 12 }}>
          <label className="field">
            Title
            <input className="input" value={p.title} placeholder={panelTitle({ ...p, title: '' })} onChange={(e) => setP({ ...p, title: e.target.value })} />
          </label>
          <div className="field dash-field">
            Chart
            <div className="seg" role="group" aria-label="Chart type" style={{ flexWrap: 'wrap' }}>
              {CHARTS.map((c) => (
                <button key={c.id} className={p.chart === c.id ? 'on' : ''} aria-pressed={p.chart === c.id} onClick={() => setP(withChart(p, c.id))}>
                  {c.label}
                </button>
              ))}
            </div>
          </div>
          {p.chart === 'scatter' ? (
            <div className="field dash-field">
              Axes
              <div className="col" style={{ gap: 6 }}>
                {(['x', 'y'] as const).map((axis, i) => (
                  <label key={axis} className="row small" style={{ gap: 6 }}>
                    <span className="mono" style={{ width: 14 }}>
                      {axis}
                    </span>
                    <input
                      className="input mono grow"
                      list={listId}
                      aria-label={`${axis.toUpperCase()} field`}
                      placeholder={i === 0 ? 'latency_ms' : 'total_tokens'}
                      value={p.metrics[i]?.field ?? ''}
                      onChange={(e) => {
                        const ms = [...p.metrics]
                        while (ms.length < 2) ms.push({ agg: 'avg' })
                        ms[i] = { ...ms[i], agg: 'avg', field: e.target.value || undefined }
                        setP({ ...p, metrics: ms.slice(0, 2) })
                      }}
                    />
                  </label>
                ))}
                <div className="small faint">One point per call; click a point to open the call. Group by colours the points.</div>
              </div>
            </div>
          ) : (
          <div className="field dash-field">
            Metrics
            <div className="col" style={{ gap: 6 }}>
              {p.metrics.map((m, i) => (
                <MetricRow
                  key={i}
                  m={m}
                  listId={listId}
                  onChange={(x) => setMetric(i, x)}
                  onRemove={p.metrics.length > 1 ? () => setP({ ...p, metrics: p.metrics.filter((_, j) => j !== i) }) : undefined}
                />
              ))}
              <div>
                <button className="btn sm" disabled={p.metrics.length >= 6} onClick={() => setP({ ...p, metrics: [...p.metrics, { agg: 'avg', field: 'latency_ms' }] })}>
                  + Add metric
                </button>
              </div>
              <div className="small faint">
                Fields: latency_ms, total/prompt/completion tokens, cost, or any numeric <span className="mono">inputs.</span>, <span className="mono">output.</span>,{' '}
                <span className="mono">attributes.</span>, <span className="mono">summary.</span> or <span className="mono">feedback.&lt;type&gt;.</span> path.
              </div>
            </div>
          </div>
          )}
          {p.chart !== 'stat' && (
            <label className="field">
              Group by
              <div className="row">
                <select
                  className="input grow"
                  value={customGroup ? '__custom' : (p.group_by ?? '')}
                  onChange={(e) => {
                    if (e.target.value === '__custom') {
                      setCustomGroup(true)
                      setP({ ...p, group_by: '' })
                    } else {
                      setCustomGroup(false)
                      setP({ ...p, group_by: e.target.value })
                    }
                  }}
                >
                  {GROUP_PRESETS.map((g) => (
                    <option key={g.id} value={g.id}>
                      {g.label}
                    </option>
                  ))}
                  <option value="__custom">Field path…</option>
                </select>
                {customGroup && (
                  <input className="input mono grow" list="dash-group-fields" placeholder="attributes.env" value={p.group_by ?? ''} onChange={(e) => setP({ ...p, group_by: e.target.value })} aria-label="Group by field" />
                )}
              </div>
            </label>
          )}
          <div className="row wrap">
            <label className="field grow">
              Op
              <select
                className="input"
                value={op}
                onChange={(e) => setP({ ...p, filter: { ...p.filter, op_names: e.target.value ? [e.target.value] : undefined } })}
              >
                <option value="">All ops</option>
                {opNames.map((n) => (
                  <option key={n}>{n}</option>
                ))}
                {op && !opNames.includes(op) && <option>{op}</option>}
              </select>
            </label>
            <label className="field grow">
              Calls
              <select
                className="input"
                value={roots === undefined ? '' : roots ? 'roots' : 'all'}
                onChange={(e) => setP({ ...p, filter: { ...p.filter, trace_roots_only: e.target.value === '' ? undefined : e.target.value === 'roots' } })}
              >
                <option value="">{dash ? 'As dashboard' : 'Default'}</option>
                <option value="roots">Trace roots only</option>
                <option value="all">All calls</option>
              </select>
            </label>
          </div>
          <label className="field">
            Search
            <input className="input" placeholder="Free text over op, inputs, output" value={p.search ?? ''} onChange={(e) => setP({ ...p, search: e.target.value || undefined })} />
          </label>
          {p.query && (
            <div className="row small">
              <span className="filter-chip" title={JSON.stringify(p.query)}>
                <span className="filter-chip-label mono">query filter</span>
                <button className="filter-chip-x" aria-label="Remove query filter" onClick={() => setP({ ...p, query: undefined })}>
                  ✕
                </button>
              </span>
            </div>
          )}
          <div className="row wrap">
            {dash && (
              <label className="field grow">
                Time range
                <TimeRangePicker value={p.time_range ?? ''} onChange={(v) => setP({ ...p, time_range: v || undefined })} allowInherit />
              </label>
            )}
            <label className="field">
              Width
              <select className="input" value={p.width ?? 1} onChange={(e) => setP({ ...p, width: Number(e.target.value) as 1 | 2 | 3 })}>
                <option value={1}>Narrow</option>
                <option value={2}>Wide</option>
                <option value={3}>Full</option>
              </select>
            </label>
          </div>
        </div>
        <div className="dash-editor-preview">
          <div className="small muted" style={{ marginBottom: 6 }}>
            Preview · {panelTitle(p)}
          </div>
          {valid ? <PanelChart projectId={projectId} panel={preview} dash={dash ?? { time_range: '7d' }} height={260} /> : <div className="faint small">Choose a field for each metric.</div>}
        </div>
      </div>
    </Modal>
  )
}
