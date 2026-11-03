import { cloneElement, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type ReactElement } from 'react'
import { Link, useNavigate, useSearchParams } from 'react-router-dom'
import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '../../api/client'
import { opDisplayName } from '../../api/refs'
import type { Any, Call, CallsFilter, Operation, Query, SortBy } from '../../api/types'
import { useProjectId } from '../../hooks/useProject'
import { callCost, callLatencyMs, callStatus, callTokens, fmtCost, fmtDate, fmtDuration, fmtNumber, fmtRelative, preview } from '../../lib/format'
import { callUrl, compareCallsUrl, threadsUrl } from '../../lib/urls'
import { Empty, ErrorBox, Modal, Pager, Spinner, StatusDot } from '../../components/common'
import { CallCharts } from '../../components/traces/CallCharts'
import { extractChat, messageText, normaliseMessage } from '../../components/traces/ChatView'
import { BASE_FILTER_FIELDS, FilterBuilder, clauseToOperation, clauseValid, decodeClauses, encodeClauses, type FilterClause } from '../../components/traces/FilterBuilder'
import { AddToDatasetModal } from '../../components/traces/AddToDatasetModal'
import { ConfirmModal, Dropdown, MenuItem } from '../../components/traces/Menu'
import { GettingStarted } from '../../components/traces/GettingStarted'
import { callLabel, discoverFeedbackFields, discoverFields, exportCalls, getDotted, reactionCounts, slugify } from '../../components/traces/callUtils'
import { CopyAsCodeModal, ServerExportModal } from '../../components/traces/ExportModal'
import { AddToDashboardModal } from '../../components/dashboards/AddToDashboardModal'
import { AddToQueueButton } from '../../components/queues/AddToQueueModal'
import '../../components/traces/columns.css'
import { FilterBar } from '../../components/ui/FilterBar'
import { parseFilters, timeWindow, type FilterState } from '../../lib/filters'
import { decodeIds, encodeIds, layoutColumns, moveBefore, moveStep, pinnedOffsets, togglePin, type ColumnSlot } from '../../components/traces/columnLayout'

// ---------------------------------------------------------------- URL state

const BASE_COLS = ['status', 'name', 'started', 'latency', 'tokens', 'cost', 'feedback', 'inputs', 'output'] as const
type BaseCol = (typeof BASE_COLS)[number]
const BASE_COL_LABEL: Record<BaseCol, string> = {
  status: 'Status',
  name: 'Op / name',
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
  name: 'op_name',
}
const PAGE_SIZES = [25, 50, 100, 200]
const STATUSES = ['success', 'error', 'running', 'descendant_error'] as const

