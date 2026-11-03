import { useMemo, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { useQueryClient } from '@tanstack/react-query'
import { api } from '../../api/client'
import type { ObjSchema } from '../../api/types'
import { Empty, ErrorBox, Loading } from '../../components/common'
import { CreateDatasetModal } from '../../components/objects/CreateDatasetModal'
import { ModelFormModal, PromptFormModal, ScorerFormModal, type Created } from '../../components/objects/ObjectForms'
import { ConfirmButton } from '../../components/objects/bits'
import { KIND_META, objClass, useObjects, type ObjectKind } from '../../components/objects/objectUtils'
import { useProjectId } from '../../hooks/useProject'
import { fmtBytes, fmtDate, fmtRelative, preview } from '../../lib/format'
import { leaderboardUrl, monitorUrl, objectUrl, objectVersionsUrl } from '../../lib/urls'

const CREATE_LABEL: Partial<Record<ObjectKind, string>> = {
  datasets: 'New dataset',
  prompts: 'New prompt',
  models: 'New model',
  scorers: 'New scorer',
}

/** A short, class-specific description shown under the name. */
function detail(o: ObjSchema): string {
  const v = o.val
  if (!v || typeof v !== 'object') return ''
  if (o.kind === 'op') return v.signature ? `${v.name ?? o.object_id}${v.signature}` : ''
  const cls = objClass(o)
  if (cls === 'LLMStructuredCompletionModel') return `${v.llm_model_id ?? ''} · T=${v.default_params?.temperature ?? '—'}`
  if (cls === 'LLMAsAJudgeScorer') return `judge · ${typeof v.model === 'string' ? v.model : 'model ref'}`
  if (cls === 'StringPrompt') return preview(v.content, 90)
  if (cls === 'MessagesPrompt') return `${(v.messages ?? []).length} messages · ${preview(v.messages?.[v.messages.length - 1]?.content, 60)}`
  if (cls === 'Evaluation') return `${(v.scorers ?? []).length} scorers`
  if (cls === 'Monitor') return `${v.active ? 'active' : 'paused'} · ${(v.op_names ?? []).join(', ') || 'all root calls'}`
  if (cls === 'Leaderboard') return `${(v.columns ?? []).length} columns`
  if (v.target_column) return `target: ${v.target_column}`
  return typeof v.description === 'string' ? preview(v.description, 90) : ''
}

function primaryUrl(projectId: string, o: ObjSchema) {
  const cls = objClass(o)
  if (cls === 'Leaderboard' || o.base_object_class === 'Leaderboard') return leaderboardUrl(projectId, o.object_id)
  if (cls === 'Monitor' || o.base_object_class === 'Monitor') return monitorUrl(projectId, o.object_id)
  return objectUrl(projectId, o.object_id, 'latest')
}

export function ObjectsPage({ kind }: { kind: ObjectKind }) {
  const projectId = useProjectId()
  const nav = useNavigate()
  const qc = useQueryClient()
  const meta = KIND_META[kind]
  const objs = useObjects(projectId, meta.filter)
  const [search, setSearch] = useState('')
  const [cls, setCls] = useState('')
  const [creating, setCreating] = useState(false)
  const [sort, setSort] = useState<{ key: 'name' | 'updated' | 'versions'; dir: 1 | -1 }>({ key: 'updated', dir: -1 })

  const classes = useMemo(() => [...new Set((objs.data ?? []).map(objClass))].sort(), [objs.data])
  const rows = useMemo(() => {
    const q = search.trim().toLowerCase()
    const xs = (objs.data ?? []).filter(
      (o) => (!q || o.object_id.toLowerCase().includes(q) || objClass(o).toLowerCase().includes(q)) && (!cls || objClass(o) === cls),
    )
    const k = (o: ObjSchema) => (sort.key === 'name' ? o.object_id.toLowerCase() : sort.key === 'versions' ? (o.version_count ?? 1) : o.created_at)
    return xs.slice().sort((a, b) => (k(a) < k(b) ? -sort.dir : k(a) > k(b) ? sort.dir : 0))
  }, [objs.data, search, cls, sort])

  const onCreated = (c: Created) => {
    setCreating(false)
    nav(objectUrl(projectId, c.objectId, c.digest))
  }
  const th = (key: typeof sort.key, label: string, className = '') => (
    <th className={`sortable ${className}`} onClick={() => setSort((s) => ({ key, dir: s.key === key ? (-s.dir as 1 | -1) : key === 'name' ? 1 : -1 }))}>
      {label} {sort.key === key ? (sort.dir === 1 ? '▲' : '▼') : ''}
    </th>
  )

  return (
    <div className="page">
      <div className="page-header">
        <div className="col" style={{ gap: 2 }}>
          <h1>{meta.title}</h1>
          <span className="muted small">{meta.blurb}</span>
        </div>
        <span className="spacer" />
        <input
          className="input"
          style={{ width: 220, maxWidth: '100%' }}
          aria-label={`Search ${meta.title.toLowerCase()}`}
          placeholder={`Search ${meta.title.toLowerCase()}…`}
          value={search} onChange={(e) => setSearch(e.target.value)} />
        {(kind === 'all' || classes.length > 1) && (
          <select className="input" aria-label="Show only objects of this class" title="Object class" value={cls} onChange={(e) => setCls(e.target.value)}>
            <option value="">All classes</option>
            {classes.map((c) => (
              <option key={c}>{c}</option>
            ))}
          </select>
        )}
        {CREATE_LABEL[kind] && (
          <button className="btn primary" onClick={() => setCreating(true)}>
            + {CREATE_LABEL[kind]}
          </button>
        )}
      </div>
      <ErrorBox error={objs.error} />
      {objs.isLoading ? (
        <Loading />
      ) : (objs.data ?? []).length === 0 ? (
        <EmptyState kind={kind} onCreate={CREATE_LABEL[kind] ? () => setCreating(true) : undefined} />
      ) : (
        <div className="table-wrap">
          <table className="table">
            <thead>
              <tr>
                {th('name', 'Name')}
                <th>Class</th>
                {th('versions', 'Versions', 'num')}
                {th('updated', 'Last updated')}
                <th>Aliases</th>
                <th className="num">Size</th>
                <th style={{ width: 40 }} />
              </tr>
            </thead>
            <tbody>
              {rows.map((o) => (
                <tr key={o.object_id} className="clickable" onClick={() => nav(primaryUrl(projectId, o))}>
                  <td style={{ maxWidth: 380 }}>
                    <Link to={primaryUrl(projectId, o)} onClick={(e) => e.stopPropagation()} style={{ fontWeight: 500 }}>
                      {o.object_id}
                    </Link>
                    {detail(o) && <div className="faint small cell-preview">{detail(o)}</div>}
                  </td>
                  <td>
                    <span className="badge">{objClass(o)}</span>
                  </td>
                  <td className="num">
                    <Link to={objectVersionsUrl(projectId, o.object_id)} onClick={(e) => e.stopPropagation()}>
                      {o.version_count ?? o.version_index + 1}
                    </Link>
                  </td>
                  <td title={fmtDate(o.created_at)} className="muted">
                    {fmtRelative(o.created_at)}
                  </td>
                  <td>
                    <span className="chips">
                      {o.aliases
                        .filter((a) => a !== 'latest')
                        .map((a) => (
                          <span key={a} className="chip on">
                            {a}
                          </span>
                        ))}
                    </span>
                  </td>
                  <td className="num muted">{fmtBytes(o.size_bytes)}</td>
                  <td onClick={(e) => e.stopPropagation()}>
                    <ConfirmButton
                      className="btn ghost sm icon danger"
                      title={`Delete ${o.object_id}?`}
                      body={
                        <>
                          This deletes all {o.version_count ?? ''} versions of <b className="mono">{o.object_id}</b>. Refs to it from calls and other objects
                          will no longer resolve.
                        </>
                      }
                      onConfirm={async () => {
                        await api.objDelete(projectId, o.object_id)
                        await qc.invalidateQueries({ queryKey: ['objs', projectId] })
                      }}
                    >
                      ✕
                    </ConfirmButton>
                  </td>
                </tr>
              ))}
              {rows.length === 0 && (
                <tr>
                  <td colSpan={7} className="faint" style={{ textAlign: 'center', padding: 24 }}>
                    No {meta.title.toLowerCase()} match “{search}”
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
      )}
      {creating && kind === 'datasets' && <CreateDatasetModal projectId={projectId} onClose={() => setCreating(false)} onCreated={onCreated} />}
      {creating && kind === 'prompts' && <PromptFormModal projectId={projectId} onClose={() => setCreating(false)} onCreated={onCreated} />}
      {creating && kind === 'models' && <ModelFormModal projectId={projectId} onClose={() => setCreating(false)} onCreated={onCreated} />}
      {creating && kind === 'scorers' && <ScorerFormModal projectId={projectId} onClose={() => setCreating(false)} onCreated={onCreated} />}
    </div>
  )
}

const EMPTY_TEXT: Record<ObjectKind, string> = {
  datasets: 'No datasets yet. Upload a CSV or JSON file, or publish one from the SDK with weft.publish(Dataset(...)).',
  models: 'No models yet. Create an LLM completion model the server can run, or publish a Model subclass from the SDK.',
  prompts: 'No prompts yet. Create a chat or string prompt template with {placeholders}.',
  scorers: 'No scorers yet. Create an LLM judge or a rule-based scorer to grade outputs.',
  ops: 'No functions yet. Decorate a function with @weft.op and call it to record its code and every run.',
  all: 'No objects yet. Objects you publish from the SDK or the UI appear here.',
}

function EmptyState({ kind, onCreate }: { kind: ObjectKind; onCreate?: () => void }) {
  return (
    <div className="card">
      <Empty>
        <div className="col" style={{ alignItems: 'center', gap: 10 }}>
          <div style={{ maxWidth: 460 }}>{EMPTY_TEXT[kind]}</div>
          {onCreate && (
            <button className="btn primary" onClick={onCreate}>
              + {CREATE_LABEL[kind]}
            </button>
          )}
        </div>
      </Empty>
    </div>
  )
}
