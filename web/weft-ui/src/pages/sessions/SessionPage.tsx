// One session: summary figures, then Timeline, Graph, Reasoning, Security,
// Scores and Raw spans (DESIGN.md §7). Breadcrumbs show Session › Agent ›
// Turn › Step for the focused step.
import { useEffect, useMemo, useState } from 'react'
import { Link, useParams, useSearchParams } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '../../api/client'
import { SCORERS_BY_SCOPE, v1, type Detection, type ReplayJob, type Score } from '../../api/v1'
import { useProjectId } from '../../hooks/useProject'
import { fmtCost, fmtDate, fmtDuration, fmtNumber, fmtRelative } from '../../lib/format'
import { buildTimeline, findStep, oneLine, STEP_LABEL, stepTitle, type BuiltTimeline } from '../../lib/timeline'
import { agentUrl, callUrl, sessionsUrl, sessionUrl, teamUrl, traceSpansUrl } from '../../lib/urls'
import { ErrorBox, Loading } from '../../components/common'
import { AgentGraphView } from '../../components/ui/AgentGraphView'
import {
  ActionBadge,
  AgentChip,
  Callout,
  CopyId,
  Crumbs,
  DetectionBadges,
  humanScorer,
  Metric,
  Panel,
  ScorePill,
  SeverityText,
  StatusPill,
  type Crumb,
} from '../../components/ui/bits'
import { Icon } from '../../components/ui/Icons'
import { usePageHelp } from '../../components/ui/PageHelp'
import { notify, undoable } from '../../components/ui/Toasts'
import { sessionTitle } from '../../components/sessions/SessionList'
import { DetectionDetails, TimelineView } from '../../components/sessions/TimelineView'

type Tab = 'timeline' | 'graph' | 'reasoning' | 'security' | 'scores' | 'raw'

