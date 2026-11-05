// Settings-style grouped lists (DESIGN.md §7.2 rules 2, 3 and 6), for the
// Project pages: Settings, Automations, Users & teams, Projects. A section is
// a heading (with at most a quiet action beside it), a white panel with 14 px
// corners and a hairline edge, and labelled rows with their controls on the
// right, as in a desktop settings app.
//
//   <SettingsSection title="Retention" action={<button className="qbtn">…</button>}>
//     <SettingsRow label="Delete spans after" detail="Feedback on them goes too." control={<input …/>} />
//   </SettingsSection>
//
// useUndoDelete: undo over confirm (§7.1 rule 11). The row disappears at
// once, a toast offers Undo for a few seconds, and only then is the delete
// sent.
import { useCallback, useId, useState, type ReactNode } from 'react'
import { Sentence } from '../ui/Sentence'
import { StatusGlyph } from '../ui/StatusGlyph'
import { undoable } from '../ui/Toasts'
import './settings.css'

export function SettingsSection({
  title,
  action,
  children,
  note,
  className,
  plain,
}: {
  title?: ReactNode
  /** A quiet button or link beside the heading ("Add price"). */
  action?: ReactNode
  children: ReactNode
  /** A short grey note under the panel. */
  note?: ReactNode
  className?: string
  /** No panel: the children carry their own (a table, an empty state). */
  plain?: boolean
}) {
  const id = useId()
  return (
    <section className={`ssection ${className ?? ''}`} aria-labelledby={title ? id : undefined}>
      {(title || action) && (
        <div className="ssection-head">
          {title && (
            <h2 className="ssection-title" id={id}>
              {title}
            </h2>
          )}
          {action && <div className="ssection-action">{action}</div>}
        </div>
      )}
      {plain ? children : <ul className="srows">{children}</ul>}
      {note && <p className="ssection-note">{note}</p>}
    </section>
  )
}

export function SettingsRow({
  label,
  detail,
  control,
  children,
  className,
  muted,
  labelFor,
}: {
  label: ReactNode
  /** One or two quiet lines under the label. */
  detail?: ReactNode
  /** On the right: a switch, a select, buttons. */
  control?: ReactNode
  /** Below the row's text, full width (an editor, a list of chips). */
  children?: ReactNode
  className?: string
  /** Greyed (a revoked key, a disabled user). */
  muted?: boolean
  /** The id of the input the label names. */
  labelFor?: string
}) {
  return (
    <li className={`srow ${muted ? 'muted-row' : ''} ${className ?? ''}`}>
      <div className="srow-main">
        <div className="srow-text">
          {labelFor ? (
            <label className="srow-label" htmlFor={labelFor}>
              {label}
            </label>
          ) : (
            <span className="srow-label">{label}</span>
          )}
          {detail && <span className="srow-detail">{detail}</span>}
        </div>
        {control && <div className="srow-control">{control}</div>}
      </div>
      {children && <div className="srow-extra">{children}</div>}
    </li>
  )
}

/** The answer at the top of a section. */
export function SettingsSentence({ children }: { children: string | null | undefined }) {
  if (!children) return null
  return <Sentence parts={[children]} size="detail" />
}

/** Words with a glyph: "Set", "Not set", "Authorized". */
export function StatusWords({ tone, children }: { tone: 'ok' | 'warn' | 'error' | 'muted'; children: ReactNode }) {
  const kind = tone === 'ok' ? 'check' : tone === 'error' ? 'failed' : tone === 'warn' ? 'warning' : 'unknown'
  return (
    <span className={`status-words ${tone === 'muted' ? '' : `tone-${tone}`}`}>
      <StatusGlyph kind={kind} size={14} decorative />
      {children}
    </span>
  )
}

/** A row that says the list is empty, inside a section's panel. */
export function SettingsEmpty({ children }: { children: ReactNode }) {
  return <li className="srow srow-empty">{children}</li>
}

/** Hide-now, delete-after-undo-window deletions keyed by a string. */
export function useUndoDelete() {
  const [hidden, setHidden] = useState<Set<string>>(() => new Set())
  const remove = useCallback(
    (key: string, message: string, commit: () => Promise<unknown>) =>
      undoable({
        message,
        apply: () => setHidden((h) => new Set(h).add(key)),
        revert: () =>
          setHidden((h) => {
            const n = new Set(h)
            n.delete(key)
            return n
          }),
        commit,
      }),
    [],
  )
  return { hidden, remove }
}
