// Security (DESIGN.md §7, §7.2): answers "what did AgentShield stop or
// question, and why?". One generated sentence (what it blocked, asked
// approval for and logged, and how much was overridden), then the
// detections grouped by rule, each rule in plain words with its severity
// and its detections as rows that open the step in the session's story.
// Approval fatigue and override escalation follow as quiet sentences when
// present; the chart over time is small; "Rules changed? Replay" lists the
// replay jobs with what each would change. A guided setup shows while the
// engine is not connected.
import { useMemo, useState } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { v1, type Detection, type DetectionStats } from '../../api/v1'
import { useProjectId } from '../../hooks/useProject'
import { SCOPE_LIMIT, sessionScopeParams, useSessionScope, type SessionScope } from '../../hooks/useSessionScope'
import { filterQueryString, isEmptyFilter, removeKey, timeWindow, TIME_LABEL, type FilterState, type TimeRange } from '../../lib/filters'
import { fmtDate, flaggedOf } from '../../lib/format'
import { relativeTime } from '../../lib/summaries'
import {
  crossAgentSentences,
  detectionGlyph,
  detectionLine,
  detectionMeta,
  fatigueSentence,
  groupCounts,
  linkedSentence,
  overrideSentence,
  rangePhrase,
  replayGlyph,
  replayLine,
  replayMeta,
  ruleGroups,
  securitySentence,
  type RuleGroup,
  type SecPart,
} from '../../lib/summaries/security'
import { agentUrl, securityUrl, sessionUrl } from '../../lib/urls'
import { ErrorBox, Loading } from '../../components/common'
import { FilterBar, FilterButton, FilterSearchField, useFilters, useWidenedTime, WidenedNote } from '../../components/ui/FilterBar'
import { Snippets } from '../../components/ui/EmptyState'
import { GroupedList, ListRow } from '../../components/ui/GroupedList'
import { CapNote, Tip } from '../../components/ui/bits'
import { Icon } from '../../components/ui/Icons'
import { PageHeader, helpUrl } from '../../components/ui/PageHeader'
import { PlainTable } from '../../components/ui/PlainTable'
import { Segmented } from '../../components/ui/Segmented'
import { Sentence, type SentencePart } from '../../components/ui/Sentence'
import { StatusGlyph } from '../../components/ui/StatusGlyph'
import { notify } from '../../components/ui/Toasts'
import { bucketize, StackedBars } from '../../components/ui/StackedBars'
import { ReplayResult } from '../sessions/SessionPage'
import '../observe.css'

const DEFAULT_TIME: TimeRange = '24h'
/** Rows a rule group shows before "Show more". */
const GROUP_ROWS = 5

const RANGES: { id: TimeRange; label: string }[] = [
  { id: '24h', label: '24 hours' },
  { id: '7d', label: '7 days' },
  { id: '30d', label: '30 days' },
]

/**
 * /api/v1/detections parameters. `sessionFilter` holds the /api/v1/sessions
 * parameters of the chips that go through sessions (team, status, score, …);
 * they are sent as session.<name> and the server resolves every matching
 * session itself (no list of ids, no cap). The detections' time range is the
 * sessions' range too.
 */
export function detectionParams(s: FilterState, time: TimeRange, sessionFilter?: Record<string, string> | null): Record<string, string | string[]> {
  const p: Record<string, string | string[]> = { source: 'engine' }
  if (s.agent) p.agent = s.agent
  if (s.tool) p.tool_name = s.tool
  if (s.has === 'block') p.action = 'block'
  else if (s.has === 'approval') p.action = 'require_approval'
  else if (s.has === 'flagged') p.action = 'block,require_approval'
  else if (s.has === 'any') p.action = 'block,require_approval,log'
  for (const [k, v] of Object.entries(sessionFilter ?? {})) if (k !== 'from' && k !== 'to') p[`session.${k}`] = v
  const w = timeWindow(s, time)
  if (w.from) p.from = w.from
  if (w.to) p.to = w.to
  return p
}

