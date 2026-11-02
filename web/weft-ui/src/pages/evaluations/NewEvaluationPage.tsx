import { useEffect, useMemo, useState } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { useQueries, useQuery } from '@tanstack/react-query'
import { api } from '../../api/client'
import { parseRef } from '../../api/refs'
import type { Any, EvalStatus } from '../../api/types'
import { ErrorBox, Loading, Section, Spinner } from '../../components/common'
import { RefChip } from '../../components/RefChip'
import { CreateDatasetModal } from '../../components/objects/CreateDatasetModal'
import { columnsOf } from '../../components/objects/csv'
import { ModelFormModal, NameField, usePublish } from '../../components/objects/ObjectForms'
import { classVal, errMsg, isLLMModel, isServerScorerClass, KIND_META, refOf, templateVars, useObjects, valClass } from '../../components/objects/objectUtils'
import { ScorerPicker } from '../../components/evals/ScorerPicker'
import { useProjectId } from '../../hooks/useProject'
import { fmtRelative } from '../../lib/format'
import { callUrl, compareEvaluationsUrl, evaluationsUrl, objectUrl } from '../../lib/urls'

interface Launched {
  modelRef: string
  callId?: string
  error?: string
}

/** Resolve an evaluation ref into its value, dataset value and a sample of dataset columns. */
function useEvaluationDetails(evalRef: string | null) {
  return useQuery({
    queryKey: ['eval-details', evalRef],
    enabled: !!evalRef,
    queryFn: async () => {
      const ev = (await api.refsReadBatch([evalRef!])).vals[0]
      if (!ev) throw new Error('Evaluation not found')
      const scorerRefs: string[] = Array.isArray(ev.scorers) ? ev.scorers.filter((s: Any) => typeof s === 'string') : []
      const [dsVals, scorerVals] = await Promise.all([
        typeof ev.dataset === 'string' ? api.refsReadBatch([ev.dataset]).then((r) => r.vals) : Promise.resolve([null]),
        scorerRefs.length ? api.refsReadBatch(scorerRefs).then((r) => r.vals) : Promise.resolve([]),
      ])
      const ds = dsVals[0]
      let columns: string[] = []
      let rowCount: number | null = null
      const tp = typeof ds?.rows === 'string' ? parseRef(ds.rows) : null
      if (tp) {
        const [sample, stats] = await Promise.all([
          api.tableQuery({ project_id: tp.projectId, digest: tp.name, limit: 20 }),
          api.tableQueryStats(tp.projectId, tp.name),
        ])
        columns = columnsOf(sample.rows.map((r) => r.val))
        rowCount = stats.count
      }
      const scorers = scorerRefs.map((ref, i) => ({ ref, cls: valClass(scorerVals[i]), runnable: isServerScorerClass(valClass(scorerVals[i])) }))
      return { ev, ds, columns, rowCount, scorers }
    },
  })
}

