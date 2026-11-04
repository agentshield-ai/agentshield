import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, ApiError } from '../../api/client'
import { parseRef } from '../../api/refs'
import type { Any, ChatMessage, CompletionInputs, LLMModelInfo, WeftObject } from '../../api/types'
import { useProjectId } from '../../hooks/useProject'
import { fmtDuration, fmtNumber } from '../../lib/format'
import { spanUrl, settingsUrl } from '../../lib/urls'
import { ErrorBox, Modal, Spinner } from '../../components/common'
import { JsonBlock } from '../../components/ValueView'
import { ChatMessageView, findInputMessages, normaliseMessage } from '../../components/traces/ChatView'
import { Dropdown, MenuItem } from '../../components/traces/Menu'
import { slugify } from '../../components/traces/callUtils'
import { HistoryPanel } from '../../components/playground/HistoryPanel'
import { ImageMode } from '../../components/playground/ImageMode'
import { addRun, loadRuns, saveRuns, type PlaygroundRun } from '../../components/playground/history'
import type { ImageInputs } from '../../api/playground'

// ---------------------------------------------------------------- model

let nextId = 1
const uid = () => `m${nextId++}`

interface EditMsg {
  id: string
  role: string
  content: string
  /** content is a JSON array of parts, edited as JSON */
  json?: boolean
  /** fields preserved verbatim (tool_calls, tool_call_id, name) */
  extra?: Record<string, Any>
}

interface Params {
  temperature: number
  max_tokens: number
  top_p: number
  frequency_penalty: number
  presence_penalty: number
  stop: string
  json: boolean
  tools: string
}

interface RunResult {
  running: boolean
  message?: ChatMessage
  finishReason?: string | null
  usage?: { prompt_tokens?: number; completion_tokens?: number; total_tokens?: number }
  latencyMs?: number
  callId?: string
  error?: string
  raw?: Any
  streamed?: boolean
}

interface Panel {
  id: string
  model: string
  params: Params
  messages: EditMsg[]
  result: RunResult | null
  showParams: boolean
}

const DEFAULT_PARAMS: Params = { temperature: 0.7, max_tokens: 1024, top_p: 1, frequency_penalty: 0, presence_penalty: 0, stop: '', json: false, tools: '' }
const DEFAULT_MESSAGES = (): EditMsg[] => [
  { id: uid(), role: 'system', content: 'You are a helpful assistant.' },
  { id: uid(), role: 'user', content: '' },
]
const MAX_PANELS = 3
const ROLES = ['system', 'user', 'assistant', 'tool']

function toEditMsg(m: Any): EditMsg {
  const { role, content, ...rest } = m ?? {}
  const extra: Record<string, Any> = {}
  for (const k of ['tool_calls', 'tool_call_id', 'name']) if (rest[k] != null) extra[k] = rest[k]
  if (typeof content === 'string' || content == null)
    return { id: uid(), role: String(role ?? 'user'), content: content ?? '', extra: Object.keys(extra).length ? extra : undefined }
  if (Array.isArray(content) && content.every((p) => p && (p.type === 'text' || p.type === 'input_text') && typeof p.text === 'string'))
    return { id: uid(), role: String(role ?? 'user'), content: content.map((p) => p.text).join('\n'), extra: Object.keys(extra).length ? extra : undefined }
  return { id: uid(), role: String(role ?? 'user'), content: JSON.stringify(content, null, 2), json: true, extra: Object.keys(extra).length ? extra : undefined }
}

function toApiMsg(m: EditMsg): ChatMessage {
  let content: Any = m.content
  if (m.json) {
    try {
      content = JSON.parse(m.content)
    } catch {
      throw new Error(`Message content for "${m.role}" is not valid JSON`)
    }
  }
  return { role: m.role, content, ...(m.extra ?? {}) }
}

const VAR_RE = /\{\{|\}\}|\{([A-Za-z_][A-Za-z0-9_]*)\}/g

/** Template variable names ({name}) used across messages, in first-seen order. */
export function templateVars(msgs: EditMsg[]): string[] {
  const seen = new Set<string>()
  for (const m of msgs) {
    if (m.json) continue
    for (const match of m.content.matchAll(VAR_RE)) if (match[1]) seen.add(match[1])
  }
  return [...seen]
}

/** Fill {name} placeholders; unknown names are left as-is and {{ }} become literal braces. */
export function fillVars(text: string, vars: Record<string, string>): string {
  return text.replace(VAR_RE, (m, name?: string) => (m === '{{' ? '{' : m === '}}' ? '}' : name && name in vars ? vars[name] : m))
}

function buildInputs(model: string, msgs: EditMsg[], p: Params): CompletionInputs {
  const inputs: CompletionInputs = { model, messages: msgs.map(toApiMsg), temperature: p.temperature, max_tokens: p.max_tokens }
  if (p.top_p !== 1) inputs.top_p = p.top_p
  if (p.frequency_penalty) inputs.frequency_penalty = p.frequency_penalty
  if (p.presence_penalty) inputs.presence_penalty = p.presence_penalty
  const stop = p.stop
    .split(',')
    .map((s) => s.trim())
    .filter(Boolean)
  if (stop.length) inputs.stop = stop
  if (p.json) inputs.response_format = { type: 'json_object' }
  if (p.tools.trim()) {
    let tools: Any
    try {
      tools = JSON.parse(p.tools)
    } catch {
      throw new Error('Tools must be a JSON array of tool definitions')
    }
    if (!Array.isArray(tools)) throw new Error('Tools must be a JSON array of tool definitions')
    inputs.tools = tools
  }
  return inputs
}

