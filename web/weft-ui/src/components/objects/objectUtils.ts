// Shared helpers for object (asset) pages: class detection, value builders
// for the object conventions in docs/weft/API.md, and small query hooks.
import { useQuery } from '@tanstack/react-query'
import { api } from '../../api/client'
import { objectRef, parseRef } from '../../api/refs'
import type { Any, ObjSchema, ObjsFilter } from '../../api/types'

export type ObjectKind = 'datasets' | 'models' | 'prompts' | 'scorers' | 'ops' | 'all'

export const KIND_META: Record<ObjectKind, { title: string; singular: string; filter: ObjsFilter; blurb: string }> = {
  datasets: {
    title: 'Datasets',
    singular: 'dataset',
    filter: { base_object_classes: ['Dataset'] },
    blurb: 'Versioned collections of examples used for evaluations.',
  },
  models: {
    title: 'Models',
    singular: 'model',
    filter: { base_object_classes: ['Model', 'LLMStructuredCompletionModel'] },
    blurb: 'Versioned model configurations. LLM completion models can be run by the server.',
  },
  prompts: {
    title: 'Prompts',
    singular: 'prompt',
    filter: { base_object_classes: ['Prompt', 'StringPrompt', 'MessagesPrompt'] },
    blurb: 'Versioned prompt templates with {placeholders}.',
  },
  scorers: {
    title: 'Scorers',
    singular: 'scorer',
    filter: { base_object_classes: ['Scorer', 'LLMAsAJudgeScorer'] },
    blurb: 'Functions and LLM judges that grade model outputs.',
  },
  ops: { title: 'Functions', singular: 'function', filter: { is_op: true }, blurb: 'Traced functions (ops), versioned by their source code.' },
  all: { title: 'All objects', singular: 'object', filter: { is_op: false }, blurb: 'Every versioned object in this project.' },
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
export function objClass(o: ObjSchema): string {
  if (o.kind === 'op') return 'Op'
  return o.leaf_object_class ?? valClass(o.val) ?? o.base_object_class ?? (Array.isArray(o.val) ? 'List' : typeof o.val === 'object' ? 'Object' : typeof o.val)
}

function classes(o: ObjSchema): string[] {
  const out = [o.base_object_class, o.leaf_object_class, valClass(o.val)]
  if (o.val && typeof o.val === 'object' && Array.isArray(o.val._bases)) out.push(...o.val._bases)
  return out.filter(Boolean) as string[]
}

export const hasClass = (o: ObjSchema, ...names: string[]) => classes(o).some((c) => names.includes(c))
export const isDataset = (o: ObjSchema) => hasClass(o, 'Dataset')
export const isPrompt = (o: ObjSchema) => hasClass(o, 'Prompt', 'StringPrompt', 'MessagesPrompt')
export const isEvaluation = (o: ObjSchema) => hasClass(o, 'Evaluation')
export const isScorer = (o: ObjSchema) => hasClass(o, 'Scorer', 'LLMAsAJudgeScorer')
export const isLLMModel = (o: ObjSchema) => hasClass(o, 'LLMStructuredCompletionModel')
export const isModel = (o: ObjSchema) => hasClass(o, 'Model', 'LLMStructuredCompletionModel')
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
export const isServerScorer = (o: ObjSchema) => isServerScorerClass(valClass(o.val))

/** The object list page an object belongs to. */
export function kindOf(o: ObjSchema): ObjectKind {
  if (o.kind === 'op') return 'ops'
  if (isDataset(o)) return 'datasets'
  if (isPrompt(o)) return 'prompts'
  if (isScorer(o)) return 'scorers'
  if (isModel(o)) return 'models'
  return 'all'
}

export const refOf = (o: ObjSchema) => objectRef(o.project_id, o.object_id, o.digest, o.kind === 'op' ? 'op' : 'object')

/** Ref with a readable version, e.g. for SDK snippets: name:v3 */
export const versionLabel = (o: Pick<ObjSchema, 'version_index'>) => `v${o.version_index}`

export function refName(ref: string | null | undefined): string {
  if (!ref) return '—'
  const p = parseRef(ref)
  return p ? p.name : ref
}

/** Valid object ids: letters, digits, -, _, . */
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

export function useObjects(projectId: string, filter: ObjsFilter, opts: { enabled?: boolean; metadataOnly?: boolean } = {}) {
  return useQuery({
    queryKey: ['objs', projectId, filter, !!opts.metadataOnly],
    queryFn: () =>
      api
        .objsQuery({
          project_id: projectId,
          filter: { latest_only: true, ...filter },
          sort_by: [{ field: 'created_at', direction: 'desc' }],
          limit: 1000,
          metadata_only: opts.metadataOnly,
        })
        .then((r) => r.objs),
    enabled: opts.enabled ?? true,
  })
}

export function useObjectVersions(projectId: string, objectId: string, metadataOnly = true) {
  return useQuery({
    queryKey: ['obj-versions', projectId, objectId, metadataOnly],
    queryFn: () =>
      api
        .objsQuery({
          project_id: projectId,
          filter: { object_ids: [objectId], latest_only: false },
          sort_by: [{ field: 'created_at', direction: 'desc' }],
          limit: 1000,
          metadata_only: metadataOnly,
        })
        .then((r) => r.objs.slice().sort((a, b) => b.version_index - a.version_index)),
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
  const out: { digest: string; val: Any; original_index: number }[] = []
  const page = 1000
  for (let offset = 0; offset < max; offset += page) {
    const r = await api.tableQuery({ project_id: projectId, digest, limit: page, offset })
    out.push(...r.rows)
    if (r.rows.length < page) break
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
