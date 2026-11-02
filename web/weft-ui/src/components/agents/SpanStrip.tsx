import type { ConversationSpan } from '../../api/agents'
import { CATEGORY_LABEL, kindClass, type SpanCategory } from './agentUtils'
import './agents.css'

/** Colour-coded sequence of a conversation's spans (agent, LLM, tool, error). */
export function SpanStrip({ spans, max = 80 }: { spans: ConversationSpan[]; max?: number }) {
  const shown = spans.length > max ? spans.slice(0, max) : spans
  return (
    <div className="span-strip" title={`${spans.length} spans`} aria-label={`${spans.length} spans`}>
      {shown.map((s) => (
        <span key={s.span_id} className={kindClass(s.operation_name, s.status)} title={`${s.span_name || s.operation_name} · ${s.status}`} />
      ))}
    </div>
  )
}

export function KindLegend({ kinds = ['agent', 'llm', 'tool'] }: { kinds?: SpanCategory[] }) {
  return (
    <span className="kind-legend">
      {kinds.map((k) => (
        <span key={k}>
          <i className={`kind-${k}`} />
          {CATEGORY_LABEL[k]}
        </span>
      ))}
      <span>
        <i className="kind-error" />
        Error
      </span>
    </span>
  )
}
