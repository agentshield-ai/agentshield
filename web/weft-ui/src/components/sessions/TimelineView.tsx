// The session Story (DESIGN.md §7 "Session page", §7.2): the session told
// as a conversation, with the inspector beside it.
//
// Each entry shows who acted (a monogram and a name), what it thought
// (reasoning in quiet italics, directly above the action it led to) and
// what it did: the user's request, a tool call with its main argument, a
// model's reply, a hand-off told as the other agent's indented reply (nested
// to any depth), the final answer. An AgentShield block or approval request
// sits inside the step it concerns, with one plain sentence saying why.
//
// Selecting a step opens it in the inspector: its tool, decision, rule,
// severity, model, timing, tokens and ids, and links to every detection of
// the rule, a replay with the current rules, and the raw span. j/k move
// between steps. On a narrow screen the inspector opens under the selected
// step instead of beside the story.
import { useEffect, useLayoutEffect, useRef, useState, type KeyboardEvent as ReactKeyboardEvent, type ReactNode } from 'react'
import { Link } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { v1, type Detection, type ReplayJob, type SessionSummary } from '../../api/v1'
import { fmtCost, fmtDate, fmtNumber } from '../../lib/format'
import { isCommandTool, joinClauses, offsetText, shortDuration } from '../../lib/summaries'
import { ruleWords } from '../../lib/ruleWords'
import { entryFor, type Delegation, type Story, type StepEntry, type StoryEntry } from '../../lib/story'
import { oneLine, type BuiltTimeline, type Step } from '../../lib/timeline'
import { agentUrl, securityUrl, spanUrl } from '../../lib/urls'
import { ErrorBox } from '../common'
import { CopyText, Inspector, InspectorDivider, InspectorLinks, KeyValueList, type KeyValue } from '../ui/Inspector'
import { Monogram } from '../ui/Monogram'
import { StatusGlyph } from '../ui/StatusGlyph'
import { notify } from '../ui/Toasts'

export const domId = (key: string) => `step-${key.replace(/[^a-zA-Z0-9_-]/g, '_')}`

const t = (s: string | null | undefined) => (s ? new Date(s).getTime() : NaN)
const NARROW = 780

// ---------------------------------------------------------------- words for a step

/** The strongest engine verdict on a step (block, then approval, then log). */
export function verdictOf(s: Step): Detection | undefined {
  const xs = s.detections.filter((d) => d.source !== 'replay' && d.action !== 'allow')
  return xs.find((d) => d.action === 'block') ?? xs.find((d) => d.action === 'require_approval') ?? xs.find((d) => d.action === 'log')
}

const FILE_READ = /^(read|read_file|readfile|view|cat|open_file)$/i
const FILE_WRITE = /^(write|write_file|create_file)$/i
const FILE_EDIT = /^(edit|multiedit|str_replace|apply_patch|edit_file)$/i
const SEARCH = /^(glob|grep|search_files|find|ls|list_files)$/i
const WEB_SEARCH = /^(web_search|websearch|search_web|google_search|bing_search)$/i
const WEB_FETCH = /^(webfetch|web_fetch|fetch|http_get|browse|open_url)$/i

/** What a tool call is, in a few words: [noun, verb done, verb tried]. */
function toolWords(tool: string): { noun: string; did: string; tried: string } {
  if (isCommandTool(tool)) return { noun: 'command', did: 'Ran a command', tried: 'run a command' }
  if (FILE_READ.test(tool)) return { noun: 'file read', did: 'Read a file', tried: 'read a file' }
  if (FILE_WRITE.test(tool)) return { noun: 'file write', did: 'Wrote a file', tried: 'write a file' }
  if (FILE_EDIT.test(tool)) return { noun: 'file edit', did: 'Edited a file', tried: 'edit a file' }
  if (SEARCH.test(tool)) return { noun: 'file search', did: 'Searched the files', tried: 'search the files' }
  if (WEB_SEARCH.test(tool)) return { noun: 'web search', did: 'Searched the web', tried: 'search the web' }
  if (WEB_FETCH.test(tool)) return { noun: 'page fetch', did: 'Fetched a page', tried: 'fetch a page' }
  return { noun: `${tool} call`, did: `Called ${tool}`, tried: `call ${tool}` }
}

