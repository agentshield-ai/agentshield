// Sessions as grouped lists (DESIGN.md §7, §7.2 rule 3): each row a status
// glyph, the first request as the title, one generated line saying what
// happened (lib/summaries.ts), the team and agents, and the time. The whole
// row opens the session, where the step responsible for a failure or block
// is selected on arrival.
import type { Detection, SessionSummary } from '../../api/v1'
import { fmtDate } from '../../lib/format'
import { needsALook, relativeTime, rowGlyph, sessionMetaLine, sessionSummaryLine, sessionTitle, type RowContext } from '../../lib/summaries'
import { sessionUrl } from '../../lib/urls'
import { GroupedList, ListRow } from '../ui/GroupedList'
import { StatusGlyph } from '../ui/StatusGlyph'

export { sessionTitle }

/** Row context shared by a list, plus each session's detections when loaded. */
export interface ListContext extends Omit<RowContext, 'detections'> {
  detections?: Map<string, Detection[]>
}

const rowCtx = (ctx: ListContext | undefined, id: string): RowContext => ({ ...ctx, detections: ctx?.detections?.get(id) })

export function SessionRow({ projectId, s, ctx }: { projectId: string; s: SessionSummary; ctx?: ListContext }) {
  const c = rowCtx(ctx, s.session_id)
  const g = rowGlyph(s, c)
  return (
    <ListRow
      className={`session-row status-${s.status}`}
      glyph={<StatusGlyph kind={g.kind} label={g.label} />}
      title={sessionTitle(s)}
      summary={sessionSummaryLine(s, c)}
      meta={sessionMetaLine(s)}
      time={relativeTime(s.last_activity)}
      timeTitle={fmtDate(s.last_activity)}
      to={sessionUrl(projectId, s.session_id)}
    />
  )
}

/** One grouped list of sessions, without a heading (Team and Agent pages). */
export function SessionList({ projectId, sessions, ctx, label = 'Sessions', title }: { projectId: string; sessions: SessionSummary[]; ctx?: ListContext; label?: string; title?: string; compact?: boolean }) {
  return (
    <GroupedList label={label} title={title}>
      {sessions.map((s) => (
        <SessionRow key={s.session_id} projectId={projectId} s={s} ctx={ctx} />
      ))}
    </GroupedList>
  )
}

/** Splits sessions into "Needs a look", "Still running" and "Finished", keeping their order. */
export function groupSessions(sessions: SessionSummary[], ctx?: ListContext) {
  const look: SessionSummary[] = []
  const running: SessionSummary[] = []
  const done: SessionSummary[] = []
  for (const s of sessions) {
    if (needsALook(s, rowCtx(ctx, s.session_id))) look.push(s)
    else if (String(s.status) === 'running') running.push(s)
    else done.push(s)
  }
  return { look, running, done }
}

/** The Sessions page lists: Needs a look, then Still running, then Finished (empty groups are left out). */
export function SessionGroups({ projectId, sessions, ctx }: { projectId: string; sessions: SessionSummary[]; ctx?: ListContext }) {
  const g = groupSessions(sessions, ctx)
  const group = (title: string, xs: SessionSummary[]) =>
    xs.length > 0 && (
      <GroupedList title={title} count={xs.length}>
        {xs.map((s) => (
          <SessionRow key={s.session_id} projectId={projectId} s={s} ctx={ctx} />
        ))}
      </GroupedList>
    )
  return (
    <>
      {group('Needs a look', g.look)}
      {group('Still running', g.running)}
      {group('Finished', g.done)}
    </>
  )
}

export function SessionListSkeleton({ rows = 5 }: { rows?: number }) {
  return (
    <ul className="glist-box" aria-busy="true" aria-label="Loading sessions">
      {Array.from({ length: rows }, (_, i) => (
        <li key={i} className="lrow">
          <div className="lrow-link static">
            <span className="sk sk-dot" />
            <span className="lrow-text">
              <span className="sk sk-line" style={{ width: `${50 + ((i * 13) % 40)}%` }} />
              <span className="sk sk-line short" />
            </span>
          </div>
        </li>
      ))}
    </ul>
  )
}
