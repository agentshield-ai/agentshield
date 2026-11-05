// The analytics overview (one agent or every agent) in the look of the
// redesigned pages (DESIGN.md §7.2): quiet single-hue charts in one panel
// (no tiles, no legend of colours), then plain tables of agents, tools,
// models and versions, and a message search listed as rows. The window is
// the page's time range; the sentence above the page states the totals, so
// the charts only carry their own figure in words.
import { useState, type ReactElement } from 'react'
import { Link } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { Bar, BarChart, CartesianGrid, Line, LineChart, ResponsiveContainer, Tooltip, XAxis, YAxis } from 'recharts'
import { agentsApi, agentUrl, conversationUrl, eqQuery, type SpanGroup } from '../../api/agents'
import type { Query } from '../../api/types'
import { fmtCost, fmtDate, fmtDuration, fmtNumber, fmtRelative, truncate } from '../../lib/format'
import { sessionUrl } from '../../lib/urls'
import { joinClauses } from '../../lib/summaries'
import type { AgentsTotals } from '../../lib/summaries/agentsTeams'
import { ErrorBox, Spinner } from '../common'
import { GroupedList, ListRow } from '../ui/GroupedList'
import { PlainTable } from '../ui/PlainTable'
import { SearchField } from '../ui/SearchField'
import { compact, granularityFor, windowStart } from './agentUtils'

const AXIS = { fontSize: 10, fill: 'var(--text-muted)' }
const TOOLTIP_STYLE = { background: 'var(--panel)', border: '1px solid var(--border)', borderRadius: 8, fontSize: 12, color: 'var(--text)' }
const num = (v: unknown) => (typeof v === 'number' ? v : 0)
const INVOCATION: Query = { $expr: { $eq: [{ $getField: 'is_invocation' }, { $literal: true }] } } as Query
const HAS_SESSION = { $expr: { $convert: { input: { $getField: 'conversation_id' }, to: 'exists' } } } as unknown as Query
const HAS_AGENT = { $expr: { $convert: { input: { $getField: 'agent_name' }, to: 'exists' } } } as unknown as Query

function and(a: Query | undefined, b: Query | undefined): Query | undefined {
  if (!a) return b
  if (!b) return a
  return { $expr: { $and: [a.$expr, b.$expr] } } as Query
}

/** Totals over the window: agents, sessions, runs, failed runs, tokens and cost (of one agent with `agent`). */
export function useAnalyticsTotals(projectId: string, range: string, agent?: string) {
  const since = windowStart(range)
  const only = agent ? eqQuery({ agent_name: agent }) : undefined
  return useQuery({
    queryKey: ['agents-analytics-totals', projectId, range, agent ?? ''],
    queryFn: async (): Promise<AgentsTotals> => {
      const year = 365 * 86_400
      const [all, inv, sessions, agents] = await Promise.all([
        agentsApi.stats({
          project_id: projectId,
          start: since,
          granularity: year,
          query: only,
          metrics: [
            { alias: 'in', value: { key: 'input_tokens' }, value_type: 'number', aggregations: ['sum'] },
            { alias: 'out', value: { key: 'output_tokens' }, value_type: 'number', aggregations: ['sum'] },
            { alias: 'cost', value: { key: 'total_cost_usd' }, value_type: 'number', aggregations: ['sum'] },
          ],
        }),
        agentsApi.stats({
          project_id: projectId,
          start: since,
          granularity: year,
          query: and(only, INVOCATION),
          metrics: [{ alias: 'err', value: { key: 'is_error' }, value_type: 'boolean', aggregations: ['count_true'] }],
        }),
        agentsApi.spans({ project_id: projectId, started_after: since, query: and(only, HAS_SESSION), group_by: [{ key: 'conversation_id' }], limit: 1 }),
        agentsApi.spans({ project_id: projectId, started_after: since, query: and(only, HAS_AGENT), group_by: [{ key: 'agent_name' }], limit: 1 }),
      ])
      const sum = (rows: Record<string, unknown>[], k: string) => rows.reduce((s, r) => s + num(r[k]), 0)
      return {
        agents: agents.total_count,
        sessions: sessions.total_count,
        invocations: sum(inv.rows, 'count'),
        failed: sum(inv.rows, 'err_count_true'),
        tokens: sum(all.rows, 'in_sum') + sum(all.rows, 'out_sum'),
        cost: sum(all.rows, 'cost_sum'),
      }
    },
  })
}

