// Leaderboard objects (Weave's Leaderboard class) saved from the UI.
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
  weft_dynamic?: { evaluation_call_ids: string[]; metrics: LbMetric[] }
}

export const leaderboardsApi = {
  save: (projectId: string, objectId: string, val: DynamicLeaderboardVal) => api.objCreate(projectId, objectId, val, 'Leaderboard'),
}
