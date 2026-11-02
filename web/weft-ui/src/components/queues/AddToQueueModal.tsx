import { useState } from 'react'
import { Link } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { queuesApi } from '../../api/queues'
import { ErrorBox, Modal, Spinner } from '../common'
import { SpecPicker } from './SpecPicker'

export const queueUrl = (projectId: string, queueId: string) => `/${projectId}/queues/${encodeURIComponent(queueId)}`
export const queuesUrl = (projectId: string) => `/${projectId}/queues`

/** Adds calls to an existing annotation queue or a new one. */
export function AddToQueueModal({ projectId, callIds, onClose }: { projectId: string; callIds: string[]; onClose: () => void }) {
  const qc = useQueryClient()
  const queues = useQuery({ queryKey: ['annotation-queues', projectId], queryFn: () => queuesApi.query(projectId) })
  const [mode, setMode] = useState<'existing' | 'new'>('existing')
  const [queueId, setQueueId] = useState('')
  const [name, setName] = useState('')
  const [specs, setSpecs] = useState<string[]>([])
  const noQueues = queues.isSuccess && queues.data.length === 0
  const effMode = noQueues ? 'new' : mode

  const add = useMutation({
    mutationFn: async () => {
      let id = queueId
      if (effMode === 'new') {
        if (!name.trim()) throw new Error('Enter a queue name')
        id = (await queuesApi.create(projectId, name.trim(), '', specs)).id
      }
      if (!id) throw new Error('Choose a queue')
      const r = await queuesApi.addItems(projectId, id, callIds)
      return { ...r, queueId: id }
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['annotation-queues', projectId] })
      void qc.invalidateQueries({ queryKey: ['annotation-queue-stats', projectId] })
    },
  })
  const done = add.data
  const n = callIds.length
  return (
    <Modal
      title={`Add ${n} call${n === 1 ? '' : 's'} to an annotation queue`}
      onClose={onClose}
      footer={
        done ? (
          <>
            <Link className="btn" to={queueUrl(projectId, done.queueId)}>
              Start reviewing
            </Link>
            <button className="btn primary" onClick={onClose}>
              Done
            </button>
          </>
        ) : (
          <>
            <button className="btn" onClick={onClose}>
              Cancel
            </button>
            <button
              className="btn primary"
              disabled={add.isPending || (effMode === 'existing' ? !queueId : !name.trim())}
              onClick={() => add.mutate()}
            >
              {add.isPending ? 'Adding…' : 'Add to queue'}
            </button>
          </>
        )
      }
    >
      {done ? (
        <div className="success-box">
          Added {done.added_count} call{done.added_count === 1 ? '' : 's'}
          {done.duplicates > 0 && <> ({done.duplicates} already in the queue)</>}.
        </div>
      ) : (
        <div className="col" style={{ gap: 12 }}>
          {queues.isLoading && <Spinner label="Loading queues…" />}
          {!noQueues && queues.isSuccess && (
            <div className="seg" role="radiogroup">
              <button className={mode === 'existing' ? 'on' : ''} onClick={() => setMode('existing')}>
                Existing queue
              </button>
              <button className={mode === 'new' ? 'on' : ''} onClick={() => setMode('new')}>
                New queue
              </button>
            </div>
          )}
          {effMode === 'existing' && queues.data && (
            <div className="pick-list" style={{ maxHeight: 260 }}>
              {queues.data.map((q) => (
                <label key={q.id}>
                  <input type="radio" name="queue" checked={queueId === q.id} onChange={() => setQueueId(q.id)} />
                  <span className="grow truncate">{q.name}</span>
                  <span className="faint small">{q.scorer_refs.length} field{q.scorer_refs.length === 1 ? '' : 's'}</span>
                </label>
              ))}
            </div>
          )}
          {effMode === 'new' && (
            <>
              <label className="field">
                Queue name
                <input className="input" value={name} onChange={(e) => setName(e.target.value)} placeholder="Weekly review" autoFocus />
              </label>
              <div className="field">
                Annotation fields
                <SpecPicker projectId={projectId} value={specs} onChange={setSpecs} />
              </div>
            </>
          )}
          <ErrorBox error={queues.error ?? add.error} />
        </div>
      )}
    </Modal>
  )
}

/** Header/toolbar button that opens AddToQueueModal. */
export function AddToQueueButton({ projectId, callIds, className = 'btn sm' }: { projectId: string; callIds: string[]; className?: string }) {
  const [open, setOpen] = useState(false)
  return (
    <>
      <button className={className} disabled={!callIds.length} onClick={() => setOpen(true)}>
        Add to queue
      </button>
      {open && <AddToQueueModal projectId={projectId} callIds={callIds} onClose={() => setOpen(false)} />}
    </>
  )
}
