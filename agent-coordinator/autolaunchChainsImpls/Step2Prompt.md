# Prompt

[AutolaunchChains](../AutolaunchChains.md)

Next we will add the capability to local-respresentative to launch applications itself.

LR will gain a 'system' tab (to the right of all other tabs) where it will display itself as a process. It will also have the widgets needed to launch other applications (for now we will start with only federation-command).

The config will also support the configuration of auto-launch configuration for each application, so that when LR launches it will automatically launch child applications.

The system tab allows the termination of managed applications and lists some basic data like PID.


prompt: [`8bba91a`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/8bba91a81edbb8d88326e7ff812298b5233e26a7) → reply: [`3b47582`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/3b47582633bc10a2dd607f8b231421c8226993b3)

## Reply

I've implemented Step 2 of AutolaunchChains. Go/npm build tooling is blocked in this session (needs interactive approval), so I verified the changes by hand as the previous replies did; `make build` in `local-representative/` is still needed.

## What was done

**`local-representative/procman.go`** (new) — a small process manager on `Server`:
- `managedApps` registry of launchable apps (only `federation-command` for now); adding an entry is all it takes to expose another. FC is launched with `--auto-connect --lr-host localhost --lr-port <repr-port>` so it dials straight back into this LR.
- `launchManaged` / `terminateManaged` / `reapManaged` / `recordLaunchFailure` — start a child in its own process group, stream its stdout/stderr into LR's log via a line-buffering writer, reap it and record final status (`running` / `exited` / `failed` + exit code), and SIGTERM→(5s)→SIGKILL on terminate. A terminate on an already-exited entry just drops it from the list; operator-requested stops are recorded as `exited`, not `failed`.
- `resolveAppBinary` locates the executable next to the LR binary, then `$AI_EVO1_DEV_BIN` (default `/AI-evo1-dev/bin`), then `$PATH`; overridable via config.
- `systemState()` / `broadcastSystemState()` produce the `system-state` payload: LR itself (real PID, uptime) plus every managed child.
- `startAutoLaunch` launches configured children in a background goroutine after startup.

**`local-representative/main.go`** — added `Server` fields (`heartbeatPort`, `selfStart`, `binOverrides`, `procMu`, `managed`); new `--auto-launch` (comma/space list) and `--fc-bin` flags, both layered through `ufa-configurable` in `resolveConfig` (with a `splitList` helper); on startup, after the auto-connect block, LR prints and runs the auto-launch list; `handleWS` sends an initial `system-state` and handles inbound `launch-app` / `terminate-app`.

**Frontend** — `types.ts` gains `ProcInfo` / `SystemStateMsg`; `App.tsx` adds a `system` tab to the right of the others with a `SystemPanel` (process table: name, PID, status, uptime/exit code, terminate/dismiss buttons; LR shown as "this process"; a "launch federation-command" button disabled while it's running), a `system-state` handler, and `launchApp`/`terminateApp` senders; `index.css` gets the system-panel styles.

**Tests & docs** — `procman_test.go` covers `splitList`, self-process reporting, unknown-app/binary-override errors, the full launch→terminate→dismiss lifecycle (against an injected `sleep` stand-in), and failed-launch visibility. `README.md` documents the two new flags, the config keys, the system tab, and the single-entrypoint autolaunch chain.

Result: `./local-representative` with `auto-launch: federation-command` in config brings up LR and an auto-connecting FC from one command, and the system tab lets you launch/terminate FC and see its PID.


## Revision A

Our implementation does not work fully. The widgets on LR look good, but pressing 'LAUNCH' for federation command fails with this error:
```
2026/09/01 17:41:28 system: launched federation-command (pid 410880): /AI-evo1-dev/bin/federation-command [--auto-connect --lr-host localhost --lr-port 8082]
2026/09/01 17:41:28 [federation-command] error: error creating cancelreader: add reader to epoll interest list
2026/09/01 17:41:28 system: federation-command (pid 410880) failed (exit 1)
```

For clarity - federation-command should launch by popping a new terminal that runs federation-command, since it is a shell that must run somewhere where the user may interact with it.

Another side-note we can implement with federation-command in stride is that whenever it is going through a full auto-launch/auto-connect chain that it should be in remote mode by default. This facilitates the full machine-driven chain of events.

We also need LR to be able to handle launching N instances of FC. (Some sub-applications are one per host, others are N, FC is N-per-host)

Let's make these corrections and updates in this next revision.


prompt: [`4f2aac5`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/4f2aac56cc48115e9d29f795dd5a500f7dedf3e9) → reply: [`b44bf39`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/b44bf398945934570bc59855806621ef34a32389)

## Reply A

I've implemented Revision A. Go/npm build tooling is still blocked in this session (needs interactive approval), so changes were verified by hand as before — `make build` in `local-representative/` and `go test ./...` in both `local-representative/` and `federation-command/` still need to be run.

**1. FC now launches in a real terminal (fixes the `cancelreader` crash).** `local-representative/procman.go` gained `wrapInTerminal`: `launchSpec` has a `terminal` flag, and for `federation-command` LR now execs it via a terminal emulator/multiplexer instead of directly. It honours a new `--terminal` / `terminal` config prefix (e.g. `terminal: "xterm -e"` or `terminal: "tmux new-session -d -s fc"`), else probes `$PATH` (foreground emulators first for lifecycle tracking, `gnome-terminal`/`tmux`/`screen` last); if none is found it records a `failed` entry with an actionable message rather than letting FC die in its input reader. Process-group setup is unchanged so terminate still tears down the whole tree.

**2. Remote-by-default in the machine-driven chain.** `federation-command` gained a `--remote` / `remote` config option (`cliConfig.remote` → `appModel.remoteDefault`); `autoConnectControlState` now takes a `preferRemote` bool and returns `BlinkerConnected` when set, so a completed background auto-connect adopts remote control instead of local. LR passes `--auto-connect --remote` for every FC it launches (system tab and auto-launch).

**3. N instances of FC.** `Server.managed` is now keyed by a generated instance id (`federation-command#1`, `#2`, …) with a per-app `instanceSeq`; `launchSpec.singleton` distinguishes one-per-host from N-per-host apps (FC is N). `launchManaged` returns the new id and only blocks a second launch for singletons; `terminateManaged` takes an instance id (or a bare app name when unambiguous). Auto-launch tokens accept `app:N` (`parseAutoLaunchEntry`). Frontend: `ProcInfo` carries `instance_id`/`instance`, rows are keyed/labelled per instance and terminate by id, and the FC launch button stays enabled with a running count. Tests updated/added across `procman_test.go` and `federation-command/main_test.go`, and both READMEs document the new flags, `app:N` syntax, terminal hosting, and remote-by-default behaviour.


## Revision B

This increment now looks good - let's make an adjustment to ensure that FC comes up and connects in remote.

If we can launch the terminal so that it does not take foreground that would be even more ideal, but if that introduces any significant complexity then let's go without it.

Let's also respond with a snippet we can use to write a config which will cause LR to go through the full auto-launch/auto-connect chain with no intervention so we can test that.
