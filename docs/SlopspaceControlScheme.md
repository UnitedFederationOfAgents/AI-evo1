# Slopspace Control Scheme

How condoccer runs a condoc under the `slopspace` control scheme. This is the second control scheme, alongside `same-repo`. It covers the target flow, what dungeon-keeper can already do, what is missing, the decisions (settled and open), and the order to build things in.

Originating condoc: [SlopspaceCondocs](../condocs/SlopspaceCondocs.md), Step 1 Revisions A and B.

## Revision B Changes

These changes from Revision B settle or reshape several earlier decisions:

- **No simultaneous execution.** Agent turns queue, one at a time. Successive turns may belong to different condocs, so condoccer can go back and forth between condocs on each invocation. "N condocs" means N *in flight*, not N running at once.
- **HTTPS + PAT everywhere**, including the pointer repo, in place of ssh.
- **PR management is new code.** AI-sandboxing is the reference starting point, but the implementation is new, and where it lives is an open decision (D10).
- **No second checkout during a turn.** The UI watches the condoc where it is expected to be: at the slopspace path when idle, and at the deploy path while deployed.
- **Pointer repo settings are implemented.** See [Status](#status).

## Target Flow

1. **Pointer repo.** The top-level setting is a *condoc pointer repo*, used instead of a working dir.
   - Defaults: repo `je-sidestuff/sloppo`, target branch `main`, workspace path `../sloppo`.
   - If the path is missing, condoccer offers to clone the repo over HTTPS, using the GitHub PAT.
   - Condoccer pushes to and pulls from the target branch regularly. The pointer repo records each condoc's state (step, substep, iteration, phase), so anyone can see where a condoc has got to.
   - Each pointer names 1-N **target repos**, which must all be different. It also names the **condoc repo** where the condoc files live (default: `sloppo` itself).
2. **Proposal.** Once a valid pointer repo is set, condoccer can propose new condocs.
   - While the condoc is still a proposal, the targets and the rest of the pointer spec can be edited.
   - The **branch** is shared by every target repo and the condoc repo, and must not exist yet in any of them. The default is the same as in `same-repo`: `condoc/<Name>-<startTime>/main`.
3. **Accept.** Accepting creates the condoc's slopspace. Every target repo and the condoc repo gets a writespace, checked out on the new branch.
4. **Progression.** This looks and feels like `same-repo`: steps, revisions, substeps and resources. Underneath, it is different:
   - Each agent turn goes like this: deploy the slopspace, send a work signal to the dungeon-keeper `agent-worker`, wait, then undeploy (return).
   - Each condoc has its own slopspace, and only one is deployed at a time. Turns from different condocs are queued and run in order, so one condoccer can interleave N condocs.
   - After each turn, condoccer commits and pushes in every repo that changed.
   - The condoc links **each repo/commit pair** for every reply, not just one commit.
5. **Propose PR.** In this scheme, completing a condoc leads into a new `propose PR` phase.
   - An in-place worker call, run in a tempdir, asks the agent for a YAML template describing the PRs.
   - The human can edit the template, regenerate it with an instruction, or accept it.
   - Accepting raises a PR in every target repo and in the condoc repo.
6. **PR.** While PRs are open the condoc is in the `PR` phase.
   - The UI shows when the PRs are mergeable and then offers a **Merge** control.
   - Pressing Merge merges the PRs.

## Status

### Done: pointer repo settings (Step 1 Revision B)

- **Backend** (`condoccer/pointer.go`):
  - The config is repo (`owner/name`, or a pasted GitHub URL), branch and path. It is seeded from `--pointer-repo`, `--pointer-branch` and `--pointer-path`, which default to `je-sidestuff/sloppo`, `main` and `../sloppo`, and can be changed from the UI.
  - A relative path resolves against the git repo holding `--root`, fixed at startup. That makes `../sloppo` a sibling checkout in the same workspace.
  - The repo state is reported as one of:
    - `missing`: nothing at the path.
    - `cloning`.
    - `invalid`: not a git repo, `origin` is not the configured repo, or HEAD is not on the target branch.
    - `ready`.
- **Clone and pull** go over HTTPS. The PAT comes from `TF_VAR_github_pat`, which is the variable dungeon-keeper uses, with `GH_TOKEN` and `GITHUB_TOKEN` as fallbacks.
  - The PAT is passed as an `Authorization` header through `GIT_CONFIG_*` environment variables. It never appears in argv or `.git/config`. Without a token, public repos still work.
  - A ready repo is pulled with `pull --rebase --autostash` on startup, whenever the config changes, and every 15 seconds. There is no manual Pull (Revision C). After each pull, local pointer commits that a failed push left behind are pushed.
- **Pointer listing.** `pointers/*.yaml` files are summarized as name, phase, step/substep/iteration, branch, condoc repo and targets.
  - The parser is shallow and has no dependencies. It handles top-level scalars, the `state:` map, and the `targets:` list.
  - `gopkg.in/yaml.v3` comes in when the full spec is read and written (step 3).
- **WebSocket messages:**
  - `pointer`: the status, sent on connect and whenever it changes.
  - `set-pointer` and `clone-pointer`: sent by the client.
- **Frontend:** the Slopspace tab's main pane shows a `PointerRepoControl` with the repo@branch, the resolved path and the state.
  - It offers **Clone** when the repo is missing, plus a **Set Pointer Repo** form.
- **Tests:** `condoccer/pointer_test.go` covers remote normalization, config validation, pointer parsing, and the `missing` → `invalid` → `ready` → `invalid` state transitions against a local git repo.

### Done: the full lifecycle, first cut (Step 1 Revisions C and D)

Not yet built or run end to end; see the Step 1 replies.

- **Pointer spec** (`condoccer/slopspec.go`, `miniyaml.go`): the whole `pointers/<Name>-<startTime>.yaml` is read and written (D4). It uses a small built-in YAML subset rather than `yaml.v3`. The listing uses the full spec and falls back to the shallow parser for files it can't read. Only the condoccer named in `holder` drives a condoc; other condoccers show it read-only.
- **Pointer writes:** every phase change commits that one pointer file and pushes, rebasing and retrying on conflict. Turn stages are only written locally, so a restart resumes them without a push per stage.
- **Handler** (`condoccer/slopspace.go`), with condoccer as the handler (S1):
  - **Propose, edit, withdraw and accept.** Accept checks the branch on every repo's remote, clones the writespaces, creates the slopspace with each repo on the branch, writes the condoc with step 1 templated, and publishes the branch everywhere.
  - **Driver.** Every 2 seconds it reads each held condoc from its expected location (D7) and reacts to the same `!HANDOFF!`/`!COMPLETED!` directives the same-repo UI writes. That covers start step, revision, substep, complete substep/step/condoc. The condoc-file edits are ported from federation-command into `condoccer/condocfiles.go`.
  - **Force Complete.** The UI's **Force Complete** skips the directive here, since condoccer is the handler. It closes any open substep and step, completes the condoc, and commits, which leads into propose-PR. A turn that failed is dropped along with its error. A turn still running must land first. (A same-repo condoc gets `!FORCE-COMPLETED!` on its main file instead. federation-command acts on it whenever it's idle or stuck on a git error.)
  - **Turn queue (D2).** One FIFO, rebuilt from the pointers on startup. A turn moves through `queued → deployed → signalled → returned → replied`, and its stage is saved after each. Condoccer deploys, writes the work signal, waits for the worker's `complete/` file, and returns the slopspace (D3, D13). It then commits and pushes each changed target and writes the reply with one commit line per repo, and the condoc repo is committed last (D6).
  - **Propose PR.** It runs an in-place turn in a tempdir inside the slopspace, holding the condoc, each repo's diff and log, and the pointer. The agent writes `prs.yaml` there. If that file is unusable, a default template is filled in and the reason is shown.
  - **Raise PRs.** The template can be edited, regenerated with an instruction, or accepted. Accepting raises one PR per repo, links the sibling PRs, and records them in the condoc.
  - **Merge.** Mergeability is polled every 30 seconds. Merge is enabled once every PR is mergeable. It merges the targets first and the condoc repo last, using merge commits, then deletes the branches and the slopspace.
