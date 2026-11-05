// The delegation graph: agents as nodes sized by runs, hand-offs as edges
// with counts, failures and median time; a ring marks an agent whose runs
// failed or that AgentShield blocked. Under 560 px the graph is unreadable,
// so the same figures show as tables instead (or, when the page already
// lists them in its own tables, a note pointing at them). Each node link is
// labelled with its figures, and a screen reader gets the hand-offs as a
// list, unless the page shows its own tables.
import { useLayoutEffect, useMemo, useRef, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import type { Graph } from '../../api/v1'
import { Link } from 'react-router-dom'
import { edgeTone, failedRuns, layoutGraph, nodeTone } from '../../lib/agentGraph'
import { fmtCost, fmtDuration, fmtNumber } from '../../lib/format'
import { monogramLetter } from './Monogram'
import { PlainTable } from './PlainTable'

/** Below this width the graph shows as tables. */
const GRAPH_MIN = 560

export function AgentGraphView({
  graph,
  agentUrl,
  title = 'Delegation graph',
  tablesOnPage = false,
}: {
  graph: Graph
  agentUrl?: (a: string) => string
  title?: string
  /** The page lists the same agents and hand-offs in its own tables (no second copy here). */
  tablesOnPage?: boolean
}) {
  const layout = useMemo(() => layoutGraph(graph), [graph])
  const [hover, setHover] = useState<string | null>(null)
  const navigate = useNavigate()
  const box = useRef<HTMLDivElement>(null)
  const [narrow, setNarrow] = useState(false)
  useLayoutEffect(() => {
    const el = box.current
    if (!el || typeof ResizeObserver === 'undefined') return
    const check = () => setNarrow(el.clientWidth < GRAPH_MIN)
    const ro = new ResizeObserver(check)
    ro.observe(el)
    check()
    return () => ro.disconnect()
  }, [])
  if (!graph.nodes.length) return <p className="muted">No agents recorded yet.</p>
  const forward = graph.edges.filter((e) => e.kind !== 'return')
  if (narrow)
    return (
      <div className="agent-graph" ref={box}>
        {tablesOnPage ? <p className="quiet-note">The graph needs a wider screen; the tables below have the same figures.</p> : <GraphTables graph={graph} agentUrl={agentUrl} />}
      </div>
    )
  return (
    <div className="agent-graph" ref={box}>
      <ul className="legend" aria-label="Legend">
        <li>
          <span className="lg-line ok" aria-hidden /> hand-off
        </li>
        <li>
          <span className="lg-line error" aria-hidden /> hand-off that failed
        </li>
        <li>
          <span className="lg-line muted dashed" aria-hidden /> return
        </li>
        <li>
          <span className="lg-ring error" aria-hidden /> runs failed or a tool call was blocked
        </li>
      </ul>
      <svg
        className="graph-svg"
        viewBox={`0 0 ${layout.width} ${layout.height}`}
        role={agentUrl ? 'group' : 'img'}
        aria-label={`${title}: ${graph.nodes.length} agents, ${forward.length} hand-off routes.${tablesOnPage ? ' The tables below list the same figures.' : ''}`}
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
          const failed = failedRuns(n)
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
                {failed ? ` · ${failed} failed` : ''}
                {n.detections?.block ? ` · ${n.detections.block} blocked` : ''}
              </text>
            </g>
          )
          return agentUrl ? (
            <a
              key={n.agent}
              href={agentUrl(n.agent)}
              className="node-link"
              aria-label={`${n.agent}: ${n.calls} run${n.calls === 1 ? '' : 's'}${failed ? `, ${failed} failed` : ''}`}
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
      {!tablesOnPage && (
        <ul className="sr-only" aria-label="Hand-offs">
          {forward.map((e) => (
            <li key={`${e.from}>${e.to}:${e.kind}`}>
              {e.from} to {e.to}: {e.count === 1 ? 'one hand-off' : `${e.count} hand-offs`}
              {e.errors ? `, ${e.errors} failed` : ''}
              {e.p50_ms != null ? `, median ${fmtDuration(e.p50_ms)}` : ''}
            </li>
          ))}
        </ul>
      )}
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
  const shield = graph.nodes.some((n) => (n.detections?.block ?? 0) + (n.detections?.require_approval ?? 0) + (n.detections?.log ?? 0) > 0)
  return (
    <div className="col" style={{ gap: 16 }}>
      <PlainTable caption="Agents">
        <thead>
          <tr>
            <th scope="col">Agent</th>
            <th scope="col" className="num">
              Runs
            </th>
            <th scope="col" className="num" title="Runs that failed">
              Failed
            </th>
            <th scope="col" className="num">
              Cost
            </th>
            {shield && <th scope="col">AgentShield</th>}
          </tr>
        </thead>
        <tbody>
          {graph.nodes.map((n) => (
            <tr key={n.agent}>
              <td>
                <AgentName name={n.agent} agentUrl={agentUrl} />
              </td>
              <td className="num">{fmtNumber(n.calls)}</td>
              <td className="num">{fmtNumber(failedRuns(n))}</td>
              <td className="num">{fmtCost(n.cost_usd)}</td>
              {shield && <td>{verdictWords(n.detections)}</td>}
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
  const scored = rows.some((e) => e.mean_score != null)
  return (
    <PlainTable caption={`${caption}, most failing first`} className="handoff-table">
      <thead>
        <tr>
          <th scope="col">Hand-off</th>
          <th scope="col" className="num" title="How many times work was handed over">
            Times
          </th>
          <th scope="col" className="num" title="Hand-offs whose receiving agent ended with an error">
            Failed
          </th>
          <th scope="col" className="num" title="Median time from hand-off to the receiving agent finishing">
            Median time
          </th>
          {scored && (
            <th scope="col" className="num" title="Mean of hand-off scores (for example, hand-off quality)">
              Score
            </th>
          )}
        </tr>
      </thead>
      <tbody>
        {rows.map((e) => (
          <tr key={`${e.from}>${e.to}:${e.kind}`}>
            <td className="route-cell">
              <AgentName name={e.from} agentUrl={agentUrl} /> → <AgentName name={e.to} agentUrl={agentUrl} />
            </td>
            <td className="num">{fmtNumber(e.count)}</td>
            <td className="num">{fmtNumber(e.errors)}</td>
            <td className="num">{fmtDuration(e.p50_ms)}</td>
            {scored && <td className="num">{e.mean_score == null ? '—' : e.mean_score.toFixed(2)}</td>}
          </tr>
        ))}
      </tbody>
    </PlainTable>
  )
}
