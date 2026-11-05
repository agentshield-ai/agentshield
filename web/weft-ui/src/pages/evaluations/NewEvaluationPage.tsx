import { useEffect, useMemo, useState } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { useQueries, useQuery } from '@tanstack/react-query'
import { api } from '../../api/client'
import { parseRef } from '../../api/refs'
import type { Any, EvalStatus } from '../../api/types'
import { ErrorBox, Loading, Spinner } from '../../components/common'
import { Dropdown, MenuItem } from '../../components/traces/Menu'
import { PageHeader } from '../../components/ui/PageHeader'
import { Segmented } from '../../components/ui/Segmented'
import { Sentence } from '../../components/ui/Sentence'
import { plural } from '../../lib/summaries'
import { metricWords } from '../../lib/summaries/evaluate'
import './evaluate.css'
import { RefChip } from '../../components/RefChip'
import { CreateDatasetModal } from '../../components/objects/CreateDatasetModal'
import { columnsOf } from '../../components/objects/csv'
import { ModelFormModal, NameField, usePublish } from '../../components/objects/ObjectForms'
import { classVal, errMsg, isLLMModel, isServerScorerClass, KIND_META, refOf, templateVars, useObjects, valClass } from '../../components/objects/objectUtils'
import { ScorerPicker } from '../../components/evals/ScorerPicker'
import { RemoteModelModal, RuntimeModelModal } from '../../components/evals/ServerModelForms'
import { serverModelKind, serverModelSummary } from '../../components/evals/modelUtils'
import { useProjectId } from '../../hooks/useProject'
import { fmtRelative } from '../../lib/format'
import { spanUrl, compareEvaluationsUrl, evaluationsUrl, objectUrl } from '../../lib/urls'

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
      const ev = (await api.refsRead([evalRef!])).items[0]
      if (!ev) throw new Error('Evaluation not found')
      const scorerRefs: string[] = Array.isArray(ev.scorers) ? ev.scorers.filter((s: Any) => typeof s === 'string') : []
      const [dsVals, scorerVals] = await Promise.all([
        typeof ev.dataset === 'string' ? api.refsRead([ev.dataset]).then((r) => r.items) : Promise.resolve([null]),
        scorerRefs.length ? api.refsRead(scorerRefs).then((r) => r.items) : Promise.resolve([]),
      ])
      const ds = dsVals[0]
      let columns: string[] = []
      let rowCount: number | null = null
      const tp = typeof ds?.rows === 'string' ? parseRef(ds.rows) : null
      if (tp) {
        const [sample, stats] = await Promise.all([
          api.tableQuery({ project_id: tp.projectId, digest: tp.name, limit: 20 }),
          api.tableCount(tp.projectId, tp.name),
        ])
        columns = columnsOf(sample.items.map((r) => r.value))
        rowCount = stats
      }
      const scorers = scorerRefs.map((ref, i) => ({ ref, cls: valClass(scorerVals[i]), runnable: isServerScorerClass(valClass(scorerVals[i])) }))
      return { ev, ds, columns, rowCount, scorers }
    },
  })
}

