// The delegation graph: agents as nodes sized by invocations, hand-offs as
// edges with counts, errors and median latency; coloured by errors and
// detections. The same data is rendered as a table for screen readers and
// for exact numbers.
import { useMemo, useState, type CSSProperties } from 'react'
import { useNavigate } from 'react-router-dom'
import type { Graph } from '../../api/v1'
import { agentColor, agentInitials } from '../../lib/agentColor'
import { edgeTone, layoutGraph, nodeTone } from '../../lib/agentGraph'
import { fmtCost, fmtDuration, fmtNumber } from '../../lib/format'
import { AgentChip, DetectionBadges, ScorePill } from './bits'

export function AgentGraphView({ graph, agentUrl, title = 'Delegation graph' }: { graph: Graph; agentUrl?: (a: string) => string; title?: string }) {
  const layout = useMemo(() => layoutGraph(graph), [graph])
  const [hover, setHover] = useState<string | null>(null)
  const [view, setView] = useState<'graph' | 'table'>('graph')
  const navigate = useNavigate()
  if (!graph.nodes.length) return <p className="muted">No agents recorded yet.</p>
  const forward = graph.edges.filter((e) => e.kind !== 'return')
  return (
    <div className="agent-graph">
      <div className="row" style={{ justifyContent: 'space-between', flexWrap: 'wrap' }}>
        <ul className="legend" aria-label="Legend">
          <li>
            <span className="lg-line ok" aria-hidden /> hand-off
          </li>
          <li>
            <span className="lg-line error" aria-hidden /> hand-off with errors
          </li>
          <li>
            <span className="lg-line muted dashed" aria-hidden /> return
          </li>
          <li>
            <span className="lg-ring block" aria-hidden /> blocked tool call
          </li>
          <li>
            <span className="lg-ring error" aria-hidden /> errors
          </li>
        </ul>
        <div className="seg" role="group" aria-label="Show as">
          <button type="button" className={view === 'graph' ? 'on' : ''} aria-pressed={view === 'graph'} onClick={() => setView('graph')}>
            Graph
          </button>
          <button type="button" className={view === 'table' ? 'on' : ''} aria-pressed={view === 'table'} onClick={() => setView('table')}>
            Table
          </button>
        </div>
      </div>
      {view === 'graph' && (
        <svg
          className="graph-svg"
          viewBox={`0 0 ${layout.width} ${layout.height}`}
          role={agentUrl ? 'group' : 'img'}
          aria-label={`${title}: ${graph.nodes.length} agents, ${forward.length} hand-off routes. The table view lists the same data.`}
          style={{ maxWidth: Math.round(layout.width * 1.4) }}
        >
          <defs>
            {(['ok', 'error', 'muted'] as const).map((t) => (
              <marker key={t} id={`arrow-${t}`} viewBox="0 0 10 10" refX="8" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse">
                <path d="M0,0 L10,5 L0,10 z" className={`arrowhead ${t}`} />
              </marker>
            ))}
          </defs>
          {layout.edges.map((e) => {
            const tone = edgeTone(e)
            const dim = hover && hover !== e.from && hover !== e.to
            return (
              <g key={`${e.from}>${e.to}:${e.kind}`} className={`edge ${tone} ${dim ? 'dim' : ''}`}>
                <path d={e.path} strokeWidth={e.width} markerEnd={`url(#arrow-${tone})`} className={e.kind === 'return' ? 'dashed' : ''} />
                {e.kind !== 'return' && (
                  <text x={e.labelX} y={e.labelY} className="edge-label" textAnchor="middle">
                    {e.count}×{e.errors ? ` · ${e.errors} failed` : ''}
                    {e.p50_ms != null ? ` · ${fmtDuration(e.p50_ms)}` : ''}
                  </text>
                )}
              </g>
            )
          })}
          {layout.nodes.map((n) => {
            const tone = nodeTone(n)
            const style = { '--ac': agentColor(n.agent) } as CSSProperties
            const inner = (
              <g
                className={`node ${tone} ${hover && hover !== n.agent ? 'dim' : ''}`}
                style={style}
                onMouseEnter={() => setHover(n.agent)}
                onMouseLeave={() => setHover(null)}
                onFocus={() => setHover(n.agent)}
                onBlur={() => setHover(null)}
              >
                <circle cx={n.x} cy={n.y} r={n.r} className="node-fill" />
                {tone !== 'ok' && <circle cx={n.x} cy={n.y} r={n.r + 4} className={`node-ring ${tone}`} />}
                <text x={n.x} y={n.y + 4} textAnchor="middle" className="node-initials" aria-hidden>
                  {agentInitials(n.agent)}
                </text>
                <text x={n.x} y={n.y + n.r + 16} textAnchor="middle" className="node-label">
                  {n.agent}
                </text>
                <text x={n.x} y={n.y + n.r + 30} textAnchor="middle" className="node-sub">
                  {n.calls} run{n.calls === 1 ? '' : 's'}
                  {n.errors ? ` · ${n.errors} err` : ''}
                  {n.detections?.block ? ` · ${n.detections.block} blocked` : ''}
                </text>
              </g>
            )
            return agentUrl ? (
              <a
                key={n.agent}
                href={agentUrl(n.agent)}
                className="node-link"
                onClick={(e) => {
                  e.preventDefault()
                  navigate(agentUrl(n.agent))
                }}
              >
                {inner}
              </a>
            ) : (
              <g key={n.agent}>{inner}</g>
            )
          })}
        </svg>
      )}
      {view === 'table' && <GraphTables graph={graph} agentUrl={agentUrl} />}
    </div>
  )
}

