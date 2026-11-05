import { useCallback, useEffect, useMemo, useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '../../api/client'
import { queuesApi, type AnnotationQueueItem, type AnnotationState } from '../../api/queues'
import { spanRef, spanDisplayName, parseRef } from '../../api/refs'
import type { Any, Span, Feedback } from '../../api/types'
import { ErrorBox, Loading, Spinner } from '../../components/common'
import { queuesUrl } from '../../components/queues/AddToQueueModal'
import { ExportQueueModal } from '../../components/queues/ExportQueueModal'
import { annotationType, displayFieldValues, nextPendingIndex, progressCounts } from '../../components/queues/queueUtils'
import { CallChatView, extractChat } from '../../components/traces/ChatView'
import { AnnotationInput, isMine, rememberUserId, useCallFeedback, type FieldSchema } from '../../components/traces/FeedbackPanel'
import { ValueView } from '../../components/ValueView'
import { useProjectId } from '../../hooks/useProject'
import { callStatus, fmtDate, callLatencyMs } from '../../lib/format'
import { spanUrl, objectUrl } from '../../lib/urls'
import { undoable } from '../../components/ui/Toasts'
import { Inspector, InspectorDivider } from '../../components/ui/Inspector'
import { PageHeader } from '../../components/ui/PageHeader'
import { Segmented } from '../../components/ui/Segmented'
import { Sentence } from '../../components/ui/Sentence'
import { StatusGlyph } from '../../components/ui/StatusGlyph'
import { relativeTime, shortDuration } from '../../lib/summaries'
import { reviewSentence } from '../../lib/summaries/evaluate'
import '../evaluations/evaluate.css'

interface QueueSpec {
  ref: string
  name: string
  description?: string
  schema: FieldSchema
  unique: boolean
}

const STATE_LABEL: Record<AnnotationState, string> = { unstarted: 'Not started', in_progress: 'In progress', completed: 'Done', skipped: 'Skipped' }
const STATE_GLYPH: Record<AnnotationState, 'unknown' | 'running' | 'check' | 'warning'> = {
  unstarted: 'unknown',
  in_progress: 'running',
  completed: 'check',
  skipped: 'warning',
}

const isTyping = (t: EventTarget | null) => {
  const el = t as HTMLElement | null
  return !!el && (el.tagName === 'INPUT' || el.tagName === 'TEXTAREA' || el.tagName === 'SELECT' || el.isContentEditable)
}

const hasValue = (v: Any) => v !== null && v !== undefined && v !== ''
const NO_SPECS: QueueSpec[] = []
const NO_FEEDBACK: Feedback[] = []

export function QueueReviewPage() {
  const projectId = useProjectId()
  const { queueId = '' } = useParams()
  const qc = useQueryClient()
  const queue = useQuery({ queryKey: ['annotation-queue', projectId, queueId], queryFn: () => queuesApi.read(projectId, queueId).then((r) => r.queue) })
  const specs = useQuery({
    queryKey: ['annotation-queue-specs', projectId, queue.data?.scorer_refs],
    enabled: !!queue.data,
    queryFn: async (): Promise<QueueSpec[]> => {
      const refs = queue.data!.scorer_refs
      if (!refs.length) return []
      const { items } = await api.refsRead(refs)
      return refs.map((ref, i) => ({
        ref,
        name: String(items[i]?.name ?? parseRef(ref)?.name ?? ref),
        description: items[i]?.description ?? undefined,
        schema: (items[i]?.field_schema ?? {}) as FieldSchema,
        unique: items[i]?.unique_among_creators !== false,
      }))
    },
  })
  const itemsKey = ['annotation-queue-items', projectId, queueId]
  const items = useQuery({ queryKey: itemsKey, queryFn: () => queuesApi.items(projectId, queueId, { limit: 10000 }).then((r) => r.items) })
  // Items removed in this session, hidden at once; the delete commits after the undo window.
  const [hidden, setHidden] = useState<ReadonlySet<string>>(new Set())
  const list = useMemo(() => (items.data ?? []).filter((x) => !hidden.has(x.id)), [items.data, hidden])
  const [idx, setIdx] = useState<number | null>(null)
  const [exporting, setExporting] = useState(false)
  useEffect(() => {
    if (idx === null && items.data) {
      const first = nextPendingIndex(items.data, -1)
      setIdx(first >= 0 ? first : 0)
    }
  }, [items.data, idx])
  useEffect(() => {
    if (idx !== null && list.length && idx >= list.length) setIdx(list.length - 1)
  }, [idx, list.length])
  const cur = idx !== null ? list[idx] : undefined
  const removeItem = useCallback(
    (item: AnnotationQueueItem) =>
      undoable({
        message: `Removed “${spanDisplayName(item.span_name)}” from the queue`,
        apply: () => setHidden((h) => new Set(h).add(item.id)),
        revert: () =>
          setHidden((h) => {
            const n = new Set(h)
            n.delete(item.id)
            return n
          }),
        commit: async () => {
          await queuesApi.removeItem(projectId, queueId, item.id)
          await qc.invalidateQueries({ queryKey: itemsKey })
          void qc.invalidateQueries({ queryKey: ['annotation-queue-stats', projectId] })
        },
      }),
    [projectId, queueId, qc],
  )
  const progress = progressCounts(list)

  const setLocalState = useCallback(
    (item: AnnotationQueueItem) => qc.setQueryData<AnnotationQueueItem[]>(itemsKey, (old) => old?.map((x) => (x.id === item.id ? { ...x, ...item } : x))),
    [qc, projectId, queueId],
  )
  const setState = useCallback(
    async (item: AnnotationQueueItem, state: AnnotationState) => {
      const r = await queuesApi.progress(projectId, queueId, item.id, state)
      setLocalState({ ...r.item, position_in_queue: item.position_in_queue })
      void qc.invalidateQueries({ queryKey: ['annotation-queue-stats', projectId] })
      return r.item
    },
    [projectId, queueId, qc, setLocalState],
  )

  // Opening an unstarted item marks it in progress for this reviewer.
  useEffect(() => {
    if (cur && cur.annotation_state === 'unstarted') void setState(cur, 'in_progress').catch(() => undefined)
  }, [cur?.id])

  const goNext = useCallback(() => setIdx((i) => (i === null || !list.length ? i : Math.min(list.length - 1, i + 1))), [list.length])
  const goPrev = useCallback(() => setIdx((i) => (i === null ? i : Math.max(0, i - 1))), [])
  const goNextPending = useCallback(
    (from: number, after?: AnnotationQueueItem[]) => {
      const n = nextPendingIndex(after ?? list, from)
      setIdx(n >= 0 ? n : from)
    },
    [list],
  )

  const back = { to: queuesUrl(projectId), label: 'Review queues' }
  if (queue.isLoading || items.isLoading) return <Loading />
  if (queue.error || items.error)
    return (
      <div className="page-frame detail">
        <PageHeader variant="detail" back={back} title="Review queue" help="evaluations.md#review-queues" helpTopic="Review queues" />
        <ErrorBox error={queue.error ?? items.error} />
      </div>
    )
  const q = queue.data!
  const allDone = list.length > 0 && progress.remaining === 0
  const sentence = reviewSentence({ position: cur ? (idx ?? 0) + 1 : null, total: progress.total, completed: progress.completed, skipped: progress.skipped })

  return (
    <div className="page-frame detail">
      <PageHeader
        variant="detail"
        back={back}
        title={q.name}
        help="evaluations.md#review-queues"
        helpTopic="Review queues"
        sentence={
          <Sentence
            size="detail"
            parts={allDone ? [sentence, ' ', { text: 'Export the annotations to a dataset', onClick: () => setExporting(true) }, '.'] : [sentence]}
          />
        }
        meta={q.description || undefined}
        controls={
          list.length > 0 && !allDone ? (
            <button type="button" className="qbtn" onClick={() => setExporting(true)}>
              Export to a dataset
            </button>
          ) : undefined
        }
      />
      {!list.length ? (
        <div className="quiet-empty">
          <p>This queue is empty.</p>
          <p className="ev-note">Add steps from the Traces page (select rows, then Add to queue) or from a step’s page.</p>
        </div>
      ) : cur ? (
        <div className="rv-layout">
          <CallPane projectId={projectId} item={cur} />
          <div className="rv-side">
            <AnnotationPane
              key={cur.id}
              projectId={projectId}
              queueId={queueId}
              item={cur}
              specs={specs.data ?? NO_SPECS}
              specsLoading={specs.isLoading}
              isFirst={idx === 0}
              isLast={idx === list.length - 1}
              onPrev={goPrev}
              onNext={goNext}
              onSkip={async () => {
                const it = await setState(cur, 'skipped')
                const after = list.map((x) => (x.id === it.id ? { ...x, annotation_state: it.annotation_state } : x))
                goNextPending(idx!, after)
              }}
              onSubmitted={async () => {
                const it = await setState(cur, 'completed')
                const after = list.map((x) => (x.id === it.id ? { ...x, annotation_state: it.annotation_state } : x))
                goNextPending(idx!, after)
              }}
            />
            <details className="ev-more-details">
              <summary>
                All items <span className="quiet">{list.length}</span>
              </summary>
              <ItemList items={list} current={idx ?? 0} onPick={setIdx} onRemove={removeItem} />
            </details>
          </div>
        </div>
      ) : (
        <Spinner />
      )}
      {exporting && <ExportQueueModal projectId={projectId} queue={q} onClose={() => setExporting(false)} />}
    </div>
  )
}

function CallPane({ projectId, item }: { projectId: string; item: AnnotationQueueItem }) {
  const call = useQuery({ queryKey: ['span', projectId, item.span_id], queryFn: () => api.spanFind(projectId, item.span_id) })
  if (call.isLoading) return <div className="rv-item"><Loading label="Loading the step…" /></div>
  if (call.error) return <div className="rv-item"><ErrorBox error={call.error} /></div>
  const c = call.data as Span | null
  if (!c)
    return (
      <div className="rv-item">
        <div className="quiet-empty">
          <p>This step no longer exists.</p>
        </div>
      </div>
    )
  const chat = extractChat(c)
  const fields = item.display_fields.length ? displayFieldValues(c, item.display_fields) : []
  const st = callStatus(c)
  return (
    <section className="rv-item" aria-label="The item to review">
      <div className="rv-head-line">
        <StatusGlyph kind={st === 'error' ? 'failed' : st === 'running' ? 'running' : 'ok'} size={18} label={st === 'error' ? 'Failed' : st === 'running' ? 'Running' : 'Finished'} />
        <span className="title">{spanDisplayName(c.name)}</span>
        <span title={fmtDate(c.started_at)}>{relativeTime(c.started_at)}</span>
        <span>took {shortDuration(callLatencyMs(c))}</span>
        <Link to={spanUrl(projectId, c.id)} target="_blank">
          Open the step
        </Link>
      </div>
      {c.error && <p className="rv-error">{c.error}</p>}
      {fields.length > 0 && (
        <div className="ev-panel">
          {fields.map((f) => (
            <div key={f.path} className="rv-block">
              <h3 className="rv-block-h">{f.path}</h3>
              <ValueView value={f.value} projectId={projectId} />
            </div>
          ))}
        </div>
      )}
      {chat ? (
        <div className="ev-panel">
          <h3 className="rv-block-h">Conversation</h3>
          <CallChatView chat={chat} projectId={projectId} />
        </div>
      ) : (
        <div className="ev-panel">
          <div className="rv-block">
            <h3 className="rv-block-h">Input</h3>
            <ValueView value={Object.fromEntries(Object.entries(c.inputs ?? {}).filter(([k]) => k !== 'self'))} projectId={projectId} />
          </div>
          <InspectorDivider />
          <div className="rv-block">
            <h3 className="rv-block-h">Output</h3>
            <ValueView value={c.output} projectId={projectId} markdown />
          </div>
        </div>
      )}
    </section>
  )
}

function Kbd({ children }: { children: string }) {
  return <kbd>{children}</kbd>
}

function AnnotationPane({
  projectId,
  queueId,
  item,
  specs,
  specsLoading,
  isFirst,
  isLast,
  onPrev,
  onNext,
  onSkip,
  onSubmitted,
}: {
  projectId: string
  queueId: string
  item: AnnotationQueueItem
  specs: QueueSpec[]
  specsLoading: boolean
  isFirst: boolean
  isLast: boolean
  onPrev: () => void
  onNext: () => void
  onSkip: () => Promise<void>
  onSubmitted: () => Promise<void>
}) {
  const qc = useQueryClient()
  const fb = useCallFeedback(projectId, item.span_id)
  const rows: Feedback[] = fb.data?.items ?? NO_FEEDBACK
  const mineBySpec = useMemo(() => {
    const m = new Map<string, Feedback>()
    for (const s of specs) {
      const f = rows.find((r) => r.feedback_type === annotationType(s.name) && isMine(r))
      if (f) m.set(s.name, f)
    }
    return m
  }, [rows, specs])
  const [drafts, setDrafts] = useState<Record<string, Any>>({})
  const [touched, setTouched] = useState(false)
  useEffect(() => {
    if (touched) return
    const d: Record<string, Any> = {}
    for (const s of specs) d[s.name] = mineBySpec.get(s.name)?.payload?.value ?? null
    setDrafts(d)
  }, [mineBySpec, specs, touched])
  const setDraft = (name: string, v: Any) => {
    setTouched(true)
    setDrafts((d) => ({ ...d, [name]: v }))
  }

  const submit = useMutation({
    mutationFn: async () => {
      const ref = spanRef(projectId, item.span_id)
      for (const s of specs) {
        const v = drafts[s.name]
        const mine = mineBySpec.get(s.name)
        if (!hasValue(v) || (mine && mine.payload?.value === v)) continue
        const r = await queuesApi.annotate({
          project_id: projectId,
          ref,
          feedback_type: annotationType(s.name),
          payload: { value: v },
          annotation_ref: s.ref,
          queue_id: queueId,
          replace_id: s.unique && mine ? mine.id : undefined,
        })
        rememberUserId(r.user_id)
      }
      await qc.invalidateQueries({ queryKey: ['feedback', projectId, ref] })
      await onSubmitted()
    },
  })
  const skip = useMutation({ mutationFn: onSkip })
  const busy = submit.isPending || skip.isPending

  // Keyboard shortcuts.
  useEffect(() => {
    const h = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key === 'Enter') {
        e.preventDefault()
        if (!busy) submit.mutate()
        return
      }
      if (isTyping(e.target) || e.metaKey || e.ctrlKey || e.altKey) return
      // Arrow keys inside a segmented field pick its value, not the item.
      const inGroup = !!(e.target as HTMLElement | null)?.closest?.('[role="radiogroup"],[role="tablist"]')
      if ((e.key === 'ArrowRight' || e.key === 'ArrowLeft') && inGroup) return
      if (e.key === 'ArrowRight' || e.key === 'j') onNext()
      else if (e.key === 'ArrowLeft' || e.key === 'k') onPrev()
      else if (e.key === 's' && !busy) skip.mutate()
      else if (/^[1-9]$/.test(e.key)) {
        const n = Number(e.key)
        for (const s of specs) {
          if (Array.isArray(s.schema.enum) && n <= s.schema.enum.length) return setDraft(s.name, s.schema.enum[n - 1])
          if ((s.schema.type === 'integer' || s.schema.type === 'number') && (s.schema.minimum ?? 1) <= n && n <= (s.schema.maximum ?? 9)) return setDraft(s.name, n)
        }
      } else if (e.key === 'y' || e.key === 'n') {
        const s = specs.find((x) => x.schema.type === 'boolean')
        if (s) setDraft(s.name, e.key === 'y')
      }
    }
    window.addEventListener('keydown', h)
    return () => window.removeEventListener('keydown', h)
  })

  const state = item.annotation_state
  return (
    <Inspector title="Your review" subtitle={STATE_LABEL[state]} label="Annotation">
      {specsLoading || fb.isLoading ? (
        <Spinner />
      ) : specs.length === 0 ? (
        <p className="ev-note">This queue asks for nothing: mark each item done or skip it. Edit the queue to add annotation fields.</p>
      ) : (
        specs.map((s) => {
          const others = rows.filter((r) => r.feedback_type === annotationType(s.name) && !isMine(r))
          const p = parseRef(s.ref)
          return (
            <div key={s.ref} className="rv-field">
              <div className="rv-field-name">
                {p ? (
                  <Link to={objectUrl(projectId, p.name, p.digest ?? 'latest')} title="The annotation field’s definition" style={{ color: 'inherit' }}>
                    {s.name}
                  </Link>
                ) : (
                  <span>{s.name}</span>
                )}
                {mineBySpec.has(s.name) && <span className="quiet">saved</span>}
              </div>
              {s.description && <div className="rv-field-help">{s.description}</div>}
              <FieldInput name={s.name} schema={s.schema} value={drafts[s.name] ?? null} onChange={(v) => setDraft(s.name, v)} />
              {others.length > 0 && (
                <div className="rv-field-help">
                  Others said: {others.map((o) => `${JSON.stringify(o.payload?.value)} (${o.creator ?? o.user_id ?? 'anonymous'})`).join(', ')}
                </div>
              )}
            </div>
          )
        })
      )}
      <ErrorBox error={submit.error ?? skip.error} />
      <div className="rv-buttons">
        <button type="button" className="qbtn primary" disabled={busy} onClick={() => submit.mutate()} title="Submit and go to the next item (Ctrl/⌘+Enter)">
          {submit.isPending ? 'Saving…' : specs.length ? 'Submit' : 'Mark done'}
        </button>
        <button type="button" className="qbtn" disabled={busy} onClick={() => skip.mutate()} title="Skip (s)">
          Skip
        </button>
        <span className="grow" />
        <button type="button" className="qbtn" disabled={isFirst || busy} onClick={onPrev} title="Previous (← or k)" aria-label="Previous item">
          ←
        </button>
        <button type="button" className="qbtn" disabled={isLast || busy} onClick={onNext} title="Next (→ or j)" aria-label="Next item">
          →
        </button>
      </div>
      <div className="rv-keys">
        <Kbd>⌘/Ctrl+Enter</Kbd> submit · <Kbd>s</Kbd> skip · <Kbd>←</Kbd> <Kbd>→</Kbd> or <Kbd>k</Kbd> <Kbd>j</Kbd> move · <Kbd>1</Kbd>–<Kbd>9</Kbd> score · <Kbd>y</Kbd>{' '}
        <Kbd>n</Kbd> yes or no
      </div>
    </Inspector>
  )
}

