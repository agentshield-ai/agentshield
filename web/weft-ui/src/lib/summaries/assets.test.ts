// Table-driven tests of the asset pages' generated words (DESIGN.md §7.2 rule 8).
import { describe, expect, it } from 'vitest'
import type { WeftObject } from '../../api/types'
import {
  assetFacts,
  assetPreview,
  assetsSentence,
  diffSentence,
  kindWords,
  placeholders,
  promptShape,
  runAnswer,
  runScores,
  runTitle,
  runWhen,
  sdkName,
  usageCounts,
  versionSentence,
  versionsSentence,
  type AssetPart,
} from './assets'

const NOW = Date.parse('2026-11-05T12:00:00Z')
const ago = (min: number) => new Date(NOW - min * 60_000).toISOString()
const text = (ps: AssetPart[]) => ps.map((p) => (typeof p === 'string' ? p : p.text)).join('')

function obj(over: Partial<WeftObject> = {}): WeftObject {
  return {
    project_id: 'e/p',
    name: 'support-tickets',
    digest: 'd1',
    version: 2,
    kind: 'dataset',
    class: 'Dataset',
    value: { _class_name: 'Dataset', name: 'support-tickets', rows: 'weft:///e/p/table/t1' },
    ref: 'weft:///e/p/object/support-tickets:d1',
    is_latest: true,
    aliases: ['latest'],
    tags: [],
    user_id: null,
    created_at: ago(5),
    deleted_at: null,
    size_bytes: 100,
    version_count: 3,
    ...over,
  }
}
const chat = obj({
  name: 'triage',
  kind: 'prompt',
  class: 'MessagesPrompt',
  version: 1,
  version_count: 2,
  value: { _class_name: 'MessagesPrompt', messages: [{ role: 'system', content: 'Route {question}' }, { role: 'user', content: '{question} for {team}' }] },
})
const judge = obj({ name: 'polite', kind: 'scorer', class: 'LLMAsAJudgeScorer', version: 0, version_count: 1, value: { _class_name: 'LLMAsAJudgeScorer', model: 'gpt-4o', scoring_prompt: 'Polite? {output}' } })
const exact = obj({ name: 'team-match', kind: 'scorer', class: 'ExactMatchScorer', version: 0, version_count: 1, value: { _class_name: 'ExactMatchScorer', target_column: 'expected' } })
const model = obj({ name: 'triage-model', kind: 'model', class: 'LLMStructuredCompletionModel', version: 0, version_count: 1, value: { llm_model_id: 'mock/echo', default_params: { temperature: 0 } } })
const fn = obj({ name: 'route_ticket', kind: 'function', class: null, version: 0, version_count: 1, value: { code: 'def route_ticket(q):\n    """Send a ticket to the right team."""\n    return q\n' } })

describe('kindWords', () => {
  it.each([
    [obj(), 'dataset'],
    [chat, 'chat prompt'],
    [judge, 'LLM judge'],
    [fn, 'function'],
    [obj({ kind: 'object', class: 'MyCustomThing', value: {} }), 'my custom thing'],
    [obj({ kind: 'annotation_spec', class: null, value: {} }), 'annotation field'],
    [obj({ kind: 'object', class: null, value: 3 }), 'saved value'],
  ])('%#', (o, want) => expect(kindWords(o)).toBe(want))
})

describe('placeholders and promptShape', () => {
  it('finds each placeholder once, in order', () => expect(placeholders('{a} {b}', '{a} {c.d}')).toEqual(['a', 'b', 'c.d']))
  it.each([
    [chat.value, '2 messages and 2 placeholders'],
    [{ content: 'Hello {name}' }, '1 placeholder'],
    [{ content: 'Hello' }, 'no placeholders'],
    [{ other: 1 }, ''],
  ])('%#', (v, want) => expect(promptShape(v)).toBe(want))
})

describe('assetFacts (the row summary)', () => {
  it.each([
    ['dataset with rows and use', obj(), { rows: 50, usedBy: 2 }, '50 rows, version 3, used by 2 evaluations'],
    ['dataset before its rows load', obj(), {}, 'Version 3'],
    ['aliases', obj({ aliases: ['latest', 'prod'] }), { rows: 1 }, '1 row, version 3, alias prod'],
    ['chat prompt', chat, {}, 'Chat prompt with 2 messages and 2 placeholders, version 2'],
    ['model', model, {}, 'Runs mock/echo at temperature 0, version 1'],
    ['judge', judge, { serverScorer: true }, 'LLM judge using gpt-4o, version 1'],
    ['sdk scorer', exact, { serverScorer: false }, 'Exact-match scorer on expected, runs only in the SDK, version 1'],
    ['function', fn, {}, 'Version 1'],
    ['evaluation', obj({ kind: 'evaluation', class: 'Evaluation', version: 0, version_count: 1, value: { dataset: 'weft:///e/p/object/qa:d', scorers: ['a', 'b'] } }), {}, 'Scores qa with 2 scorers, version 1'],
    ['leaderboard (the group heading names it)', obj({ kind: 'leaderboard', class: 'Leaderboard', version: 0, version_count: 1, value: {} }), {}, 'Version 1'],
    ['custom class', obj({ kind: 'object', class: 'MyCustomThing', version: 0, version_count: 1, value: {} }), {}, 'My custom thing, version 1'],
  ])('%s', (_, o, f, want) => expect(assetFacts(o, f)).toBe(want))
})

