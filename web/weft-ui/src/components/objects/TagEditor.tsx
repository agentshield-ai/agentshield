// Version tags (Weave /objs/{id}/versions/{digest}/tags): chips with add/remove.
import { useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { adminApi } from '../../api/admin'
import { errMsg } from './objectUtils'

export function TagEditor({ projectId, objectId, digest, tags, compact }: { projectId: string; objectId: string; digest: string; tags: string[]; compact?: boolean }) {
  const qc = useQueryClient()
  const [adding, setAdding] = useState(false)
  const [val, setVal] = useState('')
  const [err, setErr] = useState<string | null>(null)
  const refresh = () => {
    void qc.invalidateQueries({ queryKey: ['obj-versions', projectId, objectId] })
    void qc.invalidateQueries({ queryKey: ['obj', projectId, objectId] })
    void qc.invalidateQueries({ queryKey: ['objs', projectId] })
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
    <span className="chips" onClick={(e) => e.stopPropagation()} data-testid="tag-editor">
      {tags.map((t) => (
        <span key={t} className="chip" title="tag">
          #{t}
          <button className="x" title={`Remove tag ${t}`} onClick={() => void run(() => adminApi.tagsRemove(projectId, objectId, digest, [t]))}>
            ✕
          </button>
        </span>
      ))}
      {adding ? (
        <input
          className="input"
          style={{ height: 22, width: 120, fontSize: 12 }}
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
        <button className="chip selectable" style={{ borderStyle: 'dashed' }} onClick={() => setAdding(true)} title="Tag this version">
          + tag
        </button>
      )}
      {err && !compact && <span className="small" style={{ color: 'var(--error)' }}>{err}</span>}
    </span>
  )
}
