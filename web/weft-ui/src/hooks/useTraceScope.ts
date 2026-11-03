// Which traces the shared filter chips keep on the Traces page, through the
// agent index (/agents/spans/query grouped by trace):
//   - team, security, score, cost and "went on after a failed step" belong
//     to sessions: a trace is kept when it belongs to a matching session;
//   - agent, model and tool, when listing traces (roots only): a trace is
//     kept when any of its spans matches (its root span rarely does).
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { agentsApi } from '../api/agents'
import type { Operation } from '../api/types'
import { timeWindow, type FilterState, type TimeRange } from '../lib/filters'
import { useSessionScope } from './useSessionScope'

export interface TraceScope {
  /** Trace ids to keep, or null when no chip needs them. */
  traceIds: string[] | null
  ready: boolean
  error: unknown
}

const field = (key: string, value: string): Operation => ({ $eq: [{ $getField: key }, { $literal: value }] })

/** The agent-index conditions that select traces; each must hold on some span of the trace. */
export function traceConditions(f: FilterState, roots: boolean, sessionIds: string[] | null): Operation[] {
  const out: Operation[] = []
  if (sessionIds) out.push({ $in: [{ $getField: 'conversation_id' }, sessionIds.map((id) => ({ $literal: id }))] })
  if (roots) {
    if (f.agent) out.push(field('agent_name', f.agent))
    if (f.model) out.push(field('request_model', f.model))
    if (f.tool) out.push(field('tool_name', f.tool))
  }
  return out
}

export function useTraceScope(pid: string, f: FilterState, time: TimeRange, roots: boolean, enabled = true): TraceScope {
  const sessions = useSessionScope(pid, 'traces', f, time, enabled)
  const ids = sessions.active ? [...sessions.ids].sort() : null
  const conds = traceConditions(f, roots, ids)
  // A trace started in the range has its spans in it too.
  const after = timeWindow(f, time).from
  const q = useQuery({
    queryKey: ['agents', pid, 'trace-scope', conds, after],
    enabled: enabled && sessions.ready && conds.length > 0 && !(ids && ids.length === 0),
    placeholderData: keepPreviousData,
    queryFn: async () => {
      const sets = await Promise.all(
        conds.map(async (c) => {
          const r = await agentsApi.spans({ project_id: pid, query: { $expr: c }, group_by: [{ key: 'trace_id' }], limit: 100_000, started_after: after })
          return new Set(r.groups.map((g) => String(g.group_keys.trace_id)))
        }),
      )
      const [first, ...rest] = sets
      return [...first].filter((t) => rest.every((s) => s.has(t))).sort()
    },
  })
  if (!conds.length) return { traceIds: null, ready: sessions.ready, error: sessions.error }
  if (ids && ids.length === 0) return { traceIds: [], ready: true, error: sessions.error }
  return { traceIds: q.data ?? null, ready: sessions.ready && !!q.data && !q.isPlaceholderData, error: sessions.error ?? q.error }
}