export function NewEvaluationPage() {
  const projectId = useProjectId()
  const [params] = useSearchParams()
  const evals = useObjects(projectId, { kinds: ['evaluation'] })
  const models = useObjects(projectId, KIND_META.models.filter)
  // Models the server can run: LLM completion models, remote (predict_url) and runtime-bound models.
  const llmModels = (models.data ?? []).filter((o) => isLLMModel(o) || serverModelKind(o) != null)

  const [evalRef, setEvalRef] = useState<string | null>(params.get('evaluation'))
  const [mode, setMode] = useState<'existing' | 'new'>('existing')
  const [modelRefs, setModelRefs] = useState<string[]>(params.get('model') ? [params.get('model')!] : [])
  const [creatingModel, setCreatingModel] = useState<false | true | 'remote' | 'runtime'>(false)
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
    const opts = (evals.data ?? []).map((o) => ({ ref: refOf(o), label: `${o.name} (v${o.version}, latest)` }))
    if (evalRef && !opts.some((o) => o.ref === evalRef)) {
      const p = parseRef(evalRef)
      opts.unshift({ ref: evalRef, label: `${p?.name ?? evalRef}:${p?.digest?.slice(0, 8) ?? ''}` })
    }
    return opts
  }, [evals.data, evalRef])

  const modelOptions = useMemo(() => {
    const opts = llmModels.map((o) => ({ ref: refOf(o), name: o.name, value: o.value, kind: serverModelKind(o), sub: `${serverModelSummary(o)} · v${o.version} · ${fmtRelative(o.created_at)}` }))
    for (const r of modelRefs) if (!opts.some((o) => o.ref === r)) opts.unshift({ ref: r, name: parseRef(r)?.name ?? r, value: null, kind: null, sub: `pinned version ${parseRef(r)?.digest?.slice(0, 8) ?? ''}` })
    return opts
  }, [llmModels, modelRefs])

  const run = async () => {
    if (!evalRef) return
    setLaunching(true)
    const out: Launched[] = []
    for (const m of modelRefs) {
      try {
        const r = await api.evaluationRun(projectId, evalRef, m)
        out.push({ modelRef: m, callId: r.id })
      } catch (e) {
        out.push({ modelRef: m, error: errMsg(e) })
      }
    }
    setLaunched(out)
    setLaunching(false)
  }

  const canRun = mode === 'existing' && !!evalRef && modelRefs.length > 0 && !launching && nonRunnable.length === 0

  return (
    <div className="page-frame detail">
      <PageHeader
        variant="detail"
        back={{ to: evaluationsUrl(projectId), label: 'Evaluations' }}
        title="New evaluation"
        help="evaluations.md#evaluations"
        helpTopic="Evaluations"
        sentence={<Sentence size="detail" parts={[newEvalSentence(details.data ?? null, evalRef, modelRefs.length)]} />}
        meta="LLM completion models, remote models (a predict URL) and runtime models run on the server, with the built-in scorers."
      />
      <div className="ev-steps">
      <section className="ev-section" aria-labelledby="ne-1">
        <div className="ev-step-head">
          <h2 className="ev-step-h" id="ne-1">
            1. The evaluation
          </h2>
          <Segmented
            label="Evaluation"
            size="sm"
            options={evals.data?.length ? [{ id: 'existing', label: 'Use one' }, { id: 'new', label: 'Create one' }] : [{ id: 'new', label: 'Create one' }]}
            value={mode}
            onChange={setMode}
          />
        </div>
        <div className="ev-panel">
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
                <dl className="ev-facts">
                  <dt>Dataset</dt>
                  <dd>
                    {typeof details.data.ev.dataset === 'string' ? <RefChip refStr={details.data.ev.dataset} /> : '—'}
                    {details.data.rowCount != null && <span className="ev-note"> {details.data.rowCount} rows</span>}
                  </dd>
                  <dt>Columns</dt>
                  <dd className="mono">{details.data.columns.join(', ') || '—'}</dd>
                  <dt>Scorers</dt>
                  <dd>
                    {details.data.scorers.map((sc, i) => (
                      <span key={sc.ref}>
                        {i > 0 && ', '}
                        <RefChip refStr={sc.ref} />
                        {!sc.runnable && <span className="ev-error"> (the server cannot run it)</span>}
                      </span>
                    ))}
                    {details.data.scorers.length === 0 && 'none: only latency and tokens are measured'}
                  </dd>
                  <dt>Trials</dt>
                  <dd>{details.data.ev.trials ?? 1} per example</dd>
                </dl>
              )}
              {nonRunnable.length > 0 && (
                <div className="ev-note ev-error">
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
        </div>
      </section>

      <section className="ev-section" aria-labelledby="ne-2">
        <div className="ev-step-head">
          <h2 className="ev-step-h" id="ne-2">
            2. The models{modelRefs.length ? <span className="glist-count"> {modelRefs.length} chosen</span> : null}
          </h2>
          <Dropdown label="New model" className="qbtn" align="right">
            {(close) => (
              <>
                <MenuItem onClick={() => (setCreatingModel(true), close())}>An LLM model with a prompt template…</MenuItem>
                <MenuItem onClick={() => (setCreatingModel('remote'), close())}>A remote model (your HTTP endpoint)…</MenuItem>
                <MenuItem onClick={() => (setCreatingModel('runtime'), close())}>A model served by a custom runtime…</MenuItem>
              </>
            )}
          </Dropdown>
        </div>
        <div className="ev-panel">
        <ErrorBox error={models.error} />
        {models.isLoading ? (
          <Loading />
        ) : models.error ? null : modelOptions.length === 0 ? (
          <p className="ev-note">
            No server-runnable models yet.{' '}
            <button type="button" className="linkish" onClick={() => setCreatingModel(true)}>
              Create one
            </button>
          </p>
        ) : (
          <ul className="ev-pick-list" aria-label="Models">
            {modelOptions.map((m) => {
              const vars = m.value ? templateVars(...(m.value.default_params?.messages_template ?? []).map((x: Any) => (typeof x.content === 'string' ? x.content : ''))) : []
              const missing = details.data?.columns.length ? vars.filter((v) => !details.data!.columns.includes(v.split('.')[0])) : []
              return (
                <li key={m.ref}>
                  <label>
                    <input type="checkbox" checked={modelRefs.includes(m.ref)} onChange={() => setModelRefs(modelRefs.includes(m.ref) ? modelRefs.filter((x) => x !== m.ref) : [...modelRefs, m.ref])} />
                    <span className="ev-pick-text">
                      <span className="name">
                        {m.name}
                        {m.kind && m.kind !== 'llm' && <span className="ev-note"> · {m.kind} model</span>}
                      </span>
                      <span className="sub">{m.sub}</span>
                      {missing.length > 0 && <span className="sub ev-warn">Its template uses {missing.map((v) => `{${v}}`).join(' ')}, which the dataset does not have.</span>}
                    </span>
                  </label>
                </li>
              )
            })}
          </ul>
        )}
        </div>
      </section>
      <section className="ev-section" aria-labelledby="ne-3">
        <h2 className="ev-step-h" id="ne-3">
          3. Run
        </h2>
        <div className="pg2-actions">
          <button type="button" className="qbtn primary" disabled={!canRun} onClick={run}>
            {launching ? 'Starting…' : `Run ${modelRefs.length > 1 ? `${modelRefs.length} evaluations` : 'the evaluation'}`}
          </button>
          {!evalRef && <span className="ev-note">Choose or create an evaluation.</span>}
          {evalRef && modelRefs.length === 0 && <span className="ev-note">Choose at least one model.</span>}
        </div>
        {launched.length > 0 && <RunProgress projectId={projectId} launched={launched} />}
      </section>
      </div>
      {(creatingModel === 'remote' || creatingModel === 'runtime') &&
        (() => {
          const Form = creatingModel === 'remote' ? RemoteModelModal : RuntimeModelModal
          return (
            <Form
              projectId={projectId}
              onClose={() => setCreatingModel(false)}
              onCreated={(c) => {
                setCreatingModel(false)
                setModelRefs([...modelRefs, c.ref])
              }}
            />
          )
        })()}
      {creatingModel === true && (
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
              <option key={d.name} value={refOf(d)}>
                {d.name} (v{d.version})
              </option>
            ))}
          </select>
          <button type="button" className="qbtn" onClick={() => setUploading(true)}>
            New dataset
          </button>
        </div>
      </label>
      <div className="field">
        Scorers
        <ScorerPicker projectId={projectId} value={scorers} onChange={setScorers} />
      </div>
      <ErrorBox error={err} />
      <div className="row">
        <button
          type="button"
          className="qbtn"
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
        {scorers.length === 0 && <span className="ev-note">Without scorers only latency and tokens are measured.</span>}
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
      queryFn: () => api.evaluationStatus(projectId, l.callId!),
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
          <div key={l.modelRef} className="ev-progress-row">
            <div className="ev-progress-top">
              <Link to={objectUrl(projectId, p?.name ?? '', p?.digest ?? 'latest')}>{p?.name ?? l.modelRef}</Link>
              <span className="grow" />
              {l.error ? (
                <span className="ev-error">Did not start</span>
              ) : !s ? (
                <Spinner />
              ) : (
                <span className={s.code === 'failed' ? 'ev-error' : 'ev-note'}>
                  {s.completed_rows} of {s.total_rows} examples · {s.code === 'complete' ? 'finished' : s.code === 'failed' ? 'failed' : s.code === 'running' ? 'running' : s.code}
                </span>
              )}
              {l.callId && <Link to={spanUrl(projectId, l.callId)}>Raw run</Link>}
            </div>
            <div className="ev-meter" role="progressbar" aria-valuemin={0} aria-valuemax={100} aria-valuenow={l.error ? 0 : pct} aria-label={`${p?.name ?? 'Model'} progress`}>
              <span style={{ width: `${l.error ? 0 : pct}%` }} />
            </div>
            {(l.error || s?.error) && <div className="ev-note ev-error">{l.error ?? s?.error}</div>}
          </div>
        )
      })}
      <div className="pg2-actions">
        {done ? (
          ok.length > 0 ? (
            <Link className="qbtn" to={compareEvaluationsUrl(projectId, ok)}>
              {ok.length > 1 ? 'Compare the results' : 'See the results'}
            </Link>
          ) : (
            <span className="ev-note">No evaluation finished.</span>
          )
        ) : (
          <span className="ev-note">Running. You can leave this page; runs go on on the server.</span>
        )}
        <Link to={evaluationsUrl(projectId)}>All evaluations</Link>
      </div>
    </div>
  )
}

/** "support-eval runs 10 examples through each model you choose, scored by exact and similarity." */
function newEvalSentence(d: { ev: Any; rowCount: number | null; scorers: { ref: string }[] } | null, evalRef: string | null, models: number): string {
  if (!evalRef || !d) return 'Choose an evaluation (a dataset and its scorers), then the models to run on it.'
  const name = parseRef(evalRef)?.name ?? 'This evaluation'
  const rows = d.rowCount != null ? plural(d.rowCount, 'example') : 'every example'
  const sc = d.scorers.map((x) => metricWords(parseRef(x.ref)?.name ?? x.ref))
  const scored = sc.length ? `, scored by ${sc.length > 2 ? `${sc.slice(0, -1).join(', ')}, and ${sc[sc.length - 1]}` : sc.join(' and ')}` : ''
  const who = models === 0 ? 'each model you choose' : models === 1 ? 'the model you chose' : `the ${models} models you chose`
  return `${name} runs ${rows} through ${who}${scored}.`
}
