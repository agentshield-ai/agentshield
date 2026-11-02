import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '../../api/client'
import { automationsApi, type AutomationVal } from '../../api/automations'
import type { ObjSchema } from '../../api/types'
import { Empty, ErrorBox, Loading, Section } from '../../components/common'
import { AutomationEditor } from '../../components/automations/AutomationEditor'
import { describeAction, describeTrigger } from '../../components/automations/automationUtils'
import { RunStatus, RunsTable } from '../../components/automations/RunsTable'
import { ConfirmButton, Switch } from '../../components/objects/bits'
import { usePublish } from '../../components/objects/ObjectForms'
import { errMsg, useObjects } from '../../components/objects/objectUtils'
import { useProjectId } from '../../hooks/useProject'
import { fmtRelative } from '../../lib/format'

export function AutomationsPage() {
  const projectId = useProjectId()
  const qc = useQueryClient()
  const publish = usePublish(projectId)
  const autos = useObjects(projectId, { base_object_classes: ['Automation'] })
  const [editing, setEditing] = useState<ObjSchema | 'new' | null>(null)
  const [filter, setFilter] = useState<string>('')
  const runs = useQuery({ queryKey: ['automation-runs', projectId], queryFn: () => automationsApi.runs(projectId, undefined, 200).then((r) => r.runs), refetchInterval: 10_000 })
  const refreshRuns = () => qc.invalidateQueries({ queryKey: ['automation-runs', projectId] })
  const test = useMutation({ mutationFn: (id: string) => automationsApi.test(projectId, { automation_id: id }), onSettled: refreshRuns })
  const check = useMutation({ mutationFn: () => automationsApi.check(projectId), onSettled: refreshRuns })
  const [toggling, setToggling] = useState<string | null>(null)

  const lastRun = (id: string) => runs.data?.find((r) => r.automation_id === id && !r.test)
  const shownRuns = (runs.data ?? []).filter((r) => !filter || r.automation_id === filter)

  return (
    <div className="page">
      <div className="page-header">
        <div className="col" style={{ gap: 2 }}>
          <h1>Automations</h1>
          <span className="muted small">Send a Slack message or a webhook when a score crosses a threshold, the error rate climbs, or feedback arrives.</span>
        </div>
        <span className="spacer" />
        <button className="btn" disabled={check.isPending} onClick={() => check.mutate()} title="Evaluate error-rate triggers now">
          {check.isPending ? 'Checking…' : 'Check error rates now'}
        </button>
        <button className="btn primary" onClick={() => setEditing('new')}>
          + New automation
        </button>
      </div>
      {check.data && (
        <div className="info-box" style={{ marginBottom: 12 }}>
          {check.data.runs.length ? `${check.data.runs.length} error-rate automation(s) fired.` : 'No error-rate automation is over its threshold (or all are cooling down).'}
        </div>
      )}
      <ErrorBox error={autos.error ?? runs.error ?? test.error ?? check.error} />
      {autos.isLoading ? (
        <Loading />
      ) : !autos.data?.length ? (
        <div className="card">
          <Empty>
            <div className="col" style={{ alignItems: 'center', gap: 10 }}>
              <div style={{ maxWidth: 520 }}>
                No automations yet. For example: post to Slack when a monitor’s hallucination score is true, or call a webhook when more than 10% of agent runs fail in an hour.
              </div>
              <button className="btn primary" onClick={() => setEditing('new')}>
                + New automation
              </button>
            </div>
          </Empty>
        </div>
      ) : (
        <div className="table-wrap">
          <table className="table">
            <thead>
              <tr>
                <th style={{ width: 56 }}>On</th>
                <th>Name</th>
                <th>When</th>
                <th>Then</th>
                <th>Last fired</th>
                <th style={{ width: 200 }} />
              </tr>
            </thead>
            <tbody>
              {autos.data.map((o) => {
                const v = (o.val ?? {}) as AutomationVal
                const last = lastRun(o.object_id)
                const enabled = v.enabled !== false
                return (
                  <tr key={o.object_id}>
                    <td>
                      <Switch
                        on={enabled}
                        disabled={toggling === o.object_id}
                        title={enabled ? 'Disable' : 'Enable'}
                        onChange={async () => {
                          setToggling(o.object_id)
                          try {
                            await publish(o.object_id, { ...o.val, enabled: !enabled })
                          } catch (e) {
                            alert(`Could not update automation: ${errMsg(e)}`)
                          } finally {
                            setToggling(null)
                          }
                        }}
                      />
                    </td>
                    <td>
                      <span style={{ fontWeight: 500 }}>{o.object_id}</span>
                      {v.description && <div className="faint small cell-preview">{v.description}</div>}
                    </td>
                    <td className="small mono">{v.trigger ? describeTrigger(v.trigger) : '—'}</td>
                    <td className="small">{v.action ? describeAction(v.action) : '—'}</td>
                    <td className="small">
                      {last ? (
                        <span className="row" style={{ gap: 6 }}>
                          <RunStatus run={last} /> <span className="muted">{fmtRelative(last.created_at)}</span>
                        </span>
                      ) : (
                        <span className="faint">never</span>
                      )}
                    </td>
                    <td>
                      <div className="row" style={{ gap: 4, justifyContent: 'flex-end' }}>
                        <button className="btn sm" disabled={test.isPending && test.variables === o.object_id} onClick={() => test.mutate(o.object_id)}>
                          {test.isPending && test.variables === o.object_id ? 'Sending…' : 'Test'}
                        </button>
                        <button className="btn sm ghost" onClick={() => setFilter(filter === o.object_id ? '' : o.object_id)}>
                          {filter === o.object_id ? 'All runs' : 'Runs'}
                        </button>
                        <button className="btn sm ghost" onClick={() => setEditing(o)}>
                          Edit
                        </button>
                        <ConfirmButton
                          className="btn ghost sm icon danger"
                          title={`Delete automation ${o.object_id}?`}
                          body="Its run history is kept."
                          onConfirm={async () => {
                            await api.objDelete(projectId, o.object_id)
                            await qc.invalidateQueries({ queryKey: ['objs', projectId] })
                          }}
                        >
                          ✕
                        </ConfirmButton>
                      </div>
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      )}
      {test.data && (
        <div className={test.data.run.status === 'success' ? 'success-box' : 'warn-box'} style={{ marginTop: 12 }}>
          Test of {test.data.run.automation_id}: {test.data.run.status === 'success' ? 'delivered' : 'failed'}
          {test.data.run.response_status ? ` (HTTP ${test.data.run.response_status})` : ''}
          {test.data.run.error ? ` — ${test.data.run.error}` : ''}
        </div>
      )}
      <div style={{ marginTop: 18 }}>
        <Section
          title={filter ? `Recent runs of ${filter}` : 'Recent runs'}
          actions={
            filter ? (
              <button className="btn sm ghost" onClick={() => setFilter('')}>
                Show all
              </button>
            ) : undefined
          }
        >
          {runs.isLoading ? <Loading /> : <RunsTable projectId={projectId} runs={shownRuns} showAutomation={!filter} />}
        </Section>
      </div>
      {editing && (
        <AutomationEditor
          projectId={projectId}
          initial={editing === 'new' ? undefined : editing}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null)
            void qc.invalidateQueries({ queryKey: ['objs', projectId] })
            void refreshRuns()
          }}
        />
      )}
    </div>
  )
}
