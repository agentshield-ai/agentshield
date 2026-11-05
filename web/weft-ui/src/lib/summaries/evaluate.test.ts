// Table-driven tests of the Evaluate pages' generated words (DESIGN.md §7.2 rule 8).
// The run summaries are the server's (evals.Summarize): booleans become
// {true_count, true_fraction}, numbers {mean}, plus model_latency.
import { describe, expect, it } from 'vitest'
import {
  biggestDifference,
  columnLeaders,
  compareSentence,
  evaluationsSentence,
  exampleChange,
  failureCounts,
  hasFailingScore,
  headline,
  isRunFigure,
  joinParts,
  latestChanges,
  leaderboardLine,
  leaderboardsSentence,
  leaderOf,
  leaderboardSentence,
  metricText,
  metricValue,
  metricWords,
  partsText,
  playgroundSentence,
  previousRun,
  queueLine,
  queuesSentence,
  reviewSentence,
  runGlyph,
  runLine,
  runMeta,
  scoreSubject,
  scoresSentence,
  scoreTitle,
  tokensText,
  type CmpExample,
  type CmpRun,
  type EvalPart,
  type RunInfo,
} from './evaluate'

const sum = (exact: number, of: number, sim: number) => ({ exact: { true_count: exact, true_fraction: exact / of }, model_latency: { mean: 0.03 }, similarity: { mean: sim } })

function run(over: Partial<RunInfo>): RunInfo {
  return { id: 'r', evaluation: 'support-eval', model: 'support-v1', status: 'success', started_at: '2026-11-05T10:00:00Z', ended_at: '2026-11-05T10:00:02Z', summary: {}, ...over }
}

// Newest first, as the list query returns them.
const RUNS: RunInfo[] = [
  run({ id: 'r4', model: 'support-v2', summary: sum(8, 10, 0.846) }),
  run({ id: 'r3', evaluation: 'tone-check', model: 'support-v2', summary: { similarity: { mean: 0.846 } } }),
  run({ id: 'r2', model: 'support-v2', summary: sum(8, 10, 0.846) }),
  run({ id: 'r1', model: 'support-v1', summary: sum(0, 10, 0.644) }),
]

describe('metric words and values', () => {
  it.each([
    ['exact.true_fraction', 'exact'],
    ['similarity.mean', 'similarity'],
    ['GoalCompletionJudge.score.mean', 'goal completion score'],
    ['model_latency.mean', 'model latency'],
    ['accuracy', 'accuracy'],
  ])('metricWords(%s) = %s', (k, w) => expect(metricWords(k)).toBe(w))

  it.each([
    ['exact.true_fraction', 0.8, '80%'],
    ['exact.true_fraction', 0, '0%'],
    ['exact.true_fraction', 0.055, '5.5%'],
    ['similarity.mean', 0.8460622, '0.85'],
    ['model_latency.mean', 0.032, '32 ms'],
    ['count', 12, '12'],
    ['big', 1234.56, '1234.6'],
    ['flag', true, 'yes'],
    ['x', null, '—'],
  ] as const)('metricValue(%s, %s) = %s', (k, v, out) => expect(metricValue(k, v)).toBe(out))

  it('metricText', () => expect(metricText('exact.true_fraction', 0.5)).toBe('exact 50%'))

  it.each([
    ['model_latency.mean', true],
    ['exact.true_count', true],
    ['total_tokens', true],
    ['exact.true_fraction', false],
    ['similarity.mean', false],
  ])('isRunFigure(%s) = %s', (k, v) => expect(isRunFigure(k)).toBe(v))

  it('headline prefers fractions and means, then keys alphabetically, and a preferred key', () => {
    expect(headline(sum(8, 10, 0.8))).toEqual({ key: 'exact.true_fraction', value: 0.8 })
    expect(headline(sum(8, 10, 0.8), 'similarity.mean')).toEqual({ key: 'similarity.mean', value: 0.8 })
    expect(headline({ model_latency: { mean: 1 } })).toBeNull()
    expect(headline({ raw: 3, judge: { mean: 0.4 } })).toEqual({ key: 'judge.mean', value: 0.4 })
  })

  it.each([
    ['one boolean scorer', sum(8, 10, 0.8), null, [{ scorer: 'exact', failed: 2, total: 10 }]],
    ['every example failed: the total comes from the dataset', sum(0, 10, 0.6), 10, [{ scorer: 'exact', failed: 10, total: 10 }]],
    ['every example failed, no dataset size', sum(0, 10, 0.6), null, []],
    ['no boolean scorer', { similarity: { mean: 0.5 } }, 10, []],
    [
      'two boolean scorers, most failures first',
      { a: { true_count: 9, true_fraction: 0.9 }, b: { true_count: 5, true_fraction: 0.5 } },
      null,
      [
        { scorer: 'b', failed: 5, total: 10 },
        { scorer: 'a', failed: 1, total: 10 },
      ],
    ],
  ])('failureCounts: %s', (_n, s, rows, out) => expect(failureCounts(s, rows)).toEqual(out))
})

