import { describe, expect, it } from 'vitest'
import { fmtDeltaAs, parseDeltaFormat } from './diffUtils'

describe('numeric diff formats', () => {
  it('formats absolute, percentage, ratio and both', () => {
    expect(fmtDeltaAs(100, 150, 'both')).toBe('+50 (+50.0%)')
    expect(fmtDeltaAs(100, 150, 'absolute')).toBe('+50')
    expect(fmtDeltaAs(100, 150, 'percent')).toBe('+50.0%')
    expect(fmtDeltaAs(100, 150, 'ratio')).toBe('1.5×')
    expect(fmtDeltaAs(200, 50, 'percent')).toBe('−75.0%')
    expect(fmtDeltaAs(200, 50, 'ratio')).toBe('0.25×')
    expect(fmtDeltaAs(3, 1000, 'percent')).toBe('+33233%')
    expect(fmtDeltaAs(1200, 1800, 'absolute', (n) => `${n / 1000}s`)).toBe('+0.6s')
  })
  it('handles zero baselines and non-numbers', () => {
    expect(fmtDeltaAs(0, 3, 'percent')).toBe('—')
    expect(fmtDeltaAs(0, 3, 'ratio')).toBe('—')
    expect(fmtDeltaAs(0, 0, 'ratio')).toBe('1×')
    expect(fmtDeltaAs(5, 5, 'absolute')).toBe('±0')
    expect(fmtDeltaAs('a', 1, 'absolute')).toBeNull()
    expect(fmtDeltaAs(true, 1, 'ratio')).toBeNull()
  })
  it('parses the URL value', () => {
    expect(parseDeltaFormat('ratio')).toBe('ratio')
    expect(parseDeltaFormat('nope')).toBe('both')
    expect(parseDeltaFormat(null)).toBe('both')
  })
})