- **PR code** lives in the new top-level `pr-keeper` module (D10b, chosen in Revision C as shared code for later claudomation use). It uses the GitHub REST API with the PAT.
- **dungeon-keeper:**
  - `WriteRepoChanges` returns SHAs.
  - New `RepoHead` and `PushRepo` helpers.
  - The worker leaves a slopspace someone else deployed for that deployer to return.
  - The PAT is kept out of `.git/config` (`pkg/gitauth`).
- **Frontend:**
  - The Slopspace tab lists the pointers with their phase, queue position, a running spinner and any error, plus **+ Propose a condoc**.
  - A proposal opens a form with **Check Branch**, **Accept** and **Withdraw**.
  - An accepted condoc opens in the same condoc/step/substep views as same-repo. Above them sits a slopspace panel with the condoc's state, **Resubmit** on error, the PR template editor (**Accept & Raise PRs** / **Save Edits** / **Regenerate**), and the PR list with **Merge**.
- **Resources (Step 1 Revision E):** **Add Resources…** works the same as in same-repo, with all three sources (Highlighted, Upload/select from host, Voice Note). Resources always go into the condoc repo, in the condoc's Impls folder in the slopspace, and never into a target repo. The agent can copy them across itself, since both repos are in its slopspace. Adding a resource claims the condoc, the same as any other UI edit, so it's refused while a turn is in flight. No `.condoc` lock is needed, because the files aren't in the working dir. The files are committed along with the next prompt commit, as in same-repo. `/api/resource/` serves them from wherever the condoc repo currently is.
- **Not supported:** retry and revert (D8, settled in SlopspaceSlopspaceCondocs Step 1). The UI hides their controls for slopspace condocs and the backend refuses the actions.

### Done: same-repo features carried over (SlopspaceSlopspaceCondocs Step 1)

