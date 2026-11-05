import { useEffect, useMemo, useState, type ReactNode } from 'react'
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '../../api/client'
import { spanRef, spanDisplayName, parseRef } from '../../api/refs'
import type { Any, Span } from '../../api/types'
import { useProjectId } from '../../hooks/useProject'
import { callCost, callLatencyMs, callStatus, callTokens, fmtBytes, fmtCost, fmtDate, fmtDuration, fmtNumber, fmtRelative } from '../../lib/format'
import { spanUrl, playgroundUrl, refUrl, spanSessionUrl, tracesUrl } from '../../lib/urls'
import { ErrorBox, Loading, Section } from '../../components/common'
import { CopyText, KeyValueList } from '../../components/ui/Inspector'
import { PageHeader } from '../../components/ui/PageHeader'
import { PlainTable } from '../../components/ui/PlainTable'
import { Segmented, segmentedTabId } from '../../components/ui/Segmented'
import { Sentence } from '../../components/ui/Sentence'
import { StatusGlyph } from '../../components/ui/StatusGlyph'
import { spanSentence, statusGlyph } from '../../lib/summaries/traces'
import '../observe.css'
import { JsonBlock, ValueView } from '../../components/ValueView'
import { RefChip } from '../../components/RefChip'
import { CallChatView, extractChat, findInputMessages } from '../../components/traces/ChatView'
import { TraceTree } from '../../components/traces/TraceTree'
import { Timeline } from '../../components/traces/Timeline'
import { TraceGraph } from '../../components/traces/TraceGraph'
import { CodeComposition, FlameGraph } from '../../components/traces/FlameGraph'
import { LinkedCalls } from '../../components/traces/LinkedCalls'
import { OpCodeView } from '../../components/traces/OpCodeView'
import { FeedbackPanel, Reactions, ScoresPanel, useCallFeedback } from '../../components/traces/FeedbackPanel'
import { AddToDatasetModal } from '../../components/traces/AddToDatasetModal'
import { ConfirmModal, Dropdown } from '../../components/traces/Menu'
import { Icon } from '../../components/ui/Icons'
import { CodeSnippet, serverOrigin } from '../../components/traces/GettingStarted'
import { buildTree, callLabel, flattenTree, type TreeNode } from '../../components/traces/callUtils'
import { AddToQueueButton } from '../../components/queues/AddToQueueModal'

type Tab = 'span' | 'feedback' | 'scores' | 'summary' | 'attributes' | 'code' | 'use' | 'raw'
const TABS: Tab[] = ['span', 'feedback', 'scores', 'summary', 'attributes', 'code', 'use', 'raw']
/** The rarely used views, behind "More" (a second-level control). */
const MORE_TABS: Tab[] = ['summary', 'attributes', 'code', 'use', 'raw']
type TopTab = 'span' | 'feedback' | 'scores' | 'more'

/** True at phone and narrow-tablet widths, where the trace's tree moves under the title. */
function useNarrow(query = '(max-width: 800px)'): boolean {
  const get = () => typeof window !== 'undefined' && typeof window.matchMedia === 'function' && window.matchMedia(query).matches
  const [narrow, setNarrow] = useState(get)
  useEffect(() => {
    if (typeof window.matchMedia !== 'function') return
    const m = window.matchMedia(query)
    const on = () => setNarrow(m.matches)
    on()
    m.addEventListener('change', on)
    return () => m.removeEventListener('change', on)
  }, [query])
  return narrow
}
type LeftView = 'tree' | 'code' | 'flame' | 'timeline' | 'graph'
const LEFT_VIEWS: LeftView[] = ['tree', 'code', 'flame', 'timeline', 'graph']
const TRACE_LIMIT = 5000

