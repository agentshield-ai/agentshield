import { useState, type ReactNode } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { api, ApiError } from '../../api/client'
import { objectRef, parseRef } from '../../api/refs'
import type { Any, ObjSchema } from '../../api/types'
import { CopyButton, Empty, ErrorBox, Loading, Section, Spinner, Tabs } from '../../components/common'
import { RefChip } from '../../components/RefChip'
import { JsonBlock, ValueView } from '../../components/ValueView'
import { CallsMiniTable, EvalRunsTable } from '../../components/evals/EvalRunsTable'
import { objectRefPrefix, useEvalRuns } from '../../components/evals/evalUtils'
import { AliasEditor, CodeBlock, MessageList, TemplateText, TemplateVarChips, UsePanel } from '../../components/objects/bits'
import { DatasetRowsView } from '../../components/objects/DatasetRowsView'
import { ModelFormModal, PromptFormModal, ScorerFormModal, type Created } from '../../components/objects/ObjectForms'
import {
  isDataset,
  isEvaluation,
  isLLMModel,
  isModel,
  isPrompt,
  isScorer,
  isServerScorer,
  KIND_META,
  kindOf,
  objClass,
  refOf,
  SCORER_LABELS,
  useObjectVersions,
  valClass,
  type ServerScorerClass,
} from '../../components/objects/objectUtils'
import { useProjectId } from '../../hooks/useProject'
import { fmtBytes, fmtDate, fmtRelative, fmtScore, preview } from '../../lib/format'
import { contains, toQuery } from '../../lib/query'
import {
  callUrl,
  compareEvaluationsUrl,
  leaderboardUrl,
  monitorUrl,
  newEvaluationUrl,
  objectDiffUrl,
  objectUrl,
  objectVersionsUrl,
  playgroundUrl,
} from '../../lib/urls'

type Tab = 'overview' | 'use' | 'json'

