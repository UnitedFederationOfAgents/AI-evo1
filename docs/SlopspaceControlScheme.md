# Slopspace Control Scheme

How condoccer runs a condoc under the `slopspace` control scheme. This is the second control scheme, alongside `same-repo`. It covers the target flow, what dungeon-keeper can already do, what is missing, the decisions still open, and the order to build things in.

Originating condoc: [SlopspaceCondocs](../condocs/SlopspaceCondocs.md), Step 1 Revision A.

## Target Flow

1. **Pointer repo.** The top-level setting is a *condoc pointer repo*, used instead of a working dir.
   - Defaults: repo `je-sidestuff/sloppo`, target branch `main`, workspace path `../sloppo`.
   - If the path is missing, condoccer offers to clone the repo. It clones over ssh by default.
   - Condoccer pushes to and pulls from the target branch regularly. The pointer repo records each condoc's state (step, substep, iteration, phase), so anyone can see where a condoc has got to.
   - Each pointer names 1-N **target repos**, which must all be different. It also names the **condoc repo** where the condoc files live (default: `sloppo` itself).
2. **Proposal.** Once a valid pointer repo is set, condoccer can propose new condocs.
   - While the condoc is still a proposal, the targets and the rest of the pointer spec can be edited.
   - The **branch** is shared by every target repo and the condoc repo, and must not exist yet in any of them. The default is the same as in `same-repo`: `condoc/<Name>-<startTime>/main`.
3. **Accept.** Accepting creates the condoc's slopspace. Every target repo and the condoc repo gets a writespace, checked out on the new branch.
4. **Progression.** This looks and feels like `same-repo`: steps, revisions, substeps and resources. Underneath, it is different:
   - Each agent turn goes like this: deploy the slopspace, send a work signal to the dungeon-keeper `agent-worker`, wait, then undeploy (return).
   - Each condoc has its own slopspace, so one condoccer can run N condocs in parallel.
   - After each turn, condoccer commits and pushes in every repo that changed.
   - The condoc links **each repo/commit pair** for every reply, not just one commit.
5. **Propose PR.** In this scheme, completing a condoc leads into a new `propose PR` phase.
   - An in-place worker call, run in a tempdir, asks the agent for a YAML template describing the PRs.
   - The human can edit the template, regenerate it with an instruction, or accept it.
   - Accepting raises a PR in every target repo and in the condoc repo.
6. **PR.** While PRs are open the condoc is in the `PR` phase.
   - The UI shows when the PRs are mergeable and then offers a **Merge** control.
   - Pressing Merge merges the PRs.

## What Exists Today

### Same-repo scheme (for contrast)

- **The handler is federation-command, not condoccer.** `federation-command/condoc.go` holds one `CondocSession` per TUI. It creates the branch, runs the agent in `repoRoot` through `buildAgentPromptCmd`, writes `## Reply` sections, and commits and pushes.
- **Condoccer only writes directives.** It appends `!HANDOFF!`, `!COMPLETED!`, `!REVERT-…!` and filled templates to files (`performAction` in `condoccer/main.go`), then watches the files for changes.
- **Progress lives in the file tree.** It is detected from file content (`detectPhase`). A committed `.condoc` lock file shows who holds the condoc.
- **Retry is single-repo.** It pushes a take branch and then force-pushes a reset of the condoc branch (`runCondocRetryGitSequence`).

### dungeon-keeper

