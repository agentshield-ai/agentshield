import { useMemo } from 'react'
import type { Span } from '../../api/types'
import { callLatencyMs, callStatus, fmtDuration } from '../../lib/format'
import { callLabel } from './callUtils'
import { opStyle } from './opStyle'
import { layoutOpGraph, nextCallFor, opPath, type GraphCall } from './graphLayout'
import './traceViews.css'

export function toGraphCalls(calls: Span[]): GraphCall[] {
  return calls.map((c) => ({
    id: c.id,
    parent_id: c.parent_id,
    op: callLabel(c),
    error: callStatus(c) === 'error',
    latency_ms: callLatencyMs(c),
    started_at: c.started_at,
    ...opStyle(c),
  }))
}

/** Op → op graph of a trace. Clicking a node selects its calls in turn. */
export function TraceGraph({ calls, selectedId, onSelect }: { calls: Span[]; selectedId: string; onSelect: (id: string) => void }) {
  const gcalls = useMemo(() => toGraphCalls(calls), [calls])
  const g = useMemo(() => layoutOpGraph(gcalls), [gcalls])
  const path = useMemo(() => opPath(gcalls, selectedId), [gcalls, selectedId])
  const onPath = new Set(path)
  const pathEdges = new Set(path.slice(1).map((op, i) => `${path[i]}\u0000${op}`))
  const selectedOp = path[path.length - 1]
  if (!calls.length) return <div className="empty small">No calls.</div>
  const clip = (s: string, w: number) => {
    const max = Math.floor((w - 24) / 7)
    return s.length > max ? s.slice(0, Math.max(1, max - 1)) + '…' : s
  }
  return (
    <div className="tgraph-wrap">
      <div className="tgraph-legend">
        <span>
          {g.nodes.length} op{g.nodes.length === 1 ? '' : 's'} · {calls.length} call{calls.length === 1 ? '' : 's'}
        </span>
        <span>Click a name to step through its spans</span>
      </div>
      <svg width={g.width} height={g.height} role="group" aria-label="Trace graph" style={{ display: 'block' }}>
        <defs>
          <marker id="tg-arrow" viewBox="0 0 8 8" refX="7" refY="4" markerWidth="7" markerHeight="7" orient="auto-start-reverse">
            <path d="M 0 0 L 8 4 L 0 8 z" fill="var(--border-strong)" />
          </marker>
          <marker id="tg-arrow-on" viewBox="0 0 8 8" refX="7" refY="4" markerWidth="7" markerHeight="7" orient="auto-start-reverse">
            <path d="M 0 0 L 8 4 L 0 8 z" fill="var(--accent)" />
          </marker>
        </defs>
        {g.edges.map((e) => {
          const on = pathEdges.has(`${e.from}\u0000${e.to}`)
          return (
            <g key={`${e.from}>${e.to}`}>
              <path d={e.path} className={`tgraph-edge ${on ? 'path' : ''}`} markerEnd={`url(#${on ? 'tg-arrow-on' : 'tg-arrow'})`} strokeDasharray={e.back ? '4 3' : undefined} />
              {e.count > 1 && (
                <text x={e.labelX} y={e.labelY} textAnchor="middle" className="tgraph-edge-label">
                  ×{e.count}
                </text>
              )}
            </g>
          )
        })}
        {g.nodes.map((n) => {
          const sel = n.id === selectedOp
          const err = n.errors > 0
          return (
            <g
              key={n.id}
              className={`tgraph-node ${sel ? 'selected' : onPath.has(n.id) ? 'path' : ''}`}
              transform={`translate(${n.x},${n.y})`}
              tabIndex={0}
              role="button"
              aria-pressed={sel}
              aria-label={`${n.label}: ${n.count} span${n.count === 1 ? '' : 's'}${err ? `, ${n.errors} failed` : ''}`}
              onClick={() => onSelect(nextCallFor(n, selectedId))}
              onKeyDown={(ev) => {
                if (ev.key === 'Enter' || ev.key === ' ') {
                  ev.preventDefault()
                  onSelect(nextCallFor(n, selectedId))
                }
              }}
            >
              <title>
                {n.label} — {n.count} call{n.count === 1 ? '' : 's'}, {fmtDuration(n.totalMs)} total{err ? `, ${n.errors} error${n.errors === 1 ? '' : 's'}` : ''}
              </title>
              <rect width={n.w} height={n.h} rx={7} style={err ? { stroke: 'var(--error)' } : undefined} />
              {n.color && <rect width={4} height={n.h - 12} x={3} y={6} rx={2} style={{ fill: n.color, stroke: 'none' }} />}
              {err && <circle cx={n.w - 10} cy={10} r={3.5} fill="var(--error)" />}
              <text x={10} y={18}>
                {clip(n.label, n.w)}
              </text>
              <text x={10} y={34} className="sub">
                {n.kind ? `${n.kind} · ` : ''}
                {n.count > 1 ? `×${n.count} · ` : ''}
                {fmtDuration(n.totalMs)}
                {sel && n.count > 1 ? ` · ${n.callIds.indexOf(selectedId) + 1}/${n.count}` : ''}
              </text>
            </g>
          )
        })}
      </svg>
    </div>
  )
}
