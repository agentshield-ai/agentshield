// Evaluations (DESIGN.md §7, §7.1 rule 1, §7.2): one sentence answering
// "which evaluations ran, and did things get better or worse?" (each change
// links to the comparison it describes), then the runs, newest first, each
// with one generated line. A run opens its page; ticking runs compares them.
// Search, the evaluation/model/status filters and saved views live in the URL.
import { useMemo, useState } from 'react'
import { Link, useNavigate, useSearchParams } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '../../api/client'
import type { Span, WeftObject } from '../../api/types'
import { ErrorBox, Modal } from '../../components/common'
import { Dropdown, MenuItem } from '../../components/traces/Menu'
import { slugify } from '../../components/traces/callUtils'
import { evalRunInfo, useDatasetSizes, useEvalRuns } from '../../components/evals/evalUtils'
import { useAgentEvalRuns } from '../../components/agents/AgentEvals'
import { GroupedList, ListRow } from '../../components/ui/GroupedList'
import { PageHeader } from '../../components/ui/PageHeader'
import { SearchField } from '../../components/ui/SearchField'
import { Sentence, type SentencePart } from '../../components/ui/Sentence'
import { StatusGlyph } from '../../components/ui/StatusGlyph'
import { useProjectId } from '../../hooks/useProject'
import { callCost, callStatus, fmtDate } from '../../lib/format'
import { evaluationSentenceParts, runInfoOf } from '../../components/evals/sentence'
import { previousRun, runGlyph, runLine, runMeta, type RunInfo } from '../../lib/summaries/evaluate'
import { relativeTime } from '../../lib/summaries'
import { compareEvaluationsUrl, newEvaluationUrl } from '../../lib/urls'
import './evaluate.css'

const PAGE = 100
const STATUSES = [
  { id: 'success', label: 'Finished' },
  { id: 'running', label: 'Running' },
  { id: 'error', label: 'Failed' },
  { id: 'descendant_error', label: 'Finished with errors' },
]

