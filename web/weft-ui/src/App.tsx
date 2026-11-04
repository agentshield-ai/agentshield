import { lazy, Suspense, useEffect, type ReactNode } from 'react'
import { Navigate, Route, Routes, useLocation, useParams } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { api, ApiError, setApiKey } from './api/client'
import { Loading } from './components/common'
import { PlainLayout, ProjectLayout } from './components/Layout'
import { LoginScreen } from './components/LoginScreen'

const lazyNamed = <K extends string>(loader: () => Promise<Record<K, React.ComponentType<any>>>, name: K) =>
  lazy(() => loader().then((m) => ({ default: m[name] })))

// Observe (traces, spans, span sessions, playground)
const ProjectsPage = lazyNamed(() => import('./pages/projects/ProjectsPage'), 'ProjectsPage')
const TracesPage = lazyNamed(() => import('./pages/traces/TracesPage'), 'TracesPage')
const SpanPage = lazyNamed(() => import('./pages/traces/SpanPage'), 'SpanPage')
const CompareSpansPage = lazyNamed(() => import('./pages/traces/CompareSpansPage'), 'CompareSpansPage')
const SpanSessionsPage = lazyNamed(() => import('./pages/traces/SpanSessionsPage'), 'SpanSessionsPage')
const SpanSessionPage = lazyNamed(() => import('./pages/traces/SpanSessionPage'), 'SpanSessionPage')
const PlaygroundPage = lazyNamed(() => import('./pages/playground/PlaygroundPage'), 'PlaygroundPage')
// Assets & evaluation
const ObjectsPage = lazyNamed(() => import('./pages/objects/ObjectsPage'), 'ObjectsPage')
const ObjectVersionsPage = lazyNamed(() => import('./pages/objects/ObjectVersionsPage'), 'ObjectVersionsPage')
const ObjectVersionPage = lazyNamed(() => import('./pages/objects/ObjectVersionPage'), 'ObjectVersionPage')
const ObjectDiffPage = lazyNamed(() => import('./pages/objects/ObjectDiffPage'), 'ObjectDiffPage')
const EvaluationsPage = lazyNamed(() => import('./pages/evaluations/EvaluationsPage'), 'EvaluationsPage')
const CompareEvaluationsPage = lazyNamed(() => import('./pages/evaluations/CompareEvaluationsPage'), 'CompareEvaluationsPage')
const NewEvaluationPage = lazyNamed(() => import('./pages/evaluations/NewEvaluationPage'), 'NewEvaluationPage')
const LeaderboardsPage = lazyNamed(() => import('./pages/leaderboards/LeaderboardsPage'), 'LeaderboardsPage')
const LeaderboardPage = lazyNamed(() => import('./pages/leaderboards/LeaderboardPage'), 'LeaderboardPage')
const MonitorsPage = lazyNamed(() => import('./pages/monitors/MonitorsPage'), 'MonitorsPage')
const MonitorPage = lazyNamed(() => import('./pages/monitors/MonitorPage'), 'MonitorPage')
const HelpPage = lazyNamed(() => import('./pages/help/HelpPage'), 'HelpPage')
const SettingsPage = lazyNamed(() => import('./pages/settings/SettingsPage'), 'SettingsPage')
const AdminPage = lazyNamed(() => import('./pages/admin/AdminPage'), 'AdminPage')
const QueuesPage = lazyNamed(() => import('./pages/queues/QueuesPage'), 'QueuesPage')
const QueueReviewPage = lazyNamed(() => import('./pages/queues/QueueReviewPage'), 'QueueReviewPage')
const AutomationsPage = lazyNamed(() => import('./pages/automations/AutomationsPage'), 'AutomationsPage')
const AgentsPage = lazyNamed(() => import('./pages/agents/AgentsPage'), 'AgentsPage')
const AgentPage = lazyNamed(() => import('./pages/agents/AgentPage'), 'AgentPage')
const ConversationPage = lazyNamed(() => import('./pages/agents/ConversationPage'), 'ConversationPage')
const SessionsPage = lazyNamed(() => import('./pages/sessions/SessionsPage'), 'SessionsPage')
const SessionPage = lazyNamed(() => import('./pages/sessions/SessionPage'), 'SessionPage')
const AgentsListPage = lazyNamed(() => import('./pages/agents/AgentsListPage'), 'AgentsListPage')
const AgentDetailPage = lazyNamed(() => import('./pages/agents/AgentDetailPage'), 'AgentDetailPage')
const TeamsPage = lazyNamed(() => import('./pages/teams/TeamsPage'), 'TeamsPage')
const TeamPage = lazyNamed(() => import('./pages/teams/TeamPage'), 'TeamPage')
const SecurityPage = lazyNamed(() => import('./pages/security/SecurityPage'), 'SecurityPage')
const ScoresPage = lazyNamed(() => import('./pages/scores/ScoresPage'), 'ScoresPage')
const DashboardsPage = lazyNamed(() => import('./pages/dashboards/DashboardsPage'), 'DashboardsPage')
const DashboardPage = lazyNamed(() => import('./pages/dashboards/DashboardPage'), 'DashboardPage')

