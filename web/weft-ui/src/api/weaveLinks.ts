const WEAVE_SECTIONS = ['calls', 'objects', 'evaluations', 'leaderboards', 'threads', 'agents', 'datasets', 'models', 'prompts', 'scorers', 'ops', 'monitors', 'playground', 'traces']

export function weaveLinkTarget(entity: string, project: string, rest: string): string {
  const base = `/${entity}/${project}`
  const path = rest.replace(/^\/+|\/+$/g, '')
  const conv = /^agents\/conversations\/(.+)$/.exec(path)
  if (conv) return `${base}/conversations/${conv[1]}`
  const head = path.split('/')[0]
  return WEAVE_SECTIONS.includes(head) ? `${base}/${path}` : `${base}/traces`
}