export function SpanPage() {
  const projectId = useProjectId()
  const { spanId: callId = '' } = useParams()
  const [params, setParams] = useSearchParams()
  const navigate = useNavigate()
  const tab = (TABS.includes(params.get('tab') as Tab) ? params.get('tab') : 'span') as Tab
  const leftView: LeftView = LEFT_VIEWS.includes(params.get('view') as LeftView) ? (params.get('view') as LeftView) : 'tree'
  const narrow = useNarrow()

  const qc = useQueryClient()
  const callQ = useQuery({
    queryKey: ['span', projectId, callId],
    queryFn: () => api.spanFind(projectId, callId, { include_costs: true, include_feedback: true, include_storage_size: true }),
    // While loading, show the copy from an already-loaded trace so moving through the tree does not flash.
    placeholderData: () => {
      for (const [, d] of qc.getQueriesData<{ items: Span[] }>({ queryKey: ['spans', projectId, 'trace'] })) {
        const hit = d?.items.find((c) => c.id === callId)
        if (hit) return hit
      }
      return undefined
    },
  })
  const call = callQ.data ?? null
  const traceId = call?.trace_id
  const traceQ = useQuery({
    queryKey: ['spans', projectId, 'trace', traceId],
    queryFn: () =>
      api.spansQuery({ project_id: projectId, filter: { trace_ids: [traceId!] }, sort_by: [{ field: 'started_at', direction: 'asc' }], limit: TRACE_LIMIT, include_costs: true }),
    enabled: !!traceId,
  })
  const traceCalls = useMemo(() => {
    const cs = traceQ.data?.items ?? []
    // Make sure the selected span is present even if the trace query has not caught up.
    if (call && !cs.some((c) => c.id === call.id)) return [...cs, call]
    return cs
  }, [traceQ.data, call])
  const roots = useMemo(() => buildTree(traceCalls), [traceCalls])

  const [collapsed, setCollapsed] = useState<Set<string>>(new Set())
  useEffect(() => {
    // Large traces start with deep levels collapsed.
    const flat = flattenTree(roots)
    if (flat.length > 300) setCollapsed(new Set(flat.filter((n) => n.depth >= 1 && n.children.length).map((n) => n.call.id)))
    else setCollapsed(new Set())
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [traceId])

  const select = (id: string) => navigate({ pathname: spanUrl(projectId, id), search: params.toString() })
  const setTab = (t: Tab) =>
    setParams(
      (p) => {
        const n = new URLSearchParams(p)
        if (t === 'span') n.delete('tab')
        else n.set('tab', t)
        return n
      },
      { replace: true },
    )
  const setLeftView = (v: LeftView) =>
    setParams(
      (p) => {
        const n = new URLSearchParams(p)
        if (v === 'tree') n.delete('view')
        else n.set('view', v)
        return n
      },
      { replace: true },
    )

  if (callQ.isLoading) return <Loading />
  if (callQ.error)
    return (
      <div className="page-frame detail obs-page">
        <PageHeader variant="detail" back={{ to: tracesUrl(projectId), label: 'Traces' }} title="Span" help="traces.md" helpTopic="Traces" />
        <ErrorBox error={callQ.error} />
      </div>
    )
  if (!call)
    return (
      <div className="page-frame detail obs-page">
        <PageHeader variant="detail" back={{ to: tracesUrl(projectId), label: 'Traces' }} title="Span not found" help="traces.md" helpTopic="Traces" />
        <p className="quiet-note">
          Span <span className="mono">{callId}</span> was not found. It may have been deleted. <Link to={tracesUrl(projectId)}>Back to traces</Link>
        </p>
      </div>
    )

  const root = roots.find((r) => r.call.trace_id === call.trace_id && !r.call.parent_id) ?? roots[0]
  const node = findNode(roots, call.id)
  const spanCount = traceQ.isLoading ? 'Loading…' : `${traceCalls.length}${traceCalls.length >= TRACE_LIMIT ? '+' : ''} span${traceCalls.length === 1 ? '' : 's'}`

  const tree = (
    <>
        <div className="obs-span-tree-head">
          <Segmented<LeftView>
            label="How to show the trace"
            size="sm"
            options={[
              { id: 'tree', label: 'Tree' },
              { id: 'code', label: 'Code' },
              { id: 'flame', label: 'Flame' },
              { id: 'timeline', label: 'Timeline' },
              { id: 'graph', label: 'Graph' },
            ]}
            value={leftView}
            onChange={setLeftView}
          />
          <span className="grow" />
          {!narrow && (
            <span className="obs-faint small" style={{ whiteSpace: 'nowrap' }}>
              {spanCount}
            </span>
          )}
        </div>
        <ErrorBox error={traceQ.error} />
        {leftView === 'tree' ? (
          <TraceTree roots={roots} selectedId={call.id} onSelect={select} collapsed={collapsed} setCollapsed={setCollapsed} />
        ) : leftView === 'graph' ? (
          <TraceGraph calls={traceCalls} selectedId={call.id} onSelect={select} />
        ) : leftView === 'flame' ? (
          <FlameGraph roots={roots} selectedId={call.id} onSelect={select} />
        ) : leftView === 'code' ? (
          <CodeComposition roots={roots} selectedId={call.id} onSelect={select} />
        ) : (
          <Timeline roots={roots} selectedId={call.id} onSelect={select} />
        )}
    </>
  )

  // At narrow widths the page's own back link and title come first; the trace sits under them, folded.
  if (narrow)
    return (
      <div className="obs-span-narrow">
        <CallDetail
          projectId={projectId}
          call={call}
          root={root?.call}
          node={node}
          tab={tab}
          setTab={setTab}
          treeSlot={
            <details className="obs-details obs-span-tree-fold">
              <summary>
                Spans in this trace <span className="glist-count">{spanCount}</span>
              </summary>
              <div className="obs-span-tree obs-span-tree-inline" role="region" aria-label="Spans in this trace">
                {tree}
              </div>
            </details>
          }
        />
      </div>
    )

  return (
    <div className="split call-split">
      <div className={`pane-left obs-span-tree ${leftView !== 'tree' ? 'wide' : ''}`} role="region" aria-label="Spans in this trace">
        {tree}
      </div>
      <div className="pane-right">
        <CallDetail projectId={projectId} call={call} root={root?.call} node={node} tab={tab} setTab={setTab} />
      </div>
    </div>
  )
}

function findNode(roots: TreeNode[], id: string): TreeNode | undefined {
  for (const r of roots) {
    if (r.call.id === id) return r
    const f = findNode(r.children, id)
    if (f) return f
  }
}

// ---------------------------------------------------------------- detail

function DisplayNameEditor({ projectId, call }: { projectId: string; call: Span }) {
  const qc = useQueryClient()
  const [editing, setEditing] = useState(false)
  const [v, setV] = useState(call.display_name ?? '')
  useEffect(() => setV(call.display_name ?? ''), [call.display_name])
  const save = useMutation({
    mutationFn: (name: string) => api.spanUpdate(projectId, call.id, name),
    onSuccess: () => {
      setEditing(false)
      void qc.invalidateQueries({ queryKey: ['span', projectId, call.id] })
      void qc.invalidateQueries({ queryKey: ['spans', projectId] })
    },
  })
  if (!editing)
    return (
      <button type="button" className="linkish obs-display-name" onClick={() => setEditing(true)} title="Edit display name">
        {call.display_name ? <span>Display name: {call.display_name}</span> : <span>Add a display name</span>} <span aria-hidden>✎</span>
      </button>
    )
  return (
    <form
      className="row"
      onSubmit={(e) => {
        e.preventDefault()
        save.mutate(v.trim())
      }}
    >
      <input
        className="input"
        autoFocus
        value={v}
        onChange={(e) => setV(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === 'Escape') {
            setV(call.display_name ?? '')
            setEditing(false)
          }
        }}
        placeholder="Display name"
        aria-label="Display name"
      />
      <button className="qbtn" type="submit" disabled={save.isPending}>
        Save
      </button>
      <button className="qbtn" type="button" onClick={() => setEditing(false)}>
        Cancel
      </button>
      <ErrorBox error={save.error} />
    </form>
  )
}

