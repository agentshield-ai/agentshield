import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { dashboardsApi, type DashboardVal } from '../../api/dashboards'
import { ErrorBox, Loading, Modal } from '../../components/common'
import { GroupedList, ListRow } from '../../components/ui/GroupedList'
import { PageHeader } from '../../components/ui/PageHeader'
import { SearchField } from '../../components/ui/SearchField'
import { Sentence } from '../../components/ui/Sentence'
import { dashboardLine, dashboardsSentence, rangeWords } from '../../lib/summaries/dashboards'
import { relativeTime } from '../../lib/summaries'
import '../observe.css'
import { dashboardUrl } from '../../components/dashboards/AddToDashboardModal'
import { newDashboard, panelTitle, timeRangeLabel } from '../../components/dashboards/dashboardUtils'
import { slugify } from '../../components/traces/callUtils'
import { useProjectId } from '../../hooks/useProject'
import { fmtDate } from '../../lib/format'
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
      if (existing.some((o) => o.name === id)) throw new Error(`A dashboard called “${id}” already exists`)
      await dashboardsApi.save(projectId, id, newDashboard(name.trim(), template))
      return id
    },
    onSuccess: (id) => {
      void qc.invalidateQueries({ queryKey: ['objects', projectId, 'Dashboard'] })
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
                ['overview', 'Overview', 'Spans, errors, latency, tokens and cost by model, and a span-name table.'],
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
  const list = useQuery({ queryKey: ['objects', projectId, 'Dashboard'], queryFn: () => dashboardsApi.list(projectId) })
  const all = list.data ?? []
  const shown = all.filter((o) => {
    const s = q.trim().toLowerCase()
    const v = o.value as DashboardVal
    return !s || o.name.toLowerCase().includes(s) || (v?.name ?? '').toLowerCase().includes(s) || (v?.description ?? '').toLowerCase().includes(s)
  })
  const newButton = (
    <button type="button" className="qbtn primary" onClick={() => setCreating(true)}>
      New dashboard
    </button>
  )
  return (
    <div className="page-frame obs-page">
      <PageHeader
        title="Dashboards"
        help="project.md#dashboards"
        controls={
          <>
            {all.length > 4 && <SearchField label="Search dashboards" value={q} onChange={setQ} filterInput />}
            {newButton}
          </>
        }
        sentence={
          list.data ? (
            <Sentence parts={[dashboardsSentence(all.map((o) => ({ charts: ((o.value as DashboardVal)?.panels ?? []).length })))]} />
          ) : (
            <p className="sentence s-outcome sk-sentence" aria-busy="true">
              <span className="sr-only">Counting dashboards…</span>
              <span className="sk sk-line" aria-hidden />
            </p>
          )
        }
      />
      <ErrorBox error={list.error} />
      {list.isLoading ? (
        <Loading />
      ) : all.length === 0 ? (
        <div className="quiet-empty">
          <p>A dashboard holds charts of any span figure: counts, latency percentiles, tokens, cost and scores, grouped by name, model or attribute.</p>
          <p className="quiet-note obs-wrap">
            Create one from a template with New dashboard, or open Traces, choose More and add a chart of the spans you are looking at.
          </p>
        </div>
      ) : shown.length === 0 ? (
        <div className="quiet-empty">
          <p>No dashboard matches “{q}”.</p>
          <button type="button" className="qbtn" onClick={() => setQ('')}>
            Clear the search
          </button>
        </div>
      ) : (
        <GroupedList label="Dashboards">
          {shown.map((o) => {
            const v = o.value as DashboardVal
            const panels = v?.panels ?? []
            return (
              <ListRow
                key={o.name}
                title={v?.name || o.name}
                summary={dashboardLine({ titles: panels.map(panelTitle), range: rangeWords(v?.time_range ?? 'all', timeRangeLabel) })}
                meta={v?.description || undefined}
                time={relativeTime(o.created_at)}
                timeTitle={`Saved ${fmtDate(o.created_at)} (version ${o.version})`}
                to={dashboardUrl(projectId, o.name)}
              />
            )
          })}
        </GroupedList>
      )}
      {creating && <NewDashboardModal projectId={projectId} onClose={() => setCreating(false)} />}
    </div>
  )
}
