# local-representative

Per-host agent that fronts a `federation-command` instance: it runs the
`representable` heartbeat server FC connects to, serves the browser dashboard, and
relays state/logs/commands up to an `agent-coordinator`.

## Running

```bash
make build      # build frontend + Go binary
./local-representative
```

## Flags

| Flag | Default | Purpose |
| --- | --- | --- |
| `--port` | `8081` | HTTP port for the dashboard / WebSocket |
| `--repr-port` | `8082` | TCP port for the `representable` heartbeat server FC dials |
| `--name` | hostname | identifier reported to `agent-coordinator` |
| `--dev` | `false` | dev mode: don't serve the embedded frontend |
| `--auto-connect` | `false` | on startup, dial `agent-coordinator` in the background |
| `--ac-host` | `localhost` | `agent-coordinator` host/IP for `--auto-connect` |
| `--ac-port` | `8084` | `agent-coordinator` port for `--auto-connect` |

## Auto-connecting to agent-coordinator

`--auto-connect` mirrors `federation-command`'s `--auto-connect`. On startup LR
prints that the mode is selected, then dials `agent-coordinator` in the
background, retrying every 10 seconds for up to 10 minutes. The retry runs
without blocking the HTTP server; while it is in progress the dashboard's
agent-coordinator panel shows a pulsing "auto-connecting…" indicator. On success
the connection is adopted like a manual connect; if the 10-minute window elapses
first, LR prints that it gave up. Driving an explicit connect or disconnect from
the dashboard supersedes and cancels the background loop.

```bash
./local-representative --auto-connect                       # localhost:8084
./local-representative --auto-connect --ac-host 10.0.0.5 --ac-port 9000
```