export function ObjectVersionPage() {
  const projectId = useProjectId()
  const { objectId = '', digest = 'latest' } = useParams()
  const nav = useNavigate()
  const [tab, setTab] = useState<Tab>('overview')
  const [editing, setEditing] = useState(false)
  const obj = useQuery({
    queryKey: ['obj', projectId, objectId, digest],
    queryFn: () => api.objRead(projectId, objectId, digest).then((r) => r.obj),
    retry: (n, e) => !(e instanceof ApiError && e.status === 404) && n < 1,
  })
  const versions = useObjectVersions(projectId, objectId)

  if (obj.isLoading) return <Loading />
  if (obj.error || !obj.data) {
    const notFound = obj.error instanceof ApiError && obj.error.status === 404
    return (
      <div className="page">
        <div className="breadcrumbs">
          <Link to={`/${projectId}/objects`}>Objects</Link> <span>/</span> <span>{objectId}</span>
        </div>
        {notFound ? (
          <div className="card">
            <Empty>
              Version <span className="mono">{digest}</span> of <span className="mono">{objectId}</span> was not found.{' '}
              <Link to={objectVersionsUrl(projectId, objectId)}>See all versions</Link>
            </Empty>
          </div>
        ) : (
          <ErrorBox error={obj.error ?? 'Not found'} />
        )}
      </div>
    )
  }

  const o = obj.data
  const kind = kindOf(o)
  const ref = refOf(o)
  const prev = versions.data?.find((v) => v.version_index === o.version_index - 1)
  const onPublished = (c: Created) => {
    setEditing(false)
    nav(objectUrl(projectId, objectId, c.digest))
  }
  const editable = isPrompt(o) || isLLMModel(o) || isServerScorer(o)
  const useKind = kind === 'datasets' ? 'dataset' : kind === 'models' ? 'model' : kind === 'prompts' ? 'prompt' : kind === 'ops' ? 'op' : 'other'

  return (
    <div className="page">
      <div className="breadcrumbs">
        <Link to={`/${projectId}/${kind === 'all' ? 'objects' : kind}`}>{KIND_META[kind].title}</Link>
        <span>/</span>
        <Link to={objectVersionsUrl(projectId, objectId)}>{objectId}</Link>
        <span>/</span>
        <span>v{o.version_index}</span>
      </div>
      <div className="page-header">
        <h1 className="mono truncate" style={{ maxWidth: '100%' }}>
          {objectId}
        </h1>
        <span className="badge accent">v{o.version_index}</span>
        <span className="badge">{objClass(o)}</span>
        {o.is_latest && <span className="badge success">latest</span>}
        <span className="spacer" />
        <select
          className="input"
          value={o.digest}
          onChange={(e) => nav(objectUrl(projectId, objectId, e.target.value))}
          aria-label="Switch version"
          style={{ maxWidth: 220 }}
        >
          {(versions.data ?? [o]).map((v) => (
            <option key={v.digest} value={v.digest}>
              v{v.version_index} · {fmtRelative(v.created_at)}
              {v.aliases.filter((a) => a !== 'latest').length ? ` · ${v.aliases.filter((a) => a !== 'latest').join(', ')}` : ''}
            </option>
          ))}
        </select>
        {prev && (
          <Link className="btn" to={objectDiffUrl(projectId, objectId, prev.digest, o.digest)}>
            Diff vs v{prev.version_index}
          </Link>
        )}
        {editable && (
          <button className="btn primary" onClick={() => setEditing(true)}>
            Edit
          </button>
        )}
      </div>
      <div className="card">
        <div className="card-body col" style={{ gap: 6 }}>
          <div className="row wrap small" style={{ gap: 14 }}>
            <span className="muted">
              Created <b title={fmtDate(o.created_at)}>{fmtRelative(o.created_at)}</b>
            </span>
            <span className="muted">
              Size <b>{fmtBytes(o.size_bytes)}</b>
            </span>
            {o.wb_user_id && (
              <span className="muted">
                By <b>{o.wb_user_id}</b>
              </span>
            )}
            <span className="muted">
              Digest <span className="mono">{o.digest.slice(0, 16)}</span>
            </span>
          </div>
          <div className="row" style={{ minWidth: 0 }}>
            <code className="truncate grow faint" title={ref}>
              {ref}
            </code>
            <CopyButton text={ref} label="Copy ref" />
          </div>
          <div className="row wrap">
            <span className="small muted">Aliases</span>
            <AliasEditor projectId={projectId} objectId={objectId} digest={o.digest} aliases={o.aliases.filter((a) => a !== 'latest')} />
          </div>
        </div>
      </div>
      <Tabs<Tab>
        tabs={[
          { id: 'overview', label: 'Overview' },
          { id: 'use', label: 'Use' },
          { id: 'json', label: 'Raw JSON' },
        ]}
        value={tab}
        onChange={setTab}
      />
      {tab === 'overview' && <ClassView o={o} projectId={projectId} onPublished={onPublished} />}
      {tab === 'use' && (
        <Section title="Use this object">
          <UsePanel refUri={ref} name={objectId} version={`v${o.version_index}`} kind={useKind} />
        </Section>
      )}
      {tab === 'json' && <JsonBlock value={o.val} />}

      {editing && isPrompt(o) && <PromptFormModal projectId={projectId} initial={o} onClose={() => setEditing(false)} onCreated={onPublished} />}
      {editing && isLLMModel(o) && <ModelFormModal projectId={projectId} initial={o} onClose={() => setEditing(false)} onCreated={onPublished} />}
      {editing && isServerScorer(o) && <ScorerFormModal projectId={projectId} initial={o} onClose={() => setEditing(false)} onCreated={onPublished} />}
    </div>
  )
}

function ClassView({ o, projectId, onPublished }: { o: ObjSchema; projectId: string; onPublished: (c: Created) => void }) {
  if (o.kind === 'op') return <OpView o={o} projectId={projectId} />
  if (isDataset(o)) return <DatasetView o={o} projectId={projectId} onPublished={onPublished} />
  if (isPrompt(o)) return <PromptView o={o} projectId={projectId} />
  if (isEvaluation(o)) return <EvaluationView o={o} projectId={projectId} />
  if (isLLMModel(o)) return <LLMModelView o={o} projectId={projectId} />
  if (isModel(o)) return <GenericModelView o={o} projectId={projectId} />
  if (isScorer(o)) return <ScorerView o={o} projectId={projectId} />
  const cls = objClass(o)
  const link =
    cls === 'Leaderboard' || o.base_object_class === 'Leaderboard'
      ? { to: leaderboardUrl(projectId, o.object_id), label: 'Open leaderboard' }
      : cls === 'Monitor' || o.base_object_class === 'Monitor'
        ? { to: monitorUrl(projectId, o.object_id), label: 'Open monitor' }
        : null
  return (
    <Section title="Value" actions={link && <Link className="btn sm" to={link.to}>{link.label}</Link>}>
      <ValueView value={o.val} projectId={projectId} />
    </Section>
  )
}

