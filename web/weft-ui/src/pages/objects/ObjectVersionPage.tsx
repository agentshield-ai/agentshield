// One version of an asset (DESIGN.md §7, §7.1 rules 1, 2 and 4, §7.2): a
// sentence saying which version this is and what changed ("Version 3 of
// support-tickets, saved 2 days ago. It has 50 rows, 4 more than version
// 2."), then the value shown readably: rows as a table, prompts as prose,
// functions as code. The name to use in code, aliases and tags live in the
// inspector beside it; the ref and digest are in the Raw view with the stored
// JSON. The views (?tab=) are Contents, Use (SDK snippets) and Raw.
import { useMemo, useState, type ReactNode } from 'react'
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { api, ApiError } from '../../api/client'
import { objectRef, parseRef } from '../../api/refs'
import type { Any, WeftObject } from '../../api/types'
import { ErrorBox, Loading, Spinner } from '../../components/common'
import { RefChip } from '../../components/RefChip'
import { JsonBlock, ValueView } from '../../components/ValueView'
import { EvalRunsList, SpanRunsList } from '../../components/evals/EvalRunsTable'
import { useAgentEvalRuns } from '../../components/agents/AgentEvals'
import { objectRefPrefix, useEvalRuns } from '../../components/evals/evalUtils'
import { AliasEditor, CodeBlock, ShortCopy, TemplateText, UsePanel } from '../../components/objects/bits'
import { DatasetRowsView } from '../../components/objects/DatasetRowsView'
import { DatasetSources } from '../../components/objects/DatasetSources'
import { TagEditor } from '../../components/objects/TagEditor'
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
  rowsDigest,
  SCORER_LABELS,
  useObjectVersions,
  useTableCounts,
  valClass,
  type ServerScorerClass,
} from '../../components/objects/objectUtils'
import { CopyText, Inspector, InspectorDivider, InspectorLinks, KeyValueList } from '../../components/ui/Inspector'
import { PageHeader } from '../../components/ui/PageHeader'
import { Segmented, segmentedTabId } from '../../components/ui/Segmented'
import { Sentence, type SentencePart } from '../../components/ui/Sentence'
import { useProjectId } from '../../hooks/useProject'
import { fmtBytes, fmtDate, fmtRelative, fmtScore, preview } from '../../lib/format'
import { contains, toQuery } from '../../lib/query'
import { relativeTime } from '../../lib/summaries'
import { kindWords, runAnswer, runTitle, scoreReason, scoreResultWords, sdkName, versionSentence, versionWords, type AssetPart } from '../../lib/summaries/assets'
import { modelDisplayName } from '../../lib/modelWords'
import { GroupedList, ListRow } from '../../components/ui/GroupedList'
import { compareEvaluationsUrl, leaderboardUrl, monitorUrl, newEvaluationUrl, objectDiffUrl, objectUrl, objectVersionsUrl, playgroundUrl, spanUrl } from '../../lib/urls'
import './assets.css'

type Tab = 'contents' | 'use' | 'raw'
const TABS: { id: Tab; label: string; title: string }[] = [
  { id: 'contents', label: 'Contents', title: 'What this version holds' },
  { id: 'use', label: 'Use', title: 'Load this version from code' },
  { id: 'raw', label: 'Raw', title: 'The stored value as JSON' },
]
/** Earlier tab names. */
const LEGACY_TAB: Record<string, Tab> = { overview: 'contents', json: 'raw' }

const listUrl = (projectId: string, kind: ReturnType<typeof kindOf>) => `/${projectId}/${kind === 'all' ? 'objects' : kind === 'ops' ? 'functions' : kind}`

