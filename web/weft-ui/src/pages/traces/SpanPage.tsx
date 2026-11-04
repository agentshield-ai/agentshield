import { useEffect, useMemo, useState } from 'react'
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '../../api/client'
import { spanRef, spanDisplayName, parseRef } from '../../api/refs'
import type { Any, Span } from '../../api/types'
import { useProjectId } from '../../hooks/useProject'
import { callCost, callLatencyMs, callStatus, callTokens, fmtBytes, fmtCost, fmtDate, fmtDuration, fmtNumber, fmtRelative } from '../../lib/format'
import { spanUrl, playgroundUrl, refUrl, spanSessionUrl, tracesUrl } from '../../lib/urls'
import { Empty, ErrorBox, Loading, Section, StatusBadge, StatusDot, Tabs } from '../../components/common'
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
import { ConfirmModal } from '../../components/traces/Menu'
import { CodeSnippet, serverOrigin } from '../../components/traces/GettingStarted'
import { buildTree, callLabel, flattenTree, type TreeNode } from '../../components/traces/callUtils'
import { AddToQueueButton } from '../../components/queues/AddToQueueModal'

type Tab = 'span' | 'feedback' | 'scores' | 'summary' | 'attributes' | 'code' | 'use' | 'raw'
const TABS: Tab[] = ['span', 'feedback', 'scores', 'summary', 'attributes', 'code', 'use', 'raw']
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
      <div className="page">
        <ErrorBox error={callQ.error} />
      </div>
    )
  if (!call)
    return (
      <div className="page">
        <Empty>
          Span <code>{callId}</code> was not found. It may have been deleted. <Link to={tracesUrl(projectId)}>Back to traces</Link>
        </Empty>
      </div>
    )

  const root = roots.find((r) => r.call.trace_id === call.trace_id && !r.call.parent_id) ?? roots[0]
  const node = findNode(roots, call.id)

  return (
    <div className="split call-split">
      <div className={`pane-left ${leftView !== 'tree' ? 'wide' : ''}`}>
        <div className="row pane-head">
          <Tabs<LeftView>
            tabs={[
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
          <span className="faint small" style={{ whiteSpace: 'nowrap' }}>
            {traceQ.isLoading ? 'Loading…' : `${traceCalls.length}${traceCalls.length >= TRACE_LIMIT ? '+' : ''} call${traceCalls.length === 1 ? '' : 's'}`}
          </span>
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
      <button className="linkish display-name" onClick={() => setEditing(true)} title="Edit display name">
        {call.display_name ? <span>{call.display_name}</span> : <span className="faint">Add display name</span>} <span className="faint">✎</span>
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
      <button className="btn sm primary" type="submit" disabled={save.isPending}>
        Save
      </button>
      <button className="btn sm" type="button" onClick={() => setEditing(false)}>
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
}: {
  projectId: string
  call: Span
  root?: Span
  node?: TreeNode
  tab: Tab
  setTab: (t: Tab) => void
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

  const opRef = parseRef(call.name)
  return (
    <div className="page call-detail">
      <div className="col" style={{ gap: 6 }}>
        <div className="breadcrumbs">
          <Link to={tracesUrl(projectId)}>Traces</Link>
          {root && root.id !== call.id && (
            <>
              <span>/</span>
              <Link to={spanUrl(projectId, root.id)}>{callLabel(root)}</Link>
            </>
          )}
          {call.parent_id && root && call.parent_id !== root.id && (
            <>
              <span>/</span>
              <span>…</span>
            </>
          )}
        </div>
        <div className="page-header">
          <StatusDot status={st} />
          <h1 className="call-title" title={call.name}>
            {spanDisplayName(call.name)}
          </h1>
          {opRef && <RefChip refStr={call.name} />}
          <StatusBadge status={st} />
          <span className="spacer" />
          {chatInputs && (
            <Link className="btn sm" to={playgroundUrl(projectId, call.id)}>
              Open in playground
            </Link>
          )}
          <button className="btn sm" onClick={() => setModal('dataset')}>
            Add to dataset
          </button>
          <AddToQueueButton projectId={projectId} callIds={[call.id]} />
          <button className="btn sm danger" onClick={() => setModal('delete')}>
            Delete
          </button>
        </div>
        <DisplayNameEditor projectId={projectId} call={call} />
        <div className="meta-row">
          <span title={fmtDate(call.started_at)}>
            <span className="faint">Started</span> {fmtDate(call.started_at)} <span className="faint">({fmtRelative(call.started_at)})</span>
          </span>
          <span>
            <span className="faint">Latency</span> {fmtDuration(callLatencyMs(call))}
          </span>
          {tokens.total > 0 && (
            <span title={`${fmtNumber(tokens.prompt)} prompt · ${fmtNumber(tokens.completion)} completion`}>
              <span className="faint">Tokens</span> {fmtNumber(tokens.total)}
            </span>
          )}
          {cost != null && cost > 0 && (
            <span>
              <span className="faint">Cost</span> {fmtCost(cost)}
            </span>
          )}
          {call.parent_id && <Link to={spanUrl(projectId, call.parent_id)}>↑ Parent</Link>}
          {root && root.id !== call.id && <Link to={spanUrl(projectId, root.id)}>⤒ Trace root</Link>}
          {call.session_id && <Link to={spanSessionUrl(projectId, call.session_id)}>Session {call.session_id.slice(0, 12)}</Link>}
          {call.user_id && (
            <span>
              <span className="faint">User</span> {call.user_id}
            </span>
          )}
        </div>
        {fb.data && <Reactions projectId={projectId} callId={call.id} feedback={fb.data.items} />}
      </div>

      <Tabs<Tab>
        tabs={[
          { id: 'span', label: 'Span' },
          { id: 'feedback', label: <>Feedback{fbCount > 0 && <span className="tab-count">{fbCount}</span>}</> },
          { id: 'scores', label: <>Scores{scoreCount > 0 && <span className="tab-count">{scoreCount}</span>}</> },
          { id: 'summary', label: 'Summary' },
          { id: 'attributes', label: 'Attributes' },
          { id: 'code', label: 'Code' },
          { id: 'use', label: 'Use' },
          { id: 'raw', label: 'Raw' },
        ]}
        value={tab}
        onChange={setTab}
      />

      {tab === 'span' && <CallTab projectId={projectId} call={call} node={node} />}
      {tab === 'feedback' && <FeedbackPanel projectId={projectId} call={call} />}
      {tab === 'scores' && <ScoresPanel projectId={projectId} call={call} />}
      {tab === 'summary' && <SummaryTab call={call} />}
      {tab === 'attributes' && <AttributesTab projectId={projectId} call={call} />}
      {tab === 'code' && <OpCodeView call={call} />}
      {tab === 'use' && <UseTab projectId={projectId} call={call} />}
      {tab === 'raw' && <JsonBlock value={call} />}

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
    <div className="error-box exception">
      <div className="row">
        <b className="grow">{first}</b>
        {rest.length > 0 && (
          <button className="btn ghost sm" onClick={() => setOpen((o) => !o)}>
            {open ? 'Hide stack' : `Show stack (${rest.length} lines)`}
          </button>
        )}
      </div>
      {open && rest.length > 0 && <pre className="mono small" style={{ marginTop: 6 }}>{rest.join('\n')}</pre>}
    </div>
  )
}

function CallTab({ projectId, call, node }: { projectId: string; call: Span; node?: TreeNode }) {
  const chat = useMemo(() => extractChat(call), [call])
  const [mode, setMode] = useState<IOMode>(chat ? 'chat' : 'tree')
  useEffect(() => setMode((m) => (m === 'chat' && !chat ? 'tree' : m)), [chat])
  const inputs = Object.fromEntries(Object.entries(call.inputs ?? {}).filter(([k]) => k !== 'self'))
  const self = call.inputs?.self
  return (
    <div className="col" style={{ gap: 14 }}>
      {call.error && <ExceptionBox exception={call.error} />}
      <div className="row">
        <div className="seg" role="group" aria-label="Display mode">
          {(['chat', 'tree', 'json'] as IOMode[]).map((m) => (
            <button key={m} className={mode === m ? 'on' : ''} disabled={m === 'chat' && !chat} onClick={() => setMode(m)} aria-pressed={mode === m}>
              {m === 'chat' ? 'Chat' : m === 'tree' ? 'Tree' : 'JSON'}
            </button>
          ))}
        </div>
        {self != null && (
          <span className="small muted">
            self: <ValueView value={self} projectId={projectId} />
          </span>
        )}
      </div>
      {mode === 'chat' && chat ? (
        <Section title="Conversation">
          <ChatParams projectId={projectId} inputs={inputs} />
          <CallChatView chat={chat} projectId={projectId} />
          {!chat.output && call.output != null && (
            <div style={{ marginTop: 12 }}>
              <div className="small muted">Output</div>
              <ValueView value={call.output} projectId={projectId} markdown />
            </div>
          )}
        </Section>
      ) : (
        <>
          <Section title="Inputs">
            {Object.keys(inputs).length ? <ValueView value={inputs} projectId={projectId} mode={mode === 'json' ? 'json' : 'tree'} markdown /> : <span className="faint">No inputs</span>}
          </Section>
          <Section title="Output">
            {call.output === null || call.output === undefined ? (
              <span className="faint">{call.ended_at ? 'None' : 'Running…'}</span>
            ) : (
              <ValueView value={call.output} projectId={projectId} mode={mode === 'json' ? 'json' : 'tree'} markdown />
            )}
          </Section>
        </>
      )}
      {node && node.children.length > 0 && (
        <Section title={`Child spans (${node.children.length})`}>
          <div className="table-wrap">
            <table className="table">
              <thead>
                <tr>
                  <th style={{ width: 20 }} />
                  <th>Name</th>
                  <th>Started</th>
                  <th className="num">Latency</th>
                  <th className="num">Tokens</th>
                </tr>
              </thead>
              <tbody>
                {node.children.map((ch) => (
                  <tr key={ch.call.id}>
                    <td>
                      <StatusDot status={callStatus(ch.call)} />
                    </td>
                    <td>
                      <Link to={spanUrl(projectId, ch.call.id)}>{callLabel(ch.call)}</Link>
                      {ch.children.length > 0 && <span className="faint small"> · {ch.children.length} children</span>}
                    </td>
                    <td title={fmtDate(ch.call.started_at)}>+{fmtDuration(new Date(ch.call.started_at).getTime() - new Date(call.started_at).getTime())}</td>
                    <td className="num">{fmtDuration(callLatencyMs(ch.call))}</td>
                    <td className="num">{callTokens(ch.call).total ? fmtNumber(callTokens(ch.call).total) : '—'}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </Section>
      )}
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
      <div className="stat-grid">
        <Stat label="Latency" value={fmtDuration(callLatencyMs(call))} />
        <Stat label="Total tokens" value={fmtNumber(callTokens(call).total)} />
        <Stat label="Cost" value={fmtCost(totalCost)} />
        <Stat label="Successful spans" value={fmtNumber(sc?.success ?? 0)} />
        <Stat label="Errored spans" value={fmtNumber(sc?.error ?? 0)} tone={sc?.error ? 'error' : undefined} />
        {call.storage_size_bytes != null && <Stat label="Storage" value={fmtBytes(call.storage_size_bytes)} />}
      </div>
      <Section title="Usage by model">
        {models.length === 0 ? (
          <span className="faint">No LLM usage recorded in this span or its descendants.</span>
        ) : (
          <div className="table-wrap">
            <table className="table">
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
                        {c?.pricing_level === 'project' && <span className="badge accent" style={{ marginLeft: 6 }}>project price</span>}
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
            </table>
          </div>
        )}
      </Section>
      <Section title="Timing">
        <div className="kv">
          <div className="k">started_at</div>
          <div className="v mono">{call.started_at}</div>
          <div className="k">ended_at</div>
          <div className="v mono">{call.ended_at ?? '—'}</div>
          <div className="k">latency</div>
          <div className="v">{fmtDuration(callLatencyMs(call))}</div>
          <div className="k">status</div>
          <div className="v">
            <StatusBadge status={callStatus(call)} />
          </div>
        </div>
      </Section>
      {Object.keys(rest).length > 0 && (
        <Section title="Other summary fields">
          <ValueView value={rest} projectId={call.project_id} />
        </Section>
      )}
    </div>
  )
}

function Stat({ label, value, tone }: { label: string; value: string; tone?: 'error' }) {
  return (
    <div className="stat">
      <div className="label">{label}</div>
      <div className="value" style={tone === 'error' ? { color: 'var(--error)' } : undefined}>
        {value}
      </div>
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
