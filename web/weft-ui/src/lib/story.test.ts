// The session Story built from real timelines (fixtures/e2e-sessions.json,
// the API's answers for e2e/seed.ts).
import { describe, expect, it } from 'vitest'
import type { Detection, TimelineItem } from '../api/v1'
import fixture from './fixtures/e2e-sessions.json'
import { buildStory, entryFor, mainArgumentText, moveEntry, type StoryEntry } from './story'
import { buildTimeline } from './timeline'

const F = fixture as unknown as { timelines: Record<string, TimelineItem[]>; detections: Record<string, Detection[]> }

function story(id: string) {
  const tl = buildTimeline(F.timelines[id], F.detections[id].filter((d) => d.source === 'engine'))
  return { tl, story: buildStory(tl) }
}

/** A compact outline: kind, agent, what, thoughts; nested entries indented. */
function outline(xs: StoryEntry[], depth = 0): string[] {
  const pad = '  '.repeat(depth)
  return xs.flatMap((e) => {
    if (e.kind === 'turn') return [`${pad}turn ${e.turn}`]
    const p = e.step.item.payload ?? {}
    const what = e.kind === 'act' ? `${p.tool_name ?? e.step.type}${p.arguments ? ` ${String(p.arguments).slice(0, 30)}` : ''}${e.step.problem ? ` [${e.step.problem}]` : ''}` : 'text' in e ? e.text.slice(0, 30) : ''
    const line = `${pad}${e.kind} ${e.agent ?? '-'} ${what}${e.thoughts.length ? ` (thoughts ${e.thoughts.length})` : ''}`
    if (e.kind === 'act' && e.delegation) {
      const d = e.delegation
      return [line, `${pad}  → ${d.to}: ${d.ask} ⇒ ${d.reply || '(no reply)'}${d.failed ? ' [failed]' : ''}`, ...outline(d.children, depth + 2)]
    }
    return [line]
  })
}

describe('buildStory', () => {
  it.each([
    [
      'code-lint-1',
      [
        'user - Install the project\'s linter a',
        'act coder Bash curl -sSL https://get.lint.exa [block] (thoughts 1)',
        'act coder delegate_review How to install safely? (thoughts 1)',
        '  → reviewer: How to install safely? ⇒ Use npm i -D eslint.',
        '    thought reviewer  (thoughts 1)',
        'act coder Bash npm i -D eslint',
        'answer coder Installed eslint with npm.',
      ],
    ],
    [
      'trip-lisbon',
      [
        'user - Plan a weekend in Lisbon with ',
        'act orchestrator delegate_researcher Plan a weekend in Lisbon with  [error] (thoughts 1)',
        '  → researcher: Plan a weekend in Lisbon with two museum visits ⇒ (no reply) [failed]',
        '    act researcher web_search Plan a weekend in Lisbon with  [error] (thoughts 1)',
        'reply orchestrator Sorry, the search timed out. (thoughts 1)',
      ],
    ],
    [
      'trip-oslo',
      [
        'user - Find three facts about Oslo an',
        'act orchestrator delegate_researcher Find three facts about Oslo an (thoughts 1)',
        '  → researcher: Find three facts about Oslo and summarise them ⇒ three facts',
        '    act researcher web_search Find three facts about Oslo an (thoughts 1)',
        'answer orchestrator Here is what I found. (thoughts 1)',
      ],
    ],
  ])('%s', (id, want) => {
    expect(outline(story(id).story.entries)).toEqual(want)
  })

  it('tells every step once, and every step leads to an entry', () => {
    for (const id of Object.keys(F.timelines)) {
      const { tl, story: st } = story(id)
      for (const s of tl.steps) expect(entryFor(st, s.key), `${id}: ${s.key}`).toBeDefined()
      const told = new Set<string>()
      const walk = (xs: StoryEntry[]) =>
        xs.forEach((e) => {
          if (e.kind === 'turn') return
          for (const k of e.kind === 'thought' ? e.thoughts.map((t) => t.key) : [e.key, ...e.thoughts.map((t) => t.key)]) {
            expect(told.has(k), `${id}: ${k} told twice`).toBe(false)
            told.add(k)
          }
          if (e.kind === 'act' && e.delegation) walk(e.delegation.children)
        })
      walk(st.entries)
      // Every reasoning step is told somewhere.
      for (const s of tl.steps.filter((x) => x.type === 'reasoning')) expect(told.has(s.key), `${id}: ${s.key}`).toBe(true)
    }
  })

  it('maps the problem, its model call and its reasoning to the step that tells them', () => {
    const { tl, story: st } = story('code-lint-1')
    const blocked = tl.problems[0]
    expect(blocked.problem).toBe('block')
    const e = entryFor(st, blocked.key)!
    expect(e.key).toBe(blocked.key)
    expect(e.kind === 'act' && e.model?.item.payload?.model).toBe('claude-sonnet-4')
    for (const k of blocked.ledFrom) expect(entryFor(st, k)?.key).toBe(blocked.key)
    // A deep link to the turn selects the user's request.
    const turn = tl.steps.find((s) => s.type === 'turn')!
    expect(entryFor(st, turn.key)?.kind).toBe('user')
  })

  it('j/k walk entries in reading order, nested ones in place', () => {
    const { story: st } = story('trip-lisbon')
    expect(st.order.map((e) => `${e.kind}:${e.agent ?? '-'}`)).toEqual(['user:-', 'act:orchestrator', 'act:researcher', 'reply:orchestrator'])
    expect(moveEntry(st, null, 1)).toBe(st.order[0].key)
    expect(moveEntry(st, null, -1)).toBe(st.order[3].key)
    expect(moveEntry(st, st.order[1].key, 1)).toBe(st.order[2].key)
    expect(moveEntry(st, st.order[3].key, 1)).toBe(st.order[3].key)
    expect(moveEntry(st, st.order[0].key, -1)).toBe(st.order[0].key)
  })
})