export function ObjectVersionPage() {
  const projectId = useProjectId()
  const { objectId = '', digest = 'latest' } = useParams()
  const nav = useNavigate()
  const [params, setParams] = useSearchParams()
  const rawTab = params.get('tab') ?? ''
  const tab: Tab = LEGACY_TAB[rawTab] ?? (TABS.some((t) => t.id === rawTab) ? (rawTab as Tab) : 'contents')
  const setTab = (t: Tab) => {
    const n = new URLSearchParams(params)
    if (t === 'contents') n.delete('tab')
    else n.set('tab', t)
    setParams(n, { replace: true })
  }
  const [editing, setEditing] = useState(false)
  const obj = useQuery({
    queryKey: ['object', projectId, objectId, digest],
    queryFn: () => api.objectGet(projectId, objectId, digest),
    retry: (n, e) => !(e instanceof ApiError && e.status === 404) && n < 1,
  })
  const versions = useObjectVersions(projectId, objectId)
  const o = obj.data
  const prev = o ? versions.data?.find((v) => v.version === o.version - 1) : undefined
  // The previous version's rows, to say how many rows this version added (it is metadata only, so read it).
  const prevObj = useQuery({
    queryKey: ['object', projectId, objectId, prev?.digest],
    queryFn: () => api.objectGet(projectId, objectId, prev!.digest),
    enabled: !!prev && !!o && isDataset(o),
  })
  const digests = [o && isDataset(o) ? rowsDigest(o.value) : null, prevObj.data ? rowsDigest(prevObj.data.value) : null].filter(Boolean) as string[]
  const counts = useTableCounts(projectId, digests)

  if (obj.isLoading) return <Loading />
  if (obj.error || !o) {
    const notFound = obj.error instanceof ApiError && obj.error.status === 404
    return (
      <div className="page-frame detail">
        <PageHeader variant="detail" back={{ to: `/${projectId}/objects`, label: 'All objects' }} title={objectId} help="evaluations.md#datasets-models-prompts-and-scorers" helpTopic="Assets" />
        {notFound ? (
          <div className="quiet-empty">
            <p>This version of {objectId} was not found. It may have been deleted.</p>
            <Link to={objectVersionsUrl(projectId, objectId)}>See every version of {objectId}</Link>
          </div>
        ) : (
          <ErrorBox error={obj.error ?? 'Not found'} />
        )}
      </div>
    )
  }

  const kind = kindOf(o)
  const ref = refOf(o)
  const latest = versions.data?.[0]
  const rowsDigestNow = isDataset(o) ? rowsDigest(o.value) : null
  const rows = rowsDigestNow ? (counts.data?.get(rowsDigestNow) ?? null) : null
  const prevRowsDigest = prevObj.data ? rowsDigest(prevObj.data.value) : null
  const prevRows = prevRowsDigest ? (counts.data?.get(prevRowsDigest) ?? null) : null
  const onPublished = (c: Created) => {
    setEditing(false)
    nav(objectUrl(projectId, objectId, c.digest))
  }
  const editable = isPrompt(o) || isLLMModel(o) || isServerScorer(o)
  const useKind = kind === 'datasets' ? 'dataset' : kind === 'models' ? 'model' : kind === 'prompts' ? 'prompt' : kind === 'ops' ? 'function' : 'other'
  const parts: SentencePart[] = versionSentence({
    o,
    latestVersion: latest?.version ?? null,
    rows,
    prevRows: prev ? prevRows : null,
  }).map((p: AssetPart) => (typeof p === 'string' ? p : { text: p.text, to: objectUrl(projectId, objectId, 'latest'), title: 'Open the latest version' }))
  const versionCount = versions.data?.length ?? null
  const cls = objClass(o)
  const ownPage =
    cls === 'Leaderboard' || o.kind === 'leaderboard'
      ? { to: leaderboardUrl(projectId, o.name), label: 'Open the leaderboard' }
      : cls === 'Monitor' || o.kind === 'monitor'
        ? { to: monitorUrl(projectId, o.name), label: 'Open the monitor' }
        : null
  const aliases = o.aliases.filter((a) => a !== 'latest')

  return (
    <div className="page-frame detail">
      <PageHeader
        variant="detail"
        back={{ to: listUrl(projectId, kind), label: KIND_META[kind].title }}
        title={o.name}
        help="evaluations.md#datasets-models-prompts-and-scorers"
        helpTopic="Assets"
        sentence={<Sentence parts={parts} size="detail" />}
        meta={
          <>
            {capFirst(kindWords(o))}
            {versionCount != null && (
              <>
                {' · '}
                <Link to={objectVersionsUrl(projectId, objectId)}>{versionCount === 1 ? 'one version' : `${versionCount} versions`}</Link>
              </>
            )}
            {prev && (
              <>
                {' · '}
                <Link to={objectDiffUrl(projectId, objectId, prev.digest, o.digest)}>compare with {versionWords(prev)}</Link>
              </>
            )}
          </>
        }
      />
      <div className="row wrap" style={{ gap: 10 }}>
        <Segmented label="Views of this version" role="tabs" panelId="asset-view" value={tab} onChange={setTab} options={TABS} className="asset-tabs" />
        {editable && (
          <button type="button" className="qbtn" onClick={() => setEditing(true)}>
            Edit
          </button>
        )}
      </div>
      <div className="asset-layout">
        <div className="asset-main" role="tabpanel" id="asset-view" aria-labelledby={segmentedTabId('asset-view', tab)}>
          {tab === 'contents' && <ClassView o={o} projectId={projectId} onPublished={onPublished} />}
          {tab === 'use' && (
            <Block title="Load this version from code">
              <UsePanel refUri={ref} name={objectId} version={`v${o.version}`} kind={useKind} />
            </Block>
          )}
          {tab === 'raw' && (
            <>
              <div className="asset-panel">
                <KeyValueList
                  items={[
                    { label: 'Ref', value: <ShortCopy text={ref} label="ref" />, mono: true, help: 'Ref: the address other assets and spans use to point at this version' },
                    { label: 'Digest', value: <ShortCopy text={o.digest} label="digest" />, mono: true, help: 'Digest: the SHA-256 of the stored value; it names this exact version' },
                  ]}
                />
              </div>
              <JsonBlock value={o.value} />
            </>
          )}
        </div>
        <Inspector title={capFirst(versionWords(o))} subtitle={o.is_latest ? 'The latest version' : latest ? `The latest is ${versionWords(latest)}` : undefined} label="About this version" sticky>
          <KeyValueList
            items={[
              { label: 'Saved', value: <span title={fmtDate(o.created_at)}>{relativeTime(o.created_at)}</span> },
              o.user_id ? { label: 'By', value: o.user_id } : null,
              { label: 'Size', value: fmtBytes(o.size_bytes) },
              { label: 'In code', value: <CopyText text={sdkName(o)} label="name" />, mono: true, help: 'The name and version to pass to weft.get()' },
            ]}
          />
          <InspectorDivider />
          <label className="field-stack">
            Show another version
            <select className="input insp-select" value={o.digest} onChange={(e) => nav(objectUrl(projectId, objectId, e.target.value))}>
              {(versions.data ?? [o]).map((v) => (
                <option key={v.digest} value={v.digest}>
                  {capFirst(versionWords(v))} · {relativeTime(v.created_at)}
                  {v.aliases.filter((a) => a !== 'latest').length ? ` · ${v.aliases.filter((a) => a !== 'latest').join(', ')}` : ''}
                </option>
              ))}
            </select>
          </label>
          <div className="insp-chips">
            <span className="field-stack">Aliases{aliases.length ? '' : ' (names such as “prod” that point to one version)'}</span>
            <AliasEditor projectId={projectId} objectId={objectId} digest={o.digest} aliases={aliases} />
          </div>
          <div className="insp-chips">
            <span className="field-stack">Tags</span>
            <TagEditor projectId={projectId} objectId={objectId} digest={o.digest} tags={o.tags ?? []} />
          </div>
          {(ownPage || isPrompt(o) || isEvaluation(o) || isLLMModel(o) || (latest && latest.digest !== o.digest)) && (
            <>
              <InspectorDivider />
              <InspectorLinks>
                {ownPage && <Link to={ownPage.to}>{ownPage.label}</Link>}
                {isPrompt(o) && <Link to={`${playgroundUrl(projectId)}?prompt=${encodeURIComponent(ref)}`}>Try it in the playground</Link>}
                {isEvaluation(o) && <Link to={`${newEvaluationUrl(projectId)}?evaluation=${encodeURIComponent(ref)}`}>Run this evaluation</Link>}
                {isLLMModel(o) && <Link to={`${newEvaluationUrl(projectId)}?model=${encodeURIComponent(ref)}`}>Evaluate this model</Link>}
                {latest && latest.digest !== o.digest && <Link to={objectDiffUrl(projectId, objectId, o.digest, latest.digest)}>Compare with the latest</Link>}
              </InspectorLinks>
            </>
          )}
        </Inspector>
      </div>

      {editing && isPrompt(o) && <PromptFormModal projectId={projectId} initial={o} onClose={() => setEditing(false)} onCreated={onPublished} />}
      {editing && isLLMModel(o) && <ModelFormModal projectId={projectId} initial={o} onClose={() => setEditing(false)} onCreated={onPublished} />}
      {editing && isServerScorer(o) && <ScorerFormModal projectId={projectId} initial={o} onClose={() => setEditing(false)} onCreated={onPublished} />}
    </div>
  )
}

