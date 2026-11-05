// Sessions, the project home (DESIGN.md §7, §7.1 rule 1, §7.2): one
// sentence answering "what happened, and what needs a look?", each clause a
// link to the sessions it counts, then the grouped lists "Needs a look" and
// "Finished". The time range, search and filters live in the URL (§7.1 rule
// 5); the filter chips sit behind the "Filter" button.
import { useMemo, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { v1, type Detection, type SessionSummary } from '../../api/v1'
import { useProjectId } from '../../hooks/useProject'
import { filterQueryString, isEmptyFilter, removeKey, sessionsParams, type FilterState, type TimeRange } from '../../lib/filters'
import { medianCost, sessionsSentence, type SummaryPart } from '../../lib/summaries'
import { sessionsUrl, sessionUrl } from '../../lib/urls'
import { ErrorBox } from '../../components/common'
import { FilterBar, FilterButton, FilterSearchField, useFilters, useWidenedTime, WidenedNote } from '../../components/ui/FilterBar'
import { GuidedEmpty, SendTestSession } from '../../components/ui/EmptyState'
import { PageHeader } from '../../components/ui/PageHeader'
import { Segmented } from '../../components/ui/Segmented'
import { Sentence, type SentencePart } from '../../components/ui/Sentence'
import { CapNote } from '../../components/ui/bits'
import { SessionGroups, SessionListSkeleton, type ListContext } from '../../components/sessions/SessionList'

const DEFAULT_TIME: TimeRange = '24h'
const PAGE = 100
/** The most sessions the list shows (the API's largest page). */
const MAX_LIST = 1000

const RANGES: { id: TimeRange; label: string }[] = [
  { id: '24h', label: '24 hours' },
  { id: '7d', label: '7 days' },
  { id: '30d', label: '30 days' },
]

const SORTS: { id: string; label: string; field: string }[] = [
  { id: 'recent', label: 'Most recent', field: '-last_activity' },
  { id: 'cost', label: 'Most expensive', field: '-cost_usd' },
  { id: 'duration', label: 'Longest', field: '-duration_ms' },
  { id: 'errors', label: 'Most errors', field: '-error_count' },
  { id: 'detections', label: 'Most security verdicts', field: '-detection_count' },
]

export function SessionsPage() {
  const pid = useProjectId()
  const [state, setState] = useFilters()
  const [params, setParams] = useSearchParams()
  const sort = SORTS.find((s) => s.id === params.get('sort')) ?? SORTS[0]
  const [limit, setLimit] = useState(PAGE)
  const [filterOpen, setFilterOpen] = useState(false)

  const serverNoTime = sessionsParams({ ...state, time: 'all', from: undefined, to: undefined }, 'all')
  const widen = useWidenedTime(state, DEFAULT_TIME, async (r) => (await v1.sessions(pid, { ...sessionsParams({ ...state, time: r }, r), limit: 1 })).total_count, [
    pid,
    'sessions',
    serverNoTime,
  ])
  const time = widen.time
  // The server applies every chip, so the list is paged there.
  const server = sessionsParams(state, time)
  const list = useQuery({
    queryKey: ['v1', pid, 'sessions', server, sort.field, limit],
    queryFn: () => v1.sessions(pid, { ...server, sort_by: sort.field, limit }),
    enabled: widen.ready,
    placeholderData: keepPreviousData,
    refetchInterval: 15_000,
  })
  // The sentence's counts: the server counts them over every session in the range (with the other filters).
  const attention = useQuery({
    queryKey: ['v1', pid, 'sessions', 'attention', server],
    queryFn: () => v1.sessionsAttention(pid, server),
    enabled: widen.ready,
    placeholderData: keepPreviousData,
    refetchInterval: 30_000,
  })
  // Agents, models, tools and scorers to suggest in the filter editor (from the most recent sessions).
  const pool = useQuery({
    queryKey: ['v1', pid, 'sessions', 'options', server],
    queryFn: () => v1.sessions(pid, { ...server, limit: 200 }),
    enabled: widen.ready && filterOpen,
    placeholderData: keepPreviousData,
    staleTime: 60_000,
  })
  const anyData = useQuery({
    queryKey: ['v1', pid, 'sessions', 'any'],
    queryFn: () => v1.sessions(pid, { limit: 1 }),
    staleTime: 10_000,
  })
  const teams = useQuery({ queryKey: ['v1', pid, 'teams', 'options'], queryFn: () => v1.teams(pid, { limit: 200 }), staleTime: 60_000, enabled: filterOpen })

  const total = list.data?.total_count ?? 0
  const rows = useMemo(() => list.data?.items ?? [], [list.data])
  // The rules behind blocks and approval requests, for the rows' summaries.
  const flaggedIds = useMemo(() => rows.filter((s) => (s.detection_count?.block ?? 0) + (s.detection_count?.require_approval ?? 0) > 0).map((s) => s.session_id), [rows])
  const dets = useQuery({
    queryKey: ['v1', pid, 'detections', 'rows', flaggedIds],
    queryFn: () => v1.detections(pid, { session_ids: flaggedIds, action: 'block,require_approval', source: 'engine', limit: 1000 }),
    enabled: flaggedIds.length > 0,
    staleTime: 15_000,
  })
  const filterOptions = useMemo(() => optionsFrom(pool.data?.items ?? []), [pool.data])
  const projectEmpty = anyData.data?.total_count === 0
  const filtered = !isEmptyFilter({ ...state, time: undefined, from: undefined, to: undefined }, DEFAULT_TIME)
  const complete = !!list.data && rows.length === total

  const ctx: ListContext = useMemo(() => {
    const byId = new Map<string, Detection[]>()
    for (const d of dets.data?.items ?? []) byId.set(d.session_id, [...(byId.get(d.session_id) ?? []), d])
    return {
      costThreshold: attention.data?.cost_spike_threshold ?? null,
      costFactor: attention.data?.cost_spike_factor,
      lowScore: attention.data?.low_score_threshold,
      approvalHeavy: attention.data?.approval_heavy_threshold,
      medianCost: complete ? medianCost(rows) : null,
      detections: byId,
    }
  }, [attention.data, dets.data, complete, rows])

  const sentence: SentencePart[] | null = attention.data && list.data
    ? sessionsSentence({ attention: attention.data, time, filtered, rows, total }).map((p) => toSentencePart(p, pid, state))
    : null

  const header = (
    <PageHeader
      title="Sessions"
      help="sessions.md"
      controls={
        projectEmpty ? undefined : (
          <>
            <Segmented
              label="Time range"
              options={RANGES}
              value={(RANGES.some((r) => r.id === time) ? time : null) as TimeRange | null}
              onChange={(t) => setState({ ...removeKey(state, 'time'), time: t === DEFAULT_TIME && !widen.widened ? undefined : t })}
            />
            <FilterSearchField label="Search sessions" />
            <FilterButton open={filterOpen} onToggle={() => setFilterOpen((o) => !o)} defaultTime={DEFAULT_TIME} />
          </>
        )
      }
      sentence={
        projectEmpty ? undefined : sentence ? (
          <Sentence parts={sentence} />
        ) : (
          <p className="sentence s-outcome sk-sentence" aria-busy="true">
            <span className="sr-only">Counting sessions…</span>
            <span className="sk sk-line" aria-hidden />
          </p>
        )
      }
    />
  )

  if (projectEmpty)
    return (
      <div className="page-frame">
        {header}
        <GuidedEmpty projectId={pid} title="No sessions yet">
          Sessions appear here as soon as your agents send traces to <span className="mono">{pid}</span>: each conversation, with its agents, hand-offs, reasoning, tool calls and
          security verdicts.
        </GuidedEmpty>
      </div>
    )

  return (
    <div className="page-frame">
      {header}
      <FilterBar
        page="sessions"
        search="external"
        timePicker="none"
        collapsible
        open={filterOpen}
        defaultTime={DEFAULT_TIME}
        effectiveTime={time}
        options={{ ...filterOptions, teams: (teams.data?.items ?? []).filter((t) => !t.auto).map((t) => ({ id: t.team_id, name: t.name })) }}
        extra={
          <label className="sort-pick">
            <span className="sr-only">Sort</span>
            <select
              className="input"
              value={sort.id}
              onChange={(e) => {
                const n = new URLSearchParams(params)
                if (e.target.value === 'recent') n.delete('sort')
                else n.set('sort', e.target.value)
                setParams(n, { replace: true })
              }}
            >
              {SORTS.map((s) => (
                <option key={s.id} value={s.id}>
                  Sort: {s.label.toLowerCase()}
                </option>
              ))}
            </select>
          </label>
        }
      />
      {widen.widened && <WidenedNote time={time} defaultTime={DEFAULT_TIME} onReset={() => setState({ ...state, time: DEFAULT_TIME })} />}
      <ErrorBox error={list.error ?? attention.error} />
      {list.isLoading || !widen.ready ? (
        <SessionListSkeleton />
      ) : rows.length === 0 ? (
        <div className="quiet-empty">
          <p>No sessions match these filters.</p>
          <p className="quiet-note">Try a longer time range, or remove a filter.</p>
          <button type="button" className="qbtn" onClick={() => setState({})}>
            Clear filters
          </button>
        </div>
      ) : (
        <>
          <SessionGroups projectId={pid} sessions={rows} ctx={ctx} />
          {total > rows.length && limit < MAX_LIST ? (
            <button type="button" className="qbtn" onClick={() => setLimit((l) => Math.min(MAX_LIST, l + PAGE))} style={{ alignSelf: 'center' }}>
              Show {Math.min(PAGE, total - rows.length)} more of {total - rows.length}
            </button>
          ) : (
            <CapNote shown={rows.length} total={total} what="sessions" why="narrow the filters or the time range to see the rest" />
          )}
        </>
      )}
      <p className="quiet-note sessions-foot">
        Want to see how a session looks end to end? <SendTestSession projectId={pid} className="linkish" />
      </p>
    </div>
  )
}

/** A summary part as a sentence part: a clause links to its one session, or to the list it counts. */
function toSentencePart(p: SummaryPart, pid: string, base: FilterState): SentencePart {
  if (typeof p === 'string') return p
  if (p.session) return { text: p.text, to: sessionUrl(pid, p.session.id, { step: p.session.step, tab: p.session.tab }), title: 'Open this session' }
  return {
    text: p.text,
    to: sessionsUrl(pid, filterQueryString({ ...base, ...p.filter })),
    title: p.exact ? undefined : 'Lists these sessions together with the blocked ones',
  }
}

export function optionsFrom(sessions: SessionSummary[]) {
  const agents = new Set<string>()
  const models = new Set<string>()
  const tools = new Set<string>()
  const scorers = new Set<string>()
  for (const s of sessions) {
    s.agents.forEach((a) => agents.add(a))
    s.models?.forEach((m) => models.add(m))
    s.tools?.forEach((t) => tools.add(t))
    Object.keys(s.scores ?? {}).forEach((k) => scorers.add(k))
  }
  return { agents: [...agents].sort(), models: [...models].sort(), tools: [...tools].sort(), scorers: [...scorers].sort() }
}