describe('the Evaluations list', () => {
  it('previousRun is the run before of the same evaluation', () => {
    expect(previousRun(RUNS[0], RUNS)?.id).toBe('r2')
    expect(previousRun(RUNS[2], RUNS)?.id).toBe('r1')
    expect(previousRun(RUNS[1], RUNS)).toBeNull()
  })

  it.each([
    ['better than the previous run, by another model', run({ model: 'support-v2', summary: sum(8, 10, 0.8) }), run({ model: 'support-v1', summary: sum(0, 10, 0.6) }), 10, 'support-v2 scored exact 80%, up from 0% in the previous run (support-v1); 2 of 10 examples failed.'],
    ['the same', run({ summary: sum(8, 10, 0.8) }), run({ summary: sum(8, 10, 0.5) }), null, 'support-v1 scored exact 80%, the same as the previous run; 2 of 10 examples failed.'],
    ['worse', run({ summary: sum(5, 10, 0.8) }), run({ summary: sum(8, 10, 0.5) }), null, 'support-v1 scored exact 50%, down from 80% in the previous run; 5 of 10 examples failed.'],
    ['no previous run, every example passed', run({ summary: sum(10, 10, 1) }), null, null, 'support-v1 scored exact 100%; all 10 examples passed.'],
    ['a mean only', run({ summary: { similarity: { mean: 0.846 } } }), null, null, 'support-v1 scored similarity 0.85.'],
    ['no scores', run({ summary: { model_latency: { mean: 0.1 } } }), null, null, 'support-v1 finished with no scores.'],
    ['running', run({ status: 'running' }), null, null, 'support-v1 is running.'],
    ['failed with an error', run({ status: 'error', error: 'model not found\nstack' }), null, null, 'support-v1 failed: model not found.'],
    ['failed without one', run({ status: 'error' }), null, null, 'support-v1 failed.'],
    ['some examples raised errors', run({ status: 'descendant_error', summary: { similarity: { mean: 0.5 } } }), null, null, 'support-v1 scored similarity 0.50; some examples raised errors.'],
    [
      'two boolean scorers name the worst',
      run({ summary: { a: { true_count: 9, true_fraction: 0.9 }, b: { true_count: 5, true_fraction: 0.5 } } }),
      null,
      null,
      'support-v1 scored a 90%; 5 of 10 examples failed on b.',
    ],
  ])('runLine: %s', (_n, r, prev, rows, out) => expect(runLine({ ...r, rows }, prev)).toBe(out))

  it.each([
    [run({ status: 'error' }), null, 'failed'],
    [run({ status: 'running' }), null, 'running'],
    [run({ summary: sum(5, 10, 0.5) }), run({ summary: sum(8, 10, 0.5) }), 'warning'],
    [run({ summary: sum(8, 10, 0.5) }), run({ summary: sum(5, 10, 0.5) }), 'ok'],
  ])('runGlyph %#', (r, prev, kind) => expect(runGlyph(r, prev).kind).toBe(kind))

  it('runMeta', () => {
    expect(runMeta({ ...run({}), rows: 10 }, 0.0012)).toBe('10 examples · took 2 s · $0.0012')
    expect(runMeta(run({ ended_at: null }))).toBe('')
  })

  it('latestChanges compares each evaluation’s latest finished run with the one before', () => {
    const c = latestChanges(RUNS)
    expect(c.map((x) => [x.evaluation, x.latest.id, x.prev.id, x.dir])).toEqual([['support-eval', 'r4', 'r2', 'same']])
    const c2 = latestChanges(RUNS.slice(1))
    expect(c2.map((x) => [x.evaluation, x.latest.id, x.prev.id, x.dir, x.from, x.to])).toEqual([['support-eval', 'r2', 'r1', 'better', 0, 0.8]])
  })

  const links = (ps: EvalPart[]) => ps.filter((p): p is Exclude<EvalPart, string> => typeof p !== 'string').map((p) => [p.text, p.link])
  it.each<[string, RunInfo[], boolean, string]>([
    ['nothing ran', [], false, 'No evaluation has run yet.'],
    ['nothing matches', [], true, 'No runs match these filters.'],
    ['one evaluation got better', RUNS.slice(1), false, '3 runs of 2 evaluations. Since its previous run, support-eval got better (exact 0% → 80%).'],
    ['the same score', RUNS, false, '4 runs of 2 evaluations. support-eval scored the same as in its previous run.'],
    ['single runs', [RUNS[1]], false, '1 run of tone-check. None has a previous run to compare with yet.'],
    [
      'failed and running runs',
      [run({ id: 'x', status: 'error' }), run({ id: 'y', status: 'running' }), run({ id: 'z', status: 'running' })],
      false,
      '3 runs of support-eval. 1 run failed and 2 are still running.',
    ],
    ['filtered', RUNS.slice(2), true, '2 runs of support-eval match. Since its previous run, support-eval got better (exact 0% → 80%).'],
  ])('evaluationsSentence: %s', (_n, runs, filtered, out) => expect(partsText(evaluationsSentence(runs, filtered))).toBe(out))

  it('evaluationsSentence links each change to its comparison, and the failed runs to their filter', () => {
    expect(links(evaluationsSentence(RUNS.slice(1)))).toEqual([['support-eval got better (exact 0% → 80%)', { kind: 'compare', ids: ['r1', 'r2'], baseline: 'r1' }]])
    expect(links(evaluationsSentence([run({ id: 'x', status: 'error' })]))).toEqual([['1 run failed', { kind: 'runs', status: 'error' }]])
  })

  it('joinParts uses a serial comma', () => {
    expect(partsText(joinParts(['a']))).toBe('a')
    expect(partsText(joinParts(['a', 'b']))).toBe('a and b')
    expect(partsText(joinParts(['a', 'b', 'c']))).toBe('a, b, and c')
  })
})

