# Dev Mode

How the persistent sub-applications (`federation-command`, `local-representative`,
`agent-coordinator`, `condoccer`, `dungeon-keeper`) distinguish an instance
running from an in-progress development branch from one running an
operations build, per [`condocs/InitialDistributedDevelopment.md`](../condocs/InitialDistributedDevelopment.md)
Step 1. This is the foundation the rest of that condoc's steps build on; see
"Future features" below for what's still to come.

## Launching in dev mode

Dev mode is **launch-time only** — there is no runtime toggle, and no plan to
add one. It's set with a `--dev-mode` flag:

| App | Flag | Cascades to |
| --- | --- | --- |
| `federation-command` | `--dev-mode` (also `FC_DEV_MODE=1` / config key `dev-mode`) | — |
| `local-representative` | `--dev-mode` (also config key `dev-mode`) | every `federation-command` / `condoccer` it launches |
| `agent-coordinator` | `--dev-mode` | — |
| `condoccer` | `--dev-mode` | — |
| `dungeon-keeper` | `dungeon-keeper watch --dev-mode` | — |

**This is a different flag from the pre-existing `--dev`** that
`local-representative`, `agent-coordinator`, and `condoccer` already had —
that one just skips serving the embedded frontend build so a `vite` dev
server can be pointed at the backend instead. The two are unrelated and both
can be set at once; `--dev-mode` is the SDLC concept this document covers.

`federation-command`'s `cliConfig.devMode` and `local-representative`'s
`appConfig.devMode` follow the same config-file / environment-variable / CLI
precedence as every other flag those two already load through
[`ufa-configurable`](../ufa-configurable/README.md) (CLI beats env beats
config file). `agent-coordinator`, `condoccer`, and `dungeon-keeper` take a
plain flag with no config-file binding, matching how they handle their other
flags today.

## Cascading from local-representative

Per the prompt's requirement, launching `local-representative` with
`--dev-mode` launches every instance it manages — `federation-command` and
`condoccer`, see `managedApps` in `local-representative/procman.go` — with
`--dev-mode` too (and, for `federation-command`, the belt-and-braces
`FC_DEV_MODE=1` environment variable, mirroring how `--auto-connect` /
`FC_AUTO_CONNECT` already survive a terminal wrapper mangling argv). There is
no per-instance override: an LR's dev mode is also its managed instances'
dev mode.

`local-representative`'s `systemState()` (the "system" tab / `system-state`
message) stamps every `ProcInfo` — its own `self` entry and each managed
entry — with `dev_mode`, and `agent-coordinator` forwards that struct
unmodified in `lr-system-state`, so both UIs can show a `dev` tag next to a
process's name without any extra plumbing.

## Visual indicators

- **Web UI apps** (`local-representative`, `agent-coordinator`, `condoccer`):
  the whole app frame gets an easily recognizable green outline —
  `outline: 4px solid #4ec94e` plus an inset glow (`box-shadow: inset 0 0
  16px rgba(78, 201, 78, 0.5)`) in each frontend's `index.css` — when that
  instance's own `dev_mode` is true. `#4ec94e` is the same green already used
  for "healthy" / "connected" indicators across these three frontends, reused
  here rather than introducing a new accent color. An `outline` rather than a
  `border` was chosen so it never perturbs layout. (Revision A: the original
  `outline: 1px solid rgba(78, 201, 78, 0.45)` was too subtle to notice at a
  glance, so the outline was thickened to full opacity and the glow added for
  contrast against dark panel backgrounds.) Each app also gets a small `dev`
  badge, now filled instead of outline-only, next to any process/host row that
  is itself in dev mode (`.sys-dev-tag`).
- **`federation-command`**: the square brackets enclosing the blinker (the
  `[` `]` either side of the `●`/`○` indicator) render in the same green
  instead of the default light blue — `blinkerBracketDevStyle` in
  `federation-command/blinker.go`. This is launch-time only, matching the
  blinker's other dev-mode-adjacent state (`devMode` is a plain field on
  `Blinker`, set once from `NewBlinker(devMode)` and never mutated).
- **`dungeon-keeper`**: no web UI or long-lived interactive surface to put a
  border or badge on; `watch --dev-mode` just logs it at startup.

## Mode mismatch: representable-level enforcement and disclosure

`federation-command` ↔ `local-representative`, `condoccer` ↔
`local-representative`, and `local-representative` ↔ `agent-coordinator` all
ride the shared [`representable`](../representable/representable.go)
client/server protocol. Every `Msg` a client sends and the `ServerMsg{Type:
"hello"}` a server sends immediately on accept now carry a `Mode` field
(`representable.ModeDev` / `representable.ModeOps`, built from a
`--dev-mode` flag via `representable.Mode(devMode bool)`).

Per the prompt, **a dev-mode and an ops-mode instance connected to each other
do the minimum necessary to exchange health information and disclose the
mismatch, and nothing else**:

- `heartbeat` messages always flow both ways — that's the health check.
- The initial `hello` (server → client) and the `Mode` field the client
  stamps on every message it sends are the disclosure — each side always
  knows the other's mode and whether they differ (`Client.ModeMismatch()` /
  `Client.PeerMode()` on the client side; `Server.ModeMismatch(name)` /
  `Server.PeerMode(name)` on the server side).
- `state`, `log`, and `data` messages — control-mode switches, output
  streaming, remote commands, and every typed payload (`ridealong-state`,
  `system-state`, file listings, …) — are silently dropped by the receiving
  side whenever the pair is mismatched. A command already in flight when a
  mismatch is detected is likewise refused on arrival, not executed.

This is enforced inside `representable` itself (`Server.handleConn` /
`Client.readLoop`), not by each app individually, so it applies uniformly to
all three connection types without each app having to reimplement the
refusal logic. Each app registers a `SetModeMismatchHandler` to *disclose*
the mismatch in its own UI on top of that:

- `federation-command` prints a one-line warning (`devWarningStyle`) the
  moment a connected LR's mode disagrees with its own, and again when the
  mismatch clears.
- `local-representative` and `agent-coordinator` broadcast a `mode-mismatch`
  WebSocket message and show a banner across the top of the dashboard
  listing every currently-mismatched peer.
- `condoccer` does the same for its single LR connection.

## Future features

The following are **recorded here as the plan, not implemented yet** — later
steps of [`InitialDistributedDevelopment`](../condocs/InitialDistributedDevelopment.md)
build on the dev-mode foundation above:

- **Loader**: a Go application responsible for launching a sub-application so
  that it gains restart and version-switching capabilities (today, none of
  these apps can be restarted or upgraded in place by anything other than an
  operator or `local-representative`'s process manager killing and
  relaunching them).
- **LR updater**: the ability for `local-representative` to instruct updates
  to the sub-applications connected to it (today it can only launch and
  terminate them).
- **AC updater**: the next hop in the chain — `agent-coordinator` broadcasting
  update signals to a set of connected `local-representative`s.
- **Dev branch follower**: a mechanism to follow a git branch of
  in-progress development and propose updates upon change (this is presumably
  how a dev-mode instance would learn there's something new to switch to).
- **The global view in `agent-coordinator`**: an alternative set of panels to
  the per-host views that exist today, presenting a net-centric style of
  interaction rather than "pick a host, see that host's tabs."
- **The global system view in `agent-coordinator`**: the global-mode panel
  for viewing the whole working group of participants with health and
  version information — the place updates would be pushed from once the
  updater pieces above exist.
