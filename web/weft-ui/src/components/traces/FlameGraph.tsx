import { useMemo, useState } from 'react'
import { callStatus, fmtDuration } from '../../lib/format'
import { callLabel, type TreeNode } from './callUtils'
import { composeOps, flameLayout, flattenComposed } from './flameLayout'
import { opStyle } from './opStyle'
import './traceViews.css'

const PALETTE = ['var(--c1)', 'var(--c2)', 'var(--c6)', 'var(--c5)', 'var(--c4)', 'var(--c3)']
const ROW_H = 22

/** Stable colour per op label. */
export function opColor(label: string): string {
  let h = 0
  for (let i = 0; i < label.length; i++) h = (h * 31 + label.charCodeAt(i)) | 0
  return PALETTE[Math.abs(h) % PALETTE.length]
}

/**
 * Flame graph (icicle): one row per tree depth, bar width is wall time.
 * Click selects a call; double-click zooms to it.
 */
export function FlameGraph({ roots, selectedId, onSelect }: { roots: TreeNode[]; selectedId: string; onSelect: (id: string) => void }) {
  const layout = useMemo(() => flameLayout(roots), [roots])
  const [zoom, setZoom] = useState<{ x0: number; x1: number; label: string } | null>(null)
  if (!layout.rects.length) return <div className="empty small">No calls.</div>
  const z0 = zoom?.x0 ?? 0
  const zw = Math.max(1e-9, (zoom?.x1 ?? 1) - z0)
  const ticks = [0, 0.25, 0.5, 0.75, 1]
  return (
    <div className="flame-wrap">
      <div className="tgraph-legend">
        <span>
          {layout.rects.length} call{layout.rects.length === 1 ? '' : 's'} · {fmtDuration(layout.spanMs)}
        </span>
        <span className="grow" />
        {zoom ? (
          <button className="btn ghost sm" onClick={() => setZoom(null)}>
            Reset zoom ({zoom.label})
          </button>
        ) : (
          <span>Click to select · double-click to zoom</span>
        )}
      </div>
      <div className="flame-axis">
        {ticks.map((t) => (
          <span key={t} style={{ left: `${t * 100}%` }}>
            {fmtDuration((z0 + t * zw) * layout.spanMs)}
          </span>
        ))}
      </div>
      <div className="flame" role="list" aria-label="Flame graph" style={{ height: layout.depth * ROW_H }}>
        {layout.rects.map((r) => {
          const left = (r.x0 - z0) / zw
          const width = (r.x1 - r.x0) / zw
          if (left + width < 0 || left > 1) return null
          const label = callLabel(r.call)
          const st = callStatus(r.call)
          const bg = st === 'error' ? 'var(--error)' : (opStyle(r.call).color ?? opColor(label))
          const l = Math.max(0, left)
          const w = Math.min(1, left + width) - l
          return (
            <div
              key={r.id}
              role="listitem"
              className={`flame-bar ${r.id === selectedId ? 'selected' : ''} ${r.call.ended_at ? '' : 'running'}`}
              style={{ left: `${l * 100}%`, width: `max(${w * 100}%, 2px)`, top: r.depth * ROW_H, background: bg }}
              title={`${label} · ${fmtDuration(r.ms)} · starts +${fmtDuration(r.x0 * layout.spanMs)}`}
              onClick={() => onSelect(r.id)}
              onDoubleClick={() => setZoom({ x0: r.x0, x1: Math.max(r.x1, r.x0 + 1e-6), label })}
            >
              <span>
                {label} <span className="flame-ms">{fmtDuration(r.ms)}</span>
              </span>
            </div>
          )
        })}
      </div>
    </div>
  )
}

/**
 * Code composition: the trace folded by op, like reading the program that
 * produced it. Repeated sibling calls merge (×count, total time); click a
 * line to step through its calls.
 */
export function CodeComposition({ roots, selectedId, onSelect }: { roots: TreeNode[]; selectedId: string; onSelect: (id: string) => void }) {
  const ops = useMemo(() => composeOps(roots, callLabel), [roots])
  const [closed, setClosed] = useState<Set<string>>(new Set())
  const rows = flattenComposed(ops, (k) => !closed.has(k))
  const rootMs = ops.reduce((s, o) => s + o.totalMs, 0) || 1
  if (!rows.length) return <div className="empty small">No calls.</div>
  return (
    <div className="code-comp" role="tree" aria-label="Code composition">
      <div className="tgraph-legend">
        <span>
          {rows.length} op line{rows.length === 1 ? '' : 's'} folded from the call tree
        </span>
        <span className="grow" />
        <span>Click a line to step through its calls</span>
      </div>
      {rows.map((o) => {
        const sel = o.callIds.indexOf(selectedId)
        const next = o.callIds[(sel + 1) % o.callIds.length]
        const open = !closed.has(o.key)
        return (
          <div
            key={o.key}
            role="treeitem"
            aria-expanded={o.children.length ? open : undefined}
            aria-selected={sel >= 0}
            className={`code-line ${sel >= 0 ? 'selected' : ''}`}
            onClick={() => onSelect(next)}
            title={`${o.key}\n${o.count} call${o.count === 1 ? '' : 's'} · ${fmtDuration(o.totalMs)} total`}
          >
            <span className="code-indent" style={{ width: o.depth * 16 }} />
            <button
              className="code-toggle"
              aria-label={open ? 'Collapse' : 'Expand'}
              style={{ visibility: o.children.length ? 'visible' : 'hidden' }}
              onClick={(e) => {
                e.stopPropagation()
                const n = new Set(closed)
                if (open) n.add(o.key)
                else n.delete(o.key)
                setClosed(n)
              }}
            >
              {open ? '▾' : '▸'}
            </button>
            <span className="code-swatch" style={{ background: opColor(o.label) }} />
            <span className="mono code-name">
              {o.label}
              <span className="faint">()</span>
            </span>
            {o.count > 1 && <span className="badge">×{o.count}</span>}
            {o.errors > 0 && <span className="badge error">{o.errors} err</span>}
            {sel >= 0 && o.count > 1 && (
              <span className="small faint">
                {sel + 1}/{o.count}
              </span>
            )}
            <span className="grow" />
            <span className="code-share" aria-hidden>
              <span style={{ width: `${Math.min(1, o.totalMs / rootMs) * 100}%` }} />
            </span>
            <span className="small mono muted code-ms">{fmtDuration(o.totalMs)}</span>
          </div>
        )
      })}
    </div>
  )
}
