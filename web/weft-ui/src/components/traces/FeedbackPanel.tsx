import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '../../api/client'
import { callRef, objectRef, parseRef } from '../../api/refs'
import type { Any, Call, Feedback, ObjSchema } from '../../api/types'
import { fmtDate, fmtRelative, fmtScore } from '../../lib/format'
import { refUrl, objectUrl, settingsUrl } from '../../lib/urls'
import { Empty, ErrorBox, Loading, Section } from '../common'
import { RefChip } from '../RefChip'
import { ValueView } from '../ValueView'

export const REACTION_EMOJIS = ['👍', '👎', '😀', '🎉', '🤔', '👀']
const USER_KEY = 'weft.userId'

function myUserId(): string | null {
  try {
    return localStorage.getItem(USER_KEY)
  } catch {
    return null
  }
}
export function rememberUserId(id: string | null | undefined) {
  if (!id) return
  try {
    localStorage.setItem(USER_KEY, id)
  } catch {
    /* ignore */
  }
}
/** Whether a feedback row was created by this browser's user. */
export function isMine(f: Feedback): boolean {
  // The UI never sets `creator`; rows with a creator were written by someone else (SDK, scripts).
  if (f.creator) return false
  const me = myUserId()
  if (me) return f.wb_user_id === me
  // Before our first write we do not know our id; unauthenticated servers record "anonymous".
  return !f.wb_user_id || f.wb_user_id === 'anonymous'
}

export function useCallFeedback(projectId: string, callId: string) {
  const weaveRef = callRef(projectId, callId)
  return useQuery({
    queryKey: ['feedback', projectId, weaveRef],
    queryFn: () =>
      api.feedbackQuery({
        project_id: projectId,
        query: { $expr: { $eq: [{ $getField: 'weave_ref' }, { $literal: weaveRef }] } },
        sort_by: [{ field: 'created_at', direction: 'desc' }],
        limit: 1000,
      }),
  })
}

function useFeedbackMutations(projectId: string, callId: string) {
  const qc = useQueryClient()
  const weaveRef = callRef(projectId, callId)
  const invalidate = () => {
    void qc.invalidateQueries({ queryKey: ['feedback', projectId, weaveRef] })
    void qc.invalidateQueries({ queryKey: ['calls', projectId] })
  }
  const create = useMutation({
    mutationFn: (req: { feedback_type: string; payload: Any; annotation_ref?: string }) => api.feedbackCreate({ project_id: projectId, weave_ref: weaveRef, ...req }),
    onSuccess: (r) => {
      rememberUserId(r.wb_user_id)
      invalidate()
    },
  })
  const replace = useMutation({
    mutationFn: (req: { feedback_id: string; feedback_type: string; payload: Any; annotation_ref?: string }) =>
      api.feedbackReplace({ project_id: projectId, weave_ref: weaveRef, ...req }),
    onSuccess: invalidate,
  })
  const purge = useMutation({ mutationFn: (id: string) => api.feedbackPurge(projectId, id), onSuccess: invalidate })
  return { create, replace, purge }
}

// ---------------------------------------------------------------- reactions

export function Reactions({ projectId, callId, feedback }: { projectId: string; callId: string; feedback: Feedback[] }) {
  const { create, purge } = useFeedbackMutations(projectId, callId)
  const reactions = feedback.filter((f) => f.feedback_type === 'wandb.reaction.1')
  const emojis = [...new Set([...REACTION_EMOJIS, ...reactions.map((r) => r.payload?.emoji).filter(Boolean)])]
  const busy = create.isPending || purge.isPending
  return (
    <div className="row wrap" style={{ gap: 6 }}>
      {emojis.map((e) => {
        const rs = reactions.filter((r) => r.payload?.emoji === e)
        const mine = rs.find(isMine)
        return (
          <button
            key={e}
            className={`reaction ${mine ? 'active' : ''}`}
            disabled={busy}
            aria-pressed={!!mine}
            title={rs.length ? rs.map((r) => r.creator ?? r.wb_user_id ?? 'anonymous').join(', ') : `React with ${e}`}
            onClick={() => (mine ? purge.mutate(mine.id) : create.mutate({ feedback_type: 'wandb.reaction.1', payload: { emoji: e } }))}
          >
            <span>{e}</span>
            {rs.length > 0 && <span className="small">{rs.length}</span>}
          </button>
        )
      })}
      <ErrorBox error={create.error ?? purge.error} />
    </div>
  )
}

