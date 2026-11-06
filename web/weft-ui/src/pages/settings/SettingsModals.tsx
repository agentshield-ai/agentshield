// The dialogs of the Settings page: model prices, secrets, custom providers
// and their models, annotation fields (with a live preview).
import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { api } from '../../api/client'
import { parseRef } from '../../api/refs'
import type { Any, PriceEntry, SecretEntry, SecretScope, WeftObject } from '../../api/types'
import { ErrorBox, Modal } from '../../components/common'
import { ConfirmButton } from '../../components/objects/bits'
import { NameField, usePublish } from '../../components/objects/ObjectForms'
import { annotationSpecNames, classVal, errMsg, refOf } from '../../components/objects/objectUtils'

const PER_M = 1_000_000

export function CostModal({ projectId, entry, override, onClose, onSaved }: { projectId: string; entry?: PriceEntry; override: boolean; onClose: () => void; onSaved: () => void }) {
  const models = useQuery({ queryKey: ['llm-models'], queryFn: () => api.llmModels().then((r) => r.models) })
  const [llmId, setLlmId] = useState(entry?.llm_id ?? '')
  const [provider, setProvider] = useState(entry?.provider_id ?? '')
  const [inPrice, setIn] = useState(entry ? String(+(entry.prompt_token_cost * PER_M).toPrecision(6)) : '')
  const [outPrice, setOut] = useState(entry ? String(+(entry.completion_token_cost * PER_M).toPrecision(6)) : '')
  const perMOrBlank = (v: number | null | undefined) => (v == null ? '' : String(+(v * PER_M).toPrecision(6)))
  const [cacheRead, setCacheRead] = useState(perMOrBlank(entry?.cache_read_input_token_cost))
  const [cacheWrite, setCacheWrite] = useState(perMOrBlank(entry?.cache_creation_input_token_cost))
  const optPrice = (v: string) => (v.trim() === '' ? undefined : Number(v) / PER_M)
  const optValid = (v: string) => v.trim() === '' || (!Number.isNaN(Number(v)) && Number(v) >= 0)
  const [date, setDate] = useState(new Date().toISOString().slice(0, 10))
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<unknown>(null)
  const isEdit = !!entry && !override
  const valid =
    llmId && inPrice !== '' && outPrice !== '' && !Number.isNaN(Number(inPrice)) && !Number.isNaN(Number(outPrice)) && optValid(cacheRead) && optValid(cacheWrite)
  return (
    <Modal
      title={isEdit ? `Edit price for ${entry!.llm_id}` : override ? `Set a project price for ${entry!.llm_id}` : 'Add model price'}
      onClose={onClose}
      footer={
        <>
          <button type="button" className="qbtn" onClick={onClose}>
            Cancel
          </button>
          <button
            className="qbtn primary"
            disabled={!valid || busy}
            onClick={async () => {
              setBusy(true)
              setErr(null)
              try {
                await api.pricesCreate(projectId, {
                  [llmId.trim()]: {
                    prompt_token_cost: Number(inPrice) / PER_M,
                    completion_token_cost: Number(outPrice) / PER_M,
                    cache_read_input_token_cost: optPrice(cacheRead),
                    cache_creation_input_token_cost: optPrice(cacheWrite),
                    provider_id: provider || undefined,
                    effective_date: new Date(date + 'T00:00:00Z').toISOString(),
                  },
                })
                if (isEdit) await api.pricesDelete(projectId, [entry!.id])
                onSaved()
              } catch (e) {
                setErr(e)
              } finally {
                setBusy(false)
              }
            }}
          >
            {busy ? 'Saving…' : 'Save price'}
          </button>
        </>
      }
    >
      <div className="form-grid">
        <label className="field">
          Model id
          <input className="input mono" list="cost-models" value={llmId} disabled={!!entry} onChange={(e) => setLlmId(e.target.value)} placeholder="gpt-4o-mini" autoFocus={!entry} />
          <datalist id="cost-models">
            {models.data?.map((m) => (
              <option key={m.id} value={m.id} />
            ))}
          </datalist>
        </label>
        <label className="field">
          Provider
          <input className="input" value={provider} onChange={(e) => setProvider(e.target.value)} placeholder="openai" />
        </label>
        <label className="field">
          Input $ / 1M tokens
          <input className="input mono" inputMode="decimal" value={inPrice} onChange={(e) => setIn(e.target.value)} placeholder="0.15" />
        </label>
        <label className="field">
          Output $ / 1M tokens
          <input className="input mono" inputMode="decimal" value={outPrice} onChange={(e) => setOut(e.target.value)} placeholder="0.60" />
        </label>
        <label className="field">
          Cache read $ / 1M tokens
          <input className="input mono" inputMode="decimal" value={cacheRead} onChange={(e) => setCacheRead(e.target.value)} placeholder="blank = input price" />
        </label>
        <label className="field">
          Cache write $ / 1M tokens
          <input className="input mono" inputMode="decimal" value={cacheWrite} onChange={(e) => setCacheWrite(e.target.value)} placeholder="blank = input price" />
        </label>
        <label className="field">
          Effective from
          <input className="input" type="date" value={date} onChange={(e) => setDate(e.target.value)} />
        </label>
      </div>
      {valid && (
        <div className="hint mono">
          Stored per token: input {(Number(inPrice) / PER_M).toExponential(3)}, output {(Number(outPrice) / PER_M).toExponential(3)}
          {cacheRead.trim() !== '' && `, cache read ${(Number(cacheRead) / PER_M).toExponential(3)}`}
          {cacheWrite.trim() !== '' && `, cache write ${(Number(cacheWrite) / PER_M).toExponential(3)}`} USD. Cached tokens are part of the input:
          cost = (input − cache read − cache write) × input price + cache read × cache read price + cache write × cache write price + output × output price.
        </div>
      )}
      <ErrorBox error={err} />
    </Modal>
  )
}

