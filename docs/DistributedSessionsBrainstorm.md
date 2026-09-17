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

- **Default session**: When `AGENT_SESSION` is unset or `"default"`, the session is `YYYY-MM-DD-default` (e.g. `2026-09-12-default`). FC calls `clauditable get-default-session` on startup to create it if absent.
- **`-default` suffix reserved**: `set-session` (FC built-in) rejects IDs ending in `-default`. `clauditable new-session` also rejects names that would produce such an ID via slugification.
- **Session identity**: Every session folder now contains `session.yaml` with `id`, `name`, and `created` fields. The folder name is the ID; `name` is the human-readable label.
- **`clauditable new-session <name>`**: Creates a session with ID `YYYY-MM-DD_HH-MM-SS_<slug>` and writes `session.yaml`. Prints the ID to stdout.
- **`clauditable get-default-session`**: Idempotently creates today's default session (`YYYY-MM-DD-default`, name `YYYY-MM-DD Default`) and prints the ID.
- **FC `new-session [name]` / `ufa session new [name]`**: With name argument calls clauditable synchronously and switches. Without argument launches an interactive bash prompt (via `tea.ExecProcess`) then switches on completion via `sessionNewDoneMsg`.
- **Startup log append**: FC now opens `session.jsonl` with `O_APPEND` instead of truncating, so default sessions accumulate records across FC restarts on the same day.

### InitialDistributedSessions Step 1 — FC-connected distributed session baseline

Answers "Do sessions need owners?" above: yes.

- **Session ownership**: `session.yaml` now carries an `owner` field — the host
  ID (resolved the usual way: `UFA_HOST` env, else `~/.ufa/host.yaml`, else
  hostname) that created the session. Set once at
  creation (`clauditable`'s dispatch path, `new-session`, `get-default-session`,
  and the rename fallback that creates `session.yaml` if missing); never
  rewritten afterward. Sessions that predate this field read back as `""`,
  which every reader treats as "locally owned" for backward compatibility.
- **Distributed writer-leader rule**: per `SessionLeaderBrainstorm.md`,
  clauditable is now always secondary on a session it doesn't own — the
  primary/secondary writing-file race (see `LocalSessionImprovments.md` Step 3)
  is skipped entirely (`isRemoteOwned` short-circuits `checkIsPrimary`) whenever
  `session.yaml`'s `owner` differs from the local host. This is the full extent
  of what clauditable knows about distributed sessions — it never learns *how*
  a remote owner's files reach this host.
- **FC gates on the "connected" state**: distributed session behaviour is only
  active while FC is connected to local-representative (`blinker.IsConnected()`
  — the same state that gates remote-control). Disconnected FC behaves exactly
  as it always has, purely local, no sync attempts.
