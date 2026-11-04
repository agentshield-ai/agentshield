// Shared helpers for object (asset) pages: class detection, value builders
// for the object conventions in docs/weft/API.md, and small query hooks.
import { useQuery } from '@tanstack/react-query'
import { api } from '../../api/client'
import { objectRef, parseRef } from '../../api/refs'
import type { Any, ObjectKind, ObjectsFilter, TableRow, WeftObject } from '../../api/types'

/** The object list pages (Assets). */
export type ObjectPageKind = 'datasets' | 'models' | 'prompts' | 'scorers' | 'ops' | 'all'

/** Every object kind except function (docs/weft/API.md "Kind and class"). */
export const VALUE_KINDS: ObjectKind[] = [
  'dataset',
  'model',
  'evaluation',
  'scorer',
  'prompt',
  'leaderboard',
  'monitor',
  'annotation_spec',
  'saved_view',
  'provider',
  'provider_model',
  'dashboard',
  'automation',
  'object',
]

export const KIND_META: Record<ObjectPageKind, { title: string; singular: string; filter: ObjectsFilter; blurb: string }> = {
  datasets: {
    title: 'Datasets',
    singular: 'dataset',
    filter: { kinds: ['dataset'] },
    blurb: 'Versioned collections of examples used for evaluations.',
  },
  models: {
    title: 'Models',
    singular: 'model',
    filter: { kinds: ['model'] },
    blurb: 'Versioned model configurations. LLM completion models can be run by the server.',
  },
  prompts: {
    title: 'Prompts',
    singular: 'prompt',
    filter: { kinds: ['prompt'] },
    blurb: 'Versioned prompt templates with {placeholders}.',
  },
  scorers: {
    title: 'Scorers',
    singular: 'scorer',
    filter: { kinds: ['scorer'] },
    blurb: 'Functions and LLM judges that grade model outputs.',
  },
  ops: { title: 'Functions', singular: 'function', filter: { kinds: ['function'] }, blurb: 'Traced functions, versioned by their source code.' },
  all: { title: 'All objects', singular: 'object', filter: { kinds: VALUE_KINDS }, blurb: 'Every versioned object in this project.' },
}

export const OBJ_BASES = ['Object', 'BaseModel']

/** Scorer classes the server knows how to run (evaluate_model, monitors). */
export const SERVER_SCORER_CLASSES = [
  'LLMAsAJudgeScorer',
  'ExactMatchScorer',
  'ContainsScorer',
  'RegexScorer',
  'JSONValidScorer',
  'LevenshteinScorer',
] as const
export type ServerScorerClass = (typeof SERVER_SCORER_CLASSES)[number]

export const SCORER_LABELS: Record<ServerScorerClass, string> = {
  LLMAsAJudgeScorer: 'LLM judge',
  ExactMatchScorer: 'Exact match',
  ContainsScorer: 'Contains',
  RegexScorer: 'Regex',
  JSONValidScorer: 'Valid JSON',
  LevenshteinScorer: 'Levenshtein similarity',
}

export function valClass(val: Any): string | null {
  if (!val || typeof val !== 'object') return null
  return (val._class_name as string) ?? (val._type as string) ?? null
}

/** Most specific class name for display. */
export function objClass(o: WeftObject): string {
  if (o.kind === 'function') return 'Function'
  return o.class ?? valClass(o.value) ?? (Array.isArray(o.value) ? 'List' : typeof o.value === 'object' ? 'Object' : typeof o.value)
}

/** The base class of each kind. */
const KIND_CLASS: Partial<Record<ObjectKind, string>> = {
  dataset: 'Dataset',
  model: 'Model',
  evaluation: 'Evaluation',
  scorer: 'Scorer',
  prompt: 'Prompt',
  leaderboard: 'Leaderboard',
  monitor: 'Monitor',
  annotation_spec: 'AnnotationSpec',
  saved_view: 'SavedView',
  provider: 'Provider',
  provider_model: 'ProviderModel',
  dashboard: 'Dashboard',
  automation: 'Automation',
}

function classes(o: WeftObject): string[] {
  const out = [KIND_CLASS[o.kind], o.class, valClass(o.value)]
  if (o.value && typeof o.value === 'object' && Array.isArray(o.value._bases)) out.push(...o.value._bases)
  return out.filter(Boolean) as string[]
}

export const hasClass = (o: WeftObject, ...names: string[]) => classes(o).some((c) => names.includes(c))
export const isDataset = (o: WeftObject) => hasClass(o, 'Dataset')
export const isPrompt = (o: WeftObject) => hasClass(o, 'Prompt', 'StringPrompt', 'MessagesPrompt')
export const isEvaluation = (o: WeftObject) => hasClass(o, 'Evaluation')
export const isScorer = (o: WeftObject) => hasClass(o, 'Scorer', 'LLMAsAJudgeScorer')
export const isLLMModel = (o: WeftObject) => hasClass(o, 'LLMStructuredCompletionModel')
export const isModel = (o: WeftObject) => hasClass(o, 'Model', 'LLMStructuredCompletionModel')
/** More server-runnable scorer classes (created by the Signals gallery or the SDKs, not the generic scorer form). */
export const EXTRA_SERVER_SCORER_CLASSES = [
  'RemoteScorer',
  'HallucinationFreeScorer',
  'SummarizationScorer',
  'RelevanceScorer',
  'ValidXMLScorer',
  'JSONSchemaScorer',
  'OpenAIModerationScorer',
  'EmbeddingSimilarityScorer',
  'RuleScorer',
] as const
export const isServerScorerClass = (cls: string | null | undefined) =>
  (SERVER_SCORER_CLASSES as readonly string[]).includes(cls ?? '') || (EXTRA_SERVER_SCORER_CLASSES as readonly string[]).includes(cls ?? '')
