// Monitors (DESIGN.md §7, §7.2): answers "what is being scored
// automatically, and is it still running?". One sentence (how many
// monitors, how many active, how many scores in the last 24 hours), then
// the monitors as a grouped list, each with a generated line saying what it
// scores and when it last did, a switch to pause it and a delete. The
// ready-made signals follow, collapsed once the project has monitors.
import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { useQueries, useQueryClient } from '@tanstack/react-query'
import { api } from '../../api/client'
import { parseRef } from '../../api/refs'
import { ErrorBox, Loading } from '../../components/common'
import { MonitorEditor, useToggleMonitor } from '../../components/evals/MonitorEditor'
import { objectRefPrefix } from '../../components/evals/evalUtils'
import { monitorNames, monitorScopes } from '../../components/evals/monitorFields'
import { SignalsGallery } from '../../components/monitors/SignalsGallery'
import { ConfirmButton, Switch } from '../../components/objects/bits'
import { refName, useObjects } from '../../components/objects/objectUtils'
import { GroupedList, ListRow } from '../../components/ui/GroupedList'
import { Icon } from '../../components/ui/Icons'
import { PageHeader } from '../../components/ui/PageHeader'
import { Sentence } from '../../components/ui/Sentence'
import { useProjectId } from '../../hooks/useProject'
import { fmtDate } from '../../lib/format'
import { contains, toQuery } from '../../lib/query'
import { relativeTime } from '../../lib/summaries'
import { monitorLine, monitorsSentence, type MonitorActivity, type MonitorWordsInput } from '../../lib/summaries/dashboards'
import { monitorUrl } from '../../lib/urls'
import type { Any } from '../../api/types'
import '../observe.css'

/** The most recent scores read per monitor for its line. */
const ACTIVITY_LIMIT = 500

/** A monitor's stored fields as the words module reads them. */
export function monitorWords(v: Any): MonitorWordsInput {
  return {
    scorers: (v?.scorers ?? []).map((s: string) => refName(s)),
    scopes: monitorScopes(v),
    names: monitorNames(v),
    rate: v?.sampling_rate ?? 1,
    active: !!v?.active,
  }
}

/** Last score and scores in the last 24 hours, from a monitor's newest scores (newest first). */
export function activityOf(items: { created_at: string }[], limit: number, now = Date.now()): MonitorActivity {
  const since = now - 86400e3
  const today = items.filter((f) => new Date(f.created_at).getTime() >= since).length
  return { lastAt: items[0]?.created_at ?? null, today, capped: items.length >= limit && today === items.length }
}

export function MonitorsPage() {
  const projectId = useProjectId()
  const nav = useNavigate()
  const qc = useQueryClient()
  const mons = useObjects(projectId, { kinds: ['monitor'] })
  const [creating, setCreating] = useState(false)
  const { toggle, pending } = useToggleMonitor(projectId)
  const list = mons.data ?? []
  const activity = useQueries({
    queries: list.map((o) => ({
      queryKey: ['monitor-activity', projectId, o.name],
      queryFn: () =>
        api
          .feedbackQuery({
            project_id: projectId,
            query: toQuery(contains('trigger_ref', objectRefPrefix(projectId, o.name), false)),
            sort_by: [{ field: 'created_at', direction: 'desc' }],
            limit: ACTIVITY_LIMIT,
          })
          .then((r) => activityOf(r.items.filter((f) => parseRef(f.trigger_ref ?? '')?.name === o.name), ACTIVITY_LIMIT)),
      staleTime: 30_000,
      refetchInterval: 60_000,
    })),
  })
  const loaded = activity.length > 0 && activity.every((a) => a.data)
  const active = list.filter((o) => o.value?.active).length

  return (
    <div className="page-frame obs-page">
      <PageHeader
        title="Monitors"
        help="evaluations.md#monitors"
        controls={
          <button type="button" className="qbtn primary" onClick={() => setCreating(true)}>
            New monitor
          </button>
        }
        sentence={
          mons.data ? (
            <Sentence
              parts={[
                monitorsSentence({
                  total: list.length,
                  active,
                  today: loaded ? activity.reduce((a, x) => a + (x.data?.today ?? 0), 0) : null,
                  capped: activity.some((x) => x.data?.capped),
                }),
              ]}
            />
          ) : (
            <p className="sentence s-outcome sk-sentence" aria-busy="true">
              <span className="sr-only">Counting monitors…</span>
              <span className="sk sk-line" aria-hidden />
            </p>
          )
        }
      />
      <ErrorBox error={mons.error} />
      {mons.isLoading ? (
        <Loading />
      ) : !list.length ? (
        <div className="quiet-empty">
          <p>A monitor scores your agents' work as it arrives: it samples finished sessions, agent runs, hand-offs or spans and scores them with LLM judges or rules.</p>
          <p className="quiet-note obs-wrap">Start from a ready-made signal below, or choose New monitor to pick the scorers yourself.</p>
        </div>
      ) : (
        <GroupedList label="Monitors">
          {list.map((o, i) => {
            const v = o.value ?? {}
            const a = activity[i]?.data ?? null
            return (
              <ListRow
                key={o.name}
                className="obs-monitor-row"
                title={o.name}
                summary={monitorLine(monitorWords(v), a, { counts: list.length > 1 })}
                meta={v.description || undefined}
                time={a?.lastAt ? relativeTime(a.lastAt) : undefined}
                timeTitle={a?.lastAt ? `Last scored ${fmtDate(a.lastAt)}` : undefined}
                to={monitorUrl(projectId, o.name)}
                trailing={
                  <span className="obs-row-trailing">
                    <Switch on={!!v.active} disabled={pending === o.name} onChange={() => void toggle(o)} title={v.active ? `Pause monitor ${o.name}` : `Activate monitor ${o.name}`} />
                    <ConfirmButton
                      className="btn ghost sm icon"
                      title={`Delete monitor ${o.name}?`}
                      body="Scores it already produced are kept as feedback."
                      onConfirm={async () => {
                        await api.objectDelete(projectId, o.name)
                        await qc.invalidateQueries({ queryKey: ['objects', projectId] })
                      }}
                    >
                      <Icon name="trash" size={15} />
                      <span className="sr-only">Delete monitor {o.name}</span>
                    </ConfirmButton>
                  </span>
                }
              />
            )
          })}
        </GroupedList>
      )}
      <SignalsGallery projectId={projectId} onCreated={(id) => nav(monitorUrl(projectId, id))} />
      {creating && (
        <MonitorEditor
          projectId={projectId}
          onClose={() => setCreating(false)}
          onSaved={(c) => {
            setCreating(false)
            nav(monitorUrl(projectId, c.name))
          }}
        />
      )}
    </div>
  )
}
