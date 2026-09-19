# Simple Distributed Session Sequences

Concrete, minimal sequences that exercise the distributed-session machinery
landed across `InitialDistributedSessions` Step 1 and Substep C (see
`docs/DistributedSessionsBrainstorm.md`'s Working Section for the underlying
design). Each sequence is described first as steps, then as the
`session.jsonl` we expect those steps to produce — an acceptance description
to test against (manually or otherwise), not a captured transcript.

## Sequence #1 — simple complete

### Topology

- **Node 1 (owner node)**: local-representative `LR-1`; two
  federation-command instances, `FC-1` and `FC-2`, both on this node (same
  `fcHostID`, different `fcHeadID` heads).
- **agent-coordinator (`AC`)**: one shared instance both LRs dial.
- **Node 2 (secondary node)**: local-representative `LR-2`; one
  federation-command instance, `FC-3`.

Commands typed directly into an FC instance are given verbatim below (e.g.
`sleep 2; echo hello; sleep 30` is the sort of thing you'd actually type at
the FC prompt); launching and wiring up the surrounding processes is
described in plain English, since that's operational setup rather than
something FC itself records.

### Steps

1. **Launch** `FC-1`, not yet connected to any LR. **Run:**
   `new-session "Simple Complete"`. clauditable creates the session directory
   and `session.yaml` with `owner` = Node 1's host ID (ownership is stamped
   at creation regardless of connection state).
2. On `FC-1`, **run:** `sleep 2`, then, once that finishes, **run:**
   `echo "fc-1 first"`. Each is a separate clauditable invocation. No
   writing-file collision exists, so clauditable is primary for both and
   consolidates each straight into `session.jsonl` on completion.
3. **Launch** `FC-2` on the same node (still disconnected) and **run:**
   `select-session` there, picking "Simple Complete". Then, on `FC-1`,
   **run:** `sleep 3`; at roughly the same wall-clock moment, on `FC-2`,
   **run:** `echo "fc-2 overlap"`. Both are same-host, so this is the
   existing local primary/secondary race (`LocalSessionImprovments.md` Step
   3): whichever writing file lands first wins primary; the other writes as a
   same-host secondary (`-s-writing.txt`/`-s-raw.txt`). The next time the
   primary side consolidates, it picks up the secondary's `-s-raw.txt` too,
   processes it, and promotes it to a plain `-raw.txt`. Nothing distributed
   is exercised yet — this step confirms the baseline concurrency handling
   still works underneath the distributed changes.
4. **Connect** `FC-1` to `LR-1`, and connect `LR-1` to `AC`. Bring up Node 2:
   **connect** `LR-2` to the same `AC`, and **connect** `FC-3` to `LR-2`.
5. On `FC-3`, **run:** `list-sessions`. FC sends `LR-2` a
   `session-sync-request{kind:"list"}`; `LR-2` calls `AC`'s `GET /api/hosts`,
   finds `LR-1` connected, pulls `LR-1`'s `GET /api/sessions`, and writes
   Node 1's `session.yaml` into Node 2's own records path. `FC-3` now lists
   "Simple Complete" tagged `[remote: <Node 1 host>]`, rendered grey-blue.
6. On `FC-3`, **run:** `select-session`, choosing the remote-tagged entry —
   selectable exactly like a local one.
7. Send overlapping commands: on `FC-1`, **run:** `sleep 2`; at roughly the
   same wall-clock moment, on `FC-3`, **run:** `echo "fc-3 remote"`.
   - `FC-1` (owner host) is primary as always: clauditable writes and
     consolidates its own record directly into `session.jsonl`.
   - `FC-3` (non-owner host) is always secondary regardless of timing
     (`isRemoteOwned` short-circuits the same-host race entirely): clauditable
     writes `{ts}-Node2-writing.txt` → `{ts}-Node2-raw.txt`, immediately
     self-processes to `{ts}-Node2-processed.txt`, and never touches a
     `session.jsonl` of its own. `FC-3`'s own meta-command bookkeeping
     (`appendSessionRecord`) is likewise a no-op for this session, since it
     detects the session is remote-owned — consistent with Substep C's fix
     for "the secondary should not build the jsonl."