interface ViewState {
  roots: boolean
  system: boolean
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

function parseState(p: URLSearchParams): ViewState {
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
    op: p.get('op') ?? '',
    // The shared filter bar says ok/error/running; spans also have descendant_error.
    status: p.get('status') === 'ok' ? 'success' : (p.get('status') ?? ''),
    q: p.get('q') ?? '',
    clauses: decodeClauses(p.get('f')),
    expr,
    sort: { field: sf || 'started_at', direction: sd === 'asc' ? 'asc' : 'desc' },
    cols: (p.get('cols') ?? '').split(',').filter(Boolean),
    order: decodeIds(p.get('order')),
    pin: decodeIds(p.get('pin')),
    hidden: new Set((p.get('hide') ?? '').split(',').filter(Boolean)),
    offset: Math.max(0, Number(p.get('offset') ?? 0) || 0),
    limit: PAGE_SIZES.includes(Number(p.get('limit'))) ? Number(p.get('limit')) : 50,
    charts: p.get('charts') !== '0',
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

/** The shared filter bar's chips as span filters (agent, model, tool, time). */
export function sharedOps(f: FilterState): Operation[] {
  const ops: Operation[] = []
  if (f.agent) ops.push(attrEq('gen_ai.agent.name', f.agent))
  if (f.model) ops.push(attrEq('gen_ai.request.model', f.model))
  if (f.tool) ops.push(attrEq('gen_ai.tool.name', f.tool))
  const w = timeWindow(f, 'all')
  if (w.from) ops.push({ $gte: [{ $getField: 'started_at' }, { $literal: w.from }] })
  if (w.to) ops.push({ $lt: [{ $getField: 'started_at' }, { $literal: w.to }] })
  return ops
}

function buildRequest(s: ViewState, shared: FilterState = {}): { filter: CallsFilter; query?: Query; search?: string } {
  const filter: CallsFilter = {}
  if (s.roots) filter.trace_roots_only = true
  if (s.op) filter.op_names = [s.op]
  const ops: Operation[] = []
  // Monitor scorers and server-side LLM calls are tagged internal; hide them unless asked.
  if (!s.system) ops.push({ $not: [{ $eq: [{ $getField: 'attributes.weft.internal' }, { $literal: true }] }] })
  if (s.status) ops.push({ $eq: [{ $getField: 'summary.weft.status' }, { $literal: s.status }] })
  for (const c of s.clauses) if (clauseValid(c)) ops.push(clauseToOperation(c))
  ops.push(...sharedOps(shared))
  if (s.expr) ops.push(s.expr)
  const query = ops.length ? { $expr: ops.length === 1 ? ops[0] : { $and: ops } } : undefined
  return { filter, query, search: s.q.trim() || undefined }
}

// ---------------------------------------------------------------- cell helpers

function inputsPreview(c: Call): string {
  const chat = extractChat(c)
  if (chat) {
    const users = chat.input.filter((m) => m?.role === 'user')
    const last = users.length ? users[users.length - 1] : chat.input[chat.input.length - 1]
    return messageText(normaliseMessage(last)) || preview(last)
  }
  if (!c.inputs || typeof c.inputs !== 'object') return preview(c.inputs)
  return Object.entries(c.inputs)
    .filter(([k]) => k !== 'self')
    .map(([k, v]) => `${k}: ${preview(v, 60)}`)
    .join(' · ')
}

function outputPreview(c: Call): string {
  const chat = extractChat(c)
  if (chat?.output?.length) {
    const m = normaliseMessage(chat.output[chat.output.length - 1])
    const text = messageText(m)
    if (text) return text
    const tools = m.parts.filter((p) => p.type === 'tool_call').map((p) => (p as { name: string }).name)
    if (tools.length) return `→ ${tools.join(', ')}()`
  }
  const o = c.output
  if (o && typeof o === 'object' && !Array.isArray(o))
    return Object.entries(o)
      .map(([k, v]) => `${k}: ${preview(v, 60)}`)
      .join(' · ')
  return preview(o)
}

const plural = (n: number, w: string) => `${fmtNumber(n)} ${w}${n === 1 ? '' : 's'}`

function colLabel(path: string) {
  return path
}

// ---------------------------------------------------------------- saved views

interface SavedViewDef {
  filter?: CallsFilter
  query?: Query
  sort_by?: SortBy[]
  cols?: string[]
  /** Column order and pinned columns (column ids: base names or field paths). */
  col_order?: string[]
  pinned_cols?: string[]
  search?: string
  ui_params?: Record<string, string>
}

function SaveViewModal({ projectId, def, onClose, onSaved }: { projectId: string; def: SavedViewDef; onClose: () => void; onSaved: (id: string) => void }) {
  const [label, setLabel] = useState('')
  const qc = useQueryClient()
  const save = useMutation({
    mutationFn: async () => {
      const id = slugify(label)
      if (!id) throw new Error('Enter a name')
      await api.objCreate(
        projectId,
        id,
        { _class_name: 'SavedView', _bases: ['Object', 'BaseModel'], name: id, view_type: 'traces', label: label.trim(), definition: def },
        'SavedView',
      )
      return id
    },
    onSuccess: (id) => {
      void qc.invalidateQueries({ queryKey: ['objs', projectId, 'SavedView'] })
      onSaved(id)
    },
  })
  return (
    <Modal
      title="Save view"
      onClose={onClose}
      footer={
        <>
          <button className="btn" onClick={onClose}>
            Cancel
          </button>
          <button className="btn primary" disabled={!label.trim() || save.isPending} onClick={() => save.mutate()}>
            Save
          </button>
        </>
      }
    >
      <form
        onSubmit={(e) => {
          e.preventDefault()
          if (label.trim()) save.mutate()
        }}
      >
        <label className="field">
          Name
          <input className="input" autoFocus value={label} onChange={(e) => setLabel(e.target.value)} placeholder="e.g. Slow errors" />
        </label>
      </form>
      <div className="small muted">Saves the current filters, search, sort and columns. Saving with an existing name creates a new version of that view.</div>
      <ErrorBox error={save.error} />
    </Modal>
  )
}

// ---------------------------------------------------------------- page

export function TracesPage() {
  const projectId = useProjectId()
  const navigate = useNavigate()
  const qc = useQueryClient()
  const [params, setParams] = useSearchParams()
  const s = useMemo(() => parseState(params), [params])
  const shared = useMemo(() => parseFilters(params), [params])
  const req = useMemo(() => buildRequest(s, shared), [s, shared])

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
    queryKey: ['calls', projectId, 'table', req, s.sort, s.offset, s.limit],
    queryFn: () =>
      api.callsQuery({
        project_id: projectId,
        ...req,
        sort_by: sortBy,
        limit: s.limit,
        offset: s.offset,
        include_costs: true,
        include_feedback: true,
      }),
    placeholderData: keepPreviousData,
    refetchInterval,
  })
  const count = useQuery({
    queryKey: ['calls', projectId, 'count', req],
    queryFn: () => api.callsQueryStats({ project_id: projectId, ...req }),
    placeholderData: keepPreviousData,
    refetchInterval,
  })
  const opCounts = useQuery({ queryKey: ['calls', projectId, 'op_counts', s.roots], queryFn: () => api.opCounts(projectId, s.roots) })
  // op_counts may list several versions of one op; the filter works on base names.
  const opOptions = useMemo(() => {
    const m = new Map<string, number>()
    for (const o of opCounts.data?.ops ?? []) m.set(o.op_base_name, (m.get(o.op_base_name) ?? 0) + o.count)
    return [...m.entries()].sort((a, b) => b[1] - a[1])
  }, [opCounts.data])
  const views = useQuery({
    queryKey: ['objs', projectId, 'SavedView', 'latest'],
    queryFn: () => api.objsQuery({ project_id: projectId, filter: { base_object_classes: ['SavedView'], latest_only: true }, limit: 200 }),
  })
  const traceViews = (views.data?.objs ?? []).filter((o) => !o.deleted_at && (o.val?.view_type ?? 'traces') === 'traces')