function CallDetail({
  projectId,
  call,
  root,
  node,
  tab,
  setTab,
  treeSlot,
}: {
  projectId: string
  call: Span
  root?: Span
  node?: TreeNode
  tab: Tab
  setTab: (t: Tab) => void
  /** The trace's tree, under the header (narrow widths). */
  treeSlot?: ReactNode
}) {
  const navigate = useNavigate()
  const qc = useQueryClient()
  const [modal, setModal] = useState<null | 'delete' | 'dataset'>(null)
  const fb = useCallFeedback(projectId, call.id)
  const st = callStatus(call)
  const tokens = callTokens(call)
  const cost = callCost(call)
  const chatInputs = findInputMessages(call.inputs)
  const scoreCount = (fb.data?.items ?? []).filter((f) => f.feedback_type.startsWith('weft.scorer.')).length
  const fbCount = (fb.data?.items ?? []).length - scoreCount

  const del = useMutation({
    mutationFn: () => api.spansDelete(projectId, [call.id]),
    onSuccess: () => {
      setModal(null)
      void qc.invalidateQueries({ queryKey: ['spans', projectId] })
      navigate(call.parent_id ? spanUrl(projectId, call.parent_id) : tracesUrl(projectId))
    },
  })

  const g = statusGlyph(st)
  const below = (call.summary?.status_counts?.success ?? 0) + (call.summary?.status_counts?.error ?? 0)
  const isRoot = !root || root.id === call.id
  const tabs: { id: TopTab; label: string }[] = [
    { id: 'span', label: 'Overview' },
    { id: 'feedback', label: fbCount ? `Feedback ${fbCount}` : 'Feedback' },
    { id: 'scores', label: scoreCount ? `Scores ${scoreCount}` : 'Scores' },
    { id: 'more', label: 'More' },
  ]
  const moreTabs: { id: Tab; label: string }[] = [
    { id: 'summary', label: 'Usage' },
    { id: 'attributes', label: 'Attributes' },
    { id: 'code', label: 'Code' },
    { id: 'use', label: 'Use in code' },
    { id: 'raw', label: 'Raw' },
  ]
  const top: TopTab = MORE_TABS.includes(tab) ? 'more' : (tab as TopTab)
  const reactions = (fb.data?.items ?? []).filter((f) => f.feedback_type === 'weft.reaction')
  const reactionCounts = [...reactions.reduce((m, r) => m.set(r.payload?.emoji, (m.get(r.payload?.emoji) ?? 0) + 1), new Map<string, number>())].filter(([e]) => e)
  return (
    <div className="page-frame detail obs-page call-detail">
      <PageHeader
        variant="detail"
        back={isRoot ? { to: tracesUrl(projectId), label: 'Traces' } : { to: spanUrl(projectId, root!.id), label: callLabel(root!) }}
        title={
          <>
            <StatusGlyph kind={g.kind} label={g.label} size={24} className="obs-title-glyph" /> <span title={call.name}>{spanDisplayName(call.name)}</span>
          </>
        }
        helpTopic="Traces"
        help="traces.md"
        sentence={
          <Sentence
            size="detail"
            parts={[
              spanSentence({
                status: st,
                latencyMs: callLatencyMs(call),
                error: call.error,
                below,
                belowFailed: call.summary?.status_counts?.error ?? 0,
                tokens: tokens.total,
                cost,
              }),
            ]}
          />
        }
        meta={
          <>
            <span title={fmtDate(call.started_at)}>Started {fmtRelative(call.started_at)}</span>
            {call.session_id && (
              <>
                {' · '}
                <Link to={spanSessionUrl(projectId, call.session_id)}>in session {call.session_id.length > 24 ? call.session_id.slice(0, 23) + '…' : call.session_id}</Link>
              </>
            )}
            {call.user_id && <> · user {call.user_id}</>}
          </>
        }
      />
      {treeSlot}
      <div className="obs-actions">
        <DisplayNameEditor projectId={projectId} call={call} />
        <span className="grow" style={{ flex: 1 }} />
        {fb.data && (
          <Dropdown
            kind="dialog"
            className="qbtn"
            ariaLabel={reactionCounts.length ? `React (${reactionCounts.map(([e, n]) => `${e} ${n}`).join(', ')})` : 'React'}
            label={
              <>
                React
                {reactionCounts.length > 0 && <span className="obs-faint"> {reactionCounts.map(([e, n]) => `${e} ${n}`).join(' ')}</span>}
              </>
            }
          >
            <Reactions projectId={projectId} callId={call.id} feedback={fb.data.items} />
          </Dropdown>
        )}
        {chatInputs && (
          <Link className="qbtn" to={playgroundUrl(projectId, call.id)}>
            Open in playground
          </Link>
        )}
        <button type="button" className="qbtn" onClick={() => setModal('dataset')}>
          Add to dataset
        </button>
        <AddToQueueButton projectId={projectId} callIds={[call.id]} className="qbtn" />
        <button type="button" className="qbtn icon-only" onClick={() => setModal('delete')} aria-label="Delete this span" title="Delete this span">
          <Icon name="trash" size={15} />
        </button>
      </div>

      <div className="obs-tabs-scroll">
        <Segmented<TopTab>
          label="Views of this span"
          role="tabs"
          panelId="span-view"
          options={tabs}
          value={top}
          onChange={(t) => setTab(t === 'more' ? (MORE_TABS.includes(tab) ? tab : 'summary') : t)}
        />
      </div>
      {top === 'more' && (
        <div className="obs-tabs-scroll obs-subtabs">
          <Segmented label="More views of this span" size="sm" options={moreTabs} value={tab} onChange={setTab} />
        </div>
      )}
      <div role="tabpanel" id="span-view" aria-labelledby={segmentedTabId('span-view', top)} className="obs-tabpanel">
      {tab === 'span' && <CallTab projectId={projectId} call={call} node={node} root={root} />}
      {tab === 'feedback' && <FeedbackPanel projectId={projectId} call={call} />}
      {tab === 'scores' && <ScoresPanel projectId={projectId} call={call} />}
      {tab === 'summary' && <SummaryTab call={call} />}
      {tab === 'attributes' && <AttributesTab projectId={projectId} call={call} />}
      {tab === 'code' && <OpCodeView call={call} />}
      {tab === 'use' && <UseTab projectId={projectId} call={call} />}
      {tab === 'raw' && <JsonBlock value={call} />}
      </div>

      {modal === 'delete' && (
        <ConfirmModal
          title="Delete this span?"
          body={
            <>
              <b>{callLabel(call)}</b> and all of its descendant spans will be deleted.
            </>
          }
          confirmLabel="Delete"
          danger
          busy={del.isPending}
          error={del.error}
          onConfirm={() => del.mutate()}
          onClose={() => setModal(null)}
        />
      )}
      {modal === 'dataset' && <AddToDatasetModal projectId={projectId} calls={[call]} onClose={() => setModal(null)} />}
    </div>
  )
}

