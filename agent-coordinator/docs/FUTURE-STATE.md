# Agent Coordinator — Future State

This document is a planning space, not a committed design. It records the
prospective next steps for Agent Coordinator (AC) beyond the current
skeleton + single-connection baseline (see [architecture.md](architecture.md)),
along with the questions and decisions each one will need to resolve before
implementation. Nothing here is scheduled; increments will be pulled from
this list and refined as their turn comes up.

## Autolaunch chains

On startup, a full chain of participants should be able to connect without
manual wiring: AC can auto-launch and serve as the top-level participant for
a network, or auto-launch, serve *and* connect upward to a higher-tier AC
when it represents a lower-tier participant. LR can auto-launch, serve, and
connect to a specific AC. FC can auto-launch and connect to a specific LR.
The result is that starting a system reaches out and joins the full chain up
to the top-level AC of interest, without a human clicking "connect" at every
layer.

Open questions:
- How is the chain configured — env vars, a config file per app, flags
  passed down from whatever launches the whole stack?
- What does AC-to-AC do that's different from LR-to-AC? Does the current
  `representable.Server`/`Client` pair generalize directly, or does a
  mid-tier AC need to be both server (8084-equivalent) and client at once?
- Retry/backoff policy when the upstream target isn't up yet (common at
  boot, since order of process launch isn't guaranteed).
- How much of this should be "auto-launch the process" vs. "auto-connect an
  already-running process" — these are different problems (process
  supervision vs. network handshake).

## Coordinated distributed sessions

Sessions currently only exist on localhost and aren't exchanged over UFA.
Distributed sessions are the use-case most relevant to AC: replicating
session records and reports from other nodes into local agent-host-files.
Initially this will likely copy sessions verbatim; later increments will
make sessions "telescopically visible" — only the indexing files replicate
by default, with the fuller session data pulled on demand rather than fully
copied up front.

Open questions:
- What identifies a session across nodes (host + session id? a UUID minted
  at creation?), and how do we avoid collisions when the same id space is
  reused across hosts?
- What's the replication trigger — push on session completion, pull on
  demand, or both?
- "Telescopic visibility" implies a partial-fetch protocol — does that ride
  on the existing representable message types, or does it need its own
  channel (relates to File exchange below)?
- Where do agent-host-files for *other* hosts' sessions live relative to the
  local host's own — same tree with a host-scoped subpath, or a separate
  store entirely?

## Summarized sessions

Sessions will get ongoing summary generation, so humans and agents can see
at a glance what a session was about and navigate via a heuristic tree:
brief documents summarize and link down into more detailed layers.

Open questions:
- Who generates the summary — the process running the session (self-report)
  or a downstream consumer (AC, or something else) after the fact?
- Is the tree depth fixed (e.g. one-line -> paragraph -> full session) or
  variable per session type?
- Do summaries regenerate as a session continues, or only finalize once?

## Cleaned sessions

Sessions will be pre-digested and made safe: best-effort secret removal, and
large-volume records (like loading-bar output) collapsed into shorter
representations with the full volume available only via intentional
inspection. Secrets are guarded from going over the wire by default.

Open questions:
- Where does cleaning happen — at session-write time (once, locally) or at
  transfer time (every time it crosses a wire)? These have different cost
  and different risk if the detection rules change later.
- Secret detection is inherently best-effort — what's the fallback posture
  when a scan is inconclusive: withhold, redact-and-flag, or send with a
  warning?
- Does "reveal on intentional inspection" require a round-trip to the
  origin host, or is an uncleaned copy retained locally for the owner to
  view directly?

## File exchange

Both distributed sessions and general distributed interactions need
file-transfer capability built into the AC interaction. LR is likely the
application that manages this interaction directly.

Open questions:
- Does this run over the same representable TCP connection (new message
  types) or a separate channel better suited to bulk transfer?
- Why LR rather than AC or FC — is it because LR is the one with direct
  filesystem access to the sub-application's agent-host-files, and FC/AC
  only ever see them through LR?
- Resumability and backpressure for large transfers, given representable's
  current message-oriented shape.

## Network topology view

AC will present a visual view of which nodes and applications are visible
on the network and how they're connected — both a point-in-time view and an
over-time one.

Open questions:
- Point-in-time is a natural extension of the existing host sidebar/state —
  does over-time mean a stored history of connect/disconnect events, and if
  so, where does that history live (relates to Full lifetime capture)?
- Does this need to show the AC-to-AC hierarchy (relates to Autolaunch
  chains) as well as AC-to-LR, once multi-tier AC exists?

## Distributed ridealongs

Ridealongs are currently a strictly same-box FC capability. They're
intended to expand both in variety of interaction (e.g. RPA) and to support
decentralized operation.

Open questions:
- What does a "distributed" ridealong action mean operationally — an action
  initiated from one host that executes on another, or a ridealong whose
  steps span multiple hosts?
- Does this build on the existing `lr-ridealong-command` forwarding path in
  AC, or does cross-host ridealong coordination need its own protocol?

## Surfacing evidence

Inward-looking analysis reports, test automation, demo content generation,
and launching software for manual test are to be brought under a broad
"surfacing evidence" umbrella — a loose coupling to UFA that lets projects
integrate the idea broadly and in a distributed way.

Open questions:
- Is this primarily a naming/categorization umbrella over capabilities that
  already exist in some form, or does it imply a new shared interface all
  of these must conform to?
- How does "surfacing evidence" get exposed through AC's topology/UI — as
  its own panel per host, or folded into existing session/ridealong views?

## Full lifetime capture (interaction with on-my-machine)

A specific case of surfacing evidence: with on-my-machine (OMM), a full
system is deployed as code. UFA should be able to automatically initiate a
full life-cycle evaluation on that deployment, gathering all logs and
signals associated with the lifetime as surfaced evidence, then destroy the
system or retire it to a dormant state once the exercise concludes.

Open questions:
- What triggers the start/end of "the lifetime" — OMM deploy/destroy
  hooks, or a UFA-side supervisor watching for them?
- Where does the gathered evidence land — folded into the surfacing
  evidence umbrella above, or a distinct artifact type given the scale of
  logs a full lifecycle can produce?
- "Retire to dormant" vs. "destroy" implies a policy decision per exercise —
  who/what decides which applies?