export const isServerScorer = (o: WeftObject) => isServerScorerClass(valClass(o.value))

/** The object list page an object belongs to. */
export function kindOf(o: WeftObject): ObjectPageKind {
  if (o.kind === 'function') return 'ops'
  if (isDataset(o)) return 'datasets'
  if (isPrompt(o)) return 'prompts'
  if (isScorer(o)) return 'scorers'
  if (isModel(o)) return 'models'
  return 'all'
}

export const refOf = (o: WeftObject) => objectRef(o.project_id, o.name, o.digest, o.kind === 'function' ? 'function' : 'object')

/** Ref with a readable version, e.g. for SDK snippets: name:v3 */
export const versionLabel = (o: Pick<WeftObject, 'version'>) => `v${o.version}`

export function refName(ref: string | null | undefined): string {
  if (!ref) return '—'
  const p = parseRef(ref)
  return p ? p.name : ref
}

/** Valid object names: letters, digits, -, _, . */
export function sanitizeObjectId(s: string): string {
  return s
    .trim()
    .replace(/\s+/g, '-')
    .replace(/[^A-Za-z0-9_.-]/g, '')
}

export function templateVars(...texts: (string | null | undefined)[]): string[] {
  const out = new Set<string>()
  for (const t of texts) {
    if (!t) continue
    for (const m of t.matchAll(/\{([A-Za-z_][\w.]*)\}/g)) out.add(m[1])
  }
  return [...out]
}

// ------------------------------------------------------------- value builders

export function datasetVal(name: string, rowsRef: string, description: string | null = null, prev?: Any) {
  return {
    ...(prev ?? {}),
    _type: 'Dataset',
    _class_name: 'Dataset',
    _bases: OBJ_BASES,
    name,
    description: description ?? prev?.description ?? null,
    rows: rowsRef,
  }
}

export function classVal(cls: string, bases: string[], fields: Record<string, Any>) {
  return { _type: cls, _class_name: cls, _bases: [...bases, ...OBJ_BASES], ...fields }
}

// ------------------------------------------------------------- hooks

export function useObjects(projectId: string, filter: ObjectsFilter, opts: { enabled?: boolean; metadataOnly?: boolean } = {}) {
  return useQuery({
    queryKey: ['objects', projectId, filter, !!opts.metadataOnly],
    queryFn: () =>
      api
        .objectsQuery({
          project_id: projectId,
          filter: { latest_only: true, ...filter },
          sort_by: [{ field: 'created_at', direction: 'desc' }],
          limit: 1000,
          metadata_only: opts.metadataOnly,
        })
        .then((r) => r.items),
    enabled: opts.enabled ?? true,
  })
}

export function useObjectVersions(projectId: string, objectId: string, metadataOnly = true) {
  return useQuery({
    queryKey: ['obj-versions', projectId, objectId, metadataOnly],
    queryFn: () =>
      api
        .objectsQuery({
          project_id: projectId,
          filter: { names: [objectId], latest_only: false },
          sort_by: [{ field: 'created_at', direction: 'desc' }],
          limit: 1000,
          metadata_only: metadataOnly,
        })
        .then((r) => r.items.slice().sort((a, b) => b.version - a.version)),
  })
}

export function useLLMModels() {
  return useQuery({ queryKey: ['llm-models'], queryFn: () => api.llmModels().then((r) => r.models), staleTime: 60_000 })
}

/** Download helper. */
export function downloadText(filename: string, text: string, mime = 'application/json') {
  const blob = new Blob([text], { type: mime })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  document.body.appendChild(a)
  a.click()
  a.remove()
  setTimeout(() => URL.revokeObjectURL(url), 1000)
}

/** Fetch every row of a table (paged). */
export async function fetchAllRows(projectId: string, digest: string, max = 50_000) {
  const out: TableRow[] = []
  const page = 1000
  for (let offset = 0; offset < max; offset += page) {
    const r = await api.tableQuery({ project_id: projectId, digest, limit: page, offset })
    out.push(...r.items)
    if (r.items.length < page) break
  }
  return out
}

/** Table digest from a dataset value's rows ref. */
export function rowsDigest(val: Any): string | null {
  const r = val?.rows
  if (typeof r !== 'string') return null
  const p = parseRef(r)
  return p && p.kind === 'table' ? p.name : null
}

export function errMsg(e: unknown): string {
  return e instanceof Error ? e.message : String(e)
}