const capFirst = (s: string) => (s ? s[0].toUpperCase() + s.slice(1) : s)

/** A titled block of the main column. */
function Block({ title, count, note, actions, children }: { title?: ReactNode; count?: number | null; note?: ReactNode; actions?: ReactNode; children: ReactNode }) {
  return (
    <section className="asset-section">
      {(title || actions) && (
        <div className="asset-section-head">
          {title && (
            <h2>
              {title}
              {count != null && <span className="glist-count"> {count.toLocaleString()}</span>}
              {note && <span className="quiet-note" style={{ display: 'inline', marginLeft: 8 }}>{note}</span>}
            </h2>
          )}
          {actions && <div className="row wrap">{actions}</div>}
        </div>
      )}
      {children}
    </section>
  )
}

function ClassView({ o, projectId, onPublished }: { o: WeftObject; projectId: string; onPublished: (c: Created) => void }) {
  if (o.kind === 'function') return <OpView o={o} projectId={projectId} />
  if (isDataset(o)) return <DatasetView o={o} projectId={projectId} onPublished={onPublished} />
  if (isPrompt(o)) return <PromptView o={o} projectId={projectId} />
  if (isEvaluation(o)) return <EvaluationView o={o} projectId={projectId} />
  if (isLLMModel(o)) return <LLMModelView o={o} projectId={projectId} />
  if (isModel(o)) return <GenericModelView o={o} projectId={projectId} />
  if (isScorer(o)) return <ScorerView o={o} projectId={projectId} />
  return (
    <Block>
      <div className="asset-panel">
        <ValueView value={o.value} projectId={projectId} />
      </div>
    </Block>
  )
}