// ---------------------------------------------------------------- tabs

type IOMode = 'chat' | 'tree' | 'json'

function ExceptionBox({ exception }: { exception: string }) {
  const [first, ...rest] = exception.split('\n')
  const [open, setOpen] = useState(rest.length < 15)
  return (
    <div className="obs-error" role="group" aria-label="Error">
      <div className="obs-io-head">
        <b>{first}</b>
        {rest.length > 0 && (
          <button type="button" className="linkish" onClick={() => setOpen((o) => !o)}>
            {open ? 'Hide stack' : `Show stack (${rest.length} lines)`}
          </button>
        )}
      </div>
      {open && rest.length > 0 && <pre className="mono">{rest.join('\n')}</pre>}
    </div>
  )
}

function CallTab({ projectId, call, node, root }: { projectId: string; call: Span; node?: TreeNode; root?: Span }) {
  const chat = useMemo(() => extractChat(call), [call])
  const [mode, setMode] = useState<IOMode>(chat ? 'chat' : 'tree')
  useEffect(() => setMode((m) => (m === 'chat' && !chat ? 'tree' : m)), [chat])
  const inputs = Object.fromEntries(Object.entries(call.inputs ?? {}).filter(([k]) => k !== 'self'))
  const self = call.inputs?.self
  const models = Object.keys(call.summary?.usage ?? {})
  const opRef = parseRef(call.name)
  const modes: { id: IOMode; label: string }[] = chat
    ? [
        { id: 'chat', label: 'Chat' },
        { id: 'tree', label: 'Tree' },
        { id: 'json', label: 'JSON' },
      ]
    : [
        { id: 'tree', label: 'Tree' },
        { id: 'json', label: 'JSON' },
      ]
  return (
    <div className="col" style={{ gap: 20 }}>
      {/* A one-line error is told in the sentence; a stack trace is shown here. */}
      {call.error && call.error.trim().includes('\n') && <ExceptionBox exception={call.error} />}
      <div className="obs-kv-panel">
        <KeyValueList
          items={[
            { label: 'Name', value: opRef ? <RefChip refStr={call.name} /> : call.name, mono: !opRef, help: 'The span name (internal term: op). A ref links to the function that made it.' },
            { label: 'Started', value: fmtDate(call.started_at) },
            { label: 'Ended', value: call.ended_at ? fmtDate(call.ended_at) : 'Not yet' },
            models.length > 0 && { label: models.length === 1 ? 'Model' : 'Models', value: models.join(', '), mono: true },
            self != null && { label: 'Self', value: <ValueView value={self} projectId={projectId} /> },
            !!call.parent_id && { label: 'Parent', value: <Link to={spanUrl(projectId, call.parent_id)}>Open the parent span</Link> },
            root && root.id !== call.id && { label: 'Trace root', value: <Link to={spanUrl(projectId, root.id)}>{callLabel(root)}</Link> },
            !!call.session_id && { label: 'Session', value: <Link to={spanSessionUrl(projectId, call.session_id)}>{call.session_id}</Link>, mono: true },
            !!call.user_id && { label: 'User', value: call.user_id },
            { label: 'Span id', value: <CopyText text={call.id} label="span id" />, mono: true },
            { label: 'Trace id', value: <CopyText text={call.trace_id} label="trace id" />, mono: true },
          ]}
        />
      </div>
      <section className="obs-io" aria-labelledby="span-io-h">
        <div className="obs-io-head">
          <h2 className="view-h" id="span-io-h">
            {mode === 'chat' && chat ? 'Conversation' : 'Inputs and output'}
          </h2>
          <Segmented label="Display mode" size="sm" options={modes} value={mode} onChange={setMode} />
        </div>
        {mode === 'chat' && chat ? (
          <div className="view-panel">
            <ChatParams projectId={projectId} inputs={inputs} />
            <CallChatView chat={chat} projectId={projectId} />
            {!chat.output && call.output != null && (
              <div style={{ marginTop: 12 }}>
                <div className="small muted">Output</div>
                <ValueView value={call.output} projectId={projectId} markdown />
              </div>
            )}
          </div>
        ) : (
          <>
            <div className="view-panel">
              <h3 className="replay-h">Inputs</h3>
              {Object.keys(inputs).length ? <ValueView value={inputs} projectId={projectId} mode={mode === 'json' ? 'json' : 'tree'} markdown /> : <span className="obs-faint">No inputs</span>}
            </div>
            <div className="view-panel">
              <h3 className="replay-h">Output</h3>
              {call.output === null || call.output === undefined ? (
                <span className="obs-faint">{call.ended_at ? 'None' : 'Running…'}</span>
              ) : (
                <ValueView value={call.output} projectId={projectId} mode={mode === 'json' ? 'json' : 'tree'} markdown />
              )}
            </div>
          </>
        )}
      </section>
      {node && node.children.length > 0 && (
        <section className="obs-io" aria-labelledby="span-children-h">
          <h2 className="view-h" id="span-children-h">
            Spans under it <span className="glist-count">{node.children.length}</span>
          </h2>
          <PlainTable caption="Spans directly under this one" className="obs-nowrap">
            <thead>
              <tr>
                <th scope="col">
                  <span className="sr-only">Status</span>
                </th>
                <th scope="col">Name</th>
                <th scope="col">Started</th>
                <th scope="col" className="num">
                  Latency
                </th>
                <th scope="col" className="num">
                  Tokens
                </th>
              </tr>
            </thead>
            <tbody>
              {node.children.map((ch) => {
                const cg = statusGlyph(callStatus(ch.call))
                return (
                  <tr key={ch.call.id}>
                    <td style={{ width: 24 }}>
                      <StatusGlyph kind={cg.kind} label={cg.label} size={16} />
                    </td>
                    <td>
                      <Link to={spanUrl(projectId, ch.call.id)}>{callLabel(ch.call)}</Link>
                      {ch.children.length > 0 && <span className="obs-faint small"> · {ch.children.length} under it</span>}
                    </td>
                    <td title={fmtDate(ch.call.started_at)}>+{fmtDuration(new Date(ch.call.started_at).getTime() - new Date(call.started_at).getTime())}</td>
                    <td className="num">{fmtDuration(callLatencyMs(ch.call))}</td>
                    <td className="num">{callTokens(ch.call).total ? fmtNumber(callTokens(ch.call).total) : '—'}</td>
                  </tr>
                )
              })}
            </tbody>
          </PlainTable>
        </section>
      )}
      <details className="obs-details">
        <summary>Raw JSON</summary>
        <JsonBlock value={call} />
      </details>
    </div>
  )
}

