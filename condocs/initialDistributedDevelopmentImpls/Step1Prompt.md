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


## <REPLACE-Revision|Retry> A

<REPLACE-PROMPT>


## Human-Prompt

When you are done add the '!HANDOFF!' or '!COMPLETED!' directive.
