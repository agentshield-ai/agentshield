import { useMemo, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { api } from '../../api/client'
import { fmtDate, fmtNumber, fmtRelative } from '../../lib/format'
import { sessionsUrl } from '../../lib/urls'
import { ErrorBox, Spinner } from '../../components/common'
import { Logo } from '../../components/Layout'
import { GettingStarted } from '../../components/traces/GettingStarted'
import { usePageHelp } from '../../components/ui/PageHelp'

const NAME_RE = /^[A-Za-z0-9][A-Za-z0-9._-]*$/

function parseProjectInput(s: string): { entity: string; project: string } | null {
  const v = s.trim().replace(/^\/+|\/+$/g, '')
  if (!v) return null
  const parts = v.split('/')
  if (parts.length > 2) return null
  const [entity, project] = parts.length === 2 ? parts : ['default', parts[0]]
  if (!NAME_RE.test(entity) || !NAME_RE.test(project)) return null
  return { entity, project }
}

export function ProjectsPage() {
  const navigate = useNavigate()
  const projects = useQuery({ queryKey: ['projects'], queryFn: api.projects })
  const info = useQuery({ queryKey: ['server-info'], queryFn: api.serverInfo, staleTime: 60_000 })
  const [name, setName] = useState('')
  const [filter, setFilter] = useState('')
  const parsed = parseProjectInput(name)
  const list = useMemo(() => {
    const ps = [...(projects.data?.projects ?? [])].sort((a, b) => (b.last_activity ?? '').localeCompare(a.last_activity ?? ''))
    const f = filter.trim().toLowerCase()
    return f ? ps.filter((p) => p.project_id.toLowerCase().includes(f)) : ps
  }, [projects.data, filter])
  const total = projects.data?.projects.length ?? 0
  const exampleProject = list[0]?.project_id ?? 'my-team/my-project'
  const help = usePageHelp(
    'projects',
    'Projects',
    <>
      A <strong>project</strong> holds everything one team or app sends to Weft: its sessions, agents, traces, security verdicts and evaluations. Open a project to see its sessions,
      or name a new one below; it is created the first time something is written to it. The setup guide shows how to connect your agents.
    </>,
    'setup.md',
  )

  return (
    <div className="page projects-page">
      <div className="page-header">
        <Logo />
        <h1>Weft</h1>
        <span className="faint small">{info.data?.version ? `v${info.data.version}` : ''}</span>
        <span className="spacer" />
        {help.button}
      </div>
      {help.panel}

      <div className="projects-layout">
        <div className="col" style={{ gap: 12, minWidth: 0 }}>
          <div className="row wrap">
            <h2 className="grow">Projects</h2>
            {total > 6 && <input className="input" placeholder="Filter projects…" value={filter} onChange={(e) => setFilter(e.target.value)} aria-label="Filter projects" />}
          </div>
          <ErrorBox error={projects.error} />
          {projects.isLoading ? (
            <div className="empty">
              <Spinner label="Loading projects…" />
            </div>
          ) : total === 0 ? (
            <div className="card">
              <div className="card-body empty" style={{ padding: 28 }}>
                <b>No projects yet.</b>
                <div className="small" style={{ marginTop: 6 }}>
                  Open one below: its page shows how to connect your agents, or sends a test session in one click. Projects are created on first write.
                </div>
              </div>
            </div>
          ) : list.length === 0 ? (
            <div className="empty small">No projects match “{filter}”.</div>
          ) : (
            <div className="project-grid">
              {list.map((p) => (
                <Link key={p.project_id} to={sessionsUrl(p.project_id)} className="card project-card">
                  <div className="faint small">{p.entity}</div>
                  <div className="project-name">{p.project}</div>
                  <div className="project-stats">
                    <span>
                      <b>{fmtNumber(p.trace_count)}</b> traces
                    </span>
                    <span>
                      <b>{fmtNumber(p.call_count)}</b> spans
                    </span>
                    <span>
                      <b>{fmtNumber(p.object_count)}</b> objects
                    </span>
                  </div>
                  <div className="faint small" title={fmtDate(p.last_activity)}>
                    {p.last_activity ? `Active ${fmtRelative(p.last_activity)}` : 'No activity yet'}
                  </div>
                </Link>
              ))}
            </div>
          )}

          <form
            className="card"
            onSubmit={(e) => {
              e.preventDefault()
              if (parsed) navigate(sessionsUrl(`${parsed.entity}/${parsed.project}`))
            }}
          >
            <div className="card-header">
              <h3>New project</h3>
            </div>
            <div className="card-body col">
              <div className="row wrap">
                <input
                  className="input grow"
                  style={{ minWidth: 200 }}
                  placeholder="entity/project  (or just project)"
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  aria-label="New project name"
                />
                <button className="btn primary" type="submit" disabled={!parsed}>
                  Open project
                </button>
              </div>
              {name && !parsed && <span className="small" style={{ color: 'var(--error)' }}>Use letters, digits, “.”, “_” or “-”, as entity/project.</span>}
              {parsed && (
                <span className="small faint">
                  Opens <span className="mono">{parsed.entity}/{parsed.project}</span>. It is stored once the first span or object is written; its page shows how to connect your agents.
                </span>
              )}
            </div>
          </form>
        </div>

        <div className="card getting-started">
          <div className="card-header">
            <h3>Send data</h3>
          </div>
          <div className="card-body">
            <GettingStarted projectId={parsed ? `${parsed.entity}/${parsed.project}` : exampleProject} />
          </div>
        </div>
      </div>
    </div>
  )
}