/** Merge a streamed delta into the accumulated assistant message. */
function applyDelta(msg: ChatMessage, delta: Any): ChatMessage {
  const next: ChatMessage = { ...msg }
  if (typeof delta?.content === 'string') next.content = (typeof next.content === 'string' ? next.content : '') + delta.content
  if (delta?.role) next.role = delta.role
  if (Array.isArray(delta?.tool_calls)) {
    const tcs: Any[] = [...(next.tool_calls ?? [])]
    for (const d of delta.tool_calls) {
      const i = d.index ?? tcs.length
      const cur = tcs[i] ?? { id: d.id, type: 'function', function: { name: '', arguments: '' } }
      tcs[i] = {
        ...cur,
        id: d.id ?? cur.id,
        function: {
          name: (cur.function?.name ?? '') + (d.function?.name ?? ''),
          arguments: (cur.function?.arguments ?? '') + (d.function?.arguments ?? ''),
        },
      }
    }
    next.tool_calls = tcs
  }
  return next
}

/** Messages, params and model from completion inputs (a call's or a history run's). */
function stateFromInputs(inputs: Any): { msgs: EditMsg[]; params: Params; model: string | null } | null {
  const msgs = findInputMessages(inputs)
  if (!msgs) return null
  const system = typeof inputs?.system === 'string' && !msgs.some((m) => m?.role === 'system') ? [{ id: uid(), role: 'system', content: inputs.system }] : []
  const p: Params = { ...DEFAULT_PARAMS }
  for (const k of ['temperature', 'max_tokens', 'top_p', 'frequency_penalty', 'presence_penalty'] as const) if (typeof inputs[k] === 'number') p[k] = inputs[k]
  if (Array.isArray(inputs.stop)) p.stop = inputs.stop.join(', ')
  else if (typeof inputs.stop === 'string') p.stop = inputs.stop
  if (inputs.response_format?.type === 'json_object') p.json = true
  if (Array.isArray(inputs.tools)) p.tools = JSON.stringify(inputs.tools, null, 2)
  return { msgs: [...system, ...msgs.map(toEditMsg)], params: p, model: typeof inputs.model === 'string' ? inputs.model : null }
}

function messageText(m: ChatMessage | undefined): string {
  if (!m) return ''
  if (typeof m.content === 'string' && m.content) return m.content
  if (Array.isArray(m.content)) return m.content.map((x: Any) => (typeof x?.text === 'string' ? x.text : '')).join(' ')
  if (Array.isArray(m.tool_calls) && m.tool_calls.length) return `→ ${m.tool_calls.map((t: Any) => t?.function?.name).join(', ')}()`
  return ''
}

function pickDefaultModel(models: LLMModelInfo[]): string {
  const avail = models.filter((m) => m.available)
  return (avail.find((m) => m.id === 'gpt-4o-mini') ?? avail.find((m) => !m.id.startsWith('mock/')) ?? avail[0])?.id ?? 'mock/echo'
}

// ---------------------------------------------------------------- page