export function SessionPage() {
  const pid = useProjectId()
  const { sessionId = '' } = useParams()
  const [params, setParams] = useSearchParams()
  const tab = (params.get('tab') as Tab) || 'timeline'
  const stepParam = params.get('step')
  const help = usePageHelp(
    'session',
    'Session',
    <>
      Everything one session did, in order. The <strong>Timeline</strong> shows each agent's reasoning right before the tool call or hand-off it led to, and AgentShield's verdict
      on that tool call. Indentation shows which agent was in control. Click a step to see its details; use <kbd className="kbd">j</kbd> and <kbd className="kbd">k</kbd> to move.
    </>,
    'DESIGN.md#7-ui',
  )

  const summary = useQuery({ queryKey: ['v1', pid, 'session', sessionId], queryFn: () => v1.session(pid, sessionId), refetchInterval: 15_000 })
  const timeline = useQuery({ queryKey: ['v1', pid, 'timeline', sessionId], queryFn: () => v1.timeline(pid, sessionId), refetchInterval: 15_000 })
  const detections = useQuery({ queryKey: ['v1', pid, 'detections', 'session', sessionId], queryFn: () => v1.detections(pid, { session_id: sessionId, limit: 1000 }) })
  const scores = useQuery({ queryKey: ['v1', pid, 'scores', sessionId], queryFn: () => v1.scores(pid, { session_id: sessionId, limit: 500 }) })

  const engineDets = useMemo(() => (detections.data?.items ?? []).filter((d) => d.source === 'engine'), [detections.data])
  const tl = useMemo(() => buildTimeline(timeline.data?.items ?? [], engineDets), [timeline.data, engineDets])

  // ?step=problem focuses the most important problem; ?step=<span id> the matching step.
  const focusKey = useMemo(() => {
    if (!stepParam) return null
    if (stepParam === 'problem') return tl.problems[0]?.key ?? null
    return findStep(tl, stepParam)?.key ?? null
  }, [stepParam, tl])

  const setFocus = (key: string | null, _scroll: boolean) => {
    const n = new URLSearchParams(params)
    if (key) n.set('step', key)
    else n.delete('step')
    n.delete('tab')
    setParams(n, { replace: true })
  }
  const setTab = (t: Tab) => {
    const n = new URLSearchParams(params)
    if (t === 'timeline') n.delete('tab')
    else n.set('tab', t)
    setParams(n, { replace: false })
  }

  if (summary.isLoading) return <Loading label="Loading session…" />
  if (summary.error)
    return (
      <div className="page">
        <Crumbs items={[{ label: 'Sessions', to: sessionsUrl(pid) }, { label: sessionId }]} />
        <ErrorBox error={summary.error} />
        <p className="muted">
          This session does not exist in <span className="mono">{pid}</span>, or it has no spans yet. <Link to={sessionsUrl(pid)}>Back to sessions</Link>
        </p>
      </div>
    )
  const s = summary.data!
  const focused = focusKey ? tl.byKey.get(focusKey) : undefined
  const crumbs: Crumb[] = [
    { label: 'Sessions', to: sessionsUrl(pid) },
    { label: oneLine(sessionTitle(s), 48), to: sessionUrl(pid, s.session_id), title: sessionTitle(s) },
  ]
  if (focused) {
    if (focused.agent) crumbs.push({ label: <AgentChip name={focused.agent} size="sm" />, to: agentUrl(pid, focused.agent), title: `Agent ${focused.agent}` })
    const turnStep = tl.steps.find((x) => x.type === 'turn' && x.turn === focused.turn)
    crumbs.push({ label: `Turn ${focused.turn}`, to: turnStep ? sessionUrl(pid, s.session_id, { step: turnStep.key }) : undefined })
    crumbs.push({ label: `${STEP_LABEL[focused.type]}: ${oneLine(stepTitle(focused), 40)}` })
  }
  const sessionStart = new Date(s.started_at).getTime()
  const scoreCount = scores.data?.total_count ?? 0
  const detCount = Object.values(s.detection_count ?? {}).reduce((a, b) => a + (b ?? 0), 0)
  const sessionScores = Object.entries(s.scores ?? {})

  return (
    <div className="page wide session-page">
      <Crumbs items={crumbs} />
      <header className="page-head">
        <div className="grow" style={{ minWidth: 0 }}>
          <div className="row wrap" style={{ gap: 8 }}>
            <StatusPill status={s.status} />
            <DetectionBadges counts={s.detection_count} />
          </div>
          <h1 className="session-h1">{sessionTitle(s)}</h1>
          <div className="row wrap small muted" style={{ gap: 10 }}>
            <span>
              Session <CopyId id={s.session_id} label="Session id" short={24} />
            </span>
            {s.team_id && (
              <Link to={teamUrl(pid, s.team_id)}>
                <Icon name="teams" size={12} /> {s.team_name || s.team_id}
              </Link>
            )}
            <span title={fmtDate(s.started_at)}>started {fmtRelative(s.started_at)}</span>
            <span className="row" style={{ gap: 4 }}>
              {s.agents.map((a) => (
                <AgentChip key={a} name={a} to={agentUrl(pid, a)} size="sm" />
              ))}
            </span>
          </div>
        </div>
        {help.button}
      </header>
      {help.panel}
      <div className="metrics">
        <Metric label="Duration" value={fmtDuration(s.duration_ms)} tip="From the first span's start to the last span's end, across every trace of the session." />
        <Metric label="Turns" value={fmtNumber(s.turn_count)} tip="User requests the agents answered: turn spans, or else one per trace." />
        <Metric
          label="Steps"
          value={fmtNumber(s.llm_call_count + s.tool_call_count)}
          sub={`${s.llm_call_count} model · ${s.tool_call_count} tool`}
          tip="Model calls plus tool calls. Hand-offs and reasoning are counted separately."
        />
        <Metric label="Hand-offs" value={fmtNumber(s.handoff_count)} tip="Times one agent passed work to another. Returns of control are not counted." />
        <Metric
          label="Reasoning"
          value={fmtNumber(s.reasoning_step_count)}
          sub={s.reasoning_tokens ? `${fmtNumber(s.reasoning_tokens)} tokens` : undefined}
          tip="Reasoning steps (thinking blocks, reasoning summaries) the models produced. Reasoning tokens are reported by the provider and are part of output tokens."
        />
        <Metric
          label="Tokens"
          value={fmtNumber(s.input_tokens + s.output_tokens)}
          sub={`${fmtNumber(s.input_tokens)} in · ${fmtNumber(s.output_tokens)} out`}
          tip="Input plus output tokens of every model call in the session. Input includes cached tokens."
        />
        <Metric
          label="Cost"
          value={fmtCost(s.cost_usd)}
          tip="Each model call's tokens times the model's price: input, output, and cached reads and writes at their own prices. Calls to models without a known price are left out; “—” means no price was known."
        />
        <Metric
          label="Errors"
          value={fmtNumber(s.error_count)}
          tone={s.error_count ? 'error' : undefined}
          tip="Spans that ended with an error status, including tool calls the agents recovered from."
        />
        {sessionScores.length > 0 && (
          <Metric
            label="Scores"
            value={
              <span className="row wrap" style={{ gap: 4 }}>
                {sessionScores.map(([k, v]) => (
                  <ScorePill key={k} scorer={k} value={v.mean} count={v.count} />
                ))}
              </span>
            }
            tip="Session-level scores: the mean of each scorer's results, from 0 (bad) to 1 (good). The Scores tab shows each result with its reason."
          />
        )}
      </div>
      {tl.problems.length > 0 && tab === 'timeline' && <ProblemsCallout tl={tl} focus={focusKey} onGo={(k) => setFocus(k, true)} />}
      <div className="tabs tabs-scroll" role="tablist" aria-label="Session views">
        {(
          [
            ['timeline', 'Timeline', null],
            ['graph', 'Graph', s.agents.length],
            ['reasoning', 'Reasoning', s.reasoning_step_count],
            ['security', 'Security', detCount],
            ['scores', 'Scores', scoreCount],
            ['raw', 'Raw spans', s.span_count],
          ] as [Tab, string, number | null][]
        ).map(([id, label, n]) => (
          <button key={id} role="tab" aria-selected={tab === id} className={`tab ${tab === id ? 'active' : ''}`} onClick={() => setTab(id)}>
            {label}
            {n != null && n > 0 && <span className="tab-count">{n}</span>}
          </button>
        ))}
      </div>
      <div role="tabpanel" aria-label={tab}>
        {tab === 'timeline' && (timeline.isLoading ? <Loading /> : <TimelineView pid={pid} tl={tl} focus={focusKey} onFocus={setFocus} sessionStart={sessionStart} />)}
        {tab === 'graph' && <GraphTab pid={pid} sessionId={s.session_id} />}
        {tab === 'reasoning' && <ReasoningTab pid={pid} sessionId={s.session_id} tl={tl} />}
        {tab === 'security' && <SecurityTab pid={pid} sessionId={s.session_id} dets={detections.data?.items ?? []} tl={tl} />}
        {tab === 'scores' && <ScoresTab pid={pid} sessionId={s.session_id} scores={scores.data?.items ?? []} agents={s.agents} />}
        {tab === 'raw' && <RawTab pid={pid} traceIds={s.trace_ids ?? []} tl={tl} sessionId={s.session_id} />}
      </div>
    </div>
  )
}