| Capability | Where | Status for this scheme |
|---|---|---|
| Create / list / delete slopspace | `pkg/slopspace` `Create/List/Delete` | Usable as-is |
| Deploy / return slopspace | `Deploy(id, agentType)` / `Return(id)` | Usable. **One deploy path per agent type** (`/agent/agent-worker`) |
| Shared writespace clone pool | `pkg/writespace` `CloneRepo` (clone, or `pull --rebase` if it already exists) | Usable. **HTTPS + `TF_VAR_github_pat` only** |
| Add a repo to a slopspace's writespaces on a new branch | `AddWritespaceRepo` (copy, `checkout -b <ref>`, `.git` moved to `writespaces-secure`) | Usable. Doesn't check whether the branch already exists on the remote |
| Commit and push writespace repos | `WriteRepoChanges` / `WriteAllRepoChanges` | Usable. **Doesn't return the commit SHA.** `WriteAll` keeps only the last error |
| Readspace repos (read-only context) | `AddReadspaceRepo` | Not needed for the first cut |
| Work signals | `pkg/worksignal` (`ongoing/` → `complete/`, holder, events, captured output) | Usable |
| Slopspace work | `processSlopspaceWork` | **Uses whatever slopspace is currently deployed for its agent type, or creates a fresh one.** Signals carry no slopspace ID. Doesn't return the slopspace if the agent fails |
| In-place work | `processInPlaceWork` (`work_location`) | Usable as-is for the propose-PR tempdir call |
| Concurrency | Watch loop, one signal at a time | Agent runs are serialized per worker |
| LR integration | `repr.go` pushes `worker-state` | Lets LR's worker tab show condoc turns for free |
| PR creation / merge | — | **Does not exist anywhere in the repo** (no `gh` usage) |

## Considerations

- **Something new has to drive the lifecycle.** In `same-repo`, federation-command does the real work. For `slopspace`, *something* must create slopspaces, send signals, write replies, push, and raise PRs, and condoccer is the only component that sees all N condocs (see D1).
- **Parallelism is really interleaving.** With one deploy path per agent type and a worker that handles one signal at a time, N condocs can be *in flight* at once (waiting on humans, waiting for PRs). Agent turns still run one after another unless we add per-slopspace deploy paths or more workers (D3).
- **Deploy/return races.** The worker uses "whatever is deployed". If condoccer deploys slopspace B before the worker has returned A, B's turn could run in A's slopspace. The worker also leaves a slopspace deployed when the agent fails. Either condoccer serializes deploys strictly, or signals name their slopspace and the worker owns deploy and return (D3, D4).
- **Condoc files vanish while deployed.** `Deploy` *moves* writespaces to `/agent/agent-worker`. During a turn, the condoc repo's files are no longer at the slopspace path, but the UI still has to render the condoc (D9).
- **Who writes the reply.** In `same-repo` the handler captures the agent's terminal output and writes `## Reply` itself. We keep that: the worker records output as a work-signal event, and condoccer writes the reply into the condoc repo writespace *after* return, then commits.
- **Commit links per repo.** The reply preamble becomes a list of `owner/repo@sha` links instead of the single prompt→reply pair. `WriteRepoChanges` has to report SHAs and take a message (D8).
- **Two auth paths.** The pointer repo is cloned over ssh by default, but dungeon-keeper's writespaces use HTTPS+PAT. PR creation and merging also need a token (D6).
- **Branch uniqueness across repos.** This needs `git ls-remote --heads` on every target repo plus the condoc repo, done at accept time. `AddWritespaceRepo` only fails when the branch exists *locally* (D7).
- **Pointer repo contention.** Several condoccers, or several condocs in one condoccer, push to the same `main`. One pointer file per condoc keeps conflicts to `pull --rebase` retries. The lock currently lives in the condoc repo; for slopspace condocs, a holder field in the pointer replaces it (D4, D5).
- **Retry and revert are multi-repo.** The current retry rewrites history in one repo. Here it has to reset every target repo plus the condoc repo, and leave a take branch in each (D10).
- **Merging is not atomic.** N+1 PRs can't be merged as a unit, so partial failure has to be a visible state (D12).
- **The Hide Completed filter needs updating.** Slopspace condocs pass through `completed` on the way to `pr_proposed`, `pr_open` and `merged`. The filter on that tab should key off `merged`, not `completed` (D11).

## Decision Points

Each decision lists its options. **Rec** marks the one I recommend.

### D1 — Who is the slopspace handler?
- a. **Rec:** the condoccer backend. It already runs for a long time, owns the UI, and has to see every condoc to propose them and run them in parallel. It drives dungeon-keeper directly.
- b. Extend federation-command's `CondocSession`. It holds one session per TUI, so it doesn't fit N condocs.
- c. A new daemon. This makes the "pointer repo" idea cleaner, but it is one more process for LR to manage.

### D2 — How does condoccer call dungeon-keeper?
- a. **Rec:** import `dungeon-keeper/pkg/{slopspace,writespace,worksignal,types}` as a Go module. Condoccer already pulls in `representable` and `ufa-version` the same way. The worker itself stays a separate `watch` process.
- b. Shell out to the `dungeon-keeper` CLI. This decouples the two but means parsing human-readable output.
- c. Go through local-representative (`submit` over representable). This extends the existing worker-tab path, but slopspace management isn't exposed there.

