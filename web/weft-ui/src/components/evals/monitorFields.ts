// Fields of Monitor objects (API.md "Monitors"), read from any version.
import type { Any } from '../../api/types'

export type MonitorScope = 'session' | 'agent' | 'handoff' | 'span'

/** A monitor's span names (functions); versions saved before the rename,
 * and monitors published by the weave client, name them op_names. */
export function monitorNames(v: Any): string[] {
  const n = v?.names ?? v?.op_names
  return Array.isArray(n) ? n.filter((x: unknown): x is string => typeof x === 'string') : []
}

/** A monitor's scopes; the per-span scope was "call" before the rename. */
export function monitorScopes(v: Any): MonitorScope[] {
  return Array.isArray(v?.scopes) ? v.scopes.map((s: string) => (s === 'call' ? 'span' : s)) : []
}