export function PlaygroundPage() {
  const projectId = useProjectId()
  const [params, setParams] = useSearchParams()
  const fromCall = params.get('span') ?? params.get('call')
  const fromPrompt = params.get('prompt')
  const modelsQ = useQuery({ queryKey: ['llm-models'], queryFn: api.llmModels, staleTime: 60_000 })
  const models = useMemo(() => {
    const ms = [...(modelsQ.data?.models ?? [])]
    if (!ms.some((m) => m.id === 'mock/echo')) ms.push({ id: 'mock/echo', provider: 'mock', label: 'Echo (offline)', available: true, max_tokens: 4096 })
    return ms
  }, [modelsQ.data])

  const [sync, setSync] = useState(true)
  const [shared, setShared] = useState<EditMsg[]>(DEFAULT_MESSAGES)
  const [panels, setPanels] = useState<Panel[]>(() => [{ id: uid(), model: '', params: { ...DEFAULT_PARAMS }, messages: [], result: null, showParams: false }])
  const [saveOpen, setSaveOpen] = useState(false)
  const [saveModelOpen, setSaveModelOpen] = useState(false)
  const [vars, setVars] = useState<Record<string, string>>({})
  const varsRef = useRef(vars)
  varsRef.current = vars
  const [loadNote, setLoadNote] = useState<string | null>(null)
  const aborts = useRef(new Map<string, AbortController>())
  // Run history: this browser's runs (localStorage) plus tracked calls on the server.
  const mode: 'chat' | 'image' = params.get('mode') === 'image' ? 'image' : 'chat'
  const [showHistory, setShowHistory] = useState(() => {
    try {
      return localStorage.getItem('weft.playground.showHistory') === '1'
    } catch {
      return false
    }
  })
  const toggleHistory = (on: boolean) => {
    setShowHistory(on)
    try {
      localStorage.setItem('weft.playground.showHistory', on ? '1' : '0')
    } catch {
      /* storage unavailable */
    }
  }
  const [history, setHistory] = useState<PlaygroundRun[]>(() => loadRuns(projectId))
  const record = useRef((run: PlaygroundRun) => {
    setHistory((h) => {
      const next = addRun(h, run)
      saveRuns(projectId, next)
      return next
    })
  })
  const [imageInitial, setImageInitial] = useState<(Partial<ImageInputs> & { nonce: number }) | undefined>()
  const setMode = (m: 'chat' | 'image') =>
    setParams(
      (p) => {
        const n = new URLSearchParams(p)
        if (m === 'chat') n.delete('mode')
        else n.set('mode', m)
        return n
      },
      { replace: true },
    )

  // Default model once the list arrives.
  useEffect(() => {
    if (!modelsQ.data) return
    setPanels((ps) => ps.map((p) => (p.model ? p : { ...p, model: pickDefaultModel(models) })))
  }, [modelsQ.data, models])

  // Load messages, model and params from a span (?span=<id>).
  const callQ = useQuery({ queryKey: ['span', projectId, fromCall], queryFn: () => api.spanFind(projectId, fromCall!), enabled: !!fromCall })
  useEffect(() => {
    const c = callQ.data
    if (!c) return
    const st = stateFromInputs(c.inputs ?? {})
    if (!st) {
      setLoadNote(`Span ${c.id.slice(0, 8)} has no chat messages in its inputs.`)
      return
    }
    setShared(st.msgs)
    setSync(true)
    const { params: p, model } = st
    setPanels((ps) => ps.map((x, i) => (i === 0 ? { ...x, model: model ?? x.model, params: p, result: null } : x)))
    setLoadNote(`Loaded from span ${c.id.slice(0, 8)}${model ? ` (${model})` : ''}.`)
  }, [callQ.data])

  const panelMessages = (p: Panel) => (sync ? shared : p.messages)
  const setPanel = (id: string, patch: Partial<Panel> | ((p: Panel) => Partial<Panel>)) =>
    setPanels((ps) => ps.map((p) => (p.id === id ? { ...p, ...(typeof patch === 'function' ? patch(p) : patch) } : p)))
  const setPanelMessages = (p: Panel, msgs: EditMsg[]) => (sync ? setShared(msgs) : setPanel(p.id, { messages: msgs }))

  const toggleSync = (on: boolean) => {
    if (!on) setPanels((ps) => ps.map((p) => ({ ...p, messages: shared.map((m) => ({ ...m, id: uid() })) })))
    else if (panels[0]) setShared(panels[0].messages.length ? panels[0].messages : shared)
    setSync(on)
  }

  const run = useCallback(
    async (panel: Panel, msgs: EditMsg[]) => {
      aborts.current.get(panel.id)?.abort()
      let inputs: CompletionInputs
      try {
        const filled = msgs.map((m) => (m.json ? m : { ...m, content: fillVars(m.content, varsRef.current) }))
        inputs = buildInputs(panel.model, filled, panel.params)
      } catch (e) {
        setPanel(panel.id, { result: { running: false, error: (e as Error).message } })
        return
      }
      const ctrl = new AbortController()
      aborts.current.set(panel.id, ctrl)
      const t0 = performance.now()
      setPanel(panel.id, { result: { running: true, message: { role: 'assistant', content: '' }, streamed: true } })
      let msg: ChatMessage = { role: 'assistant', content: '' }
      let usage: RunResult['usage']
      let callId: string | undefined
      let finish: string | null = null
      let streamError: string | undefined
      const chunks: Any[] = []
      try {
        await api.completionsStream(
          projectId,
          inputs,
          (chunk) => {
            chunks.push(chunk)
            if (chunk?._meta?.span_id) callId = chunk._meta.span_id
            if (chunk?.error) streamError = typeof chunk.error === 'string' ? chunk.error : (chunk.error.message ?? JSON.stringify(chunk.error))
            if (chunk?.usage) usage = chunk.usage
            const ch = chunk?.choices?.[0]
            if (ch?.delta) msg = applyDelta(msg, ch.delta)
            if (ch?.message) msg = ch.message
            if (ch?.finish_reason) finish = ch.finish_reason
            setPanel(panel.id, { result: { running: true, message: msg, streamed: true } })
          },
          ctrl.signal,
        )
        setPanel(panel.id, {
          result: { running: false, message: msg, usage, callId, finishReason: finish, error: streamError, latencyMs: performance.now() - t0, raw: chunks, streamed: true },
        })
        record.current({
          id: callId ?? `local-${Date.now()}-${panel.id}`,
          at: new Date().toISOString(),
          mode: 'chat',
          model: panel.model,
          inputs: inputs as Any,
          output: messageText(msg),
          callId,
          latencyMs: performance.now() - t0,
          tokens: (usage as Any)?.total_tokens,
          error: streamError,
          source: 'local',
        })
      } catch (e) {
        if (ctrl.signal.aborted) {
          setPanel(panel.id, { result: { running: false, message: msg, error: 'Stopped', latencyMs: performance.now() - t0, callId, streamed: true } })
          return
        }
        const streamUnsupported = e instanceof ApiError && [404, 405, 501].includes(e.status)
        if (!streamUnsupported || chunks.length) {
          setPanel(panel.id, { result: { running: false, message: msg, error: (e as Error).message, latencyMs: performance.now() - t0, callId } })
          return
        }
        // Fall back to the non-streaming endpoint.
        try {
          const r = await api.completions(projectId, inputs)
          const resp = r.response
          record.current({
            id: r.span_id ?? `local-${Date.now()}-${panel.id}`,
            at: new Date().toISOString(),
            mode: 'chat',
            model: panel.model,
            inputs: inputs as Any,
            output: messageText(resp.choices?.[0]?.message),
            callId: r.span_id ?? undefined,
            latencyMs: performance.now() - t0,
            tokens: resp.usage?.total_tokens,
            error: resp.error,
            source: 'local',
          })
          setPanel(panel.id, {
            result: {
              running: false,
              message: resp.choices?.[0]?.message,
              finishReason: resp.choices?.[0]?.finish_reason,
              usage: resp.usage,
              callId: r.span_id ?? undefined,
              error: resp.error,
              latencyMs: performance.now() - t0,
              raw: resp,
            },
          })
        } catch (e2) {
          setPanel(panel.id, { result: { running: false, error: (e2 as Error).message, latencyMs: performance.now() - t0 } })
        }
      } finally {
        aborts.current.delete(panel.id)
      }
    },
    [projectId],
  )

  const runnable = (p: Panel) => !!p.model && models.find((m) => m.id === p.model)?.available !== false
  const runAll = () => panels.forEach((p) => runnable(p) && void run(p, panelMessages(p)))
  useEffect(() => {
    const h = (e: KeyboardEvent) => {
      if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
        e.preventDefault()
        runAll()
      }
    }
    window.addEventListener('keydown', h)
    return () => window.removeEventListener('keydown', h)
  })

  const addPanel = () =>
    setPanels((ps) =>
      ps.length >= MAX_PANELS
        ? ps
        : [...ps, { id: uid(), model: ps[ps.length - 1]?.model ?? pickDefaultModel(models), params: { ...(ps[ps.length - 1]?.params ?? DEFAULT_PARAMS) }, messages: (ps[ps.length - 1]?.messages ?? []).map((m) => ({ ...m, id: uid() })), result: null, showParams: false }],
    )

  const openRun = (run: PlaygroundRun, target: 'replace' | 'new') => {
    if (run.mode === 'image') {
      setMode('image')
      setImageInitial({ model: run.model, prompt: String(run.inputs.prompt ?? ''), size: typeof run.inputs.size === 'string' ? run.inputs.size : undefined, n: typeof run.inputs.n === 'number' ? run.inputs.n : undefined, nonce: Date.now() })
      return
    }
    const st = stateFromInputs(run.inputs)
    if (!st) {
      setLoadNote('That run has no chat messages to load.')
      return
    }
    setMode('chat')
    const result: RunResult | null = run.output || run.error ? { running: false, message: { role: 'assistant', content: run.output }, callId: run.callId, latencyMs: run.latencyMs, error: run.error } : null
    if (target === 'new' && panels.length < MAX_PANELS) {
      // A run with its own messages: give every panel its own list.
      const copies = panels.map((p) => ({ ...p, messages: (sync ? shared : p.messages).map((m) => ({ ...m, id: uid() })) }))
      setSync(false)
      setPanels([...copies, { id: uid(), model: st.model ?? pickDefaultModel(models), params: st.params, messages: st.msgs, result, showParams: false }])
    } else {
      if (sync) setShared(st.msgs)
      setPanels((ps) => ps.map((x, i) => (i === 0 ? { ...x, model: st.model ?? x.model, params: st.params, messages: sync ? x.messages : st.msgs, result } : x)))
    }
    setLoadNote(`Loaded a ${run.source === 'server' ? 'tracked' : 'previous'} run of ${run.model} from ${new Date(run.at).toLocaleString()}.`)
  }

  const loadPrompt = (o: WeftObject, fromUrl = false) => {
    const v = o.value ?? {}
    let msgs: EditMsg[] = []
    if (Array.isArray(v.messages)) msgs = v.messages.map(toEditMsg)
    else if (typeof v.content === 'string') msgs = [{ id: uid(), role: 'user', content: v.content }]
    else if (typeof v.prompt === 'string') msgs = [{ id: uid(), role: 'user', content: v.prompt }]
    if (!msgs.length) {
      setLoadNote(`Prompt ${o.name} has no messages.`)
      return
    }
    if (sync) setShared(msgs)
    else setPanels((ps) => ps.map((p) => ({ ...p, messages: msgs.map((m) => ({ ...m, id: uid() })) })))
    setLoadNote(`Loaded prompt ${o.name} (v${o.version}).`)
    // A prompt picked from the menu replaces whatever the URL pointed at.
    if (!fromUrl && (fromCall || fromPrompt)) setParams({}, { replace: true })
  }

  // Load a saved prompt from the URL (?prompt=<object ref or object id>).
  const promptQ = useQuery({
    queryKey: ['playground-prompt', projectId, fromPrompt],
    queryFn: async () => {
      const ref = parseRef(fromPrompt!)
      if (ref && ref.kind !== 'object') throw new Error(`${fromPrompt} is not an object ref`)
      return ref ? api.objectGet(ref.projectId, ref.name, ref.digest ?? 'latest') : api.objectGet(projectId, fromPrompt!, 'latest')
    },
    enabled: !!fromPrompt,
    staleTime: Infinity,
  })
  useEffect(() => {
    if (promptQ.data) loadPrompt(promptQ.data, true)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [promptQ.data])

  return (
    <div className="page playground">
      <div className="page-header">
        <h1>Playground</h1>
        <PromptPicker projectId={projectId} onPick={loadPrompt} />
        <button className="btn sm" onClick={() => setSaveOpen(true)}>
          Save as prompt
        </button>
        <button className="btn sm" onClick={() => setSaveModelOpen(true)} title="Save a panel's model, parameters and messages as a model you can evaluate">
          Save as model
        </button>
        <div className="seg" role="group" aria-label="Playground mode">
          <button className={mode === 'chat' ? 'on' : ''} aria-pressed={mode === 'chat'} onClick={() => setMode('chat')}>
            Chat
          </button>
          <button className={mode === 'image' ? 'on' : ''} aria-pressed={mode === 'image'} onClick={() => setMode('image')}>
            Image
          </button>
        </div>
        <button className={`btn sm ${showHistory ? 'primary' : ''}`} aria-pressed={showHistory} onClick={() => toggleHistory(!showHistory)} title="Past runs in this browser and tracked spans">
          History{history.length ? ` (${history.length})` : ''}
        </button>
        <span className="spacer" />
        {mode === 'chat' && (
        <>
        <label className="checkbox small" title="Edit one message list shared by all panels">
          <input type="checkbox" checked={sync} onChange={(e) => toggleSync(e.target.checked)} /> Sync messages
        </label>
        <button className="btn sm" onClick={addPanel} disabled={panels.length >= MAX_PANELS} title={panels.length >= MAX_PANELS ? `Up to ${MAX_PANELS} panels` : 'Compare another model'}>
          + Add model
        </button>
        <button className="btn sm primary" onClick={runAll} title="Run all panels (Ctrl+Enter)">
          Run{panels.length > 1 ? ' all' : ''} <kbd className="kbd-inline">⌘↵</kbd>
        </button>
        </>
        )}
      </div>
      {loadNote && (
        <div className="info-box row">
          <span className="grow">{loadNote}</span>
          <button className="btn ghost sm" onClick={() => setLoadNote(null)} aria-label="Dismiss">
            ✕
          </button>
        </div>
      )}
      <ErrorBox error={modelsQ.error ?? callQ.error ?? promptQ.error} />
      <div className={showHistory ? 'pg-layout' : undefined}>
      <div className="col" style={{ gap: 14, minWidth: 0 }}>
      {mode === 'image' ? (
        <ImageMode
          projectId={projectId}
          models={models}
          initial={imageInitial}
          onRun={(inputs, r) =>
            record.current({
              id: r.callId ?? `local-${Date.now()}`,
              at: new Date().toISOString(),
              mode: 'image',
              model: inputs.model,
              inputs: { ...inputs },
              output: `${r.response?.data?.length ?? 0} image${r.response?.data?.length === 1 ? '' : 's'}`,
              callId: r.callId,
              latencyMs: r.latencyMs,
              error: r.error,
              images: r.response?.data?.length ?? 0,
              source: 'local',
            })
          }
        />
      ) : (
      <>
      <VariablesCard names={templateVars(sync ? shared : panels.flatMap((p) => p.messages))} values={vars} onChange={setVars} />
      <div className="pg-grid" style={{ gridTemplateColumns: `repeat(${panels.length}, minmax(300px, 1fr))` }}>
        {panels.map((p, i) => (
          <PanelView
            key={p.id}
            projectId={projectId}
            panel={p}
            index={i}
            models={models}
            modelsLoading={modelsQ.isLoading}
            messages={panelMessages(p)}
            onMessages={(m) => setPanelMessages(p, m)}
            onChange={(patch) => setPanel(p.id, patch)}
            onRemove={panels.length > 1 ? () => setPanels((ps) => ps.filter((x) => x.id !== p.id)) : undefined}
            onRun={() => void run(p, panelMessages(p))}
            onStop={() => aborts.current.get(p.id)?.abort()}
          />
        ))}
      </div>
      </>
      )}
      </div>
      {showHistory && (
        <HistoryPanel
          projectId={projectId}
          local={history}
          onOpen={openRun}
          canAddPanel={panels.length < MAX_PANELS}
          onClear={() => {
            setHistory([])
            saveRuns(projectId, [])
          }}
          onClose={() => toggleHistory(false)}
        />
      )}
      </div>
      {saveModelOpen && <SaveModelModal projectId={projectId} panels={panels.map((p) => ({ ...p, messages: panelMessages(p) }))} onClose={() => setSaveModelOpen(false)} />}
      {saveOpen && <SavePromptModal projectId={projectId} messages={sync ? shared : (panels[0]?.messages ?? [])} onClose={() => setSaveOpen(false)} />}
    </div>
  )
}

