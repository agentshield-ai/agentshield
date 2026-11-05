// GroupedList and ListRow: things a person reads, as grouped lists in the
// manner of Mail or Settings (DESIGN.md §7.2 rule 3). A heading with a
// quiet count, then a white panel with 14 px corners and a hairline edge,
// rows separated by inset hairlines.
//
// A row: a status glyph, the title (up to two lines), the time on the
// right, one summary line saying what happened, and a quiet meta line. The
// whole row is one link.
//
//   <GroupedList title="Needs a look" count={7}>
//     <ListRow glyph={<StatusGlyph kind="blocked" />} title="Install the linter…"
//       summary="AgentShield blocked a piped install script." meta="Code crew · coder, reviewer"
//       time="50 min ago" timeTitle="5 Nov, 17:03" to="/p/sessions/code-lint-1" />
//   </GroupedList>
import { useId, type ReactNode } from 'react'
import { Link } from 'react-router-dom'

export function GroupedList({
  title,
  count,
  children,
  label,
  className,
  footer,
}: {
  /** The group's heading; omit for a list without one. */
  title?: ReactNode
  /** Shown quietly after the title. */
  count?: number
  children: ReactNode
  /** The list's accessible name when it has no visible title. */
  label?: string
  className?: string
  /** Below the panel: a "Show more" button, a cap note. */
  footer?: ReactNode
}) {
  const id = useId()
  return (
    <section className={`glist ${className ?? ''}`} aria-labelledby={title ? id : undefined} aria-label={title ? undefined : label}>
      {title && (
        <h2 className="glist-h" id={id}>
          {title}
          {count != null && <span className="glist-count"> {count.toLocaleString()}</span>}
        </h2>
      )}
      <ul className="glist-box">{children}</ul>
      {footer}
    </section>
  )
}

export function ListRow({
  glyph,
  title,
  summary,
  meta,
  time,
  timeTitle,
  to,
  className,
  trailing,
  ariaLabel,
}: {
  /** Usually a <StatusGlyph>; it sits at the left, top-aligned. */
  glyph?: ReactNode
  title: ReactNode
  summary?: ReactNode
  meta?: ReactNode
  time?: ReactNode
  /** The full date and time, as a tooltip on `time`. */
  timeTitle?: string
  /** Where the row goes; without it the row is plain text. */
  to?: string
  className?: string
  /** Something after the text block (rare: an action that is not the row's link must go outside the link). */
  trailing?: ReactNode
  ariaLabel?: string
}) {
  const body = (
    <>
      {glyph && <span className="lrow-glyph">{glyph}</span>}
      <span className="lrow-text">
        <span className="lrow-top">
          <span className="lrow-title">{title}</span>
          {time && (
            <span className="lrow-time" title={timeTitle}>
              {time}
            </span>
          )}
        </span>
        {summary && <span className="lrow-summary">{summary}</span>}
        {meta && <span className="lrow-meta">{meta}</span>}
      </span>
    </>
  )
  return (
    <li className={`lrow ${glyph ? 'has-glyph' : ''} ${className ?? ''}`}>
      {to ? (
        <Link className="lrow-link" to={to} aria-label={ariaLabel}>
          {body}
        </Link>
      ) : (
        <div className="lrow-link static">{body}</div>
      )}
      {trailing}
    </li>
  )
}
