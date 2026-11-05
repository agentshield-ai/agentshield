import { useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { leaderboardsApi } from '../../api/leaderboards'
import type { CompareEvaluation, CompareMetric } from '../../api/types'
import { leaderboardUrl } from '../../lib/urls'
import { ErrorBox, Modal } from '../common'
import { PlainTable } from '../ui/PlainTable'
import { metricWords } from '../../lib/summaries/evaluate'
import { classVal } from '../objects/objectUtils'
import { slugify } from '../traces/callUtils'
import { fmtMetric, toNum } from './evalUtils'
import { leaderboardColumns, rankRuns, type LbMetric } from './leaderboardUtils'

function SaveLeaderboardModal({
  projectId,
  evals,
  selected,
  metrics,
  onClose,
}: {
  projectId: string
  evals: CompareEvaluation[]
  selected: LbMetric[]
  metrics: CompareMetric[]
  onClose: () => void
}) {
  const qc = useQueryClient()
  const evalNames = [...new Set(evals.map((e) => e.evaluation_name).filter(Boolean))]
  const [name, setName] = useState(slugify(`${evalNames[0] ?? 'eval'}-leaderboard`))
  const [description, setDescription] = useState(`Ranks models on ${evalNames.join(', ') || 'these evaluations'} by ${selected.map((s) => s.key).join(', ')}.`)
  const columns = leaderboardColumns(evals, selected, metrics)
  const save = useMutation({
    mutationFn: async () => {
      const id = slugify(name)
      if (!id) throw new Error('Enter a name')
      const val = {
        ...classVal('Leaderboard', [], { name: id, description, columns }),
        weft_dynamic: { evaluation_run_ids: evals.map((e) => e.id), metrics: selected },
      }
      await leaderboardsApi.save(projectId, id, val as Parameters<typeof leaderboardsApi.save>[2])
      return id
    },
    onSuccess: () => void qc.invalidateQueries({ queryKey: ['objects', projectId] }),
  })
  return (
    <Modal
      title="Save as leaderboard"
      onClose={onClose}
      footer={
        save.isSuccess ? (
          <>
            <button className="btn" onClick={onClose}>
              Close
            </button>
            <Link className="btn primary" to={leaderboardUrl(projectId, save.data)}>
              Open leaderboard →
            </Link>
          </>
        ) : (
          <>
            <button className="btn" onClick={onClose}>
              Cancel
            </button>
            <button className="btn primary" disabled={!slugify(name) || !columns.length || save.isPending} onClick={() => save.mutate()}>
              Save
            </button>
          </>
        )
      }
    >
      <div className="col" style={{ gap: 10 }}>
        <span className="small muted">
          Saves a Leaderboard object with one column per metric and evaluation. Saving with an existing name creates a new version. The weights and the
          compared runs are kept so the comparison can be reopened.
        </span>
        <label className="field">
          Name
          <input className="input" value={name} onChange={(e) => setName(e.target.value)} autoFocus />
        </label>
        <label className="field">
          Description
          <textarea className="input" rows={2} value={description} onChange={(e) => setDescription(e.target.value)} />
        </label>
        <div className="small">
          <b>{columns.length}</b> column{columns.length === 1 ? '' : 's'}:{' '}
          {columns.map((c, i) => (
            <span key={i} className="chip mono" style={{ marginRight: 4 }}>
              {c.scorer_name}.{c.summary_metric_path} {c.should_minimize ? '↓' : '↑'}
            </span>
          ))}
        </div>
        <ErrorBox error={save.error} />
        {save.isSuccess && <div className="info-box">Saved leaderboard {save.data}.</div>}
      </div>
    </Modal>
  )
}

const fmtVal = (v: unknown, kind: CompareMetric['kind']) => (kind === 'fraction' && typeof v === 'number' ? `${(v * 100).toFixed(1)}%` : fmtMetric(v))

/** Rank the compared runs by a weighted mix of the selected summary metrics. */
export function InlineLeaderboard({
  evals,
  metrics,
  labels,
  colorOf,
  lowerBetter,
  value,
  onChange,
  projectId,
}: {
  projectId?: string
  evals: CompareEvaluation[]
  metrics: CompareMetric[]
  labels: Record<string, string>
  colorOf: Record<string, string>
  lowerBetter: (k: string) => boolean
  /** Selected metrics; empty means the default selection. */
  value: LbMetric[]
  onChange: (m: LbMetric[]) => void
}) {
  const [configuring, setConfiguring] = useState(false)
  const [saving, setSaving] = useState(false)
  const numeric = useMemo(() => metrics.filter((m) => evals.some((e) => toNum(m.values[e.id]) != null)), [metrics, evals])
  const defaults = useMemo<LbMetric[]>(() => {
    const scores = numeric.filter((m) => m.scorer && !/latency|tokens/.test(m.key) && !m.key.endsWith('.true_count'))
    return (scores.length ? scores : numeric).slice(0, 4).map((m) => ({ key: m.key, weight: 1, lowerBetter: lowerBetter(m.key) }))
  }, [numeric, lowerBetter])
  const selected = (value.length ? value : defaults).filter((s) => numeric.some((m) => m.key === s.key))
  const byKey = Object.fromEntries(numeric.map((m) => [m.key, m]))
  const values = Object.fromEntries(numeric.map((m) => [m.key, Object.fromEntries(evals.map((e) => [e.id, toNum(m.values[e.id])]))]))
  const ranked = rankRuns(
    evals.map((e) => e.id),
    values,
    selected,
  )
  const set = (key: string, patch: Partial<LbMetric> | null) => {
    const cur = selected.find((s) => s.key === key)
    let next: LbMetric[]
    if (patch === null) next = selected.filter((s) => s.key !== key)
    else if (cur) next = selected.map((s) => (s.key === key ? { ...s, ...patch } : s))
    else next = [...selected, { key, weight: 1, lowerBetter: lowerBetter(key), ...patch }]
    onChange(next)
  }
  if (evals.length < 2 || !numeric.length) return null
  const active = selected.filter((s) => s.weight > 0)
  return (
    <div className="ev-section">
      <div className="ev-controls">
        <span className="quiet-note">Each run gets 0 to 100 per metric, from the worst run to the best, and the weighted mean ranks them.</span>
        <button type="button" className="qbtn" aria-expanded={configuring} onClick={() => setConfiguring((c) => !c)}>
          {configuring ? 'Done' : 'Metrics and weights'}
        </button>
        {value.length > 0 && (
          <button type="button" className="linkish" onClick={() => onChange([])} title="Back to the default metrics">
            Reset
          </button>
        )}
        {projectId && (
          <button type="button" className="qbtn" disabled={!active.length} onClick={() => setSaving(true)} title="Save these metrics as a Leaderboard object">
            Save as leaderboard
          </button>
        )}
      </div>
      {configuring && (
        <PlainTable caption="Metrics to rank by" className="ev-wide">
          <thead>
            <tr>
              <th scope="col">
                <span className="sr-only">Use</span>
              </th>
              <th scope="col">Metric</th>
              <th scope="col">Better</th>
              <th scope="col">Weight</th>
            </tr>
          </thead>
          <tbody>
            {numeric.map((m) => {
              const s = selected.find((x) => x.key === m.key)
              return (
                <tr key={m.key}>
                  <td>
                    <input type="checkbox" aria-label={`Rank by ${metricWords(m.key)}`} checked={!!s} onChange={(e) => set(m.key, e.target.checked ? {} : null)} />
                  </td>
                  <td title={m.key}>{metricWords(m.key)}</td>
                  <td>
                    <button type="button" className="ev-dir" disabled={!s} onClick={() => s && set(m.key, { lowerBetter: !s.lowerBetter })}>
                      {(s?.lowerBetter ?? lowerBetter(m.key)) ? 'Lower' : 'Higher'}
                    </button>
                  </td>
                  <td>
                    <input
                      className="input"
                      type="number"
                      min={0}
                      max={100}
                      step={0.5}
                      disabled={!s}
                      aria-label={`Weight for ${metricWords(m.key)}`}
                      value={s?.weight ?? 1}
                      onChange={(e) => set(m.key, { weight: Math.max(0, Number(e.target.value) || 0) })}
                      style={{ width: 80 }}
                    />
                  </td>
                </tr>
              )
            })}
          </tbody>
        </PlainTable>
      )}
      {active.length === 0 ? (
        <p className="quiet-note">Pick at least one metric with a weight above 0.</p>
      ) : (
        <PlainTable caption="Runs ranked by the chosen metrics" className="ev-wide">
          <thead>
            <tr>
              <th scope="col" className="num">
                Rank
              </th>
              <th scope="col">Run</th>
              <th scope="col" className="num" title="Weighted mean of each metric scaled 0–100 between the worst and best run">
                Score
              </th>
              {active.map((s) => (
                <th key={s.key} scope="col" className="num" title={`${s.key} · weight ${s.weight} · ${s.lowerBetter ? 'lower' : 'higher'} is better`}>
                  {metricWords(s.key)}
                  {s.weight !== 1 ? ` ×${s.weight}` : ''}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {ranked.map((r) => (
              <tr key={r.id}>
                <td className={`num ${r.rank === 1 && r.score != null ? 'ev-best' : ''}`}>{r.rank}</td>
                <td>
                  <span className="ev-swatch" style={{ background: colorOf[r.id] }} aria-hidden />
                  {labels[r.id]}
                </td>
                <td className="num">
                  {r.score == null ? '—' : (r.score * 100).toFixed(0)}
                  {r.missing.length > 0 && <span className="ev-delta">missing {r.missing.length}</span>}
                </td>
                {active.map((s) => (
                  <td key={s.key} className={`num ${r.parts[s.key] === 1 && ranked.some((o) => (o.parts[s.key] ?? 0) < 1) ? 'ev-best' : ''}`}>
                    {fmtVal(byKey[s.key]?.values[r.id], byKey[s.key]?.kind ?? 'other')}
                  </td>
                ))}
              </tr>
            ))}
          </tbody>
        </PlainTable>
      )}
      {saving && projectId && <SaveLeaderboardModal projectId={projectId} evals={evals} selected={active} metrics={numeric} onClose={() => setSaving(false)} />}
    </div>
  )
}
