// The session timeline: turns, messages, reasoning, model calls, tool calls,
// hand-offs, errors and AgentShield verdicts in one list, nested by agent,
// with each reasoning step tied to the action it led to. j/k move between
// steps; a deep link (?step=) scrolls to a step and expands the reasoning
// that led to it.
import { useEffect, useMemo, useRef, useState, type CSSProperties, type ReactNode } from 'react'
import { Link } from 'react-router-dom'
import type { Detection } from '../../api/v1'
import { agentColor } from '../../lib/agentColor'
import { fmtCost, fmtDate, fmtDuration, fmtNumber } from '../../lib/format'
import { moveFocus, oneLine, reasoningFor, STEP_LABEL, stepTitle, type BuiltTimeline, type Step, type StepType } from '../../lib/timeline'
import { agentUrl, callUrl, securityUrl } from '../../lib/urls'
import { isTyping } from '../ui/Shortcuts'
import { ActionBadge, AgentChip, CopyId, SeverityText, StatusPill } from '../ui/bits'
import { Icon, type IconName } from '../ui/Icons'

const TYPE_ICON: Record<StepType, IconName> = {
  turn: 'turn',
  message: 'message',
  llm: 'llm',
  reasoning: 'reasoning',
  tool_call: 'tool',
  handoff: 'handoff',
  error: 'error',
  detection: 'shield',
}

const FILTERABLE: { type: StepType; label: string }[] = [
  { type: 'message', label: 'Messages' },
  { type: 'reasoning', label: 'Reasoning' },
  { type: 'llm', label: 'Model calls' },
  { type: 'tool_call', label: 'Tool calls' },
  { type: 'handoff', label: 'Hand-offs' },
  { type: 'error', label: 'Errors' },
]

