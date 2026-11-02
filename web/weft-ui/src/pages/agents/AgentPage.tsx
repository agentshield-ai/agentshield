import { useState } from 'react'
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { agentsApi, agentsUrl, conversationUrl, eqQuery, type SpanGroup } from '../../api/agents'
import type { Query } from '../../api/types'
import { useProjectId } from '../../hooks/useProject'
import { fmtCost, fmtDate, fmtDuration, fmtNumber, fmtRelative, truncate } from '../../lib/format'
import { tracesUrl } from '../../lib/urls'
import { Empty, ErrorBox, Pager, Section, Spinner, Tabs } from '../../components/common'
import { SpansTable } from '../../components/agents/SpansTable'
import { SignalsView } from '../../components/agents/SignalsView'
import { AgentEvalsView } from '../../components/agents/AgentEvals'
import { AgentCharts } from '../../components/agents/AgentCharts'
import { KindLegend, SpanStrip } from '../../components/agents/SpanStrip'
import { compact, errorRate, fmtPct, RANGES, windowStart } from '../../components/agents/agentUtils'
import '../../components/agents/agents.css'

const CONV_LIMIT = 25
type AgentTab = 'overview' | 'spans' | 'signals' | 'evals'

function withConversation(q: Query | undefined): Query {
  const exists = { $convert: { input: { $getField: 'conversation_id' }, to: 'exists' } }
  return { $expr: q ? { $and: [q.$expr, exists] } : exists } as Query
}

function ToolUsage({ projectId, agent, since }: { projectId: string; agent: string; since: string }) {
  const q = useQuery({
    queryKey: ['agent-tools', projectId, agent, since],
    queryFn: () =>
      agentsApi.spans({
        project_id: projectId,
        started_after: since,
        query: eqQuery({ agent_name: agent, operation_name: 'execute_tool' }),
        group_by: [{ key: 'tool_name' }],
        sort_by: [{ field: 'span_count', direction: 'desc' }],
        limit: 50,
      }),
  })
  if (q.isLoading) return <Spinner />
  if (q.error) return <ErrorBox error={q.error} />
  const groups = q.data?.groups ?? []
  if (!groups.length) return <span className="small muted">No tool calls in this period.</span>
  const max = Math.max(...groups.map((g) => g.span_count))
  return (
    <div className="col" style={{ gap: 8 }}>
      {groups.map((g) => {
        const name = String(g.group_keys.tool_name ?? '(unnamed)')
        const ok = g.span_count - g.error_count
        return (
          <div key={name} className="tool-bar-row">
            <span className="mono truncate" title={name}>
              {name}
            </span>
            <div className="tool-bar-track" title={`${ok} succeeded · ${g.error_count} failed`}>
              <span style={{ width: `${(ok / max) * 100}%`, background: 'var(--c6)' }} />
              <span style={{ width: `${(g.error_count / max) * 100}%`, background: 'var(--error)' }} />
            </div>
            <span className="small mono muted" style={{ whiteSpace: 'nowrap' }}>
              {fmtNumber(g.span_count)} · {fmtPct(errorRate(g.error_count, g.span_count))} err · p50 {fmtDuration(g.latency_ms_p50)}
            </span>
          </div>
        )
      })}
    </div>
  )
}