/** Splits "a.example.com, *.corp.example:8443" into entries. */
export function parseAllowedHosts(text: string): string[] {
  return text
    .split(/[\s,]+/)
    .map((h) => h.trim())
    .filter(Boolean)
}

export function SecretModal({
  name: initialName,
  projectId,
  serverAdmin,
  teamAdmin,
  current,
  onClose,
  onSaved,
}: {
  name: string
  projectId: string
  serverAdmin: boolean
  teamAdmin: boolean
  /** The secret now used for this name, if any (its scope is the default). */
  current?: SecretEntry
  onClose: () => void
  onSaved: () => void
}) {
  const team = projectId.split('/')[0]
  const scopes: { id: SecretScope; label: string; hint: string }[] = [
    { id: 'project', label: `This project (${projectId})`, hint: 'Used only by this project.' },
    ...(teamAdmin
      ? [
          {
            id: 'team' as const,
            label: `Team ${team}`,
            hint: `Used by the built-in providers of every project of team ${team}. Projects’ own providers, scorers and automations can use it only toward its allowed hosts.`,
          },
        ]
      : []),
    ...(serverAdmin
      ? [
          {
            id: 'server' as const,
            label: 'Server',
            hint: 'Used by the built-in providers of every project. Projects’ own providers, scorers and automations can use it only toward its allowed hosts.',
          },
        ]
      : []),
  ]
  const initialScope: SecretScope =
    current && current.source === 'db' && scopes.some((x) => x.id === current.scope) ? current.scope : 'project'
  const [name, setName] = useState(initialName)
  const [value, setValue] = useState('')
  const [scope, setScope] = useState<SecretScope>(initialScope)
  const [hosts, setHosts] = useState((current?.source === 'db' && current.scope === initialScope ? current.allowed_hosts : []).join(', '))
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<unknown>(null)
  const chosen = scopes.find((x) => x.id === scope) ?? scopes[0]
  return (
    <Modal
      title={initialName ? `Set ${initialName}` : 'Add secret'}
      onClose={onClose}
      footer={
        <>
          <button type="button" className="qbtn" onClick={onClose}>
            Cancel
          </button>
          <button
            className="qbtn primary"
            disabled={!name || !value || busy}
            onClick={async () => {
              setBusy(true)
              try {
                await api.secretsSet({
                  name: name.trim(),
                  value,
                  scope,
                  project_id: scope === 'project' ? projectId : undefined,
                  team: scope === 'team' ? team : undefined,
                  allowed_hosts: parseAllowedHosts(hosts),
                })
                onSaved()
              } catch (e) {
                setErr(e)
              } finally {
                setBusy(false)
              }
            }}
          >
            Save
          </button>
        </>
      }
    >
      {!initialName && (
        <label className="field">
          Name
          <input className="input mono" value={name} onChange={(e) => setName(e.target.value.toUpperCase().replace(/[^A-Z0-9_]/g, '_'))} placeholder="MY_PROVIDER_KEY" autoFocus />
        </label>
      )}
      <label className="field">
        Value
        <input className="input mono" type="password" autoComplete="off" value={value} onChange={(e) => setValue(e.target.value)} placeholder="sk-…" autoFocus={!!initialName} />
        <span className="hint">Stored encrypted on the server. It is never shown again.</span>
      </label>
      <label className="field">
        Scope
        <select className="input" value={scope} onChange={(e) => setScope(e.target.value as SecretScope)} aria-label="Scope">
          {scopes.map((x) => (
            <option key={x.id} value={x.id}>
              {x.label}
            </option>
          ))}
        </select>
        <span className="hint">{chosen.hint} A name set in a narrower scope (project, then team) takes precedence.</span>
      </label>
      <label className="field">
        Allowed hosts
        <input className="input mono" value={hosts} onChange={(e) => setHosts(e.target.value)} placeholder="api.example.com, *.corp.example" aria-label="Allowed hosts" />
        <span className="hint">
          Optional. The secret is only sent to these hosts (<span className="mono">host</span>, <span className="mono">host:port</span> or{' '}
          <span className="mono">*.domain</span>); for a webhook URL, its own host. Empty: any host for a project secret. Secrets are only sent over https
          (or to localhost).
        </span>
      </label>
      {scope !== 'project' && parseAllowedHosts(hosts).length === 0 && (
        <p className="hint warn" role="note">
          Without allowed hosts, a {scope} secret is used only by the built-in providers: projects’ own providers, scorers, predict models, OAuth
          configurations and automations cannot send it anywhere. Add the hosts they call.
        </p>
      )}
      <ErrorBox error={err} />
    </Modal>
  )
}

