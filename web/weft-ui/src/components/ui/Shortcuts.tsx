// Global keyboard shortcuts (DESIGN.md §7.1 rule 10): `?` lists them, `/`
// focuses the filter, `g` then a letter jumps to a page. Pages add their own
// (j/k on the session timeline).
import { useEffect, useRef, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { Modal } from '../common'

export const SHORTCUTS: { keys: string[]; what: string; where?: string }[] = [
  { keys: ['?'], what: 'Show this list' },
  { keys: ['/'], what: 'Focus the filter box' },
  { keys: ['j'], what: 'Next step', where: 'Session timeline' },
  { keys: ['k'], what: 'Previous step', where: 'Session timeline' },
  { keys: ['Enter'], what: 'Expand or collapse the focused step', where: 'Session timeline' },
  { keys: ['g', 's'], what: 'Go to Sessions' },
  { keys: ['g', 'a'], what: 'Go to Agents' },
  { keys: ['g', 't'], what: 'Go to Teams' },
  { keys: ['g', 'x'], what: 'Go to Security' },
  { keys: ['g', 'r'], what: 'Go to Traces' },
  { keys: ['Esc'], what: 'Close a dialog or clear the filter box' },
]

const GO: Record<string, string> = { s: 'sessions', a: 'agents', t: 'teams', x: 'security', r: 'traces' }

export function isTyping(e: KeyboardEvent): boolean {
  const t = e.target as HTMLElement | null
  if (!t) return false
  return t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.tagName === 'SELECT' || t.isContentEditable
}

export function focusFilter(): boolean {
  const el = document.querySelector<HTMLInputElement>('[data-filter-input]')
  if (!el) return false
  el.focus()
  el.select?.()
  return true
}

export function ShortcutsHost() {
  const [open, setOpen] = useState(false)
  const navigate = useNavigate()
  const { entity, project } = useParams()
  const pendingG = useRef<number>(0)
  useEffect(() => {
    const h = (e: KeyboardEvent) => {
      if (e.metaKey || e.ctrlKey || e.altKey || isTyping(e)) return
      if (document.querySelector('.modal') && e.key !== '?') return
      if (e.key === '?') {
        e.preventDefault()
        setOpen((o) => !o)
        return
      }
      if (e.key === '/') {
        if (focusFilter()) e.preventDefault()
        return
      }
      if (e.key === 'g') {
        pendingG.current = Date.now()
        return
      }
      if (pendingG.current && Date.now() - pendingG.current < 1200 && GO[e.key] && entity && project) {
        pendingG.current = 0
        e.preventDefault()
        navigate(`/${entity}/${project}/${GO[e.key]}`)
      }
    }
    window.addEventListener('keydown', h)
    return () => window.removeEventListener('keydown', h)
  }, [navigate, entity, project])
  useEffect(() => {
    const show = () => setOpen(true)
    window.addEventListener('weft:shortcuts', show)
    return () => window.removeEventListener('weft:shortcuts', show)
  }, [])
  if (!open) return null
  return (
    <Modal title="Keyboard shortcuts" onClose={() => setOpen(false)}>
      <table className="table shortcuts">
        <caption className="sr-only">Keyboard shortcuts</caption>
        <thead>
          <tr>
            <th scope="col">Keys</th>
            <th scope="col">Action</th>
            <th scope="col">Where</th>
          </tr>
        </thead>
        <tbody>
          {SHORTCUTS.map((s) => (
            <tr key={s.keys.join('+') + s.what}>
              <td>
                {s.keys.map((k, i) => (
                  <span key={i}>
                    {i > 0 && <span className="faint"> then </span>}
                    <kbd className="kbd">{k}</kbd>
                  </span>
                ))}
              </td>
              <td>{s.what}</td>
              <td className="muted">{s.where ?? 'Everywhere'}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </Modal>
  )
}
