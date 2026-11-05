// The start page: every project on this server (DESIGN.md §7.1 rule 1, §7.2).
// One sentence says how many there are and which was active last; a grouped
// list, most recently active first ("agents — 3 traces, 24 saved assets"),
// opens a project's Sessions. Below, opening (or creating) a project by name,
// and the snippets that send data to it.
import { useMemo, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { api } from '../../api/client'
import { fmtDate } from '../../lib/format'
import { relativeTime } from '../../lib/summaries'
import { projectLine, projectsSentence } from '../../lib/summaries/project'
import { sessionsUrl } from '../../lib/urls'
import { ErrorBox, Spinner } from '../../components/common'
import { Logo } from '../../components/Layout'
import { SettingsSection } from '../../components/settings/SettingsList'
import { Snippets } from '../../components/ui/EmptyState'
import { GroupedList, ListRow } from '../../components/ui/GroupedList'
import { Monogram } from '../../components/ui/Monogram'
import { PageHeader } from '../../components/ui/PageHeader'
import { SearchField } from '../../components/ui/SearchField'
import { Sentence, type SentencePart } from '../../components/ui/Sentence'

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
  const info = useQuery({ queryKey: ['server-info'], queryFn: api.server, staleTime: 60_000 })
  const [name, setName] = useState('')
  const [filter, setFilter] = useState('')
  const parsed = parseProjectInput(name)
  const all = useMemo(() => [...(projects.data?.projects ?? [])].sort((a, b) => (b.last_activity ?? '').localeCompare(a.last_activity ?? '')), [projects.data])
  const list = useMemo(() => {
    const f = filter.trim().toLowerCase()
    return f ? all.filter((p) => p.project_id.toLowerCase().includes(f)) : all
  }, [all, filter])
  const total = all.length
  const exampleProject = parsed ? `${parsed.entity}/${parsed.project}` : (list[0]?.project_id ?? 'my-team/my-project')
  const s = projects.data ? projectsSentence(all) : null
  const sentence: SentencePart[] | null = s ? [s.lead, ...(s.latest ? [{ text: s.latest, to: sessionsUrl(s.latest), title: 'Open its sessions' }] : []), s.tail] : null

  return (
    <div className="page-frame">
      <div className="brand-line">
        <Logo size={22} />
        <span>Weft{info.data?.version ? ` ${info.data.version}` : ''}</span>
      </div>
      <PageHeader
        title="Projects"
        help="setup.md"
        helpTopic="Projects"
        controls={total > 6 ? <SearchField label="Filter projects" value={filter} onChange={setFilter} placeholder="Filter projects" filterInput /> : undefined}
        sentence={sentence ? <Sentence parts={sentence} /> : undefined}
      />
      <ErrorBox error={projects.error} />
      {projects.isLoading ? (
        <Spinner label="Loading projects…" />
      ) : total > 0 && list.length === 0 ? (
        <p className="quiet-note">No projects match “{filter}”.</p>
      ) : total > 0 ? (
        <GroupedList label="Projects">
          {list.map((p) => (
            <ListRow
              key={p.project_id}
              glyph={<Monogram name={p.project} variant="user" size={28} />}
              title={p.project}
              summary={projectLine(p)}
              meta={`team ${p.entity}`}
              time={p.last_activity ? relativeTime(p.last_activity) : 'no activity yet'}
              timeTitle={p.last_activity ? `Last active ${fmtDate(p.last_activity)}` : undefined}
              to={sessionsUrl(p.project_id)}
            />
          ))}
        </GroupedList>
      ) : null}

      <SettingsSection
        plain
        title={total ? 'Open or start a project' : 'Start a project'}
        note={
          name && !parsed ? (
            <span style={{ color: 'var(--error)' }}>Use letters, digits, “.”, “_” or “-”, as team/project.</span>
          ) : parsed ? (
            <>
              Opens <span className="mono">{parsed.entity}/{parsed.project}</span>. It is stored once the first trace or asset is written to it; its page shows how to
              connect your agents, or sends a test session in one click.
            </>
          ) : (
            'A project is created when the first trace or asset is written to it. Type team/project, or just a project name for team “default”.'
          )
        }
      >
        <form
          className="projects-new"
          onSubmit={(e) => {
            e.preventDefault()
            if (parsed) navigate(sessionsUrl(`${parsed.entity}/${parsed.project}`))
          }}
        >
          <input className="input" placeholder="team/project" value={name} onChange={(e) => setName(e.target.value)} aria-label="New project name" />
          <button className="qbtn primary" type="submit" disabled={!parsed}>
            Open project
          </button>
        </form>
      </SettingsSection>

      <SettingsSection plain title="Send data">
        <div className="guided-empty" style={{ gap: 12, maxWidth: "none" }}>
          <Snippets projectId={exampleProject} />
        </div>
      </SettingsSection>
    </div>
  )
}
