import { useEffect, useId, useRef, useState } from 'react'
import type { Operand, Operation } from '../../api/types'
import { useFlip } from './Menu'

// Field + operator + value clauses, ANDed into a calls `query.$expr`.

export type FilterOp = 'eq' | 'neq' | 'contains' | 'not_contains' | 'gt' | 'gte' | 'lt' | 'lte' | 'in' | 'exists' | 'not_exists'

export interface FilterClause {
  field: string
  op: FilterOp
  value: string
}

export const FILTER_OPS: { id: FilterOp; label: string; noValue?: boolean }[] = [
  { id: 'eq', label: '=' },
  { id: 'neq', label: '≠' },
  { id: 'contains', label: 'contains' },
  { id: 'not_contains', label: 'does not contain' },
  { id: 'gt', label: '>' },
  { id: 'gte', label: '≥' },
  { id: 'lt', label: '<' },
  { id: 'lte', label: '≤' },
  { id: 'in', label: 'in (comma list)' },
  { id: 'exists', label: 'exists', noValue: true },
  { id: 'not_exists', label: 'does not exist', noValue: true },
]

export const BASE_FILTER_FIELDS = [
  'op_name',
  'display_name',
  'id',
  'trace_id',
  'parent_id',
  'thread_id',
  'turn_id',
  'exception',
  'wb_user_id',
  'started_at',
  'ended_at',
  'summary.weave.status',
  'summary.weave.latency_ms',
  'summary.weave.trace_name',
  'summary.total_tokens',
]

const NUMERIC_FIELDS = new Set(['summary.weave.latency_ms', 'summary.total_tokens'])

const fieldOp = (path: string): Operand => ({ $getField: path })
const lit = (v: unknown): Operand => ({ $literal: v })
const dbl = (path: string): Operand => ({ $convert: { input: fieldOp(path), to: 'double' } })

function typedLiterals(raw: string): unknown[] {
  const v = raw.trim()
  const out: unknown[] = [raw]
  if (v !== '' && !Number.isNaN(Number(v))) out.unshift(Number(v))
  if (v === 'true' || v === 'false') out.unshift(v === 'true')
  if (v === 'null') out.unshift(null)
  return out
}

function eqOp(field: string, raw: string): Operation {
  if (NUMERIC_FIELDS.has(field) && !Number.isNaN(Number(raw))) return { $eq: [dbl(field), lit(Number(raw))] }
  const lits = typedLiterals(raw)
  if (lits.length === 1) return { $eq: [fieldOp(field), lit(lits[0])] }
  // value is ambiguous (e.g. "42" could be stored as number or string): match either
  return { $or: lits.map((l) => ({ $eq: [fieldOp(field), lit(l)] }) as Operation) }
}

function isTimeField(f: string) {
  return f === 'started_at' || f === 'ended_at'
}

function cmpOperands(field: string, raw: string): [Operand, Operand] {
  if (isTimeField(field)) {
    const d = new Date(raw)
    return [fieldOp(field), lit(Number.isNaN(d.getTime()) ? raw : d.toISOString())]
  }
  return [dbl(field), lit(Number(raw))]
}

export function clauseToOperation(c: FilterClause): Operation {
  switch (c.op) {
    case 'eq':
      return eqOp(c.field, c.value)
    case 'neq':
      return { $not: [eqOp(c.field, c.value)] }
    case 'contains':
      return { $contains: { input: fieldOp(c.field), substr: lit(c.value), case_insensitive: true } }
    case 'not_contains':
      return { $not: [{ $contains: { input: fieldOp(c.field), substr: lit(c.value), case_insensitive: true } }] }
    case 'gt':
      return { $gt: cmpOperands(c.field, c.value) }
    case 'gte':
      return { $gte: cmpOperands(c.field, c.value) }
    case 'lt':
      return { $lt: cmpOperands(c.field, c.value) }
    case 'lte':
      return { $lte: cmpOperands(c.field, c.value) }
    case 'in': {
      const vals = c.value
        .split(',')
        .map((s) => s.trim())
        .filter(Boolean)
      return { $in: [fieldOp(c.field), vals.flatMap((v) => typedLiterals(v).map(lit))] }
    }
    case 'exists':
      return { $eq: [{ $convert: { input: fieldOp(c.field), to: 'exists' } }, lit(true)] }
    case 'not_exists':
      return { $eq: [{ $convert: { input: fieldOp(c.field), to: 'exists' } }, lit(false)] }
  }
}

export function clauseValid(c: FilterClause): boolean {
  if (!c.field.trim()) return false
  const op = FILTER_OPS.find((o) => o.id === c.op)
  if (op?.noValue) return true
  if (['gt', 'gte', 'lt', 'lte'].includes(c.op)) return isTimeField(c.field) ? !Number.isNaN(new Date(c.value).getTime()) : c.value.trim() !== '' && !Number.isNaN(Number(c.value))
  return c.value !== ''
}

export function encodeClauses(cs: FilterClause[]): string {
  return cs.length ? JSON.stringify(cs.map((c) => [c.field, c.op, c.value])) : ''
}

