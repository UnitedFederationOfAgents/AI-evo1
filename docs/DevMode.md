# Dev Mode

How the persistent sub-applications (`federation-command`, `local-representative`,
`agent-coordinator`, `condocv2`, `session-manager`, `the-conversationalist`,
`dungeon-keeper`) distinguish an instance
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
| `local-representative` | `--dev-mode` (also config key `dev-mode`) | every `federation-command` / `condocv2` / `session-manager` / `the-conversationalist` it launches |
| `local-representative` | `--dev-repo` (also config key `dev-repo`) | implies `--dev-mode`; also watches the launch working directory's git repo — see "Dev-repo watcher" below |
| `agent-coordinator` | `--dev-mode` | — |
| `condocv2` | `--dev-mode` | — |
| `session-manager` | `--dev-mode` | — |
| `the-conversationalist` | `--dev-mode` | — |
| `composer` | `--dev-mode` | — |
| `dungeon-keeper` | `dungeon-keeper watch --dev-mode` | — |

**This is a different flag from the pre-existing `--dev`** that
`local-representative`, `agent-coordinator`, `condocv2`, `session-manager`,
and `the-conversationalist` already had —
that one just skips serving the embedded frontend build so a `vite` dev
server can be pointed at the backend instead. The two are unrelated and both
can be set at once; `--dev-mode` is the SDLC concept this document covers.

`federation-command`'s `cliConfig.devMode` and `local-representative`'s
`appConfig.devMode` follow the same config-file / environment-variable / CLI
precedence as every other flag those two already load through
[`ufa-configurable`](../ufa-configurable/README.md) (CLI beats env beats
config file). `agent-coordinator`, `condocv2`, `session-manager`,
`the-conversationalist`, and `dungeon-keeper` take a
plain flag with no config-file binding, matching how they handle their other
flags today.

## Cascading from local-representative

Per the prompt's requirement, launching `local-representative` with
`--dev-mode` launches every instance it manages — `federation-command`,
`condocv2`, `session-manager` (tab: "sessions"), and `the-conversationalist`
(tab: "convo"), see `managedApps` in `local-representative/procman.go` — with
`--dev-mode` too (and, for `federation-command`, the belt-and-braces
`FC_DEV_MODE=1` environment variable, mirroring how `--auto-connect` /
`FC_AUTO_CONNECT` already survive a terminal wrapper mangling argv). There is
no per-instance override: an LR's dev mode is also its managed instances'
dev mode.

That holds for external plug-ins too, whose flag handling LR can't vouch
for (a dev-mode LR once had the `hyena` plug-in come up in ops mode). Every
launch, whatever the app, also sets `UFA_LAUNCH_MODE` (`dev` or `ops`, LR's
own mode, `representable.LaunchModeEnvVar`) in the child's environment, and
`representable.Connect` connects in that mode whatever mode its caller
passed. `SendPluginHello` stamps the hello's `dev_mode` from the
connection's mode too. So any sub-app built against the current
`representable` connects in its launching LR's mode, even if it ignores or
misreads `--dev-mode`. LR itself drops an inherited `UFA_LAUNCH_MODE` at
startup: its own mode comes from its flags and config alone.

If one LR launched still connects in the other mode (one built against an
older `representable`, or one not using it), LR stops that instance rather
than leave it running mismatched. Its row on the system tab is left exited,
and the row's exit-code tooltip (and LR's log) says why. A mismatched peer's
banner entry also clears when it disconnects, for plug-ins as well as LR's
own apps.

`local-representative`'s `systemState()` (the "system" tab / `system-state`
message) stamps every `ProcInfo` — its own `self` entry and each managed
entry — with `dev_mode`, and `agent-coordinator` forwards that struct
unmodified in `lr-system-state`, so both UIs can show a `dev` tag next to a
process's name without any extra plumbing.

## Visual indicators