// ---------------------------------------------------------------- pieces

function PromptPicker({ projectId, onPick }: { projectId: string; onPick: (o: WeftObject) => void }) {
  const prompts = useQuery({
    queryKey: ['objects', projectId, 'Prompt', 'latest'],
    queryFn: () => api.objectsQuery({ project_id: projectId, filter: { kinds: ['prompt'], latest_only: true }, sort_by: [{ field: 'created_at', direction: 'desc' }], limit: 200 }),
  })
  const list = (prompts.data?.items ?? []).filter((o) => !o.deleted_at)
  return (
    <Dropdown label="Load prompt ▾" className="btn sm">
      {(close) => (
        <>
          {prompts.isLoading && <div className="dd-note">Loading…</div>}
          {!prompts.isLoading && list.length === 0 && <div className="dd-note">No saved prompts in this project.</div>}
          {list.map((o) => (
            <MenuItem
              key={o.name}
              onClick={() => {
                onPick(o)
                close()
              }}
            >
              <span className="grow">{o.name}</span>
              <span className="faint small">{o.class ?? o.kind}</span>
            </MenuItem>
          ))}
        </>
      )}
    </Dropdown>
  )
}

function VariablesCard({ names, values, onChange }: { names: string[]; values: Record<string, string>; onChange: (v: Record<string, string>) => void }) {
  if (!names.length) return null
  return (
    <div className="card">
      <div className="card-header">
        <h3 className="grow">Variables</h3>
        <span className="small faint">Filled into {'{placeholders}'} when you run; saved prompts and models keep the placeholders.</span>
      </div>
      <div className="card-body" style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(220px, 1fr))', gap: 10 }}>
        {names.map((n) => (
          <label key={n} className="field">
            <span className="mono">{n}</span>
            <textarea
              className="input"
              style={{ minHeight: 34 }}
              rows={1}
              value={values[n] ?? ''}
              onChange={(e) => onChange({ ...values, [n]: e.target.value })}
              placeholder={`Value for {${n}}`}
            />
          </label>
        ))}
      </div>
    </div>
  )
}

