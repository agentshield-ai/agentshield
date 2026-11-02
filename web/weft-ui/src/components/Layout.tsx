import { NavLink, Link, Outlet, useParams } from 'react-router-dom'

const NAV: { section: string; items: { to: string; label: string; icon: string }[] }[] = [
  {
    section: 'Observe',
    items: [
      { to: 'traces', label: 'Traces', icon: '≡' },
      { to: 'agents', label: 'Agents', icon: '◎' },
      { to: 'threads', label: 'Threads', icon: '⇄' },
      { to: 'dashboards', label: 'Dashboards', icon: '▥' },
      { to: 'monitors', label: 'Monitors', icon: '◉' },
      { to: 'queues', label: 'Annotation queues', icon: '☰' },
    ],
  },
  {
    section: 'Evaluate',
    items: [
      { to: 'evaluations', label: 'Evaluations', icon: '✓' },
      { to: 'leaderboards', label: 'Leaderboards', icon: '▤' },
      { to: 'playground', label: 'Playground', icon: '▷' },
    ],
  },
  {
    section: 'Assets',
    items: [
      { to: 'datasets', label: 'Datasets', icon: '▦' },
      { to: 'models', label: 'Models', icon: '◆' },
      { to: 'prompts', label: 'Prompts', icon: '¶' },
      { to: 'scorers', label: 'Scorers', icon: '★' },
      { to: 'ops', label: 'Ops', icon: 'ƒ' },
      { to: 'objects', label: 'All objects', icon: '◇' },
    ],
  },
  {
    section: 'Project',
    items: [
      { to: 'automations', label: 'Automations', icon: '↯' },
      { to: 'settings', label: 'Settings', icon: '⚙' },
    ],
  },
]

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
  return (
    <div className="app">
      <nav className="sidebar">
        <Link to="/" className="brand">
          <Logo /> Weft
        </Link>
        <Link to="/" className="project-switch" title="Switch project">
          <div className="faint small">{entity}</div>
          <div style={{ color: 'var(--text)', fontWeight: 500 }}>{project}</div>
        </Link>
        {NAV.map((s) => (
          <div key={s.section}>
            <div className="nav-section">{s.section}</div>
            {s.items.map((it) => (
              <NavLink key={it.to} to={`/${entity}/${project}/${it.to}`} className={({ isActive }) => `nav-link ${isActive ? 'active' : ''}`}>
                <span style={{ width: 16, textAlign: 'center' }}>{it.icon}</span>
                {it.label}
              </NavLink>
            ))}
          </div>
        ))}
      </nav>
      <main className="main">
        <Outlet />
      </main>
    </div>
  )
}

export function PlainLayout() {
  return (
    <div className="app">
      <main className="main">
        <Outlet />
      </main>
    </div>
  )
}
