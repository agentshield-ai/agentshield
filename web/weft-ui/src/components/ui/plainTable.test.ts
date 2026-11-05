import { describe, expect, it } from 'vitest'
import { stackWord } from './PlainTable'

// The word a value carries when a PlainTable stacks its rows on a phone.
describe('stackWord', () => {
  it.each([
    [undefined, 'Failed', '3', 'failed'],
    [undefined, 'Sessions', '7', 'sessions'],
    ['run|runs', 'Runs', '1', 'run'],
    ['run|runs', 'Runs', '7', 'runs'],
    ['', 'Cost', '$0.23', ''],
    ['hand-off|hand-offs', 'Times', '5', 'hand-offs'],
  ])('stackWord(%s, %s, %s) = %s', (spec, heading, value, want) => expect(stackWord(spec, heading, value)).toBe(want))
})
