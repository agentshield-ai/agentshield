import { useEffect, useState, type ReactNode } from 'react'
import type { CallStatus } from '../api/types'

export function Spinner({ label }: { label?: string }) {
  return (
    <span className="row muted small">
      <span className="spinner" />
      {label}
    </span>
  )
}

export function Loading({ label = 'Loading…' }: { label?: string }) {
  return (
    <div className="empty">
      <Spinner label={label} />
    </div>
  )
}

export function ErrorBox({ error }: { error: unknown }) {
  if (!error) return null
  const msg = error instanceof Error ? error.message : String(error)
  return <div className="error-box">{msg}</div>
}

export function Empty({ children }: { children: ReactNode }) {
  return <div className="empty">{children}</div>
}

export function Tabs<T extends string>({
  tabs,
  value,
  onChange,
}: {
  tabs: { id: T; label: ReactNode }[]
  value: T
  onChange: (id: T) => void
}) {
  return (
    <div className="tabs" role="tablist">
      {tabs.map((t) => (
        <button key={t.id} role="tab" aria-selected={t.id === value} className={`tab ${t.id === value ? 'active' : ''}`} onClick={() => onChange(t.id)}>
          {t.label}
        </button>
      ))}
    </div>
  )
}

export function Modal({
  title,
  onClose,
  children,
  footer,
  wide,
}: {
  title: ReactNode
  onClose: () => void
  children: ReactNode
  footer?: ReactNode
  wide?: boolean
}) {
  useEffect(() => {
    const h = (e: KeyboardEvent) => e.key === 'Escape' && onClose()
    window.addEventListener('keydown', h)
    return () => window.removeEventListener('keydown', h)
  }, [onClose])
  return (
    <div className="modal-backdrop" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div className={`modal ${wide ? 'wide' : ''}`} role="dialog" aria-modal="true">
        <div className="modal-header">
          <h2 className="grow">{title}</h2>
          <button className="btn ghost icon" onClick={onClose} aria-label="Close">
            ✕
          </button>
        </div>
        <div className="modal-body">{children}</div>
        {footer && <div className="modal-footer">{footer}</div>}
      </div>
    </div>
  )
}

export function CopyButton({ text, label = 'Copy' }: { text: string; label?: string }) {
  const [done, setDone] = useState(false)
  return (
    <button
      className="btn sm"
      onClick={() => {
        void navigator.clipboard?.writeText(text)
        setDone(true)
        setTimeout(() => setDone(false), 1200)
      }}
    >
      {done ? 'Copied' : label}
    </button>
  )
}

const STATUS_CLASS: Record<CallStatus, string> = {
  success: 'success',
  error: 'error',
  running: 'running',
  descendant_error: 'warn',
}
const STATUS_LABEL: Record<CallStatus, string> = {
  success: 'Success',
  error: 'Error',
  running: 'Running',
  descendant_error: 'Child error',
}

export function StatusBadge({ status }: { status: CallStatus }) {
  return <span className={`badge ${STATUS_CLASS[status]}`}>{STATUS_LABEL[status]}</span>
}

export function StatusDot({ status }: { status: CallStatus }) {
  const color =
    status === 'success' ? 'var(--success)' : status === 'error' ? 'var(--error)' : status === 'running' ? 'var(--running)' : 'var(--warn)'
  return <span className="dot" style={{ background: color }} title={STATUS_LABEL[status]} />
}

export function Pager({
  offset,
  limit,
  total,
  onChange,
}: {
  offset: number
  limit: number
  total: number | null
  onChange: (offset: number) => void
}) {
  const page = Math.floor(offset / limit) + 1
  const pages = total == null ? null : Math.max(1, Math.ceil(total / limit))
  return (
    <div className="row small muted">
      <button className="btn sm" disabled={offset === 0} onClick={() => onChange(Math.max(0, offset - limit))}>
        ‹ Prev
      </button>
      <span>
        Page {page}
        {pages != null && ` of ${pages}`}
        {total != null && ` · ${total.toLocaleString()} total`}
      </span>
      <button className="btn sm" disabled={total != null && offset + limit >= total} onClick={() => onChange(offset + limit)}>
        Next ›
      </button>
    </div>
  )
}

export function Section({ title, actions, children }: { title: ReactNode; actions?: ReactNode; children: ReactNode }) {
  return (
    <div className="card">
      <div className="card-header">
        <h3 className="grow">{title}</h3>
        {actions}
      </div>
      <div className="card-body">{children}</div>
    </div>
  )
}
