import { useMemo, useState } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { api } from '../../api/client'
import { opDisplayName } from '../../api/refs'
import type { Any, Call } from '../../api/types'
import { useProjectId } from '../../hooks/useProject'
import { callCost, callLatencyMs, callStatus, callTokens, fmtCost, fmtDate, fmtDuration, fmtNumber, preview } from '../../lib/format'
import { callUrl, compareCallsUrl, tracesUrl } from '../../lib/urls'
import { Empty, ErrorBox, Loading, StatusBadge } from '../../components/common'
import { ValueView } from '../../components/ValueView'
import { callLabel } from '../../components/traces/callUtils'
import { ChatView, isChatMessages } from '../../components/traces/ChatView'
import { diffStrings, fmtDelta, leafDiffs, numericDelta, similarity, type DiffOp } from '../../components/traces/diffUtils'
import '../../components/traces/traceViews.css'

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
  /** Numeric rows: how to format a delta and whether a decrease is an improvement. */
  num?: { fmt: (n: number) => string; lowerBetter: boolean }
}

type Mode = 'values' | 'diff'

export function CompareCallsPage() {
  const projectId = useProjectId()
  const [params, setParams] = useSearchParams()
  const ids = useMemo(() => (params.get('ids') ?? '').split(',').filter(Boolean), [params])
  const [diffOnly, setDiffOnly] = useState(false)
  const mode: Mode = params.get('mode') === 'diff' ? 'diff' : 'values'
  const baselineId = ids.includes(params.get('baseline') ?? '') ? params.get('baseline')! : ids[0]
  const setParam = (k: string, v: string | null) =>
    setParams(
      (p) => {
        const n = new URLSearchParams(p)
        if (v == null) n.delete(k)
        else n.set(k, v)
        return n
      },
      { replace: true },
    )
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
  const baseIdx = Math.max(0, calls.findIndex((c) => c.id === baselineId))

  const rows: Row[] = useMemo(() => {
    if (!calls.length) return []
    const r: Row[] = [
      { section: 'Call', key: 'op', values: calls.map((c) => opDisplayName(c.op_name)) },
      { section: 'Call', key: 'status', values: calls.map(callStatus), render: (v) => <StatusBadge status={v} /> },
      { section: 'Call', key: 'started', values: calls.map((c) => c.started_at), render: (v) => fmtDate(v) },
      { section: 'Call', key: 'latency', values: calls.map(callLatencyMs), render: (v) => fmtDuration(v), num: { fmt: (n) => fmtDuration(n), lowerBetter: true } },
      { section: 'Call', key: 'tokens', values: calls.map((c) => callTokens(c).total), render: (v) => fmtNumber(v), num: { fmt: (n) => fmtNumber(n), lowerBetter: true } },
      { section: 'Call', key: 'cost', values: calls.map(callCost), render: (v) => fmtCost(v), num: { fmt: (n) => fmtCost(n), lowerBetter: true } },
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
        <div className="seg" role="group" aria-label="Display">
          <button className={mode === 'values' ? 'on' : ''} aria-pressed={mode === 'values'} onClick={() => setParam('mode', null)}>
            Values
          </button>
          <button className={mode === 'diff' ? 'on' : ''} aria-pressed={mode === 'diff'} onClick={() => setParam('mode', 'diff')} title="Show each call's changes against the baseline">
            Diff vs baseline
          </button>
        </div>
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
                <th key={c.id} className={i === baseIdx ? 'cmp-baseline-col' : ''}>
                  <div className="row wrap" style={{ gap: 6 }}>
                    <Link to={callUrl(projectId, c.id)}>{callLabel(c)}</Link>
                    <span className="faint mono small">{c.id.slice(-8)}</span>
                    <span className="grow" />
                    {i === baseIdx ? (
                      <span className="badge accent" title="Other calls are compared against this one">
                        Baseline
                      </span>
                    ) : (
                      <button className="btn ghost sm" onClick={() => setParam('baseline', c.id)} title="Compare the other calls against this one">
                        Set baseline
                      </button>
                    )}
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
                    {r.values.map((v, i) => {
                      const base = r.values[baseIdx]
                      const changed = stable(v) !== stable(base)
                      return (
                        <td key={i} className={`${d && changed ? 'diff-cell' : ''} ${i === baseIdx ? 'cmp-baseline-col' : ''}`}>
                          {mode === 'diff' && i !== baseIdx ? (
                            <DiffCell row={r} base={base} value={v} projectId={projectId} />
                          ) : (
                            <>
                              <PlainValue row={r} value={v} projectId={projectId} />
                              {r.num && i !== baseIdx && <NumDelta base={base} value={v} num={r.num} />}
                            </>
                          )}
                        </td>
                      )
                    })}
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

function PlainValue({ row, value: v, projectId }: { row: Row; value: Any; projectId: string }) {
  if (row.render) return <>{row.render(v)}</>
  if (v === undefined) return <span className="faint">—</span>
  if (isChatMessages(v)) return <ChatView messages={v} projectId={projectId} />
  return <ValueView value={v} projectId={projectId} />
}

function NumDelta({ base, value, num }: { base: Any; value: Any; num?: Row['num'] }) {
  const d = numericDelta(base, value)
  if (!d) return null
  if (d.delta === 0) return <div className="delta-same small">±0</div>
  const better = num ? (num.lowerBetter ? d.delta < 0 : d.delta > 0) : null
  return <div className={`small ${better == null ? 'cmp-delta' : better ? 'delta-up' : 'delta-down'}`}>{fmtDelta(d, num?.fmt)}</div>
}

export function StringDiff({ ops }: { ops: DiffOp[] }) {
  return (
    <span className="tdiff">
      {ops.map((o, i) =>
        o.op === 'same' ? (
          <span key={i}>{o.text}</span>
        ) : o.op === 'add' ? (
          <ins key={i} className="tdiff-add">
            {o.text}
          </ins>
        ) : (
          <del key={i} className="tdiff-del">
            {o.text}
          </del>
        ),
      )}
    </span>
  )
}

function DiffCell({ row, base, value, projectId }: { row: Row; base: Any; value: Any; projectId: string }) {
  if (stable(base) === stable(value))
    return (
      <span className="faint small" title="Same as the baseline">
        = baseline
      </span>
    )
  if (row.num || (typeof value === 'number' && typeof base === 'number'))
    return (
      <>
        <PlainValue row={row} value={value} projectId={projectId} />
        <NumDelta base={base} value={value} num={row.num} />
      </>
    )
  if (typeof value === 'string' && typeof base === 'string') {
    const ops = diffStrings(base, value)
    return (
      <div className="col" style={{ gap: 4 }}>
        <div className="tdiff-block">
          <StringDiff ops={ops} />
        </div>
        <span className="faint small">{Math.round(similarity(ops) * 100)}% similar</span>
      </div>
    )
  }
  if (base && value && typeof base === 'object' && typeof value === 'object') {
    const leaves = leafDiffs(base, value).filter((l) => l.status !== 'same')
    const unchanged = leafDiffs(base, value).length - leaves.length
    return (
      <div className="col" style={{ gap: 3 }}>
        {leaves.slice(0, 40).map((l) => (
          <div key={l.path} className={`cmp-leaf ${l.status}`}>
            <span className="mono small cmp-leaf-path">{l.path || '(value)'}</span>
            {l.status === 'added' ? (
              <span className="tdiff-add small">+ {preview(l.value, 80)}</span>
            ) : l.status === 'removed' ? (
              <span className="tdiff-del small">{preview(l.base, 80)}</span>
            ) : l.numeric ? (
              <span className="small">
                {preview(l.value, 40)} <span className="cmp-delta">{fmtDelta(l.numeric)}</span>
              </span>
            ) : typeof l.base === 'string' && typeof l.value === 'string' ? (
              <span className="small tdiff-block">
                <StringDiff ops={diffStrings(l.base, l.value)} />
              </span>
            ) : (
              <span className="small">
                <span className="tdiff-del">{preview(l.base, 40)}</span> → <span className="tdiff-add">{preview(l.value, 40)}</span>
              </span>
            )}
          </div>
        ))}
        {leaves.length > 40 && <span className="faint small">… {leaves.length - 40} more changes</span>}
        {unchanged > 0 && <span className="faint small">{unchanged} unchanged</span>}
      </div>
    )
  }
  return (
    <div className="col" style={{ gap: 4 }}>
      <PlainValue row={row} value={value} projectId={projectId} />
      <span className="faint small">
        was <span className="tdiff-del">{base === undefined ? 'missing' : preview(base, 60)}</span>
      </span>
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
