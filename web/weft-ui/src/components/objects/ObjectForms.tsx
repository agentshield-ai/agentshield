// Create / edit forms for prompts, LLM completion models and server-runnable scorers.
// Each form publishes a new object version via /obj/create.
import { useState, type ReactNode } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { api } from '../../api/client'
import { objectRef } from '../../api/refs'
import type { Any, ChatMessage, ObjSchema } from '../../api/types'
import { ErrorBox, Modal } from '../common'
import {
  classVal,
  errMsg,
  sanitizeObjectId,
  SCORER_LABELS,
  SERVER_SCORER_CLASSES,
  templateVars,
  useLLMModels,
  valClass,
  type ServerScorerClass,
} from './objectUtils'

export interface Created {
  objectId: string
  digest: string
  ref: string
  versionIndex: number
}

/** Publish an object and refresh object queries. */
export function usePublish(projectId: string) {
  const qc = useQueryClient()
  return async (objectId: string, val: Any): Promise<Created> => {
    const r = await api.objCreate(projectId, objectId, val)
    void qc.invalidateQueries({ queryKey: ['objs', projectId] })
    void qc.invalidateQueries({ queryKey: ['obj-versions', projectId, objectId] })
    void qc.invalidateQueries({ queryKey: ['obj', projectId, objectId] })
    return { objectId: r.object_id, digest: r.digest, ref: objectRef(projectId, r.object_id, r.digest), versionIndex: r.version_index }
  }
}

// ----------------------------------------------------------------- pieces

export function NameField({
  value,
  onChange,
  locked,
  label = 'Name',
  placeholder = 'my-object',
}: {
  value: string
  onChange: (v: string) => void
  locked?: boolean
  label?: string
  placeholder?: string
}) {
  return (
    <label className="field">
      {label}
      <input
        className="input mono"
        value={value}
        disabled={locked}
        placeholder={placeholder}
        onChange={(e) => onChange(sanitizeObjectId(e.target.value))}
        autoFocus={!locked}
      />
      {locked ? <span className="hint">Saving publishes a new version of this object.</span> : null}
    </label>
  )
}

const ROLES = ['system', 'user', 'assistant']

/** Editable list of chat messages with {placeholder} support. */
export function MessagesEditor({ value, onChange }: { value: ChatMessage[]; onChange: (v: ChatMessage[]) => void }) {
  const set = (i: number, patch: Partial<ChatMessage>) => onChange(value.map((m, j) => (j === i ? { ...m, ...patch } : m)))
  return (
    <div className="col">
      {value.map((m, i) => (
        <div key={i} className="msg">
          <div className="msg-role">
            <select className="input" style={{ height: 24, fontSize: 12 }} value={m.role} onChange={(e) => set(i, { role: e.target.value })}>
              {ROLES.map((r) => (
                <option key={r}>{r}</option>
              ))}
            </select>
            <span className="grow" />
            <button className="btn ghost sm" disabled={i === 0} onClick={() => onChange(swap(value, i, i - 1))} title="Move up">
              ↑
            </button>
            <button className="btn ghost sm" disabled={i === value.length - 1} onClick={() => onChange(swap(value, i, i + 1))} title="Move down">
              ↓
            </button>
            <button className="btn ghost sm danger" onClick={() => onChange(value.filter((_, j) => j !== i))} title="Remove message">
              ✕
            </button>
          </div>
          <textarea
            className="input mono"
            style={{ border: 'none', width: '100%', borderRadius: 0, minHeight: 64 }}
            value={typeof m.content === 'string' ? m.content : JSON.stringify(m.content)}
            placeholder={m.role === 'system' ? 'You are a helpful assistant.' : 'Use {placeholders} for dataset columns, e.g. {question}'}
            onChange={(e) => set(i, { content: e.target.value })}
          />
        </div>
      ))}
      <div className="row">
        <button className="btn sm" onClick={() => onChange([...value, { role: value.length === 0 ? 'system' : 'user', content: '' }])}>
          + Add message
        </button>
        <VarsHint texts={value.map((m) => (typeof m.content === 'string' ? m.content : ''))} />
      </div>
    </div>
  )
}