export function decodeClauses(s: string | null): FilterClause[] {
  if (!s) return []
  try {
    const arr = JSON.parse(s) as [string, FilterOp, string][]
    return arr.filter((x) => Array.isArray(x) && typeof x[0] === 'string').map(([field, op, value]) => ({ field, op, value: value ?? '' }))
  } catch {
    return []
  }
}

export function clauseLabel(c: FilterClause): string {
  const op = FILTER_OPS.find((o) => o.id === c.op)
  return `${c.field} ${op?.label ?? c.op}${op?.noValue ? '' : ` ${c.value}`}`
}

// ---------------------------------------------------------------- UI

function ClauseEditor({
  initial,
  fields,
  onSave,
  onCancel,
}: {
  initial: FilterClause
  fields: string[]
  onSave: (c: FilterClause) => void
  onCancel: () => void
}) {
  const [c, setC] = useState(initial)
  const listId = useId()
  const op = FILTER_OPS.find((o) => o.id === c.op)
  const valid = clauseValid(c)
  return (
    <form
      className="col filter-editor"
      onSubmit={(e) => {
        e.preventDefault()
        if (valid) onSave(c)
      }}
      onKeyDown={(e) => e.key === 'Escape' && onCancel()}
    >
      <label className="field">
        Field
        <input className="input mono" list={listId} autoFocus value={c.field} onChange={(e) => setC({ ...c, field: e.target.value })} placeholder="inputs.question" />
        <datalist id={listId}>
          {fields.map((f) => (
            <option key={f} value={f} />
          ))}
        </datalist>
      </label>
      <div className="row">
        <select className="input" value={c.op} onChange={(e) => setC({ ...c, op: e.target.value as FilterOp })} aria-label="Operator">
          {FILTER_OPS.map((o) => (
            <option key={o.id} value={o.id}>
              {o.label}
            </option>
          ))}
        </select>
        {!op?.noValue &&
          (c.field === 'summary.weave.status' ? (
            <select className="input grow" value={c.value} onChange={(e) => setC({ ...c, value: e.target.value })} aria-label="Value">
              <option value="">Choose…</option>
              {['success', 'error', 'running', 'descendant_error'].map((s) => (
                <option key={s}>{s}</option>
              ))}
            </select>
          ) : (
            <input
              className="input grow"
              value={c.value}
              onChange={(e) => setC({ ...c, value: e.target.value })}
              placeholder={isTimeField(c.field) ? '2026-01-01T00:00:00Z' : 'value'}
              aria-label="Value"
            />
          ))}
      </div>
      <div className="row" style={{ justifyContent: 'flex-end' }}>
        <button type="button" className="btn sm" onClick={onCancel}>
          Cancel
        </button>
        <button type="submit" className="btn sm primary" disabled={!valid}>
          Apply
        </button>
      </div>
    </form>
  )
}

/** Chips for the current clauses plus an "Add filter" popover. */
export function FilterBuilder({
  clauses,
  fields,
  onChange,
}: {
  clauses: FilterClause[]
  fields: string[]
  onChange: (cs: FilterClause[]) => void
}) {
  // index being edited; -1 = new clause; null = closed
  const [editing, setEditing] = useState<number | null>(null)
  const ref = useRef<HTMLDivElement>(null)
  const [menuRef, menuStyle] = useFlip(editing != null)
  useEffect(() => {
    if (editing == null) return
    const h = (e: MouseEvent) => ref.current && !ref.current.contains(e.target as Node) && setEditing(null)
    document.addEventListener('mousedown', h)
    return () => document.removeEventListener('mousedown', h)
  }, [editing])
  return (
    <div className="row wrap filter-bar" ref={ref}>
      {clauses.map((c, i) => (
        <span key={i} className="filter-chip">
          <button className="filter-chip-label mono" onClick={() => setEditing(i)} title="Edit filter">
            {clauseLabel(c)}
          </button>
          <button className="filter-chip-x" aria-label="Remove filter" onClick={() => onChange(clauses.filter((_, j) => j !== i))}>
            ✕
          </button>
        </span>
      ))}
      <div className="dd">
        <button className="btn sm" onClick={() => setEditing(editing === -1 ? null : -1)} aria-expanded={editing === -1}>
          + Filter
        </button>
        {editing != null && (
          <div className="dd-menu" ref={menuRef} style={{ width: 340, padding: 12, ...menuStyle }}>
            <ClauseEditor
              key={editing}
              initial={editing >= 0 ? clauses[editing] : { field: '', op: 'eq', value: '' }}
              fields={fields}
              onCancel={() => setEditing(null)}
              onSave={(c) => {
                onChange(editing >= 0 ? clauses.map((x, j) => (j === editing ? c : x)) : [...clauses, c])
                setEditing(null)
              }}
            />
          </div>
        )}
      </div>
      {clauses.length > 1 && (
        <button className="btn ghost sm" onClick={() => onChange([])}>
          Clear all
        </button>
      )}
    </div>
  )
}
