// Sessions, the project home (DESIGN.md §7.1 rule 1): what happened
// recently, and what needs attention.
import { useMemo, useState } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { v1, type SessionSummary } from '../../api/v1'
import { useProjectId } from '../../hooks/useProject'
import { computeAttention, type AttentionCard } from '../../lib/attention'
import { filterQueryString, isEmptyFilter, sessionMatchesClientFilters, sessionsParams, TIME_LABEL, type FilterState, type TimeRange } from '../../lib/filters'
import { sessionsUrl } from '../../lib/urls'
import { ErrorBox } from '../../components/common'
import { FilterBar, useFilters, useWidenedTime, WidenedNote } from '../../components/ui/FilterBar'
import { GuidedEmpty, SendTestSession } from '../../components/ui/EmptyState'
import { Icon } from '../../components/ui/Icons'
import { usePageHelp } from '../../components/ui/PageHelp'
import { Tip } from '../../components/ui/bits'
import { SessionList, SessionListSkeleton } from '../../components/sessions/SessionList'

const DEFAULT_TIME: TimeRange = '24h'
const PAGE = 100

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
  const help = usePageHelp(
    'sessions',
    'Sessions',
    <>
      A <strong>session</strong> is one conversation with your agents, from the user's first request to the final answer, including every hand-off between agents, their reasoning
      and their tool calls. The cards at the top count the sessions that need a look; click one to list them. Open a session to see exactly what happened, step by step.
    </>,
    'DESIGN.md#7-ui',
  )

  const serverNoTime = sessionsParams({ ...state, time: 'all', from: undefined, to: undefined }, 'all')
  const widen = useWidenedTime(state, DEFAULT_TIME, async (r) => (await v1.sessions(pid, { ...sessionsParams({ ...state, time: r }, r), limit: 1 })).total_count, [
    pid,
    'sessions',
    serverNoTime,
  ])
  const time = widen.time
  const server = sessionsParams(state, time)
  const list = useQuery({
    queryKey: ['v1', pid, 'sessions', server, sort.field, limit],
    queryFn: () => v1.sessions(pid, { ...server, sort_by: sort.field, limit }),
    enabled: widen.ready,
    placeholderData: keepPreviousData,
    refetchInterval: 15_000,
  })
  // Attention cards look at every session in the range (with the other filters), not just the first page.
  const pool = useQuery({
    queryKey: ['v1', pid, 'sessions', 'attention', server],
    queryFn: () => v1.sessions(pid, { ...server, limit: 1000 }),
    enabled: widen.ready,
    placeholderData: keepPreviousData,
    refetchInterval: 30_000,
  })
  const anyData = useQuery({
    queryKey: ['v1', pid, 'sessions', 'any'],
    queryFn: () => v1.sessions(pid, { limit: 1 }),
    staleTime: 10_000,
  })
  const filterOptions = useMemo(() => optionsFrom(pool.data?.items ?? []), [pool.data])
  const teams = useQuery({ queryKey: ['v1', pid, 'teams', 'options'], queryFn: () => v1.teams(pid, { limit: 200 }), staleTime: 60_000 })

  const rows = (list.data?.items ?? []).filter((s) => sessionMatchesClientFilters(s, state))
  const clientFiltered = !!state.scoreBelow && !state.scoreBelow.scorer
  const cards = computeAttention(pool.data?.items ?? [])
  const projectEmpty = anyData.data?.total_count === 0
  const filtered = !isEmptyFilter(state, DEFAULT_TIME)

  return (
    <div className="page wide">
      <header className="page-head">
        <div className="grow">
          <h1>Sessions</h1>
          <p className="page-sub">What happened recently, and what needs your attention.</p>
        </div>
        {help.button}
      </header>
      {help.panel}
      {projectEmpty ? (
        <GuidedEmpty projectId={pid} title="No sessions yet">
          Sessions appear here as soon as your agents send traces to <span className="mono">{pid}</span>: each conversation, with its agents, hand-offs, reasoning, tool calls and
          security verdicts.
        </GuidedEmpty>
      ) : (
        <>
          <FilterBar
            page="sessions"
            defaultTime={DEFAULT_TIME}
            effectiveTime={time}
            options={{ ...filterOptions, teams: (teams.data?.items ?? []).filter((t) => !t.auto).map((t) => ({ id: t.team_id, name: t.name })) }}
            placeholder="Filter sessions…  try has:block or agent:researcher"
          />
          {widen.widened && <WidenedNote time={time} defaultTime={DEFAULT_TIME} onReset={() => setState({ ...state, time: DEFAULT_TIME })} />}
          <section aria-labelledby="attention-h">
            <h2 id="attention-h" className="section-title">
              Needs attention <span className="faint small">in the {TIME_LABEL[time].toLowerCase()}</span>
            </h2>
            <div className="attn-grid">
              {cards.map((c) => (
                <AttentionCardView key={c.id} card={c} pid={pid} base={state} loading={pool.isLoading} />
              ))}
            </div>
          </section>
          <section aria-labelledby="list-h" className="col" style={{ gap: 10 }}>
            <div className="row wrap" style={{ justifyContent: 'space-between' }}>
              <h2 id="list-h" className="section-title">
                {filtered ? 'Matching sessions' : 'Recent sessions'}{' '}
                <span className="faint small">
                  {list.data ? `${clientFiltered ? rows.length : list.data.total_count} session${(clientFiltered ? rows.length : list.data.total_count) === 1 ? '' : 's'}` : ''}
                </span>
              </h2>
              <label className="row small muted">
                Sort
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
                      {s.label}
                    </option>
                  ))}
                </select>
              </label>
            </div>
            <ErrorBox error={list.error} />
            {list.isLoading || !widen.ready ? (
              <SessionListSkeleton />
            ) : rows.length === 0 ? (
              <div className="empty-inline">
                <Icon name="filter" size={18} />
                <div className="grow">
                  <strong>No sessions match these filters.</strong>
                  <div className="muted small">Try a longer time range, or remove a filter.</div>
                </div>
                <button className="btn sm" onClick={() => setState({})}>
                  Clear filters
                </button>
              </div>
            ) : (
              <>
                <SessionList projectId={pid} sessions={rows} />
                {list.data && list.data.total_count > limit && (
                  <button className="btn" onClick={() => setLimit((l) => l + PAGE)} style={{ alignSelf: 'center' }}>
                    Show more ({list.data.total_count - limit} more)
                  </button>
                )}
              </>
            )}
          </section>
          <footer className="row wrap muted small" style={{ gap: 12 }}>
            <span>Want to see how a session looks end to end?</span>
            <SendTestSession projectId={pid} className="btn sm" />
          </footer>
        </>
      )}
    </div>
  )
}

function AttentionCardView({ card, pid, base, loading }: { card: AttentionCard; pid: string; base: FilterState; loading: boolean }) {
  const quiet = card.count === 0
  const to = sessionsUrl(pid, filterQueryString({ ...base, ...card.filter }))
  const head = (
    <div className="attn-top">
      <span className="attn-title">{card.title}</span>
      <Tip text={card.explain} label={`How “${card.title}” is counted`} />
    </div>
  )
  const figures = (
    <>
      <div className="attn-count">{loading ? '…' : card.count}</div>
      <div className="attn-detail">{card.detail}</div>
    </>
  )
  return (
    <div className={`attn-card tone-${card.tone} ${quiet ? 'quiet' : ''}`}>
      {head}
      {quiet || !Object.keys(card.filter).length ? (
        <div className="attn-body">{figures}</div>
      ) : (
        <Link to={to} className="attn-link">
          <span className="sr-only">{card.title}: </span>
          {figures}
          <span className="attn-cta">
            Show {card.count === 1 ? 'it' : 'them'} <Icon name="chevron" size={12} />
          </span>
        </Link>
      )}
    </div>
  )
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