- **`session-sync-request` protocol message** (FC → LR, over the existing
  representable `data` channel, fire-and-forget from FC's side): sent with
  `kind: "append"` immediately before every command that appends to the current
  session (wraps with clauditable), and `kind: "list"` before any
  sessions-listing operation (`list-sessions`, `ufa session list`,
  `select-session`, `ufa session select`). `"append"` describes a glob of one
  session's processed + session files (`session.yaml`, `session.jsonl`,
  `*-processed.txt` — never `*-raw.txt`/`*-writing.txt`, matching "only
  transmit processed for remote sessions" above); `"list"` describes only
  `session.yaml` across every un-archived session directory, from all
  participants — the lazy-loading shape this doc's "How do we share files?"
  section sketched. FC only ever states *what* glob it needs; it has no
  opinion on how LR moves the bytes.
- **LR's `session-sync-request` handler is a stub for now**: it logs receipt
  and acknowledges the kind, but there's no cross-host transfer backend behind
  it yet — building that is real, separate infrastructure work (an
  agent-coordinator-brokered records-glob pull between participants, the
  natural extension of the single-file pull Path 2 sketches in
  `docs/DistributedExchange.md`). This increment lands the vocabulary FC and LR
  speak to each other; the next increment is teaching LR to actually act on it.

### InitialDistributedSessions Step 1 Rev A — the `"list"` glob actually syncs

Landed the "next increment" flagged above, for `kind: "list"` only (`"append"`
is still the stub described above — full session-content sync is separate,
larger work). Built pull-only, per this revision's explicit instruction: a
host only ever reads another participant's `session.yaml`, never pushes its
own — matching Path 2 of `docs/DistributedExchange.md` (LR-to-LR transfer
brokered through agent-coordinator), rather than pushing anything ahead of a
participant touching its own session.

- **agent-coordinator gained `GET /api/hosts`**: the same `{id, label,
  status}` rows the dashboard's `"hosts"` WebSocket broadcast carries, as a
  plain HTTP endpoint. This is how a local-representative discovers its sync
  peers without needing its own WebSocket client.
- **local-representative gained `GET /api/sessions`**: this host's current
  un-archived session listing (`id`/`name`/`owner`/`created` — session.yaml's
  fields, never raw/processed content). Deliberately not gated on the
  `X-UFA-Proxied-By` header, same reasoning as the files tab's content
  endpoint (`docs/DistributedExchange.md`): a read isn't the ambiguous-target
  write that upload is, so it already flows unmodified through
  agent-coordinator's transparent `/host/<id>/*` proxy.
- **local-representative's `"list"` handling**: on receiving a `"list"`
  `session-sync-request` (run in its own goroutine, not the representable
  read loop), it calls `GET /api/hosts` on agent-coordinator, then `GET
  /host/<peer>/api/sessions` for every other *connected* participant, and
  writes each remote session it doesn't already have as a plain
  `session.yaml` under its own records path — the exact shape clauditable
  itself writes, so clauditable's existing owner check and FC's
  `renderSessions`/`buildSessionPicker` (both already just scan the records
  path directly) pick it up with no further plumbing on either side.
  - **Collision guard**: never overwrites a session this host doesn't
    recognize as its own previously-cached copy of the *same* remote owner.
    Two hosts' default sessions collide by id (`YYYY-MM-DD-default`) daily —
    a locally-owned id always wins over an incoming remote copy, and an id
    already cached from one remote owner is never clobbered by a different
    remote owner claiming the same id. Both cases are logged, not silently
    dropped.
  - Needs `agent-coordinator`'s HTTP port, which local-representative didn't
    previously track (only the representable TCP port, for its own
    connection) — added as a new `-ac-http-port` flag (default `8083`,
    matching agent-coordinator's own `-port` default).
  - Needs a records path, which local-representative also didn't previously
    have any notion of — added `AGENT_RECORDS_PATH` env support (same
    variable and default as clauditable/federation-command; still three
    independent copies of the constant, no shared package yet).
- **This makes list-sessions/select-session actually block**, for the `"list"`
  kind specifically — the brainstorm's "we'll want this to block" above.
  Since representable's server→client leg only carries plain `command`
  strings (no request/reply data channel), local-representative signals
  completion by sending federation-command a `"__session-sync-done:list"`
  command once the pull above finishes (successfully or not — an unreachable
  agent-coordinator still signals "done" immediately, rather than making FC
  wait out the full timeout for nothing). FC's `awaitDistributedSessionSync`
  sends the sync request and then blocks the calling goroutine on that signal
  (2s timeout). The wake-up is handled directly on the representable client's
  reader goroutine rather than routed through bubbletea's `Update` — `Update`
  is the thing blocked waiting for it, so delivering it via the same message
  loop would deadlock.
- **Session picker/list display**: sessions owned by another host now render
  grey-blue (`remoteSessionStyle`/`pickerRemoteEntryStyle`, ANSI 256 color 67)
  instead of the standard grey, in both `list-sessions`/`ufa session list` and
  the interactive picker (`select-session`/`ufa session select`) — both
  already showed the `[remote: <owner>]` tag from Step 1's initial reply, now
  colored distinctly too. A remote session is selectable in the picker
  exactly like a local one (`switchToSession` has no ownership check); once
  selected, further writes to it flow through clauditable's existing
  secondary-writer path (see Step 1's initial reply above).
- **Open for a later increment**: the `"append"` kind's full session-content
  sync (not just `session.yaml`) still has no backend, so a remote session
  selected today shows accurately in listings but its `session.jsonl`/
  processed files won't reflect the owner's latest activity until that lands.

### InitialDistributedSessions Step 1 Rev B — confirmed `GET /api/hosts`, fixed the real sync bug

- **`GET /api/hosts` (and the rest of LR↔AC's read/discovery surface) is
  confirmed intentional, not a Rev A mistake.** FC↔LR traffic is, and stays,
  100% over representable's TCP `data`/`command`/`log` channel — no HTTP ever
  crosses that leg. LR↔AC is deliberately a *mixed* leg, and has been since
  `docs/DistributedExchange.md`'s Path 1 (AC-mediated upload): representable
  there carries only small control-plane pushes with no request/reply framing
  (`services`, `fc-state`, `files-state`, `__system:`/`__ridealong:` commands,
  and now `session-sync-request`); anything that needs a synchronous
  query-with-a-body — `GET /api/files/<id>`, `GET /api/files`,
  `POST /api/files` (upload, relayed by AC), `GET /api/sessions`, and now
  `GET /api/hosts` — already lives on the HTTP surface both a browser and a
  peer LR reach through AC's `/host/<id>/*` proxy. `GET /api/hosts` is the
  same shape as all of those: it's how a local-representative discovers peers
  without standing up its own WebSocket client just to read the "hosts" list
  AC already broadcasts there. Folding it into representable would mean
  giving that protocol request/reply semantics it doesn't have anywhere else
  today — a bigger, separate change, not a one-line fix — so this is a
  "comment" answer, not a code change. See the new `docs/InterfaceTopology.md`
  for the full inventory of which leg carries what.
- **The actual reason FC instances weren't syncing sessions to
  `/host-agent-files/`:** `notifyDistributedSessionSync`/
  `awaitDistributedSessionSync` gated on `blinker.IsConnected()`, which is
  specifically `BlinkerConnected` — FC in *remote-control* mode, driven by LR.
  But the ordinary way to exercise this (FC attached to LR, typing locally) is
  `BlinkerLocalControl`, a different state that `IsConnected()` returns false
  for — see `autoConnectControlState`, which lands a manually-driven FC there
  specifically so the foreground session isn't yanked away. So every
  `list-sessions`/`select-session`/append in the state actually used for
  manual testing silently sent nothing, with no error or log line to explain
  why (a good example of the "measures to gather more data" the prompt asked
  about — a bail-early log line here would have surfaced this immediately).
  **Fixed** by gating on `m.reprClient != nil` alone: the representable data
  channel is live in every control state FC can be connected in
  (`Connected`/`LocalControl`/`Ridealong`/`Condoc` all share the same
  `reprClient`), so the right condition is "is FC connected to LR at all",
  not "is LR currently driving FC's keystrokes".
