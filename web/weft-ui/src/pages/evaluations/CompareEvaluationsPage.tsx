// An evaluation run, or several compared (DESIGN.md §7, §7.1 rules 1 and 4,
// §7.2): a sentence stating the result and the biggest difference against
// the baseline, the runs, the scores per run with their change against the
// baseline, then every example in a plain table, failing and worse examples
// first. Selecting an example opens it in the inspector (its inputs, each
// run's output, scores, timing and links); with none selected the inspector
// holds the runs' figures and links. Charts and the weighted ranking sit
// behind disclosures. The runs, baseline, example filter, selected example,
// ranking metrics and delta format live in the URL; j/k move between examples.
import { useEffect, useMemo, useRef, useState } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { Bar, BarChart, CartesianGrid, Cell, ResponsiveContainer, Scatter, ScatterChart, Tooltip, XAxis, YAxis, ZAxis, Legend, ReferenceLine } from 'recharts'
import { api } from '../../api/client'
import type { Any, CompareEvaluation, CompareEvaluationsRes, CompareMetric, CompareRow } from '../../api/types'
import { ErrorBox, Loading } from '../../components/common'
import { ValueView } from '../../components/ValueView'
import { defaultLowerIsBetter, evalRunInfo, flattenScores, fmtMetric, seriesColor, sortMetricKeys, toNum, useEvalRuns } from '../../components/evals/evalUtils'
import { InlineLeaderboard } from '../../components/evals/InlineLeaderboard'
import { toSentence } from '../../components/evals/sentence'
import { LinkedCallsFor } from '../../components/traces/LinkedCalls'
import { EvalAgentLinks, PredictionAgentLinks, useAgentEvalRuns } from '../../components/agents/AgentEvals'
import type { AgentEvalRun } from '../../api/agents'
import { DELTA_FORMATS, fmtDeltaAs, parseDeltaFormat, type DeltaFormat } from '../../components/traces/diffUtils'
import { decodeLb, encodeLb } from '../../components/evals/leaderboardUtils'
import { CopyText, Inspector, InspectorDivider, InspectorLinks, KeyValueList } from '../../components/ui/Inspector'
import { PageHeader } from '../../components/ui/PageHeader'
import { PlainTable } from '../../components/ui/PlainTable'
import { SearchField } from '../../components/ui/SearchField'
import { Segmented } from '../../components/ui/Segmented'
import { Sentence, type SentencePart } from '../../components/ui/Sentence'
import { isTyping } from '../../components/ui/Shortcuts'
import { StatusGlyph } from '../../components/ui/StatusGlyph'
import { useProjectId } from '../../hooks/useProject'
import { fmtCost, fmtDate, fmtNumber, preview } from '../../lib/format'
import { relativeTime, shortDuration, plural } from '../../lib/summaries'
import {
  compareSentence,
  exampleChange,
  hasFailingScore,
  isRunFigure,
  metricValue,
  metricWords,
  type CmpExample,
  type ExampleFilter,
} from '../../lib/summaries/evaluate'
import { spanUrl, evaluationsUrl, refUrl, compareEvaluationsUrl } from '../../lib/urls'
import './evaluate.css'

const tooltipStyle = {
  contentStyle: { background: 'var(--panel)', border: '1px solid var(--border)', borderRadius: 8, fontSize: 12, color: 'var(--text)' },
  labelStyle: { color: 'var(--text-muted)' },
  itemStyle: { color: 'var(--text)' },
}
const tick = { fill: 'var(--text-muted)', fontSize: 11 }

