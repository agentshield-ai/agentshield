// A link to what a ref points at (a dataset, a model, a span), labelled with
// its name and version in plain words; the ref itself is the tooltip
// (DESIGN.md §7.1 rule 2: refs only in raw views and inspectors).
import { Link } from 'react-router-dom'
import { parseRef } from '../api/refs'
import { refUrl } from '../lib/urls'
import './valueview.css'

/** The plain label of a ref: an asset's name (and the field it points into), "step 0190e2e0", "rows 3f2a9c1b". */
export function refWords(ref: string): string {
  const p = parseRef(ref)
  if (!p) return ref
  if (p.kind === 'span') return `step ${p.name.slice(0, 8)}`
  if (p.kind === 'table') return `rows ${p.name.slice(0, 8)}`
  return p.extra.length ? `${p.name} › ${p.extra.filter((x) => x !== 'attr' && x !== 'key' && x !== 'index' && x !== 'id').join(' › ')}` : p.name
}

export function RefChip({ refStr }: { refStr: string }) {
  const url = refUrl(refStr)
  const label = refWords(refStr)
  const title = `Ref: ${refStr}`
  if (!url)
    return (
      <span className="ref-chip" title={title}>
        {label}
      </span>
    )
  return (
    <Link className="ref-chip" to={url} title={title}>
      {label}
    </Link>
  )
}
