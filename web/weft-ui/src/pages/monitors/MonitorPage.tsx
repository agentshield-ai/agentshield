import { useMemo, useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { CartesianGrid, Legend, Line, LineChart, ResponsiveContainer, Tooltip, XAxis, YAxis } from 'recharts'
import { api, ApiError } from '../../api/client'
import { parseRef } from '../../api/refs'
import type { Feedback } from '../../api/types'
import { ErrorBox, Loading } from '../../components/common'
import { Inspector, InspectorDivider, InspectorLinks, KeyValueList } from '../../components/ui/Inspector'
import { PageHeader } from '../../components/ui/PageHeader'
import { PlainTable } from '../../components/ui/PlainTable'
import { Sentence } from '../../components/ui/Sentence'
import { monitorSentence } from '../../lib/summaries/dashboards'
import { monitorWords } from './MonitorsPage'
import '../observe.css'
import { RefChip } from '../../components/RefChip'
import { JsonBlock } from '../../components/ValueView'
import { DEFAULT_IDLE_SECONDS, MONITOR_SCOPES, MonitorEditor, useToggleMonitor } from '../../components/evals/MonitorEditor'
import { monitorNames, monitorScopes } from '../../components/evals/monitorFields'
import { flattenScores, fmtMetric, objectRefPrefix, seriesColor, toNum } from '../../components/evals/evalUtils'
import { Switch } from '../../components/objects/bits'
import { useProjectId } from '../../hooks/useProject'
import { fmtDate, fmtRelative, preview } from '../../lib/format'
import { contains, toQuery } from '../../lib/query'
import { spanUrl, objectUrl, objectVersionsUrl } from '../../lib/urls'

const RUNNABLE_PREFIX = 'weft.scorer.'

const scorerOf = (f: Feedback) => (f.feedback_type.startsWith(RUNNABLE_PREFIX) ? f.feedback_type.slice(RUNNABLE_PREFIX.length) : f.feedback_type)

export function MonitorPage() {
  const projectId = useProjectId()
  const { objectId = '' } = useParams()
  const [editing, setEditing] = useState(false)
  const [scorerFilter, setScorerFilter] = useState('')
  const obj = useQuery({
    queryKey: ['object', projectId, objectId, 'latest'],
    queryFn: () => api.objectGet(projectId, objectId, 'latest'),
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
        .then((r) => r.items.filter((f) => parseRef(f.trigger_ref ?? '')?.name === objectId)),
    refetchInterval: 15_000,
  })
  const { toggle, pending } = useToggleMonitor(projectId)

  if (obj.isLoading) return <Loading />
  if (obj.error || !obj.data)
    return (
      <div className="page-frame detail obs-page">
        <PageHeader variant="detail" back={{ to: `/${projectId}/monitors`, label: 'Monitors' }} title={objectId} help="evaluations.md#monitors" helpTopic="Monitors" />
        {obj.error instanceof ApiError && obj.error.status === 404 ? (
          <p className="quiet-empty">Monitor “{objectId}” was not found.</p>
        ) : (
          <ErrorBox error={obj.error ?? 'Not found'} />
        )}
      </div>
    )

  const o = obj.data
  const v = o.value ?? {}
  const fb = results.data ?? []
  const scorerNames = [...new Set(fb.map(scorerOf))]
  const shown = scorerFilter ? fb.filter((f) => scorerOf(f) === scorerFilter) : fb
  const sentence = monitorSentence(monitorWords(v), {
    total: fb.length,
    capped: fb.length >= 1000,
    lastAt: fb[0]?.created_at ?? null,
    averages: results.data ? scorerNames.slice(0, 3).map((s) => ({ scorer: s, text: summarize(fb.filter((f) => scorerOf(f) === s)) })) : [],
  })

  return (
    <div className="page-frame detail obs-page">
      <PageHeader
        variant="detail"
        back={{ to: `/${projectId}/monitors`, label: 'Monitors' }}
        title={objectId}
        help="evaluations.md#monitors"
        helpTopic="Monitors"
        sentence={results.isLoading ? undefined : <Sentence size="detail" parts={[sentence]} />}
        meta={
          <>
            {v.description ? <>{v.description} · </> : null}
            <span title={fmtDate(o.created_at)}>updated {fmtRelative(o.created_at)}</span> · <Link to={objectVersionsUrl(projectId, objectId)}>version {o.version} and its history</Link>
          </>
        }
      />
      <div className="obs-actions">
        <label className="obs-check obs-toolbar">
          <Switch on={!!v.active} disabled={pending === o.name} onChange={() => void toggle(o)} title={v.active ? 'Pause monitor' : 'Activate monitor'} />
          {v.active ? 'Active' : 'Paused'}
        </label>
        <span style={{ flex: 1 }} />
        <button type="button" className="qbtn primary" onClick={() => setEditing(true)}>
          Edit
        </button>
      </div>

      <div className="obs-detail-grid">
        <div className="obs-groups">
          <ScoreChart feedback={fb} />
          <section className="obs-quiet" aria-labelledby="mon-results-h">
            <div className="obs-section-head">
              <h2 className="view-h" id="mon-results-h">
                Recent results <span className="glist-count">{fb.length >= 1000 ? '1000+' : fb.length}</span>
              </h2>
              {scorerNames.length > 1 && (
                <select className="input" aria-label="Scorer" value={scorerFilter} onChange={(e) => setScorerFilter(e.target.value)}>
                  <option value="">All scorers</option>
                  {scorerNames.map((s) => (
                    <option key={s}>{s}</option>
                  ))}
                </select>
              )}
            </div>
            <ErrorBox error={results.error} />
            {results.isLoading ? (
              <Loading />
            ) : shown.length === 0 ? (
              <p className="quiet-empty">{v.active ? 'No results yet. Matching spans are scored as they finish.' : 'This monitor is paused. Activate it to start scoring new spans.'}</p>
            ) : (
              <>
                <PlainTable caption="Recent results of this monitor">
                  <thead>
                    <tr>
                      <th scope="col">Time</th>
                      <th scope="col">Scored</th>
                      <th scope="col">Scorer</th>
                      <th scope="col">Result</th>
                      <th scope="col">
                        <span title="The scorer's own span (internal term)">Scorer span</span>
                      </th>
                    </tr>
                  </thead>
                  <tbody>
                    {shown.slice(0, 300).map((f) => {
                      const target = parseRef(f.ref)
                      const sc = f.span_ref ? parseRef(f.span_ref) : null
                      const out = f.payload?.output
                      return (
                        <tr key={f.id}>
                          <td className="obs-faint" title={fmtDate(f.created_at)} style={{ whiteSpace: 'nowrap' }}>
                            {fmtRelative(f.created_at)}
                          </td>
                          <td>
                            {target?.kind === 'span' ? (
                              <Link className="mono" to={spanUrl(projectId, target.name)} title={target.name}>
                                {target.name.slice(-8)}
                              </Link>
                            ) : (
                              <RefChip refStr={f.ref} />
                            )}
                          </td>
                          <td className="mono">{scorerOf(f)}</td>
                          <td>
                            <OutputCell v={out} />
                          </td>
                          <td>{sc?.kind === 'span' ? <Link to={spanUrl(projectId, sc.name)}>Open</Link> : <span className="obs-faint">—</span>}</td>
                        </tr>
                      )
                    })}
                  </tbody>
                </PlainTable>
                {shown.length > 300 && <p className="quiet-note">Showing 300 of {shown.length}.</p>}
              </>
            )}
          </section>
        </div>
        <Inspector title="Configuration" label="Monitor configuration">
          <KeyValueList
            items={[
              { label: 'Span names', value: monitorNames(v).length ? monitorNames(v).join(', ') : 'All top-level spans', mono: monitorNames(v).length > 0 },
              { label: 'Sampling', value: `${Math.round((v.sampling_rate ?? 1) * 100)}%`, help: 'The share of matching targets scored.' },
              {
                label: 'Scores',
                value: monitorScopes(v).length
                  ? monitorScopes(v).map((s) => MONITOR_SCOPES.find((x) => x.id === s)?.label ?? s).join(', ')
                  : 'Each scorer where it fits (sessions for session judges, spans otherwise)',
              },
              { label: 'Complete after', value: `${v.session_idle_seconds ?? DEFAULT_IDLE_SECONDS} s without new spans`, help: 'When a session counts as finished.' },
              {
                label: 'Scorers',
                value: (v.scorers ?? []).length ? (
                  <span className="chips">
                    {(v.scorers ?? []).map((s: string) => (
                      <RefChip key={s} refStr={s} />
                    ))}
                  </span>
                ) : (
                  'None'
                ),
              },
              { label: 'Spans scored', value: new Set(fb.map((f) => f.ref)).size.toLocaleString() },
            ]}
          />
          {v.query && (
            <>
              <InspectorDivider />
              <details className="insp-raw">
                <summary>Filter</summary>
                <JsonBlock value={v.query} />
              </details>
            </>
          )}
          <InspectorDivider />
          <InspectorLinks>
            <Link to={objectUrl(projectId, objectId, o.digest)}>The stored object</Link>
            <Link to={objectVersionsUrl(projectId, objectId)}>Every version</Link>
          </InspectorLinks>
        </Inspector>
      </div>
      {editing && <MonitorEditor projectId={projectId} initial={o} onClose={() => setEditing(false)} onSaved={() => setEditing(false)} />}
    </div>
  )
}

function OutputCell({ v }: { v: unknown }) {
  if (typeof v === 'boolean') return <span>{String(v)}</span>
  if (typeof v === 'number') return <span className="mono">{fmtMetric(v)}</span>
  if (v && typeof v === 'object' && !Array.isArray(v)) {
    const flat = flattenScores(v)
    const reason = (v as Record<string, unknown>).reason
    return (
      <div className="col" style={{ gap: 2 }}>
        <span>{Object.entries(flat).map(([k, x]) => `${k} ${fmtMetric(x)}`).join(' · ')}</span>
        {typeof reason === 'string' && <span className="obs-faint small">{reason}</span>}
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
  if (!nums.length) return `gave ${fs.length} result${fs.length === 1 ? '' : 's'}`
  const allBool = fs.every((f) => {
    const out = f.payload?.output
    return typeof out === 'boolean' || typeof Object.values(flattenScores(out))[0] === 'boolean'
  })
  const mean = nums.reduce((a, b) => a + b, 0) / nums.length
  return allBool ? `true ${(mean * 100).toFixed(0)}% of the time` : `averages ${fmtMetric(mean)}`
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
    <section className="obs-quiet" aria-labelledby="mon-chart-h">
      <h2 className="view-h" id="mon-chart-h">
        Scores over time
      </h2>
      <div className="view-panel">
      <div style={{ height: 200 }}>
        <ResponsiveContainer width="100%" height="100%">
          <LineChart data={points} margin={{ top: 8, right: 12, bottom: 0, left: -12 }}>
            <CartesianGrid stroke="var(--separator)" vertical={false} />
            <XAxis dataKey="t" type="number" scale="time" domain={['dataMin', 'dataMax']} tickFormatter={fmtT} tick={{ fill: 'var(--text-3)', fontSize: 11 }} stroke="var(--border)" />
            <YAxis tick={{ fill: 'var(--text-3)', fontSize: 11 }} stroke="var(--border)" width={48} />
            <Tooltip
              contentStyle={{ background: 'var(--panel)', border: '1px solid var(--border)', borderRadius: 6, fontSize: 12 }}
              labelFormatter={(t) => fmtT(Number(t))}
              formatter={(x: number) => fmtMetric(x)}
            />
            <Legend wrapperStyle={{ fontSize: 12 }} />
            {series.map((k, i) => (
              <Line key={k} type="monotone" dataKey={k} stroke={seriesColor(i)} strokeWidth={1.75} dot={{ r: 2 }} connectNulls isAnimationActive={false} />
            ))}
          </LineChart>
        </ResponsiveContainer>
      </div>
      <p className="quiet-note">True or false results are plotted as the share that were true in each period.</p>
      </div>
    </section>
  )
}
