import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Link, useNavigate, useSearchParams } from 'react-router-dom'
import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '../../api/client'
import { opDisplayName } from '../../api/refs'
import type { Any, Call, CallsFilter, Operation, Query, SortBy } from '../../api/types'
import { useProjectId } from '../../hooks/useProject'
import { callCost, callLatencyMs, callStatus, callTokens, fmtCost, fmtDate, fmtDuration, fmtNumber, fmtRelative, preview } from '../../lib/format'
import { callUrl, compareCallsUrl } from '../../lib/urls'
import { Empty, ErrorBox, Modal, Pager, Spinner, StatusDot } from '../../components/common'
import { CallCharts } from '../../components/traces/CallCharts'
import { extractChat, messageText, normaliseMessage } from '../../components/traces/ChatView'
import { BASE_FILTER_FIELDS, FilterBuilder, clauseToOperation, clauseValid, decodeClauses, encodeClauses, type FilterClause } from '../../components/traces/FilterBuilder'
import { AddToDatasetModal } from '../../components/traces/AddToDatasetModal'
import { ConfirmModal, Dropdown, MenuItem } from '../../components/traces/Menu'
import { GettingStarted } from '../../components/traces/GettingStarted'
import { callLabel, discoverFeedbackFields, discoverFields, exportCalls, getDotted, reactionCounts, slugify } from '../../components/traces/callUtils'

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
  latency: 'summary.weave.latency_ms',
  tokens: 'summary.total_tokens',
  name: 'op_name',
}
const PAGE_SIZES = [25, 50, 100, 200]
const STATUSES = ['success', 'error', 'running', 'descendant_error'] as const

interface ViewState {
  roots: boolean
  op: string
  status: string
  q: string
  clauses: FilterClause[]
  expr: Operation | null
  sort: SortBy
  cols: string[]
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
    op: p.get('op') ?? '',
    status: p.get('status') ?? '',
    q: p.get('q') ?? '',
    clauses: decodeClauses(p.get('f')),
    expr,
    sort: { field: sf || 'started_at', direction: sd === 'asc' ? 'asc' : 'desc' },
    cols: (p.get('cols') ?? '').split(',').filter(Boolean),
    hidden: new Set((p.get('hide') ?? '').split(',').filter(Boolean)),
    offset: Math.max(0, Number(p.get('offset') ?? 0) || 0),
    limit: PAGE_SIZES.includes(Number(p.get('limit'))) ? Number(p.get('limit')) : 50,
    charts: p.get('charts') !== '0',
    live: p.get('live') === '1',
    view: p.get('view') ?? '',
  }
}

