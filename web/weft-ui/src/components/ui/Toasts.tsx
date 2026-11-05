// Toasts: undoable deletions (lib/undo.ts) and short notices. A dark slip at
// the bottom of the window: the message, then Undo (a link-styled button)
// with the time left as a thin line; notices carry a glyph, never colour alone.
import { useEffect, useState } from 'react'
import { undoQueue, type PendingUndo, type UndoAction } from '../../lib/undo'
import { Icon } from './Icons'
import { StatusGlyph } from './StatusGlyph'

interface Notice {
  id: number
  message: string
  tone: 'info' | 'error' | 'ok'
}

let noticeSeq = 0
const noticeListeners = new Set<(n: Notice[]) => void>()
let notices: Notice[] = []

export function notify(message: string, tone: Notice['tone'] = 'info', ms = 4000) {
  const n = { id: ++noticeSeq, message, tone }
  notices = [...notices, n]
  noticeListeners.forEach((l) => l(notices))
  setTimeout(() => {
    notices = notices.filter((x) => x.id !== n.id)
    noticeListeners.forEach((l) => l(notices))
  }, ms)
}

/** Delete with an Undo toast instead of a confirmation dialog. */
export function undoable(action: UndoAction): number {
  return undoQueue.schedule({
    ...action,
    onError: (e) => {
      action.onError?.(e)
      notify(`Could not finish: ${e instanceof Error ? e.message : String(e)}`, 'error', 6000)
    },
  })
}

export function ToastHost() {
  const [pending, setPending] = useState<PendingUndo[]>(undoQueue.pending())
  const [ns, setNs] = useState<Notice[]>(notices)
  useEffect(() => undoQueue.subscribe(setPending), [])
  useEffect(() => {
    noticeListeners.add(setNs)
    return () => void noticeListeners.delete(setNs)
  }, [])
  useEffect(() => {
    const flush = () => void undoQueue.flush()
    window.addEventListener('pagehide', flush)
    return () => window.removeEventListener('pagehide', flush)
  }, [])
  return (
    <div className="toast-host" aria-live="polite" aria-atomic="false">
      {pending.map((p) => (
        <div key={`u${p.id}`} className="toast" role="status">
          <span className="grow">{p.message}</span>
          <button type="button" className="toast-undo" onClick={() => undoQueue.undo(p.id)}>
            Undo
          </button>
          <span className="toast-timer" style={{ animationDuration: `${Math.max(0, p.expiresAt - Date.now())}ms` }} aria-hidden />
        </div>
      ))}
      {ns.map((n) => (
        <div key={`n${n.id}`} className={`toast ${n.tone}`} role={n.tone === 'error' ? 'alert' : 'status'}>
          <span className="toast-glyph">
            {n.tone === 'info' ? <Icon name="info" /> : <StatusGlyph kind={n.tone === 'error' ? 'failed' : 'check'} size={16} decorative />}
          </span>
          <span className="grow">{n.message}</span>
        </div>
      ))}
    </div>
  )
}
