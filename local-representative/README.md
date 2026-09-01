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
| `--auto-launch` | `auto-launch` | — | comma/space-separated child applications to launch on startup; each token is `app` or `app:N` (e.g. `federation-command:2`) |
| `--fc-bin` | `fc-bin` | — | explicit path to the `federation-command` binary (default: search next to LR, the dev bin dir, then `$PATH`) |
| `--terminal` | `terminal` | autodetect | command prefix used to host `federation-command` in a terminal, e.g. `xterm -e` or `tmux new-session -d -s fc` |

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
auto-launch: federation-command:2
terminal: xterm -e
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

## System tab

The dashboard's right-most tab, **system**, shows `local-representative` as a
process (its PID and uptime) alongside any child applications it manages. From
here you can:

- **launch** a child application — currently `federation-command`, started with
  `--auto-connect --remote --lr-port <repr-port>` so it dials straight back into
  this LR and comes up in remote control, ready to drive from the dashboard;
- **terminate** a managed instance (SIGTERM to its process group, escalating to
  SIGKILL after a grace period), or **dismiss** one that has already exited;
- read each managed instance's PID, status (`running` / `exited` / `failed`) and
  exit code.

`federation-command` is **N-per-host**: the launch button stays enabled while
instances run and each press starts another, listed as `federation-command #1`,
`#2`, … Terminate/dismiss act on the individual instance.

`federation-command` is an interactive shell, so LR hosts each instance in its
own terminal window. It probes for a terminal emulator / multiplexer on `$PATH`
(`xterm`, `konsole`, `alacritty`, `kitty`, `foot`, `wezterm`, `xfce4-terminal`,
then `x-terminal-emulator`, `gnome-terminal`, `tmux`, `screen`); set
`--terminal` / `terminal` to an explicit command prefix (`terminal: "xterm -e"`,
`terminal: "tmux new-session -d -s fc"`) to override, or to supply one in an
environment where none is on `$PATH`. If nothing is found LR records the launch
as `failed` with an actionable message rather than letting FC die inside its
input reader ("error creating cancelreader").

Foreground terminals (`xterm -e`, `konsole -e`, `alacritty -e`, `kitty`, `foot`,
…) keep full lifecycle tracking — PID, status and terminate all act on the real
instance. Detached multiplexers / terminals that double-fork (`tmux`, `screen`,
`gnome-terminal`) return immediately, so LR shows the launcher's exit and can no
longer track that instance's PID; prefer a foreground terminal when you need the
system tab to manage FC.

The launch binary is resolved by looking next to the `local-representative`
executable, then in `$AI_EVO1_DEV_BIN` (default `/AI-evo1-dev/bin`), then on
`$PATH`; `--fc-bin` / `fc-bin` overrides that.

### Auto-launch chains

Set `auto-launch` (config or `--auto-launch`) to a list of child applications and
LR starts them automatically once it is up. Each token is `app` or `app:N`, so
`federation-command:3` brings up three instances. With

```yaml
# ~/.ufa/config/local-representative.yaml
auto-launch: federation-command
```

a single `./local-representative` brings up LR **and** an auto-connecting,
remote-controlled `federation-command`, completing the FC → LR chain from one
entrypoint. Combine with `auto-connect` to extend the chain up to
`agent-coordinator`.