const failed = (s: Step) => s.problem === 'error' || String(s.item.payload?.status ?? '').toUpperCase() === 'ERROR'

/** The card title of a tool call: "Tried to run a command", "Ran a command", "Called web_search, which failed". */
function actTitle(s: Step): string {
  const tool = String(s.item.payload?.tool_name ?? s.detections[0]?.tool_name ?? 'tool')
  const w = toolWords(tool)
  const v = verdictOf(s)
  if (v?.action === 'block') return `Tried to ${w.tried}`
  if (v?.action === 'require_approval') return `Asked to ${w.tried}`
  if (failed(s)) return `${w.did}, which failed`
  return w.did
}

/** The inspector title of an entry: "Blocked command", "Failed web search", "Hand-off to reviewer". */
function inspectorTitle(e: StepEntry): string {
  switch (e.kind) {
    case 'user':
      return "User's request"
    case 'answer':
      return 'Final answer'
    case 'reply':
      return 'Model reply'
    case 'thought':
      return 'Reasoning'
    case 'error':
      return 'Error'
    case 'act': {
      if (e.delegation) return `Hand-off to ${e.delegation.to}`
      if (e.step.type === 'llm') return 'Model call'
      const tool = String(e.step.item.payload?.tool_name ?? e.step.detections[0]?.tool_name ?? 'tool')
      const noun = toolWords(tool).noun
      const v = verdictOf(e.step)
      if (v?.action === 'block') return `Blocked ${noun}`
      if (v?.action === 'require_approval') return `${noun[0].toUpperCase()}${noun.slice(1)} that needed approval`
      if (failed(e.step)) return `Failed ${noun}`
      return noun[0].toUpperCase() + noun.slice(1)
    }
  }
}

/** The main argument of a tool call, as shown in its code block. */
function mainArgument(s: Step): string {
  const a = s.item.payload?.arguments
  if (a == null || a === '') return ''
  return typeof a === 'string' ? a : JSON.stringify(a)
}

const clip = (s: string, n: number) => (s.length > n ? s.slice(0, n - 1) + '…' : s)
const sev = (l: string | undefined) => (l && l !== 'none' ? l[0].toUpperCase() + l.slice(1) : 'None')

// ---------------------------------------------------------------- the view

export interface ReplayState {
  enabled: boolean | undefined
  start: () => void
  starting: boolean
  job: ReplayJob | undefined
}

/** Replays this session's tool calls with the current rules (shadow mode) and polls the job. */
export function useReplay(pid: string, sessionId: string): ReplayState {
  const qc = useQueryClient()
  const status = useQuery({ queryKey: ['v1', pid, 'shield-status'], queryFn: () => v1.shieldStatus(pid), staleTime: 60_000 })
  const [jobId, setJobId] = useState<string | null>(null)
  const start = useMutation({
    mutationFn: () => v1.replayStart({ project_id: pid, session_ids: [sessionId], label: `session-${new Date().toISOString().slice(0, 16).replace(/[-:T]/g, '')}` }),
    onSuccess: (r) => setJobId(r.job_id),
    onError: (e) => notify(`Replay failed to start: ${e instanceof Error ? e.message : String(e)}`, 'error'),
  })
  const job = useQuery({
    queryKey: ['v1', pid, 'replay', jobId],
    queryFn: () => v1.replayJob(pid, jobId!),
    enabled: !!jobId,
    refetchInterval: (q) => (q.state.data && (q.state.data.status === 'done' || q.state.data.status === 'failed') ? false : 800),
  })
  useEffect(() => {
    if (job.data?.status === 'done') void qc.invalidateQueries({ queryKey: ['v1', pid, 'detections'] })
  }, [job.data?.status, qc, pid])
  return { enabled: status.data?.replay_enabled, start: () => start.mutate(), starting: start.isPending, job: job.data }
}

