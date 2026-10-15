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
