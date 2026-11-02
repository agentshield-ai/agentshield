import { useEffect, useMemo, useRef, useState } from 'react'
import type { Call } from '../../api/types'
import { opDisplayName } from '../../api/refs'
import { callLatencyMs, callStatus, callTokens, fmtDuration, fmtNumber } from '../../lib/format'
import { StatusDot } from '../common'
import { callLabel, flattenTree, type TreeNode } from './callUtils'

function matches(c: Call, q: string) {
  const s = q.toLowerCase()
  return (
    callLabel(c).toLowerCase().includes(s) ||
    opDisplayName(c.op_name).toLowerCase().includes(s) ||
    c.id.toLowerCase().startsWith(s) ||
    (!!c.exception && c.exception.toLowerCase().includes(s))
  )
}

/** Collapsible call tree with search and arrow-key navigation. */
export function TraceTree({
  roots,
  selectedId,
  onSelect,
  collapsed,
  setCollapsed,
}: {
  roots: TreeNode[]
  selectedId: string
  onSelect: (id: string) => void
  collapsed: Set<string>
  setCollapsed: (s: Set<string>) => void
}) {
  const [q, setQ] = useState('')
  const listRef = useRef<HTMLDivElement>(null)

  const parentOf = useMemo(() => {
    const m = new Map<string, string>()
    const walk = (ns: TreeNode[]) => {
      for (const n of ns) {
        for (const ch of n.children) m.set(ch.call.id, n.call.id)
        walk(n.children)
      }
    }
    walk(roots)
    return m
  }, [roots])

  const visibleIds = useMemo(() => {
    if (!q.trim()) return null
    const keep = new Set<string>()
    const walk = (ns: TreeNode[]) => {
      for (const n of ns) {
        if (matches(n.call, q.trim())) {
          let id: string | undefined = n.call.id
          while (id && !keep.has(id)) {
            keep.add(id)
            id = parentOf.get(id)
          }
        }
        walk(n.children)
      }
    }
    walk(roots)
    return keep
  }, [q, roots, parentOf])

  const rows = useMemo(() => {
    const flat = flattenTree(roots, (id) => (visibleIds ? true : !collapsed.has(id)))
    return visibleIds ? flat.filter((n) => visibleIds.has(n.call.id)) : flat
  }, [roots, collapsed, visibleIds])

  // Expand ancestors of the selected node so it is visible.
  useEffect(() => {
    let id = parentOf.get(selectedId)
    let changed = false
    const next = new Set(collapsed)
    while (id) {
      if (next.delete(id)) changed = true
      id = parentOf.get(id)
    }
    if (changed) setCollapsed(next)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [selectedId, parentOf])

  useEffect(() => {
    const el = listRef.current?.querySelector<HTMLElement>(`[data-id="${CSS.escape(selectedId)}"]`)
    el?.scrollIntoView({ block: 'nearest' })
  }, [selectedId, rows.length])

  const toggle = (id: string) => {
    const next = new Set(collapsed)
    if (next.has(id)) next.delete(id)
    else next.add(id)
    setCollapsed(next)
  }

  const onKeyDown = (e: React.KeyboardEvent) => {
    const i = rows.findIndex((r) => r.call.id === selectedId)
    const cur = rows[i]
    if (e.key === 'ArrowDown' || e.key === 'j') {
      e.preventDefault()
      if (i < rows.length - 1) onSelect(rows[i + 1].call.id)
    } else if (e.key === 'ArrowUp' || e.key === 'k') {
      e.preventDefault()
      if (i > 0) onSelect(rows[i - 1].call.id)
    } else if (e.key === 'ArrowLeft' && cur) {
      e.preventDefault()
      if (cur.children.length && !collapsed.has(cur.call.id)) toggle(cur.call.id)
      else {
        const p = parentOf.get(cur.call.id)
        if (p) onSelect(p)
      }
    } else if (e.key === 'ArrowRight' && cur) {
      e.preventDefault()
      if (cur.children.length && collapsed.has(cur.call.id)) toggle(cur.call.id)
      else if (cur.children.length) onSelect(cur.children[0].call.id)
    }
  }

  const allIds = useMemo(() => flattenTree(roots).filter((n) => n.children.length).map((n) => n.call.id), [roots])

  return (
    <div className="col" style={{ gap: 0, minHeight: 0 }}>
      <div className="row tree-toolbar">
        <input className="input grow" placeholder="Search calls in trace…" value={q} onChange={(e) => setQ(e.target.value)} aria-label="Search trace" />
        <button className="btn ghost sm icon" title="Expand all" onClick={() => setCollapsed(new Set())}>
          ⊞
        </button>
        <button className="btn ghost sm icon" title="Collapse all" onClick={() => setCollapsed(new Set(allIds))}>
          ⊟
        </button>
      </div>
      <div className="tree" ref={listRef} tabIndex={0} onKeyDown={onKeyDown} role="tree" aria-label="Trace tree">
        {rows.length === 0 && <div className="empty small">No calls match “{q}”.</div>}
        {rows.map((n) => {
          const c = n.call
          const st = callStatus(c)
          const tokens = callTokens(c).total
          const open = visibleIds ? true : !collapsed.has(c.id)
          return (
            <div
              key={c.id}
              data-id={c.id}
              role="treeitem"
              aria-selected={c.id === selectedId}
              aria-expanded={n.children.length ? open : undefined}
              className={`tree-row ${c.id === selectedId ? 'selected' : ''}`}
              style={{ paddingLeft: 6 + n.depth * 14 }}
              onClick={() => onSelect(c.id)}
            >
              <span
                className="tree-toggle"
                onClick={(e) => {
                  e.stopPropagation()
                  if (n.children.length) toggle(c.id)
                }}
              >
                {n.children.length ? (open ? '▾' : '▸') : ''}
              </span>
              <StatusDot status={st} />
              <span className="tree-label" title={c.op_name}>
                {callLabel(c)}
                {!open && n.children.length > 0 && <span className="faint small"> ({n.children.length})</span>}
              </span>
              <span className="tree-meta">
                {tokens > 0 && <span title="Tokens">{fmtNumber(tokens)}t</span>}
                <span>{fmtDuration(callLatencyMs(c))}</span>
              </span>
            </div>
          )
        })}
      </div>
    </div>
  )
}