function swap<T>(xs: T[], i: number, j: number): T[] {
  const out = xs.slice()
  ;[out[i], out[j]] = [out[j], out[i]]
  return out
}

export function VarsHint({ texts }: { texts: string[] }) {
  const vars = templateVars(...texts)
  if (!vars.length) return <span className="hint">No template variables</span>
  return (
    <span className="chips">
      <span className="hint">Variables:</span>
      {vars.map((v) => (
        <span key={v} className="tvar">{`{${v}}`}</span>
      ))}
    </span>
  )
}

/** LLM model id input with suggestions from GET /llm/models. */
export function LLMModelInput({ value, onChange, id = 'llm-models' }: { value: string; onChange: (v: string) => void; id?: string }) {
  const models = useLLMModels()
  const cur = models.data?.find((m) => m.id === value)
  return (
    <label className="field">
      LLM model
      <input className="input mono" list={id} value={value} onChange={(e) => onChange(e.target.value)} placeholder="gpt-4o-mini" />
      <datalist id={id}>
        {models.data?.map((m) => (
          <option key={m.id} value={m.id}>
            {m.label} {m.available ? '' : '(no API key)'}
          </option>
        ))}
      </datalist>
      {cur && !cur.available ? (
        <span className="hint" style={{ color: 'var(--warn)' }}>
          No API key configured for {cur.provider}. Add one in Settings → Secrets.
        </span>
      ) : (
        <span className="hint">Use mock/echo or mock/json to test offline.</span>
      )}
    </label>
  )
}

function FormModal({
  title,
  onClose,
  onSubmit,
  children,
  submitLabel = 'Publish',
  disabled,
  wide,
}: {
  title: ReactNode
  onClose: () => void
  onSubmit: () => Promise<void>
  children: ReactNode
  submitLabel?: string
  disabled?: boolean
  wide?: boolean
}) {
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<unknown>(null)
  return (
    <Modal
      title={title}
      onClose={onClose}
      wide={wide}
      footer={
        <>
          <button className="btn" onClick={onClose}>
            Cancel
          </button>
          <button
            className="btn primary"
            disabled={disabled || busy}
            onClick={async () => {
              setBusy(true)
              setErr(null)
              try {
                await onSubmit()
              } catch (e) {
                setErr(e)
              } finally {
                setBusy(false)
              }
            }}
          >
            {busy ? 'Saving…' : submitLabel}
          </button>
        </>
      }
    >
      {children}
      <ErrorBox error={err} />
    </Modal>
  )
}

// ----------------------------------------------------------------- prompts

export function PromptFormModal({
  projectId,
  initial,
  onClose,
  onCreated,
}: {
  projectId: string
  initial?: ObjSchema
  onClose: () => void
  onCreated: (c: Created) => void
}) {
  const publish = usePublish(projectId)
  const initCls = initial ? valClass(initial.val) : null
  const [name, setName] = useState(initial?.object_id ?? '')
  const [cls, setCls] = useState<'StringPrompt' | 'MessagesPrompt'>(initCls === 'StringPrompt' ? 'StringPrompt' : 'MessagesPrompt')
  const [content, setContent] = useState<string>(initial?.val?.content ?? '')
  const [messages, setMessages] = useState<ChatMessage[]>(
    initial?.val?.messages ?? [
      { role: 'system', content: 'You are a helpful assistant.' },
      { role: 'user', content: '{question}' },
    ],
  )
  const [description, setDescription] = useState<string>(initial?.val?.description ?? '')
  return (
    <FormModal
      title={initial ? `Edit prompt ${initial.object_id}` : 'New prompt'}
      onClose={onClose}
      wide
      disabled={!name || (cls === 'StringPrompt' ? !content : messages.length === 0)}
      onSubmit={async () => {
        const fields =
          cls === 'StringPrompt'
            ? { name, description: description || null, content }
            : { name, description: description || null, messages }
        const base = initial && initCls === cls ? initial.val : {}
        onCreated(await publish(name, { ...base, ...classVal(cls, ['Prompt'], fields) }))
      }}
    >
      <div className="form-grid">
        <NameField value={name} onChange={setName} locked={!!initial} placeholder="qa-prompt" />
        <label className="field">
          Type
          <select className="input" value={cls} onChange={(e) => setCls(e.target.value as typeof cls)}>
            <option value="MessagesPrompt">Messages (chat)</option>
            <option value="StringPrompt">String</option>
          </select>
        </label>
      </div>
      <label className="field">
        Description
        <input className="input" value={description} onChange={(e) => setDescription(e.target.value)} placeholder="Optional" />
      </label>
      {cls === 'StringPrompt' ? (
        <label className="field">
          Template
          <textarea className="input mono" rows={8} value={content} onChange={(e) => setContent(e.target.value)} placeholder="Answer the question: {question}" />
          <VarsHint texts={[content]} />
        </label>
      ) : (
        <MessagesEditor value={messages} onChange={setMessages} />
      )}
    </FormModal>
  )
}

