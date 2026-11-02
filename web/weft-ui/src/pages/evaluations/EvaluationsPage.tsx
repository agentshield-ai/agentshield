import { useMemo, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { Empty, ErrorBox, Loading, Spinner } from '../../components/common'
import { EvalRunsTable } from '../../components/evals/EvalRunsTable'
import { evalRunInfo, useEvalRuns } from '../../components/evals/evalUtils'
import { useProjectId } from '../../hooks/useProject'
import { callStatus } from '../../lib/format'
import { compareEvaluationsUrl, newEvaluationUrl } from '../../lib/urls'

export function EvaluationsPage() {
  const projectId = useProjectId()
  const nav = useNavigate()
  const [sel, setSel] = useState<string[]>([])
  const [evalFilter, setEvalFilter] = useState('')
  const [modelFilter, setModelFilter] = useState('')
  const [statusFilter, setStatusFilter] = useState('')
  const [search, setSearch] = useState('')
  const runs = useEvalRuns(projectId, { limit: 500, pollWhileRunning: true })
  const all = useMemo(() => runs.data ?? [], [runs.data])
  const anyRunning = all.some((c) => !c.ended_at)

  const infos = useMemo(() => all.map((c) => ({ c, i: evalRunInfo(c) })), [all])
  const evalNames = useMemo(() => [...new Set(infos.map((x) => x.i.evalName))].sort(), [infos])
  const modelNames = useMemo(() => [...new Set(infos.map((x) => x.i.modelName))].sort(), [infos])
  const filtered = useMemo(() => {
    const q = search.trim().toLowerCase()
    return infos
      .filter(
        ({ c, i }) =>
          (!evalFilter || i.evalName === evalFilter) &&
          (!modelFilter || i.modelName === modelFilter) &&
          (!statusFilter || callStatus(c) === statusFilter) &&
          (!q || [i.evalName, i.modelName, c.display_name ?? '', c.id].some((s) => s.toLowerCase().includes(q))),
      )
      .map(({ c }) => c)
  }, [infos, evalFilter, modelFilter, statusFilter, search])

  return (
    <div className="page">
      <div className="page-header">
        <div className="col" style={{ gap: 2 }}>
          <h1>Evaluations</h1>
          <span className="muted small">Runs of an evaluation (dataset + scorers) against a model. Select runs to compare them.</span>
        </div>
        <span className="spacer" />
        {anyRunning && <Spinner label="Runs in progress" />}
        <button className="btn" disabled={sel.length < 1} onClick={() => nav(compareEvaluationsUrl(projectId, sel))}>
          Compare{sel.length ? ` (${sel.length})` : ''}
        </button>
        <Link className="btn primary" to={newEvaluationUrl(projectId)}>
          + New evaluation
        </Link>
      </div>
      {(runs.data?.length ?? 0) > 0 && (
        <div className="toolbar">
          <input className="input" style={{ width: 220, maxWidth: '100%' }} placeholder="Search runs…" value={search} onChange={(e) => setSearch(e.target.value)} />
          <select className="input" value={evalFilter} onChange={(e) => setEvalFilter(e.target.value)}>
            <option value="">All evaluations</option>
            {evalNames.map((n) => (
              <option key={n}>{n}</option>
            ))}
          </select>
          <select className="input" value={modelFilter} onChange={(e) => setModelFilter(e.target.value)}>
            <option value="">All models</option>
            {modelNames.map((n) => (
              <option key={n}>{n}</option>
            ))}
          </select>
          <select className="input" value={statusFilter} onChange={(e) => setStatusFilter(e.target.value)}>
            <option value="">Any status</option>
            <option value="success">Success</option>
            <option value="running">Running</option>
            <option value="error">Error</option>
            <option value="descendant_error">Child error</option>
          </select>
          <span className="grow" />
          <span className="muted small">
            {filtered.length} of {all.length} runs
            {sel.length ? ` · ${sel.length} selected` : ''}
          </span>
          {sel.length > 0 && (
            <button className="btn sm ghost" onClick={() => setSel([])}>
              Clear selection
            </button>
          )}
        </div>
      )}
      <ErrorBox error={runs.error} />
      {runs.isLoading ? (
        <Loading />
      ) : all.length === 0 ? (
        <div className="card">
          <Empty>
            <div className="col" style={{ alignItems: 'center', gap: 10 }}>
              <div style={{ maxWidth: 520 }}>
                No evaluation runs yet. Run one from the SDK with <code>await evaluation.evaluate(model)</code>, or run an LLM completion model against a dataset
                on the server.
              </div>
              <Link className="btn primary" to={newEvaluationUrl(projectId)}>
                + New evaluation
              </Link>
            </div>
          </Empty>
        </div>
      ) : filtered.length === 0 ? (
        <div className="card">
          <Empty>No runs match these filters.</Empty>
        </div>
      ) : (
        <EvalRunsTable projectId={projectId} calls={filtered} selected={sel} onSelect={setSel} />
      )}
    </div>
  )
}