/**
 * Figures for the detections in hand, for when the page narrows them in the
 * browser (text filter): the server's tool-call counts give the per-agent rate.
 */
export function statsFromDetections(dets: Detection[], server?: DetectionStats): DetectionStats {
  const out: DetectionStats = { total: dets.length, by_action: {}, by_severity: {}, by_source: {}, overridden: 0, linked: 0, by_rule: [], by_agent: [], by_day: [] }
  const rules = new Map<string, DetectionStats['by_rule'][number]>()
  const agents = new Map<string, number>()
  // Tool calls (linked detections) per agent: a call with several verdicts counts once.
  const flagged = new Map<string, Set<string>>()
  for (const d of dets) {
    out.by_action[d.action] = (out.by_action[d.action] ?? 0) + 1
    const sev = (d.severity || 'none').toLowerCase()
    out.by_severity[sev] = (out.by_severity[sev] ?? 0) + 1
    out.by_source[d.source] = (out.by_source[d.source] ?? 0) + 1
    if (d.overridden) out.overridden++
    if (d.span_id) out.linked++
    for (const r of d.rules) {
      const x = rules.get(r.id) ?? { rule_id: r.id, title: r.title, level: r.level, count: 0, by_action: {} }
      x.count++
      x.by_action[d.action] = (x.by_action[d.action] ?? 0) + 1
      rules.set(r.id, x)
    }
    if (d.agent_name) {
      agents.set(d.agent_name, (agents.get(d.agent_name) ?? 0) + 1)
      if (d.span_id && d.action !== 'allow') flagged.set(d.agent_name, (flagged.get(d.agent_name) ?? new Set()).add(d.span_id))
    }
  }
  out.by_rule = [...rules.values()].sort((a, b) => b.count - a.count)
  out.by_agent = [...agents.entries()]
    .map(([agent, count]) => {
      const calls = server?.by_agent.find((a) => a.agent === agent)?.tool_calls ?? 0
      const f = flagged.get(agent)?.size ?? 0
      return { agent, count, by_action: {}, tool_calls: calls, flagged_tool_calls: f, detection_rate: calls ? Math.min(1, f / calls) : null }
    })
    .sort((a, b) => b.count - a.count)
  return out
}

export function detectionMatchesText(d: Detection, q?: string): boolean {
  if (!q) return true
  const n = q.toLowerCase()
  return [d.session_id, d.tool_name, d.agent_name, d.severity, d.action, ...d.rules.map((r) => r.title), ...d.rules.map((r) => r.id)].some((x) => x && x.toLowerCase().includes(n))
}

const ACTION_SERIES = [
  { id: 'block', label: 'Blocked', cls: 'c-block' },
  { id: 'require_approval', label: 'Needs approval', cls: 'c-approval' },
  { id: 'log', label: 'Logged', cls: 'c-log' },
]
const SEVERITY_SERIES = [
  { id: 'critical', label: 'Critical', cls: 'c-critical' },
  { id: 'high', label: 'High', cls: 'c-high' },
  { id: 'medium', label: 'Medium', cls: 'c-medium' },
  { id: 'low', label: 'Low', cls: 'c-low' },
  { id: 'informational', label: 'Info', cls: 'c-info' },
]

