import { useMemo, useState } from 'react'
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '../../api/client'
import { dashboardsApi, type DashboardPanel, type DashboardVal } from '../../api/dashboards'
import { Empty, ErrorBox, Loading, Modal, Spinner } from '../../components/common'
import { dashboardsUrl } from '../../components/dashboards/AddToDashboardModal'
import { PanelChart } from '../../components/dashboards/MetricChart'
import { PanelEditor } from '../../components/dashboards/PanelEditor'
import { TimeRangePicker } from '../../components/dashboards/TimeRangePicker'
import { blankPanel, movePanel, newPanelId, panelTitle } from '../../components/dashboards/dashboardUtils'
import { ConfirmModal, Dropdown, MenuItem } from '../../components/traces/Menu'
import { useProjectId } from '../../hooks/useProject'
import { fmtRelative } from '../../lib/format'
import { tracesUrl } from '../../lib/urls'
import '../../components/dashboards/dashboards.css'

const REFRESH = [
  { s: 0, label: 'Off' },
  { s: 10, label: '10s' },
  { s: 30, label: '30s' },
  { s: 60, label: '1m' },
  { s: 300, label: '5m' },
]

export function DashboardPage() {
  const projectId = useProjectId()
  const { objectId = '' } = useParams()
  const nav = useNavigate()
  const qc = useQueryClient()
  const [params, setParams] = useSearchParams()
  const q = useQuery({ queryKey: ['dashboard', projectId, objectId], queryFn: () => dashboardsApi.read(projectId, objectId) })
  const ops = useQuery({ queryKey: ['spans', projectId, 'op_counts', false], queryFn: () => api.spanNames(projectId, false) })
  const opNames = useMemo(() => [...new Set((ops.data?.items ?? []).map((o) => o.base_name))].sort(), [ops.data])

  const [editing, setEditing] = useState<DashboardPanel | null>(null)
  const [modal, setModal] = useState<null | 'rename' | 'delete'>(null)
  const [deletePanel, setDeletePanel] = useState<DashboardPanel | null>(null)

  const save = useMutation({
    mutationFn: (val: DashboardVal) => dashboardsApi.save(projectId, objectId, val),
    onMutate: (val) => {
      // Optimistic: show the new layout immediately.
      qc.setQueryData(['dashboard', projectId, objectId], (old: Awaited<ReturnType<typeof dashboardsApi.read>> | undefined) => (old ? { ...old, val } : old))
    },
    onSettled: () => {
      void qc.invalidateQueries({ queryKey: ['dashboard', projectId, objectId] })
      void qc.invalidateQueries({ queryKey: ['objects', projectId, 'Dashboard'] })
    },
  })
  const del = useMutation({
    mutationFn: () => dashboardsApi.remove(projectId, objectId),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['objects', projectId, 'Dashboard'] })
      nav(dashboardsUrl(projectId))
    },
  })

  if (q.isLoading) return <Loading />
  if (q.error || !q.data)
    return (
      <div className="page">
        <ErrorBox error={q.error ?? 'Dashboard not found'} />
        <Link to={dashboardsUrl(projectId)}>Back to dashboards</Link>
      </div>
    )
  const val: DashboardVal = { ...q.data.value, panels: q.data.value.panels ?? [] }
  // URL overrides let people explore other ranges without saving a new version.
  const range = params.get('range') ?? val.time_range ?? '7d'
  const refresh = Number(params.get('refresh') ?? val.refresh_seconds ?? 0) || 0
  const dirtyView = range !== (val.time_range ?? '7d') || refresh !== (val.refresh_seconds ?? 0)
  const setParam = (k: string, v: string | null) =>
    setParams(
      (p) => {
        const n = new URLSearchParams(p)
        if (v == null) n.delete(k)
        else n.set(k, v)
        return n
      },
      { replace: true },
    )
  const dash = { filter: val.filter, query: val.query, time_range: range }
  const update = (patch: Partial<DashboardVal>) => save.mutate({ ...val, ...patch })
  const setPanels = (panels: DashboardPanel[]) => update({ panels })
  const op = val.filter?.names?.[0] ?? ''
  const roots = val.filter?.trace_roots_only ?? false

  return (
    <div className="page">
      <div className="breadcrumbs">
        <Link to={dashboardsUrl(projectId)}>Dashboards</Link>
        <span>/</span>
        <span>{val.name || objectId}</span>
      </div>
      <div className="page-header">
        <div className="col" style={{ gap: 2, minWidth: 0 }}>
          <h1 className="truncate">{val.name || objectId}</h1>
          {val.description && <span className="muted small">{val.description}</span>}
        </div>
        <span className="spacer" />
        {save.isPending && <Spinner label="Saving…" />}
        <span className="small faint" title={q.data.created_at}>
          v{q.data.version} · saved {fmtRelative(q.data.created_at)}
        </span>
        <button className="btn primary" onClick={() => setEditing(blankPanel())}>
          + Add chart
        </button>
        <Dropdown label="⋯" align="right" className="btn" title="Dashboard actions">
          {(close) => (
            <>
              <MenuItem
                onClick={() => {
                  setModal('rename')
                  close()
                }}
              >
                Rename / describe…
              </MenuItem>
              <MenuItem
                danger
                onClick={() => {
                  setModal('delete')
                  close()
                }}
              >
                Delete dashboard
              </MenuItem>
            </>
          )}
        </Dropdown>
      </div>

      <div className="toolbar dash-filters">
        <TimeRangePicker value={range} onChange={(v) => setParam('range', v === (val.time_range ?? '7d') ? null : v)} />
        <select className="input" aria-label="Span name filter" value={op} onChange={(e) => update({ filter: { ...val.filter, names: e.target.value ? [e.target.value] : undefined } })} style={{ maxWidth: 220 }}>
          <option value="">All names</option>
          {opNames.map((n) => (
            <option key={n}>{n}</option>
          ))}
          {op && !opNames.includes(op) && <option>{op}</option>}
        </select>
        <label className="checkbox small" title="Charts count only the root span of each trace unless a chart overrides it">
          <input type="checkbox" checked={roots} onChange={(e) => update({ filter: { ...val.filter, trace_roots_only: e.target.checked } })} /> Roots only
        </label>
        <span className="grow" />
        <label className="row small muted" style={{ gap: 6 }}>
          Auto-refresh
          <select className="input" value={refresh} onChange={(e) => setParam('refresh', Number(e.target.value) === (val.refresh_seconds ?? 0) ? null : e.target.value)}>
            {REFRESH.map((r) => (
              <option key={r.s} value={r.s}>
                {r.label}
              </option>
            ))}
          </select>
        </label>
        <button className="btn sm" title="Refresh now" aria-label="Refresh now" onClick={() => void qc.invalidateQueries({ queryKey: ['calls-metrics', projectId] })}>
          ↻
        </button>
        {dirtyView && (
          <button
            className="btn sm"
            title="Make this time range and refresh rate the dashboard's default"
            onClick={() => {
              update({ time_range: range, refresh_seconds: refresh })
              setParams(new URLSearchParams(), { replace: true })
            }}
          >
            Save as default
          </button>
        )}
      </div>
      <ErrorBox error={save.error} />

      {val.panels.length === 0 ? (
        <div className="card">
          <Empty>
            <div className="col" style={{ alignItems: 'center', gap: 10 }}>
              <div>This dashboard has no charts yet.</div>
              <button className="btn primary" onClick={() => setEditing(blankPanel())}>
                + Add chart
              </button>
            </div>
          </Empty>
        </div>
      ) : (
        <div className="dash-grid">
          {val.panels.map((p, i) => (
            <div key={p.id} className={`dash-panel w${p.width ?? 1}`}>
              <div className="dash-panel-head">
                <h3 title={panelTitle(p)}>{panelTitle(p)}</h3>
                {p.time_range && <span className="badge">{p.time_range.startsWith('custom:') ? 'custom range' : p.time_range}</span>}
                <Dropdown label="⋯" align="right" className="btn ghost sm icon" title="Chart actions">
                  {(close) => (
                    <>
                      <MenuItem
                        onClick={() => {
                          setEditing(p)
                          close()
                        }}
                      >
                        Edit…
                      </MenuItem>
                      <MenuItem
                        onClick={() => {
                          const copy = { ...p, id: newPanelId(), title: p.title ? `${p.title} (copy)` : '' }
                          setPanels([...val.panels.slice(0, i + 1), copy, ...val.panels.slice(i + 1)])
                          close()
                        }}
                      >
                        Duplicate
                      </MenuItem>
                      <MenuItem
                        disabled={i === 0}
                        onClick={() => {
                          setPanels(movePanel(val.panels, p.id, -1))
                          close()
                        }}
                      >
                        Move earlier
                      </MenuItem>
                      <MenuItem
                        disabled={i === val.panels.length - 1}
                        onClick={() => {
                          setPanels(movePanel(val.panels, p.id, 1))
                          close()
                        }}
                      >
                        Move later
                      </MenuItem>
                      <MenuItem
                        onClick={() => {
                          const w = ((p.width ?? 1) % 3) + 1
                          setPanels(val.panels.map((x) => (x.id === p.id ? { ...x, width: w as 1 | 2 | 3 } : x)))
                          close()
                        }}
                      >
                        Width: {['narrow', 'wide', 'full'][(p.width ?? 1) % 3]}
                      </MenuItem>
                      <MenuItem
                        onClick={() => {
                          const op = p.filter?.names?.[0] ?? val.filter?.names?.[0]
                          const params: Record<string, string> = {}
                          if (op) params.op = op
                          if ((p.filter?.trace_roots_only ?? val.filter?.trace_roots_only) === false) params.roots = '0'
                          if (p.search) params.q = p.search
                          nav(tracesUrl(projectId, params))
                          close()
                        }}
                      >
                        View calls
                      </MenuItem>
                      <div className="dd-sep" />
                      <MenuItem
                        danger
                        onClick={() => {
                          setDeletePanel(p)
                          close()
                        }}
                      >
                        Remove chart
                      </MenuItem>
                    </>
                  )}
                </Dropdown>
              </div>
              <PanelChart projectId={projectId} panel={p} dash={dash} refreshMs={refresh * 1000} height={p.chart === 'stat' ? 140 : 200} />
            </div>
          ))}
        </div>
      )}

      {editing && (
        <PanelEditor
          projectId={projectId}
          initial={editing}
          dash={dash}
          title={val.panels.some((p) => p.id === editing.id) ? 'Edit chart' : 'Add chart'}
          saveLabel={val.panels.some((p) => p.id === editing.id) ? 'Save chart' : 'Add chart'}
          saving={save.isPending}
          error={save.error}
          onClose={() => setEditing(null)}
          onSave={(np) => {
            const exists = val.panels.some((p) => p.id === np.id)
            setPanels(exists ? val.panels.map((p) => (p.id === np.id ? np : p)) : [...val.panels, np])
            setEditing(null)
          }}
        />
      )}
      {modal === 'rename' && <RenameModal val={val} onClose={() => setModal(null)} onSave={(name, description) => (update({ name, description }), setModal(null))} />}
      {modal === 'delete' && (
        <ConfirmModal
          title="Delete this dashboard?"
          body={<>All versions of {val.name || objectId} will be deleted. The calls it charts are not affected.</>}
          confirmLabel="Delete"
          danger
          busy={del.isPending}
          error={del.error}
          onConfirm={() => del.mutate()}
          onClose={() => setModal(null)}
        />
      )}
      {deletePanel && (
        <ConfirmModal
          title="Remove this chart?"
          body={<>{panelTitle(deletePanel)} will be removed from the dashboard. Earlier versions of the dashboard keep it.</>}
          confirmLabel="Remove"
          danger
          onConfirm={() => {
            setPanels(val.panels.filter((x) => x.id !== deletePanel.id))
            setDeletePanel(null)
          }}
          onClose={() => setDeletePanel(null)}
        />
      )}
    </div>
  )
}

function RenameModal({ val, onClose, onSave }: { val: DashboardVal; onClose: () => void; onSave: (name: string, description: string) => void }) {
  const [name, setName] = useState(val.name)
  const [desc, setDesc] = useState(val.description ?? '')
  return (
    <Modal
      title="Dashboard details"
      onClose={onClose}
      footer={
        <>
          <button className="btn" onClick={onClose}>
            Cancel
          </button>
          <button className="btn primary" disabled={!name.trim()} onClick={() => onSave(name.trim(), desc.trim())}>
            Save
          </button>
        </>
      }
    >
      <label className="field">
        Name
        <input className="input" autoFocus value={name} onChange={(e) => setName(e.target.value)} />
      </label>
      <label className="field">
        Description
        <textarea className="input" rows={3} value={desc} onChange={(e) => setDesc(e.target.value)} />
      </label>
    </Modal>
  )
}
