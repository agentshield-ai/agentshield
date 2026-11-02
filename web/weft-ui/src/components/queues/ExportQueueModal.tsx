import { useState } from 'react'
import { Link } from 'react-router-dom'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '../../api/client'
import { queuesApi, type AnnotationQueue } from '../../api/queues'
import { parseRef, tableRef } from '../../api/refs'
import type { Call } from '../../api/types'
import { objectUrl } from '../../lib/urls'
import { ErrorBox, Modal } from '../common'
import { slugify } from '../traces/callUtils'
import { buildExportRows } from './queueUtils'

/** Spec names of a queue's scorer_refs (AnnotationSpec name, else ref name). */
export async function queueSpecNames(q: AnnotationQueue): Promise<string[]> {
  if (!q.scorer_refs.length) return []
  const { vals } = await api.refsReadBatch(q.scorer_refs)
  return q.scorer_refs.map((r, i) => String(vals[i]?.name ?? parseRef(r)?.name ?? r))
}

/** Collects a queue's calls and annotations and saves them as a Dataset. */
export async function exportQueue(projectId: string, q: AnnotationQueue, datasetName: string, includeUnannotated: boolean) {
  const id = slugify(datasetName)
  if (!id) throw new Error('Enter a dataset name')
  const [{ items }, names, fb] = await Promise.all([
    queuesApi.items(projectId, q.id, { limit: 10000 }),
    queueSpecNames(q),
    api.feedbackQuery({ project_id: projectId, query: { $expr: { $eq: [{ $getField: 'queue_id' }, { $literal: q.id }] } }, limit: 100000 }),
  ])
  const calls = new Map<string, Call>()
  const ids = items.map((i) => i.call_id)
  for (let i = 0; i < ids.length; i += 500) {
    const r = await api.callsQuery({ project_id: projectId, filter: { call_ids: ids.slice(i, i + 500) }, limit: 500 })
    for (const c of r.calls) calls.set(c.id, c)
  }
  const rows = buildExportRows(items, calls, fb.result, names, includeUnannotated)
  if (!rows.length) throw new Error('Nothing to export yet: no call in this queue has an annotation.')
  const t = await api.tableCreate(projectId, rows)
  const val = {
    _type: 'Dataset',
    _class_name: 'Dataset',
    _bases: ['Object', 'BaseModel'],
    name: id,
    description: `Annotations from queue "${q.name}"`,
    rows: tableRef(projectId, t.digest),
  }
  const r = await api.objCreate(projectId, id, val, 'Dataset')
  return { objectId: id, digest: r.digest, rows: rows.length }
}

export function ExportQueueModal({ projectId, queue, onClose }: { projectId: string; queue: AnnotationQueue; onClose: () => void }) {
  const qc = useQueryClient()
  const [name, setName] = useState(slugify(`${queue.name}-annotations`))
  const [all, setAll] = useState(false)
  const run = useMutation({
    mutationFn: () => exportQueue(projectId, queue, name, all),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ['objs', projectId] }),
  })
  const done = run.data
  return (
    <Modal
      title={`Export "${queue.name}" to a dataset`}
      onClose={onClose}
      footer={
        done ? (
          <>
            <Link className="btn" to={objectUrl(projectId, done.objectId, done.digest)}>
              Open dataset
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
            <button className="btn primary" disabled={run.isPending || !slugify(name)} onClick={() => run.mutate()}>
              {run.isPending ? 'Exporting…' : 'Export'}
            </button>
          </>
        )
      }
    >
      {done ? (
        <div className="success-box">
          Saved {done.rows} row{done.rows === 1 ? '' : 's'} to dataset <b>{done.objectId}</b>.
        </div>
      ) : (
        <div className="col" style={{ gap: 12 }}>
          <span className="muted small">
            Each row holds a call’s inputs and output, one column per annotation field with the latest value given in this queue, and the annotators.
          </span>
          <label className="field">
            Dataset name
            <input className="input mono" value={name} onChange={(e) => setName(e.target.value)} />
          </label>
          <label className="checkbox">
            <input type="checkbox" checked={all} onChange={(e) => setAll(e.target.checked)} /> Include calls without annotations
          </label>
          <ErrorBox error={run.error} />
        </div>
      )}
    </Modal>
  )
}