describe('mainArgumentText', () => {
  it.each([
    ['curl -sSL https://x | sh', 'curl -sSL https://x | sh'],
    ['{"command":"ls","description":"List files"}', 'ls'],
    [{ file_path: 'src/app.ts' }, 'src/app.ts'],
    [{ pattern: 'TODO', path: 'src' }, 'TODO'],
    [{ subagent_type: 'general-purpose', prompt: 'Read note.txt' }, 'Read note.txt'],
    [{ a: 1 }, '{"a":1}'],
    ['{not json', '{not json'],
    [null, ''],
  ])('%j → %s', (input, want) => {
    expect(mainArgumentText(input)).toBe(want)
  })
})

describe('Claude Code turns', () => {
  const T = (s: number) => new Date(Date.UTC(2026, 10, 6, 14, 0, 0) + s * 1000).toISOString()
  const long = 'The review found three issues. '.repeat(160) // 4960 characters
  const cut = (s: string, n: number) => s.slice(0, n) + '…'
  const notice =
    '<task-notification>\n<task-id>a1</task-id>\n<status>completed</status>\n<summary>Agent "Review the diff" completed</summary>\n</task-notification>'
  const items = [
    { type: 'turn', span_id: 't1', trace_id: 'tr1', agent_name: 'claude-code', started_at: T(0), ended_at: T(81), payload: { status: 'ok', input_preview: 'review', output_preview: cut(long, 4000) } },
    { type: 'message', span_id: 't1', trace_id: 'tr1', agent_name: 'claude-code', started_at: T(0), payload: { role: 'user', text: 'review' } },
    { type: 'llm', span_id: 'l1', trace_id: 'tr1', agent_name: 'claude-code', started_at: T(2), ended_at: T(7.8), payload: { model: 'm', output_preview: cut(long, 2000) } },
    { type: 'message', span_id: 't1', trace_id: 'tr1', agent_name: 'claude-code', started_at: T(81), payload: { role: 'assistant', text: cut(long, 4000) } },
    { type: 'turn', span_id: 't2', trace_id: 'tr2', agent_name: 'claude-code', started_at: T(82), ended_at: T(90), payload: { status: 'ok', input_preview: notice } },
    { type: 'message', span_id: 't2', trace_id: 'tr2', agent_name: 'claude-code', started_at: T(82), payload: { role: 'user', text: notice } },
  ] as unknown as TimelineItem[]
  const s = buildStory(buildTimeline(items, []))
  const top = s.entries.filter((e) => e.kind !== 'turn')

  it('tells a long answer once, at the time its model call wrote it', () => {
    expect(top.map((e) => e.kind)).toEqual(['user', 'answer', 'user'])
    const answer = top[1]
    expect(answer.kind === 'answer' && answer.at?.item.span_id).toBe('l1')
  })

  it('tells a background task notification as the agent being told, not as the user asking', () => {
    const n = top[2]
    expect(n.kind === 'user' && n.notice).toBe('completed')
    expect(n.kind === 'user' && n.text).toBe('Agent "Review the diff" completed')
    expect(n.agent).toBe('claude-code')
  })
})