function Description({ val }: { val: Any }) {
  if (!val?.description) return null
  return <p className="asset-desc">{val.description}</p>
}

// ------------------------------------------------------------------ dataset

function DatasetView({ o, projectId, onPublished }: { o: WeftObject; projectId: string; onPublished: (c: Created) => void }) {
  return (
    <>
      <Description val={o.value} />
      <Block title="Rows">
        <DatasetRowsView projectId={projectId} obj={o} onPublished={onPublished} />
      </Block>
      <DatasetSources projectId={projectId} objectId={o.name} />
    </>
  )
}

// ------------------------------------------------------------------ function

function OpView({ o, projectId }: { o: WeftObject; projectId: string }) {
  const [allVersions, setAllVersions] = useState(false)
  const ref = objectRef(projectId, o.name, o.digest, 'function')
  const calls = useQuery({
    queryKey: ['function-spans', projectId, o.name, allVersions ? '*' : o.digest],
    queryFn: () =>
      api
        .spansQuery({
          project_id: projectId,
          filter: { names: [allVersions ? `weft:///${projectId}/function/${o.name}:*` : ref] },
          sort_by: [{ field: 'started_at', direction: 'desc' }],
          limit: 50,
        })
        .then((r) => r.items),
  })
  const v = o.value ?? {}
  // Some functions keep their source in a file (files["obj.py"]) instead of val.code.
  const fileDigest: string | undefined = !v.code && v.files && typeof v.files === 'object' ? (v.files['obj.py'] ?? Object.values(v.files)[0]) : undefined
  const fileCode = useQuery({
    queryKey: ['op-file', projectId, fileDigest],
    queryFn: () => api.fileText(projectId, fileDigest!),
    enabled: !!fileDigest,
    staleTime: Infinity,
  })
  const code: string | undefined = v.code ?? fileCode.data
  return (
    <>
      {fileCode.isLoading && <Spinner label="Loading the source…" />}
      {code ? (
        <div className="asset-code">
          <CodeBlock code={code} language={v.language ?? 'python'} title={<span className="mono">{`${v.name ?? o.name}${v.signature ?? ''}`}</span>} maxHeight={560} />
        </div>
      ) : (
        <Block title="Function">
          <p className="quiet-note">No source code was recorded for this function.</p>
          <div className="asset-panel">
            <ValueView value={v} projectId={projectId} />
          </div>
        </Block>
      )}
      <Block
        title={calls.data && calls.data.length >= 50 ? 'The latest 50 runs' : 'Recent runs'}
        count={calls.data && calls.data.length < 50 ? calls.data.length : null}
        actions={
          <label className="checkbox small">
            <input type="checkbox" checked={allVersions} onChange={(e) => setAllVersions(e.target.checked)} /> Every version
          </label>
        }
      >
        <ErrorBox error={calls.error} />
        {calls.isLoading ? (
          <Loading />
        ) : !calls.data?.length ? (
          <p className="quiet-note">No runs of this {allVersions ? 'function' : 'version'} yet.</p>
        ) : (
          <SpanRunsList projectId={projectId} calls={calls.data} label={`Runs of ${o.name}`} />
        )}
      </Block>
    </>
  )
}

