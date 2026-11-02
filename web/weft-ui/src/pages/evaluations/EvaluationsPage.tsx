import { useMemo, useState } from 'react'
import { Link, useNavigate, useSearchParams } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '../../api/client'
import type { ObjSchema } from '../../api/types'
import { Empty, ErrorBox, Loading, Modal, Spinner } from '../../components/common'
import { Dropdown, MenuItem } from '../../components/traces/Menu'
import { slugify } from '../../components/traces/callUtils'
import { EvalRunsTable } from '../../components/evals/EvalRunsTable'
import { evalRunInfo, useEvalRuns } from '../../components/evals/evalUtils'
import { useAgentEvalRuns } from '../../components/agents/AgentEvals'
import { useProjectId } from '../../hooks/useProject'
import { callStatus } from '../../lib/format'
import { compareEvaluationsUrl, newEvaluationUrl } from '../../lib/urls'

export function EvaluationsPage() {
  const projectId = useProjectId()
  const nav = useNavigate()
  const [sel, setSel] = useState<string[]>([])
  // Filters live in the URL so they can be saved as views and shared.
  const [params, setParams] = useSearchParams()
  const evalFilter = params.get('eval') ?? ''
  const modelFilter = params.get('model') ?? ''
  const statusFilter = params.get('status') ?? ''
  const search = params.get('q') ?? ''
  const setParam = (k: string, v: string) =>
    setParams(
      (p) => {
        const n = new URLSearchParams(p)
        if (v) n.set(k, v)
        else n.delete(k)
        return n
      },
      { replace: true },
    )
  const setEvalFilter = (v: string) => setParam('eval', v)
  const setModelFilter = (v: string) => setParam('model', v)
  const setStatusFilter = (v: string) => setParam('status', v)
  const setSearch = (v: string) => setParam('q', v)
  const [saving, setSaving] = useState(false)
  const views = useQuery({
    queryKey: ['objs', projectId, 'SavedView', 'latest'],
    queryFn: () => api.objsQuery({ project_id: projectId, filter: { base_object_classes: ['SavedView'], latest_only: true }, limit: 200 }),
  })
  const evalViews = (views.data?.objs ?? []).filter((o) => !o.deleted_at && o.val?.view_type === 'evaluations')
  const activeView = evalViews.find((v) => v.object_id === params.get('view'))
  const loadView = (o: ObjSchema) => setParams(new URLSearchParams({ ...(o.val?.definition?.ui_params ?? {}), view: o.object_id }))
  const uiParams = () => {
    const ui: Record<string, string> = {}
    params.forEach((v, k) => {
      if (k !== 'view') ui[k] = v
    })
    return ui
  }
  const runs = useEvalRuns(projectId, { limit: 500, pollWhileRunning: true })
  const all = useMemo(() => runs.data ?? [], [runs.data])
  const anyRunning = all.some((c) => !c.ended_at)
  const allIds = useMemo(() => all.map((c) => c.id), [all])
  const agentRuns = useAgentEvalRuns(projectId, allIds)

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
        <Dropdown label={<>{activeView ? (activeView.val?.label ?? activeView.object_id) : 'Saved views'} ▾</>} className="btn sm">
          {(close) => (
            <>
              {evalViews.length === 0 && <div className="dd-note">No saved views yet.</div>}
              {evalViews.map((v) => (
                <MenuItem
                  key={v.object_id}
                  onClick={() => {
                    loadView(v)
                    close()
                  }}
                >
                  <span className="grow">{v.val?.label ?? v.object_id}</span>
                  {v.object_id === activeView?.object_id && <span className="faint">✓</span>}
                </MenuItem>
              ))}
              <div className="dd-sep" />
              <MenuItem
                onClick={() => {
                  setSaving(true)
                  close()
                }}
              >
                Save current view…
              </MenuItem>
              <MenuItem
                disabled={![...params.keys()].length}
                onClick={() => {
                  setParams(new URLSearchParams())
                  close()
                }}
              >
                Reset to default
              </MenuItem>
            </>
          )}
        </Dropdown>
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
        <EvalRunsTable projectId={projectId} calls={filtered} selected={sel} onSelect={setSel} agentRuns={agentRuns.data} />
      )}
      {saving && (
        <SaveEvalViewModal
          projectId={projectId}
          uiParams={uiParams()}
          onClose={() => setSaving(false)}
          onSaved={(id) => {
            setSaving(false)
            setParam('view', id)
          }}
        />
      )}
    </div>
  )
}

function SaveEvalViewModal({ projectId, uiParams, onClose, onSaved }: { projectId: string; uiParams: Record<string, string>; onClose: () => void; onSaved: (id: string) => void }) {
  const [label, setLabel] = useState('')
  const qc = useQueryClient()
  const save = useMutation({
    mutationFn: async () => {
      const id = slugify(label)
      if (!id) throw new Error('Enter a name')
      await api.objCreate(
        projectId,
        id,
        {
          _class_name: 'SavedView',
          _bases: ['Object', 'BaseModel'],
          name: id,
          view_type: 'evaluations',
          label: label.trim(),
          definition: {
            filter: { evaluation: uiParams.eval || undefined, model: uiParams.model || undefined, status: uiParams.status || undefined },
            search: uiParams.q || undefined,
            ui_params: uiParams,
          },
        },
        'SavedView',
      )
      return id
    },
    onSuccess: (id) => {
      void qc.invalidateQueries({ queryKey: ['objs', projectId, 'SavedView'] })
      onSaved(id)
    },
  })
  return (
    <Modal
      title="Save view"
      onClose={onClose}
      footer={
        <>
          <button className="btn" onClick={onClose}>
            Cancel
          </button>
          <button className="btn primary" disabled={!label.trim() || save.isPending} onClick={() => save.mutate()}>
            Save
          </button>
        </>
      }
    >
      <form
        onSubmit={(e) => {
          e.preventDefault()
          if (label.trim()) save.mutate()
        }}
      >
        <label className="field">
          Name
          <input className="input" autoFocus value={label} onChange={(e) => setLabel(e.target.value)} placeholder="e.g. QA eval, GPT models" />
        </label>
      </form>
      <div className="small muted">Saves the evaluation, model, status and search filters. Saving with an existing name creates a new version of that view.</div>
      <ErrorBox error={save.error} />
    </Modal>
  )
}
