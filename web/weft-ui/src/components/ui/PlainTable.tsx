// PlainTable: data people compare across columns (per-agent figures,
// leaderboards, dataset rows), as a quiet table with few columns (DESIGN.md
// §7.2 rule 3). No zebra stripes, no coloured cells: hairline row
// separators, grey column headings, numbers right-aligned (add class "num"
// to their cells). It scrolls inside its own box when wider than its column
// (never widening the page), and is keyboard-scrollable then.
//
//   <PlainTable caption="Agents in this session">
//     <thead><tr><th scope="col">Agent</th><th scope="col" className="num">Tool calls</th></tr></thead>
//     <tbody><tr><td>coder</td><td className="num">3</td></tr></tbody>
//   </PlainTable>
//
// With `stack`, below 600 px of its own width (a phone) each row becomes two
// lines instead of columns: the first cell on line 1, the rest on line 2 as
// "7 runs · 3 failed · $0.23". Each value takes its column's heading as a
// word after it ("runs", lower case), or what the heading's data attributes
// say: data-stack="run|runs" (singular|plural; "" for no word, as for a
// cost) or data-stack-prefix="median" (a word before it: "median 1.3 s").
//
//   <th scope="col" className="num" data-stack="run|runs">Runs</th>
import { useLayoutEffect, useRef, type ReactNode } from 'react'
import { TableScroll } from './bits'

/** The word a stacked cell carries after its value: "runs", "run" for 1, or none. */
export function stackWord(spec: string | undefined, heading: string, value: string): string {
  const raw = spec ?? heading.trim().toLowerCase()
  if (!raw) return ''
  const [one, many] = raw.includes('|') ? raw.split('|') : [raw, raw]
  return value.trim() === '1' ? one : many
}

/** Gives each body cell the words it shows when the table stacks (see above). */
function labelCells(table: HTMLTableElement) {
  const heads = [...table.querySelectorAll<HTMLTableCellElement>(':scope > thead > tr:last-child > th')]
  for (const tr of table.querySelectorAll<HTMLTableRowElement>(':scope > tbody > tr')) {
    let shown = 0
    ;[...tr.cells].forEach((td, i) => {
      const th = heads[i]
      const value = td.textContent ?? ''
      if (i === 0 || !th) {
        delete td.dataset.label
        delete td.dataset.before
        return
      }
      const empty = !value.trim()
      const word = empty ? '' : stackWord(th.dataset.stack, th.textContent ?? '', value)
      const prefix = empty ? '' : (th.dataset.stackPrefix ?? '')
      const sep = !empty && shown > 0 ? ' · ' : ''
      if (!empty) shown++
      const before = `${sep}${prefix ? `${prefix} ` : ''}`
      const label = prefix ? '' : word
      if (label) td.dataset.label = label
      else delete td.dataset.label
      if (before) td.dataset.before = before
      else delete td.dataset.before
    })
  }
}

export function PlainTable({
  caption,
  children,
  className,
  captionVisible,
  count,
  stack,
}: {
  caption: string
  children: ReactNode
  className?: string
  captionVisible?: boolean
  /** Shown as a quiet grey number after a visible caption (never "(8)"). */
  count?: number
  /** Below 600 px, show each row as two lines (name, then its figures) instead of columns. */
  stack?: boolean
}) {
  const ref = useRef<HTMLTableElement>(null)
  useLayoutEffect(() => {
    const t = ref.current
    if (!stack || !t) return
    labelCells(t)
    if (typeof MutationObserver === 'undefined') return
    const mo = new MutationObserver(() => labelCells(t))
    mo.observe(t, { childList: true, subtree: true, characterData: true })
    return () => mo.disconnect()
  }, [stack])
  return (
    <div className={`plain-table-box${stack ? ' stackable' : ''}`}>
      <TableScroll label={caption}>
        <table ref={ref} className={`plain-table ${className ?? ''}`}>
          <caption className={captionVisible ? 'plain-caption' : 'sr-only'}>
            {caption}
            {count != null && <span className="glist-count"> {count.toLocaleString()}</span>}
          </caption>
          {children}
        </table>
      </TableScroll>
    </div>
  )
}
