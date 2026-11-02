import { useMemo, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { api, ApiError } from '../../api/client'
import { parseRef } from '../../api/refs'
import type { LeaderboardComputeRes } from '../../api/types'
import { Empty, ErrorBox, Loading } from '../../components/common'
import { LeaderboardEditor } from '../../components/evals/LeaderboardEditor'
import { fmtMetric, toNum } from '../../components/evals/evalUtils'
import { useProjectId } from '../../hooks/useProject'
import { fmtDate, fmtRelative } from '../../lib/format'
import { callUrl, compareEvaluationsUrl, newEvaluationUrl, objectUrl, objectVersionsUrl, refUrl } from '../../lib/urls'

type Row = LeaderboardComputeRes['rows'][number]

export function LeaderboardPage() {
  const projectId = useProjectId()
  const { objectId = '' } = useParams()
  const nav = useNavigate()
  const qc = useQueryClient()
  const [editing, setEditing] = useState(false)
  const [sort, setSort] = useState<{ col: string; dir: 1 | -1 }>({ col: 'rank', dir: 1 })
  const q = useQuery({
    queryKey: ['leaderboard', projectId, objectId],
    queryFn: () => api.leaderboardCompute(projectId, objectId),
    retry: (n, e) => !(e instanceof ApiError && e.status === 404) && n < 1,
  })

  const data = q.data
  const stats = useMemo(() => {
    if (!data) return null
    const best: Record<string, number | null> = {}
    const range: Record<string, [number, number]> = {}
    const ranks: Record<string, number[]> = {}
    for (const c of data.columns) {
      const k = String(c.index)
      const vals = data.rows.map((r) => toNum(r.values[k]?.value)).filter((v): v is number => v != null)
      best[k] = vals.length ? (c.should_minimize ? Math.min(...vals) : Math.max(...vals)) : null
      range[k] = vals.length ? [Math.min(...vals), Math.max(...vals)] : [0, 0]
      const sorted = [...new Set(vals)].sort((a, b) => (c.should_minimize ? a - b : b - a))
      for (const r of data.rows) {
        const v = toNum(r.values[k]?.value)
        if (v != null) (ranks[r.model_ref] ??= []).push(sorted.indexOf(v) + 1)
      }
    }
    const avgRank: Record<string, number | null> = {}
    for (const r of data.rows) {
      const rs = ranks[r.model_ref]
      avgRank[r.model_ref] = rs?.length ? rs.reduce((a, b) => a + b, 0) / rs.length : null
    }
    return { best, range, avgRank }
  }, [data])

  const rows = useMemo(() => {
    if (!data || !stats) return []
    const key = (r: Row): number | string | null => {
      if (sort.col === 'rank') return stats.avgRank[r.model_ref]
      if (sort.col === 'model') return r.model_name.toLowerCase()
      return toNum(r.values[sort.col]?.value)
    }
    return data.rows.slice().sort((a, b) => {
      const x = key(a)
      const y = key(b)
      if (x == null && y == null) return 0
      if (x == null) return 1
      if (y == null) return -1
      return (x < y ? -1 : x > y ? 1 : 0) * sort.dir
    })
  }, [data, stats, sort])

  const clickSort = (col: string, minimize?: boolean) =>
    setSort((s) => (s.col === col ? { col, dir: (-s.dir as 1 | -1) } : { col, dir: col === 'rank' || col === 'model' || minimize ? 1 : -1 }))
  const arrow = (col: string) => (sort.col === col ? (sort.dir === 1 ? ' ▲' : ' ▼') : '')

  return (
    <div className="page">
      <div className="breadcrumbs">
        <Link to={`/${projectId}/leaderboards`}>Leaderboards</Link>
        <span>/</span>
        <span>{objectId}</span>
      </div>
      <div className="page-header">
        <div className="col" style={{ gap: 2, minWidth: 0 }}>
          <h1 className="truncate">{objectId}</h1>
          {data?.leaderboard.val?.description && <span className="muted small">{data.leaderboard.val.description}</span>}
        </div>
        <span className="spacer" />
        {data && (
          <>
            <Link className="btn ghost" to={objectVersionsUrl(projectId, objectId)}>
              v{data.leaderboard.version_index} · history
            </Link>
            <button
              className="btn"
              disabled={!data.rows.length}
              onClick={() => {
                const ids = [...new Set(data.rows.flatMap((r) => Object.values(r.values).map((v) => v.evaluation_call_id)))].filter(Boolean)
                nav(compareEvaluationsUrl(projectId, ids))
              }}
            >
              Compare runs
            </button>
            <button className="btn primary" onClick={() => setEditing(true)}>
              Edit
            </button>
          </>
        )}
      </div>
      {q.error instanceof ApiError && q.error.status === 404 ? (
        <div className="card">
          <Empty>Leaderboard “{objectId}” was not found.</Empty>
        </div>
      ) : (
        <ErrorBox error={q.error} />
      )}
      {q.isLoading ? (
        <Loading />
      ) : data && stats ? (
        data.columns.length === 0 ? (
          <div className="card">
            <Empty>
              This leaderboard has no columns.{' '}
              <button className="btn sm" onClick={() => setEditing(true)}>
                Add columns
              </button>
            </Empty>
          </div>
        ) : data.rows.length === 0 ? (
          <div className="card">
            <Empty>
              <div className="col" style={{ alignItems: 'center', gap: 10 }}>
                <div>No finished evaluation runs match these columns yet.</div>
                <Link className="btn primary" to={`${newEvaluationUrl(projectId)}?evaluation=${encodeURIComponent(data.columns[0].evaluation_object_ref)}`}>
                  ▷ Run an evaluation
                </Link>
              </div>
            </Empty>
          </div>
        ) : (
          <div className="table-wrap">
            <table className="table">
              <thead>
                <tr>
                  <th className="sortable num" style={{ width: 60 }} onClick={() => clickSort('rank')} title="Average rank across columns">
                    Rank{arrow('rank')}
                  </th>
                  <th className="sortable" onClick={() => clickSort('model')}>
                    Model{arrow('model')}
                  </th>
                  {data.columns.map((c) => {
                    const evalUrl = refUrl(c.evaluation_object_ref)
                    return (
                      <th key={c.index} className="sortable num" onClick={() => clickSort(String(c.index), c.should_minimize)} title={c.evaluation_object_ref}>
                        <div className="faint small" style={{ fontWeight: 400 }}>
                          {evalUrl ? (
                            <Link to={evalUrl} onClick={(e) => e.stopPropagation()}>
                              {c.evaluation_name || parseRef(c.evaluation_object_ref)?.name}
                            </Link>
                          ) : (
                            c.evaluation_name
                          )}
                        </div>
                        <span className="mono">
                          {c.scorer_name}.{c.summary_metric_path}
                        </span>{' '}
                        <span className="faint" title={c.should_minimize ? 'Lower is better' : 'Higher is better'}>
                          {c.should_minimize ? '↓' : '↑'}
                        </span>
                        {arrow(String(c.index))}
                      </th>
                    )
                  })}
                </tr>
              </thead>
              <tbody>
                {rows.map((r) => {
                  const mp = parseRef(r.model_ref)
                  const ar = stats.avgRank[r.model_ref]
                  return (
                    <tr key={r.model_ref}>
                      <td className="num mono">{ar == null ? '—' : Number.isInteger(ar) ? ar : ar.toFixed(1)}</td>
                      <td>
                        {mp ? (
                          <Link to={objectUrl(mp.projectId, mp.name, mp.digest ?? 'latest')} style={{ fontWeight: 500 }} title={r.model_ref}>
                            {r.model_name}
                          </Link>
                        ) : (
                          r.model_name
                        )}
                        {mp?.digest && <span className="faint small mono"> :{mp.digest.slice(0, 6)}</span>}
                      </td>
                      {data.columns.map((c) => {
                        const k = String(c.index)
                        const cell = r.values[k]
                        const v = toNum(cell?.value)
                        const isBest = v != null && v === stats.best[k] && data.rows.length > 1
                        const [lo, hi] = stats.range[k]
                        const frac = v == null ? 0 : hi === lo ? 1 : c.should_minimize ? (hi - v) / (hi - lo) : (v - lo) / (hi - lo)
                        return (
                          <td key={k} className={`num ${isBest ? 'best' : ''}`}>
                            {cell ? (
                              <Link to={callUrl(projectId, cell.evaluation_call_id)} title={`Run ${fmtDate(cell.started_at)}`} style={{ color: 'inherit' }}>
                                <div className="mono">{fmtMetric(cell.value)}</div>
                                <div style={{ height: 3, background: 'var(--panel-2)', borderRadius: 2, marginTop: 3 }}>
                                  <div style={{ width: `${Math.max(4, frac * 100)}%`, height: '100%', background: isBest ? 'var(--success)' : 'var(--accent)', borderRadius: 2, opacity: 0.7 }} />
                                </div>
                                <div className="faint" style={{ fontSize: 10.5 }}>
                                  {fmtRelative(cell.started_at)}
                                </div>
                              </Link>
                            ) : (
                              <span className="faint">—</span>
                            )}
                          </td>
                        )
                      })}
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        )
      ) : null}
      {data && <div className="hint">Each cell is the model’s most recent finished run of that evaluation. Best value per column is highlighted.</div>}
      {editing && data && (
        <LeaderboardEditor
          projectId={projectId}
          initial={data.leaderboard}
          onClose={() => setEditing(false)}
          onSaved={() => {
            setEditing(false)
            void qc.invalidateQueries({ queryKey: ['leaderboard', projectId, objectId] })
          }}
        />
      )}
    </div>
  )
}
