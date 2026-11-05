import { useMemo, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { useQueryClient } from '@tanstack/react-query'
import { api } from '../../api/client'
import type { WeftObject } from '../../api/types'
import { Empty, ErrorBox, Loading } from '../../components/common'
import { monitorNames } from '../../components/evals/monitorFields'
import { CreateDatasetModal } from '../../components/objects/CreateDatasetModal'
import { ModelFormModal, PromptFormModal, ScorerFormModal, type Created } from '../../components/objects/ObjectForms'
import { ConfirmButton } from '../../components/objects/bits'
import { KIND_META, objClass, useObjects, type ObjectPageKind } from '../../components/objects/objectUtils'
import { useProjectId } from '../../hooks/useProject'
import { fmtBytes, fmtDate, fmtRelative, preview } from '../../lib/format'
import { leaderboardUrl, monitorUrl, objectUrl, objectVersionsUrl } from '../../lib/urls'
import { HelpLink } from '../../components/ui/PageHeader'

const CREATE_LABEL: Partial<Record<ObjectPageKind, string>> = {
  datasets: 'New dataset',
  prompts: 'New prompt',
  models: 'New model',
  scorers: 'New scorer',
}

/** A short, class-specific description shown under the name. */
function detail(o: WeftObject): string {
  const v = o.value
  if (!v || typeof v !== 'object') return ''
  if (o.kind === 'function') return v.signature ? `${v.name ?? o.name}${v.signature}` : ''
  const cls = objClass(o)
  if (cls === 'LLMStructuredCompletionModel') return `${v.llm_model_id ?? ''} · T=${v.default_params?.temperature ?? '—'}`
  if (cls === 'LLMAsAJudgeScorer') return `judge · ${typeof v.model === 'string' ? v.model : 'model ref'}`
  if (cls === 'StringPrompt') return preview(v.content, 90)
  if (cls === 'MessagesPrompt') return `${(v.messages ?? []).length} messages · ${preview(v.messages?.[v.messages.length - 1]?.content, 60)}`
  if (cls === 'Evaluation') return `${(v.scorers ?? []).length} scorers`
  if (cls === 'Monitor') return `${v.active ? 'active' : 'paused'} · ${monitorNames(v).join(', ') || 'all root spans'}`
  if (cls === 'Leaderboard') return `${(v.columns ?? []).length} columns`
  if (v.target_column) return `target: ${v.target_column}`
  return typeof v.description === 'string' ? preview(v.description, 90) : ''
}

function primaryUrl(projectId: string, o: WeftObject) {
  const cls = objClass(o)
  if (cls === 'Leaderboard' || o.kind === 'leaderboard') return leaderboardUrl(projectId, o.name)
  if (cls === 'Monitor' || o.kind === 'monitor') return monitorUrl(projectId, o.name)
  return objectUrl(projectId, o.name, 'latest')
}

export function ObjectsPage({ kind }: { kind: ObjectPageKind }) {
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
      (o) => (!q || o.name.toLowerCase().includes(q) || objClass(o).toLowerCase().includes(q)) && (!cls || objClass(o) === cls),
    )
    const k = (o: WeftObject) => (sort.key === 'name' ? o.name.toLowerCase() : sort.key === 'versions' ? (o.version_count ?? 1) : o.created_at)
    return xs.slice().sort((a, b) => (k(a) < k(b) ? -sort.dir : k(a) > k(b) ? sort.dir : 0))
  }, [objs.data, search, cls, sort])

  const onCreated = (c: Created) => {
    setCreating(false)
    nav(objectUrl(projectId, c.name, c.digest))
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
          <HelpLink doc="evaluations.md#datasets-models-prompts-and-scorers" topic={meta.title} />
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
                <tr key={o.name} className="clickable" onClick={() => nav(primaryUrl(projectId, o))}>
                  <td style={{ maxWidth: 380 }}>
                    <Link to={primaryUrl(projectId, o)} onClick={(e) => e.stopPropagation()} style={{ fontWeight: 500 }}>
                      {o.name}
                    </Link>
                    {detail(o) && <div className="faint small cell-preview">{detail(o)}</div>}
                  </td>
                  <td>
                    <span className="badge">{objClass(o)}</span>
                  </td>
                  <td className="num">
                    <Link to={objectVersionsUrl(projectId, o.name)} onClick={(e) => e.stopPropagation()}>
                      {o.version_count ?? o.version + 1}
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
                      title={`Delete ${o.name}?`}
                      body={
                        <>
                          This deletes all {o.version_count ?? ''} versions of <b className="mono">{o.name}</b>. Refs to it from calls and other objects
                          will no longer resolve.
                        </>
                      }
                      onConfirm={async () => {
                        await api.objectDelete(projectId, o.name)
                        await qc.invalidateQueries({ queryKey: ['objects', projectId] })
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

const EMPTY_TEXT: Record<ObjectPageKind, string> = {
  datasets: 'No datasets yet. Upload a CSV or JSON file, or save one from the SDK with weft.save(Dataset(...)).',
  models: 'No models yet. Create an LLM completion model the server can run, or save a Model subclass from the SDK.',
  prompts: 'No prompts yet. Create a chat or string prompt template with {placeholders}.',
  scorers: 'No scorers yet. Create an LLM judge or a rule-based scorer to grade outputs.',
  ops: 'No functions yet. Decorate a function with @weft.trace and call it to record its code and every run.',
  all: 'No objects yet. Objects you save from the SDK or the UI appear here.',
}

function EmptyState({ kind, onCreate }: { kind: ObjectPageKind; onCreate?: () => void }) {
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