// ------------------------------------------------------------------ prompt

function contentText(c: Any): string {
  if (typeof c === 'string') return c
  if (Array.isArray(c)) return c.map((p) => (typeof p === 'string' ? p : p?.type === 'text' ? p.text : p?.type === 'image_url' ? '[image]' : JSON.stringify(p))).join('\n')
  return JSON.stringify(c, null, 2)
}

const ROLE: Record<string, string> = { system: 'System', user: 'User', assistant: 'Assistant', tool: 'Tool', developer: 'Developer' }

/** Chat messages as prose: the role in quiet words, the text with its {placeholders} marked. */
export function ProseMessages({ messages }: { messages: Any[] }) {
  if (!messages.length) return <p className="quiet-note">No messages.</p>
  return (
    <div className="prose-messages">
      {messages.map((m, i) => (
        <div key={i} className="prose-msg">
          <span className="prose-role">{ROLE[m?.role] ?? m?.role ?? 'Message'}</span>
          <p className="prose-text">
            <TemplateText text={contentText(m?.content)} />
          </p>
        </div>
      ))}
    </div>
  )
}

function PromptView({ o, projectId }: { o: WeftObject; projectId: string }) {
  const v = o.value ?? {}
  const messages = Array.isArray(v.messages) ? v.messages : null
  return (
    <>
      <Description val={v} />
      {messages ? (
        <ProseMessages messages={messages} />
      ) : typeof v.content === 'string' ? (
        <div className="prose-messages">
          <div className="prose-msg">
            <p className="prose-text">
              <TemplateText text={v.content} />
            </p>
          </div>
        </div>
      ) : (
        <div className="asset-panel">
          <ValueView value={v} projectId={projectId} />
        </div>
      )}
    </>
  )
}

