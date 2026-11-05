import { useMemo, useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { api } from '../../api/client'
import type { Any, Span } from '../../api/types'
import { useProjectId } from '../../hooks/useProject'
import { callCost, callLatencyMs, callStatus, callTokens, fmtCost, fmtDate, fmtDuration, fmtNumber, fmtRelative } from '../../lib/format'
import { spanUrl, spanSessionsUrl } from '../../lib/urls'
import { ErrorBox, Loading } from '../../components/common'
import { PageHeader } from '../../components/ui/PageHeader'
import { Sentence } from '../../components/ui/Sentence'
import { StatusGlyph } from '../../components/ui/StatusGlyph'
import { spanSessionSentence, statusGlyph } from '../../lib/summaries/traces'
import '../observe.css'
import { ValueView } from '../../components/ValueView'
import { ChatMessageView, ChatView, extractChat, normaliseMessage } from '../../components/traces/ChatView'
import { callLabel, reactionCounts } from '../../components/traces/callUtils'

/** A turn is a call with turn_id == id, or with no parent inside the thread. */
function pickTurns(calls: Span[]): Span[] {
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

function Turn({ projectId, call, index, fullContext }: { projectId: string; call: Span; index: number; fullContext: boolean }) {
  const chat = extractChat(call)
  const tokens = callTokens(call).total
  const cost = callCost(call)
  const reactions = reactionCounts(call.feedback)
  return (
    <section className="obs-turn" aria-label={`Turn ${index + 1}`}>
      <div className="obs-turn-head">
        <StatusGlyph kind={statusGlyph(callStatus(call)).kind} label={statusGlyph(callStatus(call)).label} size={18} />
        <span>Turn {index + 1}</span>
        <Link to={spanUrl(projectId, call.id)} className="call-link">
          {callLabel(call)}
        </Link>
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
        <span>{fmtDuration(callLatencyMs(call))}</span>
        {tokens > 0 && <span>{fmtNumber(tokens)} tokens</span>}
        {cost ? <span>{fmtCost(cost)}</span> : null}
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
        {call.error && <div className="obs-error">{call.error.split('\n')[0]}</div>}
      </div>
    </section>
  )
}

export function SpanSessionPage() {
  const projectId = useProjectId()
  const { sessionId: threadId = '' } = useParams()
  const [fullContext, setFullContext] = useState(false)
  const summary = useQuery({
    queryKey: ['span-sessions', projectId, 'one', threadId],
    queryFn: () => api.spanSessions({ project_id: projectId, filter: { session_ids: [threadId] }, limit: 1 }),
  })
  const callsQ = useQuery({
    queryKey: ['spans', projectId, 'session', threadId],
    queryFn: () =>
      api.spansQuery({
        project_id: projectId,
        filter: { session_ids: [threadId] },
        sort_by: [{ field: 'started_at', direction: 'asc' }],
        limit: 2000,
        include_costs: true,
        include_feedback: true,
      }),
  })
  const turns = useMemo(() => pickTurns(callsQ.data?.items ?? []), [callsQ.data])
  const t = summary.data?.items?.[0]
  const failedTurns = turns.filter((c) => callStatus(c) === 'error').length
  return (
    <div className="page-frame detail obs-page thread-page">
      <PageHeader
        variant="detail"
        back={{ to: spanSessionsUrl(projectId), label: 'Span sessions' }}
        title={<span className="mono obs-break">{threadId}</span>}
        helpTopic="Traces"
        help="traces.md"
        sentence={
          t && callsQ.data ? (
            <Sentence
              size="detail"
              parts={[spanSessionSentence({ turns: turns.length, spans: t.span_count, startedAt: t.started_at, lastAt: t.last_updated, cost: t.cost_usd, failedTurns })]}
            />
          ) : undefined
        }
        meta={
          t ? (
            <>
              <span title={fmtDate(t.started_at)}>Started {fmtRelative(t.started_at)}</span> · <span title={fmtDate(t.last_updated)}>last updated {fmtRelative(t.last_updated)}</span> · turn
              time {fmtDuration(t.p50_turn_duration_ms)} (p50), {fmtDuration(t.p99_turn_duration_ms)} (p99){t.total_tokens > 0 ? <> · {fmtNumber(t.total_tokens)} tokens</> : null}
            </>
          ) : undefined
        }
        controls={
          <label className="obs-check obs-toolbar" title="Show the full message history sent with each turn">
            <input type="checkbox" checked={fullContext} onChange={(e) => setFullContext(e.target.checked)} /> Full context
          </label>
        }
      />
      <ErrorBox error={callsQ.error ?? summary.error} />
      {callsQ.isLoading ? (
        <Loading />
      ) : turns.length === 0 ? (
        <p className="quiet-empty">No spans found for this session.</p>
      ) : (
        <div className="obs-turns">
          {turns.map((c, i) => (
            <Turn key={c.id} projectId={projectId} call={c} index={i} fullContext={fullContext} />
          ))}
        </div>
      )}
    </div>
  )
}
