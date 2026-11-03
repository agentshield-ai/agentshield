// Attention cards on the Sessions home (DESIGN.md §7.1 rule 1): failed
// sessions, sessions that went on after a failed step, blocked or
// approval-heavy sessions, low scores and cost spikes, each with the filter
// that opens exactly the sessions it counts. The server counts them over
// every matching session (GET /api/v1/sessions/attention); this module only
// words them.
import type { SessionAttention } from '../api/v1'
import { APPROVAL_HEAVY, filterFromSessionsParams, type FilterState } from './filters'
import { fmtCost } from './format'

export { APPROVAL_HEAVY }

export type AttentionId = SessionAttention['items'][number]['id']

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

const plural = (n: number, w: string) => `${n} ${w}${n === 1 ? '' : 's'}`

/** The five cards, worded, from the server's counts. */
export function attentionCards(a: SessionAttention): AttentionCard[] {
  const by = Object.fromEntries(a.items.map((c) => [c.id, c])) as Record<AttentionId, SessionAttention['items'][number] | undefined>
  const card = (id: AttentionId) => by[id] ?? { id, count: 0, filter: {}, latest: null }
  const failed = card('failed')
  const step = card('failed_step')
  const security = card('security')
  const low = card('low_score')
  const cost = card('cost')
  const heavy = a.approval_heavy_threshold || APPROVAL_HEAVY
  return [
    {
      id: 'failed',
      title: 'Failed sessions',
      count: failed.count,
      detail: failed.count ? `Most recent: ${failed.latest?.title || failed.latest?.session_id || ''}` : 'No session failed.',
      explain: 'Sessions that failed themselves: the top-level span of one of their traces (agent span or not) ended with an error. Sessions that went on after a failed tool call, model call, hand-off or delegated agent are counted under “Failed steps”.',
      filter: filterFromSessionsParams(failed.filter),
      tone: 'error',
    },
    {
      id: 'failed_step',
      title: 'Failed steps',
      count: step.count,
      detail: step.count ? `${plural(step.count, 'session')} went on after a tool or model call failed` : 'No session had a failed step it went on from.',
      explain:
        'Sessions that ended without failing, but where at least one tool call, model call, hand-off or delegated agent ended with an error (a sub-agent in a trace of its own counts when it is linked to its caller). The agent may have retried, worked around it, or given a weaker answer.',
      filter: filterFromSessionsParams(step.filter),
      tone: 'warn',
    },
    {
      id: 'security',
      title: 'Blocked or needing approval',
      count: security.count,
      detail: security.count ? `${plural(a.blocked, 'session')} with a blocked tool call, ${a.approval_heavy} with ${heavy}+ approval requests` : 'AgentShield blocked nothing.',
      explain: `Sessions where AgentShield blocked a tool call, or asked for approval ${heavy} or more times (a sign of approval fatigue). A session with both counts once. Replays are not counted.`,
      filter: filterFromSessionsParams(security.filter),
      tone: 'warn',
    },
    {
      id: 'low_score',
      title: 'Low scores',
      count: low.count,
      detail: low.count && a.lowest_score != null ? `Lowest: ${a.lowest_score.toFixed(2)}` : `No session scored below ${a.low_score_threshold}.`,
      explain: `Sessions where any session-level score (the mean of that scorer's results) is below ${a.low_score_threshold}. Scores range from 0 to 1.`,
      filter: filterFromSessionsParams(low.filter),
      tone: 'warn',
    },
    {
      id: 'cost',
      title: 'Cost spikes',
      count: cost.count,
      detail:
        a.cost_spike_threshold == null
          ? 'Needs at least four sessions with a known cost.'
          : cost.count
            ? `At or above ${fmtCost(a.cost_spike_threshold)} each`
            : `None at or above ${fmtCost(a.cost_spike_threshold)}.`,
      explain: `Sessions costing at least ${a.cost_spike_factor}× the median session cost in this range. Cost is input, output and cached tokens priced per model; sessions on models without a price are left out.`,
      filter: filterFromSessionsParams(cost.filter),
      tone: 'info',
    },
  ]
}
