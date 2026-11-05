// Recent automation runs as a plain table (DESIGN.md §7.2 rule 3): when, which
// automation, what happened (in words), and whether it was delivered (a
// glyph with words, never colour alone). A row opens to show the event and
// the response.
import { Fragment, useState } from 'react'
import { Link } from 'react-router-dom'
import type { AutomationRun } from '../../api/automations'
import { fmtDate } from '../../lib/format'
import { relativeTime } from '../../lib/summaries'
import { spanUrl } from '../../lib/urls'
import { JsonBlock } from '../ValueView'
import { PlainTable } from '../ui/PlainTable'
import { StatusGlyph } from '../ui/StatusGlyph'
import '../settings/settings.css'

/** Delivered or failed: a glyph and the word. */
export function RunStatus({ run }: { run: Pick<AutomationRun, 'status'> }) {
  const ok = run.status === 'success'
  return (
    <span className={`status-words ${ok ? 'tone-ok' : 'tone-error'}`}>
      <StatusGlyph kind={ok ? 'check' : 'failed'} size={14} decorative />
      {ok ? 'Delivered' : 'Failed'}
    </span>
  )
}

const TRIGGER_WORDS: Record<string, string> = { score_threshold: 'score', error_rate: 'error rate', feedback_created: 'feedback' }

function eventSummary(e: AutomationRun['event']): string {
  if (!e || typeof e !== 'object') return ''
  switch (e.type) {
    case 'score_threshold':
      return `${e.feedback_type ?? ''}${e.path ? '.' + e.path : ''} was ${JSON.stringify(e.value)}`
    case 'error_rate':
      return `${Math.round((e.error_rate ?? 0) * 1000) / 10}% failed (${e.error_spans} of ${e.total_spans})`
    case 'feedback_created':
      return `${e.feedback_type ?? 'feedback'} arrived`
  }
  return ''
}

/** Recent automation runs with expandable event and response details. */
export function RunsTable({ projectId, runs, showAutomation = true }: { projectId: string; runs: AutomationRun[]; showAutomation?: boolean }) {
  const [open, setOpen] = useState<string | null>(null)
  if (!runs.length) return <p className="quiet-note">No runs yet. A run appears here each time an automation fires or you send a test.</p>
  const cols = showAutomation ? 6 : 5
  return (
    <PlainTable caption="Recent automation runs">
      <thead>
        <tr>
          <th scope="col">When</th>
          {showAutomation && <th scope="col">Automation</th>}
          <th scope="col">What happened</th>
          <th scope="col">Result</th>
          <th scope="col" className="num">
            Took
          </th>
          <th scope="col">
            <span className="sr-only">Details</span>
          </th>
        </tr>
      </thead>
      <tbody>
        {runs.map((r) => {
          const spanId = r.event && typeof r.event === 'object' ? r.event.span_id : undefined
          const isOpen = open === r.id
          return (
            <Fragment key={r.id}>
              <tr>
                <td title={fmtDate(r.created_at)} style={{ whiteSpace: 'nowrap' }}>
                  {relativeTime(r.created_at)}
                </td>
                {showAutomation && <td>{r.automation_id}</td>}
                <td>
                  {r.test ? 'Test: ' : `${TRIGGER_WORDS[r.trigger_type] ?? r.trigger_type}: `}
                  {eventSummary(r.event)}
                  {spanId && spanId !== 'test-span' && (
                    <>
                      {' '}
                      <Link to={spanUrl(projectId, spanId)}>open the step</Link>
                    </>
                  )}
                </td>
                <td style={{ whiteSpace: 'nowrap' }}>
                  <RunStatus run={r} />
                  {r.response_status ? <span className="quiet-note" style={{ display: 'inline', marginLeft: 6 }}>HTTP {r.response_status}</span> : null}
                </td>
                <td className="num">{r.duration_ms != null ? `${Math.round(r.duration_ms)} ms` : '—'}</td>
                <td>
                  <button type="button" className="qbtn sm plain" aria-expanded={isOpen} onClick={() => setOpen(isOpen ? null : r.id)}>
                    {isOpen ? 'Hide' : 'Details'}
                  </button>
                </td>
              </tr>
              {isOpen && (
                <tr>
                  <td colSpan={cols}>
                    {r.error && (
                      <div className="error-box small" style={{ marginBottom: 8 }}>
                        {r.error}
                      </div>
                    )}
                    <div className="run-detail">
                      <div>
                        <div className="quiet-note">Event</div>
                        <JsonBlock value={r.event} />
                      </div>
                      <div>
                        <div className="quiet-note">Response</div>
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
    </PlainTable>
  )
}