function ModelUsage({ projectId, agent, since }: { projectId: string; agent: string; since: string }) {
  const q = useQuery({
    queryKey: ['agent-models', projectId, agent, since],
    queryFn: () =>
      agentsApi.spans({
        project_id: projectId,
        started_after: since,
        include_costs: true,
        query: eqQuery({ agent_name: agent, operation_name: 'chat' }),
        group_by: [{ key: 'request_model' }],
        sort_by: [{ field: 'span_count', direction: 'desc' }],
      }),
  })
  const groups = q.data?.groups ?? []
  if (q.isLoading) return <Spinner />
  if (!groups.length) return <span className="small muted">No LLM calls in this period.</span>
  return (
    <table className="table">
      <thead>
        <tr>
          <th>Model</th>
          <th className="num">Calls</th>
          <th className="num">Tokens in / out</th>
          <th className="num">p50</th>
          <th className="num">Cost</th>
        </tr>
      </thead>
      <tbody>
        {groups.map((g) => (
          <tr key={String(g.group_keys.request_model)}>
            <td className="mono">{String(g.group_keys.request_model ?? '(unknown)')}</td>
            <td className="num">{fmtNumber(g.span_count)}</td>
            <td className="num">
              {compact(g.total_input_tokens)} / {compact(g.total_output_tokens)}
            </td>
            <td className="num">{fmtDuration(g.latency_ms_p50)}</td>
            <td className="num">{fmtCost(g.total_cost_usd)}</td>
          </tr>
        ))}
      </tbody>
    </table>
  )
}

function Versions({ projectId, agent }: { projectId: string; agent: string }) {
  const q = useQuery({
    queryKey: ['agent-versions', projectId, agent],
    queryFn: () => agentsApi.versions({ project_id: projectId, agent_name: agent, include_costs: true }),
  })
  if (q.isLoading) return <Spinner />
  if (q.error) return <ErrorBox error={q.error} />
  const vs = q.data?.versions ?? []
  return (
    <table className="table">
      <thead>
        <tr>
          <th>Version</th>
          <th className="num">Invocations</th>
          <th className="num">Error rate</th>
          <th className="num">p50 / p95</th>
          <th className="num">Tokens</th>
          <th className="num">Cost</th>
          <th>First seen</th>
          <th>Last seen</th>
        </tr>
      </thead>
      <tbody>
        {vs.map((v) => {
          const rate = errorRate(v.invocation_error_count, v.invocation_count)
          return (
            <tr key={v.agent_version ?? ''}>
              <td className="mono">{v.agent_version || <span className="faint">unversioned</span>}</td>
              <td className="num">{fmtNumber(v.invocation_count)}</td>
              <td className="num" style={rate ? { color: 'var(--error)' } : undefined}>
                {fmtPct(rate)}
              </td>
              <td className="num">
                {fmtDuration(v.latency_ms_p50)} / {fmtDuration(v.latency_ms_p95)}
              </td>
              <td className="num">{compact(v.total_input_tokens + v.total_output_tokens)}</td>
              <td className="num">{fmtCost(v.total_cost_usd)}</td>
              <td title={fmtDate(v.first_seen)}>{fmtRelative(v.first_seen)}</td>
              <td title={fmtDate(v.last_seen)}>{fmtRelative(v.last_seen)}</td>
            </tr>
          )
        })}
      </tbody>
    </table>
  )
}

