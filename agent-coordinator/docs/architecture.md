# Agent Coordinator — Architecture

## Overview

Agent Coordinator is a hierarchically organized network coordination service. It aggregates visibility and control across multiple hosts, each running a `local-representative` instance. Conventionally organized into two roles:

- **Net coordinator** — coordinates agents across a network segment
- **Web coordinator** — provides browser-accessible UI aggregating multiple net coordinators

## Ports

| Service            | Port | Protocol |
|--------------------|------|----------|
| agent-coordinator  | 8083 | HTTP/WebSocket (browser) |
| agent-coordinator  | 8084 | TCP (local-representative connections) |
| local-representative | 8081 | HTTP/WebSocket (browser) |
| local-representative | 8082 | TCP (sub-application connections) |
| condoccer          | 8080 | HTTP/WebSocket |

## Component Relationships

```
Browser
  └── WebSocket ──> agent-coordinator (:8083)
                         │
                         │ representable TCP (:8084)
                         ├── local-representative on host-A (:8081)
                         │        └── federation-command (representable :8082)
                         └── local-representative on host-B (:8081)
                                  └── federation-command (representable :8082)
```

LR is always the TCP client; AC is always the TCP server on 8084.
One connection per LR name is tracked. LR identifies itself by the `-name` flag (default: system hostname).

## LR → AC TCP Protocol (representable)

Local-representative connects to AC using `representable.Client`. Messages:

| Direction | Type | Content |
|---|---|---|
| LR → AC | `heartbeat` | keepalive |
| LR → AC | `data` / `"services"` | `StatusMsg` — service health |
| LR → AC | `data` / `"fc-state"` | `FCStateMsg` — FC control mode |
| LR → AC | `data` / `"ridealong-state"` | `RidealongStateMsg` |
| LR → AC | `data` / `"condoc-state"` | `CondocStateMsg` |
| LR → AC | `data` / `"system-state"` | `SystemStateMsg` — LR's system tab (self + managed apps) |
| LR → AC | `data` / `"condoccer-state"` | `CondoccerStateMsg` — condoc summary + condoccer's HTTP port, relayed from a managed condoccer |
| LR → AC | `data` / `"lr-http"` | `LRHTTPMsg` — LR's dashboard HTTP port, so AC can reverse-proxy `/host/<id>/…` back to it |
| LR → AC | `data` / `"files-state"` | `FilesStateMsg` — host-cache listing for the files tab; upload is relayed back down through AC's own `POST /host/<id>/api/files` route rather than this channel (see [DistributedExchange.md](../../docs/DistributedExchange.md)) |
| LR → AC | `log` (cmd/output) | FC command echo / output forwarded upstream |
| AC → LR | `command` | plain cmd or `__ridealong:action` → forwarded to FC; `__system:launch <app>` / `__system:terminate <id>` → LR's process manager |

### condoccer in the chain

condoccer is itself a `representable.Client` of LR (`--auto-connect`, name `condoccer`,
one instance per box) whenever LR is the one launching it — LR's `condoccer`
managed-app spec always passes `--auto-connect`, so a condoccer that comes up
through the autolaunch chain is wired in without any manual step. It heartbeats
to LR's `:8082` server and pushes `data` / `"condoccer-state"` (its HTTP port +
a condoc summary). `--auto-connect` isn't mandatory, though: condoccer's own UI
has a connect/disconnect widget (bottom of the sidebar) so a condoccer started
by hand, or one whose auto-connect window gave up, can link to LR on demand and
drop the link again. Its browser UI is **forwarded, not re-implemented**: LR
reverse-proxies `/condoccer/*` → condoccer's loopback port, and AC
reverse-proxies `/host/<id>/*` → that host's LR (which in turn forwards
`/condoccer/*`). A browser on AC — including one arriving through the
Tailscale + oauth2-proxy web-exposure path — therefore drives condoccer on any
connected box over a single origin. `AC → LR` `__condoccer:action <json>` /
`__condoccer:refresh` commands are relayed to condoccer for out-of-band control.

