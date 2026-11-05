import { describe, expect, it } from 'vitest'
import { isRef, spanDisplayName, parseRef, refLabel } from '../api/refs'
import { callCost, callTokens, fmtCost, fmtDuration, fmtRelative, getPath, preview } from './format'
import { and, contains, eq, gt, toQuery } from './query'
import { refUrl } from './urls'
import { extractChat, messageText, normaliseMessage } from '../components/traces/ChatView'
import type { Span } from '../api/types'

describe('refs', () => {
  it('parses object refs with extra path', () => {
    const r = parseRef('weft:///acme/app/object/my_ds:abc123/attr/rows/id/r1')!
    expect(r).toMatchObject({ projectId: 'acme/app', kind: 'object', name: 'my_ds', digest: 'abc123', extra: ['attr', 'rows', 'id', 'r1'] })
  })
  it('reads the earlier kind names op and call as function and span', () => {
    expect(parseRef('weft:///e/p/op/f:d1')).toMatchObject({ kind: 'function', name: 'f', digest: 'd1' })
    expect(parseRef('weft:///e/p/call/c1')).toMatchObject({ kind: 'span', name: 'c1', digest: null })
    expect(refUrl('weft:///e/p/call/c1')).toBe('/e/p/spans/c1')
    expect(refUrl('weft:///e/p/op/f:d1')).toBe('/e/p/objects/f/versions/d1')
    expect(spanDisplayName('weft:///e/p/op/Model.predict:xyz')).toBe('Model.predict')
  })
  it('rejects other schemes', () => {
    expect(isRef('other:///e/p/span/1')).toBe(false)
    expect(parseRef('other:///e/p/function/f:d')).toBeNull()
  })
  it('rejects non-refs', () => {
    expect(isRef('https://example.com')).toBe(false)
    expect(parseRef('weft:///e/p/bogus/x')).toBeNull()
  })
  it('labels and links refs', () => {
    expect(spanDisplayName('weft:///e/p/function/Model.predict:xyz')).toBe('Model.predict')
    expect(spanDisplayName('openai.chat')).toBe('openai.chat')
    expect(refLabel('weft:///e/p/object/ds:abcdef123')).toBe('ds:abcdef')
    expect(refUrl('weft:///e/p/span/c1')).toBe('/e/p/spans/c1')
    expect(refLabel('weft:///e/p/span/0123456789')).toBe('span 01234567')
    expect(refUrl('weft:///e/p/object/ds:d1')).toBe('/e/p/objects/ds/versions/d1')
    expect(refUrl('weft:///e/p/table/d1')).toBeNull()
  })
})

describe('format', () => {
  it('formats durations and costs', () => {
    expect(fmtDuration(null)).toBe('—')
    expect(fmtDuration(250)).toBe('250 ms')
    expect(fmtDuration(1500)).toBe('1.5 s')
    expect(fmtDuration(125_000)).toBe('2 min 5 s')
    expect(fmtCost(0)).toBe('$0')
    expect(fmtCost(0.00001)).toBe('<$0.0001')
    expect(fmtCost(2.5)).toBe('$2.50')
    expect(fmtCost(0.0048000000000000004)).toBe('$0.0048')
    expect(fmtCost(0.0016 * 3)).toBe('$0.0048')
    expect(fmtCost(0.5)).toBe('$0.50')
    expect(fmtCost(0.123456)).toBe('$0.12')
    expect(fmtCost(0.0004)).toBe('$0.0004')
    expect(fmtCost(1234.5)).toBe('$1,234.50')
    expect(fmtCost(null)).toBe('—')
  })
  it('sums tokens and costs across models', () => {
    const call = {
      summary: {
        usage: { a: { prompt_tokens: 10, completion_tokens: 5 }, b: { input_tokens: 3, output_tokens: 2, total_tokens: 5 } },
        weft: { status: 'success', latency_ms: 1, trace_name: 'x', display_name: null, costs: { a: { prompt_tokens_total_cost: 0.1, completion_tokens_total_cost: 0.2 } } },
      },
    } as unknown as Span
    expect(callTokens(call)).toEqual({ prompt: 13, completion: 7, total: 20 })
    expect(callCost(call)).toBeCloseTo(0.3)
  })
  it('reads paths and previews values', () => {
    expect(getPath({ a: { b: [1] } }, 'a.b')).toEqual([1])
    expect(getPath({ a: 1 }, 'a.b.c')).toBeUndefined()
    expect(preview('a\n\n b', 10)).toBe('a b')
    expect(preview({ k: 'x'.repeat(50) }, 12)).toHaveLength(12)
  })
})

