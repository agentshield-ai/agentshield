import { useEffect, useMemo, useState } from 'react'
import { Link, useLocation, useParams } from 'react-router-dom'
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { agentsApi, agentUrl } from '../../api/agents'
import { useProjectId } from '../../hooks/useProject'
import { fmtCost, fmtDate, fmtRelative } from '../../lib/format'
import { sessionUrl, spanSessionUrl } from '../../lib/urls'
import { Empty, ErrorBox } from '../../components/common'
import { PageHeader } from '../../components/ui/PageHeader'
import { Sentence } from '../../components/ui/Sentence'
import { conversationSentence } from '../../lib/summaries/agentsTeams'
import './agents-teams.css'
import { TurnView } from '../../components/agents/AgentTimeline'
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
  const evals = useConversationEvals(projectId, conversationId)
  const predByTrace = useMemo(() => predictionsByTrace(evals.data?.evaluations), [evals.data])
  const c = chat.data
  const firstTurn = c ? c.total_turns - c.turns.length : 0
  // Jump to #turn-N once the turns are rendered.
  useEffect(() => {
    if (!c || !location.hash) return
    document.getElementById(location.hash.slice(1))?.scrollIntoView()
  }, [c, location.hash])
  const title = c?.conversation_name || conversationId
  return (
    <div className="page-frame detail analytics-page">
      <PageHeader
        variant="detail"
        back={{ to: sessionUrl(projectId, conversationId, { tab: 'raw' }), label: 'The session' }}
        title={<span className="at-title">Turn view of {title}</span>}
        helpTopic="Turn view"
        help="agents.md"
        sentence={
          chat.isLoading ? (
            <p className="sentence s-detail sk-sentence" aria-busy="true">
              <span className="sr-only">Reading the session…</span>
              <span className="sk sk-line" aria-hidden />
            </p>
          ) : c && c.total_turns > 0 ? (
            <Sentence size="detail" parts={[conversationSentence(c)]} />
          ) : undefined
        }
        meta={
          c && c.total_turns > 0 ? (
            <>
              {c.agent_names.length > 1 && (
                <>
                  {c.agent_names.map((a, i) => (
                    <span key={a}>
                      {i > 0 && ', '}
                      <Link to={agentUrl(projectId, a)}>{a}</Link>
                    </span>
                  ))}
                  {' · '}
                </>
              )}
              {compact(c.total_input_tokens)} in / {compact(c.total_output_tokens)} out tokens · cost {fmtCost(c.total_cost_usd)} ·{' '}
              <span title={fmtDate(c.started_at)}>started {fmtRelative(c.started_at)}</span> · <span title={fmtDate(c.last_activity)}>last activity {fmtRelative(c.last_activity)}</span>
            </>
          ) : undefined
        }
        controls={
          <Link className="qbtn" to={spanSessionUrl(projectId, conversationId)}>
            View as thread
          </Link>
        }
      />
      <ErrorBox error={chat.error} />
      {chat.isLoading ? null : !c || c.total_turns === 0 ? (
        <Empty>Nothing was recorded for session “{conversationId}”.</Empty>
      ) : (
        <>
          <ConversationEvals projectId={projectId} runs={evals.data?.evaluations ?? []} />
          {c.has_more && (
            <div className="row" style={{ justifyContent: 'center' }}>
              <button type="button" className="qbtn" onClick={() => setLimit((l) => l + PAGE)}>
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