export function GraphTables({ graph, agentUrl }: { graph: Graph; agentUrl?: (a: string) => string }) {
  return (
    <div className="col" style={{ gap: 12 }}>
      <div className="table-wrap">
        <table className="table">
          <caption className="sr-only">Agents</caption>
          <thead>
            <tr>
              <th scope="col">Agent</th>
              <th scope="col">Runs</th>
              <th scope="col">Errors</th>
              <th scope="col">Tool calls</th>
              <th scope="col">Tokens</th>
              <th scope="col">Cost</th>
              <th scope="col">Security</th>
            </tr>
          </thead>
          <tbody>
            {graph.nodes.map((n) => (
              <tr key={n.agent}>
                <td>
                  <AgentChip name={n.agent} to={agentUrl?.(n.agent)} />
                </td>
                <td>{fmtNumber(n.calls)}</td>
                <td>{fmtNumber(n.errors)}</td>
                <td>{fmtNumber(n.tool_calls ?? 0)}</td>
                <td>{fmtNumber(n.tokens)}</td>
                <td>{fmtCost(n.cost_usd)}</td>
                <td>{Object.values(n.detections ?? {}).some((v) => (v ?? 0) > 0) ? <DetectionBadges counts={n.detections} /> : <span className="faint">none</span>}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <HandoffTable edges={graph.edges} agentUrl={agentUrl} />
    </div>
  )
}

export function HandoffTable({ edges, agentUrl, caption = 'Hand-offs' }: { edges: Graph['edges']; agentUrl?: (a: string) => string; caption?: string }) {
  const rows = edges.filter((e) => e.kind !== 'return').sort((a, b) => b.error_rate - a.error_rate || b.count - a.count)
  if (!rows.length) return <p className="muted">No hand-offs between agents.</p>
  return (
    <div className="table-wrap">
      <table className="table handoff-table">
        <caption className="sr-only">{caption}, most failing first</caption>
        <thead>
          <tr>
            <th scope="col">From</th>
            <th scope="col">To</th>
            <th scope="col" title="How many times control was handed over">
              Count
            </th>
            <th scope="col" title="Share of hand-offs whose receiving agent ended with an error">
              Error rate
            </th>
            <th scope="col" title="Median time from hand-off to the delegate finishing">
              Median time
            </th>
            <th scope="col" title="Mean of hand-off-level scores (e.g. Hand-off quality)">
              Mean score
            </th>
          </tr>
        </thead>
        <tbody>
          {rows.map((e) => (
            <tr key={`${e.from}>${e.to}:${e.kind}`} className={e.errors ? 'row-error' : ''}>
              <td>
                <AgentChip name={e.from} to={agentUrl?.(e.from)} size="sm" />
              </td>
              <td>
                <AgentChip name={e.to} to={agentUrl?.(e.to)} size="sm" /> <span className="faint small">{e.kind === 'delegate' ? 'delegated' : 'handed off'}</span>
              </td>
              <td>{fmtNumber(e.count)}</td>
              <td>
                <span className={e.errors ? 'text-error' : ''}>
                  {(e.error_rate * 100).toFixed(0)}%{e.errors ? ` (${e.errors} failed)` : ''}
                </span>
              </td>
              <td>{fmtDuration(e.p50_ms)}</td>
              <td>{e.mean_score == null ? <span className="faint">not scored</span> : <ScorePill value={e.mean_score} />}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}
