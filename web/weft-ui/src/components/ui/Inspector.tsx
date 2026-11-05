// Inspector and KeyValueList: detail on demand (DESIGN.md §7.2 rule 7). Ids,
// token counts, decisions and links to the raw records live here, beside
// the reading flow, never in it. A white panel with 14 px corners and a
// hairline edge; on a wide screen it sits beside the content and stays in
// view while the content scrolls (`sticky`).
//
//   <Inspector title="Blocked command" subtitle="coder · step 2 of 6" label="Selected step">
//     <KeyValueList items={[
//       { label: 'Tool', value: 'Bash' },
//       { label: 'Decision', value: 'Blocked', tone: 'error' },
//       { label: 'Call id', value: <CopyText text="toolu_1" />, mono: true },
//     ]} />
//     <InspectorDivider />
//     <InspectorLinks>
//       <Link to={…}>Every detection of this rule</Link>
//       <button className="linkish" onClick={…}>Replay with the current rules</button>
//     </InspectorLinks>
//   </Inspector>
import { useState, type ReactNode } from 'react'
import { Icon } from './Icons'

export function Inspector({
  title,
  subtitle,
  label = 'Details',
  children,
  sticky,
  className,
  id,
}: {
  title: ReactNode
  subtitle?: ReactNode
  /** The landmark's accessible name. */
  label?: string
  children: ReactNode
  /** Stay in view while the page scrolls (beside a long story or list). */
  sticky?: boolean
  className?: string
  id?: string
}) {
  return (
    <aside id={id} className={`inspector ${sticky ? 'sticky' : ''} ${className ?? ''}`} aria-label={label}>
      <div className="insp-head">
        <h2 className="insp-title">{title}</h2>
        {subtitle && <span className="insp-sub">{subtitle}</span>}
      </div>
      {children}
    </aside>
  )
}

export interface KeyValue {
  label: string
  value: ReactNode
  /** Red for a block or failure, amber for an approval. */
  tone?: 'error' | 'warn' | 'ok'
  /** Monospace (ids, commands). */
  mono?: boolean
  /** A tooltip on the label: how the value is computed. */
  help?: string
}

/** Label/value pairs in two columns (labels 96 px, values wrap anywhere). */
export function KeyValueList({ items, className }: { items: (KeyValue | null | false | undefined)[]; className?: string }) {
  const xs = items.filter(Boolean) as KeyValue[]
  if (!xs.length) return null
  return (
    <dl className={`kvlist ${className ?? ''}`}>
      {xs.map((x) => (
        <div key={x.label} className="kvlist-pair">
          <dt title={x.help}>{x.label}</dt>
          <dd className={`${x.tone ? `tone-${x.tone}` : ''} ${x.mono ? 'mono' : ''}`}>{x.value}</dd>
        </div>
      ))}
    </dl>
  )
}

export function InspectorDivider() {
  return <div className="insp-divider" role="presentation" />
}

/** A column of links and link-styled buttons (actions on the selected thing). */
export function InspectorLinks({ children }: { children: ReactNode }) {
  return <div className="insp-links">{children}</div>
}

/** Text with a copy button, for ids in the inspector. */
export function CopyText({ text, label = 'id' }: { text: string; label?: string }) {
  const [done, setDone] = useState(false)
  return (
    <span className={`copy-text ${done ? 'done' : ''}`}>
      <span className="copy-text-value">{text}</span>
      <button
        type="button"
        className="copy-text-btn"
        aria-label={`Copy ${label} ${text}`}
        title={done ? 'Copied' : `Copy ${label}`}
        onClick={() => {
          void navigator.clipboard?.writeText(text).catch(() => undefined)
          setDone(true)
          setTimeout(() => setDone(false), 1200)
        }}
      >
        <Icon name={done ? 'ok' : 'copy'} size={13} />
      </button>
    </span>
  )
}
