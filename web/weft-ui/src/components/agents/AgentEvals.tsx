import { useMemo, useState, type ReactNode } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { agentEvalsApi, conversationUrl, evalSpansUrl, type AgentEvalPrediction, type AgentEvalRun } from '../../api/agents'
import type { Any, CallStatus } from '../../api/types'
import { refUrl, callUrl, compareEvaluationsUrl } from '../../lib/urls'
import { fmtCost, fmtDate, fmtNumber, fmtRelative, preview, truncate } from '../../lib/format'
import { Empty, ErrorBox, Pager, Spinner, StatusBadge } from '../common'
import { flattenScores, fmtMetric, sortMetricKeys } from '../evals/evalUtils'
import { fmtScoreValue, predictionsByTrace, summaryMetrics } from './agentEvalUtils'

export { predictionsByTrace }
import { compact } from './agentUtils'
import './agents.css'

// Agent evals: evaluation runs whose predictions link to agent spans (spans
// tagged weave.eval.*, or turns and conversations linked through
// EvaluationLogger.log_prediction(call=...)).

const LIMIT = 50
const CALL_STATUSES: CallStatus[] = ['success', 'error', 'running', 'descendant_error']

export function EvalStatus({ status }: { status: string }) {
  return CALL_STATUSES.includes(status as CallStatus) ? <StatusBadge status={status as CallStatus} /> : <span className="small faint">{status || 'unknown'}</span>
}

/** Score chips for a prediction's scores (`{scorer: value | {field: value}}`). */
export function ScoreChips({ scores, max = 6 }: { scores: Any; max?: number }) {
  const flat = flattenScores(scores ?? {})
  const keys = sortMetricKeys(Object.keys(flat)).slice(0, max)
  if (!keys.length) return <span className="faint small">no scores</span>
  return (
    <span className="chips" style={{ flexWrap: 'wrap' }}>
      {keys.map((k) => {
        const v = flat[k]
        const cls = typeof v === 'boolean' ? (v ? 'success' : 'error') : ''
        return (
          <span key={k} className={`badge ${cls}`} title={`${k}: ${fmtMetric(v)}`}>
            <span style={{ opacity: 0.75 }}>{k}</span> {fmtScoreValue(k, v)}
          </span>
        )
      })}
    </span>
  )
}

function RefName({ refStr, name }: { refStr: string | null; name: string }) {
  const url = refStr ? refUrl(refStr) : null
  return url ? (
    <Link to={url} onClick={(e) => e.stopPropagation()} title={refStr ?? undefined}>
      {name}
    </Link>
  ) : (
    <span>{name || '—'}</span>
  )
}

/** "View spans" / "View conversations" for an evaluation run (or one prediction). */
export function EvalAgentLinks({ projectId, runId, pred, agent, compact: small }: { projectId: string; runId: string; pred?: string; agent?: string; compact?: boolean }) {
  const cls = `btn sm ${small ? 'ghost' : ''}`
  return (
    <span className="row" style={{ gap: 4, flexWrap: 'nowrap' }}>
      <Link className={cls} to={evalSpansUrl(projectId, runId, { pred, agent })} onClick={(e) => e.stopPropagation()} title="Open the Agents Spans view filtered to this evaluation">
        View spans
      </Link>
      <Link className={cls} to={evalSpansUrl(projectId, runId, { pred, agent, conversations: true })} onClick={(e) => e.stopPropagation()} title="The conversations this evaluation's predictions recorded or linked">
        Conversations
      </Link>
    </span>
  )
}

/** Agent activity per run id, for marking evaluation runs that have agent spans. */
export function useAgentEvalRuns(projectId: string, runIds: string[]) {
  const ids = useMemo(() => [...new Set(runIds)].sort(), [runIds])
  return useQuery({
    queryKey: ['agent-evals-by-run', projectId, ids],
    queryFn: () => agentEvalsApi.query({ project_id: projectId, evaluation_run_ids: ids, include_costs: true, limit: ids.length }),
    enabled: ids.length > 0,
    staleTime: 15_000,
    select: (r) => new Map(r.evaluations.map((e) => [e.call_id, e])),
  })
}

const LINK_HELP = (
  <span className="small">
    Run the agent inside <code>with ev.log_prediction(inputs) as pred:</code> of an <code>EvaluationLogger</code> (its spans get <code>weave.eval.*</code>{' '}
    attributes), link turns or conversations you already recorded with <code>ev.log_prediction(inputs, call=conversation)</code>, or set{' '}
    <code>weave.eval.run_id</code> and <code>weave.eval.predict_and_score_call_id</code> on your own OpenTelemetry spans.
  </span>
)

/**
 * The Evals tab of the Agents view: evaluation runs that involved the
 * project's agents (or one agent), with scores, linking to the comparison and
 * to the Spans view filtered to each run.
 */
