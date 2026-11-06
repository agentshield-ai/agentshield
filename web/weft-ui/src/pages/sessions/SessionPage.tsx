// One session (DESIGN.md §7 "Session page", §7.2): the title, one generated
// sentence giving the outcome, one quiet line of figures, and a segmented
// control for Story (the session as a conversation, with the inspector),
// Team (the delegation graph, its figures as tables behind "Show as a table"), Scores and Raw
// spans. The step responsible for a failure or block is selected and
// scrolled into view on arrival (§7.1 rule 3); ?step=<key> deep-links any
// step; j/k move between steps.
import { useEffect, useMemo, useRef, useState } from 'react'
import { Link, useParams, useSearchParams } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '../../api/client'
import { iamApi } from '../../api/iam'
import { SCORERS_BY_SCOPE, v1, type Detection, type Score } from '../../api/v1'
import { useProjectId } from '../../hooks/useProject'
import { buildStory, entryFor, moveEntry } from '../../lib/story'
import { sessionMetaFigures, sessionOutcome, sessionTitle, scorerWords, relativeTime } from '../../lib/summaries'
import { buildTimeline, currentFocus, oneLine, STEP_LABEL, stepTitle, type BuiltTimeline } from '../../lib/timeline'
import { agentUrl, sessionsUrl, spanUrl, traceSpansUrl } from '../../lib/urls'
import { ErrorBox, Loading } from '../../components/common'
import { GraphTables, GraphWithTables } from '../../components/ui/AgentGraphView'
import { GroupedList, ListRow } from '../../components/ui/GroupedList'
import { CopyText } from '../../components/ui/Inspector'
import { PageHeader } from '../../components/ui/PageHeader'
import { PlainTable } from '../../components/ui/PlainTable'
import { Segmented, segmentedTabId } from '../../components/ui/Segmented'
import { Sentence } from '../../components/ui/Sentence'
import { isTyping } from '../../components/ui/Shortcuts'
import { humanScorer } from '../../components/ui/bits'
import { Icon } from '../../components/ui/Icons'
import { notify, undoable } from '../../components/ui/Toasts'
import { StoryView, useReplay } from '../../components/sessions/TimelineView'
import './session.css'

/** The replay result, shared with the Security page. */
export { ReplayResult } from '../../components/sessions/TimelineView'

type View = 'story' | 'team' | 'scores' | 'raw'
const VIEWS: { id: View; label: string; title: string }[] = [
  { id: 'story', label: 'Story', title: 'What happened, step by step' },
  { id: 'team', label: 'Team', title: 'Who handed work to whom' },
  { id: 'scores', label: 'Scores', title: 'Scores and the scorers’ reasons' },
  { id: 'raw', label: 'Raw spans', title: 'The spans the story is built from' },
]
/** Earlier views' URLs: the Timeline, Reasoning and Security tabs are now the Story; the Graph tab is Team. */
const LEGACY_VIEW: Record<string, View> = { timeline: 'story', reasoning: 'story', security: 'story', graph: 'team' }

