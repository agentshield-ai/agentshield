import { useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '../../api/client'
import { parseRef, tableRef } from '../../api/refs'
import type { Any, Span } from '../../api/types'
import { objectUrl } from '../../lib/urls'
import { preview } from '../../lib/format'
import { Modal, ErrorBox } from '../common'
import { callToDatasetRow, slugify } from './callUtils'

/** Create a Dataset from calls, or append them to an existing one. */
export function AddToDatasetModal({ projectId, calls, onClose }: { projectId: string; calls: Span[]; onClose: () => void }) {
  const qc = useQueryClient()
  const datasets = useQuery({
    queryKey: ['objects', projectId, 'Dataset', 'latest'],
    queryFn: () => api.objectsQuery({ project_id: projectId, filter: { kinds: ['dataset'], latest_only: true }, sort_by: [{ field: 'created_at', direction: 'desc' }], limit: 200 }),
  })
  const [mode, setMode] = useState<'new' | 'existing'>('new')
  const [name, setName] = useState('')
  const [existing, setExisting] = useState('')
  const [includeOutput, setIncludeOutput] = useState(true)
  const rows = useMemo(() => calls.map((c) => callToDatasetRow(c, includeOutput)), [calls, includeOutput])
  const cols = useMemo(() => [...new Set(rows.flatMap((r) => Object.keys(r)))], [rows])

  const save = useMutation({
    mutationFn: async () => {
      if (mode === 'new') {
        const id = slugify(name)
        if (!id) throw new Error('Enter a dataset name')
        const t = await api.tableCreate(projectId, rows)
        const val = {
          _type: 'Dataset',
          _class_name: 'Dataset',
          _bases: ['Object', 'BaseModel'],
          name: id,
          description: null,
          rows: tableRef(projectId, t.digest),
        }
        const r = await api.objectSave(projectId, id, val, 'Dataset')
        return { objectId: id, digest: r.digest, added: rows.length }
      }
      if (!existing) throw new Error('Choose a dataset')
      const obj = await api.objectGet(projectId, existing, 'latest')
      const val: Any = { ...obj.value }
      const ref = typeof val.rows === 'string' ? parseRef(val.rows) : null
      let digest: string
      if (ref && ref.kind === 'table') {
        const u = await api.tableUpdate(projectId, ref.name, rows.map((row) => ({ append: { row } })))
        digest = u.digest
      } else {
        const prev: Any[] = Array.isArray(val.rows) ? val.rows : []
        digest = (await api.tableCreate(projectId, [...prev, ...rows])).digest
      }
      val.rows = tableRef(projectId, digest)
      const r = await api.objectSave(projectId, existing, val, 'Dataset')
      return { objectId: existing, digest: r.digest, added: rows.length }
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['objects', projectId] })
    },
  })

  const done = save.data
  return (
    <Modal
      title={`Add ${calls.length} call${calls.length === 1 ? '' : 's'} to a dataset`}
      onClose={onClose}
      wide
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
            <button className="btn primary" disabled={save.isPending || calls.length === 0} onClick={() => save.mutate()}>
              {save.isPending ? 'Saving…' : mode === 'new' ? 'Create dataset' : 'Append rows'}
            </button>
          </>
        )
      }
    >
      {done ? (
        <div className="success-box">
          Added {done.added} row{done.added === 1 ? '' : 's'} to <b>{done.objectId}</b>.
        </div>
      ) : (
        <>
          <div className="row wrap" style={{ gap: 16 }}>
            <label className="checkbox">
              <input type="radio" checked={mode === 'new'} onChange={() => setMode('new')} /> New dataset
            </label>
            <label className="checkbox">
              <input type="radio" checked={mode === 'existing'} onChange={() => setMode('existing')} disabled={!datasets.data?.items.length} /> Existing dataset
              {datasets.data && !datasets.data.items.length && <span className="faint small">(none yet)</span>}
            </label>
            <span className="grow" />
            <label className="checkbox">
              <input type="checkbox" checked={includeOutput} onChange={(e) => setIncludeOutput(e.target.checked)} /> Include output as <code>output</code>
            </label>
          </div>
          {mode === 'new' ? (
            <label className="field">
              Dataset name
              <input className="input" autoFocus value={name} onChange={(e) => setName(e.target.value)} placeholder="e.g. support-questions" />
              {name && slugify(name) !== name && <span className="faint">Saved as {slugify(name)}</span>}
            </label>
          ) : (
            <label className="field">
              Dataset
              <select className="input" value={existing} onChange={(e) => setExisting(e.target.value)}>
                <option value="">Choose…</option>
                {datasets.data?.items.map((o) => (
                  <option key={o.name} value={o.name}>
                    {o.name} (v{o.version})
                  </option>
                ))}
              </select>
            </label>
          )}
          <div className="small muted">Preview ({rows.length} rows)</div>
          <div className="table-wrap" style={{ maxHeight: 260 }}>
            <table className="table">
              <thead>
                <tr>
                  {cols.map((c) => (
                    <th key={c}>{c}</th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {rows.slice(0, 20).map((r, i) => (
                  <tr key={i}>
                    {cols.map((c) => (
                      <td key={c}>
                        <span className="cell-preview">{preview(r[c], 80)}</span>
                      </td>
                    ))}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <ErrorBox error={save.error} />
        </>
      )}
    </Modal>
  )
}