8. Before its next append, `FC-1` sends `LR-1` a
   `session-sync-request{kind:"append"}`. Once the cross-host content-sync
   backend behind that request exists (still a stub — see
   `docs/DistributedSessionsBrainstorm.md`'s "Open for a later increment"),
   `LR-1` pulls the processed+session-file glob for this session from every
   connected participant, including Node 2's `{ts}-Node2-raw.txt`/
   `{ts}-Node2-processed.txt`, into Node 1's copy of the session directory.
   The next time `FC-1`'s primary clauditable consolidates,
   `consolidatePrimaryToJSONL` picks up that host-tagged raw file, reuses
   Node 2's own processed copy as-is, appends it to `session.jsonl` in
   timestamp order, and marks it consolidated
   (`{ts}-Node2-raw.txt.consolidated`).

### Expected resulting `session.jsonl`

- **Exists in exactly one place that matters**: Node 1's copy, the owner.
  `FC-2` never diverges from it (same host, same primary/secondary
  consolidation as always). `FC-3` never builds one of its own for this
  session at all — `appendSessionRecord` and `consolidatePrimaryToJSONL`
  both refuse to act on a session they don't own — which is precisely the
  behavior Substep C's fix restored.
- **Entries, in timestamp order**:
  1. `new-session "Simple Complete"` — from `FC-1`.
  2. `sleep 2` — from `FC-1`.
  3. `echo "fc-1 first"` — from `FC-1`.
  4. `echo "fc-2 overlap"` — consolidated in from `FC-2`'s promoted
     `-s-raw.txt`; distinguishable from `FC-1`'s entries only by head, since
     the `-s-` marker is stripped once promoted.
  5. `sleep 3` (the overlap round) — from `FC-1`.
  6. `echo "fc-3 remote"` — from `FC-3`, *only once* the `"append"` sync
     backend in step 8 above actually exists and has run; until then,
     `session.jsonl` stops at entry 5, and Node 2 instead holds this
     command's raw/processed pair locally, unsynced.
- Meta-commands (`list-sessions`, `select-session`, …) are not expected to
  appear as entries *of this session* — they're recorded, if at all, against
  whichever session is current on the issuing host at the moment they're
  typed (typically each node's own local/default session before it switches
  into "Simple Complete"), not against the session being joined or listed.
  `new-session` is the one exception: it necessarily writes its own first
  entry into the session it just created.
- Node 2's own records directory retains `{ts}-Node2-raw.txt.consolidated`
  and `{ts}-Node2-processed.txt` as local provenance of its participation,
  but at no point does a second, diverging `session.jsonl` for "Simple
  Complete" appear anywhere on Node 2 — the exact "A sees B, B builds its own
  diverging session.jsonl" symptom Substep C fixed.

### Sample `session.jsonl` content

Concretely, with Node 1's host ID `node1-a1b2` and heads `fc-1111` (`FC-1`)
and `fc-2222` (`FC-2`), and Node 2's host ID `node2-c3d4` with head `fc-3333`
(`FC-3`), the fields follow `CommandRecord` in `federation-command/main.go`
(`id`, `cmd`, `ts` = start time, `delta_ms` = duration, `exit`, `host`,
`head`). After step 3 (before Node 2 ever joins), `session.jsonl` reads:

```json
{"id":"e1a2b3c4","cmd":"new-session \"Simple Complete\"","ts":"2026-09-20T10:00:00Z","delta_ms":40,"exit":0,"host":"node1-a1b2","head":"fc-1111"}
{"id":"f2b3c4d5","cmd":"sleep 2","ts":"2026-09-20T10:00:05Z","delta_ms":2004,"exit":0,"host":"node1-a1b2","head":"fc-1111"}
{"id":"a3c4d5e6","cmd":"echo \"fc-1 first\"","ts":"2026-09-20T10:00:07Z","delta_ms":32,"exit":0,"host":"node1-a1b2","head":"fc-1111"}
{"id":"b4d5e6f7","cmd":"echo \"fc-2 overlap\"","ts":"2026-09-20T10:00:15Z","delta_ms":34,"exit":0,"host":"node1-a1b2","head":"fc-2222"}
{"id":"c5e6f7a8","cmd":"sleep 3","ts":"2026-09-20T10:00:15Z","delta_ms":3005,"exit":0,"host":"node1-a1b2","head":"fc-1111"}
```

Once the `"append"` sync backend in step 8 lands and has actually run, a
sixth line is appended:

```json
{"id":"d6f7a8b9","cmd":"echo \"fc-3 remote\"","ts":"2026-09-20T10:03:00Z","delta_ms":38,"exit":0,"host":"node2-c3d4","head":"fc-3333"}
```

Note that entry keeps *Node 2's* `host`/`head` — `FC-3`'s own clauditable
resolved and stamped them locally before the record was ever synced to Node
1 — so the consolidated line is self-describing provenance of who actually
ran it, even though it lives only in the owner's `session.jsonl`.
