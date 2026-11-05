// Scores (DESIGN.md §7, §7.1 rules 1 and 8, §7.2): one sentence answering
// "how is my work scoring, and what scored badly?", counting every score the
// project has: those on sessions, agents, hand-offs and turns, and those on
// single steps (monitors and span scorers). Then the scores below 0.5
// (lowest first), the rest, and the step scores grouped by scorer, each with
// its reason inline and a link to the session or step it scored. Scope,
// scorer, "low only" and the search live in the URL; deleting a score can be
// undone for a few seconds.
import { useMemo, useState } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { keepPreviousData, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '../../api/client'
import { parseRef, spanDisplayName } from '../../api/refs'
import { v1, type Score } from '../../api/v1'
import { useProjectId } from '../../hooks/useProject'
import { fmtDate, fmtRelative } from '../../lib/format'
import { contains, toQuery } from '../../lib/query'
import { plural, scorerWords, sessionTitle } from '../../lib/summaries'
import { LOW_SCORE, scoreSubject, scoresSentence, scoreTitle, scoreValue } from '../../lib/summaries/evaluate'
import { monitorUrl, sessionUrl, spanUrl } from '../../lib/urls'
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

/** Step scores read for the page (newest first). */
const STEP_LIMIT = 1000
/** Rows shown per step scorer before "Show all". */
const STEP_ROWS = 8

/** A score a scorer gave one step (a monitor's, or a span scorer run by hand): a feedback row of type weft.scorer.<scorer>. */
interface StepScore {
  id: string
  scope: 'span'
  scorer: string
  value: number | null
  reason?: string
  span_id: string
  session_id?: string
  monitor: string | null
  created_at: string
}

const cap = (s: string) => (s ? s[0].toUpperCase() + s.slice(1) : s)

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
  const [openGroups, setOpenGroups] = useState<Set<string>>(new Set())
  const q = useQuery({
    queryKey: ['v1', pid, 'scores', 'all', scope, scorer],
    queryFn: () => v1.scores(pid, { scope: scope || undefined, scorer: scorer || undefined, limit: 500 }),
    placeholderData: keepPreviousData,
  })
  const all = useQuery({ queryKey: ['v1', pid, 'scores', 'names'], queryFn: () => v1.scores(pid, { limit: 1000 }) })
  // Scores on single steps: the scope list above leaves them out unless it is asked about one session.
  const steps = useQuery({
    queryKey: ['v1', pid, 'scores', 'steps'],
    queryFn: async (): Promise<StepScore[]> => {
      const r = await api.feedbackQuery({
        project_id: pid,
        query: toQuery(contains('feedback_type', 'weft.scorer.', false)),
        sort_by: [{ field: 'created_at', direction: 'desc' }],
        limit: STEP_LIMIT,
      })
      return r.items
        .filter((f) => f.feedback_type.startsWith('weft.scorer.') && parseRef(f.ref)?.kind === 'span')
        .map((f) => {
          const out = f.payload?.output
          const trigger = f.trigger_ref ? parseRef(f.trigger_ref)?.name : null
          const fromCreator = typeof f.creator === 'string' && f.creator.startsWith('monitor:') ? f.creator.slice('monitor:'.length) : null
          return {
            id: f.id,
            scope: 'span' as const,
            scorer: f.feedback_type.slice('weft.scorer.'.length),
            value: scoreValue(out),
            reason: out && typeof out === 'object' ? String(out.reasoning ?? out.reason ?? '') || undefined : undefined,
            span_id: parseRef(f.ref)!.name,
            monitor: trigger ?? fromCreator,
            created_at: f.created_at,
          }
        })
    },
  })
  const stepIds = useMemo(() => [...new Set((steps.data ?? []).map((s) => s.span_id))].slice(0, 500), [steps.data])
  // The scored steps' names, for the rows ("“support_agent” step").
  const stepSpans = useQuery({
    queryKey: ['v1', pid, 'scores', 'step-names', stepIds],
    queryFn: () => api.spansQuery({ project_id: pid, filter: { ids: stepIds }, limit: stepIds.length, columns: ['id', 'name', 'session_id'] }),
    enabled: stepIds.length > 0,
    staleTime: 60_000,
  })
  const stepInfo = useMemo(
    () => new Map((stepSpans.data?.items ?? []).map((s) => [s.id, { name: spanDisplayName(s.name), session: s.session_id ?? undefined }])),
    [stepSpans.data],
  )
  // Session titles for the rows (the first request of each session); also tells whether the project has any data.
  const sessions = useQuery({ queryKey: ['v1', pid, 'sessions', 'titles'], queryFn: () => v1.sessions(pid, { limit: 1000 }), staleTime: 60_000 })
  const anySpan = useQuery({ queryKey: ['spans', pid, 'any'], queryFn: () => api.spansQuery({ project_id: pid, limit: 1, columns: ['id'] }), staleTime: 60_000 })
  const titles = useMemo(() => new Map((sessions.data?.items ?? []).map((s) => [s.session_id, sessionTitle(s)])), [sessions.data])
  const names = [...new Set([...(all.data?.items ?? []).map((s) => s.scorer), ...(steps.data ?? []).map((s) => s.scorer)])].sort()
  const anyFilter = !!(scope || scorer || lowOnly)
  const filtered = anyFilter || !!search.trim()

  const t = search.trim().toLowerCase()
  const isLow = (s: { value: number | null }) => s.value != null && s.value < LOW_SCORE
  const rows = useMemo(
    () =>
      (q.data?.items ?? [])
        .filter((s) => !hidden.has(s.id))
        .filter((s) => !lowOnly || isLow(s))
        .filter((s) => !t || [s.scorer, s.reason ?? '', s.session_id ?? '', s.agent ?? '', titles.get(s.session_id ?? '') ?? ''].some((x) => x.toLowerCase().includes(t))),
    [q.data, hidden, lowOnly, t, titles],
  )
  const stepRows = useMemo(
    () =>
      (steps.data ?? [])
        .map((s) => ({ ...s, session_id: stepInfo.get(s.span_id)?.session }))
        .filter((s) => !hidden.has(s.id))
        .filter((s) => !scope || scope === 'span')
        .filter((s) => !scorer || s.scorer === scorer)
        .filter((s) => !lowOnly || isLow(s))
        .filter((s) => !t || [s.scorer, s.reason ?? '', s.monitor ?? '', stepInfo.get(s.span_id)?.name ?? ''].some((x) => x.toLowerCase().includes(t))),
    [steps.data, stepInfo, hidden, scope, scorer, lowOnly, t],
  )
  const low = [...rows.filter(isLow), ...stepRows.filter(isLow)].sort((a, b) => (a.value as number) - (b.value as number))
  const rest = rows.filter((s) => !isLow(s))
  const stepGroups = useMemo(() => {
    const m = new Map<string, StepScore[]>()
    for (const s of stepRows) if (!isLow(s)) m.set(s.scorer, [...(m.get(s.scorer) ?? []), s])
    return [...m.entries()].sort((a, b) => a[0].localeCompare(b[0]))
  }, [stepRows])

  const del = (x: { id: string; scorer: string }) =>
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
  const loading = q.isLoading || steps.isLoading
  const total = (all.data?.total_count ?? 0) + (steps.data?.length ?? 0)
  const empty = !!all.data && !!steps.data && total === 0
  // A project with sessions or traces but no scores gets the plain empty state, never the getting-started one.
  const hasData = (sessions.data?.total_count ?? 0) > 0 || (anySpan.data?.items.length ?? 0) > 0
  const sentence = toSentence(scoresSentence([...rows, ...stepRows], filtered), (l) =>
    l.kind === 'scores' && !lowOnly ? { onClick: () => setParam('low', '1'), title: 'Show only these scores' } : null,
  )
  // Every row keeps the glyph column, so text lines up across the groups.
  const glyphOf = (lowScore: boolean) =>
    lowScore ? <StatusGlyph kind="warning" label={`Below ${LOW_SCORE}`} /> : low.length ? <span className="ev-glyph-space" aria-hidden /> : undefined
  const trash = (x: { id: string; scorer: string }) => (
    <button type="button" className="icon-btn row-action" aria-label={`Delete ${humanScorer(x.scorer)} score`} title="Delete this score (can be undone)" onClick={() => del(x)}>
      <Icon name="trash" size={14} />
    </button>
  )
  const row = (x: Score) => {
    const name = x.session_id ? `“${titles.get(x.session_id) ?? x.session_id}”` : ''
    const where = x.scope === 'session' ? name || 'A session' : `${scoreSubject(x)}${name ? ` in ${name}` : ''}`
    return (
      <ListRow
        key={x.id}
        className="score-row"
        glyph={glyphOf(isLow(x))}
        title={scoreTitle(x)}
        summary={x.reason || 'No reason given.'}
        meta={[where, x.model ? `judged by ${x.model}` : ''].filter(Boolean).join(' · ')}
        time={fmtRelative(x.created_at)}
        timeTitle={fmtDate(x.created_at)}
        to={x.session_id ? sessionUrl(pid, x.session_id, { tab: 'scores' }) : undefined}
        trailing={trash(x)}
      />
    )
  }
  const stepRow = (x: StepScore, oneMonitor = false) => {
    const step = stepInfo.get(x.span_id)?.name
    return (
      <ListRow
        key={x.id}
        className="score-row"
        glyph={glyphOf(isLow(x))}
        title={scoreTitle(x)}
        summary={x.reason || 'No reason given.'}
        meta={[step ? `The “${step}” step` : 'A step', x.monitor && !oneMonitor ? `by monitor ${x.monitor}` : ''].filter(Boolean).join(' · ')}
        time={fmtRelative(x.created_at)}
        timeTitle={fmtDate(x.created_at)}
        to={spanUrl(pid, x.span_id)}
        trailing={trash(x)}
      />
    )
  }
  const anyRow = (x: Score | StepScore) => ('monitor' in x ? stepRow(x as StepScore) : row(x as Score))

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
          empty ? undefined : loading ? (
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
        hasData ? (
          <section className="quiet-empty ev-empty" aria-label="No scores yet">
            <p>
              <strong>No scores yet.</strong> Open a session and use its <strong>Scores</strong> view to judge its goal completion, reasoning or hand-offs, or create a monitor
              that scores new sessions and steps as they arrive.
            </p>
            <Link className="qbtn" to={`/${pid}/monitors`}>
              Monitors
            </Link>
          </section>
        ) : (
          <GuidedEmpty projectId={pid} title="No scores yet" doc="evaluations.md#scores" showTest>
            Open a session and use its <strong>Scores</strong> view to judge its goal completion, reasoning or hand-offs, or create a monitor that scores new sessions
            automatically.
          </GuidedEmpty>
        )
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
          <ErrorBox error={q.error ?? steps.error} />
          {loading ? null : !rows.length && !stepRows.length ? (
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
                  {low.map(anyRow)}
                </GroupedList>
              )}
              {rest.length > 0 && (
                <GroupedList title={low.length ? 'The rest' : stepGroups.length ? 'Sessions, agents and hand-offs' : 'Scores'} count={rest.length}>
                  {rest.map(row)}
                </GroupedList>
              )}
              {stepGroups.map(([name, xs]) => {
                const open = openGroups.has(name)
                const monitors = [...new Set(xs.map((x) => x.monitor).filter(Boolean))] as string[]
                return (
                  <GroupedList
                    key={name}
                    title={`${cap(scorerWords(name))} on steps`}
                    count={xs.length}
                    footer={
                      <p className="ev-note ev-group-note">
                        {monitors.length === 1 && (
                          <>
                            Recorded by monitor <Link to={monitorUrl(pid, monitors[0])}>{monitors[0]}</Link>.{' '}
                          </>
                        )}
                        {xs.length > STEP_ROWS && (
                          <button
                            type="button"
                            className="linkish"
                            aria-expanded={open}
                            onClick={() =>
                              setOpenGroups((g) => {
                                const n = new Set(g)
                                if (open) n.delete(name)
                                else n.add(name)
                                return n
                              })
                            }
                          >
                            {open ? 'Show fewer' : `Show all ${plural(xs.length, 'score')}`}
                          </button>
                        )}
                      </p>
                    }
                  >
                    {(open ? xs : xs.slice(0, STEP_ROWS)).map((x) => stepRow(x, monitors.length === 1))}
                  </GroupedList>
                )
              })}
              {steps.data && steps.data.length >= STEP_LIMIT && <p className="ev-note">Showing the newest {STEP_LIMIT} step scores.</p>}
            </>
          )}
        </>
      )}
    </div>
  )
}