  const rows = calls.data?.calls ?? []
  const total = count.data?.count ?? null

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

  // Selection survives paging; keep the Call objects for actions.
  const [selected, setSelected] = useState<Map<string, Call>>(new Map())
  const toggleSel = (c: Call) =>
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

  const [modal, setModal] = useState<null | 'delete' | 'dataset' | 'saveView'>(null)
  // Server export, copy-as-code and add-to-dashboard modals.
  const [extraModal, setExtraModal] = useState<null | 'serverExport' | 'code' | 'chart'>(null)
  const serverInfo = useQuery({ queryKey: ['server-info'], queryFn: api.serverInfo, staleTime: 60_000 })
  const del = useMutation({
    mutationFn: () => api.callsDelete(projectId, [...selected.keys()]),
    onSuccess: () => {
      setSelected(new Map())
      setModal(null)
      void qc.invalidateQueries({ queryKey: ['calls', projectId] })
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
        navigate(callUrl(projectId, rows[cursor].id))
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

  const loadView = (o: Any) => {
    const def: SavedViewDef = o.val?.definition ?? {}
    if (def.ui_params) {
      setParams(new URLSearchParams({ ...def.ui_params, view: o.object_id }), { replace: false })
      return
    }
    const p: Record<string, string> = { view: o.object_id }
    if (def.filter?.trace_roots_only === false) p.roots = '0'
    if (def.filter?.op_names?.[0]) p.op = def.filter.op_names[0]
    if (def.sort_by?.[0]) p.sort = `${def.sort_by[0].field}:${def.sort_by[0].direction}`
    if (def.cols?.length) p.cols = def.cols.join(',')
    if (def.col_order?.length) p.order = encodeIds(def.col_order)
    if (def.pinned_cols?.length) p.pin = encodeIds(def.pinned_cols)
    if (def.search) p.q = def.search
    if (def.query?.$expr) p.expr = JSON.stringify(def.query.$expr)
    setParams(new URLSearchParams(p))
  }
  const currentDef = (): SavedViewDef => {
    const ui: Record<string, string> = {}
    params.forEach((v, k) => {
      if (k !== 'view' && k !== 'offset') ui[k] = v
    })
    return { filter: req.filter, query: req.query, sort_by: sortBy, cols: s.cols, col_order: s.order.length ? s.order : undefined, pinned_cols: s.pin.length ? s.pin : undefined, search: req.search, ui_params: ui }
  }
  const activeView = traceViews.find((v) => v.object_id === s.view)
  const hasFilters = !!(s.op || s.status || s.q || s.clauses.length || s.expr || !s.roots || shared.agent || shared.model || shared.tool || shared.time)

  const selCalls = [...selected.values()]
  const exportTargets = selCalls.length ? selCalls : rows

  return (
    <div className="page page-fill">
      <div className="page-header">
        <h1>Traces</h1>
        <Link className="btn sm ghost" to={threadsUrl(projectId)} title="Spans grouped by thread id">
          Threads
        </Link>
        <Dropdown label={<>{activeView ? (activeView.val?.label ?? activeView.object_id) : 'Saved views'} ▾</>} className="btn sm">
          {(close) => (
            <>
              {traceViews.length === 0 && <div className="dd-note">No saved views yet.</div>}
              {traceViews.map((v) => (
                <MenuItem
                  key={v.object_id}
                  onClick={() => {
                    loadView(v)
                    close()
                  }}
                >
                  <span className="grow">{v.val?.label ?? v.object_id}</span>
                  {v.object_id === s.view && <span className="faint">✓</span>}
                </MenuItem>
              ))}
              <div className="dd-sep" />
              <MenuItem
                onClick={() => {
                  setModal('saveView')
                  close()
                }}
              >
                Save current view…
              </MenuItem>
              <MenuItem
                disabled={!hasFilters && !s.view && !s.cols.length}
                onClick={() => {
                  setParams(new URLSearchParams())
                  close()
                }}
              >
                Reset to default
              </MenuItem>
            </>
          )}
        </Dropdown>
        <span className="spacer" />
        <span className="small muted">
          {total != null ? `${fmtNumber(total)} ${s.roots ? 'traces' : 'spans'}` : count.isLoading ? '' : ''}
          {(calls.isFetching || count.isFetching) && <span style={{ marginLeft: 6 }}><Spinner /></span>}
        </span>
        <label className="checkbox small" title="Refresh every 5 seconds">
          <input type="checkbox" checked={s.live} onChange={(e) => update({ live: e.target.checked ? '1' : null }, false)} /> Auto-refresh
        </label>
        <button className="btn sm" onClick={() => void qc.invalidateQueries({ queryKey: ['calls', projectId] })} title="Refresh">
          ↻
        </button>
        <button className="btn sm" onClick={() => update({ charts: s.charts ? '0' : null }, false)} aria-expanded={s.charts}>
          {s.charts ? 'Hide charts' : 'Show charts'}
        </button>
        {s.charts && (
          <button className="btn sm" onClick={() => setExtraModal('chart')} title="Build a custom chart from the current filters and add it to a dashboard">
            + Add chart to dashboard
          </button>
        )}
      </div>

      <FilterBar page="traces" defaultTime="all" showViews={false} placeholder="Filter spans…  try agent:researcher or tool:Bash" />

      {s.charts && <CallCharts projectId={projectId} filter={req.filter} query={req.query} search={req.search} />}

      <div className="toolbar">
        <label className="checkbox small" title="Only show the top-level span of each trace">
          <input type="checkbox" checked={s.roots} onChange={(e) => update({ roots: e.target.checked ? null : '0' })} /> Roots only
        </label>
        <label className="checkbox small" title="Include scorer spans made by monitors and other internal server spans">
          <input type="checkbox" checked={s.system} onChange={(e) => update({ system: e.target.checked ? '1' : null })} /> System spans
        </label>
        <FilterBuilder clauses={s.clauses} fields={filterFields} onChange={(cs) => update({ f: encodeClauses(cs) || null })} />
        {s.expr && (
          <span className="filter-chip" title={JSON.stringify(s.expr)}>
            <span className="filter-chip-label mono">custom query</span>
            <button className="filter-chip-x" aria-label="Remove custom query" onClick={() => update({ expr: null })}>
              ✕
            </button>
          </span>
        )}
        <span className="grow" />
        <Dropdown label="Columns ▾" align="right">
          <div className="dd-section">Columns</div>
          {BASE_COLS.map((c) => (
            <label key={c} className="dd-check">
              <input
                type="checkbox"
                checked={!s.hidden.has(c)}
                onChange={() => {
                  const h = new Set(s.hidden)
                  if (h.has(c)) h.delete(c)
                  else h.add(c)
                  update({ hide: [...h].join(',') || null }, false)
                }}
              />
              {BASE_COL_LABEL[c]}
            </label>
          ))}
          <div className="dd-section">Fields from loaded rows</div>
          {columnCandidates.length === 0 && <div className="dd-note">No input/output fields found yet.</div>}
          <div style={{ maxHeight: 240, overflow: 'auto' }}>
            {[...new Set([...s.cols, ...columnCandidates])].map((f) => (
              <label key={f} className="dd-check mono small">
                <input
                  type="checkbox"
                  checked={s.cols.includes(f)}
                  onChange={() => update({ cols: (s.cols.includes(f) ? s.cols.filter((x) => x !== f) : [...s.cols, f]).join(',') || null }, false)}
                />
                {f}
              </label>
            ))}
          </div>
          <div className="dd-section">Order &amp; pinning</div>
          <div style={{ maxHeight: 260, overflow: 'auto' }}>
            {slots.map((c, i) => (
              <div key={c.id} className="dd-check col-order-row">
                <span className={`grow truncate ${c.id.includes('.') ? 'mono small' : ''}`} title={c.id}>
                  {(BASE_COL_LABEL as Record<string, string>)[c.id] || (c.id === 'status' ? 'Status' : c.id)}
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
        </Dropdown>
      </div>

      {selected.size > 0 && (
        <div className="selection-bar" role="toolbar" aria-label="Selection actions">
          <b>{selected.size} selected</b>
          <button className="btn sm" onClick={() => setSelected(new Map())}>
            Clear
          </button>
          <span className="grow" />
          <button className="btn sm" disabled={selected.size < 2} title={selected.size < 2 ? 'Select at least two spans' : undefined} onClick={() => navigate(compareCallsUrl(projectId, [...selected.keys()]))}>
            Compare
          </button>
          <button className="btn sm" onClick={() => setModal('dataset')}>
            Add to dataset
          </button>
          <AddToQueueButton projectId={projectId} callIds={[...selected.keys()]} />
          <Dropdown label="Export ▾" align="right">
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
          <button className="btn sm danger" onClick={() => setModal('delete')}>
            Delete
          </button>
        </div>
      )}

      <ErrorBox error={calls.error} />

      {calls.isLoading ? (
        <div className="table-wrap grow-table">
          <div className="empty">
            <Spinner label="Loading spans…" />
          </div>
        </div>
      ) : rows.length === 0 ? (
        hasFilters ? (
          <Empty>
            No spans match these filters.{' '}
            <button className="btn sm" onClick={() => setParams(new URLSearchParams())}>
              Clear filters
            </button>
          </Empty>
        ) : (
          <div className="card">
            <div className="card-header">
              <h3>No traces yet</h3>
            </div>
            <div className="card-body">
              <GettingStarted projectId={projectId} />
            </div>
          </div>
        )
      ) : (
        <div className="table-wrap grow-table">
          <table className="table traces-table">
            <thead>
              <tr ref={headRef}>
                <th style={{ width: 28 }} className={s.pin.length ? 'col-pinned col-lead' : ''}>
                  <input type="checkbox" checked={allOnPage} onChange={togglePage} aria-label="Select page" />
                </th>
                {slots.map((slot) => {
                  if (!(BASE_COLS as readonly string[]).includes(slot.id)) {
                    const p = slot.id
                    const hp = colHeadProps(slot)
                    return (
                      <th key={p} {...hp} className={`sortable mono ${hp.className}`} onClick={() => setSort(p)}>
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
                      key={c}
                      {...hp}
                      className={`${sf ? 'sortable' : ''} ${num ? 'num' : ''} ${hp.className}`}
                      onClick={sf ? () => setSort(sf) : undefined}
                      aria-sort={sf && s.sort.field === sf ? (s.sort.direction === 'asc' ? 'ascending' : 'descending') : undefined}
                    >
                      {c === 'status' ? '' : BASE_COL_LABEL[c]}
                      {sf && sortArrow(sf)}
                    </th>
                  )
                })}
              </tr>
            </thead>
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
                  onOpen={() => navigate(callUrl(projectId, c.id))}
                  onFilterOp={(op) => update({ op })}
                />
              ))}
            </tbody>
          </table>
        </div>
      )}

      {rows.length > 0 && (
        <div className="row wrap" style={{ justifyContent: 'space-between' }}>
          <div className="row small muted">
            <span className="kbd-hint">
              <kbd>j</kbd>/<kbd>k</kbd> move · <kbd>Enter</kbd> open · <kbd>x</kbd> select · <kbd>/</kbd> search
            </span>
            {!selected.size && (
              <Dropdown label="Export ▾" className="btn ghost sm">
                {(close) => (
                  <>
                    <div className="dd-section">This page</div>
                    {(['json', 'jsonl', 'csv'] as const).map((f) => (
                      <MenuItem
                        key={f}
                        onClick={() => {
                          exportCalls(exportTargets, f, s.cols)
                          close()
                        }}
                      >
                        {f.toUpperCase()}
                      </MenuItem>
                    ))}
                    <div className="dd-sep" />
                    <MenuItem
                      onClick={() => {
                        setExtraModal('serverExport')
                        close()
                      }}
                    >
                      All matching rows (server)…
                    </MenuItem>
                    <MenuItem
                      onClick={() => {
                        setExtraModal('code')
                        close()
                      }}
                    >
                      Copy as code…
                    </MenuItem>
                  </>
                )}
              </Dropdown>
            )}
          </div>
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
          title={`Delete ${selected.size} call${selected.size === 1 ? '' : 's'}?`}
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
      {extraModal === 'serverExport' && (
        <ServerExportModal spec={{ projectId, ...req, sortBy }} columns={s.cols} onClose={() => setExtraModal(null)} />
      )}
      {extraModal === 'code' && (
        <CopyAsCodeModal spec={{ projectId, ...req, sortBy }} authRequired={!!serverInfo.data?.auth_required} onClose={() => setExtraModal(null)} />
      )}
      {extraModal === 'chart' && (
        <AddToDashboardModal projectId={projectId} filter={req.filter} query={req.query} search={req.search} onClose={() => setExtraModal(null)} />
      )}
      {modal === 'saveView' && (
        <SaveViewModal
          projectId={projectId}
          def={currentDef()}
          onClose={() => setModal(null)}
          onSaved={(id) => {
            setModal(null)
            update({ view: id }, false)
          }}
        />
      )}
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
}: {
  call: Call
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
}) {
  const st = callStatus(c)
  const tokens = callTokens(c)
  const cost = callCost(c)
  const reactions = reactionCounts(c.feedback)
  const scores = (c.feedback ?? []).filter((f) => f.feedback_type.startsWith('weft.scorer.')).length
  const notes = (c.feedback ?? []).filter((f) => f.feedback_type === 'weft.note').length
  const childCounts = c.summary?.status_counts
  const opBase = opDisplayName(c.op_name)
  const label = callLabel(c)
  const cell = (col: BaseCol) => {
    switch (col) {
      case 'status':
        return (
          <td key={col} style={{ width: 20 }}>
            <StatusDot status={st} />
          </td>
        )
      case 'name':
        return (
          <td key={col} className="name-cell">
            <Link to={callUrl(projectId, c.id)} onClick={(e) => e.stopPropagation()} className="call-link">
              {label}
            </Link>
            <div className="row small faint" style={{ gap: 6, whiteSpace: 'nowrap' }}>
              {label !== opBase && (
                <button
                  className="linkish"
                  title="Filter by this op"
                  onClick={(e) => {
                    e.stopPropagation()
                    onFilterOp(opBase)
                  }}
                >
                  {opBase}
                </button>
              )}
              {childCounts && (childCounts.success ?? 0) + (childCounts.error ?? 0) > 0 && (
                <span title="Descendant spans">
                  {plural((childCounts.success ?? 0) + (childCounts.error ?? 0), 'call')}
                  {childCounts.error ? <span style={{ color: 'var(--error)' }}> · {childCounts.error} err</span> : null}
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
          <td key={col} style={{ whiteSpace: 'nowrap' }}>
            {reactions.map(([e, n]) => (
              <span key={e} className="mini-reaction">
                {e}
                {n > 1 && <span className="small faint">{n}</span>}
              </span>
            ))}
            {notes > 0 && (
              <span className="small faint" title="Notes">
                ✎{notes}{' '}
              </span>
            )}
            {scores > 0 && (
              <span className="small faint" title="Scores">
                ★{scores}
              </span>
            )}
          </td>
        )
      case 'inputs':
        return (
          <td key={col}>
            <span className="cell-preview">{inputsPreview(c)}</span>
          </td>
        )
      case 'output':
        return (
          <td key={col}>
            {c.exception ? (
              <span className="cell-preview" style={{ color: 'var(--error)' }}>
                {c.exception}
              </span>
            ) : (
              <span className="cell-preview">{outputPreview(c)}</span>
            )}
          </td>
        )
    }
  }
  return (
    <tr className={`clickable ${selected ? 'selected' : ''} ${focused ? 'focused' : ''}`} data-row={index} onClick={onOpen}>
      <td onClick={(e) => e.stopPropagation()} className={leadPinned ? 'col-pinned col-lead' : ''}>
        <input type="checkbox" checked={selected} onChange={onToggle} aria-label="Select call" />
      </td>
      {slots.map((slot) => {
        const el: ReactElement<{ className?: string; style?: React.CSSProperties }> | undefined = (BASE_COLS as readonly string[]).includes(slot.id) ? (
          cell(slot.id as BaseCol)
        ) : (
          <td key={slot.id}>
            <span className="cell-preview" style={{ maxWidth: 220 }}>
              {preview(getDotted(c, slot.id), 80)}
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
