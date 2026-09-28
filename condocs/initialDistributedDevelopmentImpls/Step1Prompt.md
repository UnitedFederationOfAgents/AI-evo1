# Prompt

[InitialDistributedDevelopment](../InitialDistributedDevelopment.md)

We will add the ability to toggle 'dev mode' to all persistent sub-applications and document the features we'll add to our initial baseline.

The sub-applications that we will implement this function for include:
- federation-command
- local-representative
- agent-coordinator
- condoccer
- dungeon-keeper

The web-UI driven projects will present easily recognizable but visually subtle green borders around the major panel borders when launched in dev mode. (We like the greens chosen so far for things like 'healthy' and 'connected' and their palette)

The dev mode federation-command instances have the square brackets in the prompt coloured green (the ones enclosing the blinker).

To launch any of the applications in dev mode an argument may be supplied - at this point we do not want to introduce dynamic switching at runtime.

Whenever local-representative is launched in dev mode it will launch manages instances in dev mode as well. Dev mode and operations mode instances will refuse to other than the minimum amount necessary to exchange health information and to disclose the mismatch.

In the document we create we will record the following features which we will later implement:
- Loader: A go application which is responsible for launching a sub-application so that it may gain restart and version-switching capabilities
- LR updater: The ability for LR to instruct updates to connected sub-applications
- AC updater: The next hop in the chain, allowing update signals to be broadcast to a set of connected LRs
- Dev branch follower: A mechanism to follow a git branch of in-progress development and propose updates upon change
- The global view in agent-coordinator -- an alternative set of panels to the per-host views we have today which present a net-centric style of interaction
- The global system view in agent-coordinator -- the global mode panel which allows viewing of the working group of participants with health and version information. The location where we can push updates from.


prompt: [`b825101`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/b82510167d73a3ae901f797db259e0898d0713b1) → reply: [`15706dc`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/15706dcd94953636fefda3f6139ca12389b01e22)

## Reply

I've implemented the dev-mode feature end-to-end. Summary of what's in the working tree:

**Shared protocol** (`representable/representable.go` + new `representable_test.go`): every `Msg`/`ServerMsg` now carries a `Mode` (`dev`/`ops`); `Connect`/`NewServer` take a mode argument, and a mismatched pair automatically drops everything except heartbeats — the server's `hello` and the client's stamped `Mode` disclose the mismatch to both sides.

**All five sub-apps** get a new `--dev-mode` flag (deliberately distinct from the pre-existing `--dev` frontend-dev flag that LR/AC/condoccer already had):
- `federation-command`: green blinker brackets (`blinkerBracketDevStyle`), startup banner, and a printed warning on an LR mode mismatch.
- `local-representative`: cascades `--dev-mode`/`FC_DEV_MODE` to every `federation-command`/`condoccer` it launches, stamps `dev_mode` on its `system-state` `ProcInfo`, and broadcasts a `mode-mismatch` banner.
- `agent-coordinator`: same mismatch banner/badge treatment, plus a `self-info` message so its own frontend knows its mode.
- `condoccer`: same pattern against its single LR connection.
- `dungeon-keeper`: flag added to `watch`, logged at startup (no web UI to decorate).

Web UIs (LR/AC/condoccer) get a subtle green outline (`#4ec94e`, the existing "healthy/connected" green) around the app frame in dev mode plus small `dev` tags on dev-mode processes/hosts.

Documentation lives in the new `docs/DevMode.md`, which also records the future-feature list (Loader, LR updater, AC updater, dev-branch follower, AC global view, AC global system view) as planned-not-built, per the prompt. I added Go tests for the new representable behavior and the LR/FC config parsing, but couldn't run `go build`/`go test` myself — the sandbox blocks invoking the `go` binary — so this has only had careful manual review, not a compiler check.


## Revision A

The visual cues for the graphical applications with dev-mode should be more pronounced. It is too hard to recognize at a glance.

Let's correct them to make them more distinct.


prompt: [`4927592`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/4927592691b9b2142f66dba34806dfe2a67038fc) → reply: [`ddddf9d`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/ddddf9d4849472ce52b4dde3ed9b051cc1802b52)

## Reply A

I strengthened the dev-mode visual cue in all three web UIs (local-representative, agent-coordinator, condoccer): the app-frame outline changed from a faint 1px/45%-opacity green to a thick, fully-opaque 4px green outline with an inset glow, and the per-process `dev` tag is now a filled badge instead of just an outlined one. Updated `docs/DevMode.md` to describe the new styling and note the Revision A rationale.


## Revision B

This is still too subtle.

Let's adjust FC's prompt cursor (the '>') to be green as well, let's also make sure the '[]' is the correct bring green (the same as '◆ dev mode — launched with --dev-mode; the blinker brackets [ ] render green for the life of this session')

For the web-UI sub-applications we will make things more pronounced - we can consider on-palette greens for more border or highlight element alternatives and/or just brightening up the green we have their now.


prompt: [`09e138d`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/09e138df7642daaba4e82bae4a61eb8ff77b9c70) → reply: [`5afb181`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/5afb1817b3d998a28f7f91b3cc2d3e547ee8b796)

## Reply B

*Note: the agent session working this reply crashed before it could write one up — the summary below was reconstructed after the fact from the working tree it left behind (recovered in commit `c57d29a`, "condoc: recovery commit before resubmit") rather than authored live.*

Implemented Revision B: `federation-command`'s interactive prompt cursor (the trailing `> `) now renders in the same green as the blinker brackets, and both read off one new shared constant (`devGreen` in `federation-command/main.go`) so they can't drift apart — `blinkerBracketDevStyle` in `blinker.go` was switched to reference `devGreen` too, and `buildPrompt` gained a `devMode` argument threaded through every call site that rebuilds the prompt. For the web-UI apps (`local-representative`, `agent-coordinator`, `condoccer`), the dev-mode outline color moved from `#4ec94e` to the brighter `#6ec96e`, the inset glow was enlarged and made more opaque, and a fixed `DEV MODE` tag was added at the top-center of the viewport as a second, position-independent cue for when the outline itself has scrolled out of view; the per-process `dev` badge now uses the same brighter green. `docs/DevMode.md` was updated to describe all of the above and record the Revision B rationale.

As with prior sessions, this sandbox blocks `go build`/`go test`/`npm`, so this was verified by manual read-through rather than an actual compile/run.


## Step Completed

This step was completed at 1789914430 (Sun Sep 20 02:27:10 PM UTC 2026).
