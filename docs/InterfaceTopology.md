# Interface Topology

Which of the three binaries talk over which transport, and what they say to
each other. Written for InitialDistributedSessions Step 1 Rev B, prompted by
a question about whether `local-representative`'s (LR) `GET /api/hosts` call
belonged on the TCP leg instead — see
`docs/DistributedSessionsBrainstorm.md`'s "Step 1 Rev B" entry for that
discussion; this doc is the map that answer refers to.

## The two transports

- **representable (TCP)** — a small, custom control-plane protocol. Every
  connection is long-lived and always dialed by the "lower" side of a pair
  (LR dials `agent-coordinator` (AC); `federation-command` (FC) dials LR).
  It carries three frame kinds, all fire-and-forget, none with request/reply
  semantics: `data` (a named JSON blob, e.g. `"fc-state"`), `command` (a bare
  string, e.g. `__ridealong:accept`), and `log`. There is no way to ask a
  question over representable and get an answer back on the same call — a
  response, if any, arrives later as its own independent `command` or `data`
  frame (see `"__session-sync-done:list"` below).
- **HTTP** — used wherever a caller needs a synchronous answer with a body:
  reading a file's bytes, listing sessions, discovering peers, a browser's
  dashboard. AC and LR each run their own HTTP server; a caller on the other
  side of a representable connection is free to also be an HTTP *client* of
  the peer it's dialed into (LR is both a representable client of AC and an
  HTTP client of AC's `/host/<id>/*` proxy — this is not a layering
  violation, it's two independent surfaces to the same peer).

FC never runs an HTTP server and never makes an HTTP call. Its entire world
is representable, dialed to LR. This is unchanged by this revision and isn't
in question — the confirmed decision below only concerns the LR↔AC leg.

## Diagram

```
 Browser                                                    Browser
    │  HTTP (dashboard, /api/hosts,                            │  HTTP (dashboard,
    │  GET/DELETE /api/files*, /host/<id>/*)                   │  /api/files*,
    │  WebSocket /ws                                           │  /condoccer/*)
    │                                                           │  WebSocket /ws
    ▼                                                           ▼
┌─────────────────────┐   representable (TCP)   ┌─────────────────────────┐
│   agent-coordinator   │◄────────────────────────│   local-representative   │
│   (AC)                │────────────────────────►│   (LR)                   │
│                       │  data: services,        │                          │
│  HTTP server :8083    │  fc-state, files-state,  │  HTTP server :8082       │
│  repr TCP    :8084    │  system-state, ...       │  (LR dials AC as a       │
│                       │  command: __system:*,    │   representable client, │
│                       │  __ridealong:*           │   host = "-ac-host")    │
└─────────┬─────────────┘                          └───────────┬──────────────┘
          │  HTTP GET /api/hosts, /host/<id>/api/sessions       │
          │  (LR → AC → peer LR, discovery + session-list pull) │
          └──────────────────────────────────────────────────────┘
                          (peer-to-peer via AC's proxy; AC brokers,
                           never buffers session/file content itself)

┌─────────────────────────┐  representable (TCP)   ┌──────────────────────────┐
│   local-representative   │◄───────────────────────│   federation-command      │
│   (LR)                   │────────────────────────►│   (FC)                   │
│                          │  data: session-sync-    │                          │
│  repr TCP :8082          │  request, ridealong-    │  no HTTP client/server   │
│  (LR is the server;      │  state, condoc-state    │  at all — everything     │
│   FC dials in)           │  command: __session-    │  FC does crosses this    │
│                          │  sync-done:list, remote  │  one TCP connection      │
│                          │  commands, __ridealong:* │                          │
└─────────┬────────────────┘                         └──────────────────────────┘
          │  HTTP GET (own /api/sessions, and via AC's proxy to peers' —
          │  see the AC↔LR block above; this is the *same* LR process acting
          │  as the HTTP client shown there)
          ▼
   other participants' session.yaml
```

## Who calls what

### FC ↔ LR — representable (TCP) only

FC dials LR (`-lr-addr`, default port `8082`); LR is always the server on
this leg. Nothing here is HTTP.

