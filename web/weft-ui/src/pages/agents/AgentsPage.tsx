import { useMemo, useState } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { Bar, BarChart, ResponsiveContainer, Tooltip } from 'recharts'
import { agentsApi, agentUrl, conversationUrl, type AgentStats, type SearchResult } from '../../api/agents'
import { useProjectId } from '../../hooks/useProject'
import { fmtCost, fmtDate, fmtDuration, fmtNumber, fmtRelative } from '../../lib/format'
import { Empty, ErrorBox, Spinner } from '../../components/common'
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

function Metric({ label, value, bad, title }: { label: string; value: string; bad?: boolean; title?: string }) {
  return (
    <div className="agent-metric" title={title}>
      <div className="label">{label}</div>
      <div className={`value ${bad ? 'bad' : ''}`}>{value}</div>
    </div>
  )
}

function AgentCard({ projectId, a, activity }: { projectId: string; a: AgentStats; activity?: { day: string; n: number }[] }) {
  const rate = errorRate(a.invocation_error_count, a.invocation_count)
  return (
    <Link to={agentUrl(projectId, a.agent_name)} className="agent-card">
      <div className="row" style={{ alignItems: 'baseline' }}>
        <span className="agent-name grow" title={a.agent_name}>
          {a.agent_name}
        </span>
        <span className="small faint" title={fmtDate(a.last_seen)}>
          {fmtRelative(a.last_seen)}
        </span>
      </div>
      <div className="chips" style={{ minHeight: 20 }}>
        {(a.versions ?? []).filter(Boolean).slice(0, 4).map((v) => (
          <span key={v} className="chip mono">
            {v}
          </span>
        ))}
        {a.version_count > 4 && <span className="chip">+{a.version_count - 4}</span>}
        {(a.models ?? []).slice(0, 2).map((m) => (
          <span key={m} className="chip faint mono">
            {m}
          </span>
        ))}
      </div>
      {activity && activity.some((d) => d.n > 0) && (
        <div style={{ height: 34 }} aria-label="Invocations per day">
          <ResponsiveContainer width="100%" height="100%">
            <BarChart data={activity} margin={{ top: 0, right: 0, bottom: 0, left: 0 }}>
              <Tooltip contentStyle={TOOLTIP_STYLE} cursor={{ fill: 'var(--panel-2)' }} labelFormatter={(_, p) => String(p?.[0]?.payload?.day ?? '')} />
              <Bar dataKey="n" name="Invocations" fill="var(--c1)" isAnimationActive={false} radius={[2, 2, 0, 0]} />
            </BarChart>
          </ResponsiveContainer>
        </div>
      )}
      <div className="agent-metrics">
        <Metric label="Conversations" value={fmtNumber(a.conversation_count)} />
        <Metric label="Invocations" value={fmtNumber(a.invocation_count)} />
        <Metric label="Calls" value={fmtNumber(a.span_count)} title={`${a.llm_call_count} LLM · ${a.tool_call_count} tool`} />
        <Metric label="Error rate" value={fmtPct(rate)} bad={!!rate} title={`${a.invocation_error_count} failed invocations · ${a.error_count} failed spans`} />
        <Metric label="p50 / p95" value={`${fmtDuration(a.latency_ms_p50)} / ${fmtDuration(a.latency_ms_p95)}`} />
        <Metric label="Failed spans" value={fmtNumber(a.error_count)} bad={a.error_count > 0} title="LLM, tool and agent spans that ended in an error" />
        <Metric label="Tokens" value={`${compact(a.total_input_tokens)} / ${compact(a.total_output_tokens)}`} title="input / output" />
        <Metric label="Cost" value={fmtCost(a.total_cost_usd)} />
        <Metric label="Tools" value={fmtNumber(a.tool_call_count)} />
      </div>
    </Link>
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

export function AgentsPage() {
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
  const totals = agents.reduce(
    (t, a) => ({
      conv: t.conv + a.conversation_count,
      inv: t.inv + a.invocation_count,
      err: t.err + a.invocation_error_count,
      tok: t.tok + a.total_input_tokens + a.total_output_tokens,
      cost: t.cost + (a.total_cost_usd ?? 0),
    }),
    { conv: 0, inv: 0, err: 0, tok: 0, cost: 0 },
  )
  return (
    <div className="page">
      <div className="page-header">
        <h1>Agents</h1>
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
          <div className="stat-grid">
            <div className="stat">
              <div className="label">Agents</div>
              <div className="value">{fmtNumber(q.data?.total_count)}</div>
            </div>
            <div className="stat">
              <div className="label">Conversations</div>
              <div className="value">{fmtNumber(totals.conv)}</div>
            </div>
            <div className="stat">
              <div className="label">Invocations</div>
              <div className="value">{fmtNumber(totals.inv)}</div>
            </div>
            <div className="stat">
              <div className="label">Error rate</div>
              <div className="value" style={totals.err ? { color: 'var(--error)' } : undefined}>
                {fmtPct(errorRate(totals.err, totals.inv))}
              </div>
            </div>
            <div className="stat">
              <div className="label">Tokens</div>
              <div className="value">{compact(totals.tok)}</div>
            </div>
            <div className="stat">
              <div className="label">Cost</div>
              <div className="value">{fmtCost(totals.cost)}</div>
            </div>
          </div>
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
            <div className="agent-grid">
              {agents.map((a) => (
                <AgentCard key={a.agent_name} projectId={projectId} a={a} activity={perAgent.get(a.agent_name)} />
              ))}
            </div>
          )}
        </>
      )}
    </div>
  )
}
