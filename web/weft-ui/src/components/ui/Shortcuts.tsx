// Global keyboard shortcuts (DESIGN.md §7.1 rule 10): `?` lists them, `/`
// focuses the filter, `g` then a letter jumps to a page. Pages add their own
// (j/k on the session story).
import { useEffect, useRef, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { Modal } from '../common'

export const SHORTCUTS: { keys: string[]; what: string; where?: string }[] = [
  { keys: ['?'], what: 'Show this list' },
  { keys: ['/'], what: 'Focus the search box' },
  { keys: ['j'], what: 'Next step', where: 'Session story' },
  { keys: ['k'], what: 'Previous step', where: 'Session story' },
  { keys: ['Enter'], what: 'Select the focused step', where: 'Session story' },
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
  const groups = [...new Set(SHORTCUTS.map((s) => s.where ?? 'Everywhere'))]
  return (
    <Modal title="Keyboard shortcuts" onClose={() => setOpen(false)}>
      <div className="shortcut-groups">
        {groups.map((g) => (
          <section key={g} className="shortcut-group" aria-label={g}>
            <h3>{g}</h3>
            <ul className="shortcut-list">
              {SHORTCUTS.filter((s) => (s.where ?? 'Everywhere') === g).map((s) => (
                <li key={s.keys.join('+') + s.what}>
                  <span>{s.what}</span>
                  <span className="shortcut-keys">
                    <span className="sr-only">{s.keys.join(' then ')}</span>
                    {s.keys.map((k, i) => (
                      <span key={i} aria-hidden="true">
                        {i > 0 && <span> then </span>}
                        <kbd>{k}</kbd>
                      </span>
                    ))}
                  </span>
                </li>
              ))}
            </ul>
          </section>
        ))}
      </div>
    </Modal>
  )
}
