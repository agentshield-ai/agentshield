import { useState } from 'react'
import { Link } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { dashboardsApi, type DashboardPanel, type DashboardVal } from '../../api/dashboards'
import type { SpansFilter, Query } from '../../api/types'
import { slugify } from '../traces/callUtils'
import { Modal } from '../common'
import { PanelEditor } from './PanelEditor'
import { blankPanel, newDashboard } from './dashboardUtils'

export const dashboardUrl = (projectId: string, objectId: string) => `/${projectId}/dashboards/${encodeURIComponent(objectId)}`
export const dashboardsUrl = (projectId: string) => `/${projectId}/dashboards`

/** "Add chart to dashboard" from the traces page: the current filters become the panel's filters. */
export function AddToDashboardModal({
  projectId,
  filter,
  query,
  search,
  onClose,
}: {
  projectId: string
  filter?: SpansFilter
  query?: Query
  search?: string
  onClose: () => void
}) {
  const qc = useQueryClient()
  const list = useQuery({ queryKey: ['objects', projectId, 'Dashboard'], queryFn: () => dashboardsApi.list(projectId) })
  const [target, setTarget] = useState<string>('')
  const [newName, setNewName] = useState('')
  const [done, setDone] = useState<{ id: string; label: string } | null>(null)
  const dashboards = list.data ?? []
  const chosen = target || (dashboards[0]?.name ?? '__new')
  const initial: DashboardPanel = { ...blankPanel(), filter, query, search, chart: 'line', metrics: [{ agg: 'count' }] }

  const save = useMutation({
    mutationFn: async (panel: DashboardPanel) => {
      let id: string
      let val: DashboardVal
      if (chosen === '__new') {
        const label = newName.trim()
        id = slugify(label)
        if (!id) throw new Error('Name the new dashboard')
        val = { ...newDashboard(label, 'blank'), filter: undefined }
      } else {
        id = chosen
        const cur = await dashboardsApi.read(projectId, id)
        val = cur.value
      }
      val = { ...val, panels: [...(val.panels ?? []), panel] }
      await dashboardsApi.save(projectId, id, val)
      return { id, label: val.name }
    },
    onSuccess: (r) => {
      void qc.invalidateQueries({ queryKey: ['objects', projectId, 'Dashboard'] })
      void qc.invalidateQueries({ queryKey: ['dashboard', projectId, r.id] })
      setDone(r)
    },
  })

  if (done)
    return (
      <Modal
        title="Chart added"
        onClose={onClose}
        footer={
          <>
            <button className="btn" onClick={onClose}>
              Close
            </button>
            <Link className="btn primary" to={dashboardUrl(projectId, done.id)} onClick={onClose}>
              Open dashboard
            </Link>
          </>
        }
      >
        The chart was added to <b>{done.label}</b>.
      </Modal>
    )

  return (
    <PanelEditor
      projectId={projectId}
      initial={initial}
      title="Add chart to dashboard"
      saveLabel="Add chart"
      saving={save.isPending}
      error={save.error}
      onClose={onClose}
      onSave={(p) => save.mutate(p)}
      header={
        <div className="row wrap" style={{ marginBottom: 12, alignItems: 'flex-end' }}>
          <label className="field grow">
            Dashboard
            <select className="input" value={chosen} onChange={(e) => setTarget(e.target.value)}>
              {dashboards.map((d) => (
                <option key={d.name} value={d.name}>
                  {d.value?.name ?? d.name}
                </option>
              ))}
              <option value="__new">+ New dashboard…</option>
            </select>
          </label>
          {chosen === '__new' && (
            <label className="field grow">
              New dashboard name
              <input className="input" value={newName} autoFocus onChange={(e) => setNewName(e.target.value)} placeholder="e.g. Production health" />
            </label>
          )}
          <span className="small faint grow" style={{ flexBasis: '100%' }}>
            The chart keeps the filters from the traces table{search ? ` (search “${search}”)` : ''}.
          </span>
        </div>
      }
    />
  )
}