export function SecurityPage() {
  const pid = useProjectId()
  const [state, setState] = useFilters()
  const [params, setParams] = useSearchParams()
  const [filterOpen, setFilterOpen] = useState(false)
  const status = useQuery({ queryKey: ['v1', pid, 'shield-status'], queryFn: () => v1.shieldStatus(pid), staleTime: 60_000 })
  const ever = useQuery({ queryKey: ['v1', pid, 'detections', 'ever'], queryFn: () => v1.detectionStats(pid, { source: 'engine' }) })
  const noTime = detectionParams({ ...state, time: 'all' }, 'all')
  const widen = useWidenedTime(state, DEFAULT_TIME, async (r) => (await v1.detectionStats(pid, detectionParams({ ...state, time: r }, r))).total, [pid, 'detections', noTime])
  const time = widen.time
  // Team, status, score, model and cost belong to sessions: keep detections in matching sessions.
  // The server resolves those sessions for detections and their figures.
  const dp = detectionParams(state, time, sessionScopeParams('security', state, time))
  const ready = widen.ready
  // The matching sessions themselves (at most SCOPE_LIMIT) narrow the approval-fatigue sentences and pick what a replay covers.
  const scope = useSessionScope(pid, 'security', state, time, widen.ready)
  const serverStats = useQuery({ queryKey: ['v1', pid, 'detections', 'stats', dp], queryFn: () => v1.detectionStats(pid, dp), enabled: ready, placeholderData: keepPreviousData })
  const list = useQuery({
    queryKey: ['v1', pid, 'detections', 'list', dp],
    queryFn: () => v1.detections(pid, { ...dp, limit: 1000 }),
    enabled: ready,
    placeholderData: keepPreviousData,
  })
  const w = timeWindow(state, time)
  const insights = useQuery({
    queryKey: ['v1', pid, 'insights', w],
    queryFn: () => v1.insights(pid, { ...w, bucket: time === '1h' || time === '24h' ? 'hour' : 'day' }),
    enabled: widen.ready,
    placeholderData: keepPreviousData,
  })
  const dets = useMemo(() => (list.data?.items ?? []).filter((d) => detectionMatchesText(d, state.q)), [list.data, state.q])
  // The text filter runs here, so the figures are counted from the matching detections.
  const st = state.q && list.data ? statsFromDetections(dets, serverStats.data) : serverStats.data
  const ruleFilter = params.get('rule')
  const groups = useMemo(() => {
    const gs = ruleGroups(dets)
    return ruleFilter ? gs.filter((g) => g.id === ruleFilter || g.detections.some((d) => d.rules.some((r) => r.id === ruleFilter))) : gs
  }, [dets, ruleFilter])

  const connected = (ever.data?.total ?? 0) > 0
  const filtered = !isEmptyFilter({ ...state, time: undefined, from: undefined, to: undefined }, DEFAULT_TIME)
  const keep = (s: { session_id: string; agents: string[] }) => (!scope.active || scope.ids.has(s.session_id)) && (!state.agent || s.agents.includes(state.agent))

  const toPart = (p: SecPart): SentencePart => {
    if (typeof p === 'string') return p
    if (p.session) return { text: p.text, to: sessionUrl(pid, p.session.id, { step: p.session.step }), title: 'Open this session at the step' }
    return { text: p.text, to: securityUrl(pid, filterQueryString({ ...state, ...p.filter })), title: 'Show only these detections' }
  }

  const header = (
    <PageHeader
      title="Security"
      help="security.md"
      controls={
        connected ? (
          <>
            <Segmented
              label="Time range"
              options={RANGES}
              value={(RANGES.some((r) => r.id === time) ? time : null) as TimeRange | null}
              onChange={(t) => setState({ ...removeKey(state, 'time'), time: t === DEFAULT_TIME && !widen.widened ? undefined : t })}
            />
            <FilterSearchField label="Search detections" />
            <FilterButton open={filterOpen} onToggle={() => setFilterOpen((o) => !o)} defaultTime={DEFAULT_TIME} />
          </>
        ) : undefined
      }
      sentence={
        !connected ? (
          ever.data ? (
            <Sentence parts={['AgentShield has not sent any verdicts to this project yet.']} />
          ) : undefined
        ) : st && list.data ? (
          <Sentence parts={securitySentence({ stats: st, time, filtered }).map(toPart)} />
        ) : (
          <p className="sentence s-outcome sk-sentence" aria-busy="true">
            <span className="sr-only">Counting verdicts…</span>
            <span className="sk sk-line" aria-hidden />
          </p>
        )
      }
    />
  )

  if (ever.isLoading)
    return (
      <div className="page-frame obs-page">
        {header}
        <Loading />
      </div>
    )
  if (!connected)
    return (
      <div className="page-frame obs-page">
        {header}
        <ErrorBox error={ever.error} />
        <SetupGuide pid={pid} replay={status.data?.replay_enabled ?? false} />
      </div>
    )

  return (
    <div className="page-frame obs-page security-page">
      {header}
      <FilterBar
        page="security"
        search="external"
        timePicker="none"
        collapsible
        open={filterOpen}
        defaultTime={DEFAULT_TIME}
        effectiveTime={time}
        options={{ agents: st?.by_agent.map((a) => a.agent) }}
      />
      {widen.widened && <WidenedNote time={time} defaultTime={DEFAULT_TIME} onReset={() => setState({ ...state, time: DEFAULT_TIME })} />}
      <ErrorBox error={serverStats.error ?? list.error ?? scope.error} />
      {list.data && (
        <CapNote
          shown={list.data.items.length}
          total={list.data.total_count}
          what="detections"
          why={state.q ? 'the newest ones; the text filter, the groups, the chart and the sentence use only these' : 'the newest ones in the groups and the chart; the sentence counts them all'}
        />
      )}
      {ruleFilter && (
        <p className="quiet-note">
          Showing one rule.{' '}
          <button
            type="button"
            className="linkish"
            onClick={() => {
              const n = new URLSearchParams(params)
              n.delete('rule')
              setParams(n, { replace: true })
            }}
          >
            Show every rule
          </button>
        </p>
      )}

      {list.isLoading || !ready ? (
        <Loading />
      ) : groups.length === 0 ? (
        <div className="quiet-empty">
          <p>{filtered || ruleFilter ? 'Nothing matches these filters.' : `AgentShield flagged nothing ${rangePhrase(time) || 'yet'}.`}</p>
          {(filtered || ruleFilter) && (
            <button type="button" className="qbtn" onClick={() => setParams(new URLSearchParams(time === DEFAULT_TIME ? {} : { time }))}>
              Clear filters
            </button>
          )}
        </div>
      ) : (
        <div className="obs-groups">
          {groups.map((g) => (
            <RuleGroupList key={g.id || 'none'} pid={pid} group={g} />
          ))}
          {st && st.linked < st.total && <p className="quiet-note">{linkedSentence(st)}</p>}
        </div>
      )}

      <Patterns pid={pid} insights={insights.data} keep={keep} toPart={toPart} />
      {scope.truncated && <CapNote shown={SCOPE_LIMIT} total={scope.total} what="matching sessions" why="the approval sentences and replay use the most recent ones" />}

      {dets.length > 0 && <OverTime dets={dets} from={w.from} to={w.to} />}
      {st && st.by_agent.length > 0 && <ByAgent pid={pid} stats={st} />}

      <ReplaySection pid={pid} enabled={status.data?.replay_enabled ?? false} state={state} time={time} scope={scope} />
    </div>
  )
}

