// Security (DESIGN.md §7): AgentShield's verdicts on the agents' tool calls:
// detections over time by action and severity, top rules, per-agent rates,
// approval fatigue and override escalation, replay jobs with their diffs,
// and a guided setup when the engine is not connected yet.
import { useMemo, useState } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { v1, type Detection, type DetectionAction, type DetectionStats } from '../../api/v1'
import { useProjectId } from '../../hooks/useProject'
import { useSessionScope, type SessionScope } from '../../hooks/useSessionScope'
import { timeWindow, TIME_LABEL, type FilterState, type TimeRange } from '../../lib/filters'
import { fmtDate, fmtNumber, fmtRelative } from '../../lib/format'
import { agentUrl, sessionUrl } from '../../lib/urls'
import { ErrorBox, Loading } from '../../components/common'
import { FilterBar, useFilters, useWidenedTime, WidenedNote } from '../../components/ui/FilterBar'
import { Snippets } from '../../components/ui/EmptyState'
import { ActionBadge, AgentChip, Bar, Callout, CopyId, Metric, Panel, SeverityText, Tip } from '../../components/ui/bits'
import { Icon } from '../../components/ui/Icons'
import { docsUrl, usePageHelp } from '../../components/ui/PageHelp'
import { notify } from '../../components/ui/Toasts'
import { bucketize, StackedBars } from '../../components/ui/StackedBars'
import { ReplayResult } from '../sessions/SessionPage'

const DEFAULT_TIME: TimeRange = '24h'