describe('assetPreview', () => {
  it.each([
    [obj({ value: { description: 'Labelled  tickets.' } }), 'Labelled tickets.'],
    [chat, '{question} for {team}'],
    [fn, 'Send a ticket to the right team.'],
    [obj({ value: { content: 'x'.repeat(200) } }), 'x'.repeat(119) + '…'],
    [obj({ value: null }), ''],
  ])('%#', (o, want) => expect(assetPreview(o)).toBe(want))
})

describe('usageCounts', () => {
  it('counts each evaluation once per asset it names', () => {
    const evs = [
      { value: { dataset: 'weft:///e/p/object/support-tickets:d1', scorers: ['weft:///e/p/object/team-match:x', 'weft:///e/p/object/team-match:y'] } },
      { value: { dataset: 'weft:///e/p/object/support-tickets:d2', model: 'weft:///e/p/object/triage-model:m' } },
      { value: { dataset: 'not a ref' } },
    ]
    const u = usageCounts(evs)
    expect(u.get('support-tickets')).toBe(2)
    expect(u.get('team-match')).toBe(1)
    expect(u.get('triage-model')).toBe(1)
    expect(u.size).toBe(3)
  })
})

describe('assetsSentence', () => {
  const qa = obj({ name: 'qa', created_at: ago(120), version: 0, version_count: 1 })
  it.each([
    ['empty', { kind: 'datasets' as const, objects: [] }, 'No datasets yet.'],
    [
      'datasets with rows',
      { kind: 'datasets' as const, objects: [obj(), qa], rows: new Map([['support-tickets', 50], ['qa', 2]]) },
      '2 datasets with 52 rows in all. The latest change was to support-tickets, 5 min ago.',
    ],
    ['one dataset', { kind: 'datasets' as const, objects: [qa], rows: new Map([['qa', 2]]) }, '1 dataset with 2 rows. The latest change was to qa, 2 h ago.'],
    ['rows still loading', { kind: 'datasets' as const, objects: [obj(), qa], rows: new Map([['qa', 2]]) }, '2 datasets. The latest change was to support-tickets, 5 min ago.'],
    ['models', { kind: 'models' as const, objects: [model, obj({ name: 'm2', kind: 'model', class: 'Model', created_at: ago(9) })] }, '2 models; the server can run 1. The latest change was to triage-model, 5 min ago.'],
    ['one model', { kind: 'models' as const, objects: [model] }, '1 model; the server can run it. The latest change was to triage-model, 5 min ago.'],
    ['scorers', { kind: 'scorers' as const, objects: [judge, exact], serverScorer: (o: { name: string }) => o.name === 'polite' }, '2 scorers; 1 runs only in the SDK. The latest change was to polite, 5 min ago.'],
    ['scorers all server', { kind: 'scorers' as const, objects: [judge, exact], serverScorer: () => true }, '2 scorers; both can run on the server. The latest change was to polite, 5 min ago.'],
    ['functions', { kind: 'ops' as const, objects: [fn] }, '1 traced function. The latest change was to route_ticket, 5 min ago.'],
    ['all', { kind: 'all' as const, objects: [obj(), chat, judge, exact] }, '4 assets of 3 kinds. The latest change was to support-tickets, 5 min ago.'],
  ])('%s', (_, input, want) => expect(text(assetsSentence({ ...input, now: NOW }))).toBe(want))

  it('links the latest name', () => {
    const p = assetsSentence({ kind: 'prompts', objects: [chat], now: NOW })
    expect(p).toContainEqual({ text: 'triage', name: 'triage' })
  })
})

describe('versionsSentence', () => {
  it.each([
    ['one', [obj({ version: 0, version_count: 1 })], 'support-tickets has one version, saved 5 min ago.'],
    [
      'several, with aliases',
      [obj(), obj({ version: 1, aliases: ['prod'], created_at: ago(60) }), obj({ version: 0, aliases: ['old'], created_at: ago(90) })],
      'support-tickets has 3 versions; the latest, version 3, was saved 5 min ago. “old” points to version 1 and “prod” points to version 2.',
    ],
    ['missing', [], 'x was not found in this project.'],
  ])('%s', (_, vs, want) => expect(text(versionsSentence(vs.length ? 'support-tickets' : 'x', vs, NOW))).toBe(want))
})

