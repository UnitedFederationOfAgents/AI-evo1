# Brainstorm Distributed Sessions

Brainstorm some aspects of session behaviour to support distributed sessions. This includes local session behaviours and distributed general behaviours like file management.

## Notes

- Sessions need to fall out of scope -- (this implementation may not be very soon)
  - Max session length is needed and they can point to the continuation
  - Sessions need 'warmth' like 'active', 'recent', '(none)', 'past'

## Questions

- How do we share files?
- How do we telescope context? How do we visualize?
- How do we implement in phases?
- How do we clean? (redact, remove noise)
- How do we summarize and compact?
- How much identification do we need to add? (app? instance? session name vs ID?)
- What local session behaviours do we need to add? How does discovery work? (local and remote)
- How much awareness does each sub-app need?
- Who does the session processing? (Which parts?)

### How do we share files?

- LR works with AC as the backbone.
  * Which sub-apps need to use this?
  * Do sub-apps genererally use the filesystem directly and not normally need networking? (Lean to yes)

- An app that uses sessions 
  * How should we 'use sessions'?
    - Call claudiable, keep knowledge of session specifics there to the extend possible

* Do we need to add UI functionality/general access to file sharing yet?
  - Probably not, better to not entangle

- For current chain under consideration:
  - We run in federation-command
  - We list-sessions, maybe with an arg to specify inclusion of remote
    - When we include remote that means FC needs to initiate discovery; let's assume this means talking to LR
      * Do we ask it directly to search the agent records dir?
        - No, let's just use a wildcard files approach with known paths
    - LR transfers the files, we'll want this to block so the list-sessions can include results.
    - This will probably make sense for the clauditable binary to handle, FC calls CLBL
  - The list of sessions comes back, with indication of which sessions are local, vs remote

### How do we telescope context? How do we visualize?

- Telescope context
  - Using summary files and abbreviated session logs that link to external '-raw' and '-processed' files
  - Let agents use their natural bring-in-files strategy to bring in what is needed
  - Cap length of session files and have natural continuation
    * Does this mean we need a manifest for a session?
      - Maybe a manifest for a session is a good idea generally

* Is there a good place to ALSO keep a 'head' style view? Or does a summary do enough?

- Visualize
  - This would be a good thing to draw/diagram
  - We need to have the right density of summary files at 

### How do we implement in phases?

- Let's consider phases: minimum distributed vs 'later' (undifferentiated for now)
  - mindist
    - We probably don't need to summarize yet at all
    - We need file transfers to work
      - We need sub-apps to be able to invoke file transfers
  - later
    - everything else

### How do we clean? (redact, remove noise)

- Needs to be a layered execution
  - Immediately - synchronous with clbl call
    - Might never want heuristic here
    - First step of the raw-->processed pipeline
  - Continuous - more often than summarizing
    - 

### How do we summarize and compact?

- A system of max sizes across different dimensions; max number of commands, high number of commands with rolling window, wall-clock time

- Two layers of summary are needed, potentially more depending on amount of low level information
  - A direct layer, closest to the actual events, giving a very specific (but not necessarily encompassing) description of what happened, fairly low level
  - A (sub)section layer, bringing direct layer summaries together to give manageable size interface files
  - A layout layer, summarizing which direct and/or section layer summaries link into what groups of events heuristicly and describing the overall activities at a higher level
  - Direct summary uses only events as context, section/layout uses layers below and events

- Compacting is never needed for working state, maybe we compress during a deeper archiving later

### How much identification do we need to add? (app? instance? session name vs ID?)

- We do want name AND ID because we want names that are like summaries (similar to conversation handles in web UI models)
  - We need to see host -- hosts will need a name and ID (this will help for deconfliction and instance lifetimes)
  - We need to see the user agent and head (instanceID)

* Will ID need to change?
  - This will be a question for later we can think about it at the same time as joining/mutating sessions

### What local session behaviours do we need to add? How does discovery work? (local and remote)

* Will we need session manager?
  - Ideally session manager is not needed for any of the lowest layer functionality/can call clbl for any of this
  - We need session manager to see session depiction outside of the use of other sub-apps
  - We don't need SM for anything like listing sessions, setting the session to use

- We need some foundational identification pieces
  - We need to have whatever behaviour is used for 'join current default session and notify'
  - We need to add the session manifest as the data store for some metadata that doesn't fit in folder name (like name)
  - We need host ID/name, head ID, user agent

