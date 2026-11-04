import { useMemo, useState } from 'react'
import { parseRef } from '../../api/refs'
import type { Any, WeftObject } from '../../api/types'
import { ErrorBox, Modal } from '../common'
import { NameField, usePublish, type Created } from '../objects/ObjectForms'
import { classVal, refOf, useObjects } from '../objects/objectUtils'
import { defaultLowerIsBetter, flattenScores, useEvalRuns } from './evalUtils'

export interface LBColumn {
  evaluation_object_ref: string
  scorer_name: string
  summary_metric_path: string
  should_minimize: boolean
}

function ColumnEditor({
  projectId,
  col,
  evalOptions,
  onChange,
  onRemove,
  index,
}: {
  projectId: string
  col: LBColumn
  evalOptions: { ref: string; label: string }[]
  onChange: (c: LBColumn) => void
  onRemove: () => void
  index: number
}) {
  const evalName = parseRef(col.evaluation_object_ref)?.name
  const runs = useEvalRuns(projectId, { evalName, limit: 25, enabled: !!evalName })
  // discovered "scorer -> metric paths" from the evaluation's recent summaries
  const discovered = useMemo(() => {
    const m = new Map<string, Set<string>>()
    for (const c of runs.data ?? []) {
      for (const key of Object.keys(flattenScores(c.output))) {
        const [scorer, ...rest] = key.split('.')
        if (!m.has(scorer)) m.set(scorer, new Set())
        if (rest.length) m.get(scorer)!.add(rest.join('.'))
      }
    }
    return m
  }, [runs.data])
  const scorers = [...discovered.keys()]
  const paths = [...(discovered.get(col.scorer_name) ?? [])]
  const listId = `lb-${index}`
  return (
    <div className="card" style={{ boxShadow: 'none' }}>
      <div className="card-body col" style={{ gap: 8 }}>
        <div className="row">
          <b className="small grow">Column {index + 1}</b>
          <button className="btn ghost sm danger" onClick={onRemove}>
            Remove
          </button>
        </div>
        <div className="form-grid">
          <label className="field">
            Evaluation
            <select className="input" value={col.evaluation_object_ref} onChange={(e) => onChange({ ...col, evaluation_object_ref: e.target.value })}>
              <option value="">Choose…</option>
              {evalOptions.map((o) => (
                <option key={o.ref} value={o.ref}>
                  {o.label}
                </option>
              ))}
            </select>
          </label>
          <label className="field">
            Scorer
            <input
              className="input mono"
              list={`${listId}-s`}
              value={col.scorer_name}
              onChange={(e) => {
                const s = e.target.value
                const ps = [...(discovered.get(s) ?? [])]
                onChange({ ...col, scorer_name: s, summary_metric_path: ps.includes(col.summary_metric_path) ? col.summary_metric_path : (ps[0] ?? ''), should_minimize: defaultLowerIsBetter(s) })
              }}
              placeholder="exact"
            />
            <datalist id={`${listId}-s`}>
              {scorers.map((s) => (
                <option key={s} value={s} />
              ))}
            </datalist>
          </label>
          <label className="field">
            Metric path
            <input className="input mono" list={`${listId}-p`} value={col.summary_metric_path} onChange={(e) => onChange({ ...col, summary_metric_path: e.target.value })} placeholder="true_fraction" />
            <datalist id={`${listId}-p`}>
              {paths.map((p) => (
                <option key={p} value={p} />
              ))}
            </datalist>
          </label>
          <label className="field">
            Direction
            <select className="input" value={col.should_minimize ? 'min' : 'max'} onChange={(e) => onChange({ ...col, should_minimize: e.target.value === 'min' })}>
              <option value="max">Higher is better</option>
              <option value="min">Lower is better</option>
            </select>
          </label>
        </div>
        {evalName && !runs.isLoading && scorers.length === 0 && <span className="hint">No finished runs of this evaluation yet, so metric paths can’t be suggested.</span>}
        {scorers.length > 0 && (
          <div className="chips">
            <span className="hint">Available:</span>
            {scorers.flatMap((s) =>
              [...(discovered.get(s) ?? [])].map((p) => (
                <button
                  key={`${s}.${p}`}
                  className={`chip selectable ${s === col.scorer_name && p === col.summary_metric_path ? 'on' : ''}`}
                  onClick={() => onChange({ ...col, scorer_name: s, summary_metric_path: p, should_minimize: defaultLowerIsBetter(`${s}.${p}`) })}
                >
                  {s}.{p}
                </button>
              )),
            )}
          </div>
        )}
      </div>
    </div>
  )
}

