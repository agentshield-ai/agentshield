import { useMemo, useState } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { api, ApiError, getApiKey, setApiKey } from '../../api/client'
import { parseRef } from '../../api/refs'
import type { Any, CostEntry, ObjSchema } from '../../api/types'
import { Empty, ErrorBox, Loading, Modal, Section, Spinner, Tabs } from '../../components/common'
import { CodeBlock, ConfirmButton, Stat } from '../../components/objects/bits'
import { NameField, usePublish } from '../../components/objects/ObjectForms'
import { classVal, errMsg, refOf, useObjects } from '../../components/objects/objectUtils'
import { useProjectId } from '../../hooks/useProject'
import { fmtBytes, fmtNumber, fmtRelative } from '../../lib/format'
import { objectUrl } from '../../lib/urls'
import { ApiKeysTab, DeleteProjectTab, RetentionTab, SamplingTab, SensitiveDataTab } from './AdminTabs'
import { AuditLogTab, MembersTab } from './IamTabs'

type Tab = 'general' | 'costs' | 'secrets' | 'annotations' | 'integrations' | 'apikeys' | 'members' | 'audit' | 'retention' | 'sampling' | 'sensitive' | 'delete'
const TABS: { id: Tab; label: string }[] = [
  { id: 'general', label: 'General' },
  { id: 'costs', label: 'Costs' },
  { id: 'secrets', label: 'Secrets & providers' },
  { id: 'annotations', label: 'Annotations' },
  { id: 'integrations', label: 'Integrations' },
  { id: 'apikeys', label: 'API keys' },
  { id: 'members', label: 'Members' },
  { id: 'audit', label: 'Audit log' },
  { id: 'retention', label: 'Retention' },
  { id: 'sampling', label: 'Sampling' },
  { id: 'sensitive', label: 'Sensitive data' },
  { id: 'delete', label: 'Delete project' },
]

export function SettingsPage() {
  const projectId = useProjectId()
  const [params, setParams] = useSearchParams()
  const raw = params.get('tab') === 'apikey' ? 'apikeys' : params.get('tab') // old link
  const tab = (TABS.some((t) => t.id === raw) ? raw : 'general') as Tab
  return (
    <div className="page">
      <div className="page-header">
        <h1>Settings</h1>
        <span className="muted small mono">{projectId}</span>
      </div>
      <div className="tabs-scroll">
        <Tabs<Tab> tabs={TABS} value={tab} onChange={(t) => setParams({ tab: t }, { replace: true })} />
      </div>
      {tab === 'general' && <GeneralTab projectId={projectId} />}
      {tab === 'costs' && <CostsTab projectId={projectId} />}
      {tab === 'secrets' && <SecretsTab projectId={projectId} />}
      {tab === 'annotations' && <AnnotationsTab projectId={projectId} />}
      {tab === 'integrations' && <IntegrationsTab projectId={projectId} />}
      {tab === 'apikeys' && (
        <div className="col" style={{ gap: 12 }}>
          <ApiKeyTab />
          <ApiKeysTab projectId={projectId} />
        </div>
      )}
      {tab === 'members' && <MembersTab projectId={projectId} />}
      {tab === 'audit' && <AuditLogTab projectId={projectId} />}
      {tab === 'retention' && <RetentionTab projectId={projectId} />}
      {tab === 'sampling' && <SamplingTab projectId={projectId} />}
      {tab === 'sensitive' && <SensitiveDataTab projectId={projectId} />}
      {tab === 'delete' && <DeleteProjectTab projectId={projectId} />}
    </div>
  )
}

// ================================================================ general

function GeneralTab({ projectId }: { projectId: string }) {
  const stats = useQuery({ queryKey: ['project-stats', projectId], queryFn: () => api.projectStats(projectId) })
  const info = useQuery({ queryKey: ['server-info'], queryFn: api.serverInfo })
  const s = stats.data
  return (
    <div className="col" style={{ gap: 16 }}>
      <Section title="Project">
        <ErrorBox error={stats.error} />
        {stats.isLoading ? (
          <Spinner />
        ) : s ? (
          <div className="stat-grid">
            <Stat label="Calls" value={fmtNumber(s.call_count)} />
            <Stat label="Traces" value={fmtNumber(s.trace_count)} />
            <Stat label="Objects" value={fmtNumber(s.object_count)} />
            <Stat label="Tables" value={fmtNumber(s.table_count)} />
            <Stat label="Files" value={fmtNumber(s.file_count)} />
            <Stat label="Feedback" value={fmtNumber(s.feedback_count)} />
            <Stat label="Storage" value={fmtBytes(s.storage_size_bytes)} />
          </div>
        ) : null}
      </Section>
      <Section title="Server">
        <ErrorBox error={info.error} />
        {info.data && (
          <div className="kv">
            <div className="k">version</div>
            <div className="v mono">{info.data.version}</div>
            <div className="k">auth</div>
            <div className="v">{info.data.auth_required ? <span className="badge warn">API key required</span> : <span className="badge">open (no API keys configured)</span>}</div>
            <div className="k">OTLP endpoint</div>
            <div className="v mono">
              {window.location.origin}
              {info.data.otel_endpoint}
            </div>
            <div className="k">project id</div>
            <div className="v mono">{projectId}</div>
          </div>
        )}
      </Section>
    </div>
  )
}

