// Teams: groups of agents that work together, named with weft.team.id or
// derived from the agents that took part in a session.
import { Link } from 'react-router-dom'
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { v1, type TeamSummary } from '../../api/v1'
import { useProjectId } from '../../hooks/useProject'
import { useSessionScope } from '../../hooks/useSessionScope'
import { ScopeCapNote } from '../../components/ui/FilterBar'
import { timeWindow, type FilterState, type TimeRange } from '../../lib/filters'
import { fmtCost, fmtDate, fmtNumber, fmtRelative } from '../../lib/format'
import { agentUrl, teamUrl } from '../../lib/urls'
import { ErrorBox, Loading } from '../../components/common'
import { FilterBar, useFilters, useWidenedTime, WidenedNote } from '../../components/ui/FilterBar'
import { GuidedEmpty } from '../../components/ui/EmptyState'
import { AgentChip, DetectionBadges, Tip, CapNote } from '../../components/ui/bits'
import { Icon } from '../../components/ui/Icons'
import { HelpLink } from '../../components/ui/PageHeader'

const DEFAULT_TIME: TimeRange = '24h'
/** The most teams one page loads (the server's order: most recent activity first). */
const LIST_LIMIT = 500

/** The chip Teams applies to a team itself; agent, text and time go to the server, the rest through sessions (useSessionScope). */
export function teamMatches(t: TeamSummary, s: FilterState): boolean {
  if (s.team && t.team_id !== s.team) return false
  return true
}

export function teamName(t: Pick<TeamSummary, 'team_id' | 'name' | 'auto'>): string {
  if (t.name) return t.name
  return t.team_id.startsWith('auto:') ? t.team_id.slice(5).replace(/\+/g, ' + ') : t.team_id
}

export function TeamsPage() {
  const pid = useProjectId()
  const [state, setState] = useFilters()
  const base = { q: state.q, agent: state.agent }
  const widen = useWidenedTime(state, DEFAULT_TIME, async (r) => (await v1.teams(pid, { ...base, ...timeWindow({ time: r }, r), limit: 1 })).total_count, [pid, 'teams', base])
  const time = widen.time
  const q = useQuery({
    queryKey: ['v1', pid, 'teams', base, time, state.from, state.to],
    queryFn: () => v1.teams(pid, { ...base, ...timeWindow(state, time), limit: LIST_LIMIT }),
    enabled: widen.ready,
    placeholderData: keepPreviousData,
  })
  const any = useQuery({ queryKey: ['v1', pid, 'teams', 'any'], queryFn: () => v1.teams(pid, { limit: 1 }) })
  const scope = useSessionScope(pid, 'teams', state, time, widen.ready)
  const all = q.data?.items ?? []
  const rows = all.filter((t) => teamMatches(t, state) && (!scope.active || scope.teams.has(t.team_id))).sort((a, b) => Number(a.auto) - Number(b.auto))
  return (
    <div className="page wide">
      <header className="page-head">
        <div className="grow">
          <h1>Teams</h1>
          <p className="page-sub">Agents that work together, and how well they hand work to each other.</p>
        </div>
        <HelpLink doc="teams.md" topic="Teams" />
      </header>
      {any.data?.total_count === 0 ? (
        <GuidedEmpty projectId={pid} title="No teams yet">
          Teams appear once a session has agents. Add <span className="mono">weft.team.id</span> and <span className="mono">weft.team.name</span> to any span to name the team.
        </GuidedEmpty>
      ) : (
        <>
          <FilterBar
            page="teams"
            defaultTime={DEFAULT_TIME}
            effectiveTime={time}
            options={{ agents: [...new Set(all.flatMap((t) => t.agents))], teams: all.map((t) => ({ id: t.team_id, name: teamName(t) })) }}
            placeholder="Filter teams…  try agent:researcher or status:failed"
          />
          {widen.widened && <WidenedNote time={time} defaultTime={DEFAULT_TIME} onReset={() => setState({ ...state, time: DEFAULT_TIME })} />}
          <ErrorBox error={q.error} />
          <ErrorBox error={scope.error} />
          {scope.truncated && <ScopeCapNote total={scope.total} />}
          {q.data && <CapNote shown={q.data.items.length} total={q.data.total_count} what="teams" why="the most recently active ones; the filters apply to these. Narrow the time range or search by name to see the rest" />}
          {q.isLoading || !scope.ready ? (
            <Loading />
          ) : rows.length === 0 ? (
            <div className="empty-inline">
              <strong className="grow">No teams match these filters.</strong>
              <button className="btn sm" onClick={() => setState({})}>
                Clear filters
              </button>
            </div>
          ) : (
            <ul className="team-grid" aria-label="Teams">
              {rows.map((t) => {
                const failRate = t.session_count ? t.error_session_count / t.session_count : 0
                return (
                  <li key={t.team_id} className="team-card">
                    <Link to={teamUrl(pid, t.team_id)} className="team-card-link">
                      <span className="team-card-title">
                        <Icon name="teams" size={16} /> {teamName(t)}
                      </span>
                      {t.auto && <span className="faint small">grouped automatically</span>}
                    </Link>
                    <div className="row wrap" style={{ gap: 4 }}>
                      {t.agents.map((a) => (
                        <AgentChip key={a} name={a} to={agentUrl(pid, a)} size="sm" />
                      ))}
                    </div>
                    <dl className="team-figures">
                      <div>
                        <dt>Sessions</dt>
                        <dd>{fmtNumber(t.session_count)}</dd>
                      </div>
                      <div>
                        <dt>
                          Failed <Tip text="Sessions of this team that ended with an error, out of all its sessions." />
                        </dt>
                        <dd className={t.error_session_count ? 'text-error' : ''}>
                          {fmtNumber(t.error_session_count)} <span className="faint small">({(failRate * 100).toFixed(0)}%)</span>
                        </dd>
                      </div>
                      <div>
                        <dt>Hand-offs</dt>
                        <dd>{fmtNumber(t.handoff_count)}</dd>
                      </div>
                      <div>
                        <dt>Cost</dt>
                        <dd>{fmtCost(t.cost_usd)}</dd>
                      </div>
                    </dl>
                    <div className="row wrap" style={{ gap: 6, justifyContent: 'space-between' }}>
                      <DetectionBadges counts={t.detection_count} />
                      <span className="faint small" title={fmtDate(t.last_activity)}>
                        active {fmtRelative(t.last_activity)}
                      </span>
                    </div>
                  </li>
                )
              })}
            </ul>
          )}
        </>
      )}
    </div>
  )
}