- Discovery works on the local filesystem (through clbl call) but may pull remote data first

- We don't NEED cli 'ufa session' entrypoint but it is worth it to make things smoother

- Chain of events
  - federation-command launches
  - FC uses 'get-default-session' to get the current default or detect if there is none
    - Notify which session is used or create one automatically if not present, indicating it is a default
    - For the moment we assume defaults are per-day
  - A default session gets the name "<date> Default"
  - A command is run and it clbls to the chosen default location

- Notes
  - We will need to support concurrency properly
    - Lock file creation when execution begins, when another writer detects lock it uses a 'tributary input'
    - When sessions are remote always use the 'tributary input'
  * When does a non-default get created? On set-session or first call to the session?
    - Let's have it on 'set-session'/'new-session'

* Do sessions need owners?

### How much awareness does each sub-app need of the mechanics of sessions?

- Very little ideally

### Who does the session processing? (Which parts?)

- The synchronous parts are ideal right in clbl
- Maybe session-manager is the right place for everything else so it can add that self-awareness to the picture

----------------------------------------------------------------

## Working Section -- concise extra documentation about decisions taken during implementation

### Step 1 Rev A — Default session and session identity

- **Default session**: When `AGENT_SESSION` is unset or `"default"`, the session is `YYYY-MM-DD-<host>-default` (e.g. `2026-09-12-box-a1b2-default`; the host ID is part of the ID so each host gets its own daily default). FC calls `clauditable get-default-session` on startup to create it if absent.
- **`-default` suffix reserved**: `set-session` (FC built-in) rejects IDs ending in `-default`. `clauditable new-session` also rejects names that would produce such an ID via slugification.
- **Session identity**: Every session folder now contains `session.yaml` with `id`, `name`, and `created` fields. The folder name is the ID; `name` is the human-readable label.
- **`clauditable new-session <name>`**: Creates a session with ID `YYYY-MM-DD_HH-MM-SS_<slug>` and writes `session.yaml`. Prints the ID to stdout.
- **`clauditable get-default-session`**: Idempotently creates today's default session (`YYYY-MM-DD-<host>-default`, name `YYYY-MM-DD Default <host>`) and prints the ID.
- **FC `new-session [name]` / `ufa session new [name]`**: With name argument calls clauditable synchronously and switches. Without argument launches an interactive bash prompt (via `tea.ExecProcess`) then switches on completion via `sessionNewDoneMsg`.
- **Startup log append**: FC now opens `session.jsonl` with `O_APPEND` instead of truncating, so default sessions accumulate records across FC restarts on the same day.

### InitialDistributedSessions Step 1 Revision A — once-transfer / sync wiring

