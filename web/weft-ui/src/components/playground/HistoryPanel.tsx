import { useState } from 'react'
import { Link } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { playgroundApi } from '../../api/playground'
import { fmtDate, fmtDuration, fmtNumber, fmtRelative, truncate } from '../../lib/format'
import { callUrl } from '../../lib/urls'
import { ErrorBox, Spinner } from '../common'
import { callToRun, runPrompt, type PlaygroundRun } from './history'
import './playground.css'

/**
 * Past playground runs: this browser's (localStorage) and the server's
 * tracked weft.completions.create / weft.images.generate calls. A run
 * re-opens into the first panel or a new one.
 */
export function HistoryPanel({
  projectId,
  local,
  onOpen,
  onClear,
  onClose,
  canAddPanel,
}: {
  projectId: string
  local: PlaygroundRun[]
  onOpen: (run: PlaygroundRun, target: 'replace' | 'new') => void
  onClear: () => void
  onClose: () => void
  canAddPanel: boolean
}) {
  const [tab, setTab] = useState<'local' | 'server'>('local')
  const [q, setQ] = useState('')
  const server = useQuery({
    queryKey: ['playground-history', projectId],
    queryFn: () => playgroundApi.history(projectId, 50),
    enabled: tab === 'server',
  })
  const runs = (tab === 'local' ? local : (server.data?.calls ?? []).map(callToRun).filter((r): r is PlaygroundRun => !!r)).filter(
    (r) => !q.trim() || `${r.model} ${runPrompt(r)} ${r.output}`.toLowerCase().includes(q.trim().toLowerCase()),
  )
  return (
    <aside className="pg-history" aria-label="Playground history">
      <div className="row pg-history-head">
        <b className="grow">History</b>
        <button className="btn ghost sm icon" aria-label="Close history" onClick={onClose}>
          ✕
        </button>
      </div>
      <div className="seg" role="group" aria-label="History source" style={{ alignSelf: 'flex-start' }}>
        <button className={tab === 'local' ? 'on' : ''} aria-pressed={tab === 'local'} onClick={() => setTab('local')} title="Runs made in this browser">
          This browser ({local.length})
        </button>
        <button className={tab === 'server' ? 'on' : ''} aria-pressed={tab === 'server'} onClick={() => setTab('server')} title="Tracked playground calls on the server">
          Tracked calls
        </button>
      </div>
      <input className="input sm" placeholder="Filter by model or text" value={q} onChange={(e) => setQ(e.target.value)} aria-label="Filter history" />
      {tab === 'server' && server.isLoading && <Spinner label="Loading calls…" />}
      <ErrorBox error={tab === 'server' ? server.error : null} />
      <div className="pg-history-list">
        {runs.length === 0 && !(tab === 'server' && server.isLoading) && (
          <div className="small muted" style={{ padding: 8 }}>
            {tab === 'local' ? 'Runs you make here are kept in this browser.' : 'No tracked playground calls yet.'}
          </div>
        )}
        {runs.map((r) => (
          <div key={r.id} className="pg-history-item">
            <div className="row small" style={{ gap: 6 }}>
              <span className={`badge ${r.mode === 'image' ? 'accent' : ''}`}>{r.mode}</span>
              <span className="mono truncate grow" title={r.model}>
                {r.model}
              </span>
              <span className="faint" title={fmtDate(r.at)}>
                {fmtRelative(r.at)}
              </span>
            </div>
            <div className="small truncate" title={runPrompt(r)}>
              {truncate(runPrompt(r), 140) || <span className="faint">(no prompt)</span>}
            </div>
            <div className={`small truncate ${r.error ? '' : 'muted'}`} style={r.error ? { color: 'var(--error)' } : undefined} title={r.error ?? r.output}>
              {r.error ? truncate(r.error, 140) : `→ ${truncate(r.output, 140) || '—'}`}
            </div>
            <div className="row small faint" style={{ gap: 8 }}>
              {r.latencyMs != null && <span>{fmtDuration(r.latencyMs)}</span>}
              {r.tokens != null && <span>{fmtNumber(r.tokens)} tok</span>}
              {r.callId && (
                <Link to={callUrl(projectId, r.callId)} title="Open the tracked call">
                  call ↗
                </Link>
              )}
              <span className="grow" />
              <button className="btn ghost sm" onClick={() => onOpen(r, 'replace')}>
                Open
              </button>
              {r.mode === 'chat' && (
                <button className="btn ghost sm" disabled={!canAddPanel} title={canAddPanel ? 'Open next to the current panels' : 'Up to 3 panels'} onClick={() => onOpen(r, 'new')}>
                  + Panel
                </button>
              )}
            </div>
          </div>
        ))}
      </div>
      {tab === 'local' && local.length > 0 && (
        <button className="btn ghost sm" onClick={onClear}>
          Clear browser history
        </button>
      )}
    </aside>
  )
}
