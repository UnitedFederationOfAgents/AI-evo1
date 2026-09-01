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

| Flag | Config key | Default | Purpose |
| --- | --- | --- | --- |
| `--config` | — | `~/.ufa/config` | directory holding the `ufa-configurable` YAML files |
| `--port` | `port` | `8081` | HTTP port for the dashboard / WebSocket |
| `--repr-port` | `repr-port` | `8082` | TCP port for the `representable` heartbeat server FC dials |
| `--name` | `name` | hostname | identifier reported to `agent-coordinator` |
| `--dev` | `dev` | `false` | dev mode: don't serve the embedded frontend |
| `--auto-connect` | `auto-connect` | `false` | on startup, dial `agent-coordinator` in the background |
| `--ac-host` | `ac-host` | `localhost` | `agent-coordinator` host/IP for `--auto-connect` |
| `--ac-port` | `ac-port` | `8084` | `agent-coordinator` port for `--auto-connect` |

## Configuration files

Every flag except `--config` can also be set from YAML, via the shared
[`ufa-configurable`](../ufa-configurable) loader. On startup LR reads:

    ~/.ufa/config/global.yaml               # shared across all sub-applications
    ~/.ufa/config/local-representative.yaml # local-representative overrides

Pass `--config <dir>` (or set `$UFA_CONFIG_DIR`) to look elsewhere. Precedence,
highest first: **command-line flag → `local-representative.yaml` →
`global.yaml` → built-in default** (resolved per key, so a single value in
`global.yaml` still applies even when the app file sets other keys).

The format is a flat `key: value` mapping; `#` comments and blank lines are
ignored.

```yaml
# ~/.ufa/config/global.yaml — applies to every sub-application
auto-connect: true

# ~/.ufa/config/local-representative.yaml — local-representative only
port: 8081
repr-port: 8082
name: edge-1
dev: false
ac-host: 10.0.0.5
ac-port: "8084"
```

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