export function AgentEvalsView({ projectId, agent }: { projectId: string; agent?: string }) {
  const nav = useNavigate()
  const [offset, setOffset] = useState(0)
  const [sel, setSel] = useState<string[]>([])
  const q = useQuery({
    queryKey: ['agent-evals', projectId, agent ?? null, offset],
    queryFn: () => agentEvalsApi.query({ project_id: projectId, agent_name: agent, include_costs: true, limit: LIMIT, offset }),
    placeholderData: keepPreviousData,
    refetchInterval: (qq) => (qq.state.data?.evaluations.some((e) => e.status === 'running') ? 5000 : false),
  })
  const runs: AgentEvalRun[] = q.data?.evaluations ?? []
  const metricKeys = useMemo(() => {
    const keys = new Set<string>()
    runs.forEach((r) => Object.keys(summaryMetrics(r.summary)).forEach((k) => keys.add(k)))
    return sortMetricKeys([...keys]).slice(0, 6)
  }, [runs])
  const toggle = (id: string) => setSel((s) => (s.includes(id) ? s.filter((x) => x !== id) : [...s, id]))

  if (q.isLoading)
    return (
      <div className="empty">
        <Spinner label="Loading evaluations…" />
      </div>
    )
  return (
    <div className="col" style={{ gap: 10 }}>
      <div className="toolbar row wrap">
        <span className="small muted grow" style={{ minWidth: 200 }}>
          Evaluation runs whose predictions recorded or linked {agent ? <b>{agent}</b> : 'agent'} spans. Totals cover {agent ? "this agent's" : 'the linked'} spans.
        </span>
        {q.isFetching && <Spinner />}
        {sel.length > 0 && (
          <button className="btn sm ghost" onClick={() => setSel([])}>
            Clear selection
          </button>
        )}
        <button className="btn sm" disabled={!sel.length} onClick={() => nav(compareEvaluationsUrl(projectId, sel))}>
          Compare{sel.length ? ` (${sel.length})` : ''}
        </button>
      </div>
      <ErrorBox error={q.error} />
      {runs.length === 0 ? (
        <Empty>
          <div className="col" style={{ alignItems: 'center', maxWidth: 620, gap: 6 }}>
            <b>No evaluations involve {agent ? `“${agent}”` : 'these agents'} yet</b>
            {LINK_HELP}
          </div>
        </Empty>
      ) : (
        <div className="table-wrap">
          <table className="table agent-evals-table">
            <thead>
              <tr>
                <th style={{ width: 32 }}>
                  <input type="checkbox" aria-label="Select all runs" checked={sel.length === runs.length} onChange={(e) => setSel(e.target.checked ? runs.map((r) => r.call_id) : [])} />
                </th>
                <th>Status</th>
                <th>Evaluation · model</th>
                <th>Started</th>
                <th className="num" title="Predictions with agent spans">
                  Predictions
                </th>
                <th className="num">Conversations</th>
                <th className="num">Spans</th>
                {!agent && <th>Agents</th>}
                {metricKeys.map((k) => {
                  const i = k.indexOf('.')
                  return (
                    <th key={k} className="num" title={k}>
                      {i > 0 ? (
                        <>
                          <div className="faint small" style={{ fontWeight: 400 }}>
                            {k.slice(0, i)}
                          </div>
                          {k.slice(i + 1)}
                        </>
                      ) : (
                        k
                      )}
                    </th>
                  )
                })}
                <th className="num">Tokens</th>
                <th className="num">Cost</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {runs.map((r) => {
                const m = summaryMetrics(r.summary)
                return (
                  <tr key={r.call_id} className={`clickable ${sel.includes(r.call_id) ? 'selected' : ''}`} onClick={() => nav(compareEvaluationsUrl(projectId, [r.call_id]))} title="Open the evaluation">
                    <td onClick={(e) => e.stopPropagation()}>
                      <input type="checkbox" checked={sel.includes(r.call_id)} onChange={() => toggle(r.call_id)} aria-label={`Select run ${r.call_id}`} />
                    </td>
                    <td>
                      <EvalStatus status={r.status} />
                    </td>
                    <td style={{ maxWidth: 260 }}>
                      <div className="truncate" style={{ fontWeight: 500 }}>
                        <RefName refStr={r.evaluation_ref} name={r.evaluation_name || r.display_name || r.call_id.slice(-8)} />
                      </div>
                      <div className="small muted truncate">
                        <RefName refStr={r.model_ref} name={r.model_name} />
                      </div>
                    </td>
                    <td className="small" title={fmtDate(r.started_at)} style={{ whiteSpace: 'nowrap' }}>
                      {fmtRelative(r.started_at ?? r.first_seen)}
                    </td>
                    <td className="num">{fmtNumber(r.prediction_count)}</td>
                    <td className="num">{fmtNumber(r.conversation_count)}</td>
                    <td className="num">
                      {fmtNumber(r.span_count)}
                      {r.error_count > 0 && (
                        <span className="badge error" style={{ marginLeft: 6 }} title={`${r.error_count} failed spans`}>
                          {r.error_count}
                        </span>
                      )}
                    </td>
                    {!agent && (
                      <td className="small" style={{ maxWidth: 180 }}>
                        <div className="truncate" title={r.agent_names.join(', ')}>
                          {r.agent_names.join(', ') || <span className="faint">—</span>}
                        </div>
                      </td>
                    )}
                    {metricKeys.map((k) => (
                      <td key={k} className="num mono">
                        {k in m ? fmtScoreValue(k, m[k]) : <span className="faint">—</span>}
                      </td>
                    ))}
                    <td className="num muted">{compact(r.total_input_tokens + r.total_output_tokens)}</td>
                    <td className="num muted">{fmtCost(r.total_cost_usd)}</td>
                    <td onClick={(e) => e.stopPropagation()} style={{ whiteSpace: 'nowrap' }}>
                      <EvalAgentLinks projectId={projectId} runId={r.call_id} agent={agent} compact />
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      )}
      {(q.data?.total_count ?? 0) > LIMIT && (
        <div className="row" style={{ justifyContent: 'flex-end' }}>
          <Pager offset={offset} limit={LIMIT} total={q.data?.total_count ?? null} onChange={setOffset} />
        </div>
      )}
    </div>
  )
}

/** Banner over the Spans view when it is filtered to an evaluation run. */
export function EvalFilterBanner({ projectId, runId, pred, agent, onClear }: { projectId: string; runId: string; pred?: string; agent?: string; onClear: () => void }) {
  const q = useAgentEvalRuns(projectId, runId ? [runId] : [])
  const r = runId ? q.data?.get(runId) : undefined
  return (
    <div className="eval-filter-banner" role="status">
      <span className="badge accent">Evaluation</span>
      {runId ? (
        <span className="row wrap grow" style={{ gap: 6, minWidth: 0 }}>
          <b className="truncate">{r ? r.evaluation_name || r.display_name || runId : runId}</b>
          {r?.model_name && <span className="muted small truncate">· {r.model_name}</span>}
          {r && <EvalStatus status={r.status} />}
          {pred && (
            <Link to={callUrl(projectId, pred)} className="chip mono" title="The prediction (predict_and_score call)">
              prediction {pred.slice(-8)}
            </Link>
          )}
          {r && (
            <span className="small muted">
              {fmtNumber(r.span_count)} spans · {fmtNumber(r.conversation_count)} conversations · {fmtNumber(r.prediction_count)} predictions
            </span>
          )}
        </span>
      ) : (
        <span className="grow">
          prediction{' '}
          <Link to={callUrl(projectId, pred ?? '')} className="mono">
            {pred}
          </Link>
        </span>
      )}
      {runId && (
        <Link className="btn sm ghost" to={compareEvaluationsUrl(projectId, [runId])}>
          Open evaluation →
        </Link>
      )}
      {runId && agent === undefined && pred && (
        <Link className="btn sm ghost" to={evalSpansUrl(projectId, runId)}>
          Whole run
        </Link>
      )}
      <button className="btn sm ghost" onClick={onClear} aria-label="Remove evaluation filter">
        ✕
      </button>
    </div>
  )
}

/** Agent conversations a prediction recorded or linked, for the comparison's row detail. */
export function PredictionAgentLinks({ projectId, runId, pasId }: { projectId: string; runId: string; pasId: string }) {
  const q = useQuery({
    queryKey: ['agent-eval-pred-convs', projectId, pasId],
    queryFn: () => agentEvalsApi.conversations({ project_id: projectId, predict_and_score_call_id: pasId, limit: 20 }),
    staleTime: 30_000,
  })
  if (!q.data || q.data.span_count === 0) return null
  return (
    <div className="eval-agent-links small" aria-label="Agent spans of this prediction">
      <div className="row wrap" style={{ gap: 6 }}>
        <span className="muted small">Agent activity</span>
        <span className="small">
          {fmtNumber(q.data.span_count)} spans{q.data.total_count ? ` in ${q.data.total_count} conversation${q.data.total_count === 1 ? '' : 's'}` : ''}
        </span>
        {q.data.conversations.map((c) => (
          <Link key={c.conversation_id} to={conversationUrl(projectId, c.conversation_id)} className="chip mono" title={c.first_message?.text}>
            {truncate(c.conversation_names[0] || c.conversation_id, 28)}
          </Link>
        ))}
        <span className="grow" />
        <EvalAgentLinks projectId={projectId} runId={runId} pred={pasId} compact />
      </div>
    </div>
  )
}

/** Which evaluations and predictions a conversation belongs to. */
export function useConversationEvals(projectId: string, conversationId: string) {
  return useQuery({
    queryKey: ['agent-conv-evals', projectId, conversationId],
    queryFn: () => agentEvalsApi.query({ project_id: projectId, conversation_id: conversationId, include_predictions: true, limit: 20 }),
    staleTime: 30_000,
  })
}

function PredictionRow({ projectId, run, p }: { projectId: string; run: AgentEvalRun; p: AgentEvalPrediction }) {
  const meta = [p.example_id && `example ${p.example_id}`, p.trial_index != null && `trial ${p.trial_index}`, p.kind && p.kind !== 'agent' && p.kind].filter(Boolean) as string[]
  return (
    <div className="eval-pred-row">
      <div className="row wrap" style={{ gap: 6 }}>
        <Link to={callUrl(projectId, p.predict_and_score_call_id)} className="mono small" title="predict_and_score call">
          prediction {p.predict_and_score_call_id.slice(-8)}
        </Link>
        <EvalStatus status={p.status} />
        {meta.map((m) => (
          <span key={m} className="chip faint">
            {m}
          </span>
        ))}
        <span className="small faint">{fmtNumber(p.span_count)} spans here</span>
        <span className="grow" />
        <Link className="btn sm ghost" to={evalSpansUrl(projectId, run.call_id, { pred: p.predict_and_score_call_id })}>
          View spans
        </Link>
      </div>
      {p.example != null && (
        <div className="small muted truncate" title={preview(p.example, 400)}>
          <span className="faint">input </span>
          {preview(p.example, 200)}
        </div>
      )}
      <ScoreChips scores={p.scores} />
    </div>
  )
}

/** Card on the conversation page: the evaluations and predictions this conversation belongs to. */
export function ConversationEvals({ projectId, runs }: { projectId: string; runs: AgentEvalRun[] }) {
  if (!runs.length) return null
  return (
    <div className="card conv-evals" aria-label="Evaluations">
      <div className="card-header">
        <h3 className="grow">
          Part of {runs.length === 1 ? 'an evaluation' : `${runs.length} evaluations`}
        </h3>
      </div>
      <div className="card-body col" style={{ gap: 12 }}>
        {runs.map((r) => {
          const m = summaryMetrics(r.summary)
          const keys = sortMetricKeys(Object.keys(m)).slice(0, 4)
          return (
            <div key={r.call_id} className="col" style={{ gap: 6 }}>
              <div className="row wrap" style={{ gap: 8 }}>
                <b className="truncate">
                  <RefName refStr={r.evaluation_ref} name={r.evaluation_name || r.display_name || r.call_id.slice(-8)} />
                </b>
                <span className="muted small">
                  · <RefName refStr={r.model_ref} name={r.model_name} />
                </span>
                <EvalStatus status={r.status} />
                <span className="small faint" title={fmtDate(r.started_at)}>
                  {fmtRelative(r.started_at)}
                </span>
                {keys.map((k) => (
                  <span key={k} className="chip mono" title={`run summary ${k}`}>
                    {k}: {fmtScoreValue(k, m[k])}
                  </span>
                ))}
                <span className="grow" />
                <Link className="btn sm" to={compareEvaluationsUrl(projectId, [r.call_id])}>
                  Open evaluation
                </Link>
                <EvalAgentLinks projectId={projectId} runId={r.call_id} compact />
              </div>
              {(r.predictions ?? []).map((p) => (
                <PredictionRow key={p.predict_and_score_call_id} projectId={projectId} run={r} p={p} />
              ))}
            </div>
          )
        })}
      </div>
    </div>
  )
}

/** Small "eval" marker for a turn that belongs to a prediction. */
export function TurnEvalBadge({ projectId, items }: { projectId: string; items: { run: AgentEvalRun; pred: AgentEvalPrediction }[] }): ReactNode {
  if (!items.length) return null
  return (
    <>
      {items.map(({ run, pred }) => {
        const flat = flattenScores(pred.scores ?? {})
        const first = sortMetricKeys(Object.keys(flat))[0]
        return (
          <Link
            key={run.call_id + pred.predict_and_score_call_id}
            to={callUrl(projectId, pred.predict_and_score_call_id)}
            className="badge accent"
            title={`Prediction ${pred.predict_and_score_call_id} of ${run.evaluation_name || run.call_id}`}
          >
            eval: {truncate(run.evaluation_name || run.call_id, 24)}
            {first ? ` · ${fmtScoreValue(first, flat[first])}` : ''}
          </Link>
        )
      })}
    </>
  )
}
