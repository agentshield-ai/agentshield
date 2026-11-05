// The delegation graph: agents as nodes sized by invocations, hand-offs as
// edges with counts, errors and median latency; coloured by errors and
// detections. The same data is rendered as a table for screen readers and
// for exact numbers.
import { useMemo, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import type { Graph } from '../../api/v1'
import { Link } from 'react-router-dom'
import { edgeTone, layoutGraph, nodeTone } from '../../lib/agentGraph'
import { fmtCost, fmtDuration, fmtNumber } from '../../lib/format'
import { Segmented } from './Segmented'
import { monogramLetter } from './Monogram'
import { PlainTable } from './PlainTable'

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
        <Segmented
          label="Show as"
          size="sm"
          options={[
            { id: 'graph', label: 'Graph' },
            { id: 'table', label: 'Table' },
          ]}
          value={view}
          onChange={setView}
        />
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
              <marker key={t} id={`arrow-${t}`} viewBox="0 0 10 10" refX="8" refY="5" markerWidth="11" markerHeight="11" markerUnits="userSpaceOnUse" orient="auto-start-reverse">
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
            const inner = (
              <g
                className={`node ${tone} ${hover && hover !== n.agent ? 'dim' : ''}`}
                onMouseEnter={() => setHover(n.agent)}
                onMouseLeave={() => setHover(null)}
                onFocus={() => setHover(n.agent)}
                onBlur={() => setHover(null)}
              >
                <circle cx={n.x} cy={n.y} r={n.r} className="node-fill" />
                {tone !== 'ok' && <circle cx={n.x} cy={n.y} r={n.r + 4} className={`node-ring ${tone}`} />}
                <text x={n.x} y={n.y + 4} textAnchor="middle" className="node-initials" aria-hidden>
                  {monogramLetter(n.agent)}
                </text>
                <text x={n.x + n.r + 10} y={n.y - 2} className="node-label">
                  {n.agent}
                </text>
                <text x={n.x + n.r + 10} y={n.y + 14} className="node-sub">
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

/** A name that links to its agent page when a URL is given. */
function AgentName({ name, agentUrl }: { name: string; agentUrl?: (a: string) => string }) {
  return agentUrl ? <Link to={agentUrl(name)}>{name}</Link> : <span>{name}</span>
}

/** Verdicts as words: "1 blocked, 2 needed approval". */
function verdictWords(d: Graph['nodes'][number]['detections'] | undefined): string {
  const xs: string[] = []
  if (d?.block) xs.push(`${d.block} blocked`)
  if (d?.require_approval) xs.push(`${d.require_approval} needed approval`)
  if (d?.log) xs.push(`${d.log} logged`)
  return xs.join(', ') || 'none'
}

export function GraphTables({ graph, agentUrl }: { graph: Graph; agentUrl?: (a: string) => string }) {
  return (
    <div className="col" style={{ gap: 16 }}>
      <PlainTable caption="Agents">
        <thead>
          <tr>
            <th scope="col">Agent</th>
            <th scope="col" className="num">Runs</th>
            <th scope="col" className="num">Errors</th>
            <th scope="col" className="num">Tool calls</th>
            <th scope="col" className="num">Tokens</th>
            <th scope="col" className="num">Cost</th>
            <th scope="col">AgentShield</th>
          </tr>
        </thead>
        <tbody>
          {graph.nodes.map((n) => (
            <tr key={n.agent}>
              <td>
                <AgentName name={n.agent} agentUrl={agentUrl} />
              </td>
              <td className="num">{fmtNumber(n.calls)}</td>
              <td className={`num ${n.errors ? 'text-error' : ''}`}>{fmtNumber(n.errors)}</td>
              <td className="num">{fmtNumber(n.tool_calls ?? 0)}</td>
              <td className="num">{fmtNumber(n.tokens)}</td>
              <td className="num">{fmtCost(n.cost_usd)}</td>
              <td className={n.detections?.block ? 'text-error' : n.detections?.require_approval ? 'text-warn' : 'faint'}>{verdictWords(n.detections)}</td>
            </tr>
          ))}
        </tbody>
      </PlainTable>
      <HandoffTable edges={graph.edges} agentUrl={agentUrl} />
    </div>
  )
}

export function HandoffTable({ edges, agentUrl, caption = 'Hand-offs' }: { edges: Graph['edges']; agentUrl?: (a: string) => string; caption?: string }) {
  const rows = edges.filter((e) => e.kind !== 'return').sort((a, b) => b.error_rate - a.error_rate || b.count - a.count)
  if (!rows.length) return <p className="muted">No hand-offs between agents.</p>
  return (
    <PlainTable caption={`${caption}, most failing first`} className="handoff-table">
      <thead>
        <tr>
          <th scope="col">From</th>
          <th scope="col">To</th>
          <th scope="col" className="num" title="How many times control was handed over">
            Count
          </th>
          <th scope="col" className="num" title="Share of hand-offs whose receiving agent ended with an error">
            Error rate
          </th>
          <th scope="col" className="num" title="Median time from hand-off to the delegate finishing">
            Median time
          </th>
          <th scope="col" className="num" title="Mean of hand-off-level scores (e.g. Hand-off quality)">
            Mean score
          </th>
        </tr>
      </thead>
      <tbody>
        {rows.map((e) => (
          <tr key={`${e.from}>${e.to}:${e.kind}`}>
            <td>
              <AgentName name={e.from} agentUrl={agentUrl} />
            </td>
            <td>
              <AgentName name={e.to} agentUrl={agentUrl} /> <span className="faint small">{e.kind === 'delegate' ? 'delegated' : 'handed off'}</span>
            </td>
            <td className="num">{fmtNumber(e.count)}</td>
            <td className="num">
              <span className={e.errors ? 'text-error' : ''}>
                {(e.error_rate * 100).toFixed(0)}%{e.errors ? ` (${e.errors} failed)` : ''}
              </span>
            </td>
            <td className="num">{fmtDuration(e.p50_ms)}</td>
            <td className="num">{e.mean_score == null ? <span className="faint">not scored</span> : e.mean_score.toFixed(2)}</td>
          </tr>
        ))}
      </tbody>
    </PlainTable>
  )
}