// ------------------------------------------------------------------ evaluation

function EvaluationView({ o, projectId }: { o: WeftObject; projectId: string }) {
  const nav = useNavigate()
  const v = o.value ?? {}
  const runs = useEvalRuns(projectId, { evalName: o.name, limit: 100 })
  const runIds = useMemo(() => (runs.data ?? []).map((c) => c.id), [runs.data])
  const agentRuns = useAgentEvalRuns(projectId, runIds)
  const [sel, setSel] = useState<string[]>([])
  const scorers: Any[] = Array.isArray(v.scorers) ? v.scorers : []
  return (
    <>
      <Description val={v} />
      <div className="asset-panel">
        <KeyValueList
          items={[
            { label: 'Dataset', value: typeof v.dataset === 'string' ? <RefChip refStr={v.dataset} /> : <ValueView value={v.dataset} projectId={projectId} /> },
            {
              label: 'Scorers',
              value: scorers.length ? (
                <span className="row wrap" style={{ gap: 4 }}>
                  {scorers.map((s, i) => (
                    <span key={i}>
                      {typeof s === 'string' ? <RefChip refStr={s} /> : preview(s, 40)}
                      {i < scorers.length - 1 ? ',' : ''}
                    </span>
                  ))}
                </span>
              ) : (
                'No scorers'
              ),
            },
            v.trials != null ? { label: 'Trials', value: `${v.trials} per row`, help: 'How many times each row is run' } : null,
          ]}
        />
      </div>
      <Block
        title="Runs"
        count={runs.data?.length}
        actions={
          <button type="button" className="qbtn sm" disabled={sel.length < 1} onClick={() => nav(compareEvaluationsUrl(projectId, sel))}>
            Compare{sel.length ? ` ${sel.length}` : ''}
          </button>
        }
      >
        <ErrorBox error={runs.error} />
        {runs.isLoading ? (
          <Loading />
        ) : !runs.data?.length ? (
          <p className="quiet-note">This evaluation has not been run yet.</p>
        ) : (
          <EvalRunsList projectId={projectId} calls={runs.data} by="model" label={`Runs of ${o.name}`} selected={sel} onSelect={setSel} agentRuns={agentRuns.data} />
        )}
      </Block>
    </>
  )
}

// ------------------------------------------------------------------ models

function ModelRuns({ o, projectId }: { o: WeftObject; projectId: string }) {
  const nav = useNavigate()
  const runs = useEvalRuns(projectId, { modelName: o.name, limit: 50 })
  const runIds = useMemo(() => (runs.data ?? []).map((c) => c.id), [runs.data])
  const agentRuns = useAgentEvalRuns(projectId, runIds)
  const [sel, setSel] = useState<string[]>([])
  return (
    <Block
      title="Evaluations of this model"
      count={runs.data?.length}
      actions={
        <button type="button" className="qbtn sm" disabled={sel.length < 1} onClick={() => nav(compareEvaluationsUrl(projectId, sel))}>
          Compare{sel.length ? ` ${sel.length}` : ''}
        </button>
      }
    >
      <ErrorBox error={runs.error} />
      {runs.isLoading ? (
        <Loading />
      ) : !runs.data?.length ? (
        <p className="quiet-note">No evaluation has run this model yet.</p>
      ) : (
        <EvalRunsList projectId={projectId} calls={runs.data} by="evaluation" label={`Evaluations of ${o.name}`} selected={sel} onSelect={setSel} agentRuns={agentRuns.data} />
      )}
    </Block>
  )
}

