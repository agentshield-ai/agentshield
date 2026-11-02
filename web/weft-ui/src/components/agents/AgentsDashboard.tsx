import { useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { Bar, BarChart, CartesianGrid, ResponsiveContainer, Tooltip, XAxis, YAxis } from 'recharts'
import { agentsApi, agentUrl, conversationUrl, signalsApi, type SpanGroup } from '../../api/agents'
import type { Query } from '../../api/types'
import { fmtCost, fmtDate, fmtDuration, fmtNumber, fmtRelative, truncate } from '../../lib/format'
import { callUrl } from '../../lib/urls'
import { ErrorBox, Section, Spinner } from '../common'
import { AgentCharts } from './AgentCharts'
import { compact, errorRate, fmtPct, windowStart } from './agentUtils'
import { RangePicker, TopList, TOOLTIP_STYLE } from './bits'
import { trendBuckets } from './signalUtils'
import './agents.css'

const plural = (n: number, w: string) => `${fmtNumber(n)} ${w}${n === 1 ? '' : 's'}`
const AXIS = { fontSize: 10, fill: 'var(--text-3)' }
const HAS_CONVERSATION = { $expr: { $convert: { input: { $getField: 'conversation_id' }, to: 'exists' } } } as unknown as Query
const HAS_AGENT = { $expr: { $convert: { input: { $getField: 'agent_name' }, to: 'exists' } } } as unknown as Query
const num = (v: unknown) => (typeof v === 'number' ? v : 0)

function useGroups(projectId: string, since: string, key: string, sortField = 'span_count', limit = 8) {
  return useQuery({
    queryKey: ['agents-dash-groups', projectId, since, key, sortField, limit],
    queryFn: () =>
      agentsApi.spans({ project_id: projectId, started_after: since, include_costs: true, group_by: [{ key }], sort_by: [{ field: sortField, direction: 'desc' }], limit }),
  })
}

function Breakdown({ title, q, onPick }: { title: string; q: ReturnType<typeof useGroups>; onPick?: (k: string) => void }) {
  const keyOf = (g: SpanGroup) => String(Object.values(g.group_keys)[0] ?? '')
  return (
    <Section title={title}>
      {q.isLoading ? (
        <Spinner />
      ) : q.error ? (
        <ErrorBox error={q.error} />
      ) : (
        <TopList
          items={(q.data?.groups ?? []).filter((g) => keyOf(g) !== '').map((g) => ({ key: keyOf(g), value: g.span_count, bad: g.error_count, sub: g.total_cost_usd ? fmtCost(g.total_cost_usd) : undefined }))}
          fmt={fmtNumber}
          onPick={onPick}
        />
      )}
    </Section>
  )
}

/** Project-wide overview across all agents: totals, volume over time, top-N breakdowns and recent activity. */
export function AgentsDashboard({ projectId, range, setRange, onFilterSpans }: { projectId: string; range: string; setRange: (r: string) => void; onFilterSpans: (patch: Record<string, string>) => void }) {
  const since = useMemo(() => windowStart(range), [range])
  const [recent, setRecent] = useState<'conversations' | 'spans'>('conversations')
  const totals = useQuery({
    queryKey: ['agents-dash-totals', projectId, since],
    queryFn: async () => {
      const [all, inv, conv, agents] = await Promise.all([
        agentsApi.stats({
          project_id: projectId,
          start: since,
          granularity: 365 * 86_400,
          metrics: [
            { alias: 'in', value: { key: 'input_tokens' }, value_type: 'number', aggregations: ['sum'] },
            { alias: 'out', value: { key: 'output_tokens' }, value_type: 'number', aggregations: ['sum'] },
            { alias: 'cost', value: { key: 'total_cost_usd' }, value_type: 'number', aggregations: ['sum'] },
            { alias: 'err', value: { key: 'is_error' }, value_type: 'boolean', aggregations: ['count_true'] },
          ],
        }),
        agentsApi.stats({
          project_id: projectId,
          start: since,
          granularity: 365 * 86_400,
          query: { $expr: { $eq: [{ $getField: 'is_invocation' }, { $literal: true }] } },
          metrics: [{ alias: 'err', value: { key: 'is_error' }, value_type: 'boolean', aggregations: ['count_true'] }],
        }),
        agentsApi.spans({ project_id: projectId, started_after: since, query: HAS_CONVERSATION, group_by: [{ key: 'conversation_id' }], limit: 1 }),
        agentsApi.spans({ project_id: projectId, started_after: since, query: HAS_AGENT, group_by: [{ key: 'agent_name' }], limit: 1 }),
      ])
      const sum = (rows: Record<string, unknown>[], k: string) => rows.reduce((s, r) => s + num(r[k]), 0)
      return {
        spans: sum(all.rows, 'count'),
        spanErrors: sum(all.rows, 'err_count_true'),
        tokens: sum(all.rows, 'in_sum') + sum(all.rows, 'out_sum'),
        cost: sum(all.rows, 'cost_sum'),
        invocations: sum(inv.rows, 'count'),
        failed: sum(inv.rows, 'err_count_true'),
        conversations: conv.total_count,
        agents: agents.total_count,
      }
    },
  })
  const tools = useGroups(projectId, since, 'tool_name')
  const agents = useGroups(projectId, since, 'agent_name')
  const models = useGroups(projectId, since, 'request_model')
  const providers = useGroups(projectId, since, 'provider_name')
  const recentConvs = useQuery({
    queryKey: ['agents-dash-recent-convs', projectId, since],
    queryFn: () =>
      agentsApi.spans({
        project_id: projectId,
        started_after: since,
        include_costs: true,
        query: HAS_CONVERSATION,
        group_by: [{ key: 'conversation_id' }],
        sort_by: [{ field: 'last_seen', direction: 'desc' }],
        limit: 10,
      }),
    enabled: recent === 'conversations',
  })
  const recentSpans = useQuery({
    queryKey: ['agents-dash-recent-spans', projectId, since],
    queryFn: () => agentsApi.spans({ project_id: projectId, started_after: since, include_costs: true, sort_by: [{ field: 'started_at', direction: 'desc' }], limit: 12 }),
    enabled: recent === 'spans',
  })
  const signals = useQuery({
    queryKey: ['agents-dash-signals', projectId, since],
    queryFn: () => signalsApi.query({ project_id: projectId, started_after: since, limit: 2000 }),
  })
  const signalTimeline = useMemo(() => {
    const rs = signals.data?.results ?? []
    const ratings = trendBuckets(rs.filter((r) => r.kind === 'rating'), new Date(since), new Date(), 24)
    const tags = trendBuckets(rs.filter((r) => r.kind === 'tag'), new Date(since), new Date(), 24)
    return ratings.map((p, i) => ({ t: p.t, ratings: p.count, tags: tags[i].value ?? 0 }))
  }, [signals.data, since])
  const t = totals.data
  return (
    <div className="col" style={{ gap: 16 }}>
      <div className="row wrap">
        <span className="small muted grow">Health of every agent in this project. All panels use the selected window.</span>
        {totals.isFetching && <Spinner />}
        <RangePicker value={range} onChange={setRange} />
      </div>
      <ErrorBox error={totals.error} />
      <div className="stat-grid">
        {[
          ['Spans', t ? fmtNumber(t.spans) : '…', undefined],
          ['Conversations', t ? fmtNumber(t.conversations) : '…', undefined],
          ['Tokens', t ? compact(t.tokens) : '…', undefined],
          ['Cost', t ? fmtCost(t.cost) : '…', undefined],
          ['Error rate', t ? fmtPct(errorRate(t.failed, t.invocations)) : '…', t && t.failed ? 'var(--error)' : undefined],
          ['Agents', t ? fmtNumber(t.agents) : '…', undefined],
        ].map(([label, value, color]) => (
          <div key={label} className="stat">
            <div className="label">{label}</div>
            <div className="value" style={color ? { color } : undefined}>
              {value}
            </div>
          </div>
        ))}
      </div>
      <AgentCharts projectId={projectId} range={range} />
      <div className="agents-dash-grid">
        <Section
          title="Recent activity"
          actions={
            <div className="seg" role="group" aria-label="Recent activity">
              <button className={recent === 'conversations' ? 'on' : ''} aria-pressed={recent === 'conversations'} onClick={() => setRecent('conversations')}>
                Conversations
              </button>
              <button className={recent === 'spans' ? 'on' : ''} aria-pressed={recent === 'spans'} onClick={() => setRecent('spans')}>
                Spans
              </button>
            </div>
          }
        >
          {recent === 'conversations' ? (
            recentConvs.isLoading ? (
              <Spinner />
            ) : !(recentConvs.data?.groups ?? []).length ? (
              <span className="small muted">No conversations in this period.</span>
            ) : (
              <div className="recent-list">
                {recentConvs.data!.groups.map((g) => {
                  const id = String(g.group_keys.conversation_id)
                  return (
                    <Link key={id} to={conversationUrl(projectId, id)} className="recent-item">
                      <div className="row small">
                        <span className="mono truncate grow">{g.conversation_names[0] || id}</span>
                        <span className="faint" title={fmtDate(g.last_seen)}>
                          {fmtRelative(g.last_seen)}
                        </span>
                      </div>
                      <div className="small muted truncate">{g.last_message ? truncate(g.last_message.text, 140) : g.first_message ? truncate(g.first_message.text, 140) : '—'}</div>
                      <div className="row small faint">
                        {g.agent_names.slice(0, 2).map((a) => (
                          <span key={a} className="chip">
                            {a}
                          </span>
                        ))}
                        <span>{plural(Math.max(g.turn_count ?? 0, g.invocation_count), 'turn')}</span>
                        {g.error_count > 0 && <span style={{ color: 'var(--error)' }}>{g.error_count} failed</span>}
                        <span className="grow" />
                        <span className="mono">{fmtCost(g.total_cost_usd)}</span>
                      </div>
                    </Link>
                  )
                })}
              </div>
            )
          ) : recentSpans.isLoading ? (
            <Spinner />
          ) : (
            <div className="recent-list">
              {(recentSpans.data?.spans ?? []).map((s) => (
                <Link key={s.span_id} to={callUrl(projectId, s.span_id)} className="recent-item">
                  <div className="row small">
                    <span className={`kind-dot kind-${s.status_code === 'ERROR' ? 'error' : s.operation_name === 'chat' ? 'llm' : s.operation_name === 'execute_tool' ? 'tool' : s.operation_name === 'invoke_agent' ? 'agent' : 'other'}`} />
                    <span className="mono truncate grow">{s.display_name}</span>
                    <span className="faint">{fmtRelative(s.started_at)}</span>
                  </div>
                  <div className="row small faint">
                    <span>{s.operation_name ?? 'span'}</span>
                    {s.agent_name && <span>{s.agent_name}</span>}
                    <span className="grow" />
                    <span className="mono">{fmtDuration(s.duration_ms)}</span>
                  </div>
                </Link>
              ))}
            </div>
          )}
        </Section>
        <div className="col" style={{ gap: 16, minWidth: 0 }}>
          <Section title="Signal activity">
            {signals.isLoading ? (
              <Spinner />
            ) : !(signals.data?.results ?? []).length ? (
              <span className="small muted">No signal results in this period. Turn on signals in the Signals tab.</span>
            ) : (
              <div style={{ height: 120 }}>
                <ResponsiveContainer width="100%" height="100%">
                  <BarChart data={signalTimeline} margin={{ top: 4, right: 4, bottom: 0, left: 0 }}>
                    <CartesianGrid vertical={false} stroke="var(--border)" />
                    <XAxis dataKey="t" tick={AXIS} minTickGap={30} axisLine={false} tickLine={false} tickFormatter={(iso: string) => new Date(iso).toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit' })} />
                    <YAxis tick={AXIS} allowDecimals={false} width={28} axisLine={false} tickLine={false} />
                    <Tooltip contentStyle={TOOLTIP_STYLE} labelFormatter={(iso) => new Date(String(iso)).toLocaleString()} cursor={{ fill: 'var(--panel-2)' }} />
                    <Bar dataKey="ratings" name="Ratings" stackId="s" fill="var(--c2)" isAnimationActive={false} />
                    <Bar dataKey="tags" name="Tags matched" stackId="s" fill="var(--c4)" isAnimationActive={false} />
                  </BarChart>
                </ResponsiveContainer>
              </div>
            )}
          </Section>
          <div className="grid-2">
            <Breakdown title="Top agents" q={agents} onPick={(k) => onFilterSpans({ agent: k })} />
            <Breakdown title="Top tools" q={tools} onPick={(k) => onFilterSpans({ tool: k })} />
            <Breakdown title="Top models" q={models} onPick={(k) => onFilterSpans({ model: k })} />
            <Breakdown title="Top providers" q={providers} />
          </div>
          {agents.data?.groups.length ? (
            <div className="small muted">
              Open an agent:{' '}
              {agents.data.groups
                .map((g) => String(g.group_keys.agent_name ?? ''))
                .filter(Boolean)
                .map((a, i) => (
                  <span key={a}>
                    {i > 0 && ' · '}
                    <Link to={agentUrl(projectId, a)}>{a}</Link>
                  </span>
                ))}
            </div>
          ) : null}
        </div>
      </div>
    </div>
  )
}