export function NewEvaluationPage() {
  const projectId = useProjectId()
  const [params] = useSearchParams()
  const evals = useObjects(projectId, { base_object_classes: ['Evaluation'] })
  const models = useObjects(projectId, KIND_META.models.filter)
  const llmModels = (models.data ?? []).filter(isLLMModel)

  const [evalRef, setEvalRef] = useState<string | null>(params.get('evaluation'))
  const [mode, setMode] = useState<'existing' | 'new'>('existing')
  const [modelRefs, setModelRefs] = useState<string[]>(params.get('model') ? [params.get('model')!] : [])
  const [creatingModel, setCreatingModel] = useState(false)
  const [launched, setLaunched] = useState<Launched[]>([])
  const [launching, setLaunching] = useState(false)

  // default to the most recent evaluation, or "new" when there are none
  useEffect(() => {
    if (evals.data && !evalRef) {
      if (evals.data.length) setEvalRef(refOf(evals.data[0]))
      else setMode('new')
    }
  }, [evals.data, evalRef])

  const details = useEvaluationDetails(mode === 'existing' ? evalRef : null)
  const nonRunnable = details.data?.scorers.filter((s) => !s.runnable) ?? []

  const evalOptions = useMemo(() => {
    const opts = (evals.data ?? []).map((o) => ({ ref: refOf(o), label: `${o.object_id} (v${o.version_index}, latest)` }))
    if (evalRef && !opts.some((o) => o.ref === evalRef)) {
      const p = parseRef(evalRef)
      opts.unshift({ ref: evalRef, label: `${p?.name ?? evalRef}:${p?.digest?.slice(0, 8) ?? ''}` })
    }
    return opts
  }, [evals.data, evalRef])

  const modelOptions = useMemo(() => {
    const opts = llmModels.map((o) => ({ ref: refOf(o), name: o.object_id, val: o.val, sub: `${o.val?.llm_model_id ?? ''} · v${o.version_index} · ${fmtRelative(o.created_at)}` }))
    for (const r of modelRefs) if (!opts.some((o) => o.ref === r)) opts.unshift({ ref: r, name: parseRef(r)?.name ?? r, val: null, sub: `pinned version ${parseRef(r)?.digest?.slice(0, 8) ?? ''}` })
    return opts
  }, [llmModels, modelRefs])

  const run = async () => {
    if (!evalRef) return
    setLaunching(true)
    const out: Launched[] = []
    for (const m of modelRefs) {
      try {
        const r = await api.evaluateModel(projectId, evalRef, m)
        out.push({ modelRef: m, callId: r.call_id })
      } catch (e) {
        out.push({ modelRef: m, error: errMsg(e) })
      }
    }
    setLaunched(out)
    setLaunching(false)
  }

  const canRun = mode === 'existing' && !!evalRef && modelRefs.length > 0 && !launching && nonRunnable.length === 0

  return (
    <div className="page" style={{ maxWidth: 1100 }}>
      <div className="breadcrumbs">
        <Link to={evaluationsUrl(projectId)}>Evaluations</Link>
        <span>/</span>
        <span>New</span>
      </div>
      <div className="page-header">
        <div className="col" style={{ gap: 2 }}>
          <h1>Run an evaluation</h1>
          <span className="muted small">The server runs each model over every dataset row and scores the outputs. Only LLM completion models and built-in scorers can run on the server.</span>
        </div>
      </div>

      <Section
        title="1 · Evaluation"
        actions={
          <div className="seg">
            <button className={mode === 'existing' ? 'on' : ''} onClick={() => setMode('existing')} disabled={!evals.data?.length}>
              Existing
            </button>
            <button className={mode === 'new' ? 'on' : ''} onClick={() => setMode('new')}>
              Create new
            </button>
          </div>
        }
      >
        <ErrorBox error={evals.error} />
        {mode === 'existing' ? (
          evals.isLoading ? (
            <Loading />
          ) : evals.error ? null : (
            <div className="col" style={{ gap: 10 }}>
              <select className="input" value={evalRef ?? ''} onChange={(e) => setEvalRef(e.target.value)} style={{ maxWidth: 480 }}>
                {evalOptions.map((o) => (
                  <option key={o.ref} value={o.ref}>
                    {o.label}
                  </option>
                ))}
              </select>
              <ErrorBox error={details.error} />
              {details.isLoading && <Spinner label="Loading evaluation…" />}
              {details.data && (
                <div className="kv">
                  <div className="k">dataset</div>
                  <div className="v row wrap">
                    {typeof details.data.ev.dataset === 'string' ? <RefChip refStr={details.data.ev.dataset} /> : '—'}
                    {details.data.rowCount != null && <span className="muted small">{details.data.rowCount} rows</span>}
                  </div>
                  <div className="k">columns</div>
                  <div className="v chips">
                    {details.data.columns.map((c) => (
                      <span key={c} className="chip mono">
                        {c}
                      </span>
                    ))}
                  </div>
                  <div className="k">scorers</div>
                  <div className="v chips">
                    {details.data.scorers.map((s) => (
                      <span key={s.ref} className="row" style={{ gap: 4 }}>
                        <RefChip refStr={s.ref} />
                        {!s.runnable && <span className="badge error">not server-runnable</span>}
                      </span>
                    ))}
                    {details.data.scorers.length === 0 && <span className="faint small">none</span>}
                  </div>
                  <div className="k">trials</div>
                  <div className="v">{details.data.ev.trials ?? 1}</div>
                </div>
              )}
              {nonRunnable.length > 0 && (
                <div className="error-box">
                  This evaluation uses scorers the server cannot run ({nonRunnable.map((s) => parseRef(s.ref)?.name).join(', ')}). Create a new evaluation with built-in
                  scorers, or run it from the SDK.
                </div>
              )}
            </div>
          )
        ) : (
          <NewEvaluationForm
            projectId={projectId}
            onCreated={(ref) => {
              setEvalRef(ref)
              setMode('existing')
            }}
          />
        )}
      </Section>

      <Section
        title={`2 · Models${modelRefs.length ? ` (${modelRefs.length} selected)` : ''}`}
        actions={
          <button className="btn sm" onClick={() => setCreatingModel(true)}>
            + New model
          </button>
        }
      >
        <ErrorBox error={models.error} />
        {models.isLoading ? (
          <Loading />
        ) : models.error ? null : modelOptions.length === 0 ? (
          <div className="faint">
            No LLM completion models yet.{' '}
            <button className="btn sm" onClick={() => setCreatingModel(true)}>
              Create one
            </button>
          </div>
        ) : (
          <div className="pick-list" style={{ maxHeight: 320 }}>
            {modelOptions.map((m) => {
              const vars = m.val ? templateVars(...(m.val.default_params?.messages_template ?? []).map((x: Any) => (typeof x.content === 'string' ? x.content : ''))) : []
              const missing = details.data?.columns.length ? vars.filter((v) => !details.data!.columns.includes(v.split('.')[0])) : []
              return (
                <label key={m.ref}>
                  <input type="checkbox" checked={modelRefs.includes(m.ref)} onChange={() => setModelRefs(modelRefs.includes(m.ref) ? modelRefs.filter((x) => x !== m.ref) : [...modelRefs, m.ref])} />
                  <span className="col grow" style={{ gap: 0, minWidth: 0 }}>
                    <span className="mono truncate">{m.name}</span>
                    <span className="hint truncate">{m.sub}</span>
                  </span>
                  {missing.length > 0 && (
                    <span className="badge warn" title="These template variables are not dataset columns">
                      missing {missing.map((v) => `{${v}}`).join(' ')}
                    </span>
                  )}
                </label>
              )
            })}
          </div>
        )}
      </Section>

      <Section title="3 · Run">
        <div className="col" style={{ gap: 12 }}>
          <div className="row wrap">
            <button className="btn primary" disabled={!canRun} onClick={run}>
              {launching ? 'Starting…' : `▷ Run ${modelRefs.length > 1 ? `${modelRefs.length} evaluations` : 'evaluation'}`}
            </button>
            {!evalRef && <span className="hint">Choose or create an evaluation.</span>}
            {evalRef && modelRefs.length === 0 && <span className="hint">Select at least one model.</span>}
          </div>
          {launched.length > 0 && <RunProgress projectId={projectId} launched={launched} />}
        </div>
      </Section>

      {creatingModel && (
        <ModelFormModal
          projectId={projectId}
          onClose={() => setCreatingModel(false)}
          onCreated={(c) => {
            setCreatingModel(false)
            setModelRefs([...modelRefs, c.ref])
          }}
        />
      )}
    </div>
  )
}

