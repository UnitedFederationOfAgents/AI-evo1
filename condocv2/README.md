# condocv2

condoccer's successor. It runs **slopspace condocs only**, and they are
explicitly incompatible with condoccer's (v1):

| | condoccer (v1) | condocv2 |
|---|---|---|
| pointer file (`pointers/*.yaml`, shared dir) | no version marker, `controlScheme: slopspace` | `condocVersion: 2`, `controlScheme: slopspace-v2` |
| condoc main file header | ` ```condoc-yaml ` | ` ```condocv2-yaml ` |
| branches | `condoc/...` | `condocv2/...` |
| holder | `condoccer@<host>` | `condocv2@<host>` |

Each filters the other's pointers, condoc files and branches out, so neither
ever lists, opens or drives the other's condocs. local-representative never
runs both at once (they share the slopspace worker).

## The sequence

The outer sequence is condoccer's: **propose** (a pointer file in the pointer
repo) and **accept**, which **begins** the condoc — a slopspace with a
writespace per repo on the condoc's branch, the condoc's main and step files
committed to the condoc repo, and the branch published on every target.

Past that a condoc is a single linear step of **interactions** (no multi-steps
or substeps yet), each one:

1. **lock** — the pointer commits a lock holding the prompt text
2. **prompt** — the prompt is inserted into the step file (`## Prompt N`) and committed
3. **submit** — straight away with *auto-submit once*, otherwise on *Submit to Agent*
4. **response** — the agent's reply (`## Response N`) is committed to the condoc
5. **commit** — each target repo the agent changed is committed and pushed
6. **record** — those commits are mapped into the condoc (`## Record N`) and committed
7. **unlock** — the pointer commits the lock's release

Every commit's subject is `condocv2: <Name> <kind>[ <N>]`. The lock records
which stage the interaction reached, so an error (fixed with *Resubmit*) or a
restart resumes it where it stopped.

## Completion and merge

Between interactions, **Complete** ends the condoc and re-integrates its
branches (`complete.go`):

1. **complete** — `## Completed` is added to the main file (condoc commit)
   and the pointer moves to `pr_drafting` (pointer commit)
2. **drafted** — an in-place agent turn writes `prs.yaml`: one PR per repo
   with changes (targets with nothing to merge are skipped), following the
   *PR guidance*, which defaults to *extremely concise*
3. **prs** — after editing (or *Redraft* with new guidance), *Raise PRs*
   opens them, targets first and the condoc repo last, and lists them under
   `## Pull Requests` in the main file
4. **merged** — *Merge*, once GitHub reports every PR mergeable, merges them
   in that order with merge commits and deletes the slopspace; the
   `condocv2/...` branches are kept

A failure leaves the error on the pointer; *Resubmit* (drafting) or the same
button again (raising, merging) resumes. The Visual tab doesn't draw these
commits yet.

## The UI

A condoc opens with two tabs:

- **Text** — the Prompt / Response / Record flow, and the prompt box.
- **Visual** — the swimlanes: one lane per repo (pointer, condoc, each
  target) and a node per commit above, read back from git by those subjects
  (`timeline.go`, `frontend/src/Swimlanes.tsx`).

## Building

`make build` (frontend, then the Go binary embedding it), `make test`.
