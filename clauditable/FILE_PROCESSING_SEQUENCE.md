# The clauditable File Processing Sequence

This document walks through how a single `clauditable` (CLBL) invocation
turns a wrapped command into the files that end up in a session directory,
and explains what each file type means. See [`RECORD_SCHEMAS.md`](RECORD_SCHEMAS.md)
for the exact on-disk formats.

## The file types

| File | Lifetime | Written by | Purpose |
|------|----------|------------|---------|
| `{ts}-writing.txt` / `{ts}-s-writing.txt` | Transient — exists only while the command runs | The invocation that dispatched the command | Marks "a writer is active at this timestamp" so concurrent invocations can detect a collision. Holds `<ts> <agent> <model>` (`-` when unset), then the command; local-representative lists it as a running agent call (`agentops.go`) |
| `{ts}-raw.txt` / `{ts}-s-raw.txt` | Permanent | The same invocation, at completion | The complete, untruncated command + response. Never edited after creation, except a `-s-raw.txt` being renamed to `-raw.txt` (see below) |
| `{ts}-processed.txt` / `{ts}-s-processed.txt` | Permanent | The **producer of the raw/s-raw file above**, immediately after writing it | The raw content run through auto-maintenance (secret redaction, loading-bar stripping, long-response truncation), with a JSON header per processing step applied. A primary writes straight to `-processed.txt`; a secondary keeps the `-s-` infix until consolidation folds and promotes it (see below). local-representative shows its response as a finished agent call's (`agentops.go`) |
| `session.jsonl` | Permanent, append-only | The primary, once its own command completes | The consolidated, human-scannable log: one JSON-metadata-plus-preview entry per invocation, in timestamp order |
| `session.yaml` | Permanent | Whichever invocation creates the session directory | Session identity (`id`, `name`, `created`) |

The `-s-` infix (`-s-writing.txt`, `-s-raw.txt`) marks a **secondary**: a
second command that started while another ("primary") command was still
running in the same session. Only one primary can be active per session at a
time; every concurrent latecomer becomes a secondary and is folded into
`session.jsonl` by the primary once the primary finishes.

## The sequence

Every invocation — primary or secondary — runs the same first three steps on
its own. Only step 4 differs between them.

```
   dispatch                          completion
      │                                  │
      ▼                                  ▼
1. write {ts}-writing.txt   3. write {ts}-raw.txt (or -s-raw.txt)
   (or -s-writing.txt            — the permanent, untouched record
    after the 200ms              │
    collision check)             ▼
      │                      4. IMMEDIATELY process what was just
      ▼                         written:
2. run the wrapped               ApplyAutoMaintenance(raw content)
   command, capture               → write {ts}-processed.txt (primary)
   stdout/stderr                    or {ts}-s-processed.txt (secondary)
                                  remove {ts}-writing.txt
                                  │
                                  ▼
                          5. PRIMARY ONLY: fold its own -processed.txt
                             plus every -s-processed.txt in the
                             directory into session.jsonl, then
                             promote each folded -s-processed.txt →
                             -processed.txt (and, if a local
                             -s-raw.txt sibling exists, that → -raw.txt)
```

The key point of step 4: **whichever invocation produces a `-raw.txt` or
`-s-raw.txt` file is the one that turns it into a `-processed.txt` file.**
Processing happens synchronously, right after the raw content is known, by
the process that has it in hand — not deferred to whatever process later
happens to run primary consolidation. This keeps consolidation (step 5)
cheap and mechanical: by the time a primary gets there, every file it needs
to fold into `session.jsonl` already has its processed counterpart sitting
next to it. (Consolidation still knows how to produce a missing
`-processed.txt` itself as a fallback, purely as a safety net — that path
shouldn't be hit in normal operation.)

Step 5 keys off `-s-processed.txt` rather than `-s-raw.txt`, so a
`-s-processed.txt` with no `-s-raw.txt` sibling is still folded. Either way,
consolidation never re-runs auto-maintenance on it; that only ever happens
once, at `-raw.txt`/`-s-raw.txt` → `-processed.txt` time, by the record's own
producer.

## Distributed sessions

The sequence above is entirely local. When more than one host shares a
session (see [`docs/DistributedSessionsBrainstorm.md`](../docs/DistributedSessionsBrainstorm.md)),
the session's **owner** — the host named by `owner:` in its `session.yaml`,
the one that created it — is the only one that writes `session.jsonl` (see
`sessionlog.go`). On any other host the same sequence runs with two
differences:

- Every record file carries the host: `{ts}-<host>-writing.txt`,
  `{ts}-<host>-raw.txt`, `{ts}-<host>-processed.txt` (and `-s-` variants), so
  two hosts writing in the same second can't collide.
- Step 5 appends to the host's own **secondary log**,
  `session.<host>.jsonl`, instead of `session.jsonl`.

Notes and resources (`add-note`/`add-resource`) follow the same rules.
A session with no `session.yaml` has no known owner, and is written to as
someone else's; clauditable never creates `session.yaml` except for the
host's own daily default (new-session and federation-command's confirmed
set-session create the others).

