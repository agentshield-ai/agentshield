// The one filter bar (DESIGN.md §7.1 rule 5) used on Sessions, Agents, Teams,
// Security and Traces: the same chips, the same `key:value` syntax in the
// text box, filters in the URL, and named views saved as SavedView objects
// (falling back to this browser when the project is read-only).
import { useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { useSearchParams } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '../../api/client'
import type { Any } from '../../api/types'
import { useProjectId } from '../../hooks/useProject'
import {
  chipsOf,
  FILTER_KEY_LABEL,
  FILTER_PARAMS,
  HAS_LABEL,
  PAGE_FILTERS,
  parseFilters,
  parseFilterText,
  removeKey,
  STATUS_LABEL,
  TIME_LABEL,
  TIME_OPTIONS,
  writeFilters,
  type FilterKey,
  type FilterState,
  type HasFilter,
  type StatusFilter,
  type TimeRange,
} from '../../lib/filters'
import { Icon } from './Icons'
import { undoable, notify } from './Toasts'

export type FilterPage = keyof typeof PAGE_FILTERS

export interface FilterOptions {
  agents?: string[]
  teams?: { id: string; name: string }[]
  models?: string[]
  tools?: string[]
  scorers?: string[]
}

/** Read and write the shared filter state in the URL. */
export function useFilters(): [FilterState, (next: FilterState) => void, URLSearchParams] {
  const [params, setParams] = useSearchParams()
  const state = useMemo(() => parseFilters(params), [params])
  const set = (next: FilterState) =>
    setParams(
      (prev) => {
        const out = writeFilters(next, prev)
        out.delete('offset')
        out.delete('view')
        return out
      },
      { replace: false },
    )
  return [state, set, params]
}

interface SavedView {
  id: string
  label: string
  params: Record<string, string>
  local?: boolean
}

const localKey = (pid: string, page: string) => `weft.views.${pid}.${page}`

function readLocalViews(pid: string, page: string): SavedView[] {
  try {
    const raw = localStorage.getItem(localKey(pid, page))
    return raw ? (JSON.parse(raw) as SavedView[]).map((v) => ({ ...v, local: true })) : []
  } catch {
    return []
  }
}

function writeLocalViews(pid: string, page: string, views: SavedView[]) {
  try {
    localStorage.setItem(localKey(pid, page), JSON.stringify(views.map(({ local: _l, ...v }) => v)))
  } catch {
    /* storage unavailable */
  }
}

function slug(s: string) {
  return (
    s
      .toLowerCase()
      .replace(/[^a-z0-9]+/g, '-')
      .replace(/^-|-$/g, '')
      .slice(0, 60) || 'view'
  )
}

function useSavedViews(pid: string, page: FilterPage) {
  const qc = useQueryClient()
  const viewType = `filters.${page}`
  const remote = useQuery({
    queryKey: ['objs', pid, 'SavedView', 'latest'],
    queryFn: () => api.objsQuery({ project_id: pid, filter: { base_object_classes: ['SavedView'], latest_only: true }, limit: 200 }),
    staleTime: 30_000,
  })
  const [local, setLocal] = useState<SavedView[]>(() => readLocalViews(pid, page))
  const [hidden, setHidden] = useState<Set<string>>(new Set())
  const views: SavedView[] = [
    ...(remote.data?.objs ?? [])
      .filter((o: Any) => !o.deleted_at && o.val?.view_type === viewType)
      .map((o: Any) => ({ id: o.object_id as string, label: (o.val?.label as string) ?? o.object_id, params: (o.val?.definition?.ui_params ?? {}) as Record<string, string> })),
    ...local,
  ].filter((v) => !hidden.has(v.id))

  const save = async (label: string, params: Record<string, string>) => {
    const id = `${page}-${slug(label)}`
    try {
      await api.objCreate(
        pid,
        id,
        { _class_name: 'SavedView', _bases: ['Object', 'BaseModel'], name: id, view_type: viewType, label, definition: { ui_params: params } },
        'SavedView',
      )
      await qc.invalidateQueries({ queryKey: ['objs', pid, 'SavedView'] })
      notify(`Saved view “${label}”`, 'ok')
    } catch {
      const next = [...local.filter((v) => v.id !== id), { id, label, params, local: true }]
      setLocal(next)
      writeLocalViews(pid, page, next)
      notify(`Saved “${label}” in this browser (the project could not store it)`, 'info')
    }
    return id
  }

  const remove = (v: SavedView) =>
    undoable({
      message: `Deleted view “${v.label}”`,
      apply: () => setHidden((h) => new Set(h).add(v.id)),
      revert: () =>
        setHidden((h) => {
          const n = new Set(h)
          n.delete(v.id)
          return n
        }),
      commit: async () => {
        if (v.local) {
          const next = readLocalViews(pid, page).filter((x) => x.id !== v.id)
          setLocal(next)
          writeLocalViews(pid, page, next)
        } else {
          await api.objDelete(pid, v.id)
          await qc.invalidateQueries({ queryKey: ['objs', pid, 'SavedView'] })
        }
      },
    })
  return { views, save, remove }
}

export function FilterBar({
  page,
  defaultTime = '24h',
  effectiveTime,
  options = {},
  placeholder,
  extra,
  showViews = true,
}: {
  page: FilterPage
  /** False where the page has its own saved views (Traces saves columns too). */
  showViews?: boolean
  defaultTime?: TimeRange
  /** The range actually shown after auto-widening (displayed in the time picker). */
  effectiveTime?: TimeRange
  options?: FilterOptions
  placeholder?: string
  extra?: ReactNode
}) {
  const pid = useProjectId()
  const [state, setState, params] = useFilters()
  const [, setParams] = useSearchParams()
  const keys = PAGE_FILTERS[page]
  // Time has its own picker and text its own box, so neither repeats as a chip.
  const chips = chipsOf(state, { defaultTime }).filter((c) => c.key !== 'time' && c.key !== 'q' && keys.includes(c.key))
  const [editing, setEditing] = useState<FilterKey | null>(null)
  const [text, setText] = useState(state.q ?? '')
  useEffect(() => setText(state.q ?? ''), [state.q])
  const { views, save, remove } = useSavedViews(pid, page)
  const [viewsOpen, setViewsOpen] = useState(false)
  const [naming, setNaming] = useState<string | null>(null)
  const currentView = params.get('view')

  // Plain text filters as you type; `key:value` tokens apply on Enter.
  useEffect(() => {
    if ((text || '') === (state.q ?? '')) return
    if (/\b(agent|team|status|has|model|tool|time|score|cost)\s*[:<>=]/i.test(text)) return
    const t = setTimeout(() => setState({ ...state, q: text.trim() || undefined }), 350)
    return () => clearTimeout(t)
  }, [text]) // eslint-disable-line react-hooks/exhaustive-deps

  const applyText = () => {
    const { q: _q, ...rest } = state
    setState(parseFilterText(text, rest))
  }

  const timeValue: TimeRange = state.time ?? effectiveTime ?? defaultTime
  const filterParams = () => {
    const out: Record<string, string> = {}
    for (const k of FILTER_PARAMS) {
      const v = params.get(k)
      if (v) out[k] = v
    }
    return out
  }
  const addable = keys.filter((k) => k !== 'time' && k !== 'q')

  return (
    <div className="filterbar" role="search" aria-label="Filters">
      <div className="filterbar-row">
        <label className="filter-text">
          <Icon name="search" size={14} />
          <span className="sr-only">Filter</span>
          <input
            data-filter-input
            className="filter-input"
            value={text}
            placeholder={placeholder ?? 'Filter…  try agent:coder has:block'}
            onChange={(e) => setText(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') applyText()
              if (e.key === 'Escape') {
                setText('')
                setState({ ...state, q: undefined })
                e.currentTarget.blur()
              }
            }}
            aria-describedby="filter-syntax-help"
          />
          <kbd className="kbd kbd-hint" aria-hidden>
            /
          </kbd>
        </label>
        {keys.includes('time') && (
          <label className="time-pick">
            <Icon name="clock" size={14} />
            <span className="sr-only">Time range</span>
            <select
              className="input"
              value={timeValue}
              onChange={(e) => {
                const v = e.target.value as TimeRange
                if (v === 'custom') setEditing('time')
                else setState({ ...removeKey(state, 'time'), time: v === defaultTime ? undefined : v })
              }}
            >
              {TIME_OPTIONS.map((o) => (
                <option key={o.id} value={o.id}>
                  {o.label}
                </option>
              ))}
              <option value="custom">{state.time === 'custom' ? 'Custom range' : 'Custom range…'}</option>
            </select>
          </label>
        )}
        <div className="dd">
          <button type="button" className="btn sm" onClick={() => setEditing(editing ? null : addable[0])} aria-expanded={!!editing}>
            <Icon name="plus" size={13} /> Add filter
          </button>
        </div>
        {showViews && (
          <div className="dd">
            <button type="button" className="btn sm ghost" aria-expanded={viewsOpen} aria-haspopup="true" onClick={() => setViewsOpen((o) => !o)}>
              <Icon name="bookmark" size={13} /> {views.find((v) => v.id === currentView)?.label ?? 'Views'}
            </button>
            {viewsOpen && (
              <div className="dd-menu right views-menu">
                {views.length === 0 && <div className="dd-note">No saved views yet. Set some filters, then save them here.</div>}
                {views.map((v) => (
                  <div key={v.id} className="view-item">
                    <button
                      type="button"
                      className="dd-item grow"
                      onClick={() => {
                        const next = writeFilters(parseFilters(new URLSearchParams(v.params)), params)
                        next.set('view', v.id)
                        next.delete('offset')
                        setParams(next)
                        setViewsOpen(false)
                      }}
                    >
                      {v.label}
                      {v.local && <span className="faint small"> (this browser)</span>}
                    </button>
                    <button type="button" className="icon-btn" aria-label={`Delete view ${v.label}`} onClick={() => remove(v)}>
                      <Icon name="trash" size={13} />
                    </button>
                  </div>
                ))}
                <div className="dd-sep" />
                {naming == null ? (
                  <button type="button" className="dd-item" disabled={!Object.keys(filterParams()).length} onClick={() => setNaming('')}>
                    Save current filters as a view…
                  </button>
                ) : (
                  <form
                    className="row"
                    style={{ padding: '4px 8px' }}
                    onSubmit={async (e) => {
                      e.preventDefault()
                      if (!naming.trim()) return
                      const id = await save(naming.trim(), filterParams())
                      setNaming(null)
                      setViewsOpen(false)
                      const next = new URLSearchParams(params)
                      next.set('view', id)
                      setParams(next, { replace: true })
                    }}
                  >
                    <input className="input grow" autoFocus aria-label="View name" placeholder="View name" value={naming} onChange={(e) => setNaming(e.target.value)} />
                    <button className="btn sm primary" type="submit" disabled={!naming.trim()}>
                      Save
                    </button>
                  </form>
                )}
              </div>
            )}
          </div>
        )}
        {extra}
      </div>
      {(chips.length > 0 || state.time === 'custom') && (
        <ul className="chips-row" aria-label="Active filters">
          {chips.map((c) => (
            <li key={c.key} className="fchip">
              <button type="button" className="fchip-body" onClick={() => setEditing(c.key)} aria-label={`Edit filter ${c.label}: ${c.value}`}>
                <span className="fchip-key">{c.label}</span>
                <span className="fchip-val">{c.value}</span>
              </button>
              <button type="button" className="fchip-x" aria-label={`Remove filter ${c.label}`} onClick={() => setState(removeKey(state, c.key))}>
                <Icon name="close" size={11} />
              </button>
            </li>
          ))}
          {state.time === 'custom' && (
            <li className="fchip">
              <button type="button" className="fchip-body" onClick={() => setEditing('time')}>
                <span className="fchip-key">Time</span>
                <span className="fchip-val">{chipsOf(state).find((c) => c.key === 'time')?.value}</span>
              </button>
              <button type="button" className="fchip-x" aria-label="Remove time filter" onClick={() => setState(removeKey(state, 'time'))}>
                <Icon name="close" size={11} />
              </button>
            </li>
          )}
          {chips.length > 1 && (
            <li>
              <button type="button" className="btn ghost sm" onClick={() => setState(state.time ? { time: state.time, from: state.from, to: state.to } : {})}>
                Clear all
              </button>
            </li>
          )}
        </ul>
      )}
      {editing && (
        <FilterEditor
          keys={[...addable, ...(keys.includes('q') ? (['q'] as FilterKey[]) : []), ...(editing === 'time' ? (['time'] as FilterKey[]) : [])]}
          initial={editing}
          state={state}
          options={options}
          onApply={(s) => {
            setState(s)
            setEditing(null)
          }}
          onClose={() => setEditing(null)}
        />
      )}
      <p id="filter-syntax-help" className="sr-only">
        Type text to search. You can also type filters such as agent:name, team:id, status:failed, has:block, has:approval, score&lt;0.5, model:name, tool:name, time:7d and press
        Enter.
      </p>
    </div>
  )
}

function FilterEditor({
  keys,
  initial,
  state,
  options,
  onApply,
  onClose,
}: {
  keys: FilterKey[]
  initial: FilterKey
  state: FilterState
  options: FilterOptions
  onApply: (s: FilterState) => void
  onClose: () => void
}) {
  const [key, setKey] = useState<FilterKey>(initial)
  const [draft, setDraft] = useState<FilterState>(state)
  const firstRef = useRef<HTMLElement | null>(null)
  useEffect(() => {
    setTimeout(() => firstRef.current?.focus(), 0)
  }, [key])
  const listId = `fl-${key}`
  const suggestions = key === 'agent' ? options.agents : key === 'model' ? options.models : key === 'tool' ? options.tools : undefined
  const textField = (field: 'agent' | 'model' | 'tool' | 'q', label: string) => (
    <label className="field">
      {label}
      <input
        ref={(el) => {
          firstRef.current = el
        }}
        className="input"
        list={suggestions?.length ? listId : undefined}
        value={(draft[field] as string | undefined) ?? ''}
        onChange={(e) => setDraft({ ...draft, [field]: e.target.value || undefined })}
      />
      {suggestions?.length ? (
        <datalist id={listId}>
          {suggestions.map((s) => (
            <option key={s} value={s} />
          ))}
        </datalist>
      ) : null}
    </label>
  )
  const radios = <T extends string>(name: string, value: T | undefined, labels: Record<T, string>, set: (v: T) => void) => (
    <fieldset className="radios">
      <legend className="sr-only">{name}</legend>
      {(Object.keys(labels) as T[]).map((k, i) => (
        <label key={k} className="checkbox">
          <input
            ref={
              i === 0
                ? (el) => {
                    firstRef.current = el
                  }
                : undefined
            }
            type="radio"
            name={name}
            checked={value === k}
            onChange={() => set(k)}
          />
          {labels[k]}
        </label>
      ))}
    </fieldset>
  )
  let body: ReactNode = null
  switch (key) {
    case 'agent':
      body = textField('agent', 'Sessions where this agent took part')
      break
    case 'model':
      body = textField('model', 'A model that was used')
      break
    case 'tool':
      body = textField('tool', 'A tool that was called')
      break
    case 'q':
      body = textField('q', 'Text in the title, id, agents or team')
      break
    case 'team':
      body = options.teams?.length ? (
        <label className="field">
          Team
          <select
            ref={(el) => {
              firstRef.current = el
            }}
            className="input"
            value={draft.team ?? ''}
            onChange={(e) => setDraft({ ...draft, team: e.target.value || undefined })}
          >
            <option value="">Any team</option>
            {options.teams.map((t) => (
              <option key={t.id} value={t.id}>
                {t.name || t.id}
              </option>
            ))}
          </select>
        </label>
      ) : (
        <label className="field">
          Team id
          <input
            ref={(el) => {
              firstRef.current = el
            }}
            className="input"
            value={draft.team ?? ''}
            onChange={(e) => setDraft({ ...draft, team: e.target.value || undefined })}
          />
        </label>
      )
      break
    case 'status':
      body = radios<StatusFilter>('status', draft.status, STATUS_LABEL, (v) => setDraft({ ...draft, status: v }))
      break
    case 'has':
      body = radios<HasFilter>('has', draft.has, HAS_LABEL, (v) => setDraft({ ...draft, has: v }))
      break
    case 'score':
      body = (
        <div className="row wrap" style={{ alignItems: 'flex-end' }}>
          <label className="field">
            Score below (0 to 1)
            <input
              ref={(el) => {
                firstRef.current = el
              }}
              className="input"
              type="number"
              min={0}
              max={1}
              step={0.05}
              value={draft.scoreBelow?.value ?? ''}
              onChange={(e) => setDraft({ ...draft, scoreBelow: e.target.value === '' ? undefined : { ...draft.scoreBelow, value: Number(e.target.value) } })}
            />
          </label>
          <label className="field">
            Scorer (optional)
            <input
              className="input"
              list="fl-scorers"
              value={draft.scoreBelow?.scorer ?? ''}
              placeholder="any scorer"
              onChange={(e) => setDraft({ ...draft, scoreBelow: { value: draft.scoreBelow?.value ?? 0.5, scorer: e.target.value || undefined } })}
            />
            <datalist id="fl-scorers">
              {(options.scorers ?? ['GoalCompletionJudge', 'ReasoningConsistencyJudge', 'ToolSafetyScorer']).map((s) => (
                <option key={s} value={s} />
              ))}
            </datalist>
          </label>
        </div>
      )
      break
    case 'cost':
      body = (
        <label className="field">
          Cost at least (USD)
          <input
            ref={(el) => {
              firstRef.current = el
            }}
            className="input"
            type="number"
            min={0}
            step="any"
            value={draft.costMin ?? ''}
            onChange={(e) => setDraft({ ...draft, costMin: e.target.value === '' ? undefined : Number(e.target.value) })}
          />
        </label>
      )
      break
    case 'time':
      body = (
        <div className="row wrap">
          <label className="field">
            From
            <input
              ref={(el) => {
                firstRef.current = el
              }}
              className="input"
              type="datetime-local"
              value={toLocalInput(draft.from)}
              onChange={(e) => setDraft({ ...draft, time: 'custom', from: fromLocalInput(e.target.value) })}
            />
          </label>
          <label className="field">
            To (empty: now)
            <input
              className="input"
              type="datetime-local"
              value={toLocalInput(draft.to)}
              onChange={(e) => setDraft({ ...draft, time: 'custom', to: fromLocalInput(e.target.value) })}
            />
          </label>
        </div>
      )
      break
  }
  return (
    <form
      className="filter-editor"
      aria-label={`Edit the ${FILTER_KEY_LABEL[key]} filter`}
      onSubmit={(e) => {
        e.preventDefault()
        onApply(draft)
      }}
      onKeyDown={(e) => {
        if (e.key === 'Escape') {
          e.stopPropagation()
          onClose()
        }
      }}
    >
      <div className="filter-editor-keys" role="tablist" aria-label="Filter kind">
        {keys.map((k) => (
          <button key={k} type="button" role="tab" aria-selected={k === key} className={`seg-btn ${k === key ? 'on' : ''}`} onClick={() => setKey(k)}>
            {k === 'time' ? TIME_LABEL.custom : FILTER_KEY_LABEL[k]}
          </button>
        ))}
      </div>
      <div className="filter-editor-body">{body}</div>
      <div className="row" style={{ justifyContent: 'flex-end' }}>
        <button type="button" className="btn sm ghost" onClick={onClose}>
          Cancel
        </button>
        <button type="submit" className="btn sm primary">
          Apply
        </button>
      </div>
    </form>
  )
}

function toLocalInput(iso?: string): string {
  if (!iso) return ''
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return ''
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`
}

function fromLocalInput(v: string): string | undefined {
  if (!v) return undefined
  const d = new Date(v)
  return Number.isNaN(d.getTime()) ? undefined : d.toISOString()
}

/** Widens the default range (24 h → 7 d → 30 d → all) until `probe` finds data; explicit ranges are left alone. */
export function useWidenedTime(
  state: FilterState,
  defaultTime: TimeRange,
  probe: (range: TimeRange) => Promise<number>,
  keyParts: unknown[],
): { time: TimeRange; widened: boolean; ready: boolean } {
  const explicit = !!state.time
  const q = useQuery({
    queryKey: ['widen', ...keyParts, defaultTime],
    enabled: !explicit,
    staleTime: 30_000,
    queryFn: async () => {
      const seq = (['24h', '7d', '30d', 'all'] as TimeRange[]).slice(Math.max(0, ['24h', '7d', '30d', 'all'].indexOf(defaultTime)))
      for (const r of seq) if ((await probe(r)) > 0) return r
      return defaultTime
    },
  })
  if (explicit) return { time: state.time!, widened: false, ready: true }
  return { time: q.data ?? defaultTime, widened: !!q.data && q.data !== defaultTime, ready: !q.isLoading }
}

export function WidenedNote({ time, defaultTime, onReset }: { time: TimeRange; defaultTime: TimeRange; onReset: () => void }) {
  return (
    <p className="widened-note" role="status">
      <Icon name="clock" size={14} /> Showing <strong>{TIME_LABEL[time].toLowerCase()}</strong> because nothing happened in the {TIME_LABEL[defaultTime].toLowerCase()}.{' '}
      <button type="button" className="linkish" onClick={onReset}>
        Keep {TIME_LABEL[defaultTime].toLowerCase()}
      </button>
    </p>
  )
}
