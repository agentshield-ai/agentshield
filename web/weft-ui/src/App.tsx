import { lazy, Suspense, useEffect, type ReactNode } from 'react'
import { Navigate, Route, Routes, useLocation, useParams } from 'react-router-dom'
import { weaveLinkTarget } from './api/weaveLinks'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { api, ApiError, setApiKey } from './api/client'
import { Loading } from './components/common'
import { PlainLayout, ProjectLayout } from './components/Layout'
import { LoginScreen } from './components/LoginScreen'

const lazyNamed = <K extends string>(loader: () => Promise<Record<K, React.ComponentType<any>>>, name: K) =>
  lazy(() => loader().then((m) => ({ default: m[name] })))

// Observe (traces, calls, threads, playground)
const ProjectsPage = lazyNamed(() => import('./pages/projects/ProjectsPage'), 'ProjectsPage')
const TracesPage = lazyNamed(() => import('./pages/traces/TracesPage'), 'TracesPage')
const CallPage = lazyNamed(() => import('./pages/traces/CallPage'), 'CallPage')
const CompareCallsPage = lazyNamed(() => import('./pages/traces/CompareCallsPage'), 'CompareCallsPage')
const ThreadsPage = lazyNamed(() => import('./pages/threads/ThreadsPage'), 'ThreadsPage')
const ThreadPage = lazyNamed(() => import('./pages/threads/ThreadPage'), 'ThreadPage')
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
const SettingsPage = lazyNamed(() => import('./pages/settings/SettingsPage'), 'SettingsPage')
const AdminPage = lazyNamed(() => import('./pages/admin/AdminPage'), 'AdminPage')
const QueuesPage = lazyNamed(() => import('./pages/queues/QueuesPage'), 'QueuesPage')
const QueueReviewPage = lazyNamed(() => import('./pages/queues/QueueReviewPage'), 'QueueReviewPage')
const AutomationsPage = lazyNamed(() => import('./pages/automations/AutomationsPage'), 'AutomationsPage')
const AgentsPage = lazyNamed(() => import('./pages/agents/AgentsPage'), 'AgentsPage')
const AgentPage = lazyNamed(() => import('./pages/agents/AgentPage'), 'AgentPage')
const ConversationPage = lazyNamed(() => import('./pages/agents/ConversationPage'), 'ConversationPage')
const DashboardsPage = lazyNamed(() => import('./pages/dashboards/DashboardsPage'), 'DashboardsPage')
const DashboardPage = lazyNamed(() => import('./pages/dashboards/DashboardPage'), 'DashboardPage')

// Links built by the official weave client and Weave's UI use a /weave/ segment
// (e.g. /e/p/weave/calls/<id>, /e/p/weave/agents/conversations/<id>). Map the
// ones Weft has an equivalent for and send the rest to Traces.
function WeaveLinkRedirect() {
  const { entity, project, '*': rest } = useParams()
  const loc = useLocation()
  return <Navigate to={weaveLinkTarget(entity ?? '', project ?? '', rest ?? '') + loc.search} replace />
}

function WeaveCallRedirect() {
  const { entity, project, callId } = useParams()
  return <Navigate to={`/${entity}/${project}/calls/${callId}`} replace />
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
          </Route>
          <Route path="/:entity/:project" element={<ProjectLayout />}>
            <Route index element={<Navigate to="traces" replace />} />
            <Route path="traces" element={<TracesPage />} />
            <Route path="calls/:callId" element={<CallPage />} />
            <Route path="compare-calls" element={<CompareCallsPage />} />
            <Route path="threads" element={<ThreadsPage />} />
            <Route path="threads/:threadId" element={<ThreadPage />} />
            <Route path="playground" element={<PlaygroundPage />} />
            <Route path="evaluations" element={<EvaluationsPage />} />
            <Route path="evaluations/compare" element={<CompareEvaluationsPage />} />
            <Route path="evaluations/new" element={<NewEvaluationPage />} />
            <Route path="datasets" element={<ObjectsPage kind="datasets" />} />
            <Route path="models" element={<ObjectsPage kind="models" />} />
            <Route path="prompts" element={<ObjectsPage kind="prompts" />} />
            <Route path="scorers" element={<ObjectsPage kind="scorers" />} />
            <Route path="ops" element={<ObjectsPage kind="ops" />} />
            <Route path="objects" element={<ObjectsPage kind="all" />} />
            <Route path="objects/:objectId" element={<ObjectVersionsPage />} />
            <Route path="objects/:objectId/versions/:digest" element={<ObjectVersionPage />} />
            <Route path="objects/:objectId/compare" element={<ObjectDiffPage />} />
            <Route path="leaderboards" element={<LeaderboardsPage />} />
            <Route path="leaderboards/:objectId" element={<LeaderboardPage />} />
            <Route path="monitors" element={<MonitorsPage />} />
            <Route path="monitors/:objectId" element={<MonitorPage />} />
            <Route path="settings" element={<SettingsPage />} />
            <Route path="queues" element={<QueuesPage />} />
            <Route path="queues/:queueId" element={<QueueReviewPage />} />
            <Route path="automations" element={<AutomationsPage />} />
            <Route path="agents" element={<AgentsPage />} />
            <Route path="agents/:agentName" element={<AgentPage />} />
            <Route path="conversations/:conversationId" element={<ConversationPage />} />
            <Route path="dashboards" element={<DashboardsPage />} />
            <Route path="dashboards/:objectId" element={<DashboardPage />} />
            {/* Links printed by the official weave client */}
            <Route path="weave" element={<Navigate to="../traces" relative="path" replace />} />
            <Route path="weave/*" element={<WeaveLinkRedirect />} />
            <Route path="r/call/:callId" element={<WeaveCallRedirect />} />
          </Route>
          <Route path="*" element={<div className="empty">Page not found</div>} />
        </Routes>
      </Suspense>
    </AuthGate>
  )
}