describe('versionSentence', () => {
  it.each([
    ['dataset, more rows', { o: obj(), rows: 50, prevRows: 46, latestVersion: 2 }, 'Version 3 of support-tickets, saved 5 min ago. It has 50 rows, 4 more than version 2.'],
    ['dataset, fewer rows', { o: obj(), rows: 40, prevRows: 46 }, 'Version 3 of support-tickets, saved 5 min ago. It has 40 rows, 6 fewer than version 2.'],
    ['dataset, same', { o: obj(), rows: 46, prevRows: 46 }, 'Version 3 of support-tickets, saved 5 min ago. It has 46 rows, as many as version 2.'],
    ['first dataset version', { o: obj({ version: 0 }), rows: 1, prevRows: null }, 'Version 1 of support-tickets, saved 5 min ago. It has 1 row.'],
    ['older version', { o: obj({ version: 0, user_id: 'ann' }), latestVersion: 2 }, 'Version 1 of support-tickets, saved 5 min ago by ann. A newer version, version 3, exists.'],
    ['prompt', { o: chat }, 'Version 2 of triage, saved 5 min ago. It has 2 messages and 2 placeholders: {question}, {team}.'],
    ['model', { o: model }, 'Version 1 of triage-model, saved 5 min ago. It runs mock/echo.'],
    ['judge', { o: judge }, 'Version 1 of polite, saved 5 min ago. It is an LLM judge using gpt-4o.'],
    ['exact', { o: exact }, 'Version 1 of team-match, saved 5 min ago. It is an exact-match scorer.'],
    ['evaluation', { o: obj({ name: 'ev', kind: 'evaluation', class: 'Evaluation', version: 0, value: { scorers: ['a', 'b'], trials: 3 } }) }, 'Version 1 of ev, saved 5 min ago. It scores each row with 2 scorers, 3 times.'],
  ])('%s', (_, input, want) => expect(text(versionSentence({ ...input, now: NOW }))).toBe(want))
})

describe('diffSentence', () => {
  const a = { version: 1 }
  const b = { version: 2 }
  it.each([
    ['same', { a: b, b, same: true, changed: 0, added: 0, removed: 0 }, 'Both sides are version 3, so there is nothing to compare.'],
    ['changes', { a, b, same: false, changed: 1, added: 2, removed: 1 }, 'From version 2 to version 3, 1 value changed, 2 were added, and 1 was removed.'],
    ['rows only', { a, b, same: false, changed: 1, added: 0, removed: 0, rows: { a: 46, b: 50, added: 4, removed: 0 } }, 'From version 2 to version 3, 1 value changed. The rows went from 46 to 50: 4 added, none removed.'],
    ['same rows', { a, b, same: false, changed: 0, added: 0, removed: 0, rows: { a: 2, b: 2, added: 0, removed: 0 } }, 'From version 2 to version 3, no value changed. The 2 rows are the same.'],
  ])('%s', (_, input, want) => expect(diffSentence(input)).toBe(want))
})

describe('run rows', () => {
  it.each([
    [{ question: 'Where is my order?', user_id: 'u1' }, 'Where is my order?'],
    [{ self: 'weft:///e/p/object/x:d', user_id: 'u1' }, 'u1'],
    ['plain text', 'plain text'],
    [{ n: 3 }, 'support_agent'],
    [null, 'support_agent'],
  ])('runTitle %#', (inputs, want) => expect(runTitle(inputs, 'support_agent')).toBe(want))
  it.each([
    [{ answer: 'Shipped.', sources: ['kb://faq'] }, null, 'Shipped.'],
    ['ok', null, 'ok'],
    [null, 'RateLimitError: 429 Too Many Requests\n  at x', 'Failed: RateLimitError: 429 Too Many Requests'],
    [null, null, 'No answer recorded.'],
    [{ score: 4 }, null, '{"score":4}'],
  ])('runAnswer %#', (out, err, want) => expect(runAnswer(out, err)).toBe(want))
  it('runWhen', () => {
    expect(runWhen(3390, ago(120), NOW)).toBe('3.4 s · 2 h ago')
    expect(runWhen(null, ago(5), NOW)).toBe('5 min ago')
  })
  it.each([
    [{ 'exact-match': { true_count: 0, true_fraction: 0 }, 'helpfulness-judge': { score: { mean: 2.75 } }, model_latency: { mean: 0.1 } }, 'Exact match 0% and helpfulness judge score 2.75'],
    [{}, 'No scores.'],
    [{ a: { mean: 1 }, b: { mean: 2 }, c: { mean: 3 } }, 'A 1, b 2, and 1 more'],
  ])('runScores %#', (summary, want) => expect(runScores(summary, 2)).toBe(want))
})

it('sdkName keeps the API numbering', () => expect(sdkName(obj())).toBe('support-tickets:v2'))
