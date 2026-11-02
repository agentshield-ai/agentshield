import type { Operand, Operation, Query } from '../api/types'

// Small helpers for building the $expr query language.
export const field = (path: string): Operand => ({ $getField: path })
export const lit = (v: unknown): Operand => ({ $literal: v })
export const eq = (path: string, v: unknown): Operation => ({ $eq: [field(path), lit(v)] })
export const contains = (path: string, s: string, ci = true): Operation => ({
  $contains: { input: field(path), substr: lit(s), case_insensitive: ci },
})
export const gt = (path: string, v: number): Operation => ({ $gt: [{ $convert: { input: field(path), to: 'double' } }, lit(v)] })
export const gte = (path: string, v: number): Operation => ({ $gte: [{ $convert: { input: field(path), to: 'double' } }, lit(v)] })
export const lt = (path: string, v: number): Operation => ({ $lt: [{ $convert: { input: field(path), to: 'double' } }, lit(v)] })
export const and = (...ops: (Operation | null | undefined | false)[]): Operation | null => {
  const xs = ops.filter(Boolean) as Operation[]
  if (xs.length === 0) return null
  return xs.length === 1 ? xs[0] : { $and: xs }
}
export const not = (op: Operation): Operation => ({ $not: [op] })
export const toQuery = (op: Operation | null): Query | undefined => (op ? { $expr: op } : undefined)
