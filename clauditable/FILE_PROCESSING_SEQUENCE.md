# The clauditable File Processing Sequence

This document walks through how a single `clauditable` (CLBL) invocation
turns a wrapped command into the files that end up in a session directory,
and explains what each file type means. See [`RECORD_SCHEMAS.md`](RECORD_SCHEMAS.md)
for the exact on-disk formats.

## The file types

| File | Lifetime | Written by | Purpose |
|------|----------|------------|---------|
| `{ts}-writing.txt` / `{ts}-s-writing.txt` | Transient — exists only while the command runs | The invocation that dispatched the command | Marks "a writer is active at this timestamp" so concurrent invocations can detect a collision |
| `{ts}-raw.txt` / `{ts}-s-raw.txt` | Permanent | The same invocation, at completion | The complete, untruncated command + response. Never edited after creation, except a `-s-raw.txt` being renamed to `-raw.txt` (see below) |
| `{ts}-processed.txt` / `{ts}-s-processed.txt` | Permanent | The **producer of the raw/s-raw file above**, immediately after writing it | The raw content run through auto-maintenance (secret redaction, loading-bar stripping, long-response truncation), with a JSON header per processing step applied. A primary writes straight to `-processed.txt`; a secondary keeps the `-s-` infix until consolidation folds and promotes it (see below) — the same infix a distributed once-transfer targets |
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

Step 5 keys off `-s-processed.txt` rather than `-s-raw.txt` on purpose: a
`-s-raw.txt` only ever exists locally (a concurrent secondary invocation on
this same host), but a `-s-processed.txt` can *also* have arrived from
another host entirely — see "Distributed sessions" below. Either way,
consolidation never re-runs auto-maintenance on it; that only ever happens
once, at `-raw.txt`/`-s-raw.txt` → `-processed.txt` time, by the record's own
producer.

## Distributed sessions

The sequence above is entirely local. When more than one host shares a
session (see [`docs/DistributedSessionsBrainstorm.md`](../docs/DistributedSessionsBrainstorm.md)),
`local-representative` adds two triggers around it, both implemented as
pulls a host makes on its own behalf (never a push another host forces on
it):

```
 secondary host                      primary host                    any viewer host
 ───────────────                     ────────────                    ───────────────
 {ts}-s-writing.txt
        │
        ▼
 {ts}-s-raw.txt          ──xfer once, on the primary's
        │                  next dispatch, if not already       ┐
        ▼                  seen locally──────────────────────► │
 {ts}-s-processed.txt      (glob: *-s-processed.txt)            ▼
                                                        {ts}-s-processed.txt
                                                        sits alongside any
                                                        LOCAL secondary's own
                                                        {ts}-s-processed.txt,
                                                        folded into session.jsonl
                                                        by step 5 exactly the
                                                        same way — promoted to
                                                        {ts}-processed.txt with
                                                        no {ts}-raw.txt ever
                                                        appearing (its raw
                                                        content never left the
                                                        secondary host)
                                                                │
                                                                ▼
                                                   session.jsonl / {ts}-processed.txt
                                                        ──sync, refreshed any───► session.jsonl /
                                                          number of times,        {ts}-processed.txt
                                                          checksum-compared
                                                          to skip unchanged
                                                          files (glob:
                                                          session.jsonl,
                                                          *-processed.txt)
```

- **Once-transfer**: whenever a host is about to write a record and finds
  itself primary, it asks every other LR-active host holding this session for
  a copy of its `*-s-processed.txt` files, skipping any name it already has.
  Since `-s-processed.txt` is already fully auto-maintained by its own
  producer, nothing runs again on arrival — the transferred copy is folded
  into `session.jsonl` and promoted to `-processed.txt` exactly like a local
  secondary's, just with no `-raw.txt` counterpart ever appearing locally
  (the pre-redaction content never crosses hosts).
- **Sync**: whenever a host is about to read a session — supplying it to an
  agent for context, or opening it in session-manager for viewing — it asks
  every other LR-active host for its current `session.jsonl` and
  `*-processed.txt`, refreshed as many times as the session is read.
  Checksums (not just presence) are compared so an unchanged file is never
  re-transferred.

Both triggers are implemented as one HTTP call from clauditable
(once-transfer, right after determining primary/secondary — see
`distsync.go`) or session-manager (sync, right before rendering a view — see
`session-manager/repr.go`) to their own host's `local-representative`, which
performs the actual cross-host fetch over `agent-coordinator`'s existing
`/host/<id>/*` proxy (see `local-representative/sessions.go`). Neither
trigger is required for `local-representative` to be present or connected —
both degrade silently to the purely local sequence above when it isn't.

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

Had `git log` instead run to completion on a *different* LR-active host
sharing this session, only its `1-s-processed.txt` would ever reach this
host (once-transferred in ahead of `git status`'s consolidation — see
"Distributed sessions" below); `1-s-raw.txt` would stay on that other host,
so the sequence above would promote straight to `1-processed.txt` with no
local `1-raw.txt` ever appearing.