export function TimelineView({
  pid,
  tl,
  focus,
  onFocus,
  sessionStart,
}: {
  pid: string
  tl: BuiltTimeline
  focus: string | null
  onFocus: (key: string | null, scroll: boolean) => void
  sessionStart: number
}) {
  const [hidden, setHidden] = useState<Set<StepType>>(new Set())
  const [onlyProblems, setOnlyProblems] = useState(false)
  const [agentFilter, setAgentFilter] = useState<string | null>(null)
  const [expanded, setExpanded] = useState<Set<string>>(new Set())
  const [openReasoning, setOpenReasoning] = useState<Set<string>>(new Set())
  const [allReasoning, setAllReasoning] = useState(false)
  const [hoverPair, setHoverPair] = useState<string[]>([])
  const listRef = useRef<HTMLOListElement>(null)

  const visible = useMemo(
    () =>
      tl.steps.filter((s) => {
        if (s.type === 'turn') return !onlyProblems && !agentFilter
        if (hidden.has(s.type)) return false
        if (agentFilter && s.agent !== agentFilter) return false
        if (onlyProblems) return !!s.problem || (focus != null && reasoningFor(tl, focus).includes(s.key))
        return true
      }),
    [tl, hidden, onlyProblems, agentFilter, focus],
  )

  // A focused step expands itself and the reasoning that led to it.
  useEffect(() => {
    if (!focus) return
    const keys = reasoningFor(tl, focus)
    if (keys.length) setOpenReasoning((o) => new Set([...o, ...keys]))
    setExpanded((e) => new Set(e).add(focus))
  }, [focus, tl])

  // Scroll the focused step into view once it is rendered.
  const lastScrolled = useRef<string | null>(null)
  useEffect(() => {
    if (!focus || lastScrolled.current === focus) return
    const el = document.getElementById(domId(focus))
    if (!el) return
    lastScrolled.current = focus
    el.scrollIntoView({ block: 'center', behavior: 'auto' })
    el.focus({ preventScroll: true })
  }, [focus, visible])

  // j / k / Enter
  useEffect(() => {
    const h = (e: KeyboardEvent) => {
      if (e.metaKey || e.ctrlKey || e.altKey || isTyping(e) || document.querySelector('.modal')) return
      if (e.key === 'j' || e.key === 'k') {
        e.preventDefault()
        lastScrolled.current = null
        onFocus(moveFocus(visible, focus, e.key === 'j' ? 1 : -1), true)
      } else if (e.key === 'Enter' && focus && (e.target as HTMLElement)?.closest?.('.tl-step')) {
        e.preventDefault()
        toggle(focus)
      }
    }
    window.addEventListener('keydown', h)
    return () => window.removeEventListener('keydown', h)
  })

  const toggle = (key: string) =>
    setExpanded((e) => {
      const n = new Set(e)
      if (n.has(key)) n.delete(key)
      else n.add(key)
      return n
    })

  const toggleType = (t: StepType) =>
    setHidden((h) => {
      const n = new Set(h)
      if (n.has(t)) n.delete(t)
      else n.add(t)
      return n
    })

  return (
    <div className="timeline-view">
      <div className="tl-toolbar" role="group" aria-label="Timeline display">
        <div className="tl-types">
          {FILTERABLE.map((f) => (
            <button key={f.type} type="button" className={`tchip ${hidden.has(f.type) ? '' : 'on'}`} aria-pressed={!hidden.has(f.type)} onClick={() => toggleType(f.type)}>
              <Icon name={TYPE_ICON[f.type]} size={13} /> {f.label}
            </button>
          ))}
        </div>
        <div className="tl-types">
          {tl.agents.length > 1 &&
            tl.agents.map((a) => (
              <button
                key={a}
                type="button"
                className={`tchip agent ${agentFilter === a ? 'on' : ''}`}
                style={{ '--ac': agentColor(a) } as CSSProperties}
                aria-pressed={agentFilter === a}
                onClick={() => setAgentFilter((x) => (x === a ? null : a))}
                title={agentFilter === a ? 'Show all agents' : `Only ${a}`}
              >
                <span className="agent-dot" aria-hidden /> {a}
              </button>
            ))}
          <label className="checkbox small">
            <input type="checkbox" checked={onlyProblems} onChange={(e) => setOnlyProblems(e.target.checked)} /> Only problems
          </label>
          <label className="checkbox small">
            <input type="checkbox" checked={allReasoning} onChange={(e) => setAllReasoning(e.target.checked)} /> Expand all reasoning
          </label>
        </div>
        <span className="faint small kbd-hint">
          <kbd className="kbd">j</kbd> <kbd className="kbd">k</kbd> to move, <kbd className="kbd">Enter</kbd> to open
        </span>
      </div>
      {visible.length === 0 && <p className="muted">Nothing to show with these settings.</p>}
      <ol className="tl-list" ref={listRef} aria-label="Session timeline">
        {visible.map((s, i) => (
          <StepRow
            key={s.key}
            pid={pid}
            s={s}
            tl={tl}
            prev={visible[i - 1]}
            focused={focus === s.key}
            expanded={expanded.has(s.key)}
            reasoningOpen={allReasoning || openReasoning.has(s.key)}
            highlighted={hoverPair.includes(s.key)}
            sessionStart={sessionStart}
            onToggle={() => {
              onFocus(s.key, false)
              if (s.type === 'reasoning')
                setOpenReasoning((o) => {
                  const n = new Set(o)
                  if (n.has(s.key)) n.delete(s.key)
                  else n.add(s.key)
                  return n
                })
              else toggle(s.key)
            }}
            onJump={(key) => {
              lastScrolled.current = null
              setHidden(new Set())
              setAgentFilter(null)
              onFocus(key, true)
            }}
            onHoverPair={setHoverPair}
          />
        ))}
      </ol>
    </div>
  )
}

export const domId = (key: string) => `step-${key.replace(/[^a-zA-Z0-9_-]/g, '_')}`