function ProblemsCallout({ tl, focus, onGo }: { tl: BuiltTimeline; focus: string | null; onGo: (k: string) => void }) {
  const items = tl.problems.slice(0, 3)
  const title =
    tl.problems[0].problem === 'block'
      ? 'AgentShield blocked a tool call in this session'
      : tl.problems[0].problem === 'approval'
        ? 'AgentShield asked for approval in this session'
        : 'Something failed in this session'
  return (
    <Callout tone={tl.problems[0].problem === 'approval' ? 'warn' : 'error'} title={title}>
      <ul className="problem-list">
        {items.map((p) => {
          const d = p.detections.find((x) => x.action === 'block' || x.action === 'require_approval')
          return (
            <li key={p.key}>
              <span className="problem-what">
                {d ? <ActionBadge action={d.action} /> : <StatusPill status="error" />} <AgentChip name={p.agent} size="sm" />{' '}
                <span className="mono small">{oneLine(stepTitle(p), 70)}</span>
                {d?.rules[0] && (
                  <span className="small">
                    {' '}
                    — rule <strong>{d.rules[0].title}</strong>
                  </span>
                )}
              </span>
              <button type="button" className="btn sm" onClick={() => onGo(p.key)} aria-current={focus === p.key ? 'step' : undefined}>
                Show step <Icon name="chevron" size={12} />
              </button>
            </li>
          )
        })}
      </ul>
      {tl.problems.length > 3 && <div className="small muted">and {tl.problems.length - 3} more (tick “Only problems” to list them)</div>}
    </Callout>
  )
}