const CHAT_KEYS = new Set(['messages', 'system', 'gen_ai.input.messages', 'input_messages', 'input', 'prompt', 'contents', 'history'])

/** Model and request parameters shown above a chat transcript. */
function ChatParams({ projectId, inputs }: { projectId: string; inputs: Record<string, Any> }) {
  const [open, setOpen] = useState(false)
  const rest = Object.entries(inputs).filter(([k, v]) => !CHAT_KEYS.has(k) && !(Array.isArray(v) && v.length && typeof v[0] === 'object' && 'role' in (v[0] ?? {})))
  if (!rest.length) return null
  const simple = rest.filter(([, v]) => v == null || typeof v !== 'object')
  const complex = rest.filter(([, v]) => v != null && typeof v === 'object')
  return (
    <div className="chat-params">
      {simple.map(([k, v]) => (
        <span key={k} className="badge" title={k}>
          <span className="faint">{k}</span> <span className="mono">{String(v)}</span>
        </span>
      ))}
      {complex.length > 0 && (
        <button className="btn ghost sm" onClick={() => setOpen((o) => !o)} aria-expanded={open}>
          {open ? 'Hide' : 'Show'} {complex.map(([k]) => k).join(', ')}
        </button>
      )}
      {open && (
        <div style={{ flexBasis: '100%' }}>
          <ValueView value={Object.fromEntries(complex)} projectId={projectId} />
        </div>
      )}
    </div>
  )
}

