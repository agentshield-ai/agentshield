// Attention cards on the Sessions home (DESIGN.md §7.1 rule 1): failed
// sessions, blocked or approval-heavy sessions, low scores and cost spikes,
// each with the filter that opens the matching list.
import type { SessionSummary } from '../api/v1'
import type { FilterState } from './filters'

export type AttentionId = 'failed' | 'security' | 'low_score' | 'cost'

export interface AttentionCard {
  id: AttentionId
  title: string
  count: number
  detail: string
  /** How the number is computed (shown as a tooltip). */
  explain: string
  filter: FilterState
  tone: 'error' | 'warn' | 'info'
}

export const LOW_SCORE = 0.5
export const APPROVAL_HEAVY = 3
export const COST_SPIKE_FACTOR = 3

export function median(xs: number[]): number | null {
  if (!xs.length) return null
  const s = [...xs].sort((a, b) => a - b)
  const m = Math.floor(s.length / 2)
  return s.length % 2 ? s[m] : (s[m - 1] + s[m]) / 2
}

/** The cost above which a session counts as a spike, or null with too little data. */
export function costSpikeThreshold(sessions: Pick<SessionSummary, 'cost_usd'>[]): number | null {
  const costs = sessions.map((s) => s.cost_usd).filter((c): c is number => typeof c === 'number' && c > 0)
  if (costs.length < 4) return null
  const m = median(costs)!
  return roundUp(m * COST_SPIKE_FACTOR)
}

function roundUp(x: number): number {
  if (x <= 0) return 0
  const p = Math.pow(10, Math.floor(Math.log10(x)) - 1)
  return Math.ceil(x / p) * p
}

export function lowestScore(s: Pick<SessionSummary, 'scores'>): number | null {
  const means = Object.values(s.scores ?? {}).map((v) => v.mean)
  return means.length ? Math.min(...means) : null
}

export function computeAttention(sessions: SessionSummary[]): AttentionCard[] {
  const failed = sessions.filter((s) => s.status === 'error')
  const blocked = sessions.filter((s) => (s.detection_count?.block ?? 0) > 0)
  const approvalHeavy = sessions.filter((s) => (s.detection_count?.require_approval ?? 0) >= APPROVAL_HEAVY)
  const security = new Set([...blocked, ...approvalHeavy].map((s) => s.session_id))
  const low = sessions.filter((s) => {
    const m = lowestScore(s)
    return m != null && m < LOW_SCORE
  })
  const threshold = costSpikeThreshold(sessions)
  const spikes = threshold == null ? [] : sessions.filter((s) => (s.cost_usd ?? 0) >= threshold)
  const plural = (n: number, w: string) => `${n} ${w}${n === 1 ? '' : 's'}`
  return [
    {
      id: 'failed',
      title: 'Failed sessions',
      count: failed.length,
      detail: failed.length ? `Most recent: ${failed[0].title || failed[0].session_id}` : 'No session failed.',
      explain: 'Sessions where the top-level run or an agent invocation ended with an error. A failed tool call that an agent recovered from does not count.',
      filter: { status: 'error' },
      tone: 'error',
    },
    {
      id: 'security',
      title: 'Blocked or needing approval',
      count: security.size,
      detail: security.size
        ? `${plural(blocked.length, 'session')} with a blocked tool call, ${approvalHeavy.length} with ${APPROVAL_HEAVY}+ approval requests`
        : 'AgentShield blocked nothing.',
      explain: `Sessions where AgentShield blocked a tool call, or asked for approval ${APPROVAL_HEAVY} or more times (a sign of approval fatigue). Replays are not counted.`,
      filter: { has: blocked.length || !approvalHeavy.length ? 'block' : 'approval' },
      tone: 'warn',
    },
    {
      id: 'low_score',
      title: 'Low scores',
      count: low.length,
      detail: low.length ? `Lowest: ${Math.min(...low.map((s) => lowestScore(s)!)).toFixed(2)}` : 'No session scored below ' + LOW_SCORE + '.',
      explain: `Sessions where any session-level score (the mean of that scorer's results) is below ${LOW_SCORE}. Scores range from 0 to 1.`,
      filter: { scoreBelow: { value: LOW_SCORE } },
      tone: 'warn',
    },
    {
      id: 'cost',
      title: 'Cost spikes',
      count: spikes.length,
      detail: threshold == null ? 'Needs at least four sessions with a known cost.' : spikes.length ? `At or above $${threshold} each` : `None at or above $${threshold}.`,
      explain: `Sessions costing at least ${COST_SPIKE_FACTOR}× the median session cost in this range. Cost is input, output and cached tokens priced per model; sessions on models without a price are left out.`,
      filter: threshold == null ? {} : { costMin: threshold },
      tone: 'info',
    },
  ]
}
