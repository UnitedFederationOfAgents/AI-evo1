# Agent Coordinator — Architecture

## Overview

Agent Coordinator is a hierarchically organized network coordination service. It aggregates visibility and control across multiple hosts, each running a `local-representative` instance. Conventionally organized into two roles:

- **Net coordinator** — coordinates agents across a network segment
- **Web coordinator** — provides browser-accessible UI aggregating multiple net coordinators

## Ports

| Service            | Port | Protocol |
|--------------------|------|----------|
| agent-coordinator  | 8083 | HTTP/WebSocket |

(Other sub-applications: condoccer :8080, local-representative :8081/:8082)

## Component Relationships

```
Browser
  └── WebSocket ──> agent-coordinator (:8083)
                         │
                         │ [TCP — deferred]
                         ├──> local-representative (:8081) on host-A
                         └──> local-representative (:8081) on host-B
```

## WebSocket Protocol

### Server → Client

| Message type | Payload | Description |
|---|---|---|
| `hosts` | `{ hosts: Host[] }` | List of configured hosts with connection status |
| `lr-state` | `{ host_id, active, services? }` | Local-representative state for a host |

### Client → Server

| Message type | Payload | Description |
|---|---|---|
| `select-host` | `{ host_id }` | Request lr-state for a specific host |

## Host States

- `unknown` — not yet attempted connection
- `connected` — TCP link to local-representative established
- `disconnected` — TCP link lost or refused

## Deferred Work

- TCP communication to local-representatives (Step 2+)
- Host configuration persistence
- Net/web coordinator hierarchy
