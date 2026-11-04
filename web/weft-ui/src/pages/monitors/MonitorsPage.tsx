import { useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { useQueryClient } from '@tanstack/react-query'
import { api } from '../../api/client'
import { Empty, ErrorBox, Loading } from '../../components/common'
import { MonitorEditor, useToggleMonitor } from '../../components/evals/MonitorEditor'
import { monitorNames } from '../../components/evals/monitorFields'
import { SignalsGallery } from '../../components/monitors/SignalsGallery'
import { ConfirmButton, Switch } from '../../components/objects/bits'
import { refName, useObjects } from '../../components/objects/objectUtils'
import { useProjectId } from '../../hooks/useProject'
import { fmtDate, fmtRelative } from '../../lib/format'
import { monitorUrl } from '../../lib/urls'

export function MonitorsPage() {
  const projectId = useProjectId()
  const nav = useNavigate()
  const qc = useQueryClient()
  const mons = useObjects(projectId, { kinds: ['monitor'] })
  const [creating, setCreating] = useState(false)
  const { toggle, pending } = useToggleMonitor(projectId)
  return (
    <div className="page">
      <div className="page-header">
        <div className="col" style={{ gap: 2 }}>
          <h1>Monitors</h1>
          <span className="muted small">Score production spans automatically as they arrive. Results are stored as feedback on each span.</span>
        </div>
        <span className="spacer" />
        <button className="btn primary" onClick={() => setCreating(true)}>
          + New monitor
        </button>
      </div>
      <ErrorBox error={mons.error} />
      <SignalsGallery projectId={projectId} onCreated={(id) => nav(monitorUrl(projectId, id))} />
      {mons.isLoading ? (
        <Loading />
      ) : !mons.data?.length ? (
        <div className="card">
          <Empty>
            <div className="col" style={{ alignItems: 'center', gap: 10 }}>
              <div style={{ maxWidth: 480 }}>No monitors yet. A monitor samples spans of the functions you choose and scores them with LLM judges or rule-based scorers.</div>
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
                <th>Span names</th>
                <th className="num">Sampling</th>
                <th>Scorers</th>
                <th>Updated</th>
                <th style={{ width: 40 }} />
              </tr>
            </thead>
            <tbody>
              {mons.data.map((o) => {
                const v = o.value ?? {}
                return (
                  <tr key={o.name} className="clickable" onClick={() => nav(monitorUrl(projectId, o.name))}>
                    <td>
                      <Switch on={!!v.active} disabled={pending === o.name} onChange={() => void toggle(o)} title={v.active ? 'Pause monitor' : 'Activate monitor'} />
                    </td>
                    <td>
                      <Link to={monitorUrl(projectId, o.name)} style={{ fontWeight: 500 }} onClick={(e) => e.stopPropagation()}>
                        {o.name}
                      </Link>
                      {v.description && <div className="faint small cell-preview">{v.description}</div>}
                    </td>
                    <td>
                      <span className="chips">
                        {monitorNames(v).length === 0 ? (
                          <span className="faint small">all root spans</span>
                        ) : (
                          monitorNames(v).slice(0, 3).map((n) => (
                            <span key={n} className="chip mono">
                              {n}
                            </span>
                          ))
                        )}
                        {monitorNames(v).length > 3 && <span className="faint small">+{monitorNames(v).length - 3}</span>}
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
                        title={`Delete monitor ${o.name}?`}
                        body="Scores it already produced are kept as feedback."
                        onConfirm={async () => {
                          await api.objectDelete(projectId, o.name)
                          await qc.invalidateQueries({ queryKey: ['objects', projectId] })
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
            nav(monitorUrl(projectId, c.name))
          }}
        />
      )}
    </div>
  )
}
