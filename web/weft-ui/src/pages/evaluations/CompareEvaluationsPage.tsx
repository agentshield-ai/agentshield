import { useMemo, useState } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { Bar, BarChart, CartesianGrid, Cell, ResponsiveContainer, Scatter, ScatterChart, Tooltip, XAxis, YAxis, ZAxis, Legend, ReferenceLine } from 'recharts'
import { api } from '../../api/client'
import type { Any, CompareEvaluation, CompareEvaluationsRes, CompareMetric, CompareRow } from '../../api/types'
import { Empty, ErrorBox, Loading, Pager, Section, StatusBadge } from '../../components/common'
import { ValueView } from '../../components/ValueView'
import { Drawer } from '../../components/objects/bits'
import { defaultLowerIsBetter, evalRunInfo, flattenScores, fmtMetric, seriesColor, sortMetricKeys, toNum, useEvalRuns } from '../../components/evals/evalUtils'
import { useProjectId } from '../../hooks/useProject'
import { fmtCost, fmtDuration, fmtNumber, fmtRelative, preview } from '../../lib/format'
import { callUrl, evaluationsUrl, refUrl } from '../../lib/urls'

const tooltipStyle = {
  contentStyle: { background: 'var(--panel)', border: '1px solid var(--border)', borderRadius: 6, fontSize: 12, color: 'var(--text)' },
  labelStyle: { color: 'var(--text-2)' },
  itemStyle: { color: 'var(--text)' },
}
const tick = { fill: 'var(--text-3)', fontSize: 11 }

/** Unique, readable label per evaluation run. */
function useLabels(evals: CompareEvaluation[]) {
  return useMemo(() => {
    const counts: Record<string, number> = {}
    evals.forEach((e) => (counts[e.model_name] = (counts[e.model_name] ?? 0) + 1))
    return Object.fromEntries(evals.map((e) => [e.call_id, counts[e.model_name] > 1 ? `${e.model_name} · ${e.call_id.slice(-4)}` : e.model_name || e.call_id.slice(-8)]))
  }, [evals])
}

function evalDurationMs(e: CompareEvaluation): number | null {
  if (!e.ended_at) return null
  return new Date(e.ended_at).getTime() - new Date(e.started_at).getTime()
}

/** Per-row aggregate (mean over trials) of flattened scores + latency/tokens. */
function rowAgg(rs: CompareRow['results'][string] | undefined): Record<string, number | boolean> {
  if (!rs?.length) return {}
  const sums: Record<string, { s: number; n: number; bool: boolean }> = {}
  for (const r of rs) {
    const flat: Record<string, number | boolean> = { ...flattenScores(r.scores ?? {}) }
    if (r.model_latency != null) flat.model_latency = r.model_latency
    if (r.total_tokens != null) flat.total_tokens = r.total_tokens
    for (const [k, v] of Object.entries(flat)) {
      const cur = (sums[k] ??= { s: 0, n: 0, bool: typeof v === 'boolean' })
      cur.s += toNum(v) ?? 0
      cur.n++
      if (typeof v !== 'boolean') cur.bool = false
    }
  }
  return Object.fromEntries(Object.entries(sums).map(([k, { s, n, bool }]) => [k, bool && n === 1 ? s === 1 : s / n]))
}

const NON_SCORE_KEYS = new Set(['model_latency', 'total_tokens'])

export function CompareEvaluationsPage() {
  const projectId = useProjectId()
  const [params, setParams] = useSearchParams()
  const ids = useMemo(() => (params.get('ids') ?? '').split(',').filter(Boolean), [params])
  const q = useQuery({
    queryKey: ['eval-compare', projectId, ids],
    queryFn: () => api.evaluationsCompare(projectId, ids),
    enabled: ids.length > 0,
    refetchInterval: (qq) => (qq.state.data?.evaluations.some((e) => e.status === 'running') ? 4000 : false),
  })
  const setIds = (next: string[]) => {
    const p = new URLSearchParams(params)
    p.set('ids', next.join(','))
    if (!next.includes(p.get('baseline') ?? '')) p.delete('baseline')
    setParams(p, { replace: true })
  }
  const baselineId = params.get('baseline') && ids.includes(params.get('baseline')!) ? params.get('baseline')! : ids[0]
  const setBaseline = (id: string) => {
    const p = new URLSearchParams(params)
    p.set('baseline', id)
    setParams(p, { replace: true })
  }

  if (ids.length === 0)
    return (
      <div className="page">
        <h1>Compare evaluations</h1>
        <div className="card">
          <Empty>
            Select runs on the <Link to={evaluationsUrl(projectId)}>Evaluations</Link> page to compare them.
          </Empty>
        </div>
      </div>
    )

  return (
    <div className="page">
      <div className="breadcrumbs">
        <Link to={evaluationsUrl(projectId)}>Evaluations</Link>
        <span>/</span>
        <span>Compare</span>
      </div>
      <div className="page-header">
        <h1>Compare evaluations</h1>
        <span className="muted small">{ids.length} runs</span>
        <span className="spacer" />
        <AddRun projectId={projectId} ids={ids} onAdd={(id) => setIds([...ids, id])} />
      </div>
      <ErrorBox error={q.error} />
      {q.isLoading ? <Loading label="Comparing…" /> : q.data ? <CompareBody data={q.data} projectId={projectId} baselineId={baselineId} setBaseline={setBaseline} onRemove={(id) => setIds(ids.filter((x) => x !== id))} /> : null}
    </div>
  )
}

