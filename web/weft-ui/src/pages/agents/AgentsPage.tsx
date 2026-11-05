import { useMemo, useState } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { Bar, BarChart, ResponsiveContainer, Tooltip } from 'recharts'
import { agentsApi, agentUrl, conversationUrl, type AgentStats, type SearchResult } from '../../api/agents'
import { useProjectId } from '../../hooks/useProject'
import { fmtCost, fmtDate, fmtDuration, fmtNumber, fmtRelative } from '../../lib/format'
import { Empty, ErrorBox, Spinner } from '../../components/common'
import { PageHeader } from '../../components/ui/PageHeader'
import { PlainTable } from '../../components/ui/PlainTable'
import { Segmented, segmentedTabId } from '../../components/ui/Segmented'
import { Sentence } from '../../components/ui/Sentence'
import { agentsUrlFor } from '../../lib/urls'
import { agentsTotalsSentence } from '../../lib/summaries/agentsTeams'
import './agents-teams.css'
import { AgentsDashboard } from '../../components/agents/AgentsDashboard'
import { SpansTable } from '../../components/agents/SpansTable'
import { SignalsView } from '../../components/agents/SignalsView'
import { AgentEvalsView } from '../../components/agents/AgentEvals'
import { compact, errorRate, fmtPct } from '../../components/agents/agentUtils'
import '../../components/agents/agents.css'

const SORTS: { id: string; label: string; field: string; direction: 'asc' | 'desc' }[] = [
  { id: 'recent', label: 'Last seen', field: 'last_seen', direction: 'desc' },
  { id: 'invocations', label: 'Most invocations', field: 'invocation_count', direction: 'desc' },
  { id: 'tokens', label: 'Most input tokens', field: 'total_input_tokens', direction: 'desc' },
  { id: 'cost', label: 'Highest cost', field: 'total_cost_usd', direction: 'desc' },
  { id: 'errors', label: 'Most errors', field: 'error_count', direction: 'desc' },
  { id: 'latency', label: 'Slowest (p95)', field: 'latency_ms_p95', direction: 'desc' },
  { id: 'name', label: 'Name', field: 'agent_name', direction: 'asc' },
]

const TOOLTIP_STYLE = { background: 'var(--panel)', border: '1px solid var(--border)', borderRadius: 6, fontSize: 12, color: 'var(--text)' }

/** Invocations per day over the last 14 days, as a tiny bar chart. */
function Activity({ data }: { data?: { day: string; n: number }[] }) {
  if (!data || !data.some((d) => d.n > 0)) return <span className="faint">—</span>
  return (
    <div style={{ height: 26, width: 96 }} role="img" aria-label={`Runs per day over the last 14 days: ${data.map((d) => d.n).join(', ')}`}>
      <ResponsiveContainer width="100%" height="100%">
        <BarChart data={data} margin={{ top: 0, right: 0, bottom: 0, left: 0 }}>
          <Tooltip contentStyle={TOOLTIP_STYLE} cursor={{ fill: 'var(--panel-2)' }} labelFormatter={(_, p) => String(p?.[0]?.payload?.day ?? '')} />
          <Bar dataKey="n" name="Runs" fill="var(--text-muted)" isAnimationActive={false} radius={[2, 2, 0, 0]} />
        </BarChart>
      </ResponsiveContainer>
    </div>
  )
}