export function ProviderModal({ projectId, initial, onClose, onSaved }: { projectId: string; initial?: WeftObject; onClose: () => void; onSaved: () => void }) {
  const publish = usePublish(projectId)
  const v = initial?.value ?? {}
  const [name, setName] = useState(initial?.name ?? '')
  const [baseUrl, setBaseUrl] = useState<string>(v.base_url ?? 'http://localhost:11434/v1')
  const [keyName, setKeyName] = useState<string>(v.api_key_name ?? '')
  const [headers, setHeaders] = useState<string>(v.extra_headers && Object.keys(v.extra_headers).length ? JSON.stringify(v.extra_headers, null, 2) : '')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<unknown>(null)
  let headersErr: string | null = null
  try {
    if (headers.trim()) {
      const h = JSON.parse(headers)
      if (!h || typeof h !== 'object' || Array.isArray(h)) headersErr = 'Must be a JSON object'
    }
  } catch (e) {
    headersErr = errMsg(e)
  }
  return (
    <Modal
      title={initial ? `Edit provider ${initial.name}` : 'Add OpenAI-compatible provider'}
      onClose={onClose}
      footer={
        <>
          <button type="button" className="qbtn" onClick={onClose}>
            Cancel
          </button>
          <button
            className="qbtn primary"
            disabled={!name || !baseUrl || !!headersErr || busy}
            onClick={async () => {
              setBusy(true)
              try {
                await publish(name, {
                  ...v,
                  ...classVal('Provider', [], { name, base_url: baseUrl.trim(), api_key_name: keyName.trim(), extra_headers: headers.trim() ? JSON.parse(headers) : {}, return_type: 'openai' }),
                })
                onSaved()
              } catch (e) {
                setErr(e)
              } finally {
                setBusy(false)
              }
            }}
          >
            Save provider
          </button>
        </>
      }
    >
      <NameField value={name} onChange={setName} locked={!!initial} placeholder="local" label="Name (used as model prefix)" />
      <label className="field">
        Base URL
        <input className="input mono" value={baseUrl} onChange={(e) => setBaseUrl(e.target.value)} placeholder="https://my-gateway.example.com/v1" />
      </label>
      <label className="field">
        API key secret name
        <input className="input mono" value={keyName} onChange={(e) => setKeyName(e.target.value.toUpperCase().replace(/[^A-Z0-9_]/g, '_'))} placeholder="LOCAL_LLM_KEY" />
        <span className="hint">The secret holding the key. Set its value under Provider API keys.</span>
      </label>
      <label className="field">
        Extra headers (JSON, optional)
        <textarea className="input mono" rows={3} value={headers} onChange={(e) => setHeaders(e.target.value)} placeholder='{"X-Org": "research"}' />
        {headersErr && <span className="hint" style={{ color: 'var(--error)' }}>{headersErr}</span>}
      </label>
      <ErrorBox error={err} />
    </Modal>
  )
}

