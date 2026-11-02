import { Link } from 'react-router-dom'
import { refLabel } from '../api/refs'
import { refUrl } from '../lib/urls'

export function RefChip({ refStr }: { refStr: string }) {
  const url = refUrl(refStr)
  const label = refLabel(refStr)
  if (!url)
    return (
      <span className="ref-chip" title={refStr}>
        {label}
      </span>
    )
  return (
    <Link className="ref-chip" to={url} title={refStr}>
      {label}
    </Link>
  )
}