### files tab

Each LR exposes a `/api/files` REST surface (`GET` lists, `POST` uploads a
multipart `file` into `local-representative`'s host-cache directory —
`/host-agent-files/exchange/host-cache` by default; entries are swept an hour
after upload). LR pushes the listing up as `data` / `"files-state"`, which AC
relays to browsers as `lr-files-state`.

Viewing and downloading a file's bytes (`GET
/host/<id>/api/files/<file-id>`, `?download=1` for an attachment) go through
AC's ordinary `proxyToHost` transparent reverse proxy — a read isn't the
arbitrary-filesystem-write upload is, so it isn't gated on the header below,
and AC's files tab reuses LR's raw-serving route unmodified for its own
viewer page and download button.

Upload (`POST /host/<id>/api/files`) is **relayed, not proxied**: `proxyToHost`
recognizes that path+method and hands it to a dedicated `handleFileUploadRelay`
route instead of the transparent passthrough. That route builds a fresh
outbound request to the target LR — never buffering the file to AC's own
filesystem, or looking at LR's — stamped with both `X-UFA-Proxied-By:
agent-coordinator` and `X-UFA-Relayed-Upload-By: agent-coordinator`. LR's
upload handler refuses any request carrying the first header unless the
second is also present with that exact value; a request arriving through the
transparent passthrough can never carry the second header, since its
`Director` strips any client-supplied copy before forwarding. This is Path 1
of [`docs/DistributedExchange.md`](../../docs/DistributedExchange.md), which
also covers the still-open Path 2 (LR-to-LR transfer brokered through AC).

## WebSocket Protocol (AC ↔ Browser)

### Server → Client

| Message type | Payload | Description |
|---|---|---|
| `hosts` | `{ hosts: Host[] }` | List of known hosts with connection status |
| `lr-state` | `{ host_id, active, services? }` | Service health for a host |
| `lr-fc-state` | `{ host_id, state }` | FC control mode for a host |
| `lr-fc-log` | `{ host_id, line, kind }` | FC log entry for a host |
| `lr-ridealong-state` | `{ host_id, active, ...fields }` | Ridealong state for a host |
| `lr-condoc-state` | `{ host_id, active, ...fields }` | Condoc state for a host |
| `lr-system-state` | `{ host_id, active, self, managed[] }` | Host's system tab (LR process + managed apps) |
| `lr-condoccer-state` | `{ host_id, available, root?, condocs[]? }` | Host's condoc summary; `available` gates the forwarded `/host/<id>/condoccer/` iframe |
| `lr-files-state` | `{ host_id, active, files[]? }` | Host's files tab listing; upload goes over `POST /host/<id>/api/files`, not this channel |

### Client → Server

| Message type | Payload | Description |
|---|---|---|
| `select-host` | `{ host_id }` | Request snapshot for a host |
| `lr-command` | `{ host_id, cmd }` | Run command on host's FC |
| `lr-ridealong-command` | `{ host_id, action }` | Ridealong action on host's FC |
| `lr-launch-app` | `{ host_id, name }` | Launch a managed app on the host's LR |
| `lr-terminate-app` | `{ host_id, id }` | Terminate/dismiss a managed instance on the host's LR |

## WebSocket Protocol (LR ↔ Browser) — additions

| Message type | Direction | Payload | Description |
|---|---|---|---|
| `ac-state` | LR → Browser | `{ connected, host?, port? }` | AC connection status |
| `connect-ac` | Browser → LR | `{ host, port }` | Initiate connection to AC |
| `disconnect-ac` | Browser → LR | `{}` | Close AC connection |

## Host States

- `connected` — LR is heartbeating within the stale threshold
- `disconnected` — LR dropped or was never connected

Hosts appear dynamically in the sidebar as LRs connect; they remain visible (as disconnected) after dropping.