export function SessionPage() {
  const pid = useProjectId()
  const { sessionId = '' } = useParams()
  const [params, setParams] = useSearchParams()
  const rawView = params.get('tab')
  const view: View = (LEGACY_VIEW[rawView ?? ''] ?? (VIEWS.some((v) => v.id === rawView) ? rawView : 'story')) as View
  const stepParam = params.get('step')

  // Old view names keep working: rewrite the URL once.
  useEffect(() => {
    if (!rawView || !(rawView in LEGACY_VIEW)) return
    const n = new URLSearchParams(params)
    if (LEGACY_VIEW[rawView] === 'story') n.delete('tab')
    else n.set('tab', LEGACY_VIEW[rawView])
    setParams(n, { replace: true })
  }, [rawView]) // eslint-disable-line react-hooks/exhaustive-deps

  const summary = useQuery({ queryKey: ['v1', pid, 'session', sessionId], queryFn: () => v1.session(pid, sessionId), refetchInterval: 15_000 })
  const timeline = useQuery({ queryKey: ['v1', pid, 'timeline', sessionId], queryFn: () => v1.timeline(pid, sessionId), refetchInterval: 15_000 })
  const detections = useQuery({ queryKey: ['v1', pid, 'detections', 'session', sessionId], queryFn: () => v1.detections(pid, { session_id: sessionId, limit: 1000 }) })
  const scores = useQuery({ queryKey: ['v1', pid, 'scores', sessionId], queryFn: () => v1.scores(pid, { session_id: sessionId, limit: 500 }) })
  const replay = useReplay(pid, sessionId)

  const allDets = useMemo(() => detections.data?.items ?? [], [detections.data])
  // Engine verdicts mark the steps; replay verdicts are shown with the step they re-checked.
  const tl = useMemo(() => {
    const x = buildTimeline(timeline.data?.items ?? [], allDets.filter((d) => d.source === 'engine'))
    attachReplays(x, allDets)
    return x
  }, [timeline.data, allDets])
  const story = useMemo(() => buildStory(tl), [tl])
  const ready = !!timeline.data && !detections.isLoading

  // The selected step: the deep link's, else the step responsible for a failure or block.
  const selected = useMemo(() => {
    if (!ready) return null
    if (stepParam === 'problem') return tl.problems[0] ? (entryFor(story, tl.problems[0].key)?.key ?? null) : null
    if (stepParam === 'error') {
      const p = tl.problems.find((x) => x.problem === 'error') ?? tl.problems[0]
      return p ? (entryFor(story, p.key)?.key ?? null) : null
    }
    if (stepParam) return entryFor(story, stepParam)?.key ?? null
    return tl.problems[0] ? (entryFor(story, tl.problems[0].key)?.key ?? null) : null
  }, [ready, stepParam, story, tl])

  // Scroll to the selected step once on arrival, and on every j/k.
  const [scrollKey, setScrollKey] = useState<string | null>(null)
  const arrived = useRef(false)
  useEffect(() => {
    if (!ready || arrived.current || view !== 'story') return
    arrived.current = true
    if (selected) setScrollKey(selected)
  }, [ready, selected, view])

  const select = (key: string | null) => {
    const n = new URLSearchParams(params)
    if (key) n.set('step', key)
    else n.delete('step')
    n.delete('tab')
    setParams(n, { replace: true })
  }
  const setView = (v: View) => {
    const n = new URLSearchParams(params)
    if (v === 'story') n.delete('tab')
    else n.set('tab', v)
    setParams(n, { replace: false })
  }

  // j / k: keys can arrive faster than the URL (and so `selected`) updates.
  const pending = useRef<{ key: string | null; at: number } | null>(null)
  useEffect(() => {
    if (pending.current && pending.current.key === selected) pending.current = null
  }, [selected])
  useEffect(() => {
    if (view !== 'story') return
    const h = (e: KeyboardEvent) => {
      if (e.metaKey || e.ctrlKey || e.altKey || isTyping(e) || document.querySelector('.modal')) return
      if (e.key !== 'j' && e.key !== 'k') return
      e.preventDefault()
      const next = moveEntry(story, currentFocus(pending.current, selected), e.key === 'j' ? 1 : -1)
      pending.current = { key: next, at: Date.now() }
      setScrollKey(next)
      select(next)
    }
    window.addEventListener('keydown', h)
    return () => window.removeEventListener('keydown', h)
  })

  if (summary.isLoading) return <Loading label="Loading session…" />
  if (summary.error)
    return (
      <div className="page-frame detail">
        <PageHeader variant="detail" back={{ to: sessionsUrl(pid), label: 'Sessions' }} title={`Session ${sessionId}`} help="sessions.md#read-a-session" helpTopic="Reading a session" />
        <ErrorBox error={summary.error} />
        <p className="quiet-note">
          This session does not exist in {pid}, or it has no spans yet. <Link to={sessionsUrl(pid)}>Back to sessions</Link>
        </p>
      </div>
    )
  const s = summary.data!

  return (
    <div className="page-frame detail wide session-page">
      <PageHeader
        variant="detail"
        back={{ to: sessionsUrl(pid), label: 'Sessions' }}
        title={sessionTitle(s)}
        help="sessions.md#read-a-session"
        helpTopic="Reading a session"
        sentence={
          ready ? (
            <Sentence size="detail" parts={[sessionOutcome({ summary: s, tl, detections: allDets })]} />
          ) : (
            <p className="sentence s-detail sk-sentence" aria-busy="true">
              <span className="sr-only">Reading the session…</span>
              <span className="sk sk-line" aria-hidden />
            </p>
          )
        }
        meta={sessionMetaFigures(s)}
      />
      <Segmented label="Views of this session" role="tabs" panelId="session-view" className="segmented-views" options={VIEWS} value={view} onChange={setView} />
      <div role="tabpanel" id="session-view" aria-labelledby={segmentedTabId('session-view', view)}>
        {view === 'story' &&
          (!ready ? (
            <Loading />
          ) : (
            <StoryView pid={pid} summary={s} tl={tl} story={story} selected={selected} onSelect={(k) => select(k)} scrollKey={scrollKey} replay={replay} />
          ))}
        {view === 'team' && <TeamView pid={pid} sessionId={s.session_id} />}
        {view === 'scores' && <ScoresView pid={pid} sessionId={s.session_id} scores={scores.data?.items ?? []} agents={s.agents} />}
        {view === 'raw' && <RawView pid={pid} traceIds={s.trace_ids ?? []} tl={tl} sessionId={s.session_id} />}
      </div>
    </div>
  )
}

