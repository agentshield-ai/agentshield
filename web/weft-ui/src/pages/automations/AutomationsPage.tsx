// Automations (DESIGN.md §7, §7.1 rules 1 and 11, §7.2): one sentence says how
// many there are, how many are on and how the last day went ("2 automations;
// 1 is on. In the last 24 hours they fired 3 times; 1 delivery failed."). Each
// automation is a row in plain words ("When polite scores below 0.5, post
// to Slack") with its switch and a menu (Send a test, Show its runs, Edit,
// Delete) on the right; deleting can be undone for a few seconds. Below, the recent runs (?runs=<name>
// narrows them to one automation).
import { useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '../../api/client'
import { automationsApi, type AutomationVal } from '../../api/automations'
import type { WeftObject } from '../../api/types'
import { ErrorBox, Loading } from '../../components/common'
import { AutomationEditor } from '../../components/automations/AutomationEditor'
import { RunsTable } from '../../components/automations/RunsTable'
import { Switch } from '../../components/objects/bits'
import { usePublish } from '../../components/objects/ObjectForms'
import { errMsg, useObjects } from '../../components/objects/objectUtils'
import { SettingsRow, SettingsSection, useUndoDelete } from '../../components/settings/SettingsList'
import { Icon } from '../../components/ui/Icons'
import { Dropdown, MenuItem } from '../../components/traces/Menu'
import { PageHeader } from '../../components/ui/PageHeader'
import { Sentence } from '../../components/ui/Sentence'
import { notify } from '../../components/ui/Toasts'
import { useProjectId } from '../../hooks/useProject'
import { automationLine, automationsSentence, lastRunLine } from '../../lib/summaries/project'

const RUNS_LIMIT = 200

export function AutomationsPage() {
  const projectId = useProjectId()
  const qc = useQueryClient()
  const publish = usePublish(projectId)
  const autos = useObjects(projectId, { kinds: ['automation'] })
  const [editing, setEditing] = useState<WeftObject | 'new' | null>(null)
  const [params, setParams] = useSearchParams()
  const filter = params.get('runs') ?? ''
  const setFilter = (name: string) => {
    const n = new URLSearchParams(params)
    if (name) n.set('runs', name)
    else n.delete('runs')
    setParams(n, { replace: true })
  }
  const runs = useQuery({
    queryKey: ['automation-runs', projectId],
    queryFn: () => automationsApi.runs(projectId, undefined, RUNS_LIMIT).then((r) => r.runs),
    refetchInterval: 10_000,
  })
  const refreshRuns = () => qc.invalidateQueries({ queryKey: ['automation-runs', projectId] })
  const test = useMutation({
    mutationFn: (id: string) => automationsApi.test(projectId, { automation_id: id }),
    onSettled: refreshRuns,
    onSuccess: (r) =>
      notify(
        `Test of ${r.run.automation_id}: ${r.run.status === 'success' ? 'delivered' : 'failed'}${r.run.response_status ? ` (HTTP ${r.run.response_status})` : ''}${r.run.error ? `: ${r.run.error}` : ''}.`,
        r.run.status === 'success' ? 'ok' : 'error',
        7000,
      ),
  })
  const check = useMutation({
    mutationFn: () => automationsApi.check(projectId),
    onSettled: refreshRuns,
    onSuccess: (r) =>
      notify(r.runs.length ? `${r.runs.length} error-rate ${r.runs.length === 1 ? 'automation' : 'automations'} fired.` : 'No error-rate automation is over its threshold (or all are cooling down).', 'info', 6000),
  })
  const [toggling, setToggling] = useState<string | null>(null)
  const { hidden, remove } = useUndoDelete()

  const list = (autos.data ?? []).filter((o) => !hidden.has(o.name))
  const lastRun = (id: string) => runs.data?.find((r) => r.automation_id === id && !r.test)
  const shownRuns = (runs.data ?? []).filter((r) => !filter || r.automation_id === filter)
  const sentence =
    autos.data && runs.data
      ? automationsSentence({
          automations: list.map((o) => ({ name: o.name, enabled: (o.value as AutomationVal | undefined)?.enabled !== false })),
          runs: runs.data,
          capped: runs.data.length >= RUNS_LIMIT,
        })
      : null
  const hasErrorRate = list.some((o) => (o.value as AutomationVal | undefined)?.trigger?.type === 'error_rate')

  return (
    <div className="page-frame">
      <PageHeader
        title="Automations"
        help="project.md#automations"
        controls={
          <>
            {hasErrorRate && (
              <button type="button" className="qbtn" disabled={check.isPending} onClick={() => check.mutate()} title="Evaluate the error-rate automations now">
                {check.isPending ? 'Checking…' : 'Check error rates now'}
              </button>
            )}
            <button type="button" className="qbtn primary" onClick={() => setEditing('new')}>
              <Icon name="plus" size={14} /> New automation
            </button>
          </>
        }
        sentence={sentence ? <Sentence parts={[sentence]} /> : undefined}
      />
      <ErrorBox error={autos.error ?? runs.error ?? test.error ?? check.error} />
      {autos.isLoading ? (
        <Loading />
      ) : !list.length ? (
        <div className="quiet-empty">
          <p>
            An automation sends a Slack message or calls a webhook when something happens: a score crosses a threshold, too many runs fail within an hour, or someone
            leaves feedback. For example, post to Slack when a monitor finds a hallucination.
          </p>
        </div>
      ) : (
        <SettingsSection>
          {list.map((o) => {
            const v = (o.value ?? {}) as AutomationVal
            const enabled = v.enabled !== false
            return (
              <SettingsRow
                key={o.name}
                label={o.name}
                muted={!enabled}
                detail={
                  <>
                    {v.description && (
                      <>
                        {v.description}
                        <br />
                      </>
                    )}
                    {automationLine(v)}
                    {lastRun(o.name) ? ` ${lastRunLine(lastRun(o.name))}` : ''}
                  </>
                }
                control={
                  <>
                    <Dropdown
                      align="right"
                      className="qbtn sm row-menu"
                      title={`Actions for ${o.name}`}
                      label={
                        <>
                          <span aria-hidden="true">{test.isPending && test.variables === o.name ? 'Sending…' : '•••'}</span>
                          <span className="sr-only">Actions for {o.name}</span>
                        </>
                      }
                    >
                      {(close) => (
                        <>
                          <MenuItem
                            disabled={test.isPending && test.variables === o.name}
                            onClick={() => {
                              close()
                              test.mutate(o.name)
                            }}
                          >
                            Send a test
                          </MenuItem>
                          <MenuItem
                            onClick={() => {
                              close()
                              setFilter(filter === o.name ? '' : o.name)
                            }}
                          >
                            {filter === o.name ? 'Show every automation’s runs' : 'Show its runs'}
                          </MenuItem>
                          <MenuItem
                            onClick={() => {
                              close()
                              setEditing(o)
                            }}
                          >
                            Edit
                          </MenuItem>
                          <div className="dd-sep" />
                          <MenuItem
                            danger
                            onClick={() => {
                              close()
                              remove(o.name, `Deleted automation ${o.name}`, async () => {
                                await api.objectDelete(projectId, o.name)
                                await qc.invalidateQueries({ queryKey: ['objects', projectId] })
                              })
                            }}
                          >
                            <Icon name="trash" size={14} /> Delete
                          </MenuItem>
                        </>
                      )}
                    </Dropdown>
                    <Switch
                      on={enabled}
                      disabled={toggling === o.name}
                      label={`${o.name} on`}
                      title={enabled ? 'On: turn off' : 'Off: turn on'}
                      onChange={async () => {
                        setToggling(o.name)
                        try {
                          await publish(o.name, { ...o.value, enabled: !enabled })
                        } catch (e) {
                          notify(`Could not update ${o.name}: ${errMsg(e)}`, 'error', 7000)
                        } finally {
                          setToggling(null)
                        }
                      }}
                    />
                  </>
                }
              />
            )
          })}
        </SettingsSection>
      )}
      <SettingsSection
        plain
        title={filter ? `Recent runs of ${filter}` : 'Recent runs'}
        action={
          filter ? (
            <button type="button" className="qbtn sm" onClick={() => setFilter('')}>
              Show every automation
            </button>
          ) : undefined
        }
      >
        {runs.isLoading ? <Loading /> : <RunsTable projectId={projectId} runs={shownRuns} showAutomation={!filter} />}
      </SettingsSection>
      {editing && (
        <AutomationEditor
          projectId={projectId}
          initial={editing === 'new' ? undefined : editing}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null)
            void qc.invalidateQueries({ queryKey: ['objects', projectId] })
            void refreshRuns()
          }}
        />
      )}
    </div>
  )
}