Every log append (clauditable, federation-command, session-manager's fuse)
is a single write under an exclusive `flock` on the log.

```
 non-owner host                                  owner host
 ──────────────                                  ──────────
 {ts}-<host>-raw.txt   (never leaves the host)
 {ts}-<host>-processed.txt ───pulled once by name─────────►  {ts}-<host>-processed.txt
 session.<host>.jsonl ────pulled when its checksum changes─► session.<host>.jsonl
                                                                   │ session-manager fuses the
                                                                   ▼ turns it doesn't have yet
 session.jsonl  ◄──pulled when its checksum changes──────── session.jsonl
   │
   ▼ session-manager prunes the turns that came back from
     session.<host>.jsonl, and removes it once it's empty
```

All transfers are pulls `local-representative` makes on its own host's
behalf, over `agent-coordinator`'s `/host/<id>/*` proxy (see
`local-representative/sessions.go`'s `handleSessionsPull`), triggered by
session-manager before it renders a view (`session-manager/repr.go`) and by
federation-command's set-session:

- `session.yaml` and `session.jsonl` only come from the owner, and the owner
  never takes them from anyone. (A host with no copy at all may take one
  from a non-owner when the owner can't be reached, but never replaces one.)
- `session.<host>.jsonl` only goes to the owner, and only from `<host>`.
- Processed records and resources never change once written, so they're
  fetched once by name. Resources over 256 KiB are left behind until someone
  opens them.
- Raw files, `-writing.txt` files and unconsolidated `-s-processed.txt`
  files never cross hosts.

session-manager's transcript splices every secondary log's not-yet-fused
turns in among `session.jsonl`'s, marked with the host they came from (see
`session-manager/sessionlog.go`). None of this needs `local-representative`
to be present or connected — without it, each host just keeps its own logs.

## Worked example: one primary, one concurrent secondary

```
t=0    clauditable git status         (becomes primary)
         → 0-writing.txt
t=0.1  clauditable git log             (starts while primary is running)
         → 1-writing.txt, then detects primary's 0-writing.txt
         → renamed to 1-s-writing.txt
t=0.2  `git log` finishes
         → 1-s-raw.txt written (permanent)
         → 1-s-processed.txt written immediately (by this same invocation)
         → 1-s-writing.txt removed
         (this invocation is secondary, so it stops here)
t=1.0  `git status` finishes
         → 0-raw.txt written (permanent)
         → 0-processed.txt written immediately (by this same invocation)
         → 0-writing.txt removed
         (this invocation is primary, so it now consolidates:)
         → reads 0-processed.txt and 1-s-processed.txt
         → appends both session-log entries to session.jsonl, in
           timestamp order (1 before 0)
         → renames 1-s-processed.txt → 1-processed.txt
         → renames 1-s-raw.txt → 1-raw.txt
```

After this, the session directory holds `0-raw.txt`, `0-processed.txt`,
`1-raw.txt` (promoted from secondary), `1-processed.txt` (promoted from
`1-s-processed.txt`), and `session.jsonl` — no `-writing.txt`, `-s-raw.txt`,
or `-s-processed.txt` files remain.

Had `git log` instead run on a *different* host that doesn't own this
session, it would have been that host's own primary, leaving
`1-<host>-raw.txt`, `1-<host>-processed.txt` and an entry in
`session.<host>.jsonl` there; only the processed file and the secondary log
would reach the owner (see "Distributed sessions" above).