- **Diff viewer.** Each reply gets the same **⊞ View files changed** entry as in same-repo. The files-changed pane lists every changed repo's files, headed by repo, and each file opens in the usual hunk-by-hunk diff view. The list covers the targets the turn changed, the condoc repo (always: the reply itself lands there, along with its condoc files), and the pointer repo's pointer file when the turn changed it.
  - The frontend reads the ranges off the reply preamble's per-repo links (`parseSlopCommitRanges`). A reply's range is `owner/name@<base>,…` → `owner/name@<head>,…`. A repo listed with only a head diffs that one commit, and a `(no changes)` repo is left out.
  - The condoc repo and the pointer repo are committed only after the reply is written, so the preamble can't link their heads. Their lines read `- owner/name: [base](…) → this reply` and go into the range with a base and no head. The pointer repo's base is its HEAD when the turn starts, recorded with the turn's other bases. It is left out when the pointer repo is also one of the condoc's repos. Replies written before this have no such lines and list only their targets.
  - `get-diff`/`get-file-diff` on a `slopspace:` path go to `slopHandleGetDiff`/`slopHandleGetFileDiff`. They check each repo is one of the pointer's repos, or the pointer repo, and run git in its `writespaces-secure` git dir, which stays put whether or not the slopspace is deployed. For the pointer repo they use its checkout, limited to this condoc's pointer file. Files come back as `owner/name/<path>`. A target's list leaves out condoc files, as in same-repo; the condoc repo's and pointer repo's lists don't.
  - `slopResolveHead` finds the missing heads. In the condoc repo, the head is the first commit after the base, which is the reply's: nothing else commits there during a turn. In the pointer repo, it is the first commit after the base to the pointer file that clears the turn. If that commit doesn't exist yet, the repo is left out. It is also left out if a pointer push rebased the base away.
  - The diff's place in the URL hash is URI-encoded, so a refresh lands back on a slopspace diff too.
- **Agent-created resources (Revision B).** A turn's agent can add resources to the condoc, as well as the human. `condoccer/agentresources.go` handles it.
  - Every turn's prompt names a staging folder, `writespaces/condoc-resources/` in the deployed slopspace (`agentResourcesNote`). The folder is outside every repo, so nothing commits it. The agent writes files there and doesn't edit the condoc files or link anything itself. It may explain what it added in its reply.
  - The folder is emptied when the turn starts. After the slopspace is returned and the reply written, `addAgentResources` moves each file into the condoc's Impls folder, the same place the human sources put theirs, under the same `<8-hex>_<name>` filename an Upload gets. Zips are unzipped as usual. It then inserts a `## Resource N (agent)` block just after the reply, saying which reply the files came with, and the files are committed with the reply. Hidden files are ignored. Directories, symlinks and files over 64MiB are left in the folder and listed as "Not linked". If nothing was staged, no block is added. A failure here is logged and doesn't fail the turn, since the reply is already written.
  - The ` (agent)` marker sits between the number and any ` -- <name>`, so numbering is shared with human resources. `parseIterations` sets `agent` on the iteration and the frontend's `parseStepSections` does the same. The UI shows these with a hollow sidebar dot, a dashed section border and an **agent** tag on the label.
  - Only slopspace turns have this. Same-repo turns are run by federation-command, which doesn't stage or collect agent resources.
