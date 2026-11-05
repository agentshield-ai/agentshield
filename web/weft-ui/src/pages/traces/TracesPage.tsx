import { normaliseSpansFilter } from '../../api/filters'
import { cloneElement, createContext, useCallback, useContext, useEffect, useLayoutEffect, useMemo, useRef, useState, type ReactElement } from 'react'
import { Link, useNavigate, useSearchParams } from 'react-router-dom'
import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '../../api/client'
import { isRef, parseRef, spanDisplayName } from '../../api/refs'
import type { Any, Span, SpansFilter, Operation, Query, SortBy } from '../../api/types'
import { useProjectId } from '../../hooks/useProject'
import { callCost, callLatencyMs, callStatus, callTokens, fmtCost, fmtDate, fmtDuration, fmtNumber, fmtRelative, preview } from '../../lib/format'
import { spanUrl, compareSpansUrl, spanSessionsUrl } from '../../lib/urls'
import { ErrorBox, Pager, Spinner } from '../../components/common'
import { CallCharts } from '../../components/traces/CallCharts'
import { extractChat, messageText, normaliseMessage } from '../../components/traces/ChatView'
import { BASE_FILTER_FIELDS, clauseToOperation, clauseValid, decodeClauses, encodeClauses, type FilterClause } from '../../components/traces/FilterBuilder'
import { AddToDatasetModal } from '../../components/traces/AddToDatasetModal'
import { ConfirmModal, Dropdown, MenuItem } from '../../components/traces/Menu'
import { GuidedEmpty } from '../../components/ui/EmptyState'
import { callLabel, discoverFeedbackFields, discoverFields, exportCalls, getDotted, reactionCounts } from '../../components/traces/callUtils'
import { CopyAsCodeModal, ServerExportModal } from '../../components/traces/ExportModal'
import { AddToDashboardModal } from '../../components/dashboards/AddToDashboardModal'
import { AddToQueueButton } from '../../components/queues/AddToQueueModal'
import '../../components/traces/columns.css'
import { FilterBar, FilterButton, FilterSearchField, ScopeCapNote, useWidenedTime, WidenedNote } from '../../components/ui/FilterBar'
import { FILTER_PARAMS, isEmptyFilter, parseFilters, STATUS_LABEL as RUN_STATUS_LABEL, timeWindow, type FilterState, type StatusFilter, type TimeRange } from '../../lib/filters'
import { useTraceScope } from '../../hooks/useTraceScope'
import { decodeIds, encodeIds, layoutColumns, moveBefore, moveStep, pinnedOffsets, togglePin, type ColumnSlot } from '../../components/traces/columnLayout'
import { PageHeader } from '../../components/ui/PageHeader'
import { PlainTable } from '../../components/ui/PlainTable'
import { Segmented } from '../../components/ui/Segmented'
import { Sentence, type SentencePart } from '../../components/ui/Sentence'
import { StatusGlyph } from '../../components/ui/StatusGlyph'
import { cellValueWords, feedbackWords, scoreSummaryWords, SPAN_SESSIONS_TITLE, statusGlyph, tracesSentence } from '../../lib/summaries/traces'
import '../observe.css'

// ---------------------------------------------------------------- URL state

const BASE_COLS = ['status', 'name', 'started', 'latency', 'tokens', 'cost', 'feedback', 'inputs', 'output'] as const
type BaseCol = (typeof BASE_COLS)[number]
const BASE_COL_LABEL: Record<BaseCol, string> = {
  status: 'Status',
  name: 'Name',
  started: 'Started',
  latency: 'Latency',
  tokens: 'Tokens',
  cost: 'Cost',
  feedback: 'Feedback',
  inputs: 'Inputs',
  output: 'Output',
}
const SORT_FIELD: Partial<Record<BaseCol, string>> = {
  started: 'started_at',
  latency: 'summary.weft.latency_ms',
  tokens: 'summary.total_tokens',
  name: 'name',
}
const PAGE_SIZES = [25, 50, 100, 200]
/** The shared status chip's values (filters.ts StatusFilter). */
const STATUSES = ['ok', 'error', 'failed_step', 'running', 'unknown'] as const
const DEFAULT_TIME: TimeRange = '24h'
/** Columns hidden until chosen (DESIGN.md §7.1 rule 6: few columns by default). `hide=none` shows them all. */
export const DEFAULT_HIDDEN: BaseCol[] = ['tokens', 'cost', 'feedback']
const RANGES: { id: TimeRange; label: string }[] = [
  { id: '24h', label: '24 hours' },
  { id: '7d', label: '7 days' },
  { id: '30d', label: '30 days' },
]

/** The `hide` parameter for a set of hidden columns (null when it is the default). */
export function hideParam(hidden: Set<string>): string | null {
  const xs = [...hidden].sort()
  if (xs.join(',') === [...DEFAULT_HIDDEN].sort().join(',')) return null
  return xs.length ? xs.join(',') : 'none'
}
/** URL parameters a saved Traces view keeps besides the shared filters. */
const VIEW_KEYS = ['roots', 'system', 'replay', 'op', 'f', 'expr', 'sort', 'cols', 'order', 'pin', 'hide', 'limit', 'charts']

interface ViewState {
  roots: boolean
  system: boolean
  /** Include AgentShield replay evaluations (hidden by default, like on Sessions). */
  replay: boolean
  op: string
  status: string
  q: string
  clauses: FilterClause[]
  expr: Operation | null
  sort: SortBy
  cols: string[]
  /** Column display order and pinned (sticky) columns, by column id. */
  order: string[]
  pin: string[]
  hidden: Set<string>
  offset: number
  limit: number
  charts: boolean
  live: boolean
  view: string
}

