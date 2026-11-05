// Agents (DESIGN.md §7, §7.1 rule 1, §7.2): one sentence answering "which
// agents are working, and which are in trouble?", naming those that need a
// look, then the grouped lists "Needs a look" and "Fine". Each row says in
// one generated line how much the agent ran and what went wrong. The time
// range, search and filters live in the URL; the chips sit behind "Filter".
import { useMemo, useState } from 'react'
import { Link, Navigate, useSearchParams } from 'react-router-dom'
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { v1, type AgentSummary } from '../../api/v1'
import { useProjectId } from '../../hooks/useProject'
import { useSessionScope } from '../../hooks/useSessionScope'
import { isEmptyFilter, isFlagged, removeKey, scoreBelowMatches, timeWindow, type FilterState, type TimeRange } from '../../lib/filters'
import { agentNeedsALook, agentsSentence } from '../../lib/summaries/agentsTeams'
import { agentAnalyticsUrl } from '../../lib/urls'
import { ErrorBox } from '../../components/common'
import { FilterBar, FilterButton, FilterSearchField, ScopeCapNote, useFilters, useWidenedTime, WidenedNote } from '../../components/ui/FilterBar'
import { GuidedEmpty } from '../../components/ui/EmptyState'
import { GroupedList } from '../../components/ui/GroupedList'
import { PageHeader } from '../../components/ui/PageHeader'
import { Segmented } from '../../components/ui/Segmented'
import { Sentence } from '../../components/ui/Sentence'
import { CapNote } from '../../components/ui/bits'
import { AgentRow, RANGES, RowsSkeleton, toSentenceParts } from '../../components/agents/AgentRows'
import './agents-teams.css'

const DEFAULT_TIME: TimeRange = '24h'
/** The most agents one page loads (the server's order: most recent activity first). */
const LIST_LIMIT = 500

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

/** Tabs the Agents page had before analytics moved to its own page: their URLs open it. */
const ANALYTICS_TABS = ['dashboard', 'agents', 'spans', 'signals', 'evals']

export function AgentsListPage() {
  const [params] = useSearchParams()
  const pid = useProjectId()
  if (ANALYTICS_TABS.includes(params.get('tab') ?? '')) return <Navigate replace to={`${agentAnalyticsUrl(pid)}?${params}`} />
  return <AgentsList />
}

function AgentsList() {
  const pid = useProjectId()
  const [state, setState] = useFilters()
  const [filterOpen, setFilterOpen] = useState(false)
  const base = { q: state.q, team_id: state.team }
  const widen = useWidenedTime(state, DEFAULT_TIME, async (r) => (await v1.agents(pid, { ...base, ...timeWindow({ time: r }, r), limit: 1 })).total_count, [pid, 'agents', base])
  const time = widen.time
  const q = useQuery({
    queryKey: ['v1', pid, 'agents', base, time, state.from, state.to],
    queryFn: () => v1.agents(pid, { ...base, ...timeWindow(state, time), limit: LIST_LIMIT }),
    enabled: widen.ready,
    placeholderData: keepPreviousData,
  })
  const any = useQuery({ queryKey: ['v1', pid, 'agents', 'any'], queryFn: () => v1.agents(pid, { limit: 1 }) })
  const teams = useQuery({ queryKey: ['v1', pid, 'teams', 'options'], queryFn: () => v1.teams(pid, { limit: 200 }), staleTime: 60_000, enabled: filterOpen })
  const scope = useSessionScope(pid, 'agents', state, time, widen.ready)
  const all = useMemo(() => q.data?.items ?? [], [q.data])
  const rows = all.filter((a) => agentMatches(a, state) && (!scope.active || scope.agents.has(a.agent_name)))
  const look = rows.filter(agentNeedsALook)
  const fine = rows.filter((a) => !agentNeedsALook(a))
  const options = useMemo(
    () => ({
      agents: all.map((a) => a.agent_name),
      models: [...new Set(all.flatMap((a) => a.models ?? []))],
      tools: [...new Set(all.flatMap((a) => a.tools ?? []))],
      teams: (teams.data?.items ?? []).filter((t) => !t.auto).map((t) => ({ id: t.team_id, name: t.name })),
    }),
    [all, teams.data],
  )
  const projectEmpty = any.data?.total_count === 0
  const loading = q.isLoading || !scope.ready || !widen.ready
  const filtered = !isEmptyFilter({ ...state, time: undefined, from: undefined, to: undefined }, DEFAULT_TIME)

  const header = (
    <PageHeader
      title="Agents"
      help="agents.md"
      controls={
        projectEmpty ? undefined : (
          <>
            <Segmented
              label="Time range"
              options={RANGES}
              value={(RANGES.some((r) => r.id === time) ? time : null) as TimeRange | null}
              onChange={(t) => setState({ ...removeKey(state, 'time'), time: t === DEFAULT_TIME && !widen.widened ? undefined : t })}
            />
            <FilterSearchField label="Search agents" />
            <FilterButton open={filterOpen} onToggle={() => setFilterOpen((o) => !o)} defaultTime={DEFAULT_TIME} />
          </>
        )
      }
      sentence={
        projectEmpty ? undefined : loading ? (
          <p className="sentence s-outcome sk-sentence" aria-busy="true">
            <span className="sr-only">Counting agents…</span>
            <span className="sk sk-line" aria-hidden />
          </p>
        ) : (
          <Sentence parts={toSentenceParts(agentsSentence({ agents: rows, time, filtered }), pid)} />
        )
      }
    />
  )

  if (projectEmpty)
    return (
      <div className="page-frame">
        {header}
        <GuidedEmpty projectId={pid} title="No agents yet">
          Agents appear here once your agents send their name with each run; the setup snippets do this for you. Hand-offs between them are found automatically.
        </GuidedEmpty>
      </div>
    )

  return (
    <div className="page-frame">
      {header}
      <FilterBar
        page="agents"
        search="external"
        timePicker="none"
        collapsible
        open={filterOpen}
        defaultTime={DEFAULT_TIME}
        effectiveTime={time}
        options={options}
      />
      {widen.widened && <WidenedNote time={time} defaultTime={DEFAULT_TIME} onReset={() => setState({ ...state, time: DEFAULT_TIME })} />}
      <ErrorBox error={q.error} />
      <ErrorBox error={scope.error} />
      {scope.truncated && <ScopeCapNote total={scope.total} />}
      {q.data && <CapNote shown={q.data.items.length} total={q.data.total_count} what="agents" why="the most recently active ones; the filters apply to these. Narrow the time range or search by name to see the rest" />}
      {loading ? (
        <RowsSkeleton label="Loading agents" />
      ) : rows.length === 0 ? (
        <div className="quiet-empty">
          <p>No agents match these filters.</p>
          <p className="quiet-note">Try a longer time range, or remove a filter.</p>
          <button type="button" className="qbtn" onClick={() => setState({})}>
            Clear filters
          </button>
        </div>
      ) : (
        <>
          {look.length > 0 && (
            <GroupedList title="Needs a look" count={look.length} className="agent-list">
              {look.map((a) => (
                <AgentRow key={a.agent_name} pid={pid} a={a} />
              ))}
            </GroupedList>
          )}
          {fine.length > 0 && (
            <GroupedList title="Fine" count={fine.length} className="agent-list">
              {fine.map((a) => (
                <AgentRow key={a.agent_name} pid={pid} a={a} />
              ))}
            </GroupedList>
          )}
        </>
      )}
      <p className="at-foot">
        Charts, every step, ratings and evaluations are in <Link to={agentAnalyticsUrl(pid)}>agent analytics</Link>.
      </p>
    </div>
  )
}
