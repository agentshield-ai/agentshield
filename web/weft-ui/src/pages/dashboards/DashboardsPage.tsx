import { useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { dashboardsApi, type DashboardVal } from '../../api/dashboards'
import { Empty, ErrorBox, Loading, Modal } from '../../components/common'
import { dashboardUrl } from '../../components/dashboards/AddToDashboardModal'
import { newDashboard, panelTitle, timeRangeLabel } from '../../components/dashboards/dashboardUtils'
import { slugify } from '../../components/traces/callUtils'
import { useProjectId } from '../../hooks/useProject'
import { fmtRelative } from '../../lib/format'
import '../../components/dashboards/dashboards.css'

function NewDashboardModal({ projectId, onClose }: { projectId: string; onClose: () => void }) {
  const nav = useNavigate()
  const qc = useQueryClient()
  const [name, setName] = useState('')
  const [template, setTemplate] = useState<'overview' | 'blank'>('overview')
  const create = useMutation({
    mutationFn: async () => {
      const id = slugify(name)
      if (!id) throw new Error('Enter a name')
      const existing = await dashboardsApi.list(projectId)
      if (existing.some((o) => o.object_id === id)) throw new Error(`A dashboard called “${id}” already exists`)
      await dashboardsApi.save(projectId, id, newDashboard(name.trim(), template))
      return id
    },
    onSuccess: (id) => {
      void qc.invalidateQueries({ queryKey: ['objs', projectId, 'Dashboard'] })
      nav(dashboardUrl(projectId, id))
    },
  })
  return (
    <Modal
      title="New dashboard"
      onClose={onClose}
      footer={
        <>
          <button className="btn" onClick={onClose}>
            Cancel
          </button>
          <button className="btn primary" disabled={!name.trim() || create.isPending} onClick={() => create.mutate()}>
            Create
          </button>
        </>
      }
    >
      <form
        className="col"
        style={{ gap: 12 }}
        onSubmit={(e) => {
          e.preventDefault()
          if (name.trim()) create.mutate()
        }}
      >
        <label className="field">
          Name
          <input className="input" autoFocus value={name} onChange={(e) => setName(e.target.value)} placeholder="e.g. Production health" />
        </label>
        <div className="field dash-field">
          Start from
          <div className="dash-template" role="radiogroup">
            {(
              [
                ['overview', 'Overview', 'Calls, errors, latency, tokens and cost by model, and an ops table.'],
                ['blank', 'Blank', 'An empty dashboard to add your own charts to.'],
              ] as const
            ).map(([id, label, desc]) => (
              <button type="button" key={id} role="radio" aria-checked={template === id} className={template === id ? 'on' : ''} onClick={() => setTemplate(id)}>
                <b>{label}</b>
                <div className="small muted">{desc}</div>
              </button>
            ))}
          </div>
        </div>
      </form>
      <ErrorBox error={create.error} />
    </Modal>
  )
}

export function DashboardsPage() {
  const projectId = useProjectId()
  const [creating, setCreating] = useState(false)
  const [q, setQ] = useState('')
  const list = useQuery({ queryKey: ['objs', projectId, 'Dashboard'], queryFn: () => dashboardsApi.list(projectId) })
  const all = list.data ?? []
  const shown = all.filter((o) => {
    const s = q.trim().toLowerCase()
    const v = o.val as DashboardVal
    return !s || o.object_id.toLowerCase().includes(s) || (v?.name ?? '').toLowerCase().includes(s) || (v?.description ?? '').toLowerCase().includes(s)
  })
  return (
    <div className="page">
      <div className="page-header">
        <div className="col" style={{ gap: 2 }}>
          <h1>Dashboards</h1>
          <span className="muted small">Custom charts of any call metric: latency percentiles, tokens, cost, scores, grouped by op, model or attribute.</span>
        </div>
        <span className="spacer" />
        <button className="btn primary" onClick={() => setCreating(true)}>
          + New dashboard
        </button>
      </div>
      {all.length > 4 && (
        <div className="toolbar">
          <input className="input" style={{ width: 240, maxWidth: '100%' }} placeholder="Search dashboards…" value={q} onChange={(e) => setQ(e.target.value)} aria-label="Search dashboards" />
        </div>
      )}
      <ErrorBox error={list.error} />
      {list.isLoading ? (
        <Loading />
      ) : all.length === 0 ? (
        <div className="card">
          <Empty>
            <div className="col" style={{ alignItems: 'center', gap: 10 }}>
              <div style={{ maxWidth: 480 }}>
                No dashboards yet. Create one from a template, or use <b>Add chart to dashboard</b> above the charts on the Traces page.
              </div>
              <button className="btn primary" onClick={() => setCreating(true)}>
                + New dashboard
              </button>
            </div>
          </Empty>
        </div>
      ) : (
        <div className="dash-card-list">
          {shown.map((o) => {
            const v = o.val as DashboardVal
            const panels = v?.panels ?? []
            return (
              <Link key={o.object_id} to={dashboardUrl(projectId, o.object_id)} className="dash-card">
                <b className="truncate">{v?.name || o.object_id}</b>
                {v?.description && <span className="small muted truncate">{v.description}</span>}
                <span className="small faint truncate">{panels.length ? panels.slice(0, 4).map(panelTitle).join(' · ') : 'No charts yet'}</span>
                <span className="row small muted" style={{ gap: 10 }}>
                  <span>
                    {panels.length} chart{panels.length === 1 ? '' : 's'}
                  </span>
                  <span>{timeRangeLabel(v?.time_range ?? 'all')}</span>
                  <span className="grow" />
                  <span title={o.created_at}>
                    v{o.version_index} · {fmtRelative(o.created_at)}
                  </span>
                </span>
              </Link>
            )
          })}
        </div>
      )}
      {creating && <NewDashboardModal projectId={projectId} onClose={() => setCreating(false)} />}
    </div>
  )
}
