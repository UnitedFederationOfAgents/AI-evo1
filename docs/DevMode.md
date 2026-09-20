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
  `outline: 4px solid #6ec96e` plus an inset glow (`box-shadow: inset 0 0
  24px rgba(110, 201, 110, 0.65)`) in each frontend's `index.css` — when that
  instance's own `dev_mode` is true, plus a `DEV MODE` tag fixed to the top
  center of the viewport (`.app-dev-mode::before`) so it's visible even when
  the outline itself has scrolled out of view. An `outline` rather than a
  `border` was chosen so it never perturbs layout. (Revision A: the original
  `outline: 1px solid rgba(78, 201, 78, 0.45)` was too subtle to notice at a
  glance, so the outline was thickened to full opacity and the glow added for
  contrast against dark panel backgrounds. Revision B: still too subtle, so
  the color moved from `#4ec94e` to the brighter `#6ec96e` — already used
  elsewhere in these frontends for "connect"/"launch" accent text, so it
  stays on-palette — the glow was enlarged and made more opaque, and the
  fixed `DEV MODE` tag was added as a second, position-independent cue.)
  Each app also gets a small `dev` badge, filled and now using the same
  brighter `#6ec96e`, next to any process/host row that is itself in dev mode
  (`.sys-dev-tag`).
- **`federation-command`**: the square brackets enclosing the blinker (the
  `[` `]` either side of the `●`/`○` indicator) render in the same green
  instead of the default light blue — `blinkerBracketDevStyle` in
  `federation-command/blinker.go`. This is launch-time only, matching the
  blinker's other dev-mode-adjacent state (`devMode` is a plain field on
  `Blinker`, set once from `NewBlinker(devMode)` and never mutated). Revision
  B: the interactive prompt's cursor (the trailing `> `) now renders in the
  same green too (`buildPrompt`'s `devMode` argument, `federation-command/main.go`),
  and both it and the blinker brackets now read off a single shared constant
  — `devGreen` in `federation-command/main.go` — so the two visual cues can
  never drift apart.
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

## Loader

[`ufa-loader`](../ufa-loader) — Step 2 of
[`InitialDistributedDevelopment`](../condocs/InitialDistributedDevelopment.md)
— is a wrapping executable that launches a sub-application binary and
restarts it on request: `ufa-loader <binary> [binary-args...]` runs
`<binary>`, watching its stdout for a restart announcement (an identifying
banner, a line of structured JSON, and a footer — the
[`restartsignal`](../ufa-loader/restartsignal/restartsignal.go) protocol) the
sub-application prints as its final act before exiting. Seeing one turns
that exit into a relaunch of the same binary/args instead of a stop — the
expectation, per the condoc, being that the binary on disk has been replaced
with a newer build by then. A plain exit with no announcement is propagated
as-is (exit code and all): `ufa-loader` never restarts a binary that didn't
ask for it.

Only `local-representative` speaks the protocol so far: sending it `SIGHUP`
(directly to its own pid, not through `ufa-loader`) makes it announce a
restart and exit 0, exactly as described above — see `make run-loader` in
[`local-representative`](../local-representative)'s Makefile for the wired-up
dev loop. Other sub-applications gaining restart/version-switching is just a
matter of them adopting `restartsignal.Announce` too; `ufa-loader` itself
already has no sub-application-specific knowledge to extend.

`ufa-loader` also sets `UFA_LOADER_INIT` on every sub-application it
launches, so a launched sub-application can tell it's loader-managed without
knowing anything else about how it was invoked (`restartsignal.IsLoaderManaged`).
LR's dashboard uses this for its system tab's **restart** control: pressing it
does the same thing as `SIGHUP` (terminate so `ufa-loader` relaunches with the
identical config), but the control greys itself out — and the equivalent
`__system:restart` command from `agent-coordinator` is refused — when LR
isn't loader-managed, since there would be nothing to bring it back up.

See [`ufa-loader/README.md`](../ufa-loader/README.md) for the full protocol
and flags.

## Versioning

Every sub-application binary knows its own build version, per
[`InitialDistributedDevelopment`](../condocs/InitialDistributedDevelopment.md)
Step 2 Revision B — this is what a "dev branch follower" (see "Future
features" below) would eventually compare across hosts to decide whether an
update is available.

Versions are computed at the **repo level**, not per sub-application:
[`scripts/compute-version.sh`](../scripts/compute-version.sh) derives one
string from the current git state and every Makefile embeds the *same*
string into whatever it builds, via
[`ufa-version`](../ufa-version)'s `Version` variable and `-ldflags -X`:

- if `HEAD` is exactly a `vX.Y.Z` tag, that tag is the version, verbatim;
- otherwise it's `<next-patch-of-most-recent-tag>-<branch-heuristic>-<shortsha>`
  — e.g. most recent tag `v0.4.2` on branch `main` gives `v0.4.3-main-8b1e2d4`,
  or on a condoc branch like
  `condoc/InitialDistributedDevelopment-1789821651/main` gives
  `v0.4.3-inidisdev-8b1e2d4` (the heuristic condenses the branch's CamelCase
  step name to its words' initials).

Because it's one version for the whole repo, `make deploy-dev-binaries` after
touching *any single* sub-application gives *every* binary a new version
(the shortsha moved). Outside a git checkout (e.g. a Docker build that
doesn't `COPY` `.git`) it falls back to `dev`, same as a plain `go
build`/`go run .` with no `-ldflags` at all.

Every sub-application binary answers `--version` by printing its version and
exiting instead of starting up (`ufa-version.HandleVersionFlag`; `ufa-loader`
handles it separately since its own argv is followed by a wrapped binary's —
see [`ufa-loader/README.md`](../ufa-loader/README.md)). A few sub-applications
also surface it somewhere more visible:

- **`federation-command`**: the `version` / `ufa-version` shell commands.
- **`local-representative`**: next to its own name on the system tab.
- **`condoccer`**: a subtle annotation next to the "Condoccer" banner in the
  sidebar.

## Future features

The following are **recorded here as the plan, not implemented yet** — later
steps of [`InitialDistributedDevelopment`](../condocs/InitialDistributedDevelopment.md)
build on the dev-mode foundation above:

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