export function EvaluationsPage() {
  const projectId = useProjectId()
  const nav = useNavigate()
  const [sel, setSel] = useState<string[]>([])
  const [shown, setShown] = useState(PAGE)
  // Filters live in the URL so they can be saved as views and shared.
  const [params, setParams] = useSearchParams()
  const evalFilter = params.get('eval') ?? ''
  const modelFilter = params.get('model') ?? ''
  const statusFilter = params.get('status') ?? ''
  const search = params.get('q') ?? ''
  const anyFilter = !!(evalFilter || modelFilter || statusFilter)
  const [trayOpen, setTrayOpen] = useState(false)
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
  const [saving, setSaving] = useState(false)
  const views = useQuery({
    queryKey: ['objects', projectId, 'SavedView', 'latest'],
    queryFn: () => api.objectsQuery({ project_id: projectId, filter: { kinds: ['saved_view'], latest_only: true }, limit: 200 }),
  })
  const evalViews = (views.data?.items ?? []).filter((o) => !o.deleted_at && o.value?.view_type === 'evaluations')
  const activeView = evalViews.find((v) => v.name === params.get('view'))
  const loadView = (o: WeftObject) => setParams(new URLSearchParams({ ...(o.value?.definition?.ui_params ?? {}), view: o.name }))
  const uiParams = () => {
    const ui: Record<string, string> = {}
    params.forEach((v, k) => {
      if (k !== 'view') ui[k] = v
    })
    return ui
  }

  const runs = useEvalRuns(projectId, { limit: 500, pollWhileRunning: true })
  const all = useMemo(() => runs.data ?? [], [runs.data])
  const allIds = useMemo(() => all.map((c) => c.id), [all])
  const agentRuns = useAgentEvalRuns(projectId, allIds)
  const sizes = useDatasetSizes(all.map((c) => evalRunInfo(c).evalRef).filter(Boolean) as string[])

  const infos = useMemo(() => all.map((c) => ({ c, i: evalRunInfo(c) })), [all])
  const evalNames = useMemo(() => [...new Set(infos.map((x) => x.i.evalName))].sort(), [infos])
  const modelNames = useMemo(() => [...new Set(infos.map((x) => x.i.modelName))].sort(), [infos])
  // Every run, as the summaries read it (the previous run is found among all of them, not just the filtered ones).
  const runInfos = useMemo(() => all.map((c) => runInfoOf(c, sizes.data)), [all, sizes.data])
  const byId = useMemo(() => new Map(runInfos.map((r) => [r.id, r])), [runInfos])
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
  const filteredInfos = useMemo(() => filtered.map((c) => byId.get(c.id)!).filter(Boolean), [filtered, byId])
  const isFiltered = anyFilter || !!search.trim()

  const sentence: SentencePart[] = evaluationSentenceParts(filteredInfos, isFiltered, projectId, (status) => {
    setParam('status', status)
    setTrayOpen(true)
  })
  const toggle = (id: string) => setSel((s) => (s.includes(id) ? s.filter((x) => x !== id) : [...s, id]))
  const empty = !runs.isLoading && all.length === 0

  return (
    <div className="page-frame">
      <PageHeader
        title="Evaluations"
        help="evaluations.md#evaluations"
        controls={
          empty ? (
            <Link className="qbtn primary" to={newEvaluationUrl(projectId)}>
              New evaluation
            </Link>
          ) : (
            <>
              <SearchField label="Search runs" value={search} onChange={(v) => setParam('q', v)} filterInput placeholder="Search runs" />
              <button type="button" className={`qbtn ${anyFilter ? 'on' : ''}`} aria-expanded={trayOpen || anyFilter} onClick={() => setTrayOpen((o) => !o)}>
                Filter{anyFilter ? <span className="qbtn-count">{[evalFilter, modelFilter, statusFilter].filter(Boolean).length}</span> : null}
              </button>
              <Link className="qbtn primary" to={newEvaluationUrl(projectId)}>
                New evaluation
              </Link>
            </>
          )
        }
        sentence={
          runs.isLoading ? (
            <p className="sentence s-outcome sk-sentence" aria-busy="true">
              <span className="sr-only">Reading the runs…</span>
              <span className="sk sk-line" aria-hidden />
            </p>
          ) : empty ? undefined : (
            <Sentence parts={sentence} />
          )
        }
      />
      {(trayOpen || anyFilter) && !empty && (
        <div className="ev-tray" role="group" aria-label="Filter runs">
          <label>
            Evaluation
            <select className="input" value={evalFilter} onChange={(e) => setParam('eval', e.target.value)}>
              <option value="">Any</option>
              {evalNames.map((n) => (
                <option key={n}>{n}</option>
              ))}
            </select>
          </label>
          <label>
            Model
            <select className="input" value={modelFilter} onChange={(e) => setParam('model', e.target.value)}>
              <option value="">Any</option>
              {modelNames.map((n) => (
                <option key={n}>{n}</option>
              ))}
            </select>
          </label>
          <label>
            Status
            <select className="input" value={statusFilter} onChange={(e) => setParam('status', e.target.value)}>
              <option value="">Any</option>
              {STATUSES.map((s) => (
                <option key={s.id} value={s.id}>
                  {s.label}
                </option>
              ))}
            </select>
          </label>
          <Dropdown label={activeView ? `View: ${activeView.value?.label ?? activeView.name}` : 'Views'} className="qbtn">
            {(close) => (
              <>
                {evalViews.length === 0 && <div className="dd-note">No saved views yet.</div>}
                {evalViews.map((v) => (
                  <MenuItem
                    key={v.name}
                    onClick={() => {
                      loadView(v)
                      close()
                    }}
                  >
                    <span className="grow">{v.value?.label ?? v.name}</span>
                    {v.name === activeView?.name && <span className="faint">✓</span>}
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
          {anyFilter && (
            <button type="button" className="linkish" onClick={() => setParams(new URLSearchParams(search ? { q: search } : {}), { replace: true })}>
              Clear filters
            </button>
          )}
        </div>
      )}
      <ErrorBox error={runs.error} />
      {runs.isLoading ? (
        <RunsSkeleton />
      ) : empty ? (
        <div className="quiet-empty">
          <p>
            <strong>No evaluation runs yet.</strong> An evaluation runs a model over every example of a dataset and scores each answer; its runs, and whether each did better
            than the one before, appear here.
          </p>
          <p className="ev-note">
            Start one on the server with New evaluation, or from the SDK with <code>await evaluation.evaluate(model)</code>.
          </p>
        </div>
      ) : filtered.length === 0 ? (
        <div className="quiet-empty">
          <p>No runs match these filters.</p>
          <button type="button" className="qbtn" onClick={() => setParams(new URLSearchParams(), { replace: true })}>
            Clear filters
          </button>
        </div>
      ) : (
        <>
          {sel.length > 0 && (
            <div className="ev-selection" role="status">
              <span className="grow">
                {sel.length === 1 ? '1 run selected. Tick another to compare it with.' : `${sel.length} runs selected.`}
              </span>
              <button type="button" className="qbtn" onClick={() => nav(compareEvaluationsUrl(projectId, sel))}>
                {sel.length === 1 ? 'Open' : `Compare ${sel.length} runs`}
              </button>
              <button type="button" className="linkish" onClick={() => setSel([])}>
                Clear selection
              </button>
            </div>
          )}
          <GroupedList
            title={isFiltered ? 'Matching runs' : 'Runs'}
            count={filtered.length}
            footer={
              filtered.length > shown ? (
                <button type="button" className="qbtn ev-more" onClick={() => setShown((n) => n + PAGE)}>
                  Show {Math.min(PAGE, filtered.length - shown)} more of {filtered.length - shown}
                </button>
              ) : null
            }
          >
            {filtered.slice(0, shown).map((c) => (
              <RunRow
                key={c.id}
                projectId={projectId}
                c={c}
                run={byId.get(c.id)!}
                prev={previousRun(byId.get(c.id)!, runInfos)}
                agents={agentRuns.data?.get(c.id)?.conversation_count ?? 0}
                selected={sel.includes(c.id)}
                onToggle={() => toggle(c.id)}
              />
            ))}
          </GroupedList>
        </>
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

function RunRow({
  projectId,
  c,
  run,
  prev,
  agents,
  selected,
  onToggle,
}: {
  projectId: string
  c: Span
  run: RunInfo
  prev: RunInfo | null
  agents: number
  selected: boolean
  onToggle: () => void
}) {
  const g = runGlyph(run, prev)
  const meta = [runMeta(run, callCost(c)), agents ? `${agents} agent conversation${agents === 1 ? '' : 's'}` : ''].filter(Boolean).join(' · ')
  return (
    <ListRow
      className={`ev-run-row ${selected ? 'selected' : ''}`}
      glyph={<StatusGlyph kind={g.kind} label={g.label} />}
      title={run.evaluation}
      summary={runLine(run, prev)}
      meta={meta || undefined}
      time={relativeTime(c.started_at)}
      timeTitle={fmtDate(c.started_at)}
      to={compareEvaluationsUrl(projectId, [c.id])}
      trailing={
        <label className="ev-pick" title="Select to compare">
          <input type="checkbox" checked={selected} onChange={onToggle} aria-label={`Select the ${run.model} run of ${run.evaluation} (${relativeTime(c.started_at)}) to compare`} />
        </label>
      }
    />
  )
}

function RunsSkeleton() {
  return (
    <section className="glist" aria-busy="true" aria-label="Loading runs">
      <ul className="glist-box">
        {[0, 1, 2].map((i) => (
          <li key={i} className="lrow has-glyph">
            <div className="lrow-link static">
              <span className="sk sk-dot" aria-hidden />
              <span className="lrow-text">
                <span className="sk sk-line" style={{ width: '40%' }} aria-hidden />
                <span className="sk sk-line" style={{ width: '75%' }} aria-hidden />
              </span>
            </div>
          </li>
        ))}
      </ul>
    </section>
  )
}

function SaveEvalViewModal({ projectId, uiParams, onClose, onSaved }: { projectId: string; uiParams: Record<string, string>; onClose: () => void; onSaved: (id: string) => void }) {
  const [label, setLabel] = useState('')
  const qc = useQueryClient()
  const save = useMutation({
    mutationFn: async () => {
      const id = slugify(label)
      if (!id) throw new Error('Enter a name')
      await api.objectSave(
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
      void qc.invalidateQueries({ queryKey: ['objects', projectId, 'SavedView'] })
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
