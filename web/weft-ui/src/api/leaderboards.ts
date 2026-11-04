// Leaderboard objects saved from the UI.
import { api } from './client'
import type { LeaderboardColumnSpec, LbMetric } from '../components/evals/leaderboardUtils'

export interface DynamicLeaderboardVal {
  _type: 'Leaderboard'
  _class_name: 'Leaderboard'
  _bases: string[]
  name: string
  description: string
  columns: LeaderboardColumnSpec[]
  /** Weft: the comparison it was made from — runs and weighted metrics — so it can be reopened. */
  weft_dynamic?: { evaluation_run_ids: string[]; metrics: LbMetric[] }
}

/** The evaluation runs a dynamic leaderboard was saved from (versions saved before the rename name them evaluation_call_ids). */
export function dynamicRunIds(value: unknown): string[] {
  const d = (value as { weft_dynamic?: { evaluation_run_ids?: unknown; evaluation_call_ids?: unknown } } | null)?.weft_dynamic
  const ids = d?.evaluation_run_ids ?? d?.evaluation_call_ids
  return Array.isArray(ids) ? ids.filter((x): x is string => typeof x === 'string') : []
}

export const leaderboardsApi = {
  save: (projectId: string, name: string, value: DynamicLeaderboardVal) => api.objectSave(projectId, name, value, 'Leaderboard'),
}
