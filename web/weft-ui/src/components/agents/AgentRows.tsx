// Rows and sentence links shared by the Agents and Teams pages (DESIGN.md
// §7, §7.2 rule 3): an agent or a team as a grouped-list row (a status
// glyph, the name, one generated line, a quiet meta line, the time), and
// generated sentence parts turned into links.
import { useMemo } from 'react'
import { useQuery } from '@tanstack/react-query'
import { v1, type AgentSummary, type Detection, type SessionSummary, type TeamSummary } from '../../api/v1'
import type { ListContext } from '../sessions/SessionList'
import { filterQueryString, type TimeRange } from '../../lib/filters'
import { fmtDate } from '../../lib/format'
import { relativeTime } from '../../lib/summaries'
import { agentGlyph, agentMetaLine, agentSummaryLine, teamGlyph, teamMetaLine, teamName, teamSummaryLine, type LinkedPart } from '../../lib/summaries/agentsTeams'
import { agentUrl, sessionsUrl, teamUrl } from '../../lib/urls'
import { ListRow } from '../ui/GroupedList'
import type { SentencePart } from '../ui/Sentence'
import { StatusGlyph } from '../ui/StatusGlyph'

/** The time ranges of the Agents and Teams lists (as on Sessions). */
export const RANGES: { id: TimeRange; label: string }[] = [
  { id: '24h', label: '24 hours' },
  { id: '7d', label: '7 days' },
  { id: '30d', label: '30 days' },
]

/** Generated parts as sentence parts: agents and teams link to their pages, figures to the sessions behind them. */
export function toSentenceParts(parts: LinkedPart[], pid: string): SentencePart[] {
  return parts.map((p) => {
    if (typeof p === 'string') return p
    if ('agent' in p) return { text: p.text, to: agentUrl(pid, p.agent) }
    if ('team' in p) return { text: p.text, to: teamUrl(pid, p.team) }
    return { text: p.text, to: sessionsUrl(pid, filterQueryString(p.sessions)), title: 'Open these sessions' }
  })
}

export function AgentRow({ pid, a }: { pid: string; a: AgentSummary }) {
  const g = agentGlyph(a)
  return (
    <ListRow
      className="agent-row"
      glyph={<StatusGlyph kind={g.kind} label={g.label} />}
      title={a.agent_name}
      summary={agentSummaryLine(a)}
      meta={agentMetaLine(a) || undefined}
      time={a.last_seen ? relativeTime(a.last_seen) : undefined}
      timeTitle={a.last_seen ? `Last seen ${fmtDate(a.last_seen)}` : undefined}
      to={agentUrl(pid, a.agent_name)}
    />
  )
}

export function TeamRow({ pid, t }: { pid: string; t: TeamSummary }) {
  const g = teamGlyph(t)
  return (
    <ListRow
      className="team-row"
      glyph={<StatusGlyph kind={g.kind} label={g.label} />}
      title={teamName(t)}
      summary={teamSummaryLine(t)}
      meta={teamMetaLine(t)}
      time={t.last_activity ? relativeTime(t.last_activity) : undefined}
      timeTitle={t.last_activity ? `Last active ${fmtDate(t.last_activity)}` : undefined}
      to={teamUrl(pid, t.team_id)}
    />
  )
}

/** Row context for a short list of sessions: the rules behind their blocks and approval requests, so rows name them as on Sessions. */
export function useSessionRowContext(pid: string, sessions: SessionSummary[] | undefined): ListContext {
  const ids = useMemo(() => (sessions ?? []).filter((s) => (s.detection_count?.block ?? 0) + (s.detection_count?.require_approval ?? 0) > 0).map((s) => s.session_id), [sessions])
  const dets = useQuery({
    queryKey: ['v1', pid, 'detections', 'rows', ids],
    queryFn: () => v1.detections(pid, { session_ids: ids, action: 'block,require_approval', source: 'engine', limit: 1000 }),
    enabled: ids.length > 0,
    staleTime: 15_000,
  })
  return useMemo(() => {
    const byId = new Map<string, Detection[]>()
    for (const d of dets.data?.items ?? []) byId.set(d.session_id, [...(byId.get(d.session_id) ?? []), d])
    return { detections: byId }
  }, [dets.data])
}

/** A grey placeholder list while rows load. */
export function RowsSkeleton({ label, rows = 4 }: { label: string; rows?: number }) {
  return (
    <ul className="glist-box" aria-busy="true" aria-label={label}>
      {Array.from({ length: rows }, (_, i) => (
        <li key={i} className="lrow">
          <div className="lrow-link static">
            <span className="sk sk-dot" />
            <span className="lrow-text">
              <span className="sk sk-line" style={{ width: `${45 + ((i * 17) % 40)}%` }} />
              <span className="sk sk-line short" />
            </span>
          </div>
        </li>
      ))}
    </ul>
  )
}
