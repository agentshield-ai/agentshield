// SearchField: the one search box of a list (DESIGN.md §7.2 rule 6): a grey
// rounded field with a magnifier and a visually hidden label. Controlled;
// pass `filterInput` on the page's main search so `/` focuses it
// (Shortcuts.tsx focusFilter looks for [data-filter-input]).
//
//   <SearchField label="Search sessions" value={q} onChange={setQ} filterInput />
import { forwardRef, type InputHTMLAttributes } from 'react'

type Props = Omit<InputHTMLAttributes<HTMLInputElement>, 'onChange' | 'value'> & {
  /** The accessible name (visually hidden). */
  label: string
  value: string
  onChange: (value: string) => void
  /** Marks the page's main search box, the one `/` focuses. */
  filterInput?: boolean
  width?: number | string
}

export const SearchField = forwardRef<HTMLInputElement, Props>(function SearchField(
  { label, value, onChange, filterInput, width, placeholder = 'Search', className, ...rest },
  ref,
) {
  return (
    <label className={`search-field ${className ?? ''}`} style={width != null ? { width } : undefined}>
      <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.2" strokeLinecap="round" aria-hidden="true" focusable="false">
        <circle cx="11" cy="11" r="7" />
        <path d="M20 20l-3.5-3.5" />
      </svg>
      <span className="sr-only">{label}</span>
      <input
        ref={ref}
        type="search"
        value={value}
        placeholder={placeholder}
        onChange={(e) => onChange(e.target.value)}
        {...(filterInput ? { 'data-filter-input': true } : {})}
        {...rest}
      />
    </label>
  )
})
