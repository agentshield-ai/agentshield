import { useMemo } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import type { Span } from '../../api/types'
import { StatusBadge } from '../common'
import { callCost, callLatencyMs, callStatus, callTokens, fmtCost, fmtDate, fmtDuration, fmtNumber, fmtRelative } from '../../lib/format'
import { spanUrl, objectUrl } from '../../lib/urls'
import { parseRef } from '../../api/refs'
import { evalRunInfo, flattenScores, fmtMetric, sortMetricKeys, useDatasetSizes } from './evalUtils'
import type { AgentEvalRun } from '../../api/agents'
import { EvalAgentLinks } from '../agents/AgentEvals'

function RefLink({ refStr, label }: { refStr: string | null; label: string }) {
  const p = refStr ? parseRef(refStr) : null
  if (!p) return <span>{label}</span>
  return (
    <Link to={objectUrl(p.projectId, p.name, p.digest ?? 'latest')} onClick={(e) => e.stopPropagation()} title={refStr ?? ''}>
      {label}
    </Link>
  )
}

export function EvalRunsTable({
  projectId,
  calls,
  selected,
  onSelect,
  hideEval,
  hideModel,
  maxMetrics = 12,
  agentRuns,
}: {
  projectId: string
  calls: Span[]
  selected?: string[]
  onSelect?: (ids: string[]) => void
  hideEval?: boolean
  hideModel?: boolean
  maxMetrics?: number
  /** Agent activity per run (Weft agent evals); adds an Agents column when any run has some. */
  agentRuns?: Map<string, AgentEvalRun>
}) {
  const nav = useNavigate()
  const infos = useMemo(() => calls.map((c) => ({ c, i: evalRunInfo(c), m: flattenScores(c.output) })), [calls])
  const metricKeys = useMemo(() => {
    const keys = new Set<string>()
    infos.forEach(({ m }) => Object.keys(m).forEach((k) => keys.add(k)))
    // true_count is redundant next to true_fraction
    const xs = [...keys].filter((k) => !(k.endsWith('.true_count') && keys.has(k.replace(/true_count$/, 'true_fraction'))))
    return sortMetricKeys(xs).slice(0, maxMetrics)
  }, [infos, maxMetrics])
  const sizes = useDatasetSizes(infos.map(({ i }) => i.evalRef).filter(Boolean) as string[])
  const sel = selected ?? []
  const showAgents = !!agentRuns && calls.some((c) => agentRuns.has(c.id))
  const toggle = (id: string) => onSelect?.(sel.includes(id) ? sel.filter((x) => x !== id) : [...sel, id])

  return (
    <div className="table-wrap">
      <table className="table">
        <thead>
          <tr>
            {onSelect && (
              <th style={{ width: 32 }}>
                <input
                  type="checkbox"
                  aria-label="Select all"
                  checked={calls.length > 0 && sel.length === calls.length}
                  onChange={(e) => onSelect(e.target.checked ? calls.map((c) => c.id) : [])}
                />
              </th>
            )}
            <th>Status</th>
            {!hideEval && <th>Evaluation</th>}
            {!hideModel && <th>Model</th>}
            <th>Started</th>
            <th className="num">Duration</th>
            <th className="num">Rows</th>
            {metricKeys.map((k) => {
              const i = k.indexOf('.')
              return (
                <th key={k} className="num" title={k}>
                  {i > 0 ? (
                    <>
                      <div className="faint small" style={{ fontWeight: 400 }}>
                        {k.slice(0, i)}
                      </div>
                      {k.slice(i + 1)}
                    </>
                  ) : (
                    k
                  )}
                </th>
              )
            })}
            <th className="num">Tokens</th>
            <th className="num">Cost</th>
            {showAgents && <th title="Agent spans and conversations recorded or linked by this run's predictions">Agents</th>}
          </tr>
        </thead>
        <tbody>
          {infos.map(({ c, i, m }) => (
            <tr key={c.id} className={`clickable ${sel.includes(c.id) ? 'selected' : ''}`} onClick={() => nav(spanUrl(projectId, c.id))}>
              {onSelect && (
                <td onClick={(e) => e.stopPropagation()}>
                  <input type="checkbox" checked={sel.includes(c.id)} onChange={() => toggle(c.id)} aria-label="Select run" />
                </td>
              )}
              <td>
                <StatusBadge status={callStatus(c)} />
              </td>
              {!hideEval && (
                <td style={{ whiteSpace: 'nowrap' }} title={c.display_name ?? undefined}>
                  <RefLink refStr={i.evalRef} label={i.evalName} />
                </td>
              )}
              {!hideModel && (
                <td style={{ whiteSpace: 'nowrap' }}>
                  <RefLink refStr={i.modelRef} label={i.modelName} />
                </td>
              )}
              <td title={fmtDate(c.started_at)} className="muted" style={{ whiteSpace: 'nowrap' }}>
                {fmtRelative(c.started_at)}
              </td>
              <td className="num">{fmtDuration(callLatencyMs(c))}</td>
              <td className="num muted">{i.evalRef ? fmtNumber(sizes.data?.[i.evalRef] ?? null) : '—'}</td>
              {metricKeys.map((k) => (
                <td key={k} className="num mono">
                  {k.endsWith('true_fraction') && typeof m[k] === 'number' ? `${((m[k] as number) * 100).toFixed(1)}%` : fmtMetric(m[k])}
                </td>
              ))}
              <td className="num muted">{fmtNumber(callTokens(c).total || null)}</td>
              <td className="num muted">{fmtCost(callCost(c))}</td>
              {showAgents && (
                <td onClick={(e) => e.stopPropagation()} style={{ whiteSpace: 'nowrap' }}>
                  {agentRuns?.get(c.id) ? (
                    <div className="col" style={{ gap: 2 }}>
                      <span className="small muted">
                        {fmtNumber(agentRuns.get(c.id)!.conversation_count)} conv · {fmtNumber(agentRuns.get(c.id)!.span_count)} spans
                      </span>
                      <EvalAgentLinks projectId={projectId} runId={c.id} compact />
                    </div>
                  ) : (
                    <span className="faint">—</span>
                  )}
                </td>
              )}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

/** Generic compact list of spans (for "recent spans of this function", "used in spans"). */
export function CallsMiniTable({ projectId, calls }: { projectId: string; calls: Span[] }) {
  const nav = useNavigate()
  return (
    <div className="table-wrap">
      <table className="table">
        <thead>
          <tr>
            <th>Status</th>
            <th>Span</th>
            <th>Started</th>
            <th className="num">Latency</th>
            <th>Inputs</th>
            <th>Output</th>
          </tr>
        </thead>
        <tbody>
          {calls.map((c) => (
            <tr key={c.id} className="clickable" onClick={() => nav(spanUrl(projectId, c.id))}>
              <td>
                <StatusBadge status={callStatus(c)} />
              </td>
              <td>
                <Link to={spanUrl(projectId, c.id)} onClick={(e) => e.stopPropagation()} className="mono">
                  {c.display_name || c.id.slice(-8)}
                </Link>
              </td>
              <td className="muted" title={fmtDate(c.started_at)}>
                {fmtRelative(c.started_at)}
              </td>
              <td className="num">{fmtDuration(callLatencyMs(c))}</td>
              <td>
                <span className="cell-preview mono small">{previewArgs(c.inputs)}</span>
              </td>
              <td>
                <span className="cell-preview mono small">{c.error ? <span style={{ color: 'var(--error)' }}>{c.error}</span> : previewArgs(c.output)}</span>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

function previewArgs(v: unknown): string {
  if (v == null) return ''
  if (typeof v === 'string') return v.slice(0, 200)
  if (typeof v === 'object' && !Array.isArray(v)) {
    return Object.entries(v as Record<string, unknown>)
      .filter(([k]) => k !== 'self')
      .map(([k, x]) => `${k}=${typeof x === 'string' ? (x.startsWith('weft:///') ? (parseRef(x)?.name ?? x) : JSON.stringify(x.slice(0, 60))) : JSON.stringify(x)?.slice(0, 60)}`)
      .join(', ')
  }
  return JSON.stringify(v).slice(0, 200)
}
