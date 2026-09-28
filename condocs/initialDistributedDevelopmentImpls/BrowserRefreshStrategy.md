agent -r "In our session /host-agent-files/agent-records/2026-09-21_20-02-28-default we discussed a strategy for refreshing the page when the frontend content changed, but there was a misunderstanding. Because the rebuild of the binaries always occurs there is no problem with what is being served. The problem is with RELOADING THE BROWSER. Let's discuss a strategy that would let the brwoser know when a hard refresh is required and/or perform this refresh automatically."y."
invoking claude in read (files read-only) mode...
● session: /host-agent-files/agent-records/2026-09-21_20-02-28-default
Good — I read the prior session (it went a genuinely different direction: "is the served bundle stale," which you're now confirming isn't the problem since rebuild+restart already guarantees fresh content is being served). I also checked how this repo's WS/reconnect/versioning actually work. Here's the reframe and the strategy it points to.

## The actual gap

The rebuild pipeline already guarantees freshness server-side:

- `repowatch.go`'s auto-rebuild runs `make deploy-dev-binaries` whenever HEAD moves.
- `ufa-loader` restarts the process on its `restartsignal` announcement, so the *new* process is what's live.
- `compute-version.sh` computes one version for the whole repo, so a frontend-only change still bumps the version every binary reports.

None of that touches an **already-open tab**. That tab has old JS sitting in the browser's memory; it doesn't re-fetch `index.html` or re-run Vite's module graph just because the server behind it got replaced. That's the entire problem: browser-side staleness detection, not serving correctness.

## The signal you already have, for free

`local-representative/frontend/src/App.tsx`'s `connect()` already does this:

```ts
ws.onclose = () => { setConnected(false); retryRef.current = setTimeout(connect, 2000) }
```

A server restart necessarily drops `/ws` (the process that held the socket is gone). So **every restart already produces a reconnect within ~2s, with zero new plumbing** — this is the trigger you were describing wanting, it's just currently wired to "resume the SPA" instead of "check whether the page under me changed."

## Proposed strategy

1. **Reuse the version string, don't invent a build id.** The previous session's proposal wanted a separate content-hash for the frontend, decoupled from the git-sha version. You don't need that: `compute-version.sh` already produces one version for the *whole repo* per build, and Vite already runs at the same build step — so bake that identical string into the frontend bundle too (`define: { __APP_VERSION__: JSON.stringify(...) }` in each `vite.config.ts`, shelling out to the same script the Go `-ldflags` build uses). Frontend and backend then always agree bit-for-bit, no second versioning scheme to keep in sync.

2. **Have the server report its running version the moment a socket opens** — not on the general `system-state` cadence, immediately on `ws.onopen`'s handshake, mirroring the `hello` message `representable` already sends on the backend-to-backend links (which carries `Mode` the same way). A tiny first message on `/ws` carrying `{version: ufaversion.Version}` before anything else.

3. **Compare, and reload only on mismatch.** In `connect()`'s `onopen`/first-message handler: if the server's reported version differs from this page's own `__APP_VERSION__`, `window.location.reload()` — no banner, no button. (Unlike the backend "update and restart" control, which is manual because restarting a process is disruptive, reloading a page isn't — so this is the one place to skip the manual affordance entirely, straight to auto-reload.) If it matches, do nothing — this is what makes it precise: a laptop sleep/wake or wifi blip reconnects to the *same* running process and shouldn't flash a reload.

4. **Duplicate this three times**, once per frontend (LR, AC, condoccer) — they already have zero shared frontend code (`selfversion.go` sets the precedent of deliberate duplication over a shared package for exactly this kind of thing), so match that rather than introducing new shared infra now.

This is strictly less machinery than the earlier proposal: no separate frontend build-id file, no piggybacking on the `system-state` poll tick, no new watcher — the WS reconnect that already fires exactly on restart *is* the trigger, and the repo-wide version that already exists *is* the comparison value. It also closes the actual thing you're describing (an open tab not knowing to reload) rather than the served-content-correctness problem that was never actually broken.

Want me to go implement this, starting with `local-representative` since its pattern gets mirrored to the other two?