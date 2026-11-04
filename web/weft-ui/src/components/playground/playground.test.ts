import { describe, expect, it } from 'vitest'
import type { Span } from '../../api/types'
import { addRun, callToRun, loadRuns, runPrompt, saveRuns, type PlaygroundRun } from './history'
import { imageSrc } from './imageUtils'

const run = (id: string, over: Partial<PlaygroundRun> = {}): PlaygroundRun => ({
  id,
  at: '2026-01-01T00:00:00Z',
  mode: 'chat',
  model: 'gpt-4o-mini',
  inputs: { model: 'gpt-4o-mini', messages: [{ role: 'system', content: 'sys' }, { role: 'user', content: 'Hello  there' }] },
  output: 'hi',
  source: 'local',
  ...over,
})

function memStore() {
  const m = new Map<string, string>()
  return { getItem: (k: string) => m.get(k) ?? null, setItem: (k: string, v: string) => void m.set(k, v) }
}

describe('playground history', () => {
  it('adds newest first, de-duplicates and caps', () => {
    let h: PlaygroundRun[] = []
    for (let i = 0; i < 5; i++) h = addRun(h, run(`r${i}`), 3)
    expect(h.map((r) => r.id)).toEqual(['r4', 'r3', 'r2'])
    h = addRun(h, run('r2', { output: 'x'.repeat(5000) }), 3)
    expect(h.map((r) => r.id)).toEqual(['r2', 'r4', 'r3'])
    expect(h[0].output.length).toBeLessThan(4100)
  })
  it('persists per project and survives bad storage', () => {
    const s = memStore()
    saveRuns('e/p', [run('a')], s)
    expect(loadRuns('e/p', s).map((r) => r.id)).toEqual(['a'])
    expect(loadRuns('e/other', s)).toEqual([])
    s.setItem('weft.playground.history.e/p', '{not json')
    expect(loadRuns('e/p', s)).toEqual([])
    expect(loadRuns('e/p', null)).toEqual([])
  })
  it('describes prompts', () => {
    expect(runPrompt(run('a'))).toBe('Hello there')
    expect(runPrompt(run('b', { mode: 'image', inputs: { model: 'm', prompt: 'a cat' } }))).toBe('a cat')
  })
  it('turns tracked spans into runs', () => {
    const chat = {
      id: 'c1',
      name: 'weft:///e/p/function/weft.completions.create:x',
      started_at: '2026-01-01T00:00:00Z',
      ended_at: '2026-01-01T00:00:01.5Z',
      inputs: { model: 'gpt-4o-mini', messages: [{ role: 'user', content: 'q' }] },
      output: { choices: [{ message: { role: 'assistant', content: 'answer' } }] },
      summary: { usage: { 'gpt-4o-mini': { prompt_tokens: 3, completion_tokens: 2 } } },
      error: null,
    } as unknown as Span
    expect(callToRun(chat)).toMatchObject({ id: 'call:c1', mode: 'chat', output: 'answer', callId: 'c1', latencyMs: 1500, tokens: 5, source: 'server' })
    const img = { ...chat, id: 'c2', name: 'weft:///e/p/function/weft.images.generate:x', inputs: { model: 'mock/img', prompt: 'p' }, output: { data: [{}, {}] } } as unknown as Span
    expect(callToRun(img)).toMatchObject({ mode: 'image', images: 2, output: '2 images' })
    expect(callToRun({ ...chat, inputs: {} } as unknown as Span)).toBeNull()
  })
})

describe('image sources', () => {
  it('builds data URLs and filters unsafe URLs', () => {
    expect(imageSrc({ b64_json: 'iVBORw0KGgo=' })).toBe('data:image/png;base64,iVBORw0KGgo=')
    expect(imageSrc({ b64_json: '/9j/4AAQ' })).toBe('data:image/jpeg;base64,/9j/4AAQ')
    expect(imageSrc({ url: 'https://x/y.png' })).toBe('https://x/y.png')
    expect(imageSrc({ url: 'javascript:alert(1)' })).toBeNull()
    expect(imageSrc({ b64_json: '<script>' })).toBeNull()
    expect(imageSrc(null)).toBeNull()
  })
})