/** One rule: its plain words and severity, then its detections, each opening the step. */
function RuleGroupList({ pid, group: g }: { pid: string; group: RuleGroup }) {
  const [n, setN] = useState(GROUP_ROWS)
  const id = `rule-${(g.id || 'none').replace(/[^a-zA-Z0-9_-]/g, '-')}`
  return (
    <section className="obs-group" aria-labelledby={id}>
      <div className="obs-group-head">
        <h2 className="glist-h" id={id}>
          {g.heading}
          <span className="glist-count"> {g.count.toLocaleString()}</span>
        </h2>
        <p className="obs-group-why">
          {g.reason}
          {g.ruleTitle && <span className="obs-rule-title"> Rule: {g.ruleTitle}.</span>} <span className="obs-rule-title">{groupCounts(g)}.</span>
        </p>
      </div>
      <GroupedList
        label={`Detections of ${g.heading}`}
        footer={
          g.detections.length > n ? (
            <button type="button" className="qbtn obs-more" onClick={() => setN((x) => x + 25)}>
              Show {Math.min(25, g.detections.length - n)} more of {g.detections.length - n}
            </button>
          ) : undefined
        }
      >
        {g.detections.slice(0, n).map((d) => {
          const meta = detectionMeta(d)
          return (
            <ListRow
              key={d.id}
              className="det-row"
              glyph={<StatusGlyph kind={detectionGlyph(d)} label={d.action === 'log' ? 'Logged' : undefined} />}
              title={d.session_title?.trim() || `Session ${d.session_id}`}
              summary={detectionLine(d)}
              meta={meta || undefined}
              time={relativeTime(d.created_at)}
              timeTitle={fmtDate(d.created_at)}
              to={sessionUrl(pid, d.session_id, { step: d.span_id ? `tool:${d.span_id}` : 'problem' })}
            />
          )
        })}
      </GroupedList>
    </section>
  )
}