function AddRun({ projectId, ids, onAdd }: { projectId: string; ids: string[]; onAdd: (id: string) => void }) {
  const runs = useEvalRuns(projectId, { limit: 100 })
  const options = (runs.data ?? []).filter((c) => !ids.includes(c.id))
  if (!options.length) return null
  return (
    <select className="input" value="" onChange={(e) => e.target.value && onAdd(e.target.value)} style={{ maxWidth: 280 }}>
      <option value="">+ Add run to comparison…</option>
      {options.map((c) => {
        const i = evalRunInfo(c)
        return (
          <option key={c.id} value={c.id}>
            {i.evalName} · {i.modelName} · {fmtRelative(c.started_at)}
          </option>
        )
      })}
    </select>
  )
}

function CompareBody({
  data,
  projectId,
  baselineId,
  setBaseline,
  onRemove,
}: {
  data: CompareEvaluationsRes
  projectId: string
  baselineId: string
  setBaseline: (id: string) => void
  onRemove: (id: string) => void
}) {
  const evals = data.evaluations
  const labels = useLabels(evals)
  const colorOf = useMemo(() => Object.fromEntries(evals.map((e, i) => [e.call_id, seriesColor(i)])), [evals])
  const baseline = evals.find((e) => e.call_id === baselineId) ?? evals[0]
  const [lowerOverrides, setLowerOverrides] = useState<Record<string, boolean>>({})
  const lowerBetter = (key: string) => lowerOverrides[key] ?? defaultLowerIsBetter(key)
  const metrics = useMemo(() => {
    const order = sortMetricKeys(data.metrics.map((m) => m.key))
    return order.map((k) => data.metrics.find((m) => m.key === k)!)
  }, [data.metrics])

  if (!evals.length)
    return (
      <div className="card">
        <Empty>None of these runs were found.</Empty>
      </div>
    )
  const datasets = new Set(evals.map((e) => e.dataset_ref).filter(Boolean))

  return (
    <>
      {datasets.size > 1 && (
        <div className="error-box" style={{ background: 'var(--warn-bg)', color: 'var(--warn)' }}>
          These runs use different datasets, so per-example rows may not line up.
        </div>
      )}
      <div className="card-grid">
        {evals.map((e) => {
          const isBase = e.call_id === baseline?.call_id
          const modelUrl = e.model_ref ? refUrl(e.model_ref) : null
          const evalUrl = e.evaluation_ref ? refUrl(e.evaluation_ref) : null
          const ml = toNum((e.summary as Any)?.model_latency?.mean)
          return (
            <div key={e.call_id} className={`eval-card ${isBase ? 'baseline' : ''}`}>
              <div className="row" style={{ minWidth: 0 }}>
                <span className="swatch" style={{ background: colorOf[e.call_id] }} />
                <b className="truncate grow" title={labels[e.call_id]}>
                  {modelUrl ? <Link to={modelUrl}>{labels[e.call_id]}</Link> : labels[e.call_id]}
                </b>
                {evals.length > 1 && (
                  <button className="btn ghost sm icon" title="Remove from comparison" onClick={() => onRemove(e.call_id)}>
                    ✕
                  </button>
                )}
              </div>
              <div className="small muted truncate">
                {evalUrl ? <Link to={evalUrl}>{e.evaluation_name}</Link> : e.evaluation_name} · {fmtRelative(e.started_at)}
              </div>
              <div className="row wrap small" style={{ gap: 10 }}>
                <StatusBadge status={e.status} />
                <span className="muted">{fmtNumber(e.row_count)} rows</span>
                <span className="muted">{fmtNumber(e.total_tokens)} tok</span>
                <span className="muted">{fmtCost(e.cost_usd)}</span>
              </div>
              <div className="row wrap small" style={{ gap: 10 }}>
                <span className="muted">Duration {fmtDuration(evalDurationMs(e))}</span>
                {ml != null && <span className="muted">Model latency {fmtDuration(ml * 1000)}</span>}
              </div>
              <div className="row">
                {isBase ? (
                  <span className="badge accent">Baseline</span>
                ) : (
                  <button className="btn sm" onClick={() => setBaseline(e.call_id)}>
                    Set as baseline
                  </button>
                )}
                <span className="grow" />
                <Link className="btn sm ghost" to={callUrl(projectId, e.call_id)}>
                  Open run →
                </Link>
              </div>
            </div>
          )
        })}
      </div>
      <MetricsSection
        evals={evals}
        metrics={metrics}
        labels={labels}
        colorOf={colorOf}
        baselineId={baseline.call_id}
        lowerBetter={lowerBetter}
        toggleLower={(k) => setLowerOverrides((o) => ({ ...o, [k]: !lowerBetter(k) }))}
      />
      {data.rows.length > 0 && <ScatterSection data={data} labels={labels} colorOf={colorOf} baselineId={baseline.call_id} />}
      <RowsSection data={data} projectId={projectId} labels={labels} colorOf={colorOf} baselineId={baseline.call_id} metrics={metrics} lowerBetter={lowerBetter} />
    </>
  )
}

