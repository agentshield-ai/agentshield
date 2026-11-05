import { useEffect, useState } from 'react'
import { Link, Outlet, useLocation, useParams } from 'react-router-dom'
import { UserMenu } from './UserMenu'
import { Icon, type IconName } from './ui/Icons'
import { navSection, useNavHint } from './ui/navHint'
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
      { to: 'functions', label: 'Functions', icon: 'functions', hint: 'Traced functions and their versions' },
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
      className="side-foot-btn"
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

/** Weft's mark: a dark rounded square with a W (DESIGN.md §7.2: no colour beyond the accent). */
export function Logo({ size = 26 }: { size?: number }) {
  return (
    <span className="weft-mark" aria-hidden="true" style={{ width: size, height: size, fontSize: Math.round(size * 0.54) }}>
      W
    </span>
  )
}

/**
 * The app shell (DESIGN.md §7.2 rule 2): a light grey sidebar with the
 * project switcher at the top, plain text items under section labels, and
 * the theme and keyboard shortcuts at the bottom. Under 900 px the sidebar
 * becomes a drawer behind a top bar.
 */
export function ProjectLayout() {
  const { entity, project } = useParams()
  const [open, setOpen] = useState(false)
  const loc = useLocation()
  useEffect(() => setOpen(false), [loc.pathname])
  // The highlighted item: the one the page's back link leads to when it names one, else the URL's.
  const base = `/${entity}/${project}`
  const items = NAV.flatMap((s) => s.items.map((it) => it.to))
  const hinted = navSection(useNavHint() ?? '', base)
  const current = hinted && items.includes(hinted) ? hinted : navSection(loc.pathname, base)
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
          <Logo size={22} /> Weft
        </Link>
        <span className="topbar-project">
          {project}
        </span>
      </header>
      {open && <div className="nav-scrim" onClick={() => setOpen(false)} aria-hidden />}
      <nav className="sidebar" id="sidebar" aria-label="Main">
        <Link to="/" className="project-switch" title="Switch project">
          <Logo />
          <span className="project-switch-text">
            <span className="project-name-side">{project}</span>
            <span className="project-entity">{entity}</span>
          </span>
          <span className="sr-only">: switch project</span>
        </Link>
        {NAV.map((s) => (
          <div key={s.section} className="nav-group" role="group" aria-labelledby={`nav-${s.section}`}>
            <div className="nav-section" id={`nav-${s.section}`}>
              {s.section}
            </div>
            {s.items.map((it) => (
              <Link
                key={it.to}
                to={`${base}/${it.to}`}
                className={`nav-link ${current === it.to ? 'active' : ''}`}
                aria-current={current === it.to ? 'page' : undefined}
                title={it.hint}
              >
                {it.label}
              </Link>
            ))}
          </div>
        ))}
        <div className="side-foot">
          <ThemeSwitch />
          <button type="button" className="side-foot-btn" onClick={() => window.dispatchEvent(new Event('weft:shortcuts'))}>
            <Icon name="keyboard" size={14} /> Keyboard shortcuts <kbd className="kbd">?</kbd>
          </button>
        </div>
        <UserMenu />
      </nav>
      <main className="main" id="main" tabIndex={-1}>
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