| Direction | Frame | Purpose |
|---|---|---|
| FC → LR | `data: "session-sync-request"` | `{kind: "append"\|"list", session_id}` — sent before an append-to-session command, or before a sessions-listing operation. Fire-and-forget from FC's side. |
| FC → LR | `data: "ridealong-state"`, `"condoc-state"` | Mode pushes so LR's dashboard reflects what FC is doing. |
| FC → LR | `state: "remote-control"\|"local-control"` | Which side currently owns keystrokes. |
| LR → FC | `command: "__session-sync-done:list"` | Wakes FC's blocked `awaitDistributedSessionSync` once a `"list"` pull finishes (successfully or not). |
| LR → FC | `command` (arbitrary), `"__ridealong:<action>"`, `"__system:*"` | Remote-control keystrokes and app-launch/terminate relayed from AC's browser clients through LR (see the AC → LR row below) — LR is a pass-through for these, not their origin. |

### LR ↔ AC — both transports, by design

LR dials AC (`-ac-host`/`-ac-port`, default port `8084`) for the
representable leg; AC's HTTP server (`-port`, default `8083`) is separate and
reachable by anyone, including LR itself as a client.

**representable (TCP), LR → AC — state pushes, no reply expected:**
`"services"`, `"fc-state"`, `"ridealong-state"`, `"condoc-state"`,
`"system-state"`, `"lr-http"`, `"files-state"`, `"condoccer-state"`.

**representable (TCP), AC → LR — commands relayed from a browser on AC's dashboard:**
arbitrary `command` strings, `"__ridealong:<action>"`, `"__system:launch <name>"`,
`"__system:terminate <id>"`.

**HTTP, LR → AC — synchronous reads, used for distributed-session sync:**

| Call | Purpose |
|---|---|
| `GET /api/hosts` | Discover connected participants (mirrors AC's own `"hosts"` WebSocket broadcast). *This is the call Rev B asked about confirming — see below.* |
| `GET /host/<peer>/api/sessions` | Through AC's transparent proxy, pull one peer's un-archived `session.yaml` listing. |

**HTTP, browser/AC → LR:**

| Call | Purpose |
|---|---|
| `GET /api/sessions` | Serves this host's session listing — the endpoint the row above pulls from a peer. Ungated on `X-UFA-Proxied-By`, a read. |
| `GET /api/files`, `GET /api/files/<id>`, `DELETE /api/files/<id>`, `POST /api/files/<id>/hold\|persist` | Host-cache file listing/content/actions (`docs/DistributedExchange.md`). |
| `POST /api/files` | Upload — refused when it arrives through AC's transparent proxy, *unless* relayed via AC's own `handleFileUploadRelay` (Path 1, dedicated route, not the transparent passthrough). |
| `/host/<id>/*` (general) | AC's transparent reverse proxy onto LR's dashboard/API, stamped `X-UFA-Proxied-By: agent-coordinator`. |

### Why `GET /api/hosts` is not a mistake

FC↔LR never touches HTTP, so the prompt's concern doesn't apply to that leg
at all. LR↔AC has carried both transports since `docs/DistributedExchange.md`
Path 1 landed — representable there is scoped to small, replyless
control-plane pushes and relayed commands; anything needing a synchronous
answer with a body (file bytes, a session list, a host list) is HTTP, because
representable has no request/reply frame to build one on. `GET /api/hosts`
is the same shape as the already-established `GET /api/files/<id>` and the
new `GET /api/sessions`: a plain read, safe to leave on the HTTP surface both
a browser and a peer LR already share. Giving representable request/reply
semantics just for this would be a real, separate protocol change — out of
scope for a one-line fix, and not obviously worth it while HTTP already
covers every "list/get" case in this codebase.

## Open questions for later

- Should representable ever gain real request/reply framing? Only if a
  future need can't be expressed as a fire-and-forget push plus a later
  independent frame (as `session-sync-request` / `__session-sync-done:list`
  already does) — see `docs/DistributedExchange.md`'s closing question on
  this same point.
- The HTTP calls LR makes to AC (`sessionSyncHTTPTimeout`, 2s) are the only
  place a slow/unreachable participant can stall LR's read loop if ever
  called from the wrong goroutine — `handleSessionSyncRequest` already runs
  the `"list"` pull off the representable read loop for this reason; keep
  that invariant if this surface grows.
