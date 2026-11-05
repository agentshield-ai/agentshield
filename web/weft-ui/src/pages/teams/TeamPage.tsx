// One team (DESIGN.md §7, §7.2): a sentence that leads with the hand-off
// that fails most ("Trip planners worked in 7 sessions and 3 failed, each
// time at the hand-off from orchestrator to researcher, which failed 3 of 5
// times, usually after 1.3 seconds"), its clauses linking to the failed
// sessions; one quiet line of figures; then how work flows (the delegation
// graph, with the hand-offs and the agents as plain tables behind "Show as a
// table"; on a phone the tables stand in for it), and the team's recent
// sessions.
import { Link, useParams } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { v1, type TeamDetail } from '../../api/v1'
import { useProjectId } from '../../hooks/useProject'
import { filterQueryString } from '../../lib/filters'
import { fmtCost, fmtNumber } from '../../lib/format'
import { teamMetaFigures, teamName, teamOutcome } from '../../lib/summaries/agentsTeams'
import { agentUrl, sessionsUrl, teamsUrl } from '../../lib/urls'
import { ErrorBox } from '../../components/common'
import { GraphWithTables, HandoffTable } from '../../components/ui/AgentGraphView'
import { PageHeader } from '../../components/ui/PageHeader'
import { PlainTable } from '../../components/ui/PlainTable'
import { Sentence } from '../../components/ui/Sentence'
import { SessionList } from '../../components/sessions/SessionList'
import { toSentenceParts, useSessionRowContext } from '../../components/agents/AgentRows'
import '../agents/agents-teams.css'

export function TeamPage() {
  const pid = useProjectId()
  const { teamId = '' } = useParams()
  const t = useQuery({ queryKey: ['v1', pid, 'team', teamId], queryFn: () => v1.team(pid, teamId) })
  const ctx = useSessionRowContext(pid, t.data?.recent_sessions.slice(0, 10))
  const back = { to: teamsUrl(pid), label: 'Teams' }
  const fallbackTitle = teamName({ team_id: teamId, name: '', auto: teamId.startsWith('auto:') })
  if (t.isLoading)
    return (
      <div className="page-frame">
        <PageHeader
          variant="detail"
          back={back}
          title={fallbackTitle}
          help="teams.md#find-the-hand-off-that-fails-most"
          helpTopic="Team"
          sentence={
            <p className="sentence s-detail sk-sentence" aria-busy="true">
              <span className="sr-only">Reading the team…</span>
              <span className="sk sk-line" aria-hidden />
            </p>
          }
        />
      </div>
    )
  if (t.error || !t.data)
    return (
      <div className="page-frame">
        <PageHeader variant="detail" back={back} title={fallbackTitle} help="teams.md#find-the-hand-off-that-fails-most" helpTopic="Team" />
        <ErrorBox error={t.error} />
      </div>
    )
  const d = t.data
  const name = teamName(d)
  return (
    <div className="page-frame at-page">
      <PageHeader
        variant="detail"
        back={back}
        title={name}
        help="teams.md#find-the-hand-off-that-fails-most"
        helpTopic="Team"
        sentence={<Sentence size="detail" parts={toSentenceParts(teamOutcome(d), pid)} />}
        meta={teamMetaFigures(d).join(' · ')}
      />

      <section className="at-section" aria-labelledby="flow-h">
        <h2 id="flow-h" className="at-h">
          How work flows
        </h2>
        <GraphWithTables
          graph={d.graph}
          agentUrl={(a) => agentUrl(pid, a)}
          title={`Delegation graph of ${name}`}
          panelClassName="at-panel"
          tables={
            <>
              <section className="at-section" aria-labelledby="handoffs-h">
                <h3 id="handoffs-h" className="at-h">
                  Hand-offs
                </h3>
                <HandoffTable edges={d.handoff_stats} agentUrl={(a) => agentUrl(pid, a)} />
              </section>
              <section className="at-section" aria-labelledby="agents-h">
                <h3 id="agents-h" className="at-h">
                  Agents
                </h3>
                <TeamAgents pid={pid} d={d} />
              </section>
            </>
          }
        />
      </section>

      <section className="at-section" aria-labelledby="recent-h">
        <div className="at-h-line">
          <h2 id="recent-h" className="at-h">
            Recent sessions
          </h2>
          <Link className="at-h-link" to={sessionsUrl(pid, filterQueryString({ team: d.team_id, time: 'all' }))}>
            All sessions of this team
          </Link>
        </div>
        {d.recent_sessions.length ? (
          <SessionList projectId={pid} sessions={d.recent_sessions.slice(0, 10)} ctx={ctx} label={`Recent sessions of ${name}`} />
        ) : (
          <p className="quiet-note">No sessions.</p>
        )}
      </section>
    </div>
  )
}

/** Verdicts as words: "1 blocked, 2 needed approval", or "none". */
function verdictText(c: Partial<Record<string, number>> | null | undefined): string {
  const xs: string[] = []
  if (c?.block) xs.push(`${c.block} blocked`)
  if (c?.require_approval) xs.push(`${c.require_approval} needed approval`)
  if (c?.log) xs.push(`${c.log} logged`)
  return xs.join(', ') || 'none'
}

/** The team's agents: runs and failed runs (counted as the Agents page counts them), cost, and AgentShield's verdicts when there are any. */
function TeamAgents({ pid, d }: { pid: string; d: TeamDetail }) {
  const shield = d.agent_stats.some((a) => (a.detections?.block ?? 0) + (a.detections?.require_approval ?? 0) + (a.detections?.log ?? 0) > 0)
  return (
    <PlainTable caption={`Agents of ${teamName(d)}, over the team's sessions`} className="team-agents-table" stack>
      <thead>
        <tr>
          <th scope="col">Agent</th>
          <th scope="col" className="num" data-stack="session|sessions">
            Sessions
          </th>
          <th scope="col" className="num" data-stack="run|runs">
            Runs
          </th>
          <th scope="col" className="num" title="Runs that failed, counted as the Agents page counts them">
            Failed
          </th>
          <th scope="col" className="num" data-stack="">
            Cost
          </th>
          {shield && (
            <th scope="col" data-stack-prefix="AgentShield:">
              AgentShield
            </th>
          )}
        </tr>
      </thead>
      <tbody>
        {d.agent_stats.map((a) => (
          <tr key={a.agent}>
            <td>
              <Link to={agentUrl(pid, a.agent)}>{a.agent}</Link>
            </td>
            <td className="num">{fmtNumber(a.sessions)}</td>
            <td className="num">{fmtNumber(a.calls)}</td>
            <td className="num">{fmtNumber(a.failed_calls ?? a.errors)}</td>
            <td className="num">{fmtCost(a.cost_usd)}</td>
            {shield && <td>{verdictText(a.detections)}</td>}
          </tr>
        ))}
      </tbody>
    </PlainTable>
  )
}
