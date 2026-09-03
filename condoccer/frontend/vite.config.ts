import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  // Relative asset URLs so the built UI works when reverse-proxied under a path
  // prefix (local-representative serves it at /condoccer/, agent-coordinator at
  // /host/<id>/condoccer/) as well as at the site root.
  base: './',
  plugins: [react()],
  server: {
    // Proxy WebSocket and API to the Go backend in dev mode.
    proxy: {
      '/ws': {
        target: 'ws://localhost:8080',
        ws: true,
      },
    },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
  },
})
