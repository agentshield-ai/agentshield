import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// API endpoints live at the server root (Weave-style trace server paths).
// In dev, proxy them to a locally running `weft` server.
const target = process.env.WEFT_SERVER_URL ?? 'http://localhost:6345'
const apiPrefixes = [
  '/call/', '/calls/', '/obj/', '/objs/', '/table/', '/refs/', '/file/', '/feedback/',
  '/cost/', '/completions/', '/llm/', '/secrets/', '/evaluate_model', '/evaluation_status',
  '/evaluations/', '/leaderboard/', '/threads/', '/project/', '/projects', '/server_info',
  '/health', '/auth/', '/otel/', '/agents/', '/image/',
  '/annotation_queues', '/automations/', '/embeddings/',
  '/api_keys/', '/users/', '/teams/', '/audit/', '/dataset_sources/', '/files/', '/trace/', '/v2/', '^/(tags|aliases)(\\?.*)?$',
]

export default defineConfig({
  plugins: [react()],
  build: {
    outDir: '../../internal/weft/webui/static',
    emptyOutDir: true,
    chunkSizeWarningLimit: 2000,
  },
  server: {
    port: 5173,
    proxy: Object.fromEntries(apiPrefixes.map((p) => [p, { target, changeOrigin: true }])),
  },
  test: { environment: 'node' },
})
