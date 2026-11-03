import type { FullConfig } from '@playwright/test'
import { seed } from './seed'

// Seeds fresh projects for this run; tests read their names from the environment.
export default async function globalSetup(config: FullConfig) {
  const base = config.projects[0].use.baseURL ?? 'http://127.0.0.1:5210'
  const stamp = new Date().toISOString().replace(/\D/g, '').slice(4, 14)
  const p = await seed(base, stamp)
  process.env.WEFT_E2E_PROJECT = p.project
  process.env.WEFT_E2E_OLDER = p.older
  process.env.WEFT_E2E_EMPTY = p.empty
}
