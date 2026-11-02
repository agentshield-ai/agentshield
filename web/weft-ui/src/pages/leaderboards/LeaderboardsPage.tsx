import { useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { useQueryClient } from '@tanstack/react-query'
import { api } from '../../api/client'
import type { Any } from '../../api/types'
import { Empty, ErrorBox, Loading } from '../../components/common'
import { LeaderboardEditor } from '../../components/evals/LeaderboardEditor'
import { ConfirmButton } from '../../components/objects/bits'
import { refName, useObjects } from '../../components/objects/objectUtils'
import { useProjectId } from '../../hooks/useProject'
import { fmtDate, fmtRelative } from '../../lib/format'
import { leaderboardUrl, objectVersionsUrl } from '../../lib/urls'

export function LeaderboardsPage() {
  const projectId = useProjectId()
  const nav = useNavigate()
  const qc = useQueryClient()
  const lbs = useObjects(projectId, { base_object_classes: ['Leaderboard'] })
  const [creating, setCreating] = useState(false)
  const [search, setSearch] = useState('')
  const rows = (lbs.data ?? []).filter((o) => !search || o.object_id.toLowerCase().includes(search.toLowerCase()))
  return (
    <div className="page">
      <div className="page-header">
        <div className="col" style={{ gap: 2 }}>
          <h1>Leaderboards</h1>
          <span className="muted small">Rank models across evaluation metrics.</span>
        </div>
        <span className="spacer" />
        {(lbs.data?.length ?? 0) > 0 && <input className="input" style={{ width: 200, maxWidth: '100%' }} placeholder="Search…" value={search} onChange={(e) => setSearch(e.target.value)} />}
        <button className="btn primary" onClick={() => setCreating(true)}>
          + New leaderboard
        </button>
      </div>
      <ErrorBox error={lbs.error} />
      {lbs.isLoading ? (
        <Loading />
      ) : !lbs.data?.length ? (
        <div className="card">
          <Empty>
            <div className="col" style={{ alignItems: 'center', gap: 10 }}>
              <div>No leaderboards yet. A leaderboard picks metrics from one or more evaluations and ranks every model that has been evaluated on them.</div>
              <button className="btn primary" onClick={() => setCreating(true)}>
                + New leaderboard
              </button>
            </div>
          </Empty>
        </div>
      ) : (
        <div className="table-wrap">
          <table className="table">
            <thead>
              <tr>
                <th>Name</th>
                <th>Columns</th>
                <th>Evaluations</th>
                <th>Updated</th>
                <th className="num">Versions</th>
                <th style={{ width: 40 }} />
              </tr>
            </thead>
            <tbody>
              {rows.map((o) => {
                const cols: Any[] = Array.isArray(o.val?.columns) ? o.val.columns : []
                const evals = [...new Set(cols.map((c) => refName(c.evaluation_object_ref)))]
                return (
                  <tr key={o.object_id} className="clickable" onClick={() => nav(leaderboardUrl(projectId, o.object_id))}>
                    <td>
                      <Link to={leaderboardUrl(projectId, o.object_id)} style={{ fontWeight: 500 }} onClick={(e) => e.stopPropagation()}>
                        {o.object_id}
                      </Link>
                      {o.val?.description && <div className="faint small cell-preview">{o.val.description}</div>}
                    </td>
                    <td>
                      <span className="chips">
                        {cols.slice(0, 4).map((c, i) => (
                          <span key={i} className="chip mono">
                            {c.scorer_name}.{c.summary_metric_path}
                          </span>
                        ))}
                        {cols.length > 4 && <span className="faint small">+{cols.length - 4}</span>}
                      </span>
                    </td>
                    <td className="muted small">{evals.join(', ') || '—'}</td>
                    <td className="muted" title={fmtDate(o.created_at)}>
                      {fmtRelative(o.created_at)}
                    </td>
                    <td className="num">
                      <Link to={objectVersionsUrl(projectId, o.object_id)} onClick={(e) => e.stopPropagation()}>
                        {o.version_count ?? o.version_index + 1}
                      </Link>
                    </td>
                    <td onClick={(e) => e.stopPropagation()}>
                      <ConfirmButton
                        className="btn ghost sm icon danger"
                        title={`Delete leaderboard ${o.object_id}?`}
                        body="Evaluation runs are not affected."
                        onConfirm={async () => {
                          await api.objDelete(projectId, o.object_id)
                          await qc.invalidateQueries({ queryKey: ['objs', projectId] })
                        }}
                      >
                        ✕
                      </ConfirmButton>
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      )}
      {creating && (
        <LeaderboardEditor
          projectId={projectId}
          onClose={() => setCreating(false)}
          onSaved={(c) => {
            setCreating(false)
            nav(leaderboardUrl(projectId, c.objectId))
          }}
        />
      )}
    </div>
  )
}