describe('an evaluation run and its comparison', () => {
  const runs: CmpRun[] = [
    { id: 'r1', label: 'support-v1', evaluation: 'support-eval', status: 'success', summary: sum(0, 10, 0.644) },
    { id: 'r4', label: 'support-v2', evaluation: 'support-eval', status: 'success', summary: sum(8, 10, 0.846) },
  ]
  const metrics = [
    { key: 'exact.true_count', values: { r1: 0, r4: 8 } },
    { key: 'exact.true_fraction', values: { r1: 0, r4: 0.8 } },
    { key: 'model_latency.mean', values: { r1: 0.035, r4: 0.032 } },
    { key: 'similarity.mean', values: { r1: 0.644, r4: 0.846 } },
  ]
  const ex = (a: Record<string, number | boolean>, b: Record<string, number | boolean>): CmpExample => ({
    scores: { r1: a, r4: b },
    failed: { r1: hasFailingScore(a), r4: hasFailingScore(b) },
  })
  const examples = [ex({ exact: false, similarity: 0.6 }, { exact: true, similarity: 1 }), ex({ exact: false, similarity: 0.23 }, { exact: false, similarity: 0.18 }), ex({ exact: false, similarity: 0.5 }, { exact: false, similarity: 0.5 })]
  const lower = (k: string) => /latency/.test(k)

  it('exampleChange against the baseline', () => {
    expect(exampleChange(examples[0], 'r1', ['r1', 'r4'], lower)).toEqual({ regressed: false, improved: true })
    expect(exampleChange(examples[1], 'r1', ['r1', 'r4'], lower)).toEqual({ regressed: true, improved: false })
    expect(exampleChange(examples[2], 'r1', ['r1', 'r4'], lower)).toEqual({ regressed: false, improved: false })
  })

  it.each([
    [{ exact: false }, true],
    [{ exact: true, judge: { ok: false } }, true],
    [{ similarity: 0.1 }, false],
    [null, false],
  ])('hasFailingScore(%j) = %s', (s, v) => expect(hasFailingScore(s)).toBe(v))

  it('biggestDifference skips run figures and measures fractions in points', () => {
    expect(biggestDifference({ runs, baseline: 'r1', metrics, lower })).toEqual({ key: 'exact.true_fraction', run: 'r4', from: 0, to: 0.8, dir: 'better' })
  })

  it.each<[string, Parameters<typeof compareSentence>[0], string]>([
    [
      'two runs',
      { runs, baseline: 'r1', metrics, examples, lower },
      'support-v2 did better than support-v1 on 2 of 2 metrics. The biggest difference is exact: 80% against 0%. 1 example got worse and 1 got better.',
    ],
    [
      'the other way round',
      { runs, baseline: 'r4', metrics, examples, lower },
      'support-v1 did worse than support-v2 on 2 of 2 metrics. The biggest difference is exact: 0% against 80%. 1 example got worse and 1 got better.',
    ],
    ['one run', { runs: [runs[1]], baseline: 'r4', metrics, examples, lower }, 'support-v2 scored exact 80% and similarity 0.85 on support-eval. 2 of 3 examples failed.'],
    [
      'one run, every example passed',
      { runs: [runs[1]], baseline: 'r4', metrics, examples: [examples[0]], lower },
      'support-v2 scored exact 80% and similarity 0.85 on support-eval. The one example passed.',
    ],
    ['one run still running', { runs: [{ ...runs[1], status: 'running' }], baseline: 'r4', metrics, examples: [], lower }, 'support-v2 is still running on support-eval.'],
    [
      'the same scores',
      { runs, baseline: 'r1', metrics: [{ key: 'similarity.mean', values: { r1: 0.5, r4: 0.5 } }], examples: [examples[2]], lower },
      'support-v2 scored the same as support-v1 on the one metric. No example changed.',
    ],
    [
      'three runs',
      {
        runs: [...runs, { id: 'r5', label: 'support-v3', evaluation: 'support-eval', status: 'success', summary: {} }],
        baseline: 'r1',
        metrics: [
          { key: 'exact.true_fraction', values: { r1: 0, r4: 0.8, r5: 0.9 } },
          { key: 'similarity.mean', values: { r1: 0.6, r4: 0.8, r5: 0.5 } },
        ],
        examples: [],
        lower,
      },
      'Against support-v1, the other runs did better on: support-v2 on 2 of 2 and support-v3 on 1 of 2 metrics. The biggest difference is exact: 90% for support-v3 against 0% for support-v1.',
    ],
  ])('compareSentence: %s', (_n, input, out) => expect(partsText(compareSentence(input))).toBe(out))

  it('compareSentence links the example counts to their filters', () => {
    const ps = compareSentence({ runs, baseline: 'r1', metrics, examples, lower }).filter((p) => typeof p !== 'string')
    expect(ps.map((p) => (typeof p === 'string' ? null : p.link))).toEqual([
      { kind: 'examples', filter: 'regressed' },
      { kind: 'examples', filter: 'improved' },
    ])
  })
})