function StepRow({
  pid,
  s,
  tl,
  prev,
  focused,
  expanded,
  reasoningOpen,
  highlighted,
  sessionStart,
  onToggle,
  onJump,
  onHoverPair,
}: {
  pid: string
  s: Step
  tl: BuiltTimeline
  prev?: Step
  focused: boolean
  expanded: boolean
  reasoningOpen: boolean
  highlighted: boolean
  sessionStart: number
  onToggle: () => void
  onJump: (key: string) => void
  onHoverPair: (keys: string[]) => void
}) {
  const p = s.item.payload ?? {}
  const offset = new Date(s.item.started_at).getTime() - sessionStart
  const showGap = s.gapMs >= 1000 && prev && s.type !== 'turn'
  const style = { '--ac': agentColor(s.agent), '--depth': s.depth } as CSSProperties
  const pair = s.ledTo ? [s.key, s.ledTo] : s.ledFrom.length ? [s.key, ...s.ledFrom] : []
  const showAgent = !prev || prev.agent !== s.agent || s.type === 'turn'

  if (s.type === 'turn') {
    return (
      <li className="tl-turn" id={domId(s.key)} tabIndex={-1} aria-current={focused ? 'step' : undefined}>
        <span className="tl-turn-label">Turn {s.turn}</span>
        <span className="grow tl-turn-text">{p.input_preview ? oneLine(String(p.input_preview), 160) : ''}</span>
        <span className="faint small">{fmtDuration(p.duration_ms)}</span>
        {String(p.status).toUpperCase() === 'ERROR' && <StatusPill status="error" />}
      </li>
    )
  }

  const cls = [
    'tl-step',
    `type-${s.type}`,
    s.problem ? `problem-${s.problem}` : '',
    focused ? 'focused' : '',
    highlighted ? 'paired' : '',
    s.ledTo ? 'leads' : '',
    s.ledFrom.length ? 'led' : '',
  ].join(' ')

  return (
    <>
      {showGap && (
        <li className="tl-gap" aria-hidden style={style}>
          <span>{fmtDuration(s.gapMs)} later</span>
        </li>
      )}
      <li
        className={cls}
        id={domId(s.key)}
        tabIndex={-1}
        style={style}
        aria-current={focused ? 'step' : undefined}
        onMouseEnter={() => pair.length && onHoverPair(pair)}
        onMouseLeave={() => pair.length && onHoverPair([])}
      >
        <div className="tl-rail" aria-hidden />
        <div className="tl-card">
          <button type="button" className="tl-head" onClick={onToggle} aria-expanded={s.type === 'reasoning' ? reasoningOpen : expanded}>
            <span className={`tl-icon t-${s.type}`}>
              <Icon name={TYPE_ICON[s.type]} size={14} />
            </span>
            <span className="tl-kind">{STEP_LABEL[s.type]}</span>
            <span className="tl-title">{s.type === 'reasoning' ? (reasoningOpen ? (p.kind === 'summary' ? 'Reasoning summary' : 'What the model was thinking') : reasoningPreview(p)) : stepTitle(s)}</span>
            <span className="tl-right">
              {s.detections
                .filter((d) => d.source === 'engine' && d.action !== 'allow')
                .map((d) => (
                  <ActionBadge key={d.id} action={d.action} overridden={d.overridden} />
                ))}
              {s.problem === 'error' && <StatusPill status="error" />}
              {s.durationMs != null && s.type !== 'message' && s.type !== 'reasoning' && <span className="faint small step-dur">{fmtDuration(s.durationMs)}</span>}
              <span className="faint small tl-at" title={fmtDate(s.item.started_at)}>
                +{fmtDuration(Math.max(0, offset))}
              </span>
            </span>
          </button>
          {showAgent && s.agent && (
            <div className="tl-agent">
              <AgentChip name={s.agent} to={agentUrl(pid, s.agent)} size="sm" />
            </div>
          )}
          {s.type === 'reasoning' && reasoningOpen && <ReasoningBody s={s} tl={tl} onJump={onJump} />}
          {s.type === 'reasoning' && !reasoningOpen && s.ledTo && (
            <button type="button" className="led-to" onClick={() => onJump(s.ledTo!)}>
              <Icon name="chevron" size={12} /> led to <strong>{stepTitle(tl.byKey.get(s.ledTo)!)}</strong>
            </button>
          )}
          {s.ledFrom.length > 0 && s.type !== 'reasoning' && (
            <button type="button" className="because" onClick={() => onJump(s.ledFrom[0])}>
              <Icon name="reasoning" size={12} /> because: <span className="because-text">{oneLine(String(tl.byKey.get(s.ledFrom[0])?.item.payload?.text ?? 'reasoning'), 140)}</span>
            </button>
          )}
          {s.errorText && !expanded && (
            <div className="step-error">
              <Icon name="error" size={12} /> {oneLine(s.errorText, 160)}
            </div>
          )}
          {expanded && s.type !== 'reasoning' && <StepBody pid={pid} s={s} />}
        </div>
      </li>
    </>
  )
}