export function parseState(p: URLSearchParams): ViewState {
  const [sf, sd] = (p.get('sort') ?? 'started_at:desc').split(':')
  let expr: Operation | null = null
  try {
    expr = p.get('expr') ? (JSON.parse(p.get('expr')!) as Operation) : null
  } catch {
    expr = null
  }
  return {
    roots: p.get('roots') !== '0',
    system: p.get('system') === '1',
    replay: p.get('replay') === '1',
    op: p.get('op') ?? '',
    // The shared status chip means the same as on Sessions (filters.ts
    // STATUS_HELP), read off each row's run status (statusOp).
    status: traceStatus(p.get('status')),
    q: p.get('q') ?? '',
    clauses: decodeClauses(p.get('f')),
    expr,
    sort: { field: sf || 'started_at', direction: sd === 'asc' ? 'asc' : 'desc' },
    cols: (p.get('cols') ?? '').split(',').filter(Boolean),
    order: decodeIds(p.get('order')),
    pin: decodeIds(p.get('pin')),
    hidden: p.get('hide') == null ? new Set(DEFAULT_HIDDEN) : new Set((p.get('hide') ?? '').split(',').filter((c) => c && c !== 'none')),
    offset: Math.max(0, Number(p.get('offset') ?? 0) || 0),
    limit: PAGE_SIZES.includes(Number(p.get('limit'))) ? Number(p.get('limit')) : 50,
    // Charts open on request (older links said charts=0 to hide them).
    charts: p.get('charts') === '1',
    live: p.get('live') === '1',
    view: p.get('view') ?? '',
  }
}

/** A span attribute, whether it came over OTLP (flat dotted keys) or from an SDK (nested). */
function attrEq(key: string, value: string): Operation {
  return {
    $or: [
      { $eq: [{ $getField: `attributes.otel.attributes.[${key}]` }, { $literal: value }] },
      { $eq: [{ $getField: `attributes.${key}` }, { $literal: value }] },
    ],
  }
}

/**
 * The shared filter bar's chips as span filters: agent, model and tool on
 * each span (when listing spans; a trace matches them through any of its
 * spans, see useTraceScope) and the time range.
 */
export function sharedOps(f: FilterState, time: TimeRange = 'all', perSpan = true): Operation[] {
  const ops: Operation[] = []
  if (perSpan && f.agent) ops.push(attrEq('gen_ai.agent.name', f.agent))
  if (perSpan && f.model) ops.push(attrEq('gen_ai.request.model', f.model))
  if (perSpan && f.tool) ops.push(attrEq('gen_ai.tool.name', f.tool))
  const w = timeWindow(f, time)
  if (w.from) ops.push({ $gte: [{ $getField: 'started_at' }, { $literal: w.from }] })
  if (w.to) ops.push({ $lt: [{ $getField: 'started_at' }, { $literal: w.to }] })
  return ops
}

/** The shared status chip value in a Traces URL, or '' for none (span status names from older links are accepted). */
export function traceStatus(v: string | null): string {
  if (v === 'success') return 'ok'
  if (v === 'descendant_error') return 'failed_step'
  return (STATUSES as readonly string[]).includes(v ?? '') ? v! : ''
}

/**
 * The status chip on Traces keeps the rows whose run status
 * (summary.weft.run_status, computed by the server as on Sessions) is the
 * chip's value. A trace's run status is read off its top-level span, agent
 * span or not: Failed is that span failing, except that a sub-agent trace
 * linked to its caller (span link, parent call link or hand-off) that
 * failed is a failed step of the caller's run; "went on after a failed
 * step" is a top-level span that ended without an error over a failed
 * span. AgentShield verdict records (the engine's evaluate traces) are not
 * runs: they take their session's status, "No agent data" when the session
 * is known only from verdicts. So the same chip keeps the same runs on
 * Sessions and Traces.
 */
export function statusOp(status: string): Operation {
  return { $eq: [{ $getField: 'summary.weft.run_status' }, { $literal: status }] }
}

/** Never matches a trace id; stands for "no trace matched the session filters". */
const NO_TRACE = 'no-matching-trace'

export function buildRequest(
  s: ViewState,
  shared: FilterState = {},
  time: TimeRange = 'all',
  traceIds: string[] | null = null,
): { filter: SpansFilter; query?: Query; search?: string } {
  const filter: SpansFilter = {}
  if (s.roots) filter.trace_roots_only = true
  if (traceIds) filter.trace_ids = traceIds.length ? traceIds : [NO_TRACE]
  if (s.op) filter.names = [s.op]
  const ops: Operation[] = []
  // Monitor scorers and server-side LLM calls are tagged internal; hide them unless asked.
  if (!s.system) ops.push({ $not: [{ $eq: [{ $getField: 'attributes.weft.internal' }, { $literal: true }] }] })
  // AgentShield's evaluations of replayed tool calls (whole engine traces) are not agent activity; hidden unless asked.
  if (!s.replay) ops.push({ $not: [{ $eq: [{ $getField: 'summary.weft.replay' }, { $literal: true }] }] })
  if (s.status) ops.push(statusOp(s.status))
  for (const c of s.clauses) if (clauseValid(c)) ops.push(clauseToOperation(c))
  ops.push(...sharedOps(shared, time, !s.roots))
  if (s.expr) ops.push(s.expr)
  const query = ops.length ? { $expr: ops.length === 1 ? ops[0] : { $and: ops } } : undefined
  return { filter, query, search: s.q.trim() || undefined }
}

// ---------------------------------------------------------------- cell helpers

type VersionOf = (name: string, digest: string) => number | null | undefined

/** The versions of the objects the listed rows refer to, so a ref reads "polite-echo, version 1". */
const RefVersions = createContext<VersionOf>(() => null)

function inputsPreview(c: Span, versionOf?: VersionOf): string {
  const chat = extractChat(c)
  if (chat) {
    const users = chat.input.filter((m) => m?.role === 'user')
    const last = users.length ? users[users.length - 1] : chat.input[chat.input.length - 1]
    return messageText(normaliseMessage(last)) || preview(last)
  }
  if (!c.inputs || typeof c.inputs !== 'object') return cellValueWords(c.inputs, versionOf, 120)
  return Object.entries(c.inputs)
    .filter(([k]) => k !== 'self')
    .map(([k, v]) => `${k}: ${cellValueWords(v, versionOf)}`)
    .join(' · ')
}

