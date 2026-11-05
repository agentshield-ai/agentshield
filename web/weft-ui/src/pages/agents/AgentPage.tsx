// One agent's analytics (/agents/:agentName/analytics): see AnalyticsView.
import { useParams } from 'react-router-dom'
import { AnalyticsView } from '../../components/agents/AnalyticsView'

export function AgentPage() {
  const { agentName = '' } = useParams()
  return <AnalyticsView agent={agentName} />
}