function NewEvaluationForm({ projectId, onCreated }: { projectId: string; onCreated: (ref: string) => void }) {
  const publish = usePublish(projectId)
  const datasets = useObjects(projectId, KIND_META.datasets.filter)
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [dataset, setDataset] = useState('')
  const [scorers, setScorers] = useState<string[]>([])
  const [trials, setTrials] = useState(1)
  const [uploading, setUploading] = useState(false)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<unknown>(null)
  const dsRef = dataset || (datasets.data?.[0] ? refOf(datasets.data[0]) : '')
  return (
    <div className="col" style={{ gap: 12 }}>
      <div className="form-grid">
        <NameField value={name} onChange={setName} placeholder="qa-eval" />
        <label className="field">
          Trials per row
          <input className="input" type="number" min={1} max={20} value={trials} onChange={(e) => setTrials(Math.max(1, Number(e.target.value) || 1))} />
        </label>
      </div>
      <label className="field">
        Description
        <input className="input" value={description} onChange={(e) => setDescription(e.target.value)} placeholder="Optional" />
      </label>
      <label className="field">
        Dataset
        <div className="row">
          <select className="input grow" value={dsRef} onChange={(e) => setDataset(e.target.value)}>
            {!datasets.data?.length && <option value="">No datasets</option>}
            {datasets.data?.map((d) => (
              <option key={d.object_id} value={refOf(d)}>
                {d.object_id} (v{d.version_index})
              </option>
            ))}
          </select>
          <button className="btn sm" onClick={() => setUploading(true)}>
            + New dataset
          </button>
        </div>
      </label>
      <div className="field" style={{ fontSize: 12, color: 'var(--text-2)' }}>
        Scorers
        <ScorerPicker projectId={projectId} value={scorers} onChange={setScorers} />
      </div>
      <ErrorBox error={err} />
      <div className="row">
        <button
          className="btn primary"
          disabled={!name || !dsRef || busy}
          onClick={async () => {
            setBusy(true)
            setErr(null)
            try {
              const c = await publish(
                name,
                classVal('Evaluation', [], {
                  name,
                  description: description || null,
                  dataset: dsRef,
                  scorers,
                  trials,
                }),
              )
              onCreated(c.ref)
            } catch (e) {
              setErr(e)
            } finally {
              setBusy(false)
            }
          }}
        >
          {busy ? 'Publishing…' : 'Create evaluation'}
        </button>
        {scorers.length === 0 && <span className="hint">Without scorers only latency and tokens are measured.</span>}
      </div>
      {uploading && (
        <CreateDatasetModal
          projectId={projectId}
          onClose={() => setUploading(false)}
          onCreated={(c) => {
            setUploading(false)
            setDataset(c.ref)
          }}
        />
      )}
    </div>
  )
}

