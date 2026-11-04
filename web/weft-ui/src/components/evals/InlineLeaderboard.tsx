import { useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { leaderboardsApi } from '../../api/leaderboards'
import type { CompareEvaluation, CompareMetric } from '../../api/types'
import { leaderboardUrl } from '../../lib/urls'
import { ErrorBox, Modal, Section } from '../common'
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
    <Section
      title="Leaderboard"
      actions={
        <div className="row" style={{ gap: 6 }}>
          {value.length > 0 && (
            <button className="btn ghost sm" onClick={() => onChange([])} title="Back to the default metrics">
              Reset
            </button>
          )}
          <button className="btn sm" aria-expanded={configuring} onClick={() => setConfiguring((c) => !c)}>
            {configuring ? 'Done' : 'Metrics & weights'}
          </button>
          {projectId && (
            <button className="btn sm" disabled={!active.length} onClick={() => setSaving(true)} title="Save these metrics as a Leaderboard object">
              Save as leaderboard
            </button>
          )}
        </div>
      }
    >
      <div className="col" style={{ gap: 12 }}>
        {configuring && (
          <div className="table-wrap">
            <table className="table">
              <thead>
                <tr>
                  <th style={{ width: 28 }} />
                  <th>Metric</th>
                  <th>Better</th>
                  <th style={{ width: 110 }}>Weight</th>
                </tr>
              </thead>
              <tbody>
                {numeric.map((m) => {
                  const s = selected.find((x) => x.key === m.key)
                  return (
                    <tr key={m.key}>
                      <td>
                        <input type="checkbox" aria-label={`Rank by ${m.key}`} checked={!!s} onChange={(e) => set(m.key, e.target.checked ? {} : null)} />
                      </td>
                      <td className="mono small">{m.key}</td>
                      <td>
                        <button className="btn ghost sm" disabled={!s} onClick={() => s && set(m.key, { lowerBetter: !s.lowerBetter })}>
                          {(s?.lowerBetter ?? lowerBetter(m.key)) ? '↓ lower' : '↑ higher'}
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
                          aria-label={`Weight for ${m.key}`}
                          value={s?.weight ?? 1}
                          onChange={(e) => set(m.key, { weight: Math.max(0, Number(e.target.value) || 0) })}
                          style={{ width: 80 }}
                        />
                      </td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        )}
        {active.length === 0 ? (
          <div className="faint small">Pick at least one metric with a weight above 0.</div>
        ) : (
          <div className="table-wrap">
            <table className="table">
              <thead>
                <tr>
                  <th style={{ width: 48 }}>Rank</th>
                  <th>Run</th>
                  <th style={{ minWidth: 140 }} title="Weighted mean of each metric scaled 0–100% between the worst and best run">
                    Score
                  </th>
                  {active.map((s) => (
                    <th key={s.key} className="num" title={`${s.key} · weight ${s.weight} · ${s.lowerBetter ? 'lower' : 'higher'} is better`}>
                      <span className="mono small">{s.key}</span>
                      <span className="faint small">
                        {' '}
                        {s.lowerBetter ? '↓' : '↑'}
                        {s.weight !== 1 ? ` ×${s.weight}` : ''}
                      </span>
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {ranked.map((r) => (
                  <tr key={r.id}>
                    <td>
                      <span className={`badge ${r.rank === 1 && r.score != null ? 'accent' : ''}`}>#{r.rank}</span>
                    </td>
                    <td>
                      <span className="row" style={{ gap: 6, minWidth: 0 }}>
                        <span className="swatch" style={{ background: colorOf[r.id] }} />
                        <span className="truncate">{labels[r.id]}</span>
                      </span>
                    </td>
                    <td>
                      {r.score == null ? (
                        <span className="faint">—</span>
                      ) : (
                        <div className="row" style={{ gap: 8 }}>
                          <div style={{ flex: 1, height: 6, background: 'var(--panel-2)', borderRadius: 3, minWidth: 60 }} aria-hidden>
                            <div style={{ width: `${Math.round(r.score * 100)}%`, height: '100%', background: colorOf[r.id], borderRadius: 3 }} />
                          </div>
                          <span className="mono small">{(r.score * 100).toFixed(0)}</span>
                        </div>
                      )}
                      {r.missing.length > 0 && <div className="small faint">missing {r.missing.length}</div>}
                    </td>
                    {active.map((s) => (
                      <td key={s.key} className={`num mono ${r.parts[s.key] === 1 && ranked.some((o) => (o.parts[s.key] ?? 0) < 1) ? 'best' : ''}`}>
                        {fmtVal(byKey[s.key]?.values[r.id], byKey[s.key]?.kind ?? 'other')}
                      </td>
                    ))}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
      {saving && projectId && <SaveLeaderboardModal projectId={projectId} evals={evals} selected={active} metrics={numeric} onClose={() => setSaving(false)} />}
    </Section>
  )
}
