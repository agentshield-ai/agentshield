import { useMemo, useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { CartesianGrid, Legend, Line, LineChart, ResponsiveContainer, Tooltip, XAxis, YAxis } from 'recharts'
import { api, ApiError } from '../../api/client'
import { parseRef } from '../../api/refs'
import type { Feedback } from '../../api/types'
import { Empty, ErrorBox, Loading, Section, Spinner } from '../../components/common'
import { RefChip } from '../../components/RefChip'
import { JsonBlock } from '../../components/ValueView'
import { DEFAULT_IDLE_SECONDS, MONITOR_SCOPES, MonitorEditor, useToggleMonitor } from '../../components/evals/MonitorEditor'
import { flattenScores, fmtMetric, objectRefPrefix, seriesColor, toNum } from '../../components/evals/evalUtils'
import { Stat, Switch } from '../../components/objects/bits'
import { useProjectId } from '../../hooks/useProject'
import { fmtDate, fmtRelative, preview } from '../../lib/format'
import { contains, toQuery } from '../../lib/query'
import { callUrl, objectUrl, objectVersionsUrl } from '../../lib/urls'

const RUNNABLE_PREFIX = 'weft.scorer.'

const scorerOf = (f: Feedback) => (f.feedback_type.startsWith(RUNNABLE_PREFIX) ? f.feedback_type.slice(RUNNABLE_PREFIX.length) : f.feedback_type)

export function MonitorPage() {
  const projectId = useProjectId()
  const { objectId = '' } = useParams()
  const [editing, setEditing] = useState(false)
  const [scorerFilter, setScorerFilter] = useState('')
  const obj = useQuery({
    queryKey: ['obj', projectId, objectId, 'latest'],
    queryFn: () => api.objRead(projectId, objectId, 'latest').then((r) => r.obj),
    retry: (n, e) => !(e instanceof ApiError && e.status === 404) && n < 1,
  })
  const results = useQuery({
    queryKey: ['monitor-feedback', projectId, objectId],
    queryFn: () =>
      api
        .feedbackQuery({
          project_id: projectId,
          query: toQuery(contains('trigger_ref', objectRefPrefix(projectId, objectId), false)),
          sort_by: [{ field: 'created_at', direction: 'desc' }],
          limit: 1000,
        })
        .then((r) => r.result.filter((f) => parseRef(f.trigger_ref ?? '')?.name === objectId)),
    refetchInterval: 15_000,
  })
  const { toggle, pending } = useToggleMonitor(projectId)

  if (obj.isLoading) return <Loading />
  if (obj.error || !obj.data)
    return (
      <div className="page">
        <div className="breadcrumbs">
          <Link to={`/${projectId}/monitors`}>Monitors</Link> <span>/</span> <span>{objectId}</span>
        </div>
        {obj.error instanceof ApiError && obj.error.status === 404 ? (
          <div className="card">
            <Empty>Monitor “{objectId}” was not found.</Empty>
          </div>
        ) : (
          <ErrorBox error={obj.error ?? 'Not found'} />
        )}
      </div>
    )

  const o = obj.data
  const v = o.val ?? {}
  const fb = results.data ?? []
  const scorerNames = [...new Set(fb.map(scorerOf))]
  const shown = scorerFilter ? fb.filter((f) => scorerOf(f) === scorerFilter) : fb

  return (
    <div className="page">
      <div className="breadcrumbs">
        <Link to={`/${projectId}/monitors`}>Monitors</Link>
        <span>/</span>
        <span>{objectId}</span>
      </div>
      <div className="page-header">
        <Switch on={!!v.active} disabled={pending === o.object_id} onChange={() => void toggle(o)} title={v.active ? 'Pause monitor' : 'Activate monitor'} />
        <h1 className="truncate">{objectId}</h1>
        <span className={`badge ${v.active ? 'success' : ''}`}>{v.active ? 'Active' : 'Paused'}</span>
        <span className="spacer" />
        <Link className="btn ghost" to={objectVersionsUrl(projectId, objectId)}>
          v{o.version_index} · history
        </Link>
        <button className="btn primary" onClick={() => setEditing(true)}>
          Edit
        </button>
      </div>
      {v.description && <p className="muted" style={{ margin: 0 }}>{v.description}</p>}

      <div className="grid-2">
        <Section title="Configuration">
          <div className="kv">
            <div className="k">ops</div>
            <div className="v chips">
              {(v.op_names ?? []).length ? (
                (v.op_names as string[]).map((n) => (
                  <span key={n} className="chip mono">
                    {n}
                  </span>
                ))
              ) : (
                <span className="faint">all root calls</span>
              )}
            </div>
            <div className="k">sampling</div>
            <div className="v">
              <div className="row">
                <div className="progress grow" style={{ maxWidth: 160 }}>
                  <span style={{ width: `${(v.sampling_rate ?? 1) * 100}%` }} />
                </div>
                <span className="mono">{Math.round((v.sampling_rate ?? 1) * 100)}%</span>
              </div>
            </div>
            <div className="k">scores</div>
            <div className="v">
              {Array.isArray(v.scopes) && v.scopes.length ? (
                (v.scopes as string[]).map((s) => MONITOR_SCOPES.find((x) => x.id === s)?.label ?? s).join(', ')
              ) : (
                <span className="faint">each scorer where it fits (sessions for session judges, calls otherwise)</span>
              )}
              <span className="faint small"> · a session is complete after {v.session_idle_seconds ?? DEFAULT_IDLE_SECONDS}s without new spans</span>
            </div>
            <div className="k">scorers</div>
            <div className="v chips">
              {(v.scorers ?? []).map((s: string) => (
                <RefChip key={s} refStr={s} />
              ))}
            </div>
            <div className="k">filter</div>
            <div className="v">{v.query ? <JsonBlock value={v.query} /> : <span className="faint">none</span>}</div>
            <div className="k">updated</div>
            <div className="v" title={fmtDate(o.created_at)}>
              {fmtRelative(o.created_at)} · <Link to={objectUrl(projectId, objectId, o.digest)}>object</Link>
            </div>
          </div>
        </Section>
        <Section title="Activity">
          {results.isLoading ? (
            <Spinner />
          ) : (
            <div className="stat-grid">
              <Stat label="Scores" value={fb.length >= 1000 ? '1000+' : fb.length.toLocaleString()} />
              <Stat label="Calls scored" value={new Set(fb.map((f) => f.ref)).size.toLocaleString()} />
              <Stat label="Last score" value={fb[0] ? fmtRelative(fb[0].created_at) : '—'} title={fb[0] ? fmtDate(fb[0].created_at) : undefined} />
              {scorerNames.slice(0, 3).map((s) => (
                <Stat key={s} label={s} value={summarize(fb.filter((f) => scorerOf(f) === s))} />
              ))}
            </div>
          )}
        </Section>
      </div>

      <ScoreChart feedback={fb} />

      <Section
        title="Recent results"
        actions={
          scorerNames.length > 1 && (
            <select className="input" style={{ height: 26, fontSize: 12 }} value={scorerFilter} onChange={(e) => setScorerFilter(e.target.value)}>
              <option value="">All scorers</option>
              {scorerNames.map((s) => (
                <option key={s}>{s}</option>
              ))}
            </select>
          )
        }
      >
        <ErrorBox error={results.error} />
        {results.isLoading ? (
          <Loading />
        ) : shown.length === 0 ? (
          <div className="faint">
            {v.active ? 'No results yet. Matching spans are scored as they finish.' : 'This monitor is paused. Activate it to start scoring new spans.'}
          </div>
        ) : (
          <div className="table-wrap" style={{ maxHeight: 560 }}>
            <table className="table">
              <thead>
                <tr>
                  <th>Time</th>
                  <th>Span</th>
                  <th>Scorer</th>
                  <th>Output</th>
                  <th>Scorer call</th>
                </tr>
              </thead>
              <tbody>
                {shown.slice(0, 300).map((f) => {
                  const target = parseRef(f.ref)
                  const sc = f.call_ref ? parseRef(f.call_ref) : null
                  const out = f.payload?.output
                  return (
                    <tr key={f.id}>
                      <td className="muted" title={fmtDate(f.created_at)}>
                        {fmtRelative(f.created_at)}
                      </td>
                      <td>
                        {target?.kind === 'call' ? (
                          <Link className="mono" to={callUrl(projectId, target.name)}>
                            {target.name.slice(-8)}
                          </Link>
                        ) : (
                          <RefChip refStr={f.ref} />
                        )}
                      </td>
                      <td>
                        <span className="chip mono">{scorerOf(f)}</span>
                      </td>
                      <td>
                        <OutputCell v={out} />
                      </td>
                      <td>{sc?.kind === 'call' ? <Link to={callUrl(projectId, sc.name)}>view →</Link> : <span className="faint">—</span>}</td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
            {shown.length > 300 && <div className="hint" style={{ padding: 8 }}>Showing 300 of {shown.length}.</div>}
          </div>
        )}
      </Section>
      {editing && <MonitorEditor projectId={projectId} initial={o} onClose={() => setEditing(false)} onSaved={() => setEditing(false)} />}
    </div>
  )
}

function OutputCell({ v }: { v: unknown }) {
  if (typeof v === 'boolean') return <span className={`badge ${v ? 'success' : 'error'}`}>{String(v)}</span>
  if (typeof v === 'number') return <span className="mono">{fmtMetric(v)}</span>
  if (v && typeof v === 'object' && !Array.isArray(v)) {
    const flat = flattenScores(v)
    const reason = (v as Record<string, unknown>).reason
    return (
      <div className="col" style={{ gap: 2 }}>
        <span className="chips">
          {Object.entries(flat).map(([k, x]) => (
            <span key={k} className={`badge ${typeof x === 'boolean' ? (x ? 'success' : 'error') : ''}`}>
              {k} {fmtMetric(x)}
            </span>
          ))}
        </span>
        {typeof reason === 'string' && <span className="faint small cell-preview">{reason}</span>}
      </div>
    )
  }
  return <span className="cell-preview small mono">{preview(v, 160)}</span>
}

function summarize(fs: Feedback[]): string {
  const vals = fs.map((f) => {
    const out = f.payload?.output
    if (typeof out === 'boolean' || typeof out === 'number') return toNum(out)
    const flat = flattenScores(out)
    const first = Object.values(flat)[0]
    return first === undefined ? null : toNum(first)
  })
  const nums = vals.filter((x): x is number => x != null)
  if (!nums.length) return `${fs.length}`
  const allBool = fs.every((f) => {
    const out = f.payload?.output
    return typeof out === 'boolean' || typeof Object.values(flattenScores(out))[0] === 'boolean'
  })
  const mean = nums.reduce((a, b) => a + b, 0) / nums.length
  return allBool ? `${(mean * 100).toFixed(0)}% true` : `avg ${fmtMetric(mean)}`
}

/** Score over time, bucketed means per numeric/bool series. */
function ScoreChart({ feedback }: { feedback: Feedback[] }) {
  const { series, points } = useMemo(() => {
    const items: { t: number; key: string; v: number }[] = []
    for (const f of feedback) {
      const out = f.payload?.output
      const flat = typeof out === 'boolean' || typeof out === 'number' ? { value: out } : flattenScores(out)
      for (const [k, x] of Object.entries(flat)) {
        const n = toNum(x)
        if (n != null) items.push({ t: new Date(f.created_at).getTime(), key: `${scorerOf(f)}${k === 'value' ? '' : '.' + k}`, v: n })
      }
    }
    if (!items.length) return { series: [] as string[], points: [] as Record<string, number>[] }
    const t0 = Math.min(...items.map((i) => i.t))
    const t1 = Math.max(...items.map((i) => i.t))
    const n = Math.min(40, Math.max(1, Math.ceil(items.length / 3)))
    const width = Math.max(60_000, (t1 - t0) / n)
    const buckets = new Map<number, Map<string, { s: number; c: number }>>()
    for (const i of items) {
      const b = t0 + Math.floor((i.t - t0) / width) * width
      const m = buckets.get(b) ?? new Map()
      const cur = m.get(i.key) ?? { s: 0, c: 0 }
      cur.s += i.v
      cur.c++
      m.set(i.key, cur)
      buckets.set(b, m)
    }
    // keep the most frequent series to stay readable
    const freq = new Map<string, number>()
    items.forEach((i) => freq.set(i.key, (freq.get(i.key) ?? 0) + 1))
    const series = [...freq.entries()].sort((a, b) => b[1] - a[1]).slice(0, 6).map(([k]) => k)
    const points = [...buckets.entries()]
      .sort((a, b) => a[0] - b[0])
      .map(([t, m]) => {
        const p: Record<string, number> = { t }
        for (const k of series) {
          const c = m.get(k)
          if (c) p[k] = c.s / c.c
        }
        return p
      })
    return { series, points }
  }, [feedback])
  if (!series.length) return null
  const fmtT = (t: number) => new Date(t).toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' })
  return (
    <Section title="Scores over time">
      <div style={{ height: 220 }}>
        <ResponsiveContainer width="100%" height="100%">
          <LineChart data={points} margin={{ top: 8, right: 12, bottom: 0, left: -12 }}>
            <CartesianGrid stroke="var(--border)" vertical={false} />
            <XAxis dataKey="t" type="number" scale="time" domain={['dataMin', 'dataMax']} tickFormatter={fmtT} tick={{ fill: 'var(--text-3)', fontSize: 11 }} stroke="var(--border)" />
            <YAxis tick={{ fill: 'var(--text-3)', fontSize: 11 }} stroke="var(--border)" width={48} />
            <Tooltip
              contentStyle={{ background: 'var(--panel)', border: '1px solid var(--border)', borderRadius: 6, fontSize: 12 }}
              labelFormatter={(t) => fmtT(Number(t))}
              formatter={(x: number) => fmtMetric(x)}
            />
            <Legend wrapperStyle={{ fontSize: 12 }} />
            {series.map((k, i) => (
              <Line key={k} type="monotone" dataKey={k} stroke={seriesColor(i)} strokeWidth={2} dot={{ r: 2 }} connectNulls isAnimationActive={false} />
            ))}
          </LineChart>
        </ResponsiveContainer>
      </div>
      <div className="hint">Booleans are plotted as the fraction of true results per time bucket.</div>
    </Section>
  )
}