describe('scores', () => {
  const S = [
    { scorer: 'GoalCompletionJudge', value: 0.1, session_id: 'a', scope: 'session' },
    { scorer: 'GoalCompletionJudge', value: 0.9, session_id: 'b', scope: 'session' },
    { scorer: 'GoalCompletionJudge', value: 0.6, session_id: 'c', scope: 'session' },
    { scorer: 'HandoffQualityJudge', value: 0.3, session_id: 'a', scope: 'handoff' },
  ]
  it.each<[string, typeof S, boolean, string]>([
    ['none', [], false, 'Nothing has been scored yet.'],
    ['none match', [], true, 'No scores match these filters.'],
    ['some low', S, false, '4 scores from 2 scorers on 3 sessions. 2 are below 0.5. Goal completion averages 0.53.'],
    ['one low, in one session', S.slice(0, 1), false, '1 score from 1 scorer on 1 session. One is below 0.5.'],
    ['none low', S.slice(1, 3), true, '2 scores from 1 scorer on 2 sessions match. None is below 0.5. Goal completion averages 0.75.'],
  ])('scoresSentence: %s', (_n, s, f, out) => expect(partsText(scoresSentence(s, f))).toBe(out))

  it.each([
    [{ scorer: 'GoalCompletionJudge', value: 0.1 }, 'Goal completion 0.10'],
    [{ scorer: 'tool_safety', value: null }, 'Tool safety —'],
  ])('scoreTitle %#', (s, t) => expect(scoreTitle(s)).toBe(t))

  it.each([
    [{ scope: 'session' }, 'The session'],
    [{ scope: 'agent', agent: 'coder' }, 'Agent coder'],
    [{ scope: 'handoff' }, 'A hand-off'],
    [{ scope: 'span' }, 'A step'],
    [{ scope: 'turn' }, 'A turn'],
  ])('scoreSubject %#', (s, t) => expect(scoreSubject(s)).toBe(t))
})

