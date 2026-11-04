import { useMemo, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '../../api/client'
import type { WeftObject } from '../../api/types'
import { ErrorBox, Modal, Spinner } from '../common'
import { NameField, usePublish, type Created } from '../objects/ObjectForms'
import { classVal, errMsg } from '../objects/objectUtils'
import { ScorerPicker } from './ScorerPicker'

const QUERY_EXAMPLE = `{"$expr": {"$gt": [{"$getField": "summary.weft.latency_ms"}, {"$literal": 1000}]}}`

export type MonitorScope = 'session' | 'agent' | 'handoff' | 'call'

/** Where a monitor's scorers run (API.md "Monitors": scopes). */
export const MONITOR_SCOPES: { id: MonitorScope; label: string; help: string }[] = [
  { id: 'session', label: 'Each session', help: 'once a session is complete, e.g. goal completion' },
  { id: 'agent', label: 'Each agent in a session', help: 'per agent that took part' },
  { id: 'handoff', label: 'Each hand-off', help: 'per hand-off between agents' },
  { id: 'call', label: 'Each span', help: 'every matching span as it finishes' },
]
export const DEFAULT_IDLE_SECONDS = 60

/** The monitor fields for its scopes and idle time; no scopes means "each scorer where it fits". */
export function monitorScopeFields(scopes: MonitorScope[], idleSeconds: number): { scopes?: MonitorScope[]; session_idle_seconds: number } {
  const idle = Math.min(86400, Math.max(1, Math.round(idleSeconds) || DEFAULT_IDLE_SECONDS))
  return scopes.length ? { scopes: MONITOR_SCOPES.map((s) => s.id).filter((s) => scopes.includes(s)), session_idle_seconds: idle } : { session_idle_seconds: idle }
}

export function MonitorEditor({ projectId, initial, onClose, onSaved }: { projectId: string; initial?: WeftObject; onClose: () => void; onSaved: (c: Created) => void }) {
  const publish = usePublish(projectId)
  const v = initial?.value ?? {}
  const [name, setName] = useState<string>(initial?.name ?? '')
  const [description, setDescription] = useState<string>(v.description ?? '')
  const [active, setActive] = useState<boolean>(v.active ?? true)
  const [opNames, setOpNames] = useState<string[]>(Array.isArray(v.op_names) ? v.op_names : [])
  const [rate, setRate] = useState<number>(typeof v.sampling_rate === 'number' ? v.sampling_rate : 1)
  const [queryText, setQueryText] = useState<string>(v.query ? JSON.stringify(v.query, null, 2) : '')
  const [scorers, setScorers] = useState<string[]>(Array.isArray(v.scorers) ? v.scorers : [])
  const [scopes, setScopes] = useState<MonitorScope[]>(Array.isArray(v.scopes) ? v.scopes : [])
  const [idle, setIdle] = useState<number>(typeof v.session_idle_seconds === 'number' ? v.session_idle_seconds : DEFAULT_IDLE_SECONDS)
  const [opSearch, setOpSearch] = useState('')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<unknown>(null)

  const ops = useQuery({ queryKey: ['span-names', projectId], queryFn: () => api.spanNames(projectId).then((r) => r.items) })
  const opChoices = useMemo(() => {
    const m = new Map<string, number>()
    for (const o of ops.data ?? []) {
      if (o.base_name.endsWith('.score') || o.base_name.startsWith('Evaluation.') || o.base_name.startsWith('weft.') || o.base_name === 'LLMStructuredCompletionModel.predict') continue
      m.set(o.base_name, (m.get(o.base_name) ?? 0) + o.count)
    }
    for (const n of opNames) if (!m.has(n)) m.set(n, 0)
    return [...m.entries()].sort((a, b) => b[1] - a[1])
  }, [ops.data, opNames])

  const queryErr = useMemo(() => {
    if (!queryText.trim()) return null
    try {
      const q = JSON.parse(queryText)
      if (!q || typeof q !== 'object' || !('$expr' in q)) return 'Filter must be an object with a "$expr" key'
      return null
    } catch (e) {
      return errMsg(e)
    }
  }, [queryText])

  const shownOps = opChoices.filter(([n]) => !opSearch || n.toLowerCase().includes(opSearch.toLowerCase()))
  const valid = !!name && scorers.length > 0 && !queryErr

  return (
    <Modal
      title={initial ? `Edit monitor ${initial.name}` : 'New monitor'}
      wide
      onClose={onClose}
      footer={
        <>
          <span className="hint grow">{scorers.length === 0 ? 'Pick at least one scorer.' : ''}</span>
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
                const fields: Record<string, unknown> = { name, description, active, sampling_rate: rate, op_names: opNames, scorers, ...monitorScopeFields(scopes, idle) }
                const base = { ...(initial?.value ?? {}) }
                delete base.query
                delete base.scopes
                if (queryText.trim()) fields.query = JSON.parse(queryText)
                onSaved(await publish(name, { ...base, ...classVal('Monitor', [], fields) }))
              } catch (e) {
                setErr(e)
              } finally {
                setBusy(false)
              }
            }}
          >
            {busy ? 'Saving…' : initial ? 'Save new version' : 'Create monitor'}
          </button>
        </>
      }
    >
      <div className="form-grid">
        <NameField value={name} onChange={setName} locked={!!initial} placeholder="answer-quality" />
        <label className="field">
          Description
          <input className="input" value={description} onChange={(e) => setDescription(e.target.value)} placeholder="Optional" />
        </label>
      </div>
      <label className="checkbox">
        <input type="checkbox" checked={active} onChange={(e) => setActive(e.target.checked)} /> Active: score new sessions and calls as they finish
      </label>

      <div className="field" style={{ fontSize: 12, color: 'var(--text-2)' }}>
        <div className="row">
          <span className="grow">Span names to monitor {opNames.length ? `(${opNames.length})` : '(none selected: all root spans)'}</span>
          <input className="input" style={{ height: 24, width: 160, fontSize: 12 }} placeholder="Filter or add a span name…" value={opSearch} onChange={(e) => setOpSearch(e.target.value)} onKeyDown={(e) => {
            if (e.key === 'Enter' && opSearch.trim() && !opNames.includes(opSearch.trim())) {
              setOpNames([...opNames, opSearch.trim()])
              setOpSearch('')
            }
          }} />
        </div>
        {ops.isLoading ? (
          <Spinner />
        ) : (
          <div className="chips" style={{ maxHeight: 140, overflow: 'auto', padding: '4px 0' }}>
            {shownOps.map(([n, count]) => (
              <button key={n} className={`chip selectable ${opNames.includes(n) ? 'on' : ''}`} onClick={() => setOpNames(opNames.includes(n) ? opNames.filter((x) => x !== n) : [...opNames, n])}>
                {opNames.includes(n) ? '✓ ' : ''}
                {n}
                {count > 0 && <span className="faint"> {count}</span>}
              </button>
            ))}
            {shownOps.length === 0 && <span className="hint">{opSearch ? 'Press Enter to add this span name.' : 'No spans recorded yet.'}</span>}
          </div>
        )}
      </div>

      <label className="field">
        Sampling rate: {Math.round(rate * 100)}% of matching calls
        <input className="input" type="range" min={0} max={1} step={0.01} value={rate} onChange={(e) => setRate(Number(e.target.value))} />
      </label>

      <label className="field">
        Filter (optional query)
        <textarea className="input mono" rows={3} value={queryText} onChange={(e) => setQueryText(e.target.value)} placeholder={QUERY_EXAMPLE} />
        {queryErr ? <span className="hint" style={{ color: 'var(--error)' }}>{queryErr}</span> : <span className="hint">Only spans matching this query are scored. Uses the same $expr language as trace filters.</span>}
      </label>

      <div className="field" style={{ fontSize: 12, color: 'var(--text-2)' }}>
        Scorers
        <ScorerPicker projectId={projectId} value={scorers} onChange={setScorers} judgeOnlyCreate />
      </div>

      <fieldset className="field monitor-scopes">
        <legend>Score</legend>
        {MONITOR_SCOPES.map((s) => (
          <label key={s.id} className="checkbox">
            <input
              type="checkbox"
              checked={scopes.includes(s.id)}
              onChange={(e) => setScopes(e.target.checked ? [...scopes, s.id] : scopes.filter((x) => x !== s.id))}
            />{' '}
            {s.label} <span className="hint">{s.help}</span>
          </label>
        ))}
        <span className="hint">
          {scopes.length ? '' : 'None ticked: session, agent and hand-off judges score each session, other scorers score each span.'}
        </span>
      </fieldset>
      {(scopes.length === 0 || scopes.some((s) => s !== 'call')) && (
        <label className="field">
          A session is complete after this many seconds without new spans
          <input className="input" type="number" min={1} max={86400} step={1} value={idle} onChange={(e) => setIdle(Number(e.target.value))} style={{ maxWidth: 140 }} />
          <span className="hint">Session, agent and hand-off scorers run then. Default {DEFAULT_IDLE_SECONDS}.</span>
        </label>
      )}
      <ErrorBox error={err} />
    </Modal>
  )
}

/** Toggle a monitor's active flag by publishing a new version. */
export function useToggleMonitor(projectId: string) {
  const publish = usePublish(projectId)
  const qc = useQueryClient()
  const [pending, setPending] = useState<string | null>(null)
  const toggle = async (o: WeftObject) => {
    setPending(o.name)
    try {
      await publish(o.name, { ...o.value, active: !o.value?.active })
      await qc.invalidateQueries({ queryKey: ['objects', projectId] })
      await qc.invalidateQueries({ queryKey: ['object', projectId, o.name] })
    } catch (e) {
      alert(`Could not update monitor: ${errMsg(e)}`)
    } finally {
      setPending(null)
    }
  }
  return { toggle, pending }
}

