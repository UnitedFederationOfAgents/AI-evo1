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
| LR → AC | `data` / `"system-state"` | `SystemStateMsg` — LR's system tab (self + managed apps); `ProcInfo` carries each process's `version`, `update_available` (self and managed both drive a restart control; self's is additionally gated on `loader_managed`) |
| LR → AC | `data` / `"repo-state"` | `RepoStateMsg` — LR's dev-repo watcher (`--dev-repo`, see [DevMode.md](../../docs/DevMode.md)); `watched: false` when that LR wasn't launched with `--dev-repo` |
| LR → AC | `data` / `"condoccer-state"` | `CondoccerStateMsg` — condoc summary + condoccer's HTTP port, relayed from a managed condoccer |
| LR → AC | `data` / `"lr-http"` | `LRHTTPMsg` — LR's dashboard HTTP port, so AC can reverse-proxy `/host/<id>/…` back to it |
| LR → AC | `data` / `"files-state"` | `FilesStateMsg` — host-cache listing for the files tab; upload is relayed back down through AC's own `POST /host/<id>/api/files` route rather than this channel (see [DistributedExchange.md](../../docs/DistributedExchange.md)) |
| LR → AC | `log` (cmd/output) | FC command echo / output forwarded upstream |
| AC → LR | `command` | plain cmd or `__ridealong:action` → forwarded to FC; `__system:launch <app>` / `__system:terminate <id>` / `__system:restart-managed <id>` → LR's process manager; `__system:restart` / `__system:rebuild` / `__system:auto-rebuild <on\|off>` → LR's own restart control and dev-repo watcher (see [DevMode.md](../../docs/DevMode.md)) |

### condoccer in the chain

condoccer is itself a `representable.Client` of LR (`--auto-connect`, name `condoccer`,
one instance per box) whenever LR is the one launching it — LR's `condoccer`
managed-app spec always passes `--auto-connect`, so a condoccer that comes up
through the autolaunch chain is wired in without any manual step. It heartbeats
to LR's `:8082` server and pushes `data` / `"condoccer-state"` (its HTTP port +
a condoc summary). `--auto-connect` isn't mandatory, though: condoccer's own UI
has a connect/disconnect widget (bottom of the sidebar), plus its own
first-class **auto-connect** toggle alongside it, so a condoccer started by
hand can link to LR on demand and drop the link again — the toggle stays
armed across a successful connection and resumes the retry cycle on its own
after an unintentional drop, but an explicit disconnect turns it off (see
`condocs/initialDistributedDevelopmentImpls/Step3Prompt.md` Revision I). Its
browser UI is **forwarded, not re-implemented**: LR
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
| `lr-repo-state` | `{ host_id, watched, dirty, rebuild_ready, building, auto_rebuild, ...fields }` | Host's dev-repo watcher state (`--dev-repo`); `watched: false` when not watching a repo or not connected |
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
| `lr-restart-managed-app` | `{ host_id, id }` | Restart one managed instance on the host's LR: terminate it, then launch a fresh instance of the same app (see Step4Prompt.md Revision H) |
| `lr-restart-app` | `{ host_id }` | Restart the host's LR itself (only takes effect if it's loader-managed) |
| `lr-rebuild-app` | `{ host_id }` | Run `make deploy-dev-binaries` on the host's watched dev-repo |
| `lr-set-auto-rebuild` | `{ host_id, enabled }` | Toggle the host's dev-repo watcher auto-rebuild flag |
| `ac-restart-app` | `{}` | Restart agent-coordinator itself (only takes effect if it's loader-managed) — no host to target, unlike `lr-restart-app` |

## WebSocket Protocol (LR ↔ Browser) — additions

| Message type | Direction | Payload | Description |
|---|---|---|---|
| `ac-state` | LR → Browser | `{ connected, host?, port?, connecting?, auto_connect? }` | AC connection status; `auto_connect` is the persistent toggle (Revision I), independent of `connecting`/`connected` |
| `connect-ac` | Browser → LR | `{ host, port }` | Initiate connection to AC |
| `disconnect-ac` | Browser → LR | `{}` | Close AC connection — also disarms auto-connect |
| `set-auto-connect-ac` | Browser → LR | `{ enabled, host?, port? }` | Arm/disarm the persistent auto-connect toggle |

## Host States

- `connected` — LR is heartbeating within the stale threshold
- `disconnected` — LR dropped or was never connected

Hosts appear dynamically in the sidebar as LRs connect; they remain visible (as disconnected) after dropping.

## Global view

The sidebar's "global" entry sits above the per-host list and is mutually
exclusive with selecting a particular host (`selectedHostId === null` in
`App.tsx`); it's the default view on first load. It presents the same set of
tabs a host's dashboard has, but from a net-wide vantage point instead of one
host's — only the `system` tab has a global view implemented so far, with
nested `topology`/`timeline` tabs of its own; the rest (including
`timeline`) render a "not yet implemented" placeholder. `topology` (see
`condocs/initialDistributedDevelopmentImpls/global_topology_panel.jpg`) is a
main pane of host cards plus a details-and-control pane on the right:

- agent-coordinator's own card always leads its own row above the per-host
  cards, mirroring the sketch's `(self)` node, with a faint divider line
  separating it from the hosts below. `self-info` now discloses AC's own
  `host_id` (`ufahostid.GetHostID()`, the same value a co-located LR defaults
  its `-name` to) alongside `dev_mode`; when that id matches a connected
  host, the AC box collapses into that host's own LR/FC/CO/W card -- one
  panel, keeping the host's real name -- and it's selectable like any other
  host card. Without a match (no co-located LR connected), a static,
  unselectable "agent-coordinator" placeholder card is shown instead.
- Each host card's FC/CO/W sub-application boxes turn green once that host's
  `lr-state` reports that service healthy (the same `services` list
  `LRView`'s health indicator reads); the LR/AC boxes keep their static
  colors.
- Clicking a host card selects it (mutually exclusive, click again to
  deselect), which drives the details pane's Host/status/version/uptime
  readouts (version/uptime come from that host's `lr-system-state`, same as
  the per-host system tab) and its controls. The details pane's "update"
  placeholder button is gone -- in its place, when AC's own `dev_mode` is on,
  sits the same `lr-repo-state`-driven rebuild/auto-rebuild widget the
  per-host system tab shows (sends `lr-rebuild-app` / `lr-set-auto-rebuild`
  for the selected host), plus the restart button, enabled once that host's
  LR is loader-managed -- it sends `lr-restart-app` for that host, same as
  the per-host system tab's self-restart control, and reads "restart and
  update LR" instead of "restart LR" whenever that host's self `ProcInfo`
  reports `update_available` (labeled "...LR" rather than the per-host tab's
  bare "restart" now that it sits alongside the agent-coordinator section's
  own restart controls below).
- Still in dev mode only: a host card gets a faint orange halo -- distinct
  from, and layered outside, the grey-vs-blue selected ring -- whenever that
  host is out of date with the dev branch its LR tracks: its dev-repo watcher
  has a rebuild ready (`lr-repo-state.rebuild_ready`, not currently building)
  and/or its self `ProcInfo` has `update_available` set (`hostOutOfDate` in
  `App.tsx`). Outside dev mode neither the halo nor the rebuild widget
  appears, and the restart button never reads "update".
- Also dev mode only, and independent of the card-level halo above: an
  individual FC/CO/W box gets that same orange halo drawn around its own
  green border once it's connected *and* out of date -- that managed
  instance's `ProcInfo.update_available` is set, meaning the application's
  on-disk binary (see local-representative's `pollManagedVersions`) now
  differs from the version it last reported over representable (`system.
  managed`, matched by app name against `services`, in `subAppOutOfDate` in
  `App.tsx`). The green border itself is unaffected -- it's kept as a future
  health indication independent of update state (dev or ops mode). LR is
  deliberately excluded: the host card's own halo already covers it.
- The active tab (e.g. `system`) is shared between the global view and a
  host's view (lifted to `App`), so selecting or deselecting a host keeps
  whichever tab was active instead of resetting it.
- When the selected card is the one agent-coordinator itself runs on (i.e.
  `selectedHostId === selfHostId`), the details pane grows a small
  "agent-coordinator" section below the rebuild/restart controls, with its
  own restart button that sends `ac-restart-app` — distinct from the control
  above it, which always targets that host's *LR*. It follows the same
  paradigm as every other restart control here: always enabled once
  loader-managed (never gated on dev mode), turning orange and reading
  "restart and update" instead of "restart" only when both dev mode is on
  and AC's own on-disk binary has drifted from what's running. Since it's a
  legitimate deployment to run agent-coordinator alone on a box with only
  off-node LRs, AC can't lean on any LR's `pollManagedVersions` for this —
  it runs its own duplicate of local-representative's `selfVersionWatch`
  (`agent-coordinator/selfversion.go`, polling its own executable's
  `--version` every 5s) and discloses the verdict via `self-info`'s new
  `loader_managed`/`update_available` fields, re-broadcasting that message
  whenever the verdict changes rather than only once per connection.
- That same "agent-coordinator" section also gets a "host update all" button,
  next to the AC restart button, enabled only once at least one connected
  FC/CO/W sub-application on this host has `update_available` set (the same
  per-app check that drives that box's own halo above --
  `anySubAppUpdateAvailable` in `App.tsx`) and both this host's LR and AC
  itself are loader-managed. Clicking it only sends a restart to whichever of
  the two is actually running a stale binary -- `lr-restart-app` for this
  host when its LR's own `update_available` is set, `ac-restart-app` when
  AC's is (both, one after the other, when both are stale; neither when the
  pending update is limited to a FC/CO/W instance) -- so it never restarts a
  process that's already current.
