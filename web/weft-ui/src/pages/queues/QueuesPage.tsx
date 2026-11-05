// Review queues (DESIGN.md §7, §7.1 rules 1 and 11, §7.2): one sentence
// answering "what is waiting for me to review?", then each queue with how
// much is left, in words. A queue opens its one-item-at-a-time review;
// export, edit and delete sit in each row's menu (deleting can be undone).
import { useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '../../api/client'
import { queuesApi, type AnnotationQueue } from '../../api/queues'
import { parseRef } from '../../api/refs'
import { ErrorBox, Modal } from '../../components/common'
import { toSentence } from '../../components/evals/sentence'
import { queueUrl } from '../../components/queues/AddToQueueModal'
import { ExportQueueModal } from '../../components/queues/ExportQueueModal'
import { SpecPicker } from '../../components/queues/SpecPicker'
import { Dropdown, MenuItem } from '../../components/traces/Menu'
import { GroupedList, ListRow } from '../../components/ui/GroupedList'
import { PageHeader } from '../../components/ui/PageHeader'
import { SearchField } from '../../components/ui/SearchField'
import { Sentence } from '../../components/ui/Sentence'
import { StatusGlyph } from '../../components/ui/StatusGlyph'
import { undoable } from '../../components/ui/Toasts'
import { useProjectId } from '../../hooks/useProject'
import { fmtDate, fmtRelative } from '../../lib/format'
import { queueAsks, queueLine, queuesSentence } from '../../lib/summaries/evaluate'

import { tracesUrl } from '../../lib/urls'
import '../evaluations/evaluate.css'

export function QueuesPage() {
  const projectId = useProjectId()
  const nav = useNavigate()
  const qc = useQueryClient()
  const [search, setSearch] = useState('')
  const [editing, setEditing] = useState<AnnotationQueue | 'new' | null>(null)
  const [exporting, setExporting] = useState<AnnotationQueue | null>(null)
  const [hiddenQueues, setHiddenQueues] = useState<Set<string>>(new Set())
  const queues = useQuery({ queryKey: ['annotation-queues', projectId], queryFn: () => queuesApi.query(projectId) })
  const ids = (queues.data ?? []).map((q) => q.id)
  const stats = useQuery({
    queryKey: ['annotation-queue-stats', projectId, ids],
    queryFn: () => queuesApi.stats(projectId, ids).then((r) => new Map(r.stats.map((s) => [s.queue_id, s]))),
    enabled: ids.length > 0,
  })
  // The queues' annotation fields, read once, so each row can say what it asks of reviewers.
  const specRefs = [...new Set((queues.data ?? []).flatMap((q) => q.scorer_refs))].sort()
  const specs = useQuery({
    queryKey: ['annotation-specs', projectId, specRefs],
    queryFn: () => api.refsRead(specRefs).then((r) => new Map(specRefs.map((ref, i) => [ref, r.items[i]]))),
    enabled: specRefs.length > 0,
    staleTime: 60_000,
  })
  const live = (queues.data ?? []).filter((q) => !hiddenQueues.has(q.id))
  const shown = live.filter((q) => !search || q.name.toLowerCase().includes(search.toLowerCase()) || q.description.toLowerCase().includes(search.toLowerCase()))
  const counts = (id: string) => {
    const s = stats.data?.get(id)
    return { total: s?.total_items ?? 0, done: s?.completed_items ?? 0 }
  }
  const waiting = shown.filter((q) => counts(q.id).total - counts(q.id).done > 0)
  const done = shown.filter((q) => !(counts(q.id).total - counts(q.id).done > 0))
  const empty = !queues.isLoading && !queues.error && live.length === 0
  const remove = (q: AnnotationQueue) =>
    undoable({
      message: `Deleted queue “${q.name}”`,
      apply: () => setHiddenQueues((h) => new Set(h).add(q.id)),
      revert: () =>
        setHiddenQueues((h) => {
          const n = new Set(h)
          n.delete(q.id)
          return n
        }),
      commit: async () => {
        await queuesApi.remove(projectId, q.id)
        await qc.invalidateQueries({ queryKey: ['annotation-queues', projectId] })
      },
    })

  const row = (q: AnnotationQueue) => {
    const c = counts(q.id)
    const left = c.total - c.done
    const fields = q.scorer_refs.map((r) => {
      const v = specs.data?.get(r)
      return { name: String(v?.name ?? parseRef(r)?.name ?? r), schema: v?.field_schema ?? null }
    })
    // Waiting: the approval glyph; all reviewed: a check. An empty queue has nothing to show, so no glyph (a blank keeps the text aligned).
    const glyph =
      left > 0 ? (
        <StatusGlyph kind="approval" label={`${left} left to review`} />
      ) : c.total ? (
        <StatusGlyph kind="ok" label="All reviewed" />
      ) : (
        <span className="ev-glyph-space" aria-hidden />
      )
    return (
      <ListRow
        key={q.id}
        glyph={glyph}
        title={q.name}
        summary={stats.data ? queueLine(c) : 'Counting…'}
        meta={[q.description, queueAsks(fields), q.created_by && q.created_by !== 'anonymous' ? `by ${q.created_by}` : ''].filter(Boolean).join(' · ')}
        time={fmtRelative(q.updated_at)}
        timeTitle={`Updated ${fmtDate(q.updated_at)}`}
        to={queueUrl(projectId, q.id)}
        trailing={
          <span className="ev-actions">
            <Dropdown
              align="right"
              className="icon-btn"
              label={
                <>
                  <span aria-hidden>⋯</span>
                  <span className="sr-only">More for {q.name}</span>
                </>
              }
            >
              {(close) => (
                <>
                  <MenuItem
                    onClick={() => {
                      setExporting(q)
                      close()
                    }}
                  >
                    Export to a dataset…
                  </MenuItem>
                  <MenuItem
                    onClick={() => {
                      setEditing(q)
                      close()
                    }}
                  >
                    Edit queue…
                  </MenuItem>
                  <div className="dd-sep" />
                  <MenuItem
                    onClick={() => {
                      remove(q)
                      close()
                    }}
                  >
                    Delete queue (annotations are kept)
                  </MenuItem>
                </>
              )}
            </Dropdown>
          </span>
        }
      />
    )
  }

  return (
    <div className="page-frame">
      <PageHeader
        title="Review queues"
        help="evaluations.md#review-queues"
        helpTopic="Review queues"
        controls={
          <>
            {!empty && <SearchField label="Search queues" value={search} onChange={setSearch} filterInput />}
            <button type="button" className="qbtn primary" onClick={() => setEditing('new')}>
              New queue
            </button>
          </>
        }
        sentence={
          empty ? undefined : !queues.data || (ids.length > 0 && !stats.data) ? (
            <p className="sentence s-outcome sk-sentence" aria-busy="true">
              <span className="sr-only">Counting the items…</span>
              <span className="sk sk-line" aria-hidden />
            </p>
          ) : (
            <Sentence
              parts={toSentence(
                queuesSentence(live.map((q) => ({ id: q.id, name: q.name, ...counts(q.id) }))),
                (l) => (l.kind === 'queue' ? { to: queueUrl(projectId, l.id), title: 'Start reviewing' } : null),
              )}
            />
          )
        }
      />
      <ErrorBox error={queues.error ?? stats.error} />
      {empty ? (
        <div className="quiet-empty">
          <p>
            <strong>No review queues yet.</strong> A queue collects agent steps for people to review one at a time, with the annotation fields you choose.
          </p>
          <p className="ev-note">
            Create a queue, then add steps from <Link to={tracesUrl(projectId)}>Traces</Link> (select rows, then Add to queue) or from a step’s page.
          </p>
        </div>
      ) : queues.isLoading ? null : shown.length === 0 ? (
        <div className="quiet-empty">
          <p>No queues match “{search}”.</p>
        </div>
      ) : (
        <>
          {waiting.length > 0 && (
            <GroupedList title="Waiting for review" count={waiting.length} className="ev-menu-list">
              {waiting.map(row)}
            </GroupedList>
          )}
          {done.length > 0 && (
            <GroupedList title={waiting.length ? 'Done or empty' : 'Queues'} count={done.length} className="ev-menu-list">
              {done.map(row)}
            </GroupedList>
          )}
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
      title={initial ? `Edit queue ${initial.name}` : 'New review queue'}
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