### D3 — How are agent turns made concurrent?
- a. **Rec (first cut):** serialize. Condoccer keeps a FIFO of pending turns and a single deploy lock for `agent-worker`. N condocs are in flight, and their turns are queued.
- b. Add `slopspace_id` to `WorkSignal`. The worker then deploys that slopspace (to `/agent/agent-worker/<id>`, or the fixed path) and returns it itself. This removes the race and makes (c) possible later. **Rec as a dungeon-keeper change paired with (a).**
- c. Several worker processes, each with its own `AGENT_SLOPSPACE_ROOT`/`WorkerID`. This gives real parallel agent turns but needs procman/LR support.

### D4 — Who deploys and returns?
- a. Condoccer deploys, the worker returns (today's behavior). This needs a fix so the slopspace is returned when the agent fails.
- b. **Rec:** the worker owns both, keyed on the signal's `slopspace_id` (see D3b), always returning in a `defer`. Condoccer only creates the slopspace, sends signals, and handles commit and push.

### D5 — Pointer repo layout and spec
- a. **Rec:** one file per condoc, `pointers/<Name>-<startTime>.yaml`, holding:
  - static spec: `name`, `startTime`, `controlScheme: slopspace`, `branch`, `condocRepo`, `condocPath`, `targets[]`
  - runtime: `slopspaceId`, `holder` (condoccer host/instance), `state{phase, step, substep, iteration}`, `prs[]`, `updatedAt`
  - Condoc files go under `condocs/` in the condoc repo, laid out as they are today.
- b. A single `index.yaml` listing every condoc. Easier to scan, but every update conflicts.
- c. The state lives only in the condoc-yaml header inside the condoc file. Simpler, but then the state sits on the condoc *branch* rather than on the pointer's `main`.
- Sub-decisions:
  - How often to push pointer updates. Rec: on every phase change, not every tick.
  - Whether proposals that were never accepted are committed. Rec: yes, as `phase: proposed`, so they show up on other hosts.

### D6 — Git transport and credentials
- a. ssh everywhere. dungeon-keeper's `CloneRepo` gains an ssh URL mode.
- b. **Rec:** ssh for the pointer repo, which is what the brief asks for. Keep HTTPS+PAT for dungeon-keeper writespaces, and use the same PAT through `gh` (or the REST API) for PRs. Make the transport configurable per repo later.
- c. HTTPS+PAT everywhere, including the pointer clone. Contradicts the "ssh by default" requirement.

### D7 — Branch validation
- When: a. live while editing (advisory) and again on accept (blocking). **Rec.** b. Only on accept.
- How: `git ls-remote --heads <remote> <branch>` on each target repo and the condoc repo.
- On conflict: block and offer to bump the default branch's timestamp.
- Can the condoc repo also be one of the targets?
  - a. **Rec:** no. Keep them distinct for now, so the agent's code changes and the condoc bookkeeping don't share a commit.
  - b. Yes, with the two collapsed into one writespace.

### D8 — Commits, messages and links
- Extend `WriteRepoChanges` to return `(sha string, changed bool, err)`, and make `WriteAllRepoChanges` return per-repo results with every error kept.
- Message convention, all repos: `condoc: <Name> step N <iteration> reply`. For the condoc repo, the prompt edit is its own commit made before deploying: `… prompt`.
- Link format in the reply preamble:
  - a. **Rec:** one line per repo: `- owner/repo: [\`abc1234\`](url)`. Repos with no changes are listed as `(no changes)`.
  - b. One inline sentence.
  - c. A YAML block.

### D9 — How the UI reads condoc content during a turn
- a. **Rec:** condoccer keeps a checkout of the condoc branch in the workspace and pulls after each push, for example a `git worktree` of `../sloppo` on the condoc branch. Rendering never touches the slopspace.
- b. Read from the slopspace's root, or from its deploy path if `SLOPSPACE.json` says it is deployed.
- c. Make the condoc repo a readspace for the agent and keep condoccer as the only writer. This conflicts with the brief, which gives the condoc repo a writespace.

### D10 — Scope of iteration types in the first cut
- a. **Rec:** handoff, revision, substep and resources first. Retry and revert come later, as a multi-repo version of `runCondocRetryGitSequence` with a take branch per repo.
- b. Everything at parity before shipping.

### D11 — Phase model for slopspace condocs
- Add `pr_proposed`, `pr_open` and `merged` after `completed`. `!COMPLETED!` on a slopspace condoc moves it to `pr_proposed` rather than ending it.
- Phases go in the pointer `state.phase` (D5) and are mirrored in the condoc file, for example as `## PR Proposed` / `## PR Raised` / `## Condoc Merged` sections, so the history stays readable.
- Slopspace teardown:
  - a. **Rec:** delete the slopspace on `merged`.
  - b. Delete it as soon as the PRs are raised.

### D12 — PR template, raising and merging
- **Tempdir contents** for the in-place call:
  - the condoc files
  - for each repo, `git diff <base>...<branch>` plus the log
  - the pointer spec
- **YAML schema:** `prs: [{repo, title, body, base}]`, with the condoc repo included. Optionally a shared `summary` that is reused in every body.
- **Regenerate with instruction:** a new in-place signal that includes the previous YAML and the instruction.
- **Raising PRs:**
  - a. **Rec:** `gh pr create` for each repo.
  - b. The GitHub REST API with the PAT.
- Each PR body links its sibling PRs.
- **Mergeability:** poll `gh pr view --json mergeable,mergeStateStatus,statusCheckRollup`. The Merge button is enabled only when *all* PRs are mergeable.
- **Merge method:**
  - a. **Rec:** merge commit, which keeps the per-revision SHAs that the condoc links point to.
  - b. Squash.
  - c. Configurable per repo.
- **Merge order and partial failure:** merge the target repos first and the condoc repo last. If one fails, stop, show the PR states repo by repo, and allow a retry.
- **After merge:** delete the branches (rec: yes, except take branches), mark `merged`, and update the pointer.

### D13 — Agent/model selection for worker turns
- a. **Rec:** a default agent/model in the condoccer config, which the proposal form can override per condoc. Store it in the pointer spec.
- b. Use the current fedcom agent, which isn't available to condoccer.

### D14 — Where things run
- Condoccer and the `agent-worker` must share `/host-agent-files` (slopspaces, work signals) and `/agent`. Is condoccer expected to run in the worker's container or on the host alongside it?
- Rec: on the host for now, with `SLOPSPACES_DIR`/`WORK_SIGNALS_DIR` read from the same env vars dungeon-keeper uses.

## Implementation Plan (next steps)

1. **Pointer repo setting and Slopspace tab, read-only**
   - Add the pointer repo config (repo, branch, path) next to the working-dir root picker, with the clone-over-ssh offer.
   - Settle the pointer file format (D5).
   - The Slopspace tab lists the pointers it finds, with their phase.
2. **dungeon-keeper groundwork**
   - Add `slopspace_id` to signals, and have the worker deploy and return with return-on-failure (D3b/D4).
   - Make `WriteRepoChanges` return SHAs, with per-repo results and a commit message (D8).
   - Add a remote-branch existence check helper (D7).
   - Add tests in `pkg/slopspace` and `main_test.go`.
3. **Proposal and accept**
   - Proposal form for targets, condoc repo, branch and agent/model, which writes a `phase: proposed` pointer.
   - On accept: validate the branch, clone the writespaces, create the slopspace and `AddWritespaceRepo` each repo on the branch, write the condoc template into the condoc repo, push the branch everywhere, and update the pointer.
4. **Progression**
   - Turn queue (D3a). Prompt commit, then signal, then wait for completion.
   - After return: write the reply with per-repo links, then commit and push changed repos.
   - Update the pointer state. Reuse the same-repo step/revision/substep UI components on the Slopspace tab.
5. **Propose PR:** the in-place tempdir signal, the YAML editor, regenerate, and accept to raise the PRs (D12).
6. **PR phase and Merge:** mergeability polling, the Merge control, ordered merging, partial-failure display, and teardown (D11/D12).
7. **Later:** multi-repo retry and revert (D10), and real parallel agent turns with several workers (D3c).

Steps 1 and 2 are independent and can run in either order. Step 3 needs both.