/** Unique, readable label per evaluation run. */
function useLabels(evals: CompareEvaluation[]) {
  return useMemo(() => {
    const counts: Record<string, number> = {}
    evals.forEach((e) => (counts[e.model_name] = (counts[e.model_name] ?? 0) + 1))
    return Object.fromEntries(evals.map((e) => [e.id, counts[e.model_name] > 1 ? `${e.model_name} · ${relativeTime(e.started_at)}` : e.model_name || e.id.slice(-8)]))
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
const STATUS_GLYPH: Record<string, { kind: 'ok' | 'failed' | 'running' | 'warning'; label: string }> = {
  success: { kind: 'ok', label: 'Finished' },
  error: { kind: 'failed', label: 'Failed' },
  running: { kind: 'running', label: 'Running' },
  descendant_error: { kind: 'warning', label: 'Finished with errors' },
}
const glyphOf = (s: string) => STATUS_GLYPH[s] ?? STATUS_GLYPH.success

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
  const patch = (fn: (p: URLSearchParams) => void) => {
    const p = new URLSearchParams(params)
    fn(p)
    setParams(p, { replace: true })
  }
  const setIds = (next: string[]) =>
    patch((p) => {
      p.set('ids', next.join(','))
      if (!next.includes(p.get('baseline') ?? '')) p.delete('baseline')
    })
  const baselineId = params.get('baseline') && ids.includes(params.get('baseline')!) ? params.get('baseline')! : ids[0]
  const setBaseline = (id: string) => patch((p) => p.set('baseline', id))
  const back = { to: evaluationsUrl(projectId), label: 'Evaluations' }

  if (ids.length === 0)
    return (
      <div className="page-frame detail">
        <PageHeader variant="detail" back={back} title="Compare evaluations" help="evaluations.md#evaluations" />
        <div className="quiet-empty">
          <p>No runs chosen.</p>
          <p className="ev-note">
            Open a run from <Link to={evaluationsUrl(projectId)}>Evaluations</Link>, or tick several there to compare them.
          </p>
        </div>
      </div>
    )
  if (q.isLoading) return <Loading label="Comparing…" />
  if (q.error || !q.data)
    return (
      <div className="page-frame detail">
        <PageHeader variant="detail" back={back} title={ids.length === 1 ? 'Evaluation run' : 'Compare evaluations'} help="evaluations.md#evaluations" />
        <ErrorBox error={q.error} />
      </div>
    )
  return <CompareBody data={q.data} projectId={projectId} ids={ids} baselineId={baselineId} setBaseline={setBaseline} setIds={setIds} back={back} />
}

function CompareBody({
  data,
  projectId,
  ids,
  baselineId,
  setBaseline,
  setIds,
  back,
}: {
  data: CompareEvaluationsRes
  projectId: string
  ids: string[]
  baselineId: string
  setBaseline: (id: string) => void
  setIds: (ids: string[]) => void
  back: { to: string; label: string }
}) {
  const evals = data.evaluations
  const [params, setParams] = useSearchParams()
  const setParam = (k: string, v: string | null) =>
    setParams(
      (p) => {
        const n = new URLSearchParams(p)
        if (v) n.set(k, v)
        else n.delete(k)
        return n
      },
      { replace: true },
    )
  const lb = useMemo(() => decodeLb(params.get('lb')), [params])
  const setLb = (next: ReturnType<typeof decodeLb>) => setParam('lb', next.length ? encodeLb(next) : null)
  const labels = useLabels(evals)
  const agentRuns = useAgentEvalRuns(
    projectId,
    useMemo(() => evals.map((e) => e.id), [evals]),
  ).data
  const colorOf = useMemo(() => Object.fromEntries(evals.map((e, i) => [e.id, seriesColor(i)])), [evals])
  const baseline = evals.find((e) => e.id === baselineId) ?? evals[0]
  const [lowerOverrides, setLowerOverrides] = useState<Record<string, boolean>>({})
  const lowerBetter = (key: string) => lowerOverrides[key] ?? defaultLowerIsBetter(key)
  const metrics = useMemo(() => {
    const order = sortMetricKeys(data.metrics.map((m) => m.key))
    return order.map((k) => data.metrics.find((m) => m.key === k)!)
  }, [data.metrics])

  // The previous run of the same evaluation, offered when one run is open.
  const one = evals.length === 1 ? evals[0] : null
  const sameEval = useEvalRuns(projectId, { evalName: one?.evaluation_name, limit: 50, enabled: !!one })
  const previous = useMemo(() => {
    if (!one || !sameEval.data) return null
    const i = sameEval.data.findIndex((c) => c.id === one.id)
    return i >= 0 ? (sameEval.data.slice(i + 1).find((c) => c.ended_at && !c.error) ?? null) : null
  }, [one, sameEval.data])

  // Examples, with each run's averaged scores and whether it failed there.
  const examples = useMemo(
    () =>
      data.rows.map((r, idx) => {
        const aggs = Object.fromEntries(evals.map((e) => [e.id, rowAgg(r.results[e.id])]))
        const failed = Object.fromEntries(evals.map((e) => [e.id, (r.results[e.id] ?? []).some((t) => hasFailingScore(t.scores))]))
        const outputs = evals.map((e) => JSON.stringify(r.results[e.id]?.[0]?.output ?? null))
        const keys = new Set<string>()
        Object.values(aggs).forEach((a) => Object.keys(a).forEach((k) => !NON_SCORE_KEYS.has(k) && keys.add(k)))
        const scoreKeys = sortMetricKeys([...keys])
        const scoreDiffer = scoreKeys.some((k) => new Set(evals.map((e) => JSON.stringify(aggs[e.id]?.[k] ?? null))).size > 1)
        const cmp: CmpExample = { scores: aggs, failed }
        const ch = exampleChange(cmp, baseline.id, evals.map((e) => e.id), (k) => lowerFor(metrics, lowerBetter, k))
        return {
          r,
          idx,
          aggs,
          scoreKeys,
          cmp,
          failedAny: Object.values(failed).some(Boolean),
          disagree: scoreDiffer || new Set(outputs).size > 1,
          regressed: ch.regressed,
          improved: ch.improved,
          text: JSON.stringify(r.inputs).toLowerCase(),
        }
      }),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [data.rows, evals, baseline.id, metrics, lowerOverrides],
  )

  const sentenceParts: SentencePart[] = toSentence(
    compareSentence({
      runs: evals.map((e) => ({ id: e.id, label: labels[e.id], evaluation: e.evaluation_name, status: e.status, summary: e.summary })),
      baseline: baseline.id,
      metrics: metrics.map((m) => ({ key: m.key, values: m.values })),
      examples: examples.map((x) => x.cmp),
      lower: lowerBetter,
    }),
    (l) => (l.kind === 'examples' ? { onClick: () => setParam('show', l.filter), title: 'Show these examples' } : null),
  )
  if (previous) sentenceParts.push(' ', { text: 'Compare with the previous run', to: `${compareEvaluationsUrl(projectId, [previous.id, one!.id])}&baseline=${previous.id}`, title: `${evalRunInfo(previous).modelName}, ${relativeTime(previous.started_at)}` }, '.')

  const evalNames = [...new Set(evals.map((e) => e.evaluation_name))]
  const title = !evals.length
    ? 'Compare evaluations'
    : one
      ? `${one.evaluation_name} · ${labels[one.id]}`
      : evalNames.length === 1
        ? `${evalNames[0]}: ${evals.map((e) => labels[e.id]).join(' vs ')}`
        : `Comparing ${evals.length} runs`
  const rowsTotal = Math.max(0, ...evals.map((e) => e.row_count))
  const meta = one
    ? [plural(data.rows.length, 'example'), `started ${relativeTime(one.started_at)}`, evalDurationMs(one) != null ? `took ${shortDuration(evalDurationMs(one))}` : 'still running'].join(' · ')
    : [plural(evals.length, 'run'), plural(data.rows.length, 'example'), `baseline ${labels[baseline.id]}`].join(' · ')

  // The selected example (?ex=<row key>), and j/k through the shown examples.
  const exKey = params.get('ex')
  const selected = exKey ? examples.find((x) => x.r.row_key === exKey) ?? null : null
  const [order, setOrder] = useState<string[]>([])
  const select = (key: string | null) => setParam('ex', key)
  const pending = useRef<string | null>(null)
  useEffect(() => {
    const h = (e: KeyboardEvent) => {
      if (e.metaKey || e.ctrlKey || e.altKey || isTyping(e) || document.querySelector('.modal')) return
      if (e.key === 'Escape' && exKey) return select(null)
      if (e.key !== 'j' && e.key !== 'k') return
      if (!order.length) return
      e.preventDefault()
      const cur = pending.current ?? exKey
      const i = cur ? order.indexOf(cur) : -1
      const next = order[Math.max(0, Math.min(order.length - 1, i < 0 ? 0 : i + (e.key === 'j' ? 1 : -1)))]
      pending.current = next
      select(next)
      setTimeout(() => document.querySelector(`[data-ex="${CSS.escape(next)}"]`)?.scrollIntoView({ block: 'nearest' }), 0)
    }
    window.addEventListener('keydown', h)
    return () => window.removeEventListener('keydown', h)
  })
  useEffect(() => {
    if (pending.current === exKey) pending.current = null
  }, [exKey])

  if (!evals.length)
    return (
      <div className="page-frame detail">
        <PageHeader variant="detail" back={back} title="Compare evaluations" help="evaluations.md#evaluations" />
        <div className="quiet-empty">
          <p>None of these runs were found.</p>
        </div>
      </div>
    )
  const datasets = new Set(evals.map((e) => e.dataset_ref).filter(Boolean))

  return (
    <div className="page-frame detail">
      <PageHeader variant="detail" back={back} title={title} help="evaluations.md#evaluations" helpTopic="Evaluations" sentence={<Sentence size="detail" parts={sentenceParts} />} meta={meta} />
      <div className="ev-layout">
        <div className="ev-main">
          {datasets.size > 1 && <p className="ev-note">These runs use different datasets, so their examples may not line up.</p>}
          <RunsSection
            projectId={projectId}
            evals={evals}
            ids={ids}
            labels={labels}
            colorOf={colorOf}
            baselineId={baseline.id}
            setBaseline={setBaseline}
            onRemove={(id) => setIds(ids.filter((x) => x !== id))}
            onAdd={(id) => setIds([...ids, id])}
          />
          <MetricsSection
            evals={evals}
            metrics={metrics}
            labels={labels}
            colorOf={colorOf}
            baselineId={baseline.id}
            lowerBetter={lowerBetter}
            toggleLower={(k) => setLowerOverrides((o) => ({ ...o, [k]: !lowerBetter(k) }))}
          />
          {evals.length > 1 && metrics.length > 0 && (
            <details className="ev-more-details">
              <summary>
                Rank these runs <span className="quiet">by a weighted mix of metrics</span>
              </summary>
              <InlineLeaderboard projectId={projectId} evals={evals} metrics={metrics} labels={labels} colorOf={colorOf} lowerBetter={lowerBetter} value={lb} onChange={setLb} />
            </details>
          )}
          <ExamplesSection
            evals={evals}
            examples={examples}
            labels={labels}
            colorOf={colorOf}
            baselineId={baseline.id}
            lowerFor={(k) => lowerFor(metrics, lowerBetter, k)}
            filter={(params.get('show') as ExampleFilter | null) ?? 'all'}
            setFilter={(f) => setParam('show', f === 'all' ? null : f)}
            selectedKey={selected?.r.row_key ?? null}
            onSelect={(k) => select(k === selected?.r.row_key ? null : k)}
            onOrder={setOrder}
            total={rowsTotal}
          />
        </div>
        <div className="ev-side">
          {selected ? (
            <ExampleInspector
              projectId={projectId}
              ex={selected}
              evals={evals}
              labels={labels}
              colorOf={colorOf}
              baselineId={baseline.id}
              agentRuns={agentRuns}
              lowerFor={(k) => lowerFor(metrics, lowerBetter, k)}
              position={order.indexOf(selected.r.row_key)}
              count={order.length}
              onMove={(d) => {
                const i = order.indexOf(selected.r.row_key)
                const next = order[i + d]
                if (next) select(next)
              }}
              onClose={() => select(null)}
            />
          ) : (
            <RunsInspector projectId={projectId} evals={evals} labels={labels} colorOf={colorOf} baselineId={baseline.id} agentRuns={agentRuns} />
          )}
        </div>
      </div>
    </div>
  )
}

/** Direction for a row-level score key, borrowing the summary metric's toggle. */
function lowerFor(metrics: CompareMetric[], lowerBetter: (k: string) => boolean, k: string) {
  const m = metrics.find((mm) => mm.key === k || mm.key.startsWith(k + '.'))
  return m ? lowerBetter(m.key) : defaultLowerIsBetter(k)
}

// ---------------------------------------------------------------- runs

function RunsSection({
  projectId,
  evals,
  ids,
  labels,
  colorOf,
  baselineId,
  setBaseline,
  onRemove,
  onAdd,
}: {
  projectId: string
  evals: CompareEvaluation[]
  ids: string[]
  labels: Record<string, string>
  colorOf: Record<string, string>
  baselineId: string
  setBaseline: (id: string) => void
  onRemove: (id: string) => void
  onAdd: (id: string) => void
}) {
  return (
    <section className="ev-section" aria-labelledby="runs-h">
      <div className="ev-section-head">
        <h2 className="ev-h" id="runs-h">
          {evals.length === 1 ? 'Run' : 'Runs'}
          {evals.length > 1 && <span className="glist-count"> {evals.length}</span>}
        </h2>
        <AddRun projectId={projectId} ids={ids} onAdd={onAdd} />
      </div>
      <PlainTable caption="Runs in this comparison">
        <thead>
          <tr>
            <th scope="col">Model</th>
            <th scope="col">Evaluation</th>
            <th scope="col">Status</th>
            <th scope="col">Started</th>
            {evals.length > 1 && (
              <th scope="col">
                <span className="sr-only">Actions</span>
              </th>
            )}
          </tr>
        </thead>
        <tbody>
          {evals.map((e) => {
            const g = glyphOf(e.status)
            const modelUrl = e.model_ref ? refUrl(e.model_ref) : null
            const evalUrl = e.evaluation_ref ? refUrl(e.evaluation_ref) : null
            return (
              <tr key={e.id}>
                <td>
                  {evals.length > 1 && <span className="ev-swatch" style={{ background: colorOf[e.id] }} aria-hidden />}
                  {modelUrl ? <Link to={modelUrl}>{labels[e.id]}</Link> : labels[e.id]}
                  {evals.length > 1 && e.id === baselineId && <span className="ev-delta">baseline</span>}
                </td>
                <td>{evalUrl ? <Link to={evalUrl}>{e.evaluation_name}</Link> : e.evaluation_name}</td>
                <td>
                  <span className="ev-fail">
                    <StatusGlyph kind={g.kind} size={16} decorative /> {g.label}
                  </span>
                </td>
                <td title={fmtDate(e.started_at)}>{relativeTime(e.started_at)}</td>
                {evals.length > 1 && (
                  <td>
                    <span className="ev-runs-actions">
                      {e.id !== baselineId && (
                        <button type="button" className="linkish" onClick={() => setBaseline(e.id)}>
                          Make baseline
                        </button>
                      )}
                      <button type="button" className="linkish" onClick={() => onRemove(e.id)} aria-label={`Remove ${labels[e.id]} from the comparison`}>
                        Remove
                      </button>
                    </span>
                  </td>
                )}
              </tr>
            )
          })}
        </tbody>
      </PlainTable>
    </section>
  )
}

function AddRun({ projectId, ids, onAdd }: { projectId: string; ids: string[]; onAdd: (id: string) => void }) {
  const runs = useEvalRuns(projectId, { limit: 100 })
  const options = (runs.data ?? []).filter((c) => !ids.includes(c.id))
  if (!options.length) return null
  return (
    <label className="ev-controls">
      <span className="sr-only">Add a run to compare</span>
      <select className="input" value="" onChange={(e) => e.target.value && onAdd(e.target.value)} style={{ maxWidth: 300 }}>
        <option value="">{ids.length === 1 ? 'Compare with another run…' : 'Add a run…'}</option>
        {options.map((c) => {
          const i = evalRunInfo(c)
          return (
            <option key={c.id} value={c.id}>
              {i.evalName} · {i.modelName} · {relativeTime(c.started_at)}
            </option>
          )
        })}
      </select>
    </label>
  )
}

// ---------------------------------------------------------------- metrics

function Delta({ v, base, lower, kind, dfmt = 'both' }: { v: number | null; base: number | null; lower: boolean; kind: CompareMetric['kind']; dfmt?: DeltaFormat }) {
  if (v == null || base == null) return null
  const d = v - base
  if (Math.abs(d) < 1e-12) return <span className="ev-delta">{dfmt === 'ratio' ? '1×' : 'no change'}</span>
  const good = lower ? d < 0 : d > 0
  const abs = kind === 'fraction' ? `${d > 0 ? '+' : ''}${(d * 100).toFixed(1)} pts` : `${d > 0 ? '+' : ''}${fmtMetric(d)}`
  let txt = abs
  let rel = base !== 0 && kind !== 'fraction' ? ` (${d > 0 ? '+' : ''}${((d / Math.abs(base)) * 100).toFixed(0)}%)` : ''
  if (dfmt !== 'both') {
    // Fractions in absolute mode keep percentage points; the other formats are relative.
    txt = dfmt === 'absolute' ? abs : (fmtDeltaAs(base, v, dfmt) ?? abs)
    rel = ''
  }
  return (
    <span className={`ev-delta ${good ? 'better' : 'worse'}`}>
      <span aria-hidden>{good ? '▲' : '▼'} </span>
      {txt}
      {rel}
      <span className="sr-only"> ({good ? 'better' : 'worse'} than the baseline)</span>
    </span>
  )
}

function fmtMetricValue(key: string, v: unknown) {
  return typeof v === 'number' || typeof v === 'boolean' ? metricValue(key, v) : fmtMetric(v)
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
  const [dparams, setDparams] = useSearchParams()
  const dfmt = parseDeltaFormat(dparams.get('nfmt'))
  const keys = new Set(metrics.map((m) => m.key))
  // true_count is the same fact as true_fraction.
  const shown = metrics.filter((m) => !(m.key.endsWith('.true_count') && keys.has(m.key.replace(/true_count$/, 'true_fraction'))))
  const numeric = shown.filter((m) => Object.values(m.values).some((v) => toNum(v) != null))
  if (!metrics.length)
    return (
      <section className="ev-section" aria-labelledby="scores-h">
        <h2 className="ev-h" id="scores-h">
          Scores
        </h2>
        <p className="ev-note">{evals.some((e) => e.status === 'running') ? 'Scores appear when the run finishes.' : 'These runs have no scores.'}</p>
      </section>
    )
  const many = evals.length > 1
  return (
    <section className="ev-section" aria-labelledby="scores-h">
      <div className="ev-section-head">
        <h2 className="ev-h" id="scores-h">
          Scores
        </h2>
        {many && (
          <Segmented
            label="Change against the baseline as"
            size="sm"
            options={DELTA_FORMATS.map((f) => ({ id: f.id, label: f.label, title: f.title }))}
            value={dfmt}
            onChange={(id) =>
              setDparams(
                (p) => {
                  const n = new URLSearchParams(p)
                  if (id === 'both') n.delete('nfmt')
                  else n.set('nfmt', id)
                  return n
                },
                { replace: true },
              )
            }
          />
        )}
      </div>
      <PlainTable caption="Scores of each run">
        <thead>
          <tr>
            <th scope="col">Metric</th>
            <th scope="col" title="Which way is better; select to flip it">
              Better
            </th>
            {evals.map((e) => (
              <th key={e.id} scope="col" className="num">
                {many && <span className="ev-swatch" style={{ background: colorOf[e.id] }} aria-hidden />}
                {many ? labels[e.id] : 'Value'}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {shown.map((m) => {
            const base = toNum(m.values[baselineId])
            const lower = lowerBetter(m.key)
            const nums = evals.map((e) => toNum(m.values[e.id])).filter((x): x is number => x != null)
            const best = nums.length > 1 && new Set(nums).size > 1 ? (lower ? Math.min(...nums) : Math.max(...nums)) : null
            return (
              <tr key={m.key}>
                <td title={m.key}>
                  {metricWords(m.key)}
                  {isRunFigure(m.key) && <span className="ev-delta">not a score</span>}
                </td>
                <td>
                  <button type="button" className="ev-dir" onClick={() => toggleLower(m.key)} title="Flip which way is better" aria-label={`${metricWords(m.key)}: ${lower ? 'lower' : 'higher'} is better; flip`}>
                    {lower ? 'Lower' : 'Higher'}
                  </button>
                </td>
                {evals.map((e) => {
                  const raw = m.values[e.id]
                  const v = toNum(raw)
                  return (
                    <td key={e.id} className={`num ${best != null && v === best ? 'ev-best' : ''}`}>
                      {fmtMetricValue(m.key, raw)}
                      {many && e.id !== baselineId && <Delta v={v} base={base} lower={lower} kind={m.kind} dfmt={dfmt} />}
                    </td>
                  )
                })}
              </tr>
            )
          })}
        </tbody>
      </PlainTable>
      {numeric.length > 0 && (
        <details className="ev-more-details">
          <summary>
            Charts <span className="quiet">per metric, and per example</span>
          </summary>
          <div className="ev-section">
            <div className="ev-charts">
              {numeric.slice(0, 12).map((m) => {
                const rows = evals.map((e) => ({ id: e.id, name: labels[e.id], value: toNum(m.values[e.id]) }))
                const best = rows.reduce<number | null>((b, r) => (r.value == null ? b : b == null ? r.value : lowerBetter(m.key) ? Math.min(b, r.value) : Math.max(b, r.value)), null)
                return (
                  <figure key={m.key} className="ev-chart ev-panel" style={{ margin: 0, padding: 14 }}>
                    <figcaption className="ev-chart-title" title={m.key}>
                      {metricWords(m.key)} <span className="ev-note" style={{ display: 'inline' }}>({lowerBetter(m.key) ? 'lower' : 'higher'} is better)</span>
                    </figcaption>
                    <div style={{ height: 110 }}>
                      <ResponsiveContainer width="100%" height="100%">
                        <BarChart data={rows} margin={{ top: 6, right: 4, left: 0, bottom: 0 }}>
                          <CartesianGrid vertical={false} stroke="var(--separator)" />
                          <XAxis dataKey="name" tick={false} axisLine={{ stroke: 'var(--border)' }} height={4} />
                          <YAxis tick={tick} axisLine={false} tickLine={false} width={40} domain={m.kind === 'fraction' ? [0, 1] : [0, 'auto']} tickFormatter={(v: number) => (m.kind === 'fraction' ? `${Math.round(v * 100)}%` : fmtMetric(v))} />
                          <Tooltip {...tooltipStyle} cursor={{ fill: 'var(--panel-2)' }} formatter={(v: number) => fmtMetricValue(m.key, v)} />
                          <Bar dataKey="value" radius={[3, 3, 0, 0]} isAnimationActive={false}>
                            {rows.map((r) => (
                              <Cell key={r.id} fill={colorOf[r.id]} fillOpacity={r.value === best ? 1 : 0.55} />
                            ))}
                          </Bar>
                        </BarChart>
                      </ResponsiveContainer>
                    </div>
                  </figure>
                )
              })}
            </div>
          </div>
        </details>
      )}
    </section>
  )
}

// ---------------------------------------------------------------- scatter (inside the examples' disclosure)

function ScatterChartView({
  rows,
  evals,
  labels,
  colorOf,
  baselineId,
}: {
  rows: CompareRow[]
  evals: CompareEvaluation[]
  labels: Record<string, string>
  colorOf: Record<string, string>
  baselineId: string
}) {
  const aggs = useMemo(() => rows.map((r) => Object.fromEntries(evals.map((e) => [e.id, rowAgg(r.results[e.id])]))), [rows, evals])
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
  // Continuous scores first: boolean scatter plots are mostly overlapping points.
  const scoreKeys = [...keys.filter((k) => !NON_SCORE_KEYS.has(k) && !boolKeys.has(k)), ...keys.filter((k) => !NON_SCORE_KEYS.has(k) && boolKeys.has(k))]
  const [mode, setMode] = useState<'metrics' | 'models'>(evals.length > 1 ? 'models' : 'metrics')
  const [xKey, setXKey] = useState<string>(scoreKeys[0] ?? keys[0] ?? '')
  const [yKey, setYKey] = useState<string>(scoreKeys[1] ?? (keys.includes('model_latency') ? 'model_latency' : (keys[1] ?? keys[0] ?? '')))
  const [challenger, setChallenger] = useState<string>(evals.find((e) => e.id !== baselineId)?.id ?? '')
  if (!keys.length) return <p className="ev-note">No numeric scores per example.</p>
  const challengerId = evals.some((e) => e.id === challenger && e.id !== baselineId) ? challenger : (evals.find((e) => e.id !== baselineId)?.id ?? '')

  const keySelect = (label: string, v: string, set: (s: string) => void) => (
    <label className="ev-controls">
      <span className="ev-note">{label}</span>
      <select className="input" value={v} onChange={(e) => set(e.target.value)}>
        {keys.map((k) => (
          <option key={k} value={k}>
            {metricWords(k)}
          </option>
        ))}
      </select>
    </label>
  )

  // Jitter booleans slightly so overlapping points stay visible.
  const jitter = (v: number | boolean | undefined, i: number) => {
    const n = toNum(v)
    if (n == null) return null
    return typeof v === 'boolean' ? n + (((i * 7919) % 100) / 100 - 0.5) * 0.12 : n
  }

  let series: { id: string; name: string; color: string; points: { x: number; y: number; row: number }[] }[] = []
  let xLabel = metricWords(xKey)
  let yLabel = metricWords(yKey)
  if (mode === 'metrics' || !challengerId) {
    series = evals.map((e) => ({
      id: e.id,
      name: labels[e.id],
      color: colorOf[e.id],
      points: aggs
        .map((a, i) => ({ x: jitter(a[e.id]?.[xKey], i), y: jitter(a[e.id]?.[yKey], i + 31), row: i }))
        .filter((p): p is { x: number; y: number; row: number } => p.x != null && p.y != null),
    }))
  } else {
    xLabel = `${metricWords(xKey)} · ${labels[baselineId]}`
    yLabel = `${metricWords(xKey)} · ${labels[challengerId]}`
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
    <div className="ev-section">
      <div className="ev-controls">
        {evals.length > 1 && (
          <Segmented
            label="Scatter plot"
            size="sm"
            options={[
              { id: 'models', label: 'Baseline against another run' },
              { id: 'metrics', label: 'One metric against another' },
            ]}
            value={mode}
            onChange={setMode}
          />
        )}
        {mode === 'metrics' || !challengerId ? (
          <>
            {keySelect('Across', xKey, setXKey)}
            {keySelect('Up', yKey, setYKey)}
          </>
        ) : (
          <>
            {keySelect('Metric', xKey, setXKey)}
            <label className="ev-controls">
              <span className="ev-note">Run</span>
              <select className="input" value={challengerId} onChange={(e) => setChallenger(e.target.value)}>
                {evals
                  .filter((e) => e.id !== baselineId)
                  .map((e) => (
                    <option key={e.id} value={e.id}>
                      {labels[e.id]}
                    </option>
                  ))}
              </select>
            </label>
            <span className="ev-note">Points above the diagonal: that run scored higher than the baseline.</span>
          </>
        )}
      </div>
      {total === 0 ? (
        <p className="ev-note">No numeric values for this choice.</p>
      ) : (
        <div style={{ height: 320 }}>
          <div className="ev-note">↑ {yLabel}</div>
          <ResponsiveContainer width="100%" height="94%">
            <ScatterChart margin={{ top: 10, right: 16, bottom: 24, left: 0 }}>
              <CartesianGrid stroke="var(--separator)" />
              <XAxis type="number" dataKey="x" name={xLabel} tick={tick} stroke="var(--border)" label={{ value: xLabel, position: 'insideBottom', offset: -12, fill: 'var(--text-muted)', fontSize: 11 }} domain={dom} allowDataOverflow={!!shared} tickFormatter={(v: number) => fmtMetric(v)} />
              <YAxis type="number" dataKey="y" name={yLabel} tick={tick} stroke="var(--border)" width={48} domain={dom} allowDataOverflow={!!shared} tickFormatter={(v: number) => fmtMetric(v)} />
              <ZAxis range={[36, 36]} />
              <Tooltip {...tooltipStyle} formatter={(v: number) => fmtMetric(v)} labelFormatter={() => ''} />
              {paired && <ReferenceLine segment={diagonal(series)} stroke="var(--text-muted)" strokeDasharray="4 4" />}
              {series.length > 1 && <Legend wrapperStyle={{ fontSize: 12 }} />}
              {series.map((s) => (
                <Scatter key={s.id} name={s.name} data={s.points} fill={s.color} fillOpacity={0.7} isAnimationActive={false} />
              ))}
            </ScatterChart>
          </ResponsiveContainer>
        </div>
      )}
    </div>
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

// ---------------------------------------------------------------- examples

interface Ex {
  r: CompareRow
  idx: number
  aggs: Record<string, Record<string, number | boolean>>
  scoreKeys: string[]
  cmp: CmpExample
  failedAny: boolean
  disagree: boolean
  regressed: boolean
  improved: boolean
  text: string
}

const PAGE = 50

function ExamplesSection({
  evals,
  examples,
  labels,
  colorOf,
  baselineId,
  lowerFor,
  filter,
  setFilter,
  selectedKey,
  onSelect,
  onOrder,
  total,
}: {
  evals: CompareEvaluation[]
  examples: Ex[]
  labels: Record<string, string>
  colorOf: Record<string, string>
  baselineId: string
  lowerFor: (k: string) => boolean
  filter: ExampleFilter
  setFilter: (f: ExampleFilter) => void
  selectedKey: string | null
  onSelect: (key: string) => void
  onOrder: (keys: string[]) => void
  total: number
}) {
  const [search, setSearch] = useState('')
  const [offset, setOffset] = useState(0)
  const many = evals.length > 1
  const counts: Record<ExampleFilter, number> = {
    all: examples.length,
    failed: examples.filter((x) => x.failedAny).length,
    regressed: examples.filter((x) => x.regressed).length,
    improved: examples.filter((x) => x.improved).length,
    disagree: examples.filter((x) => x.disagree).length,
  }
  const filters: { id: ExampleFilter; label: string }[] = [
    { id: 'all', label: `All ${counts.all}` },
    { id: 'failed', label: `Failed ${counts.failed}` },
    ...(many
      ? ([
          { id: 'regressed', label: `Worse ${counts.regressed}` },
          { id: 'improved', label: `Better ${counts.improved}` },
          { id: 'disagree', label: `Differ ${counts.disagree}` },
        ] as { id: ExampleFilter; label: string }[])
      : []),
  ]
  const active: ExampleFilter = filters.some((f) => f.id === filter) ? filter : 'all'
  // Failing and worse examples first, then in dataset order.
  const rank = (x: Ex) => (x.failedAny ? 0 : 2) + (x.regressed ? 0 : 1)
  const rows = useMemo(
    () =>
      examples
        .filter((x) => (active === 'all' || x[active === 'failed' ? 'failedAny' : active]) && (!search || x.text.includes(search.toLowerCase())))
        .sort((a, b) => rank(a) - rank(b) || a.idx - b.idx),
    [examples, active, search],
  )
  const keys = rows.map((x) => x.r.row_key).join('\n')
  useEffect(() => onOrder(rows.map((x) => x.r.row_key)), [keys]) // eslint-disable-line react-hooks/exhaustive-deps
  // Keep the selected example's page in view.
  useEffect(() => {
    if (!selectedKey) return
    const i = rows.findIndex((x) => x.r.row_key === selectedKey)
    if (i >= 0 && (i < offset || i >= offset + PAGE)) setOffset(Math.floor(i / PAGE) * PAGE)
  }, [selectedKey]) // eslint-disable-line react-hooks/exhaustive-deps
  const shown = rows.slice(offset, offset + PAGE)

  return (
    <section className="ev-section" aria-labelledby="examples-h">
      <div className="ev-section-head">
        <h2 className="ev-h" id="examples-h">
          Examples <span className="glist-count">{examples.length}</span>
        </h2>
        <SearchField label="Search the examples’ inputs" value={search} onChange={(v) => (setSearch(v), setOffset(0))} placeholder="Search inputs" width={220} />
      </div>
      {examples.length > 0 && (
        <Segmented
          label="Which examples"
          size="sm"
          options={filters}
          value={active}
          onChange={(f) => {
            setFilter(f)
            setOffset(0)
          }}
        />
      )}
      {rows.length === 0 ? (
        <p className="ev-note">
          {examples.length === 0 ? (evals.some((e) => e.status === 'running') ? 'Examples appear as the run scores them.' : 'No per-example results were recorded for these runs.') : 'No examples match.'}
        </p>
      ) : (
        <>
          <p className="ev-note">
            {many ? 'Failed and worse examples first.' : 'Failed examples first.'} Select one for its details; j and k move between them.
            {total > examples.length ? ` ${total} results in all, ${examples.length} examples.` : ''}
          </p>
          <PlainTable caption="Examples and each run’s answer">
            <thead>
              <tr>
                <th scope="col" className="num">
                  #
                </th>
                <th scope="col">Input</th>
                {evals.map((e) => (
                  <th key={e.id} scope="col">
                    {many && <span className="ev-swatch" style={{ background: colorOf[e.id] }} aria-hidden />}
                    {many ? labels[e.id] : 'Answer'}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {shown.map((x) => {
                const sel = x.r.row_key === selectedKey
                return (
                  <tr
                    key={x.r.row_key + x.idx}
                    className="ev-row"
                    data-ex={x.r.row_key}
                    aria-selected={sel}
                    tabIndex={0}
                    onClick={() => onSelect(x.r.row_key)}
                    onKeyDown={(e) => {
                      if (e.key === 'Enter' || e.key === ' ') {
                        e.preventDefault()
                        onSelect(x.r.row_key)
                      }
                    }}
                    aria-label={`Example ${x.idx + 1}${x.failedAny ? ', failed' : ''}${x.regressed ? ', worse than the baseline' : ''}`}
                  >
                    <td className="num">{x.idx + 1}</td>
                    <td>
                      <span className="ev-cell-text">{inputsText(x.r.inputs)}</span>
                    </td>
                    {evals.map((e) => {
                      const res = x.r.results[e.id]
                      return (
                        <td key={e.id}>
                          {!res?.length ? (
                            <span className="ev-note">no result</span>
                          ) : (
                            <>
                              <span className="ev-cell-text">
                                {x.cmp.failed[e.id] && (
                                  <span className="ev-fail" style={{ marginRight: 6 }}>
                                    <StatusGlyph kind="failed" size={14} label="Failed" />
                                  </span>
                                )}
                                {preview(res[0].output, 240) || <span className="ev-note">empty</span>}
                              </span>
                              <ScoreText aggs={x.aggs} id={e.id} baselineId={baselineId} keys={x.scoreKeys} lowerFor={lowerFor} />
                              {res.length > 1 && <span className="ev-delta">{res.length} trials, averaged</span>}
                            </>
                          )}
                        </td>
                      )
                    })}
                  </tr>
                )
              })}
            </tbody>
          </PlainTable>
          {rows.length > PAGE && (
            <div className="ev-pager">
              <button type="button" className="qbtn" disabled={offset === 0} onClick={() => setOffset(Math.max(0, offset - PAGE))}>
                Previous
              </button>
              <span>
                {offset + 1}–{Math.min(rows.length, offset + PAGE)} of {rows.length}
              </span>
              <button type="button" className="qbtn" disabled={offset + PAGE >= rows.length} onClick={() => setOffset(offset + PAGE)}>
                Next
              </button>
            </div>
          )}
        </>
      )}
      {examples.length > 0 && (
        <details className="ev-more-details">
          <summary>
            Scatter plot <span className="quiet">of the examples’ scores</span>
          </summary>
          <ScatterChartView rows={examples.map((x) => x.r)} evals={evals} labels={labels} colorOf={colorOf} baselineId={baselineId} />
        </details>
      )}
    </section>
  )
}

function inputsText(inputs: Any): string {
  if (!inputs || typeof inputs !== 'object' || Array.isArray(inputs)) return preview(inputs, 200)
  const entries = Object.entries(inputs)
  if (entries.length === 1) return preview(entries[0][1], 200)
  return entries
    .slice(0, 4)
    .map(([k, v]) => `${k}: ${preview(v, 120)}`)
    .join(' · ')
}

/** The example's scores as text, "exact no · similarity 0.18 (▼)", with the change against the baseline in words. */
function ScoreText({
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
    <span className="ev-cell-scores">
      {present.map((k, i) => {
        const v = a[k]
        const nv = toNum(v)
        const nb = toNum(b[k])
        let dir: 'better' | 'worse' | null = null
        if (id !== baselineId && nv != null && nb != null && Math.abs(nv - nb) > 1e-12) dir = (lowerFor(k) ? nv < nb : nv > nb) ? 'better' : 'worse'
        return (
          <span key={k} title={`${k}: ${fmtMetric(v)}${id !== baselineId && nb != null ? ` (baseline ${fmtMetric(b[k])})` : ''}`}>
            {i > 0 && ' · '}
            {metricWords(k)} {typeof v === 'boolean' ? (v ? 'pass' : 'fail') : metricValue(k, v)}
            {dir && (
              <span className={dir} title={`${dir === 'better' ? 'Better' : 'Worse'} than the baseline`}>
                {' '}
                <span aria-hidden>{dir === 'better' ? '▲' : '▼'}</span>
                <span className="sr-only">({dir} than the baseline)</span>
              </span>
            )}
          </span>
        )
      })}
    </span>
  )
}

// ---------------------------------------------------------------- inspector

function ExampleInspector({
  projectId,
  ex,
  evals,
  labels,
  colorOf,
  baselineId,
  agentRuns,
  lowerFor,
  position,
  count,
  onMove,
  onClose,
}: {
  projectId: string
  ex: Ex
  evals: CompareEvaluation[]
  labels: Record<string, string>
  colorOf: Record<string, string>
  baselineId: string
  agentRuns?: Map<string, AgentEvalRun>
  lowerFor: (k: string) => boolean
  position: number
  count: number
  onMove: (d: number) => void
  onClose: () => void
}) {
  const many = evals.length > 1
  const state = [ex.failedAny ? 'failed' : 'passed', many && ex.regressed ? 'worse than the baseline' : '', many && ex.improved ? 'better than the baseline' : ''].filter(Boolean).join(', ')
  return (
    <Inspector title={`Example ${ex.idx + 1}`} subtitle={`${position >= 0 ? `${position + 1} of ${count} shown · ` : ''}${state}`} label="Selected example" sticky>
      <div className="ev-insp-nav">
        <button type="button" className="qbtn" disabled={position <= 0} onClick={() => onMove(-1)} title="Previous example (k)">
          Previous
        </button>
        <button type="button" className="qbtn" disabled={position < 0 || position >= count - 1} onClick={() => onMove(1)} title="Next example (j)">
          Next
        </button>
        <button type="button" className="linkish" onClick={onClose} title="Back to the runs (Escape)">
          Close
        </button>
      </div>
      <div className="ev-insp-block">
        <span className="ev-insp-label">Input</span>
        <ValueView value={ex.r.inputs} projectId={projectId} />
      </div>
      <InspectorDivider />
      {evals.map((e) => {
        const res = ex.r.results[e.id] ?? []
        return (
          <div key={e.id} className="ev-insp-run">
            <span className="ev-insp-label">
              {many && <span className="ev-swatch" style={{ background: colorOf[e.id] }} aria-hidden />}
              {labels[e.id]}
              {many && e.id === baselineId ? ' (baseline)' : ''}
              {ex.cmp.failed[e.id] ? ' · failed' : ''}
            </span>
            {res.length === 0 && <p className="ev-note">No result for this example.</p>}
            {res.map((t, ti) => (
              <div key={t.span_id || ti} className="ev-insp-block">
                {res.length > 1 && <span className="ev-note">Trial {t.trial + 1}</span>}
                <ValueView value={t.output} projectId={projectId} markdown />
                <ScoreText aggs={{ [e.id]: rowAgg([t]), [baselineId]: ex.aggs[baselineId] }} id={e.id} baselineId={baselineId} keys={ex.scoreKeys} lowerFor={lowerFor} />
                <KeyValueList
                  items={[
                    { label: 'Latency', value: t.model_latency != null ? shortDuration(t.model_latency * 1000) : '—', help: 'How long the model took to answer' },
                    { label: 'Tokens', value: fmtNumber(t.total_tokens) },
                    t.span_id ? { label: 'Step id', value: <CopyText text={t.span_id} label="step id" />, mono: true } : null,
                  ]}
                />
                <InspectorLinks>
                  {t.span_id && (
                    <Link to={spanUrl(projectId, t.span_id)} title="The predict-and-score span with its inputs, output and attributes">
                      Show the raw step
                    </Link>
                  )}
                </InspectorLinks>
                {t.span_id && <LinkedCallsFor projectId={projectId} callId={t.span_id} />}
                {t.span_id && agentRuns?.has(e.id) && <PredictionAgentLinks projectId={projectId} runId={e.id} pasId={t.span_id} />}
                <details>
                  <summary className="ev-note" style={{ cursor: 'pointer', display: 'list-item' }}>
                    Raw scores
                  </summary>
                  <ValueView value={t.scores} projectId={projectId} />
                </details>
              </div>
            ))}
          </div>
        )
      })}
    </Inspector>
  )
}

function RunsInspector({
  projectId,
  evals,
  labels,
  colorOf,
  baselineId,
  agentRuns,
}: {
  projectId: string
  evals: CompareEvaluation[]
  labels: Record<string, string>
  colorOf: Record<string, string>
  baselineId: string
  agentRuns?: Map<string, AgentEvalRun>
}) {
  const many = evals.length > 1
  return (
    <Inspector title={many ? 'The runs' : 'This run'} subtitle="Select an example for its details." label="Run details" sticky>
      {evals.map((e) => {
        const ml = toNum((e.summary as Any)?.model_latency?.mean)
        const a = agentRuns?.get(e.id)
        const g = glyphOf(e.status)
        return (
          <div key={e.id} className="ev-insp-run">
            {many && (
              <span className="ev-insp-label">
                <span className="ev-swatch" style={{ background: colorOf[e.id] }} aria-hidden />
                {labels[e.id]}
                {e.id === baselineId ? ' (baseline)' : ''}
              </span>
            )}
            <KeyValueList
              items={[
                { label: 'Status', value: g.label, tone: g.kind === 'failed' ? 'error' : g.kind === 'warning' ? 'warn' : undefined },
                { label: 'Started', value: fmtDate(e.started_at) },
                { label: 'Duration', value: evalDurationMs(e) != null ? shortDuration(evalDurationMs(e)) : 'still running' },
                { label: 'Examples', value: fmtNumber(e.row_count), help: 'Results recorded (examples × trials)' },
                { label: 'Tokens', value: fmtNumber(e.total_tokens) },
                { label: 'Cost', value: fmtCost(e.cost_usd), help: 'From the project’s price table' },
                ml != null ? { label: 'Latency', value: shortDuration(ml * 1000), help: 'The model’s mean time per example' } : null,
                a ? { label: 'Agents', value: `${plural(a.conversation_count, 'conversation')}, ${plural(a.span_count, 'span')}${a.error_count ? `, ${a.error_count} failed` : ''}` } : null,
                { label: 'Run id', value: <CopyText text={e.id} label="run id" />, mono: true },
              ]}
            />
            <InspectorLinks>
              {e.evaluation_ref && refUrl(e.evaluation_ref) && <Link to={refUrl(e.evaluation_ref)!}>The evaluation, {e.evaluation_name}</Link>}
              {e.model_ref && refUrl(e.model_ref) && <Link to={refUrl(e.model_ref)!}>The model, {e.model_name}</Link>}
              <Link to={spanUrl(projectId, e.id)} title="The Evaluation.evaluate span, with every prediction under it">
                Show the raw run
              </Link>
              {a && <EvalAgentLinks projectId={projectId} runId={e.id} />}
            </InspectorLinks>
          </div>
        )
      })}
    </Inspector>
  )
}
