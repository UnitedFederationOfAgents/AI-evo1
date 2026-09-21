# local-representative

Per-host agent that fronts a `federation-command` instance: it runs the
`representable` heartbeat server FC connects to, serves the browser dashboard, and
relays state/logs/commands up to an `agent-coordinator`.

## Running

```bash
make build      # build frontend + Go binary
./local-representative
```

### Running under ufa-loader

`SIGHUP` sent to LR's own pid makes it print a restart announcement on
stdout and exit 0, instead of just logging and continuing — see
[`docs/DevMode.md`](../docs/DevMode.md)'s "Loader" section. Run it under
[`ufa-loader`](../ufa-loader) to have that turned into an actual relaunch —
expected once the on-disk binary has been replaced with a newer build:

```bash
make run-loader                    # builds ufa-loader too, then wraps LR
make run-loader ARGS="--dev-mode"  # ARGS is passed straight through to LR
kill -HUP <local-representative pid>   # ask the running instance to restart
```

Plain `kill -HUP` without a wrapping `ufa-loader` still works — LR
announces and exits — but nothing relaunches it in that case.

The system tab's **restart** control (see below) does the same thing from
the dashboard instead of a shell. `ufa-loader` sets `UFA_LOADER_INIT` on
every sub-application it launches, which is how LR tells whether it is
loader-managed at all — the control greys itself out (and the backend
refuses the request) when it isn't, since pressing it would otherwise just
stop LR for good.

## Flags