// eslint-disable-next-line @typescript-eslint/no-explicit-any
function reasoningPreview(p: Record<string, any>): string {
  if (p.kind === 'redacted') return 'The provider hid this reasoning.'
  return oneLine(String(p.text ?? ''), 140)
}

function ReasoningBody({ s, tl, onJump }: { s: Step; tl: BuiltTimeline; onJump: (k: string) => void }) {
  const p = s.item.payload ?? {}
  const target = s.ledTo ? tl.byKey.get(s.ledTo) : undefined
  return (
    <div className="tl-body reasoning-body">
      {p.kind === 'redacted' ? <p className="muted">The model's reasoning was redacted by the provider; only the fact that it reasoned is recorded.</p> : <LongText text={String(p.text ?? '')} />}
      <div className="row wrap small muted" style={{ gap: 12 }}>
        <span>{p.kind === 'summary' ? 'Reasoning summary' : 'Thinking'}</span>
        {p.tokens != null && <span>{fmtNumber(p.tokens)} reasoning tokens</span>}
        {target && (
          <button type="button" className="led-to inline" onClick={() => onJump(target.key)}>
            <Icon name="chevron" size={12} /> led to <strong>{stepTitle(target)}</strong>
          </button>
        )}
        {!target && <span>did not lead to a tool call or hand-off</span>}
      </div>
    </div>
  )
}

export function LongText({ text, limit = 600 }: { text: string; limit?: number }) {
  const [all, setAll] = useState(false)
  const long = text.length > limit
  return (
    <div className="long-text">
      <p className="pre">{all || !long ? text : text.slice(0, limit) + '…'}</p>
      {long && (
        <button type="button" className="linkish small" onClick={() => setAll((a) => !a)}>
          {all ? 'Show less' : `Show all (${fmtNumber(text.length)} characters)`}
        </button>
      )}
    </div>
  )
}

function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="kv-row">
      <dt>{label}</dt>
      <dd>{children}</dd>
    </div>
  )
}

