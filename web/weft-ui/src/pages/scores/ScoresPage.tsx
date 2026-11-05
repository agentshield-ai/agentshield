// Scores (DESIGN.md §7, §7.1 rules 1 and 8, §7.2): one sentence answering
// "how are my sessions scoring, and which scored badly?", then the scores
// below 0.5 (lowest first) and the rest, each with its scorer's reason
// inline and a link to the session it scored. Scope, scorer, "low only" and
// the search live in the URL; deleting a score can be undone for a few seconds.
import { useMemo, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { keepPreviousData, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '../../api/client'
import { v1, type Score } from '../../api/v1'
import { useProjectId } from '../../hooks/useProject'
import { fmtDate } from '../../lib/format'
import { relativeTime, sessionTitle } from '../../lib/summaries'
import { LOW_SCORE, scoreSubject, scoresSentence, scoreTitle } from '../../lib/summaries/evaluate'
import { sessionUrl } from '../../lib/urls'
import { ErrorBox } from '../../components/common'
import { toSentence } from '../../components/evals/sentence'
import { GuidedEmpty } from '../../components/ui/EmptyState'
import { GroupedList, ListRow } from '../../components/ui/GroupedList'
import { PageHeader } from '../../components/ui/PageHeader'
import { SearchField } from '../../components/ui/SearchField'
import { Sentence } from '../../components/ui/Sentence'
import { StatusGlyph } from '../../components/ui/StatusGlyph'
import { humanScorer } from '../../components/ui/bits'
import { Icon } from '../../components/ui/Icons'
import { undoable } from '../../components/ui/Toasts'
import '../evaluations/evaluate.css'

const SCOPES = [
  { id: '', label: 'Anything' },
  { id: 'session', label: 'Sessions' },
  { id: 'agent', label: 'Agents' },
  { id: 'handoff', label: 'Hand-offs' },
  { id: 'turn', label: 'Turns' },
  { id: 'span', label: 'Steps' },
]

export function ScoresPage() {
  const pid = useProjectId()
  const qc = useQueryClient()
  const [params, setParams] = useSearchParams()
  const scope = params.get('scope') ?? ''
  const scorer = params.get('scorer') ?? ''
  const lowOnly = params.get('low') === '1'
  const search = params.get('q') ?? ''
  const setParam = (k: string, v: string) =>
    setParams(
      (p) => {
        const n = new URLSearchParams(p)
        if (v) n.set(k, v)
        else n.delete(k)
        return n
      },
      { replace: true },
    )
  const [trayOpen, setTrayOpen] = useState(false)
  const [hidden, setHidden] = useState<Set<string>>(new Set())
  const q = useQuery({
    queryKey: ['v1', pid, 'scores', 'all', scope, scorer],
    queryFn: () => v1.scores(pid, { scope: scope || undefined, scorer: scorer || undefined, limit: 500 }),
    placeholderData: keepPreviousData,
  })
  const all = useQuery({ queryKey: ['v1', pid, 'scores', 'names'], queryFn: () => v1.scores(pid, { limit: 1000 }) })
  // Session titles for the rows (the first request of each session).
  const sessions = useQuery({ queryKey: ['v1', pid, 'sessions', 'titles'], queryFn: () => v1.sessions(pid, { limit: 1000 }), staleTime: 60_000 })
  const titles = useMemo(() => new Map((sessions.data?.items ?? []).map((s) => [s.session_id, sessionTitle(s)])), [sessions.data])
  const names = [...new Set((all.data?.items ?? []).map((s) => s.scorer))].sort()
  const anyFilter = !!(scope || scorer || lowOnly)
  const filtered = anyFilter || !!search.trim()

  const rows = useMemo(() => {
    const t = search.trim().toLowerCase()
    return (q.data?.items ?? [])
      .filter((s) => !hidden.has(s.id))
      .filter((s) => !lowOnly || (s.value != null && s.value < LOW_SCORE))
      .filter((s) => !t || [s.scorer, s.reason ?? '', s.session_id ?? '', s.agent ?? '', titles.get(s.session_id ?? '') ?? ''].some((x) => x.toLowerCase().includes(t)))
  }, [q.data, hidden, lowOnly, search, titles])
  const low = rows.filter((s) => s.value != null && s.value < LOW_SCORE).sort((a, b) => (a.value as number) - (b.value as number))
  const rest = rows.filter((s) => !(s.value != null && s.value < LOW_SCORE))

  const del = (x: Score) =>
    undoable({
      message: `Deleted ${humanScorer(x.scorer)} score`,
      apply: () => setHidden((h) => new Set(h).add(x.id)),
      revert: () =>
        setHidden((h) => {
          const n = new Set(h)
          n.delete(x.id)
          return n
        }),
      commit: async () => {
        await api.feedbackDelete(pid, x.id)
        await qc.invalidateQueries({ queryKey: ['v1', pid] })
      },
    })
  const empty = all.data?.total_count === 0
  const sentence = toSentence(scoresSentence(rows, filtered), (l) =>
    l.kind === 'scores' && !lowOnly ? { onClick: () => setParam('low', '1'), title: 'Show only these scores' } : null,
  )
  const row = (x: Score) => {
    const lowScore = x.value != null && x.value < LOW_SCORE
    const name = x.session_id ? `“${titles.get(x.session_id) ?? x.session_id}”` : ''
    const where = x.scope === 'session' ? name || 'A session' : `${scoreSubject(x)}${name ? ` in ${name}` : ''}`
    return (
      <ListRow
        key={x.id}
        className="score-row"
        glyph={lowScore ? <StatusGlyph kind="warning" label={`Below ${LOW_SCORE}`} /> : undefined}
        title={scoreTitle(x)}
        summary={x.reason || 'No reason given.'}
        meta={[where, x.model ? `judged by ${x.model}` : ''].filter(Boolean).join(' · ')}
        time={relativeTime(x.created_at)}
        timeTitle={fmtDate(x.created_at)}
        to={x.session_id ? sessionUrl(pid, x.session_id, { tab: 'scores' }) : undefined}
        trailing={
          <button type="button" className="icon-btn row-action" aria-label={`Delete ${humanScorer(x.scorer)} score`} title="Delete this score (can be undone)" onClick={() => del(x)}>
            <Icon name="trash" size={14} />
          </button>
        }
      />
    )
  }

  return (
    <div className="page-frame">
      <PageHeader
        title="Scores"
        help="evaluations.md#scores"
        controls={
          empty ? undefined : (
            <>
              <SearchField label="Search scores" value={search} onChange={(v) => setParam('q', v)} filterInput placeholder="Search reasons and sessions" />
              <button type="button" className={`qbtn ${anyFilter ? 'on' : ''}`} aria-expanded={trayOpen || anyFilter} onClick={() => setTrayOpen((o) => !o)}>
                Filter{anyFilter ? <span className="qbtn-count">{[scope, scorer, lowOnly].filter(Boolean).length}</span> : null}
              </button>
            </>
          )
        }
        sentence={
          empty ? undefined : q.isLoading ? (
            <p className="sentence s-outcome sk-sentence" aria-busy="true">
              <span className="sr-only">Reading the scores…</span>
              <span className="sk sk-line" aria-hidden />
            </p>
          ) : (
            <Sentence parts={sentence} />
          )
        }
      />
      {empty ? (
        <GuidedEmpty projectId={pid} title="No scores yet" doc="evaluations.md#scores" showTest>
          Open a session and use its <strong>Scores</strong> view to run the goal-completion, reasoning-consistency or hand-off-quality judges, or create a monitor that scores new
          sessions automatically.
        </GuidedEmpty>
      ) : (
        <>
          {(trayOpen || anyFilter) && (
            <div className="ev-tray" role="group" aria-label="Filter scores">
              <label>
                Scored
                <select className="input" value={scope} onChange={(e) => setParam('scope', e.target.value)}>
                  {SCOPES.map((s) => (
                    <option key={s.id} value={s.id}>
                      {s.label}
                    </option>
                  ))}
                </select>
              </label>
              <label>
                Scorer
                <select className="input" value={scorer} onChange={(e) => setParam('scorer', e.target.value)}>
                  <option value="">Any</option>
                  {names.map((n) => (
                    <option key={n} value={n}>
                      {humanScorer(n)}
                    </option>
                  ))}
                </select>
              </label>
              <label>
                <input type="checkbox" checked={lowOnly} onChange={(e) => setParam('low', e.target.checked ? '1' : '')} /> Only below {LOW_SCORE}
              </label>
              {anyFilter && (
                <button type="button" className="linkish" onClick={() => setParams(new URLSearchParams(search ? { q: search } : {}), { replace: true })}>
                  Clear filters
                </button>
              )}
            </div>
          )}
          <ErrorBox error={q.error} />
          {q.isLoading ? null : !rows.length ? (
            <div className="quiet-empty">
              <p>No scores match.</p>
              <button type="button" className="qbtn" onClick={() => setParams(new URLSearchParams(), { replace: true })}>
                Clear filters
              </button>
            </div>
          ) : (
            <>
              {low.length > 0 && (
                <GroupedList title={`Below ${LOW_SCORE}`} count={low.length}>
                  {low.map(row)}
                </GroupedList>
              )}
              {rest.length > 0 && (
                <GroupedList title={low.length ? 'The rest' : 'Scores'} count={rest.length}>
                  {rest.map(row)}
                </GroupedList>
              )}
            </>
          )}
        </>
      )}
    </div>
  )
}