export function LeaderboardEditor({
  projectId,
  initial,
  onClose,
  onSaved,
}: {
  projectId: string
  initial?: WeftObject
  onClose: () => void
  onSaved: (c: Created) => void
}) {
  const publish = usePublish(projectId)
  const evals = useObjects(projectId, { kinds: ['evaluation'] })
  const [name, setName] = useState(initial?.name ?? '')
  const [description, setDescription] = useState<string>(initial?.value?.description ?? '')
  const [columns, setColumns] = useState<LBColumn[]>(
    Array.isArray(initial?.value?.columns)
      ? initial!.value.columns.map((c: Any) => ({
          evaluation_object_ref: c.evaluation_object_ref ?? '',
          scorer_name: c.scorer_name ?? '',
          summary_metric_path: c.summary_metric_path ?? '',
          should_minimize: !!c.should_minimize,
        }))
      : [],
  )
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<unknown>(null)
  const evalOptions = useMemo(() => {
    const opts = (evals.data ?? []).map((o) => ({ ref: refOf(o), label: `${o.name} (latest v${o.version})` }))
    for (const c of columns)
      if (c.evaluation_object_ref && !opts.some((o) => o.ref === c.evaluation_object_ref))
        opts.push({ ref: c.evaluation_object_ref, label: `${parseRef(c.evaluation_object_ref)?.name ?? c.evaluation_object_ref} (${parseRef(c.evaluation_object_ref)?.digest?.slice(0, 6) ?? ''})` })
    return opts
  }, [evals.data, columns])
  const valid = name && columns.length > 0 && columns.every((c) => c.evaluation_object_ref && c.scorer_name)
  return (
    <Modal
      title={initial ? `Edit leaderboard ${initial.name}` : 'New leaderboard'}
      wide
      onClose={onClose}
      footer={
        <>
          <button className="btn" onClick={onClose}>
            Cancel
          </button>
          <button
            className="btn primary"
            disabled={!valid || busy}
            onClick={async () => {
              setBusy(true)
              setErr(null)
              try {
                onSaved(await publish(name, { ...(initial?.value ?? {}), ...classVal('Leaderboard', [], { name, description, columns }) }))
              } catch (e) {
                setErr(e)
              } finally {
                setBusy(false)
              }
            }}
          >
            {busy ? 'Saving…' : 'Save leaderboard'}
          </button>
        </>
      }
    >
      <div className="form-grid">
        <NameField value={name} onChange={setName} locked={!!initial} placeholder="qa-leaderboard" />
        <label className="field">
          Description
          <input className="input" value={description} onChange={(e) => setDescription(e.target.value)} placeholder="Optional" />
        </label>
      </div>
      <div className="hint">Each column shows one summary metric of one evaluation. Rows are models; each cell uses that model’s latest finished run.</div>
      {columns.map((c, i) => (
        <ColumnEditor
          key={i}
          index={i}
          projectId={projectId}
          col={c}
          evalOptions={evalOptions}
          onChange={(nc) => setColumns(columns.map((x, j) => (j === i ? nc : x)))}
          onRemove={() => setColumns(columns.filter((_, j) => j !== i))}
        />
      ))}
      <div>
        <button
          className="btn sm"
          onClick={() =>
            setColumns([
              ...columns,
              { evaluation_object_ref: columns[columns.length - 1]?.evaluation_object_ref ?? evalOptions[0]?.ref ?? '', scorer_name: '', summary_metric_path: '', should_minimize: false },
            ])
          }
        >
          + Add column
        </button>
      </div>
      <ErrorBox error={err} />
    </Modal>
  )
}
