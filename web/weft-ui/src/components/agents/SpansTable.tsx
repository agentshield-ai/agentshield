import { Fragment, useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { agentEvalsApi, agentsApi, agentUrl, conversationUrl, signalsApi, type AgentEvalPrediction, type AgentSpan, type SignalResult, type SpanGroup } from '../../api/agents'
import { fmtCost, fmtDate, fmtDuration, fmtNumber, fmtRelative, truncate } from '../../lib/format'
import { callUrl } from '../../lib/urls'
import { Empty, ErrorBox, Pager, Spinner } from '../common'
import { compact, errorRate, fmtPct, kindClass, windowStartOrAll } from './agentUtils'
import { EvalFilterBanner, ScoreChips } from './AgentEvals'
import { RangePicker, ScorePill } from './bits'
import {
  activeFilterCount,
  EMPTY_SPAN_FILTERS,
  GROUP_SORT_FIELDS,
  parseSort,
  RATING_OP_LABEL,
  readSpanFilters,
  SENTIMENTS,
  SPAN_GROUPS,
  SPAN_SORT_FIELDS,
  spanQueryParts,
  writeSpanFilters,
  type SpanFilterState,
} from './signalUtils'
import type { SignalRatingFilter } from '../../api/agents'
import './agents.css'

const LIMIT = 50

type Update = (patch: Record<string, string | null>) => void

function FilterSelect({ label, value, options, onChange }: { label: string; value: string; options: string[]; onChange: (v: string) => void }) {
  const opts = value && !options.includes(value) ? [value, ...options] : options
  return (
    <label className="small muted row" style={{ gap: 4 }}>
      {label}
      <select className="input sm" value={value} onChange={(e) => onChange(e.target.value)} aria-label={label}>
        <option value="">Any</option>
        {opts.map((o) => (
          <option key={o} value={o}>
            {o}
          </option>
        ))}
      </select>
    </label>
  )
}

function SignalFilterEditor({ f, set }: { f: SpanFilterState; set: (f: SpanFilterState) => void }) {
  const [tag, setTag] = useState('')
  const [r, setR] = useState<SignalRatingFilter>({ scorer_key: '', op: 'lt', value: 0.5 })
  return (
    <div className="row wrap" style={{ gap: 8 }}>
      <form
        className="row"
        style={{ gap: 4 }}
        onSubmit={(e) => {
          e.preventDefault()
          const t = tag.trim().toLowerCase()
          if (t && !f.tags.includes(t)) set({ ...f, tags: [...f.tags, t] })
          setTag('')
        }}
      >
        <input className="input sm" style={{ width: 150 }} placeholder="Has tag, e.g. nsfw" value={tag} onChange={(e) => setTag(e.target.value)} aria-label="Signal tag" />
      </form>
      <form
        className="row"
        style={{ gap: 4 }}
        onSubmit={(e) => {
          e.preventDefault()
          if (r.scorer_key.trim()) set({ ...f, ratings: [...f.ratings, { ...r, scorer_key: r.scorer_key.trim() }] })
        }}
      >
        <input className="input sm mono" style={{ width: 150 }} placeholder="Rating, e.g. response_quality" value={r.scorer_key} onChange={(e) => setR({ ...r, scorer_key: e.target.value })} aria-label="Rating name" />
        <select className="input sm" value={r.op} onChange={(e) => setR({ ...r, op: e.target.value as SignalRatingFilter['op'] })} aria-label="Comparison">
          {Object.entries(RATING_OP_LABEL).map(([k, v]) => (
            <option key={k} value={k}>
              {v}
            </option>
          ))}
        </select>
        <input className="input sm" type="number" step="0.05" min={0} max={1} style={{ width: 70 }} value={r.value} onChange={(e) => setR({ ...r, value: Number(e.target.value) })} aria-label="Rating value" />
        <button className="btn sm" type="submit" disabled={!r.scorer_key.trim()}>
          Add
        </button>
      </form>
      <label className="small muted row" style={{ gap: 4 }}>
        Sentiment
        <select
          className="input sm"
          value=""
          onChange={(e) => e.target.value && !f.sentiment.includes(e.target.value) && set({ ...f, sentiment: [...f.sentiment, e.target.value] })}
          aria-label="Conversation sentiment"
        >
          <option value="">Add…</option>
          {SENTIMENTS.filter((s) => !f.sentiment.includes(s)).map((s) => (
            <option key={s} value={s}>
              {s}
            </option>
          ))}
        </select>
      </label>
    </div>
  )
}

function FilterChips({ f, set, pinnedAgent }: { f: SpanFilterState; set: (f: SpanFilterState) => void; pinnedAgent?: string }) {
  const chips: { label: string; clear: () => void }[] = []
  for (const k of ['agent', 'op', 'model', 'tool', 'status'] as const)
    if (f[k] && !(k === 'agent' && pinnedAgent)) chips.push({ label: `${k === 'op' ? 'operation' : k}: ${f[k]}`, clear: () => set({ ...f, [k]: '' }) })
  for (const t of f.tags) chips.push({ label: `tag: ${t}`, clear: () => set({ ...f, tags: f.tags.filter((x) => x !== t) }) })
  f.ratings.forEach((r, i) => chips.push({ label: `${r.scorer_key} ${RATING_OP_LABEL[r.op]} ${r.value}`, clear: () => set({ ...f, ratings: f.ratings.filter((_, j) => j !== i) }) }))
  if (f.sentiment.length) chips.push({ label: `sentiment: ${f.sentiment.join(' or ')}`, clear: () => set({ ...f, sentiment: [] }) })
  if (!chips.length) return null
  return (
    <div className="row wrap" style={{ gap: 6 }}>
      {chips.map((c) => (
        <span key={c.label} className="filter-chip">
          <span className="filter-chip-label">{c.label}</span>
          <button className="filter-chip-x" aria-label={`Remove filter ${c.label}`} onClick={c.clear}>
            ✕
          </button>
        </span>
      ))}
      <button className="btn ghost sm" onClick={() => set({ ...EMPTY_SPAN_FILTERS, agent: pinnedAgent ?? '', evalRun: f.evalRun, evalPred: f.evalPred })}>
        Clear all
      </button>
    </div>
  )
}

function SignalsCell({ rs }: { rs: SignalResult[] | undefined }) {
  if (!rs?.length) return <span className="faint">—</span>
  return (
    <div className="chips" style={{ flexWrap: 'wrap' }}>
      {rs.map((r) =>
        r.kind === 'rating' ? (
          <span key={r.feedback_id + r.signal} className="row small" style={{ gap: 4 }} title={r.reason ?? undefined}>
            <span className="muted">{r.signal}</span>
            <ScorePill value={r.score} confidence={r.confidence} reason={r.reason} />
          </span>
        ) : (
          r.tags.map((t) => (
            <span key={r.feedback_id + t} className="chip tag-chip" title={[r.reason, r.confidence != null ? `confidence ${Math.round(r.confidence * 100)}%` : ''].filter(Boolean).join(' · ') || undefined}>
              {t}
            </span>
          ))
        ),
      )}
    </div>
  )
}

function SortTh({ id, label, sort, setSort, num }: { id: string; label: string; sort: { col: string; direction: string }; setSort: (col: string) => void; num?: boolean }) {
  const on = sort.col === id
  return (
    <th className={`sortable ${num ? 'num' : ''}`} onClick={() => setSort(id)} aria-sort={on ? (sort.direction === 'asc' ? 'ascending' : 'descending') : undefined}>
      {label}
      {on ? (sort.direction === 'desc' ? ' ↓' : ' ↑') : ''}
    </th>
  )
}

/**
 * Cross-agent span table: filters (including signal and insight filters),
 * sorting, group-by with drill-down, and per-turn ratings and tags. State
 * lives in the URL (`params`/`update`) so views are shareable.
 */
export function SpansTable({ projectId, params, update, agent }: { projectId: string; params: URLSearchParams; update: Update; agent?: string }) {
  const range = params.get('range') ?? '24h'
  const since = useMemo(() => windowStartOrAll(range), [range])
  const f = readSpanFilters(params)
  const setF = (nf: SpanFilterState) => update({ ...writeSpanFilters(nf), offset: null })
  const group = params.get('group') ?? ''
  const offset = Math.max(0, Number(params.get('offset') ?? 0) || 0)
  const sort = group ? parseSort(params.get('gsort'), GROUP_SORT_FIELDS, 'spans') : parseSort(params.get('ssort'), SPAN_SORT_FIELDS, 'started')
  const setSort = (col: string) => {
    const dir = sort.col === col && sort.direction === 'desc' ? 'asc' : 'desc'
    update({ [group ? 'gsort' : 'ssort']: `${col}:${dir}`, offset: null })
  }
  const [showFilters, setShowFilters] = useState(false)
  const [hlTrace, setHlTrace] = useState<string | null>(null)
  const parts = spanQueryParts(f, agent)

  const attrs = useQuery({
    queryKey: ['agent-custom-attrs', projectId, since],
    queryFn: () => agentsApi.customAttrs({ project_id: projectId, started_after: since, limit: 50 }),
    staleTime: 60_000,
  })
  const groupOptions = [...SPAN_GROUPS, ...(attrs.data?.attributes ?? []).map((a) => ({ key: `${a.source}.${a.key}`, label: `attr: ${a.key}` }))]
  const groupRef = group ? (() => {
    const m = /^(custom_attrs_(?:string|int|float|bool))\.(.+)$/.exec(group)
    return m ? { key: m[2], source: m[1] as 'custom_attrs_string', alias: 'group' } : { key: group, alias: 'group' }
  })() : null

  const facets = useQuery({
    queryKey: ['agent-span-facets', projectId, since, agent, f.evalRun, f.evalPred],
    queryFn: async () => {
      const base = {
        project_id: projectId,
        started_after: since,
        query: agent ? spanQueryParts(EMPTY_SPAN_FILTERS, agent).query : undefined,
        evaluation_run_id: f.evalRun || undefined,
        predict_and_score_call_id: f.evalPred || undefined,
        limit: 100,
      }
      const [ops, models, tools, agents] = await Promise.all(
        ['operation_name', 'request_model', 'tool_name', 'agent_name'].map((k) => agentsApi.spans({ ...base, group_by: [{ key: k }], sort_by: [{ field: 'span_count', direction: 'desc' }] })),
      )
      const keys = (r: { groups: SpanGroup[] }) => r.groups.map((g) => String(Object.values(g.group_keys)[0] ?? '')).filter(Boolean)
      return { ops: keys(ops), models: keys(models), tools: keys(tools), agents: keys(agents) }
    },
    staleTime: 30_000,
  })

  const q = useQuery({
    queryKey: ['agent-spans-table', projectId, since, parts, group, sort.field, sort.direction, offset],
    queryFn: () =>
      agentsApi.spans({
        project_id: projectId,
        started_after: since,
        include_costs: true,
        include_details: !group,
        ...parts,
        group_by: groupRef ? [groupRef] : undefined,
        sort_by: [{ field: sort.field, direction: sort.direction }],
        limit: LIMIT,
        offset,
      }),
    placeholderData: keepPreviousData,
  })
  const spans: AgentSpan[] = q.data?.spans ?? []
  const traceIds = useMemo(() => [...new Set(spans.map((s) => s.trace_id))], [spans])
  const sig = useQuery({
    queryKey: ['agent-span-signals', projectId, traceIds],
    queryFn: () => signalsApi.query({ project_id: projectId, trace_ids: traceIds, limit: 1000 }),
    enabled: traceIds.length > 0 && !group,
  })
  const sigBySpan = useMemo(() => {
    const m = new Map<string, SignalResult[]>()
    for (const r of sig.data?.results ?? []) m.set(r.span_id, [...(m.get(r.span_id) ?? []), r])
    return m
  }, [sig.data])

  const nFilters = activeFilterCount({ ...f, agent: agent ? '' : f.agent })
  const evalFiltered = !!(f.evalRun || f.evalPred)
  // Conversations of an evaluation: the predictions each belongs to, with scores.
  const evalConvs = useQuery({
    queryKey: ['agent-eval-convs', projectId, f.evalRun, f.evalPred, agent ?? null],
    queryFn: () =>
      agentEvalsApi.conversations({ project_id: projectId, evaluation_run_id: f.evalRun || undefined, predict_and_score_call_id: f.evalPred || undefined, agent_name: agent, limit: 1000 }),
    enabled: evalFiltered && groupRef?.key === 'conversation_id',
    staleTime: 15_000,
  })
  const predsByConv = useMemo(() => new Map((evalConvs.data?.conversations ?? []).map((c) => [c.conversation_id, c.predictions])), [evalConvs.data])
  const drill = (g: SpanGroup) => {
    const k = Object.keys(g.group_keys)[0]
    const v = g.group_keys[k]
    const field = groupRef?.key ?? ''
    const patch: Record<string, string | null> = { group: null, offset: null }
    const known: Record<string, keyof SpanFilterState> = { agent_name: 'agent', operation_name: 'op', request_model: 'model', tool_name: 'tool' }
    if (known[field] && v != null && v !== '') patch[known[field]] = String(v)
    else if (field === 'status_code') patch.status = v === 'ERROR' ? 'error' : 'ok'
    else if (field === 'trace_id' && v) setHlTrace(String(v))
    else return // no span filter for this group key
    update(patch)
  }

  return (
    <div className="col" style={{ gap: 10 }}>
      <div className="toolbar row wrap">
        <RangePicker value={range} allowAll onChange={(r) => update({ range: r === '24h' ? null : r, offset: null })} />
        {!agent && <FilterSelect label="Agent" value={f.agent} options={facets.data?.agents ?? []} onChange={(v) => setF({ ...f, agent: v })} />}
        <FilterSelect label="Operation" value={f.op} options={facets.data?.ops ?? []} onChange={(v) => setF({ ...f, op: v })} />
        <FilterSelect label="Model" value={f.model} options={facets.data?.models ?? []} onChange={(v) => setF({ ...f, model: v })} />
        <FilterSelect label="Tool" value={f.tool} options={facets.data?.tools ?? []} onChange={(v) => setF({ ...f, tool: v })} />
        <label className="small muted row" style={{ gap: 4 }}>
          Status
          <select className="input sm" value={f.status} onChange={(e) => setF({ ...f, status: e.target.value as SpanFilterState['status'] })} aria-label="Status">
            <option value="">Any</option>
            <option value="ok">OK</option>
            <option value="error">Error</option>
          </select>
        </label>
        <button className={`btn sm ${showFilters ? 'primary' : ''}`} aria-expanded={showFilters} onClick={() => setShowFilters((s) => !s)}>
          Signal filters{f.tags.length + f.ratings.length + (f.sentiment.length ? 1 : 0) ? ` (${f.tags.length + f.ratings.length + (f.sentiment.length ? 1 : 0)})` : ''}
        </button>
        <span className="grow" />
        <label className="small muted row" style={{ gap: 4 }}>
          Group by
          <select className="input sm" value={group} onChange={(e) => update({ group: e.target.value || null, offset: null })} aria-label="Group by">
            {groupOptions.map((g) => (
              <option key={g.key} value={g.key}>
                {g.label}
              </option>
            ))}
          </select>
        </label>
        {q.isFetching && <Spinner />}
      </div>
      {showFilters && (
        <div className="card">
          <div className="card-body col" style={{ gap: 6 }}>
            <SignalFilterEditor f={f} set={setF} />
            <span className="small faint">Tags match turns carrying any of them; every rating condition must hold. Sentiment is per conversation.</span>
          </div>
        </div>
      )}
      {evalFiltered && (
        <EvalFilterBanner projectId={projectId} runId={f.evalRun} pred={f.evalPred || undefined} agent={agent} onClear={() => setF({ ...f, evalRun: '', evalPred: '' })} />
      )}
      {nFilters > (evalFiltered ? 1 : 0) && <FilterChips f={f} set={setF} pinnedAgent={agent} />}
      <ErrorBox error={q.error} />
      {q.isLoading ? (
        <div className="empty">
          <Spinner label="Loading spans…" />
        </div>
      ) : group ? (
        <GroupTable projectId={projectId} groups={q.data?.groups ?? []} groupKey={groupRef?.key ?? group} sort={sort} setSort={setSort} onDrill={drill} allTime={range === 'all'} predsByConv={evalFiltered ? predsByConv : undefined} />
      ) : spans.length === 0 ? (
        <Empty>{evalFiltered ? `No agent spans of this evaluation${range === 'all' ? '' : ' in this period'}${nFilters > 1 ? ' match these filters' : ''}.` : `No spans match${nFilters ? ' these filters' : ' in this period'}.`}</Empty>
      ) : (
        <div className="table-wrap">
          <table className="table spans-table">
            <thead>
              <tr>
                <SortTh id="span" label="Span" sort={sort} setSort={setSort} />
                <SortTh id="op" label="Operation" sort={sort} setSort={setSort} />
                {!agent && <SortTh id="agent" label="Agent" sort={sort} setSort={setSort} />}
                <SortTh id="model" label="Model / tool" sort={sort} setSort={setSort} />
                <th>Ratings & tags</th>
                <SortTh id="in" label="In" sort={sort} setSort={setSort} num />
                <SortTh id="out" label="Out" sort={sort} setSort={setSort} num />
                <SortTh id="cost" label="Cost" sort={sort} setSort={setSort} num />
                <SortTh id="duration" label="Duration" sort={sort} setSort={setSort} num />
                <th>Finish</th>
                <SortTh id="status" label="Status" sort={sort} setSort={setSort} />
                <SortTh id="started" label="Started" sort={sort} setSort={setSort} />
              </tr>
            </thead>
            <tbody>
              {spans.map((s) => (
                <tr
                  key={s.span_id}
                  className={`clickable ${hlTrace === s.trace_id ? 'trace-hl' : ''}`}
                  onClick={() => setHlTrace((t) => (t === s.trace_id ? null : s.trace_id))}
                  title="Click to highlight every span in this trace"
                >
                  <td style={{ maxWidth: 280 }}>
                    <div className="row" style={{ gap: 6 }}>
                      <span className={`kind-dot ${kindClass(s.operation_name, s.status_code)}`} />
                      <Link to={callUrl(projectId, s.span_id)} className="mono small truncate" onClick={(e) => e.stopPropagation()}>
                        {s.display_name}
                      </Link>
                      {s.is_turn && <span className="badge">turn</span>}
                    </div>
                    <div className="small faint mono truncate">
                      trace {s.trace_id.slice(0, 12)}
                      {s.conversation_id && (
                        <>
                          {' · '}
                          <Link to={conversationUrl(projectId, s.conversation_id)} onClick={(e) => e.stopPropagation()}>
                            {truncate(s.conversation_name || s.conversation_id, 24)}
                          </Link>
                        </>
                      )}
                    </div>
                  </td>
                  <td className="mono small">{s.operation_name ?? <span className="faint">—</span>}</td>
                  {!agent && (
                    <td className="small" style={{ whiteSpace: 'nowrap' }}>
                      {s.agent_name ? (
                        <Link to={agentUrl(projectId, s.agent_name)} onClick={(e) => e.stopPropagation()}>
                          {s.agent_name}
                        </Link>
                      ) : (
                        <span className="faint">—</span>
                      )}
                    </td>
                  )}
                  <td className="mono small truncate" style={{ maxWidth: 180 }}>
                    {s.tool_name ?? s.request_model ?? <span className="faint">—</span>}
                  </td>
                  <td style={{ maxWidth: 260 }}>
                    <SignalsCell rs={sigBySpan.get(s.span_id)} />
                  </td>
                  <td className="num">{s.input_tokens != null ? compact(s.input_tokens) : <span className="faint">—</span>}</td>
                  <td className="num">{s.output_tokens != null ? compact(s.output_tokens) : <span className="faint">—</span>}</td>
                  <td className="num">{s.total_cost_usd ? fmtCost(s.total_cost_usd) : <span className="faint">—</span>}</td>
                  <td className="num">{fmtDuration(s.duration_ms)}</td>
                  <td className="small mono">{s.finish_reasons?.length ? s.finish_reasons.join(', ') : <span className="faint">—</span>}</td>
                  <td>
                    {s.status_code === 'ERROR' ? (
                      <span className="badge error" title={s.status_message ?? undefined}>
                        {s.error_type || 'error'}
                      </span>
                    ) : (
                      <span className="small muted">{s.status_code === 'OK' ? 'ok' : 'unset'}</span>
                    )}
                  </td>
                  <td className="small" title={fmtDate(s.started_at)} style={{ whiteSpace: 'nowrap' }}>
                    {fmtRelative(s.started_at)}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {(q.data?.total_count ?? 0) > LIMIT && (
        <div className="row" style={{ justifyContent: 'flex-end' }}>
          <Pager offset={offset} limit={LIMIT} total={q.data?.total_count ?? null} onChange={(o) => update({ offset: o ? String(o) : null })} />
        </div>
      )}
    </div>
  )
}

function GroupTable({
  projectId,
  groups,
  groupKey,
  sort,
  setSort,
  onDrill,
  allTime,
  predsByConv,
}: {
  projectId: string
  groups: SpanGroup[]
  groupKey: string
  sort: { col: string; direction: string }
  setSort: (c: string) => void
  onDrill: (g: SpanGroup) => void
  allTime?: boolean
  predsByConv?: Map<string, AgentEvalPrediction[]>
}) {
  const showPreds = !!predsByConv && groupKey === 'conversation_id'
  if (!groups.length) return <Empty>No spans to group{allTime ? '' : ' in this period'}.</Empty>
  const label = (g: SpanGroup) => {
    const v = Object.values(g.group_keys)[0]
    return v == null || v === '' ? '(none)' : String(v)
  }
  return (
    <div className="table-wrap">
      <table className="table">
        <thead>
          <tr>
            <th>{groupKey}</th>
            {showPreds && <th>Prediction · scores</th>}
            <SortTh id="spans" label="Spans" sort={sort} setSort={setSort} num />
            <SortTh id="errors" label="Errors" sort={sort} setSort={setSort} num />
            <th>Agents</th>
            <SortTh id="in" label="In" sort={sort} setSort={setSort} num />
            <SortTh id="out" label="Out" sort={sort} setSort={setSort} num />
            <SortTh id="cost" label="Cost" sort={sort} setSort={setSort} num />
            <SortTh id="p50" label="p50" sort={sort} setSort={setSort} num />
            <SortTh id="p95" label="p95" sort={sort} setSort={setSort} num />
            <SortTh id="last" label="Last seen" sort={sort} setSort={setSort} />
          </tr>
        </thead>
        <tbody>
          {groups.map((g, i) => (
            <Fragment key={i}>
              <tr className="clickable" onClick={() => onDrill(g)} title="Filter the span list to this group">
                <td className="mono small" style={{ maxWidth: 300 }}>
                  <div className="truncate">
                    {groupKey === 'conversation_id' && g.group_keys[Object.keys(g.group_keys)[0]] ? (
                      <Link to={conversationUrl(projectId, label(g))} onClick={(e) => e.stopPropagation()}>
                        {g.conversation_names[0] || label(g)}
                      </Link>
                    ) : (
                      label(g)
                    )}
                  </div>
                  {g.first_message && <div className="small muted truncate">{truncate(g.first_message.text, 100)}</div>}
                </td>
                {showPreds && (
                  <td style={{ minWidth: 180, maxWidth: 320 }} onClick={(e) => e.stopPropagation()}>
                    {(predsByConv!.get(label(g)) ?? []).map((p) => (
                      <div key={p.predict_and_score_call_id} className="col" style={{ gap: 2 }}>
                        <Link to={callUrl(projectId, p.predict_and_score_call_id)} className="small mono" title="predict_and_score call">
                          {p.example_id ? `example ${p.example_id}` : `prediction ${p.predict_and_score_call_id.slice(-8)}`}
                        </Link>
                        <ScoreChips scores={p.scores} max={4} />
                      </div>
                    ))}
                  </td>
                )}
                <td className="num">{fmtNumber(g.span_count)}</td>
                <td className="num" style={g.error_count ? { color: 'var(--error)' } : undefined}>
                  {fmtNumber(g.error_count)} <span className="faint small">({fmtPct(errorRate(g.error_count, g.span_count))})</span>
                </td>
                <td className="small truncate" style={{ maxWidth: 160 }}>
                  {g.agent_names.join(', ') || <span className="faint">—</span>}
                </td>
                <td className="num">{compact(g.total_input_tokens)}</td>
                <td className="num">{compact(g.total_output_tokens)}</td>
                <td className="num">{fmtCost(g.total_cost_usd)}</td>
                <td className="num">{fmtDuration(g.latency_ms_p50)}</td>
                <td className="num">{fmtDuration(g.latency_ms_p95)}</td>
                <td className="small" title={fmtDate(g.last_seen)}>
                  {fmtRelative(g.last_seen)}
                </td>
              </tr>
            </Fragment>
          ))}
        </tbody>
      </table>
    </div>
  )
}
