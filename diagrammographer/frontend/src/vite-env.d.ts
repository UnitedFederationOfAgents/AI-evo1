/// <reference types="vite/client" />

// __APP_VERSION__ is baked in at build time by vite.config.ts's `define`
// (see scripts/compute-version.sh) -- App.tsx compares it against the
// server's own reported version on every WebSocket connect so an
// already-open tab notices a rebuild behind it and reloads itself; see
// condocs/initialDistributedDevelopmentImpls/BrowserRefreshStrategy.md.
declare const __APP_VERSION__: string
