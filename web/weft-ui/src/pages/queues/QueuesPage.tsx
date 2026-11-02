import { useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { queuesApi, type AnnotationQueue } from '../../api/queues'
import { parseRef } from '../../api/refs'
import { Empty, ErrorBox, Loading, Modal } from '../../components/common'
import { ConfirmButton } from '../../components/objects/bits'
import { queueUrl } from '../../components/queues/AddToQueueModal'
import { ExportQueueModal } from '../../components/queues/ExportQueueModal'
import { SpecPicker } from '../../components/queues/SpecPicker'
import { useProjectId } from '../../hooks/useProject'
import { fmtDate, fmtRelative } from '../../lib/format'
import { tracesUrl } from '../../lib/urls'

export function QueuesPage() {
  const projectId = useProjectId()
  const nav = useNavigate()
  const qc = useQueryClient()
  const [search, setSearch] = useState('')
  const [editing, setEditing] = useState<AnnotationQueue | 'new' | null>(null)
  const [exporting, setExporting] = useState<AnnotationQueue | null>(null)
  const queues = useQuery({ queryKey: ['annotation-queues', projectId], queryFn: () => queuesApi.query(projectId) })
  const ids = (queues.data ?? []).map((q) => q.id)
  const stats = useQuery({
    queryKey: ['annotation-queue-stats', projectId, ids],
    queryFn: () => queuesApi.stats(projectId, ids).then((r) => new Map(r.stats.map((s) => [s.queue_id, s]))),
    enabled: ids.length > 0,
  })
  const shown = (queues.data ?? []).filter((q) => !search || q.name.toLowerCase().includes(search.toLowerCase()))
  const totals = [...(stats.data?.values() ?? [])].reduce((a, s) => ({ items: a.items + s.total_items, done: a.done + s.completed_items }), { items: 0, done: 0 })

  return (
    <div className="page">
      <div className="page-header">
        <div className="col" style={{ gap: 2 }}>
          <h1>Annotation queues</h1>
          <span className="muted small">Collect calls for human review, then work through them one at a time with structured annotation fields.</span>
        </div>
        <span className="spacer" />
        <button className="btn primary" onClick={() => setEditing('new')}>
          + New queue
        </button>
      </div>
      <ErrorBox error={queues.error ?? stats.error} />
      {queues.isLoading ? (
        <Loading />
      ) : !queues.data?.length ? (
        <div className="card">
          <Empty>
            <div className="col" style={{ alignItems: 'center', gap: 10 }}>
              <div style={{ maxWidth: 520 }}>
                No annotation queues yet. Create a queue, then add calls from the <Link to={tracesUrl(projectId)}>Traces</Link> page (select rows → Add to queue) or
                from a call’s page.
              </div>
              <button className="btn primary" onClick={() => setEditing('new')}>
                + New queue
              </button>
            </div>
          </Empty>
        </div>
      ) : (
        <>
          <div className="stat-grid" style={{ marginBottom: 14 }}>
            <div className="stat">
              <div className="label">Queues</div>
              <div className="value">{queues.data.length}</div>
            </div>
            <div className="stat">
              <div className="label">Items</div>
              <div className="value">{totals.items}</div>
            </div>
            <div className="stat">
              <div className="label">Reviewed</div>
              <div className="value">
                {totals.done} <span className="faint small">{totals.items ? `${Math.round((100 * totals.done) / totals.items)}%` : ''}</span>
              </div>
            </div>
          </div>
          <div className="toolbar" style={{ marginBottom: 10 }}>
            <input className="input" style={{ maxWidth: 260 }} placeholder="Search queues…" value={search} onChange={(e) => setSearch(e.target.value)} />
          </div>
          <div className="table-wrap">
            <table className="table">
              <thead>
                <tr>
                  <th>Name</th>
                  <th>Fields</th>
                  <th style={{ width: 220 }}>Progress</th>
                  <th>Created by</th>
                  <th>Updated</th>
                  <th style={{ width: 210 }} />
                </tr>
              </thead>
              <tbody>
                {shown.map((q) => {
                  const st = stats.data?.get(q.id)
                  const pct = st && st.total_items ? st.completed_items / st.total_items : 0
                  return (
                    <tr key={q.id} className="clickable" onClick={() => nav(queueUrl(projectId, q.id))}>
                      <td>
                        <Link to={queueUrl(projectId, q.id)} style={{ fontWeight: 500 }} onClick={(e) => e.stopPropagation()}>
                          {q.name}
                        </Link>
                        {q.description && <div className="faint small cell-preview">{q.description}</div>}
                      </td>
                      <td>
                        <span className="chips">
                          {q.scorer_refs.length === 0 ? (
                            <span className="faint small">none</span>
                          ) : (
                            q.scorer_refs.map((r) => (
                              <span key={r} className="chip mono">
                                {parseRef(r)?.name ?? r}
                              </span>
                            ))
                          )}
                        </span>
                      </td>
                      <td>
                        {st ? (
                          <div className="row" style={{ gap: 8 }}>
                            <div className="progress grow" title={`${st.completed_items} of ${st.total_items} reviewed`}>
                              <span style={{ width: `${pct * 100}%` }} />
                            </div>
                            <span className="small mono">
                              {st.completed_items}/{st.total_items}
                            </span>
                          </div>
                        ) : (
                          <span className="faint small">…</span>
                        )}
                      </td>
                      <td className="muted">{q.created_by}</td>
                      <td className="muted" title={fmtDate(q.updated_at)}>
                        {fmtRelative(q.updated_at)}
                      </td>
                      <td onClick={(e) => e.stopPropagation()}>
                        <div className="row" style={{ gap: 4, justifyContent: 'flex-end' }}>
                          <Link className="btn sm primary" to={queueUrl(projectId, q.id)}>
                            Review
                          </Link>
                          <button className="btn sm" onClick={() => setExporting(q)}>
                            Export
                          </button>
                          <button className="btn sm ghost" onClick={() => setEditing(q)} title="Edit queue">
                            Edit
                          </button>
                          <ConfirmButton
                            className="btn ghost sm icon danger"
                            title={`Delete queue ${q.name}?`}
                            body="Annotations already given are kept as feedback on the calls."
                            onConfirm={async () => {
                              await queuesApi.remove(projectId, q.id)
                              await qc.invalidateQueries({ queryKey: ['annotation-queues', projectId] })
                            }}
                          >
                            ✕
                          </ConfirmButton>
                        </div>
                      </td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        </>
      )}
      {editing && (
        <QueueEditor
          projectId={projectId}
          initial={editing === 'new' ? undefined : editing}
          onClose={() => setEditing(null)}
          onSaved={(id, created) => {
            setEditing(null)
            if (created) nav(queueUrl(projectId, id))
          }}
        />
      )}
      {exporting && <ExportQueueModal projectId={projectId} queue={exporting} onClose={() => setExporting(null)} />}
    </div>
  )
}

function QueueEditor({
  projectId,
  initial,
  onClose,
  onSaved,
}: {
  projectId: string
  initial?: AnnotationQueue
  onClose: () => void
  onSaved: (id: string, created: boolean) => void
}) {
  const qc = useQueryClient()
  const [name, setName] = useState(initial?.name ?? '')
  const [description, setDescription] = useState(initial?.description ?? '')
  const [specs, setSpecs] = useState<string[]>(initial?.scorer_refs ?? [])
  const save = useMutation({
    mutationFn: async () => {
      if (initial) {
        await queuesApi.update(projectId, initial.id, { name: name.trim(), description, scorer_refs: specs })
        return initial.id
      }
      return (await queuesApi.create(projectId, name.trim(), description, specs)).id
    },
    onSuccess: async (id) => {
      await qc.invalidateQueries({ queryKey: ['annotation-queues', projectId] })
      await qc.invalidateQueries({ queryKey: ['annotation-queue', projectId, id] })
      onSaved(id, !initial)
    },
  })
  return (
    <Modal
      title={initial ? `Edit queue ${initial.name}` : 'New annotation queue'}
      onClose={onClose}
      footer={
        <>
          <span className="hint grow">{specs.length === 0 ? 'Without annotation fields reviewers can only mark items done or skipped.' : ''}</span>
          <button className="btn" onClick={onClose}>
            Cancel
          </button>
          <button className="btn primary" disabled={!name.trim() || save.isPending} onClick={() => save.mutate()}>
            {save.isPending ? 'Saving…' : initial ? 'Save' : 'Create queue'}
          </button>
        </>
      }
    >
      <div className="col" style={{ gap: 12 }}>
        <label className="field">
          Name
          <input className="input" value={name} onChange={(e) => setName(e.target.value)} placeholder="Weekly quality review" autoFocus />
        </label>
        <label className="field">
          Description
          <textarea className="input" rows={2} value={description} onChange={(e) => setDescription(e.target.value)} placeholder="What reviewers should look for" />
        </label>
        <div className="field">
          Annotation fields
          <SpecPicker projectId={projectId} value={specs} onChange={setSpecs} />
        </div>
        <ErrorBox error={save.error} />
      </div>
    </Modal>
  )
}