/** Approval fatigue, override escalation and correlated agents, each a sentence, only when present. */
function Patterns({
  insights,
  keep,
  toPart,
}: {
  pid: string
  insights?: Awaited<ReturnType<typeof v1.insights>>
  keep: (s: { session_id: string; agents: string[] }) => boolean
  toPart: (p: SecPart) => SentencePart
}) {
  if (!insights) return null
  const fatigue = fatigueSentence(insights.approval_fatigue.by_session.filter(keep), insights.approval_fatigue.threshold)
  const overrides = overrideSentence(insights.override_escalation.by_session.filter(keep))
  const cross = crossAgentSentences(insights.cross_agent ?? [])
  if (!fatigue && !overrides && !cross.length) return null
  return (
    <div className="obs-quiet-sections">
      {fatigue && (
        <section className="obs-quiet" aria-labelledby="sec-fatigue">
          <h2 className="view-h" id="sec-fatigue">
            Approval fatigue
          </h2>
          <Sentence size="body" parts={fatigue.map(toPart)} />
        </section>
      )}
      {overrides && (
        <section className="obs-quiet" aria-labelledby="sec-overrides">
          <h2 className="view-h" id="sec-overrides">
            Overrides
          </h2>
          <Sentence size="body" parts={overrides.map(toPart)} />
        </section>
      )}
      {cross.length > 0 && (
        <section className="obs-quiet" aria-labelledby="sec-cross">
          <h2 className="view-h" id="sec-cross">
            Across agents
          </h2>
          {cross.map((c) => (
            <p key={c} className="sentence s-body">
              {c}
            </p>
          ))}
        </section>
      )}
    </div>
  )
}

/** The verdicts over time, small and quiet, by action or by severity. */
function OverTime({ dets, from, to }: { dets: Detection[]; from?: string; to?: string }) {
  const [by, setBy] = useState<'action' | 'severity'>('action')
  const buckets = useMemo(() => {
    const now = Date.now()
    const f = from ? new Date(from).getTime() : Math.min(now - 86400e3, ...dets.map((d) => new Date(d.created_at).getTime()))
    const t = to ? new Date(to).getTime() : now
    return bucketize(
      dets,
      (d) => new Date(d.created_at).getTime(),
      (d) => (by === 'action' ? d.action : (d.severity || 'none').toLowerCase()),
      f,
      t,
    )
  }, [dets, by, from, to])
  return (
    <section className="obs-quiet obs-chart" aria-labelledby="sec-time">
      <div className="obs-section-head">
        <h2 className="view-h" id="sec-time">
          Over time
        </h2>
        <Segmented
          label="Colour by"
          size="sm"
          options={[
            { id: 'action', label: 'By action' },
            { id: 'severity', label: 'By severity' },
          ]}
          value={by}
          onChange={setBy}
        />
      </div>
      <div className="view-panel">
        <StackedBars buckets={buckets} series={by === 'action' ? ACTION_SERIES : SEVERITY_SERIES} caption={`Detections over time by ${by}`} height={72} />
      </div>
    </section>
  )
}

