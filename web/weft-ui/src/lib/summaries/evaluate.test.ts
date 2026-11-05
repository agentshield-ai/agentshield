// Table-driven tests of the Evaluate pages' generated words (DESIGN.md §7.2 rule 8).
// The run summaries are the server's (evals.Summarize): booleans become
// {true_count, true_fraction}, numbers {mean}, plus model_latency.
import { describe, expect, it } from 'vitest'
import {
  biggestDifference,
  changeText,
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
  onMetrics,
  plainText,
  queueAsks,
  reviewView,
  times,
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
  scoreShapes,
  needsLook,
  inferScale,
  scaleFromText,
  judgements,
  judgementParts,
  scoreTitle,
  scoreValue,
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
    ['exact.true_fraction', 'exact match'],
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

  it.each([
    ['exact.true_fraction', 0, 0.75, 'both', '+75 pts'],
    ['exact.true_fraction', 0.8, 0.75, 'both', '−5 pts'],
    ['model_latency.mean', 0.015, 0.0237, 'both', '+9 ms (+58%)'],
    ['model_latency.mean', 0.015, 0.0237, 'absolute', '+9 ms'],
    ['similarity.mean', 0.594, 0.791, 'both', '+0.20 (+33%)'],
    ['similarity.mean', 0.594, 0.791, 'percent', '+33%'],
    ['similarity.mean', 0.5, 0.75, 'ratio', '1.5×'],
    ['similarity.mean', 0, 0.75, 'percent', '+0.75'],
    ['similarity.mean', 0.5, 0.5, 'both', 'no change'],
  ] as const)('changeText(%s, %s, %s, %s) = %s', (k, b, v, f, out) => expect(changeText(k, b, v, f)).toBe(out))

  it('metricText', () => expect(metricText('exact.true_fraction', 0.5)).toBe('exact match 50%'))

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
    ['better than the previous run, by another model', run({ model: 'support-v2', summary: sum(8, 10, 0.8) }), run({ model: 'support-v1', summary: sum(0, 10, 0.6) }), 10, 'Scored exact match 80%, up from 0% in the previous run (support-v1); 2 of 10 examples failed.'],
    ['the same', run({ summary: sum(8, 10, 0.8) }), run({ summary: sum(8, 10, 0.5) }), null, 'Scored exact match 80%, the same as the previous run; 2 of 10 examples failed.'],
    ['worse', run({ summary: sum(5, 10, 0.8) }), run({ summary: sum(8, 10, 0.5) }), null, 'Scored exact match 50%, down from 80% in the previous run; 5 of 10 examples failed.'],
    ['no previous run, every example passed', run({ summary: sum(10, 10, 1) }), null, null, 'Scored exact match 100%; all 10 examples passed.'],
    ['a mean only', run({ summary: { similarity: { mean: 0.846 } } }), null, null, 'Scored similarity 0.85.'],
    ['no scores', run({ summary: { model_latency: { mean: 0.1 } } }), null, null, 'Finished with no scores.'],
    ['running', run({ status: 'running' }), null, null, 'Still running.'],
    ['failed with an error', run({ status: 'error', error: 'model not found\nstack' }), null, null, 'Failed: model not found.'],
    ['failed without one', run({ status: 'error' }), null, null, 'Failed.'],
    ['some examples raised errors', run({ status: 'descendant_error', summary: { similarity: { mean: 0.5 } } }), null, null, 'Scored similarity 0.50; some examples raised errors.'],
    [
      'two boolean scorers name the worst',
      run({ summary: { a: { true_count: 9, true_fraction: 0.9 }, b: { true_count: 5, true_fraction: 0.5 } } }),
      null,
      null,
      'Scored a 90%; 5 of 10 examples failed on b.',
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
    ['one evaluation got better', RUNS.slice(1), false, '3 runs of 2 evaluations. Since its previous run, support-eval got better (exact match 0% → 80%).'],
    ['the same score', RUNS, false, '4 runs of 2 evaluations. support-eval scored the same as in its previous run.'],
    ['single runs', [RUNS[1]], false, 'tone-check ran once. The latest run, with support-v2, scored 0.85 on similarity.'],
    [
      'failed and running runs',
      [run({ id: 'x', status: 'error' }), run({ id: 'y', status: 'running' }), run({ id: 'z', status: 'running' })],
      false,
      'support-eval ran 3 times. 1 run failed and 2 are still running.',
    ],
    ['filtered', RUNS.slice(2), true, '2 runs of support-eval match. The latest run, with support-v2, scored 80% on exact match, up from 0%.'],
  ])('evaluationsSentence: %s', (_n, runs, filtered, out) => expect(partsText(evaluationsSentence(runs, filtered))).toBe(out))

  it.each<[string, RunInfo[], string]>([
    ['got better', [run({ id: 'b', model: 'support-v2', summary: sum(3, 4, 0.79) }), run({ id: 'a', summary: sum(0, 4, 0.59) })], 'support-eval ran twice. The latest run, with support-v2, scored 75% on exact match, up from 0%.'],
    ['got worse', [run({ id: 'b', summary: sum(1, 4, 0.5) }), run({ id: 'a', summary: sum(3, 4, 0.5) })], 'support-eval ran twice. The latest run, with support-v1, scored 25% on exact match, down from 75%.'],
    ['the same', [run({ id: 'b', summary: sum(1, 4, 0.5) }), run({ id: 'a', summary: sum(1, 4, 0.9) })], 'support-eval ran twice. The latest run, with support-v1, scored 25% on exact match, the same as the run before.'],
    [
      'the newest run failed',
      [run({ id: 'c', status: 'error' }), run({ id: 'b', summary: sum(1, 4, 0.5) }), run({ id: 'a', summary: sum(3, 4, 0.5) })],
      'support-eval ran 3 times. The latest finished run, with support-v1, scored 25% on exact match, down from 75%. 1 run failed.',
    ],
    ['no scores', [run({ id: 'a', summary: { model_latency: { mean: 0.1 } } })], 'support-eval ran once. The latest run, with support-v1, finished with no scores.'],
  ])('evaluationsSentence, one evaluation: %s', (_n, runs, out) => expect(partsText(evaluationsSentence(runs))).toBe(out))

  it('the one-evaluation sentence links the latest run to its comparison with the one before, or to itself', () => {
    expect(links(evaluationsSentence([run({ id: 'b', summary: sum(3, 4, 0.5) }), run({ id: 'a', summary: sum(0, 4, 0.5) })])).map((l) => l[1])).toEqual([{ kind: 'compare', ids: ['a', 'b'], baseline: 'a' }])
    expect(links(evaluationsSentence([run({ id: 'a', summary: sum(0, 4, 0.5) })])).map((l) => l[1])).toEqual([{ kind: 'compare', ids: ['a'] }])
  })

  it.each([
    [1, 'once'],
    [2, 'twice'],
    [12, '12 times'],
  ])('times(%s) = %s', (n, out) => expect(times(n)).toBe(out))

  it('evaluationsSentence links each change to its comparison, and the failed runs to their filter', () => {
    expect(links(evaluationsSentence(RUNS.slice(1)))).toEqual([['support-eval got better (exact match 0% → 80%)', { kind: 'compare', ids: ['r1', 'r2'], baseline: 'r1' }]])
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
      'support-v2 did better than support-v1 on both metrics. The biggest difference is exact match: 80% against 0%. 1 example got worse and 1 got better.',
    ],
    [
      'the other way round',
      { runs, baseline: 'r4', metrics, examples, lower },
      'support-v1 did worse than support-v2 on both metrics. The biggest difference is exact match: 0% against 80%. 1 example got worse and 1 got better.',
    ],
    ['one run', { runs: [runs[1]], baseline: 'r4', metrics, examples, lower }, 'support-v2 scored exact match 80% and similarity 0.85 on support-eval. 2 of 3 examples failed.'],
    [
      'one run, every example passed',
      { runs: [runs[1]], baseline: 'r4', metrics, examples: [examples[0]], lower },
      'support-v2 scored exact match 80% and similarity 0.85 on support-eval. The one example passed.',
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
      'Against support-v1, the other runs did better on: support-v2 on 2 of 2 and support-v3 on 1 of 2 metrics. The biggest difference is exact match: 90% for support-v3 against 0% for support-v1.',
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
    ['some low', S, false, '4 scores from 2 scorers on 3 sessions. 2 need a look. Goal completion averages 0.53.'],
    ['one low, in one session', S.slice(0, 1), false, '1 score from 1 scorer on 1 session. One is below 0.5.'],
    ['none low', S.slice(1, 3), true, '2 scores from 1 scorer on 2 sessions match. None is below 0.5. Goal completion averages 0.75.'],
  ])('scoresSentence: %s', (_n, s, f, out) => expect(partsText(scoresSentence(s, f))).toBe(out))

  it('scoresSentence counts step scores on their steps', () => {
    const steps = [
      { scorer: 'helpfulness-judge', value: 5, span_id: 's1', scope: 'span' },
      { scorer: 'helpfulness-judge', value: 4, span_id: 's2', scope: 'span' },
    ]
    expect(partsText(scoresSentence(steps))).toBe('2 scores from 1 scorer on 2 steps. None is below 3 out of 5. Helpfulness judge averages 4.50 out of 5.')
    expect(partsText(scoresSentence([...S.slice(0, 1), ...steps]))).toBe('3 scores from 2 scorers on 1 session and 2 steps. One needs a look. Helpfulness judge averages 4.50 out of 5.')
  })

  // The demo's helpfulness judge: {score: 1-5, correct: bool}; 22 correct, 3 not, scores averaging 2.52.
  const demoScores = [4, 1, 1, 2, 1, 1, 1, 5, 2, 2, 4, 1, 1, 4, 2, 5, 2, 1, 4, 5, 4, 1, 3, 2, 4]
  const demo = demoScores.map((score, i) => ({
    scorer: 'helpfulness-judge',
    value: score,
    output: { score, correct: ![15, 19, 22].includes(i), reasoning: 'mock judgement' },
    span_id: `s${i}`,
    scope: 'span',
  }))
  const step = (output: unknown, i = 0) => ({ scorer: 'x', value: scoreValue(output), output, span_id: `s${i}`, scope: 'span' })
  it.each<[string, ReturnType<typeof step>[], string]>([
    ['booleans with a 1-5 score: the booleans first', demo, '25 scores from 1 scorer on 25 steps. 22 said correct; 3 did not. The score averages 2.52 out of 5.'],
    ['bare booleans', [true, false, true].map(step), '3 scores from 1 scorer on 3 steps. 2 were true; 1 was false.'],
    ['all passed', [{ passed: true }, { passed: true }].map((o, i) => step(o, i)), '2 scores from 1 scorer on 2 steps. Both passed.'],
    ['none matched', [{ match: false }, { match: false }, { match: false }].map((o, i) => step(o, i)), '3 scores from 1 scorer on 3 steps. All 3 did not match.'],
    ['a 1-5 scale with no booleans: below its midpoint', [1, 2, 4, 5].map((o, i) => step(o, i)), '4 scores from 1 scorer on 4 steps. 2 are below 3 out of 5. X averages 3 out of 5.'],
    ['a 0-10 scale', [{ rating: 8 }, { rating: 3 }].map((o, i) => step(o, i)), '2 scores from 1 scorer on 2 steps. One is below 5 out of 10. X averages 5.50 out of 10.'],
    ['a 0-1 share', [0.2, 0.9].map((o, i) => step(o, i)), '2 scores from 1 scorer on 2 steps. One is below 0.5. X averages 0.55.'],
    ['numbers with no known range', [250, 900].map((o, i) => step(o, i)), '2 scores from 1 scorer on 2 steps. X averages 575.'],
  ])('scoresSentence reads each scorer by its shape: %s', (_n, s, out) => expect(partsText(scoresSentence(s))).toBe(out))

  it('scoresSentence links what needs a look, and a scorer’s own scale wins over the values seen', () => {
    const ps = scoresSentence(demo).filter((p) => typeof p !== 'string')
    expect(ps).toEqual([{ text: '3 did not', link: { kind: 'scores', filter: 'low' } }])
    const twos = [1, 1].map((o, i) => step(o, i))
    expect(partsText(scoresSentence(twos))).toBe('2 scores from 1 scorer on 2 steps. None is below 0.5. X averages 1.')
    expect(partsText(scoresSentence(twos, false, scoreShapes(twos, { x: { min: 1, max: 5 } })))).toBe(
      '2 scores from 1 scorer on 2 steps. 2 are below 3 out of 5. X averages 1 out of 5.',
    )
  })

  it('a filter does not move the threshold: shapes come from every score', () => {
    const shapes = scoreShapes(demo)
    const low = demo.filter((s) => needsLook(s, shapes[s.scorer]))
    expect(low.map((s) => s.value)).toEqual([5, 5, 3])
    expect(partsText(scoresSentence(low, true, shapes))).toBe('3 scores from 1 scorer on 3 steps match. All 3 did not say correct. The score averages 4.33 out of 5.')
  })

  it.each<[string, number[], { min: number; max: number } | null]>([
    ['0-1', [0, 0.4, 1], { min: 0, max: 1 }],
    ['1-5', [1, 5, 3], { min: 1, max: 5 }],
    ['0-5', [0, 4], { min: 0, max: 5 }],
    ['1-10', [2, 7], { min: 0, max: 10 }],
    ['0-100', [12, 80], { min: 0, max: 100 }],
    ['none', [250], null],
    ['negative', [-1, 2], null],
  ])('inferScale %s', (_n, v, out) => expect(inferScale(v)).toEqual(out))

  it.each<[string, { min: number; max: number } | null]>([
    ['Rate helpfulness 1-5 as JSON', { min: 1, max: 5 }],
    ['Give a score from 0 to 10.', { min: 0, max: 10 }],
    ['a mark out of 10', { min: 0, max: 10 }],
    ['Say yes or no.', null],
  ])('scaleFromText %s', (t, out) => expect(scaleFromText(t)).toEqual(out))

  it('judgementParts names each scorer when there are several', () => {
    const js = judgements([...demo, ...[0.2, 0.8, 0.4].map((v, i) => ({ scorer: 'GoalCompletionJudge', value: v, session_id: `a${i}`, scope: 'session' }))])
    expect(partsText(judgementParts(js))).toBe('5 need a look. Goal completion averages 0.47 and helpfulness judge: 22 of 25 said correct.')
  })

  it.each<[unknown, string]>([
    [{ score: 5, correct: false }, 'Helpfulness judge: not correct, score 5 out of 5'],
    [{ score: 4, correct: true }, 'Helpfulness judge: correct, score 4 out of 5'],
  ])('scoreTitle with the scorer’s shape %#', (output, t) => {
    const shapes = scoreShapes(demo)
    expect(scoreTitle({ scorer: 'helpfulness-judge', value: scoreValue(output), output }, shapes['helpfulness-judge'])).toBe(t)
  })


  it.each([
    [{ scorer: 'GoalCompletionJudge', value: 0.1 }, 'Goal completion 0.10'],
    [{ scorer: 'tool_safety', value: null }, 'Tool safety —'],
    [{ scorer: 'helpfulness-judge', value: 4 }, 'Helpfulness judge 4'],
  ])('scoreTitle %#', (s, t) => expect(scoreTitle(s)).toBe(t))

  it.each([
    [0.4, 0.4],
    [true, 1],
    [false, 0],
    [{ score: 5, correct: false, reasoning: 'x' }, 5],
    [{ correct: false, reasoning: 'x' }, 0],
    [{ b: 2, a: 'text', c: true }, 2],
    ['text', null],
    [null, null],
  ])('scoreValue %#', (v, out) => expect(scoreValue(v)).toBe(out))

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
    ['both metrics', cols.slice(0, 2), [rows[1], { model: 'v4', values: { '0': 0.1, '1': 0.1, '2': 0 } }], 'v2 leads on both metrics. 2 models ranked.'],
    ['all three', cols, [rows[1], { model: 'v4', values: { '0': 0.1, '1': 0.1, '2': 1 } }], 'v2 leads on all 3 metrics. 2 models ranked.'],
    ['one model', cols, rows.slice(0, 1), 'Only v1 has finished runs for these metrics.'],
    ['no rows', cols, [], 'No model has a finished run of these evaluations yet.'],
    ['no columns', [], [], 'This leaderboard has no metrics yet.'],
  ])('leaderboardSentence: %s', (_n, c, r, out) => expect(leaderboardSentence(c, r)).toBe(out))

  it.each<[typeof cols, typeof rows, string]>([
    [cols, rows, 'v2 leads on 2 of 3 metrics, among 3 models.'],
    [cols.slice(0, 2), [rows[1], { model: 'v4', values: { '0': 0.1, '1': 0.1, '2': 0 } }], 'v2 leads on both metrics, among 2 models.'],
    [cols, [], '3 metrics; no finished runs yet.'],
    [[], [], 'No metrics yet.'],
  ])('leaderboardLine %#', (c, r, out) => expect(leaderboardLine(c, r)).toBe(out))

  it.each([
    [1, 1, 'the one metric'],
    [2, 2, 'both metrics'],
    [3, 3, 'all 3 metrics'],
    [2, 3, '2 of 3 metrics'],
    [1, 2, '1 of 2 metrics'],
  ])('onMetrics(%s, %s) = %s', (n, t, out) => expect(onMetrics(n, t)).toBe(out))

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

  it.each<[string, Parameters<typeof queueAsks>[0], string]>([
    [
      'a scale and a yes/no',
      [
        { name: 'quality', schema: { type: 'integer' } },
        { name: 'correct', schema: { type: 'boolean' } },
      ],
      'asks reviewers to rate quality and say whether it is correct',
    ],
    [
      'every kind',
      [
        { name: 'tone', schema: { type: 'number' } },
        { name: 'helpfulness', schema: { type: 'integer' } },
        { name: 'is_safe', schema: { type: 'boolean' } },
        { name: 'category', schema: { type: 'string', enum: ['a', 'b'] } },
        { name: 'notes', schema: { type: 'string' } },
      ],
      'asks reviewers to rate tone and helpfulness, say whether it is safe, choose the category, and add notes',
    ],
    ['not read yet', [{ name: 'quality' }], 'asks reviewers about quality'],
    ['nothing', [], 'asks for nothing: reviewers mark each item done or skip it'],
  ])('queueAsks: %s', (_n, f, out) => expect(queueAsks(f)).toBe(out))

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

