// Shared presentational pieces: tooltips that explain numbers, a scrolling
// box for wide tables, copyable ids, breadcrumbs and the cap note. Status is
// a StatusGlyph with words and a score is text (no pills or badges).
import { useEffect, useId, useRef, useState, type ReactNode } from 'react'
import { Link } from 'react-router-dom'
import { Icon } from './Icons'

export function Tip({ text, label = 'How is this computed?' }: { text: ReactNode; label?: string }) {
  const id = useId()
  return (
    <span className="tip">
      <button type="button" className="tip-btn" aria-label={label} aria-describedby={id}>
        <Icon name="info" size={13} />
      </button>
      <span role="tooltip" id={id} className="tip-body">
        {text}
      </span>
    </span>
  )
}

// TableScroll holds a table that can be wider than its column (a narrow
// screen, a wide font or letter spacing, a long unbroken name): the table
// scrolls inside it instead of widening the page. While it scrolls it is a
// labelled, focusable region, so it can be scrolled from the keyboard.
export function TableScroll({ label, children }: { label: string; children: ReactNode }) {
  const ref = useRef<HTMLDivElement>(null)
  const [scrolls, setScrolls] = useState(false)
  useEffect(() => {
    const el = ref.current
    if (!el || typeof ResizeObserver === 'undefined') return
    const check = () => setScrolls(el.scrollWidth > el.clientWidth + 1)
    const ro = new ResizeObserver(check)
    ro.observe(el)
    if (el.firstElementChild) ro.observe(el.firstElementChild)
    check()
    return () => ro.disconnect()
  }, [])
  return (
    <div ref={ref} className="table-scroll" {...(scrolls ? { tabIndex: 0, role: 'region', 'aria-label': label } : {})}>
      {children}
    </div>
  )
}

export function SeverityText({ level }: { level: string }) {
  const l = (level || 'none').toLowerCase()
  return <span className={`sev sev-${l}`}>{l === 'none' ? 'no severity' : l}</span>
}

export function humanScorer(name: string): string {
  return name
    .replace(/Judge$|Scorer$/, '')
    .replace(/([a-z])([A-Z])/g, '$1 $2')
    .replace(/^./, (c) => c.toUpperCase())
}

export function CopyId({ id, label = 'ID', short = 10 }: { id: string; label?: string; short?: number }) {
  const [done, setDone] = useState(false)
  const shown = id.length > short + 2 ? `${id.slice(0, short)}…` : id
  return (
    <span className="copy-id">
      <span className="mono faint" title={id}>
        {shown}
      </span>
      <button
        type="button"
        className="icon-btn"
        aria-label={`Copy ${label.toLowerCase()} ${id}`}
        title={`Copy ${label.toLowerCase()}`}
        onClick={() => {
          void navigator.clipboard?.writeText(id).catch(() => undefined)
          setDone(true)
          setTimeout(() => setDone(false), 1200)
        }}
      >
        <Icon name={done ? 'ok' : 'copy'} size={13} />
      </button>
    </span>
  )
}

export interface Crumb {
  label: ReactNode
  to?: string
  title?: string
}

export function Crumbs({ items }: { items: Crumb[] }) {
  return (
    <nav aria-label="Breadcrumb" className="crumbs">
      <ol>
        {items.map((c, i) => (
          <li key={i} aria-current={i === items.length - 1 ? 'page' : undefined}>
            {c.to && i < items.length - 1 ? (
              <Link to={c.to} title={c.title}>
                {c.label}
              </Link>
            ) : (
              <span title={c.title}>{c.label}</span>
            )}
          </li>
        ))}
      </ol>
    </nav>
  )
}

export function Panel({
  title,
  tip,
  actions,
  children,
  className,
  id,
}: {
  title: ReactNode
  tip?: ReactNode
  actions?: ReactNode
  children: ReactNode
  className?: string
  id?: string
}) {
  return (
    <section className={`panel ${className ?? ''}`} aria-labelledby={id ? `${id}-h` : undefined}>
      <header className="panel-head">
        <h2 id={id ? `${id}-h` : undefined}>
          {title}
          {tip && <Tip text={tip} />}
        </h2>
        <span className="grow" />
        {actions}
      </header>
      <div className="panel-body">{children}</div>
    </section>
  )
}

export function Bar({ value, max, tone = 'accent', label }: { value: number; max: number; tone?: 'accent' | 'error' | 'warn' | 'ok'; label?: string }) {
  const pct = max > 0 ? Math.max(2, Math.round((value / max) * 100)) : 0
  return (
    <span className={`hbar ${tone}`} role="img" aria-label={label ?? `${value} of ${max}`}>
      <span style={{ width: `${pct}%` }} />
    </span>
  )
}

/** Says a list or figure was built from only the first `shown` of `total` items, so a cap is never silent. */
export function CapNote({ shown, total, what, why }: { shown: number; total: number; what: string; why?: ReactNode }) {
  if (total <= shown) return null
  return (
    <p className="cap-note" role="status">
      <Icon name="info" size={13} /> Showing the first {shown.toLocaleString()} of {total.toLocaleString()} {what}
      {why ? <>: {why}</> : '.'}
    </p>
  )
}
