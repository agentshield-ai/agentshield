import { useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { api } from '../../api/client'
import { monitorUrl } from '../../lib/urls'
import { ErrorBox, Modal } from '../common'
import { GroupedList, ListRow } from '../ui/GroupedList'
import { LLMModelInput, usePublish } from '../objects/ObjectForms'
import { useLLMModels } from '../objects/objectUtils'
import { createSignal, planSignal, SIGNALS, type SignalCategory, type SignalTemplate } from './signals'

const CATEGORIES: SignalCategory[] = ['Quality', 'Safety', 'Reliability']

/** Ready-made monitors ("signals"): one click publishes the scorer and an active Monitor. */
export function SignalsGallery({ projectId, onCreated }: { projectId: string; onCreated: (monitorId: string) => void }) {
  const [picked, setPicked] = useState<SignalTemplate | null>(null)
  const [open, setOpen] = useState<boolean | null>(null)
  const monitors = useQuery({
    queryKey: ['objects', projectId, 'signal-monitors'],
    queryFn: () =>
      api.objectsQuery({
        project_id: projectId,
        filter: { kinds: ['monitor'], latest_only: true },
        metadata_only: true,
        limit: 1000,
      }),
  })
  const installed = useMemo(() => {
    const m = new Map<string, string>()
    for (const o of monitors.data?.items ?? []) {
      for (const s of SIGNALS) if (o.name === `signal-${s.id}` || o.name.startsWith(`signal-${s.id}-`)) m.set(s.id, o.name)
    }
    return m
  }, [monitors.data])
  // Expanded while the project has no monitors; collapsible afterwards.
  const expanded = open ?? (monitors.isSuccess && (monitors.data?.items.length ?? 0) === 0)
  return (
    <section className="obs-signals obs-quiet" aria-labelledby="signals-h">
      <div className="obs-section-head">
        <h2 className="view-h" id="signals-h">
          Ready-made signals
        </h2>
        <button type="button" className="linkish" onClick={() => setOpen(!expanded)} aria-expanded={expanded} aria-controls="signals-list">
          {expanded ? 'Hide' : `Show ${SIGNALS.length} signals`}
        </button>
      </div>
      <p className="quiet-note obs-wrap">Monitors for common problems. Each creates a scorer and an active monitor you can edit afterwards.</p>
      {expanded && (
        <div id="signals-list" className="obs-groups" style={{ gap: 20 }}>
          {CATEGORIES.map((cat) => (
            <GroupedList key={cat} title={cat}>
              {SIGNALS.filter((x) => x.category === cat).map((x) => {
                const have = installed.get(x.id)
                return (
                  <ListRow
                    key={x.id}
                    className="obs-signal"
                    title={x.title}
                    summary={x.description}
                    meta={
                      <span title={`Feedback type weft.scorer.${x.scorer}`}>
                        {x.model === 'judge' ? 'LLM judge' : x.model === 'moderation' ? 'Moderation API' : 'Rule'}
                        {have ? ' · added' : ''}
                      </span>
                    }
                    trailing={
                      <span className="obs-row-trailing" data-signal={x.id}>
                        {have ? (
                          <Link className="qbtn" to={monitorUrl(projectId, have)} aria-label={`Open the ${x.title} monitor`}>
                            Open
                          </Link>
                        ) : (
                          <button type="button" className="qbtn" onClick={() => setPicked(x)} aria-label={`Add the ${x.title} signal`}>
                            Add
                          </button>
                        )}
                      </span>
                    }
                  />
                )
              })}
            </GroupedList>
          ))}
        </div>
      )}
      {picked && (
        <AddSignalModal
          projectId={projectId}
          template={picked}
          onClose={() => setPicked(null)}
          onCreated={(id) => {
            setPicked(null)
            onCreated(id)
          }}
        />
      )}
    </section>
  )
}

function AddSignalModal({
  projectId,
  template,
  onClose,
  onCreated,
}: {
  projectId: string
  template: SignalTemplate
  onClose: () => void
  onCreated: (monitorId: string) => void
}) {
  const publish = usePublish(projectId)
  const models = useLLMModels()
  const firstAvailable = models.data?.find((m) => m.available && !m.id.startsWith('mock/'))?.id
  const [model, setModel] = useState<string>('')
  const effModel = model || firstAvailable || 'mock/json'
  const [ops, setOps] = useState('')
  const [rate, setRate] = useState(template.model ? 0.2 : 1)
  const [latency, setLatency] = useState(10)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<unknown>(null)
  const spanNames = useQuery({
    queryKey: ['span-names', projectId],
    queryFn: () => api.spanNames(projectId).then((r) => r.items),
  })

  const create = async () => {
    setBusy(true)
    setErr(null)
    try {
      const all = await api.objectsQuery({
        project_id: projectId,
        filter: { latest_only: true },
        metadata_only: true,
        limit: 10000,
      })
      const taken = new Set(all.items.map((o) => o.name))
      const opNames = ops
        .split(',')
        .map((s) => s.trim())
        .filter(Boolean)
      const plan = planSignal(
        template,
        {
          model: effModel,
          opNames,
          samplingRate: rate,
          latencySeconds: latency,
        },
        taken,
      )
      const res = await createSignal(plan, (id, val) => publish(id, val))
      onCreated(res.monitorId)
    } catch (e) {
      setErr(e)
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal
      title={`Add signal: ${template.title}`}
      onClose={onClose}
      footer={
        <>
          <button className="btn" onClick={onClose}>
            Cancel
          </button>
          <button className="btn primary" disabled={busy} onClick={() => void create()}>
            {busy ? 'Creating…' : 'Create monitor'}
          </button>
        </>
      }
    >
      <div className="col" style={{ gap: 12 }}>
        <span className="muted small">{template.description}</span>
        {template.model === 'judge' && <LLMModelInput value={effModel} onChange={setModel} id="signal-models" />}
        {template.model === 'moderation' && (
          <span className="hint">
            Uses the OpenAI moderation API (omni-moderation-latest) with the OPENAI_API_KEY secret. Pick a mock/ model to test offline:{' '}
            <select
              className="input"
              style={{ width: 'auto', display: 'inline-block' }}
              value={effModel.startsWith('mock/') ? 'mock' : 'openai'}
              onChange={(e) => setModel(e.target.value === 'mock' ? 'mock/json' : 'gpt-4o')}
            >
              <option value="openai">OpenAI moderation</option>
              <option value="mock">Offline mock</option>
            </select>
          </span>
        )}
        {template.id === 'latency' && (
          <label className="field">
            Flag calls slower than (seconds)
            <input className="input" type="number" min={0} step="any" value={latency} onChange={(e) => setLatency(Number(e.target.value))} />
          </label>
        )}
        <label className="field">
          Span names to monitor
          <input
            className="input mono"
            list="signal-ops"
            value={ops}
            onChange={(e) => setOps(e.target.value)}
            placeholder="Comma-separated; empty means all root spans"
          />
          <datalist id="signal-ops">
            {(spanNames.data ?? []).map((o) => (
              <option key={o.base_name} value={o.base_name} />
            ))}
          </datalist>
        </label>
        <label className="field">
          Sampling rate: {Math.round(rate * 100)}% of matching calls
          <input className="input" type="range" min={0.01} max={1} step={0.01} value={rate} onChange={(e) => setRate(Number(e.target.value))} />
          {template.model && <span className="hint">LLM-based signals cost a judge call per scored span; sample to control spend.</span>}
        </label>
        <ErrorBox error={err} />
      </div>
    </Modal>
  )
}