describe('one review item', () => {
  it.each([
    ['text', 'text'],
    [true, 'yes'],
    [0.1666666, '0.1667'],
    [1200, '1,200'],
    [['a', 'b'], 'a, b'],
    [{ answer: 'Go to Account', sources: ['kb://faq'] }, 'Answer: Go to Account\nSources: kb://faq'],
    [null, '—'],
  ])('plainText %#', (v, out) => expect(plainText(v)).toBe(out))

  it('an evaluation step: the row’s question first, the answer, the expected answer and the scores as text', () => {
    const v = reviewView(
      { example: { question: 'Do you support SSO?', expected: 'Yes, on the Team plan.' }, model: 'weft:///p/q/object/support-v2:abc', self: 'weft:///p/q/object/support-eval:def' },
      { model_latency: 0.024, output: 'Do you support SSO?', scores: { exact: false, similarity: 1 } },
      { 'weft:///p/q/object/support-v2:abc': 'support-v2' },
    )
    expect(v).toEqual({
      asked: [{ label: 'Question', text: 'Do you support SSO?' }],
      answer: { label: 'Answer', text: 'Do you support SSO?' },
      expected: [{ label: 'Expected', text: 'Yes, on the Team plan.' }],
      scores: 'exact match failed · similarity 1',
      model: 'support-v2',
      modelLatency: 0.024,
    })
  })

  it('any other step: its inputs and output as text; refs that were not read are left out', () => {
    expect(reviewView({ user_message: 'Hi', doc: 'weft:///p/q/object/x:1' }, 'Hello!')).toEqual({
      asked: [{ label: 'User message', text: 'Hi' }],
      answer: { label: 'Answer', text: 'Hello!' },
      expected: [],
      scores: null,
      model: null,
      modelLatency: null,
    })
    expect(reviewView(null, null).answer).toBeNull()
  })
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