function AgentTable({ projectId, agents, activity }: { projectId: string; agents: AgentStats[]; activity: Map<string, { day: string; n: number }[]> }) {
  return (
    <PlainTable caption="Agents, with their runs, failures, timing, tokens and cost" className="analytics-agents-table">
      <thead>
        <tr>
          <th scope="col">Agent</th>
          <th scope="col">Last 14 days</th>
          <th scope="col" className="num">
            Conversations
          </th>
          <th scope="col" className="num" title="Invocations of the agent">
            Runs
          </th>
          <th scope="col" className="num" title="Failed runs, out of all runs">
            Failed
          </th>
          <th scope="col" className="num" title="Median and 95th percentile time of one run">
            p50 / p95
          </th>
          <th scope="col" className="num" title="LLM, tool and agent spans that ended in an error">
            Failed spans
          </th>
          <th scope="col" className="num" title="Input / output tokens">
            Tokens
          </th>
          <th scope="col" className="num">
            Cost
          </th>
          <th scope="col">Versions and models</th>
          <th scope="col">Last seen</th>
        </tr>
      </thead>
      <tbody>
        {agents.map((a) => {
          const rate = errorRate(a.invocation_error_count, a.invocation_count)
          return (
            <tr key={a.agent_name}>
              <td>
                <Link to={agentUrl(projectId, a.agent_name)}>{a.agent_name}</Link>
              </td>
              <td>
                <Activity data={activity.get(a.agent_name)} />
              </td>
              <td className="num">{fmtNumber(a.conversation_count)}</td>
              <td className="num" title={`${a.llm_call_count} LLM calls · ${a.tool_call_count} tool calls · ${a.span_count} spans`}>
                {fmtNumber(a.invocation_count)}
              </td>
              <td className={`num ${rate ? 'text-error' : ''}`}>{fmtPct(rate)}</td>
              <td className="num nowrap">
                {fmtDuration(a.latency_ms_p50)} / {fmtDuration(a.latency_ms_p95)}
              </td>
              <td className={`num ${a.error_count ? 'text-error' : ''}`}>{fmtNumber(a.error_count)}</td>
              <td className="num nowrap">
                {compact(a.total_input_tokens)} / {compact(a.total_output_tokens)}
              </td>
              <td className="num">{fmtCost(a.total_cost_usd)}</td>
              <td className="small mono">
                {[...(a.versions ?? []).filter(Boolean).slice(0, 4), ...(a.version_count > 4 ? [`+${a.version_count - 4}`] : []), ...(a.models ?? []).slice(0, 2)].join(', ') || <span className="faint">—</span>}
              </td>
              <td className="nowrap" title={fmtDate(a.last_seen)}>
                {fmtRelative(a.last_seen)}
              </td>
            </tr>
          )
        })}
      </tbody>
    </PlainTable>
  )
}

function highlight(text: string, q: string) {
  if (!q) return text
  const i = text.toLowerCase().indexOf(q.toLowerCase())
  if (i < 0) return text
  return (
    <>
      {text.slice(0, i)}
      <mark>{text.slice(i, i + q.length)}</mark>
      {text.slice(i + q.length)}
    </>
  )
}

function SearchResults({ projectId, q }: { projectId: string; q: string }) {
  const res = useQuery({
    queryKey: ['agent-search', projectId, q],
    queryFn: () => agentsApi.search({ project_id: projectId, query: q, limit: 20 }),
    enabled: q.length > 0,
  })
  if (res.isLoading) return <Spinner label="Searching messages…" />
  if (res.error) return <ErrorBox error={res.error} />
  const results: SearchResult[] = res.data?.results ?? []
  return (
    <div className="card">
      <div className="card-header">
        <h3 className="grow">
          Messages matching “{q}”
        </h3>
        <span className="small muted">{fmtNumber(res.data?.total_conversations ?? 0)} conversations</span>
      </div>
      {results.length === 0 ? (
        <div className="card-body small muted">No messages match.</div>
      ) : (
        <div>
          {results.map((r) => (
            <div key={r.conversation_id} className="agent-search-hit">
              <div className="row small">
                <Link to={conversationUrl(projectId, r.conversation_id)} className="mono">
                  {r.conversation_name || r.conversation_id}
                </Link>
                {r.agent_name && (
                  <Link to={agentUrl(projectId, r.agent_name)} className="chip">
                    {r.agent_name}
                  </Link>
                )}
                <span className="grow" />
                <span className="faint">{fmtRelative(r.last_activity)}</span>
              </div>
              {r.matched_messages.slice(0, 3).map((m) => (
                <div key={m.content_digest} className="small" style={{ marginTop: 4 }}>
                  <span className="badge" style={{ marginRight: 6 }}>
                    {m.role || 'message'}
                  </span>
                  <span className="muted">{highlight(m.content_preview, q)}</span>
                </div>
              ))}
              {r.matched_messages.length > 3 && <div className="small faint">+{r.matched_messages.length - 3} more</div>}
            </div>
          ))}
        </div>
      )}
    </div>
  )
}

