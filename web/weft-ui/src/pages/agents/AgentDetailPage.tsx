// One agent (DESIGN.md §7, §7.2): a sentence answering "how is this agent
// doing, and why?" (how much it ran, whether its runs failed and in which
// tool, what AgentShield flagged, a low score), one quiet line of figures,
// then who hands work to it and whom it hands work to, the tools it calls
// (a plain table), its scores and its recent sessions.
import type { ReactNode } from 'react'
import { Link, useParams } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { v1, type AgentDetail } from '../../api/v1'
import { useProjectId } from '../../hooks/useProject'
import { filterQueryString } from '../../lib/filters'
import { fmtNumber } from '../../lib/format'
import { scorerWords, shortDuration } from '../../lib/summaries'
import { agentMetaFigures, agentOutcome, handoffWords, teamName } from '../../lib/summaries/agentsTeams'
import { agentAnalyticsUrl, agentsUrlFor, agentUrl, sessionsUrl, teamUrl } from '../../lib/urls'
import { ErrorBox } from '../../components/common'
import { GroupedList, ListRow } from '../../components/ui/GroupedList'
import { Monogram } from '../../components/ui/Monogram'
import { PageHeader } from '../../components/ui/PageHeader'
import { PlainTable } from '../../components/ui/PlainTable'
import { Sentence } from '../../components/ui/Sentence'
import { SessionList, SessionListSkeleton } from '../../components/sessions/SessionList'
import { toSentenceParts, useSessionRowContext } from '../../components/agents/AgentRows'
import './agents-teams.css'

export function AgentDetailPage() {
  const pid = useProjectId()
  const { agentName = '' } = useParams()
  const a = useQuery({ queryKey: ['v1', pid, 'agent', agentName], queryFn: () => v1.agent(pid, agentName) })
  const sessions = useQuery({ queryKey: ['v1', pid, 'sessions', { agent: agentName }], queryFn: () => v1.sessions(pid, { agent: agentName, limit: 10 }) })
  // Team names for the meta line (the agent lists its teams by id).
  const teams = useQuery({ queryKey: ['v1', pid, 'teams', 'options'], queryFn: () => v1.teams(pid, { limit: 200 }), staleTime: 60_000 })
  const ctx = useSessionRowContext(pid, sessions.data?.items)
  const back = { to: agentsUrlFor(pid), label: 'Agents' }

  if (a.isLoading)
    return (
      <div className="page-frame">
        <PageHeader
          variant="detail"
          back={back}
          title={agentName}
          help="agents.md#look-at-one-agent"
          helpTopic="Agent"
          sentence={
            <p className="sentence s-detail sk-sentence" aria-busy="true">
              <span className="sr-only">Reading the agent…</span>
              <span className="sk sk-line" aria-hidden />
            </p>
          }
        />
      </div>
    )
  if (a.error || !a.data)
    return (
      <div className="page-frame">
        <PageHeader variant="detail" back={back} title={agentName} help="agents.md#look-at-one-agent" helpTopic="Agent" />
        <ErrorBox error={a.error} />
      </div>
    )
  const d = a.data
  const names = new Map((teams.data?.items ?? []).map((t) => [t.team_id, teamName(t)]))
  const meta = metaLine(pid, d, names)

  return (
    <div className="page-frame at-page">
      <PageHeader
        variant="detail"
        back={back}
        title={
          <span className="at-title">
            <Monogram name={d.agent_name} size={34} />
            <span>{d.agent_name}</span>
          </span>
        }
        helpTopic="Agent"
        help="agents.md#look-at-one-agent"
        sentence={<Sentence size="detail" parts={toSentenceParts(agentOutcome(d), pid)} />}
        meta={meta}
      />

      <div className="at-two">
        <HandoffList pid={pid} title="Who hands work to it" items={d.handoffs_from} none="Nobody: it is started directly (for example, by the user)." />
        <HandoffList pid={pid} title="Whom it hands work to" items={d.handoffs_to} none="Nobody: it does its work itself." />
      </div>

      <section className="at-section" aria-labelledby="tools-h">
        <h2 id="tools-h" className="at-h">
          Tools it calls
        </h2>
        {d.tool_counts.length === 0 ? (
          <p className="quiet-note">No tool calls recorded.</p>
        ) : (
          <PlainTable caption={`Tools ${d.agent_name} calls, with how many calls failed`} className="tools-table">
            <thead>
              <tr>
                <th scope="col">Tool</th>
                <th scope="col" className="num">
                  Calls
                </th>
                <th scope="col" className="num">
                  Failed
                </th>
              </tr>
            </thead>
            <tbody>
              {d.tool_counts.map((t) => (
                <tr key={t.tool}>
                  <td>
                    <Link className="mono" to={sessionsUrl(pid, filterQueryString({ agent: agentName, tool: t.tool, time: 'all' }))} title={`Sessions where ${agentName} called ${t.tool}`}>
                      {t.tool}
                    </Link>
                  </td>
                  <td className="num">{fmtNumber(t.count)}</td>
                  <td className={`num ${t.errors ? 'text-error' : ''}`}>{fmtNumber(t.errors)}</td>
                </tr>
              ))}
            </tbody>
          </PlainTable>
        )}
      </section>

      <section className="at-section" aria-labelledby="scores-h">
        <h2 id="scores-h" className="at-h">
          Scores
        </h2>
        {Object.keys(d.scores ?? {}).length === 0 ? (
          <p className="quiet-note">Not scored yet. Open one of its sessions and run “Reasoning consistency” for each agent from the session's Scores view.</p>
        ) : (
          <PlainTable caption="Agent-level scores: each scorer's mean for this agent, from 0 to 1">
            <thead>
              <tr>
                <th scope="col">Scorer</th>
                <th scope="col" className="num">
                  Mean
                </th>
                <th scope="col" className="num">
                  Results
                </th>
              </tr>
            </thead>
            <tbody>
              {Object.entries(d.scores).map(([k, v]) => (
                <tr key={k}>
                  <td title={k}>{scorerWords(k)}</td>
                  <td className={`num ${v.mean < 0.5 ? 'text-error' : ''}`}>{v.mean.toFixed(2)}</td>
                  <td className="num">{fmtNumber(v.count)}</td>
                </tr>
              ))}
            </tbody>
          </PlainTable>
        )}
      </section>

      <section className="at-section" aria-labelledby="recent-h">
        <div className="at-h-line">
          <h2 id="recent-h" className="at-h">
            Recent sessions
          </h2>
          <Link className="at-h-link" to={sessionsUrl(pid, filterQueryString({ agent: agentName, time: 'all' }))}>
            All sessions with {agentName}
          </Link>
        </div>
        {sessions.isLoading ? (
          <SessionListSkeleton rows={3} />
        ) : sessions.data?.items.length ? (
          <SessionList projectId={pid} sessions={sessions.data.items} ctx={ctx} label={`Recent sessions with ${agentName}`} />
        ) : (
          <p className="quiet-note">No sessions.</p>
        )}
        <ErrorBox error={sessions.error} />
      </section>
      <p className="at-foot">
        Charts, spans, signals and agent evaluations of {d.agent_name} are in <Link to={agentAnalyticsUrl(pid, agentName)}>its analytics</Link>.
      </p>
    </div>
  )
}

