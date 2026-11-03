// Agents: every agent that ran in the project, with its hand-offs,
// reasoning, errors, AgentShield detection rate and scores.
import { useMemo } from 'react'
import { Link } from 'react-router-dom'
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { v1, type AgentSummary } from '../../api/v1'
import { useProjectId } from '../../hooks/useProject'
import { useSessionScope } from '../../hooks/useSessionScope'
import { ScopeCapNote } from '../../components/ui/FilterBar'
import { isFlagged, scoreBelowMatches, timeWindow, type FilterState, type TimeRange } from '../../lib/filters'
import { flaggedOf, fmtCost, fmtDate, fmtNumber, fmtRelative } from '../../lib/format'
import { agentAnalyticsUrl, agentUrl } from '../../lib/urls'
import { ErrorBox, Loading } from '../../components/common'
import { FilterBar, useFilters, useWidenedTime, WidenedNote } from '../../components/ui/FilterBar'
import { GuidedEmpty } from '../../components/ui/EmptyState'
import { AgentChip, DetectionBadges, ScorePill, Tip } from '../../components/ui/bits'
import { usePageHelp } from '../../components/ui/PageHelp'

const DEFAULT_TIME: TimeRange = '24h'

/** The chips Agents applies to an agent's own figures; the rest go through sessions (useSessionScope). */
export function agentMatches(a: AgentSummary, s: FilterState): boolean {
  if (s.agent && a.agent_name !== s.agent) return false
  if (s.status === 'error' && !(a.error_count > 0)) return false
  if (s.status === 'ok' && a.error_count > 0) return false
  if (s.has === 'block' && !((a.detection_counts?.block ?? 0) > 0)) return false
  if (s.has === 'approval' && !((a.detection_counts?.require_approval ?? 0) > 0)) return false
  if (s.has === 'flagged' && !isFlagged(a.detection_counts)) return false
  if (s.has === 'any' && !Object.values(a.detection_counts ?? {}).some((v) => (v ?? 0) > 0)) return false
  if (s.model && !(a.models ?? []).includes(s.model)) return false
  if (s.tool && !(a.tools ?? []).includes(s.tool)) return false
  if (s.scoreBelow && !scoreBelowMatches(a, s.scoreBelow)) return false
  return true
}