// ================================================================ costs

const PER_M = 1_000_000
const fmtPerM = (perToken: number) => {
  const v = perToken * PER_M
  return v === 0 ? '$0' : v < 0.01 ? `$${v.toFixed(4)}` : `$${v.toFixed(v < 10 ? 3 : 2)}`
}

function CostsTab({ projectId }: { projectId: string }) {
  const qc = useQueryClient()
  const costs = useQuery({ queryKey: ['costs', projectId], queryFn: () => api.costQuery(projectId, true).then((r) => r.results) })
  const [search, setSearch] = useState('')
  const [level, setLevel] = useState<'' | 'project' | 'default'>('')
  const [editing, setEditing] = useState<{ entry?: CostEntry; llmId?: string } | null>(null)
  const rows = useMemo(() => {
    const q = search.toLowerCase()
    return (costs.data ?? [])
      .filter((c) => (!q || c.llm_id.toLowerCase().includes(q) || c.provider_id?.toLowerCase().includes(q)) && (!level || c.pricing_level === level))
      .sort((a, b) => (a.pricing_level === b.pricing_level ? a.llm_id.localeCompare(b.llm_id) : a.pricing_level === 'project' ? -1 : 1))
  }, [costs.data, search, level])
  const overridden = new Set((costs.data ?? []).filter((c) => c.pricing_level === 'project').map((c) => c.llm_id))
  return (
    <div className="col" style={{ gap: 12 }}>
      <div className="toolbar">
        <span className="muted small grow">Prices are per 1M tokens in USD. Project prices override the built-in defaults for cost calculation.</span>
        <input className="input" placeholder="Search models…" value={search} onChange={(e) => setSearch(e.target.value)} style={{ width: 200, maxWidth: '100%' }} />
        <select className="input" value={level} onChange={(e) => setLevel(e.target.value as typeof level)}>
          <option value="">All prices</option>
          <option value="project">Project</option>
          <option value="default">Built-in</option>
        </select>
        <button className="btn primary" onClick={() => setEditing({})}>
          + Add price
        </button>
      </div>
      <ErrorBox error={costs.error} />
      {costs.isLoading ? (
        <Loading />
      ) : rows.length === 0 ? (
        <div className="card">
          <Empty>No prices match.</Empty>
        </div>
      ) : (
        <div className="table-wrap" style={{ maxHeight: '65vh' }}>
          <table className="table">
            <thead>
              <tr>
                <th>Model</th>
                <th>Provider</th>
                <th className="num">Input / 1M</th>
                <th className="num">Output / 1M</th>
                <th>Effective</th>
                <th>Level</th>
                <th style={{ width: 130 }} />
              </tr>
            </thead>
            <tbody>
              {rows.map((c) => (
                <tr key={c.id} style={c.pricing_level === 'default' && overridden.has(c.llm_id) ? { opacity: 0.5 } : undefined}>
                  <td className="mono">{c.llm_id}</td>
                  <td className="muted">{c.provider_id || '—'}</td>
                  <td className="num mono">{fmtPerM(c.prompt_token_cost)}</td>
                  <td className="num mono">{fmtPerM(c.completion_token_cost)}</td>
                  <td className="muted small">{c.effective_date ? new Date(c.effective_date).toLocaleDateString() : '—'}</td>
                  <td>
                    <span className={`badge ${c.pricing_level === 'project' ? 'accent' : ''}`}>{c.pricing_level === 'project' ? 'project' : 'built-in'}</span>
                  </td>
                  <td>
                    <div className="row" style={{ justifyContent: 'flex-end' }}>
                      {c.pricing_level === 'project' ? (
                        <>
                          <button className="btn sm" onClick={() => setEditing({ entry: c })}>
                            Edit
                          </button>
                          <ConfirmButton
                            className="btn sm ghost danger"
                            title={`Delete project price for ${c.llm_id}?`}
                            body="Costs fall back to the built-in price, if there is one."
                            onConfirm={async () => {
                              await api.costPurge(projectId, [c.id])
                              await qc.invalidateQueries({ queryKey: ['costs', projectId] })
                            }}
                          >
                            Delete
                          </ConfirmButton>
                        </>
                      ) : (
                        <button className="btn sm ghost" onClick={() => setEditing({ entry: c, llmId: c.llm_id })}>
                          Override
                        </button>
                      )}
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {editing && (
        <CostModal
          projectId={projectId}
          entry={editing.entry}
          override={!!editing.llmId}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null)
            void qc.invalidateQueries({ queryKey: ['costs', projectId] })
          }}
        />
      )}
    </div>
  )
}

function CostModal({ projectId, entry, override, onClose, onSaved }: { projectId: string; entry?: CostEntry; override: boolean; onClose: () => void; onSaved: () => void }) {
  const models = useQuery({ queryKey: ['llm-models'], queryFn: () => api.llmModels().then((r) => r.models) })
  const [llmId, setLlmId] = useState(entry?.llm_id ?? '')
  const [provider, setProvider] = useState(entry?.provider_id ?? '')
  const [inPrice, setIn] = useState(entry ? String(+(entry.prompt_token_cost * PER_M).toPrecision(6)) : '')
  const [outPrice, setOut] = useState(entry ? String(+(entry.completion_token_cost * PER_M).toPrecision(6)) : '')
  const [date, setDate] = useState(new Date().toISOString().slice(0, 10))
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<unknown>(null)
  const isEdit = !!entry && !override
  const valid = llmId && inPrice !== '' && outPrice !== '' && !Number.isNaN(Number(inPrice)) && !Number.isNaN(Number(outPrice))
  return (
    <Modal
      title={isEdit ? `Edit price for ${entry!.llm_id}` : override ? `Override price for ${entry!.llm_id}` : 'Add model price'}
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
                await api.costCreate(projectId, {
                  [llmId.trim()]: {
                    prompt_token_cost: Number(inPrice) / PER_M,
                    completion_token_cost: Number(outPrice) / PER_M,
                    provider_id: provider || undefined,
                    effective_date: new Date(date + 'T00:00:00Z').toISOString(),
                  },
                })
                if (isEdit) await api.costPurge(projectId, [entry!.id])
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
          Effective from
          <input className="input" type="date" value={date} onChange={(e) => setDate(e.target.value)} />
        </label>
      </div>
      {valid && (
        <div className="hint mono">
          Stored per token: input {(Number(inPrice) / PER_M).toExponential(3)}, output {(Number(outPrice) / PER_M).toExponential(3)} USD
        </div>
      )}
      <ErrorBox error={err} />
    </Modal>
  )
}

// ================================================================ secrets & providers

const KNOWN_SECRETS = [
  { name: 'OPENAI_API_KEY', label: 'OpenAI' },
  { name: 'ANTHROPIC_API_KEY', label: 'Anthropic' },
  { name: 'GEMINI_API_KEY', label: 'Google Gemini' },
  { name: 'MISTRAL_API_KEY', label: 'Mistral' },
  { name: 'GROQ_API_KEY', label: 'Groq' },
]

function SecretsTab({ projectId }: { projectId: string }) {
  const qc = useQueryClient()
  const secrets = useQuery({ queryKey: ['secrets'], queryFn: () => api.secretsList().then((r) => r.secrets) })
  const providers = useObjects(projectId, { base_object_classes: ['Provider'] })
  const providerModels = useObjects(projectId, { base_object_classes: ['ProviderModel'] })
  const [setting, setSetting] = useState<string | null>(null)
  const [providerEdit, setProviderEdit] = useState<{ initial?: ObjSchema } | null>(null)
  const [pmEdit, setPmEdit] = useState<{ initial?: ObjSchema } | null>(null)
  const byName = new Map((secrets.data ?? []).map((s) => [s.name, s]))
  const providerKeyNames = (providers.data ?? []).map((p) => p.val?.api_key_name).filter(Boolean) as string[]
  const names = [...new Set([...KNOWN_SECRETS.map((k) => k.name), ...providerKeyNames, ...(secrets.data ?? []).map((s) => s.name)])]
  const refresh = () => qc.invalidateQueries({ queryKey: ['secrets'] }).then(() => qc.invalidateQueries({ queryKey: ['llm-models'] }))
  const qcObjs = () => qc.invalidateQueries({ queryKey: ['objs', projectId] })
  return (
    <div className="col" style={{ gap: 16 }}>
      <Section
        title="Provider API keys"
        actions={
          <button className="btn sm" onClick={() => setSetting('')}>
            + Custom secret
          </button>
        }
      >
        <div className="hint" style={{ marginBottom: 8 }}>
          Used by the playground, server-side evaluations and LLM judges. Values are write-only; keys set via environment variables can’t be changed here.
        </div>
        <ErrorBox error={secrets.error} />
        {secrets.isLoading ? (
          <Spinner />
        ) : (
          <div className="table-wrap">
            <table className="table">
              <thead>
                <tr>
                  <th>Name</th>
                  <th>Provider</th>
                  <th>Status</th>
                  <th style={{ width: 150 }} />
                </tr>
              </thead>
              <tbody>
                {names.map((n) => {
                  const s = byName.get(n)
                  const known = KNOWN_SECRETS.find((k) => k.name === n)
                  return (
                    <tr key={n}>
                      <td className="mono">{n}</td>
                      <td className="muted">{known?.label ?? (providerKeyNames.includes(n) ? 'custom provider' : '—')}</td>
                      <td>{s ? <span className="badge success">set · {s.source === 'env' ? 'environment' : 'stored'}</span> : <span className="badge">not set</span>}</td>
                      <td>
                        <div className="row" style={{ justifyContent: 'flex-end' }}>
                          {s?.source !== 'env' && (
                            <button className="btn sm" onClick={() => setSetting(n)}>
                              {s ? 'Replace' : 'Set'}
                            </button>
                          )}
                          {s?.source === 'db' && (
                            <ConfirmButton
                              className="btn sm ghost danger"
                              title={`Delete ${n}?`}
                              body="Features that use this provider will stop working until a new key is set."
                              onConfirm={async () => {
                                await api.secretsDelete(n)
                                await refresh()
                              }}
                            >
                              Delete
                            </ConfirmButton>
                          )}
                        </div>
                      </td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        )}
      </Section>

      <Section
        title="Custom providers"
        actions={
          <button className="btn sm" onClick={() => setProviderEdit({})}>
            + Add provider
          </button>
        }
      >
        <div className="hint" style={{ marginBottom: 8 }}>
          Any OpenAI-compatible endpoint (vLLM, Ollama, LM Studio, gateways). Use models as <span className="mono">&lt;provider&gt;/&lt;model&gt;</span>.
        </div>
        <ErrorBox error={providers.error} />
        {providers.isLoading ? (
          <Spinner />
        ) : !providers.data?.length ? (
          <div className="faint small">No custom providers.</div>
        ) : (
          <div className="table-wrap">
            <table className="table">
              <thead>
                <tr>
                  <th>Name</th>
                  <th>Base URL</th>
                  <th>API key secret</th>
                  <th>Models</th>
                  <th style={{ width: 130 }} />
                </tr>
              </thead>
              <tbody>
                {providers.data.map((p) => {
                  const pms = (providerModels.data ?? []).filter((m) => parseRef(m.val?.provider ?? '')?.name === p.object_id)
                  return (
                    <tr key={p.object_id}>
                      <td>
                        <Link to={objectUrl(projectId, p.object_id, p.digest)} className="mono">
                          {p.object_id}
                        </Link>
                      </td>
                      <td className="mono small">{p.val?.base_url}</td>
                      <td className="mono small">
                        {p.val?.api_key_name || '—'} {p.val?.api_key_name && !byName.has(p.val.api_key_name) && <span className="badge warn">missing</span>}
                      </td>
                      <td>
                        <span className="chips">
                          {pms.map((m) => (
                            <button key={m.object_id} className="chip selectable mono" onClick={() => setPmEdit({ initial: m })}>
                              {p.object_id}/{m.val?.name ?? m.object_id}
                            </button>
                          ))}
                        </span>
                      </td>
                      <td>
                        <div className="row" style={{ justifyContent: 'flex-end' }}>
                          <button className="btn sm" onClick={() => setProviderEdit({ initial: p })}>
                            Edit
                          </button>
                          <ConfirmButton
                            className="btn sm ghost danger"
                            title={`Delete provider ${p.object_id}?`}
                            body="Models that reference it will stop working."
                            onConfirm={async () => {
                              await api.objDelete(projectId, p.object_id)
                              await qcObjs()
                            }}
                          >
                            ✕
                          </ConfirmButton>
                        </div>
                      </td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        )}
        {!!providers.data?.length && (
          <div style={{ marginTop: 10 }}>
            <button className="btn sm" onClick={() => setPmEdit({})}>
              + Add provider model
            </button>
          </div>
        )}
      </Section>

      {setting !== null && (
        <SecretModal
          name={setting}
          onClose={() => setSetting(null)}
          onSaved={() => {
            setSetting(null)
            void refresh()
          }}
        />
      )}
      {providerEdit && <ProviderModal projectId={projectId} initial={providerEdit.initial} onClose={() => setProviderEdit(null)} onSaved={() => setProviderEdit(null)} />}
      {pmEdit && (
        <ProviderModelModal projectId={projectId} providers={providers.data ?? []} initial={pmEdit.initial} onClose={() => setPmEdit(null)} onSaved={() => setPmEdit(null)} />
      )}
    </div>
  )
}

function SecretModal({ name: initialName, onClose, onSaved }: { name: string; onClose: () => void; onSaved: () => void }) {
  const [name, setName] = useState(initialName)
  const [value, setValue] = useState('')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<unknown>(null)
  return (
    <Modal
      title={initialName ? `Set ${initialName}` : 'Add secret'}
      onClose={onClose}
      footer={
        <>
          <button className="btn" onClick={onClose}>
            Cancel
          </button>
          <button
            className="btn primary"
            disabled={!name || !value || busy}
            onClick={async () => {
              setBusy(true)
              try {
                await api.secretsSet(name.trim(), value)
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
        <span className="hint">Stored on the server. It is never shown again.</span>
      </label>
      <ErrorBox error={err} />
    </Modal>
  )
}

function ProviderModal({ projectId, initial, onClose, onSaved }: { projectId: string; initial?: ObjSchema; onClose: () => void; onSaved: () => void }) {
  const publish = usePublish(projectId)
  const v = initial?.val ?? {}
  const [name, setName] = useState(initial?.object_id ?? '')
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
      title={initial ? `Edit provider ${initial.object_id}` : 'Add OpenAI-compatible provider'}
      onClose={onClose}
      footer={
        <>
          <button className="btn" onClick={onClose}>
            Cancel
          </button>
          <button
            className="btn primary"
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

function ProviderModelModal({
  projectId,
  providers,
  initial,
  onClose,
  onSaved,
}: {
  projectId: string
  providers: ObjSchema[]
  initial?: ObjSchema
  onClose: () => void
  onSaved: () => void
}) {
  const publish = usePublish(projectId)
  const v = initial?.val ?? {}
  const [objectId, setObjectId] = useState(initial?.object_id ?? '')
  const [modelName, setModelName] = useState<string>(v.name ?? '')
  const [provider, setProvider] = useState<string>(v.provider ?? (providers[0] ? refOf(providers[0]) : ''))
  const [maxTokens, setMaxTokens] = useState<number>(v.max_tokens ?? 4096)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<unknown>(null)
  const providerName = parseRef(provider)?.name
  return (
    <Modal
      title={initial ? `Edit provider model ${initial.object_id}` : 'Add provider model'}
      onClose={onClose}
      footer={
        <>
          {initial && (
            <ConfirmButton
              className="btn danger"
              title={`Delete ${initial.object_id}?`}
              body="The model will no longer be listed."
              onConfirm={async () => {
                await api.objDelete(projectId, initial.object_id)
                onSaved()
              }}
            >
              Delete
            </ConfirmButton>
          )}
          <span className="grow" />
          <button className="btn" onClick={onClose}>
            Cancel
          </button>
          <button
            className="btn primary"
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
              <option key={p.object_id} value={refOf(p)}>
                {p.object_id}
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

// ================================================================ annotations

type FieldKind = 'integer' | 'number' | 'enum' | 'boolean' | 'text'

interface SpecDraft {
  kind: FieldKind
  min: string
  max: string
  options: string[]
  maxLength: string
}

function draftFromSchema(fs: Any): SpecDraft {
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

function describeSchema(fs: Any): string {
  if (!fs) return '—'
  if (fs.type === 'boolean') return 'Yes / No'
  if (fs.type === 'string' && Array.isArray(fs.enum)) return `One of: ${fs.enum.join(', ')}`
  if (fs.type === 'string') return fs.maxLength ? `Text (max ${fs.maxLength})` : 'Text'
  if (fs.type === 'integer' || fs.type === 'number') return `${fs.type === 'integer' ? 'Integer' : 'Number'} ${fs.minimum ?? '−∞'} – ${fs.maximum ?? '∞'}`
  return JSON.stringify(fs)
}

function AnnotationsTab({ projectId }: { projectId: string }) {
  const qc = useQueryClient()
  const specs = useObjects(projectId, { base_object_classes: ['AnnotationSpec'] })
  const [edit, setEdit] = useState<{ initial?: ObjSchema } | null>(null)
  return (
    <div className="col" style={{ gap: 12 }}>
      <div className="toolbar">
        <span className="muted small grow">Annotation fields appear in the feedback panel of every call, so reviewers can label traces consistently.</span>
        <button className="btn primary" onClick={() => setEdit({})}>
          + New annotation field
        </button>
      </div>
      <ErrorBox error={specs.error} />
      {specs.isLoading ? (
        <Loading />
      ) : !specs.data?.length ? (
        <div className="card">
          <Empty>No annotation fields yet. Create one, e.g. a 1–5 quality score or a “correct / incorrect” label.</Empty>
        </div>
      ) : (
        <div className="table-wrap">
          <table className="table">
            <thead>
              <tr>
                <th>Name</th>
                <th>Type</th>
                <th>Description</th>
                <th>Scope</th>
                <th>Updated</th>
                <th style={{ width: 120 }} />
              </tr>
            </thead>
            <tbody>
              {specs.data.map((s) => (
                <tr key={s.object_id}>
                  <td>
                    <Link to={objectUrl(projectId, s.object_id, s.digest)} style={{ fontWeight: 500 }}>
                      {s.val?.name ?? s.object_id}
                    </Link>
                  </td>
                  <td>{describeSchema(s.val?.field_schema)}</td>
                  <td className="muted small">{s.val?.description || '—'}</td>
                  <td className="small">
                    {Array.isArray(s.val?.op_scope) && s.val.op_scope.length ? s.val.op_scope.join(', ') : <span className="faint">all calls</span>}
                    {s.val?.unique_among_creators && <div className="faint">one per reviewer</div>}
                  </td>
                  <td className="muted">{fmtRelative(s.created_at)}</td>
                  <td>
                    <div className="row" style={{ justifyContent: 'flex-end' }}>
                      <button className="btn sm" onClick={() => setEdit({ initial: s })}>
                        Edit
                      </button>
                      <ConfirmButton
                        className="btn sm ghost danger"
                        title={`Delete ${s.object_id}?`}
                        body="Existing annotations are kept but the field is no longer offered."
                        onConfirm={async () => {
                          await api.objDelete(projectId, s.object_id)
                          await qc.invalidateQueries({ queryKey: ['objs', projectId] })
                        }}
                      >
                        ✕
                      </ConfirmButton>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {edit && <AnnotationSpecModal projectId={projectId} initial={edit.initial} onClose={() => setEdit(null)} />}
    </div>
  )
}

function AnnotationSpecModal({ projectId, initial, onClose }: { projectId: string; initial?: ObjSchema; onClose: () => void }) {
  const publish = usePublish(projectId)
  const v = initial?.val ?? {}
  const [name, setName] = useState(initial?.object_id ?? '')
  const [description, setDescription] = useState<string>(v.description ?? '')
  const [draft, setDraft] = useState<SpecDraft>(draftFromSchema(v.field_schema))
  const [unique, setUnique] = useState<boolean>(v.unique_among_creators ?? true)
  const [scope, setScope] = useState<string>(Array.isArray(v.op_scope) ? v.op_scope.join(', ') : '')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<unknown>(null)
  const set = (p: Partial<SpecDraft>) => setDraft({ ...draft, ...p })
  const schema = schemaFromDraft(draft)
  const valid = name && (draft.kind !== 'enum' || schema.enum.length >= 2) && (draft.kind === 'integer' || draft.kind === 'number' ? draft.min === '' || draft.max === '' || Number(draft.min) <= Number(draft.max) : true)
  return (
    <Modal
      title={initial ? `Edit annotation field ${initial.object_id}` : 'New annotation field'}
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
              try {
                const opScope = scope
                  .split(',')
                  .map((s) => s.trim())
                  .filter(Boolean)
                await publish(name, {
                  ...v,
                  ...classVal('AnnotationSpec', [], { name, description, field_schema: schema, unique_among_creators: unique, op_scope: opScope.length ? opScope : null }),
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
              <input className="input grow" value={o} onChange={(e) => set({ options: draft.options.map((x, j) => (j === i ? e.target.value : x)) })} />
              <button className="btn ghost sm" onClick={() => set({ options: draft.options.filter((_, j) => j !== i) })} disabled={draft.options.length <= 1}>
                ✕
              </button>
            </div>
          ))}
          <button className="btn sm" style={{ alignSelf: 'flex-start' }} onClick={() => set({ options: [...draft.options, ''] })}>
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
        Limit to ops (optional, comma separated)
        <input className="input mono" value={scope} onChange={(e) => setScope(e.target.value)} placeholder="answer_question, openai.chat" />
      </label>
      <div className="card" style={{ boxShadow: 'none' }}>
        <div className="card-body col" style={{ gap: 6 }}>
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
        <button className={`btn sm ${val === true ? 'primary' : ''}`} onClick={() => setVal(true)}>
          Yes
        </button>
        <button className={`btn sm ${val === false ? 'primary' : ''}`} onClick={() => setVal(false)}>
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
          <button key={n} className={`btn sm ${val === n ? 'primary' : ''}`} onClick={() => setVal(n)}>
            {n}
          </button>
        ))}
      </div>
    )
  return <input className="input" type="number" min={schema.minimum} max={schema.maximum} step={schema.type === 'integer' ? 1 : 'any'} style={{ width: 140 }} />
}

// ================================================================ integrations

function IntegrationsTab({ projectId }: { projectId: string }) {
  const info = useQuery({ queryKey: ['server-info'], queryFn: api.serverInfo })
  const base = window.location.origin
  const otel = `${base}${info.data?.otel_endpoint ?? '/otel/v1/traces'}`
  const auth = info.data?.auth_required
  const keyLine = auth ? '\nexport WEFT_API_KEY="<your api key>"' : ''
  const [lang, setLang] = useState<'python' | 'typescript' | 'otel-py' | 'otel-node' | 'env' | 'curl'>('python')
  const snippets: Record<typeof lang, { code: string; language: string; title: string }> = {
    python: {
      language: 'python',
      title: 'pip install weft',
      code: `# export WEFT_BASE_URL="${base}"${keyLine.replace('\n', '\n# ')}
import weft

weft.init("${projectId}")

@weft.op
def answer(question: str) -> str:
    return llm(question)   # nested calls and LLM usage are traced

answer("What is OpenTelemetry?")
`,
    },
    typescript: {
      language: 'typescript',
      title: 'npm install weft',
      code: `// WEFT_BASE_URL=${base}${auth ? '\n// WEFT_API_KEY=<your api key>' : ''}
import * as weft from 'weft'

await weft.init('${projectId}')

const answer = weft.op(async (question: string) => {
  return await llm(question)
}, { name: 'answer' })

await answer('What is OpenTelemetry?')
`,
    },
    'otel-py': {
      language: 'python',
      title: 'pip install opentelemetry-sdk opentelemetry-exporter-otlp-proto-http',
      code: `from opentelemetry import trace
from opentelemetry.sdk.trace import TracerProvider
from opentelemetry.sdk.trace.export import BatchSpanProcessor
from opentelemetry.exporter.otlp.proto.http.trace_exporter import OTLPSpanExporter

exporter = OTLPSpanExporter(
    endpoint="${otel}",
    headers={"project_id": "${projectId}"${auth ? ', "Authorization": "Bearer <your api key>"' : ''}},
)
provider = TracerProvider()
provider.add_span_processor(BatchSpanProcessor(exporter))
trace.set_tracer_provider(provider)

# Any OpenInference / OpenLLMetry / gen_ai.* instrumentation now reports to Weft.
`,
    },
    'otel-node': {
      language: 'typescript',
      title: 'npm install @opentelemetry/sdk-node @opentelemetry/exporter-trace-otlp-proto',
      code: `import { NodeSDK } from '@opentelemetry/sdk-node'
import { OTLPTraceExporter } from '@opentelemetry/exporter-trace-otlp-proto'

const sdk = new NodeSDK({
  traceExporter: new OTLPTraceExporter({
    url: '${otel}',
    headers: { project_id: '${projectId}'${auth ? ", Authorization: 'Bearer <your api key>'" : ''} },
  }),
})
sdk.start()
`,
    },
    env: {
      language: 'bash',
      title: 'Standard OTLP environment variables (any language)',
      code: `export OTEL_EXPORTER_OTLP_TRACES_ENDPOINT="${otel}"
export OTEL_EXPORTER_OTLP_TRACES_PROTOCOL="http/protobuf"
export OTEL_EXPORTER_OTLP_TRACES_HEADERS="project_id=${projectId}${auth ? ',Authorization=Bearer%20<your api key>' : ''}"
`,
    },
    curl: {
      language: 'bash',
      title: 'Record a call with the HTTP API',
      code: `curl -s ${base}/call/start \\
  -H 'Content-Type: application/json'${auth ? " \\\n  -H 'Authorization: Bearer <your api key>'" : ''} \\
  -d '{"start": {"project_id": "${projectId}", "op_name": "hello", "started_at": "'$(date -u +%Y-%m-%dT%H:%M:%SZ)'", "inputs": {"name": "world"}}}'
# => {"id": "<call id>", "trace_id": "..."}

curl -s ${base}/call/end \\
  -H 'Content-Type: application/json'${auth ? " \\\n  -H 'Authorization: Bearer <your api key>'" : ''} \\
  -d '{"end": {"project_id": "${projectId}", "id": "<call id>", "ended_at": "'$(date -u +%Y-%m-%dT%H:%M:%SZ)'", "output": "Hello, world"}}'
`,
    },
  }
  const s = snippets[lang]
  return (
    <div className="col" style={{ gap: 12 }}>
      <div className="muted small">
        Send traces with the Weft SDKs, or point any OpenTelemetry exporter at this server. The <span className="mono">project_id</span> header picks the project.
      </div>
      <div className="tabs-scroll">
        <div className="seg">
          {(
            [
              ['python', 'Python SDK'],
              ['typescript', 'TypeScript SDK'],
              ['otel-py', 'OTLP · Python'],
              ['otel-node', 'OTLP · Node'],
              ['env', 'OTLP · env vars'],
              ['curl', 'curl'],
            ] as [typeof lang, string][]
          ).map(([id, label]) => (
            <button key={id} className={lang === id ? 'on' : ''} onClick={() => setLang(id)}>
              {label}
            </button>
          ))}
        </div>
      </div>
      <CodeBlock code={s.code} language={s.language} title={<span className="mono">{s.title}</span>} />
      <div className="kv">
        <div className="k">server URL</div>
        <div className="v mono">{base}</div>
        <div className="k">OTLP traces</div>
        <div className="v mono">{otel}</div>
        <div className="k">project_id</div>
        <div className="v mono">{projectId}</div>
      </div>
    </div>
  )
}

// ================================================================ api key

function ApiKeyTab() {
  const qc = useQueryClient()
  const [stored, setStored] = useState<string | null>(getApiKey())
  const [value, setValue] = useState('')
  const info = useQuery({ queryKey: ['server-info'], queryFn: api.serverInfo })
  const check = useQuery({ queryKey: ['auth-check-settings', stored], queryFn: api.authCheck, retry: false })
  const ok = check.isSuccess
  const unauthorized = check.error instanceof ApiError && check.error.status === 401
  const mask = (k: string) => (k.length <= 8 ? '••••••••' : `${k.slice(0, 4)}••••••••${k.slice(-4)}`)
  return (
    <div className="col" style={{ gap: 12, maxWidth: 640 }}>
      <Section title="Browser API key">
        <div className="col" style={{ gap: 10 }}>
          <div className="hint">This key is stored in this browser only and sent with every request from the UI.</div>
          <div className="kv">
            <div className="k">server auth</div>
            <div className="v">{info.data ? (info.data.auth_required ? 'required' : 'not required') : '…'}</div>
            <div className="k">stored key</div>
            <div className="v mono">{stored ? mask(stored) : <span className="faint">none</span>}</div>
            <div className="k">status</div>
            <div className="v">
              {check.isLoading ? <Spinner /> : ok ? <span className="badge success">authorized</span> : unauthorized ? <span className="badge error">unauthorized</span> : <span className="badge">unknown</span>}
            </div>
          </div>
          <form
            className="row wrap"
            onSubmit={(e) => {
              e.preventDefault()
              if (!value.trim()) return
              setApiKey(value.trim())
              setStored(value.trim())
              setValue('')
              void qc.invalidateQueries()
            }}
          >
            <input className="input grow mono" type="password" autoComplete="off" placeholder="Paste an API key" value={value} onChange={(e) => setValue(e.target.value)} style={{ minWidth: 200 }} />
            <button className="btn primary" type="submit" disabled={!value.trim()}>
              Save key
            </button>
            <button
              className="btn danger"
              type="button"
              disabled={!stored}
              onClick={() => {
                setApiKey(null)
                setStored(null)
                void qc.invalidateQueries()
              }}
            >
              Clear
            </button>
          </form>
        </div>
      </Section>
    </div>
  )
}