function SaveModelModal({ projectId, panels, onClose }: { projectId: string; panels: Panel[]; onClose: () => void }) {
  const qc = useQueryClient()
  const [name, setName] = useState('')
  const [idx, setIdx] = useState(0)
  const panel = panels[Math.min(idx, panels.length - 1)]
  const save = useMutation({
    mutationFn: async () => {
      const id = slugify(name)
      if (!id) throw new Error('Enter a name')
      const p = panel.params
      const default_params: Record<string, Any> = {
        messages_template: panel.messages.map(toApiMsg),
        temperature: p.temperature,
        max_tokens: p.max_tokens,
      }
      if (p.top_p !== 1) default_params.top_p = p.top_p
      if (p.json) default_params.response_format = 'json_object'
      const val = { _class_name: 'LLMStructuredCompletionModel', _bases: ['Model', 'Object', 'BaseModel'], name: id, llm_model_id: panel.model, default_params }
      const r = await api.objectSave(projectId, id, val, 'LLMStructuredCompletionModel')
      return { id, ...r }
    },
    onSuccess: () => void qc.invalidateQueries({ queryKey: ['objects', projectId] }),
  })
  return (
    <Modal
      title="Save as model"
      onClose={onClose}
      footer={
        save.data ? (
          <button className="btn primary" onClick={onClose}>
            Done
          </button>
        ) : (
          <>
            <button className="btn" onClick={onClose}>
              Cancel
            </button>
            <button className="btn primary" disabled={!name.trim() || !panel?.model || save.isPending} onClick={() => save.mutate()}>
              Save
            </button>
          </>
        )
      }
    >
      {save.data ? (
        <div className="success-box">
          Saved model <b>{save.data.id}</b> (v{save.data.version}). It can now be run in{' '}
          <Link to={`/${projectId}/evaluations/new`}>a new evaluation</Link>.
        </div>
      ) : (
        <>
          {panels.length > 1 && (
            <label className="field">
              Panel
              <select className="input" value={idx} onChange={(e) => setIdx(Number(e.target.value))}>
                {panels.map((p, i) => (
                  <option key={p.id} value={i}>
                    Panel {i + 1}: {p.model}
                  </option>
                ))}
              </select>
            </label>
          )}
          <label className="field">
            Model name
            <input className="input" autoFocus value={name} onChange={(e) => setName(e.target.value)} placeholder="e.g. support-gpt4o-v2" />
            {name && slugify(name) !== name && <span className="faint">Saved as {slugify(name)}</span>}
          </label>
          <div className="small muted">
            Saves <code>{panel?.model}</code> with this panel's parameters and {panel?.messages.length ?? 0} messages as the template. Dataset columns fill the{' '}
            {'{placeholders}'} when the model is evaluated.
          </div>
        </>
      )}
      <ErrorBox error={save.error} />
    </Modal>
  )
}