describe('leaderboards', () => {
  const cols = [
    { index: 0, name: 'exact', minimize: false },
    { index: 1, name: 'similarity', minimize: false },
    { index: 2, name: 'latency', minimize: true },
  ]
  const rows = [
    { model: 'v1', values: { '0': 0, '1': 0.9, '2': 0.5 } },
    { model: 'v2', values: { '0': 0.8, '1': 0.8, '2': 0.3 } },
    { model: 'v3', values: { '0': 0.8, '1': null, '2': 0.9 } },
  ]
  it('columnLeaders: ties lead together, lower wins when minimised', () => {
    expect(columnLeaders(cols, rows)).toEqual({ '0': ['v2', 'v3'], '1': ['v1'], '2': ['v2'] })
  })
  it.each<[string, typeof cols, typeof rows, string]>([
    ['mixed leaders', cols, rows, 'v2 leads on 2 of 3 metrics; v1 leads on similarity. 3 models ranked.'],
    ['one leader everywhere', cols.slice(0, 1), rows.slice(1, 2).concat(rows.slice(0, 1)), 'v2 leads on the one metric. 2 models ranked.'],
    ['one model', cols, rows.slice(0, 1), 'Only v1 has finished runs for these metrics.'],
    ['no rows', cols, [], 'No model has a finished run of these evaluations yet.'],
    ['no columns', [], [], 'This leaderboard has no metrics yet.'],
  ])('leaderboardSentence: %s', (_n, c, r, out) => expect(leaderboardSentence(c, r)).toBe(out))

  it.each<[typeof cols, typeof rows, string]>([
    [cols, rows, 'v2 leads on 2 of 3 metrics, among 3 models.'],
    [cols, [], '3 metrics; no finished runs yet.'],
    [[], [], 'No metrics yet.'],
  ])('leaderboardLine %#', (c, r, out) => expect(leaderboardLine(c, r)).toBe(out))

  it('leaderOf', () => {
    expect(leaderOf(cols, rows)).toBe('v2')
    expect(leaderOf(cols, [])).toBeNull()
  })

  it.each<[{ name: string; leader: string | null }[], string]>([
    [[], 'No leaderboards yet.'],
    [[{ name: 'a', leader: 'v2' }], '1 leaderboard. v2 leads it.'],
    [[{ name: 'a', leader: 'v2' }, { name: 'b', leader: 'v2' }], '2 leaderboards. v2 leads both.'],
    [[{ name: 'a', leader: 'v2' }, { name: 'b', leader: 'v1' }, { name: 'c', leader: 'v2' }], '3 leaderboards. v2 leads 2 of them.'],
    [[{ name: 'a', leader: null }], '1 leaderboard. None has a finished run to rank yet.'],
  ])('leaderboardsSentence %#', (xs, out) => expect(leaderboardsSentence(xs)).toBe(out))
})

