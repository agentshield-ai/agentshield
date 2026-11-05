import { describe, expect, it } from 'vitest'
import { scorerScale } from './useScorerScales'

describe('a scorer object’s own scale', () => {
  it.each<[unknown, { min: number; max: number } | null]>([
    [{ scale: { min: 0, max: 10 } }, { min: 0, max: 10 }],
    [{ min_score: 1, max_score: 7 }, { min: 1, max: 7 }],
    [{ scoring_prompt: 'Rate helpfulness 1-5 as JSON {"score": n, "correct": bool}.' }, { min: 1, max: 5 }],
    [{ description: 'Scores from 0 to 100' }, { min: 0, max: 100 }],
    [{ scoring_prompt: 'Is the answer correct? Reply yes or no.' }, null],
    [{ scale: { min: 5, max: 1 } }, null],
    [null, null],
  ])('%#', (v, out) => expect(scorerScale(v)).toEqual(out))
})