const PARAM_WORDS: Record<string, string> = {
  temperature: 'Temperature',
  max_tokens: 'Max tokens',
  top_p: 'Top p',
  response_format: 'Response format',
  frequency_penalty: 'Frequency penalty',
  presence_penalty: 'Presence penalty',
  stop: 'Stop',
  n_times: 'Times',
}

function LLMModelView({ o, projectId }: { o: WeftObject; projectId: string }) {
  const v = o.value ?? {}
  const p = v.default_params ?? {}
  const msgs = Array.isArray(p.messages_template) ? p.messages_template : []
  const { messages_template: _m, ...otherParams } = p
  return (
    <>
      <Description val={v} />
      <div className="asset-panel">
        <KeyValueList
          items={[
            { label: 'Model', value: <span title={typeof v.llm_model_id === 'string' ? v.llm_model_id : undefined}>{modelDisplayName(v.llm_model_id) || '—'}</span> },
            ...Object.entries(otherParams).map(([k, x]) => ({ label: PARAM_WORDS[k] ?? k.replace(/_/g, ' '), value: fmtScore(x), mono: true })),
          ]}
        />
      </div>
      <Block title="Messages" note="placeholders are filled from the dataset row’s columns of the same name">
        <ProseMessages messages={msgs} />
      </Block>
      <ModelRuns o={o} projectId={projectId} />
    </>
  )
}

function GenericModelView({ o, projectId }: { o: WeftObject; projectId: string }) {
  return (
    <>
      <div className="asset-panel">
        <ValueView value={o.value} projectId={projectId} />
      </div>
      <ModelRuns o={o} projectId={projectId} />
    </>
  )
}

// ------------------------------------------------------------------ scorers

/** One score in the "Recent scores" list: from a feedback record (monitors, on-demand scoring) or a scorer run with none. */
interface ScoreEntry {
  key: string
  /** The question the scored step was asked, or a fallback. */
  title: ReactNode
  to?: string
  result: string
  reason: string
  at: string
  /** The judge's own run, opened from the "Why" disclosure. */
  judgeRun?: string
}

const spanIdOf = (ref: string | null | undefined) => {
  const p = ref ? parseRef(ref) : null
  return p?.kind === 'span' ? p.name : null
}

