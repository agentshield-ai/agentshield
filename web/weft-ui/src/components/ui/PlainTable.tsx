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
import type { ReactNode } from 'react'
import { TableScroll } from './bits'

export function PlainTable({
  caption,
  children,
  className,
  captionVisible,
  count,
}: {
  caption: string
  children: ReactNode
  className?: string
  captionVisible?: boolean
  /** Shown as a quiet grey number after a visible caption (never "(8)"). */
  count?: number
}) {
  return (
    <div className="plain-table-box">
      <TableScroll label={caption}>
        <table className={`plain-table ${className ?? ''}`}>
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
