import type { Span } from '../../api/types'
import { opStyle } from './opStyle'
import './traceViews.css'

/** Small kind pill / colour mark for ops that set attributes.weft.span_kind or .color. */
export function OpKind({ call }: { call: Pick<Span, 'attributes'> }) {
  const { kind, color } = opStyle(call)
  if (!kind && !color) return null
  return (
    <span className="op-kind" style={color ? { borderColor: color, color } : undefined} title={`kind: ${kind ?? '—'}${color ? ` · colour ${color}` : ''}`}>
      {kind ?? '●'}
    </span>
  )
}
