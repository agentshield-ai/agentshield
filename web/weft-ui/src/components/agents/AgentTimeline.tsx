import { useState, type ReactNode } from 'react'
import { ScoreText } from './bits'
import { StatusGlyph } from '../ui/StatusGlyph'
import { turnSignals } from './signalUtils'
import { Link } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { api } from '../../api/client'
import type { ChatMessage, TraceChat } from '../../api/agents'
import type { Any } from '../../api/types'
import { fmtCost, fmtDate, fmtDuration, fmtNumber } from '../../lib/format'
import { spanUrl } from '../../lib/urls'
import { ErrorBox, Spinner } from '../common'
import { MediaView, ValueView, isMedia } from '../ValueView'
import { CallChatView, ChatMessageView, extractChat, normaliseMessage } from '../traces/ChatView'
import { parseMaybeJSON } from './agentUtils'
import './agents.css'

function Status({ code }: { code: string | null | undefined }) {
  if (code === 'ERROR')
    return (
      <span className="row small" style={{ gap: 4, display: 'inline-flex' }}>
        <StatusGlyph kind="failed" size={14} decorative /> failed
      </span>
    )
  if (code === 'UNSET')
    return (
      <span className="row small" style={{ gap: 4, display: 'inline-flex' }}>
        <StatusGlyph kind="running" size={14} decorative /> running
      </span>
    )
  return null
}

/** The full call behind an LLM span: every input message and the output. */
function CallDetail({ projectId, callId }: { projectId: string; callId: string }) {
  const q = useQuery({ queryKey: ['span', projectId, callId, 'agent-detail'], queryFn: () => api.spanFind(projectId, callId, { include_costs: true }) })
  if (q.isLoading) return <Spinner label="Loading span…" />
  if (q.error) return <ErrorBox error={q.error} />
  const call = q.data
  if (!call) return <span className="faint small">Span not found.</span>
  const chat = extractChat(call)
  return chat ? (
    <CallChatView chat={chat} projectId={projectId} />
  ) : (
    <div className="grid-2">
      <div>
        <div className="small muted">Inputs</div>
        <ValueView value={call.inputs} projectId={projectId} />
      </div>
      <div>
        <div className="small muted">Output</div>
        <ValueView value={call.output} projectId={projectId} />
      </div>
    </div>
  )
}

function SpanLink({ projectId, id }: { projectId: string; id: string | null }) {
  if (!id) return null
  return (
    <Link to={spanUrl(projectId, id)} className="small" onClick={(e) => e.stopPropagation()}>
      open call ↗
    </Link>
  )
}

function textMessage(role: string, text: string, media?: Any[]) {
  return normaliseMessage({ role, content: media?.length ? [{ type: 'text', text }, ...media] : text })
}

