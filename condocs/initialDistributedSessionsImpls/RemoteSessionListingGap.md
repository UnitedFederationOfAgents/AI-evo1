# Does `list-sessions` show a session created on another host?

**Update (Step 1 Substep B Revision A): the gap described below is now
closed.** See "What was added" at the end of this document for what
changed; the rest of the document is left as written (the trace of why the
gap existed in the first place is still accurate background).

Scenario: a session gets created on host A. Host B is connected to the same
chain — `X <--> LR <--> AC <--> LR <--> X`, i.e. host A's clauditable-using
app talking to its own `local-representative`, over `agent-coordinator`, to
host B's `local-representative`, to host B's own clauditable-using app (`X`
is `federation-command`'s `list-sessions` or session-manager's session list,
whichever runs the command). Host B runs `list-sessions`. Does host A's
session appear?

**No — not today.** Tracing the actual chain of events shows why.

## The chain of events as it exists today

1. **Host A creates the session.** `clauditable new-session`/`get-default-session`
   (or federation-command's/session-manager's thin wrappers around the same)
   only ever touches host A's own `AGENT_RECORDS_PATH`: it `mkdir`s a new
   session directory and writes `session.yaml`/`session.jsonl` into it. No
   network call happens — host A's `local-representative` and
   `agent-coordinator` are never told a session was created.
2. **Host B runs `list-sessions`.** Both entry points that implement it —
   federation-command's `renderSessions` (`federation-command/main.go:5053`)
   and session-manager's `listSessions` (`session-manager/sessions.go:229`) —
   do exactly one thing: `os.ReadDir(recordsPath)` on host B's own
   `AGENT_RECORDS_PATH`, then read each entry's `session.yaml` for its name.
   Neither makes an HTTP call, neither talks to host B's own
   `local-representative`, and neither is aware `agent-coordinator` or any
   peer host exists.
3. Host A's session directory was never created on host B's filesystem, and
   nothing asked anyone to create it — so step 2 finds nothing to list.

The two hosts' `list-sessions` outputs stay whatever each host's own local
directory happens to contain; they never converge.

## Why the existing distributed-sessions plumbing doesn't close this gap

`docs/DistributedSessionsBrainstorm.md` and
`clauditable/FILE_PROCESSING_SEQUENCE.md#distributed-sessions` describe two
pulls that *are* wired up end-to-end over this same `X <--> LR <--> AC <--> LR`
chain — but both are scoped to a session ID the calling host already knows:

- **Once-transfer** (`clauditable/distsync.go`'s `triggerOnceTransfer`): fires
  when a host is about to write a record and finds itself primary, for the
  session it is about to write — a session ID it necessarily already has
  (`AGENT_SESSION` names it).
- **Sync** (`session-manager/repr.go`'s `triggerSessionSync`): fires right
  before rendering a session view, for the session ID the user already
  selected — which itself only ever comes from that host's own (local-only)
  `list-sessions`/session picker.

Both ultimately call `local-representative`'s `POST /api/sessions/<id>/pull`
(`local-representative/sessions.go`'s `handleSessionsPull`), which in turn
asks `agent-coordinator`'s `GET /api/hosts` for every other LR-active host
and fetches matching files from each one's `GET /api/sessions/<id>/list` and
`/file/<name>` — but every one of those routes has `<id>` baked into its URL
path. There is no route on `local-representative` (or relayed through
`agent-coordinator`) that answers "what session IDs do you have at all,"
only "here's what's inside the one I already told you about."

So the loop these two triggers close is *"keep files fresh inside a session
both sides already agree exists."* Discovering a session neither side has
ever heard the ID of is a different problem, and nothing in the current
implementation attempts it: `list-sessions` never leaves the local
filesystem, and the pull mechanism has no unscoped "list every session" entry
point to call even if it did.

## What would need to be added

Closing the gap would need, at minimum:

- An unscoped listing route on `local-representative` — e.g.
  `GET /api/sessions` returning every session ID + name on that host (a small
  variant of `handleSessionsList` without the `<id>` segment) — reachable
  through `agent-coordinator`'s transparent `/host/<id>/*` proxy the same way
  the existing per-session routes already are.
- `list-sessions` (or whichever of `federation-command`/`session-manager`
  issues it) asking its own `local-representative` to fan that request out to
  every peer `agent-coordinator` shows as connected (mirroring
  `listPeerHosts`/`handleSessionsPull`'s shape) and merging the results into
  the locally-rendered list, tagged as remote — most plausibly *not*
  materializing a full local session directory just from being listed, since
  that would pull file contents nobody asked to view yet.

None of this exists yet; it is a reasonable next increment on top of the
once-transfer/sync wiring already in place, not a fix to something broken
within that wiring.

## What was added (Step 1 Substep B Revision A)

Both pieces sketched above now exist, plus one more: a live poll fired every
time either entry point is about to render, not just a mechanism that would
work if called.

- **`local-representative` gained the unscoped listing route**: `GET
  /api/sessions` (`sessions.go`'s `handleSessionsIndex`) returns every
  session ID this host has plus its `session.yaml` name -- no file contents,
  registered as its own exact-path route alongside the existing
  `/api/sessions/` per-session subtree (mirroring `/api/files` vs
  `/api/files/`). Reachable unmodified through `agent-coordinator`'s
  transparent `/host/<id>/*` proxy, same as the existing `list`/`file`
  routes.
- **`local-representative` gained the fan-out**: `POST
  /api/sessions/discover` (`handleSessionsDiscover`) asks `agent-coordinator`
  for every LR-active peer (the same `listPeerHosts` the pull already used)
  and calls each one's `GET /api/sessions`, returning every session it
  found tagged with which host reported it. Deliberately read-only on both
  ends -- unlike a pull, nothing is fetched or written into this host's own
  `AGENT_RECORDS_PATH`; a session showing up here does not materialize a
  local directory for it, matching the "most plausibly not" call made
  above. Refused through the transparent proxy for the same reason a pull
  is: it's this host acting as a client on its own behalf.
- **`federation-command` and `session-manager` both now call the fan-out
  before rendering `list-sessions`**, merging in any discovered session
  whose ID isn't already in the local list, tagged as remote:
  `federation-command`'s three `renderSessions` call sites
  (`list-sessions`, `ufa session list`, and the ridealong builtin) each pass
  in a fresh `discoverRemoteSessions()` result; `session-manager`'s
  `sendSessions`/`broadcastSessions` (which back the `list-sessions` WS verb,
  every new browser connection, and every mutation) go through a new
  `listSessionsWithRemote` that merges in `triggerSessionsDiscovery()`'s
  result the same way. Both are the synchronous "any list-sessions
  behaviour initiates this poll" half of Step 1 Substep B's Revision A.
- **Both also poll once more right after connecting to their own
  `local-representative`**, independent of whether `list-sessions` is ever
  run: `session-manager`'s `connectLoop` fires `refreshSessionsAfterConnect`
  (which re-broadcasts the merged session list to any open browser tabs);
  `federation-command`'s `reprConnectedMsg`/`autoConnectResultMsg` success
  paths queue `sessionsDiscoveryDelayCmd`, which prints a one-line notice if
  the poll turns up anything. Both wait `sessionsDiscoveryDelay` (500ms)
  first so local-representative's "hello" has time to disclose its HTTP
  port (see `representable.Client.PeerHTTPPort`) -- without it, a poll fired
  the instant `Connect` returns would almost always race the hello and
  silently no-op.

A session discovered this way stays a synthesized remote entry (`id`/`name`
plus which host reported it) until something actually asks to view it --
discovery alone still never pulls a file or creates a local session
directory, exactly as sketched above.
