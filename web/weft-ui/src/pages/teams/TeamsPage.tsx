// Teams (DESIGN.md §7, §7.2): one sentence answering "which teams are
// working, and which are in trouble?", naming those that need a look, then
// the grouped lists "Needs a look" and "Fine". A team is named with
// weft.team.id or grouped from the agents that took part in a session.
import { useMemo, useState } from 'react'
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { v1, type TeamSummary } from '../../api/v1'
import { useProjectId } from '../../hooks/useProject'
import { useSessionScope } from '../../hooks/useSessionScope'
import { isEmptyFilter, removeKey, timeWindow, type FilterState, type TimeRange } from '../../lib/filters'
import { teamName, teamNeedsALook, teamsSentence } from '../../lib/summaries/agentsTeams'
import { ErrorBox } from '../../components/common'
import { FilterBar, FilterButton, FilterSearchField, ScopeCapNote, useFilters, useWidenedTime, WidenedNote } from '../../components/ui/FilterBar'
import { GuidedEmpty } from '../../components/ui/EmptyState'
import { GroupedList } from '../../components/ui/GroupedList'
import { PageHeader } from '../../components/ui/PageHeader'
import { Segmented } from '../../components/ui/Segmented'
import { Sentence } from '../../components/ui/Sentence'
import { CapNote } from '../../components/ui/bits'
import { RANGES, RowsSkeleton, TeamRow, toSentenceParts } from '../../components/agents/AgentRows'
import '../agents/agents-teams.css'

export { teamName }

const DEFAULT_TIME: TimeRange = '24h'
/** The most teams one page loads (the server's order: most recent activity first). */
const LIST_LIMIT = 500

/** The chip Teams applies to a team itself; agent, text and time go to the server, the rest through sessions (useSessionScope). */
export function teamMatches(t: TeamSummary, s: FilterState): boolean {
  if (s.team && t.team_id !== s.team) return false
  return true
}

export function TeamsPage() {
  const pid = useProjectId()
  const [state, setState] = useFilters()
  const [filterOpen, setFilterOpen] = useState(false)
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
  const all = useMemo(() => q.data?.items ?? [], [q.data])
  // Named teams first, then the ones Weft grouped automatically.
  const rows = all.filter((t) => teamMatches(t, state) && (!scope.active || scope.teams.has(t.team_id))).sort((a, b) => Number(a.auto) - Number(b.auto))
  const look = rows.filter(teamNeedsALook)
  const fine = rows.filter((t) => !teamNeedsALook(t))
  const options = useMemo(() => ({ agents: [...new Set(all.flatMap((t) => t.agents))], teams: all.map((t) => ({ id: t.team_id, name: teamName(t) })) }), [all])
  const projectEmpty = any.data?.total_count === 0
  const loading = q.isLoading || !scope.ready || !widen.ready
  const filtered = !isEmptyFilter({ ...state, time: undefined, from: undefined, to: undefined }, DEFAULT_TIME)

  const header = (
    <PageHeader
      title="Teams"
      help="teams.md"
      controls={
        projectEmpty ? undefined : (
          <>
            <Segmented
              label="Time range"
              options={RANGES}
              value={(RANGES.some((r) => r.id === time) ? time : null) as TimeRange | null}
              onChange={(t) => setState({ ...removeKey(state, 'time'), time: t === DEFAULT_TIME && !widen.widened ? undefined : t })}
            />
            <FilterSearchField label="Search teams" />
            <FilterButton open={filterOpen} onToggle={() => setFilterOpen((o) => !o)} defaultTime={DEFAULT_TIME} />
          </>
        )
      }
      sentence={
        projectEmpty ? undefined : loading ? (
          <p className="sentence s-outcome sk-sentence" aria-busy="true">
            <span className="sr-only">Counting teams…</span>
            <span className="sk sk-line" aria-hidden />
          </p>
        ) : (
          <Sentence parts={toSentenceParts(teamsSentence({ teams: rows, time, filtered }), pid)} />
        )
      }
    />
  )

  if (projectEmpty)
    return (
      <div className="page-frame">
        {header}
        <GuidedEmpty projectId={pid} title="No teams yet">
          Teams appear once a session has agents. To give a team a name, send it with each run; the setup guide shows how.
        </GuidedEmpty>
      </div>
    )

  return (
    <div className="page-frame">
      {header}
      <FilterBar page="teams" search="external" timePicker="none" collapsible open={filterOpen} defaultTime={DEFAULT_TIME} effectiveTime={time} options={options} />
      {widen.widened && <WidenedNote time={time} defaultTime={DEFAULT_TIME} onReset={() => setState({ ...state, time: DEFAULT_TIME })} />}
      <ErrorBox error={q.error} />
      <ErrorBox error={scope.error} />
      {scope.truncated && <ScopeCapNote total={scope.total} />}
      {q.data && <CapNote shown={q.data.items.length} total={q.data.total_count} what="teams" why="the most recently active ones; the filters apply to these. Narrow the time range or search by name to see the rest" />}
      {loading ? (
        <RowsSkeleton label="Loading teams" />
      ) : rows.length === 0 ? (
        <div className="quiet-empty">
          <p>No teams match these filters.</p>
          <p className="quiet-note">Try a longer time range, or remove a filter.</p>
          <button type="button" className="qbtn" onClick={() => setState({})}>
            Clear filters
          </button>
        </div>
      ) : (
        <>
          {look.length > 0 && (
            <GroupedList title="Needs a look" count={look.length} className="team-list">
              {look.map((t) => (
                <TeamRow key={t.team_id} pid={pid} t={t} />
              ))}
            </GroupedList>
          )}
          {fine.length > 0 && (
            <GroupedList title="Fine" count={fine.length} className="team-list">
              {fine.map((t) => (
                <TeamRow key={t.team_id} pid={pid} t={t} />
              ))}
            </GroupedList>
          )}
        </>
      )}
    </div>
  )
}
