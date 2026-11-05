// Version tags (/api/v1/objects/{name}/versions/{version}/tags): chips with add/remove.
import { useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { adminApi } from '../../api/admin'
import { errMsg } from './objectUtils'
import './bits.css'

export function TagEditor({ projectId, objectId, digest, tags, compact }: { projectId: string; objectId: string; digest: string; tags: string[]; compact?: boolean }) {
  const qc = useQueryClient()
  const [adding, setAdding] = useState(false)
  const [val, setVal] = useState('')
  const [err, setErr] = useState<string | null>(null)
  const refresh = () => {
    void qc.invalidateQueries({ queryKey: ['obj-versions', projectId, objectId] })
    void qc.invalidateQueries({ queryKey: ['object', projectId, objectId] })
    void qc.invalidateQueries({ queryKey: ['objects', projectId] })
  }
  const run = async (fn: () => Promise<unknown>) => {
    try {
      await fn()
      setErr(null)
      refresh()
    } catch (e) {
      setErr(errMsg(e))
    }
  }
  const add = async () => {
    const t = val.trim()
    setAdding(false)
    setVal('')
    if (t && !tags.includes(t)) await run(() => adminApi.tagsAdd(projectId, objectId, digest, [t]))
  }
  return (
    <span className="name-list" onClick={(e) => e.stopPropagation()} data-testid="tag-editor">
      {tags.map((t) => (
        <span key={t} className="name-item" title="tag">
          {t}
          <button type="button" className="name-x" aria-label={`Remove tag ${t}`} title={`Remove tag ${t}`} onClick={() => void run(() => adminApi.tagsRemove(projectId, objectId, digest, [t]))}>
            ✕
          </button>
        </span>
      ))}
      {adding ? (
        <input
          className="input name-input"
          aria-label="New tag"
          autoFocus
          value={val}
          placeholder="tag, e.g. reviewed"
          onChange={(e) => setVal(e.target.value.replace(/[^\w.:/ -]/g, ''))}
          onKeyDown={(e) => {
            if (e.key === 'Enter') void add()
            if (e.key === 'Escape') setAdding(false)
          }}
          onBlur={() => void add()}
        />
      ) : (
        <button type="button" className="name-add" onClick={() => setAdding(true)} title="Tag this version">
          Add a tag
        </button>
      )}
      {err && !compact && <span className="small" style={{ color: 'var(--error)' }}>{err}</span>}
    </span>
  )
}