function Description({ val }: { val: Any }) {
  if (!val?.description) return null
  return <p className="muted" style={{ margin: 0 }}>{val.description}</p>
}

// ------------------------------------------------------------------ dataset

function DatasetView({ o, projectId, onPublished }: { o: ObjSchema; projectId: string; onPublished: (c: Created) => void }) {
  return (
    <div className="col" style={{ gap: 12 }}>
      <Description val={o.val} />
      <DatasetRowsView projectId={projectId} obj={o} onPublished={onPublished} />
    </div>
  )
}

// ------------------------------------------------------------------ op

function OpView({ o, projectId }: { o: ObjSchema; projectId: string }) {
  const [allVersions, setAllVersions] = useState(false)
  const ref = objectRef(projectId, o.object_id, o.digest, 'op')
  const calls = useQuery({
    queryKey: ['op-calls', projectId, o.object_id, allVersions ? '*' : o.digest],
    queryFn: () =>
      api
        .callsQuery({
          project_id: projectId,
          filter: { op_names: [allVersions ? `weft:///${projectId}/op/${o.object_id}:*` : ref] },
          sort_by: [{ field: 'started_at', direction: 'desc' }],
          limit: 50,
        })
        .then((r) => r.calls),
  })
  const v = o.val ?? {}
  // Ops serialised by the official weave client keep their source in a file (files["obj.py"]).
  const fileDigest: string | undefined = !v.code && v.files && typeof v.files === 'object' ? (v.files['obj.py'] ?? Object.values(v.files)[0]) : undefined
  const fileCode = useQuery({
    queryKey: ['op-file', projectId, fileDigest],
    queryFn: () => api.fileContentText(projectId, fileDigest!),
    enabled: !!fileDigest,
    staleTime: Infinity,
  })
  const code: string | undefined = v.code ?? fileCode.data
  return (
    <div className="col" style={{ gap: 16 }}>
      {fileCode.isLoading && <Spinner label="Loading source…" />}
      {code ? (
        <CodeBlock
          code={code}
          language={v.language ?? 'python'}
          title={
            <span className="mono">
              {v.name ?? o.object_id}
              {v.signature ?? ''}
            </span>
          }
          maxHeight={560}
        />
      ) : (
        <Section title="Op">
          <div className="faint">No source code was captured for this op.</div>
          <ValueView value={v} projectId={projectId} />
        </Section>
      )}
      <Section
        title="Recent calls"
        actions={
          <label className="checkbox small">
            <input type="checkbox" checked={allVersions} onChange={(e) => setAllVersions(e.target.checked)} /> All versions
          </label>
        }
      >
        <ErrorBox error={calls.error} />
        {calls.isLoading ? (
          <Loading />
        ) : !calls.data?.length ? (
          <div className="faint">No calls of this op{allVersions ? '' : ' version'} yet.</div>
        ) : (
          <CallsMiniTable projectId={projectId} calls={calls.data} />
        )}
      </Section>
    </div>
  )
}

// ------------------------------------------------------------------ prompt

function PromptView({ o, projectId }: { o: ObjSchema; projectId: string }) {
  const v = o.val ?? {}
  const messages = Array.isArray(v.messages) ? v.messages : null
  const texts: string[] = messages ? messages.map((m: Any) => (typeof m.content === 'string' ? m.content : '')) : [String(v.content ?? '')]
  const ref = refOf(o)
  return (
    <div className="col" style={{ gap: 16 }}>
      <Description val={v} />
      <Section
        title={messages ? `Messages (${messages.length})` : 'Template'}
        actions={
          <Link className="btn sm" to={`${playgroundUrl(projectId)}?prompt=${encodeURIComponent(ref)}`}>
            Open in playground
          </Link>
        }
      >
        {messages ? (
          <MessageList messages={messages} />
        ) : typeof v.content === 'string' ? (
          <div className="msg">
            <div className="msg-body">
              <TemplateText text={v.content} />
            </div>
          </div>
        ) : (
          <ValueView value={v} projectId={projectId} />
        )}
      </Section>
      <Section title="Template variables">
        <TemplateVarChips texts={texts} />
      </Section>
    </div>
  )
}