function Conversations({ projectId, agent, since }: { projectId: string; agent: string; since: string }) {
  const navigate = useNavigate()
  const [offset, setOffset] = useState(0)
  const q = useQuery({
    queryKey: ['agent-conversations', projectId, agent, since, offset],
    queryFn: () =>
      agentsApi.spans({
        project_id: projectId,
        started_after: since,
        include_costs: true,
        query: withConversation(eqQuery({ agent_name: agent })),
        group_by: [{ key: 'conversation_id' }],
        sort_by: [{ field: 'last_seen', direction: 'desc' }],
        limit: CONV_LIMIT,
        offset,
      }),
    placeholderData: keepPreviousData,
  })
  const groups: SpanGroup[] = q.data?.groups ?? []
  const ids = groups.map((g) => String(g.group_keys.conversation_id))
  const strips = useQuery({
    queryKey: ['agent-conv-spans', projectId, ids],
    queryFn: () => agentsApi.conversationSpans({ project_id: projectId, conversation_ids: ids }),
    enabled: ids.length > 0,
  })
  const stripFor = new Map((strips.data?.conversations ?? []).map((c) => [c.conversation_id, c.spans]))
  if (q.isLoading) return <Spinner />
  if (q.error) return <ErrorBox error={q.error} />
  if (!groups.length) return <span className="small muted">No conversations in this period.</span>
  return (
    <>
      <div className="row small" style={{ justifyContent: 'flex-end', marginBottom: 6 }}>
        <KindLegend />
      </div>
      <div className="table-wrap">
        <table className="table">
          <thead>
            <tr>
              <th>Conversation</th>
              <th>Last message</th>
              <th>Spans</th>
              <th className="num">Turns</th>
              <th className="num">Tokens</th>
              <th className="num">Cost</th>
              <th>Last activity</th>
            </tr>
          </thead>
          <tbody>
            {groups.map((g) => {
              const id = String(g.group_keys.conversation_id)
              return (
                <tr key={id} className="clickable" onClick={() => navigate(conversationUrl(projectId, id))}>
                  <td style={{ maxWidth: 320 }}>
                    <Link to={conversationUrl(projectId, id)} className="mono small" onClick={(e) => e.stopPropagation()}>
                      {g.conversation_names[0] || id}
                    </Link>
                    {g.first_message && <div className="small muted truncate">{truncate(g.first_message.text, 120)}</div>}
                  </td>
                  <td style={{ maxWidth: 280 }} className="small muted">
                    <div className="truncate">{g.last_message ? truncate(g.last_message.text, 120) : '—'}</div>
                  </td>
                  <td style={{ minWidth: 120 }}>
                    <div className="row" style={{ gap: 6 }}>
                      <span className="small mono">{g.span_count}</span>
                      <div className="grow">{stripFor.get(id) && <SpanStrip spans={stripFor.get(id)!} />}</div>
                    </div>
                  </td>
                  <td className="num">
                    {fmtNumber(g.invocation_count)}
                    {g.error_count > 0 && (
                      <span className="badge error" style={{ marginLeft: 6 }} title={`${g.error_count} failed spans`}>
                        {g.error_count}
                      </span>
                    )}
                  </td>
                  <td className="num">{compact(g.total_input_tokens + g.total_output_tokens)}</td>
                  <td className="num">{fmtCost(g.total_cost_usd)}</td>
                  <td title={fmtDate(g.last_seen)}>{fmtRelative(g.last_seen)}</td>
                </tr>
              )
            })}
          </tbody>
        </table>
      </div>
      <div className="row" style={{ justifyContent: 'flex-end', marginTop: 8 }}>
        <Pager offset={offset} limit={CONV_LIMIT} total={q.data?.total_count ?? null} onChange={setOffset} />
      </div>
    </>
  )
}

