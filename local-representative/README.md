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
| `--terminal` | `terminal` | autodetect | command prefix used to host `federation-command` in a terminal, e.g. `tmux new-session -d -s fc` (detached, no window — preferred) or `xterm -e` |

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
terminal: tmux new-session -d -s fc   # optional; tmux/screen are autodetected
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

`federation-command` is an interactive shell and must run in a real terminal or
its input reader dies on startup ("error creating cancelreader"). LR probes
`$PATH` in this order, **detached multiplexers first** so the launch never pops a
window or grabs the desktop foreground:

    tmux, screen,                          # detached session — preferred
    xterm, konsole, alacritty, kitty, foot, wezterm, xfce4-terminal,
    x-terminal-emulator, gnome-terminal   # windowed emulators

Set `--terminal` / `terminal` to an explicit command prefix
(`terminal: "tmux new-session -d -s fc"`, `terminal: "xterm -e"`) to override, or
to supply one where none is on `$PATH`. If nothing is found LR records the launch
as `failed` with an actionable message.

With **tmux** (or **screen**) LR starts each instance in its own detached session
(`tmux new-session -d -s fc-<id>`) — no window, never in the foreground. The
system tab lists the instance as `running` with an *attach hint*
(`tmux attach -t fc-<id>`) in its detail column; because the child lives inside
the multiplexer rather than as an LR child process, its PID shows as `—` and
**terminate** tears the session down with `tmux kill-session` (`screen -X quit`).
A configured `terminal:` override containing `-d`/`-dm` is treated the same way,
except LR does not know the session name, so terminate just drops the entry and
you close the session yourself.

Windowed emulators (`xterm -e`, `konsole -e`, …) keep full lifecycle tracking —
real PID, status and a process-group terminate — but pop a window that generally
grabs focus; use one only when you want FC visible on a desktop.

The **system** tab also shows `federation-command control: remote / local / not
connected` above the launch button whenever an instance is running, so you can
confirm the machine-driven chain landed in remote control.

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

### Full hands-off chain (agent-coordinator → LR → FC)

Install `tmux` (so FC is hosted detached, no window), then write:

```yaml
# ~/.ufa/config/local-representative.yaml
auto-connect: true                 # dial agent-coordinator on startup
ac-host: localhost
ac-port: "8084"
auto-launch: federation-command    # "federation-command:3" for three FC instances
# no 'terminal:' key needed — tmux is autodetected and used detached.
# override only for a specific emulator, e.g.  terminal: "xterm -e"
```

Then:

```bash
./agent-coordinator            # in one terminal (or already running)
./local-representative          # in another — no flags needed
```

LR starts its `representable` server, auto-launches `federation-command` into a
detached `tmux` session, and auto-connects to `agent-coordinator`. FC dials LR in
the background and adopts **remote control** (`--remote` implies `--auto-connect`
and forces remote). No further interaction is required; the dashboard's **system**
tab shows `federation-command control: remote` once the chain is up. Attach with
`tmux attach -t fc-<id>` (see the system tab's detail column) to watch or drive FC
directly.
