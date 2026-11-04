import { useState } from 'react'
import { Link } from 'react-router-dom'
import { ScorerFormModal } from '../objects/ObjectForms'
import { isServerScorer, KIND_META, refOf, SCORER_LABELS, useObjects, valClass, type ServerScorerClass } from '../objects/objectUtils'
import { objectUrl } from '../../lib/urls'
import { parseRef } from '../../api/refs'
import { Spinner } from '../common'

/**
 * Multi-select of server-runnable scorer objects (latest versions), with an
 * inline "new scorer" form. Values are object refs.
 */
export function ScorerPicker({
  projectId,
  value,
  onChange,
  judgeOnlyCreate,
}: {
  projectId: string
  value: string[]
  onChange: (refs: string[]) => void
  judgeOnlyCreate?: boolean
}) {
  const scorers = useObjects(projectId, KIND_META.scorers.filter)
  const [creating, setCreating] = useState(false)
  const runnable = (scorers.data ?? []).filter(isServerScorer)
  const other = (scorers.data ?? []).filter((o) => !isServerScorer(o))
  const selectedNames = new Set(value.map((r) => parseRef(r)?.name))
  // refs selected that are not the latest version of a listed scorer
  const extra = value.filter((r) => !runnable.some((o) => refOf(o) === r))
  const toggle = (ref: string, name: string) => {
    if (value.includes(ref)) onChange(value.filter((r) => r !== ref))
    else onChange([...value.filter((r) => parseRef(r)?.name !== name), ref])
  }
  return (
    <div className="col" style={{ gap: 6 }}>
      {scorers.isLoading ? (
        <Spinner label="Loading scorers…" />
      ) : runnable.length === 0 && extra.length === 0 ? (
        <div className="hint">No server-runnable scorers yet. Create one below.</div>
      ) : (
        <div className="pick-list">
          {extra.map((r) => (
            <label key={r}>
              <input type="checkbox" checked onChange={() => onChange(value.filter((x) => x !== r))} />
              <span className="mono grow truncate">{parseRef(r)?.name ?? r}</span>
              <span className="badge">{parseRef(r)?.digest?.slice(0, 6) ?? ''}</span>
            </label>
          ))}
          {runnable.map((o) => {
            const ref = refOf(o)
            const cls = valClass(o.value) as ServerScorerClass
            return (
              <label key={o.name}>
                <input type="checkbox" checked={value.includes(ref)} onChange={() => toggle(ref, o.name)} />
                <span className="mono grow truncate">{o.name}</span>
                {selectedNames.has(o.name) && !value.includes(ref) && <span className="badge warn">older version selected</span>}
                <span className="badge">{SCORER_LABELS[cls] ?? cls}</span>
                <Link to={objectUrl(projectId, o.name, o.digest)} target="_blank" className="small" onClick={(e) => e.stopPropagation()}>
                  view
                </Link>
              </label>
            )
          })}
        </div>
      )}
      <div className="row wrap">
        <button className="btn sm" onClick={() => setCreating(true)}>
          + {judgeOnlyCreate ? 'New LLM judge' : 'New scorer'}
        </button>
        {other.length > 0 && <span className="hint">{other.length} SDK-only scorer(s) hidden: the server can only run built-in scorer classes.</span>}
      </div>
      {creating && (
        <ScorerFormModal
          projectId={projectId}
          onlyJudge={judgeOnlyCreate}
          onClose={() => setCreating(false)}
          onCreated={(c) => {
            setCreating(false)
            onChange([...value.filter((r) => parseRef(r)?.name !== c.name), c.ref])
          }}
        />
      )}
    </div>
  )
}