// ---------------------------------------------------------------- notes

function Notes({ projectId, callId, feedback }: { projectId: string; callId: string; feedback: Feedback[] }) {
  const { create, purge } = useFeedbackMutations(projectId, callId)
  const [text, setText] = useState('')
  const notes = feedback.filter((f) => f.feedback_type === 'wandb.note.1')
  const submit = () => {
    if (!text.trim()) return
    create.mutate({ feedback_type: 'wandb.note.1', payload: { note: text.trim() } }, { onSuccess: () => setText('') })
  }
  return (
    <div className="col" style={{ gap: 10 }}>
      {notes.length === 0 && <div className="faint small">No notes yet.</div>}
      {notes.map((n) => (
        <div key={n.id} className="note">
          <div className="row small faint">
            <span>{n.creator ?? n.wb_user_id ?? 'anonymous'}</span>
            <span title={fmtDate(n.created_at)}>{fmtRelative(n.created_at)}</span>
            <span className="grow" />
            <button className="btn ghost sm" onClick={() => purge.mutate(n.id)} disabled={purge.isPending} aria-label="Delete note">
              Delete
            </button>
          </div>
          <div style={{ whiteSpace: 'pre-wrap' }}>{String(n.payload?.note ?? '')}</div>
        </div>
      ))}
      <form
        className="col"
        onSubmit={(e) => {
          e.preventDefault()
          submit()
        }}
      >
        <textarea
          className="input"
          placeholder="Add a note… (Ctrl+Enter to save)"
          value={text}
          onChange={(e) => setText(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
              e.preventDefault()
              submit()
            }
          }}
        />
        <div className="row" style={{ justifyContent: 'flex-end' }}>
          <button className="btn sm primary" type="submit" disabled={!text.trim() || create.isPending}>
            Add note
          </button>
        </div>
      </form>
      <ErrorBox error={create.error ?? purge.error} />
    </div>
  )
}

// ---------------------------------------------------------------- annotations

interface FieldSchema {
  type?: string
  minimum?: number
  maximum?: number
  enum?: Any[]
  maxLength?: number
}

function specName(o: ObjSchema): string {
  return String(o.val?.name ?? o.object_id)
}

function specApplies(spec: ObjSchema, call: Call | undefined): boolean {
  const scope = spec.val?.op_scope
  if (!call || !Array.isArray(scope) || scope.length === 0) return true
  const base = (s: string) => parseRef(s)?.name ?? s
  return scope.some((s: string) => base(s) === base(call.op_name))
}

function AnnotationInput({ schema, value, onChange }: { schema: FieldSchema; value: Any; onChange: (v: Any) => void }) {
  const t = schema.type
  if (Array.isArray(schema.enum))
    return (
      <select className="input" value={value == null ? '' : String(value)} onChange={(e) => onChange(schema.enum!.find((x) => String(x) === e.target.value) ?? e.target.value)}>
        <option value="">—</option>
        {schema.enum.map((x) => (
          <option key={String(x)} value={String(x)}>
            {String(x)}
          </option>
        ))}
      </select>
    )
  if (t === 'boolean')
    return (
      <div className="row">
        {[true, false].map((b) => (
          <button key={String(b)} type="button" className={`btn sm ${value === b ? 'primary' : ''}`} aria-pressed={value === b} onClick={() => onChange(b)}>
            {b ? 'Yes' : 'No'}
          </button>
        ))}
      </div>
    )
  if (t === 'number' || t === 'integer') {
    const step = t === 'integer' ? 1 : (schema.maximum ?? 1) - (schema.minimum ?? 0) > 10 ? 1 : 0.1
    if (schema.minimum != null && schema.maximum != null)
      return (
        <div className="row">
          <input
            type="range"
            className="grow"
            min={schema.minimum}
            max={schema.maximum}
            step={step}
            value={value ?? schema.minimum}
            onChange={(e) => onChange(Number(e.target.value))}
            aria-label="Value"
          />
          <span className="mono small" style={{ minWidth: 32, textAlign: 'right' }}>
            {value ?? '—'}
          </span>
        </div>
      )
    return (
      <input
        className="input"
        type="number"
        step={step}
        min={schema.minimum}
        max={schema.maximum}
        value={value ?? ''}
        onChange={(e) => onChange(e.target.value === '' ? null : Number(e.target.value))}
      />
    )
  }
  return <textarea className="input" maxLength={schema.maxLength} value={value ?? ''} onChange={(e) => onChange(e.target.value)} style={{ minHeight: 54 }} />
}

