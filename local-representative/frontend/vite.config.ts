import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import { execFileSync } from 'node:child_process'

// The same repo-wide version string every Go binary gets baked in via
// -ldflags (see scripts/compute-version.sh) -- baked into this frontend
// bundle too so an already-open tab can tell the server behind it was
// rebuilt out from under it (see App.tsx's connect() and
// condocs/initialDistributedDevelopmentImpls/BrowserRefreshStrategy.md).
const appVersion = execFileSync('bash', ['../../scripts/compute-version.sh']).toString().trim()

export default defineConfig({
  plugins: [react()],
  define: {
    __APP_VERSION__: JSON.stringify(appVersion),
  },
  server: {
    // Proxy WebSocket to the Go backend in dev mode.
    proxy: {
      '/ws': {
        target: 'ws://localhost:8081',
        ws: true,
      },
    },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
  },
})
