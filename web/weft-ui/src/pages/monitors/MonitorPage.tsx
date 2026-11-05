import { useMemo, useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { CartesianGrid, Line, LineChart, ResponsiveContainer, Tooltip, XAxis, YAxis } from 'recharts'
import { api, ApiError } from '../../api/client'
import { parseRef } from '../../api/refs'
import type { Feedback } from '../../api/types'
import { ErrorBox, Loading } from '../../components/common'
import { Inspector, InspectorDivider, InspectorLinks, KeyValueList } from '../../components/ui/Inspector'
import { PageHeader } from '../../components/ui/PageHeader'
import { PlainTable } from '../../components/ui/PlainTable'
import { Sentence } from '../../components/ui/Sentence'
import { monitorSentence } from '../../lib/summaries/dashboards'
import { boolWords, judge, needsLook, onScale, scoreParts, scoreResultText, scoreShape, scoreValue, type ScoreShape } from '../../lib/summaries/evaluate'
import { useScorerScales } from '../scores/useScorerScales'
import { StatusGlyph } from '../../components/ui/StatusGlyph'
import { turnName } from '../../lib/summaries/traces'
import { scorerWords } from '../../lib/summaries'
import { spanDisplayName } from '../../api/refs'
import { monitorWords } from './MonitorsPage'
import '../observe.css'
import { RefChip } from '../../components/RefChip'
import { JsonBlock } from '../../components/ValueView'
import { DEFAULT_IDLE_SECONDS, MONITOR_SCOPES, MonitorEditor, useToggleMonitor } from '../../components/evals/MonitorEditor'
import { monitorNames, monitorScopes } from '../../components/evals/monitorFields'
import { objectRefPrefix, seriesColor } from '../../components/evals/evalUtils'
import { Switch } from '../../components/objects/bits'
import { useProjectId } from '../../hooks/useProject'
import { fmtDate, fmtRelative, preview, versionLabel } from '../../lib/format'
import { plural } from '../../lib/summaries'
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
  // The scored spans, named by what was asked (the table shows names, not ids).
  const targetIds = useMemo(() => {
    const ids = new Set<string>()
    for (const f of (results.data ?? []).slice(0, 300)) {
      const t = parseRef(f.ref)
      if (t?.kind === 'span') ids.add(t.name)
    }
    return [...ids].sort()
  }, [results.data])
  const targets = useQuery({
    queryKey: ['spans', projectId, 'monitor-targets', targetIds],
    queryFn: () => api.spansQuery({ project_id: projectId, filter: { ids: targetIds }, columns: ['id', 'name', 'inputs', 'display_name'], limit: targetIds.length }),
    enabled: targetIds.length > 0,
    staleTime: 60_000,
  })
  const targetName = useMemo(() => {
    const m = new Map<string, string>()
    for (const sp of targets.data?.items ?? []) m.set(sp.id, sp.display_name || turnName(sp.inputs, 70) || spanDisplayName(sp.name))
    return m
  }, [targets.data])
  const scorerNames = useMemo(() => [...new Set((results.data ?? []).map(scorerOf))], [results.data])
  const scales = useScorerScales(projectId, scorerNames)
  // Each scorer's results read by their shape, as the Scores page reads them.
  const shapes = useMemo(() => {
    const m: Record<string, ScoreShape> = {}
    for (const n of scorerNames) m[n] = scoreShape((results.data ?? []).filter((f) => scorerOf(f) === n).map(itemOf), scales[n])
    return m
  }, [results.data, scorerNames, scales])

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
  const shown = scorerFilter ? fb.filter((f) => scorerOf(f) === scorerFilter) : fb
  const sentence = monitorSentence(monitorWords(v), {
    total: fb.length,
    capped: fb.length >= 1000,
    lastAt: fb[0]?.created_at ?? null,
    results: results.data ? scorerNames.map((s) => judge(s, fb.filter((f) => scorerOf(f) === s).map(itemOf), shapes[s])) : [],
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
            <span title={fmtDate(o.created_at)}>
              updated {fmtRelative(o.created_at)}, {versionLabel(o.version)}
            </span>
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
          <ScoreCharts feedback={fb} shapes={shapes} />
          <section className="obs-quiet" aria-labelledby="mon-results-h">
            <div className="obs-section-head">
              <h2 className="view-h" id="mon-results-h">
                Recent results
              </h2>
              {scorerNames.length > 1 && (
                <select className="input" aria-label="Scorer" value={scorerFilter} onChange={(e) => setScorerFilter(e.target.value)}>
                  <option value="">All scorers</option>
                  {scorerNames.map((s) => (
                    <option key={s} value={s}>
                      {scorerWords(s)}
                    </option>
                  ))}
                </select>
              )}
            </div>
            <ErrorBox error={results.error} />
            {results.isLoading ? (
              <Loading />
            ) : shown.length === 0 ? (
              <p className="quiet-empty">{v.active ? 'No results yet. Matching steps are scored as they finish.' : 'This monitor is paused. Activate it to start scoring new steps.'}</p>
            ) : (
              <>
                <PlainTable caption="Recent results of this monitor">
                  <thead>
                    <tr>
                      <th scope="col">Scored</th>
                      {scorerNames.length > 1 && <th scope="col">Scorer</th>}
                      <th scope="col">Result</th>
                      <th scope="col">When</th>
                    </tr>
                  </thead>
                  <tbody>
                    {shown.slice(0, 300).map((f) => {
                      const target = parseRef(f.ref)
                      const sc = f.span_ref ? parseRef(f.span_ref) : null
                      const out = f.payload?.output
                      return (
                        <tr key={f.id}>
                          <td>
                            {target?.kind === 'span' ? (
                              <Link to={spanUrl(projectId, target.name)} title={`Span ${target.name}`}>
                                {targetName.get(target.name) ?? 'Open the scored step'}
                              </Link>
                            ) : (
                              <RefChip refStr={f.ref} />
                            )}
                          </td>
                          {scorerNames.length > 1 && <td>{scorerWords(scorerOf(f))}</td>}
                          <td>
                            <OutputCell v={out} shape={shapes[scorerOf(f)]} />
                            {sc?.kind === 'span' && (
                              <Link className="obs-faint small" to={spanUrl(projectId, sc.name)} title="The judge's own run, with its reasoning">
                                {' '}
                                Why
                              </Link>
                            )}
                          </td>
                          <td className="obs-faint" title={fmtDate(f.created_at)} style={{ whiteSpace: 'nowrap' }}>
                            {fmtRelative(f.created_at)}
                          </td>
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
              { label: 'Step names', value: monitorNames(v).length ? monitorNames(v).join(', ') : 'All top-level steps', mono: monitorNames(v).length > 0 },
              { label: 'Sampling', value: `${Math.round((v.sampling_rate ?? 1) * 100)}%`, help: 'The share of matching targets scored.' },
              {
                label: 'Scores',
                value: monitorScopes(v).length
                  ? monitorScopes(v).map((s) => MONITOR_SCOPES.find((x) => x.id === s)?.label ?? s).join(', ')
                  : 'Each scorer where it fits (sessions for session judges, steps otherwise)',
              },
              { label: 'Complete after', value: `${v.session_idle_seconds ?? DEFAULT_IDLE_SECONDS} s without new steps`, help: 'When a session counts as finished.' },
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
              // The scores are counted in the sentence; the spans they cover differ when a span was scored more than once.
              new Set(fb.map((f) => f.ref)).size !== fb.length && { label: 'Steps scored', value: new Set(fb.map((f) => f.ref)).size.toLocaleString() },
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

/** A result as the sentences read it: its raw output and its number. */
function itemOf(f: Feedback) {
  const output = f.payload?.output
  return { output, value: scoreValue(output) }
}

function OutputCell({ v, shape }: { v: unknown; shape?: ScoreShape }) {
  if (shape && (shape.boolKey || shape.numKey)) {
    const item = { output: v, value: scoreValue(v) }
    const look = needsLook(item, shape)
    const reason = v && typeof v === 'object' ? (v as Record<string, unknown>).reason : undefined
    return (
      <div className="col" style={{ gap: 2 }}>
        <span className="obs-result">
          {look && <StatusGlyph kind="warning" size={16} label="Needs a look" />}
          {scoreResultText(item, shape)}
        </span>
        {typeof reason === 'string' && <span className="obs-faint small">{reason}</span>}
      </div>
    )
  }
  return <span className="cell-preview small">{preview(v, 160)}</span>
}

/** 24-hour times on the axis ("22:00"), with the day when the range spans more than one. */
function timeTick(spanMs: number) {
  return (t: number) => {
    const d = new Date(t)
    const time = d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit', hourCycle: 'h23' })
    return spanMs > 86_400_000 ? `${d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' })} ${time}` : time
  }
}

/** Fewer periods than this and the sentence says it all. */
const MIN_PERIODS = 3

interface Series {
  id: string
  title: string
  kind: 'share' | 'number'
  shape: ScoreShape
  points: { t: number; v: number }[]
}

/**
 * Each scorer's results over time, one chart per kind of result: the share
 * that said yes ("Correct", 0–100%) and the mean number on its own range
 * ("Score", 1–5). Nothing when there are fewer than three periods.
 */
function ScoreCharts({ feedback, shapes }: { feedback: Feedback[]; shapes: Record<string, ScoreShape> }) {
  const { series, span } = useMemo(() => {
    const items = feedback.map((f) => ({ t: new Date(f.created_at).getTime(), scorer: scorerOf(f), item: itemOf(f) }))
    if (!items.length) return { series: [] as Series[], span: 0 }
    const t0 = Math.min(...items.map((i) => i.t))
    const t1 = Math.max(...items.map((i) => i.t))
    const n = Math.min(40, Math.max(1, Math.ceil(items.length / 3)))
    const width = Math.max(60_000, (t1 - t0) / n)
    const bucket = (t: number) => t0 + Math.floor((t - t0) / width) * width
    const names = [...new Set(items.map((i) => i.scorer))].slice(0, 3)
    const out: Series[] = []
    for (const name of names) {
      const shape = shapes[name]
      if (!shape) continue
      const yes = new Map<number, { y: number; c: number }>()
      const num = new Map<number, { s: number; c: number }>()
      for (const i of items) {
        if (i.scorer !== name) continue
        const b = bucket(i.t)
        const p = scoreParts(i.item, shape)
        if (p.yes != null) {
          const cur = yes.get(b) ?? { y: 0, c: 0 }
          cur.y += p.yes ? 1 : 0
          cur.c++
          yes.set(b, cur)
        }
        if (p.num != null) {
          const cur = num.get(b) ?? { s: 0, c: 0 }
          cur.s += p.num
          cur.c++
          num.set(b, cur)
        }
      }
      const who = names.length > 1 ? `${scorerWords(name)}: ` : ''
      const capFirst = (x: string) => x[0].toUpperCase() + x.slice(1)
      if (shape.boolKey && yes.size >= MIN_PERIODS)
        out.push({
          id: `${name}-share`,
          title: capFirst(`${who}${boolWords(shape.boolKey)[2]}`),
          kind: 'share',
          shape,
          points: [...yes.entries()].sort((a, b) => a[0] - b[0]).map(([t, x]) => ({ t, v: (x.y / x.c) * 100 })),
        })
      if (shape.numKey && num.size >= MIN_PERIODS)
        out.push({
          id: `${name}-num`,
          title: capFirst(`${who}${!shape.numKey || shape.numKey === 'value' || shape.numKey === 'score' ? 'score' : shape.numKey.replace(/[_.]+/g, ' ')}`),
          kind: 'number',
          shape,
          points: [...num.entries()].sort((a, b) => a[0] - b[0]).map(([t, x]) => ({ t, v: x.s / x.c })),
        })
    }
    return { series: out, span: t1 - t0 }
  }, [feedback, shapes])
  if (!series.length) return null
  const fmtT = timeTick(span)
  const tip = (t: number) => new Date(t).toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', hourCycle: 'h23' })
  return (
    <section className="obs-quiet" aria-labelledby="mon-chart-h">
      <h2 className="view-h" id="mon-chart-h">
        Over time
      </h2>
      <div className="obs-chart-grid">
        {series.map((s, i) => {
          const fmtV = (x: number) => (s.kind === 'share' ? `${Math.round(x)}%` : onScale(Number(x.toFixed(2)), s.shape.scale))
          const domain: [number, number] | ['auto', 'auto'] = s.kind === 'share' ? [0, 100] : s.shape.scale ? [s.shape.scale.min, s.shape.scale.max] : ['auto', 'auto']
          return (
            <figure className="view-panel obs-chart" key={s.id} aria-label={`${s.title} over time, ${plural(s.points.length, 'period')}`}>
              <figcaption className="obs-chart-title">{s.title}</figcaption>
              <div style={{ height: 160 }}>
                <ResponsiveContainer width="100%" height="100%">
                  <LineChart data={s.points} margin={{ top: 8, right: 12, bottom: 0, left: -12 }}>
                    <CartesianGrid stroke="var(--separator)" vertical={false} />
                    <XAxis dataKey="t" type="number" scale="time" domain={['dataMin', 'dataMax']} tickFormatter={fmtT} tick={{ fill: 'var(--text-3)', fontSize: 11 }} stroke="var(--border)" minTickGap={28} />
                    <YAxis
                      domain={domain}
                      allowDecimals={s.kind === 'number' && !s.shape.scale}
                      tickFormatter={(x: number) => (s.kind === 'share' ? `${x}%` : String(x))}
                      tick={{ fill: 'var(--text-3)', fontSize: 11 }}
                      stroke="var(--border)"
                      width={48}
                    />
                    <Tooltip
                      contentStyle={{ background: 'var(--panel)', border: '1px solid var(--border)', borderRadius: 6, fontSize: 12 }}
                      labelFormatter={(t) => tip(Number(t))}
                      formatter={(x: number) => [fmtV(x), s.title]}
                    />
                    <Line type="linear" dataKey="v" name={s.title} stroke={seriesColor(i)} strokeWidth={1.75} dot={{ r: 2 }} connectNulls isAnimationActive={false} />
                  </LineChart>
                </ResponsiveContainer>
              </div>
            </figure>
          )
        })}
      </div>
    </section>
  )
}