function SavePromptModal({ projectId, messages, onClose }: { projectId: string; messages: EditMsg[]; onClose: () => void }) {
  const qc = useQueryClient()
  const [name, setName] = useState('')
  const save = useMutation({
    mutationFn: async () => {
      const id = slugify(name)
      if (!id) throw new Error('Enter a name')
      const msgs = messages.map(toApiMsg)
      const r = await api.objectSave(projectId, id, { _class_name: 'MessagesPrompt', _bases: ['Prompt', 'Object', 'BaseModel'], name: id, messages: msgs }, 'MessagesPrompt')
      return { id, ...r }
    },
    onSuccess: () => void qc.invalidateQueries({ queryKey: ['objects', projectId] }),
  })
  return (
    <Modal
      title="Save messages as a prompt"
      onClose={onClose}
      footer={
        save.data ? (
          <button className="btn primary" onClick={onClose}>
            Done
          </button>
        ) : (
          <>
            <button className="btn" onClick={onClose}>
              Cancel
            </button>
            <button className="btn primary" disabled={!name.trim() || save.isPending} onClick={() => save.mutate()}>
              Save
            </button>
          </>
        )
      }
    >
      {save.data ? (
        <div className="success-box">
          Saved <b>{save.data.id}</b> as version v{save.data.version}.
        </div>
      ) : (
        <>
          <form
            onSubmit={(e) => {
              e.preventDefault()
              if (name.trim()) save.mutate()
            }}
          >
            <label className="field">
              Prompt name
              <input className="input" autoFocus value={name} onChange={(e) => setName(e.target.value)} placeholder="e.g. support-agent" />
              {name && slugify(name) !== name && <span className="faint">Saved as {slugify(name)}</span>}
            </label>
          </form>
          <div className="small muted">
            {messages.length} message{messages.length === 1 ? '' : 's'} will be saved as a <code>MessagesPrompt</code>. Saving under an existing name creates a new version.
          </div>
        </>
      )}
      <ErrorBox error={save.error} />
    </Modal>
  )
}

