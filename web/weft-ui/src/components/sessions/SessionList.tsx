// A plain list of sessions with one-line summaries (DESIGN.md §7.1 rule 1):
// the first user request, the agents, the outcome, duration, cost,
// AgentShield verdicts and scores.
import { Link } from 'react-router-dom'
import type { SessionSummary } from '../../api/v1'
import { fmtCost, fmtDate, fmtDuration, fmtRelative } from '../../lib/format'
import { agentUrl, sessionUrl, teamUrl } from '../../lib/urls'
import { AgentChip, DetectionBadges, ScorePill, StatusPill } from '../ui/bits'
import { Icon } from '../ui/Icons'

export function sessionTitle(s: Pick<SessionSummary, 'title' | 'name' | 'session_id'>): string {
  return s.title?.trim() || s.name?.trim() || `Session ${s.session_id}`
}

export function SessionList({ projectId, sessions, compact }: { projectId: string; sessions: SessionSummary[]; compact?: boolean }) {
  return (
    <ul className={`session-list ${compact ? 'compact' : ''}`} aria-label="Sessions">
      {sessions.map((s) => (
        <SessionRow key={s.session_id} projectId={projectId} s={s} />
      ))}
    </ul>
  )
}

export function SessionRow({ projectId, s }: { projectId: string; s: SessionSummary }) {
  const det = s.detection_count ?? {}
  const hasSecurity = (det.block ?? 0) > 0 || (det.require_approval ?? 0) > 0
  const scores = Object.entries(s.scores ?? {})
  const steps = s.tool_call_count + s.llm_call_count
  return (
    <li className={`session-row status-${s.status}`}>
      <div className="session-main">
        <div className="session-title-line">
          <StatusPill status={s.status} />
          <Link className="session-title" to={sessionUrl(projectId, s.session_id)}>
            {sessionTitle(s)}
          </Link>
        </div>
        <div className="session-meta">
          <span className="agents">
            {s.agents.length ? s.agents.map((a) => <AgentChip key={a} name={a} to={agentUrl(projectId, a)} size="sm" />) : <span className="faint">no agents recorded</span>}
          </span>
          {s.team_id && !s.team_id.startsWith('auto:') && (
            <Link className="team-link small" to={teamUrl(projectId, s.team_id)}>
              <Icon name="teams" size={12} /> {s.team_name || s.team_id}
            </Link>
          )}
          <span className="faint small" title={fmtDate(s.last_activity)}>
            {fmtRelative(s.last_activity)}
          </span>
        </div>
      </div>
      <dl className="session-figures">
        <div>
          <dt>Duration</dt>
          <dd>{fmtDuration(s.duration_ms)}</dd>
        </div>
        <div>
          <dt>Steps</dt>
          <dd title={`${s.llm_call_count} model calls, ${s.tool_call_count} tool calls, ${s.handoff_count} hand-offs`}>
            {steps}
            {s.handoff_count ? (
              <span className="faint">
                {' '}
                · {s.handoff_count} hand-off{s.handoff_count === 1 ? '' : 's'}
              </span>
            ) : null}
          </dd>
        </div>
        <div>
          <dt>Cost</dt>
          <dd>{fmtCost(s.cost_usd)}</dd>
        </div>
      </dl>
      <div className="session-flags">
        {hasSecurity && (
          <Link className="flag-link" to={sessionUrl(projectId, s.session_id, { step: 'problem' })} title={`Go to the ${det.block ? 'blocked' : 'flagged'} step`}>
            <DetectionBadges counts={det} />
            <span className="sr-only">
              : go to the {det.block ? 'blocked' : 'flagged'} step in {sessionTitle(s)}
            </span>
          </Link>
        )}
        {!hasSecurity && <DetectionBadges counts={det} />}
        {s.status === 'error' && (
          <Link className="flag-link small" to={sessionUrl(projectId, s.session_id, { step: 'error' })}>
            Go to the failure <Icon name="chevron" size={12} />
          </Link>
        )}
        {s.status !== 'error' && s.error_count > 0 && (
          <Link
            className="flag-link pill warn step-failed"
            to={sessionUrl(projectId, s.session_id, { step: 'error' })}
            title="A tool call, model call or hand-off failed, but the session went on. Click to see the failed step."
          >
            <Icon name="error" size={12} /> {s.error_count} failed step{s.error_count === 1 ? '' : 's'}
            <span className="sr-only">: go to the failed step in {sessionTitle(s)}</span>
          </Link>
        )}
        {scores.map(([name, v]) => (
          <ScorePill key={name} scorer={name} value={v.mean} count={v.count} />
        ))}
      </div>
    </li>
  )
}

export function SessionListSkeleton({ rows = 5 }: { rows?: number }) {
  return (
    <ul className="session-list" aria-busy="true" aria-label="Loading sessions">
      {Array.from({ length: rows }, (_, i) => (
        <li key={i} className="session-row skeleton">
          <div className="sk sk-line" style={{ width: `${50 + ((i * 13) % 40)}%` }} />
          <div className="sk sk-line short" />
        </li>
      ))}
    </ul>
  )
}
