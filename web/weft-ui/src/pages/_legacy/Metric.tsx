// TEMPORARY: the old number tile, kept only so the Team, Agent and Security
// pages render until they are redesigned (DESIGN.md §7.1 rule 1: no rows of
// number tiles; a figure appears where it answers something). Do not use it
// on a new or redesigned page; delete this file with its last user.
import type { ReactNode } from 'react'
import { Tip } from '../../components/ui/bits'

export function Metric({ label, value, tip, sub, tone }: { label: string; value: ReactNode; tip?: ReactNode; sub?: ReactNode; tone?: 'error' | 'warn' | 'ok' }) {
  return (
    <div className={`metric ${tone ? `tone-${tone}` : ''}`}>
      <div className="metric-label">
        {label}
        {tip && <Tip text={tip} label={`About ${label.toLowerCase()}`} />}
      </div>
      <div className="metric-value">{value}</div>
      {sub && <div className="metric-sub">{sub}</div>}
    </div>
  )
}