function ModelSelect({ models, value, onChange, loading }: { models: LLMModelInfo[]; value: string; onChange: (v: string) => void; loading: boolean }) {
  const groups = useMemo(() => {
    const g = new Map<string, LLMModelInfo[]>()
    for (const m of models) g.set(m.provider, [...(g.get(m.provider) ?? []), m])
    return [...g.entries()].sort(([a], [b]) => (a === 'mock' ? 1 : b === 'mock' ? -1 : a.localeCompare(b)))
  }, [models])
  const known = models.some((m) => m.id === value)
  return (
    <select className="input grow" value={value} onChange={(e) => onChange(e.target.value)} aria-label="Model" disabled={loading}>
      {loading && <option>Loading models…</option>}
      {!known && value && <option value={value}>{value}</option>}
      {groups.map(([provider, ms]) => (
        <optgroup key={provider} label={provider}>
          {ms.map((m) => (
            <option key={m.id} value={m.id} disabled={!m.available}>
              {m.label || m.id}
              {!m.available ? ' — add API key in Settings' : ''}
            </option>
          ))}
        </optgroup>
      ))}
    </select>
  )
}

function NumParam({ label, value, onChange, min, max, step }: { label: string; value: number; onChange: (v: number) => void; min: number; max: number; step: number }) {
  return (
    <label className="field pg-param">
      <span className="row">
        <span className="grow">{label}</span>
        <input
          className="input pg-num"
          type="number"
          value={value}
          min={min}
          max={max}
          step={step}
          onChange={(e) => e.target.value !== '' && onChange(Number(e.target.value))}
        />
      </span>
      <input type="range" min={min} max={max} step={step} value={value} onChange={(e) => onChange(Number(e.target.value))} aria-label={label} />
    </label>
  )
}

function ParamsEditor({ params, onChange, maxTokens }: { params: Params; onChange: (p: Params) => void; maxTokens: number }) {
  const set = <K extends keyof Params>(k: K, v: Params[K]) => onChange({ ...params, [k]: v })
  let toolsError: string | null = null
  if (params.tools.trim()) {
    try {
      if (!Array.isArray(JSON.parse(params.tools))) toolsError = 'Must be a JSON array'
    } catch {
      toolsError = 'Invalid JSON'
    }
  }
  return (
    <div className="pg-params">
      <NumParam label="Temperature" value={params.temperature} onChange={(v) => set('temperature', v)} min={0} max={2} step={0.05} />
      <NumParam label="Max tokens" value={params.max_tokens} onChange={(v) => set('max_tokens', Math.round(v))} min={1} max={maxTokens} step={1} />
      <NumParam label="Top P" value={params.top_p} onChange={(v) => set('top_p', v)} min={0} max={1} step={0.01} />
      <NumParam label="Frequency penalty" value={params.frequency_penalty} onChange={(v) => set('frequency_penalty', v)} min={-2} max={2} step={0.1} />
      <NumParam label="Presence penalty" value={params.presence_penalty} onChange={(v) => set('presence_penalty', v)} min={-2} max={2} step={0.1} />
      <label className="field">
        Stop sequences (comma separated)
        <input className="input" value={params.stop} onChange={(e) => set('stop', e.target.value)} placeholder="e.g. ###, END" />
      </label>
      <label className="checkbox">
        <input type="checkbox" checked={params.json} onChange={(e) => set('json', e.target.checked)} /> JSON response format
      </label>
      <label className="field">
        Tools (JSON array, OpenAI format)
        <textarea
          className="input mono"
          value={params.tools}
          onChange={(e) => set('tools', e.target.value)}
          placeholder='[{"type": "function", "function": {"name": "get_weather", "parameters": {"type": "object", "properties": {"city": {"type": "string"}}}}}]'
          style={{ minHeight: 60 }}
        />
        {toolsError && <span style={{ color: 'var(--error)' }}>{toolsError}</span>}
      </label>
    </div>
  )
}

function MessagesEditor({ messages, onChange }: { messages: EditMsg[]; onChange: (m: EditMsg[]) => void }) {
  const update = (i: number, patch: Partial<EditMsg>) => onChange(messages.map((m, j) => (j === i ? { ...m, ...patch } : m)))
  const move = (i: number, d: number) => {
    const j = i + d
    if (j < 0 || j >= messages.length) return
    const next = [...messages]
    ;[next[i], next[j]] = [next[j], next[i]]
    onChange(next)
  }
  return (
    <div className="col pg-messages">
      {messages.map((m, i) => {
        const lines = m.content.split('\n').length
        let jsonErr = false
        if (m.json)
          try {
            JSON.parse(m.content)
          } catch {
            jsonErr = true
          }
        return (
          <div key={m.id} className={`pg-msg role-${m.role}`}>
            <div className="row">
              <select className="input pg-role" value={m.role} onChange={(e) => update(i, { role: e.target.value })} aria-label="Role">
                {[...new Set([...ROLES, m.role])].map((r) => (
                  <option key={r}>{r}</option>
                ))}
              </select>
              {m.json && <span className={`badge ${jsonErr ? 'error' : ''}`}>{jsonErr ? 'invalid JSON' : 'JSON parts'}</span>}
              {m.extra?.tool_calls && <span className="badge accent" title={JSON.stringify(m.extra.tool_calls)}>{(m.extra.tool_calls as Any[]).length} tool call(s)</span>}
              {m.extra?.tool_call_id && <span className="faint small mono">↩ {String(m.extra.tool_call_id)}</span>}
              <span className="grow" />
              <button className="btn ghost sm icon" onClick={() => move(i, -1)} disabled={i === 0} aria-label="Move up" title="Move up">
                ↑
              </button>
              <button className="btn ghost sm icon" onClick={() => move(i, 1)} disabled={i === messages.length - 1} aria-label="Move down" title="Move down">
                ↓
              </button>
              <button className="btn ghost sm icon" onClick={() => onChange(messages.filter((_, j) => j !== i))} aria-label="Remove message" title="Remove">
                ✕
              </button>
            </div>
            <textarea
              className={`input ${m.json ? 'mono' : ''}`}
              rows={Math.min(16, Math.max(2, lines))}
              value={m.content}
              placeholder={m.role === 'system' ? 'System instructions…' : m.role === 'user' ? 'Enter a message…' : ''}
              onChange={(e) => update(i, { content: e.target.value })}
              aria-label={`${m.role} message`}
            />
          </div>
        )
      })}
      <div className="row">
        <button className="btn sm" onClick={() => onChange([...messages, { id: uid(), role: messages[messages.length - 1]?.role === 'user' ? 'assistant' : 'user', content: '' }])}>
          + Message
        </button>
      </div>
    </div>
  )
}