// ---------------------------------------------------------------- metrics

function Delta({ v, base, lower, kind }: { v: number | null; base: number | null; lower: boolean; kind: CompareMetric['kind'] }) {
  if (v == null || base == null) return null
  const d = v - base
  if (Math.abs(d) < 1e-12) return <span className="delta-same">±0</span>
  const good = lower ? d < 0 : d > 0
  const txt = kind === 'fraction' ? `${d > 0 ? '+' : ''}${(d * 100).toFixed(1)}pp` : `${d > 0 ? '+' : ''}${fmtMetric(d)}`
  const rel = base !== 0 && kind !== 'fraction' ? ` (${d > 0 ? '+' : ''}${((d / Math.abs(base)) * 100).toFixed(0)}%)` : ''
  return (
    <span className={good ? 'delta-up' : 'delta-down'}>
      {good ? '▲' : '▼'} {txt}
      {rel}
    </span>
  )
}

function fmtMetricValue(v: unknown, kind: CompareMetric['kind']) {
  if (kind === 'fraction' && typeof v === 'number') return `${(v * 100).toFixed(1)}%`
  return fmtMetric(v)
}

function MetricsSection({
  evals,
  metrics,
  labels,
  colorOf,
  baselineId,
  lowerBetter,
  toggleLower,
}: {
  evals: CompareEvaluation[]
  metrics: CompareMetric[]
  labels: Record<string, string>
  colorOf: Record<string, string>
  baselineId: string
  lowerBetter: (k: string) => boolean
  toggleLower: (k: string) => void
}) {
  const [showCharts, setShowCharts] = useState(true)
  const keys = new Set(metrics.map((m) => m.key))
  const numeric = metrics.filter(
    (m) => Object.values(m.values).some((v) => toNum(v) != null) && !(m.key.endsWith('.true_count') && keys.has(m.key.replace(/true_count$/, 'true_fraction'))),
  )
  if (!metrics.length)
    return (
      <Section title="Summary metrics">
        <div className="faint">These runs have no summary metrics yet.</div>
      </Section>
    )
  return (
    <Section
      title="Summary metrics"
      actions={
        <label className="checkbox small">
          <input type="checkbox" checked={showCharts} onChange={(e) => setShowCharts(e.target.checked)} /> Charts
        </label>
      }
    >
      <div className="col" style={{ gap: 14 }}>
        {showCharts && (
          <div className="card-grid" style={{ gridTemplateColumns: 'repeat(auto-fill, minmax(220px, 1fr))' }}>
            {numeric.slice(0, 12).map((m) => {
              const rows = evals.map((e) => ({ id: e.call_id, name: labels[e.call_id], value: toNum(m.values[e.call_id]) }))
              const best = rows.reduce<number | null>((b, r) => (r.value == null ? b : b == null ? r.value : lowerBetter(m.key) ? Math.min(b, r.value) : Math.max(b, r.value)), null)
              return (
                <div key={m.key} className="eval-card" style={{ gap: 2 }}>
                  <div className="row small">
                    <b className="truncate grow mono" title={m.key}>
                      {m.key}
                    </b>
                    <span className="faint">{lowerBetter(m.key) ? '↓ better' : '↑ better'}</span>
                  </div>
                  <div style={{ height: 110 }}>
                    <ResponsiveContainer width="100%" height="100%">
                      <BarChart data={rows} margin={{ top: 6, right: 4, left: 0, bottom: 0 }}>
                        <CartesianGrid vertical={false} stroke="var(--border)" />
                        <XAxis dataKey="name" tick={false} axisLine={{ stroke: 'var(--border)' }} height={4} />
                        <YAxis tick={tick} axisLine={false} tickLine={false} width={40} domain={m.kind === 'fraction' ? [0, 1] : [0, 'auto']} tickFormatter={(v: number) => (m.kind === 'fraction' ? `${Math.round(v * 100)}%` : fmtMetric(v))} />
                        <Tooltip {...tooltipStyle} cursor={{ fill: 'var(--panel-2)' }} formatter={(v: number) => fmtMetricValue(v, m.kind)} />
                        <Bar dataKey="value" radius={[3, 3, 0, 0]} isAnimationActive={false}>
                          {rows.map((r) => (
                            <Cell key={r.id} fill={colorOf[r.id]} fillOpacity={r.value === best ? 1 : 0.55} />
                          ))}
                        </Bar>
                      </BarChart>
                    </ResponsiveContainer>
                  </div>
                </div>
              )
            })}
          </div>
        )}
        <div className="table-wrap">
          <table className="table">
            <thead>
              <tr>
                <th>Metric</th>
                <th title="Click to flip whether lower values are better">Better</th>
                {evals.map((e) => (
                  <th key={e.call_id} className="num">
                    <span className="row" style={{ justifyContent: 'flex-end', gap: 6 }}>
                      <span className="swatch" style={{ background: colorOf[e.call_id] }} />
                      <span className="truncate" style={{ maxWidth: 160 }}>
                        {labels[e.call_id]}
                      </span>
                      {e.call_id === baselineId && <span className="badge accent">base</span>}
                    </span>
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {metrics.map((m) => {
                const base = toNum(m.values[baselineId])
                const lower = lowerBetter(m.key)
                const nums = evals.map((e) => toNum(m.values[e.call_id])).filter((x): x is number => x != null)
                const best = nums.length > 1 ? (lower ? Math.min(...nums) : Math.max(...nums)) : null
                return (
                  <tr key={m.key}>
                    <td className="mono small">{m.key}</td>
                    <td>
                      <button className="btn ghost sm" onClick={() => toggleLower(m.key)} title="Toggle direction">
                        {lower ? '↓ lower' : '↑ higher'}
                      </button>
                    </td>
                    {evals.map((e) => {
                      const raw = m.values[e.call_id]
                      const v = toNum(raw)
                      return (
                        <td key={e.call_id} className={`num ${best != null && v === best ? 'best' : ''}`}>
                          <div className="mono">{fmtMetricValue(raw, m.kind)}</div>
                          {e.call_id !== baselineId && <Delta v={v} base={base} lower={lower} kind={m.kind} />}
                        </td>
                      )
                    })}
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      </div>
    </Section>
  )
}

// ---------------------------------------------------------------- scatter

function ScatterSection({
  data,
  labels,
  colorOf,
  baselineId,
}: {
  data: CompareEvaluationsRes
  labels: Record<string, string>
  colorOf: Record<string, string>
  baselineId: string
}) {
  const evals = data.evaluations
  const aggs = useMemo(() => data.rows.map((r) => Object.fromEntries(evals.map((e) => [e.call_id, rowAgg(r.results[e.call_id])]))), [data.rows, evals])
  const keys = useMemo(() => {
    const s = new Set<string>()
    aggs.forEach((a) => Object.values(a).forEach((x) => Object.keys(x).forEach((k) => s.add(k))))
    return sortMetricKeys([...s])
  }, [aggs])
  const boolKeys = useMemo(() => {
    const s = new Set<string>()
    aggs.forEach((a) => Object.values(a).forEach((x) => Object.entries(x).forEach(([k, v]) => typeof v === 'boolean' && s.add(k))))
    return s
  }, [aggs])
  // prefer continuous scores: boolean scatter plots are mostly overlapping points
  const scoreKeys = [...keys.filter((k) => !NON_SCORE_KEYS.has(k) && !boolKeys.has(k)), ...keys.filter((k) => !NON_SCORE_KEYS.has(k) && boolKeys.has(k))]
  const [mode, setMode] = useState<'metrics' | 'models'>(evals.length > 1 ? 'models' : 'metrics')
  const [xKey, setXKey] = useState<string>(scoreKeys[0] ?? keys[0] ?? '')
  const [yKey, setYKey] = useState<string>(scoreKeys[1] ?? (keys.includes('model_latency') ? 'model_latency' : (keys[1] ?? keys[0] ?? '')))
  const [challenger, setChallenger] = useState<string>(evals.find((e) => e.call_id !== baselineId)?.call_id ?? '')
  if (!keys.length) return null
  const challengerId = evals.some((e) => e.call_id === challenger && e.call_id !== baselineId) ? challenger : (evals.find((e) => e.call_id !== baselineId)?.call_id ?? '')

  const keySelect = (v: string, set: (s: string) => void) => (
    <select className="input" value={v} onChange={(e) => set(e.target.value)} style={{ height: 26, fontSize: 12 }}>
      {keys.map((k) => (
        <option key={k}>{k}</option>
      ))}
    </select>
  )

  // jitter booleans slightly so overlapping points stay visible
  const jitter = (v: number | boolean | undefined, i: number) => {
    const n = toNum(v)
    if (n == null) return null
    return typeof v === 'boolean' ? n + (((i * 7919) % 100) / 100 - 0.5) * 0.12 : n
  }

  let series: { id: string; name: string; color: string; points: { x: number; y: number; row: number }[] }[] = []
  let xLabel = xKey
  let yLabel = yKey
  if (mode === 'metrics' || !challengerId) {
    series = evals.map((e) => ({
      id: e.call_id,
      name: labels[e.call_id],
      color: colorOf[e.call_id],
      points: aggs
        .map((a, i) => ({ x: jitter(a[e.call_id]?.[xKey], i), y: jitter(a[e.call_id]?.[yKey], i + 31), row: i }))
        .filter((p): p is { x: number; y: number; row: number } => p.x != null && p.y != null),
    }))
  } else {
    xLabel = `${xKey} · ${labels[baselineId]}`
    yLabel = `${xKey} · ${labels[challengerId]}`
    series = [
      {
        id: challengerId,
        name: `${labels[challengerId]} vs ${labels[baselineId]}`,
        color: colorOf[challengerId],
        points: aggs
          .map((a, i) => ({ x: jitter(a[baselineId]?.[xKey], i), y: jitter(a[challengerId]?.[xKey], i + 31), row: i }))
          .filter((p): p is { x: number; y: number; row: number } => p.x != null && p.y != null),
      },
    ]
  }
  const total = series.reduce((n, s) => n + s.points.length, 0)
  const paired = mode === 'models' && !!challengerId
  let shared: [number, number] | null = null
  if (paired && total) {
    const [a, b] = diagonal(series)
    const pad = (b.x - a.x || 1) * 0.05
    shared = [a.x - pad, b.x + pad]
  }
  const dom = (shared ?? ['auto', 'auto']) as [number, number]

  return (
    <Section
      title="Per-example scatter"
      actions={
        <div className="toolbar">
          {evals.length > 1 && (
            <div className="seg">
              <button className={mode === 'models' ? 'on' : ''} onClick={() => setMode('models')}>
                Baseline vs challenger
              </button>
              <button className={mode === 'metrics' ? 'on' : ''} onClick={() => setMode('metrics')}>
                Metric vs metric
              </button>
            </div>
          )}
        </div>
      }
    >
      <div className="toolbar small" style={{ marginBottom: 8 }}>
        {mode === 'metrics' || !challengerId ? (
          <>
            <span className="muted">X</span>
            {keySelect(xKey, setXKey)}
            <span className="muted">Y</span>
            {keySelect(yKey, setYKey)}
          </>
        ) : (
          <>
            <span className="muted">Metric</span>
            {keySelect(xKey, setXKey)}
            <span className="muted">Challenger</span>
            <select className="input" value={challengerId} onChange={(e) => setChallenger(e.target.value)} style={{ height: 26, fontSize: 12 }}>
              {evals
                .filter((e) => e.call_id !== baselineId)
                .map((e) => (
                  <option key={e.call_id} value={e.call_id}>
                    {labels[e.call_id]}
                  </option>
                ))}
            </select>
            <span className="hint">Points above the diagonal: challenger scored higher.</span>
          </>
        )}
      </div>
      {total === 0 ? (
        <div className="faint">No numeric values for this selection.</div>
      ) : (
        <div style={{ height: 320 }}>
          <div className="hint mono">↑ {yLabel}</div>
          <ResponsiveContainer width="100%" height="94%">
            <ScatterChart margin={{ top: 10, right: 16, bottom: 24, left: 0 }}>
              <CartesianGrid stroke="var(--border)" />
              <XAxis type="number" dataKey="x" name={xLabel} tick={tick} stroke="var(--border)" label={{ value: xLabel, position: 'insideBottom', offset: -12, fill: 'var(--text-3)', fontSize: 11 }} domain={dom} allowDataOverflow={!!shared} tickFormatter={(v: number) => fmtMetric(v)} />
              <YAxis type="number" dataKey="y" name={yLabel} tick={tick} stroke="var(--border)" width={48} domain={dom} allowDataOverflow={!!shared} tickFormatter={(v: number) => fmtMetric(v)} />
              <ZAxis range={[36, 36]} />
              <Tooltip {...tooltipStyle} formatter={(v: number) => fmtMetric(v)} labelFormatter={() => ''} />
              {mode === 'models' && challengerId && <ReferenceLine segment={diagonal(series)} stroke="var(--text-3)" strokeDasharray="4 4" />}
              {series.length > 1 && <Legend wrapperStyle={{ fontSize: 12 }} />}
              {series.map((s) => (
                <Scatter key={s.id} name={s.name} data={s.points} fill={s.color} fillOpacity={0.7} isAnimationActive={false} />
              ))}
            </ScatterChart>
          </ResponsiveContainer>
        </div>
      )}
    </Section>
  )
}

function diagonal(series: { points: { x: number; y: number }[] }[]): [{ x: number; y: number }, { x: number; y: number }] {
  const xs = series.flatMap((s) => s.points.flatMap((p) => [p.x, p.y]))
  const lo = Math.min(...xs)
  const hi = Math.max(...xs)
  return [
    { x: lo, y: lo },
    { x: hi, y: hi },
  ]
}

// ---------------------------------------------------------------- rows

type RowFilter = 'all' | 'disagree' | 'regressed' | 'improved'
const PAGE = 50

function RowsSection({
  data,
  projectId,
  labels,
  colorOf,
  baselineId,
  metrics,
  lowerBetter,
}: {
  data: CompareEvaluationsRes
  projectId: string
  labels: Record<string, string>
  colorOf: Record<string, string>
  baselineId: string
  metrics: CompareMetric[]
  lowerBetter: (k: string) => boolean
}) {
  const evals = data.evaluations
  const [filter, setFilter] = useState<RowFilter>('all')
  const [search, setSearch] = useState('')
  const [offset, setOffset] = useState(0)
  const [open, setOpen] = useState<number | null>(null)

  /** Direction for a row-level score key, borrowing the summary metric's toggle. */
  const lowerFor = (k: string) => {
    const m = metrics.find((mm) => mm.key === k || mm.key.startsWith(k + '.'))
    return m ? lowerBetter(m.key) : defaultLowerIsBetter(k)
  }

  const enriched = useMemo(
    () =>
      data.rows.map((r, idx) => {
        const aggs = Object.fromEntries(evals.map((e) => [e.call_id, rowAgg(r.results[e.call_id])]))
        const outputs = evals.map((e) => JSON.stringify(r.results[e.call_id]?.[0]?.output ?? null))
        const keys = new Set<string>()
        Object.values(aggs).forEach((a) => Object.keys(a).forEach((k) => !NON_SCORE_KEYS.has(k) && keys.add(k)))
        const scoreKeys = sortMetricKeys([...keys])
        const scoreDiffer = scoreKeys.some((k) => new Set(evals.map((e) => JSON.stringify(aggs[e.call_id]?.[k] ?? null))).size > 1)
        let regressed = false
        let improved = false
        for (const e of evals) {
          if (e.call_id === baselineId) continue
          for (const k of scoreKeys) {
            const b = toNum(aggs[baselineId]?.[k])
            const v = toNum(aggs[e.call_id]?.[k])
            if (b == null || v == null || Math.abs(v - b) < 1e-12) continue
            const better = lowerFor(k) ? v < b : v > b
            if (better) improved = true
            else regressed = true
          }
        }
        return { r, idx, aggs, scoreKeys, disagree: scoreDiffer || new Set(outputs).size > 1, regressed, improved, text: JSON.stringify(r.inputs).toLowerCase() }
      }),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [data.rows, evals, baselineId, metrics, lowerBetter],
  )
  const counts = {
    all: enriched.length,
    disagree: enriched.filter((x) => x.disagree).length,
    regressed: enriched.filter((x) => x.regressed).length,
    improved: enriched.filter((x) => x.improved).length,
  }
  const rows = enriched.filter((x) => (filter === 'all' || x[filter]) && (!search || x.text.includes(search.toLowerCase())))
  const shown = rows.slice(offset, offset + PAGE)
  const opened = open != null ? enriched[open] : null

  return (
    <Section
      title={`Examples (${data.rows.length})`}
      actions={
        <input
          className="input"
          style={{ height: 26, width: 200, maxWidth: '40vw' }}
          placeholder="Search inputs…"
          value={search}
          onChange={(e) => {
            setSearch(e.target.value)
            setOffset(0)
          }}
        />
      }
    >
      <div className="col" style={{ gap: 10 }}>
        <div className="seg" style={{ alignSelf: 'flex-start', flexWrap: 'wrap' }}>
          {(['all', 'disagree', 'regressed', 'improved'] as RowFilter[])
            .filter((f) => f === 'all' || evals.length > 1)
            .map((f) => (
              <button
                key={f}
                className={filter === f ? 'on' : ''}
                onClick={() => {
                  setFilter(f)
                  setOffset(0)
                }}
              >
                {f === 'all' ? 'All' : f === 'disagree' ? 'Disagree' : f === 'regressed' ? 'Regressed vs baseline' : 'Improved vs baseline'} ({counts[f]})
              </button>
            ))}
        </div>
        {rows.length === 0 ? (
          <div className="faint">{data.rows.length === 0 ? 'No per-example results were recorded for these runs.' : 'No examples match.'}</div>
        ) : (
          <div className="table-wrap" style={{ maxHeight: '75vh' }}>
            <table className="table">
              <thead>
                <tr>
                  <th className="num" style={{ width: 36 }}>
                    #
                  </th>
                  <th style={{ minWidth: 200 }}>Inputs</th>
                  {evals.map((e) => (
                    <th key={e.call_id} style={{ minWidth: 220 }}>
                      <span className="row" style={{ gap: 6 }}>
                        <span className="swatch" style={{ background: colorOf[e.call_id] }} />
                        <span className="truncate" style={{ maxWidth: 200 }}>
                          {labels[e.call_id]}
                        </span>
                        {e.call_id === baselineId && <span className="badge accent">base</span>}
                      </span>
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {shown.map((x) => (
                  <tr key={x.r.row_key + x.idx} className="clickable" onClick={() => setOpen(x.idx)}>
                    <td className="num faint small">{x.idx + 1}</td>
                    <td>
                      <InputsPreview inputs={x.r.inputs} />
                    </td>
                    {evals.map((e) => {
                      const res = x.r.results[e.call_id]
                      return (
                        <td key={e.call_id}>
                          {!res?.length ? (
                            <span className="faint small">no result</span>
                          ) : (
                            <div className="col" style={{ gap: 4 }}>
                              <span className="small" style={{ display: '-webkit-box', WebkitLineClamp: 3, WebkitBoxOrient: 'vertical', overflow: 'hidden', wordBreak: 'break-word' }}>
                                {preview(res[0].output, 240) || <span className="faint">empty</span>}
                              </span>
                              <ScoreChips aggs={x.aggs} id={e.call_id} baselineId={baselineId} keys={x.scoreKeys} lowerFor={lowerFor} />
                              {res.length > 1 && <span className="hint">{res.length} trials (mean)</span>}
                            </div>
                          )}
                        </td>
                      )
                    })}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        {rows.length > PAGE && <Pager offset={offset} limit={PAGE} total={rows.length} onChange={setOffset} />}
      </div>
      {opened && (
        <Drawer
          title={`Example ${opened.idx + 1}`}
          onClose={() => setOpen(null)}
          actions={
            <>
              <button className="btn sm" disabled={opened.idx === 0} onClick={() => setOpen(opened.idx - 1)}>
                ‹
              </button>
              <button className="btn sm" disabled={opened.idx >= enriched.length - 1} onClick={() => setOpen(opened.idx + 1)}>
                ›
              </button>
            </>
          }
        >
          <div>
            <h3 style={{ marginBottom: 6 }}>Inputs</h3>
            <ValueView value={opened.r.inputs} projectId={projectId} />
          </div>
          {evals.map((e) => {
            const res = opened.r.results[e.call_id] ?? []
            return (
              <div key={e.call_id} className="card">
                <div className="card-header">
                  <span className="swatch" style={{ background: colorOf[e.call_id] }} />
                  <b className="grow truncate">{labels[e.call_id]}</b>
                  {e.call_id === baselineId && <span className="badge accent">baseline</span>}
                </div>
                <div className="card-body col" style={{ gap: 10 }}>
                  {res.length === 0 && <span className="faint">No result for this example.</span>}
                  {res.map((t, ti) => (
                    <div key={t.call_id || ti} className="col" style={{ gap: 6 }}>
                      <div className="row small muted wrap">
                        {res.length > 1 && <span className="badge">trial {t.trial}</span>}
                        <span>latency {t.model_latency != null ? fmtDuration(t.model_latency * 1000) : '—'}</span>
                        <span>{fmtNumber(t.total_tokens)} tokens</span>
                        <span className="grow" />
                        {t.call_id && <Link to={callUrl(projectId, t.call_id)}>predict_and_score call →</Link>}
                      </div>
                      <div>
                        <div className="small muted">Output</div>
                        <ValueView value={t.output} projectId={projectId} markdown />
                      </div>
                      <div>
                        <div className="small muted">Scores</div>
                        <ScoreChips aggs={{ [e.call_id]: rowAgg([t]), [baselineId]: opened.aggs[baselineId] }} id={e.call_id} baselineId={baselineId} keys={opened.scoreKeys} lowerFor={lowerFor} />
                        <details style={{ marginTop: 4 }}>
                          <summary className="small faint" style={{ cursor: 'pointer' }}>
                            Raw scores
                          </summary>
                          <ValueView value={t.scores} projectId={projectId} />
                        </details>
                      </div>
                    </div>
                  ))}
                </div>
              </div>
            )
          })}
        </Drawer>
      )}
    </Section>
  )
}

function InputsPreview({ inputs }: { inputs: Any }) {
  if (!inputs || typeof inputs !== 'object' || Array.isArray(inputs))
    return <span className="small" style={{ wordBreak: 'break-word' }}>{preview(inputs, 200)}</span>
  const entries = Object.entries(inputs).slice(0, 4)
  return (
    <div className="col" style={{ gap: 2 }}>
      {entries.map(([k, v]) => (
        <div key={k} className="small" style={{ display: '-webkit-box', WebkitLineClamp: 2, WebkitBoxOrient: 'vertical', overflow: 'hidden', wordBreak: 'break-word' }}>
          <span className="faint mono">{k}: </span>
          {preview(v, 160)}
        </div>
      ))}
    </div>
  )
}

function ScoreChips({
  aggs,
  id,
  baselineId,
  keys,
  lowerFor,
}: {
  aggs: Record<string, Record<string, number | boolean>>
  id: string
  baselineId: string
  keys: string[]
  lowerFor: (k: string) => boolean
}) {
  const a = aggs[id] ?? {}
  const b = aggs[baselineId] ?? {}
  const present = keys.filter((k) => a[k] !== undefined)
  if (!present.length) return null
  return (
    <span className="chips">
      {present.map((k) => {
        const v = a[k]
        const nv = toNum(v)
        const nb = toNum(b[k])
        let cls = ''
        if (id !== baselineId && nv != null && nb != null && Math.abs(nv - nb) > 1e-12) cls = (lowerFor(k) ? nv < nb : nv > nb) ? 'success' : 'error'
        else if (typeof v === 'boolean') cls = v ? 'success' : 'error'
        return (
          <span key={k} className={`badge ${cls}`} title={`${k}: ${fmtMetric(v)}${id !== baselineId && nb != null ? ` (baseline ${fmtMetric(b[k])})` : ''}`}>
            <span style={{ opacity: 0.75 }}>{k}</span> {typeof v === 'boolean' ? (v ? '✓' : '✗') : fmtMetric(v)}
          </span>
        )
      })}
    </span>
  )
}