/** Replay verdicts join the tool call they re-checked (shown in its inspector). */
function attachReplays(tl: BuiltTimeline, dets: Detection[]) {
  for (const d of dets) {
    if (d.source !== 'replay' || !d.span_id) continue
    const s = tl.steps.find((x) => x.type === 'tool_call' && x.item.span_id === d.span_id)
    if (s && !s.detections.includes(d)) s.detections.push(d)
  }
}

function TeamView({ pid, sessionId }: { pid: string; sessionId: string }) {
  const g = useQuery({ queryKey: ['v1', pid, 'graph', sessionId], queryFn: () => v1.graph(pid, sessionId) })
  if (g.isLoading) return <Loading />
  if (g.error) return <ErrorBox error={g.error} />
  return (
    <section className="view-section" aria-labelledby="team-h">
      <h2 id="team-h" className="view-h">
        Who handed work to whom
      </h2>
      <GraphWithTables graph={g.data!} agentUrl={(a) => agentUrl(pid, a)} panelClassName="view-panel" tables={<GraphTables graph={g.data!} agentUrl={(a) => agentUrl(pid, a)} />} />
    </section>
  )
}

function ScoresView({ pid, sessionId, scores, agents }: { pid: string; sessionId: string; scores: Score[]; agents: string[] }) {
  const qc = useQueryClient()
  const [hidden, setHidden] = useState<Set<string>>(new Set())
  const [scope, setScope] = useState<'session' | 'agent' | 'handoff'>('session')
  const [picked, setPicked] = useState<Set<string>>(new Set(['GoalCompletionJudge']))
  const [model, setModel] = useState('gpt-4o-mini')
  const [agent, setAgent] = useState('')
  const [open, setOpen] = useState(false)
  const run = useMutation({
    mutationFn: () =>
      v1.runScores({
        project_id: pid,
        scope,
        scorers: [...picked].filter((n) => SCORERS_BY_SCOPE[scope].some((x) => x.name === n)),
        session_ids: [sessionId],
        model,
        agent: scope === 'agent' && agent ? agent : undefined,
      }),
    onSuccess: async (r) => {
      const errs = r.items.filter((x) => x.error)
      notify(
        errs.length ? `${r.items.length - errs.length} scored, ${errs.length} failed: ${errs[0].error}` : `Scored ${r.items.length} target${r.items.length === 1 ? '' : 's'}.`,
        errs.length ? 'error' : 'ok',
        7000,
      )
      await qc.invalidateQueries({ queryKey: ['v1', pid] })
    },
    onError: (e) => notify(`Scoring failed: ${e instanceof Error ? e.message : String(e)}`, 'error', 8000),
  })
  const shown = scores.filter((x) => !hidden.has(x.id))
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
  const scorerOptions = SCORERS_BY_SCOPE[scope]
  return (
    <div className="view-section">
      {shown.length === 0 ? (
        <p className="quiet-note">No scores yet. Run a scorer below, or let a monitor score new sessions automatically.</p>
      ) : (
        <GroupedList title="Scores" count={shown.length}>
          {shown.map((x) => (
            <ListRow
              key={x.id}
              className="score-row"
              title={`${scorerWords(x.scorer).replace(/^./, (c) => c.toUpperCase())} ${x.value == null ? '—' : x.value.toFixed(2)}`}
              summary={x.reason || 'No reason given.'}
              meta={`${x.scope === 'session' ? 'The session' : x.scope === 'agent' ? `Agent ${x.agent ?? ''}` : x.scope === 'handoff' ? 'A hand-off' : x.scope}${x.model ? ` · judged by ${x.model}` : ''}`}
              time={relativeTime(x.created_at)}
              trailing={
                <button type="button" className="icon-btn row-action" aria-label={`Delete ${humanScorer(x.scorer)} score`} onClick={() => del(x)}>
                  <Icon name="trash" size={14} />
                </button>
              }
            />
          ))}
        </GroupedList>
      )}
      <section className="view-panel" aria-labelledby="run-h">
        <div className="row wrap" style={{ justifyContent: 'space-between', gap: 8 }}>
          <h2 id="run-h" className="view-h">
            Run a scorer
          </h2>
          <button type="button" className="qbtn" aria-expanded={open} onClick={() => setOpen((o) => !o)}>
            {open ? 'Cancel' : 'Score this session'}
          </button>
        </div>
        <p className="quiet-note">Scorers judge the whole session, one agent or one hand-off.</p>
        {open && (
          <form
            className="col"
            style={{ gap: 12 }}
            onSubmit={(e) => {
              e.preventDefault()
              run.mutate()
            }}
          >
            <div className="row wrap" style={{ gap: 12 }}>
              <fieldset className="radios inline">
                <legend className="small muted">Score each</legend>
                {(['session', 'agent', 'handoff'] as const).map((sc) => (
                  <label key={sc} className="checkbox">
                    <input
                      type="radio"
                      name="scope"
                      checked={scope === sc}
                      onChange={() => {
                        setScope(sc)
                        setPicked(new Set([SCORERS_BY_SCOPE[sc][0].name]))
                      }}
                    />
                    {sc === 'session' ? 'Session' : sc === 'agent' ? 'Agent' : 'Hand-off'}
                  </label>
                ))}
              </fieldset>
              {scope === 'agent' && (
                <label className="field">
                  Agent
                  <select className="input" value={agent} onChange={(e) => setAgent(e.target.value)}>
                    <option value="">Every agent</option>
                    {agents.map((a) => (
                      <option key={a}>{a}</option>
                    ))}
                  </select>
                </label>
              )}
              <label className="field">
                Judge model
                <input className="input" value={model} onChange={(e) => setModel(e.target.value)} list="judge-models" title="A model with a provider key in Settings, or the offline test judge" />
                <datalist id="judge-models">
                  <option value="gpt-4o-mini" />
                  <option value="claude-3-5-haiku-latest" />
                  <option value="mock/json" label="Offline test judge" />
                </datalist>
              </label>
            </div>
            <fieldset className="scorer-picks">
              <legend className="small muted">Scorers</legend>
              {scorerOptions.map((o) => (
                <label key={o.name} className="checkbox scorer-pick">
                  <input
                    type="checkbox"
                    checked={picked.has(o.name)}
                    onChange={(e) =>
                      setPicked((p) => {
                        const n = new Set(p)
                        if (e.target.checked) n.add(o.name)
                        else n.delete(o.name)
                        return n
                      })
                    }
                  />
                  <span>
                    <strong>{o.label}</strong> <span className="muted small">{o.help}</span>
                  </span>
                </label>
              ))}
            </fieldset>
            <div className="row">
              <button className="qbtn primary" type="submit" disabled={run.isPending || ![...picked].some((n) => scorerOptions.some((x) => x.name === n))}>
                {run.isPending ? 'Scoring…' : 'Score now'}
              </button>
            </div>
          </form>
        )}
      </section>
    </div>
  )
}

