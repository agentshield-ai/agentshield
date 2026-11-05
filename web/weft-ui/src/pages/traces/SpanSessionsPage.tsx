import { useNavigate, useSearchParams } from 'react-router-dom'
import { Link } from 'react-router-dom'
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { api } from '../../api/client'
import type { SortBy } from '../../api/types'
import { useProjectId } from '../../hooks/useProject'
import { fmtCost, fmtDate, fmtDuration, fmtNumber, fmtRelative } from '../../lib/format'
import { spanSessionUrl, tracesUrl } from '../../lib/urls'
import { spanSessionsSentence } from '../../lib/summaries/traces'
import { ErrorBox, Pager, Spinner } from '../../components/common'
import { PageHeader } from '../../components/ui/PageHeader'
import { PlainTable } from '../../components/ui/PlainTable'
import { Sentence } from '../../components/ui/Sentence'
import '../observe.css'

const SORTABLE = ['last_updated', 'started_at', 'turn_count'] as const
const LIMIT = 50

export function SpanSessionsPage() {
  const projectId = useProjectId()
  const navigate = useNavigate()
  const [params, setParams] = useSearchParams()
  const [sf, sd] = (params.get('sort') ?? 'last_updated:desc').split(':')
  const sort: SortBy = { field: (SORTABLE as readonly string[]).includes(sf) ? sf : 'last_updated', direction: sd === 'asc' ? 'asc' : 'desc' }
  const offset = Math.max(0, Number(params.get('offset') ?? 0) || 0)
  const q = useQuery({
    queryKey: ['span-sessions', projectId, sort, offset],
    queryFn: () => api.spanSessions({ project_id: projectId, sort_by: [sort], limit: LIMIT + 1, offset }),
    placeholderData: keepPreviousData,
  })
  const all = q.data?.items ?? []
  const hasMore = all.length > LIMIT
  const threads = all.slice(0, LIMIT)
  const set = (patch: Record<string, string | null>) =>
    setParams((p) => {
      const n = new URLSearchParams(p)
      for (const [k, v] of Object.entries(patch)) v == null ? n.delete(k) : n.set(k, v)
      return n
    })
  const sortBy = (f: string) => {
    const dir = sort.field === f && sort.direction === 'desc' ? 'asc' : 'desc'
    set({ sort: `${f}:${dir}`, offset: null })
  }
  const th = (f: (typeof SORTABLE)[number], label: string, num = false) => (
    <th scope="col" className={`sortable ${num ? 'num' : ''}`} onClick={() => sortBy(f)} aria-sort={sort.field === f ? (sort.direction === 'asc' ? 'ascending' : 'descending') : undefined}>
      {label}
      {sort.field === f ? (sort.direction === 'desc' ? ' ↓' : ' ↑') : ''}
    </th>
  )
  const sentence = q.data ? spanSessionsSentence({ shown: threads.length, more: hasMore, offset, latest: offset === 0 && sort.field === 'last_updated' && sort.direction === 'desc' ? threads[0]?.last_updated : null }) : null
  return (
    <div className="page-frame obs-page obs-wide">
      <PageHeader
        variant="detail"
        back={{ to: tracesUrl(projectId), label: 'Traces' }}
        title="Span sessions"
        help="traces.md"
        helpTopic="Traces"
        sentence={
          sentence ? (
            <Sentence size="detail" parts={[sentence]} />
          ) : (
            <p className="sentence s-detail sk-sentence" aria-busy="true">
              <span className="sr-only">Counting sessions…</span>
              <span className="sk sk-line" aria-hidden />
            </p>
          )
        }
        meta="Spans grouped by their session id, the developer view of Sessions."
      />
      <ErrorBox error={q.error} />
      {q.isLoading ? (
        <div className="quiet-empty">
          <Spinner label="Loading sessions…" />
        </div>
      ) : threads.length === 0 ? (
        <div className="quiet-empty">
          <p>
            <b>No span sessions yet.</b> Spans with a <code>session_id</code> are grouped by session. With the Python SDK wrap each turn in{' '}
            <code>with weft.session("conversation-id"):</code>; with OpenTelemetry set <code>gen_ai.conversation.id</code> or <code>session.id</code> on spans.
          </p>
        </div>
      ) : (
        <>
          <PlainTable caption="Span sessions" className="obs-nowrap">
            <thead>
              <tr>
                <th scope="col">Session</th>
                {th('turn_count', 'Turns', true)}
                <th scope="col" className="num">
                  Spans
                </th>
                {th('started_at', 'Started')}
                {th('last_updated', 'Last updated')}
                <th scope="col" className="num" title="Median and 99th percentile of the turns' durations">
                  Turn time (p50 / p99)
                </th>
                <th scope="col" className="num">
                  Cost
                </th>
              </tr>
            </thead>
            <tbody>
              {threads.map((t) => (
                <tr key={t.session_id} className="clickable" onClick={() => navigate(spanSessionUrl(projectId, t.session_id))}>
                  <td className="mono">
                    <Link to={spanSessionUrl(projectId, t.session_id)} onClick={(e) => e.stopPropagation()}>
                      {t.session_id}
                    </Link>
                  </td>
                  <td className="num">{fmtNumber(t.turn_count)}</td>
                  <td className="num">{fmtNumber(t.span_count)}</td>
                  <td title={fmtDate(t.started_at)}>{fmtRelative(t.started_at)}</td>
                  <td title={fmtDate(t.last_updated)}>{fmtRelative(t.last_updated)}</td>
                  <td className="num">
                    {fmtDuration(t.p50_turn_duration_ms)} / {fmtDuration(t.p99_turn_duration_ms)}
                  </td>
                  <td className="num" title={t.total_tokens ? `${fmtNumber(t.total_tokens)} tokens` : undefined}>
                    {t.cost_usd ? fmtCost(t.cost_usd) : '—'}
                  </td>
                </tr>
              ))}
            </tbody>
          </PlainTable>
          <div className="obs-foot" style={{ justifyContent: 'flex-end' }}>
            <Pager offset={offset} limit={LIMIT} total={hasMore ? null : offset + threads.length} onChange={(o) => set({ offset: o ? String(o) : null })} />
          </div>
        </>
      )}
    </div>
  )
}
