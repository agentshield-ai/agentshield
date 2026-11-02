import { useParams } from 'react-router-dom'

/** Returns "entity/project" from the current route. */
export function useProjectId(): string {
  const { entity, project } = useParams()
  return `${entity ?? 'default'}/${project ?? 'default'}`
}