// ----------------------------------------------------------------- models

export interface LLMModelFields {
  llm_model_id: string
  messages: ChatMessage[]
  temperature: number
  max_tokens: number
  response_format: 'text' | 'json_object'
}

export function llmModelVal(name: string, f: LLMModelFields, prev?: Any) {
  return {
    ...(prev ?? {}),
    ...classVal('LLMStructuredCompletionModel', ['Model'], {
      name,
      description: prev?.description ?? null,
      llm_model_id: f.llm_model_id,
      default_params: {
        ...(prev?.default_params ?? {}),
        messages_template: f.messages,
        temperature: f.temperature,
        max_tokens: f.max_tokens,
        response_format: f.response_format,
      },
    }),
  }
}

/** The form body for an LLMStructuredCompletionModel; reused inline in the evaluation wizard. */
export function LLMModelFieldsEditor({ value, onChange }: { value: LLMModelFields; onChange: (v: LLMModelFields) => void }) {
  return (
    <>
      <div className="form-grid">
        <LLMModelInput value={value.llm_model_id} onChange={(llm_model_id) => onChange({ ...value, llm_model_id })} />
        <label className="field">
          Temperature ({value.temperature})
          <input
            className="input"
            type="range"
            min={0}
            max={2}
            step={0.1}
            value={value.temperature}
            onChange={(e) => onChange({ ...value, temperature: Number(e.target.value) })}
          />
        </label>
        <label className="field">
          Max tokens
          <input className="input" type="number" min={1} value={value.max_tokens} onChange={(e) => onChange({ ...value, max_tokens: Number(e.target.value) || 1 })} />
        </label>
        <label className="field">
          Response format
          <select className="input" value={value.response_format} onChange={(e) => onChange({ ...value, response_format: e.target.value as LLMModelFields['response_format'] })}>
            <option value="text">text</option>
            <option value="json_object">json_object</option>
          </select>
        </label>
      </div>
      <div className="field" style={{ fontSize: 12, color: 'var(--text-2)' }}>
        Messages template
        <span className="hint">Placeholders like {'{question}'} are filled from each dataset row.</span>
      </div>
      <MessagesEditor value={value.messages} onChange={(messages) => onChange({ ...value, messages })} />
    </>
  )
}

export const DEFAULT_MODEL_FIELDS: LLMModelFields = {
  llm_model_id: 'gpt-4o-mini',
  messages: [
    { role: 'system', content: 'You are a concise, accurate assistant.' },
    { role: 'user', content: '{question}' },
  ],
  temperature: 0,
  max_tokens: 512,
  response_format: 'text',
}

export function modelFieldsFrom(val: Any): LLMModelFields {
  const p = val?.default_params ?? {}
  return {
    llm_model_id: val?.llm_model_id ?? DEFAULT_MODEL_FIELDS.llm_model_id,
    messages: Array.isArray(p.messages_template) ? p.messages_template : DEFAULT_MODEL_FIELDS.messages,
    temperature: typeof p.temperature === 'number' ? p.temperature : 0,
    max_tokens: typeof p.max_tokens === 'number' ? p.max_tokens : 512,
    response_format: p.response_format === 'json_object' ? 'json_object' : 'text',
  }
}