function buildRequest(s: ViewState): { filter: CallsFilter; query?: Query; search?: string } {
  const filter: CallsFilter = {}
  if (s.roots) filter.trace_roots_only = true
  if (s.op) filter.op_names = [s.op]
  const ops: Operation[] = []
  if (s.status) ops.push({ $eq: [{ $getField: 'summary.weave.status' }, { $literal: s.status }] })
  for (const c of s.clauses) if (clauseValid(c)) ops.push(clauseToOperation(c))
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
  const req = useMemo(() => buildRequest(s), [s])

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

  // Search box is debounced into the URL.
  const [searchText, setSearchText] = useState(s.q)
  useEffect(() => setSearchText(s.q), [s.q])
  useEffect(() => {
    if (searchText === s.q) return
    const t = setTimeout(() => update({ q: searchText || null }), 300)
    return () => clearTimeout(t)
  }, [searchText, s.q, update])

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
  const searchRef = useRef<HTMLInputElement>(null)
  const reqKey = JSON.stringify([req, s.sort, s.offset, s.limit])
  useEffect(() => setCursor(-1), [reqKey])
  useEffect(() => {
    const h = (e: KeyboardEvent) => {
      const t = e.target as HTMLElement
      if (t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.tagName === 'SELECT' || t.isContentEditable)) return
      if (e.metaKey || e.ctrlKey || e.altKey || document.querySelector('.modal')) return
      if (e.key === '/') {
        e.preventDefault()
        searchRef.current?.focus()
      } else if (e.key === 'j' || e.key === 'ArrowDown') {
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
    if (def.search) p.q = def.search
    if (def.query?.$expr) p.expr = JSON.stringify(def.query.$expr)
    setParams(new URLSearchParams(p))
  }
  const currentDef = (): SavedViewDef => {
    const ui: Record<string, string> = {}
    params.forEach((v, k) => {
      if (k !== 'view' && k !== 'offset') ui[k] = v
    })
    return { filter: req.filter, query: req.query, sort_by: sortBy, cols: s.cols, search: req.search, ui_params: ui }
  }
  const activeView = traceViews.find((v) => v.object_id === s.view)
  const hasFilters = !!(s.op || s.status || s.q || s.clauses.length || s.expr || !s.roots)

  const selCalls = [...selected.values()]
  const exportTargets = selCalls.length ? selCalls : rows

  return (
    <div className="page page-fill">
      <div className="page-header">
        <h1>Traces</h1>
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
          {total != null ? `${fmtNumber(total)} ${s.roots ? 'traces' : 'calls'}` : count.isLoading ? '' : ''}
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
      </div>

      {s.charts && <CallCharts projectId={projectId} filter={req.filter} query={req.query} search={req.search} />}

      <div className="toolbar">
        <input
          ref={searchRef}
          className="input"
          style={{ width: 220 }}
          placeholder="Search…  (/)"
          value={searchText}
          onChange={(e) => setSearchText(e.target.value)}
          onKeyDown={(e) => e.key === 'Escape' && (e.currentTarget.blur(), setSearchText(''))}
          aria-label="Search calls"
        />
        <select className="input" value={s.op} onChange={(e) => update({ op: e.target.value || null })} aria-label="Op filter" style={{ maxWidth: 220 }}>
          <option value="">All ops</option>
          {opOptions.map(([name, n]) => (
            <option key={name} value={name}>
              {name} ({fmtNumber(n)})
            </option>
          ))}
          {s.op && !opOptions.some(([name]) => name === s.op) && <option value={s.op}>{s.op}</option>}
        </select>
        <select className="input" value={s.status} onChange={(e) => update({ status: e.target.value || null })} aria-label="Status filter">
          <option value="">Any status</option>
          {STATUSES.map((st) => (
            <option key={st} value={st}>
              {st === 'descendant_error' ? 'child error' : st}
            </option>
          ))}
        </select>
        <label className="checkbox small" title="Only show root calls of each trace">
          <input type="checkbox" checked={s.roots} onChange={(e) => update({ roots: e.target.checked ? null : '0' })} /> Roots only
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
        </Dropdown>
      </div>

      {selected.size > 0 && (
        <div className="selection-bar" role="toolbar" aria-label="Selection actions">
          <b>{selected.size} selected</b>
          <button className="btn sm" onClick={() => setSelected(new Map())}>
            Clear
          </button>
          <span className="grow" />
          <button className="btn sm" disabled={selected.size < 2} title={selected.size < 2 ? 'Select at least two calls' : undefined} onClick={() => navigate(compareCallsUrl(projectId, [...selected.keys()]))}>
            Compare
          </button>
          <button className="btn sm" onClick={() => setModal('dataset')}>
            Add to dataset
          </button>
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
            <Spinner label="Loading calls…" />
          </div>
        </div>
      ) : rows.length === 0 ? (
        hasFilters ? (
          <Empty>
            No calls match these filters.{' '}
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
              <tr>
                <th style={{ width: 28 }}>
                  <input type="checkbox" checked={allOnPage} onChange={togglePage} aria-label="Select page" />
                </th>
                {visibleBase.map((c) => {
                  const sf = SORT_FIELD[c]
                  const num = c === 'latency' || c === 'tokens' || c === 'cost'
                  return (
                    <th
                      key={c}
                      className={`${sf ? 'sortable' : ''} ${num ? 'num' : ''}`}
                      onClick={sf ? () => setSort(sf) : undefined}
                      aria-sort={sf && s.sort.field === sf ? (s.sort.direction === 'asc' ? 'ascending' : 'descending') : undefined}
                    >
                      {c === 'status' ? '' : BASE_COL_LABEL[c]}
                      {sf && sortArrow(sf)}
                    </th>
                  )
                })}
                {s.cols.map((p) => (
                  <th key={p} className="sortable mono" onClick={() => setSort(p)}>
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
                ))}
              </tr>
            </thead>
            <tbody>
              {rows.map((c, i) => (
                <CallRow
                  key={c.id}
                  call={c}
                  index={i}
                  projectId={projectId}
                  cols={visibleBase}
                  extra={s.cols}
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
              <Dropdown label="Export page ▾" className="btn ghost sm">
                {(close) =>
                  (['json', 'jsonl', 'csv'] as const).map((f) => (
                    <MenuItem
                      key={f}
                      onClick={() => {
                        exportCalls(exportTargets, f, s.cols)
                        close()
                      }}
                    >
                      {f.toUpperCase()}
                    </MenuItem>
                  ))
                }
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
          body={<>The selected calls and all of their descendants will be deleted. This cannot be undone from the UI.</>}
          confirmLabel="Delete"
          danger
          busy={del.isPending}
          error={del.error}
          onConfirm={() => del.mutate()}
          onClose={() => setModal(null)}
        />
      )}
      {modal === 'dataset' && <AddToDatasetModal projectId={projectId} calls={selCalls} onClose={() => setModal(null)} />}
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
  cols,
  extra,
  selected,
  focused,
  onToggle,
  onOpen,
  onFilterOp,
}: {
  call: Call
  index: number
  projectId: string
  cols: readonly BaseCol[]
  extra: string[]
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
  const scores = (c.feedback ?? []).filter((f) => f.feedback_type.startsWith('wandb.runnable.')).length
  const notes = (c.feedback ?? []).filter((f) => f.feedback_type === 'wandb.note.1').length
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
                <span title="Descendant calls">
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
      <td onClick={(e) => e.stopPropagation()}>
        <input type="checkbox" checked={selected} onChange={onToggle} aria-label="Select call" />
      </td>
      {cols.map(cell)}
      {extra.map((p) => (
        <td key={p}>
          <span className="cell-preview" style={{ maxWidth: 220 }}>
            {preview(getDotted(c, p), 80)}
          </span>
        </td>
      ))}
    </tr>
  )
}
