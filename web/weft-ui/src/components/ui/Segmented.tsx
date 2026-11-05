// Segmented: the control for switching views or picking one value, such as
// a time range (DESIGN.md §7.2 rule 6). A grey track with the selected
// segment lifted in white.
//
// Two roles:
//   - "tabs": a tablist (views of one thing). Give `panelId` and render
//     <div role="tabpanel" id={panelId} aria-labelledby={segmentedTabId(panelId, value)}>.
//   - "radio": a radiogroup (one value of a setting, e.g. the time range).
// Both use a roving tabindex: Tab reaches the selected segment, the arrow
// keys (and Home/End) move and select.
//
//   <Segmented label="Time range" role="radio" value={t} onChange={setT}
//     options={[{ id: '24h', label: '24 hours' }, { id: '7d', label: '7 days' }]} />
//   <Segmented label="Views of this session" role="tabs" panelId="session-view"
//     value={tab} onChange={setTab} options={[{ id: 'story', label: 'Story' }, …]} />
import { useRef, type KeyboardEvent } from 'react'

export interface SegmentOption<T extends string> {
  id: T
  label: string
  /** A tooltip, e.g. what the view shows. */
  title?: string
}

export const segmentedTabId = (panelId: string, id: string) => `${panelId}-tab-${id}`

export function Segmented<T extends string>({
  label,
  options,
  value,
  onChange,
  role = 'radio',
  panelId,
  size = 'md',
  className,
}: {
  /** The accessible name of the group. */
  label: string
  options: SegmentOption<T>[]
  /** The selected id; null or an id not in `options` selects none. */
  value: T | null
  onChange: (id: T) => void
  role?: 'tabs' | 'radio'
  panelId?: string
  size?: 'md' | 'sm'
  className?: string
}) {
  const refs = useRef<(HTMLButtonElement | null)[]>([])
  const selected = options.findIndex((o) => o.id === value)
  const focusable = selected >= 0 ? selected : 0
  const move = (e: KeyboardEvent, i: number) => {
    const last = options.length - 1
    const to = e.key === 'ArrowRight' || e.key === 'ArrowDown' ? (i === last ? 0 : i + 1) : e.key === 'ArrowLeft' || e.key === 'ArrowUp' ? (i === 0 ? last : i - 1) : e.key === 'Home' ? 0 : e.key === 'End' ? last : -1
    if (to < 0) return
    e.preventDefault()
    refs.current[to]?.focus()
    onChange(options[to].id)
  }
  const tabs = role === 'tabs'
  return (
    <div className={`segmented segmented-${size} ${className ?? ''}`} role={tabs ? 'tablist' : 'radiogroup'} aria-label={label}>
      {options.map((o, i) => {
        const on = i === selected
        return (
          <button
            key={o.id}
            ref={(el) => {
              refs.current[i] = el
            }}
            type="button"
            className={`segmented-item ${on ? 'on' : ''}`}
            title={o.title}
            tabIndex={i === focusable ? 0 : -1}
            onClick={() => onChange(o.id)}
            onKeyDown={(e) => move(e, i)}
            {...(tabs
              ? { role: 'tab', 'aria-selected': on, id: panelId ? segmentedTabId(panelId, o.id) : undefined, 'aria-controls': panelId && on ? panelId : undefined }
              : { role: 'radio', 'aria-checked': on })}
          >
            {o.label}
          </button>
        )
      })}
    </div>
  )
}