function GraphTab({ pid, sessionId }: { pid: string; sessionId: string }) {
  const g = useQuery({ queryKey: ['v1', pid, 'graph', sessionId], queryFn: () => v1.graph(pid, sessionId) })
  if (g.isLoading) return <Loading />
  if (g.error) return <ErrorBox error={g.error} />
  return (
    <Panel
      title="Who handed work to whom"
      tip="Each circle is an agent, sized by how many times it ran. Arrows are hand-offs, labelled with their count, failures and median time; dashed arrows are returns of control. Red marks errors or blocked tool calls."
    >
      <AgentGraphView graph={g.data!} agentUrl={(a) => agentUrl(pid, a)} />
    </Panel>
  )
}

function ReasoningTab({ pid, sessionId, tl }: { pid: string; sessionId: string; tl: BuiltTimeline }) {
  const r = useQuery({ queryKey: ['v1', pid, 'reasoning', sessionId], queryFn: () => v1.reasoning(pid, sessionId) })
  if (r.isLoading) return <Loading />
  if (r.error) return <ErrorBox error={r.error} />
  const items = r.data?.items ?? []
  if (!items.length)
    return (
      <p className="muted">
        No reasoning was recorded. Models that think (Anthropic thinking, OpenAI reasoning summaries, Gemini thoughts) are picked up automatically; with the SDK, call{' '}
        <span className="mono">weft.reasoning(text)</span> inside a span.
      </p>
    )
  return (
    <ol className="reasoning-list">
      {items.map((x) => {
        const target = x.next_span_id ? tl.steps.find((s) => s.item.span_id === x.next_span_id && (s.type === 'tool_call' || s.type === 'handoff')) : undefined
        return (
          <li key={x.id} className="reasoning-item">
            <div className="row wrap" style={{ gap: 8 }}>
              <AgentChip name={x.agent_name} to={x.agent_name ? agentUrl(pid, x.agent_name) : undefined} size="sm" />
              <span className="small muted">
                {x.kind === 'summary' ? 'summary' : x.kind === 'redacted' ? 'redacted' : 'thinking'}
                {x.tokens != null ? ` · ${fmtNumber(x.tokens)} tokens` : ''} · {fmtRelative(x.started_at)}
              </span>
              <span className="grow" />
              <Link className="small" to={sessionUrl(pid, sessionId, { step: `reasoning:${x.id}` })}>
                Show in timeline
              </Link>
            </div>
            <p className="pre">{x.kind === 'redacted' ? <em className="muted">Hidden by the provider.</em> : x.text}</p>
            {target ? (
              <Link className="led-to inline" to={sessionUrl(pid, sessionId, { step: target.key })}>
                <Icon name="chevron" size={12} /> led to <strong>{stepTitle(target)}</strong>
                {target.problem && (
                  <span className={`text-${target.problem === 'approval' ? 'warn' : 'error'}`}>
                    {' '}
                    ({target.problem === 'block' ? 'blocked' : target.problem === 'approval' ? 'needed approval' : 'failed'})
                  </span>
                )}
              </Link>
            ) : (
              <span className="small faint">did not lead to a tool call or hand-off</span>
            )}
          </li>
        )
      })}
    </ol>
  )
}

