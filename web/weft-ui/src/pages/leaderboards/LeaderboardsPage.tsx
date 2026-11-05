// Leaderboards (DESIGN.md §7, §7.2): one sentence saying which model leads,
// then each leaderboard with who leads it and on how many metrics.
// Deleting a leaderboard can be undone for a few seconds.
import { useMemo, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { useQueries, useQueryClient } from '@tanstack/react-query'
import { api } from '../../api/client'
import type { Any } from '../../api/types'
import { ErrorBox } from '../../components/common'
import { LeaderboardEditor } from '../../components/evals/LeaderboardEditor'
import { lbShapes } from '../../components/evals/sentence'
import { refName, useObjects } from '../../components/objects/objectUtils'
import { GroupedList, ListRow } from '../../components/ui/GroupedList'
import { PageHeader } from '../../components/ui/PageHeader'
import { SearchField } from '../../components/ui/SearchField'
import { Sentence } from '../../components/ui/Sentence'
import { Icon } from '../../components/ui/Icons'
import { undoable } from '../../components/ui/Toasts'
import { useProjectId } from '../../hooks/useProject'
import { fmtDate, fmtRelative, versionLabel } from '../../lib/format'
import { plural } from '../../lib/summaries'
import { leaderboardLine, leaderboardsSentence, leaderOf } from '../../lib/summaries/evaluate'
import { leaderboardUrl } from '../../lib/urls'
import '../evaluations/evaluate.css'

/** Leaderboards whose leader is worked out for the list (each is one compute request). */
const COMPUTE_CAP = 30

export function LeaderboardsPage() {
  const projectId = useProjectId()
  const nav = useNavigate()
  const qc = useQueryClient()
  const lbs = useObjects(projectId, { kinds: ['leaderboard'] })
  const [creating, setCreating] = useState(false)
  const [search, setSearch] = useState('')
  const [hidden, setHidden] = useState<Set<string>>(new Set())
  const items = useMemo(() => (lbs.data ?? []).filter((o) => !hidden.has(o.name)), [lbs.data, hidden])
  const rows = items.filter((o) => !search || o.name.toLowerCase().includes(search.toLowerCase()) || String(o.value?.description ?? '').toLowerCase().includes(search.toLowerCase()))
  const computed = useQueries({
    queries: items.slice(0, COMPUTE_CAP).map((o) => ({
      queryKey: ['leaderboard', projectId, o.name],
      queryFn: () => api.leaderboardCompute(projectId, o.name),
      staleTime: 30_000,
      retry: false,
    })),
  })
  const shapes = new Map(items.slice(0, COMPUTE_CAP).map((o, i) => [o.name, computed[i]?.data ? lbShapes(computed[i].data!) : null]))
  const ready = computed.every((c) => !c.isLoading)
  const del = (name: string) =>
    undoable({
      message: `Deleted leaderboard “${name}”`,
      apply: () => setHidden((h) => new Set(h).add(name)),
      revert: () =>
        setHidden((h) => {
          const n = new Set(h)
          n.delete(name)
          return n
        }),
      commit: async () => {
        await api.objectDelete(projectId, name)
        await qc.invalidateQueries({ queryKey: ['objects', projectId] })
      },
    })
  const empty = !lbs.isLoading && !lbs.error && items.length === 0

  return (
    <div className="page-frame">
      <PageHeader
        title="Leaderboards"
        help="evaluations.md#leaderboards"
        controls={
          <>
            {!empty && <SearchField label="Search leaderboards" value={search} onChange={setSearch} filterInput />}
            <button type="button" className="qbtn primary" onClick={() => setCreating(true)}>
              New leaderboard
            </button>
          </>
        }
        sentence={
          empty ? undefined : lbs.isLoading || !ready ? (
            <p className="sentence s-outcome sk-sentence" aria-busy="true">
              <span className="sr-only">Ranking…</span>
              <span className="sk sk-line" aria-hidden />
            </p>
          ) : (
            <Sentence
              parts={[
                leaderboardsSentence(
                  items.slice(0, COMPUTE_CAP).map((o) => {
                    const sh = shapes.get(o.name)
                    return { name: o.name, leader: sh ? leaderOf(sh.columns, sh.rows) : null }
                  }),
                ) + (items.length > COMPUTE_CAP ? ` (Counted over the ${COMPUTE_CAP} newest.)` : ''),
              ]}
            />
          )
        }
      />
      <ErrorBox error={lbs.error} />
      {empty ? (
        <div className="quiet-empty">
          <p>
            <strong>No leaderboards yet.</strong> A leaderboard picks metrics from one or more evaluations and ranks every model evaluated on them, using each model’s latest
            finished run.
          </p>
          <p className="ev-note">Create one here, or save one from a comparison of evaluation runs (Rank these runs, then Save as leaderboard).</p>
        </div>
      ) : (
        !lbs.isLoading && (
          <GroupedList title={search ? 'Matching leaderboards' : 'Leaderboards'} count={rows.length} label="Leaderboards">
            {rows.map((o) => {
              const cols: Any[] = Array.isArray(o.value?.columns) ? o.value.columns : []
              const evals = [...new Set(cols.map((c) => refName(c.evaluation_object_ref)))]
              const sh = shapes.get(o.name)
              const line = sh ? leaderboardLine(sh.columns, sh.rows) : `${plural(cols.length, 'metric')}.`
              return (
                <ListRow
                  key={o.name}
                  title={o.name}
                  summary={line}
                  meta={[o.value?.description, evals.length ? `from ${evals.join(', ')}` : '', versionLabel(o.version)].filter(Boolean).join(' · ')}
                  time={fmtRelative(o.created_at)}
                  timeTitle={`Updated ${fmtDate(o.created_at)}`}
                  to={leaderboardUrl(projectId, o.name)}
                  trailing={
                    <button type="button" className="icon-btn row-action" aria-label={`Delete leaderboard ${o.name}`} title="Delete this leaderboard (can be undone; evaluation runs are kept)" onClick={() => del(o.name)}>
                      <Icon name="trash" size={14} />
                    </button>
                  }
                />
              )
            })}
          </GroupedList>
        )
      )}
      {creating && (
        <LeaderboardEditor
          projectId={projectId}
          onClose={() => setCreating(false)}
          onSaved={(c) => {
            setCreating(false)
            nav(leaderboardUrl(projectId, c.name))
          }}
        />
      )}
    </div>
  )
}
