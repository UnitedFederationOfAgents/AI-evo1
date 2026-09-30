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
| `{ts}-processed.txt` | Permanent | The **producer of the raw/s-raw file above**, immediately after writing it | The raw content run through auto-maintenance (secret redaction, loading-bar stripping, long-response truncation), with a JSON header per processing step applied |
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
   command, capture               → write {ts}-processed.txt
   stdout/stderr                  remove {ts}-writing.txt
                                  │
                                  ▼
                          5. PRIMARY ONLY: fold every written file
                             (its own -raw.txt, plus every
                             -s-raw.txt in the directory) into
                             session.jsonl, reading each one's
                             already-produced -processed.txt for
                             the session-log text, and rename each
                             consolidated -s-raw.txt → -raw.txt
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

## Worked example: one primary, one concurrent secondary

```
t=0    clauditable git status         (becomes primary)
         → 0-writing.txt
t=0.1  clauditable git log             (starts while primary is running)
         → 1-writing.txt, then detects primary's 0-writing.txt
         → renamed to 1-s-writing.txt
t=0.2  `git log` finishes
         → 1-s-raw.txt written (permanent)
         → 1-processed.txt written immediately (by this same invocation)
         → 1-s-writing.txt removed
         (this invocation is secondary, so it stops here)
t=1.0  `git status` finishes
         → 0-raw.txt written (permanent)
         → 0-processed.txt written immediately (by this same invocation)
         → 0-writing.txt removed
         (this invocation is primary, so it now consolidates:)
         → reads 0-processed.txt and 1-processed.txt
         → appends both session-log entries to session.jsonl, in
           timestamp order (1 before 0)
         → renames 1-s-raw.txt → 1-raw.txt
```

After this, the session directory holds `0-raw.txt`, `0-processed.txt`,
`1-raw.txt` (promoted from secondary), `1-processed.txt`, and
`session.jsonl` — no `-writing.txt` or `-s-raw.txt` files remain.