// ------------------------------------------------------------------ evaluation

function EvaluationView({ o, projectId }: { o: ObjSchema; projectId: string }) {
  const nav = useNavigate()
  const v = o.val ?? {}
  const runs = useEvalRuns(projectId, { evalName: o.object_id, limit: 100 })
  const [sel, setSel] = useState<string[]>([])
  const scorers: string[] = Array.isArray(v.scorers) ? v.scorers : []
  return (
    <div className="col" style={{ gap: 16 }}>
      <Description val={v} />
      <div className="grid-2">
        <Section title="Dataset">
          {typeof v.dataset === 'string' ? <RefChip refStr={v.dataset} /> : <ValueView value={v.dataset} projectId={projectId} />}
          {v.trials != null && <div className="small muted" style={{ marginTop: 8 }}>Trials per row: {v.trials}</div>}
        </Section>
        <Section title={`Scorers (${scorers.length})`}>
          {scorers.length === 0 ? (
            <span className="faint">No scorers</span>
          ) : (
            <div className="chips">
              {scorers.map((s, i) => (typeof s === 'string' ? <RefChip key={i} refStr={s} /> : <span key={i} className="chip">{preview(s, 40)}</span>))}
            </div>
          )}
        </Section>
      </div>
      <Section
        title="Evaluation runs"
        actions={
          <>
            <button className="btn sm" disabled={sel.length < 1} onClick={() => nav(compareEvaluationsUrl(projectId, sel))}>
              Compare {sel.length ? `(${sel.length})` : ''}
            </button>
            <Link className="btn sm primary" to={`${newEvaluationUrl(projectId)}?evaluation=${encodeURIComponent(refOf(o))}`}>
              ▷ Run evaluation
            </Link>
          </>
        }
      >
        <ErrorBox error={runs.error} />
        {runs.isLoading ? (
          <Loading />
        ) : !runs.data?.length ? (
          <div className="faint">This evaluation has not been run yet.</div>
        ) : (
          <EvalRunsTable projectId={projectId} calls={runs.data} hideEval selected={sel} onSelect={setSel} maxMetrics={8} />
        )}
      </Section>
    </div>
  )
}

// ------------------------------------------------------------------ models

function ModelRuns({ o, projectId }: { o: ObjSchema; projectId: string }) {
  const nav = useNavigate()
  const runs = useEvalRuns(projectId, { modelName: o.object_id, limit: 50 })
  const [sel, setSel] = useState<string[]>([])
  return (
    <Section
      title="Evaluations of this model"
      actions={
        <>
          <button className="btn sm" disabled={sel.length < 1} onClick={() => nav(compareEvaluationsUrl(projectId, sel))}>
            Compare {sel.length ? `(${sel.length})` : ''}
          </button>
          {isLLMModel(o) && (
            <Link className="btn sm primary" to={`${newEvaluationUrl(projectId)}?model=${encodeURIComponent(refOf(o))}`}>
              ▷ Evaluate
            </Link>
          )}
        </>
      }
    >
      <ErrorBox error={runs.error} />
      {runs.isLoading ? (
        <Loading />
      ) : !runs.data?.length ? (
        <div className="faint">No evaluation runs use this model yet.</div>
      ) : (
        <EvalRunsTable projectId={projectId} calls={runs.data} hideModel selected={sel} onSelect={setSel} maxMetrics={8} />
      )}
    </Section>
  )
}

function KV({ items }: { items: [string, ReactNode][] }) {
  return (
    <div className="kv">
      {items.map(([k, v]) => (
        <div key={k} style={{ display: 'contents' }}>
          <div className="k">{k}</div>
          <div className="v">{v}</div>
        </div>
      ))}
    </div>
  )
}

