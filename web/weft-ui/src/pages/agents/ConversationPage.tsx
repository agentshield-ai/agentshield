import { useEffect, useMemo, useState } from 'react'
import { Link, useLocation, useParams } from 'react-router-dom'
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { agentsApi, agentsUrl, agentUrl } from '../../api/agents'
import { useProjectId } from '../../hooks/useProject'
import { fmtCost, fmtDate, fmtNumber, fmtRelative } from '../../lib/format'
import { spanSessionUrl } from '../../lib/urls'
import { Empty, ErrorBox, Spinner } from '../../components/common'
import { TurnView } from '../../components/agents/AgentTimeline'
import { KindLegend, SpanStrip } from '../../components/agents/SpanStrip'
import { compact } from '../../components/agents/agentUtils'
import { ConversationEvals, predictionsByTrace, TurnEvalBadge, useConversationEvals } from '../../components/agents/AgentEvals'
import '../../components/agents/agents.css'

const PAGE = 20

export function ConversationPage() {
  const projectId = useProjectId()
  const { conversationId = '' } = useParams()
  const location = useLocation()
  const [limit, setLimit] = useState(PAGE)
  const chat = useQuery({
    queryKey: ['agent-conversation', projectId, conversationId, limit],
    queryFn: () =>
      agentsApi.conversationChat({ project_id: projectId, conversation_id: conversationId, include_feedback: true, include_model_tool_calls: true, limit }),
    placeholderData: keepPreviousData,
  })
  const spans = useQuery({
    queryKey: ['agent-conv-spans', projectId, [conversationId]],
    queryFn: () => agentsApi.conversationSpans({ project_id: projectId, conversation_ids: [conversationId] }),
  })
  const evals = useConversationEvals(projectId, conversationId)
  const predByTrace = useMemo(() => predictionsByTrace(evals.data?.evaluations), [evals.data])
  const c = chat.data
  const firstTurn = c ? c.total_turns - c.turns.length : 0
  // Jump to #turn-N once the turns are rendered.
  useEffect(() => {
    if (!c || !location.hash) return
    document.getElementById(location.hash.slice(1))?.scrollIntoView()
  }, [c, location.hash])
  return (
    <div className="page">
      <div className="breadcrumbs">
        <Link to={agentsUrl(projectId)}>Agents</Link>
        {c?.agent_names?.[0] && (
          <>
            <span>/</span>
            <Link to={agentUrl(projectId, c.agent_names[0])}>{c.agent_names[0]}</Link>
          </>
        )}
        <span>/</span>
        <span>Conversation</span>
      </div>
      <div className="page-header">
        <h1 className="mono" style={{ fontSize: 18 }}>
          {c?.conversation_name || conversationId}
        </h1>
        {c && c.agent_names.length > 1 && (
          <span className="chips">
            {c.agent_names.map((a) => (
              <Link key={a} to={agentUrl(projectId, a)} className="chip">
                {a}
              </Link>
            ))}
          </span>
        )}
        {chat.isFetching && <Spinner />}
        <span className="spacer" />
        <Link className="btn sm" to={spanSessionUrl(projectId, conversationId)}>
          View as thread
        </Link>
      </div>
      <ErrorBox error={chat.error} />
      {chat.isLoading ? (
        <div className="empty">
          <Spinner label="Loading conversation…" />
        </div>
      ) : !c || c.total_turns === 0 ? (
        <Empty>No spans recorded for conversation “{conversationId}”.</Empty>
      ) : (
        <>
          <div className="stat-grid">
            <div className="stat">
              <div className="label">Turns</div>
              <div className="value">{fmtNumber(c.total_turns)}</div>
            </div>
            <div className="stat">
              <div className="label">Spans</div>
              <div className="value">
                {fmtNumber(c.span_count)}
                {c.error_count > 0 && <span style={{ color: 'var(--error)', fontSize: 13, marginLeft: 6 }}>{c.error_count} failed</span>}
              </div>
            </div>
            <div className="stat">
              <div className="label">Tokens in / out</div>
              <div className="value">
                {compact(c.total_input_tokens)} / {compact(c.total_output_tokens)}
              </div>
            </div>
            <div className="stat">
              <div className="label">Cost</div>
              <div className="value">{fmtCost(c.total_cost_usd)}</div>
            </div>
            <div className="stat">
              <div className="label">Started</div>
              <div className="value" title={fmtDate(c.started_at)} style={{ fontSize: 15 }}>
                {c.started_at ? new Date(c.started_at).toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' }) : '—'}
              </div>
            </div>
            <div className="stat">
              <div className="label">Last activity</div>
              <div className="value" title={fmtDate(c.last_activity)} style={{ fontSize: 15 }}>
                {fmtRelative(c.last_activity)}
              </div>
            </div>
          </div>
          <ConversationEvals projectId={projectId} runs={evals.data?.evaluations ?? []} />
          {spans.data?.conversations[0]?.spans.length ? (
            <div className="col" style={{ gap: 4 }}>
              <div className="row small muted">
                <span className="grow">Events</span>
                <KindLegend />
              </div>
              <SpanStrip spans={spans.data.conversations[0].spans} max={400} />
            </div>
          ) : null}
          {c.has_more && (
            <div className="row" style={{ justifyContent: 'center' }}>
              <button className="btn sm" onClick={() => setLimit((l) => l + PAGE)}>
                Load {Math.min(PAGE, firstTurn)} earlier {firstTurn === 1 ? 'turn' : 'turns'}
              </button>
            </div>
          )}
          <div className="col" style={{ gap: 12 }}>
            {c.turns.map((t, i) => (
              <TurnView
                key={`${t.trace_id}-${t.root_span_id}`}
                turn={t}
                index={firstTurn + i}
                projectId={projectId}
                badges={<TurnEvalBadge projectId={projectId} items={predByTrace.get(t.trace_id) ?? []} />}
              />
            ))}
          </div>
        </>
      )}
    </div>
  )
}