Implements the flow sketched in that step's "Distributed CLBL Cooking Flow"
resource: hooking clauditable's on-host file sequence (see
[`clauditable/FILE_PROCESSING_SEQUENCE.md`](../clauditable/FILE_PROCESSING_SEQUENCE.md#distributed-sessions))
up to actual cross-host transfer, entirely as pulls a host makes on its own
behalf through `local-representative`/`agent-coordinator`'s existing
infrastructure — no push primitive, no representable protocol change.

- **`clauditable` now keeps a secondary's processed file distinct from a
  primary's**: `writeProcessedFile` writes `{ts}-s-processed.txt` for a
  secondary (previously it wrote straight to `{ts}-processed.txt`, same as a
  primary) and `consolidatePrimaryToJSONL` now folds from
  `*-s-processed.txt` rather than `*-s-raw.txt`, promoting the folded file to
  `{ts}-processed.txt` (and, only if a local `{ts}-s-raw.txt` sibling exists,
  that to `{ts}-raw.txt`). This is what lets a remotely once-transferred
  record — which only ever brings the already-processed file across, never
  the pre-redaction raw one — fold in exactly the same way a local secondary
  does, with no `{ts}-raw.txt` ever appearing locally for it.
- **`local-representative` gained a `/api/sessions/<id>/...` surface**
  (`sessions.go`): `GET .../list?glob=` and `GET .../file/<name>` are
  read-only lookups into `AGENT_RECORDS_PATH` (ungated on the
  `X-UFA-Proxied-By` guard, same "reads aren't the write-to-an-arbitrary-host
  concern" posture as the files tab's existing endpoints — see
  [`docs/DistributedExchange.md`](DistributedExchange.md)), reused unmodified
  through `agent-coordinator`'s transparent `/host/<id>/*` proxy. `POST
  .../pull?glob=&mode=once|sync` is the opposite direction — this LR, acting
  as a client, discovering every other host `agent-coordinator` shows as
  connected (a new `GET /api/hosts` route there) and fetching from their
  `list`/`file` endpoints — so it's refused when it arrives through that same
  proxy (never something another host should trigger on this one remotely).
  `mode=once` never re-fetches an already-present filename; `mode=sync`
  compares each listed file's reported sha256 and only re-fetches on a
  mismatch or absence.
- **`clauditable` triggers a `mode=once` pull for `*-s-processed.txt`** right
  after a dispatching invocation is declared primary, before it runs the
  wrapped command (`distsync.go`) — "when a host is about to write a CLBL
  record and it is primary." **`session-manager` triggers a `mode=sync` pull
  for `session.jsonl` and `*-processed.txt`** right before rendering a
  session view (`repr.go`'s `triggerSessionSync`, called from
  `sendSessionView`) — "whenever any host is about to read a session ...
  bringing it up in session-manager for viewing." Both are one best-effort,
  bounded-timeout local HTTP call to the caller's own `local-representative`;
  neither errors, blocks meaningfully, or changes behavior when
  local-representative isn't running, isn't connected to agent-coordinator,
  or has no peers — the purely local sequence is unaffected either way.
  `clauditable` (too short-lived to keep its own representable connection)
  addresses its local LR via `LR_HTTP_HOST`/`LR_HTTP_PORT` (default
  `localhost:8081`); `session-manager`, already representable-connected,
  instead reads LR's disclosed HTTP port off
  `representable.Client.PeerHTTPPort()` the same way condoccer already does
  (see `docs/DistributedExchange.md`).
- **Not wired in this increment**: "supplying \[a session] to an agent for
  context" — the other reader case named alongside session-manager's viewer
  in the step prompt — since nothing in this repo yet reads a session
  directory for that purpose to begin with; `triggerSessionSync`'s HTTP call
  is a plain, reusable one-liner for whatever adds that later. Multi-primary
  merge of `session.jsonl` (the same session written from different hosts at
  different times, never having synced with each other in between) is also
  out of scope — sync is a one-way "catch this host up" pull, not a CRDT-style
  merge.

### InitialDistributedSessions Step 1 Substep B Revision A — discovering a session neither host has heard of

Closes the gap traced in
[`condocs/initialDistributedSessionsImpls/RemoteSessionListingGap.md`](../condocs/initialDistributedSessionsImpls/RemoteSessionListingGap.md):
until now `list-sessions` never left the local filesystem on either
`federation-command` or `session-manager`, because every existing pull
(once-transfer, sync) takes a session ID the caller already has — nothing
answered "what sessions do you have at all."

- **`local-representative` gained a `GET /api/sessions` unscoped index**
  (`sessions.go`'s `handleSessionsIndex`) — every session ID this host has
  plus its `session.yaml` name, no file contents — and a `POST
  /api/sessions/discover` (`handleSessionsDiscover`) that fans that same
  question out to every LR-active peer (via the existing `GET /api/hosts` /
  `listPeerHosts`) and returns everything found, each entry tagged with
  which host reported it. Both are read-only on every host involved:
  discovery never fetches a file or creates a local session directory —
  only a pull (unchanged) still does that, and only once something actually
  asks to view the session.
- **`federation-command`'s `renderSessions` and `session-manager`'s
  `listSessions`/`sendSessions`/`broadcastSessions`** now merge in
  `discoverRemoteSessions()`'s/`triggerSessionsDiscovery()`'s result (each
  binary's own copy of the discovery HTTP call, same posture as their
  existing sync/once pull callers), skipping any ID already present
  locally. This fires every time either binary is about to render a session
  list — the WS "list-sessions" verb, a new browser connection, and any
  mutation all flow through session-manager's two functions; `list-sessions`,
  `ufa session list`, and the ridealong builtin all flow through
  federation-command's `renderSessions`.
- **Both also fire the same poll once more right after connecting** to
  their own `local-representative` — `session-manager`'s `connectLoop`
  re-broadcasts the merged list to open browser tabs;
  `federation-command`'s two connect-success paths print a one-line notice
  if anything new turns up. Each waits 500ms first so local-representative's
  "hello" has had time to disclose its HTTP port (see
  `representable.Client.PeerHTTPPort`) before polling — otherwise the very
  first poll right after `Connect` returns would almost always race the
  hello and silently no-op.