- **Web UI apps** (`local-representative`, `agent-coordinator`, `condoccer`):
  the whole app frame gets an easily recognizable green outline —
  `outline: 2px solid #6ec96e` plus an inset glow (`box-shadow: inset 0 0
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
  fixed `DEV MODE` tag was added as a second, position-independent cue.
  Revision G: with the brighter green now easy to spot on its own, the
  outline was thinned from `4px` back down to `2px`.) Each app also gets a
  small `dev` badge, filled and now using the same brighter `#6ec96e`, next
  to any process/host row that is itself in dev mode (`.sys-dev-tag`).
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

`local-representative`, `condoccer`, `federation-command`, `dungeon-keeper`
(its `watch` loop only — every other subcommand already exits on its own),
and `agent-coordinator` all speak the protocol: sending `SIGHUP` directly to
one of their pids (not through `ufa-loader`) makes it announce a restart and
exit 0, exactly as described above — see `make run-loader` in each of their
Makefiles for the wired-up dev loop. `federation-command`'s TUI quits and
restores the terminal first, then announces, since the banner has to land as
a clean stdout line rather than get interleaved with the live TUI's own
redraws. Any further sub-application gaining restart/version-switching is
just a matter of it adopting
`restartsignal.Announce` too; `ufa-loader` itself already has no
sub-application-specific knowledge to extend.

`ufa-loader` also sets `UFA_LOADER_INIT` on every sub-application it
launches, so a launched sub-application can tell it's loader-managed without
knowing anything else about how it was invoked (`restartsignal.IsLoaderManaged`).
LR's dashboard uses this for its system tab's **restart** control: pressing it
does the same thing as `SIGHUP` (terminate so `ufa-loader` relaunches with the
identical config), but the control greys itself out — and the equivalent
`__system:restart` command from `agent-coordinator` is refused — when LR
isn't loader-managed, since there would be nothing to bring it back up.
`agent-coordinator`'s own per-host system tab shows the identical **restart**
control (sending `__system:restart` over the same representable link), since
these controls always act on the selected host's LR, never on
`agent-coordinator` itself. The one control that *does* act on
`agent-coordinator` itself lives in the global topology view's
Details & Control pane: selecting the host AC runs on grows a small
"agent-coordinator" section below the per-host rebuild/restart controls with
its own **restart** button, sending a dedicated `ac-restart-app` WebSocket
message (no host to target, unlike `lr-restart-app`) straight to AC's own
`requestRestart` — see
[`condocs/initialDistributedDevelopmentImpls/Step4Prompt.md`](../condocs/initialDistributedDevelopmentImpls/Step4Prompt.md)
Revision E.

An announcement can also carry opaque `state` that `ufa-loader` hands the
*next* launch back as `UFA_LOADER_STATE` (`restartsignal.AnnounceState` /
`restartsignal.PreviousState`), so live in-memory state established since
startup survives a restart instead of resetting to whatever the relaunch's
flags/config say. LR uses this for its own `auto-rebuild` toggle (see
"Dev-repo watcher" below) and its `auto-connect` connection to
agent-coordinator: `reststate.go`'s `lrState` is attached to every restart
announcement and re-applied — taking precedence over `--auto-connect`/
`--ac-host`/`--ac-port` — before the replacement instance dials out. See
[`condocs/initialDistributedDevelopmentImpls/Step3Prompt.md`](../condocs/initialDistributedDevelopmentImpls/Step3Prompt.md)
Revision D.

When LR is loader-managed it also polls its own on-disk binary every 5
seconds — running it with `--version` and comparing the answer against the
version running in this process (see
[`selfversion.go`](../local-representative/selfversion.go)) — to notice a
newer build has already landed. While it hasn't, the control reads plain
**restart**; once it has, the control turns orange and reads **update and
restart** instead, so pressing it visibly means "come back up on the newer
build" rather than just "come back up". Polling `--version` keeps this
consistent with every other version check in the codebase, rather than
introducing a second, file-mtime-based notion of "changed".

`agent-coordinator` runs the identical poll-and-compare against its *own*
on-disk binary, independently of any LR (see
[`agent-coordinator/selfversion.go`](../agent-coordinator/selfversion.go), a
deliberate duplicate of LR's `selfversion.go` rather than a shared package):
it's a legitimate deployment to run `agent-coordinator` alone on a box with
only off-node LRs, so AC can't lean on any LR's self-version watch to detect
its own binary drifting. The verdict rides along in `self-info` (new
`loader_managed`/`update_available` fields, re-broadcast whenever it
changes rather than sent only once per connection) and drives the global
topology view's "restart AC" button the same way: plain **restart AC** until
an update lands on disk, then orange **restart and update AC**. (It's
labeled "...AC" rather than the bare **restart**/**restart and update** the
neighboring per-host control uses, and "restart LR"/"restart and update LR"
for the control above it in this view specifically, so the three restart
targets read unambiguously alongside each other — that one above still just
targets the selected host's LR, same as everywhere else.) That same
"agent-coordinator" section also has a **host update all** button, enabled
once one or more of the host's connected `federation-command`/`condoccer`/
`worker` instances are out of date per the poll below — pressing it sends a
restart only to whichever of that host's LR / AC is itself actually stale
(`lr-restart-app` when the LR's own `update_available` is set,
`ac-restart-app` when AC's is, both in sequence when both are), so one click
never restarts a process that's already running the latest build — see
[`condocs/initialDistributedDevelopmentImpls/Step4Prompt.md`](../condocs/initialDistributedDevelopmentImpls/Step4Prompt.md)
Revisions F and G.

Two more controls round out that same section (Revision J). A green
**rebuild all** button, enabled once one or more *connected* hosts' LRs have
a rebuild ready (same per-host condition as the rebuild button above, just
OR'd across every host instead of just the selected one), rebuilds only
those hosts when pressed — its neighboring auto-rebuild checkbox instead
reaches every connected host with a watched dev-repo at once, since
auto-rebuild is a standing setting rather than a one-shot action, and reads
checked only once all of them already have it on. And a blue-or-orange
**network update all** button generalizes **host update all** to the whole
network: enabled once any connected host's own LR is stale, or AC itself is
— pressing it restarts every stale, loader-managed host's LR, plus AC itself
if it's stale and loader-managed too, in one click.

Each of the **restart**/**update and restart** controls above — LR's own
system tab, `agent-coordinator`'s per-host mirror, and the global topology
view's "restart LR" control for whichever host is selected — has a
neighboring **auto-update** checkbox (see
[`condocs/initialDistributedDevelopmentImpls/Step5Prompt.md`](../condocs/initialDistributedDevelopmentImpls/Step5Prompt.md)
Revision E): switching it on makes that one host's LR restart itself the
moment `selfVersionWatch` next notices an update on disk — same effect as
pressing the button by hand, just without waiting for an operator — or, if an
update is already sitting there the instant the checkbox is ticked, right
away rather than waiting for the next 5-second poll. It's a no-op (and stays
unchecked and disabled) while that LR isn't loader-managed, same guard the
restart control itself is disabled on, and is settable remotely via
`__system:auto-update <on|off>` alongside `__system:auto-rebuild`. Like
auto-rebuild and auto-connect, it's carried across the very restart it may
have just triggered via `lrState.AutoUpdate` (`reststate.go`) — otherwise it
would silently turn itself back off after firing once. The global topology
view's "agent-coordinator" section additionally grows its own
**auto-update-all** checkbox alongside **network update all**, mirroring **auto-rebuild**'s
pairing with **rebuild all**: it sweeps every connected, loader-managed
host's toggle at once rather than the single selected host, and — unlike
**rebuild all**'s neighboring checkbox — also arms `agent-coordinator`'s own
auto-update alongside them (sending a dedicated `ac-set-auto-update`
WebSocket message, no host to target, mirroring `ac-restart-app`), so AC
restarts itself the moment its own `selfVersionWatch` next notices an update
too, rather than only ever coming back up via a manual **restart AC**. It
reads checked only once every connected host's toggle, and AC's own, already
have it on. AC's half is carried across its own restart the same way LR's
is, via `acState.AutoUpdate` (`agent-coordinator/reststate.go`) — see
[`condocs/initialShellsSessionManagerAndTheConversationalistImpls/Step1SubstepCPrompt.md`](../condocs/initialShellsSessionManagerAndTheConversationalistImpls/Step1SubstepCPrompt.md)
Revision D.

See [`ufa-loader/README.md`](../ufa-loader/README.md) for the full protocol
and flags.

The same 5-second poll-and-compare exists for LR's managed instances (`federation-command`, `condoccer`), independent of `ufa-loader` -- see
[`procman.go`](../local-representative/procman.go)'s `pollManagedVersions`. Instead of comparing an on-disk binary against the version compiled into the running process, it compares that on-disk binary's `--version` against whatever version the connected instance last reported (see "Versioning" below): a `terminate` + `launch` from LR's system tab is what "restart" is for a managed instance, so no loader is involved. This drives `agent-coordinator`'s global topology view, which draws an orange halo around a connected sub-application's green box once it's out of date this way — see
[`condocs/initialDistributedDevelopmentImpls/Step4Prompt.md`](../condocs/initialDistributedDevelopmentImpls/Step4Prompt.md) Revision D.

That comparison was spuriously true for `federation-command` alone: every
other sub-application answers `--version` with the bare version string
(`ufaversion.HandleVersionFlag`, matching what it self-reports over
representable's "version" message), but `federation-command` hand-rolls its
own `--version`/`-v` handling and prints a friendlier, name-prefixed
`"federation-command <version>"` for a human at a terminal — so the string
comparison in `pollManagedVersions` never matched even when the two sides
were the same build, permanently flagging FC as having an update available
on both the global topology and per-host system tab. Rather than drop that
human-facing prefix (and the `-v` short flag, which only FC supports, and
which two of FC's own tests assert on), `pollManagedVersions` now strips a
leading `"<binName> "` off the on-disk `--version` output before comparing —
see [`condocs/initialDistributedDevelopmentImpls/Step5Prompt.md`](../condocs/initialDistributedDevelopmentImpls/Step5Prompt.md)
Revision J.

Clicking a row on the per-host system tab's process table (LR itself or a
managed instance) now opens a small drill-down panel underneath the table
with that process's current (`version`) and pending (`pending_version`)
build version — the latter being exactly the on-disk string
`pollManagedVersions`/`selfversion.go`'s poll most recently observed,
surfaced for the first time rather than collapsed into the boolean
`update_available` alone. It reads "up to date" once `update_available` is
false, and clicking the same row again (or a different one) closes/switches
the panel, same toggle-select behavior as the topology view's host cards —
see Step5Prompt.md Revision J above.

Each managed instance row on the per-host system tab's process table now has
that button for real: **restart** (plain, always available, unlike self's
which is disabled unless loader-managed) — `terminateManaged` followed by
`launchManaged` for the same app (`restartManaged` in `procman.go`), delivered
as `__system:restart-managed <id>` over the same representable command
channel as `launch`/`terminate`. It reads **restart and update** instead,
turning orange, whenever `pollManagedVersions` has set that instance's
`update_available` — same visual language as every other restart control
here, just without a loader gating it. See
[`condocs/initialDistributedDevelopmentImpls/Step4Prompt.md`](../condocs/initialDistributedDevelopmentImpls/Step4Prompt.md)
Revision H.

LR's own **restart** — self's, from either `SIGHUP` or the system tab's
control — now also carries its *directly launched* managed sub-applications
through the restart rather than orphaning them: as its final act before
announcing and exiting, it terminates every currently-running instance it
launched itself (an `--auto-launch` entry or one started from the system
tab), same terminate-and-wait as `restartManaged` above but for all of them
at once (`terminateManagedForRestart` in `procman.go`). Which apps were
running is captured into `lrState.ManagedApps` (auto-launch-style
`"app"`/`"app:N"` tokens, see `runningManagedTokens`) *before* they're
terminated, and rides along in the same `UFA_LOADER_STATE` payload as
`auto-rebuild`/`auto-connect`; the replacement instance feeds it straight
into `cfg.autoLaunch` (taking precedence over whatever `--auto-launch` it
happens to be relaunched with, same as `auto-connect`), so its ordinary
auto-launch path relaunches exactly the sub-apps that were running — no new
code path, no UI change. This deliberately covers only LR-launched
instances: a future "manage-on-connect" instance — a sub-application that
merely connects to LR without LR having started it — isn't a child of LR's
process at all, so a restart leaves it running rather than terminating a
process it doesn't own; see
[`condocs/initialDistributedDevelopmentImpls/Step4Prompt.md`](../condocs/initialDistributedDevelopmentImpls/Step4Prompt.md)
Revision I.

`condoccer` (condocs v1) has been removed in favor of `condocv2`, per
[`switchToUfabuildImpls/Step1.md`](https://github.com/user-1/sloppo/blob/main/condocs/switchToUfabuildImpls/Step1.md).
LR no longer launches or proxies it: `--condoccer-port`/`--condoccer-root`
are still accepted but ignored, and a `condoccer` token in `--auto-launch`
or in an older instance's restart state is skipped with a log line rather
than failing the launch.

## Dev-repo watcher

[`--dev-repo`](../local-representative/repowatch.go) — Step 3 of
[`InitialDistributedDevelopment`](../condocs/InitialDistributedDevelopment.md)
— gives `local-representative` the ability to detect changes in its own
source repo and rebuild from them. It's a stronger form of `--dev-mode`:
`--dev-repo` sets dev-mode automatically and additionally marks the *current
working directory's* git repository as watched (LR refuses to start with
`--dev-repo` if it isn't launched from inside one — `git rev-parse
--show-toplevel` has to succeed).

The watched repo runs the **ufabuild paradigm** — the same gitignored
`.ufabuild` requests plug-in dev repos use (see "Plug-in dev repos" below)
— per
[`switchToUfabuildImpls/Step1.md`](https://github.com/user-1/sloppo/blob/main/condocs/switchToUfabuildImpls/Step1.md),
which replaced both the old "HEAD moved" watch and the committed-`.ufabuild`
"ufabuild watch" (both described in earlier revisions of this document). A
watched repo is polled every 5 seconds (see `local-representative/repowatch.go`
and `ufabuild.go`):

1. **Pull.** If the repo is clean, LR checks whether the upstream branch has
   moved (`git fetch` + `git rev-list HEAD..@{u}`) and, if so, brings the
   repo forward with `git pull --rebase` (a conflicting rebase is aborted,
   leaving the branch where it was).
2. **Scan.** Whenever HEAD moves — by that pull, a branch switch, or a
   commit made by hand — LR scans the commits it moved over for a condoc
   tag, `[condocv2-xxxx]`, which condocv2 puts on every commit holding an
   agent's work (see "condoc tag" under "Plug-in dev repos"). The HEAD LR
   starts on counts as already scanned (a restart carries the scan point
   over in `lrState.UfabuildScanned`), so commits that were already there
   don't ask for a build on that count.
   **Catch-up.** Once per HEAD, with no request waiting, LR also checks the
   build record (`.ufabuild-built`, below): if a sub-app has changed since
   the commit it was last built from, and a tagged commit since then changed
   it, LR writes a request after all. So a tagged commit the scan missed
   (one that landed while the watcher was stuck, or before an LR that
   started without a scan point to carry over) still gets built. An untagged
   change still never asks for a build on its own, and a sub-app with no
   recorded build here isn't caught up on.
   Every git command the watcher runs is time-limited (30 s; 2 minutes for
   `fetch` and `pull`) and never prompts, so a stalled fetch can't freeze
   the watcher.
3. **Request.** A tagged commit makes LR write `.ufabuild` at the repo root,
   naming the new HEAD (`commit:`), the tag (`tag:`) and the tagged commit
   (`tagged:`). It's **gitignored** (`/.ufabuild` in the root
   `.gitignore`) and never committed. `scripts/ufabuild.sh` writes one by
   hand for the current HEAD (on this checkout only).
4. **Build.** Only a request makes the rebuild button ready; HEAD moving
   on its own never does.

The button keeps its old symbology:

- **dirty** (`git diff HEAD` is non-empty — modified unstaged or staged
  changes; untracked files, `.ufabuild` among them, don't count) — orange,
  reads **dirty**, and stays disabled: rebuilding a dirty tree would
  silently bake in uncommitted, unreviewed changes.
- **rebuild** — green and active while a `.ufabuild` request is waiting on a
  clean repo ("when it's needed").
- otherwise active with neutral/grey styling: no request since the last
  successful build, so selecting it rebuilds manually.

The panel's `.ufabuild:` line shows the pending request's commit
(`ufabuild_request`), or why there isn't one (`ufabuild_note`): none yet,
an upstream the last poll couldn't fetch or pull, a failed scan, a tracked `.ufabuild` left over from the old committed
paradigm (ignored), or that the request's last build failed.

Pressing the button (or `agent-coordinator`'s `__system:rebuild`) builds
just the sub-apps the request's commits changed, with `make
update-dev-binaries APPS="…"` at the repo root, and then restarts just
those (see "Per-sub-app builds" below). The button reads **building…** and
is disabled meanwhile. A successful build removes the request. A failed one
leaves it in place, so pressing **rebuild** retries it.

The **auto-rebuild** toggle next to it (also settable remotely via
`__system:auto-rebuild <on|off>`, and carried across restarts) makes LR
press that button itself — but not the instant a request lands. Instead it
starts a 90-second debounce timer, shown next to the toggle as **rebuilding
in Ns**; if a further change lands before the timer runs out (a newer
request from another tagged commit, or HEAD moving again), the timer is
bumped back to the full 90 seconds. The rebuild only actually happens once
the timer reaches 0 with auto-rebuild still on and the request still
waiting. This keeps a burst of condoc commits landing on the watched repo
from triggering a rebuild per commit. Turning auto-rebuild off, the repo
going dirty, or the request going away cancels the pending timer.
Auto-rebuild doesn't retry a request whose build failed; a newer request
(or the button) does.

### Per-sub-app builds

Per
[`switchToUfabuildImpls/Step1.md`](https://github.com/user-1/sloppo/blob/main/condocs/switchToUfabuildImpls/Step1.md)
Prompt 4, the watched (core) repo no longer rebuilds and restarts
everything for every request (see `local-representative/ufaplan.go`).
Plug-in dev repos still build with `make deploy-dev-binaries`.

**The build record.** Both `make deploy-dev-binaries` and the new `make
update-dev-binaries` write `.ufabuild-built` into the dev bin dir
(`/AI-evo1-dev/bin/.ufabuild-built`). It has one `<sub-app> <commit>` line
per sub-app, giving the commit that sub-app was last built from
(`scripts/record-dev-build.sh`). The record is written into the staging dir,
so it is swapped in with the binaries it describes. A failed build leaves
both untouched.

**The plan.** For a request, LR works out which sub-apps need building.
The sub-apps are the root Makefile's `SUBPROJECTS` (`make
list-subprojects`), minus the libraries, whose Makefiles have no
`BINARY_NAME`. A sub-app is planned when any of these is true:

- it has no line in the record, or its recorded commit isn't in the repo;
- a file under its **inputs** differs between its recorded commit and HEAD
  (`git diff --name-only --no-renames`). Its inputs are its own directory
  plus every local module its `go.mod` replaces in, followed transitively
  (`ufa-version/`, `ufa-hostid/`, `representable/`, `ufa-loader/` for
  `restartsignal`, …). So a change to a library plans every sub-app built
  on it, and never the library itself;
- the root `Makefile`, `scripts/compute-version.sh` or
  `scripts/record-dev-build.sh` changed: these are build-wide inputs, so
  every sub-app is planned.

`_test.go` and `.md` files never count, because they never reach a binary.
The first build after switching to this paradigm finds no record, so it
plans everything once. After that, builds are incremental.

**The build.** `make update-dev-binaries APPS="condocv2 the-conversationalist"`
builds the named sub-apps in `SUBPROJECTS` order. It rejects an empty or
unknown `APPS`. Like `deploy-dev-binaries`, it holds the `.building` lock and
builds into `/AI-evo1-dev/bin.new`. The difference is that the staging dir
starts as a copy of the current dev bin dir, so every binary not named stays
exactly as it was, version included. LR passes `DEV_BIN_DIR` (from
`$AI_EVO1_DEV_BIN`, default `/AI-evo1-dev/bin`). If LR can't work out a
plan at all (no `list-subprojects` target, as on a branch from before this
change, or a git failure), it falls back to a full `make
deploy-dev-binaries`. If the plan is empty (say the tagged commits only
touched docs), the request is consumed without building or restarting
anything.

**The restart.** Once a build passes and the binaries are swapped in, LR
restarts only what it has to (`Server.restartAfterCoreBuild`):

- **LR itself was rebuilt, or the build was full:** LR restarts through
  ufa-loader. As always, that terminates every managed app and relaunches it
  from the new binaries, so the others restart too. The restart runs last,
  after the request is consumed. The state it carries over (`lrState.LastBuild`)
  lets the new LR still show what the build did. An LR that isn't
  loader-managed can't come back up, so it is left to the operator, and the
  note says so.
- **Otherwise:** each *running* managed instance launched from a rebuilt
  binary is restarted (terminate, then relaunch, in parallel), and nothing
  else is. The managed app is matched by its binary, so `the-conversationalist`
  restarts `convo#1`. A rebuilt sub-app with nothing running (a CLI like
  `dtt` or `clod`, or an app that isn't launched) needs no restart: its next
  run picks the new binary up. An instance run from an override such as
  `--fc-bin` isn't the binary just built, so it is left alone.
- `ufa-loader` is never restarted from here, since it is what runs LR. A
  rebuilt one is picked up the next time LR is launched from a shell.

These restarts happen on every successful core build, whether auto-rebuild
or the button started it. **auto-update** now only matters for a new LR
binary that lands some other way, such as a `make` run by hand.

**Signals** (`repo-state`, from LR to its dashboard and on to
agent-coordinator, which relays them unchanged as `lr-repo-state`):

| field | meaning |
| --- | --- |
| `build_plan` | the sub-apps the pending request would rebuild, in `SUBPROJECTS` order. It is re-planned whenever HEAD, the request or the record changes, and is empty when there's no request |
| `build_plan_note` | set when the plan isn't just a list: nothing changed (the request will be consumed without building), or it couldn't be worked out (a full `deploy-dev-binaries` will run) |
| `build_apps` | the sub-apps the running build covers (while `building`), and afterwards the most recent build's. When `build_failed` is set, these are the sub-apps whose build failed |
| `build_full` | that build was a full `deploy-dev-binaries`, covering every sub-app |
| `build_managed` | the names `build_apps` show as on the system tab and the timeline. This is the managed app a sub-app's binary runs as (`convo` for `the-conversationalist`), or the sub-app's own name when nothing runs from it (`local-representative`, `dtt`) |
| `restarted` | what the last successful build restarted: managed instance ids (`convo#1`), or `local-representative` when LR's own restart covered everything |
| `restart_note` | a readable summary: what was restarted, what failed to restart, which rebuilt sub-apps weren't running, or why LR wasn't restarted |

`build_apps`, `build_full` and `build_managed` are cleared for a build LR
didn't start, such as `make update-dev-binaries` run by hand, because LR
doesn't know what such a build covers. `restarted` and `restart_note` are
cleared when a build starts. All five carry across LR's own restart.

In the dev-repo panel (on LR and on AC's per-host system tab), under the
`.ufabuild:` line:

- **next build:** shows `build_plan`, or its note, while a request is
  waiting. The rebuild button's tooltip says the same.
- **building:** shows `build_apps` while a build runs.
- **last build:** shows what the last build covered, with its
  `restart_note`.

On **agent-coordinator's timeline**, a per-sub-app build draws only the
rebuilt sub-apps' tracks building (green marching ants). These are the
tracks named in `build_managed`. LR's own fat laser is among them only when
LR itself was rebuilt. A rebuilt sub-app with nothing running (`dtt`, say)
gets its own building track while it builds. A failed build marks those
same tracks red. A full build, a build run by hand, or an LR from before
this change still draws LR's laser alone, as before. The restarts then show
as the usual restart and version marks on exactly the tracks that were
restarted.

LR's laser also carries the repo watcher's state on the timeline. While
`dirty` is set it gets the `tl-dirty` halo (the `tl-fresh` halo in orange),
whatever its state, and dirty outranks fresh: the green halo isn't drawn
over dirty stretches. While a `.ufabuild` request waits for its rebuild, the
laser itself is `tl-state-pending` (the `tl-state-building` ants in orange).
That's `rebuild_ready`, which already excludes dirty, except while a build
runs, and except when the waiting request is the one whose build failed
(`ufabuild_failed`): it then waits for a manual retry, and the red failed
state says so. The right-edge tooltip shows the `auto-rebuild in ~Ns`
countdown only while one is brewing. A new `.ufabuild` request draws a
`rebuild_requested` tick on LR's laser; hover it for the request.

The per-sub-app build and restart flow is:

```
[condocv2-xxxx] commit pulled
  → .ufabuild request                 (ufabuild_request; build_plan previewed)
  → 90s debounce / rebuild button     (auto_rebuild_pending, auto_rebuild_seconds)
  → make update-dev-binaries APPS=…   (building, build_apps, build_managed; .building lock)
  → record + swap into the dev bin dir
  → restart: LR (and so everything) if LR was rebuilt, else just the rebuilt running instances
                                      (restarted, restart_note)
  → 30s settling, building clears
```

To move a host onto a condoc's branch, switch its dev branch (below) to it:
the switch moves HEAD over the condoc's tagged commits, which writes a
request like any pull does.

Below that, a **dev-branch** row shows the branch the watched repo has
checked out (the branch the poll above follows the upstream of) with a
**switch** button that opens a picker over every local and remote-tracking
branch and runs `git switch <branch>` (also settable remotely via
`__system:switch-branch <branch>`). A switch is refused while the repo is
dirty or a build is running, with the reason shown under the row. A switch
moves HEAD the same way a pull does, so its tagged commits are scanned for
a request like any other change. The row
appears only in per-host system tabs. The global view gets its own
**dev-branch** control under **rebuild all**, which reads `<differs>` when
the watched hosts are on different branches and switches every one of them.

Below the dev-branch row, an **external** section registers directories
beside the watched repo for watching, per
[`privateRepoOpsImpls/Step6Prompt.md`](https://github.com/user-1/sloppo/blob/main/condocs/privateRepoOpsImpls/Step6Prompt.md)
Revision C. The first one is the `HyenaExternalUfaPlugin` checkout at
`../HyenaExternalUfaPlugin`. Type a path (relative to the watched repo's
root, or absolute) and press **watch** (or send
`__system:watch-external <path>`). The directory has to exist already, and
can't be the watched repo or anything inside it. Each registered path then
gets a row showing its branch, short HEAD, whether it's dirty and when it
last changed, plus an **unwatch** button (`__system:unwatch-external <path>`).
A directory that isn't the root of its own git repo is still listed, with a
note saying so. Registrations are saved per watched repo in
`external-watches.json` beside the plug-in approvals (`--external-watches`
to move it, empty for memory only), so they survive restarts. A plain
registration only watches: it never fetches, pulls, builds or relaunches
anything for the path (see `local-representative/extwatch.go`).

**Plug-in dev repos** (Revision D) are where the revised `.ufabuild`
paradigm is enabled first. Press **plugin dev repo** with the path box empty
and `../HyenaExternalUfaPlugin` is registered as one, in one press; type a
path to register another (or to turn a watched path into one), and an app
id to override the one derived from the directory's name
(`HyenaExternalUfaPlugin` is `hyena-external-ufa-plugin`). From
agent-coordinator that's `__system:watch-external-plugin <path> [app-id]`.
Registering one approves the plug-in's app id on this host straight away,
just as a manual registration on the plugins tab does. After that, each
poll (see `local-representative/extbuild.go`):

1. **Pulls** the repo's upstream: fetch, then fast-forward only. It doesn't
   pull a dirty tree, a detached HEAD, a branch with local-only commits, or
   while a build is running there. The row says why when it doesn't.
2. **Scans** the commits HEAD moved over, whether the pull or something else
   moved it, for a **condoc tag**: `[condocv2-xxxx]` (four lowercase
   alphanumerics). Every commit holding an agent's work carries one after
   any conventional-commit prefix, for example
   `feat: [SRJ-3456] [condocv2-g6jc] Message`. condocv2 puts it on each
   target's interaction commit
   (`condocv2: [condocv2-g6jc] <Name> commit <n>`). condocs v1's
   `[condoc-xxxx]` tag no longer counts: condoccer and federation-command's
   v1 condocs are gone.
3. **Writes `.ufabuild`** at the repo's root when a tagged commit came in,
   naming the commit to build (the new HEAD) and the tag that asked for it.
   In this paradigm `.ufabuild` is **gitignored by convention**: a local
   request, never committed. Plug-in repos should list `/.ufabuild` in
   their `.gitignore`. If one doesn't, the watcher adds it to the repo's
   `.git/info/exclude` itself. It refuses a repo where `.ufabuild` is
   tracked.
4. **Builds** whenever a `.ufabuild` is present, whether written by step 3,
   by the row's **build** button (`__system:build-external <path>`, a
   request for the current HEAD), or by hand. It runs
   `make deploy-dev-binaries DEV_PLUGIN_DIR=… DEV_PLUGIN_BIN=… PLUGIN_APP_ID=…`
   at the repo's root. That target is the plug-in repo's hook: it builds the
   plug-in to `$(DEV_PLUGIN_BIN)` and nothing more (see "Plug-in lifecycle"
   below). Until the repo implements it, builds fail, and the row shows why.
5. **Removes the request** once its build has run, whether it passed or
   failed, so a failed request isn't retried until another one lands. After
   a successful build it deploys the binary, approves the app id again (so
   the plug-in's hello goes online at once) and restarts the plug-in if it's
   running.

The registration also saves the last HEAD it saw, so tagged commits pulled
in while LR was down still get built after a restart. Unlike the watched
repo itself (above), a plug-in dev repo builds a request straight away,
without auto-rebuild's debounce.

Revision E brings the external section to agent-coordinator too. The
per-host system tab's dev-repo panel has the same rows, **watch**, **plugin
dev repo**, **build** and **unwatch** controls, sent on as the
`__system:` commands above. Because `watch-external-plugin` splits its path
from its app id at the first space, AC won't send a plug-in dev repo whose
path or app id contains a space. Each plug-in dev repo's row links to its
**registration** (on LR's own dashboard as well as AC's). The link opens the
plugins tab's registration sub-tab and scrolls to and briefly highlights
that repo's row in a new **plug-in dev repos** table. The row shows whether
the app id is approved on the host, the plug-in's registry status and its
last build. In the global topology view, each host card gets a row of
**plug-in tiles** below its agent tiles, one per registered external
plug-in: one that has said hello and is approved, or a plug-in dev repo's
plug-in even before it says hello. A tile is green while the plug-in is
online and red after a failed build. While a plug-in dev repo is building,
its tile wears the card's marching-ants halo. For 10 minutes after its
managed instance is launched or restarted (including the restart after a
build), it wears the card's green "fresh" halo. Double-clicking a tile opens
that plug-in's own tab in that host's plugins tab, or its row on the
registration sub-tab if it hasn't said hello on that host yet (Revision G).

**Plug-in lifecycle** (Revision F, see `local-representative/extlaunch.go`).
A plug-in dev repo's plug-in is a managed app under its app id, like the
in-repo cats-ufa-plugin:

- **Deployment** is LR's job, not the repo's. Builds are deployed to
  `/AI-evo1-dev/plugin/<app-id>` (`$AI_EVO1_DEV_PLUGIN` moves the
  directory). `make deploy-dev-binaries` is handed
  `DEV_PLUGIN_BIN=/AI-evo1-dev/plugin/.staging/<app-id>` on its command
  line and builds the plug-in there. Once it passes, LR moves the staged
  binary into place. A failed build, or one that built nothing at
  `DEV_PLUGIN_BIN`, leaves the deployed binary as it was.
- **Launch:** while no instance is running, the repo's row (on LR and on
  AC's per-host system tab) offers **launch**. It is enabled once a build is
  deployed and is the same as `__system:launch <app-id>`. The plug-in runs
  with cats-ufa-plugin's arguments: `--lr-host`, `--lr-port`, `--port` (a
  free loopback port picked at each launch), `--name <app-id>` and
  `--dev-mode` when LR has it, plus `UFA_LAUNCH_MODE` in its environment
  (see "Cascading from local-representative"). It shows in the managed list, where it is
  terminated, and like any managed app it is relaunched when LR restarts.
- **Restart:** each successful build restarts a running instance, and the
  row notes it. A plug-in that isn't running isn't started by a build.

An app id can't be one of LR's own managed apps, nor another plug-in dev
repo's.

The old launch-time **ufabuild watch** (`--ufabuild-watch` /
`ufabuild-watch: true`, per
[`SlopspaceSlopspaceCondocs`](https://github.com/user-1/sloppo/blob/main/condocs/SlopspaceSlopspaceCondocs.md)
Step 2), which built a *committed* `.ufabuild` naming its own parent commit
straight away, is gone: the watched repo always runs the paradigm above
now. The flag is still accepted so existing launch scripts and
`local-representative.yaml` files keep working, but it does nothing. A
`.ufabuild` still tracked on an older branch is ignored, and the panel's
`.ufabuild:` line says so.

Every git/make invocation the watcher makes — the poll's own status check,
the pull, and a rebuild, whether triggered by the operator or by
auto-rebuild — goes through one mutex, so **the check-and-rebuild process is
single-threaded**: nothing here ever runs concurrently with itself, per the
prompt.

`make deploy-dev-binaries` itself creates and removes a `.building` lock
file at the repo root — gitignored, since it's a purely local, ephemeral
marker with nothing for other nodes to learn from (see
[`condocs/initialDistributedDevelopmentImpls/Step5Prompt.md`](../condocs/initialDistributedDevelopmentImpls/Step5Prompt.md)
Revision I). The watcher detects rebuild completion off this file's
presence rather than solely off its own `make` invocation returning, so a
build already running when LR starts (e.g. one that outlived an auto-update
restart) or one kicked off by hand outside LR is picked up correctly too.
Once the lock file disappears, the button keeps reading **building…** for
an additional 30-second settling period before actually clearing — "to help
eliminate race conditions" against anything still settling on disk right as
the lock is removed.

`make deploy-dev-binaries` builds every sub-project's binary into a
scratch staging directory (`/AI-evo1-dev/bin.new`) and only swaps it into
the real dev bin dir (`/AI-evo1-dev/bin`) once every sub-project has
deployed successfully — it never wipes the real dev bin dir up front the
way it used to (Revision L). A failed or interrupted build (a compile
error partway through the sub-project loop, or the recipe getting killed
outright) now simply leaves the previous, still-working dev bin dir
untouched instead of leaving it half-emptied for `ufa-loader` to relaunch
from.

If a rebuild does fail, the **system tab**'s "trough" — a collapsed single
line at the bottom of the panel that expands into a scrollable log on
click — records it (Revision L). It's a live, session-scoped notification
log fed by `RepoStateMsg.last_error`; agent-coordinator's per-host system
tab mirrors it the same way it mirrors the rest of that panel, and its
global system tab aggregates every connected host's failures into one
trough, each entry tagged with the host it came from.

LR forwards its watcher snapshot up to `agent-coordinator` as `repo-state`,
and `agent-coordinator`'s own per-host system tab renders the identical
rebuild/dirty/auto-rebuild panel from it — the **rebuild** button and
**auto-rebuild** toggle there drive the selected host's LR the same way LR's
own dashboard does (`__system:rebuild` / `__system:auto-rebuild <on|off>`).
It's only ever shown for a host whose LR is actually watching a repo
(`watched: true`), i.e. one launched with `--dev-repo` — an ops-mode LR never
shows it, so the trigger stays dev-mode-only regardless of which dashboard
it's driven from.

Revision O/P: investigating a report that the trough and the nav arrows
(see "Screen history" below) were non-functional on `agent-coordinator` while
working on `local-representative` turned up two real, `agent-coordinator`-only
bugs, both now fixed. First, its own `RepoStateMsg`/`LRRepoStateMsg` structs
(and the `repoStateMsg()` mapper between them) had fallen out of sync with
local-representative's and were missing the `condoc_locked` field entirely,
so `json.Unmarshal` silently dropped it and the repo-watch panel's "condoc"
label could never surface there — the same class of bug as Revision K's
missing `AutoUpdate` field. Second, and the actual cause of the nav arrows
looking dead: `agent-coordinator`'s mobile off-canvas-drawer toggle
(`.mobile-nav-toggle`/`.mobile-back-btn`) was copied from condoccer's pattern,
which has no header to clear, and from local-representative, which has no
such toggle at all — so nobody had noticed that at viewport widths under
760px it sits `position: fixed; top: 10px`, right on top of `agent-coordinator`'s
own `.app-header` (nav arrows + connection dot), swallowing clicks meant for
them. It now sits below the header (`top: 48px`, with `.app-body`'s
reserved padding grown to match) instead of on top of it. The trough itself
traced out clean end-to-end (LR's rebuild failure → `repo-state` relay →
`agent-coordinator`'s backend → `lr-repo-state` broadcast → the `Trough`
component) with no defect found; its collapsed line is legitimately disabled
until a rebuild actually fails on that host, on both dashboards alike.

Revision Q: the mobile-drawer occlusion above only ever hid the arrows below
760px, but the report was that desktop viewports of `agent-coordinator` look
like there's *nothing* between the `AGENT-COORDINATOR` title and the
connection dot at all. Unlike `local-representative`, whose header has a row
of visible tab buttons filling that space, `agent-coordinator`'s header has
nothing between the (flex-grown, so mostly blank) title and the two small,
grey, often-disabled arrow glyphs -- with no bordered/backgrounded affordance
of their own, they were easy to read as empty background rather than a
control. `.nav-arrows` now has its own subtle bordered pill (matching the
app's existing dark-theme control-grouping style), so the arrow pair reads as
a distinct, always-visible control regardless of viewport width or whether
either direction is currently enabled.

```bash
./local-representative --dev-repo   # from inside a checkout of this repo
```

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
(the shortsha moved). `make update-dev-binaries` (see "Per-sub-app builds")
gives a new version only to the sub-apps it builds; every other binary keeps
the version of the commit it was last built from, so "update available"
flags and timeline version marks only appear on what was actually rebuilt.
Outside a git checkout (e.g. a Docker build that
doesn't `COPY` `.git`) it falls back to `dev`, same as a plain `go
build`/`go run .` with no `-ldflags` at all.

Every sub-application binary answers `--version` by printing its version and
exiting instead of starting up (`ufa-version.HandleVersionFlag`; `ufa-loader`
handles it separately since its own argv is followed by a wrapped binary's —
see [`ufa-loader/README.md`](../ufa-loader/README.md)). A few sub-applications
also surface it somewhere more visible:

- **`federation-command`**: the `version` / `ufa version` shell commands.
- **`local-representative`**: next to its own name on the system tab.
- **`condoccer`**: a subtle annotation next to the "Condoccer" banner in the
  sidebar.

`federation-command` and `condoccer` also report their version to
`local-representative` once they connect over `representable` (a "version"
data message, mirroring how `condoccer` already reports its condoc summary
the same way) — LR lists it next to each managed instance on the system tab,
same as its own. The report itself is one-shot on connect, not a live poll: a
managed instance's version tag reflects whatever build it was launched with,
until it reconnects. LR does separately poll the *on-disk* binary for that
application every 5 seconds and compares it against the last-reported tag —
see the "Loader" section above.

Every version tag on LR's system tab (its own and each managed instance's)
rides along in the `system-state` LR already forwards to `agent-coordinator`,
so `agent-coordinator`'s per-host system tab shows the same version column
without any protocol addition of its own. `ProcInfo`'s `pending_version`
field (Step5Prompt.md Revision J, see "Loader" above) rides the same
message: the one addition needed to also surface a process's *pending*
version, not just whether one is available.

## Browser refresh

The rebuild pipeline above already guarantees the *server* is serving fresh
content the moment `make deploy-dev-binaries` and `ufa-loader`'s restart land
— see "Dev-repo watcher" and "Loader". None of that touches a tab that was
already open before the restart: it has old JS sitting in memory and has no
reason to re-fetch anything on its own. Each frontend (`local-representative`,
`agent-coordinator`, `condoccer`) closes that gap the same way, per
[`InitialDistributedDevelopment`](../condocs/InitialDistributedDevelopment.md)
Step 5 Revision B (design writeup:
[`BrowserRefreshStrategy.md`](../condocs/initialDistributedDevelopmentImpls/BrowserRefreshStrategy.md)):

- Each `vite.config.ts` bakes the same repo-wide version string
  `scripts/compute-version.sh` produces into its bundle as `__APP_VERSION__`
  (`define`), so frontend and backend built from the same commit always agree
  bit-for-bit — no separate frontend build-id scheme.
- Each server already discloses its own running `ufa-version.Version` to a
  freshly-connected browser client — `local-representative` on `self.version`
  within `system-state`, `agent-coordinator` and `condoccer` on `self-info`'s
  `version` (see "Versioning" above).
- Each frontend's WebSocket `onmessage` handler compares that reported
  version against its own `__APP_VERSION__` every time it arrives and calls
  `window.location.reload()` on a mismatch — no banner, no button, since
  (unlike restarting a process) reloading a page isn't disruptive. A server
  restart necessarily drops the WebSocket, so the reconnect that already
  happens ~2s later re-triggers this check for free; a same-process
  reconnect (laptop sleep/wake, wifi blip) reports the same version and does
  nothing.
- The check is skipped entirely under the Vite dev server
  (`import.meta.env.DEV`): HMR already keeps that tab current, and the Go
  server it proxies to is typically run unlinked (`go run .`, version `dev`)
  so the two are never expected to agree there.

## Browser pickup

A reload — whether triggered by the mismatch check above or done by hand —
used to always drop the viewer back at the condoc list, even if they'd been
looking at one particular step/iteration/diff. Per
[`InitialDistributedDevelopment`](../condocs/InitialDistributedDevelopment.md)
Step 5 Revision C (design writeup:
[`BrowserPickupStrategy.md`](../condocs/initialDistributedDevelopmentImpls/BrowserPickupStrategy.md)),
each frontend now resumes on the same view instead:

- **`condoccer` (Layer 1)**: its nav state (selected condoc/step/iteration,
  and any open diff/file/hunk) is mirrored into `location.hash` with
  `history.replaceState` — never `pushState`, so this is a pure resume
  mechanism and doesn't grow browser history — and re-parsed to seed that
  same state on mount. A URL *fragment* is used rather than a real path
  because `condoccer/vite.config.ts` sets `base: './'` so its assets resolve
  correctly whether served at `/`, `/condoccer/`, or
  `/host/<id>/condoccer/`; a fragment never reaches the server, so the
  SPA-fallback handler keeps serving `index.html` unchanged regardless of
  where in a condoc the hash points. Once the WebSocket is open, a hash-seeded
  condoc/diff is (re-)subscribed/fetched exactly as a click would; if the
  *server* explicitly rejects the subscribe before any state ever loaded
  (the condoc named in the hash was renamed/reverted/deleted since),
  condoccer falls back to the condoc list and the stale hash is cleared the
  same way. This is deliberately keyed off the server's own rejection, not
  any transport-level WebSocket hiccup — a reconnect flaking while a host is
  still coming back up (e.g. mid auto-update restart) must not be mistaken
  for staleness and bounce the viewer off a perfectly valid deep link.
- **`local-representative` and `agent-coordinator` (Layer 2)**: condoccer is
  always embedded via a same-origin iframe with a hardcoded `src`, so its own
  Layer 1 resume can't survive a reload of the *outer* page — the iframe just
  remounts at the bare `src`. Both dashboards capture the iframe's
  `hashchange` events and bake the last-seen hash back into the iframe's
  `src`, and separately remember their own active tab (`agent-coordinator`
  also remembers the selected host) — all via `sessionStorage`, not
  `localStorage`: it survives a reload, stays scoped per browser tab (so two
  tabs open on different condocs/hosts don't clobber each other), and clears
  once the tab actually closes rather than pinning stale state forever.
  `history.replaceState` never fires a `hashchange` event on its own (unlike
  assigning `location.hash` directly), so condoccer dispatches one by hand
  right after each `replaceState` call — otherwise the embedder's listener
  would only ever see the hash as of the iframe's initial load, and a later
  reload of the *outer* page (e.g. `agent-coordinator`'s own auto-update
  reload) would hand the iframe back that stale, initial hash instead of
  wherever the viewer had actually navigated to since (Step 5 Revision G).

## Screen history

Per [`InitialDistributedDevelopment`](../condocs/InitialDistributedDevelopment.md)
Step 5 Revision M, both `local-representative` and `agent-coordinator` show a
pair of forward/back nav arrows in the upper right corner of their header, next
to the connection dot. They step through a small in-app stack of recently
visited "screens" — up to `NAV_HISTORY_MAX` (currently 20, a pragmatic
starting value) — and are only illuminated (clickable) in the direction that's
actually available.

This is a from-scratch, in-app history stack (`useScreenHistory` in each
`App.tsx`), not a wrapper around the real browser back/forward buttons: this
app deliberately never calls `history.pushState` (see "Browser pickup" above —
`replaceState` was chosen specifically to avoid growing browser history), so
there's no native forward/back to hook into. A "screen" is scoped to each
app's own top-level navigation state, not every nested selection:

- **`local-representative`**: a screen is the active tab (`federation-command`
  / `condoccer` / `worker` / `system` / `files`).
- **`agent-coordinator`**: a screen is the pair (selected host, or global; active
  tab), since those two together are what `agent-coordinator` otherwise
  persists via `sessionStorage` for browser-pickup purposes.

Navigating normally (clicking a tab, picking a host) truncates any forward
history beyond the current point and pushes the new screen, same as browser
history; clicking a nav arrow replays a previously-visited screen without
pushing a new entry. Deeper drill-down state — `files`' selected file/open
viewer, or condoccer's own internal step/iteration/diff position — isn't part
of the stack; it resets to that tab's default the same way it already does on
an ordinary tab switch. Widening the stack to cover those finer-grained moves
is possible but was left for a later revision, per the prompt's own "start
with a pragmatic value for N."

The arrows sit to the left of the connection dot and use the same grey
palette as the rest of the header outline — `#666` when unavailable, `#ccc`
(matching `.tab:hover`/`.tab-active`) when illuminated. The original
Revision M styling used `#444` for the unavailable state, which had almost
no contrast against the `#252526` tab-bar background and made the arrows
effectively invisible; Revision N raised that to `#666` so they're always
visible, with illumination still conveying availability (Step 5 Revision N).

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
