import { useMemo, useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { api } from '../../api/client'
import type { Any, Call } from '../../api/types'
import { useProjectId } from '../../hooks/useProject'
import { callCost, callLatencyMs, callStatus, callTokens, fmtCost, fmtDate, fmtDuration, fmtNumber, fmtRelative } from '../../lib/format'
import { callUrl, threadsUrl } from '../../lib/urls'
import { Empty, ErrorBox, Loading, StatusBadge } from '../../components/common'
import { ValueView } from '../../components/ValueView'
import { ChatMessageView, ChatView, extractChat, normaliseMessage } from '../../components/traces/ChatView'
import { callLabel, reactionCounts } from '../../components/traces/callUtils'

/** A turn is a call with turn_id == id, or with no parent inside the thread. */
function pickTurns(calls: Call[]): Call[] {
  const ids = new Set(calls.map((c) => c.id))
  return calls
    .filter((c) => c.turn_id === c.id || !c.parent_id || !ids.has(c.parent_id))
    .sort((a, b) => a.started_at.localeCompare(b.started_at))
}

/** Messages new in this turn: everything after the last assistant message of the input history. */
function newInputMessages(input: Any[]): Any[] {
  let lastAssistant = -1
  input.forEach((m, i) => {
    if (m?.role === 'assistant') lastAssistant = i
  })
  return input.slice(lastAssistant + 1).filter((m) => m?.role !== 'system' || lastAssistant === -1)
}

function Turn({ projectId, call, index, fullContext }: { projectId: string; call: Call; index: number; fullContext: boolean }) {
  const chat = extractChat(call)
  const tokens = callTokens(call).total
  const cost = callCost(call)
  const reactions = reactionCounts(call.feedback)
  return (
    <div className="turn card">
      <div className="turn-head">
        <span className="badge">Turn {index + 1}</span>
        <Link to={callUrl(projectId, call.id)} className="call-link">
          {callLabel(call)}
        </Link>
        <StatusBadge status={callStatus(call)} />
        <span className="grow" />
        {reactions.map(([e, n]) => (
          <span key={e} className="mini-reaction">
            {e}
            {n > 1 && <span className="small faint">{n}</span>}
          </span>
        ))}
        <span className="small muted" title={fmtDate(call.started_at)}>
          {fmtRelative(call.started_at)}
        </span>
        <span className="small mono">{fmtDuration(callLatencyMs(call))}</span>
        {tokens > 0 && <span className="small mono">{fmtNumber(tokens)} tok</span>}
        {cost ? <span className="small mono">{fmtCost(cost)}</span> : null}
      </div>
      <div className="turn-body">
        {chat ? (
          <>
            <ChatView messages={fullContext ? chat.input : newInputMessages(chat.input)} projectId={projectId} />
            {chat.output ? (
              <div className="chat">
                {chat.output.map((m, i) => (
                  <ChatMessageView key={i} message={normaliseMessage(m)} projectId={projectId} />
                ))}
              </div>
            ) : call.output != null ? (
              <ValueView value={call.output} projectId={projectId} markdown />
            ) : null}
          </>
        ) : (
          <div className="chat">
            <div className="chat-msg role-user">
              <div className="chat-role">input</div>
              <div className="chat-body">
                <ValueView value={Object.fromEntries(Object.entries(call.inputs ?? {}).filter(([k]) => k !== 'self'))} projectId={projectId} markdown />
              </div>
            </div>
            <div className="chat-msg role-assistant">
              <div className="chat-role">output</div>
              <div className="chat-body">
                {call.output == null ? <span className="faint">{call.ended_at ? 'None' : 'Running…'}</span> : <ValueView value={call.output} projectId={projectId} markdown />}
              </div>
            </div>
          </div>
        )}
        {call.exception && <div className="error-box">{call.exception.split('\n')[0]}</div>}
      </div>
    </div>
  )
}

export function ThreadPage() {
  const projectId = useProjectId()
  const { threadId = '' } = useParams()
  const [fullContext, setFullContext] = useState(false)
  const summary = useQuery({
    queryKey: ['threads', projectId, 'one', threadId],
    queryFn: () => api.threadsQuery({ project_id: projectId, filter: { thread_ids: [threadId] }, limit: 1 }),
  })
  const callsQ = useQuery({
    queryKey: ['calls', projectId, 'thread', threadId],
    queryFn: () =>
      api.callsQuery({
        project_id: projectId,
        filter: { thread_ids: [threadId] },
        sort_by: [{ field: 'started_at', direction: 'asc' }],
        limit: 2000,
        include_costs: true,
        include_feedback: true,
      }),
  })
  const turns = useMemo(() => pickTurns(callsQ.data?.calls ?? []), [callsQ.data])
  const t = summary.data?.threads?.[0]
  return (
    <div className="page thread-page">
      <div className="breadcrumbs">
        <Link to={threadsUrl(projectId)}>Threads</Link>
        <span>/</span>
        <span className="mono">{threadId}</span>
      </div>
      <div className="page-header">
        <h1 className="mono" style={{ fontSize: 17, wordBreak: 'break-all' }}>
          {threadId}
        </h1>
        <span className="spacer" />
        <label className="checkbox small" title="Show the full message history sent with each turn">
          <input type="checkbox" checked={fullContext} onChange={(e) => setFullContext(e.target.checked)} /> Full context
        </label>
      </div>
      {t && (
        <div className="meta-row">
          <span>
            <span className="faint">Turns</span> {fmtNumber(t.turn_count)}
          </span>
          <span>
            <span className="faint">Spans</span> {fmtNumber(t.call_count)}
          </span>
          <span title={fmtDate(t.start_time)}>
            <span className="faint">Started</span> {fmtDate(t.start_time)}
          </span>
          <span title={fmtDate(t.last_updated)}>
            <span className="faint">Last updated</span> {fmtRelative(t.last_updated)}
          </span>
          <span>
            <span className="faint">p50 / p99 turn</span> {fmtDuration(t.p50_turn_duration_ms)} / {fmtDuration(t.p99_turn_duration_ms)}
          </span>
          {t.total_tokens > 0 && (
            <span>
              <span className="faint">Tokens</span> {fmtNumber(t.total_tokens)}
            </span>
          )}
          {t.cost_usd > 0 && (
            <span>
              <span className="faint">Cost</span> {fmtCost(t.cost_usd)}
            </span>
          )}
        </div>
      )}
      <ErrorBox error={callsQ.error ?? summary.error} />
      {callsQ.isLoading ? (
        <Loading />
      ) : turns.length === 0 ? (
        <Empty>No calls found for this thread.</Empty>
      ) : (
        <div className="col" style={{ gap: 14, maxWidth: 980 }}>
          {turns.map((c, i) => (
            <Turn key={c.id} projectId={projectId} call={c} index={i} fullContext={fullContext} />
          ))}
        </div>
      )}
    </div>
  )
}