function RawView({ pid, traceIds, tl, sessionId }: { pid: string; traceIds: string[]; tl: BuiltTimeline; sessionId: string }) {
  const spans = new Map<string, { type: string; name: string; agent: string | null }>()
  for (const s of tl.steps) {
    if (!s.item.span_id || s.type === 'reasoning' || s.type === 'message' || s.type === 'detection') continue
    if (!spans.has(s.item.span_id)) spans.set(s.item.span_id, { type: STEP_LABEL[s.type], name: stepTitle(s), agent: s.agent })
  }
  return (
    <div className="view-section">
      <section className="view-panel" aria-labelledby="traces-h">
        <h2 id="traces-h" className="view-h" title="A trace is the tree of spans one request produced. A session can span several traces (one per turn, for example).">
          Traces
        </h2>
        <ul className="plain">
          {traceIds.map((t) => (
            <li key={t} className="row wrap" style={{ gap: 12 }}>
              <span className="mono small">
                <CopyText text={t} label="trace id" />
              </span>
              <Link to={traceSpansUrl(pid, t)}>Open in the span explorer</Link>
            </li>
          ))}
          {!traceIds.length && <li className="quiet-note">No trace ids recorded.</li>}
        </ul>
        <p className="quiet-note">
          The older turn-by-turn view is still available: <Link to={`/${pid}/conversations/${encodeURIComponent(sessionId)}`}>turn view</Link>.
        </p>
      </section>
      <SessionSourcesPanel pid={pid} sessionId={sessionId} />
      <PlainTable caption="Spans of this session" count={spans.size} captionVisible>
        <thead>
          <tr>
            <th scope="col">Kind</th>
            <th scope="col">Agent</th>
            <th scope="col">What</th>
            <th scope="col" title="A span is one recorded operation; open it for its inputs, outputs and attributes.">
              Span
            </th>
          </tr>
        </thead>
        <tbody>
          {[...spans.entries()].map(([id, x]) => (
            <tr key={id}>
              <td>{x.type}</td>
              <td>{x.agent ?? '—'}</td>
              <td className="mono small">{oneLine(x.name, 80)}</td>
              <td>
                <Link to={spanUrl(pid, id)} className="mono small">
                  {id}
                </Link>
              </td>
            </tr>
          ))}
        </tbody>
      </PlainTable>
    </div>
  )
}