function PanelView({
  projectId,
  panel,
  index,
  models,
  modelsLoading,
  messages,
  onMessages,
  onChange,
  onRemove,
  onRun,
  onStop,
}: {
  projectId: string
  panel: Panel
  index: number
  models: LLMModelInfo[]
  modelsLoading: boolean
  messages: EditMsg[]
  onMessages: (m: EditMsg[]) => void
  onChange: (patch: Partial<Panel>) => void
  onRemove?: () => void
  onRun: () => void
  onStop: () => void
}) {
  const [raw, setRaw] = useState(false)
  const info = models.find((m) => m.id === panel.model)
  const unavailable = info && !info.available
  const r = panel.result
  const hasContent = !!r?.message && ((typeof r.message.content === 'string' && r.message.content.length > 0) || !!r.message.tool_calls?.length || Array.isArray(r.message.content))
  return (
    <div className="card pg-panel">
      <div className="card-header">
        <span className="faint small">{index + 1}</span>
        <ModelSelect models={models} value={panel.model} onChange={(model) => onChange({ model })} loading={modelsLoading} />
        <button className={`btn sm ${panel.showParams ? 'primary' : ''}`} onClick={() => onChange({ showParams: !panel.showParams })} aria-expanded={panel.showParams} title="Parameters">
          ⚙
        </button>
        {onRemove && (
          <button className="btn ghost sm icon" onClick={onRemove} aria-label="Remove panel" title="Remove panel">
            ✕
          </button>
        )}
      </div>
      <div className="card-body col" style={{ gap: 12 }}>
        {unavailable && (
          <div className="warn-box small">
            No API key configured for {info.provider}. <Link to={settingsUrl(projectId, 'secrets')}>Add one in Settings</Link>, or use <code>mock/echo</code>.
          </div>
        )}
        {!info && panel.model && !modelsLoading && (
          <div className="warn-box small">
            <code>{panel.model}</code> is not in this server's model list. It runs only if the server can route it (for example a <code>&lt;provider&gt;/&lt;model&gt;</code> id for a
            configured provider).
          </div>
        )}
        {panel.showParams && <ParamsEditor params={panel.params} onChange={(params) => onChange({ params })} maxTokens={info?.max_tokens || 32768} />}
        <MessagesEditor messages={messages} onChange={onMessages} />
        <div className="row">
          {r?.running ? (
            <button className="btn sm" onClick={onStop}>
              Stop
            </button>
          ) : (
            <button className="btn sm primary" onClick={onRun} disabled={!panel.model || unavailable || messages.length === 0}>
              Run
            </button>
          )}
          {r?.running && <Spinner label="Generating…" />}
          <span className="grow" />
          {r && !r.running && r.latencyMs != null && <span className="small mono muted">{fmtDuration(r.latencyMs)}</span>}
          {r?.usage?.total_tokens != null && (
            <span className="small mono muted" title={`${r.usage.prompt_tokens ?? 0} prompt · ${r.usage.completion_tokens ?? 0} completion`}>
              {fmtNumber(r.usage.total_tokens)} tok
            </span>
          )}
          {r?.callId && (
            <Link className="small" to={spanUrl(projectId, r.callId)}>
              View call
            </Link>
          )}
        </div>
        {r && (
          <div className="pg-response">
            {r.error && <div className={r.error === 'Stopped' ? 'warn-box small' : 'error-box'}>{r.error}</div>}
            {hasContent && (
              <>
                <div className="row">
                  <span className="small muted grow">Response{r.finishReason ? ` · finish: ${r.finishReason}` : ''}</span>
                  {!r.running && (
                    <>
                      <button className="btn ghost sm" onClick={() => setRaw((x) => !x)}>
                        {raw ? 'Formatted' : 'Raw'}
                      </button>
                      <button
                        className="btn ghost sm"
                        title="Append this response as an assistant message"
                        onClick={() => onMessages([...messages, toEditMsg({ ...r.message, role: 'assistant' }), { id: uid(), role: 'user', content: '' }])}
                      >
                        + Add to conversation
                      </button>
                    </>
                  )}
                </div>
                {raw ? <JsonBlock value={r.raw ?? r.message} /> : <ChatMessageView message={normaliseMessage(r.message)} projectId={projectId} />}
              </>
            )}
          </div>
        )}
      </div>
    </div>
  )
}
