// One team: the delegation graph aggregated over its sessions, a per-agent
// table, a per-hand-off table (count, error rate, median time, mean score)
// and recent sessions.
import { Link, useParams } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { v1 } from '../../api/v1'
import { useProjectId } from '../../hooks/useProject'
import { filterQueryString } from '../../lib/filters'
import { fmtCost, fmtNumber, fmtRelative } from '../../lib/format'
import { agentUrl, sessionsUrl, teamsUrl } from '../../lib/urls'
import { ErrorBox, Loading } from '../../components/common'
import { AgentGraphView, HandoffTable } from '../../components/ui/AgentGraphView'
import { AgentChip, Callout, Crumbs, DetectionBadges, Metric, Panel } from '../../components/ui/bits'
import { usePageHelp } from '../../components/ui/PageHelp'
import { SessionList } from '../../components/sessions/SessionList'
import { teamName } from './TeamsPage'

export function TeamPage() {
  const pid = useProjectId()
  const { teamId = '' } = useParams()
  const help = usePageHelp(
    'team',
    'Team',
    <>
      How work flows through this team across all its sessions. The graph shows each agent and the hand-offs between them; the hand-off table is sorted with the most failing route
      first, so problems between agents stand out.
    </>,
    'API.md#teams',
  )
  const t = useQuery({ queryKey: ['v1', pid, 'team', teamId], queryFn: () => v1.team(pid, teamId) })
  if (t.isLoading) return <Loading />
  if (t.error)
    return (
      <div className="page">
        <Crumbs items={[{ label: 'Teams', to: teamsUrl(pid) }, { label: teamId }]} />
        <ErrorBox error={t.error} />
      </div>
    )
  const d = t.data!
  const worst = [...d.handoff_stats].filter((e) => e.errors > 0).sort((a, b) => b.error_rate - a.error_rate || b.errors - a.errors)[0]
  const failRate = d.session_count ? d.error_session_count / d.session_count : 0
  return (
    <div className="page wide">
      <Crumbs items={[{ label: 'Teams', to: teamsUrl(pid) }, { label: teamName(d) }]} />
      <header className="page-head">
        <div className="grow">
          <h1>{teamName(d)}</h1>
          <div className="row wrap small muted" style={{ gap: 6, marginTop: 6 }}>
            {d.agents.map((a) => (
              <AgentChip key={a} name={a} to={agentUrl(pid, a)} size="sm" />
            ))}
            <span>{d.auto ? '· grouped automatically from the agents in each session' : ''}</span>
          </div>
        </div>
        {help.button}
      </header>
      {help.panel}
      <div className="metrics">
        <Metric label="Sessions" value={fmtNumber(d.session_count)} tip="Sessions this team took part in." />
        <Metric
          label="Failed sessions"
          value={`${(failRate * 100).toFixed(0)}%`}
          sub={`${d.error_session_count} of ${d.session_count}`}
          tone={d.error_session_count ? 'error' : undefined}
          tip="Sessions that ended with an error (a top-level run or an agent invocation failed)."
        />
        <Metric label="Hand-offs" value={fmtNumber(d.handoff_count)} tip="Times an agent of this team passed work to another (returns excluded)." />
        <Metric label="Tokens" value={fmtNumber(d.input_tokens + d.output_tokens)} tip="Input plus output tokens over all the team's sessions." />
        <Metric label="Cost" value={fmtCost(d.cost_usd)} tip="Token cost over all sessions, priced per model including cached tokens." />
        <Metric
          label="Security"
          value={Object.values(d.detection_count ?? {}).some((v) => (v ?? 0) > 0) ? <DetectionBadges counts={d.detection_count} /> : <span className="muted">nothing flagged</span>}
          tip="AgentShield verdicts on this team's tool calls."
        />
      </div>
      {worst && (
        <Callout
          tone="error"
          title={
            <>
              Most failing hand-off: <AgentChip name={worst.from} size="sm" /> → <AgentChip name={worst.to} size="sm" />
            </>
          }
          actions={
            <Link className="btn sm" to={sessionsUrl(pid, filterQueryString({ team: d.team_id, agent: worst.to, status: 'error', time: 'all' }))}>
              Show failed sessions
            </Link>
          }
        >
          {(worst.error_rate * 100).toFixed(0)}% of {worst.count} hand-offs failed ({worst.errors} failures), typically after{' '}
          {worst.p50_ms != null ? `${(worst.p50_ms / 1000).toFixed(1)} s` : 'an unknown time'}.
        </Callout>
      )}
      <Panel
        title="How work flows"
        tip="Agents sized by how often they ran; arrows are hand-offs labelled with count, failures and median time, aggregated over the team's sessions. Red marks failures or blocked tool calls."
      >
        <AgentGraphView graph={d.graph} agentUrl={(a) => agentUrl(pid, a)} title={`Delegation graph of ${teamName(d)}`} />
      </Panel>
      <Panel
        title="Hand-offs"
        tip="Each route between two agents, most failing first. Error rate: share of hand-offs whose receiving agent ended with an error. Median time: from hand-off to the delegate finishing. Mean score: hand-off quality scores, when scored."
      >
        <HandoffTable edges={d.handoff_stats} agentUrl={(a) => agentUrl(pid, a)} />
      </Panel>
      <Panel title="Agents" tip="Per agent, over this team's sessions.">
        <div className="table-wrap">
          <table className="table">
            <caption className="sr-only">Agents of this team</caption>
            <thead>
              <tr>
                <th scope="col">Agent</th>
                <th scope="col">Sessions</th>
                <th scope="col">Runs</th>
                <th scope="col">Errors</th>
                <th scope="col">Tool calls</th>
                <th scope="col">Handed to it</th>
                <th scope="col">Handed on</th>
                <th scope="col">Cost</th>
                <th scope="col">Security</th>
              </tr>
            </thead>
            <tbody>
              {d.agent_stats.map((a) => (
                <tr key={a.agent}>
                  <td>
                    <AgentChip name={a.agent} to={agentUrl(pid, a.agent)} />
                  </td>
                  <td>{fmtNumber(a.sessions)}</td>
                  <td>{fmtNumber(a.calls)}</td>
                  <td className={a.errors ? 'text-error' : ''}>{fmtNumber(a.errors)}</td>
                  <td>{fmtNumber(a.tool_calls)}</td>
                  <td>{fmtNumber(a.handoffs_in)}</td>
                  <td>{fmtNumber(a.handoffs_out)}</td>
                  <td>{fmtCost(a.cost_usd)}</td>
                  <td>{Object.values(a.detections ?? {}).some((v) => (v ?? 0) > 0) ? <DetectionBadges counts={a.detections} /> : <span className="faint">none</span>}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </Panel>
      <Panel
        title="Recent sessions"
        actions={
          <Link className="small" to={sessionsUrl(pid, filterQueryString({ team: d.team_id, time: 'all' }))}>
            All sessions of this team
          </Link>
        }
      >
        {d.recent_sessions.length ? <SessionList projectId={pid} sessions={d.recent_sessions.slice(0, 10)} compact /> : <p className="muted small">No sessions.</p>}
        <p className="faint small">Last activity {fmtRelative(d.last_activity)}.</p>
      </Panel>
    </div>
  )
}