function outputPreview(c: Span, versionOf?: VersionOf): string {
  const chat = extractChat(c)
  if (chat?.output?.length) {
    const m = normaliseMessage(chat.output[chat.output.length - 1])
    const text = messageText(m)
    if (text) return text
    const tools = m.parts.filter((p) => p.type === 'tool_call').map((p) => (p as { name: string }).name)
    if (tools.length) return `→ ${tools.join(', ')}()`
  }
  const o = c.output
  // An evaluation's result: its scores in words ("exact match 0% · …").
  const scores = scoreSummaryWords(o)
  if (scores) return scores
  if (o && typeof o === 'object' && !Array.isArray(o))
    return Object.entries(o)
      .map(([k, v]) => `${k}: ${cellValueWords(v, versionOf)}`)
      .join(' · ')
  return cellValueWords(o, versionOf, 120)
}

/** Object names a row's inputs and output refer to (top-level values only). */
function refNamesOf(rows: Span[]): string[] {
  const out = new Set<string>()
  const add = (v: unknown) => {
    if (!isRef(v)) return
    const p = parseRef(v)
    if (p && (p.kind === 'object' || p.kind === 'function') && p.digest) out.add(p.name)
  }
  for (const c of rows) {
    for (const v of [c.inputs, c.output]) {
      if (v && typeof v === 'object' && !Array.isArray(v)) Object.values(v).forEach(add)
      else add(v)
    }
  }
  return [...out].sort().slice(0, 50)
}

const plural = (n: number, w: string) => `${fmtNumber(n)} ${w}${n === 1 ? '' : 's'}`

function colLabel(path: string) {
  return path
}

// ---------------------------------------------------------------- saved views

interface SavedViewDef {
  filter?: SpansFilter
  query?: Query
  sort_by?: SortBy[]
  cols?: string[]
  /** Column order and pinned columns (column ids: base names or field paths). */
  col_order?: string[]
  pinned_cols?: string[]
  search?: string
  ui_params?: Record<string, string>
}

/** URL parameters of a Traces view saved before views moved into the filter bar. */
export function legacyTraceView(o: Any): Record<string, string> | null {
  const t = o.value?.view_type
  if (t !== undefined && t !== 'traces') return null
  const def: SavedViewDef = o.value?.definition ?? {}
  // Views saved before Traces had a time range showed all time.
  if (def.ui_params) return def.ui_params.time || def.ui_params.from || def.ui_params.to ? def.ui_params : { ...def.ui_params, time: 'all' }
  const p: Record<string, string> = {}
  const filter = normaliseSpansFilter(def.filter)
  if (filter.trace_roots_only === false) p.roots = '0'
  if (filter.names?.[0]) p.op = filter.names[0]
  if (def.sort_by?.[0]) p.sort = `${def.sort_by[0].field}:${def.sort_by[0].direction}`
  if (def.cols?.length) p.cols = def.cols.join(',')
  if (def.col_order?.length) p.order = encodeIds(def.col_order)
  if (def.pinned_cols?.length) p.pin = encodeIds(def.pinned_cols)
  if (def.search) p.q = def.search
  if (def.query?.$expr) p.expr = JSON.stringify(def.query.$expr)
  p.time = 'all'
  return p
}

// ---------------------------------------------------------------- page