export function AgentPage() {
  const projectId = useProjectId()
  const { agentName = '' } = useParams()
  const [params, setParams] = useSearchParams()
  const range = (RANGES as readonly string[]).includes(params.get('range') ?? '') ? params.get('range')! : '7d'
  const tab: AgentTab = (['spans', 'signals', 'evals'] as const).includes(params.get('tab') as 'spans') ? (params.get('tab') as AgentTab) : 'overview'
  const update = (patch: Record<string, string | null>) =>
    setParams(
      (p) => {
        const n = new URLSearchParams(p)
        for (const [k, v] of Object.entries(patch)) (v == null || v === '' ? n.delete(k) : n.set(k, v))
        return n
      },
      { replace: true },
    )
  const since = windowStart(range)
  const summary = useQuery({
    queryKey: ['agent-summary', projectId, agentName],
    queryFn: () => agentsApi.query({ project_id: projectId, include_costs: true, filters: { agent_name: agentName }, limit: 200 }),
  })
  const a = summary.data?.agents.find((x) => x.agent_name === agentName)
  const query = eqQuery({ agent_name: agentName })
  const rate = a ? errorRate(a.invocation_error_count, a.invocation_count) : null
  return (
    <div className="page">
      <div className="breadcrumbs">
        <Link to={agentsUrl(projectId)}>Agents</Link>
        <span>/</span>
        <span>{agentName}</span>
      </div>
      <div className="page-header">
        <h1>{agentName}</h1>
        {a && (
          <span className="small muted" title={fmtDate(a.last_seen)}>
            last seen {fmtRelative(a.last_seen)}
          </span>
        )}
        <span className="spacer" />
        <Link className="btn sm" to={tracesUrl(projectId)}>
          Traces
        </Link>
        {tab === 'overview' && (
          <div className="seg" role="group" aria-label="Time range">
            {RANGES.map((r) => (
              <button key={r} className={r === range ? 'on' : ''} onClick={() => setParams((p) => (p.set('range', r), p), { replace: true })}>
                {r}
              </button>
            ))}
          </div>
        )}
      </div>
      <Tabs<AgentTab>
        tabs={[
          { id: 'overview', label: 'Overview' },
          { id: 'spans', label: 'Spans' },
          { id: 'signals', label: 'Signals' },
          { id: 'evals', label: 'Evals' },
        ]}
        value={tab}
        onChange={(t) => {
          const n = new URLSearchParams()
          if (t !== 'overview') n.set('tab', t)
          if (params.get('range') && (params.get('range') !== 'all' || t === 'spans')) n.set('range', params.get('range')!)
          setParams(n)
        }}
      />
      <ErrorBox error={summary.error} />
      {tab === 'spans' ? (
        <SpansTable projectId={projectId} params={params} update={update} agent={agentName} />
      ) : tab === 'signals' ? (
        <SignalsView projectId={projectId} params={params} update={update} agent={agentName} />
      ) : tab === 'evals' ? (
        <AgentEvalsView projectId={projectId} agent={agentName} />
      ) : summary.isLoading ? (
        <Spinner />
      ) : !a ? (
        <Empty>No spans recorded for agent “{agentName}”.</Empty>
      ) : (
        <>
          <div className="stat-grid">
            <div className="stat">
              <div className="label">Conversations</div>
              <div className="value">{fmtNumber(a.conversation_count)}</div>
            </div>
            <div className="stat">
              <div className="label">Invocations</div>
              <div className="value">{fmtNumber(a.invocation_count)}</div>
            </div>
            <div className="stat">
              <div className="label">Error rate</div>
              <div className="value" style={rate ? { color: 'var(--error)' } : undefined}>
                {fmtPct(rate)}
              </div>
            </div>
            <div className="stat">
              <div className="label">Failed spans</div>
              <div className="value" style={a.error_count ? { color: 'var(--error)' } : undefined}>
                {fmtNumber(a.error_count)}
              </div>
            </div>
            <div className="stat">
              <div className="label">Latency p50 / p95</div>
              <div className="value">
                {fmtDuration(a.latency_ms_p50)} / {fmtDuration(a.latency_ms_p95)}
              </div>
            </div>
            <div className="stat">
              <div className="label">Tokens in / out</div>
              <div className="value">
                {compact(a.total_input_tokens)} / {compact(a.total_output_tokens)}
              </div>
            </div>
            <div className="stat">
              <div className="label">Cost</div>
              <div className="value">{fmtCost(a.total_cost_usd)}</div>
            </div>
          </div>
          <AgentCharts projectId={projectId} query={query} range={range} />
          <div className="grid-2">
            <Section title="Tool usage">
              <ToolUsage projectId={projectId} agent={agentName} since={since} />
            </Section>
            <Section title="Models">
              <ModelUsage projectId={projectId} agent={agentName} since={since} />
            </Section>
          </div>
          <Section title={`Versions (${a.version_count || 1})`}>
            <Versions projectId={projectId} agent={agentName} />
          </Section>
          <Section title="Conversations">
            <Conversations projectId={projectId} agent={agentName} since={since} />
          </Section>
        </>
      )}
    </div>
  )
}
