import { useState } from 'react'
import { Modal } from '../common'
import { customRange, TIME_RANGES, timeRangeBounds, timeRangeLabel } from './dashboardUtils'

function toLocalInput(iso?: string): string {
  if (!iso) return ''
  const d = new Date(iso)
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`
}

/** Preset relative ranges plus a custom absolute range. */
export function TimeRangePicker({ value, onChange, allowInherit, label = 'Time range' }: { value: string; onChange: (v: string) => void; allowInherit?: boolean; label?: string }) {
  const [custom, setCustom] = useState<{ start: string; end: string } | null>(null)
  const isCustom = value.startsWith('custom:')
  return (
    <>
      <select
        className="input"
        aria-label={label}
        value={isCustom ? '__custom' : value}
        onChange={(e) => {
          if (e.target.value === '__custom') {
            const b = timeRangeBounds(isCustom ? value : '24h')
            setCustom({ start: toLocalInput(b.start), end: toLocalInput(b.end) })
          } else onChange(e.target.value)
        }}
      >
        {allowInherit && <option value="">Dashboard range</option>}
        {TIME_RANGES.map((r) => (
          <option key={r.id} value={r.id}>
            {r.label}
          </option>
        ))}
        <option value="__custom">{isCustom ? timeRangeLabel(value) : 'Custom range…'}</option>
      </select>
      {custom && (
        <Modal
          title="Custom time range"
          onClose={() => setCustom(null)}
          footer={
            <>
              <button className="btn" onClick={() => setCustom(null)}>
                Cancel
              </button>
              <button
                className="btn primary"
                disabled={!custom.start || !custom.end || new Date(custom.end) <= new Date(custom.start)}
                onClick={() => {
                  onChange(customRange(new Date(custom.start).toISOString(), new Date(custom.end).toISOString()))
                  setCustom(null)
                }}
              >
                Apply
              </button>
            </>
          }
        >
          <div className="row wrap">
            <label className="field grow">
              From
              <input className="input" type="datetime-local" value={custom.start} onChange={(e) => setCustom({ ...custom, start: e.target.value })} />
            </label>
            <label className="field grow">
              To
              <input className="input" type="datetime-local" value={custom.end} onChange={(e) => setCustom({ ...custom, end: e.target.value })} />
            </label>
          </div>
        </Modal>
      )}
    </>
  )
}