export function StoryView({
  pid,
  summary,
  tl,
  story,
  selected,
  onSelect,
  scrollKey,
  replay,
}: {
  pid: string
  summary: SessionSummary
  tl: BuiltTimeline
  story: Story
  /** The selected entry's key, or null. */
  selected: string | null
  onSelect: (key: string) => void
  /** Scroll this entry into view (on arrival and on j/k), once per change. */
  scrollKey: string | null
  replay: ReplayState
}) {
  const sessionStart = t(summary.started_at)
  const layoutRef = useRef<HTMLDivElement>(null)
  const [narrow, setNarrow] = useState(false)
  useLayoutEffect(() => {
    const el = layoutRef.current
    if (!el || typeof ResizeObserver === 'undefined') return
    const check = () => setNarrow(el.clientWidth < NARROW)
    const ro = new ResizeObserver(check)
    ro.observe(el)
    check()
    return () => ro.disconnect()
  }, [])

  // Bring the selected step into view on arrival and on j/k, then focus it.
  const lastScrolled = useRef<string | null>(null)
  useEffect(() => {
    if (!scrollKey || lastScrolled.current === scrollKey) return
    const el = document.getElementById(domId(scrollKey))
    if (!el) return
    lastScrolled.current = scrollKey
    // Only when it is not already in view (on arrival the top of the page stays put if it can).
    const r = el.getBoundingClientRect()
    if (r.top < 0 || r.bottom > window.innerHeight) el.scrollIntoView({ block: 'center', behavior: 'auto' })
    el.focus({ preventScroll: true })
  }, [scrollKey, narrow])

  const sel = selected ? story.byKey.get(selected) : undefined
  const inspector = sel ? (
    <StepInspector pid={pid} summary={summary} entry={sel} story={story} tl={tl} replay={replay} sticky={!narrow} />
  ) : (
    <SessionInspector pid={pid} summary={summary} replay={replay} sticky={!narrow} />
  )
  const ctx: EntryCtx = { pid, sessionStart, selected, onSelect, inline: narrow ? inspector : null }

  if (!story.order.length)
    return (
      <div className="story-layout" ref={layoutRef}>
        <p className="quiet-note">Nothing was recorded for this session yet: no messages, tool calls or verdicts.</p>
      </div>
    )
  return (
    <div className={`story-layout ${narrow ? 'narrow' : ''}`} ref={layoutRef}>
      <ol className="story" aria-label="What happened">
        {story.entries.map((e) => (
          <TopEntry key={e.key} e={e} ctx={ctx} />
        ))}
      </ol>
      {(!narrow || !sel) && <div className="story-side">{inspector}</div>}
    </div>
  )
}

interface EntryCtx {
  pid: string
  sessionStart: number
  selected: string | null
  onSelect: (key: string) => void
  /** The inspector, rendered under the selected step on a narrow screen. */
  inline: ReactNode
}

function startOf(e: StepEntry): number {
  const first = e.thoughts[0] ?? e.step
  return t(first.item.started_at)
}

function TopEntry({ e, ctx }: { e: StoryEntry; ctx: EntryCtx }) {
  if (e.kind === 'turn')
    return (
      <li className="story-turn">
        <span>Turn {e.turn}</span>
      </li>
    )
  const at = offsetText(startOf(e) - ctx.sessionStart)
  const user = e.kind === 'user'
  const head = user ? (
    <>
      <span className="se-who">User</span> · {e.step.turn > 1 ? 'asked again' : 'asked'}
    </>
  ) : (
    <>
      <span className="se-who">{e.agent ?? 'Unknown agent'}</span> · {e.kind === 'answer' ? `answered after ${at}` : `after ${at}`}
    </>
  )
  return (
    <li className={`story-entry kind-${e.kind}`}>
      <Monogram name={user ? 'User' : e.agent} variant={user ? 'user' : 'agent'} />
      <div className="se-body">
        <div className="se-head">{head}</div>
        <EntryBody e={e} ctx={ctx} />
      </div>
    </li>
  )
}