function SecurityTab({ pid, sessionId, dets, tl }: { pid: string; sessionId: string; dets: Detection[]; tl: BuiltTimeline }) {
  const qc = useQueryClient()
  const status = useQuery({ queryKey: ['v1', pid, 'shield-status'], queryFn: () => v1.shieldStatus(pid), staleTime: 60_000 })
  const [jobId, setJobId] = useState<string | null>(null)
  const start = useMutation({
    mutationFn: () => v1.replayStart({ project_id: pid, session_ids: [sessionId], label: `session-${new Date().toISOString().slice(0, 16).replace(/[-:T]/g, '')}` }),
    onSuccess: (r) => setJobId(r.job_id),
    onError: (e) => notify(`Replay failed to start: ${e instanceof Error ? e.message : String(e)}`, 'error'),
  })
  const job = useQuery({
    queryKey: ['v1', pid, 'replay', jobId],
    queryFn: () => v1.replayJob(pid, jobId!),
    enabled: !!jobId,
    refetchInterval: (q) => (q.state.data && (q.state.data.status === 'done' || q.state.data.status === 'failed') ? false : 800),
  })
  useEffect(() => {
    if (job.data?.status === 'done') void qc.invalidateQueries({ queryKey: ['v1', pid, 'detections'] })
  }, [job.data?.status, qc, pid])
  const engine = dets.filter((d) => d.source === 'engine')
  const replay = dets.filter((d) => d.source === 'replay')
  const stepFor = (d: Detection) => (d.span_id ? tl.steps.find((s) => s.type === 'tool_call' && s.item.span_id === d.span_id)?.key : undefined)
  return (
    <div className="col" style={{ gap: 16 }}>
      <Panel
        title="Re-check with the current rules"
        tip="Replays every tool call of this session through AgentShield's rules in shadow mode and compares the result with the verdicts recorded at the time. Nothing is blocked or changed in your agents."
        actions={
          <button className="btn sm primary" disabled={!status.data?.replay_enabled || start.isPending || job.data?.status === 'running'} onClick={() => start.mutate()}>
            <Icon name="replay" size={13} /> {start.isPending ? 'Starting…' : 'Replay with current rules'}
          </button>
        }
      >
        {status.data && !status.data.replay_enabled && (
          <p className="muted small">
            Replay is off on this server. Start Weft with <span className="mono">--shield-rules-dir rules/rules</span> (AgentShield's Sigma rules) or{' '}
            <span className="mono">--shield-url</span> pointing at a running engine.
          </p>
        )}
        {status.data?.replay_enabled && !jobId && <p className="muted small">Shows which tool calls would now be blocked, no longer blocked, or judged differently.</p>}
        {job.data && <ReplayResult pid={pid} job={job.data} />}
      </Panel>
      <Panel title={`Verdicts recorded by AgentShield (${engine.length})`} tip="What the AgentShield engine decided when the agents ran, linked to the exact tool call.">
        {engine.length === 0 ? (
          <p className="muted">AgentShield recorded no verdicts for this session. Either nothing matched a rule, or the engine is not sending verdicts to Weft (see Security).</p>
        ) : (
          <DetectionTable pid={pid} sessionId={sessionId} dets={engine} stepFor={stepFor} />
        )}
      </Panel>
      {replay.length > 0 && (
        <Panel
          title={`Replay verdicts (${replay.length})`}
          tip="Verdicts produced by re-running the rules on the recorded tool calls (shadow mode). They never affected the agents."
        >
          <DetectionTable pid={pid} sessionId={sessionId} dets={replay} stepFor={stepFor} />
        </Panel>
      )}
    </div>
  )
}

function DetectionTable({ pid, sessionId, dets, stepFor }: { pid: string; sessionId: string; dets: Detection[]; stepFor: (d: Detection) => string | undefined }) {
  return (
    <ul className="det-list">
      {dets.map((d) => {
        const key = stepFor(d)
        return (
          <li key={d.id} className="det-item">
            <div className="row wrap" style={{ gap: 8 }}>
              <ActionBadge action={d.action} overridden={d.overridden} />
              <AgentChip name={d.agent_name} size="sm" />
              <span className="mono small">{d.tool_name}</span>
              <span className="faint small">{fmtRelative(d.created_at)}</span>
              <span className="grow" />
              {key ? (
                <Link className="btn sm" to={sessionUrl(pid, sessionId, { step: key })}>
                  Show step <Icon name="chevron" size={12} />
                </Link>
              ) : (
                <span className="small faint" title="No tool span with the same session and tool-call id (or tool name within ±5 s) was found">
                  not linked to a step
                </span>
              )}
            </div>
            <DetectionDetails pid={pid} dets={[d]} />
          </li>
        )
      })}
    </ul>
  )
}

export function ReplayResult({ pid, job }: { pid: string; job: ReplayJob }) {
  if (job.status === 'queued' || job.status === 'running')
    return (
      <p className="muted small">
        <span className="spinner" /> Replaying… ({job.status})
      </p>
    )
  if (job.status === 'failed') return <ErrorBox error={job.error ?? 'Replay failed'} />
  const sm = job.summary
  const groups: [string, string, NonNullable<ReplayJob['newly_blocked']>][] = [
    ['Newly blocked', 'Tool calls the current rules would block that were not blocked at the time.', job.newly_blocked ?? []],
    ['No longer blocked', 'Tool calls that were blocked at the time but would now be allowed.', job.no_longer_blocked ?? []],
    ['Changed', 'A different action or a different set of rules, otherwise.', job.changed ?? []],
  ]
  return (
    <div className="col" style={{ gap: 10 }}>
      {sm && (
        <p className="small">
          Checked <strong>{sm.evaluated}</strong> of {sm.tool_calls} tool calls with <span className="mono">{job.ruleset}</span> ({job.rules_loaded ?? '?'} rules):{' '}
          <strong>{sm.newly_blocked}</strong> newly blocked, <strong>{sm.no_longer_blocked}</strong> no longer blocked, <strong>{sm.changed}</strong> changed, {sm.unchanged}{' '}
          unchanged.
        </p>
      )}
      {groups.map(([title, help, xs]) =>
        xs.length ? (
          <div key={title}>
            <h3 className="small-h" title={help}>
              {title} ({xs.length})
            </h3>
            <ul className="plain diff-list">
              {xs.map((c) => (
                <li key={c.span_id}>
                  <AgentChip name={c.agent_name} size="sm" /> <span className="mono small">{c.tool_name}</span>{' '}
                  <span className="small">
                    {c.before ? <ActionBadge action={c.before.action} /> : <span className="faint">no verdict</span>} →{' '}
                    {c.after ? <ActionBadge action={c.after.action} /> : <span className="faint">allowed</span>}
                  </span>{' '}
                  <Link className="small" to={sessionUrl(pid, c.session_id, { step: `tool:${c.span_id}` })}>
                    Show step
                  </Link>
                </li>
              ))}
            </ul>
          </div>
        ) : null,
      )}
      {job.truncated && <p className="small muted">Lists are cut at 500 items.</p>}
    </div>
  )
}

function ScoresTab({ pid, sessionId, scores, agents }: { pid: string; sessionId: string; scores: Score[]; agents: string[] }) {
  const qc = useQueryClient()
  const [hidden, setHidden] = useState<Set<string>>(new Set())
  const [scope, setScope] = useState<'session' | 'agent' | 'handoff'>('session')
  const [picked, setPicked] = useState<Set<string>>(new Set(['GoalCompletionJudge']))
  const [model, setModel] = useState('gpt-4o-mini')
  const [agent, setAgent] = useState('')
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
        await api.feedbackPurge(pid, x.id)
        await qc.invalidateQueries({ queryKey: ['v1', pid] })
      },
    })
  const scorerOptions = SCORERS_BY_SCOPE[scope]
  return (
    <div className="col" style={{ gap: 16 }}>
      <Panel
        title="Run scorers"
        tip="Scores the session, each agent, or each hand-off with LLM judges or the tool-safety replay. Results are stored and shown below with the judge's reason."
      >
        <form
          className="col"
          style={{ gap: 10 }}
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
              <input className="input" value={model} onChange={(e) => setModel(e.target.value)} list="judge-models" />
              <datalist id="judge-models">
                <option value="gpt-4o-mini" />
                <option value="claude-3-5-haiku-latest" />
                <option value="mock/json" />
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
            <button className="btn primary" type="submit" disabled={run.isPending || ![...picked].some((n) => scorerOptions.some((x) => x.name === n))}>
              {run.isPending ? 'Scoring…' : 'Run scorers'}
            </button>
            <span className="small muted">Judges need a provider key for the model (Settings → Secrets); mock/json works offline.</span>
          </div>
        </form>
      </Panel>
      <Panel title={`Scores (${shown.length})`} tip="Each score with its scope, scorer, value (0 to 1) and the scorer's reason.">
        {shown.length === 0 ? (
          <p className="muted">No scores yet. Run a scorer above, or let a monitor score new sessions automatically.</p>
        ) : (
          <ul className="score-list">
            {shown.map((x) => (
              <li key={x.id} className="score-item">
                <ScorePill value={x.value} scorer={x.scorer} />
                <span className="small muted">
                  {x.scope === 'session' ? 'Session' : x.scope === 'agent' ? `Agent ${x.agent ?? ''}` : x.scope === 'handoff' ? 'Hand-off' : x.scope}
                  {x.model ? ` · judged by ${x.model}` : ''} · {fmtRelative(x.created_at)}
                </span>
                <span className="grow score-reason">{x.reason || <span className="faint">no reason given</span>}</span>
                <button type="button" className="icon-btn" aria-label={`Delete ${humanScorer(x.scorer)} score`} onClick={() => del(x)}>
                  <Icon name="trash" size={13} />
                </button>
              </li>
            ))}
          </ul>
        )}
      </Panel>
    </div>
  )
}

