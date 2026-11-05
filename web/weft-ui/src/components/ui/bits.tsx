// Shared presentational pieces for the agent-first pages: tooltips that
// explain numbers, status pills (icon + text, never colour alone), agent
// chips with their consistent colour, copyable ids and breadcrumbs.
import { useEffect, useId, useRef, useState, type CSSProperties, type ReactNode } from 'react'
import { Link } from 'react-router-dom'
import type { DetectionAction, SessionStatus } from '../../api/v1'
import { agentColor } from '../../lib/agentColor'
import { Icon, type IconName } from './Icons'

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

export function AgentChip({ name, to, size }: { name: string | null | undefined; to?: string | null; size?: 'sm' }) {
  if (!name) return <span className="agent-chip muted">unknown agent</span>
  const style = { '--ac': agentColor(name) } as CSSProperties
  const body = (
    <>
      <span className="agent-dot" aria-hidden />
      <span className="agent-name">{name}</span>
    </>
  )
  const cls = `agent-chip ${size === 'sm' ? 'sm' : ''}`
  return to ? (
    <Link className={cls} style={style} to={to} title={`Agent ${name}`}>
      {body}
    </Link>
  ) : (
    <span className={cls} style={style}>
      {body}
    </span>
  )
}

export function AgentDot({ name }: { name: string | null | undefined }) {
  return <span className="agent-dot" aria-hidden style={{ '--ac': agentColor(name) } as CSSProperties} />
}

const STATUS: Record<SessionStatus, { icon: IconName; label: string; cls: string }> = {
  ok: { icon: 'ok', label: 'Succeeded', cls: 'ok' },
  error: { icon: 'error', label: 'Failed', cls: 'error' },
  running: { icon: 'running', label: 'Running', cls: 'running' },
  // Known only from AgentShield verdicts: no agent spans, so no outcome.
  unknown: { icon: 'info', label: 'No agent data', cls: 'muted' },
}

export function StatusPill({ status, label }: { status: SessionStatus | string; label?: string }) {
  const s = STATUS[(String(status).toLowerCase() === 'unset' ? 'ok' : String(status).toLowerCase()) as SessionStatus] ?? STATUS.ok
  return (
    <span className={`pill ${s.cls}`}>
      <Icon name={s.icon} size={13} />
      {label ?? s.label}
    </span>
  )
}

export const ACTION_META: Record<DetectionAction, { icon: IconName; label: string; cls: string; help: string }> = {
  block: { icon: 'block', label: 'Blocked', cls: 'error', help: 'AgentShield stopped this tool call.' },
  require_approval: { icon: 'approval', label: 'Needs approval', cls: 'warn', help: 'AgentShield asked a person to approve this tool call.' },
  log: { icon: 'log', label: 'Logged', cls: 'info', help: 'AgentShield recorded an alert but let the call run.' },
  allow: { icon: 'ok', label: 'Allowed', cls: 'ok', help: 'No rule matched.' },
}

export function ActionBadge({ action, count, overridden }: { action: DetectionAction; count?: number; overridden?: boolean }) {
  const m = ACTION_META[action] ?? ACTION_META.log
  return (
    <span className={`pill ${m.cls}`} title={m.help}>
      <Icon name={m.icon} size={13} />
      {m.label}
      {count != null && count > 1 && <span className="pill-count">×{count}</span>}
      {overridden && <span className="pill-note">overridden</span>}
    </span>
  )
}

/** One badge per non-allow action, most severe first. */
export function DetectionBadges({ counts }: { counts: Partial<Record<DetectionAction, number>> | null | undefined }) {
  const order: DetectionAction[] = ['block', 'require_approval', 'log']
  const xs = order.filter((a) => (counts?.[a] ?? 0) > 0)
  if (!xs.length) return null
  return (
    <span className="row" style={{ gap: 4, flexWrap: 'wrap' }}>
      {xs.map((a) => (
        <ActionBadge key={a} action={a} count={counts![a]} />
      ))}
    </span>
  )
}

export function SeverityText({ level }: { level: string }) {
  const l = (level || 'none').toLowerCase()
  return <span className={`sev sev-${l}`}>{l === 'none' ? 'no severity' : l}</span>
}

export function scoreTone(v: number | null | undefined): 'ok' | 'warn' | 'error' | 'muted' {
  if (v == null || Number.isNaN(v)) return 'muted'
  if (v < 0.5) return 'error'
  if (v < 0.75) return 'warn'
  return 'ok'
}

export function ScorePill({ value, scorer, reason, count }: { value: number | null | undefined; scorer?: string; reason?: string; count?: number }) {
  const tone = scoreTone(value)
  const text = value == null ? '—' : Number.isInteger(value) ? String(value) : value.toFixed(2)
  const title = [scorer, count && count > 1 ? `mean of ${count} results` : null, reason].filter(Boolean).join(' · ')
  return (
    <span className={`pill score ${tone}`} title={title || undefined}>
      {scorer && <span className="pill-scorer">{humanScorer(scorer)}</span>}
      <strong>{text}</strong>
    </span>
  )
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

export function Callout({ tone = 'info', title, children, actions }: { tone?: 'info' | 'warn' | 'error' | 'ok'; title?: ReactNode; children?: ReactNode; actions?: ReactNode }) {
  const icon: IconName = tone === 'error' ? 'error' : tone === 'warn' ? 'approval' : tone === 'ok' ? 'ok' : 'info'
  return (
    <div className={`callout ${tone}`} role={tone === 'error' ? 'alert' : undefined}>
      <Icon name={icon} size={16} />
      <div className="grow">
        {title && <div className="callout-title">{title}</div>}
        {children}
      </div>
      {actions && <div className="callout-actions">{actions}</div>}
    </div>
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