- **Resources to files (Revision E).** Any resource with linked files, same-repo or slopspace, human or agent, has a **to files** button at the right of its label. It copies those files into this condoccer's local-representative's files tab (`condoccer/tofiles.go`'s `POST /api/resource-to-files`). Each file goes as an ordinary upload to LR's `POST /api/files`, one per request so LR's 64MiB cap applies per file, and its `<8-hex>_` prefix is stripped first so LR's own isn't stacked on it. The condoc isn't changed.
  - **Already in files (Revision F).** When the button mounts (and after a copy), condoccer asks `GET /api/resource-in-files` which linked files are already in LR's files tab. A file counts when LR lists one with its original name and size and LR's bytes for it (`GET /api/files/<id>`) match; the newest such copy wins. Once every linked file is there, the button is lit green and pressing it posts a `condoccer-open-file` message with that LR file id (the first, for a multi-file resource) to the page embedding condoccer. LR, and AC's per-host view, take it only from their own condoccer frame and switch to the files tab with that file selected. Opened outside an embedding page, the lit button just says the file is already in files. Until then, pressing it copies only the files not yet there.
- **Sequence icons in the files tab (Revision E).** A `.yaml`/`.yml` file in the host-cache or host-store that would import cleanly as a sequence gets its own icon in LR's and AC's files tabs. A control-tab sequence (`lr-control-v1`) shows three linked steps, and a robot sequence (IANAR's `ianar-sequence-v2`) shows a robot's head. `local-representative/seqfiles.go` sets `FileInfo.sequence` on each listing, and `kind` stays `text`, so the viewer is unchanged. A control sequence is checked with the control tab's own decoder and `checkControlLibrary`. IANAR is a separate module, so the robot sequence-v2 checks are ported into `seqfiles.go`. Its instructions are checked against IANAR's reported ops once IANAR has sent them, and for naming an op until then. Only a document holding at least one sequence counts, and it must be self-contained: its steps' actions must be in the same file.
  - **Integrations (Revision G).** Below the standard file-details actions, in LR's and AC's files tabs, a sequence file gets an "integrations" subsection with **import** and **import and run** (`local-representative/fileintegrations.go`'s `POST /api/files/<id>/import[?run=1]`; AC reaches it through `/host/<id>/*`, like hold/persist). **Import** merges the file into its tab's library, the same as that tab's own import. **Import and run** then runs the document's *first* sequence with every control at its default, so it only works for a sequence that can run that way. A control sequence goes to LR's control engine (`handleLibRequest` `import`, then `start`), and the run shows in the control tab. A robot sequence is sent to IANAR as `__robot:seq-import <json>` (`ianar/lrimport.go`), which merges it as `seq2-import` does and answers `robot-seq-import-result` once the import is done and any run has started (LR waits up to 15s). IANAR broadcasts the run to every robot tab as `seq2-started`/`-progress`/`-result`, and a robot tab not running anything of its own picks it up. Robot files over 56KiB are refused, because the representable link carries one line of at most 64KiB. If the import succeeds but the run can't start, the import stays and the error says so.
  - **Watching the run (Revision H).** **Import** leaves you in the files tab, so several files can be imported in a row. A successful **import and run** switches (in LR, or in AC's host view) to the tab the run shows in: control or robot. The robot tab's page only connects once it's shown, usually after the run has started, so IANAR keeps the latest imported run's `seq2-started`, each step's latest `seq2-progress` and its `seq2-result`, and replays them to a robot tab that connects while it runs or up to 2 minutes after it finishes (`importedRun` in `ianar/lrimport.go`). `seq2-started` carries `imported: true` for these runs, and the robot tab switches to its sequence-v2 view when it sees one. It also opens that view's runner on the run's sequence (Revision I), since the runner otherwise shows the library's first sequence and hides a run of any other. A run the tab started itself that's still going keeps its selection.

### Done: slopspace visibility on the worker tab (SlopspaceSlopspaceCondocs Step 1 Substep B)

- The worker tab, both local-representative's and agent-coordinator's per-host copy, is split into two sub-tabs. **direct** is what was there: the prompt box and the work signals. **slopspace** lists the host's slopspaces, with a manual control panel below the list (Revision A).
- dungeon-keeper puts the list in its `worker-state` as `slopspaces` (`repr.go`'s `slopspaces`): each slopspace's id, creation time, deploy count, writespace repos, whether it is deployed and to which agent type, and whether it is **in use**. In use means it is deployed and an ongoing slopspace signal for that agent type is `processing`. Only one slopspace is deployed per agent type, so that work is running in it. In-use slopspaces come first, then deployed ones, then the rest newest first, up to 50. `slopspaces_omitted` counts any left out.
- The list is rebuilt on each watch-loop pass, like the signals, and sent only when it changes. Which condoc a slopspace belongs to isn't shown: that link is in condoccer's pointer specs, not in the slopspace.
- **Manual control panel** (Revision A). Pick a slopspace from the dropdown or by clicking its row. Each control is one call on `pkg/slopspace`'s Manager, the same interface condoccer and the `dungeon-keeper slopspace` CLI use:
  - **path** (Revision C): read-only, under the picker. It shows where the picked slopspace's spaces are right now: its deploy location (`deploy_path`, e.g. `/agent/worker`) while deployed, else its directory under the slopspaces dir (`path`, its `RootPath`). While deployed, a note under it gives the slopspaces-dir path too, since its metadata stays there. Both come from `worker-state` and are paths on the dungeon-keeper host.
  - **+ create**: `Create`. The new slopspace is picked automatically.
  - **deploy** (with an agent type) / **return** / **delete**: `Deploy` / `Return` / `Delete`. Delete asks for confirmation first.
  - **add repo**: clones or pulls `owner/name` into the shared writespace pool (`writespace.CloneRepo`), then `AddWritespaceRepo` on a new branch, which is required, as in the CLI.
  - **commit & push**: `WriteRepoChanges` for one repo, or `WriteAllRepoChanges` for every repo, with an optional message. The reply lists each repo's pushed SHA, or "no changes".
  - **command** (Revision B): a custom prompt for the agent. It queues a `slopspace` work signal (role `slopspace-command`, mode `write`) for the agent type the slopspace is deployed to. The watch loop runs it in the deployed slopspace and, because it didn't deploy it, leaves it deployed. The signal shows with the others on the **direct** sub-tab, with the agent's output. The command entry is only enabled on a deployed slopspace, and only when it is deployed for the agent type this host's dungeon-keeper runs. A signal for any other type would never be picked up.
- How a request travels: browser → LR `worker-slopspace` (or AC `lr-worker-slopspace` → LR `__worker:slopspace`) → dungeon-keeper `__worker:slopspace <json>` (`dungeon-keeper/slopctl.go`). dungeon-keeper runs the op off its command goroutine, answers with `slopspace-reply`, and pushes a fresh `worker-state`. LR relays the answer as `worker-slopspace-reply` to its own browsers and up to AC, which broadcasts it as `lr-worker-slopspace-reply`. LR and AC answer themselves when the request never reaches dungeon-keeper, for example when the worker isn't connected.
- Guards, in dungeon-keeper:
  - Manual ops run one at a time.
  - Nothing can be done to an **in use** slopspace.
  - Nothing can be done to a deployed slopspace while slopspace work for its agent type is still `pending` (Revision B), including a second command. Queued slopspace work runs in whatever is deployed when it is picked up, or in a new slopspace if nothing is. Returning it first would send a queued command somewhere else.
  - A deploy is refused while another slopspace is deployed for that agent type. This matters because `Deploy` clears the deploy path first, which would throw away the other slopspace's writespaces.
  - The UI only enables add repo and commit & push on a stored (returned) slopspace, because the Manager refuses both while it is deployed.
- **Caveat:** these are raw Manager calls. Condoccer doesn't know about them. Deploying, returning or deleting a condoc's slopspace by hand while condoccer is driving it will confuse its turn queue. The panel can't tell which slopspaces belong to a condoc (see above). The same applies to a command: one sent to a condoc's deployed slopspace runs in that condoc's writespaces, and condoccer might return the slopspace before the command is picked up.
- **visual** sub-tab (CondoccerV2 Step 1 Revision E, `WorkerVisual.tsx` in both frontends): one horizontal swimlane per agent type, time running left to right in proportion, zoomable (−/+/fit, or Ctrl/⌘ + scroll around the pointer). In each lane, the work submissions are blue boxes on a blue track, spanning creation to completion (or now). Below that, each slopspace is a group of red lines, one per writespace repo, from its creation until now. While a slopspace is deployed to the lane's type (Revision F), its lines merge into a red box spanning the deployment, from deploy to return. The box sits just below the blue track, so work done during the deployment but not in the slopspace runs above it; work that ran in it is within it, as the box rises around the part of the submission that ran. Only the deployed slopspace's own lines touch its box; another slopspace's lines that the box reaches down past stop short of it on both sides, leaving a visible gap (Revision I). Clicking a submission, a slopspace or a deployment fills the details panel on the right; clicking the blue track itself (Revision G) shows its agent type: whether this worker runs it, the slopspace deployed to it now, its recorded deployments, its reported submissions by status, time spent working, the agents that ran them, and its work, newest first. A lane's slopspaces are those deployed to its type (now or before) plus any its work ran in; never-deployed ones get an "undeployed" lane. The deployments come from the slopspace metadata: `Deploy` appends one to `deployments` in `SLOPSPACE.json` and `Return` closes it (the last 50 are kept; `worker-state` carries the last 10 per slopspace). A slopspace deployed before this was recorded gets its current deployment from its `SLOPSPACE_ID` marker's modification time. Slopspace work without a `slopspace_id` is taken to have run in the slopspace deployed to its agent type when it started. Work in a slopspace with no recorded deployment covering it gets a dashed box spanning just that work. To draw this, each signal in `worker-state` now carries `agent_type`, `started_at` and, for slopspace work, `slopspace_id`. dungeon-keeper records that id as a `slopspace` event on the signal just before the agent starts. It draws only what `worker-state` holds: ongoing work, the 20 most recent finished signals, and the slopspaces still on the host. A deleted slopspace shows as a dashed track spanning the work that ran in it, and work from before this change has no red box.

### Done: outside pushes to a condoc branch (SlopspaceSlopspaceCondocs Step 2 Revision G)

- **The problem.** The slopspace's copy of a branch used to be the only writer, so condoccer never fetched. Anything else pushed to the branch, such as `scripts/ufabuild.sh` from a dev checkout or a hand-made fix, made the next reply's push fail with `! [rejected] … (fetch first)`, and the turn stuck at `returned`.
- **Before pushing.** `WriteRepoChanges` now calls `gitIntegrateRemote` after committing. It fetches the branch, and if the remote has commits the slopspace doesn't, it rebases the slopspace's unpushed commits onto them (`rebase --autostash`). Only unpushed commits are rewritten, so no pushed SHA in an earlier reply changes. A branch not on the remote yet is left alone.
- **At turn start.** Before taking a turn's bases, condoccer calls the new `SyncRepo` on each target, so the agent sees outside commits and they stay out of the reply's base → head range. The condoc repo isn't synced then, because the UI's uncommitted prompt edits sit in its working tree. It's still rebased before its push.
- **Conflicts.** If an outside commit conflicts with an unpushed one, the rebase is aborted, the repo is left as it was, and the turn fails with an error saying so. To fix it by hand, with condoccer stopped or the condoc idle on that error, use `g() { git --git-dir="$SS/writespaces-secure/repos/<owner>/<repo>" --work-tree="$SS/writespaces/repos/<owner>/<repo>" "$@"; }`, with `$SS` the slopspace's directory. Run `g pull --rebase origin <branch>`, resolve the conflicts, `g add` the files, `g rebase --continue` and `g push origin HEAD`, then press **Resubmit**.

## What Exists Today

### Same-repo scheme (for contrast)

- **The handler is federation-command, not condoccer.** `federation-command/condoc.go` holds one `CondocSession` per TUI. It creates the branch, runs the agent in `repoRoot` through `buildAgentPromptCmd`, writes `## Reply` sections, and commits and pushes.
- **Condoccer only writes directives.** It appends `!HANDOFF!`, `!COMPLETED!`, `!FORCE-COMPLETED!`, `!REVERT-…!` and filled templates to files (`performAction` in `condoccer/main.go`), then watches the files for changes.
- **Progress lives in the file tree.** It is detected from file content (`detectPhase`). A committed `.condoc` lock file shows who holds the condoc.
- **Retry is single-repo.** It pushes a take branch and then force-pushes a reset of the condoc branch (`runCondocRetryGitSequence`).

### dungeon-keeper

| Capability | Where | Status for this scheme |
|---|---|---|
| Create / list / delete slopspace | `pkg/slopspace` `Create/List/Delete` | Usable as-is |
| Deploy / return slopspace | `Deploy(id, agentType)` / `Return(id)` | Usable. One deploy path per agent type (`/agent/agent-worker`), which fits one-at-a-time turns |
| Shared writespace clone pool | `pkg/writespace` `CloneRepo` (clone, or `pull --rebase` if it already exists) | Usable. HTTPS + `TF_VAR_github_pat`, which matches the transport we settled on. Note that it embeds the PAT in the clone URL, so the PAT ends up in `.git/config` |
| Add a repo to a slopspace's writespaces on a new branch | `AddWritespaceRepo` (copy, `checkout -b <ref>`, `.git` moved to `writespaces-secure`) | Usable. Doesn't check whether the branch already exists on the remote |
| Commit and push writespace repos | `WriteRepoChanges` / `WriteAllRepoChanges` | Usable. **Doesn't return the commit SHA.** `WriteAll` keeps only the last error |
| Readspace repos (read-only context) | `AddReadspaceRepo` | Not needed for the first cut |
| Work signals | `pkg/worksignal` (`ongoing/` → `complete/`, holder, events, captured output) | Usable |
| Slopspace work | `processSlopspaceWork` | **Uses whatever slopspace is currently deployed for its agent type, or creates a fresh one.** Signals carry no slopspace ID. Doesn't return the slopspace if the agent fails |
| In-place work | `processInPlaceWork` (`work_location`) | Usable as-is for the propose-PR tempdir call |
| Concurrency | Watch loop, one signal at a time | Matches the queued model |
| LR integration | `repr.go` pushes `worker-state` | Lets LR's worker tab show condoc turns for free |
| PR creation / merge | — | Not in this repo. AI-sandboxing is the reference; the implementation is new (D10) |

## Considerations

- **Something new has to drive the lifecycle.** In `same-repo`, federation-command does the real work. For `slopspace`, *something* must create slopspaces, send signals, write replies, push, and raise PRs. Condoccer is the only component that sees every condoc and owns the turn queue (D1).
- **One turn at a time, many condocs in flight.** Condoccer keeps a FIFO of pending turns across all its condocs and runs the head of the queue: deploy, signal, wait, return, commit and push. Then it moves on to the next turn, which may belong to a different condoc. Condocs waiting on a human or on PRs hold no slot (D3).
- **Deploy/return must be strictly paired.** The worker uses "whatever is deployed", so the next deploy must not start until the previous slopspace has been returned. The worker also leaves a slopspace deployed when the agent fails, so either condoccer always returns it after the signal completes, or the worker does it in a `defer` (D3).
- **Watching the condoc during a turn.** `Deploy` *moves* the writespaces to the deploy path, so the condoc file's location depends on the slopspace's deploy state. The UI resolves the path from `SLOPSPACE.json` on each read: the slopspace root when idle, `/agent/agent-worker/...` when deployed. No extra checkout is needed (D7).
- **Who writes the reply.** In `same-repo` the handler captures the agent's terminal output and writes `## Reply` itself. We keep that: the worker records output as a work-signal event, and condoccer writes the reply into the condoc repo writespace *after* return, then commits.
- **Commit links per repo.** The reply preamble becomes a list of `owner/repo@sha` links instead of the single prompt→reply pair. `WriteRepoChanges` has to report SHAs and take a message (D6).
- **One auth path.** Everything uses HTTPS + one PAT: pointer clone and push, dungeon-keeper writespaces, PR create and merge. The PAT is scoped to the pointer repo, the condoc repo and every target. Condoccer's pointer clone keeps the token out of `.git/config`, but dungeon-keeper's writespace clones don't yet (D11).
- **Branch uniqueness across repos.** This needs `git ls-remote --heads` on every target repo plus the condoc repo, done at accept time. `AddWritespaceRepo` only fails when the branch exists *locally* (D5).
- **Pointer repo contention.** Several condoccers, or several condocs in one condoccer, push to the same `main`. One pointer file per condoc keeps conflicts down to `pull --rebase` retries. For slopspace condocs, a `holder` field in the pointer replaces the `.condoc` lock (D4).
- **Retry and revert would be multi-repo.** The current retry rewrites history in one repo. Here it would have to reset every target repo plus the condoc repo, and leave a take branch in each. We won't support them (D8).
- **Merging is not atomic.** N+1 PRs can't be merged as a unit, so partial failure has to be a visible state (D10).
- **The Hide Completed filter needs updating.** Slopspace condocs pass through `completed` on the way to `pr_proposed`, `pr_open` and `merged`. Once that tab gets a filter, it should key off `merged`, not `completed` (D9).

## Decision Points

**Settled** marks a decision that has been made. **Rec** marks the option I recommend for an open decision. The numbering is new for Revision B. The Revision A numbers appear in brackets.

### Settled

- **S1. Who is the slopspace handler** [A: D1]? **The condoccer backend.** It owns the UI and the turn queue, and it sees every condoc. federation-command's one-session-per-TUI model and a new daemon were both rejected.
- **S2. Concurrency** [A: D3]? **Turns are queued, one at a time.** Consecutive turns may belong to different condocs. Real parallel turns, from several workers or per-slopspace deploy paths, are out of scope for now.
- **S3. Git transport** [A: D6]? **HTTPS + PAT everywhere**, including the pointer repo (now implemented). ssh is dropped as the default.
- **S4. Watching the condoc during a turn** [A: D9]? **Watch the expected location.** Condoccer resolves the condoc file's path from the slopspace's deploy state and reads it from there. There is no second checkout.
- **S5. PR implementation** [A: D12, partly]. **New code**, using AI-sandboxing's PR handling as the reference. Where the code goes is still open (D10).
- **S6. Pointer repo setting** [A: plan step 1]. Repo, branch and path are configured, with clone-if-missing, a periodic pull, and a read-only listing. See [Status](#status).

### Open

#### D1 — How does condoccer call dungeon-keeper? [A: D2]
- a. **Rec:** import `dungeon-keeper/pkg/{slopspace,writespace,worksignal,types}` as a Go module through a `replace` directive, the same way condoccer already pulls in `representable`. The worker stays a separate `watch` process.
- b. Shell out to the `dungeon-keeper` CLI. This decouples the two but means parsing human-readable output.
- c. Go through local-representative (`submit` over representable). Slopspace management isn't exposed there.

#### D2 — Turn queue shape [new; follows from S2]
- Scope of the queue:
  - a. **Rec:** one global FIFO per condoccer.
  - b. Per agent type. Only `agent-worker` exists today, so this adds nothing yet.
- Ordering:
  - a. **Rec:** plain FIFO, so each condoc's turns keep their order and different condocs interleave.
  - b. Round-robin across condocs, so one busy condoc can't starve the others.
- Whether the queue survives a condoccer restart:
  - a. **Rec:** yes. Store `state.phase: queued` in each pointer, and rebuild the queue from the pointers on startup.
  - b. In memory only. A restart drops queued turns back to `awaiting_action`.
- The UI shows each condoc's queue position.

#### D3 — Who deploys and returns? [A: D4, reshaped by S2]
Because turns are serialized, condoccer can safely own the deploy/return pair.
- a. **Rec (first cut):** condoccer deploys, sends the signal, waits for `complete/`, then returns the slopspace. It returns whether the agent succeeded or failed. There are no dungeon-keeper signal changes. The only dungeon-keeper fix is to stop the worker returning the slopspace itself, or make that return a no-op when the slopspace has already been returned, so the two don't race.
- b. Add `slopspace_id` to `WorkSignal`. The worker then deploys that ID and returns it in a `defer`. This is more robust if another client ever shares the worker. It is a larger dungeon-keeper change.

#### D4 — Pointer repo layout and spec [A: D5]
- a. **Rec:** one file per condoc, `pointers/<Name>-<startTime>.yaml`. The listing parser already expects this layout.
  - static spec: `name`, `startTime`, `controlScheme: slopspace`, `branch`, `condocRepo`, `condocPath`, `targets[]` (as `- repo: owner/name`), `agent`
  - runtime: `slopspaceId`, `holder` (condoccer host/instance), `state{phase, step, substep, iteration}`, `prs[]`, `updatedAt`
  - Condoc files go under `condocs/` in the condoc repo, laid out as they are today.
- b. A single `index.yaml` listing every condoc. Easier to scan, but every update conflicts.
- c. The state lives only in the condoc-yaml header inside the condoc file. Simpler, but then the state sits on the condoc *branch* rather than on the pointer's `main`.
- Sub-decisions:
  - How often to push pointer updates. Rec: on every phase change, including `queued`, not every tick.
  - Whether proposals that were never accepted are committed. Rec: yes, as `phase: proposed`, so they show up on other hosts.
  - How to handle a push conflict. Rec: `pull --rebase` and retry up to 3 times. Each condoccer only writes the pointer files it holds, so the rebase never conflicts.

#### D5 — Branch validation [A: D7]
- When:
  - a. **Rec:** live while editing (advisory) and again on accept (blocking).
  - b. Only on accept.
- How: `git ls-remote --heads https://github.com/<repo>.git <branch>`, with the PAT passed the same way as the pointer clone, on each target repo and the condoc repo.
- On conflict: block and offer to bump the default branch's timestamp.
- Can the condoc repo also be one of the targets?
  - a. **Rec:** no. Keep them distinct for now, so the agent's code changes and the condoc bookkeeping don't share a commit.
  - b. Yes, with the two collapsed into one writespace.

#### D6 — Commits, messages and links [A: D8]
- Extend `WriteRepoChanges` to return `(sha string, changed bool, err)`, and make `WriteAllRepoChanges` return per-repo results with every error kept.
- Message convention, all repos: `condoc: <Name> step N <iteration> reply`. For the condoc repo, the prompt edit is its own commit made before deploying: `… prompt`.
- Link format in the reply preamble:
  - a. **Rec:** one line per repo: `- owner/repo: [\`abc1234\`](url)`. Repos with no changes are listed as `(no changes)`.
  - b. One inline sentence.
  - c. A YAML block.

#### D7 — Resolving the "expected location" [new; follows from S4]
- Where the path comes from:
  - a. **Rec:** read `SLOPSPACE.json` on each poll tick. When it says the slopspace is deployed, read from `<deployPath>/writespaces/<condocRepo>/…`; otherwise read from `<slopspaceRoot>/writespaces/<condocRepo>/…`.
  - b. Condoccer tracks deploy state itself and skips the file read. This is cheaper, but it drifts if anything else deploys or returns the slopspace.
- During the move itself the file briefly exists in neither place. Rec: keep showing the last rendered state and don't treat "not found" as staleness while a turn is in flight.
- The agent edits the condoc in place during a turn, so the UI shows those edits live. This is a bonus over `same-repo`.

#### D8 — Scope of iteration types [A: D10]
- a. ~~**Rec:** handoff, revision, substep and resources first. Retry and revert come later, as a multi-repo version of `runCondocRetryGitSequence` with a take branch per repo.~~
- b. Everything at parity before shipping.
- **Settled (SlopspaceSlopspaceCondocs Step 1):** handoff, revision, substep and resources only. **Retry and revert are not supported** for slopspace condocs, now or later. Their controls are hidden in the UI, and `slopPerformAction` refuses them. Other same-repo features, such as the diff viewer, are carried over.

#### D9 — Phase model for slopspace condocs [A: D11]
- Add `queued` (D2) before `agent_running`. Add `pr_proposed`, `pr_open` and `merged` after `completed`. `!COMPLETED!` on a slopspace condoc moves it to `pr_proposed` rather than ending it.
- Phases go in the pointer `state.phase` (D4) and are mirrored in the condoc file, for example as `## PR Proposed` / `## PR Raised` / `## Condoc Merged` sections, so the history stays readable.
- The slopspace tab's Hide filter keys off `merged`.
- Slopspace teardown:
  - a. **Rec:** delete the slopspace on `merged`.
  - b. Delete it as soon as the PRs are raised.

#### D10 — PR management: location and behavior [A: D12, reshaped by S5]
- **Where the new code lives:**
  - a. **Rec:** a new `dungeon-keeper/pkg/pullrequest` package. It sits beside the writespace code that already holds the repos and the PAT, condoccer imports it like the other dungeon-keeper packages (D1), and the dungeon-keeper CLI can expose it for manual use.
  - b. A new top-level Go module (`pr-keeper` or similar), shared by condoccer and anything else that needs it. Cleaner ownership, but one more module.
  - c. Inside condoccer (`condoccer/pr.go`). Quickest, but it can't be reused.
- **API:**
  - a. **Rec:** the GitHub REST API with the PAT (`net/http`). There is no new binary dependency, and it uses the same token as everything else.
  - b. The `gh` CLI. Less code, but it has to be installed and authenticated on the host.
  - What to take from AI-sandboxing: check its create/merge/mergeability logic and what it handles at the edges, and port what fits. I couldn't read it from this session, so it hasn't been reviewed yet.
- **Tempdir contents** for the in-place call:
  - the condoc files
  - for each repo, `git diff <base>...<branch>` plus the log
  - the pointer spec
- **YAML schema:** `prs: [{repo, title, body, base}]`, with the condoc repo included. Optionally a shared `summary` that is reused in every body.
- **Regenerate with instruction:** a new in-place signal that includes the previous YAML and the instruction. It goes through the same turn queue (D2).
- **Raising PRs:** one per repo. Each PR body links its sibling PRs (create first, then edit the bodies).
- **Mergeability:** poll `mergeable` / `mergeable_state` and the check runs. The Merge button is enabled only when *all* PRs are mergeable.
- **Merge method:**
  - a. **Rec:** merge commit, which keeps the per-revision SHAs that the condoc links point to.
  - b. Squash.
  - c. Configurable per repo.
- **Merge order and partial failure:** merge the target repos first and the condoc repo last. If one fails, stop, show the PR states repo by repo, and allow a retry.
- **After merge:** delete the branches (rec: yes, except take branches), mark `merged`, and update the pointer.

#### D11 — PAT handling in dungeon-keeper writespaces [new; follows from S3]
- a. **Rec:** change `CloneRepo` to the same header-via-env approach as the pointer clone, so the PAT isn't written into `.git/config`. That matters because deploy moves `.git` into the agent's reach through `writespaces-secure`.
- b. Leave it as is. It works, but the token sits in every writespace clone's remote URL.

#### D12 — Agent/model selection for worker turns [A: D13]
- a. **Rec:** a default agent/model in the condoccer config, which the proposal form can override per condoc. Store it in the pointer spec (`agent`).
- b. Use the current fedcom agent, which isn't available to condoccer.

#### D13 — Where things run [A: D14]
- Condoccer and the `agent-worker` must share `/host-agent-files` (slopspaces, work signals) and `/agent`, the deploy path condoccer watches (S4). Is condoccer expected to run in the worker's container or on the host alongside it?
- Rec: on the host for now, with `SLOPSPACES_DIR`/`WORK_SIGNALS_DIR` read from the same env vars dungeon-keeper uses.

## Implementation Plan (next steps)

1. ~~**Pointer repo setting and Slopspace tab, read-only.**~~ Done in Step 1 Revision B; see [Status](#status).
Steps 2–7 are done in a first cut (Step 1 Revisions C and D; see [Status](#status)). What's left is step 8, a first end-to-end run, and the architecture review of `pr-keeper` agreed in Revision C.

2. **dungeon-keeper groundwork**
   - Make `WriteRepoChanges` return SHAs, with per-repo results and a commit message (D6).
   - Make deploy and return safe for condoccer to own: no double return, and the slopspace is returned when the agent fails (D3a).
   - Add a remote-branch existence check helper (D5).
   - Keep the PAT out of `.git/config` (D11).
   - Add tests in `pkg/slopspace` and `main_test.go`.
3. **Proposal and accept**
   - Add `gopkg.in/yaml.v3` and full pointer read/write, plus pointer push with rebase-retry (D4).
   - Proposal form for targets, condoc repo, branch and agent/model, which writes a `phase: proposed` pointer.
   - On accept: validate the branch, clone the writespaces, create the slopspace and `AddWritespaceRepo` each repo on the branch, write the condoc template into the condoc repo, push the branch everywhere, and update the pointer.
4. **Progression**
   - Turn queue (D2): mark the condoc `queued`, then prompt commit, deploy, signal, wait, and return.
   - Expected-location watching (D7).
   - After return: write the reply with per-repo links, then commit and push changed repos.
   - Update the pointer state. Reuse the same-repo step/revision/substep UI components on the Slopspace tab.
5. **PR management package** (D10): decide the location, review AI-sandboxing, and implement create, mergeability and merge against the REST API.
6. **Propose PR:** the in-place tempdir signal, the YAML editor, regenerate, and accept to raise the PRs.
7. **PR phase and Merge:** mergeability polling, the Merge control, ordered merging, partial-failure display, and teardown (D9/D10).
8. ~~**Later:** multi-repo retry and revert (D8).~~ Dropped: retry and revert won't be supported (D8).
9. **Same-repo features carried over** (SlopspaceSlopspaceCondocs): the diff viewer is done; see [Status](#status).