function EventView({ m, projectId, agentName }: { m: ChatMessage; projectId: string; agentName: string | null }) {
  const [open, setOpen] = useState(false)
  const toggle = (
    <button className="toggle" onClick={() => setOpen((o) => !o)} aria-expanded={open} aria-label={open ? 'Collapse' : 'Expand'}>
      {open ? '▾' : '▸'}
    </button>
  )
  const when = m.started_at ? <span className="faint" title={fmtDate(m.started_at)}>{new Date(m.started_at).toLocaleTimeString()}</span> : null
  switch (m.type) {
    case 'user_message':
      return (
        <div className="agent-event">
          <ChatMessageView message={textMessage('user', m.user_message?.text ?? '', m.media)} projectId={projectId} label="user" />
        </div>
      )
    case 'assistant_message': {
      const a = m.assistant_message!
      const sub = m.agent_name && agentName && m.agent_name !== agentName
      return (
        <div className={`agent-event ${sub ? 'indent' : ''}`}>
          <div className="agent-event-head">
            {m.operation_name === 'chat' && toggle}
            <b style={{ color: 'var(--text)' }}>{m.agent_name ?? 'assistant'}</b>
            {a.model && <span className="mono">{a.model}</span>}
            {when}
            {a.duration_ms != null && <span className="mono">{fmtDuration(a.duration_ms)}</span>}
            {(a.input_tokens != null || a.output_tokens != null) && (
              <span className="mono">
                {fmtNumber(a.input_tokens ?? 0)} in · {fmtNumber(a.output_tokens ?? 0)} out
              </span>
            )}
            {a.total_cost_usd != null && <span className="mono">{fmtCost(a.total_cost_usd)}</span>}
            <Status code={m.status_code} />
            <SpanLink projectId={projectId} id={m.span_id} />
          </div>
          <div className="col" style={{ gap: 4 }}>
            {a.reasoning_content && (
              <details>
                <summary className="small muted">Reasoning{a.reasoning_tokens ? ` · ${fmtNumber(a.reasoning_tokens)} tokens` : ''}</summary>
                <div className="agent-reasoning">{a.reasoning_content}</div>
              </details>
            )}
            {a.text || m.media?.length ? (
              <ChatMessageView message={textMessage('assistant', a.text, m.media)} projectId={projectId} label="assistant" />
            ) : a.requested_tools?.length ? (
              <div className="small muted" style={{ paddingLeft: 18 }}>
                requested {a.requested_tools.map((t, i) => (
                  <span key={i}>
                    {i > 0 && ', '}
                    <span className="mono">{t}</span>
                  </span>
                ))}
              </div>
            ) : null}
            {m.status_code === 'ERROR' && m.status_message && <div className="error-box">{m.status_message.split('\n')[0]}</div>}
            {open && m.span_id && (
              <div className="agent-detail">
                <CallDetail projectId={projectId} callId={m.span_id} />
              </div>
            )}
          </div>
        </div>
      )
    }
    case 'tool_call': {
      const t = m.tool_call!
      const args = parseMaybeJSON(t.tool_arguments)
      const result = parseMaybeJSON(t.tool_result)
      const requestedOnly = m.operation_name === 'chat'
      return (
        <div className="agent-event indent">
          <div className="agent-event-head">
            {toggle}
            <span className="faint">tool</span>
            <span className="mono" style={{ color: 'var(--text)' }}>
              {t.tool_name ?? 'tool'}
            </span>
            {requestedOnly && <span className="faint">requested by the model, no execution recorded</span>}
            {when}
            {t.duration_ms != null && <span className="mono">{fmtDuration(t.duration_ms)}</span>}
            <Status code={m.status_code} />
            {!requestedOnly && <SpanLink projectId={projectId} id={m.span_id} />}
          </div>
          {open ? (
            <div className={`agent-bubble tool ${m.status_code === 'ERROR' ? 'error' : ''}`}>
              <div className="kv">
                <div className="k">args</div>
                <div className="v">{args == null ? <span className="faint">none</span> : <ValueView value={args} projectId={projectId} />}</div>
                <div className="k">result</div>
                <div className="v">
                  {m.status_message ? (
                    <span style={{ color: 'var(--error)' }}>{m.status_message.split('\n')[0]}</span>
                  ) : result == null ? (
                    <span className="faint">none</span>
                  ) : (
                    <ValueView value={result} projectId={projectId} />
                  )}
                </div>
              </div>
            </div>
          ) : (
            <>
              {(t.tool_arguments || m.status_message) && (
                <div className="small faint truncate" style={{ paddingLeft: 18, color: m.status_message ? 'var(--error)' : undefined }}>
                  {m.status_message ? m.status_message.split('\n')[0] : t.tool_arguments}
                </div>
              )}
              {/* A tool that returned media (a bucket URI, an image) shows it without expanding. */}
              {!m.status_message && isMedia(result) && (
                <div className="agent-tool-media">
                  <MediaView value={result} projectId={projectId} />
                </div>
              )}
            </>
          )}
        </div>
      )
    }
    case 'agent_start': {
      const s = m.agent_start!
      return (
        <div className="agent-event">
          <div className="agent-event-head">
            {(s.system_instructions || s.tool_definitions) && toggle}
            <span className="faint">sub-agent</span>
            <b style={{ color: 'var(--text)' }}>{m.agent_name ?? m.span_name}</b>
            {s.model && <span className="mono">{s.model}</span>}
            {when}
            <Status code={m.status_code} />
            <SpanLink projectId={projectId} id={m.span_id} />
          </div>
          {open && (
            <div className="agent-bubble subagent">
              {s.system_instructions && <ChatMessageView message={textMessage('system', s.system_instructions)} projectId={projectId} />}
              {s.tool_definitions && <ValueView value={parseMaybeJSON(s.tool_definitions)} projectId={projectId} />}
            </div>
          )}
        </div>
      )
    }
  }
  return null
}

