import { useState } from 'react'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import type { Any, Span } from '../../api/types'
import { MediaView, ValueView, isMedia } from '../ValueView'
import { isReferenceUri } from '../media/mediaUtils'

// Chat-shaped data detection and rendering. Handles OpenAI chat messages and
// completions, OpenAI Responses API items, Anthropic content blocks, and the
// OpenTelemetry gen_ai message shape ({role, parts:[{type, content}]}).

export type ChatPart =
  | { type: 'text'; text: string }
  | { type: 'image'; url: string }
  | { type: 'tool_call'; id?: string; name: string; arguments: Any }
  | { type: 'tool_result'; id?: string; content: Any }
  | { type: 'other'; value: Any }

export interface NormMessage {
  role: string
  name?: string
  parts: ChatPart[]
  toolCallId?: string
  finishReason?: string
}

const isObj = (v: unknown): v is Record<string, Any> => !!v && typeof v === 'object' && !Array.isArray(v)

function isMessageLike(m: unknown): boolean {
  if (!isObj(m)) return false
  if (typeof m.role === 'string') return 'content' in m || 'parts' in m || 'tool_calls' in m || 'function_call' in m
  // Responses API items without role
  return m.type === 'message' || m.type === 'function_call' || m.type === 'function_call_output'
}

/** True when `v` looks like a list of chat messages. */
export function isChatMessages(v: unknown): v is Any[] {
  return Array.isArray(v) && v.length > 0 && v.every(isMessageLike) && v.some((m) => typeof (m as Any).role === 'string')
}

function parseArgs(a: Any): Any {
  if (typeof a !== 'string') return a
  try {
    return JSON.parse(a)
  } catch {
    return a
  }
}

function partFrom(p: Any): ChatPart | null {
  if (p == null) return null
  if (typeof p === 'string') return { type: 'text', text: p }
  if (!isObj(p)) return { type: 'other', value: p }
  switch (p.type) {
    case 'text':
    case 'input_text':
    case 'output_text':
      return { type: 'text', text: String(p.text ?? p.content ?? '') }
    case 'image_url':
      return { type: 'image', url: typeof p.image_url === 'string' ? p.image_url : p.image_url?.url ?? '' }
    case 'input_image':
      return { type: 'image', url: p.image_url ?? '' }
    case 'image':
      if (p.source?.type === 'base64') return { type: 'image', url: `data:${p.source.media_type};base64,${p.source.data}` }
      if (p.source?.url) return { type: 'image', url: p.source.url }
      return { type: 'other', value: p }
    case 'tool_use':
      return { type: 'tool_call', id: p.id, name: p.name, arguments: p.input }
    case 'tool_call':
      return { type: 'tool_call', id: p.id, name: p.name, arguments: parseArgs(p.arguments) }
    case 'function_call':
      return { type: 'tool_call', id: p.call_id ?? p.id, name: p.name, arguments: parseArgs(p.arguments) }
    case 'tool_result':
      return { type: 'tool_result', id: p.tool_use_id, content: normaliseToolResult(p.content) }
    case 'tool_call_response':
      return { type: 'tool_result', id: p.id, content: p.response ?? p.result }
    case 'refusal':
      return { type: 'text', text: `Refusal: ${p.refusal}` }
    case 'thinking':
      return { type: 'text', text: `*Thinking:* ${p.thinking ?? ''}` }
  }
  if (typeof p.text === 'string') return { type: 'text', text: p.text }
  if (typeof p.content === 'string' && p.type == null) return { type: 'text', text: p.content }
  return { type: 'other', value: p }
}

function normaliseToolResult(c: Any): Any {
  if (Array.isArray(c) && c.every((x) => isObj(x) && x.type === 'text')) return c.map((x) => x.text).join('\n')
  return c
}

function contentParts(c: Any): ChatPart[] {
  if (c == null) return []
  if (typeof c === 'string') return c ? [{ type: 'text', text: c }] : []
  if (Array.isArray(c)) return c.map(partFrom).filter(Boolean) as ChatPart[]
  const p = partFrom(c)
  return p ? [p] : []
}