/** Per-agent figures, behind a disclosure: verdicts and the share of each agent's tool calls flagged. */
function ByAgent({ pid, stats }: { pid: string; stats: DetectionStats }) {
  return (
    <details className="obs-details">
      <summary>By agent</summary>
      <p className="quiet-note obs-wrap" style={{ marginBottom: 10 }}>
        Flagged tool calls: the agent's recorded tool calls with at least one verdict other than allow (a call with several verdicts counts once), out of all its recorded tool calls.
        Verdicts also counts those not linked to a recorded tool call.
      </p>
      <PlainTable caption="Detections by agent">
        <thead>
          <tr>
            <th scope="col">Agent</th>
            <th scope="col" className="num">
              Verdicts
            </th>
            <th scope="col" className="num">
              Flagged tool calls
            </th>
          </tr>
        </thead>
        <tbody>
          {stats.by_agent.map((a) => (
            <tr key={a.agent}>
              <td>
                <Link to={agentUrl(pid, a.agent)}>{a.agent}</Link>
              </td>
              <td className="num">{a.count}</td>
              <td className="num">
                {flaggedOf(a.flagged_tool_calls, a.tool_calls)}
                {a.detection_rate != null && <span className="obs-faint"> ({Math.round(a.detection_rate * 100)}%)</span>}
              </td>
            </tr>
          ))}
        </tbody>
      </PlainTable>
    </details>
  )
}

/** "Rules changed? Replay": the replay button, the jobs as a list, and the selected job's differences. */
function ReplaySection({ pid, enabled, state, time, scope }: { pid: string; enabled: boolean; state: FilterState; time: TimeRange; scope: SessionScope }) {
  const qc = useQueryClient()
  const [params] = useSearchParams()
  const jobs = useQuery({
    queryKey: ['v1', pid, 'replay-jobs'],
    queryFn: () => v1.replayJobs(pid, { limit: 20 }),
    refetchInterval: (q) => (q.state.data?.items.some((j) => j.status === 'running' || j.status === 'queued') ? 1000 : false),
  })
  const [started, setStarted] = useState<string | null>(null)
  const selected = params.get('replay') ?? started ?? jobs.data?.items[0]?.job_id ?? null
  const job = useQuery({
    queryKey: ['v1', pid, 'replay', selected],
    queryFn: () => v1.replayJob(pid, selected!),
    enabled: !!selected,
    refetchInterval: (q) => (q.state.data && (q.state.data.status === 'done' || q.state.data.status === 'failed') ? false : 800),
  })
  const start = useMutation({
    mutationFn: () => {
      const w = timeWindow(state, time)
      const filter: Record<string, unknown> = { ...w }
      if (state.agent) filter.agent = state.agent
      const label = `ui-${new Date().toISOString().slice(0, 16).replace(/[-:T]/g, '')}`
      // With session filters on, replay exactly the matching sessions.
      if (scope.active) return v1.replayStart({ project_id: pid, session_ids: [...scope.ids], filter: state.agent ? { agent: state.agent } : undefined, label })
      return v1.replayStart({ project_id: pid, filter, label })
    },
    onSuccess: async (r) => {
      setStarted(r.job_id)
      await qc.invalidateQueries({ queryKey: ['v1', pid, 'replay-jobs'] })
    },
    onError: (e) => notify(`Replay failed to start: ${e instanceof Error ? e.message : String(e)}`, 'error', 8000),
  })
  const jobUrl = (id: string) => {
    const n = new URLSearchParams(params)
    n.set('replay', id)
    return `?${n.toString()}`
  }
  const what = scope.active
    ? `${scope.ids.size} matching session${scope.ids.size === 1 ? '' : 's'}`
    : time === 'all'
      ? 'all recorded tool calls'
      : time === 'custom'
        ? 'this time range'
        : TIME_LABEL[time].toLowerCase().replace(/^last/, 'the last')
  return (
    <section className="obs-replay" aria-labelledby="sec-replay">
      <div className="obs-section-head">
        <h2 className="view-h" id="sec-replay">
          Rules changed? Replay
        </h2>
        <button type="button" className="qbtn primary" disabled={!enabled || start.isPending || (scope.active && !scope.ids.size)} onClick={() => start.mutate()}>
          <Icon name="replay" size={13} /> Replay {what}
          {state.agent ? ` for ${state.agent}` : ''}
        </button>
      </div>
      <p className="quiet-note obs-wrap">
        Replay runs the recorded tool calls through the current rules in shadow mode and lists what would change. Your agents are not affected.
      </p>
      {!enabled && (
        <p className="quiet-note obs-wrap">
          Replay is off on this server: start Weft with <span className="mono">--shield-rules-dir rules/rules</span> to replay in-process with AgentShield's rules, or{' '}
          <span className="mono">--shield-url</span> to use a running engine.{' '}
          <Link to={helpUrl('security.md#replay-recorded-tool-calls-after-a-rule-change')}>How replay works</Link>
        </p>
      )}
      {jobs.data && jobs.data.items.length > 0 && (
        <GroupedList label="Replay jobs" className="obs-jobs">
          {jobs.data.items.map((listed) => {
            // The list may leave out the figures; the selected job's own record has them.
            const j = job.data?.job_id === listed.job_id ? { ...listed, ...job.data } : listed
            return (
            <ListRow
              key={j.job_id}
              className={selected === j.job_id ? 'obs-selected' : ''}
              glyph={<StatusGlyph kind={replayGlyph(j)} label={j.status === 'done' ? 'Finished' : j.status === 'failed' ? 'Failed' : 'Running'} />}
              title={<span className="mono">{j.label}</span>}
              summary={replayLine(j)}
              meta={[replayMeta(j), selected === j.job_id ? 'shown below' : ''].filter(Boolean).join(' · ') || undefined}
              time={relativeTime(j.created_at)}
              timeTitle={fmtDate(j.created_at)}
              to={jobUrl(j.job_id)}
            />
            )
          })}
        </GroupedList>
      )}
      {selected && (job.data || job.isLoading) && (
        <div className="view-panel obs-replay-result" aria-label="Replay result">
          {job.data ? <ReplayResult pid={pid} job={job.data} /> : <Loading />}
        </div>
      )}
      {enabled && jobs.data?.items.length === 0 && <p className="quiet-note obs-wrap">No replays yet. A replay shows what your current rules would block in the sessions you already recorded.</p>}
    </section>
  )
}

