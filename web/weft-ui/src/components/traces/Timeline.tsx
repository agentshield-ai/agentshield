import { useMemo } from 'react'
import { callStatus, fmtDuration } from '../../lib/format'
import { callLabel, flattenTree, type TreeNode } from './callUtils'

const STATUS_COLOR = {
  success: 'var(--c1)',
  error: 'var(--error)',
  running: 'var(--running)',
  descendant_error: 'var(--warn)',
} as const

/** Gantt-style view: one bar per call, positioned relative to the earliest start. */
export function Timeline({ roots, selectedId, onSelect }: { roots: TreeNode[]; selectedId: string; onSelect: (id: string) => void }) {
  const rows = useMemo(() => flattenTree(roots), [roots])
  const { t0, span } = useMemo(() => {
    let min = Infinity
    let max = -Infinity
    const now = Date.now()
    for (const n of rows) {
      const s = new Date(n.call.started_at).getTime()
      const e = n.call.ended_at ? new Date(n.call.ended_at).getTime() : now
      min = Math.min(min, s)
      max = Math.max(max, e)
    }
    return { t0: min, span: Math.max(1, max - min) }
  }, [rows])

  const ticks = [0, 0.25, 0.5, 0.75, 1]
  return (
    <div className="timeline" role="list" aria-label="Trace timeline">
      <div className="tl-row tl-axis">
        <div className="tl-label faint small">{rows.length} call{rows.length === 1 ? '' : 's'}</div>
        <div className="tl-track">
          {ticks.map((t) => (
            <span key={t} className="tl-tick" style={{ left: `${t * 100}%` }}>
              {t === 0 ? '0' : fmtDuration(t * span)}
            </span>
          ))}
        </div>
      </div>
      {rows.map((n) => {
        const c = n.call
        const s = new Date(c.started_at).getTime() - t0
        const e = (c.ended_at ? new Date(c.ended_at).getTime() : Date.now()) - t0
        const left = (s / span) * 100
        const width = Math.max(0.4, ((e - s) / span) * 100)
        const st = callStatus(c)
        return (
          <div
            key={c.id}
            role="listitem"
            className={`tl-row ${c.id === selectedId ? 'selected' : ''}`}
            onClick={() => onSelect(c.id)}
            title={`${callLabel(c)} · starts +${fmtDuration(s)} · ${fmtDuration(e - s)}`}
          >
            <div className="tl-label" style={{ paddingLeft: 4 + n.depth * 10 }}>
              {callLabel(c)}
            </div>
            <div className="tl-track">
              {ticks.slice(1, -1).map((t) => (
                <span key={t} className="tl-grid" style={{ left: `${t * 100}%` }} />
              ))}
              <div
                className={`tl-bar ${c.ended_at ? '' : 'running'}`}
                style={{ left: `${left}%`, width: `${width}%`, background: STATUS_COLOR[st] }}
              />
              <span
                className={`tl-dur ${left + width > 80 ? 'inside' : ''}`}
                style={left + width > 80 ? { right: `calc(${100 - left - width}% + 4px)` } : { left: `calc(${left + width}% + 4px)` }}
              >
                {fmtDuration(e - s)}
              </span>
            </div>
          </div>
        )
      })}
    </div>
  )
}
