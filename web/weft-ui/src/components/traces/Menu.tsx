import { useEffect, useLayoutEffect, useRef, useState, type CSSProperties, type ReactNode, type RefObject } from 'react'
import { Modal } from '../common'

/** Keeps an absolutely positioned popover inside the viewport by flipping its horizontal anchor. */
export function useFlip(open: boolean): [RefObject<HTMLDivElement>, CSSProperties] {
  const ref = useRef<HTMLDivElement>(null)
  const [style, setStyle] = useState<CSSProperties>({})
  useLayoutEffect(() => {
    if (!open) {
      setStyle({})
      return
    }
    const el = ref.current
    if (!el) return
    const r = el.getBoundingClientRect()
    const vw = document.documentElement.clientWidth
    if (r.right > vw - 8) setStyle({ left: 'auto', right: 0 })
    else if (r.left < 8) setStyle({ left: 0, right: 'auto' })
  }, [open])
  return [ref, style]
}

/** A button that opens a popover. Closes on outside click and Escape. */
export function Dropdown({
  label,
  children,
  align = 'left',
  className = 'btn sm',
  disabled,
  title,
  kind = 'menu',
  ariaLabel,
}: {
  label: ReactNode
  children: ReactNode | ((close: () => void) => ReactNode)
  align?: 'left' | 'right'
  className?: string
  disabled?: boolean
  title?: string
  /** 'menu' holds MenuItems; 'dialog' holds any controls (a small popover). */
  kind?: 'menu' | 'dialog'
  /** The button's accessible name, when its label is not words. */
  ariaLabel?: string
}) {
  const [open, setOpen] = useState(false)
  const ref = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (!open) return
    const onDown = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false)
    }
    const onKey = (e: KeyboardEvent) => e.key === 'Escape' && setOpen(false)
    document.addEventListener('mousedown', onDown)
    document.addEventListener('keydown', onKey)
    return () => {
      document.removeEventListener('mousedown', onDown)
      document.removeEventListener('keydown', onKey)
    }
  }, [open])
  const close = () => setOpen(false)
  const [menuRef, menuStyle] = useFlip(open)
  return (
    <div className="dd" ref={ref}>
      <button className={className} onClick={() => setOpen((o) => !o)} disabled={disabled} aria-haspopup={kind} aria-expanded={open} title={title} aria-label={ariaLabel}>
        {label}
      </button>
      {open && (
        <div
          ref={menuRef}
          style={menuStyle}
          className={`dd-menu ${align === 'right' ? 'right' : ''} ${kind === 'dialog' ? 'dd-pop' : ''}`}
          role={kind}
          aria-label={kind === 'dialog' ? (ariaLabel ?? (typeof label === 'string' ? label : undefined)) : undefined}
        >
          {typeof children === 'function' ? children(close) : children}
        </div>
      )}
    </div>
  )
}

export function MenuItem({ onClick, children, disabled, danger }: { onClick: () => void; children: ReactNode; disabled?: boolean; danger?: boolean }) {
  return (
    <button className={`dd-item ${danger ? 'danger' : ''}`} role="menuitem" onClick={onClick} disabled={disabled}>
      {children}
    </button>
  )
}

export function ConfirmModal({
  title,
  body,
  confirmLabel = 'Confirm',
  danger,
  busy,
  error,
  onConfirm,
  onClose,
}: {
  title: ReactNode
  body: ReactNode
  confirmLabel?: string
  danger?: boolean
  busy?: boolean
  error?: unknown
  onConfirm: () => void
  onClose: () => void
}) {
  return (
    <Modal
      title={title}
      onClose={onClose}
      footer={
        <>
          <button className="btn" onClick={onClose}>
            Cancel
          </button>
          <button className={`btn ${danger ? 'danger-solid' : 'primary'}`} onClick={onConfirm} disabled={busy} autoFocus>
            {busy ? 'Working…' : confirmLabel}
          </button>
        </>
      }
    >
      <div>{body}</div>
      {error ? <div className="error-box">{error instanceof Error ? error.message : String(error)}</div> : null}
    </Modal>
  )
}
