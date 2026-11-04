import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// The API lives at the server root (/api/v1/, /otel/, /agents/, ...).
// In dev, proxy them to a locally running `weft` server.
const target = process.env.WEFT_SERVER_URL ?? 'http://localhost:6345'
const apiPrefixes = [
  '/api/', '/llm/', '/secrets/', '/project/', '/projects', '/health', '/auth/', '/otel/', '/agents/',
  '/annotation_queues', '/automations/', '/api_keys/', '/users/', '/teams/', '/audit/', '/reference_buckets/',
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