function ScorerView({ o, projectId }: { o: WeftObject; projectId: string }) {
  const v = o.value ?? {}
  const cls = valClass(v)
  const feedback = useQuery({
    queryKey: ['scorer-feedback', projectId, o.name],
    queryFn: () =>
      api
        .feedbackQuery({
          project_id: projectId,
          query: toQuery(contains('runnable_ref', objectRefPrefix(projectId, o.name), false)),
          sort_by: [{ field: 'created_at', direction: 'desc' }],
          limit: 30,
        })
        .then((r) => r.items),
  })
  const runs = useQuery({
    queryKey: ['scorer-calls', projectId, o.name],
    queryFn: () =>
      api.spansQuery({ project_id: projectId, filter: { names: [`${o.name}.score`] }, sort_by: [{ field: 'started_at', direction: 'desc' }], limit: 20 }).then((r) => r.items),
  })
  // The steps that were scored, for the question each was asked.
  const scoredIds = useMemo(() => [...new Set((feedback.data ?? []).map((f) => spanIdOf(f.ref)).filter(Boolean) as string[])], [feedback.data])
  const scored = useQuery({
    queryKey: ['scored-spans', projectId, scoredIds],
    queryFn: () => api.spansQuery({ project_id: projectId, filter: { ids: scoredIds }, columns: ['id', 'inputs'], limit: scoredIds.length }).then((r) => r.items),
    enabled: scoredIds.length > 0,
  })
  const entries = useMemo<ScoreEntry[]>(() => {
    const byId = new Map((scored.data ?? []).map((c) => [c.id, c]))
    const runById = new Map((runs.data ?? []).map((c) => [c.id, c]))
    const usedRuns = new Set<string>()
    const out: ScoreEntry[] = (feedback.data ?? []).map((f) => {
      const out = f.payload?.output ?? f.payload
      const stepId = spanIdOf(f.ref)
      const judgeRun = spanIdOf(f.span_ref) ?? undefined
      if (judgeRun) usedRuns.add(judgeRun)
      const q = runTitle(stepId ? byId.get(stepId)?.inputs : null, '') || runTitle(judgeRun ? runById.get(judgeRun)?.inputs : null, '')
      return {
        key: f.id,
        title: stepId ? q || 'A step' : <RefChip refStr={f.ref} />,
        to: stepId ? spanUrl(projectId, stepId) : undefined,
        result: scoreResultWords(out),
        reason: scoreReason(out),
        at: f.created_at,
        judgeRun,
      }
    })
    for (const c of runs.data ?? []) {
      if (usedRuns.has(c.id)) continue
      out.push({
        key: c.id,
        title: runTitle(c.inputs, 'A run'),
        to: spanUrl(projectId, c.id),
        result: c.error ? runAnswer(null, c.error) : scoreResultWords(c.output),
        reason: c.error ? '' : scoreReason(c.output),
        at: c.started_at,
      })
    }
    return out.sort((a, b) => (a.at < b.at ? 1 : a.at > b.at ? -1 : 0))
  }, [feedback.data, runs.data, scored.data, projectId])
  const modelId = typeof v.model === 'string' ? v.model : null
  return (
    <>
      <Description val={v} />
      {!isServerScorer(o) && <p className="quiet-note">This scorer runs in the SDK only; monitors and server-side evaluations cannot run it.</p>}
      {cls === 'LLMAsAJudgeScorer' ? (
        <>
          <div className="asset-panel">
            <KeyValueList
              items={[
                { label: 'Kind', value: SCORER_LABELS[cls as ServerScorerClass] },
                {
                  label: 'Model',
                  value: modelId && parseRef(modelId) ? <RefChip refStr={modelId} /> : <span title={modelId ?? undefined}>{modelDisplayName(modelId) || '—'}</span>,
                },
                { label: 'Answers as', value: v.response_format === 'json_object' ? 'JSON' : (v.response_format ?? 'text') },
              ]}
            />
          </div>
          <Block title="Scoring prompt">
            <div className="prose-messages">
              <div className="prose-msg">
                <p className="prose-text">
                  <TemplateText text={String(v.scoring_prompt ?? '')} />
                </p>
              </div>
            </div>
          </Block>
        </>
      ) : (
        <div className="asset-panel">
          {cls && cls in SCORER_LABELS && <p className="quiet-note" style={{ marginBottom: 10 }}>{SCORER_LABELS[cls as ServerScorerClass]} scorer</p>}
          <ValueView value={v} projectId={projectId} />
        </div>
      )}
      <ErrorBox error={feedback.error ?? runs.error} />
      {entries.length > 0 && (
        <Block title="Recent scores" count={entries.length}>
          <GroupedList label={`Recent scores from ${o.name}`} className="score-list">
            {entries.map((e) => (
              <ListRow
                key={e.key}
                title={
                  e.to ? (
                    <Link className="lrow-title-link" to={e.to}>
                      {e.title}
                    </Link>
                  ) : (
                    e.title
                  )
                }
                summary={capFirst(e.result)}
                meta={
                  e.reason || e.judgeRun ? (
                    <details className="score-why">
                      <summary>Why</summary>
                      {e.reason && <p className="score-why-text">{e.reason}</p>}
                      {e.judgeRun && (
                        <Link className="score-why-link" to={spanUrl(projectId, e.judgeRun)}>
                          Open the judge’s run
                        </Link>
                      )}
                    </details>
                  ) : undefined
                }
                time={fmtRelative(e.at)}
                timeTitle={fmtDate(e.at)}
              />
            ))}
          </GroupedList>
        </Block>
      )}
    </>
  )
}
