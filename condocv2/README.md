# condocv2

It runs **slopspace condocs only**, marked as v2 throughout:

| | condocv2 |
|---|---|
| pointer file (`pointers/*.yaml`) | `condocVersion: 2`, `controlScheme: slopspace-v2` |
| condoc main file header | ` ```condocv2-yaml ` |
| branches | `condocv2/...` |
| holder | `condocv2@<host>` |

Anything else in the pointer repo's `pointers/` (e.g. legacy v1 pointers) is
skipped, and branches outside `condocv2/` are refused.

## The sequence

The outer sequence: **propose** (a pointer file in the pointer
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

Every commit's subject is `condocv2: <Name> <kind>[ <N>]`. The exception is
a target's commit, the one holding the agent's work. It also carries a condoc
tag after the prefix: `condocv2: [condocv2-g6jc] <Name> commit <N>`. The tag
is four lowercase alphanumerics derived from the name and N. A dev-repo
watcher pulling the target uses it to trigger a `.ufabuild` build (see
`docs/DevMode.md`, "Plug-in dev repos"). The lock records
which stage the interaction reached, so an error (fixed with *Resubmit*) or a
restart resumes it where it stopped.

While a turn waits on the agent (an interaction's `signalled` stage, or the PR
draft), *Cancel turn…* stops it. It appends a `cancel_requested` event to the
work signal; dungeon-keeper kills the agent's process group and fails the
signal. If the worker hasn't completed the signal a minute after the request
(it's dead or wedged), condocv2 fails it itself. Either way the turn finishes
as for any failed run, so whatever the agent changed before it stopped is
committed as that turn's work. Every turn's signal also carries a 90-minute
`timeout_seconds`, after which the worker stops the agent the same way.

## DTT proposals

The agent may propose DTT tool calls by writing them into the slopspace's
`writespaces/dtt-requests/{read,write}` (its prompt says where). When an
interaction's slopspace comes back with proposals, dungeon-keeper won't
deploy it again until they're answered (`review.go`):

- the **response** commit is the proposal's one commit: the reply, a
  `## Proposals N` section, and the documents copied beside the step file
  (`dtt/N/`), subject `response-dtt N`
- the interaction's **unlock** also holds the condoc (phase `reviewing`): no
  prompt and no Complete until every proposal is accepted or rejected

Each proposal the human then accepts or rejects (one at a time, or all at
once, on the Text tab) is a **sub-iteration N.k** of its own, numbered in the
order they're answered:

1. **lock N.k** — the pointer locks the condoc for it
2. **proposal** — `## DTT N.k` in the step file: `dtt-read N.k`,
   `dtt-assert N.k` (`dtt-delete` when the document's comments say
   `Mode: delete`), or `dtt-reject N.k`
3. an accepted one is run with dtt (`RunCanvasDoc`) as soon as that commit is
   pushed
4. **image** — `## Result N.k` and the image beside the document:
   `dtt-image N.k`, or `dtt-failed N.k` with why
5. **unlock N.k** — the last one's also releases the hold

