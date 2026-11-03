// Applies the filter chips a page cannot read off its own rows through the
// sessions that match them (lib/filters.ts FILTER_SUPPORT): an agent, team,
// detection or trace is kept when it belongs to a matching session. The
// server applies every chip; a page that needs the session ids themselves
// (Agents, Teams, Traces) reads at most SCOPE_LIMIT of them and says so
// (ScopeCapNote). Security passes the chips to the detections API instead,
// which resolves every matching session itself.
import { useMemo } from 'react'
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { v1, type SessionSummary } from '../api/v1'
import { sessionScope, sessionsParams, type FilterPage, type FilterState, type TimeRange } from '../lib/filters'

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
  /** How many sessions matched in all. */
  total: number
  error: unknown
}

/** The /api/v1/sessions parameters of the chips a page applies through sessions, in the page's time range. */
export function sessionScopeParams(page: FilterPage, state: FilterState, time: TimeRange): Record<string, string> | null {
  const scope = sessionScope(page, state)
  if (!Object.keys(scope).length) return null
  return sessionsParams({ ...scope, time: state.time, from: state.from, to: state.to }, time)
}

export function useSessionScope(pid: string, page: FilterPage, state: FilterState, time: TimeRange, enabled = true): SessionScope {
  const params = sessionScopeParams(page, state, time)
  const active = params != null
  const q = useQuery({
    queryKey: ['v1', pid, 'sessions', 'scope', params],
    queryFn: () => v1.sessions(pid, { ...params, limit: SCOPE_LIMIT }),
    enabled: active && enabled,
    placeholderData: keepPreviousData,
  })
  return useMemo(() => {
    const sessions = q.data?.items ?? []
    return {
      active,
      ready: !active || (!!q.data && !q.isPlaceholderData),
      sessions,
      ids: new Set(sessions.map((s) => s.session_id)),
      agents: new Set(sessions.flatMap((s) => s.agents)),
      teams: new Set(sessions.map((s) => s.team_id).filter((t): t is string => !!t)),
      truncated: (q.data?.total_count ?? 0) > SCOPE_LIMIT,
      total: q.data?.total_count ?? 0,
      error: q.error,
    }
  }, [active, q.data, q.isPlaceholderData, q.error])
}