function AnnotationRow({ projectId, callId, spec, feedback }: { projectId: string; callId: string; spec: ObjSchema; feedback: Feedback[] }) {
  const { create, replace, purge } = useFeedbackMutations(projectId, callId)
  const name = specName(spec)
  const type = `wandb.annotation.${name}`
  const schema: FieldSchema = spec.val?.field_schema ?? {}
  const unique = spec.val?.unique_among_creators !== false
  const rows = feedback.filter((f) => f.feedback_type === type)
  const mine = rows.find(isMine)
  const others = rows.filter((r) => r !== mine)
  const [draft, setDraft] = useState<Any>(mine?.payload?.value ?? null)
  useEffect(() => setDraft(mine?.payload?.value ?? null), [mine?.id, mine?.payload?.value])
  const dirty = draft !== (mine?.payload?.value ?? null)
  const annotationRef = objectRef(projectId, spec.object_id, spec.digest)
  const save = (v: Any) => {
    if (v == null || v === '') return
    const req = { feedback_type: type, payload: { value: v }, annotation_ref: annotationRef }
    if (unique && mine) replace.mutate({ feedback_id: mine.id, ...req })
    else create.mutate(req)
  }
  const instant = Array.isArray(schema.enum) || schema.type === 'boolean'
  const busy = create.isPending || replace.isPending || purge.isPending
  return (
    <div className="annotation">
      <div className="row">
        <Link to={objectUrl(projectId, spec.object_id, spec.digest)} className="mono small" title="Annotation spec">
          {name}
        </Link>
        <span className="faint small">{schema.type ?? 'string'}{Array.isArray(schema.enum) ? ' enum' : ''}{schema.minimum != null ? ` ${schema.minimum}–${schema.maximum ?? ''}` : ''}</span>
        <span className="grow" />
        {mine && (
          <button className="btn ghost sm" disabled={busy} onClick={() => purge.mutate(mine.id)}>
            Clear
          </button>
        )}
      </div>
      {spec.val?.description && <div className="faint small">{spec.val.description}</div>}
      <div className="row" style={{ alignItems: 'flex-start' }}>
        <div className="grow">
          <AnnotationInput
            schema={schema}
            value={draft}
            onChange={(v) => {
              setDraft(v)
              if (instant) save(v)
            }}
          />
        </div>
        {!instant && (
          <button className="btn sm primary" disabled={!dirty || busy || draft == null || draft === ''} onClick={() => save(draft)}>
            Save
          </button>
        )}
      </div>
      {others.length > 0 && (
        <div className="row wrap small" style={{ gap: 6 }}>
          <span className="faint">Others:</span>
          {others.map((o) => (
            <span key={o.id} className="badge" title={fmtDate(o.created_at)}>
              {fmtScore(o.payload?.value)} · {o.creator ?? o.wb_user_id ?? 'anonymous'}
            </span>
          ))}
        </div>
      )}
      <ErrorBox error={create.error ?? replace.error ?? purge.error} />
    </div>
  )
}