function AgentsGrid() {
  const projectId = useProjectId()
  const [params, setParams] = useSearchParams()
  const sortId = params.get('sort') ?? 'recent'
  const sort = SORTS.find((s) => s.id === sortId) ?? SORTS[0]
  const name = params.get('name') ?? ''
  const search = params.get('q') ?? ''
  const [draft, setDraft] = useState(search)
  const set = (patch: Record<string, string | null>) =>
    setParams((p) => {
      const n = new URLSearchParams(p)
      for (const [k, v] of Object.entries(patch)) (v == null || v === '' ? n.delete(k) : n.set(k, v))
      return n
    }, { replace: true })

  const q = useQuery({
    queryKey: ['agents', projectId, sort.id, name],
    queryFn: () =>
      agentsApi.query({ project_id: projectId, include_costs: true, limit: 200, filters: name ? { agent_name: name } : undefined, sort_by: [{ field: sort.field, direction: sort.direction }] }),
    placeholderData: keepPreviousData,
  })
  const since = useMemo(() => new Date(Date.now() - 14 * 86_400_000).toISOString(), [])
  const activity = useQuery({
    queryKey: ['agents-activity', projectId, since],
    queryFn: () =>
      agentsApi.stats({
        project_id: projectId,
        start: since,
        granularity: 86_400,
        group_by: [{ key: 'agent_name' }],
        group_limit: 200,
        query: { $expr: { $eq: [{ $getField: 'is_invocation' }, { $literal: true }] } },
      }),
  })
  const perAgent = useMemo(() => {
    const out = new Map<string, { day: string; n: number }[]>()
    const rows = activity.data?.rows ?? []
    const days = [...new Set(rows.map((r) => String(r.timestamp)))].sort()
    for (const r of rows) {
      const a = r.agent_name
      if (typeof a !== 'string') continue
      if (!out.has(a)) out.set(a, days.map((d) => ({ day: new Date(d).toLocaleDateString(), n: 0 })))
      out.get(a)![days.indexOf(String(r.timestamp))].n = Number(r.count ?? 0)
    }
    return out
  }, [activity.data])

  const agents = q.data?.agents ?? []
  return (
    <div className="col" style={{ gap: 16 }}>
      <div className="row wrap">
        {q.isFetching && <Spinner />}
        <span className="spacer" />
        <form
          className="row"
          onSubmit={(e) => {
            e.preventDefault()
            set({ q: draft.trim() })
          }}
        >
          <input className="input" style={{ width: 260 }} placeholder="Search messages in conversations…" value={draft} onChange={(e) => setDraft(e.target.value)} aria-label="Search messages" />
          {search && (
            <button
              type="button"
              className="btn"
              onClick={() => {
                setDraft('')
                set({ q: null })
              }}
            >
              Clear
            </button>
          )}
        </form>
      </div>
      <ErrorBox error={q.error} />
      {search && <SearchResults projectId={projectId} q={search} />}
      {q.isLoading ? (
        <div className="empty">
          <Spinner label="Loading agents…" />
        </div>
      ) : agents.length === 0 && !name ? (
        <Empty>
          <div className="col" style={{ alignItems: 'center', maxWidth: 560 }}>
            <b>No agents yet</b>
            <span className="small">
              Agents are grouped by name. Send OpenTelemetry spans with <code>gen_ai.operation.name</code> (<code>invoke_agent</code>, <code>chat</code>,{' '}
              <code>execute_tool</code>), <code>gen_ai.agent.name</code> and <code>gen_ai.conversation.id</code>, or OpenInference / OpenLLMetry agent spans.
              With the SDKs, mark an op with <code>kind="agent"</code> (it sets <code>weft.span_kind</code>).
            </span>
          </div>
        </Empty>
      ) : (
        <>
          <div className="toolbar row">
            <input className="input" style={{ width: 220 }} placeholder="Filter agents by name" value={name} onChange={(e) => set({ name: e.target.value })} aria-label="Filter agents by name" />
            <span className="grow" />
            <label className="small muted row">
              Sort
              <select className="input" value={sort.id} onChange={(e) => set({ sort: e.target.value === 'recent' ? null : e.target.value })}>
                {SORTS.map((s) => (
                  <option key={s.id} value={s.id}>
                    {s.label}
                  </option>
                ))}
              </select>
            </label>
          </div>
          {agents.length === 0 ? (
            <Empty>No agents match “{name}”.</Empty>
          ) : (
            <AgentTable projectId={projectId} agents={agents} activity={perAgent} />
          )}
        </>
      )}
    </div>
  )
}

