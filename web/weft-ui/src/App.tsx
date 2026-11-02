import { lazy, Suspense, useState, type ReactNode } from 'react'
import { Navigate, Route, Routes } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { api, ApiError, setApiKey } from './api/client'
import { Loading } from './components/common'
import { Logo, PlainLayout, ProjectLayout } from './components/Layout'

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

function AuthGate({ children }: { children: ReactNode }) {
  const qc = useQueryClient()
  const check = useQuery({ queryKey: ['auth-check'], queryFn: api.authCheck, retry: false })
  const [key, setKey] = useState('')
  if (check.isLoading) return <Loading />
  if (check.error instanceof ApiError && check.error.status === 401) {
    return (
      <div className="empty" style={{ maxWidth: 420, margin: '10vh auto' }}>
        <div className="col" style={{ alignItems: 'center', gap: 14 }}>
          <Logo />
          <h2>This Weft server needs an API key</h2>
          <form
            className="row"
            style={{ width: '100%' }}
            onSubmit={(e) => {
              e.preventDefault()
              setApiKey(key.trim())
              void qc.invalidateQueries()
            }}
          >
            <input className="input grow" type="password" placeholder="API key" value={key} onChange={(e) => setKey(e.target.value)} autoFocus />
            <button className="btn primary" type="submit">
              Continue
            </button>
          </form>
        </div>
      </div>
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
          </Route>
          <Route path="*" element={<div className="empty">Page not found</div>} />
        </Routes>
      </Suspense>
    </AuthGate>
  )
}
