import { useMemo, useState } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { api } from '../../api/client'
import { spanDisplayName } from '../../api/refs'
import type { Any, Span } from '../../api/types'
import { useProjectId } from '../../hooks/useProject'
import { callCost, callLatencyMs, callStatus, callTokens, fmtCost, fmtDate, fmtDuration, fmtNumber, fmtRelative, preview } from '../../lib/format'
import { spanUrl, compareSpansUrl, tracesUrl } from '../../lib/urls'
import { ErrorBox, Loading } from '../../components/common'
import { PageHeader } from '../../components/ui/PageHeader'
import { PlainTable } from '../../components/ui/PlainTable'
import { Segmented } from '../../components/ui/Segmented'
import { Sentence } from '../../components/ui/Sentence'
import { StatusGlyph } from '../../components/ui/StatusGlyph'
import { compareSentence, statusGlyph } from '../../lib/summaries/traces'
import '../observe.css'
import { ValueView } from '../../components/ValueView'
import { callLabel } from '../../components/traces/callUtils'
import { ChatView, isChatMessages } from '../../components/traces/ChatView'
import { DELTA_FORMATS, diffStrings, fmtDeltaAs, leafDiffs, numericDelta, parseDeltaFormat, similarity, type DeltaFormat, type DiffOp } from '../../components/traces/diffUtils'
import '../../components/traces/traceViews.css'

function stable(v: unknown): string {
  if (v === undefined) return '\u0000undefined'
  return JSON.stringify(v, (_k, x) => (x && typeof x === 'object' && !Array.isArray(x) ? Object.fromEntries(Object.entries(x).sort(([a], [b]) => a.localeCompare(b))) : x))
}

function keysOf(calls: Span[], pick: (c: Span) => Any): string[] {
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
  /** The field in words, for the span's own fields ("Latency"); data keys show as recorded. */
  label?: string
  values: Any[]
  render?: (v: Any) => React.ReactNode
  /** Numeric rows: how to format a delta and whether a decrease is an improvement. */
  num?: { fmt: (n: number) => string; lowerBetter: boolean }
  /** Timestamp rows show the offset from the baseline instead of a text diff. */
  time?: boolean
  /** Compare as whole values (no word diff). */
  atomic?: boolean
}

type Mode = 'values' | 'diff'