describe('review queues', () => {
  it.each<[string, Parameters<typeof queuesSentence>[0], string]>([
    ['none', [], 'No review queues yet.'],
    ['one waiting', [{ id: 'q1', name: 'Weekly', total: 6, done: 2 }], '4 items wait for review in Weekly.'],
    [
      'two waiting',
      [
        { id: 'q1', name: 'Weekly', total: 6, done: 2 },
        { id: 'q2', name: 'Failed', total: 3, done: 0 },
      ],
      '7 items wait for review in 2 queues; Weekly has 4 of them.',
    ],
    [
      'one of two waiting',
      [
        { id: 'q1', name: 'Weekly', total: 6, done: 6 },
        { id: 'q2', name: 'Failed', total: 1, done: 0 },
      ],
      '1 item waits for review in Failed.',
    ],
    ['all reviewed', [{ id: 'q1', name: 'Weekly', total: 6, done: 6 }], 'Every item in the queue has been reviewed.'],
    ['empty', [{ id: 'q1', name: 'Weekly', total: 0, done: 0 }], 'The queue is empty.'],
  ])('queuesSentence: %s', (_n, q, out) => expect(partsText(queuesSentence(q))).toBe(out))

  it.each([
    [{ total: 6, done: 2 }, '4 of 6 items left to review.'],
    [{ total: 6, done: 6 }, 'All 6 items reviewed.'],
    [{ total: 0, done: 0 }, 'Empty. Add steps from Traces or a step’s page.'],
  ])('queueLine %#', (q, out) => expect(queueLine(q)).toBe(out))

  it.each([
    [{ position: 3, total: 6, completed: 2, skipped: 0 }, 'Item 3 of 6. 4 are left to review; 2 done.'],
    [{ position: 1, total: 2, completed: 0, skipped: 1 }, 'Item 1 of 2. One is left to review; 1 skipped.'],
    [{ position: 1, total: 1, completed: 0, skipped: 0 }, 'Item 1 of 1. One is left to review.'],
    [{ position: 2, total: 3, completed: 2, skipped: 1 }, 'You have reviewed every item: 2 done, 1 skipped.'],
    [{ position: null, total: 0, completed: 0, skipped: 0 }, 'This queue is empty.'],
  ])('reviewSentence %#', (p, out) => expect(reviewSentence(p)).toBe(out))
})

describe('playground', () => {
  it.each([
    [[{ model: 'gpt-4o-mini' }], 'Try a prompt on gpt-4o-mini.'],
    [[{ model: 'a' }, { model: 'b' }], 'Try one prompt on a and b side by side.'],
    [[{ model: 'a', latencyMs: 1234, tokens: { total: 34 } }], 'a answered in 1.2 s with 34 tokens.'],
    [[{ model: 'a', running: true }, { model: 'b', error: 'No API key' }], 'a is answering… b failed: No API key.'],
    [[{ model: 'a', error: 'Stopped', latencyMs: 10 }], 'a was stopped.'],
  ])('playgroundSentence %#', (r, out) => expect(playgroundSentence(r)).toBe(out))

  it.each([
    [{ prompt_tokens: 20, completion_tokens: 14, total_tokens: 34 }, '34 tokens (20 in, 14 out)'],
    [{ total_tokens: 1 }, '1 token'],
    [null, ''],
  ])('tokensText %#', (u, out) => expect(tokensText(u)).toBe(out))
})
