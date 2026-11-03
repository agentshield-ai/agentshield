// Applies the filter chips a page cannot read off its own rows through the
// sessions that match them (lib/filters.ts FILTER_SUPPORT): an agent, team,
// detection or trace is kept when it belongs to a matching session.
import { useMemo } from 'react'
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { v1, type SessionSummary } from '../api/v1'
import { sessionMatchesClientFilters, sessionScope, sessionsParams, type FilterPage, type FilterState, type TimeRange } from '../lib/filters'

/** The most sessions one scope looks at (one /api/v1/sessions page). */
export const SCOPE_LIMIT = 1000

export interface SessionScope {
  /** False when no chip on this page goes through sessions. */
  active: boolean
  ready: boolean
  sessions: SessionSummary[]
  ids: Set<string>
  agents: Set<string>
  teams: Set<string>
  /** More sessions matched than SCOPE_LIMIT; only the most recent were used. */
  truncated: boolean
  error: unknown
}

export function useSessionScope(pid: string, page: FilterPage, state: FilterState, time: TimeRange, enabled = true): SessionScope {
  const scope = sessionScope(page, state)
  const active = Object.keys(scope).length > 0
  // The page's time range, with the scope's chips.
  const params = sessionsParams({ ...scope, time: state.time, from: state.from, to: state.to }, time)
  const q = useQuery({
    queryKey: ['v1', pid, 'sessions', 'scope', params],
    queryFn: () => v1.sessions(pid, { ...params, limit: SCOPE_LIMIT }),
    enabled: active && enabled,
    placeholderData: keepPreviousData,
  })
  return useMemo(() => {
    const sessions = (q.data?.items ?? []).filter((s) => sessionMatchesClientFilters(s, scope))
    return {
      active,
      ready: !active || (!!q.data && !q.isPlaceholderData),
      sessions,
      ids: new Set(sessions.map((s) => s.session_id)),
      agents: new Set(sessions.flatMap((s) => s.agents)),
      teams: new Set(sessions.map((s) => s.team_id).filter((t): t is string => !!t)),
      truncated: (q.data?.total_count ?? 0) > SCOPE_LIMIT,
      error: q.error,
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [active, q.data, q.isPlaceholderData, q.error, JSON.stringify(scope)])
}