/** The quiet line of figures: models · median run · teams (linked) · reasoning · cost · last seen. */
function metaLine(pid: string, d: AgentDetail, names: Map<string, string>): ReactNode {
  const figs = agentMetaFigures(d)
  const items: ReactNode[] = []
  if (d.models?.length) items.push(figs.shift())
  if (d.latency_ms_p50 != null) items.push(<span title="The median time one run takes">median run {shortDuration(d.latency_ms_p50)}</span>)
  for (const t of d.teams ?? [])
    items.push(
      <Link to={teamUrl(pid, t)} title="Open this team">
        {names.get(t) ?? teamName({ team_id: t, name: '', auto: t.startsWith('auto:') })}
      </Link>,
    )
  items.push(...figs)
  return items.map((x, i) => (
    <span key={i}>
      {i > 0 && ' · '}
      {x}
    </span>
  ))
}

function HandoffList({ pid, title, items, none }: { pid: string; title: string; items: AgentDetail['handoffs_from']; none: string }) {
  const rows = items.filter((h) => h.kind !== 'return')
  if (!rows.length)
    return (
      <section className="at-section" aria-label={title}>
        <h2 className="at-h">{title}</h2>
        <p className="quiet-note">{none}</p>
      </section>
    )
  return (
    <GroupedList title={title} className="handoff-list">
      {rows.map((h) => (
        <ListRow key={h.agent + h.kind} glyph={<Monogram name={h.agent} size={26} />} title={h.agent} summary={handoffWords(h)} to={agentUrl(pid, h.agent)} />
      ))}
    </GroupedList>
  )
}