export function ModelFormModal({
  projectId,
  initial,
  onClose,
  onCreated,
}: {
  projectId: string
  initial?: ObjSchema
  onClose: () => void
  onCreated: (c: Created) => void
}) {
  const publish = usePublish(projectId)
  const [name, setName] = useState(initial?.object_id ?? '')
  const [fields, setFields] = useState<LLMModelFields>(initial ? modelFieldsFrom(initial.val) : DEFAULT_MODEL_FIELDS)
  return (
    <FormModal
      title={initial ? `Edit model ${initial.object_id}` : 'New LLM completion model'}
      onClose={onClose}
      wide
      disabled={!name || !fields.llm_model_id}
      onSubmit={async () => onCreated(await publish(name, llmModelVal(name, fields, initial?.val)))}
    >
      <NameField value={name} onChange={setName} locked={!!initial} placeholder="gpt4o-mini-qa" />
      <LLMModelFieldsEditor value={fields} onChange={setFields} />
    </FormModal>
  )
}

// ----------------------------------------------------------------- scorers

export interface ScorerFields {
  cls: ServerScorerClass
  model: string
  scoring_prompt: string
  response_format: 'json_object' | 'text'
  target_column: string
  case_insensitive: boolean
  pattern: string
}

export const DEFAULT_SCORER_FIELDS: ScorerFields = {
  cls: 'LLMAsAJudgeScorer',
  model: 'gpt-4o-mini',
  scoring_prompt:
    'You are grading an answer.\nQuestion: {question}\nExpected: {expected}\nAnswer: {output}\n\nReply with JSON {"correct": true|false, "score": 1-5, "reason": "..."}',
  response_format: 'json_object',
  target_column: 'expected',
  case_insensitive: true,
  pattern: '',
}

export function scorerFieldsFrom(val: Any): ScorerFields {
  const cls = (SERVER_SCORER_CLASSES as readonly string[]).includes(valClass(val) ?? '') ? (valClass(val) as ServerScorerClass) : 'LLMAsAJudgeScorer'
  return {
    ...DEFAULT_SCORER_FIELDS,
    cls,
    model: typeof val?.model === 'string' ? val.model : DEFAULT_SCORER_FIELDS.model,
    scoring_prompt: val?.scoring_prompt ?? DEFAULT_SCORER_FIELDS.scoring_prompt,
    response_format: val?.response_format === 'text' ? 'text' : 'json_object',
    target_column: val?.target_column ?? 'expected',
    case_insensitive: val?.case_insensitive ?? true,
    pattern: val?.pattern ?? '',
  }
}

export function scorerVal(name: string, f: ScorerFields, prev?: Any) {
  let fields: Record<string, Any> = { name, description: prev?.description ?? null }
  switch (f.cls) {
    case 'LLMAsAJudgeScorer':
      fields = { ...fields, model: f.model, scoring_prompt: f.scoring_prompt, response_format: f.response_format }
      break
    case 'ExactMatchScorer':
    case 'LevenshteinScorer':
      fields = { ...fields, target_column: f.target_column }
      break
    case 'ContainsScorer':
      fields = { ...fields, target_column: f.target_column, case_insensitive: f.case_insensitive }
      break
    case 'RegexScorer':
      fields = { ...fields, pattern: f.pattern }
      break
    case 'JSONValidScorer':
      break
  }
  const keep = prev && valClass(prev) === f.cls ? prev : {}
  return { ...keep, ...classVal(f.cls, ['Scorer'], fields) }
}

const SCORER_HELP: Record<ServerScorerClass, string> = {
  LLMAsAJudgeScorer: 'Asks an LLM to grade each output. {output} and dataset columns are available in the prompt. JSON replies are parsed into the score.',
  ExactMatchScorer: 'true when the output equals the target column (after trimming).',
  ContainsScorer: 'true when the output contains the target column value.',
  RegexScorer: 'true when the output matches the regular expression.',
  JSONValidScorer: 'true when the output parses as JSON.',
  LevenshteinScorer: 'Similarity between output and target, from 0 to 1.',
}