| Flag | Config key | Default | Purpose |
| --- | --- | --- | --- |
| `--version` | — | — | print version and exit instead of starting the dashboard (see [`docs/DevMode.md`](../docs/DevMode.md) "Versioning") |
| `--config` | — | `~/.ufa/config` | directory holding the `ufa-configurable` YAML files |
| `--port` | `port` | `8081` | HTTP port for the dashboard / WebSocket |
| `--repr-port` | `repr-port` | `8082` | TCP port for the `representable` heartbeat server FC dials |
| `--name` | `name` | hostname | identifier reported to `agent-coordinator` |
| `--dev` | `dev` | `false` | dev mode: don't serve the embedded frontend |
| `--dev-mode` | `dev-mode` | `false` | dev mode in the SDLC sense (see [`docs/DevMode.md`](../docs/DevMode.md)): this LR — and every `federation-command`/`condoccer` it launches — is running from an in-progress branch. Unrelated to `--dev` above. |
| `--dev-repo` | `dev-repo` | `false` | implies `--dev-mode`; also watches the launch working directory's git repo for changes to rebuild from (see "Dev-repo watcher" below and [`docs/DevMode.md`](../docs/DevMode.md)) — requires running inside a git repository |
| `--auto-connect` | `auto-connect` | `false` | on startup, dial `agent-coordinator` in the background |
| `--ac-host` | `ac-host` | `localhost` | `agent-coordinator` host/IP for `--auto-connect` |
| `--ac-port` | `ac-port` | `8084` | `agent-coordinator` port for `--auto-connect` |
| `--auto-launch` | `auto-launch` | — | comma/space-separated child applications to launch on startup; each token is `app` or `app:N` (e.g. `federation-command:2`) |
| `--fc-bin` | `fc-bin` | — | explicit path to the `federation-command` binary (default: search next to LR, the dev bin dir, then `$PATH`) |
| `--terminal` | `terminal` | autodetect | command prefix used to host `federation-command` in a terminal, e.g. `xterm -e` (visible window — preferred) or `tmux new-session -d -s fc` (detached fallback) |
| `--condoccer-port` | `condoccer-port` | `8080` | HTTP port a managed `condoccer` serves on; its UI is reverse-proxied at `/condoccer/` |
| `--condoccer-root` | `condoccer-root` | — | repo root a managed `condoccer` scans (default: condoccer's own `-root`) |
| `--file-cache-dir` | `file-cache-dir` | `/host-agent-files/exchange/host-cache` | directory the `files` tab uploads into; entries older than 1 hour are swept (72 hours once held) |
| `--host-store-dir` | `host-store-dir` | `/host-agent-files/exchange/host-store` | directory the file details dialog's **persist** button moves a file into; never swept |

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
terminal: xterm -e                   # optional; an emulator on $PATH is autodetected
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
  `--auto-connect --lr-port <repr-port>` *and* the matching `FC_*` environment
  variables (`FC_AUTO_CONNECT`, `FC_LR_HOST`, `FC_LR_PORT`) so it dials straight
  back into this LR and comes up in **remote control** (auto-connect implies
  remote — there is no separate `--remote` flag), ready to drive from the
  dashboard. The env vars are belt-and-braces: a terminal wrapper that mangles
  trailing argv can't drop `--auto-connect` and leave FC stuck in local control;
- **terminate** a managed instance (SIGTERM to its process group, escalating to
  SIGKILL after a grace period), or **dismiss** one that has already exited;
- **restart** LR itself — the same as sending it `SIGHUP` (see "Running under
  ufa-loader" above): it terminates so a wrapping `ufa-loader` relaunches it
  with the identical config. Only enabled when this LR is loader-managed
  (`UFA_LOADER_INIT` was set at startup) — otherwise the button is greyed
  out, since there would be nothing to bring it back. When loader-managed, LR
  also polls its own on-disk binary's `--version` every 5 seconds; once it
  disagrees with the version actually running, the button turns orange and
  reads **update and restart** instead of a plain **restart**, so you know
  pressing it lands a newer build rather than just cycling the same one;
- read each managed instance's PID, status (`running` / `exited` / `failed`) and
  exit code;
- see this LR's own build **version** next to its name (see
  [`docs/DevMode.md`](../docs/DevMode.md) "Versioning") — and, once a managed
  `federation-command`/`condoccer` instance has connected and reported in,
  its version next to *its* name too.

`federation-command` is **N-per-host**: the launch button stays enabled while
instances run and each press starts another, listed as `federation-command #1`,
`#2`, … Terminate/dismiss act on the individual instance.

`condoccer` is **one-per-box** (singleton): the launch button disables while an
instance runs. It is launched with `--auto-connect --lr-port <repr-port> --port
<condoccer-port>`, so it dials this LR's `representable` server as `condoccer`,
heartbeats alongside `federation-command`, and pushes its condoc summary up the
chain. Its browser UI is **forwarded, not embedded**: LR reverse-proxies
`/condoccer/*` to the managed condoccer on loopback (the **condoccer** tab shows
it in an iframe), and `agent-coordinator` reverse-proxies `/host/<name>/*` back
through this LR — so the coordinator dashboard (and anything reaching it through
the web-exposure path) can drive condoccer on this box with no shell here.
`--auto-connect` is how LR always launches it, but it isn't mandatory for
condoccer in general: its UI has its own connect/disconnect widget for the case
where it's started by hand instead.

`federation-command` is an interactive shell and must run in a real terminal or
its input reader dies on startup ("error creating cancelreader"). It should be
**visible** — the operator wants to see FC come up — so LR probes `$PATH` for a
windowed emulator first and only falls back to a detached multiplexer where no
emulator exists:

    xterm, konsole, alacritty, kitty, foot, wezterm, xfce4-terminal,
    x-terminal-emulator, gnome-terminal   # visible window — preferred
    tmux, screen                          # detached (invisible) — last resort

The new window is launched with `DESKTOP_STARTUP_ID` / `XDG_ACTIVATION_TOKEN`
stripped from its environment, so an EWMH-compliant window manager / Wayland
compositor maps it **visible but unfocused** — an auto-launched FC no longer
steals focus from whatever the operator is doing. (Final behaviour is
WM-dependent; this suppresses the activation request, not the window.)

Set `--terminal` / `terminal` to an explicit command prefix
(`terminal: "xterm -e"`, `terminal: "tmux new-session -d -s fc"`) to override, or
to supply one where none is on `$PATH`. If nothing is found LR records the launch
as `failed` with an actionable message.

Windowed emulators keep full lifecycle tracking — real PID, status and a
process-group **terminate**. If you deliberately choose **tmux**/**screen** (or a
`terminal:` override containing `-d`/`-dm`) LR starts each instance in its own
detached session; the system tab lists it as `running` with an *attach hint*
(`tmux attach -t fc-<id>`) in its detail column, its PID shows as `—`, and
**terminate** tears the session down with `tmux kill-session` (`screen -X quit`).

The **system** tab also shows `federation-command control: remote / local / not
connected` above the launch button whenever an instance is running, so you can
confirm the machine-driven chain landed in remote control.

### Driving the system tab from agent-coordinator

When LR is connected to an `agent-coordinator`, it mirrors this system tab up
over the `representable` channel (a `system-state` data message on every change,
plus a `repo-state` one when `--dev-repo` is set) and accepts `__system:launch
<app>` / `__system:terminate <instance-id>` / `__system:restart` /
`__system:rebuild` / `__system:auto-rebuild <on|off>` commands back. The
coordinator dashboard gains a matching **system** tab per host, so an operator
can launch, terminate, and restart managed applications — and, for a
`--dev-repo` host, trigger or auto-enable a rebuild — on any connected LR
without a shell on that host, closing the loop for provisioning a new machine
and bringing it up fully remote-controlled. `__system:restart` is subject to
the same loader-managed guard as the dashboard's own button — it's a no-op
(logged) if this LR wasn't launched by `ufa-loader`; `__system:rebuild` /
`__system:auto-rebuild` are likewise no-ops (logged, or silent for
auto-rebuild) if this LR wasn't launched with `--dev-repo`.

The launch binary is resolved by looking next to the `local-representative`
executable, then in `$AI_EVO1_DEV_BIN` (default `/AI-evo1-dev/bin`), then on
`$PATH`; `--fc-bin` / `fc-bin` overrides that.

### Dev-repo watcher

Launch with `--dev-repo` (from inside a git checkout — it refuses to start
otherwise) and the system tab grows a **rebuild** control for the repo LR was
launched from. The watcher treats the HEAD it first sees as already built, so
the button starts out inactive rather than lighting up for a repo that hasn't
changed since LR started watching. Every 5 seconds LR checks the repo: a
dirty working tree (`git diff HEAD` non-empty — untracked files don't count)
turns the button orange and labels it **dirty**, but leaves it disabled —
rebuilding a dirty tree would silently bake in uncommitted, unreviewed
changes; a clean tree instead pulls in any upstream movement (`git fetch` +
`pull --rebase`) and, if HEAD has moved since the last successful rebuild,
makes the button active, green, and labelled **rebuild**. Pressing it (or
`agent-coordinator`'s `__system:rebuild`) runs `make deploy-dev-binaries` at
the repo root, greying the button and showing **building…** meanwhile.

The **auto-rebuild** checkbox next to it (also `__system:auto-rebuild
<on|off>` from `agent-coordinator`) makes LR press that button itself
whenever it becomes active — which, since rebuilding is disabled while
dirty, means waiting for a clean repo with HEAD moved. Every git/make call
the watcher makes — checks, the pull, and a rebuild — is serialized through
one mutex, so the check-and-rebuild process never overlaps itself. See
[`docs/DevMode.md`](../docs/DevMode.md) "Dev-repo watcher" for the full
detection rules.

## Files tab

The **files** tab shows a wireframe-icon view (text / image / other — a small
set for this increment) of whatever sits in this LR's host-cache directory
(`--file-cache-dir`, default `/host-agent-files/exchange/host-cache`) plus its
host-store directory (`--host-store-dir`). Drag a file from your system's file
manager onto the tab to upload it; clicking a file opens a right-hand detail
pane with its name, size, type and upload/expiry times, plus **enter →** and
**download** buttons. A freshly-uploaded file is swept an hour after upload —
its icon renders orange — unless held or persisted (see below).

Double-clicking a file (or the detail pane's **enter →** button) opens a
full-page **viewer**, reminiscent of drilling into a step through condoccer:
images render inline, text files are fetched and shown as plain text, and
anything else falls back to a "use download" notice — a **← back** button
returns to the grid. **download** (in the detail pane or the viewer) saves
the file's bytes as-is, from `GET /api/files/<id>` (add `?download=1` for an
attachment `Content-Disposition`; without it the response is `inline`, which
is what the viewer embeds/fetches).

The detail pane also has **hold**/**persist** and **delete** buttons:

- **hold** (`POST /api/files/<id>/hold`) extends a file's sweep-eligibility
  from 1 hour to 72 hours from the press, and turns its icon yellow. The
  button then reads **persist** in its place.
- **persist** (`POST /api/files/<id>/persist`) moves a held file out of the
  host-cache into the host-store, where it is never swept; its icon turns
  green.
- **delete** (`DELETE /api/files/<id>`, behind a confirm dialog) removes the
  file — cached, held, or persisted — immediately.

Alongside every uploaded file, LR also writes a hidden
`.manifest_<id>.yaml` sidecar (flat `key: value` YAML — name, kind, size,
upload/expiry times, held, creator). It's invisible to the files tab; LR
reads `held`/`expires_at` back to survive a restart, and records `creator`
(this LR's own identity) for future correlation once file exchange spans
more than one host. Uploads whose claimed filename starts with `.manifest_`
are refused; the sweep removes a manifest alongside its expired data file,
and a **persist** press moves the manifest into the host-store along with
the file rather than dropping it.

Upload from a browser connected straight to this LR always works. A request
arriving through `agent-coordinator`'s `/host/<id>/*` transparent reverse
proxy is refused (`agent-coordinator` stamps proxied requests with an
`X-UFA-Proxied-By` header LR's upload handler checks for) — *unless* it also
carries `X-UFA-Relayed-Upload-By: agent-coordinator`, which only
agent-coordinator's own dedicated upload-relay route can set (it builds a
fresh outbound request rather than forwarding the browser's request
verbatim, so a client can't spoof the header through the transparent proxy
path instead). That's how the coordinator's own **files** tab gets a dropzone
too: it POSTs to `/host/<id>/api/files`, which agent-coordinator relays down
to this LR rather than proxying transparently or keeping its own copy of the
file. Viewing and downloading are **not** gated on either header — a GET
reaching `/api/files/<id>` through AC's proxy is served the same as a direct
request, so the coordinator's files tab gets the same viewer/enter/download
widgets, just proxied at `/host/<id>/api/files/<id>`.
See [`docs/DistributedExchange.md`](../docs/DistributedExchange.md) for how
this and cross-host (LR↔AC↔LR) transfer fit together.

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

Add `condoccer` to the list (`auto-launch: federation-command condoccer`) to also
bring up the one-per-box `condoccer`, auto-connected to LR and with its UI
forwarded at `/condoccer/`. `condoccer:N` is accepted but only the first instance
starts — it is a singleton.

### Full hands-off chain (agent-coordinator → LR → FC)

Have a terminal emulator on `$PATH` (`xterm`, `konsole`, …) so FC comes up in a
visible window, then write:

```yaml
# ~/.ufa/config/local-representative.yaml
auto-connect: true                 # dial agent-coordinator on startup
ac-host: localhost
ac-port: "8084"
auto-launch: federation-command    # "federation-command:3" for three FC instances
# no 'terminal:' key needed — an emulator on $PATH is autodetected.
# override only if you want a specific one, e.g.  terminal: "xterm -e"
```

Then:

```bash
./agent-coordinator            # in one terminal (or already running)
./local-representative          # in another — no flags needed
```

LR starts its `representable` server, auto-launches `federation-command` in a
terminal window (visible, but not focus-stealing), and auto-connects to
`agent-coordinator`. FC dials LR in the background and adopts **remote control**
(auto-connect implies it; FC's local input stays suspended until LR connects).
No further interaction is required; the dashboard's **system** tab shows
`federation-command control: remote` once the chain is up. Watch or drive FC
directly in its terminal window (or, if you chose the tmux/screen fallback,
`tmux attach -t fc-<id>` using the id in the system tab's detail column). The
same **system** tab is available in `agent-coordinator` for every connected LR,
so the launch/terminate controls work from the coordinator too.
