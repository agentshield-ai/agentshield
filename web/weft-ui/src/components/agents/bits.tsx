import { Bar, BarChart, Line, LineChart, ResponsiveContainer, YAxis } from 'recharts'
import { RANGES } from './agentUtils'
import { fmtScore, scoreTone, type TrendPoint } from './signalUtils'

export const TOOLTIP_STYLE = { background: 'var(--panel)', border: '1px solid var(--border)', borderRadius: 6, fontSize: 12, color: 'var(--text)' }

/** Time window selector shared by the agent tabs. */
export function RangePicker({ value, onChange, allowAll }: { value: string; onChange: (r: string) => void; allowAll?: boolean }) {
  const ranges: string[] = allowAll ? [...RANGES, 'all'] : [...RANGES]
  return (
    <div className="seg" role="group" aria-label="Time range">
      {ranges.map((r) => (
        <button key={r} className={r === value ? 'on' : ''} aria-pressed={r === value} onClick={() => onChange(r)}>
          {r}
        </button>
      ))}
    </div>
  )
}

/** Horizontal bar list for top-N breakdowns. */
export function TopList({ items, onPick, fmt = (n) => String(n), empty = 'No data in this period.' }: { items: { key: string; value: number; sub?: string; bad?: number }[]; onPick?: (key: string) => void; fmt?: (n: number) => string; empty?: string }) {
  if (!items.length) return <span className="small muted">{empty}</span>
  const max = Math.max(...items.map((i) => i.value), 1)
  return (
    <div className="col" style={{ gap: 6 }}>
      {items.map((it) => (
        <div key={it.key} className="tool-bar-row">
          {onPick ? (
            <button className="linkish mono truncate" style={{ textAlign: 'left' }} title={it.key} onClick={() => onPick(it.key)}>
              {it.key || '(none)'}
            </button>
          ) : (
            <span className="mono truncate" title={it.key}>
              {it.key || '(none)'}
            </span>
          )}
          <div className="tool-bar-track">
            <span style={{ width: `${((it.value - (it.bad ?? 0)) / max) * 100}%`, background: 'var(--c1)' }} />
            {!!it.bad && <span style={{ width: `${(it.bad / max) * 100}%`, background: 'var(--error)' }} />}
          </div>
          <span className="small mono muted" style={{ whiteSpace: 'nowrap' }}>
            {fmt(it.value)}
            {it.sub ? ` · ${it.sub}` : ''}
          </span>
        </div>
      ))}
    </div>
  )
}

/** Tiny trend chart: a line for ratings, bars for tag counts. */
export function Sparkline({ points, kind, width = 90, height = 22 }: { points: TrendPoint[]; kind: 'rating' | 'tag'; width?: number; height?: number }) {
  if (!points.some((p) => p.count > 0)) return <span className="faint small">—</span>
  return (
    <div style={{ width, height }} aria-hidden>
      <ResponsiveContainer width="100%" height="100%">
        {kind === 'rating' ? (
          <LineChart data={points} margin={{ top: 3, right: 3, bottom: 3, left: 3 }}>
            <YAxis hide domain={[0, (max: number) => Math.max(1, max)]} />
            <Line dataKey="value" stroke="var(--c2)" dot={{ r: 1.5, fill: 'var(--c2)', strokeWidth: 0 }} strokeWidth={1.5} isAnimationActive={false} connectNulls />
          </LineChart>
        ) : (
          <BarChart data={points} margin={{ top: 0, right: 0, bottom: 0, left: 0 }}>
            <Bar dataKey="value" fill="var(--c4)" isAnimationActive={false} />
          </BarChart>
        )}
      </ResponsiveContainer>
    </div>
  )
}

/** A 0–1 rating with a small bar. */
export function ScorePill({ value, confidence, reason }: { value: number | null; confidence?: number | null; reason?: string | null }) {
  const tone = scoreTone(value)
  const title = [reason, confidence != null ? `confidence ${Math.round(confidence * 100)}%` : null].filter(Boolean).join(' · ')
  return (
    <span className={`score-pill tone-${tone}`} title={title || undefined}>
      <span className="score-bar">
        <span style={{ width: `${Math.max(0, Math.min(1, value ?? 0)) * 100}%` }} />
      </span>
      <span className="mono">{fmtScore(value)}</span>
    </span>
  )
}