export function CompareSpansPage() {
  const projectId = useProjectId()
  const [params, setParams] = useSearchParams()
  const ids = useMemo(() => (params.get('ids') ?? '').split(',').filter(Boolean), [params])
  const [diffOnly, setDiffOnly] = useState(false)
  const mode: Mode = params.get('mode') === 'diff' ? 'diff' : 'values'
  const baselineId = ids.includes(params.get('baseline') ?? '') ? params.get('baseline')! : ids[0]
  const dfmt = parseDeltaFormat(params.get('nfmt'))
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
    queryKey: ['spans', projectId, 'compare', ids],
    queryFn: () => api.spansQuery({ project_id: projectId, filter: { ids: ids }, include_costs: true, limit: ids.length }),
    enabled: ids.length > 0,
  })
  // keep the order of ?ids
  const calls = useMemo(() => {
    const by = new Map((q.data?.items ?? []).map((c) => [c.id, c]))
    return ids.map((id) => by.get(id)).filter(Boolean) as Span[]
  }, [q.data, ids])
  const baseIdx = Math.max(0, calls.findIndex((c) => c.id === baselineId))

  const rows: Row[] = useMemo(() => {
    if (!calls.length) return []
    const r: Row[] = [
      { section: 'Overview', key: 'name', label: 'Name', values: calls.map((c) => spanDisplayName(c.name)), atomic: true },
      { section: 'Overview', key: 'status', label: 'Status', values: calls.map(callStatus), render: (v) => (
          <span className="row" style={{ gap: 6 }}>
            <StatusGlyph kind={statusGlyph(v).kind} size={16} decorative /> {statusGlyph(v).label}
          </span>
        ) },
      { section: 'Overview', key: 'started', label: 'Started', values: calls.map((c) => c.started_at), render: (v) => fmtDate(v), time: true },
      { section: 'Overview', key: 'latency', label: 'Latency', values: calls.map(callLatencyMs), render: (v) => fmtDuration(v), num: { fmt: (n) => fmtDuration(n), lowerBetter: true } },
      { section: 'Overview', key: 'tokens', label: 'Tokens', values: calls.map((c) => callTokens(c).total), render: (v) => fmtNumber(v), num: { fmt: (n) => fmtNumber(n), lowerBetter: true } },
      { section: 'Overview', key: 'cost', label: 'Cost', values: calls.map(callCost), render: (v) => fmtCost(v), num: { fmt: (n) => fmtCost(n), lowerBetter: true } },
      { section: 'Overview', key: 'models', label: 'Models', values: calls.map((c) => Object.keys(c.summary?.usage ?? {}).sort().join(', ') || null), atomic: true },
    ]
    if (calls.some((c) => c.error)) r.push({ section: 'Overview', key: 'error', label: 'Error', values: calls.map((c) => c.error) })
    for (const k of keysOf(calls, (c) => c.inputs)) r.push({ section: 'Inputs', key: k, values: calls.map((c) => c.inputs?.[k]) })
    const outKeys = keysOf(calls, (c) => c.output)
    const outputsAreObjects = calls.every((c) => c.output == null || (typeof c.output === 'object' && !Array.isArray(c.output)))
    if (outputsAreObjects && outKeys.length) for (const k of outKeys) r.push({ section: 'Output', key: k, values: calls.map((c) => c.output?.[k]) })
    else r.push({ section: 'Output', key: 'output', values: calls.map((c) => c.output) })
    return r
  }, [calls])

  if (ids.length < 1)
    return (
      <div className="page-frame detail obs-page">
        <PageHeader variant="detail" back={{ to: tracesUrl(projectId), label: 'Traces' }} title="Compare spans" help="traces.md" helpTopic="Traces" />
        <div className="quiet-empty">
          <p>
            Select two or more spans on the <Link to={tracesUrl(projectId)}>Traces</Link> page and choose Compare.
          </p>
        </div>
      </div>
    )
  if (q.isLoading) return <Loading />
  const missing = ids.filter((id) => !calls.some((c) => c.id === id))
  const isDiff = (r: Row) => new Set(r.values.map(stable)).size > 1
  const shown = diffOnly ? rows.filter((r) => isDiff(r) || r.key === 'name') : rows
  const diffCount = rows.filter(isDiff).length
  let lastSection = ''
  return (
    <div className="page-frame obs-page obs-wide">
      <PageHeader
        variant="detail"
        back={{ to: tracesUrl(projectId), label: 'Traces' }}
        title={`Compare ${calls.length} span${calls.length === 1 ? '' : 's'}`}
        help="traces.md"
        helpTopic="Traces"
        sentence={
          <Sentence
            size="detail"
            parts={[compareSentence({ spans: calls.length, fields: rows.length, differ: diffCount, missing: missing.length, baseline: calls[baseIdx] ? callLabel(calls[baseIdx]) : null, diffMode: mode === 'diff' })]}
          />
        }
        meta={missing.length > 0 ? `Not found: ${missing.join(', ')}` : undefined}
      />
      <div className="obs-toolbar">
        <Segmented<Mode>
          label="Display"
          size="sm"
          options={[
            { id: 'values', label: 'Values' },
            { id: 'diff', label: 'Changes from the baseline', title: "Show each span's changes against the baseline" },
          ]}
          value={mode}
          onChange={(m) => setParam('mode', m === 'diff' ? 'diff' : null)}
        />
        <span className="obs-inline-control">
          <span aria-hidden>Show change as</span>
          <Segmented label="Show change as" size="sm" options={DELTA_FORMATS} value={dfmt} onChange={(f) => setParam('nfmt', f === 'both' ? null : f)} />
        </span>
        <label className="obs-check">
          <input type="checkbox" checked={diffOnly} onChange={(e) => setDiffOnly(e.target.checked)} /> Only differences
        </label>
      </div>
      <ErrorBox error={q.error} />
      <PlainTable caption="Fields of the compared spans" className="compare-table">
          <thead>
            <tr>
              <th scope="col" style={{ width: 160 }}>Field</th>
              {calls.map((c, i) => (
                <th scope="col" key={c.id} className={i === baseIdx ? 'cmp-baseline-col' : ''}>
                  <div className="row wrap" style={{ gap: 6 }}>
                    <Link to={spanUrl(projectId, c.id)} title={`Span ${c.id}`}>
                      {callLabel(c)}
                    </Link>
                    <span className="obs-faint small" title={fmtDate(c.started_at)}>
                      {fmtRelative(c.started_at)}
                    </span>
                    <span className="grow" />
                    {i === baseIdx ? (
                      <span className="obs-faint small" title="Other spans are compared against this one">
                        Baseline
                      </span>
                    ) : (
                      <button type="button" className="linkish small" onClick={() => setParam('baseline', c.id)} title="Compare the other spans against this one">
                        Set baseline
                      </button>
                    )}
                    {calls.length > 2 && (
                      <Link className="faint small" title="Remove from comparison" to={compareSpansUrl(projectId, ids.filter((x) => x !== c.id))}>
                        ✕
                      </Link>
                    )}
                    {i > 0 && (
                      <Link
                        className="faint small"
                        title="Move left"
                        to={compareSpansUrl(projectId, ids.map((x, j) => (j === i - 1 ? ids[i] : j === i ? ids[i - 1] : x)))}
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
                    <td className={`cmp-field ${r.label ? '' : 'cmp-key'}`}>
                      {r.label ?? r.key}
                      {d && <span className="sr-only"> (differs)</span>}
                    </td>
                    {r.values.map((v, i) => {
                      const base = r.values[baseIdx]
                      const changed = stable(v) !== stable(base)
                      return (
                        <td key={i} className={`${d && changed ? 'diff-cell' : ''} ${i === baseIdx ? 'cmp-baseline-col' : ''}`}>
                          {mode === 'diff' && i !== baseIdx ? (
                            <DiffCell row={r} base={base} value={v} projectId={projectId} dfmt={dfmt} />
                          ) : (
                            <>
                              <PlainValue row={r} value={v} projectId={projectId} />
                              {r.num && i !== baseIdx && <NumDelta base={base} value={v} num={r.num} dfmt={dfmt} />}
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
      </PlainTable>
    </div>
  )
}

function PlainValue({ row, value: v, projectId }: { row: Row; value: Any; projectId: string }) {
  if (row.render) return <>{row.render(v)}</>
  if (v === undefined) return <span className="faint">—</span>
  if (isChatMessages(v)) return <ChatView messages={v} projectId={projectId} />
  return <ValueView value={v} projectId={projectId} />
}

function NumDelta({ base, value, num, dfmt }: { base: Any; value: Any; num?: Row['num']; dfmt: DeltaFormat }) {
  const d = numericDelta(base, value)
  if (!d) return null
  if (d.delta === 0) return <div className="delta-same small">{dfmt === 'ratio' ? '1×' : '±0'}</div>
  // A change is told in plain grey text; colour is kept for failed and succeeded glyphs.
  return <div className="small cmp-delta">{fmtDeltaAs(base, value, dfmt, num?.fmt)}</div>
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

function DiffCell({ row, base, value, projectId, dfmt }: { row: Row; base: Any; value: Any; projectId: string; dfmt: DeltaFormat }) {
  if (stable(base) === stable(value))
    return (
      <span className="faint small" title="Same as the baseline">
        = baseline
      </span>
    )
  if (row.time && typeof base === 'string' && typeof value === 'string') {
    const ms = new Date(value).getTime() - new Date(base).getTime()
    return (
      <>
        <PlainValue row={row} value={value} projectId={projectId} />
        <div className="small cmp-delta">
          {fmtDuration(Math.abs(ms))} {ms >= 0 ? 'after' : 'before'} baseline
        </div>
      </>
    )
  }
  // Identifiers, model names and other single tokens read better as "new (was old)".
  const atomic = row.atomic || (typeof value === 'string' && typeof base === 'string' && value.length < 60 && base.length < 60 && !/\s/.test(value + base))
  if (atomic && (typeof value !== 'object' || value === null))
    return (
      <div className="col" style={{ gap: 2 }}>
        <PlainValue row={row} value={value} projectId={projectId} />
        <span className="faint small">
          was <span className="tdiff-del">{base == null ? 'empty' : preview(base, 60)}</span>
        </span>
      </div>
    )
  if (row.num || (typeof value === 'number' && typeof base === 'number'))
    return (
      <>
        <PlainValue row={row} value={value} projectId={projectId} />
        <NumDelta base={base} value={value} num={row.num} dfmt={dfmt} />
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
                {preview(l.value, 40)} <span className="cmp-delta">{fmtDeltaAs(l.base, l.value, dfmt)}</span>
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