export function ProviderModelModal({
  projectId,
  providers,
  initial,
  onClose,
  onSaved,
}: {
  projectId: string
  providers: WeftObject[]
  initial?: WeftObject
  onClose: () => void
  onSaved: () => void
}) {
  const publish = usePublish(projectId)
  const v = initial?.value ?? {}
  const [objectId, setObjectId] = useState(initial?.name ?? '')
  const [modelName, setModelName] = useState<string>(v.name ?? '')
  const [provider, setProvider] = useState<string>(v.provider ?? (providers[0] ? refOf(providers[0]) : ''))
  const [maxTokens, setMaxTokens] = useState<number>(v.max_tokens ?? 4096)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<unknown>(null)
  const providerName = parseRef(provider)?.name
  return (
    <Modal
      title={initial ? `Edit provider model ${initial.name}` : 'Add provider model'}
      onClose={onClose}
      footer={
        <>
          {initial && (
            <ConfirmButton
              className="qbtn danger"
              title={`Delete ${initial.name}?`}
              body="The model will no longer be listed."
              onConfirm={async () => {
                await api.objectDelete(projectId, initial.name)
                onSaved()
              }}
            >
              Delete
            </ConfirmButton>
          )}
          <span className="grow" />
          <button type="button" className="qbtn" onClick={onClose}>
            Cancel
          </button>
          <button
            className="qbtn primary"
            disabled={!objectId || !modelName || !provider || busy}
            onClick={async () => {
              setBusy(true)
              try {
                await publish(objectId, { ...v, ...classVal('ProviderModel', [], { name: modelName.trim(), provider, max_tokens: maxTokens }) })
                onSaved()
              } catch (e) {
                setErr(e)
              } finally {
                setBusy(false)
              }
            }}
          >
            Save model
          </button>
        </>
      }
    >
      <div className="form-grid">
        <label className="field">
          Provider
          <select className="input" value={provider} onChange={(e) => setProvider(e.target.value)}>
            {providers.map((p) => (
              <option key={p.name} value={refOf(p)}>
                {p.name}
              </option>
            ))}
          </select>
        </label>
        <label className="field">
          Model name at the provider
          <input
            className="input mono"
            value={modelName}
            onChange={(e) => {
              setModelName(e.target.value)
              if (!initial) setObjectId(`${providerName ?? 'provider'}-${e.target.value}`.replace(/[^A-Za-z0-9_.-]/g, '-'))
            }}
            placeholder="llama3.1:8b"
          />
        </label>
        <NameField value={objectId} onChange={setObjectId} locked={!!initial} label="Object name" placeholder="local-llama3" />
        <label className="field">
          Max tokens
          <input className="input" type="number" min={1} value={maxTokens} onChange={(e) => setMaxTokens(Number(e.target.value) || 1)} />
        </label>
      </div>
      {providerName && modelName && (
        <div className="hint">
          Use as <span className="mono">{providerName}/{modelName}</span> in the playground and in LLM completion models.
        </div>
      )}
      <ErrorBox error={err} />
    </Modal>
  )
}

type FieldKind = 'integer' | 'number' | 'enum' | 'boolean' | 'text'

interface SpecDraft {
  kind: FieldKind
  min: string
  max: string
  options: string[]
  maxLength: string
}

