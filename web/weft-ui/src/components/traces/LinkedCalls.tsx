import { Link } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { api } from '../../api/client'
import { conversationUrl } from '../../api/agents'
import { spanUrl } from '../../lib/urls'
import { hasLinks, linksOf } from './linkedCalls'
import './traceViews.css'

/** Linked calls, conversations and the evaluation a call belongs to; nothing when there are none. */
export function LinkedCalls({ projectId, attributes, compact }: { projectId: string; attributes: unknown; compact?: boolean }) {
  const l = linksOf(attributes)
  if (!hasLinks(l)) return null
  return (
    <div className={`linked-calls ${compact ? 'small' : ''}`} aria-label="Linked calls">
      {l.calls.length > 0 && (
        <div className="row wrap" style={{ gap: 6 }}>
          <span className="muted small">Linked {l.calls.length === 1 ? 'call' : 'calls'}</span>
          {l.calls.map((c) => (
            <Link key={c.callId} to={spanUrl(projectId, c.callId)} className="chip mono" title={c.traceId ? `trace ${c.traceId}` : undefined}>
              {c.callId.slice(0, 12)} ↗
            </Link>
          ))}
        </div>
      )}
      {l.conversations.length > 0 && (
        <div className="row wrap" style={{ gap: 6 }}>
          <span className="muted small">Conversations</span>
          {l.conversations.map((id) => (
            <Link key={id} to={conversationUrl(projectId, id)} className="chip mono">
              {id}
            </Link>
          ))}
        </div>
      )}
      {l.evalLink && (
        <div className="row wrap" style={{ gap: 6 }}>
          <span className="muted small">Recorded during an evaluation</span>
          {l.evalLink.predictAndScoreCallId && (
            <Link to={spanUrl(projectId, l.evalLink.predictAndScoreCallId)} className="chip">
              predict_and_score ↗
            </Link>
          )}
          {l.evalLink.runId && (
            <Link to={spanUrl(projectId, l.evalLink.runId)} className="chip">
              evaluation run ↗
            </Link>
          )}
          {Object.entries(l.evalLink.extra).map(([k, v]) => (
            <span key={k} className="chip faint mono" title={`weft.eval.${k}`}>
              {k}: {v}
            </span>
          ))}
        </div>
      )}
    </div>
  )
}

/** LinkedCalls for a call known only by id (reads its attributes). */
export function LinkedCallsFor({ projectId, callId }: { projectId: string; callId: string }) {
  const q = useQuery({ queryKey: ['span', projectId, callId], queryFn: () => api.spanFind(projectId, callId), staleTime: 60_000 })
  if (!q.data) return null
  return <LinkedCalls projectId={projectId} attributes={q.data.attributes} compact />
}