type AgentsTab = 'dashboard' | 'agents' | 'spans' | 'signals' | 'evals'
const AGENT_TABS: { id: AgentsTab; label: string; title: string }[] = [
  { id: 'dashboard', label: 'Dashboard', title: 'Activity, failures, tools and models over a time window' },
  { id: 'agents', label: 'Agents', title: 'Every agent with its runs, timing, tokens and cost; search messages' },
  { id: 'spans', label: 'Spans', title: 'Search and group every agent span' },
  { id: 'signals', label: 'Signals', title: 'Ratings and tags computed over conversations' },
  { id: 'evals', label: 'Evaluations', title: 'Evaluations of agents' },
]

/** Agent analytics, the developer view behind Agents: dashboard, every agent's figures, spans, signals and evaluations. */
export function AgentsPage() {
  const projectId = useProjectId()
  const [params, setParams] = useSearchParams()
  const tab = (AGENT_TABS.some((t) => t.id === params.get('tab')) ? params.get('tab') : 'dashboard') as AgentsTab
  const all = useQuery({
    queryKey: ['agents', projectId, 'recent', ''],
    queryFn: () => agentsApi.query({ project_id: projectId, include_costs: true, limit: 200, sort_by: [{ field: 'last_seen', direction: 'desc' }] }),
  })
  const update = (patch: Record<string, string | null>) =>
    setParams(
      (p) => {
        const n = new URLSearchParams(p)
        for (const [k, v] of Object.entries(patch)) (v == null || v === '' ? n.delete(k) : n.set(k, v))
        return n
      },
      { replace: true },
    )
  const switchTab = (t: AgentsTab, extra: Record<string, string> = {}) => {
    // Each tab keeps its own filters; only the time range carries over.
    const n = new URLSearchParams(extra)
    if (t !== 'dashboard') n.set('tab', t)
    const range = params.get('range')
    // "all" (no time bound) exists only in the Spans tab.
    if (range && !n.has('range') && (range !== 'all' || t === 'spans')) n.set('range', range)
    setParams(n)
  }
  return (
    <div className="page-frame detail analytics-page">
      <PageHeader
        variant="detail"
        back={{ to: agentsUrlFor(projectId), label: 'Agents' }}
        title="Agent analytics"
        help="agents.md"
        helpTopic="Agent analytics"
        sentence={
          all.data ? (
            <Sentence size="detail" parts={[allAgentsSentence(all.data.agents, all.data.total_count)]} />
          ) : (
            <p className="sentence s-detail sk-sentence" aria-busy="true">
              <span className="sr-only">Counting agents…</span>
              <span className="sk sk-line" aria-hidden />
            </p>
          )
        }
      />
      <ErrorBox error={all.error} />
      <Segmented label="Views" role="tabs" panelId="analytics-view" className="segmented-views" options={AGENT_TABS} value={tab} onChange={(t) => switchTab(t)} />
      <div role="tabpanel" id="analytics-view" aria-labelledby={segmentedTabId('analytics-view', tab)} className="analytics-panel">
        {tab === 'dashboard' && (
          <AgentsDashboard projectId={projectId} range={params.get('range') ?? '24h'} setRange={(r) => update({ range: r === '24h' ? null : r })} onFilterSpans={(patch) => switchTab('spans', patch)} />
        )}
        {tab === 'agents' && <AgentsGrid />}
        {tab === 'spans' && <SpansTable projectId={projectId} params={params} update={update} />}
        {tab === 'signals' && <SignalsView projectId={projectId} params={params} update={update} />}
        {tab === 'evals' && <AgentEvalsView projectId={projectId} />}
      </div>
    </div>
  )
}

/** The header sentence: every agent's runs, failures, tokens and cost over all time. */
function allAgentsSentence(agents: AgentStats[], total: number): string {
  const t = agents.reduce(
    (t, a) => ({
      spans: t.spans + a.span_count,
      invocations: t.invocations + a.invocation_count,
      failed: t.failed + a.invocation_error_count,
      tokens: t.tokens + a.total_input_tokens + a.total_output_tokens,
      cost: t.cost + (a.total_cost_usd ?? 0),
    }),
    { spans: 0, invocations: 0, failed: 0, tokens: 0, cost: 0 },
  )
  // Conversations are not summed: several agents can share one.
  const s = agentsTotalsSentence({ agents: total, conversations: 0, ...t }, 'all')
  return total > agents.length ? `${s} (Figures for the ${agents.length} most recently active.)` : s
}
