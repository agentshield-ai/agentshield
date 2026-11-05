// One leaderboard (DESIGN.md §7, §7.2 rule 3): a sentence saying who leads
// on how many metrics, then a plain ranked table: the rank (average rank
// across the metrics), the model, and each metric's value from the model's
// latest finished run, the best in bold. A value opens that run.
import { dynamicRunIds } from '../../api/leaderboards'
import { useMemo, useState, type ReactNode } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { api, ApiError } from '../../api/client'
import { parseRef } from '../../api/refs'
import type { LeaderboardComputeRes } from '../../api/types'
import { ErrorBox, Loading } from '../../components/common'
import { LeaderboardEditor } from '../../components/evals/LeaderboardEditor'
import { toNum } from '../../components/evals/evalUtils'
import { encodeLb } from '../../components/evals/leaderboardUtils'
import { lbShapes } from '../../components/evals/sentence'
import { PageHeader } from '../../components/ui/PageHeader'
import { PlainTable } from '../../components/ui/PlainTable'
import { Sentence } from '../../components/ui/Sentence'
import { useProjectId } from '../../hooks/useProject'
import { fmtDate } from '../../lib/format'
import { relativeTime } from '../../lib/summaries'
import { leaderboardSentence, metricValue, metricWords } from '../../lib/summaries/evaluate'
import { compareEvaluationsUrl, newEvaluationUrl, objectUrl, objectVersionsUrl, refUrl } from '../../lib/urls'
import '../evaluations/evaluate.css'

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
    const ranks: Record<string, number[]> = {}
    for (const c of data.columns) {
      const k = String(c.index)
      const vals = data.rows.map((r) => toNum(r.values[k]?.value)).filter((v): v is number => v != null)
      best[k] = vals.length ? (c.should_minimize ? Math.min(...vals) : Math.max(...vals)) : null
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
    return { best, avgRank }
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
    setSort((s) => (s.col === col ? { col, dir: -s.dir as 1 | -1 } : { col, dir: col === 'rank' || col === 'model' || minimize ? 1 : -1 }))
  const ariaSort = (col: string): 'ascending' | 'descending' | undefined => (sort.col === col ? (sort.dir === 1 ? 'ascending' : 'descending') : undefined)
  const back = { to: `/${projectId}/leaderboards`, label: 'Leaderboards' }

  if (q.isLoading) return <Loading />
  if (!data)
    return (
      <div className="page-frame detail">
        <PageHeader variant="detail" back={back} title={objectId} help="evaluations.md#leaderboards" helpTopic="Leaderboards" />
        {q.error instanceof ApiError && q.error.status === 404 ? (
          <div className="quiet-empty">
            <p>Leaderboard “{objectId}” was not found.</p>
          </div>
        ) : (
          <ErrorBox error={q.error} />
        )}
      </div>
    )
  const sh = lbShapes(data)
  const many = new Set(data.columns.map((c) => c.evaluation_name)).size > 1
  const runIds = [...new Set(data.rows.flatMap((r) => Object.values(r.values).map((v) => v.evaluation_run_id)))].filter(Boolean)
  const dyn = dynamicRunIds(data.leaderboard.value)
  const SortHead = ({ col, label, title, minimize, num }: { col: string; label: ReactNode; title?: string; minimize?: boolean; num?: boolean }) => (
    <th scope="col" className={num ? 'num' : undefined} aria-sort={ariaSort(col)} title={title}>
      <button type="button" className="ev-dir" style={{ color: 'inherit' }} onClick={() => clickSort(col, minimize)}>
        {label}
        {sort.col === col ? (sort.dir === 1 ? ' ↑' : ' ↓') : ''}
      </button>
    </th>
  )

  return (
    <div className="page-frame detail">
      <PageHeader
        variant="detail"
        back={back}
        title={objectId}
        help="evaluations.md#leaderboards"
        helpTopic="Leaderboards"
        sentence={<Sentence size="detail" parts={[leaderboardSentence(sh.columns, sh.rows)]} />}
        meta={[data.leaderboard.value?.description, `each value is the model’s latest finished run`].filter(Boolean).join(' · ')}
        controls={
          <>
            <button type="button" className="qbtn" onClick={() => setEditing(true)}>
              Edit
            </button>
            <button type="button" className="qbtn" disabled={!runIds.length} onClick={() => nav(compareEvaluationsUrl(projectId, runIds))} title="Compare the runs behind these values">
              Compare these runs
            </button>
            {dyn.length > 0 && (
              <Link
                className="qbtn"
                title="Open the evaluation comparison this leaderboard was saved from"
                to={`${compareEvaluationsUrl(projectId, dyn)}${Array.isArray(data.leaderboard.value.weft_dynamic.metrics) ? `&lb=${encodeURIComponent(encodeLb(data.leaderboard.value.weft_dynamic.metrics))}` : ''}`}
              >
                Source comparison
              </Link>
            )}
            <Link className="linkish" to={objectVersionsUrl(projectId, objectId)}>
              Version history
            </Link>
          </>
        }
      />
      {data.columns.length === 0 ? (
        <div className="quiet-empty">
          <p>This leaderboard has no metrics.</p>
          <button type="button" className="qbtn" onClick={() => setEditing(true)}>
            Add metrics
          </button>
        </div>
      ) : data.rows.length === 0 ? (
        <div className="quiet-empty">
          <p>No finished evaluation runs match these metrics yet.</p>
          <Link className="qbtn" to={`${newEvaluationUrl(projectId)}?evaluation=${encodeURIComponent(data.columns[0].evaluation_object_ref)}`}>
            Run an evaluation
          </Link>
        </div>
      ) : stats ? (
        <PlainTable caption={`Models ranked on ${objectId}`}>
          <thead>
            <tr>
              <SortHead col="rank" label="Rank" title="Average rank across the metrics" num />
              <SortHead col="model" label="Model" />
              {data.columns.map((c) => (
                <SortHead
                  key={c.index}
                  col={String(c.index)}
                  minimize={c.should_minimize}
                  num
                  title={`${c.scorer_name}.${c.summary_metric_path} of ${c.evaluation_name || parseRef(c.evaluation_object_ref)?.name}; ${c.should_minimize ? 'lower' : 'higher'} is better`}
                  label={
                    <>
                      {metricWords(`${c.scorer_name}.${c.summary_metric_path}`)}
                      {many && <span className="ev-delta">{c.evaluation_name}</span>}
                    </>
                  }
                />
              ))}
            </tr>
          </thead>
          <tbody>
            {rows.map((r) => {
              const mp = parseRef(r.model_ref)
              const ar = stats.avgRank[r.model_ref]
              return (
                <tr key={r.model_ref}>
                  <td className={`num ${ar === 1 ? 'ev-best' : ''}`}>{ar == null ? '—' : Number.isInteger(ar) ? ar : ar.toFixed(1)}</td>
                  <td>
                    {mp ? (
                      <Link to={objectUrl(mp.projectId, mp.name, mp.digest ?? 'latest')} title={r.model_ref}>
                        {r.model_name}
                      </Link>
                    ) : (
                      r.model_name
                    )}
                  </td>
                  {data.columns.map((c) => {
                    const k = String(c.index)
                    const cell = r.values[k]
                    const v = toNum(cell?.value)
                    const isBest = v != null && v === stats.best[k] && data.rows.length > 1
                    const key = `${c.scorer_name}.${c.summary_metric_path}`
                    return (
                      <td key={k} className={`num ${isBest ? 'ev-best' : ''}`}>
                        {cell ? (
                          <Link to={compareEvaluationsUrl(projectId, [cell.evaluation_run_id])} title={`Run of ${fmtDate(cell.started_at)}${isBest ? ' (best)' : ''}`}>
                            {typeof cell.value === 'number' || typeof cell.value === 'boolean' ? metricValue(key, cell.value) : '—'}
                          </Link>
                        ) : (
                          '—'
                        )}
                        {cell && <span className="ev-delta">{relativeTime(cell.started_at)}</span>}
                      </td>
                    )
                  })}
                </tr>
              )
            })}
          </tbody>
        </PlainTable>
      ) : null}
      {(() => {
        const ev = data.columns[0]?.evaluation_object_ref
        return ev && refUrl(ev) ? (
          <p className="ev-note">
            Add a model by running it on <Link to={`${newEvaluationUrl(projectId)}?evaluation=${encodeURIComponent(ev)}`}>{data.columns[0].evaluation_name || parseRef(ev)?.name}</Link>.
          </p>
        ) : null
      })()}
      {editing && (
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