export function TracesPage() {
  const projectId = useProjectId()
  const navigate = useNavigate()
  const qc = useQueryClient()
  const [params, setParams] = useSearchParams()
  const s = useMemo(() => parseState(params), [params])
  const shared = useMemo(() => parseFilters(params), [params])
  // Last 24 hours by default, widening when that is empty (DESIGN.md §7.1 rule 6).
  const widen = useWidenedTime(shared, DEFAULT_TIME, async (r) => (await api.spansCount({ project_id: projectId, ...buildRequest(s, { ...shared, time: r }, r) })), [
    projectId,
    'traces',
    JSON.stringify(buildRequest(s, { ...shared, time: 'all' }, 'all')),
  ])
  const time = widen.time
  // Team, security, score and cost through sessions; agent, model and tool through any span of the trace.
  const scope = useTraceScope(projectId, shared, time, s.roots, widen.ready)
  const req = useMemo(() => buildRequest(s, shared, time, scope.traceIds), [s, shared, time, scope.traceIds])
  const ready = widen.ready && scope.ready

  const update = useCallback(
    (patch: Record<string, string | null>, resetOffset = true) => {
      setParams(
        (prev) => {
          const next = new URLSearchParams(prev)
          for (const [k, v] of Object.entries(patch)) {
            if (v == null || v === '') next.delete(k)
            else next.set(k, v)
          }
          if (resetOffset && !('offset' in patch)) next.delete('offset')
          return next
        },
        { replace: true },
      )
    },
    [setParams],
  )


  const refetchInterval = s.live ? 5000 : false
  const sortBy: SortBy[] = [s.sort]
  const calls = useQuery({
    queryKey: ['spans', projectId, 'table', req, s.sort, s.offset, s.limit],
    queryFn: () =>
      api.spansQuery({
        project_id: projectId,
        ...req,
        sort_by: sortBy,
        limit: s.limit,
        offset: s.offset,
        include_costs: true,
        include_feedback: true,
        include_run_status: true,
      }),
    enabled: ready,
    placeholderData: keepPreviousData,
    refetchInterval,
  })
  const count = useQuery({
    queryKey: ['spans', projectId, 'count', req],
    queryFn: () => api.spansCount({ project_id: projectId, ...req }),
    enabled: ready,
    placeholderData: keepPreviousData,
    refetchInterval,
  })
  const spanNames = useQuery({ queryKey: ['spans', projectId, 'op_counts', s.roots], queryFn: () => api.spanNames(projectId, s.roots) })
  // op_counts may list several versions of one op; the filter works on base names.
  const opOptions = useMemo(() => {
    const m = new Map<string, number>()
    for (const o of spanNames.data?.items ?? []) m.set(o.base_name, (m.get(o.base_name) ?? 0) + o.count)
    return [...m.entries()].sort((a, b) => b[1] - a[1])
  }, [spanNames.data])

  const rows = calls.data?.items ?? []
  const total = count.data ?? null
  // The versions of objects the rows refer to: one metadata query for every listed name.
  const refNames = useMemo(() => refNamesOf(calls.data?.items ?? []), [calls.data])
  const refObjects = useQuery({
    queryKey: ['objects', projectId, 'ref-versions', refNames],
    queryFn: () => api.objectsQuery({ project_id: projectId, filter: { names: refNames }, metadata_only: true, limit: 1000 }),
    enabled: refNames.length > 0,
    staleTime: 60_000,
  })
  const versionOf = useMemo<VersionOf>(() => {
    const m = new Map((refObjects.data?.items ?? []).map((o) => [`${o.name}:${o.digest}`, o.version]))
    return (name, digest) => m.get(`${name}:${digest}`)
  }, [refObjects.data])

  // Field discovery for the filter builder and column picker. Accumulate across pages.
  const [seenFields, setSeenFields] = useState<string[]>([])
  useEffect(() => {
    if (!rows.length) return
    const found = [...discoverFields(rows, ['inputs', 'output', 'attributes']), ...discoverFeedbackFields(rows)]
    setSeenFields((prev) => {
      const set = new Set(prev)
      let changed = false
      for (const f of found)
        if (!set.has(f)) {
          set.add(f)
          changed = true
        }
      return changed ? [...set].sort() : prev
    })
  }, [rows])
  const columnCandidates = seenFields.filter((f) => f.startsWith('inputs.') || f.startsWith('output.'))
  const filterFields = useMemo(() => [...BASE_FILTER_FIELDS, ...seenFields], [seenFields])

  // Selection survives paging; keep the Span objects for actions.
  const [selected, setSelected] = useState<Map<string, Span>>(new Map())
  const toggleSel = (c: Span) =>
    setSelected((prev) => {
      const n = new Map(prev)
      if (n.has(c.id)) n.delete(c.id)
      else n.set(c.id, c)
      return n
    })
  const allOnPage = rows.length > 0 && rows.every((r) => selected.has(r.id))
  const togglePage = () =>
    setSelected((prev) => {
      const n = new Map(prev)
      if (allOnPage) rows.forEach((r) => n.delete(r.id))
      else rows.forEach((r) => n.set(r.id, r))
      return n
    })

  const [modal, setModal] = useState<null | 'delete' | 'dataset'>(null)
  // Server export, copy-as-code and add-to-dashboard modals.
  const [extraModal, setExtraModal] = useState<null | 'serverExport' | 'code' | 'chart'>(null)
  const server = useQuery({ queryKey: ['server-info'], queryFn: api.server, staleTime: 60_000 })
  const del = useMutation({
    mutationFn: () => api.spansDelete(projectId, [...selected.keys()]),
    onSuccess: () => {
      setSelected(new Map())
      setModal(null)
      void qc.invalidateQueries({ queryKey: ['spans', projectId] })
      void qc.invalidateQueries({ queryKey: ['calls-stats', projectId] })
    },
  })

  // Keyboard navigation
  const [cursor, setCursor] = useState(-1)
  const reqKey = JSON.stringify([req, s.sort, s.offset, s.limit])
  useEffect(() => setCursor(-1), [reqKey])
  useEffect(() => {
    const h = (e: KeyboardEvent) => {
      const t = e.target as HTMLElement
      if (t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.tagName === 'SELECT' || t.isContentEditable)) return
      if (e.metaKey || e.ctrlKey || e.altKey || document.querySelector('.modal')) return
      if (e.key === 'j' || e.key === 'ArrowDown') {
        e.preventDefault()
        setCursor((c) => Math.min(rows.length - 1, c + 1))
      } else if (e.key === 'k' || e.key === 'ArrowUp') {
        e.preventDefault()
        setCursor((c) => Math.max(0, c - 1))
      } else if (e.key === 'Enter' && cursor >= 0 && rows[cursor]) {
        navigate(spanUrl(projectId, rows[cursor].id))
      } else if (e.key === 'x' && cursor >= 0 && rows[cursor]) {
        toggleSel(rows[cursor])
      }
    }
    window.addEventListener('keydown', h)
    return () => window.removeEventListener('keydown', h)
  })
  useEffect(() => {
    if (cursor >= 0) document.querySelector(`[data-row="${cursor}"]`)?.scrollIntoView({ block: 'nearest' })
  }, [cursor])

  const visibleBase = BASE_COLS.filter((c) => !s.hidden.has(c))
  // Column order and pinning: pinned columns stick to the left while scrolling.
  const slots = useMemo(() => layoutColumns([...visibleBase, ...s.cols], s.order, s.pin), [visibleBase.join(','), s.cols, s.order, s.pin]) // eslint-disable-line react-hooks/exhaustive-deps
  const slotIds = slots.map((c) => c.id)
  const setOrder = (ids: string[]) => update({ order: encodeIds(ids) || null }, false)
  const setPin = (ids: string[]) => update({ pin: encodeIds(ids) || null }, false)
  const headRef = useRef<HTMLTableRowElement>(null)
  const [colWidths, setColWidths] = useState<Record<string, number>>({})
  useLayoutEffect(() => {
    if (!s.pin.length || !headRef.current) return
    const w: Record<string, number> = {}
    headRef.current.querySelectorAll<HTMLElement>('th[data-col]').forEach((th) => (w[th.dataset.col!] = th.offsetWidth))
    if (JSON.stringify(w) !== JSON.stringify(colWidths)) setColWidths(w)
  })
  const leadWidth = 28
  const offsets = pinnedOffsets(slots, colWidths, leadWidth)
  const [dragCol, setDragCol] = useState<string | null>(null)
  const colHeadProps = (slot: ColumnSlot) => ({
    'data-col': slot.id,
    draggable: true,
    onDragStart: (e: React.DragEvent) => {
      setDragCol(slot.id)
      e.dataTransfer.effectAllowed = 'move'
      e.dataTransfer.setData('text/plain', slot.id)
    },
    onDragOver: (e: React.DragEvent) => {
      if (dragCol && dragCol !== slot.id) e.preventDefault()
    },
    onDrop: (e: React.DragEvent) => {
      e.preventDefault()
      if (!dragCol) return
      // Dropping onto a pinned column pins the dragged one too, and vice versa.
      if (slot.pinned !== s.pin.includes(dragCol)) setPin(togglePin(s.pin, dragCol))
      setOrder(moveBefore(slotIds, dragCol, slot.id))
      setDragCol(null)
    },
    onDragEnd: () => setDragCol(null),
    className: `${slot.pinned ? 'col-pinned' : ''} ${dragCol === slot.id ? 'col-dragging' : ''}`,
    style: slot.pinned ? { left: offsets[slot.id] ?? 0 } : undefined,
    title: 'Drag to reorder',
  })
  const setSort = (field: string) => {
    const dir = s.sort.field === field && s.sort.direction === 'desc' ? 'asc' : 'desc'
    update({ sort: field === 'started_at' && dir === 'desc' ? null : `${field}:${dir}` })
  }
  const sortArrow = (field: string) => (s.sort.field === field ? (s.sort.direction === 'desc' ? ' ↓' : ' ↑') : '')

  // Saved views also store the span query, for readers of the API.
  const viewDefinition = () => ({ filter: req.filter, query: req.query, sort_by: sortBy, cols: s.cols, search: req.search })
  const hasFilters = !!(s.op || s.status || s.q || s.clauses.length || s.expr || !s.roots || s.replay || FILTER_PARAMS.some((k) => params.get(k)))

  const selCalls = [...selected.values()]
  const exportTargets = selCalls.length ? selCalls : rows

  // The sentence: how many traces (or spans), and how many of them failed.
  const failedReq = useMemo(() => (s.status ? null : buildRequest({ ...s, status: 'error' }, shared, time, scope.traceIds)), [s, shared, time, scope.traceIds])
  const failed = useQuery({
    queryKey: ['spans', projectId, 'count', 'failed', failedReq],
    queryFn: () => api.spansCount({ project_id: projectId, ...failedReq! }),
    enabled: ready && !!failedReq,
    placeholderData: keepPreviousData,
    refetchInterval,
  })
  const [filterOpen, setFilterOpen] = useState(false)
  const filtered = !!(s.op || s.status || s.q || s.clauses.length || s.expr) || !isEmptyFilter({ ...shared, time: undefined, from: undefined, to: undefined }, DEFAULT_TIME)
  const statusLink = (st: StatusFilter) => {
    const n = new URLSearchParams(params)
    n.set('status', st)
    n.delete('offset')
    return `?${n.toString()}`
  }
  const sentence: SentencePart[] | null =
    total != null && (!failedReq || failed.data != null)
      ? tracesSentence({ total, failed: failedReq ? (failed.data ?? 0) : null, roots: s.roots, time, filtered }).map((p) =>
          typeof p === 'string' ? p : { text: p.text, to: statusLink(p.status), title: 'Show only these' },
        )
      : null
  const toggleColumn = (c: string) => {
    const h = new Set(s.hidden)
    if (h.has(c)) h.delete(c)
    else h.add(c)
    update({ hide: hideParam(h) }, false)
  }

  // Nothing recorded at all: the guided start, not an empty table under every control.
  const empty = ready && !calls.isLoading && rows.length === 0 && !hasFilters && total === 0
  const viewChanged = hasFilters || !!s.view || s.cols.length > 0 || params.has('hide') || params.has('order') || params.has('pin')
  const check = (label: string, checked: boolean, onChange: (v: boolean) => void, title?: string) => (
    <label className="dd-check" title={title}>
      <input type="checkbox" checked={checked} onChange={(e) => onChange(e.target.checked)} /> {label}
    </label>
  )
  // One menu for what the table lists and how: rows, columns, charts, refresh, export and the page's other views.
  const viewMenu = (
    <Dropdown kind="dialog" label="View" ariaLabel="View options" align="right" className="qbtn">
      {(close) => (
        <div className="obs-view-menu">
          <div className="dd-section">Rows</div>
          {check('Top-level spans only', s.roots, (v) => update({ roots: v ? null : '0' }), 'Only show the top-level span of each trace')}
          {check('System spans', s.system, (v) => update({ system: v ? '1' : null }), 'Include scorer spans made by monitors and other internal server spans')}
          {check('Replay evaluations', s.replay, (v) => update({ replay: v ? '1' : null }), 'Include the engine traces of AgentShield replays (re-evaluations of recorded tool calls)')}
          <div className="dd-section">Show</div>
          {check('Charts', s.charts, (v) => update({ charts: v ? '1' : null }, false))}
          {check('Refresh every 5 seconds', s.live, (v) => update({ live: v ? '1' : null }, false))}
          <div className="dd-section">Columns</div>
          {BASE_COLS.map((c) => (
            <label key={c} className="dd-check">
              <input type="checkbox" checked={!s.hidden.has(c)} onChange={() => toggleColumn(c)} />
              {BASE_COL_LABEL[c]}
            </label>
          ))}
          {columnCandidates.length + s.cols.length > 0 && <div className="dd-note">Fields from the loaded rows</div>}
          <div style={{ maxHeight: 200, overflow: 'auto' }}>
            {[...new Set([...s.cols, ...columnCandidates])].map((f) => (
              <label key={f} className="dd-check small">
                <input
                  type="checkbox"
                  checked={s.cols.includes(f)}
                  onChange={() => update({ cols: (s.cols.includes(f) ? s.cols.filter((x) => x !== f) : [...s.cols, f]).join(',') || null }, false)}
                />
                {f}
              </label>
            ))}
          </div>
          <details className="dd-details">
            <summary>Order and pinning</summary>
            <div style={{ maxHeight: 240, overflow: 'auto' }}>
              {slots.map((c, i) => (
                <div key={c.id} className="dd-check col-order-row">
                  <span className="grow truncate" title={c.id}>
                    {(BASE_COL_LABEL as Record<string, string>)[c.id] || c.id}
                  </span>
                  <button className="btn ghost sm icon" aria-label={`Move ${c.id} left`} disabled={i === 0} onClick={() => setOrder(moveStep(slotIds, c.id, -1))}>
                    ↑
                  </button>
                  <button className="btn ghost sm icon" aria-label={`Move ${c.id} right`} disabled={i === slots.length - 1} onClick={() => setOrder(moveStep(slotIds, c.id, 1))}>
                    ↓
                  </button>
                  <button className={`btn sm ${c.pinned ? 'primary' : 'ghost'}`} aria-pressed={c.pinned} aria-label={`${c.pinned ? 'Unpin' : 'Pin'} ${c.id}`} onClick={() => setPin(togglePin(s.pin, c.id))}>
                    {c.pinned ? 'Pinned' : 'Pin'}
                  </button>
                </div>
              ))}
            </div>
            {(s.order.length > 0 || s.pin.length > 0) && (
              <button className="btn ghost sm" style={{ margin: '4px 8px' }} onClick={() => update({ order: null, pin: null }, false)}>
                Reset order and pins
              </button>
            )}
          </details>
          <div className="dd-section">{selected.size ? `Export the ${selected.size} selected` : 'Export this page'}</div>
          <div className="dd-row">
            {(['json', 'jsonl', 'csv'] as const).map((f) => (
              <button
                key={f}
                type="button"
                className="btn ghost sm"
                onClick={() => {
                  exportCalls(exportTargets, f, s.cols)
                  close()
                }}
              >
                {f.toUpperCase()}
              </button>
            ))}
          </div>
          <button
            type="button"
            className="dd-item"
            onClick={() => {
              setExtraModal('serverExport')
              close()
            }}
          >
            Export all matching rows…
          </button>
          <button
            type="button"
            className="dd-item"
            onClick={() => {
              setExtraModal('code')
              close()
            }}
          >
            Copy this query as code…
          </button>
          <div className="dd-sep" />
          <button
            type="button"
            className="dd-item"
            onClick={() => {
              void qc.invalidateQueries({ queryKey: ['spans', projectId] })
              close()
            }}
          >
            Refresh now
          </button>
          <button
            type="button"
            className="dd-item"
            onClick={() => {
              setExtraModal('chart')
              close()
            }}
          >
            Add a chart of these spans to a dashboard…
          </button>
          <Link className="dd-item" to={spanSessionsUrl(projectId)} onClick={close}>
            {SPAN_SESSIONS_TITLE}
          </Link>
          {viewChanged && (
            <button
              type="button"
              className="dd-item"
              onClick={() => {
                setParams(new URLSearchParams())
                close()
              }}
            >
              Reset filters, columns and sort
            </button>
          )}
          <div className="dd-note obs-keys">
            Keys: <kbd>j</kbd>/<kbd>k</kbd> move, <kbd>Enter</kbd> open, <kbd>x</kbd> select, <kbd>/</kbd> search
          </div>
        </div>
      )}
    </Dropdown>
  )

  return (
    <div className="page-frame obs-page obs-wide traces-page">
      <PageHeader
        title="Traces"
        help="traces.md"
        controls={
          empty ? undefined : (
          <>
            <Segmented
              label="Time range"
              options={RANGES}
              value={(RANGES.some((r) => r.id === time) ? time : null) as TimeRange | null}
              onChange={(t) => update({ time: t === DEFAULT_TIME && !widen.widened ? null : t, from: null, to: null })}
            />
            <FilterSearchField label="Search traces" />
            <FilterButton open={filterOpen} onToggle={() => setFilterOpen((o) => !o)} defaultTime={DEFAULT_TIME} />
            {viewMenu}
          </>
          )
        }
        sentence={
          empty ? undefined : sentence ? (
            <Sentence parts={sentence} />
          ) : (
            <p className="sentence s-outcome sk-sentence" aria-busy="true">
              <span className="sr-only">Counting traces…</span>
              <span className="sk sk-line" aria-hidden />
            </p>
          )
        }
      />

      {!empty && (
      <FilterBar
          page="traces"
          search="external"
          timePicker="none"
          collapsible
          open={filterOpen}
          defaultTime={DEFAULT_TIME}
          effectiveTime={time}
          viewKeys={VIEW_KEYS}
          viewDefinition={viewDefinition}
          legacyView={legacyTraceView}
          advanced={{ fields: filterFields, clauses: s.clauses, onChange: (cs) => update({ f: encodeClauses(cs) || null }) }}
        />
      )}
      {widen.widened && <WidenedNote time={time} defaultTime={DEFAULT_TIME} onReset={() => update({ time: DEFAULT_TIME })} />}
      <ErrorBox error={scope.error} />
      {scope.sessionsTruncated && <ScopeCapNote total={scope.sessionsTotal} />}

      {s.expr && (
        <div className="obs-toolbar" role="group" aria-label="Custom query">
          <span className="filter-chip" title={JSON.stringify(s.expr)}>
            <span className="filter-chip-label">Custom query</span>
            <button className="filter-chip-x" aria-label="Remove custom query" onClick={() => update({ expr: null })}>
              ✕
            </button>
          </span>
        </div>
      )}

      {s.charts && <CallCharts projectId={projectId} filter={req.filter} query={req.query} search={req.search} />}

      {selected.size > 0 && (
        <div className="obs-selection" role="toolbar" aria-label="Selection actions">
          <b>{selected.size} selected</b>
          <button type="button" className="linkish" onClick={() => setSelected(new Map())}>
            Clear
          </button>
          <span className="grow" />
          <button type="button" className="qbtn" disabled={selected.size < 2} title={selected.size < 2 ? 'Select at least two spans' : undefined} onClick={() => navigate(compareSpansUrl(projectId, [...selected.keys()]))}>
            Compare
          </button>
          <button type="button" className="qbtn" onClick={() => setModal('dataset')}>
            Add to dataset
          </button>
          <AddToQueueButton projectId={projectId} callIds={[...selected.keys()]} className="qbtn" />
          <Dropdown label="Export" align="right" className="qbtn">
            {(close) =>
              (['json', 'jsonl', 'csv'] as const).map((f) => (
                <MenuItem
                  key={f}
                  onClick={() => {
                    exportCalls(selCalls, f, s.cols)
                    close()
                  }}
                >
                  {f.toUpperCase()}
                </MenuItem>
              ))
            }
          </Dropdown>
          <button type="button" className="qbtn danger" onClick={() => setModal('delete')}>
            Delete
          </button>
        </div>
      )}

      <ErrorBox error={calls.error} />

      {calls.isLoading || !ready ? (
        <div className="quiet-empty">
          <Spinner label="Loading spans…" />
        </div>
      ) : rows.length === 0 ? (
        hasFilters ? (
          <div className="quiet-empty">
            <p>No spans match these filters.</p>
            <button type="button" className="qbtn" onClick={() => setParams(new URLSearchParams())}>
              Clear filters
            </button>
          </div>
        ) : (
          <GuidedEmpty projectId={projectId} title="No traces yet" doc="traces.md">
            Every span your agents and code send to <span className="mono">{projectId}</span> appears here, grouped into traces, with its inputs, output, timing and cost.
          </GuidedEmpty>
        )
      ) : (
        <PlainTable caption={`${s.roots ? 'Traces' : 'Spans'}, newest first`} className="traces-table">
          <thead>
            <tr ref={headRef}>
              <th scope="col" style={{ width: 28 }} className={s.pin.length ? 'col-pinned col-lead' : ''}>
                <input type="checkbox" checked={allOnPage} onChange={togglePage} aria-label="Select page" />
              </th>
              {slots.map((slot) => {
                if (!(BASE_COLS as readonly string[]).includes(slot.id)) {
                  const p = slot.id
                  const hp = colHeadProps(slot)
                  return (
                    <th scope="col" key={p} {...hp} className={`sortable mono ${hp.className}`} onClick={() => setSort(p)}>
                      {colLabel(p)}
                      {sortArrow(p)}
                      <button
                        className="th-x"
                        aria-label={`Remove column ${p}`}
                        onClick={(e) => {
                          e.stopPropagation()
                          update({ cols: s.cols.filter((x) => x !== p).join(',') || null }, false)
                        }}
                      >
                        ✕
                      </button>
                    </th>
                  )
                }
                const c = slot.id as BaseCol
                const hp = colHeadProps(slot)
                const sf = SORT_FIELD[c]
                const num = c === 'latency' || c === 'tokens' || c === 'cost'
                return (
                  <th
                    scope="col"
                    key={c}
                    {...hp}
                    className={`${sf ? 'sortable' : ''} ${num ? 'num' : ''} ${hp.className}`}
                    onClick={sf ? () => setSort(sf) : undefined}
                    aria-sort={sf && s.sort.field === sf ? (s.sort.direction === 'asc' ? 'ascending' : 'descending') : undefined}
                  >
                    {c === 'status' ? <span className="sr-only">Status</span> : BASE_COL_LABEL[c]}
                    {sf && sortArrow(sf)}
                  </th>
                )
              })}
            </tr>
          </thead>
          <RefVersions.Provider value={versionOf}>
          <tbody>
            {rows.map((c, i) => (
              <CallRow
                key={c.id}
                call={c}
                index={i}
                projectId={projectId}
                slots={slots}
                offsets={offsets}
                leadPinned={s.pin.length > 0}
                selected={selected.has(c.id)}
                focused={cursor === i}
                onToggle={() => toggleSel(c)}
                onOpen={() => navigate(spanUrl(projectId, c.id))}
                onFilterOp={(op) => update({ op })}
                provisionalRoot={s.roots && !!c.parent_id}
              />
            ))}
          </tbody>
          </RefVersions.Provider>
        </PlainTable>
      )}

      {rows.length > 0 && (
        <div className="obs-foot">
          <span className="grow" />
          <div className="row">
            <select className="input" value={s.limit} onChange={(e) => update({ limit: e.target.value === '50' ? null : e.target.value })} aria-label="Page size">
              {PAGE_SIZES.map((n) => (
                <option key={n} value={n}>
                  {n} / page
                </option>
              ))}
            </select>
            <Pager offset={s.offset} limit={s.limit} total={total ?? (rows.length < s.limit ? s.offset + rows.length : null)} onChange={(o) => update({ offset: o ? String(o) : null }, false)} />
          </div>
        </div>
      )}

      {modal === 'delete' && (
        <ConfirmModal
          title={`Delete ${selected.size} span${selected.size === 1 ? '' : 's'}?`}
          body={<>The selected spans and all of their descendants will be deleted. This cannot be undone from the UI.</>}
          confirmLabel="Delete"
          danger
          busy={del.isPending}
          error={del.error}
          onConfirm={() => del.mutate()}
          onClose={() => setModal(null)}
        />
      )}
      {modal === 'dataset' && <AddToDatasetModal projectId={projectId} calls={selCalls} onClose={() => setModal(null)} />}
      {extraModal === 'serverExport' && <ServerExportModal spec={{ projectId, ...req, sortBy }} columns={s.cols} onClose={() => setExtraModal(null)} />}
      {extraModal === 'code' && <CopyAsCodeModal spec={{ projectId, ...req, sortBy }} authRequired={!!server.data?.auth_required} onClose={() => setExtraModal(null)} />}
      {extraModal === 'chart' && <AddToDashboardModal projectId={projectId} filter={req.filter} query={req.query} search={req.search} onClose={() => setExtraModal(null)} />}
    </div>
  )
}