export function AgentsListPage() {
  const pid = useProjectId()
  const [state, setState] = useFilters()
  const help = usePageHelp(
    'agents',
    'Agents',
    <>
      Each <strong>agent</strong> that took part in a session: how often it ran, who handed work to it and whom it handed work to, how much it reasoned, how often AgentShield
      flagged its tool calls, and its scores. Click an agent for its tools and recent sessions.
    </>,
    'agents.md',
  )
  const base = { q: state.q, team_id: state.team }
  const widen = useWidenedTime(state, DEFAULT_TIME, async (r) => (await v1.agents(pid, { ...base, ...timeWindow({ time: r }, r), limit: 1 })).total_count, [pid, 'agents', base])
  const time = widen.time
  const q = useQuery({
    queryKey: ['v1', pid, 'agents', base, time, state.from, state.to],
    queryFn: () => v1.agents(pid, { ...base, ...timeWindow(state, time), limit: 500 }),
    enabled: widen.ready,
    placeholderData: keepPreviousData,
  })
  const any = useQuery({ queryKey: ['v1', pid, 'agents', 'any'], queryFn: () => v1.agents(pid, { limit: 1 }) })
  const teams = useQuery({ queryKey: ['v1', pid, 'teams', 'options'], queryFn: () => v1.teams(pid, { limit: 200 }), staleTime: 60_000 })
  const scope = useSessionScope(pid, 'agents', state, time, widen.ready)
  const all = q.data?.items ?? []
  const rows = all.filter((a) => agentMatches(a, state) && (!scope.active || scope.agents.has(a.agent_name)))
  const options = useMemo(
    () => ({
      agents: all.map((a) => a.agent_name),
      models: [...new Set(all.flatMap((a) => a.models ?? []))],
      tools: [...new Set(all.flatMap((a) => a.tools ?? []))],
      teams: (teams.data?.items ?? []).filter((t) => !t.auto).map((t) => ({ id: t.team_id, name: t.name })),
    }),
    [all, teams.data],
  )

  return (
    <div className="page wide">
      <header className="page-head">
        <div className="grow">
          <h1>Agents</h1>
          <p className="page-sub">Who does the work, how they hand it on, and how safely.</p>
        </div>
        <Link className="btn ghost sm" to={agentAnalyticsUrl(pid)} title="Charts, span search, signals and agent evaluations">
          Analytics and spans
        </Link>
        {help.button}
      </header>
      {help.panel}
      {any.data?.total_count === 0 ? (
        <GuidedEmpty projectId={pid} title="No agents yet">
          Agents appear when spans carry an agent name (<span className="mono">gen_ai.agent.name</span> on <span className="mono">invoke_agent</span> spans, or the SDKs'
          conversation API). Hand-offs between them are detected automatically.
        </GuidedEmpty>
      ) : (
        <>
          <FilterBar page="agents" defaultTime={DEFAULT_TIME} effectiveTime={time} options={options} placeholder="Filter agents…  try has:block or tool:web_search" />
          {widen.widened && <WidenedNote time={time} defaultTime={DEFAULT_TIME} onReset={() => setState({ ...state, time: DEFAULT_TIME })} />}
          <ErrorBox error={q.error} />
          <ErrorBox error={scope.error} />
          {scope.truncated && <ScopeCapNote total={scope.total} />}
          {q.isLoading || !scope.ready ? (
            <Loading />
          ) : rows.length === 0 ? (
            <div className="empty-inline">
              <strong className="grow">No agents match these filters.</strong>
              <button className="btn sm" onClick={() => setState({})}>
                Clear filters
              </button>
            </div>
          ) : (
            <div className="table-wrap">
              <table className="table agents-table">
                <caption className="sr-only">Agents</caption>
                <thead>
                  <tr>
                    <th scope="col">Agent</th>
                    <th scope="col">Sessions</th>
                    <th scope="col">
                      Runs <Tip text="Invocations of the agent (invoke_agent spans)." />
                    </th>
                    <th scope="col">
                      Hand-offs <Tip text="In: work handed to this agent. Out: work it handed to others. Returns of control are not counted." />
                    </th>
                    <th scope="col">
                      Reasoning <Tip text="Reasoning steps, and reasoning tokens as reported by the provider." />
                    </th>
                    <th scope="col">
                      Errors <Tip text="Spans of this agent that ended with an error, including tool calls it recovered from." />
                    </th>
                    <th scope="col">
                      Flagged <Tip text="The agent's recorded tool calls that drew a non-allow AgentShield verdict (block, approval or log), out of all its recorded tool calls. A call with several verdicts counts once. Replays are not counted." />
                    </th>
                    <th scope="col">Scores</th>
                    <th scope="col">Cost</th>
                    <th scope="col">Last seen</th>
                  </tr>
                </thead>
                <tbody>
                  {rows.map((a) => (
                    <tr key={a.agent_name}>
                      <td>
                        <AgentChip name={a.agent_name} to={agentUrl(pid, a.agent_name)} />
                      </td>
                      <td>{fmtNumber(a.session_count)}</td>
                      <td>{fmtNumber(a.invocation_count)}</td>
                      <td className="nowrap">
                        <span title="handed to this agent">↘ {fmtNumber(a.handoffs_in)}</span> · <span title="handed on by this agent">↗ {fmtNumber(a.handoffs_out)}</span>
                      </td>
                      <td className="nowrap">
                        {fmtNumber(a.reasoning_step_count)} steps{a.reasoning_tokens ? <span className="faint"> · {fmtNumber(a.reasoning_tokens)} tok</span> : null}
                      </td>
                      <td className={a.error_count ? 'text-error' : ''}>{fmtNumber(a.error_count)}</td>
                      <td>
                        <div className="col" style={{ gap: 2 }}>
                          <span title={flaggedOf(a.flagged_tool_calls, a.tool_call_count)}>
                            {a.detection_rate == null ? <span className="faint">—</span> : `${Math.round(a.detection_rate * 100)}%`}
                          </span>
                          {a.detection_rate != null && <span className="faint small">{flaggedOf(a.flagged_tool_calls, a.tool_call_count)}</span>}
                          <DetectionBadges counts={a.detection_counts} />
                        </div>
                      </td>
                      <td>
                        <div className="row wrap" style={{ gap: 4 }}>
                          {Object.entries(a.scores ?? {}).map(([k, v]) => (
                            <ScorePill key={k} scorer={k} value={v.mean} count={v.count} />
                          ))}
                          {!Object.keys(a.scores ?? {}).length && <span className="faint">—</span>}
                        </div>
                      </td>
                      <td>{fmtCost(a.total_cost_usd)}</td>
                      <td className="nowrap" title={fmtDate(a.last_seen)}>
                        {fmtRelative(a.last_seen)}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </>
      )}
    </div>
  )
}