describe('query builders', () => {
  it('builds $expr trees', () => {
    expect(toQuery(and(eq('name', 'f'), null, gt('summary.weft.latency_ms', 100)))).toEqual({
      $expr: {
        $and: [
          { $eq: [{ $getField: 'name' }, { $literal: 'f' }] },
          { $gt: [{ $convert: { input: { $getField: 'summary.weft.latency_ms' }, to: 'double' } }, { $literal: 100 }] },
        ],
      },
    })
    expect(toQuery(and())).toBeUndefined()
    expect(and(contains('inputs.q', 'hi'))).toEqual({ $contains: { input: { $getField: 'inputs.q' }, substr: { $literal: 'hi' }, case_insensitive: true } })
  })
})

describe('chat extraction', () => {
  it('handles OpenAI chat completions with tool calls', () => {
    const chat = extractChat({
      inputs: { model: 'gpt-4o', messages: [{ role: 'user', content: 'weather?' }] },
      output: {
        choices: [
          { finish_reason: 'tool_calls', message: { role: 'assistant', content: null, tool_calls: [{ id: 't', function: { name: 'get', arguments: '{"city":"Oslo"}' } }] } },
        ],
      },
    })!
    expect(chat.input).toHaveLength(1)
    const out = normaliseMessage(chat.output![0])
    expect(out.parts.find((p) => p.type === 'tool_call')).toMatchObject({ name: 'get', arguments: { city: 'Oslo' } })
  })
  it('handles Anthropic content blocks and a system kwarg', () => {
    const chat = extractChat({
      inputs: { system: 'be brief', messages: [{ role: 'user', content: [{ type: 'text', text: 'hi' }] }] },
      output: { type: 'message', role: 'assistant', content: [{ type: 'text', text: 'hello' }], stop_reason: 'end_turn' },
    })!
    expect(chat.system).toBe('be brief')
    expect(messageText(normaliseMessage(chat.output![0]))).toBe('hello')
  })
  it('handles OTel GenAI parts and plain string outputs', () => {
    const chat = extractChat({ inputs: { messages: [{ role: 'user', parts: [{ type: 'text', content: 'q' }] }] }, output: 'answer' })!
    expect(messageText(normaliseMessage(chat.input[0]))).toBe('q')
    expect(chat.output).toEqual([{ role: 'assistant', content: 'answer' }])
  })
  it('returns null for non-chat calls', () => {
    expect(extractChat({ inputs: { x: 1 }, output: 2 })).toBeNull()
  })
})

describe('playground variables', async () => {
  const { templateVars, fillVars } = await import('../pages/playground/PlaygroundPage')
  it('finds and fills placeholders', () => {
    const msgs = [
      { id: '1', role: 'system', content: 'You help {company} customers. Literal {{braces}}.' },
      { id: '2', role: 'user', content: '{question} ({company})' },
      { id: '3', role: 'user', content: '{"json": true}', json: true },
    ]
    expect(templateVars(msgs)).toEqual(['company', 'question'])
    expect(fillVars('{question} for {company} {{x}} {missing}', { question: 'Q?', company: 'Acme' })).toBe('Q? for Acme {x} {missing}')
  })
})

describe('fmtRelative', () => {
  const now = Date.parse('2026-11-05T12:00:00Z')
  const ago = (ms: number) => new Date(now - ms).toISOString()
  it.each([
    [30_000, 'just now'],
    [59 * 60_000 + 59_000, '59 min ago'],
    [60 * 60_000, '1 h ago'],
    [23 * 3_600_000 + 59 * 60_000, '23 h ago'],
    [24 * 3_600_000, '1 day ago'],
    [3 * 86_400_000 + 5 * 3_600_000, '3 days ago'],
  ])('%d ms → %s', (ms, want) => {
    expect(fmtRelative(ago(ms), now)).toBe(want)
  })
})