export function ScorerFieldsEditor({
  value,
  onChange,
  lockClass,
  onlyJudge,
}: {
  value: ScorerFields
  onChange: (v: ScorerFields) => void
  lockClass?: boolean
  onlyJudge?: boolean
}) {
  const set = (p: Partial<ScorerFields>) => onChange({ ...value, ...p })
  return (
    <>
      {!onlyJudge && (
        <label className="field">
          Scorer type
          <select className="input" value={value.cls} disabled={lockClass} onChange={(e) => set({ cls: e.target.value as ServerScorerClass })}>
            {SERVER_SCORER_CLASSES.map((c) => (
              <option key={c} value={c}>
                {SCORER_LABELS[c]} ({c})
              </option>
            ))}
          </select>
          <span className="hint">{SCORER_HELP[value.cls]}</span>
        </label>
      )}
      {value.cls === 'LLMAsAJudgeScorer' && (
        <>
          <div className="form-grid">
            <LLMModelInput value={value.model} onChange={(model) => set({ model })} id="judge-models" />
            <label className="field">
              Response format
              <select className="input" value={value.response_format} onChange={(e) => set({ response_format: e.target.value as ScorerFields['response_format'] })}>
                <option value="json_object">json_object</option>
                <option value="text">text</option>
              </select>
            </label>
          </div>
          <label className="field">
            Scoring prompt
            <textarea className="input mono" rows={8} value={value.scoring_prompt} onChange={(e) => set({ scoring_prompt: e.target.value })} />
            <VarsHint texts={[value.scoring_prompt]} />
          </label>
        </>
      )}
      {(value.cls === 'ExactMatchScorer' || value.cls === 'ContainsScorer' || value.cls === 'LevenshteinScorer') && (
        <label className="field">
          Target column
          <input className="input mono" value={value.target_column} onChange={(e) => set({ target_column: e.target.value })} placeholder="expected" />
          <span className="hint">The dataset column holding the expected answer.</span>
        </label>
      )}
      {value.cls === 'ContainsScorer' && (
        <label className="checkbox">
          <input type="checkbox" checked={value.case_insensitive} onChange={(e) => set({ case_insensitive: e.target.checked })} /> Case insensitive
        </label>
      )}
      {value.cls === 'RegexScorer' && (
        <label className="field">
          Pattern
          <input className="input mono" value={value.pattern} onChange={(e) => set({ pattern: e.target.value })} placeholder="^\d+$" />
          <RegexCheck pattern={value.pattern} />
        </label>
      )}
    </>
  )
}

function RegexCheck({ pattern }: { pattern: string }) {
  if (!pattern) return null
  try {
    new RegExp(pattern)
    return <span className="hint">Valid pattern (server uses Go RE2 syntax).</span>
  } catch (e) {
    return <span className="hint" style={{ color: 'var(--error)' }}>{errMsg(e)}</span>
  }
}

export function scorerFieldsValid(f: ScorerFields): boolean {
  switch (f.cls) {
    case 'LLMAsAJudgeScorer':
      return !!f.model && !!f.scoring_prompt.trim()
    case 'RegexScorer':
      return !!f.pattern
    case 'JSONValidScorer':
      return true
    default:
      return !!f.target_column
  }
}

export function ScorerFormModal({
  projectId,
  initial,
  onClose,
  onCreated,
  onlyJudge,
}: {
  projectId: string
  initial?: ObjSchema
  onClose: () => void
  onCreated: (c: Created) => void
  onlyJudge?: boolean
}) {
  const publish = usePublish(projectId)
  const [name, setName] = useState(initial?.object_id ?? '')
  const [fields, setFields] = useState<ScorerFields>(initial ? scorerFieldsFrom(initial.val) : DEFAULT_SCORER_FIELDS)
  return (
    <FormModal
      title={initial ? `Edit scorer ${initial.object_id}` : onlyJudge ? 'New LLM judge' : 'New scorer'}
      onClose={onClose}
      wide={fields.cls === 'LLMAsAJudgeScorer'}
      disabled={!name || !scorerFieldsValid(fields)}
      onSubmit={async () => onCreated(await publish(name, scorerVal(name, fields, initial?.val)))}
    >
      <NameField value={name} onChange={setName} locked={!!initial} placeholder={fields.cls === 'LLMAsAJudgeScorer' ? 'correctness-judge' : 'exact'} />
      <ScorerFieldsEditor value={fields} onChange={setFields} lockClass={!!initial} onlyJudge={onlyJudge} />
      <span className="hint">The scorer's name is the key its results appear under in evaluation summaries.</span>
    </FormModal>
  )
}
