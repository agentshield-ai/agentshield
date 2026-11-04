import { useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { api } from '../../api/client'
import { monitorUrl } from '../../lib/urls'
import { ErrorBox, Modal } from '../common'
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
    <div className="card" style={{ marginBottom: 16 }}>
      <div className="card-header row">
        <b>Signals</b>
        <span className="muted small grow">Ready-made monitors for common problems. Each creates a scorer and an active monitor you can edit afterwards.</span>
        <button className="btn sm ghost" onClick={() => setOpen(!expanded)} aria-expanded={expanded}>
          {expanded ? 'Hide' : `Show ${SIGNALS.length} signals`}
        </button>
      </div>
      {expanded && (
        <div className="card-body col" style={{ gap: 12 }}>
          {CATEGORIES.map((cat) => (
            <div key={cat} className="col" style={{ gap: 6 }}>
              <div className="faint small" style={{ textTransform: 'uppercase', letterSpacing: '0.04em' }}>
                {cat}
              </div>
              <div className="card-grid">
                {SIGNALS.filter((s) => s.category === cat).map((s) => {
                  const have = installed.get(s.id)
                  return (
                    <div key={s.id} className="eval-card" data-signal={s.id}>
                      <div className="row">
                        <b className="grow">{s.title}</b>
                        <span className={`badge ${s.model ? 'accent' : ''}`}>
                          {s.model === 'judge' ? 'LLM judge' : s.model === 'moderation' ? 'Moderation API' : 'Rule'}
                        </span>
                      </div>
                      <span className="muted small grow">{s.description}</span>
                      <div className="row" style={{ minWidth: 0 }}>
                        <span className="faint small mono grow truncate" style={{ minWidth: 0 }} title={`Feedback type weft.scorer.${s.scorer}`}>
                          weft.scorer.{s.scorer}
                        </span>
                        {have ? (
                          <Link className="btn sm" to={monitorUrl(projectId, have)}>
                            Open
                          </Link>
                        ) : (
                          <button className="btn sm primary" onClick={() => setPicked(s)}>
                            + Add
                          </button>
                        )}
                      </div>
                    </div>
                  )
                })}
              </div>
            </div>
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
    </div>
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