/** Reasoning, then the step itself, then (for a hand-off) the other agent's work. */
function EntryBody({ e, ctx }: { e: StepEntry; ctx: EntryCtx }) {
  const selected = ctx.selected === e.key
  return (
    <>
      {e.kind !== 'thought' && e.thoughts.map((x) => <Thought key={x.key} s={x} onClick={() => ctx.onSelect(e.key)} />)}
      <Block e={e} ctx={ctx} />
      {selected && ctx.inline && <div className="story-inline-inspector">{ctx.inline}</div>}
      {e.kind === 'act' && e.delegation && <Nested d={e.delegation} ctx={ctx} />}
    </>
  )
}

function Thought({ s, onClick }: { s: Step; onClick?: () => void }) {
  const p = s.item.payload ?? {}
  const text = p.kind === 'redacted' ? 'The provider hid this reasoning.' : String(p.text ?? '')
  const [all, setAll] = useState(false)
  const long = text.length > 420
  return (
    <div className="se-thought-wrap">
      <p className={`se-thought ${long && !all ? 'clamped' : ''}`} onClick={onClick}>
        {text}
      </p>
      {long && (
        <button type="button" className="linkish se-more" onClick={() => setAll((a) => !a)} aria-expanded={all}>
          {all ? 'Show less' : 'Show all of the reasoning'}
        </button>
      )}
    </div>
  )
}

/** The selectable part of an entry. */
function Block({ e, ctx }: { e: StepEntry; ctx: EntryCtx }) {
  const selected = ctx.selected === e.key
  const props = {
    id: domId(e.key),
    role: 'button',
    tabIndex: 0,
    'aria-current': selected ? ('step' as const) : undefined,
    onClick: () => ctx.onSelect(e.key),
    onKeyDown: (ev: ReactKeyboardEvent) => {
      if (ev.key === 'Enter' || ev.key === ' ') {
        ev.preventDefault()
        ctx.onSelect(e.key)
      }
    },
  }
  switch (e.kind) {
    case 'user':
      return (
        <div {...props} className="story-step bubble user">
          {clip(e.text, 1200)}
        </div>
      )
    case 'answer':
      return (
        <div {...props} className="story-step bubble answer">
          {clip(e.text, 1200)}
        </div>
      )
    case 'reply':
      return (
        <div {...props} className="story-step reply">
          {clip(e.text, 1200)}
        </div>
      )
    case 'thought':
      return (
        <div {...props} className="story-step thoughts">
          {e.thoughts.map((x) => (
            <p key={x.key} className="se-thought">
              {x.item.payload?.kind === 'redacted' ? 'The provider hid this reasoning.' : clip(String(x.item.payload?.text ?? ''), 1200)}
            </p>
          ))}
        </div>
      )
    case 'error':
      return (
        <div {...props} className="story-step card">
          <div className="sc-head">
            <span className="sc-title">
              <StatusGlyph kind="failed" size={16} decorative /> Something failed
            </span>
          </div>
          <p className="sc-error">{clip(String(e.step.item.payload?.message ?? 'Error'), 400)}</p>
        </div>
      )
    case 'act': {
      if (e.delegation) {
        const d = e.delegation
        return (
          <div {...props} className="story-step ask">
            <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" focusable="false">
              <path d="M4 12h14 M13 6l6 6-6 6" />
            </svg>
            <span>
              Asked <b>{d.to}</b>
              {d.ask ? `: “${clip(oneLine(d.ask, 400), 300)}”` : ''}
            </span>
          </div>
        )
      }
      return <ActCard e={e} props={props} />
    }
  }
}

