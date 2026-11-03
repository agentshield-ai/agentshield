import { useCallback, useEffect, useMemo, useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '../../api/client'
import { queuesApi, type AnnotationQueueItem, type AnnotationState } from '../../api/queues'
import { callRef, opDisplayName, parseRef } from '../../api/refs'
import type { Any, Call, Feedback } from '../../api/types'
import { Empty, ErrorBox, Loading, Section, Spinner, StatusBadge } from '../../components/common'
import { queuesUrl } from '../../components/queues/AddToQueueModal'
import { ExportQueueModal } from '../../components/queues/ExportQueueModal'
import { annotationType, displayFieldValues, nextPendingIndex, progressCounts } from '../../components/queues/queueUtils'
import { CallChatView, extractChat } from '../../components/traces/ChatView'
import { AnnotationInput, isMine, rememberUserId, useCallFeedback, type FieldSchema } from '../../components/traces/FeedbackPanel'
import { ValueView } from '../../components/ValueView'
import { useProjectId } from '../../hooks/useProject'
import { callStatus, fmtDate, fmtDuration, callLatencyMs } from '../../lib/format'
import { callUrl, objectUrl } from '../../lib/urls'
import { undoable } from '../../components/ui/Toasts'

interface QueueSpec {
  ref: string
  name: string
  description?: string
  schema: FieldSchema
  unique: boolean
}

const STATE_LABEL: Record<AnnotationState, string> = { unstarted: 'Not started', in_progress: 'In progress', completed: 'Done', skipped: 'Skipped' }
const STATE_COLOR: Record<AnnotationState, string> = {
  unstarted: 'var(--text-3)',
  in_progress: 'var(--running)',
  completed: 'var(--success)',
  skipped: 'var(--warn)',
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
      const { vals } = await api.refsReadBatch(refs)
      return refs.map((ref, i) => ({
        ref,
        name: String(vals[i]?.name ?? parseRef(ref)?.name ?? ref),
        description: vals[i]?.description ?? undefined,
        schema: (vals[i]?.field_schema ?? {}) as FieldSchema,
        unique: vals[i]?.unique_among_creators !== false,
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
        message: `Removed “${opDisplayName(item.call_op_name)}” from the queue`,
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

  if (queue.isLoading || items.isLoading) return <Loading />
  if (queue.error || items.error)
    return (
      <div className="page">
        <ErrorBox error={queue.error ?? items.error} />
        <Link to={queuesUrl(projectId)}>← All queues</Link>
      </div>
    )
  const q = queue.data!
  const allDone = list.length > 0 && progress.remaining === 0

  return (
    <div className="page">
      <div className="breadcrumbs">
        <Link to={queuesUrl(projectId)}>Review queues</Link>
        <span>/</span>
        <span>{q.name}</span>
      </div>
      <div className="page-header">
        <div className="col" style={{ gap: 2, minWidth: 0 }}>
          <h1>{q.name}</h1>
          {q.description && <span className="muted small">{q.description}</span>}
        </div>
        <span className="spacer" />
        <button className="btn sm" onClick={() => setExporting(true)}>
          Export to dataset
        </button>
      </div>
      <div className="row" style={{ gap: 12, marginBottom: 14 }} aria-label="Queue progress">
        <div className="progress grow" title={`${progress.completed} done, ${progress.skipped} skipped, ${progress.remaining} remaining`}>
          <span style={{ width: `${progress.done * 100}%` }} />
        </div>
        <span className="small mono">
          {progress.completed + progress.skipped}/{progress.total} reviewed
        </span>
        {cur && (
          <span className="small muted">
            Item {(idx ?? 0) + 1} of {list.length}
          </span>
        )}
      </div>
      {allDone && (
        <div className="success-box row" style={{ marginBottom: 14 }}>
          <span className="grow">You have reviewed every item in this queue.</span>
          <button className="btn sm" onClick={() => setExporting(true)}>
            Export to dataset
          </button>
        </div>
      )}
      {!list.length ? (
        <div className="card">
          <Empty>
            This queue is empty. Add calls from the Traces page (select rows → <b>Add to queue</b>) or from a call’s page.
          </Empty>
        </div>
      ) : cur ? (
        <div style={{ display: 'grid', gridTemplateColumns: 'minmax(0, 1fr) minmax(300px, 380px)', gap: 16, alignItems: 'start' }}>
          <CallPane projectId={projectId} item={cur} />
          <div className="col" style={{ gap: 12, position: 'sticky', top: 12 }}>
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
            <ItemList items={list} current={idx ?? 0} onPick={setIdx} onRemove={removeItem} />
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
  const call = useQuery({ queryKey: ['call', projectId, item.call_id], queryFn: () => api.callRead(projectId, item.call_id).then((r) => r.call) })
  if (call.isLoading) return <Loading label="Loading call…" />
  if (call.error) return <ErrorBox error={call.error} />
  const c = call.data as Call | null
  if (!c) return <div className="card"><Empty>This call no longer exists.</Empty></div>
  const chat = extractChat(c)
  const fields = item.display_fields.length ? displayFieldValues(c, item.display_fields) : []
  return (
    <div className="col" style={{ gap: 12, minWidth: 0 }}>
      <div className="card">
        <div className="card-header row">
          <b className="mono truncate">{opDisplayName(c.op_name)}</b>
          <StatusBadge status={callStatus(c)} />
          <span className="spacer grow" />
          <span className="faint small">{fmtDate(c.started_at)}</span>
          <span className="faint small">{fmtDuration(callLatencyMs(c))}</span>
          <Link className="btn sm ghost" to={callUrl(projectId, c.id)} target="_blank">
            Open call ↗
          </Link>
        </div>
      </div>
      {c.exception && <div className="error-box mono small">{c.exception}</div>}
      {fields.length > 0 && (
        <Section title="Fields">
          <div className="col" style={{ gap: 10 }}>
            {fields.map((f) => (
              <div key={f.path}>
                <div className="faint small mono">{f.path}</div>
                <ValueView value={f.value} projectId={projectId} />
              </div>
            ))}
          </div>
        </Section>
      )}
      {chat ? (
        <Section title="Conversation">
          <CallChatView chat={chat} projectId={projectId} />
        </Section>
      ) : (
        <>
          <Section title="Inputs">
            <ValueView value={Object.fromEntries(Object.entries(c.inputs ?? {}).filter(([k]) => k !== 'self'))} projectId={projectId} />
          </Section>
          <Section title="Output">
            <ValueView value={c.output} projectId={projectId} markdown />
          </Section>
        </>
      )}
    </div>
  )
}

function Kbd({ children }: { children: string }) {
  return <span className="kbd-inline">{children}</span>
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
  const fb = useCallFeedback(projectId, item.call_id)
  const rows: Feedback[] = fb.data?.result ?? NO_FEEDBACK
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
      const ref = callRef(projectId, item.call_id)
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
    <div className="card">
      <div className="card-header row">
        <b className="grow">Annotate</b>
        <span className="badge" style={{ color: STATE_COLOR[state] }}>
          {STATE_LABEL[state]}
        </span>
      </div>
      <div className="card-body col" style={{ gap: 14 }}>
        {specsLoading || fb.isLoading ? (
          <Spinner />
        ) : specs.length === 0 ? (
          <div className="hint">This queue has no annotation fields. Mark items done or skip them, or edit the queue to add fields.</div>
        ) : (
          specs.map((s) => {
            const others = rows.filter((r) => r.feedback_type === annotationType(s.name) && !isMine(r))
            return (
              <div key={s.ref} className="annotation">
                <div className="row">
                  {(() => {
                    const p = parseRef(s.ref)
                    return p ? (
                      <Link className="mono small" to={objectUrl(projectId, p.name, p.digest ?? 'latest')} title="Annotation spec">
                        {s.name}
                      </Link>
                    ) : (
                      <span className="mono small">{s.name}</span>
                    )
                  })()}
                  <span className="grow" />
                  {mineBySpec.has(s.name) && <span className="faint small">saved</span>}
                </div>
                {s.description && <div className="faint small">{s.description}</div>}
                <AnnotationInput schema={s.schema} value={drafts[s.name] ?? null} onChange={(v) => setDraft(s.name, v)} />
                {others.length > 0 && (
                  <div className="faint small">
                    Others: {others.map((o) => `${JSON.stringify(o.payload?.value)} (${o.creator ?? o.user_id ?? 'anonymous'})`).join(', ')}
                  </div>
                )}
              </div>
            )
          })
        )}
        <ErrorBox error={submit.error ?? skip.error} />
        <div className="row" style={{ gap: 6 }}>
          <button className="btn sm" disabled={isFirst || busy} onClick={onPrev} title="Previous (←)">
            ← Prev
          </button>
          <button className="btn sm" disabled={busy} onClick={() => skip.mutate()} title="Skip (s)">
            Skip
          </button>
          <span className="grow" />
          <button className="btn sm" disabled={isLast || busy} onClick={onNext} title="Next (→)">
            Next →
          </button>
          <button className="btn sm primary" disabled={busy} onClick={() => submit.mutate()} title="Submit (Ctrl/⌘+Enter)">
            {submit.isPending ? 'Saving…' : 'Submit'}
          </button>
        </div>
        <div className="faint small" style={{ lineHeight: 1.8 }}>
          <Kbd>⌘/Ctrl+Enter</Kbd> submit · <Kbd>←</Kbd>/<Kbd>→</Kbd> prev/next · <Kbd>s</Kbd> skip · <Kbd>1</Kbd>–<Kbd>9</Kbd> score ·{' '}
          <Kbd>y</Kbd>/<Kbd>n</Kbd> yes/no
        </div>
      </div>
    </div>
  )
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
    <div className="card">
      <div className="card-header">
        <b>Items</b>
      </div>
      <div style={{ maxHeight: 280, overflow: 'auto' }}>
        {items.map((it, i) => (
          <div
            key={it.id}
            className="row small"
            style={{ gap: 4, paddingRight: 6, background: i === current ? 'var(--accent-2)' : undefined, borderBottom: '1px solid var(--border)' }}
          >
            <button
              className="linkish row small grow"
              style={{ minWidth: 0, gap: 8, padding: '5px 6px 5px 12px', textAlign: 'left', background: 'none', border: 'none', cursor: 'pointer' }}
              onClick={() => onPick(i)}
              aria-current={i === current}
            >
              <span className="dot" style={{ background: STATE_COLOR[it.annotation_state], width: 8, height: 8, borderRadius: 4, flex: 'none' }} />
              <span className="mono faint">{it.position_in_queue ?? i + 1}</span>
              <span className="mono truncate grow">{opDisplayName(it.call_op_name)}</span>
              <span className="faint">{STATE_LABEL[it.annotation_state]}</span>
            </button>
            <button
              type="button"
              className="icon-btn"
              aria-label={`Remove ${opDisplayName(it.call_op_name)} (item ${it.position_in_queue ?? i + 1}) from the queue`}
              title="Remove from the queue (annotations already given are kept)"
              onClick={() => onRemove(it)}
            >
              ✕
            </button>
          </div>
        ))}
      </div>
    </div>
  )
}