function Annotations({ projectId, call, feedback }: { projectId: string; call?: Call; feedback: Feedback[] }) {
  const specs = useQuery({
    queryKey: ['objs', projectId, 'AnnotationSpec', 'latest'],
    queryFn: () => api.objsQuery({ project_id: projectId, filter: { base_object_classes: ['AnnotationSpec'], latest_only: true }, limit: 200 }),
  })
  if (specs.isLoading) return <Loading />
  if (specs.error) return <ErrorBox error={specs.error} />
  const list = (specs.data?.objs ?? []).filter((s) => !s.deleted_at && specApplies(s, call))
  if (!list.length)
    return (
      <div className="faint small">
        No annotation specs in this project. <Link to={settingsUrl(projectId, 'annotations')}>Create one in Settings</Link> (or publish an{' '}
        <code>AnnotationSpec</code> object from the SDK) to add structured human feedback fields here.
      </div>
    )
  return (
    <div className="col" style={{ gap: 14 }}>
      {list.map((s) => (
        <AnnotationRow key={s.object_id} projectId={projectId} callId={call?.id ?? ''} spec={s} feedback={feedback} />
      ))}
    </div>
  )
}

// ---------------------------------------------------------------- panel

const KNOWN = (t: string) => t === 'wandb.reaction.1' || t === 'wandb.note.1' || t.startsWith('wandb.annotation.') || t.startsWith('wandb.runnable.')

export function FeedbackPanel({ projectId, call }: { projectId: string; call: Call }) {
  const fb = useCallFeedback(projectId, call.id)
  if (fb.isLoading) return <Loading />
  if (fb.error) return <ErrorBox error={fb.error} />
  const all = fb.data?.result ?? []
  const other = all.filter((f) => !KNOWN(f.feedback_type))
  return (
    <div className="col" style={{ gap: 14 }}>
      <Section title="Reactions">
        <Reactions projectId={projectId} callId={call.id} feedback={all} />
      </Section>
      <Section title="Annotations">
        <Annotations projectId={projectId} call={call} feedback={all} />
      </Section>
      <Section title="Notes">
        <Notes projectId={projectId} callId={call.id} feedback={all} />
      </Section>
      {other.length > 0 && (
        <Section title="Other feedback">
          <FeedbackTable projectId={projectId} rows={other} />
        </Section>
      )}
    </div>
  )
}