function ChartRow({ title, figure, label, children }: { title: string; figure: string; label: string; children: ReactElement }) {
  return (
    <div className="an-chart">
      <div className="an-chart-head">
        <h3 className="an-chart-title">{title}</h3>
        <span className="an-chart-figure">{figure}</span>
      </div>
      <div className="an-chart-plot" role="img" aria-label={label}>
        <ResponsiveContainer width="100%" height="100%">
          {children}
        </ResponsiveContainer>
      </div>
    </div>
  )
}

/** Runs, time per run, tokens and cost over the window: one quiet panel, one hue. */
export function QuietCharts({ projectId, range, agent }: { projectId: string; range: string; agent?: string }) {
  const start = windowStart(range)
  const granularity = granularityFor(range)
  const only = agent ? eqQuery({ agent_name: agent }) : undefined
  const inv = useQuery({
    queryKey: ['agents-analytics-chart', projectId, range, agent ?? '', 'inv'],
    queryFn: () =>
      agentsApi.stats({
        project_id: projectId,
        start,
        granularity,
        query: and(only, INVOCATION),
        metrics: [
          { alias: 'latency', value: { key: 'duration_ms' }, value_type: 'number', percentiles: [50] },
          { alias: 'err', value: { key: 'is_error' }, value_type: 'boolean', aggregations: ['count_true'] },
        ],
      }),
  })
  const all = useQuery({
    queryKey: ['agents-analytics-chart', projectId, range, agent ?? '', 'all'],
    queryFn: () =>
      agentsApi.stats({
        project_id: projectId,
        start,
        granularity,
        query: only,
        metrics: [
          { alias: 'in', value: { key: 'input_tokens' }, value_type: 'number', aggregations: ['sum'] },
          { alias: 'out', value: { key: 'output_tokens' }, value_type: 'number', aggregations: ['sum'] },
          { alias: 'cost', value: { key: 'total_cost_usd' }, value_type: 'number', aggregations: ['sum'] },
        ],
      }),
  })
  if (inv.isLoading || all.isLoading)
    return (
      <div className="an-panel an-loading">
        <Spinner label="Loading charts…" />
      </div>
    )
  if (inv.error || all.error) return <ErrorBox error={inv.error ?? all.error} />
  const byTs = new Map<string, { t: string; runs: number; failed: number; p50: number | null; tokens: number; cost: number }>()
  const row = (t: string) => byTs.get(t) ?? { t, runs: 0, failed: 0, p50: null, tokens: 0, cost: 0 }
  for (const r of inv.data!.rows) {
    const x = row(String(r.timestamp))
    byTs.set(x.t, { ...x, runs: num(r.count), failed: num(r.err_count_true), p50: (r.latency_p50 as number | null) ?? null })
  }
  for (const r of all.data!.rows) {
    const x = row(String(r.timestamp))
    byTs.set(x.t, { ...x, tokens: num(r.in_sum) + num(r.out_sum), cost: num(r.cost_sum) })
  }
  const data = [...byTs.values()].sort((a, b) => a.t.localeCompare(b.t))
  const total = (k: 'runs' | 'failed' | 'tokens' | 'cost') => data.reduce((s, r) => s + r[k], 0)
  const p50s = data.map((r) => r.p50).filter((v): v is number => v != null)
  const median = p50s.length ? [...p50s].sort((a, b) => a - b)[p50s.length >> 1] : null
  const short = granularity < 86_400
  const tick = (iso: string) => {
    const d = new Date(iso)
    return short ? d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit', hourCycle: 'h23' }) : d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' })
  }
  const labelFmt = (iso: unknown) => fmtDate(String(iso))
  const xAxis = <XAxis dataKey="t" tickFormatter={tick} tick={AXIS} minTickGap={32} axisLine={false} tickLine={false} />
  const grid = <CartesianGrid vertical={false} stroke="var(--hairline)" />
  const margin = { top: 4, right: 4, bottom: 0, left: 0 }
  const bar = (key: string, name: string, fmt: (v: number) => string, whole = true) => (
    <BarChart data={data} margin={margin}>
      {grid}
      {xAxis}
      <YAxis tick={AXIS} tickFormatter={(v: number) => (v === 0 ? '0' : fmt(v))} axisLine={false} tickLine={false} allowDecimals={!whole} width={56} />
      <Tooltip contentStyle={TOOLTIP_STYLE} labelFormatter={labelFmt} formatter={(v: number) => fmt(v)} cursor={{ fill: 'var(--fill)' }} />
      <Bar dataKey={key} name={name} fill="var(--accent)" isAnimationActive={false} radius={[2, 2, 0, 0]} />
    </BarChart>
  )
  const runs = total('runs')
  const failed = total('failed')
  return (
    <div className="an-panel">
      <ChartRow
        title="Runs"
        figure={runs ? `${fmtNumber(runs)}${failed ? `, ${fmtNumber(failed)} failed` : ', none failed'}` : 'none'}
        label={`Runs over time: ${fmtNumber(runs)} in all, ${fmtNumber(failed)} failed`}
      >
        {bar('runs', 'Runs', (v) => compact(v))}
      </ChartRow>
      <ChartRow title="Time per run" figure={median != null ? `median ${fmtDuration(median)}` : 'none'} label="Median time per run over time">
        <LineChart data={data} margin={margin}>
          {grid}
          {xAxis}
          <YAxis tick={AXIS} tickFormatter={(v: number) => (v === 0 ? '0' : fmtDuration(v))} axisLine={false} tickLine={false} width={48} />
          <Tooltip contentStyle={TOOLTIP_STYLE} labelFormatter={labelFmt} formatter={(v: number) => fmtDuration(v)} />
          <Line dataKey="p50" name="Median" stroke="var(--accent)" dot={false} strokeWidth={1.5} isAnimationActive={false} connectNulls />
        </LineChart>
      </ChartRow>
      <ChartRow title="Tokens" figure={fmtNumber(total('tokens'))} label={`Tokens over time: ${fmtNumber(total('tokens'))} in all`}>
        {bar('tokens', 'Tokens', (v) => compact(v))}
      </ChartRow>
      <ChartRow title="Cost" figure={fmtCost(total('cost'))} label={`Cost over time: ${fmtCost(total('cost'))} in all`}>
        {bar('cost', 'Cost', (v) => fmtCost(v), false)}
      </ChartRow>
    </div>
  )
}

