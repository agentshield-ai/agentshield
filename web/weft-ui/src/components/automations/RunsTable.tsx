import { Fragment, useState } from 'react'
import { Link } from 'react-router-dom'
import type { AutomationRun } from '../../api/automations'
import { fmtDate, fmtRelative } from '../../lib/format'
import { callUrl } from '../../lib/urls'
import { JsonBlock } from '../ValueView'

export function RunStatus({ run }: { run: AutomationRun }) {
  return <span className={`badge ${run.status === 'success' ? 'success' : 'error'}`}>{run.status === 'success' ? 'delivered' : 'failed'}</span>
}

function eventSummary(e: AutomationRun['event']): string {
  if (!e || typeof e !== 'object') return ''
  switch (e.type) {
    case 'score_threshold':
      return `${e.feedback_type ?? ''}${e.path ? '.' + e.path : ''} = ${JSON.stringify(e.value)}`
    case 'error_rate':
      return `${Math.round((e.error_rate ?? 0) * 1000) / 10}% errors (${e.error_calls}/${e.total_calls})`
    case 'feedback_created':
      return String(e.feedback_type ?? '')
  }
  return ''
}

/** Recent automation runs with expandable event and response details. */
export function RunsTable({ projectId, runs, showAutomation = true }: { projectId: string; runs: AutomationRun[]; showAutomation?: boolean }) {
  const [open, setOpen] = useState<string | null>(null)
  if (!runs.length) return <div className="empty small">No runs yet. Runs appear here when an automation fires or you send a test.</div>
  return (
    <div className="table-wrap">
      <table className="table">
        <thead>
          <tr>
            <th>When</th>
            {showAutomation && <th>Automation</th>}
            <th>Trigger</th>
            <th>Event</th>
            <th>Status</th>
            <th className="num">HTTP</th>
            <th className="num">ms</th>
          </tr>
        </thead>
        <tbody>
          {runs.map((r) => {
            const callId = r.event && typeof r.event === 'object' ? r.event.call_id : undefined
            return (
              <Fragment key={r.id}>
                <tr className="clickable" onClick={() => setOpen(open === r.id ? null : r.id)}>
                  <td className="muted" title={fmtDate(r.created_at)}>
                    {fmtRelative(r.created_at)}
                  </td>
                  {showAutomation && <td className="mono">{r.automation_id}</td>}
                  <td className="small">
                    {r.trigger_type}
                    {r.test && <span className="badge accent" style={{ marginLeft: 6 }}>test</span>}
                  </td>
                  <td className="small">
                    {eventSummary(r.event)}
                    {callId && callId !== 'test-call' && (
                      <>
                        {' '}
                        <Link to={callUrl(projectId, callId)} onClick={(e) => e.stopPropagation()}>
                          call
                        </Link>
                      </>
                    )}
                  </td>
                  <td>
                    <RunStatus run={r} />
                  </td>
                  <td className="num mono">{r.response_status ?? '—'}</td>
                  <td className="num mono">{r.duration_ms != null ? Math.round(r.duration_ms) : '—'}</td>
                </tr>
                {open === r.id && (
                  <tr>
                    <td colSpan={showAutomation ? 7 : 6}>
                      {r.error && <div className="error-box small" style={{ marginBottom: 8 }}>{r.error}</div>}
                      <div className="grid-2">
                        <div>
                          <div className="faint small">Event</div>
                          <JsonBlock value={r.event} />
                        </div>
                        <div>
                          <div className="faint small">Response body</div>
                          <JsonBlock value={r.response_body ?? ''} />
                        </div>
                      </div>
                    </td>
                  </tr>
                )}
              </Fragment>
            )
          })}
        </tbody>
      </table>
    </div>
  )
}
