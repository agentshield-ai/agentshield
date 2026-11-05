import React from 'react'
import ReactDOM from 'react-dom/client'
import { BrowserRouter } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { App } from './App'
import { installCsrfFetch } from './api/session'
import '@fontsource-variable/inter/wght.css'
import '@fontsource-variable/inter/wght-italic.css'
import './tokens.css'
import './styles.css'
import './weft.css'
import './components/ui/ui.css'
import { applyTheme, readTheme } from './components/Layout'

installCsrfFetch() // cookie sessions: send X-Weft-CSRF on same-origin writes
applyTheme(readTheme()) // light / dark / system, remembered per browser

const queryClient = new QueryClient({
  defaultOptions: { queries: { staleTime: 5_000, retry: 1, refetchOnWindowFocus: false } },
})

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <QueryClientProvider client={queryClient}>
      <BrowserRouter>
        <App />
      </BrowserRouter>
    </QueryClientProvider>
  </React.StrictMode>,
)
