import { useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { useQueryClient } from '@tanstack/react-query'
import { api } from '../../api/client'
import { Empty, ErrorBox, Loading } from '../../components/common'
import { MonitorEditor, useToggleMonitor } from '../../components/evals/MonitorEditor'
import { ConfirmButton, Switch } from '../../components/objects/bits'
import { refName, useObjects } from '../../components/objects/objectUtils'
import { useProjectId } from '../../hooks/useProject'
import { fmtDate, fmtRelative } from '../../lib/format'
import { monitorUrl } from '../../lib/urls'

export function MonitorsPage() {
  const projectId = useProjectId()
  const nav = useNavigate()
  const qc = useQueryClient()
  const mons = useObjects(projectId, { base_object_classes: ['Monitor'] })
  const [creating, setCreating] = useState(false)
  const { toggle, pending } = useToggleMonitor(projectId)
  return (
    <div className="page">
      <div className="page-header">
        <div className="col" style={{ gap: 2 }}>
          <h1>Monitors</h1>
          <span className="muted small">Score production calls automatically as they arrive. Results are stored as feedback on each call.</span>
        </div>
        <span className="spacer" />
        <button className="btn primary" onClick={() => setCreating(true)}>
          + New monitor
        </button>
      </div>
      <ErrorBox error={mons.error} />
      {mons.isLoading ? (
        <Loading />
      ) : !mons.data?.length ? (
        <div className="card">
          <Empty>
            <div className="col" style={{ alignItems: 'center', gap: 10 }}>
              <div style={{ maxWidth: 480 }}>No monitors yet. A monitor samples calls of the ops you choose and scores them with LLM judges or rule-based scorers.</div>
              <button className="btn primary" onClick={() => setCreating(true)}>
                + New monitor
              </button>
            </div>
          </Empty>
        </div>
      ) : (
        <div className="table-wrap">
          <table className="table">
            <thead>
              <tr>
                <th style={{ width: 56 }}>Active</th>
                <th>Name</th>
                <th>Ops</th>
                <th className="num">Sampling</th>
                <th>Scorers</th>
                <th>Updated</th>
                <th style={{ width: 40 }} />
              </tr>
            </thead>
            <tbody>
              {mons.data.map((o) => {
                const v = o.val ?? {}
                return (
                  <tr key={o.object_id} className="clickable" onClick={() => nav(monitorUrl(projectId, o.object_id))}>
                    <td>
                      <Switch on={!!v.active} disabled={pending === o.object_id} onChange={() => void toggle(o)} title={v.active ? 'Pause monitor' : 'Activate monitor'} />
                    </td>
                    <td>
                      <Link to={monitorUrl(projectId, o.object_id)} style={{ fontWeight: 500 }} onClick={(e) => e.stopPropagation()}>
                        {o.object_id}
                      </Link>
                      {v.description && <div className="faint small cell-preview">{v.description}</div>}
                    </td>
                    <td>
                      <span className="chips">
                        {(v.op_names ?? []).length === 0 ? (
                          <span className="faint small">all root calls</span>
                        ) : (
                          (v.op_names as string[]).slice(0, 3).map((n) => (
                            <span key={n} className="chip mono">
                              {n}
                            </span>
                          ))
                        )}
                        {(v.op_names ?? []).length > 3 && <span className="faint small">+{v.op_names.length - 3}</span>}
                      </span>
                    </td>
                    <td className="num">{Math.round((v.sampling_rate ?? 1) * 100)}%</td>
                    <td className="muted small">{(v.scorers ?? []).map((s: string) => refName(s)).join(', ') || '—'}</td>
                    <td className="muted" title={fmtDate(o.created_at)}>
                      {fmtRelative(o.created_at)}
                    </td>
                    <td onClick={(e) => e.stopPropagation()}>
                      <ConfirmButton
                        className="btn ghost sm icon danger"
                        title={`Delete monitor ${o.object_id}?`}
                        body="Scores it already produced are kept as feedback."
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
        <MonitorEditor
          projectId={projectId}
          onClose={() => setCreating(false)}
          onSaved={(c) => {
            setCreating(false)
            nav(monitorUrl(projectId, c.objectId))
          }}
        />
      )}
    </div>
  )
}
