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
    // The dev server's origin is not Weft's: Weft refuses state-changing
    // requests from other origins (docs/weft/API.md), so the proxy drops the
    // browser's Origin header, as a same-origin deployment would not need.
    proxy: Object.fromEntries(
      apiPrefixes.map((p) => [
        p,
        { target, changeOrigin: true, configure: (proxy) => proxy.on('proxyReq', (req) => req.removeHeader('origin')) },
      ]),
    ),
  },
  test: { environment: 'node', include: ['src/**/*.test.{ts,tsx}'] },
})
