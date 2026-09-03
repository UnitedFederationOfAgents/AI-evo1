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
| LR → AC | `log` (cmd/output) | FC command echo / output forwarded upstream |
| AC → LR | `command` | plain cmd or `__ridealong:action` → forwarded to FC; `__system:launch <app>` / `__system:terminate <id>` → LR's process manager |

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