function FeedbackTable({ projectId, rows }: { projectId: string; rows: Feedback[] }) {
  return (
    <div className="table-wrap">
      <table className="table">
        <thead>
          <tr>
            <th>Type</th>
            <th>Payload</th>
            <th>Creator</th>
            <th>Created</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((f) => (
            <tr key={f.id}>
              <td className="mono">{f.feedback_type}</td>
              <td>
                <ValueView value={f.payload} projectId={projectId} />
              </td>
              <td>{f.creator ?? f.wb_user_id ?? '—'}</td>
              <td title={fmtDate(f.created_at)}>{fmtRelative(f.created_at)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

// ---------------------------------------------------------------- scores

const RUNNABLE_SCORERS = ['LLMAsAJudgeScorer', 'ExactMatchScorer', 'ContainsScorer', 'RegexScorer', 'JSONValidScorer', 'LevenshteinScorer']

/** Runs a server-runnable scorer on this call; the result lands as feedback. */
function RunScorerBar({ projectId, call }: { projectId: string; call: Call }) {
  const qc = useQueryClient()
  const scorers = useQuery({
    queryKey: ['objs', projectId, 'runnable-scorers'],
    queryFn: () =>
      api.objsQuery({ project_id: projectId, filter: { base_object_classes: ['Scorer'], leaf_object_classes: RUNNABLE_SCORERS, latest_only: true }, metadata_only: true }),
  })
  const [sel, setSel] = useState('')
  const run = useMutation({
    mutationFn: (ref: string) => api.callsScore(projectId, [call.id], [ref]),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ['feedback', projectId] }),
  })
  const list = scorers.data?.objs ?? []
  if (!scorers.isLoading && !list.length)
    return (
      <div className="small muted">
        Create an LLM judge or rule-based scorer under <Link to={`/${projectId}/scorers`}>Scorers</Link> to score calls on demand.
      </div>
    )
  return (
    <div className="col" style={{ gap: 6 }}>
      <div className="row wrap">
        <select className="input" value={sel} onChange={(e) => setSel(e.target.value)} aria-label="Scorer">
          <option value="">Choose a scorer…</option>
          {list.map((o) => (
            <option key={o.object_id} value={objectRef(o.project_id, o.object_id, o.digest)}>
              {o.object_id} ({o.leaf_object_class})
            </option>
          ))}
        </select>
        <button className="btn primary" disabled={!sel || run.isPending} onClick={() => run.mutate(sel)}>
          {run.isPending ? 'Scoring…' : 'Run scorer'}
        </button>
        {run.isSuccess && !run.data.results.some((r) => r.error) && <span className="small muted">Score added.</span>}
      </div>
      {run.data?.results.filter((r) => r.error).map((r) => <ErrorBox key={r.scorer} error={`${r.scorer}: ${r.error}`} />)}
      {run.error && <ErrorBox error={run.error} />}
    </div>
  )
}

export function ScoresPanel({ projectId, call }: { projectId: string; call: Call }) {
  return (
    <div className="col" style={{ gap: 14 }}>
      <RunScorerBar projectId={projectId} call={call} />
      <ScoresList projectId={projectId} call={call} />
    </div>
  )
}

function ScoresList({ projectId, call }: { projectId: string; call: Call }) {
  const fb = useCallFeedback(projectId, call.id)
  if (fb.isLoading) return <Loading />
  if (fb.error) return <ErrorBox error={fb.error} />
  const scores = (fb.data?.result ?? []).filter((f) => f.feedback_type.startsWith('wandb.runnable.'))
  const evalScores = call.output && typeof call.output === 'object' && call.output.scores && typeof call.output.scores === 'object' ? (call.output.scores as Record<string, Any>) : null
  if (!scores.length && !evalScores)
    return <Empty>No scores yet. Scores appear here when a monitor or an evaluation scorer runs on this call.</Empty>
  return (
    <div className="col" style={{ gap: 14 }}>
      {scores.length > 0 && (
        <div className="table-wrap">
          <table className="table">
            <thead>
              <tr>
                <th>Scorer</th>
                <th>Output</th>
                <th>Scorer call</th>
                <th>Source</th>
                <th>Created</th>
              </tr>
            </thead>
            <tbody>
              {scores.map((f) => {
                const scorerCallUrl = f.call_ref ? refUrl(f.call_ref) : null
                return (
                  <tr key={f.id}>
                    <td>
                      <div className="mono">{f.feedback_type.slice('wandb.runnable.'.length)}</div>
                      {f.runnable_ref && <RefChip refStr={f.runnable_ref} />}
                    </td>
                    <td style={{ minWidth: 200 }}>
                      <ScoreValue value={f.payload?.output} projectId={projectId} />
                    </td>
                    <td>{scorerCallUrl ? <Link to={scorerCallUrl}>View call</Link> : <span className="faint">—</span>}</td>
                    <td>{f.trigger_ref ? <RefChip refStr={f.trigger_ref} /> : <span className="faint">manual</span>}</td>
                    <td title={fmtDate(f.created_at)}>{fmtRelative(f.created_at)}</td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      )}
      {evalScores && (
        <Section title="Evaluation scores (from output.scores)">
          <ValueView value={evalScores} projectId={projectId} />
        </Section>
      )}
    </div>
  )
}

export function ScoreValue({ value, projectId }: { value: Any; projectId: string }) {
  if (typeof value === 'boolean') return <span className={`badge ${value ? 'success' : 'error'}`}>{String(value)}</span>
  if (value == null || typeof value !== 'object') return <span className="mono">{fmtScore(value)}</span>
  return <ValueView value={value} projectId={projectId} />
}