function ActCard({ e, props }: { e: Extract<StepEntry, { kind: 'act' }>; props: Record<string, unknown> }) {
  const s = e.step
  const p = s.item.payload ?? {}
  const v = verdictOf(s)
  const ok = !v && !failed(s) && s.type !== 'llm'
  const arg = s.type === 'llm' ? ((p.tool_calls ?? []) as { name: string }[]).map((c) => c.name).join(', ') : mainArgument(s)
  const title = s.type === 'llm' ? 'Asked to call a tool' : actTitle(s)
  const words = v ? ruleWords(v.rules[0], v.tool_name) : null
  return (
    <div {...props} className={`story-step card ${v?.action === 'block' ? 'blocked' : ''}`}>
      <div className="sc-head">
        <span className="sc-title">
          {ok && <StatusGlyph kind="check" size={16} />}
          {title}
        </span>
        {s.durationMs != null && <span className="sc-dur">{shortDuration(s.durationMs)}</span>}
      </div>
      {arg && <code className="sc-arg">{clip(arg, 600)}</code>}
      {v && (v.action === 'block' || v.action === 'require_approval') && (
        <div className={`shield-box ${v.action === 'block' ? 'block' : 'approval'}`}>
          <StatusGlyph kind={v.action === 'block' ? 'blocked' : 'approval'} size={20} decorative />
          <div className="sb-text">
            <span className="sb-title">{v.action === 'block' ? 'Blocked by AgentShield' : 'AgentShield asked for approval'}</span>
            <span className="sb-reason">
              {words!.reason}
              {v.overridden ? (v.action === 'block' ? ' The user overrode the block.' : ' The user approved it.') : ''}
            </span>
          </div>
        </div>
      )}
      {v?.action === 'log' && <p className="sb-log">AgentShield noted this: {v.rules[0]?.title ?? 'a rule matched'}.</p>}
      {failed(s) && v?.action !== 'block' && s.errorText && <p className="sc-error">{clip(oneLine(s.errorText, 400), 300)}</p>}
    </div>
  )
}

/** A hand-off: the other agent's work, indented, then what it replied. */
function Nested({ d, ctx }: { d: Delegation; ctx: EntryCtx }) {
  const reply = d.reply ? `Replied: “${clip(oneLine(d.reply, 600), 400)}”` : d.failed ? 'Did not reply: the hand-off failed.' : 'Returned without a reply.'
  return (
    <div className="se-nested">
      <Monogram name={d.to} variant="nested" />
      <div className="se-nested-body">
        <div className="se-head">
          <span className="se-who">{d.to}</span>
          {d.durationMs != null ? ` · worked for ${shortDuration(d.durationMs)}` : ''}
        </div>
        {d.children.map((c) =>
          c.kind === 'turn' ? null : (
            <div key={c.key} className="se-child">
              {c.agent && c.agent !== d.to && <div className="se-head">{c.agent}</div>}
              <EntryBody e={c} ctx={ctx} />
            </div>
          ),
        )}
        <div className={`se-replied ${!d.reply && d.failed ? 'failed' : ''}`}>
          {!d.reply && d.failed && <StatusGlyph kind="failed" size={16} decorative />}
          {reply}
        </div>
      </div>
    </div>
  )
}

// ---------------------------------------------------------------- inspectors

function stepWhen(s: Step, sessionStart: number): string {
  const at = offsetText(t(s.item.started_at) - sessionStart)
  return s.durationMs != null ? `${at} in, took ${shortDuration(s.durationMs)}` : `${at} in`
}

function tokens(llm: Step | undefined): string | null {
  const p = llm?.item.payload
  if (!p || (p.input_tokens == null && p.output_tokens == null)) return null
  return `${fmtNumber(p.input_tokens)} in · ${fmtNumber(p.output_tokens)} out${p.reasoning_tokens ? ` · ${fmtNumber(p.reasoning_tokens)} reasoning` : ''}`
}