export function draftFromSchema(fs: Any): SpecDraft {
  const d: SpecDraft = { kind: 'integer', min: '1', max: '5', options: ['good', 'bad'], maxLength: '' }
  if (!fs) return d
  if (fs.type === 'boolean') return { ...d, kind: 'boolean' }
  if (fs.type === 'string' && Array.isArray(fs.enum)) return { ...d, kind: 'enum', options: fs.enum.map(String) }
  if (fs.type === 'string') return { ...d, kind: 'text', maxLength: fs.maxLength != null ? String(fs.maxLength) : '' }
  if (fs.type === 'number' || fs.type === 'integer') return { ...d, kind: fs.type, min: fs.minimum != null ? String(fs.minimum) : '', max: fs.maximum != null ? String(fs.maximum) : '' }
  return d
}

function schemaFromDraft(d: SpecDraft): Any {
  switch (d.kind) {
    case 'boolean':
      return { type: 'boolean' }
    case 'enum':
      return { type: 'string', enum: d.options.map((o) => o.trim()).filter(Boolean) }
    case 'text':
      return d.maxLength ? { type: 'string', maxLength: Number(d.maxLength) } : { type: 'string' }
    default: {
      const s: Any = { type: d.kind }
      if (d.min !== '') s.minimum = Number(d.min)
      if (d.max !== '') s.maximum = Number(d.max)
      return s
    }
  }
}

export function describeSchema(fs: Any): string {
  if (!fs) return '—'
  if (fs.type === 'boolean') return 'Yes / No'
  if (fs.type === 'string' && Array.isArray(fs.enum)) return `One of: ${fs.enum.join(', ')}`
  if (fs.type === 'string') return fs.maxLength ? `Text (max ${fs.maxLength})` : 'Text'
  if (fs.type === 'integer' || fs.type === 'number') return `${fs.type === 'integer' ? 'Integer' : 'Number'} ${fs.minimum ?? '−∞'} – ${fs.maximum ?? '∞'}`
  return JSON.stringify(fs)
}