function SummaryTab({ call }: { call: Span }) {
  const usage = call.summary?.usage ?? {}
  const costs = call.summary?.weft?.costs ?? {}
  const models = [...new Set([...Object.keys(usage), ...Object.keys(costs)])]
  const sc = call.summary?.status_counts
  const totalCost = callCost(call)
  const rest = Object.fromEntries(Object.entries(call.summary ?? {}).filter(([k]) => !['usage', 'status_counts', 'weft'].includes(k)))
  return (
    <div className="col" style={{ gap: 14 }}>
      <div className="obs-kv-panel">
        <KeyValueList
          items={[
            { label: 'Latency', value: fmtDuration(callLatencyMs(call)) },
            { label: 'Tokens', value: fmtNumber(callTokens(call).total), help: 'Prompt and completion tokens of this span and every span under it.' },
            { label: 'Cost', value: fmtCost(totalCost), help: 'Priced from the token counts with the project’s or the built-in prices.' },
            { label: 'Spans', value: `${fmtNumber(sc?.success ?? 0)} succeeded · ${fmtNumber(sc?.error ?? 0)} failed`, tone: sc?.error ? 'error' : undefined, help: 'Spans under this one (status counts).' },
            call.storage_size_bytes != null && { label: 'Storage', value: fmtBytes(call.storage_size_bytes) },
            { label: 'Started at', value: call.started_at, mono: true },
            { label: 'Ended at', value: call.ended_at ?? '—', mono: true },
          ]}
        />
      </div>
      <Section title="Usage by model">
        {models.length === 0 ? (
          <span className="faint">No LLM usage recorded in this span or its descendants.</span>
        ) : (
          <PlainTable caption="Usage by model">
              <thead>
                <tr>
                  <th>Model</th>
                  <th className="num">Requests</th>
                  <th className="num">Prompt tokens</th>
                  <th className="num">Completion tokens</th>
                  <th className="num">Total tokens</th>
                  <th className="num">Prompt cost</th>
                  <th className="num">Completion cost</th>
                  <th className="num">Total cost</th>
                </tr>
              </thead>
              <tbody>
                {models.map((m) => {
                  const u = usage[m] ?? {}
                  const c = costs[m]
                  const p = u.prompt_tokens ?? u.input_tokens ?? c?.prompt_tokens ?? 0
                  const o = u.completion_tokens ?? u.output_tokens ?? c?.completion_tokens ?? 0
                  return (
                    <tr key={m}>
                      <td className="mono" style={{ whiteSpace: 'nowrap' }}>
                        {m}
                        {c?.pricing_level === 'project' && <span className="obs-faint"> (project price)</span>}
                      </td>
                      <td className="num">{fmtNumber(u.requests ?? c?.requests)}</td>
                      <td className="num">{fmtNumber(p)}</td>
                      <td className="num">{fmtNumber(o)}</td>
                      <td className="num">{fmtNumber(u.total_tokens ?? p + o)}</td>
                      <td className="num" title={c?.prompt_token_cost != null ? `${c.prompt_token_cost} ${c.prompt_token_cost_unit ?? 'USD'}/token` : undefined}>
                        {c ? fmtCost(c.prompt_tokens_total_cost) : '—'}
                      </td>
                      <td className="num" title={c?.completion_token_cost != null ? `${c.completion_token_cost} ${c.completion_token_cost_unit ?? 'USD'}/token` : undefined}>
                        {c ? fmtCost(c.completion_tokens_total_cost) : '—'}
                      </td>
                      <td className="num">{c ? fmtCost((c.prompt_tokens_total_cost ?? 0) + (c.completion_tokens_total_cost ?? 0)) : <span className="faint" title="No price for this model">—</span>}</td>
                    </tr>
                  )
                })}
              </tbody>
          </PlainTable>
        )}
      </Section>
      {Object.keys(rest).length > 0 && (
        <Section title="Other summary fields">
          <ValueView value={rest} projectId={call.project_id} />
        </Section>
      )}
    </div>
  )
}