function StepInspector({ pid, summary, entry: e, story, tl, replay, sticky }: { pid: string; summary: SessionSummary; entry: StepEntry; story: Story; tl: BuiltTimeline; replay: ReplayState; sticky: boolean }) {
  const s = e.step
  const p = s.item.payload ?? {}
  const sessionStart = t(summary.started_at)
  const n = story.order.indexOf(e)
  const turnPart = tl.turns > 1 ? ` · turn ${s.turn}` : ''
  const sub = `${e.kind === 'user' ? 'User' : (e.agent ?? 'Unknown agent')}${turnPart} · step ${n + 1} of ${story.order.length}`
  const items: (KeyValue | null)[] = []
  const v = verdictOf(s)
  const rule = v?.rules[0]
  const links: ReactNode[] = []

  if (e.kind === 'act') {
    const d = e.delegation
    if (s.type === 'tool_call' || s.type === 'detection') items.push({ label: 'Tool', value: String(p.tool_name ?? s.detections[0]?.tool_name ?? '—') })
    if (v) {
      const decision = v.action === 'block' ? 'Blocked' : v.action === 'require_approval' ? 'Needed approval' : 'Logged'
      items.push({ label: 'Decision', value: `${decision}${v.overridden ? ' (overridden by the user)' : ''}`, tone: v.action === 'block' ? 'error' : v.action === 'require_approval' ? 'warn' : undefined })
      items.push({ label: v.rules.length > 1 ? 'Rules' : 'Rule', value: v.rules.map((r) => r.title || r.id).join('; ') || '—' })
      items.push({ label: 'Severity', value: sev(v.severity) })
      items.push({ label: 'Mode', value: sev(v.mode) })
    } else if (failed(s)) items.push({ label: 'Outcome', value: 'Failed', tone: 'error' })
    if (d) {
      items.push({ label: 'Hand-off', value: `${d.handoff.item.payload?.from_agent ?? e.agent} → ${d.to}` })
      items.push({ label: 'Kind', value: d.handoff.item.payload?.kind === 'delegate' ? 'Delegated (waits for the result)' : 'Handed off' })
      if (d.ask) items.push({ label: 'Asked', value: clip(d.ask, 600) })
      items.push({ label: 'Replied', value: d.reply ? clip(d.reply, 600) : d.failed ? 'Nothing: the hand-off failed' : 'Nothing' , tone: !d.reply && d.failed ? 'error' : undefined })
    }
    const model = e.model ?? (s.type === 'llm' ? s : undefined)
    if (model?.item.payload?.model) items.push({ label: 'Model', value: String(model.item.payload.model) })
    items.push({ label: 'When', value: stepWhen(s, sessionStart) })
    const tk = tokens(model)
    if (tk) items.push({ label: 'Tokens', value: tk, help: 'Of the model call that asked for this step. Input includes cached tokens.' })
    // The story shows up to 600 characters of the main argument: the rest is here.
    if (s.type === 'tool_call' && mainArgument(s).length > 600) items.push({ label: 'Arguments', value: clip(mainArgument(s), 4000), mono: true })
    if (s.type === 'tool_call' && p.result) items.push({ label: 'Result', value: clip(String(p.result), 1200) })
    if (s.errorText && v?.action !== 'block') items.push({ label: 'Error', value: clip(s.errorText, 1200), tone: 'error' })
    if (p.tool_call_id) items.push({ label: 'Call id', value: <CopyText text={String(p.tool_call_id)} label="call id" />, mono: true })
    if (rule) links.push(<Link key="rule" to={securityUrl(pid, new URLSearchParams({ q: rule.title || rule.id, time: '30d' }).toString())}>Every detection of this rule</Link>)
    if (d) links.push(<Link key="agent" to={agentUrl(pid, d.to)}>About the {d.to}</Link>)
  } else if (e.kind === 'reply') {
    items.push({ label: 'Model', value: String(p.model ?? '—') })
    items.push({ label: 'When', value: stepWhen(s, sessionStart) })
    const tk = tokens(s)
    if (tk) items.push({ label: 'Tokens', value: tk })
    if (p.cost_usd != null) items.push({ label: 'Cost', value: fmtCost(p.cost_usd) })
  } else if (e.kind === 'thought') {
    const kinds = [...new Set(e.thoughts.map((x) => x.item.payload?.kind ?? 'thinking'))]
    items.push({ label: 'Kind', value: joinClauses(kinds.map((k) => (k === 'summary' ? 'reasoning summary' : k === 'redacted' ? 'hidden by the provider' : 'thinking'))) })
    const tok = e.thoughts.reduce((a, x) => a + (Number(x.item.payload?.tokens) || 0), 0)
    if (tok) items.push({ label: 'Tokens', value: fmtNumber(tok) })
    items.push({ label: 'Led to', value: 'No tool call or hand-off' })
    items.push({ label: 'When', value: stepWhen(s, sessionStart) })
  } else if (e.kind === 'error') {
    items.push({ label: 'Message', value: clip(String(p.message ?? ''), 1200), tone: 'error' })
    if (p.error_type) items.push({ label: 'Type', value: String(p.error_type) })
    if (p.name) items.push({ label: 'In', value: String(p.name) })
    items.push({ label: 'When', value: stepWhen(s, sessionStart) })
  } else {
    items.push({ label: 'When', value: `${offsetText(t(s.item.started_at) - sessionStart)} in` })
    items.push({ label: 'Length', value: `${fmtNumber(e.text.length)} characters` })
    if (e.text.length > 1200) items.push({ label: 'Text', value: e.text })
  }
  if (s.item.span_id && s.type !== 'detection') items.push({ label: 'Span id', value: <CopyText text={s.item.span_id} label="span id" />, mono: true })
  if (s.type === 'tool_call' || s.type === 'detection')
    links.push(
      <button key="replay" type="button" className="linkish" onClick={replay.start} disabled={replay.starting || replay.job?.status === 'running' || replay.job?.status === 'queued'}>
        {replay.starting ? 'Starting the replay…' : 'Replay with the current rules'}
      </button>,
    )
  if (s.item.span_id && s.type !== 'detection') links.push(<Link key="raw" to={spanUrl(pid, s.item.span_id)}>Show the raw span</Link>)

  const replays = s.detections.filter((d) => d.source === 'replay')
  return (
    <Inspector title={inspectorTitle(e)} subtitle={sub} label="Selected step" sticky={sticky} id="inspector">
      <KeyValueList items={items} />
      {replays.length > 0 && (
        <p className="quiet-note">
          Replayed with {replays[0].ruleset ?? 'the rules'}: {replays.map((d) => (d.action === 'block' ? 'would be blocked' : d.action === 'require_approval' ? 'would need approval' : d.action === 'log' ? 'would be logged' : 'would be allowed')).join(', ')}.
        </p>
      )}
      <InspectorDivider />
      <InspectorLinks>{links}</InspectorLinks>
      <ReplayNote replay={replay} pid={pid} />
      <details className="insp-raw">
        <summary>Raw JSON</summary>
        <pre className="code-block">{JSON.stringify({ ...s.item, detections: s.detections.length ? s.detections : undefined }, null, 2)}</pre>
      </details>
    </Inspector>
  )
}