function SetupGuide({ pid, replay }: { pid: string; replay: boolean }) {
  return (
    <section className="guided-empty" aria-label="Connect AgentShield">
      <div className="guided-head">
        <div className="guided-icon" aria-hidden>
          <Icon name="shield" size={20} />
        </div>
        <div className="grow">
          <h2>Connect AgentShield to this project</h2>
          <p className="muted">
            Once it does, every blocked, logged or approval-gated tool call appears here and on the exact step of the session's story. Point the engine at Weft in one of two ways:
          </p>
        </div>
      </div>
      <ol className="steps">
        <li>
          <strong>Push verdicts</strong> (recommended): add a <span className="mono">weft:</span> section to AgentShield's <span className="mono">config.yaml</span> and restart the
          engine.
          <Snippets projectId={pid} only={['agentshield']} />
        </li>
        <li>
          <strong>Or export over OpenTelemetry</strong>: set the engine's <span className="mono">telemetry.endpoint</span> to this server's{' '}
          <span className="mono">/otel/v1/traces</span> with <span className="mono">project_id: {pid}</span> in the headers.
        </li>
        <li>
          <strong>Use the same ids</strong>: the AgentShield and Weft plugins both send the harness session id and tool-use id, so verdicts land on the right tool call.{' '}
          <Tip text="AgentShield session_id = Weft gen_ai.conversation.id; AgentShield tool_call_id = Weft gen_ai.tool.call.id." />
        </li>
      </ol>
      <p className="small muted">
        Replay of recorded tool calls is {replay ? <strong>available on this server</strong> : 'off on this server (start Weft with --shield-rules-dir to turn it on)'}.{' '}
        <Link to={helpUrl('security.md#connect-agentshield')}>Full guide</Link>
      </p>
    </section>
  )
}
