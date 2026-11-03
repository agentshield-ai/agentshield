// One agent: its figures, who hands work to it and whom it hands work to,
// its tools, scores and recent sessions.
import { Link, useParams } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { v1 } from '../../api/v1'
import { useProjectId } from '../../hooks/useProject'
import { flaggedOf, fmtCost, fmtDuration, fmtNumber, fmtRelative } from '../../lib/format'
import { agentAnalyticsUrl, agentsUrlFor, agentUrl, sessionsUrl, teamUrl } from '../../lib/urls'
import { ErrorBox, Loading } from '../../components/common'
import { AgentChip, AgentDot, Bar, Crumbs, humanScorer, Metric, Panel, ScorePill } from '../../components/ui/bits'
import { usePageHelp } from '../../components/ui/PageHelp'
import { SessionList } from '../../components/sessions/SessionList'
import { filterQueryString } from '../../lib/filters'

export function AgentDetailPage() {
  const pid = useProjectId()
  const { agentName = '' } = useParams()
  const help = usePageHelp(
    'agent',
    'Agent',
    <>
      One agent across every session: how often it runs and fails, who delegates to it and whom it delegates to, which tools it calls, how often AgentShield flags them, and how its
      work scores.
    </>,
    'agents.md#look-at-one-agent',
  )
  const a = useQuery({ queryKey: ['v1', pid, 'agent', agentName], queryFn: () => v1.agent(pid, agentName) })
  const sessions = useQuery({ queryKey: ['v1', pid, 'sessions', { agent: agentName }], queryFn: () => v1.sessions(pid, { agent: agentName, limit: 10 }) })
  if (a.isLoading) return <Loading />
  if (a.error)
    return (
      <div className="page">
        <Crumbs items={[{ label: 'Agents', to: agentsUrlFor(pid) }, { label: agentName }]} />
        <ErrorBox error={a.error} />
      </div>
    )
  const d = a.data!
  const errRate = d.invocation_count ? (d.invocation_error_count ?? 0) / d.invocation_count : 0
  const maxTool = Math.max(1, ...d.tool_counts.map((t) => t.count))
  return (
    <div className="page wide">
      <Crumbs items={[{ label: 'Agents', to: agentsUrlFor(pid) }, { label: agentName }]} />
      <header className="page-head">
        <div className="grow">
          <h1 className="row" style={{ gap: 10 }}>
            <AgentDot name={d.agent_name} /> {d.agent_name}
          </h1>
          <div className="row wrap small muted" style={{ gap: 10, marginTop: 6 }}>
            {(d.models ?? []).length > 0 && <span>Models: {(d.models ?? []).join(', ')}</span>}
            {(d.teams ?? []).map((t) => (
              <Link key={t} to={teamUrl(pid, t)}>
                Team {t.startsWith('auto:') ? t.slice(5).replace(/\+/g, ' + ') : t}
              </Link>
            ))}
            <span>last seen {fmtRelative(d.last_seen)}</span>
          </div>
        </div>
        <Link className="btn ghost sm" to={agentAnalyticsUrl(pid, agentName)} title="Charts, spans, signals and agent evaluations for this agent">
          Analytics and spans
        </Link>
        {help.button}
      </header>
      {help.panel}
      <div className="metrics">
        <Metric label="Sessions" value={fmtNumber(d.session_count)} tip="Sessions in which this agent ran at least once." />
        <Metric
          label="Runs"
          value={fmtNumber(d.invocation_count)}
          sub={`median ${fmtDuration(d.latency_ms_p50)}`}
          tip="Invocations of the agent; the median is the typical time one run takes."
        />
        <Metric
          label="Failed runs"
          value={`${(errRate * 100).toFixed(0)}%`}
          sub={`${fmtNumber(d.invocation_error_count ?? 0)} of ${fmtNumber(d.invocation_count)}`}
          tone={errRate > 0 ? 'error' : undefined}
          tip="Runs that ended with an error, out of all runs."
        />
        <Metric label="Handed to it" value={fmtNumber(d.handoffs_in)} tip="Hand-offs where another agent passed work to this one (returns excluded)." />
        <Metric label="Handed on" value={fmtNumber(d.handoffs_out)} tip="Hand-offs where this agent passed work to another agent (returns excluded)." />
        <Metric
          label="Reasoning"
          value={fmtNumber(d.reasoning_step_count)}
          sub={`${fmtNumber(d.reasoning_tokens)} tokens`}
          tip="Reasoning steps this agent's models produced, and the reasoning tokens the providers reported."
        />
        <Metric
          label="Flagged tool calls"
          value={d.detection_rate == null ? '—' : `${Math.round(d.detection_rate * 100)}%`}
          sub={
            <>
              {flaggedOf(d.flagged_tool_calls, d.tool_call_count)}
              <br />
              {detectionSummary(d.detection_counts)}
            </>
          }
          tip="This agent's recorded tool calls that drew a block, approval request or logged alert from AgentShield (a call with several verdicts counts once), out of all its recorded tool calls. Replays are not counted."
        />
        <Metric label="Cost" value={fmtCost(d.total_cost_usd)} tip="Token cost of this agent's model calls, priced per model, including cached tokens at their own price." />
      </div>
      <div className="grid-2">
        <Panel title="Who hands work to it" tip="Agents that delegated or handed off to this agent, and how often.">
          {d.handoffs_from.filter((h) => h.kind !== 'return').length === 0 ? (
            <p className="muted small">Nobody: this agent is started directly (for example, by the user).</p>
          ) : (
            <ul className="plain flow-list">
              {d.handoffs_from
                .filter((h) => h.kind !== 'return')
                .map((h) => (
                  <li key={h.agent + h.kind}>
                    <AgentChip name={h.agent} to={agentUrl(pid, h.agent)} size="sm" /> <span className="muted small">{h.kind === 'delegate' ? 'delegated' : 'handed off'}</span>{' '}
                    <strong>{h.count}×</strong>
                  </li>
                ))}
            </ul>
          )}
        </Panel>
        <Panel title="Whom it hands work to" tip="Agents this agent delegated or handed off to, and how often.">
          {d.handoffs_to.filter((h) => h.kind !== 'return').length === 0 ? (
            <p className="muted small">Nobody: this agent does its work itself.</p>
          ) : (
            <ul className="plain flow-list">
              {d.handoffs_to
                .filter((h) => h.kind !== 'return')
                .map((h) => (
                  <li key={h.agent + h.kind}>
                    <AgentChip name={h.agent} to={agentUrl(pid, h.agent)} size="sm" /> <span className="muted small">{h.kind === 'delegate' ? 'delegated' : 'handed off'}</span>{' '}
                    <strong>{h.count}×</strong>
                  </li>
                ))}
            </ul>
          )}
        </Panel>
      </div>
      <div className="grid-2">
        <Panel title="Tools it calls" tip="Each tool this agent called, with how many calls failed.">
          {d.tool_counts.length === 0 ? (
            <p className="muted small">No tool calls recorded.</p>
          ) : (
            <table className="table compact-table">
              <caption className="sr-only">Tools</caption>
              <thead>
                <tr>
                  <th scope="col">Tool</th>
                  <th scope="col">Calls</th>
                  <th scope="col">Failed</th>
                </tr>
              </thead>
              <tbody>
                {d.tool_counts.map((t) => (
                  <tr key={t.tool}>
                    <td>
                      <Link className="mono small" to={sessionsUrl(pid, filterQueryString({ agent: agentName, tool: t.tool, time: 'all' }))}>
                        {t.tool}
                      </Link>
                    </td>
                    <td>
                      <span className="row" style={{ gap: 6 }}>
                        <Bar value={t.count} max={maxTool} label={`${t.count} calls`} /> {t.count}
                      </span>
                    </td>
                    <td className={t.errors ? 'text-error' : ''}>{t.errors}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </Panel>
        <Panel title="Scores" tip="Agent-level scores: the mean of each scorer's results for this agent, from 0 to 1.">
          {Object.keys(d.scores ?? {}).length === 0 ? (
            <p className="muted small">Not scored yet. Open a session and run “Reasoning consistency” at agent scope from its Scores tab.</p>
          ) : (
            <ul className="plain flow-list">
              {Object.entries(d.scores).map(([k, v]) => (
                <li key={k}>
                  <ScorePill value={v.mean} scorer={k} count={v.count} />{' '}
                  <span className="small muted">
                    {humanScorer(k)}, mean of {v.count}
                  </span>
                </li>
              ))}
            </ul>
          )}
        </Panel>
      </div>
      <Panel
        title="Recent sessions"
        actions={
          <Link className="small" to={sessionsUrl(pid, filterQueryString({ agent: agentName, time: 'all' }))}>
            All sessions with {agentName}
          </Link>
        }
      >
        {sessions.data?.items.length ? <SessionList projectId={pid} sessions={sessions.data.items} compact /> : <p className="muted small">No sessions.</p>}
      </Panel>
    </div>
  )
}

export function detectionSummary(c: Partial<Record<string, number>> | null | undefined): string {
  const parts: string[] = []
  if (c?.block) parts.push(`${c.block} blocked`)
  if (c?.require_approval) parts.push(`${c.require_approval} needed approval`)
  if (c?.log) parts.push(`${c.log} logged`)
  return parts.join(' · ') || 'nothing flagged'
}