function RunProgress({ projectId, launched }: { projectId: string; launched: Launched[] }) {
  const statuses = useQueries({
    queries: launched.map((l) => ({
      queryKey: ['eval-status', projectId, l.callId],
      enabled: !!l.callId,
      queryFn: () => api.evaluationStatus(projectId, l.callId!).then((r) => r.status),
      refetchInterval: (q: { state: { data?: EvalStatus } }) => (q.state.data && q.state.data.code !== 'running' ? false : 1000),
    })),
  })
  const done = statuses.every((s, i) => !launched[i].callId || (s.data && s.data.code !== 'running'))
  const ok = launched.filter((l, i) => l.callId && statuses[i].data?.code === 'complete').map((l) => l.callId!)
  return (
    <div className="col" style={{ gap: 10 }}>
      {launched.map((l, i) => {
        const s = statuses[i].data
        const pct = s && s.total_rows ? Math.round((s.completed_rows / s.total_rows) * 100) : s?.code === 'complete' ? 100 : 0
        const p = parseRef(l.modelRef)
        return (
          <div key={l.modelRef} className="col" style={{ gap: 4 }}>
            <div className="row small">
              <Link to={objectUrl(projectId, p?.name ?? '', p?.digest ?? 'latest')} className="mono">
                {p?.name ?? l.modelRef}
              </Link>
              <span className="grow" />
              {l.error ? (
                <span className="badge error">failed to start</span>
              ) : !s ? (
                <Spinner />
              ) : (
                <>
                  <span className="muted">
                    {s.completed_rows}/{s.total_rows} rows
                  </span>
                  <span className={`badge ${s.code === 'complete' ? 'success' : s.code === 'failed' ? 'error' : s.code === 'running' ? 'running' : ''}`}>{s.code}</span>
                </>
              )}
              {l.callId && <Link to={callUrl(projectId, l.callId)}>trace →</Link>}
            </div>
            <div className={`progress ${s?.code === 'complete' ? 'success' : s?.code === 'failed' || l.error ? 'error' : ''}`}>
              <span style={{ width: `${l.error ? 100 : pct}%` }} />
            </div>
            {(l.error || s?.error) && <div className="small" style={{ color: 'var(--error)' }}>{l.error ?? s?.error}</div>}
          </div>
        )
      })}
      <div className="row">
        {done ? (
          ok.length > 0 ? (
            <Link className="btn primary" to={compareEvaluationsUrl(projectId, ok)}>
              {ok.length > 1 ? 'Compare results →' : 'View results →'}
            </Link>
          ) : (
            <span className="muted small">No evaluation completed.</span>
          )
        ) : (
          <span className="muted small">Running… you can leave this page; runs continue on the server.</span>
        )}
        <Link className="btn ghost" to={evaluationsUrl(projectId)}>
          All evaluations
        </Link>
      </div>
    </div>
  )
}