function maybeJson(v: Any): Any {
  if (typeof v !== 'string' || v.length < 2 || !/^[[{]/.test(v)) return v
  try {
    return JSON.parse(v)
  } catch {
    return v
  }
}

function otelAttrs(v: Any): Record<string, Any> {
  return Object.fromEntries(Object.entries(otelAttrsRaw(v)).map(([k, x]) => [k, maybeJson(x)]))
}

function otelAttrsRaw(v: Any): Record<string, Any> {
  // OTLP attributes may be a plain map or a list of {key, value:{stringValue|intValue|…}}
  if (Array.isArray(v)) {
    const out: Record<string, Any> = {}
    for (const kv of v) {
      if (!kv || typeof kv !== 'object') continue
      const val = kv.value
      out[kv.key] =
        val && typeof val === 'object'
          ? (val.stringValue ?? val.intValue ?? val.doubleValue ?? val.boolValue ?? val.arrayValue ?? val.kvlistValue ?? val.bytesValue ?? val)
          : val
    }
    return out
  }
  return v && typeof v === 'object' ? v : {}
}

function fmtOtelTime(v: Any): string {
  if (v == null) return ''
  if (typeof v === 'string' && /^\d+$/.test(v)) return new Date(Number(BigInt(v) / 1_000_000n)).toISOString()
  if (typeof v === 'number') return new Date(v > 1e15 ? v / 1e6 : v).toISOString()
  return String(v)
}

function AttributesTab({ projectId, call }: { projectId: string; call: Span }) {
  const { otel, ...attrs } = call.attributes ?? {}
  return (
    <div className="col" style={{ gap: 14 }}>
      <LinkedCalls projectId={projectId} attributes={call.attributes} />
      <Section title="Attributes">
        {Object.keys(attrs).length ? <ValueView value={attrs} projectId={projectId} /> : <span className="faint">No attributes</span>}
      </Section>
      {otel && <OtelView projectId={projectId} otel={otel} />}
    </div>
  )
}

function OtelView({ projectId, otel }: { projectId: string; otel: Any }) {
  const resource = otelAttrs(otel.resource?.attributes ?? otel.resource)
  const spanAttrs = otelAttrs(otel.attributes)
  const events: Any[] = Array.isArray(otel.events) ? otel.events : []
  const links: Any[] = Array.isArray(otel.links) ? otel.links : []
  const status = otel.status
  return (
    <>
      <Section title="OpenTelemetry span">
        <div className="kv">
          <div className="k">span_name</div>
          <div className="v mono">{String(otel.span_name ?? '—')}</div>
          <div className="k">kind</div>
          <div className="v mono">{String(otel.kind ?? '—')}</div>
          <div className="k">status</div>
          <div className="v mono">{typeof status === 'object' && status ? `${status.code ?? ''} ${status.message ?? ''}` : String(status ?? '—')}</div>
          {otel.scope && (
            <>
              <div className="k">scope</div>
              <div className="v mono">
                {typeof otel.scope === 'object' ? `${otel.scope.name ?? ''}${otel.scope.version ? ` ${otel.scope.version}` : ''}` : String(otel.scope)}
              </div>
            </>
          )}
        </div>
      </Section>
      <Section title={`Span attributes (${Object.keys(spanAttrs).length})`}>
        {Object.keys(spanAttrs).length ? <ValueView value={spanAttrs} projectId={projectId} /> : <span className="faint">None</span>}
      </Section>
      <Section title={`Events (${events.length})`}>
        {events.length === 0 ? (
          <span className="faint">No span events</span>
        ) : (
          <div className="col" style={{ gap: 10 }}>
            {events.map((e, i) => {
              const a = otelAttrs(e.attributes)
              const isExc = e.name === 'exception'
              return (
                <div key={i} className={`otel-event ${isExc ? 'exc' : ''}`}>
                  <div className="row small">
                    <b className="mono">{e.name}</b>
                    <span className="faint mono">{fmtOtelTime(e.time ?? e.time_unix_nano ?? e.timeUnixNano ?? e.timestamp)}</span>
                  </div>
                  {isExc && a['exception.stacktrace'] ? (
                    <>
                      <div className="mono small">
                        {String(a['exception.type'] ?? '')}: {String(a['exception.message'] ?? '')}
                      </div>
                      <pre className="code-block small">{String(a['exception.stacktrace'])}</pre>
                    </>
                  ) : Object.keys(a).length ? (
                    <ValueView value={a} projectId={projectId} />
                  ) : null}
                </div>
              )
            })}
          </div>
        )}
      </Section>
      <Section title="Resource">
        {Object.keys(resource).length ? <ValueView value={resource} projectId={projectId} /> : <span className="faint">None</span>}
      </Section>
      {links.length > 0 && (
        <Section title={`Links (${links.length})`}>
          <ValueView value={links} projectId={projectId} />
        </Section>
      )}
    </>
  )
}

function UseTab({ projectId, call }: { projectId: string; call: Span }) {
  const info = useQuery({ queryKey: ['server-info'], queryFn: api.server, staleTime: 60_000 })
  const origin = serverOrigin()
  const auth = info.data?.auth_required ? " \\\n  -H 'Authorization: Bearer $WEFT_API_KEY'" : ''
  const py = `import weft

client = weft.init("${projectId}", server_url="${origin}")
span = client.span("${call.id}")

print(span.inputs)
print(span.output)`
  const feedbackPy = `span.feedback.add_reaction("👍")
span.feedback.add_note("Looks good")`
  const curl = `curl -s '${origin}/api/v1/spans/${encodeURIComponent(call.id)}?project_id=${encodeURIComponent(projectId)}&include_costs=true'${auth}`
  return (
    <div className="col" style={{ gap: 14 }}>
      <Section title="Python SDK">
        <CodeSnippet code={py} lang="python" />
        <div className="small muted" style={{ margin: '10px 0 6px' }}>
          Add feedback to this span:
        </div>
        <CodeSnippet code={feedbackPy} lang="python" />
      </Section>
      <Section title="HTTP">
        <CodeSnippet code={curl} lang="bash" />
      </Section>
      <Section title="References">
        <div className="kv">
          <div className="k">span id</div>
          <div className="v mono">{call.id}</div>
          <div className="k">trace id</div>
          <div className="v mono">{call.trace_id}</div>
          <div className="k">ref</div>
          <div className="v mono">{spanRef(projectId, call.id)}</div>
          <div className="k">name</div>
          <div className="v mono">
            {refUrl(call.name) ? <RefChip refStr={call.name} /> : call.name}
          </div>
        </div>
      </Section>
    </div>
  )
}