/** /api/v1/detections parameters; `sessionIds` applies the chips that go through sessions (team, status, score, …). */
export function detectionParams(s: FilterState, time: TimeRange, sessionIds?: string[]): Record<string, string | string[]> {
  const p: Record<string, string | string[]> = { source: 'engine' }
  if (s.agent) p.agent = s.agent
  if (s.tool) p.tool_name = s.tool
  if (s.has === 'block') p.action = 'block'
  else if (s.has === 'approval') p.action = 'require_approval'
  else if (s.has === 'flagged') p.action = 'block,require_approval'
  else if (s.has === 'any') p.action = 'block,require_approval,log'
  if (sessionIds) p.session_ids = sessionIds
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
    if (d.agent_name) agents.set(d.agent_name, (agents.get(d.agent_name) ?? 0) + 1)
  }
  out.by_rule = [...rules.values()].sort((a, b) => b.count - a.count)
  out.by_agent = [...agents.entries()]
    .map(([agent, count]) => {
      const calls = server?.by_agent.find((a) => a.agent === agent)?.tool_calls ?? 0
      return { agent, count, by_action: {}, tool_calls: calls, detection_rate: calls ? Math.min(1, count / calls) : null }
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
  const help = usePageHelp(
    'security',
    'Security',
    <>
      AgentShield checks every tool call your agents make against its rules and answers <strong>allow</strong>, <strong>log</strong>, <strong>needs approval</strong> or{' '}
      <strong>block</strong>. Weft stores those verdicts as <strong>detections</strong> on the exact tool call. This page shows trends, the rules that fire most, agents that trip
      them, signs of approval fatigue, and lets you replay recorded tool calls when the rules change.
    </>,
    'security.md',
  )
  const status = useQuery({ queryKey: ['v1', pid, 'shield-status'], queryFn: () => v1.shieldStatus(pid), staleTime: 60_000 })
  const ever = useQuery({ queryKey: ['v1', pid, 'detections', 'ever'], queryFn: () => v1.detectionStats(pid, { source: 'engine' }) })
  const noTime = detectionParams({ ...state, time: 'all' }, 'all')
  const widen = useWidenedTime(state, DEFAULT_TIME, async (r) => (await v1.detectionStats(pid, detectionParams({ ...state, time: r }, r))).total, [pid, 'detections', noTime])
  const time = widen.time
  // Team, status, score, model and cost belong to sessions: keep detections in matching sessions.
  const scope = useSessionScope(pid, 'security', state, time, widen.ready)
  const noMatch = scope.active && scope.ready && scope.ids.size === 0
  const dp = detectionParams(state, time, scope.active ? [...scope.ids].sort() : undefined)
  const ready = widen.ready && scope.ready && !noMatch
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
  const [by, setBy] = useState<'action' | 'severity'>('action')
  const dets = noMatch ? [] : (list.data?.items ?? []).filter((d) => detectionMatchesText(d, state.q))
  // The text filter runs here, so the figures are counted from the matching detections.
  const stats = {
    data: noMatch ? statsFromDetections([]) : state.q && list.data ? statsFromDetections(dets, serverStats.data) : serverStats.data,
    error: serverStats.error,
  }
  const buckets = useMemo(() => {
    const now = Date.now()
    const from = w.from ? new Date(w.from).getTime() : Math.min(now - 86400e3, ...dets.map((d) => new Date(d.created_at).getTime()))
    const to = w.to ? new Date(w.to).getTime() : now
    return bucketize(
      dets,
      (d) => new Date(d.created_at).getTime(),
      (d) => (by === 'action' ? d.action : (d.severity || 'none').toLowerCase()),
      from,
      to,
    )
  }, [dets, by, w.from, w.to])

  const connected = (ever.data?.total ?? 0) > 0
  const st = stats.data
  const ruleFilter = params.get('rule')

  return (
    <div className="page wide">
      <header className="page-head">
        <div className="grow">
          <h1>Security</h1>
          <p className="page-sub">What AgentShield blocked, flagged or asked approval for, and why.</p>
        </div>
        {help.button}
      </header>
      {help.panel}
      {ever.isLoading ? (
        <Loading />
      ) : !connected ? (
        <SetupGuide pid={pid} replay={status.data?.replay_enabled ?? false} />
      ) : (
        <>
          <FilterBar
            page="security"
            defaultTime={DEFAULT_TIME}
            effectiveTime={time}
            options={{ agents: st?.by_agent.map((a) => a.agent) }}
            placeholder="Filter by rule, tool, agent or session…"
          />
          {widen.widened && <WidenedNote time={time} defaultTime={DEFAULT_TIME} onReset={() => setState({ ...state, time: DEFAULT_TIME })} />}
          <ErrorBox error={stats.error ?? list.error ?? scope.error} />
          <div className="metrics">
            <Metric
              label="Verdicts"
              value={fmtNumber(st?.total)}
              tip={`AgentShield verdicts other than allow in the ${TIME_LABEL[time].toLowerCase()}. Replays are not counted.`}
            />
            <Metric
              label="Blocked"
              value={fmtNumber(st?.by_action.block ?? 0)}
              tone={(st?.by_action.block ?? 0) > 0 ? 'error' : undefined}
              tip="Tool calls AgentShield stopped (enforce mode)."
            />
            <Metric
              label="Needs approval"
              value={fmtNumber(st?.by_action.require_approval ?? 0)}
              tone={(st?.by_action.require_approval ?? 0) > 0 ? 'warn' : undefined}
              tip="Tool calls a person had to approve."
            />
            <Metric label="Logged" value={fmtNumber(st?.by_action.log ?? 0)} tip="Alerts recorded while the call ran (audit mode, or low severity)." />
            <Metric
              label="Overridden"
              value={fmtNumber(st?.overridden ?? 0)}
              tip="Verdicts a user overrode (approved, or ran anyway). Repeated overrides are a sign of escalation."
            />
            <Metric
              label="Linked to a step"
              value={st && st.total ? `${Math.round((st.linked / st.total) * 100)}%` : '—'}
              tip="Verdicts attached to the exact tool call, by tool-call id or by tool name within ±5 s. Unlinked ones stay on the session."
            />
          </div>
          <div className="grid-2">
            <Panel
              title="Over time"
              tip="Verdicts per hour (ranges up to two days) or per day."
              actions={
                <div className="seg" role="group" aria-label="Colour by">
                  <button type="button" className={by === 'action' ? 'on' : ''} aria-pressed={by === 'action'} onClick={() => setBy('action')}>
                    By action
                  </button>
                  <button type="button" className={by === 'severity' ? 'on' : ''} aria-pressed={by === 'severity'} onClick={() => setBy('severity')}>
                    By severity
                  </button>
                </div>
              }
            >
              <StackedBars buckets={buckets} series={by === 'action' ? ACTION_SERIES : SEVERITY_SERIES} caption={`Detections over time by ${by}`} />
            </Panel>
            <Panel title="Rules that fire most" tip="Each rule with how many verdicts it produced. A verdict can match several rules.">
              {!st?.by_rule.length ? (
                <p className="muted small">No rules matched.</p>
              ) : (
                <table className="table compact-table">
                  <caption className="sr-only">Top rules</caption>
                  <thead>
                    <tr>
                      <th scope="col">Rule</th>
                      <th scope="col">Level</th>
                      <th scope="col">Verdicts</th>
                    </tr>
                  </thead>
                  <tbody>
                    {st.by_rule.slice(0, 8).map((r) => (
                      <tr key={r.rule_id} className={ruleFilter === r.rule_id ? 'selected' : ''}>
                        <td>
                          <button
                            type="button"
                            className="linkish left"
                            onClick={() => {
                              const n = new URLSearchParams(params)
                              if (ruleFilter === r.rule_id) n.delete('rule')
                              else n.set('rule', r.rule_id)
                              setParams(n, { replace: true })
                            }}
                            aria-pressed={ruleFilter === r.rule_id}
                            title="Show only this rule's detections below"
                          >
                            {r.title || r.rule_id}
                          </button>
                        </td>
                        <td>
                          <SeverityText level={r.level} />
                        </td>
                        <td>
                          <span className="row" style={{ gap: 6 }}>
                            <Bar value={r.count} max={st.by_rule[0].count} tone={r.by_action.block ? 'error' : 'warn'} label={`${r.count} verdicts`} /> {r.count}
                          </span>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              )}
            </Panel>
          </div>
          <div className="grid-2">
            <Panel title="By agent" tip="Detection rate: the share of the agent's recorded tool calls that drew a non-allow verdict.">
              {!st?.by_agent.length ? (
                <p className="muted small">No agents flagged.</p>
              ) : (
                <table className="table compact-table">
                  <caption className="sr-only">Detections by agent</caption>
                  <thead>
                    <tr>
                      <th scope="col">Agent</th>
                      <th scope="col">Verdicts</th>
                      <th scope="col">Rate</th>
                    </tr>
                  </thead>
                  <tbody>
                    {st.by_agent.map((a) => (
                      <tr key={a.agent}>
                        <td>
                          <AgentChip name={a.agent} to={agentUrl(pid, a.agent)} size="sm" />
                        </td>
                        <td>
                          {a.count} <span className="faint small">of {a.tool_calls} tool calls</span>
                        </td>
                        <td>
                          <span className="row" style={{ gap: 6 }}>
                            <Bar value={a.detection_rate ?? 0} max={1} tone="warn" label={`${Math.round((a.detection_rate ?? 0) * 100)}%`} />
                            {a.detection_rate == null ? '—' : `${Math.round(a.detection_rate * 100)}%`}
                          </span>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              )}
            </Panel>
            <FatiguePanels pid={pid} insights={insights.data} keep={(s) => (!scope.active || scope.ids.has(s.session_id)) && (!state.agent || s.agents.includes(state.agent))} />
          </div>
          <RecentDetections
            pid={pid}
            dets={ruleFilter ? dets.filter((d) => d.rules.some((r) => r.id === ruleFilter)) : dets}
            ruleFilter={ruleFilter}
            onClearRule={() => {
              const n = new URLSearchParams(params)
              n.delete('rule')
              setParams(n, { replace: true })
            }}
          />
          <ReplayPanel pid={pid} enabled={status.data?.replay_enabled ?? false} state={state} time={time} scope={scope} />
        </>
      )}
    </div>
  )
}

/** A session labelled by its name or first request, its id in the tooltip and copyable. */
function SessionLink({ pid, id, title, step }: { pid: string; id: string; title?: string; step?: string }) {
  const t = title?.trim()
  return (
    <span className="session-link">
      <Link to={sessionUrl(pid, id, step ? { step } : undefined)} className={t ? 'small' : 'mono small'} title={`Session ${id}`}>
        {t ? (t.length > 60 ? t.slice(0, 59) + '…' : t) : id}
      </Link>
      {t && <CopyId id={id} label="Session ID" short={8} />}
    </span>
  )
}

function FatiguePanels({
  pid,
  insights,
  keep,
}: {
  pid: string
  insights?: Awaited<ReturnType<typeof v1.insights>>
  /** The filters, as far as they apply to a session. */
  keep: (s: { session_id: string; agents: string[] }) => boolean
}) {
  const fatigue = insights?.approval_fatigue
  const esc = insights?.override_escalation
  const tired = (fatigue?.by_session ?? []).filter((s) => (s.fatigued || s.require_approval >= 3) && keep(s))
  const escalating = (esc?.by_session ?? []).filter((s) => (s.escalating || s.overrides > 0) && keep(s))
  return (
    <div className="col" style={{ gap: 16 }}>
      <Panel
        title="Approval fatigue"
        tip={`Sessions that asked a person for approval ${fatigue?.threshold ?? 5} or more times are flagged: people start approving without reading. “Approved” counts approvals a user overrode.`}
      >
        {!tired.length ? (
          <p className="muted small">No session asked for approval often.</p>
        ) : (
          <ul className="plain insight-list">
            {tired.slice(0, 6).map((s) => (
              <li key={s.session_id}>
                <SessionLink pid={pid} id={s.session_id} title={s.session_title} step="problem" />{' '}
                {s.agents.map((a) => (
                  <AgentChip key={a} name={a} size="sm" />
                ))}{' '}
                <span className="small">
                  <strong>{s.require_approval}</strong> approval requests, {s.approved} approved
                </span>
                {s.fatigued && (
                  <span className="pill warn">
                    <Icon name="approval" size={12} /> fatigue
                  </span>
                )}
              </li>
            ))}
          </ul>
        )}
      </Panel>
      <Panel title="Override escalation" tip={`Sessions where users overrode verdicts ${esc?.threshold ?? 3} or more times, or overrode two blocks, are escalating.`}>
        {!escalating.length ? (
          <p className="muted small">No overrides.</p>
        ) : (
          <ul className="plain insight-list">
            {escalating.slice(0, 6).map((s) => (
              <li key={s.session_id}>
                <SessionLink pid={pid} id={s.session_id} title={s.session_title} step="problem" />{' '}
                <span className="small">
                  <strong>{s.overrides}</strong> overrides ({s.overridden_blocks} blocks, {s.overridden_approvals} approvals)
                </span>
                {s.escalating && (
                  <span className="pill error">
                    <Icon name="error" size={12} /> escalating
                  </span>
                )}
              </li>
            ))}
          </ul>
        )}
      </Panel>
      {(insights?.cross_agent ?? []).some((c) => c.correlated) && (
        <Panel title="Across agents" tip="Teams where the same rules fired for two or more agents close together in time.">
          <ul className="plain insight-list">
            {insights!.cross_agent
              .filter((c) => c.correlated)
              .map((c) => (
                <li key={c.team_id}>
                  {c.agents_with_detections.map((a) => (
                    <AgentChip key={a} name={a} size="sm" />
                  ))}{' '}
                  <span className="small">
                    {c.incidents.length} incident{c.incidents.length === 1 ? '' : 's'}, {c.shared_rules.map((r) => r.title).join(', ')}
                  </span>
                </li>
              ))}
          </ul>
        </Panel>
      )}
    </div>
  )
}

function RecentDetections({ pid, dets, ruleFilter, onClearRule }: { pid: string; dets: Detection[]; ruleFilter: string | null; onClearRule: () => void }) {
  const [n, setN] = useState(25)
  return (
    <Panel
      title={`Detections (${dets.length})`}
      tip="Newest first. “Show step” opens the session timeline at the tool call, with the reasoning that led to it."
      actions={
        ruleFilter ? (
          <button className="btn sm" onClick={onClearRule}>
            Show all rules
          </button>
        ) : undefined
      }
    >
      {!dets.length ? (
        <p className="muted small">Nothing matches these filters.</p>
      ) : (
        <ul className="det-list">
          {dets.slice(0, n).map((d) => (
            <li key={d.id} className="det-row">
              <ActionBadge action={d.action as DetectionAction} overridden={d.overridden} />
              <span className="det-rule">
                <strong>{d.rules[0]?.title ?? 'No rule recorded'}</strong>
                {d.rules.length > 1 && <span className="faint small"> +{d.rules.length - 1} more</span>} <SeverityText level={d.severity} />
              </span>
              <span className="det-ctx small">
                <AgentChip name={d.agent_name} size="sm" /> <span className="mono">{d.tool_name}</span> in{' '}
                <SessionLink pid={pid} id={d.session_id} title={d.session_title} />
                <span className="faint" title={fmtDate(d.created_at)}>
                  {' '}
                  · {fmtRelative(d.created_at)}
                </span>
              </span>
              <Link className="btn sm" to={sessionUrl(pid, d.session_id, { step: d.span_id ? `tool:${d.span_id}` : 'problem' })}>
                Show step <Icon name="chevron" size={12} />
              </Link>
            </li>
          ))}
        </ul>
      )}
      {dets.length > n && (
        <button className="btn sm" onClick={() => setN((x) => x + 50)}>
          Show more
        </button>
      )}
    </Panel>
  )
}

function ReplayPanel({ pid, enabled, state, time, scope }: { pid: string; enabled: boolean; state: FilterState; time: TimeRange; scope: SessionScope }) {
  const qc = useQueryClient()
  const jobs = useQuery({
    queryKey: ['v1', pid, 'replay-jobs'],
    queryFn: () => v1.replayJobs(pid, { limit: 20 }),
    refetchInterval: (q) => (q.state.data?.items.some((j) => j.status === 'running' || j.status === 'queued') ? 1000 : false),
  })
  const [sel, setSel] = useState<string | null>(null)
  const selected = sel ?? jobs.data?.items[0]?.job_id ?? null
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
      setSel(r.job_id)
      await qc.invalidateQueries({ queryKey: ['v1', pid, 'replay-jobs'] })
    },
    onError: (e) => notify(`Replay failed to start: ${e instanceof Error ? e.message : String(e)}`, 'error', 8000),
  })
  return (
    <Panel
      title="Rules changed? Replay recorded tool calls"
      tip="Re-runs the recorded tool calls through the current rules in shadow mode and compares with the verdicts recorded at the time. Your agents are not affected."
      actions={
        <button className="btn sm primary" disabled={!enabled || start.isPending || (scope.active && !scope.ids.size)} onClick={() => start.mutate()}>
          <Icon name="replay" size={13} />{' '}
          {scope.active ? `Replay ${scope.ids.size} matching session${scope.ids.size === 1 ? '' : 's'}` : `Replay the ${TIME_LABEL[time].toLowerCase()}`}
          {state.agent ? ` for ${state.agent}` : ''}
        </button>
      }
    >
      {!enabled && (
        <Callout tone="info" title="Replay is not configured on this server">
          Start Weft with <span className="mono">--shield-rules-dir rules/rules</span> to replay in-process with AgentShield's rules, or <span className="mono">--shield-url</span>{' '}
          to use a running engine.{' '}
          <Link to={docsUrl('security.md#replay-recorded-tool-calls-after-a-rule-change')}>How replay works</Link>
        </Callout>
      )}
      {jobs.data && jobs.data.items.length > 0 && (
        <div className="replay-grid">
          <ul className="plain job-list" aria-label="Replay jobs">
            {jobs.data.items.map((j) => (
              <li key={j.job_id}>
                <button type="button" className={`job ${selected === j.job_id ? 'on' : ''}`} aria-pressed={selected === j.job_id} onClick={() => setSel(j.job_id)}>
                  <span className="mono small">{j.label}</span>
                  <span className="faint small">
                    {j.status} · {fmtRelative(j.created_at)}
                  </span>
                  {j.summary && (
                    <span className="small">
                      {j.summary.newly_blocked} newly blocked · {j.summary.no_longer_blocked} unblocked
                    </span>
                  )}
                </button>
              </li>
            ))}
          </ul>
          <div className="grow">{job.data ? <ReplayResult pid={pid} job={job.data} /> : job.isLoading ? <Loading /> : null}</div>
        </div>
      )}
      {enabled && jobs.data?.items.length === 0 && (
        <p className="muted small">No replays yet. Replaying shows what your current rules would block in the sessions you already recorded.</p>
      )}
    </Panel>
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
          <h2>AgentShield is not sending verdicts to this project yet</h2>
          <p className="muted">
            Once it does, every blocked, logged or approval-gated tool call appears here and on the exact step of the session timeline. Point the engine at Weft in one of two ways:
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
        <Link to={docsUrl('security.md#connect-agentshield')}>Full guide</Link>
      </p>
    </section>
  )
}