/**
 * The producers that recorded this session (a Weft plugin, the harness's own
 * traces or logs). The session is shown from the highest-ranked one; spans of
 * the others are hidden, not deleted. A plugin source is claimed by a header
 * any project writer can send, so a project admin can show the hidden spans
 * again and stop honouring plugin claims for the session.
 */
function SessionSourcesPanel({ pid, sessionId }: { pid: string; sessionId: string }) {
  const qc = useQueryClient()
  const src = useQuery({ queryKey: ['v1', pid, 'session-sources', sessionId], queryFn: () => v1.sessionSources(pid, sessionId), retry: false })
  const members = useQuery({ queryKey: ['project-members', pid], queryFn: () => iamApi.projectMembers(pid), retry: false })
  const restore = useMutation({
    mutationFn: () => v1.restoreSessionSources(pid, sessionId),
    onSuccess: async (r) => {
      notify(`${r.restored ?? 0} hidden span${r.restored === 1 ? '' : 's'} shown again; plugin source claims are no longer honoured for this session.`, 'ok', 7000)
      await qc.invalidateQueries({ queryKey: ['v1', pid] })
    },
    onError: (e) => notify(`Restore failed: ${e instanceof Error ? e.message : String(e)}`, 'error', 8000),
  })
  const sources = src.data?.sources ?? []
  if (!sources.length) return null
  const hidden = sources.reduce((n, x) => n + x.hidden, 0)
  const canManage = members.data?.can_manage ?? false
  return (
    <section className="view-panel" aria-labelledby="sources-h">
      <h2 id="sources-h" className="view-h" title="The session is shown from its highest-ranked source; spans of lower-ranked sources are hidden, not deleted.">
        Sources
      </h2>
      <ul className="plain">
        {sources.map((x) => (
          <li key={x.source} className="row wrap" style={{ gap: 12 }}>
            <span className="mono small">{x.source}</span>
            <span className="small">
              {x.spans} shown{x.hidden ? `, ${x.hidden} hidden` : ''}
            </span>
          </li>
        ))}
      </ul>
      {src.data?.ignore_plugin_claims && <p className="quiet-note">Plugin source claims are not honoured for this session (restored by a project admin).</p>}
      {hidden > 0 && (
        <p className="quiet-note">
          {hidden} span{hidden === 1 ? ' is' : 's are'} hidden because a higher-ranked source sent spans for this session. Any project member can claim a
          plugin source, so if those spans are not from a real plugin run,{' '}
          {canManage ? (
            <button type="button" className="qbtn" disabled={restore.isPending} onClick={() => restore.mutate()}>
              show the hidden spans again
            </button>
          ) : (
            'a project admin can show the hidden spans again'
          )}
          .
        </p>
      )}
    </section>
  )
}
