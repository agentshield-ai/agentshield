import { useNavigate, useSearchParams } from 'react-router-dom'
import { Link } from 'react-router-dom'
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { api } from '../../api/client'
import type { SortBy } from '../../api/types'
import { useProjectId } from '../../hooks/useProject'
import { fmtCost, fmtDate, fmtDuration, fmtNumber, fmtRelative } from '../../lib/format'
import { threadUrl } from '../../lib/urls'
import { Empty, ErrorBox, Pager, Spinner } from '../../components/common'

const SORTABLE = ['last_updated', 'start_time', 'turn_count'] as const
const LIMIT = 50

export function ThreadsPage() {
  const projectId = useProjectId()
  const navigate = useNavigate()
  const [params, setParams] = useSearchParams()
  const [sf, sd] = (params.get('sort') ?? 'last_updated:desc').split(':')
  const sort: SortBy = { field: (SORTABLE as readonly string[]).includes(sf) ? sf : 'last_updated', direction: sd === 'asc' ? 'asc' : 'desc' }
  const offset = Math.max(0, Number(params.get('offset') ?? 0) || 0)
  const q = useQuery({
    queryKey: ['threads', projectId, sort, offset],
    queryFn: () => api.threadsQuery({ project_id: projectId, sort_by: [sort], limit: LIMIT + 1, offset }),
    placeholderData: keepPreviousData,
  })
  const all = q.data?.threads ?? []
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
    <th className={`sortable ${num ? 'num' : ''}`} onClick={() => sortBy(f)} aria-sort={sort.field === f ? (sort.direction === 'asc' ? 'ascending' : 'descending') : undefined}>
      {label}
      {sort.field === f ? (sort.direction === 'desc' ? ' ↓' : ' ↑') : ''}
    </th>
  )
  return (
    <div className="page">
      <div className="page-header">
        <h1>Threads</h1>
        {q.isFetching && <Spinner />}
        <span className="spacer" />
        <span className="small muted">Conversations grouped by thread id</span>
      </div>
      <ErrorBox error={q.error} />
      {q.isLoading ? (
        <div className="empty">
          <Spinner label="Loading threads…" />
        </div>
      ) : threads.length === 0 ? (
        <Empty>
          <div className="col" style={{ alignItems: 'center' }}>
            <b>No threads yet</b>
            <span className="small">
              Calls with a <code>thread_id</code> are grouped into threads. With the Python SDK wrap each turn in <code>with weft.thread("conversation-id"):</code>; with OpenTelemetry set{' '}
              <code>gen_ai.conversation.id</code> or <code>session.id</code> on spans.
            </span>
          </div>
        </Empty>
      ) : (
        <>
          <div className="table-wrap">
            <table className="table">
              <thead>
                <tr>
                  <th>Thread</th>
                  {th('turn_count', 'Turns', true)}
                  <th className="num">Calls</th>
                  {th('start_time', 'Started')}
                  {th('last_updated', 'Last updated')}
                  <th className="num">p50 turn</th>
                  <th className="num">p99 turn</th>
                  <th className="num">Tokens</th>
                  <th className="num">Cost</th>
                </tr>
              </thead>
              <tbody>
                {threads.map((t) => (
                  <tr key={t.thread_id} className="clickable" onClick={() => navigate(threadUrl(projectId, t.thread_id))}>
                    <td className="mono">
                      <Link to={threadUrl(projectId, t.thread_id)} onClick={(e) => e.stopPropagation()}>
                        {t.thread_id}
                      </Link>
                    </td>
                    <td className="num">{fmtNumber(t.turn_count)}</td>
                    <td className="num">{fmtNumber(t.call_count)}</td>
                    <td title={fmtDate(t.start_time)}>{fmtRelative(t.start_time)}</td>
                    <td title={fmtDate(t.last_updated)}>{fmtRelative(t.last_updated)}</td>
                    <td className="num">{fmtDuration(t.p50_turn_duration_ms)}</td>
                    <td className="num">{fmtDuration(t.p99_turn_duration_ms)}</td>
                    <td className="num">{t.total_tokens ? fmtNumber(t.total_tokens) : '—'}</td>
                    <td className="num">{t.cost_usd ? fmtCost(t.cost_usd) : '—'}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <div className="row" style={{ justifyContent: 'flex-end' }}>
            <Pager offset={offset} limit={LIMIT} total={hasMore ? null : offset + threads.length} onChange={(o) => set({ offset: o ? String(o) : null })} />
          </div>
        </>
      )}
    </div>
  )
}
