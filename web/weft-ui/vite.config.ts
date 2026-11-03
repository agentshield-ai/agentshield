import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// The API lives at the server root (/call/, /calls/, /api/v1/, /otel/, ...).
// In dev, proxy them to a locally running `weft` server.
const target = process.env.WEFT_SERVER_URL ?? 'http://localhost:6345'
const apiPrefixes = [
  '/call/', '/calls/', '/obj/', '/objs/', '/table/', '/refs/', '/file/', '/feedback/',
  '/cost/', '/completions/', '/llm/', '/secrets/', '/evaluate_model', '/evaluation_status',
  '/evaluations/', '/leaderboard/', '/threads/', '/project/', '/projects', '/server_info',
  '/health', '/auth/', '/otel/', '/agents/', '/image/',
  '/annotation_queues', '/api/', '/automations/', '/embeddings/',
  '/api_keys/', '/users/', '/teams/', '/audit/', '/dataset_sources/', '/files/', '/reference_buckets/', '/trace/', '/v2/', '^/(tags|aliases)(\\?.*)?$',
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
  test: { environment: 'node', include: ['src/**/*.test.{ts,tsx}'] },
})