export function AnnotationSpecModal({ projectId, initial, onClose }: { projectId: string; initial?: WeftObject; onClose: () => void }) {
  const publish = usePublish(projectId)
  const v = initial?.value ?? {}
  const [name, setName] = useState(initial?.name ?? '')
  const [description, setDescription] = useState<string>(v.description ?? '')
  const [draft, setDraft] = useState<SpecDraft>(draftFromSchema(v.field_schema))
  const [unique, setUnique] = useState<boolean>(v.unique_among_creators ?? true)
  const [scope, setScope] = useState<string>(annotationSpecNames(v).join(', '))
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<unknown>(null)
  const set = (p: Partial<SpecDraft>) => setDraft({ ...draft, ...p })
  const schema = schemaFromDraft(draft)
  const valid = name && (draft.kind !== 'enum' || schema.enum.length >= 2) && (draft.kind === 'integer' || draft.kind === 'number' ? draft.min === '' || draft.max === '' || Number(draft.min) <= Number(draft.max) : true)
  return (
    <Modal
      title={initial ? `Edit annotation field ${initial.name}` : 'New annotation field'}
      onClose={onClose}
      footer={
        <>
          <button type="button" className="qbtn" onClick={onClose}>
            Cancel
          </button>
          <button
            className="qbtn primary"
            disabled={!valid || busy}
            onClick={async () => {
              setBusy(true)
              try {
                const opScope = scope
                  .split(',')
                  .map((s) => s.trim())
                  .filter(Boolean)
                const { op_scope: _old, ...rest } = v // the earlier name of names
                await publish(name, {
                  ...rest,
                  ...classVal('AnnotationSpec', [], { name, description, field_schema: schema, unique_among_creators: unique, names: opScope.length ? opScope : null }),
                })
                onClose()
              } catch (e) {
                setErr(e)
              } finally {
                setBusy(false)
              }
            }}
          >
            Save field
          </button>
        </>
      }
    >
      <div className="form-grid">
        <NameField value={name} onChange={setName} locked={!!initial} placeholder="quality" />
        <label className="field">
          Type
          <select className="input" value={draft.kind} onChange={(e) => set({ kind: e.target.value as FieldKind })}>
            <option value="integer">Integer range</option>
            <option value="number">Number range</option>
            <option value="enum">Choice (enum)</option>
            <option value="boolean">Yes / No</option>
            <option value="text">Free text</option>
          </select>
        </label>
      </div>
      <label className="field">
        Description / instructions
        <input className="input" value={description} onChange={(e) => setDescription(e.target.value)} placeholder="How good is the answer overall?" />
      </label>
      {(draft.kind === 'integer' || draft.kind === 'number') && (
        <div className="form-grid">
          <label className="field">
            Minimum
            <input className="input" type="number" value={draft.min} onChange={(e) => set({ min: e.target.value })} />
          </label>
          <label className="field">
            Maximum
            <input className="input" type="number" value={draft.max} onChange={(e) => set({ max: e.target.value })} />
          </label>
        </div>
      )}
      {draft.kind === 'enum' && (
        <div className="col" style={{ gap: 6 }}>
          <span className="small muted">Options</span>
          {draft.options.map((o, i) => (
            <div key={i} className="row">
              <input className="input grow" aria-label={`Option ${i + 1}`} value={o} onChange={(e) => set({ options: draft.options.map((x, j) => (j === i ? e.target.value : x)) })} />
              <button type="button" className="qbtn sm plain" aria-label={`Remove option ${i + 1}`} onClick={() => set({ options: draft.options.filter((_, j) => j !== i) })} disabled={draft.options.length <= 1}>
                ✕
              </button>
            </div>
          ))}
          <button className="qbtn sm" style={{ alignSelf: 'flex-start' }} onClick={() => set({ options: [...draft.options, ''] })}>
            + Option
          </button>
        </div>
      )}
      {draft.kind === 'text' && (
        <label className="field">
          Max length (optional)
          <input className="input" type="number" min={1} value={draft.maxLength} onChange={(e) => set({ maxLength: e.target.value })} />
        </label>
      )}
      <label className="checkbox">
        <input type="checkbox" checked={unique} onChange={(e) => setUnique(e.target.checked)} /> One value per reviewer (replace instead of adding)
      </label>
      <label className="field">
        Only for these steps (optional; span names, comma separated)
        <input className="input mono" value={scope} onChange={(e) => setScope(e.target.value)} placeholder="answer_question, openai.chat" />
      </label>
      <div style={{ background: 'var(--panel-2)', borderRadius: 10, padding: '12px 14px' }}>
        <div className="col" style={{ gap: 6 }}>
          <span className="small muted">Preview</span>
          <b className="small">{name || 'field'}</b>
          {description && <span className="hint">{description}</span>}
          <SpecPreview schema={schema} />
          <code className="faint small">{JSON.stringify(schema)}</code>
        </div>
      </div>
      <ErrorBox error={err} />
    </Modal>
  )
}

function SpecPreview({ schema }: { schema: Any }) {
  const [val, setVal] = useState<Any>(null)
  if (schema.type === 'boolean')
    return (
      <div className="row">
        <button className={`qbtn sm ${val === true ? 'primary' : ''}`} onClick={() => setVal(true)}>
          Yes
        </button>
        <button className={`qbtn sm ${val === false ? 'primary' : ''}`} onClick={() => setVal(false)}>
          No
        </button>
      </div>
    )
  if (schema.type === 'string' && Array.isArray(schema.enum))
    return (
      <div className="chips">
        {schema.enum.map((o: string) => (
          <button key={o} className={`chip selectable ${val === o ? 'on' : ''}`} onClick={() => setVal(o)}>
            {o}
          </button>
        ))}
      </div>
    )
  if (schema.type === 'string') return <textarea className="input" rows={2} maxLength={schema.maxLength} placeholder="Reviewer notes…" />
  const span = schema.maximum != null && schema.minimum != null ? schema.maximum - schema.minimum : null
  if (schema.type === 'integer' && span != null && span >= 0 && span <= 10)
    return (
      <div className="row wrap">
        {Array.from({ length: span + 1 }, (_, i) => schema.minimum + i).map((n: number) => (
          <button key={n} className={`qbtn sm ${val === n ? 'primary' : ''}`} onClick={() => setVal(n)}>
            {n}
          </button>
        ))}
      </div>
    )
  return <input className="input" type="number" min={schema.minimum} max={schema.maximum} step={schema.type === 'integer' ? 1 : 'any'} style={{ width: 140 }} />
}