function LLMModelView({ o, projectId }: { o: ObjSchema; projectId: string }) {
  const v = o.val ?? {}
  const p = v.default_params ?? {}
  const msgs = Array.isArray(p.messages_template) ? p.messages_template : []
  const { messages_template: _m, ...otherParams } = p
  return (
    <div className="col" style={{ gap: 16 }}>
      <Description val={v} />
      <div className="grid-2">
        <Section title="Configuration">
          <KV
            items={[
              ['llm_model_id', <span className="mono">{v.llm_model_id ?? '—'}</span>],
              ...Object.entries(otherParams).map(([k, x]) => [k, <span className="mono">{fmtScore(x)}</span>] as [string, ReactNode]),
            ]}
          />
        </Section>
        <Section title="Template variables">
          <TemplateVarChips texts={msgs.map((m: Any) => (typeof m.content === 'string' ? m.content : ''))} />
          <div className="hint" style={{ marginTop: 8 }}>
            Each variable is filled from the dataset row with the same column name.
          </div>
        </Section>
      </div>
      <Section title={`Messages template (${msgs.length})`}>
        <MessageList messages={msgs} />
      </Section>
      <ModelRuns o={o} projectId={projectId} />
    </div>
  )
}

function GenericModelView({ o, projectId }: { o: ObjSchema; projectId: string }) {
  return (
    <div className="col" style={{ gap: 16 }}>
      <Section title="Attributes">
        <ValueView value={o.val} projectId={projectId} />
      </Section>
      <ModelRuns o={o} projectId={projectId} />
    </div>
  )
}

// ------------------------------------------------------------------ scorers

function ScorerView({ o, projectId }: { o: ObjSchema; projectId: string }) {
  const v = o.val ?? {}
  const cls = valClass(v)
  const feedback = useQuery({
    queryKey: ['scorer-feedback', projectId, o.object_id],
    queryFn: () =>
      api
        .feedbackQuery({
          project_id: projectId,
          query: toQuery(contains('runnable_ref', objectRefPrefix(projectId, o.object_id), false)),
          sort_by: [{ field: 'created_at', direction: 'desc' }],
          limit: 30,
        })
        .then((r) => r.result),
  })
  const runs = useQuery({
    queryKey: ['scorer-calls', projectId, o.object_id],
    queryFn: () =>
      api
        .callsQuery({ project_id: projectId, filter: { op_names: [`${o.object_id}.score`] }, sort_by: [{ field: 'started_at', direction: 'desc' }], limit: 20 })
        .then((r) => r.calls),
  })
  return (
    <div className="col" style={{ gap: 16 }}>
      <Description val={v} />
      <Section title={cls && cls in SCORER_LABELS ? `${SCORER_LABELS[cls as ServerScorerClass]} scorer` : 'Scorer'}>
        {cls === 'LLMAsAJudgeScorer' ? (
          <div className="col" style={{ gap: 10 }}>
            <KV
              items={[
                ['model', typeof v.model === 'string' && parseRef(v.model) ? <RefChip refStr={v.model} /> : <span className="mono">{String(v.model ?? '—')}</span>],
                ['response_format', <span className="mono">{v.response_format ?? 'text'}</span>],
              ]}
            />
            <div className="msg">
              <div className="msg-role">scoring prompt</div>
              <div className="msg-body">
                <TemplateText text={String(v.scoring_prompt ?? '')} />
              </div>
            </div>
          </div>
        ) : (
          <ValueView value={v} projectId={projectId} />
        )}
        {!isServerScorer(o) && <div className="hint" style={{ marginTop: 8 }}>This scorer runs in the SDK; the server cannot run it for monitors or server-side evaluations.</div>}
      </Section>
      {!!feedback.data?.length && (
        <Section title="Recent scores (monitors & feedback)">
          <div className="table-wrap">
            <table className="table">
              <thead>
                <tr>
                  <th>Time</th>
                  <th>Target</th>
                  <th>Output</th>
                </tr>
              </thead>
              <tbody>
                {feedback.data.map((f) => {
                  const p = parseRef(f.weave_ref)
                  return (
                    <tr key={f.id}>
                      <td className="muted" title={fmtDate(f.created_at)}>
                        {fmtRelative(f.created_at)}
                      </td>
                      <td>{p?.kind === 'call' ? <Link to={callUrl(projectId, p.name)} className="mono">{p.name.slice(-8)}</Link> : <RefChip refStr={f.weave_ref} />}</td>
                      <td>
                        <span className="cell-preview mono small">{preview(f.payload?.output ?? f.payload, 160)}</span>
                      </td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        </Section>
      )}
      {!!runs.data?.length && (
        <Section title="Recent scorer calls">
          <CallsMiniTable projectId={projectId} calls={runs.data} />
        </Section>
      )}
    </div>
  )
}