function CallRow({
  call: c,
  index,
  projectId,
  slots,
  offsets,
  leadPinned,
  selected,
  focused,
  onToggle,
  onOpen,
  onFilterOp,
  provisionalRoot,
}: {
  call: Span
  index: number
  projectId: string
  slots: ColumnSlot[]
  offsets: Record<string, number>
  leadPinned: boolean
  selected: boolean
  focused: boolean
  onToggle: () => void
  onOpen: () => void
  onFilterOp: (op: string) => void
  /** Listed for its trace while the trace's top-level span has not arrived. */
  provisionalRoot: boolean
}) {
  const versionOf = useContext(RefVersions)
  const st = callStatus(c)
  const runStatus = c.summary?.weft?.run_status
  const tokens = callTokens(c)
  const cost = callCost(c)
  const reactions = reactionCounts(c.feedback)
  const scores = (c.feedback ?? []).filter((f) => f.feedback_type.startsWith('weft.scorer.')).length
  const notes = (c.feedback ?? []).filter((f) => f.feedback_type === 'weft.note').length
  const childCounts = c.summary?.status_counts
  const opBase = spanDisplayName(c.name)
  const label = callLabel(c)
  const cell = (col: BaseCol) => {
    switch (col) {
      case 'status':
        return (
          <td key={col} style={{ width: 24 }}>
            <RunStatusGlyph status={runStatus} own={st} />
          </td>
        )
      case 'name':
        return (
          <td key={col} className="name-cell">
            <Link to={spanUrl(projectId, c.id)} onClick={(e) => e.stopPropagation()} className="call-link">
              {label}
            </Link>
            <div className="name-sub">
              {label !== opBase && (
                <button
                  className="linkish"
                  title="Filter by this name"
                  onClick={(e) => {
                    e.stopPropagation()
                    onFilterOp(opBase)
                  }}
                >
                  {opBase}
                </button>
              )}
              {provisionalRoot && (
                <span title="OTLP sends a span when it ends: this trace's top-level span has not arrived yet, so its first step stands in for it.">
                  top-level span not received yet
                </span>
              )}
              {childCounts && (childCounts.success ?? 0) + (childCounts.error ?? 0) > 0 && (
                <span title="Descendant spans">
                  {plural((childCounts.success ?? 0) + (childCounts.error ?? 0), 'span')}
                  {childCounts.error ? <span> · {childCounts.error} failed</span> : null}
                </span>
              )}
            </div>
          </td>
        )
      case 'started':
        return (
          <td key={col} title={fmtDate(c.started_at)} style={{ whiteSpace: 'nowrap' }}>
            {fmtRelative(c.started_at)}
          </td>
        )
      case 'latency':
        return (
          <td key={col} className="num">
            {fmtDuration(callLatencyMs(c))}
          </td>
        )
      case 'tokens':
        return (
          <td key={col} className="num" title={tokens.total ? `${fmtNumber(tokens.prompt)} prompt · ${fmtNumber(tokens.completion)} completion` : undefined}>
            {tokens.total ? fmtNumber(tokens.total) : <span className="faint">—</span>}
          </td>
        )
      case 'cost':
        return (
          <td key={col} className="num">
            {cost ? fmtCost(cost) : <span className="faint">—</span>}
          </td>
        )
      case 'feedback':
        return (
          <td key={col} className="obs-faint" title={reactions.length ? reactions.map(([e, n]) => `${e} ${n}`).join(' ') : undefined}>
            {feedbackWords({ reactions: reactions.reduce((t, [, n]) => t + n, 0), notes, scores }) || '—'}
          </td>
        )
      case 'inputs':
        return (
          <td key={col}>
            <span className="cell-preview">{inputsPreview(c, versionOf)}</span>
          </td>
        )
      case 'output':
        return (
          <td key={col}>
            {c.error ? (
              // Red only when the run failed: a failed step the run went on
              // after (or a verdict record of such a session) is not a failure.
              <span
                className={`cell-preview ${!runStatus || runStatus === 'error' ? 'failed' : ''}`}
                title={runStatus && runStatus !== 'error' ? `This span failed. Run: ${RUN_STATUS_LABEL[runStatus]}` : undefined}
              >
                {c.error}
              </span>
            ) : (
              <span className="cell-preview">{outputPreview(c, versionOf)}</span>
            )}
          </td>
        )
    }
  }
  return (
    <tr className={`clickable ${selected ? 'selected' : ''} ${focused ? 'focused' : ''}`} data-row={index} onClick={onOpen}>
      <td onClick={(e) => e.stopPropagation()} className={leadPinned ? 'col-pinned col-lead' : ''}>
        <input type="checkbox" checked={selected} onChange={onToggle} aria-label="Select span" />
      </td>
      {slots.map((slot) => {
        const el: ReactElement<{ className?: string; style?: React.CSSProperties }> | undefined = (BASE_COLS as readonly string[]).includes(slot.id) ? (
          cell(slot.id as BaseCol)
        ) : (
          <td key={slot.id}>
            <span className="cell-preview" style={{ maxWidth: 220 }}>
              {cellValueWords(getDotted(c, slot.id), versionOf, 80)}
            </span>
          </td>
        )
        if (!slot.pinned || !el) return el
        // Pinned cells stick to the left edge of the scrolling table.
        return cloneElement(el, { className: `${el.props.className ?? ''} col-pinned`, style: { ...(el.props.style ?? {}), left: offsets[slot.id] ?? 0 } })
      })}
    </tr>
  )
}

const OWN_STATUS_LABEL: Record<ReturnType<typeof callStatus>, string> = {
  success: 'succeeded',
  error: 'failed',
  running: 'running',
  descendant_error: 'succeeded, a span under it failed',
}

/**
 * A row's run status (summary.weft.run_status, what the status chip keeps)
 * as a glyph, with the span's own status in the tooltip: a failed sub-agent
 * run is a failed step of its caller, and a trace whose top-level span has
 * not arrived is running. Without a run status, the span's own.
 */
function RunStatusGlyph({ status, own }: { status?: StatusFilter; own: ReturnType<typeof callStatus> }) {
  const g = statusGlyph(status ?? own)
  const label = status ? `${RUN_STATUS_LABEL[status]} (this span ${OWN_STATUS_LABEL[own]})` : g.label
  return (
    <span className="obs-glyph-cell" title={label} role="img" aria-label={label} data-run-status={status}>
      <StatusGlyph kind={g.kind} size={16} decorative />
    </span>
  )
}