A write proposal (assert or delete) can also be **read** first — the 👁 Read
button between Reject and Accept: a sub-iteration of its own like the rest,
`dtt-preread N.k`, run with `dtt read` on the document still in
`dtt-requests` (dungeon-keeper's `PreReadProposal`), whose image is
`write/<doc>.read.image.json` beside where the assert's would go. The
proposal then still awaits accept or reject.

**Request changes** instead covers every proposal still unanswered: the
adjustment request is the next interaction, auto-submitted, and its lock
releases the hold. Decisions made on local-representative's slopspace tab
are picked up by the next tick and run the same way. Condocs held before the
sub-iterations show their `hold` / `review-<decision>` / `release` commits as
they were.

## Read-only repos

A proposal can also list **read-only** repos, `owner/name` or
`owner/name@ref` (a branch, tag or commit; the default branch otherwise),
which the agent reads but never changes (`readonly.go`). They get no branch,
no commits and no PRs, and can't also be a target or the condoc repo.

- **begin** clones each into dungeon-keeper's shared readspaces and adds it
  to the slopspace (`AddReadspaceRepo`), recording the commit it starts at
  in the pointer's begin commit
- every **deploy** and **return** of a turn pulls each one afresh:
  dungeon-keeper fetches it, resolves the ref and exports that commit to
  `readspaces/repos/<owner>/<name>` without `.git` and read-only, throwing
  away the agent's copy (`pkg/slopspace/readrepos.go`); `SLOPSPACE.json`
  keeps every commit each repo resolved to
- a new commit is recorded on the pointer (`readResolutions`) in a commit of
  its own, `condocv2: <Name> readspace <N>`, so which commit each turn saw
  is traceable
- the agent's prompt lists them, at their commits, and says not to change
  them
- the **Visual** tab gives each its own column after the targets: a blue
  dotted circle per new commit — on begin's row, an interaction's response
  row (its deploy) or record row (its return) — linking the commit

### Configure condoc

**Configure condoc** (under every held condoc's details, read-only repos
or not) changes a condoc between interactions (`configure.go`). For now it
has one subsection, **Repos**, which does two things:

- rename the branch (or tag, or commit) a read-only repo follows — empty
  follows its default branch
- **Add** a read-only repo (`owner/name[@ref]`) mid-condoc, by the proposal
  form's rules: not a target, not the condoc repo, not already read-only

Like a DTT tool call each runs within a momentary lock in the pointer repo,
so the read-only repos and their refs stay in the version-controlled record:

1. **configure-lock N.c** — the pointer commits the change asked for
   (`configure:` — kind (`add`, or none for a ref change), repo, from, to); N
   is the interactions completed, c counts the configurations since
2. the slopspace's copy follows the new ref: its shared clone is brought up
   to date (or, for an add, made) and dungeon-keeper exports it at the commit
   the ref resolves to (`AddReadspaceRepo`, which replaces a repo the
   slopspace has and appends one it hasn't)
3. **configure-unlock N.c** — the pointer's `readOnly` takes the new ref or
   the added repo, the configuration is kept in `configurations` (with the
   commit it resolved to, or why it failed — a failed one leaves the repos as
   they were), and a new commit joins `readResolutions` (`at: configure`,
   with the ref it followed)

An added repo is then like any other: pulled afresh on every deploy and
return, listed in the agent's prompt from the next interaction, and given
its own column on the Visual tab. Only the pointer records repos — the
condoc's files name just its pointer repo — so a condocv2 that later takes a
condoc up from its pointer alone (a remote condoc) sees the same read-only
repos, and from `configurations`/`readResolutions` when each came in.

It is refused while an interaction holds the lock, a DTT sub-iteration is
running, the slopspace is deployed, the condoc has an error, or it is
completing or final. The claim is held throughout; a lock a restart leaves
behind is finished by the tick. On the Visual tab the new commit is a blue
dotted circle on interaction N's record row.

## Refresh remotes

Someone may push to a condoc's branch from outside condocv2: another client,
or a fix made by hand. **Refresh remotes**, beside *Configure condoc*, pulls
those commits into the slopspace (`external.go`). It brings each target and
the condoc repo up to date with its remote branch using dungeon-keeper's
`SyncRepo`. The same refresh runs by itself just before a prompt takes the
lock, and before a request for changes does, so the agent always starts from
what the remote has.

- **Nothing new:** nothing is recorded, and nothing shows.
- **Something new:** one pointer commit, `condocv2: <Name> external N.e`,
  records it in the pointer's `externals`. Each moved repo gets an entry with
  its branch's head before and after, how many commits came in, and whether a
  refresh or a prompt found them. N is the number of interactions completed,
  and e counts the external changes found since.
- **On the Visual tab:** each external change is a section of its own between
  interaction N and N+1, with "external" in place of a number. Each moved lane
  gets a blue dotted circle, joined by blue dotted arrows to the pointer's
  `external` commit.
- **In the step file:** the next interaction's `## Record` lists each change,
  `from → to` and its commit count.
- **In the timeline record:** the change is part of the timeline, so it is
  kept in `Record.html` at completion.

Because the commits are pulled in before the next interaction's bases are
taken, that interaction's record maps only the agent's work.

A refresh needs the condoc between interactions. It is refused while an
interaction holds the lock, a DTT sub-iteration or a configuration is
running, or the condoc has an error.

A push made while an interaction holds the lock isn't handled this way yet.
It is still pulled in when the turn is queued, or rebased under the turn's
commits, without being recorded.

If the remote's commits conflict with the slopspace's, the refresh stops with
dungeon-keeper's conflict error. Repos already pulled in are still recorded.
A prompt whose refresh fails doesn't take the lock.

## Resources

**Add resource**, beside *Refresh remotes*, adds files to the condoc for the
agent to use, as condoccer v1's step prompts carried "Resource N" sections
(`resources.go`). A resource has an optional title and its files from one
source:

- **Upload:** files from the browser's machine.
- **Host files:** entries of this host's files tab: its host-cache and
  host-store (`--host-cache-dir` and `--host-store-dir`, by default
  local-representative's `/host-agent-files/exchange/host-cache` and
  `host-store`).
- **Highlighted:** the files highlighted in local-representative's files tab
  (its "highlight" button, the yellow ring), and in the files tabs of the other
  hosts connected to agent-coordinator, as session-manager's Add Resource takes
  them (`highlighted.go`). condocv2 lists them over the connected LR's HTTP
  dashboard (`GET /api/files`, and `GET /api/file-peers` for the other hosts)
  and fetches each with `GET /api/files/<id>?download=1`, so it needs the LR
  connection. All of them are taken unless some are unticked; they are listed
  again when the resource is added, and one no longer highlighted is refused.
- **Text:** text entered in the form, kept as one file (`resource.md` unless
  named).
- **Voice:** text dictated with The Conversationalist, through the same
  capture panel as the prompt's mic button, kept as one file
  (`dictation.md` unless named). Each dictation is added to the end, and the
  text can be corrected before the resource is added. The tab says so when The
  Conversationalist isn't available. The title and the Text tab's text also
  get a mic button while it is.

Resources are numbered 1, 2, ... across the whole condoc. Resource R's files
go into the condoc repo beside the step files, under `resources/R/`, and the
scope it is added in gets a `## Resource R` section with its title and a link
to each file. That is one condoc commit, then one pointer commit, both
`condocv2: <Name> resource N.R`, where N is the number of interactions
completed. The pointer keeps it in `resources`. A failure after the condoc
commit is retried by adding it again.

From the next interaction on, the agent reads the section in its step file;
its prompt says what a `## Resource` section is.

- **On the Visual tab:** each resource is a section of its own after
  interaction N, labelled "resource R": a dot in the pointer lane, and a green
  circle with a file in the condoc lane. The human column shows its title, or
  its files.
- **On the Text tab:** a resource box after interaction N, with its files
  linked. condocv2 serves them at `api/resource/<stem>/<R>/<file>`.
- **In the timeline record:** it is drawn the same way.

A resource needs the condoc between interactions, as a step does. It is
refused while an interaction holds the lock or its DTT proposals await
answers, while a configuration is running, or while the condoc has an error.
A resource takes at most 50 files and 50 MB.

## Asides

**Aside…**, under the prompt box, asks the agent a question about the condoc
off the record (`aside.go`). The agent sees everything an interaction's
agent would: its step files, its target repos, its read-only repos, and the
DTT canvas with the images the tool calls returned. But it runs in
ambiguous-agent's **read** mode (`-r`, where an interaction's runs `-w`), and
nothing about the aside goes into the condoc. There is no prompt, response or
record section, no commit, and nothing on the pointer.

- **Where it's kept:** only in this host's local history, one file per aside:
  `<asides-dir>/<pointer stem>/aside-<A>.json`. `--asides-dir` defaults to
  `~/.ufa/condocv2/asides/`, and a blank one turns asides off. The pointer
  stem (`<Name>-<startTime>`) correlates the aside back to its condoc. A is 1,
  2, ... per condoc. The file also holds the number of interactions completed
  and the scope when the aside ran, its times, the agent and model, and the
  answer.
- **When it runs:** an aside can be asked whenever the condoc's interactions
  are going on (awaiting a prompt or locked), but not while the condoc is
  held for DTT proposals: dungeon-keeper won't deploy the slopspace until
  they are decided. It waits in the turn queue like a turn, so one asked
  during an interaction runs after it (and fails if that interaction leaves
  the condoc held for proposals). While it runs it holds the condoc's claim,
  so a prompt or any other action waits until it is done.
- **How it runs:** like an interaction's turn. The slopspace is deployed
  (waiting, as a turn does, while another condoc's is), which pulls the
  read-only repos afresh as every deploy does. The agent gets a slopspace
  work signal (role `condocv2-aside-<stem>`, mode `read`), and the slopspace
  is returned once it is done. Nothing is committed. If an interaction that
  failed midway left the slopspace deployed, the aside uses it as it is and
  leaves it deployed. **Cancel aside** stops one that is queued or running.
- **Read-only:** the agent's mode is the guard. After the run, each repo's
  `git status` is compared with what it was before. A repo the agent changed
  anyway is reported on the aside and left as it is, so the next interaction
  would commit the change unless you undo it. DTT proposals it left anyway
  are reported as `dtt-requests`; they would hold the condoc for review.
- **Later agent runs see the asides:** each interaction's turn, and each
  aside's, gets the answered asides before it as Markdown (`Aside-<A>.md`)
  in the deployed slopspace's `readspaces/condoc-asides/`, and its prompt
  says where. The readspaces are thrown away on return, so the asides never
  reach a repo.
- **On the Text tab:** each aside is a red box in the scope it ran in, after
  the interaction before it. It shows the question, the answer, and the
  aside's status.
- **On the Visual tab:** each aside is a red bubble saying "aside" in the
  local operations column, hung off the last node of its own scope before
  it ran. Its tooltip has the number, the agent, and the question's first
  line. In the embedding page, clicking it opens the work submission.
- **In the timeline record:** asides aren't there. Like the rest of the
  local operations, they are read fresh from this host and never kept in the
  condoc's model.

## Steps

A condoc can indent its scope into a **step**, and a step into a substep, as
condoccer v1's steps and substeps did (`steps.go`). A step is another scope of
the same interactions. Its step file is its own, which keeps any one file
(and the agent's context) from holding the whole condoc.

- **Paths:** the condoc starts in the main scope. The first step entered from
  it is `step-1`, the next `step-2`. The first entered from `step-1` is
  `step-1-1`, then `step-1-1-1`, and so on.
- **Files:** the main scope's interactions are in `Step1.md`. A step's are in
  `Step-<path>.md` beside it (`Step-1.md`, `Step-1-1.md`, ...), which links
  back to the scope it was entered from. That scope gets a `## Step <path>`
  section linking the step, and a `## Step <path> Completed` when it ends.
- **Interaction numbers run on:** entering a step doesn't reset the counter.
  Interaction N is N whichever scope it was made in.
- **The agent** is pointed at the current scope's step file. In a step, it is
  also told where the files of the scopes around it are.
- **Commits:** entering is `condocv2: <Name> step <path>`, in the condoc repo
  and then on the pointer. The pointer keeps every step's span in `steps:`
  (the interactions completed when it was entered and left) and the current
  scope in `state.step`. Completing is `condocv2: <Name> step-complete <path>`,
  which brings the condoc back up to the scope the step was entered from.
  Both are made between interactions only. They are refused while an
  interaction holds the lock, DTT proposals are held, a configuration is
  running, or the condoc has an error.

The scope bar under *Configure condoc* shows the scope being viewed, as
`main scope › step-1 › step-1-1`, with **↑ Up** to go back to the scope it was
entered from. In the condoc's current scope it has **→ New step** (or substep,
with an optional title) and, in a step, **← Complete step-…**.

Viewing a step shows only what happened in it, on both tabs:

- **Visual:** entering a step is a section of its own in the scope it was
  entered from. It is drawn as a dot in the pointer lane beside a green circle
  with a right-arrow in the condoc lane. Click the arrow to descend into the
  step. Completing it is a green left-arrow in the condoc lane. The step's own
  view starts with its entry and ends with its completion; click the
  left-arrow there to come back up.
- **Text:** shows the scope's interactions with the step entries and
  completions in place.

The prompt box, an interaction in flight and any held DTT proposals appear
only in the current scope. Elsewhere, *Go to step-…* takes you there.

The scope being viewed is kept in the URL hash
(`#/condoc/<stem>/<tab>/step/<path|main>`), as condoccer v1 kept its step. A
reload or a condocv2 restart brings you back to the step you were viewing.
The pages embedding condocv2 keep that hash too. A condoc opened without one
shows the scope it is in now. When the condoc enters or completes a step while
its current scope is shown, the view follows it.

The timeline record (`Record.html`) draws every scope in one picture: each
step's entry and completion are sections between the interactions.

## Completion and merge

Between interactions, **Complete** ends the condoc and re-integrates its
branches (`complete.go`):

1. **complete** — `## Completed` is added to the main file and the timeline
   record is saved beside the step file (condoc commit), and the pointer
   moves to `pr_drafting` (pointer commit)
2. **drafted** — an in-place agent turn writes `prs.yaml`: one PR per repo
   with changes (targets with nothing to merge are skipped), following the
   *PR guidance*, which defaults to *extremely concise*
3. **prs** — after editing (or *Redraft* with new guidance), *Raise PRs*
   opens them, targets first and the condoc repo last, and lists them under
   `## Pull Requests` in the main file
4. **merged** — *Merge*, once GitHub reports every PR mergeable, merges them
   in that order with merge commits, waits until GitHub reports each one
   merged (up to two minutes), and then makes one final pointer commit:
   phase `merged` plus a `finalization` block with the final state, when, the
   timeline record's link, and the completion nodes nothing else keeps (the
   condoc lane's complete and prs commits, and each repo's merge commit with
   its PR). Only then is the slopspace deleted; the `condocv2/...` branches
   are kept

A failure leaves the error on the pointer; *Resubmit* (drafting) or the same
button again (raising, merging) resumes. The Visual tab draws these commits
with their own icons (◆ complete, a dashed PR for a draft, PR, ⑂ merged —
the pointer's, then each repo's merge commit, joined in purple). The
timeline record is saved at **complete**, so it stops there; after the
merge the completion is added to it from the pointer's history and its
`finalization` (`withFinalization`), so the PR phases stay drawn.

## Local repos

A target or condoc repo can be `local/<name>`: the git repo at
`$UFA_LOCAL_ROOT/<name>` (`~/workspace/ufa-local/<name>` by default), with no
remote (`localrepo.go`, using dungeon-keeper's `gitauth`). dungeon-keeper
clones the slopspace's copy from that path, and pushes the condoc branch
straight back into it. condocv2 adds the rest:

- The branch check at proposal and accept looks in the local repo.
- Links read `ufa-local:<name>/commit/<sha>` and
  `ufa-local:<name>/blob/<ref>/<path>`. The UI shows them as text, with the
  link as a tooltip, since there's no page to open.
- **Raise PRs** records a *local merge* for each local repo instead of a PR.
  It needs no GitHub token when every repo is local. The PRs list shows it as
  `local merge ready` while the condoc branch is in the repo.
- **Merge** merges the condoc branch into the repo's base itself (`git merge
  --no-ff`), as GitHub's merge would. If the base is checked out, the merge
  runs in the repo's own work tree, which must have no uncommitted changes.
  Otherwise it runs in a temporary work tree that is removed afterwards. The
  merge commit goes into the finalization, as a PR's does. Merging again
  finds it merged already.
- Once merged, the timeline record and the DTT viewer's files are read from
  the local repo.

GitHub and local repos may be mixed in one condoc, and each PR goes its own
way. The pointer repo is still on GitHub. dtt's `git` tool makes and
removes local repos (`git.repos`). The `condoc-local` demo uses it to run a
condoc's whole lifecycle on two of them.

## Final states

Two final states end a condoc for good (`final.go`). Both are **markers
only**: one pointer commit moves the phase and records the phase it left
(`finalFrom`) and when (`finalAt`); no slopspace, branch, PR or condoc file is
touched, and condocv2 never drives the condoc again.

- **archived** — *Archive*, after **merged**: the end of the normal lifecycle.
- **abandoned** — *Abandon…*, from any phase before **merged** (a merged
  condoc is archived instead), after a warning. A queued turn is dropped; a
  running one has to finish first. Whatever the condoc had (slopspace,
  branches, open PRs) is left for you to clean up. *Unabandon* undoes it: one
  pointer commit puts the phase back to `finalFrom` and clears the marker, and
  condocv2 drives the condoc again from there (re-queuing a dropped turn).

The sidebar hides final condocs unless **show final** is ticked. A new
proposal opens by itself once it's listed, unless you've opened something
else meanwhile.

## The UI

A condoc opens with two tabs:

- **Text** — the Prompt / Response / Record flow, and the prompt box.
- **Visual** — the swimlanes, flowing from the top down in numbered sections
  (solid dividers for start, each interaction N and completion; dashed for
  each sub-iteration N.k): the human's column (prompts, and the answer to
  each proposal — amber while the proposals block the condoc), one lane per
  repo (pointer, condoc, each target) with a node per commit above, read
  back from git by those subjects (`timeline.go`,
  `frontend/src/Swimlanes.tsx`): a wrench for a response that proposed DTT
  tool calls, a `?` on its record, then per sub-iteration a dashed eye
  (read), wrench (assert) or ✗ (rejected), and ✓ for its image. On the right
  are the **local operations**: each agent invocation (its work signal) and
  DTT execution (an image in the slopspace's `dtt-images`), with level red
  arrows out and back just below the node it followed — an agent's prompt, a
  DTT run's proposal. Those are read fresh from this host's records and
  matched by time (`ops.go`); they are never stored in the condoc's model or
  its timeline record. Clicking an agent's box opens the embedding page's
  (local-representative's, or agent-coordinator's host view's) worker ›
  visual with that work submission selected, found by its slopspace, role
  and signal file in the worker's own history.

Every DTT file name — a proposal on the Text tab's review panel, the links in
a `## Proposals`, `## DTT` or `## Result` section — and on the Visual tab a
sub-iteration's proposal or ✓ node, an answer, the wait, or a DTT run's box,
opens the **DTT viewer**: the interaction's tool calls and their images as a
menu, and the picked one's content. condocv2 serves the condoc's copies under
`dtt/N/` at `api/dtt/<stem>/<N>/<file>` (`review.go`'s `handleDTTFile`; a
merged condoc's redirect to GitHub, or for a local condoc repo, read from it).

The rows that move repos — an interaction's response and commits row (the
agent's work: its response commit on the condoc repo and its commit on each
target) and each external change — have a **+/-** in the Visual tab's left
margin; on the Text tab, a record's **+/- changes** and each external change it
lists do the same. It opens the **diff viewer**, condoccer's multi-repo diff
view: each repo's range and the files it changed down the side, the picked
file's diff beside it with its hunks to jump to. condocv2 diffs it in the
slopspace's git dirs (`diffview.go`) at `api/diff/<stem>/<row>` (`<row>` is
`N` or `external-N.e`) and `api/diff/<stem>/<row>/<owner>/<name>/<path>`; once
merged there is no slopspace to diff, and the PRs have the changes.

The open condoc and tab, and the DTT viewer's interaction and entry (or the
diff viewer's row and file) when it is open, are in the URL hash, which local-representative and agent-coordinator
keep for the condocv2 frame, and the scroll position is kept per condoc and tab
— so coming back from the worker tab (the nav arrow), or condocv2 reloading
after a rebuild-and-restart, lands where you were. The hosts read the kept hash
again whenever the frame comes back after condocv2 was down, not only when the
tab is opened.

The condoc's files and its condoc and target lanes are read from the
slopspace, which Merge deletes. So **complete** also saves the view's model as
`<condoc>Impls/Record.html` (`record.go`): a standalone page drawing the same
swimlanes and the text flow, with the model (timeline and interactions)
embedded as JSON. Once merged, both tabs are drawn from that record, fetched
from GitHub (the kept branch, else the default branch). A record saved before
it carried the interactions gets its text from the step file beside it.
Condocs completed before the record existed show only the pointer lane after
merge, and no text.

## Agent override

Each condoc is proposed with an agent and model (by default `--slop-agent`
and `--slop-model`). **Override agent**, in the sidebar above the
local-representative widget, sets an agent/model pair on this condocv2 instead
(`override.go`). While it is set, every agent run condocv2 hands the worker —
each interaction's turn and the PR drafting — runs with that pair, whatever
the condoc says. A blank model means the agent's own default. The agent is an
ambiguous-agent name (`claude`, `pi`, `opencode`, …). **Clear** goes back to
each condoc's own pair. A change applies from the next run handed to the
worker; a run already queued with the worker keeps the pair it was given.

The override belongs to the application, not to any condoc, and the condoc
never sees it:

- It is kept in `agent-override.json` in this host's config dir
  (`~/.config/condocv2/` on Linux; `--agent-override-file` moves it, and a
  blank one keeps it in memory only). It is not in the pointer or the condoc
  files, and the pointer keeps the condoc's own agent/model.
- ambiguous-agent's `invoking <agent> (<model>) in <mode> mode...` line is
  dropped from every reply before it becomes a Response, override or not, so
  a step file reads the same whichever agent ran it.

It shows only in this condocv2's UI (the sidebar, and the condoc's details
with its own pair struck through) and in this host's work signals, which the
Visual tab's operations column reads.

## Demos

[`docs/demos/`](docs/demos/README.md) holds DTT documents that demonstrate
condocv2 end to end. `condoc-lifecycle` makes two GitHub repositories, has
another node's robot take a condoc through its whole lifecycle in a browser
(one agent interaction, a story about cats), and composes the recordings
and a Polly narration into a video. local-representative's `condoc-local`
demo does the same on two local repos, which dtt's `git` tool makes and
removes. It merges locally, and nothing is on GitHub but the pointer repo.

## Building

`make build` (frontend, then the Go binary embedding it), `make test`.