/** Normalise one message of any supported shape. */
export function normaliseMessage(m: Any): NormMessage {
  if (!isObj(m)) return { role: 'unknown', parts: contentParts(m) }
  // Responses API items
  if (m.type === 'function_call') return { role: 'assistant', parts: [partFrom(m)!] }
  if (m.type === 'function_call_output')
    return { role: 'tool', toolCallId: m.call_id, parts: [{ type: 'tool_result', id: m.call_id, content: parseArgs(m.output) }] }
  const role = String(m.role ?? (m.type === 'message' ? 'assistant' : 'unknown'))
  const parts: ChatPart[] = []
  if ('parts' in m && Array.isArray(m.parts)) parts.push(...contentParts(m.parts))
  if ('content' in m) {
    if (role === 'tool' && m.content != null && typeof m.content !== 'string' && !Array.isArray(m.content))
      parts.push({ type: 'tool_result', id: m.tool_call_id, content: m.content })
    else parts.push(...contentParts(m.content))
  }
  if (Array.isArray(m.tool_calls))
    for (const tc of m.tool_calls) {
      const fn = tc.function ?? tc
      parts.push({ type: 'tool_call', id: tc.id, name: fn.name ?? '?', arguments: parseArgs(fn.arguments ?? fn.input) })
    }
  if (isObj(m.function_call)) parts.push({ type: 'tool_call', name: m.function_call.name, arguments: parseArgs(m.function_call.arguments) })
  return {
    role,
    name: typeof m.name === 'string' ? m.name : undefined,
    parts,
    toolCallId: m.tool_call_id,
    finishReason: m.finish_reason ?? undefined,
  }
}

export function normaliseMessages(msgs: Any[]): NormMessage[] {
  return msgs.map(normaliseMessage)
}

const INPUT_MESSAGE_KEYS = ['messages', 'gen_ai.input.messages', 'input_messages', 'input', 'prompt', 'contents', 'history']

/** Find the chat messages in a call's inputs. */
export function findInputMessages(inputs: Any): Any[] | null {
  if (isChatMessages(inputs)) return inputs
  if (!isObj(inputs)) return null
  for (const k of INPUT_MESSAGE_KEYS) {
    const v = inputs[k]
    if (isChatMessages(v)) return v
    if (isObj(v) && isChatMessages(v.messages)) return v.messages
  }
  // OpenAI SDK wrappers sometimes nest kwargs
  if (isObj(inputs.kwargs) && isChatMessages(inputs.kwargs.messages)) return inputs.kwargs.messages
  return null
}

/** Find assistant messages in a call output. */
export function findOutputMessages(output: Any): Any[] | null {
  if (output == null) return null
  if (isChatMessages(output)) return output
  if (!isObj(output)) return null
  if (Array.isArray(output.choices) && output.choices.length) {
    const msgs = output.choices
      .map((c: Any) => (isObj(c.message) ? { ...c.message, finish_reason: c.finish_reason } : typeof c.text === 'string' ? { role: 'assistant', content: c.text } : null))
      .filter(Boolean)
    return msgs.length ? msgs : null
  }
  // Anthropic message
  if (Array.isArray(output.content) && (output.role === 'assistant' || output.type === 'message'))
    return [{ role: output.role ?? 'assistant', content: output.content, finish_reason: output.stop_reason }]
  // Responses API
  if (Array.isArray(output.output) && output.output.every(isObj) && output.output.some((o: Any) => o.type === 'message' || o.type === 'function_call'))
    return output.output.filter((o: Any) => o.type === 'message' || o.type === 'function_call')
  if (typeof output.role === 'string' && ('content' in output || 'tool_calls' in output || 'parts' in output)) return [output]
  for (const k of ['messages', 'gen_ai.output.messages', 'output_messages']) if (isChatMessages(output[k])) return output[k]
  if (isObj(output.message) && typeof output.message.role === 'string') return [output.message]
  return null
}

export interface CallChat {
  input: Any[]
  output: Any[] | null
  /** extra system prompt (Anthropic `system` kwarg) */
  system?: string
}

/** Extract a chat view from a call, or null when its data is not chat-shaped. */
export function extractChat(call: Pick<Span, 'inputs' | 'output'>): CallChat | null {
  const input = findInputMessages(call.inputs)
  if (!input) return null
  let output = findOutputMessages(call.output)
  if (!output && typeof call.output === 'string') output = [{ role: 'assistant', content: call.output }]
  const sys = isObj(call.inputs) ? call.inputs.system : undefined
  const system = typeof sys === 'string' ? sys : Array.isArray(sys) ? sys.map((b: Any) => b?.text ?? '').join('\n') : undefined
  return { input, output, system }
}

/** Plain text of a message's text parts. */
export function messageText(m: NormMessage): string {
  return m.parts
    .filter((p) => p.type === 'text')
    .map((p) => (p as { text: string }).text)
    .join('\n')
}