/** One turn: user prompt, LLM and tool spans inline, final answer. */
export function TurnView({ turn, index, projectId, badges }: { turn: TraceChat; index: number; projectId: string; badges?: ReactNode }) {
  const tools = turn.messages.filter((m) => m.type === 'tool_call').length
  const replies = turn.messages.filter((m) => m.type === 'assistant_message').length
  return (
    <section className="agent-turn" id={`turn-${index + 1}`}>
      <div className="agent-turn-head">
        <b>Turn {index + 1}</b>
        <Link to={spanUrl(projectId, turn.root_span_id)} className="mono small">
          {turn.root_span_name ?? turn.root_span_id}
        </Link>
        <Status code={turn.status_code} />
        {badges}
        <span className="grow" />
        <span className="small muted">
          {replies} {replies === 1 ? 'response' : 'responses'} · {tools} tool {tools === 1 ? 'call' : 'calls'}
        </span>
        <span className="small muted" title={fmtDate(turn.started_at)}>
          {turn.started_at ? new Date(turn.started_at).toLocaleString() : ''}
        </span>
        <span className="small mono">{fmtDuration(turn.total_duration_ms)}</span>
        {turn.total_input_tokens + turn.total_output_tokens > 0 && (
          <span className="small mono">
            {fmtNumber(turn.total_input_tokens)} in · {fmtNumber(turn.total_output_tokens)} out
          </span>
        )}
        {turn.total_cost_usd != null && <span className="small mono">{fmtCost(turn.total_cost_usd)}</span>}
      </div>
      <div className="agent-turn-body">
        {turn.messages.length === 0 ? (
          <span className="faint small">No messages recorded for this turn.</span>
        ) : (
          turn.messages.map((m, i) => <EventView key={`${m.span_id}-${m.type}-${i}`} m={m} projectId={projectId} agentName={turn.agent_name} />)
        )}
        {!!turn.feedback?.length && (
          <div className="small" style={{ display: 'flex', flexWrap: 'wrap', gap: '4px 12px' }}>
            {turn.feedback
              .filter((f) => f.feedback_type === 'weft.reaction')
              .map((f) => (
                <span key={f.id}>{String(f.payload.emoji ?? '')}</span>
              ))}
            {turnSignals(turn.feedback.filter((f) => f.feedback_type !== 'weft.reaction')).ratings.map((r, i) => (
              <span key={`r${i}`} title={[r.reason, r.confidence != null ? `confidence ${Math.round(r.confidence * 100)}%` : ''].filter(Boolean).join(' · ') || undefined}>
                <span className="muted">{r.name}</span> <ScoreText value={r.value} />
              </span>
            ))}
            {turnSignals(turn.feedback.filter((f) => f.feedback_type !== 'weft.reaction')).tags.map((t) => (
              <span key={`t${t.name}`} title={[t.reason, t.confidence != null ? `confidence ${Math.round(t.confidence * 100)}%` : ''].filter(Boolean).join(' · ') || undefined}>
                tagged {t.name}
              </span>
            ))}
          </div>
        )}
      </div>
    </section>
  )
}
