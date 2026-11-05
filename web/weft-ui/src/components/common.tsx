import { useEffect, useId, useState, type ReactNode } from 'react'
import './common.css'

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
  const id = useId()
  useEffect(() => {
    const h = (e: KeyboardEvent) => e.key === 'Escape' && onClose()
    window.addEventListener('keydown', h)
    return () => window.removeEventListener('keydown', h)
  }, [onClose])
  return (
    <div className="modal-backdrop" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div className={`modal ${wide ? 'wide' : ''}`} role="dialog" aria-modal="true" aria-labelledby={id}>
        <div className="modal-header">
          <h2 className="grow" id={id}>
            {title}
          </h2>
          <button type="button" className="modal-close" onClick={onClose} aria-label="Close">
            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.4" strokeLinecap="round" aria-hidden="true" focusable="false">
              <path d="M6 6l12 12M18 6L6 18" />
            </svg>
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
      type="button"
      className="qbtn sm copy-btn"
      aria-label={label === 'Copy' ? undefined : label}
      onClick={() => {
        void navigator.clipboard?.writeText(text).catch(() => undefined)
        setDone(true)
        setTimeout(() => setDone(false), 1200)
      }}
    >
      {done ? 'Copied' : 'Copy'}
    </button>
  )
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
