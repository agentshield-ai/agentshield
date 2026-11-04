import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { api } from '../../api/client'
import { runtimesApi } from '../../api/runtimes'
import { ErrorBox, Modal, Spinner } from '../common'
import { NameField, usePublish } from '../objects/ObjectForms'
import { classVal } from '../objects/objectUtils'
import { predictUrlError, promptColumns } from './modelUtils'

type Created = Awaited<ReturnType<ReturnType<typeof usePublish>>>

function SecretSelect({ value, onChange, label }: { value: string; onChange: (v: string) => void; label: string }) {
  const secrets = useQuery({ queryKey: ['secrets'], queryFn: () => api.secretsList().then((r) => r.secrets) })
  return (
    <label className="field">
      {label}
      <select className="input" value={value} onChange={(e) => onChange(e.target.value)}>
        <option value="">None</option>
        {(secrets.data ?? []).map((s) => (
          <option key={s.name} value={s.name}>
            {s.name} ({s.source})
          </option>
        ))}
        {value && !(secrets.data ?? []).some((s) => s.name === value) && <option value={value}>{value}</option>}
      </select>
      <span className="hint">Sent as an Authorization: Bearer header. Add secrets in Settings → Secrets.</span>
    </label>
  )
}

/** A model the server evaluates by POSTing each dataset row to `predict_url`. */
export function RemoteModelModal({ projectId, onClose, onCreated }: { projectId: string; onClose: () => void; onCreated: (c: Created) => void }) {
  const publish = usePublish(projectId)
  const [name, setName] = useState('')
  const [url, setUrl] = useState('')
  const [secret, setSecret] = useState('')
  const [timeout, setTimeoutS] = useState(120)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<unknown>(null)
  const urlErr = url ? predictUrlError(url) : null
  const save = async () => {
    setBusy(true)
    setErr(null)
    try {
      const fields: Record<string, unknown> = { name, predict_url: url.trim(), predict_timeout: timeout }
      if (secret) fields.predict_api_key_secret = secret
      onCreated(await publish(name, classVal('RemoteModel', ['Model'], fields)))
    } catch (e) {
      setErr(e)
    } finally {
      setBusy(false)
    }
  }
  return (
    <Modal
      title="New remote model"
      onClose={onClose}
      footer={
        <>
          <button className="btn" onClick={onClose}>
            Cancel
          </button>
          <button className="btn primary" disabled={!name || !url || !!predictUrlError(url) || busy} onClick={save}>
            {busy ? 'Saving…' : 'Create model'}
          </button>
        </>
      }
    >
      <div className="col" style={{ gap: 12 }}>
        <span className="small muted">
          The server POSTs <span className="mono">{'{"inputs": <row>, "model_ref", "project_id"}'}</span> to this URL for every dataset row. A JSON{' '}
          <span className="mono">{'{"output": …}'}</span> response is the prediction (any other body is used as is).
        </span>
        <NameField value={name} onChange={setName} placeholder="my-remote-model" />
        <label className="field">
          Predict URL
          <input className="input mono" value={url} onChange={(e) => setUrl(e.target.value)} placeholder="https://models.internal/predict" />
          {urlErr && <span className="small" style={{ color: 'var(--error)' }}>{urlErr}</span>}
        </label>
        <SecretSelect value={secret} onChange={setSecret} label="API key secret (optional)" />
        <label className="field">
          Timeout (seconds)
          <input className="input" type="number" min={1} max={600} value={timeout} onChange={(e) => setTimeoutS(Math.max(1, Math.min(600, Number(e.target.value) || 120)))} style={{ width: 100 }} />
        </label>
        <ErrorBox error={err} />
      </div>
    </Modal>
  )
}

/** A model bound to a custom runtime: a predict endpoint, or an OpenAI-compatible model with prompts. */
export function RuntimeModelModal({ projectId, onClose, onCreated }: { projectId: string; onClose: () => void; onCreated: (c: Created) => void }) {
  const publish = usePublish(projectId)
  const runtimes = useQuery({ queryKey: ['runtimes', projectId], queryFn: () => runtimesApi.list(projectId) })
  const [name, setName] = useState('')
  const [runtime, setRuntime] = useState('')
  const [runtimeId, setRuntimeId] = useState('')
  const [system, setSystem] = useState('You are a helpful assistant.')
  const [template, setTemplate] = useState('{question}')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<unknown>(null)
  const list = runtimes.data?.items ?? []
  const rt = list.find((r) => r.name === runtime)
  const isPredict = rt?.kind === 'predict'
  const save = async () => {
    setBusy(true)
    setErr(null)
    try {
      const fields: Record<string, unknown> = { name, runtime }
      if (runtimeId) fields.runtime_id = runtimeId
      if (!isPredict) {
        fields.system_prompt = system
        fields.prompt_template = template
      }
      onCreated(await publish(name, classVal('RuntimeModel', ['Model'], fields)))
    } catch (e) {
      setErr(e)
    } finally {
      setBusy(false)
    }
  }
  return (
    <Modal
      title="New runtime model"
      onClose={onClose}
      footer={
        <>
          <button className="btn" onClick={onClose}>
            Cancel
          </button>
          <button className="btn primary" disabled={!name || !runtime || (!isPredict && !template.trim()) || busy} onClick={save}>
            {busy ? 'Saving…' : 'Create model'}
          </button>
        </>
      }
    >
      <div className="col" style={{ gap: 12 }}>
        {runtimes.isLoading ? (
          <Spinner label="Loading runtimes…" />
        ) : list.length === 0 ? (
          <div className="info-box small">
            No runtimes in this project yet. Register one with <span className="mono">PUT /api/v1/runtimes/&lt;name&gt;</span> (the SDKs'{' '}
            <span className="mono">register_custom_runtime</span>).
          </div>
        ) : null}
        <ErrorBox error={runtimes.error} />
        <NameField value={name} onChange={setName} placeholder="llama-qa" />
        <label className="field">
          Runtime
          <select
            className="input"
            value={runtime}
            onChange={(e) => {
              setRuntime(e.target.value)
              setRuntimeId(list.find((r) => r.name === e.target.value)?.runtime_ids[0]?.id ?? '')
            }}
          >
            <option value="">Choose a runtime…</option>
            {list.map((r) => (
              <option key={r.name} value={r.name}>
                {r.name} · {r.kind ?? 'openai'} · {r.base_url}
              </option>
            ))}
          </select>
        </label>
        {rt && rt.runtime_ids.length > 0 && (
          <label className="field">
            Runtime ID
            <select className="input mono" value={runtimeId} onChange={(e) => setRuntimeId(e.target.value)}>
              {rt.runtime_ids.map((r) => (
                <option key={r.id} value={r.id}>
                  {r.id}
                </option>
              ))}
            </select>
          </label>
        )}
        {rt && !isPredict && (
          <>
            <label className="field">
              System prompt
              <textarea className="input" rows={2} value={system} onChange={(e) => setSystem(e.target.value)} />
            </label>
            <label className="field">
              Prompt template
              <textarea className="input mono" rows={3} value={template} onChange={(e) => setTemplate(e.target.value)} />
              <span className="hint">
                Dataset columns in braces are filled per row
                {promptColumns(template).length ? `: ${promptColumns(template).map((c) => `{${c}}`).join(' ')}` : ''}.
              </span>
            </label>
          </>
        )}
        {rt && isPredict && <span className="small muted">Rows are POSTed to the runtime's base URL with its headers and API key secret.</span>}
        <ErrorBox error={err} />
      </div>
    </Modal>
  )
}