/** One annotation field: small scales and yes/no as segments (arrow keys move), anything else the shared input. */
function FieldInput({ name, schema, value, onChange }: { name: string; schema: FieldSchema; value: Any; onChange: (v: Any) => void }) {
  if (schema.type === 'boolean')
    return (
      <Segmented
        label={name}
        options={[
          { id: 'yes', label: 'Yes' },
          { id: 'no', label: 'No' },
        ]}
        value={value === true ? 'yes' : value === false ? 'no' : null}
        onChange={(id) => onChange(id === 'yes')}
      />
    )
  if (schema.type === 'integer' && schema.minimum != null && schema.maximum != null && schema.maximum - schema.minimum <= 9 && !Array.isArray(schema.enum)) {
    const opts = Array.from({ length: schema.maximum - schema.minimum + 1 }, (_, i) => String(schema.minimum! + i))
    return <Segmented label={name} options={opts.map((o) => ({ id: o, label: o }))} value={value == null ? null : String(value)} onChange={(id) => onChange(Number(id))} />
  }
  return <AnnotationInput schema={schema} value={value} onChange={onChange} />
}

function ItemList({
  items,
  current,
  onPick,
  onRemove,
}: {
  items: AnnotationQueueItem[]
  current: number
  onPick: (i: number) => void
  onRemove: (item: AnnotationQueueItem) => void
}) {
  return (
    <ul className="rv-items glist-box" aria-label="Items in this queue">
      {items.map((it, i) => (
        <li key={it.id} className={i === current ? 'current' : undefined}>
          <button type="button" className="rv-pick" onClick={() => onPick(i)} aria-current={i === current ? 'true' : undefined}>
            <StatusGlyph kind={STATE_GLYPH[it.annotation_state]} size={14} decorative />
            <span className="quiet-note">{it.position_in_queue ?? i + 1}</span>
            <span className="name">{spanDisplayName(it.span_name)}</span>
            <span className="state">{STATE_LABEL[it.annotation_state]}</span>
          </button>
          <button
            type="button"
            className="icon-btn"
            aria-label={`Remove ${spanDisplayName(it.span_name)} (item ${it.position_in_queue ?? i + 1}) from the queue`}
            title="Remove from the queue (annotations already given are kept; can be undone)"
            onClick={() => onRemove(it)}
          >
            ✕
          </button>
        </li>
      ))}
    </ul>
  )
}