function StepBody({ pid, s }: { pid: string; s: Step }) {
  const p = s.item.payload ?? {}
  return (
    <div className="tl-body">
      {s.detections.length > 0 && <DetectionDetails pid={pid} dets={s.detections} />}
      <dl className="kv-list">
        {s.errorText && (
          <Field label="Error">
            <span className="text-error">
              <LongText text={s.errorText} />
            </span>
          </Field>
        )}
        {s.type === 'tool_call' && (
          <>
            <Field label="Tool">{p.tool_name}</Field>
            <Field label="Arguments">
              <LongText text={String(p.arguments ?? '')} />
            </Field>
            <Field label="Result">{p.result ? <LongText text={String(p.result)} /> : <span className="faint">{String(p.status).toUpperCase() === 'ERROR' ? 'none (failed)' : 'none recorded'}</span>}</Field>
            <Field label="Status">
              <StatusPill status={String(p.status).toUpperCase() === 'ERROR' ? 'error' : 'ok'} />
            </Field>
            {p.tool_call_id && (
              <Field label="Tool call id">
                <CopyId id={String(p.tool_call_id)} label="Tool call id" short={24} />
              </Field>
            )}
          </>
        )}
        {s.type === 'llm' && (
          <>
            <Field label="Model">
              {p.model} {p.provider && <span className="faint">({p.provider})</span>}
            </Field>
            <Field label="Tokens">
              {fmtNumber(p.input_tokens)} in · {fmtNumber(p.output_tokens)} out
              {p.reasoning_tokens ? ` · ${fmtNumber(p.reasoning_tokens)} reasoning` : ''}
            </Field>
            <Field label="Cost">{fmtCost(p.cost_usd)}</Field>
            {p.output_preview && (
              <Field label="Reply">
                <LongText text={String(p.output_preview)} />
              </Field>
            )}
            {Array.isArray(p.tool_calls) && p.tool_calls.length > 0 && (
              <Field label="Asked for">
                <ul className="plain">
                  {p.tool_calls.map((c: { id: string; name: string; arguments: string }) => (
                    <li key={c.id}>
                      <span className="mono">{c.name}</span> <span className="faint">{oneLine(String(c.arguments ?? ''), 120)}</span>
                    </li>
                  ))}
                </ul>
              </Field>
            )}
          </>
        )}
        {s.type === 'handoff' && (
          <>
            <Field label="From → to">
              <AgentChip name={p.from_agent} size="sm" /> → <AgentChip name={p.to_agent} size="sm" />
            </Field>
            <Field label="Kind">{p.kind === 'delegate' ? 'Delegated (the caller waits for the result)' : p.kind === 'return' ? 'Returned control' : 'Handed off'}</Field>
            {p.input_preview && (
              <Field label="Task given">
                <LongText text={String(p.input_preview)} />
              </Field>
            )}
            {p.output_preview && (
              <Field label="Result">
                <LongText text={String(p.output_preview)} />
              </Field>
            )}
            <Field label="Status">
              <StatusPill status={p.status === 'error' ? 'error' : p.status === 'running' ? 'running' : 'ok'} />
            </Field>
          </>
        )}
        {s.type === 'message' && (
          <Field label={p.role === 'user' ? 'User' : 'Assistant'}>
            <LongText text={String(p.text ?? '')} />
          </Field>
        )}
        {s.type === 'error' && (
          <>
            <Field label="Message">
              <LongText text={String(p.message ?? '')} />
            </Field>
            {p.error_type && <Field label="Type">{p.error_type}</Field>}
            {p.name && <Field label="In">{p.name}</Field>}
          </>
        )}
        {s.item.span_id && s.type !== 'detection' && (
          <Field label="Span">
            <CopyId id={s.item.span_id} label="Span id" />{' '}
            <Link className="small" to={callUrl(pid, s.item.span_id)}>
              Open raw span
            </Link>
          </Field>
        )}
      </dl>
      <details className="raw-details">
        <summary>Details (raw JSON)</summary>
        <pre className="code-block">{JSON.stringify({ ...s.item, detections: s.detections.length ? s.detections : undefined }, null, 2)}</pre>
      </details>
    </div>
  )
}

export function DetectionDetails({ pid, dets }: { pid: string; dets: Detection[] }) {
  return (
    <div className="det-box">
      {dets.map((d) => (
        <div key={d.id} className={`det det-${d.action}`}>
          <div className="row wrap" style={{ gap: 8 }}>
            <ActionBadge action={d.action} overridden={d.overridden} />
            <span className="small muted">
              {d.source === 'replay' ? `Replay (${d.ruleset ?? 'rules'})` : `AgentShield, ${d.mode} mode`} · severity <SeverityText level={d.severity} />
            </span>
          </div>
          <ul className="rules">
            {d.rules.map((r) => (
              <li key={r.id}>
                <strong>{r.title || r.id}</strong> <SeverityText level={r.level} />
                <span className="faint small"> {r.tags?.slice(0, 3).join(', ')}</span>
              </li>
            ))}
          </ul>
          <Link className="small" to={securityUrl(pid, new URLSearchParams({ q: d.rules[0]?.title ?? d.tool_name ?? '' }).toString())}>
            See every detection of this rule
          </Link>
        </div>
      ))}
    </div>
  )
}
