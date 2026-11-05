// Sessions by id (the developer view of Sessions): answers "which session
// ids have spans, and how did they go?". One sentence, then the sessions as
// a list, newest first; each row is named by its first turn's request and
// opens the session. The id stays as the row's quiet line.
import { useMemo } from 'react'
import { useSearchParams } from 'react-router-dom'
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { api } from '../../api/client'
import type { SortBy } from '../../api/types'
import { useProjectId } from '../../hooks/useProject'
import { fmtDate, fmtRelative } from '../../lib/format'
import { spanSessionUrl, tracesUrl } from '../../lib/urls'
import { spanSessionLine, spanSessionsSentence, SPAN_SESSIONS_TITLE, turnName } from '../../lib/summaries/traces'
import { ErrorBox, Pager, Spinner } from '../../components/common'
import { GroupedList, ListRow } from '../../components/ui/GroupedList'
import { PageHeader } from '../../components/ui/PageHeader'
import { Segmented } from '../../components/ui/Segmented'
import { Sentence } from '../../components/ui/Sentence'
import '../observe.css'

const SORTABLE = ['last_updated', 'started_at', 'turn_count'] as const
type SortField = (typeof SORTABLE)[number]
const LIMIT = 50

export function SpanSessionsPage() {
  const projectId = useProjectId()
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
  // Each session is named by its first turn's request.
  const firstIds = useMemo(() => threads.map((t) => t.first_turn_id).filter((x): x is string => !!x), [threads])
  const firsts = useQuery({
    queryKey: ['spans', projectId, 'first-turns', firstIds],
    queryFn: () => api.spansQuery({ project_id: projectId, filter: { ids: firstIds }, columns: ['id', 'inputs'], limit: firstIds.length }),
    enabled: firstIds.length > 0,
    staleTime: 60_000,
    placeholderData: keepPreviousData,
  })
  const names = useMemo(() => new Map((firsts.data?.items ?? []).map((s) => [s.id, turnName(s.inputs)])), [firsts.data])
  const set = (patch: Record<string, string | null>) =>
    setParams((p) => {
      const n = new URLSearchParams(p)
      for (const [k, v] of Object.entries(patch)) v == null ? n.delete(k) : n.set(k, v)
      return n
    })
  const sentence = q.data ? spanSessionsSentence({ shown: threads.length, more: hasMore, offset, latest: offset === 0 && sort.field === 'last_updated' && sort.direction === 'desc' ? threads[0]?.last_updated : null }) : null
  return (
    <div className="page-frame obs-page">
      <PageHeader
        variant="detail"
        back={{ to: tracesUrl(projectId), label: 'Traces' }}
        title={SPAN_SESSIONS_TITLE}
        help="traces.md"
        helpTopic="Traces"
        controls={
          <Segmented<SortField>
            label="Order"
            size="sm"
            options={[
              { id: 'last_updated', label: 'Last active' },
              { id: 'started_at', label: 'Started' },
              { id: 'turn_count', label: 'Most turns' },
            ]}
            value={sort.field as SortField}
            onChange={(f) => set({ sort: f === 'last_updated' ? null : `${f}:desc`, offset: null })}
          />
        }
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
      />
      <ErrorBox error={q.error} />
      {q.isLoading ? (
        <div className="quiet-empty">
          <Spinner label="Loading sessions…" />
        </div>
      ) : threads.length === 0 ? (
        <div className="quiet-empty">
          <p>
            <b>No sessions yet.</b> Spans that carry a session id are grouped here. With the Python SDK wrap each turn in <code>with weft.session("chat-id"):</code>; with
            OpenTelemetry set <code>session.id</code> on spans.
          </p>
        </div>
      ) : (
        <>
          <GroupedList label="Sessions by id">
            {threads.map((t) => {
              const name = t.first_turn_id ? names.get(t.first_turn_id) : ''
              return (
                <ListRow
                  key={t.session_id}
                  title={name || t.session_id}
                  summary={spanSessionLine(t)}
                  meta={name ? t.session_id : undefined}
                  time={fmtRelative(t.last_updated)}
                  timeTitle={`Last active ${fmtDate(t.last_updated)}`}
                  to={spanSessionUrl(projectId, t.session_id)}
                />
              )
            })}
          </GroupedList>
          {(offset > 0 || hasMore) && (
            <div className="obs-foot" style={{ justifyContent: 'flex-end' }}>
              <Pager offset={offset} limit={LIMIT} total={hasMore ? null : offset + threads.length} onChange={(o) => set({ offset: o ? String(o) : null })} />
            </div>
          )}
        </>
      )}
    </div>
  )
}