function RawTab({ pid, traceIds, tl, sessionId }: { pid: string; traceIds: string[]; tl: BuiltTimeline; sessionId: string }) {
  const spans = new Map<string, { type: string; name: string; agent: string | null }>()
  for (const s of tl.steps) {
    if (!s.item.span_id || s.type === 'reasoning' || s.type === 'message' || s.type === 'detection') continue
    if (!spans.has(s.item.span_id)) spans.set(s.item.span_id, { type: STEP_LABEL[s.type], name: stepTitle(s), agent: s.agent })
  }
  return (
    <div className="col" style={{ gap: 16 }}>
      <Panel title="Traces" tip="A trace is the tree of spans one request produced. A session can span several traces (one per turn, for example).">
        <ul className="plain">
          {traceIds.map((t) => (
            <li key={t} className="row wrap" style={{ gap: 8 }}>
              <CopyId id={t} label="Trace id" short={16} />
              <Link to={traceSpansUrl(pid, t)}>Open in the span explorer</Link>
            </li>
          ))}
          {!traceIds.length && <li className="muted">No trace ids recorded.</li>}
        </ul>
        <p className="small muted">
          The older turn-by-turn view is still available: <Link to={`/${pid}/conversations/${encodeURIComponent(sessionId)}`}>turn view</Link>.
        </p>
      </Panel>
      <Panel title={`Spans (${spans.size})`} tip="Spans are the raw records the timeline is built from. Open one for its inputs, outputs, attributes and feedback.">
        <div className="table-wrap">
          <table className="table">
            <caption className="sr-only">Spans of this session</caption>
            <thead>
              <tr>
                <th scope="col">Kind</th>
                <th scope="col">Agent</th>
                <th scope="col">What</th>
                <th scope="col">Span</th>
              </tr>
            </thead>
            <tbody>
              {[...spans.entries()].map(([id, x]) => (
                <tr key={id}>
                  <td>{x.type}</td>
                  <td>{x.agent ? <AgentChip name={x.agent} size="sm" /> : '—'}</td>
                  <td className="mono small">{oneLine(x.name, 80)}</td>
                  <td>
                    <Link to={callUrl(pid, id)} className="mono small">
                      {id}
                    </Link>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </Panel>
    </div>
  )
}

