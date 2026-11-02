import { useMemo, useState } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { api } from '../../api/client'
import { opDisplayName } from '../../api/refs'
import type { Any, Call } from '../../api/types'
import { useProjectId } from '../../hooks/useProject'
import { callCost, callLatencyMs, callStatus, callTokens, fmtCost, fmtDate, fmtDuration, fmtNumber } from '../../lib/format'
import { callUrl, compareCallsUrl, tracesUrl } from '../../lib/urls'
import { Empty, ErrorBox, Loading, StatusBadge } from '../../components/common'
import { ValueView } from '../../components/ValueView'
import { callLabel } from '../../components/traces/callUtils'
import { ChatView, isChatMessages } from '../../components/traces/ChatView'

function stable(v: unknown): string {
  if (v === undefined) return '\u0000undefined'
  return JSON.stringify(v, (_k, x) => (x && typeof x === 'object' && !Array.isArray(x) ? Object.fromEntries(Object.entries(x).sort(([a], [b]) => a.localeCompare(b))) : x))
}

function keysOf(calls: Call[], pick: (c: Call) => Any): string[] {
  const ks = new Set<string>()
  for (const c of calls) {
    const v = pick(c)
    if (v && typeof v === 'object' && !Array.isArray(v)) Object.keys(v).forEach((k) => ks.add(k))
  }
  return [...ks].filter((k) => k !== 'self')
}

interface Row {
  section: string
  key: string
  values: Any[]
  render?: (v: Any) => React.ReactNode
}

export function CompareCallsPage() {
  const projectId = useProjectId()
  const [params] = useSearchParams()
  const ids = (params.get('ids') ?? '').split(',').filter(Boolean)
  const [diffOnly, setDiffOnly] = useState(false)
  const q = useQuery({
    queryKey: ['calls', projectId, 'compare', ids],
    queryFn: () => api.callsQuery({ project_id: projectId, filter: { call_ids: ids }, include_costs: true, limit: ids.length }),
    enabled: ids.length > 0,
  })
  // keep the order of ?ids
  const calls = useMemo(() => {
    const by = new Map((q.data?.calls ?? []).map((c) => [c.id, c]))
    return ids.map((id) => by.get(id)).filter(Boolean) as Call[]
  }, [q.data, ids])

  const rows: Row[] = useMemo(() => {
    if (!calls.length) return []
    const r: Row[] = [
      { section: 'Call', key: 'op', values: calls.map((c) => opDisplayName(c.op_name)) },
      { section: 'Call', key: 'status', values: calls.map(callStatus), render: (v) => <StatusBadge status={v} /> },
      { section: 'Call', key: 'started', values: calls.map((c) => c.started_at), render: (v) => fmtDate(v) },
      { section: 'Call', key: 'latency', values: calls.map(callLatencyMs), render: (v) => fmtDuration(v) },
      { section: 'Call', key: 'tokens', values: calls.map((c) => callTokens(c).total), render: (v) => fmtNumber(v) },
      { section: 'Call', key: 'cost', values: calls.map(callCost), render: (v) => fmtCost(v) },
      { section: 'Call', key: 'models', values: calls.map((c) => Object.keys(c.summary?.usage ?? {}).sort().join(', ') || null) },
    ]
    if (calls.some((c) => c.exception)) r.push({ section: 'Call', key: 'exception', values: calls.map((c) => c.exception) })
    for (const k of keysOf(calls, (c) => c.inputs)) r.push({ section: 'Inputs', key: k, values: calls.map((c) => c.inputs?.[k]) })
    const outKeys = keysOf(calls, (c) => c.output)
    const outputsAreObjects = calls.every((c) => c.output == null || (typeof c.output === 'object' && !Array.isArray(c.output)))
    if (outputsAreObjects && outKeys.length) for (const k of outKeys) r.push({ section: 'Output', key: k, values: calls.map((c) => c.output?.[k]) })
    else r.push({ section: 'Output', key: 'output', values: calls.map((c) => c.output) })
    return r
  }, [calls])

  if (ids.length < 1)
    return (
      <div className="page">
        <Empty>
          Select two or more calls on the <Link to={tracesUrl(projectId)}>Traces</Link> page and choose Compare.
        </Empty>
      </div>
    )
  if (q.isLoading) return <Loading />
  const missing = ids.filter((id) => !calls.some((c) => c.id === id))
  const isDiff = (r: Row) => new Set(r.values.map(stable)).size > 1
  const shown = diffOnly ? rows.filter((r) => isDiff(r) || r.key === 'op') : rows
  const diffCount = rows.filter(isDiff).length
  let lastSection = ''
  return (
    <div className="page">
      <div className="breadcrumbs">
        <Link to={tracesUrl(projectId)}>Traces</Link>
        <span>/</span>
        <span>Compare</span>
      </div>
      <div className="page-header">
        <h1>Compare {calls.length} calls</h1>
        <span className="badge">{diffCount} differing fields</span>
        <span className="spacer" />
        <label className="checkbox">
          <input type="checkbox" checked={diffOnly} onChange={(e) => setDiffOnly(e.target.checked)} /> Only differences
        </label>
      </div>
      <ErrorBox error={q.error} />
      {missing.length > 0 && <div className="warn-box">Not found: {missing.join(', ')}</div>}
      <div className="table-wrap">
        <table className="table compare-table">
          <thead>
            <tr>
              <th style={{ width: 160 }}>Field</th>
              {calls.map((c, i) => (
                <th key={c.id}>
                  <div className="row">
                    <Link to={callUrl(projectId, c.id)}>{callLabel(c)}</Link>
                    <span className="faint mono small">{c.id.slice(-8)}</span>
                    <span className="grow" />
                    {calls.length > 2 && (
                      <Link className="faint small" title="Remove from comparison" to={compareCallsUrl(projectId, ids.filter((x) => x !== c.id))}>
                        ✕
                      </Link>
                    )}
                    {i > 0 && (
                      <Link
                        className="faint small"
                        title="Move left"
                        to={compareCallsUrl(projectId, ids.map((x, j) => (j === i - 1 ? ids[i] : j === i ? ids[i - 1] : x)))}
                      >
                        ←
                      </Link>
                    )}
                  </div>
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {shown.map((r) => {
              const d = isDiff(r)
              const head = r.section !== lastSection
              lastSection = r.section
              return (
                <FragmentRows key={`${r.section}.${r.key}`} head={head ? r.section : null} cols={calls.length + 1}>
                  <tr className={d ? 'diff-row' : ''}>
                    <td className="mono small">
                      {r.key}
                      {d && <span className="diff-mark" title="Values differ" />}
                    </td>
                    {r.values.map((v, i) => (
                      <td key={i} className={d ? 'diff-cell' : ''}>
                        {r.render ? (
                          r.render(v)
                        ) : v === undefined ? (
                          <span className="faint">—</span>
                        ) : isChatMessages(v) ? (
                          <ChatView messages={v} projectId={projectId} />
                        ) : (
                          <ValueView value={v} projectId={projectId} />
                        )}
                      </td>
                    ))}
                  </tr>
                </FragmentRows>
              )
            })}
          </tbody>
        </table>
      </div>
    </div>
  )
}

function FragmentRows({ head, cols, children }: { head: string | null; cols: number; children: React.ReactNode }) {
  return (
    <>
      {head && (
        <tr className="section-row">
          <td colSpan={cols}>{head}</td>
        </tr>
      )}
      {children}
    </>
  )
}
