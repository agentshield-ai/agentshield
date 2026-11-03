import { useEffect, useState } from 'react'
import { NavLink, Link, Outlet, useLocation, useParams } from 'react-router-dom'
import { UserMenu } from './UserMenu'
import { Icon, type IconName } from './ui/Icons'
import { RouteHelp } from './ui/RouteHelp'
import { ShortcutsHost } from './ui/Shortcuts'
import { ToastHost } from './ui/Toasts'

// Navigation (DESIGN.md §7): plain words, grouped by what people come to do.
export const NAV: { section: string; items: { to: string; label: string; icon: IconName; hint?: string }[] }[] = [
  {
    section: 'Observe',
    items: [
      { to: 'sessions', label: 'Sessions', icon: 'sessions', hint: 'What happened recently, and what needs attention' },
      { to: 'agents', label: 'Agents', icon: 'agents' },
      { to: 'teams', label: 'Teams', icon: 'teams' },
      { to: 'security', label: 'Security', icon: 'shield', hint: 'AgentShield verdicts' },
      { to: 'traces', label: 'Traces', icon: 'traces', hint: 'The raw span explorer' },
      { to: 'dashboards', label: 'Dashboards', icon: 'dashboards' },
      { to: 'monitors', label: 'Monitors', icon: 'monitors' },
    ],
  },
  {
    section: 'Evaluate',
    items: [
      { to: 'evaluations', label: 'Evaluations', icon: 'evaluations' },
      { to: 'scores', label: 'Scores', icon: 'scores' },
      { to: 'leaderboards', label: 'Leaderboards', icon: 'leaderboards' },
      { to: 'review-queues', label: 'Review queues', icon: 'review' },
      { to: 'playground', label: 'Playground', icon: 'playground' },
    ],
  },
  {
    section: 'Assets',
    items: [
      { to: 'datasets', label: 'Datasets', icon: 'datasets' },
      { to: 'models', label: 'Models', icon: 'models' },
      { to: 'prompts', label: 'Prompts', icon: 'prompts' },
      { to: 'scorers', label: 'Scorers', icon: 'scorers' },
      { to: 'functions', label: 'Functions', icon: 'functions', hint: 'Traced functions (ops) and their versions' },
      { to: 'objects', label: 'All objects', icon: 'objects' },
    ],
  },
  {
    section: 'Project',
    items: [
      { to: 'automations', label: 'Automations', icon: 'automations' },
      { to: 'settings', label: 'Settings', icon: 'settings' },
    ],
  },
]

export type Theme = 'system' | 'light' | 'dark'

export function readTheme(): Theme {
  try {
    const t = localStorage.getItem('weft.theme')
    return t === 'light' || t === 'dark' ? t : 'system'
  } catch {
    return 'system'
  }
}

export function applyTheme(t: Theme) {
  const root = document.documentElement
  if (t === 'system') root.removeAttribute('data-theme')
  else root.setAttribute('data-theme', t)
  try {
    if (t === 'system') localStorage.removeItem('weft.theme')
    else localStorage.setItem('weft.theme', t)
  } catch {
    /* storage unavailable */
  }
}

function ThemeSwitch() {
  const [t, setT] = useState<Theme>(readTheme)
  const order: Theme[] = ['system', 'light', 'dark']
  const label = t === 'system' ? 'Theme: system' : t === 'light' ? 'Theme: light' : 'Theme: dark'
  return (
    <button
      type="button"
      className="btn ghost sm side-btn"
      onClick={() => {
        const n = order[(order.indexOf(t) + 1) % order.length]
        applyTheme(n)
        setT(n)
      }}
      title="Switch between system, light and dark"
    >
      <Icon name={t === 'dark' ? 'moon' : t === 'light' ? 'sun' : 'spark'} size={14} /> {label}
    </button>
  )
}

export function Logo() {
  return (
    <svg width="22" height="22" viewBox="0 0 32 32" aria-hidden>
      <rect width="32" height="32" rx="7" fill="var(--accent)" />
      <path d="M7 10h18M7 16h18M7 22h18M11 6v20M16 6v20M21 6v20" stroke="var(--accent-text)" strokeWidth="2" opacity=".9" />
    </svg>
  )
}

export function ProjectLayout() {
  const { entity, project } = useParams()
  const [open, setOpen] = useState(false)
  const loc = useLocation()
  useEffect(() => setOpen(false), [loc.pathname])
  return (
    <div className={`app ${open ? 'nav-open' : ''}`}>
      <a href="#main" className="skip-link">
        Skip to content
      </a>
      <header className="topbar">
        <button
          type="button"
          className="icon-btn"
          aria-label={open ? 'Close navigation' : 'Open navigation'}
          aria-expanded={open}
          aria-controls="sidebar"
          onClick={() => setOpen((o) => !o)}
        >
          <Icon name={open ? 'close' : 'menu'} size={18} />
        </button>
        <Link to={`/${entity}/${project}/sessions`} className="brand">
          <Logo /> Weft
        </Link>
        <span className="topbar-project mono small">
          {entity}/{project}
        </span>
      </header>
      {open && <div className="nav-scrim" onClick={() => setOpen(false)} aria-hidden />}
      <nav className="sidebar" id="sidebar" aria-label="Main">
        <Link to="/" className="brand">
          <Logo /> Weft
        </Link>
        <Link to="/" className="project-switch" title="Switch project">
          <span className="faint small">{entity}</span>
          <span className="project-name-side">{project}</span>
          <span className="faint small">Switch project</span>
        </Link>
        {NAV.map((s) => (
          <div key={s.section} role="group" aria-labelledby={`nav-${s.section}`}>
            <div className="nav-section" id={`nav-${s.section}`}>
              {s.section}
            </div>
            {s.items.map((it) => (
              <NavLink key={it.to} to={`/${entity}/${project}/${it.to}`} className={({ isActive }) => `nav-link ${isActive ? 'active' : ''}`} title={it.hint}>
                <Icon name={it.icon} size={16} />
                {it.label}
              </NavLink>
            ))}
          </div>
        ))}
        <div className="side-foot">
          <ThemeSwitch />
          <button type="button" className="btn ghost sm side-btn" onClick={() => window.dispatchEvent(new Event('weft:shortcuts'))}>
            <Icon name="keyboard" size={14} /> Keyboard shortcuts <kbd className="kbd">?</kbd>
          </button>
        </div>
        <UserMenu />
      </nav>
      <main className="main" id="main" tabIndex={-1}>
        <RouteHelp />
        <Outlet />
      </main>
      <ShortcutsHost />
      <ToastHost />
    </div>
  )
}

export function PlainLayout() {
  return (
    <div className="app plain">
      <main className="main" id="main">
        <Outlet />
      </main>
      <ToastHost />
    </div>
  )
}