/** The inspector when no step is selected: the session's ids and figures. */
function SessionInspector({ pid, summary: s, replay, sticky }: { pid: string; summary: SessionSummary; replay: ReplayState; sticky: boolean }) {
  const steps = s.llm_call_count + s.tool_call_count
  return (
    <Inspector title="This session" subtitle="Select a step to see its details." label="Session details" sticky={sticky} id="inspector">
      <KeyValueList
        items={[
          { label: 'Session id', value: <CopyText text={s.session_id} label="session id" />, mono: true },
          { label: 'Started', value: fmtDate(s.started_at) },
          s.duration_ms != null && !s.verdicts_only ? { label: 'Took', value: shortDuration(s.duration_ms) } : null,
          { label: 'Turns', value: fmtNumber(s.turn_count) },
          { label: 'Steps', value: `${fmtNumber(steps)}: ${s.llm_call_count} model, ${s.tool_call_count} tool`, help: 'Model calls plus tool calls.' },
          s.handoff_count ? { label: 'Hand-offs', value: fmtNumber(s.handoff_count) } : null,
          { label: 'Tokens', value: `${fmtNumber(s.input_tokens)} in · ${fmtNumber(s.output_tokens)} out`, help: 'Every model call in the session. Input includes cached tokens.' },
          {
            label: 'Cost',
            value: fmtCost(s.cost_usd),
            help: 'Each model call’s tokens times the model’s price: input, output, and cached reads and writes at their own prices. Models without a known price are left out.',
          },
          s.models?.length ? { label: s.models.length > 1 ? 'Models' : 'Model', value: s.models.join(', ') } : null,
        ]}
      />
      <InspectorDivider />
      <InspectorLinks>
        <Link to={securityUrl(pid, new URLSearchParams({ q: s.session_id, time: 'all' }).toString())}>Every AgentShield verdict in this session</Link>
        <button type="button" className="linkish" onClick={replay.start} disabled={replay.starting || replay.job?.status === 'running' || replay.job?.status === 'queued'}>
          {replay.starting ? 'Starting the replay…' : 'Replay with the current rules'}
        </button>
      </InspectorLinks>
      <ReplayNote replay={replay} pid={pid} />
    </Inspector>
  )
}