/**
 * Renamed paths keep working: /queues → /review-queues, /ops → /functions,
 * and the span pages' earlier paths (links printed by older clients).
 */
function Renamed({ to }: { to: string }) {
  const { entity, project, id } = useParams()
  const loc = useLocation()
  return <Navigate to={`/${entity}/${project}/${to}${id ? `/${encodeURIComponent(id)}` : ''}${loc.search}`} replace />
}

function AuthGate({ children }: { children: ReactNode }) {
  const qc = useQueryClient()
  const check = useQuery({ queryKey: ['auth-check'], queryFn: api.authCheck, retry: false })
  const authed = check.isSuccess
  useEffect(() => {
    if (!authed) return
    // Remember who we are so feedback panels can tell our reactions from others'.
    api
      .whoami()
      .then((w) => {
        try {
          localStorage.setItem('weft.userId', w.user_id)
        } catch {
          /* storage unavailable */
        }
      })
      .catch(() => undefined)
  }, [authed])
  if (check.isLoading) return <Loading />
  if (check.error instanceof ApiError && check.error.status === 401) {
    return (
      <LoginScreen
        onKey={(key) => {
          setApiKey(key)
          void qc.invalidateQueries()
        }}
      />
    )
  }
  return <>{children}</>
}

export function App() {
  return (
    <AuthGate>
      <Suspense fallback={<Loading />}>
        <Routes>
          <Route element={<PlainLayout />}>
            <Route path="/" element={<ProjectsPage />} />
            <Route path="/admin" element={<AdminPage />} />
            <Route path="/help" element={<HelpPage />} />
            <Route path="/help/:doc" element={<HelpPage />} />
          </Route>
          <Route path="/:entity/:project" element={<ProjectLayout />}>
            <Route index element={<Navigate to="sessions" replace />} />
            <Route path="sessions" element={<SessionsPage />} />
            <Route path="sessions/:sessionId" element={<SessionPage />} />
            <Route path="teams" element={<TeamsPage />} />
            <Route path="teams/:teamId" element={<TeamPage />} />
            <Route path="security" element={<SecurityPage />} />
            <Route path="scores" element={<ScoresPage />} />
            <Route path="traces" element={<TracesPage />} />
            <Route path="traces/sessions" element={<SpanSessionsPage />} />
            <Route path="traces/sessions/:sessionId" element={<SpanSessionPage />} />
            <Route path="spans/:spanId" element={<SpanPage />} />
            <Route path="compare-spans" element={<CompareSpansPage />} />
            <Route path="calls/:id" element={<Renamed to="spans" />} />
            <Route path="compare-calls" element={<Renamed to="compare-spans" />} />
            <Route path="threads" element={<Renamed to="traces/sessions" />} />
            <Route path="threads/:id" element={<Renamed to="traces/sessions" />} />
            <Route path="playground" element={<PlaygroundPage />} />
            <Route path="evaluations" element={<EvaluationsPage />} />
            <Route path="evaluations/compare" element={<CompareEvaluationsPage />} />
            <Route path="evaluations/new" element={<NewEvaluationPage />} />
            <Route path="datasets" element={<ObjectsPage kind="datasets" />} />
            <Route path="models" element={<ObjectsPage kind="models" />} />
            <Route path="prompts" element={<ObjectsPage kind="prompts" />} />
            <Route path="scorers" element={<ObjectsPage kind="scorers" />} />
            <Route path="functions" element={<ObjectsPage kind="ops" />} />
            <Route path="ops" element={<Renamed to="functions" />} />
            <Route path="objects" element={<ObjectsPage kind="all" />} />
            <Route path="objects/:objectId" element={<ObjectVersionsPage />} />
            <Route path="objects/:objectId/versions/:digest" element={<ObjectVersionPage />} />
            <Route path="objects/:objectId/compare" element={<ObjectDiffPage />} />
            <Route path="leaderboards" element={<LeaderboardsPage />} />
            <Route path="leaderboards/:objectId" element={<LeaderboardPage />} />
            <Route path="monitors" element={<MonitorsPage />} />
            <Route path="monitors/:objectId" element={<MonitorPage />} />
            <Route path="settings" element={<SettingsPage />} />
            <Route path="review-queues" element={<QueuesPage />} />
            <Route path="review-queues/:queueId" element={<QueueReviewPage />} />
            <Route path="queues" element={<Renamed to="review-queues" />} />
            <Route path="queues/:id" element={<Renamed to="review-queues" />} />
            <Route path="automations" element={<AutomationsPage />} />
            <Route path="agents" element={<AgentsListPage />} />
            <Route path="agents/:agentName" element={<AgentDetailPage />} />
            <Route path="agents/:agentName/analytics" element={<AgentPage />} />
            <Route path="agent-analytics" element={<AgentsPage />} />
            <Route path="conversations/:conversationId" element={<ConversationPage />} />
            <Route path="dashboards" element={<DashboardsPage />} />
            <Route path="dashboards/:objectId" element={<DashboardPage />} />
          </Route>
          <Route
            path="*"
            element={
              <div className="empty">
                <p>This page does not exist.</p>
                <a href="/">Go to your projects</a>
              </div>
            }
          />
        </Routes>
      </Suspense>
    </AuthGate>
  )
}
