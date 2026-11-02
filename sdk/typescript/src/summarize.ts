/** Summarise scores: booleans → {true_count, true_fraction}, numbers → {mean}, objects recursively. */
export function autoSummarizeValues(values: unknown[]): Record<string, unknown> | null {
  const vals = values.filter((v) => v !== null && v !== undefined);
  if (!vals.length) return null;
  if (vals.every((v) => typeof v === 'boolean')) {
    const tc = vals.filter(Boolean).length;
    return { true_count: tc, true_fraction: tc / vals.length };
  }
  if (vals.every((v) => typeof v === 'number' && Number.isFinite(v))) {
    return { mean: (vals as number[]).reduce((a, b) => a + b, 0) / vals.length };
  }
  if (vals.every((v) => typeof v === 'object' && !Array.isArray(v))) {
    const keys: string[] = [];
    for (const v of vals) for (const k of Object.keys(v as object)) if (!keys.includes(k)) keys.push(k);
    const out: Record<string, unknown> = {};
    for (const k of keys) {
      const s = autoSummarizeValues(vals.map((v) => (v as Record<string, unknown>)[k]));
      if (s !== null) out[k] = s;
    }
    return Object.keys(out).length ? out : null;
  }
  return null;
}