function ReplayNote({ replay, pid }: { replay: ReplayState; pid: string }) {
  if (replay.enabled === false && (replay.starting || replay.job))
    return <p className="quiet-note">Replay is off on this server: start Weft with --shield-rules-dir (AgentShield's rules) or --shield-url.</p>
  if (!replay.job) return null
  return <ReplayResult pid={pid} job={replay.job} />
}

/** What a replay with the current rules found, in a sentence and short lists. */
export function ReplayResult({ pid, job }: { pid: string; job: ReplayJob }) {
  if (job.status === 'queued' || job.status === 'running') return <p className="quiet-note">Replaying with the current rules…</p>
  if (job.status === 'failed') return <ErrorBox error={job.error ?? 'Replay failed'} />
  const sm = job.summary
  const groups: [string, NonNullable<ReplayJob['newly_blocked']>][] = [
    ['Newly blocked', job.newly_blocked ?? []],
    ['No longer blocked', job.no_longer_blocked ?? []],
    ['Changed', job.changed ?? []],
  ]
  const word = (a: string | undefined) => (a === 'block' ? 'blocked' : a === 'require_approval' ? 'needs approval' : a === 'log' ? 'logged' : 'allowed')
  return (
    <div className="replay-result">
      {sm && (
        <p className="quiet-note">
          Checked {sm.evaluated} of {sm.tool_calls} tool calls with {job.ruleset} ({job.rules_loaded ?? '?'} rules): {sm.newly_blocked} newly blocked, {sm.no_longer_blocked} no longer
          blocked, {sm.changed} changed, {sm.unchanged} unchanged.
        </p>
      )}
      {groups.map(([title, xs]) =>
        xs.length ? (
          <div key={title}>
            <h3 className="replay-h">{title}</h3>
            <ul className="replay-list">
              {xs.map((c) => (
                <li key={c.span_id}>
                  {c.agent_name ?? 'agent'}: {c.tool_name} — {c.before ? word(c.before.action) : 'no verdict'} → {c.after ? word(c.after.action) : 'allowed'}{' '}
                  <Link to={`/${pid}/sessions/${encodeURIComponent(c.session_id)}?step=${encodeURIComponent(`tool:${c.span_id}`)}`}>Show step</Link>
                </li>
              ))}
            </ul>
          </div>
        ) : null,
      )}
      {job.truncated && <p className="quiet-note">Lists are cut at 500 items.</p>}
    </div>
  )
}

/** The entry to select on arrival: the step responsible for a failure or block (blocks first, then approvals, then the deepest failure). */
export function arrivalEntry(story: Story, tl: BuiltTimeline): string | null {
  const p = tl.problems[0]
  return p ? (entryFor(story, p.key)?.key ?? null) : null
}
