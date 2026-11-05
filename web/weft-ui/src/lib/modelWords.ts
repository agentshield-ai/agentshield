// A model id in plain words, for reading text (DESIGN.md §7, convention 8:
// no "mock/json" in the reading flow). The server's offline test models get
// a name a person understands; a real model keeps its provider/model id,
// which is what people know it by. The exact id stays in the Raw view and
// in forms where it is typed.

/** The offline test models the server runs without a provider. */
export const OFFLINE_MODEL_WORDS: Record<string, string> = {
  'mock/echo': 'offline echo model',
  'mock/json': 'offline JSON judge',
}

/** True for the server's offline test models (`mock/…`). */
export function isOfflineModel(id: string | null | undefined): boolean {
  return typeof id === 'string' && id.startsWith('mock/')
}

/**
 * The display name of a model id: "offline echo model", "offline JSON judge",
 * "offline test model" for another `mock/…` id, and the provider/model id
 * itself ("gpt-4o-mini", "anthropic/claude-sonnet-4") for real ones.
 */
export function modelDisplayName(id: string | null | undefined): string {
  if (typeof id !== 'string' || !id.trim()) return ''
  const t = id.trim()
  return OFFLINE_MODEL_WORDS[t] ?? (isOfflineModel(t) ? 'offline test model' : t)
}

/** The display name with "the" before an offline model, for prose: "It runs the offline echo model." / "It runs gpt-4o-mini." */
export function modelInProse(id: string | null | undefined): string {
  const n = modelDisplayName(id)
  return n && isOfflineModel(id) ? `the ${n}` : n
}