/** Spans grouped by agent, tool or model over the window, as a plain table (runs or calls, failed, cost). */
export function GroupTable({
  projectId,
  range,
  agent,
  by,
  subject,
  noun,
  onPick,
}: {
  projectId: string
  range: string
  agent?: string
  by: 'agent_name' | 'tool_name' | 'request_model'
  /** What the rows are, for the table's caption ("Tools called"); the columns shown are added to it. */
  subject: string
  /** The first column's heading. */
  noun: string
  /** Opens the spans of one group (the Spans view filtered to it). */
  onPick?: (key: string) => void
}) {
  const since = windowStart(range)
  const base = by === 'tool_name' ? { operation_name: 'execute_tool' } : by === 'request_model' ? { operation_name: 'chat' } : {}
  const agents = by === 'agent_name'
  const q = useQuery({
    queryKey: ['agents-analytics-groups', projectId, range, agent ?? '', by],
    queryFn: async () => {
      const req = (query: Query | undefined) =>
        agentsApi.spans({ project_id: projectId, started_after: since, include_costs: true, query, group_by: [{ key: by }], sort_by: [{ field: 'span_count', direction: 'desc' }], limit: 50 })
      if (!agents) return { groups: (await req(eqQuery({ ...base, agent_name: agent }))).groups, runs: null }
      // An agent's runs, failed runs and run time come from its invocation spans (as the Agents page counts them); its cost from all its spans.
      const [all, runs] = await Promise.all([req(HAS_AGENT), req(and(HAS_AGENT, INVOCATION))])
      return { groups: all.groups, runs: new Map(runs.groups.map((g) => [String(Object.values(g.group_keys)[0] ?? ''), g])) }
    },
  })
  if (q.isLoading) return <Spinner />
  if (q.error) return <ErrorBox error={q.error} />
  const key = (g: SpanGroup) => String(Object.values(g.group_keys)[0] ?? '')
  const groups = (q.data?.groups ?? []).filter((g) => key(g) !== '')
  if (!groups.length) return <p className="quiet-note">None in this time range.</p>
  const runs = q.data?.runs
  const costs = groups.some((g) => g.total_cost_usd)
  return (
    <PlainTable caption={`${subject}, with ${joinClauses([agents ? 'runs' : 'calls', 'failures', 'median time', ...(costs ? ['cost'] : [])]).replace(/, and /, ' and ')}`} stack>
      <thead>
        <tr>
          <th scope="col">{noun}</th>
          <th scope="col" className="num" data-stack={agents ? 'run|runs' : 'call|calls'}>
            {agents ? 'Runs' : 'Calls'}
          </th>
          <th scope="col" className="num">
            Failed
          </th>
          <th scope="col" className="num" data-stack-prefix="median">
            Median time
          </th>
          {costs && (
            <th scope="col" className="num" data-stack="">
              Cost
            </th>
          )}
        </tr>
      </thead>
      <tbody>
        {groups.map((g) => {
          const k = key(g)
          // For an agent: its runs; for a tool or model: its calls.
          const r = runs ? runs.get(k) : g
          return (
            <tr key={k}>
              <td>
                {agents ? (
                  <Link to={agentUrl(projectId, k)}>{k}</Link>
                ) : onPick ? (
                  <button type="button" className="linkish" onClick={() => onPick(k)} title={`Show the steps that used ${k}`}>
                    {k}
                  </button>
                ) : (
                  k
                )}
              </td>
              <td className="num">{fmtNumber(r?.span_count ?? 0)}</td>
              <td className="num">{fmtNumber(r?.error_count ?? 0)}</td>
              <td className="num">{fmtDuration(r?.latency_ms_p50)}</td>
              {costs && <td className="num">{fmtCost(g.total_cost_usd)}</td>}
            </tr>
          )
        })}
      </tbody>
    </PlainTable>
  )
}

