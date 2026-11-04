import { Link } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { api } from '../../api/client'
import { objectRef, parseRef } from '../../api/refs'
import type { WeftObject } from '../../api/types'
import { settingsUrl } from '../../lib/urls'
import { Spinner } from '../common'
import { specName } from './queueUtils'

export function useAnnotationSpecs(projectId: string) {
  return useQuery({
    queryKey: ['objects', projectId, 'AnnotationSpec', 'latest'],
    queryFn: () => api.objectsQuery({ project_id: projectId, filter: { kinds: ['annotation_spec'], latest_only: true }, limit: 200 }),
    select: (r) => r.items.filter((o) => !o.deleted_at),
  })
}

export const specRef = (o: WeftObject) => objectRef(o.project_id, o.name, o.digest)

function schemaLabel(o: WeftObject): string {
  const s = o.value?.field_schema ?? {}
  if (Array.isArray(s.enum)) return `one of ${s.enum.slice(0, 4).join(', ')}${s.enum.length > 4 ? '…' : ''}`
  if (s.minimum != null && s.maximum != null) return `${s.type ?? 'number'} ${s.minimum}–${s.maximum}`
  return s.type ?? 'text'
}

/** Multi-select of AnnotationSpec objects; values are object refs. */
export function SpecPicker({ projectId, value, onChange }: { projectId: string; value: string[]; onChange: (refs: string[]) => void }) {
  const specs = useAnnotationSpecs(projectId)
  if (specs.isLoading) return <Spinner label="Loading annotation specs…" />
  const list = specs.data ?? []
  const names = new Set(value.map((r) => parseRef(r)?.name))
  if (!list.length)
    return (
      <div className="hint">
        No annotation specs yet. <Link to={settingsUrl(projectId, 'annotations')}>Create one in Settings</Link> (for example a 1–5 quality score or a
        correct/incorrect label), then pick it here.
      </div>
    )
  return (
    <div className="pick-list">
      {list.map((o) => {
        const ref = specRef(o)
        const on = value.includes(ref) || names.has(o.name)
        return (
          <label key={o.name}>
            <input
              type="checkbox"
              checked={on}
              onChange={() => onChange(on ? value.filter((r) => parseRef(r)?.name !== o.name) : [...value, ref])}
            />
            <span className="mono grow truncate">{specName(o)}</span>
            <span className="faint small">{schemaLabel(o)}</span>
          </label>
        )
      })}
    </div>
  )
}
