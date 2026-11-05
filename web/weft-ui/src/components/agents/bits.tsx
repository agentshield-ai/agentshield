import { Bar, BarChart, Line, LineChart, ResponsiveContainer, YAxis } from 'recharts'
import { fmtScore, type TrendPoint } from './signalUtils'

export const TOOLTIP_STYLE = { background: 'var(--panel)', border: '1px solid var(--border)', borderRadius: 8, fontSize: 12, color: 'var(--text)' }

/** Tiny trend chart in the accent hue: a line for ratings, bars for tag counts. */
export function Sparkline({ points, kind, width = 90, height = 22 }: { points: TrendPoint[]; kind: 'rating' | 'tag'; width?: number; height?: number }) {
  if (!points.some((p) => p.count > 0)) return <span className="faint small">—</span>
  return (
    <div style={{ width, height }} aria-hidden>
      <ResponsiveContainer width="100%" height="100%">
        {kind === 'rating' ? (
          <LineChart data={points} margin={{ top: 3, right: 3, bottom: 3, left: 3 }}>
            <YAxis hide domain={[0, (max: number) => Math.max(1, max)]} />
            <Line dataKey="value" stroke="var(--accent)" dot={{ r: 1.5, fill: 'var(--accent)', strokeWidth: 0 }} strokeWidth={1.5} isAnimationActive={false} connectNulls />
          </LineChart>
        ) : (
          <BarChart data={points} margin={{ top: 0, right: 0, bottom: 0, left: 0 }}>
            <Bar dataKey="value" fill="var(--accent)" isAnimationActive={false} />
          </BarChart>
        )}
      </ResponsiveContainer>
    </div>
  )
}

/** A 0–1 rating as text ("0.60"), with its reason and confidence as a tooltip. No pill, no colour. */
export function ScoreText({ value, confidence, reason }: { value: number | null; confidence?: number | null; reason?: string | null }) {
  const title = [reason, confidence != null ? `confidence ${Math.round(confidence * 100)}%` : null].filter(Boolean).join(' · ')
  return (
    <span className="score-text" title={title || undefined}>
      {fmtScore(value)}
    </span>
  )
}