/** One agent's versions as a plain table. */
export function VersionsTable({ projectId, agent }: { projectId: string; agent: string }) {
  const q = useQuery({
    queryKey: ['agent-versions', projectId, agent],
    queryFn: () => agentsApi.versions({ project_id: projectId, agent_name: agent, include_costs: true }),
  })
  if (q.isLoading) return <Spinner />
  if (q.error) return <ErrorBox error={q.error} />
  const vs = q.data?.versions ?? []
  if (!vs.length) return <p className="quiet-note">No versions recorded.</p>
  return (
    <PlainTable caption={`Versions of ${agent}`} stack>
      <thead>
        <tr>
          <th scope="col">Version</th>
          <th scope="col" className="num" data-stack="run|runs">
            Runs
          </th>
          <th scope="col" className="num">
            Failed
          </th>
          <th scope="col" className="num" data-stack-prefix="median">
            Median time
          </th>
          <th scope="col" className="num" data-stack="">
            Cost
          </th>
          <th scope="col" data-stack-prefix="last seen">
            Last seen
          </th>
        </tr>
      </thead>
      <tbody>
        {vs.map((v) => (
          <tr key={v.agent_version ?? ''}>
            <td>{v.agent_version || 'No version recorded'}</td>
            <td className="num">{fmtNumber(v.invocation_count)}</td>
            <td className="num">{fmtNumber(v.invocation_error_count)}</td>
            <td className="num">{fmtDuration(v.latency_ms_p50)}</td>
            <td className="num">{fmtCost(v.total_cost_usd)}</td>
            <td title={fmtDate(v.last_seen)}>{fmtRelative(v.last_seen)}</td>
          </tr>
        ))}
      </tbody>
    </PlainTable>
  )
}

/** Searches the messages of every session (of one agent with `agent`) and lists the sessions that match. */
export function MessageSearch({ projectId, agent }: { projectId: string; agent?: string }) {
  const [q, setQ] = useState('')
  const [submitted, setSubmitted] = useState('')
  const res = useQuery({
    queryKey: ['agent-search', projectId, submitted, agent ?? ''],
    queryFn: () => agentsApi.search({ project_id: projectId, query: submitted, agent_name: agent, limit: 20 }),
    enabled: submitted.length > 0,
  })
  return (
    <div className="an-search">
      <form
        onSubmit={(e) => {
          e.preventDefault()
          setSubmitted(q.trim())
        }}
      >
        <SearchField label="Search messages" placeholder="Search what users and agents wrote" value={q} onChange={setQ} />
      </form>
      {res.isLoading && <Spinner label="Searching messages…" />}
      <ErrorBox error={res.error} />
      {res.data &&
        (res.data.results.length === 0 ? (
          <p className="quiet-note">No messages match “{submitted}”.</p>
        ) : (
          <GroupedList title={`Sessions with messages matching “${submitted}”`} count={res.data.total_conversations}>
            {res.data.results.map((r) => (
              <ListRow
                key={r.conversation_id}
                title={r.conversation_name || r.conversation_id}
                summary={r.matched_messages
                  .slice(0, 2)
                  .map((m) => `${m.role ? `${m.role[0].toUpperCase()}${m.role.slice(1)}: ` : ''}${truncate(m.content_preview, 140)}`)
                  .join(' · ')}
                meta={r.agent_name ?? undefined}
                time={fmtRelative(r.last_activity)}
                to={sessionUrl(projectId, r.conversation_id)}
              />
            ))}
          </GroupedList>
        ))}
    </div>
  )
}

export { conversationUrl }
