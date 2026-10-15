# Prompt

[SlopspaceCondocs](../SlopspaceCondocs.md)

We will start by putting the condocs implementation we have so far under a new 'same-repo' tab and adding a 'slopspace' tab.

The slopspace tab will not yet be populated.

On our 'same-repo' tab we will also introduce a filter (enabled by default) to 'Hide Completed'.


prompt: [`3bb3382`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/3bb3382518899b5cafc585183df47efb6f3b58f2) → reply: [`ff34c50`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/ff34c5093be27defa897d430c13074fe21e877e8)

## Reply

I added a Same-Repo / Slopspace tab bar to the top of the condoccer condoc list. All the existing behaviour now sits under Same-Repo, and that tab has a "Hide Completed" checkbox that is on by default. I couldn't build or type-check it: `npm run build` and `tsc` both needed approval that wasn't given. So I've only read through the diff by hand, and nothing has been run in a browser.

- **Same-Repo tab:** the condoc list now leaves out completed condocs unless the box is unticked. Next to the checkbox it shows how many are hidden, and if every condoc is completed it says "All condocs are completed." The checkbox setting is remembered across reloads in the browser's local storage.
- **Slopspace tab:** deliberately empty for now. The sidebar and main pane just say the mode isn't supported yet. Selecting this tab sets the page URL to `#/slopspace`, so a refresh comes back to it.

The tab bar only appears on the top-level list. Once you open a condoc you're in the same-repo views, as before. The changes are in `condoccer/frontend/src/App.tsx` and `index.css`, and are left uncommitted.


## Revision A

To start the new implementation we will document the way forward and respond (in the 'md' and the response) with considerations we need to make, and how for the current slopspace and dungeon-keeper capabilities.

For the 'slopspace' control scheme:

- We have a 'condoc pointer' repo. This is a repo (default 'je-sidestuff/sloppo') where we regularly push/pull on the target branch (default 'main'). We keep it in a workspace path (default '../sloppo').
    - The pointer repo is specified top-level, as an alternative to the 'working dir'
    - Pointer repo is updated as the state of the condoc progresses (We can see what step/substep/state the condoc progression is in)
    - it specifies the 1-N repos that the condoc targets. Must be different repos.
    - it specifies which repo the condoc itself is stored in (also 'sloppo' by default)
    - When we specify a pointer repo that is not there condoccer offers to clone it for us (uses ssh by default)
- When we have a pointer repo specified correctly we are now able to propose new condocs from condoccer
    - When in the proposal state we can configure the targets/modify the pointer spec before it is accepted
    - Specifies the branch - the same branch must be used in all target repos and must not yet exist (picks the same default as 'same-repo' control scheme)
- When we accept the proposal our slopspace is created and we get write spaces for our target repos and our condoc repo
- The look-and-feel of progressing the condoc is very similar to that of the 'same-repo' control scheme
    - Under the hood we now use our slopspace calls and our dungeon-keeper agent worker
    - We deploy the slopspace for the worker to work, we undeploy afterward
        - This means one condoccer may work on N parallel condocs
    - We commit and push to each repo that had changes
    - We now link in the condoc to EACH repo/commit pair per revision
- When we move the condoc to 'complete' we now get a new 'propose PR' state
    - The propose PR uses an in-place worker call in a tempdir to prompt the agent to create a yaml template of what the PR could look like
    - We may edit this/regenerate with instruction/accept
    - When we accept it raises the PR in each target repo and the condoc repo
- When we have a PR raised this enters a 'PR' state for the condoc
    - We can see when it is mergeable and we get a 'Merge' control progression
    - Pressing merge will merge the PR

Let's begin by documenting this way forward and commenting on the next steps in implementation, as well as discretely enumerating any decision points we must address (and ideally enumerating potential decisions within).


prompt: [`a475cf8`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/a475cf841df8db8abe3a9651aeeaa3f6073bc743) → reply: [`1781828`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/178182876a5d2b820d398f39f7b0f967605c1558)

## Reply A

I wrote the design doc at `docs/SlopspaceControlScheme.md`. It sets out the slopspace flow you described and compares it with what dungeon-keeper and the condoc handler can do today. It also lists 14 decision points, each with options and a recommendation, and a 7-step build plan. No code changed, and the doc is not committed.

**Main things I found in the code:**
- **The handler would have to move.** In same-repo mode, federation-command does the real work (one session per TUI) and condoccer only writes directives. To run N condocs in parallel, the condoccer backend has to become the handler for slopspace condocs, using dungeon-keeper's Go packages directly.
- **dungeon-keeper gaps:**
  - It has one deploy path per agent type, and the worker uses "whatever slopspace is deployed", with no slopspace ID in the work signal. So "N parallel condocs" really means N in flight with agent turns run one after another, unless we change that.
  - The worker doesn't return the slopspace when the agent fails.
  - The commit-and-push call doesn't report commit SHAs, which the per-repo/commit links need.
  - Its repo clones only use HTTPS with a token, but the pointer repo should default to ssh.
  - Nothing in the repo creates or merges PRs yet.
- **Condoc files disappear during a turn.** Deploying *moves* the condoc repo's files into the agent's workspace, so the UI needs its own checkout of the condoc branch to keep showing the condoc.

**Decisions to make (my recommendation in brackets):**
1. Who runs the lifecycle [condoccer].
2. How condoccer calls dungeon-keeper [import its Go packages].
3. How turns run concurrently [queue them now; add a slopspace ID to signals].
4. Who deploys and returns slopspaces [the worker, keyed on that ID, always returning].
5. Pointer repo layout [one YAML file per condoc].
6. Git transport and credentials [ssh for the pointer repo, HTTPS token for writespaces and PRs].
7. Branch-uniqueness check [checked against every repo's remote; condoc repo kept separate from targets].
8. Commit messages and per-repo link format.
9. How the UI reads the condoc during a turn [a local checkout of the condoc branch].
10. First-cut scope [retry/revert across several repos comes later].
11. New phases after completed: `pr_proposed`, `pr_open`, `merged`.
12. PR template format, how PRs are raised and merged, and handling a merge that fails partway.
13. Agent/model choice [stored per condoc].
14. Where condoccer runs relative to the worker.

**Suggested first steps:** pointer repo settings with a read-only Slopspace tab, and the dungeon-keeper fixes above. These two can be done in either order, and the proposal/accept step needs both.


## Revision B

Let's revise based on the following:
- We do not want N executions simultaneously for now, they may queue - but we may go back and forth between different condocs each invocation
- We can go with https+PAT after all, let's prefer that over ssh
- We can look at AI-sandboxing for a PR management starting point, but it will be new impls. We can discuss where to best locate that code.
- We do not need a second checkout during a turn because we can watch the expected location

Let's do the pointer repo settings now with this understanding and then revise the decision points and restate them based on the updated understanding.