// ---------------------------------------------------------------- rendering

function ToolCallView({ part, projectId }: { part: Extract<ChatPart, { type: 'tool_call' }>; projectId: string }) {
  const [raw, setRaw] = useState(false)
  return (
    <div className="chat-tool">
      <div className="row small">
        <span className="badge accent">tool call</span>
        <span className="mono">{part.name}</span>
        {part.id && <span className="faint mono small">{part.id}</span>}
        <span className="grow" />
        <button className="btn ghost sm" onClick={() => setRaw((r) => !r)}>
          {raw ? 'Tree' : 'JSON'}
        </button>
      </div>
      <div style={{ marginTop: 6 }}>
        {raw || typeof part.arguments !== 'object' ? (
          <pre className="code-block">{typeof part.arguments === 'string' ? part.arguments : JSON.stringify(part.arguments, null, 2)}</pre>
        ) : (
          <ValueView value={part.arguments} projectId={projectId} />
        )}
      </div>
    </div>
  )
}

function PartView({ part, projectId }: { part: ChatPart; projectId: string }) {
  switch (part.type) {
    case 'text':
      // A message whose whole text is a bucket URI renders the object.
      if (isReferenceUri(part.text)) return <MediaView value={part.text} projectId={projectId} />
      return (
        <div className="markdown chat-text">
          <ReactMarkdown remarkPlugins={[remarkGfm]}>{part.text}</ReactMarkdown>
        </div>
      )
    case 'image':
      if (isReferenceUri(part.url)) return <MediaView value={{ _type: 'Image', uri: part.url.trim() }} projectId={projectId} />
      return part.url.startsWith('data:') ? (
        <MediaView value={part.url} projectId={projectId} />
      ) : (
        <a href={part.url} target="_blank" rel="noreferrer">
          <img src={part.url} alt="" style={{ maxWidth: 360, borderRadius: 6 }} />
        </a>
      )
    case 'tool_call':
      return <ToolCallView part={part} projectId={projectId} />
    case 'tool_result':
      return (
        <div className="chat-tool">
          <div className="row small">
            <span className="badge">tool result</span>
            {part.id && <span className="faint mono small">{part.id}</span>}
          </div>
          <div style={{ marginTop: 6 }}>
            {typeof part.content === 'string' && !isMedia(part.content) ? (
              <pre className="small">{part.content}</pre>
            ) : (
              <ValueView value={part.content} projectId={projectId} />
            )}
          </div>
        </div>
      )
    default:
      return <ValueView value={part.value} projectId={projectId} />
  }
}

export function ChatMessageView({ message, projectId, label }: { message: NormMessage; projectId: string; label?: string }) {
  const role = message.role.toLowerCase()
  return (
    <div className={`chat-msg role-${role}`}>
      <div className="chat-role">
        <span>{label ?? (role ? role[0].toUpperCase() + role.slice(1) : role)}</span>
        {message.name && <span className="faint mono">{message.name}</span>}
        {message.toolCallId && <span className="faint mono">↩ {message.toolCallId}</span>}
        {message.finishReason && <span className="faint small">finish: {message.finishReason}</span>}
      </div>
      <div className="chat-body">
        {message.parts.length === 0 ? <span className="faint small">(empty)</span> : message.parts.map((p, i) => <PartView key={i} part={p} projectId={projectId} />)}
      </div>
    </div>
  )
}

/** Render a list of chat messages of any supported shape. */
export function ChatView({ messages, projectId, outputStart }: { messages: Any[]; projectId: string; outputStart?: number }) {
  const norm = normaliseMessages(messages)
  return (
    <div className="chat">
      {norm.map((m, i) => (
        <div key={i}>
          {outputStart === i && <div className="chat-divider">Output</div>}
          <ChatMessageView message={m} projectId={projectId} />
        </div>
      ))}
    </div>
  )
}

/** Inputs and output of a call as one conversation. */
export function CallChatView({ chat, projectId }: { chat: CallChat; projectId: string }) {
  const msgs: Any[] = []
  if (chat.system && !chat.input.some((m) => m?.role === 'system')) msgs.push({ role: 'system', content: chat.system })
  msgs.push(...chat.input)
  const outputStart = chat.output ? msgs.length : undefined
  if (chat.output) msgs.push(...chat.output)
  return <ChatView messages={msgs} projectId={projectId} outputStart={outputStart} />
}
